package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
)

// undoableOps maps op types to the extra key that holds the rollback target SHA.
// commit -> parent (the commit before this one)
// amend  -> oldSha (the commit that was replaced)
// reword -> oldSha (the commit that was replaced)
var undoableOps = map[string]string{
	"commit": "parent",
	"amend":  "oldSha",
	"reword": "oldSha",
}

func runUndo(flags globalFlags, bypassSession bool, count int) {
	const cmd = "undo"

	if count <= 0 {
		die(flags, cmd, 1, fmt.Sprintf("--count must be positive, got %d", count))
	}

	gitDir := mustGitDir(flags, cmd)
	if err := repo.EnsureInitialized(gitDir); err != nil {
		die(flags, cmd, 4, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("loading config: %v", err))
	}

	ctx := context.Background()

	// Resolve current branch
	ref, err := git.HeadRef(ctx)
	if err != nil || ref == "" {
		die(flags, cmd, 1, "HEAD is detached; undo requires a branch")
	}

	// Read all oplog entries
	allEntries, err := oplog.Read(sgDir)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("reading oplog: %v", err))
	}

	// Filter to entries for this ref (and session, unless bypass-session)
	sessionID := os.Getenv("CLAUDE_CODE_SESSION_ID")
	if !bypassSession && sessionID == "" {
		die(flags, cmd, 1, "no session ID found (CLAUDE_CODE_SESSION_ID not set); pass --bypass-session to undo across all sessions")
	}

	var entries []oplog.Entry
	for _, e := range allEntries {
		if e.Extra == nil {
			continue
		}
		entryRef, ok := e.Extra["ref"].(string)
		if !ok || entryRef != ref {
			continue
		}
		if !bypassSession && e.SessionID != sessionID {
			continue
		}
		entries = append(entries, e)
	}

	// Walk backwards through entries to find the Nth undoable entry.
	// "undo" entries in the oplog cancel one preceding undoable entry each.
	cancelled := 0
	liveSteps := 0
	var targetEntry *oplog.Entry

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]

		if e.Op == "undo" {
			// Each undo entry means one preceding undoable was already undone
			cancelled++
			continue
		}

		// Check if this op is undoable
		if _, isUndoable := undoableOps[e.Op]; !isUndoable {
			// Non-undoable ops (redo, rewrite-author, scrub-*, etc.) are simply skipped
			continue
		}

		// This is an undoable op
		if cancelled > 0 {
			// Already undone by a later undo entry, skip it
			cancelled--
			continue
		}

		// This is a live undoable step
		liveSteps++
		if liveSteps == count {
			targetEntry = &entries[i]
			break
		}
	}

	if targetEntry == nil {
		if liveSteps == 0 {
			die(flags, cmd, 1, fmt.Sprintf("no undoable operations found for %s in the oplog", refShortName(ref)))
		}
		die(flags, cmd, 1, fmt.Sprintf("only %d undoable operations available, requested %d", liveSteps, count))
	}

	// Determine the target key and SHA for the rollback
	targetKey := undoableOps[targetEntry.Op]
	targetSHARaw, fieldPresent := targetEntry.Extra[targetKey]
	if !fieldPresent {
		die(flags, cmd, 1, fmt.Sprintf("oplog entry for %q is missing %q field", targetEntry.Op, targetKey))
	}

	targetSHA, _ := targetSHARaw.(string)

	// Handle empty target SHA based on target key
	isRootUndo := false
	if targetSHA == "" {
		if targetKey == "parent" {
			// Root commit undo: the commit had no parent
			isRootUndo = true
		} else {
			// amend/reword can't have empty oldSha
			die(flags, cmd, 1, fmt.Sprintf("oplog entry for %q has empty %q field", targetEntry.Op, targetKey))
		}
	}

	// Get the actual current SHA from git (not from oplog, which may be stale
	// when undoing N > 1 steps)
	currentSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("resolving HEAD: %v", err))
	}

	if flags.dryRun {
		fmt.Printf("would undo %d operation(s) on %s\n", count, refShortName(ref))
		if isRootUndo {
			fmt.Printf("  %s -> (empty, delete ref)\n", currentSHA[:8])
		} else {
			fmt.Printf("  %s -> %s\n", currentSHA[:8], targetSHA[:8])
		}
		return
	}

	// Acquire lock on the ref
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, ref, "undo", timeout)
	if err != nil {
		die(flags, cmd, 1, fmt.Sprintf("acquiring lock: %v", err))
	}
	defer lk.Release()

	// Perform the ref update
	if isRootUndo {
		if err := git.DeleteRef(ctx, ref, currentSHA); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("delete-ref failed (ref may have moved): %v", err))
		}
	} else {
		if err := git.UpdateRef(ctx, ref, targetSHA, currentSHA); err != nil {
			die(flags, cmd, 1, fmt.Sprintf("update-ref failed (ref may have moved): %v", err))
		}
	}

	// Sync main index so git status/diff reflect the change
	// For root undo, pass "" to trigger read-tree --empty
	syncTreeish := targetSHA
	if isRootUndo {
		syncTreeish = ""
	}
	if err := git.SyncMainIndex(ctx, syncTreeish); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", err)
	}

	// If the commit being undone triggered a parent bump, inform the user.
	if bumpSHA := findAssociatedParentBump(sgDir, targetEntry); bumpSHA != "" {
		fmt.Fprintf(os.Stderr, "note: undoing commit that triggered parent bump %s\n", bumpSHA[:8])
	}

	if err := maybeAutoBumpParent(ctx, flags, gitDir, targetSHA, "undo", ""); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		os.Exit(1)
	}

	// Log the undo to the oplog
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "undo",
		Extra: map[string]interface{}{
			"ref":      ref,
			"undoneOp": targetEntry.Op,
			"sha":      targetSHA,
			"oldSha":   currentSHA,
			"count":    count,
		},
	})

	if !flags.quiet {
		if count == 1 {
			fmt.Printf("undid %s on %s\n", targetEntry.Op, refShortName(ref))
		} else {
			fmt.Printf("undid %d operations on %s (last: %s)\n", count, refShortName(ref), targetEntry.Op)
		}
		if isRootUndo {
			fmt.Printf("  %s -> (empty)\n", currentSHA[:8])
		} else {
			fmt.Printf("  %s -> %s\n", currentSHA[:8], targetSHA[:8])
		}
	}
}

// findAssociatedParentBump scans the oplog for an "auto-bump-parent" entry
// that immediately follows the given entry (by timestamp). Returns the parent
// bump SHA if found, empty string otherwise.
func findAssociatedParentBump(sgDir string, target *oplog.Entry) string {
	entries, err := oplog.Read(sgDir)
	if err != nil || len(entries) == 0 {
		return ""
	}

	// Find the target entry's position by matching timestamp and op.
	targetIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Op == target.Op && e.Timestamp.Equal(target.Timestamp) && e.PID == target.PID {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 || targetIdx+1 >= len(entries) {
		return ""
	}

	// Look at entries following the target for an auto-bump-parent.
	next := entries[targetIdx+1]
	if next.Op == "auto-bump-parent" && next.Extra != nil {
		if sha, ok := next.Extra["parentBumpSHA"].(string); ok && sha != "" {
			return sha
		}
	}
	return ""
}
