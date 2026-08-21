package main

import (
	"fmt"
	"os"
	"strings"
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

// sessionIDEnvVar is the Claude Code session handshake variable. It is declared
// on the app (WithHandshakeEnv) and read through ctx.InfraValue, so callers pass
// the resolved value in rather than reaching for the environment here.
const sessionIDEnvVar = "CLAUDE_CODE_SESSION_ID"

func runUndo(flags globalFlags, bypassSession bool, count int, sessionID string) {
	const cmd = "undo"

	if count <= 0 {
		die(1, fmt.Sprintf("--count must be positive, got %d", count))
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(4, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(1, fmt.Sprintf("loading config: %v", err))
	}

	ctx := flags.ctx()

	// Resolve current branch
	ref, err := git.HeadRef(ctx)
	if err != nil || ref == "" {
		die(1, "HEAD is detached; undo requires a branch")
	}

	// Read all oplog entries. Undo walks the log backwards and reverses what
	// it finds, so a log with unreadable lines cannot be undone from: the
	// entry that would be reversed next may be one of the lines that did not
	// parse. Refuse instead of undoing the wrong operation.
	allEntries, skipped, err := oplog.Read(sgDir)
	if err != nil {
		die(1, fmt.Sprintf("reading oplog: %v", err))
	}
	if skipped > 0 {
		die(1, fmt.Sprintf("operation log has %d unparseable line(s); undo needs a complete log and refuses to guess (inspect %s)", skipped, oplog.Path(sgDir)))
	}

	// Filter to entries for this ref (and session, unless bypass-session)
	if !bypassSession && sessionID == "" {
		die(1, "no session ID found ("+sessionIDEnvVar+" not set); pass --bypass-session to undo across all sessions")
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
	// We also track the most recent live entry's TipSHA for the CAS old value.
	cancelled := 0
	liveSteps := 0
	var targetEntry *oplog.Entry
	var mostRecentTipSHA string // TipSHA of the most recent live undoable entry

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]

		if e.Op == "undo" {
			// Each undo entry means one preceding undoable was already undone
			cancelled++
			continue
		}

		// Check if this op is undoable
		if _, isUndoable := undoableOps[e.Op]; !isUndoable {
			// Scrub and rewrite-author operations invalidate all prior SHAs in
			// the oplog. We cannot safely undo anything before them.
			if strings.HasPrefix(e.Op, "scrub-") || e.Op == "rewrite-author" {
				die(1, fmt.Sprintf("cannot undo %s — history rewrite invalidated prior oplog entries", e.Op))
			}
			// Other non-undoable ops are simply skipped.
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
		if liveSteps == 1 {
			// Record the TipSHA of the most recent live entry for CAS.
			mostRecentTipSHA = oplog.TipSHA(e.Extra)
		}
		if liveSteps == count {
			targetEntry = &entries[i]
			break
		}
	}

	if targetEntry == nil {
		if liveSteps == 0 {
			die(1, fmt.Sprintf("no undoable operations found for %s in the oplog", refShortName(ref)))
		}
		die(1, fmt.Sprintf("only %d undoable operations available, requested %d", liveSteps, count))
	}

	// Determine the target key and SHA for the rollback
	targetKey := undoableOps[targetEntry.Op]
	targetSHARaw, fieldPresent := targetEntry.Extra[targetKey]
	if !fieldPresent {
		die(1, fmt.Sprintf("oplog entry for %q is missing %q field", targetEntry.Op, targetKey))
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
			die(1, fmt.Sprintf("oplog entry for %q has empty %q field", targetEntry.Op, targetKey))
		}
	}

	// Use the oplog's recorded TipSHA as the CAS old value. This detects
	// concurrent branch moves: if another session committed on top, the ref
	// won't match the oplog's expectation and the CAS fails.
	currentSHA := mostRecentTipSHA
	if currentSHA == "" {
		// Fallback: resolve HEAD directly (shouldn't happen for well-formed oplog)
		currentSHA, err = git.RevParse(ctx, "HEAD")
		if err != nil {
			die(1, fmt.Sprintf("resolving HEAD: %v", err))
		}
	}

	if flags.dryRun {
		outf(flags, "would undo %d operation(s) on %s\n", count, refShortName(ref))
		if isRootUndo {
			outf(flags, "  %s -> (empty, delete ref)\n", currentSHA[:8])
		} else {
			outf(flags, "  %s -> %s\n", currentSHA[:8], targetSHA[:8])
		}
		return
	}

	// Acquire lock on the ref
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, ref, "undo", timeout)
	if err != nil {
		die(1, fmt.Sprintf("acquiring lock: %v", err))
	}
	defer lk.Release()

	// Perform the ref update
	if isRootUndo {
		if err := git.DeleteRef(ctx, ref, currentSHA); err != nil {
			die(1, fmt.Sprintf("delete-ref failed (ref may have moved): %v", err))
		}
	} else {
		if err := git.UpdateRef(ctx, ref, targetSHA, currentSHA); err != nil {
			die(1, fmt.Sprintf("update-ref failed (ref may have moved): %v", err))
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

	if !flags.silent() {
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
	// runUndo refuses before this point when the log has unparseable lines, so
	// a nonzero skip count here is not reachable through undo; treat it like a
	// read error anyway rather than searching an incomplete log.
	entries, skipped, err := oplog.Read(sgDir)
	if err != nil || skipped > 0 || len(entries) == 0 {
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
