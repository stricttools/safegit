package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/strictcli/go/strictcli"
)

// undoableOps maps op types to the extra key that holds the rollback target SHA.
// commit -> parent (the commit before this one)
// amend  -> oldSha (the commit that was replaced)
// reword -> oldSha (the commit that was replaced)
//
// The three conclusion commands record their own op names rather than "commit",
// so undo can name what it is reversing -- and warn about the half it cannot
// reverse (see conclusionOps).
var undoableOps = map[string]string{
	"commit":               "parent",
	"mv":                   "parent",
	"amend":                "oldSha",
	"reword":               "oldSha",
	"merge":                "parent",
	"pull":                 "parent",
	"cherry-pick":          "parent",
	"revert":               "parent",
	"merge-continue":       "parent",
	"cherry-pick-continue": "parent",
	"revert-continue":      "parent",
}

// authoredByPipeline reports whether an entry records a commit SAFEGIT MADE, as
// opposed to a ref movement it merely performed.
//
// The discriminator is the outcome field, and it is exact rather than a
// heuristic: the commit pipeline writes ref/parent/sha/tree/attempts and never
// an outcome, because a commit entry exists only when the commit does. Every
// entry that carries an outcome comes from the guarded-operation recorder,
// where the entry describes what GIT did to the branch -- a merge that
// fast-forwarded onto somebody else's commits, a merge left parked, an
// operation git refused.
//
// It is why `safegit merge` can share one op name across both: the merge it
// authors is undoable, and the fast-forward it performs is not, because the
// commits a fast-forward moved onto are not safegit's to take back off.
func authoredByPipeline(e oplog.Entry) bool {
	if e.Extra == nil {
		return false
	}
	_, hasOutcome := e.Extra["outcome"]
	return !hasOutcome
}

// conclusionOps are the operations whose undo is PARTIAL by construction.
//
// Undo moves a ref and reconciles the index. A conclusion did that AND removed
// git's operation state files, and nothing in the oplog records what those
// files held -- MERGE_HEAD's commits, the message draft, the conflict stages the
// index no longer carries. So undoing one gives back the pre-conclusion tip and
// a repository git considers idle, not one it considers mid-merge. Saying so is
// the whole point of these ops being distinguishable from a plain commit.
var conclusionOps = map[string]string{
	"merge":                "merge",
	"pull":                 "merge",
	"cherry-pick":          "cherry-pick",
	"revert":               "revert",
	"merge-continue":       "merge",
	"cherry-pick-continue": "cherry-pick",
	"revert-continue":      "revert",
}

// undoPayload is what `undo` puts in the envelope's payload: which ref moved,
// what operation was reversed, and the two object names the move is between.
//
// Unlike a commit's, every value here is REAL in both modes -- undo does not
// create an object, it moves a ref between two that already exist, and both
// come out of the operation log. A root undo has no rollback target at all, so
// sha is null and deleted says why.
type undoPayload struct {
	Ref      string  `json:"ref"`
	UndoneOp string  `json:"undone_op"`
	SHA      *string `json:"sha"`
	OldSHA   string  `json:"old_sha"`
	Count    int     `json:"count"`
	Deleted  bool    `json:"deleted"`
	// Residue carries the aftercare steps that failed after the ref moved --
	// the same commit-stands reporting every other ref-moving command does.
	Residue []residueEntry `json:"residue"`
	DryRun  bool           `json:"dry_run"`
}

var undoPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"ref":       strictcli.SchemaType("string"),
		"undone_op": strictcli.SchemaType("string"),
		"sha":       strictcli.SchemaType("string", "null"),
		"old_sha":   strictcli.SchemaType("string"),
		"count":     strictcli.SchemaType("integer"),
		"deleted":   strictcli.SchemaType("boolean"),
		"residue": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"step":   strictcli.SchemaType("string"),
				"detail": strictcli.SchemaType("string"),
			},
			[]string{"step", "detail"},
			false,
		)),
		"dry_run": strictcli.SchemaType("boolean"),
	},
	[]string{"ref", "undone_op", "sha", "old_sha", "count", "deleted", "residue", "dry_run"},
	false,
)

// undoPayloadFor builds the payload both modes emit.
func undoPayloadFor(flags globalFlags, ref, op, targetSHA, currentSHA string, count int, isRootUndo bool, residue []residueEntry) undoPayload {
	var sha *string
	if !isRootUndo {
		sha = &targetSHA
	}
	return undoPayload{
		Ref:      ref,
		UndoneOp: op,
		SHA:      sha,
		OldSHA:   currentSHA,
		Count:    count,
		Deleted:  isRootUndo,
		Residue:  orEmptyResidue(residue),
		DryRun:   flags.dryRun,
	}
}

// recordUndoRefUpdate mints undo's ref move through the effects handle: the
// compare-and-swap update for an ordinary undo, git's deletion form for a root
// undo whose branch had nothing before it.
//
// It is one site for both modes, like the commit pipeline's own ref update, and
// for the same reason -- a preview must record the argv the execute path really
// runs. Both SHAs are REAL in a preview too: the rollback target and the
// compare-and-swap pin come out of the operation log, so there is nothing here a
// preview would have to guess at (currentSHA can fall back to a RevParse of HEAD
// on a thin log, which is still a real object name).
//
// Check(false) keeps git's own stderr readable to the caller, exactly as the
// commit pipeline's ref update does.
func recordUndoRefUpdate(flags globalFlags, ref, targetSHA, currentSHA string, isRootUndo bool) error {
	args := []string{"update-ref", ref, targetSHA, currentSHA}
	verb := "update-ref"
	if isRootUndo {
		args = []string{"update-ref", "-d", ref, currentSHA}
		verb = "delete-ref"
	}
	argv, err := gitexec.ArgvAny(gitexec.ExemptUndoRefUpdate, gitexec.NoDoor, args...)
	if err != nil {
		return err
	}
	done, err := flags.effects().Run(argv, strictcli.Resource("ref:"+ref), strictcli.Check(false))
	if err != nil {
		return err
	}
	if flags.dryRun {
		// Recorded instead of performed: the carrier is unsettled and asking it
		// anything would panic, and there is nothing to ask.
		return nil
	}
	if code := done.ExitCode(); code != 0 {
		return fmt.Errorf("%s failed: exit %d: %s", verb, code, strings.TrimSpace(done.Stderr()))
	}
	return nil
}

// sessionIDEnvVar is the Claude Code session handshake variable. It is declared
// on the app (WithHandshakeEnv) and read through ctx.InfraValue, so callers pass
// the resolved value in rather than reaching for the environment here.
const sessionIDEnvVar = "CLAUDE_CODE_SESSION_ID"

// runUndo RETURNS its exit code rather than exiting, for the same reason the
// commit family does: once the ref has moved, an aftercare failure is a
// ref-moved-stands outcome that has to be REPORTED, and os.Exit runs below the
// seam that reports it. Every refusal above the ref update still dies.
func runUndo(flags globalFlags, bypassSession bool, count int, sessionID string) int {
	const cmd = "undo"

	// Post-parse argument validation: the framework accepted the command line,
	// safegit rejects the value. That is exitcode.Usage by the registry's own
	// definition.
	if count <= 0 {
		die(exitcode.Usage, fmt.Sprintf("--count must be positive, got %d", count))
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		die(exitcode.NotInitialized, err.Error())
	}

	sgDir := repo.SafegitDir(gitDir)

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("loading config: %v", err))
	}

	// Before any lock is taken and long before a ref moves: undoing a commit in a
	// submodule moves the parent's gitlink back, and a parent that has not
	// answered the auto-bump question is a refusal rather than a rollback
	// followed by one. It is the same refusal, in the same place, that every
	// other commit-family route makes.
	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
	}

	// Outermost, and taken BEFORE the in-flight check below so the check reads a
	// state no passthrough in this worktree can change while undo acts on it.
	// The ref lock further down is the inner one.
	release, code := acquireOperationLock(flags, gitDir, cmd)
	if code != 0 {
		os.Exit(code)
	}
	defer release()

	// Undoing while git has an operation in flight is incoherent on its face --
	// the operation was computed against a commit undo is about to move off
	// HEAD -- and the rollback's index reconciliation would erase the conflict
	// stages git needs to conclude it, leaving a repository that looks clean and
	// mid-merge at once.
	if err := coord.GuardInFlight(gitDir, cmd, nil); err != nil {
		die(exitcode.CoordinationBusy, err.Error())
	}

	ctx := flags.ctx()

	// Resolve current branch
	ref, err := git.HeadRef(ctx)
	if err != nil || ref == "" {
		die(exitcode.General, "HEAD is detached; undo requires a branch")
	}

	// Read all oplog entries. Undo walks the log backwards and reverses what
	// it finds, so a log with unreadable lines cannot be undone from: the
	// entry that would be reversed next may be one of the lines that did not
	// parse. Refuse instead of undoing the wrong operation.
	allEntries, skipped, err := oplog.Read(sgDir)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("reading oplog: %v", err))
	}
	if skipped > 0 {
		die(exitcode.General, fmt.Sprintf("operation log has %d unparseable line(s); undo needs a complete log and refuses to guess (inspect %s)", skipped, oplog.Path(sgDir)))
	}

	// Filter to entries for this ref (and session, unless bypass-session)
	if !bypassSession && sessionID == "" {
		die(exitcode.General, "no session ID found ("+sessionIDEnvVar+" not set); pass --bypass-session to undo across all sessions")
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
	// notOurs is the newest entry whose OP is undoable but whose commit safegit
	// did not author -- a fast-forward, the one shape that puts a ref move
	// safegit performed and a commit it did not create in the same entry. It is
	// kept only so the refusal can name it.
	var notOurs *oplog.Entry
	// reversing is the tip each live undoable step recorded: exactly the
	// commits this undo claims to be taking back off the branch, and what the
	// range check below measures the branch's actual history against.
	reversing := make(map[string]bool, count)

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]

		if e.Op == "undo" {
			// Each undo entry means one preceding undoable was already undone
			cancelled++
			continue
		}

		// Check if this op is undoable. An op name alone does not settle it:
		// `merge` names both the merge safegit authored and the fast-forward it
		// performed onto commits git created, and only the first is safegit's
		// to take back.
		if _, isUndoable := undoableOps[e.Op]; !isUndoable || !authoredByPipeline(e) {
			if isUndoable {
				// Remembered so the refusal below can say WHY nothing was
				// found, rather than reporting an empty log.
				if notOurs == nil {
					copied := e
					notOurs = &copied
				}
				continue
			}
			// Scrub and rewrite-author operations invalidate all prior SHAs in
			// the oplog. We cannot safely undo anything before them.
			if strings.HasPrefix(e.Op, "scrub-") || e.Op == "rewrite-author" {
				die(exitcode.General, fmt.Sprintf("cannot undo %s — history rewrite invalidated prior oplog entries", e.Op))
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
		if tip := oplog.TipSHA(e.Extra); tip != "" {
			reversing[tip] = true
		}
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
			if notOurs != nil {
				outcome, _ := notOurs.Extra["outcome"].(string)
				die(exitcode.General, fmt.Sprintf(
					"no undoable operations found for %s in the oplog\n"+
						"  the last thing safegit did to this branch was a %s (%s), which created no commit of its own.\n"+
						"  undo reverses commits safegit authored; the commits this branch moved onto are git's,\n"+
						"  and taking them back off is a reset rather than an undo.",
					refShortName(ref), notOurs.Op, outcome))
			}
			die(exitcode.General, fmt.Sprintf("no undoable operations found for %s in the oplog", refShortName(ref)))
		}
		die(exitcode.General, fmt.Sprintf("only %d undoable operations available, requested %d", liveSteps, count))
	}

	// Determine the target key and SHA for the rollback
	targetKey := undoableOps[targetEntry.Op]
	targetSHARaw, fieldPresent := targetEntry.Extra[targetKey]
	if !fieldPresent {
		die(exitcode.General, fmt.Sprintf("oplog entry for %q is missing %q field", targetEntry.Op, targetKey))
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
			die(exitcode.General, fmt.Sprintf("oplog entry for %q has empty %q field", targetEntry.Op, targetKey))
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
			die(exitcode.General, fmt.Sprintf("resolving HEAD: %v", err))
		}
	}

	// The RANGE check, run before the preview as well as before the update: the
	// arithmetic above is oplog arithmetic and has not looked at the branch
	// once. See refuseUnaccountedRange.
	refuseUnaccountedRange(ctx, ref, targetSHA, currentSHA, reversing, allEntries, sessionID, bypassSession)

	if flags.dryRun {
		// The dry-run doctrine, in the order it names: every state READ first,
		// then the would-do mutations in the order the execute path performs
		// them. The reads are the rollback arithmetic above plus the
		// parent-bump decision's own (parent config, the nested check, the
		// gitlink) -- made HERE, before the first record, because a recorded
		// mutation is one that did not happen and every read after it would be
		// reading a world the preview has already described as changed.
		bump, bumpErr := planParentBump(ctx, flags, targetSHA)
		if bumpErr != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", bumpErr))
		}

		// Execution order: the real run moves the ref and then bumps the
		// parent, so the records come out that way round.
		if err := recordUndoRefUpdate(flags, ref, targetSHA, currentSHA, isRootUndo); err != nil {
			die(exitcode.General, fmt.Sprintf("recording the ref update: %v", err))
		}
		if bump != nil {
			// The REAL Triggered-by value, not the placeholder: undo does not
			// author a commit in the submodule, it rolls the branch back onto one
			// that already exists, and targetSHA is exactly what the execute path
			// writes into the parent's commit message.
			if err := recordParentBumpPreview(flags, bump, targetSHA, "undo", ""); err != nil {
				die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
			}
		}

		flags.payload(undoPayloadFor(flags, ref, targetEntry.Op, targetSHA, currentSHA, count, isRootUndo, nil))

		outf(flags, "would undo %d operation(s) on %s\n", count, refShortName(ref))
		if isRootUndo {
			outf(flags, "  %s -> (empty, delete ref)\n", currentSHA[:8])
		} else {
			outf(flags, "  %s -> %s\n", currentSHA[:8], targetSHA[:8])
		}
		return exitcode.OK
	}

	// Acquire lock on the ref
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, ref, "undo", timeout)
	if err != nil {
		if lock.IsTimeout(err) {
			die(exitcode.LockTimeout, fmt.Sprintf("acquiring lock: %v", err))
		}
		die(exitcode.General, fmt.Sprintf("acquiring lock: %v", err))
	}
	defer lk.Release()

	// Perform the ref update -- through the effects handle, which is what makes
	// the move visible in machine mode: recorded in a preview, performed here.
	if err := recordUndoRefUpdate(flags, ref, targetSHA, currentSHA, isRootUndo); err != nil {
		die(exitcode.General, fmt.Sprintf("%s (ref may have moved)", err))
	}

	// Reconcile the shared index so git status/diff reflect the rollback while
	// every staged change the undone commit does not account for survives it.
	// For a root undo the target is the empty tree, spelled "".
	//
	// The ref has already moved, so a failure here is a ref-moved-stands outcome
	// rather than a refusal: it is recorded and carried in the exit code, and the
	// rest of the undo -- the notes, and the oplog entry that keeps every later
	// reader from mistaking this move for one safegit did not make -- still runs.
	syncTreeish := targetSHA
	if isRootUndo {
		syncTreeish = ""
	}
	var residue []residueEntry
	if err := git.ReconcileMainIndex(ctx, currentSHA, syncTreeish); err != nil {
		detail := fmt.Sprintf("%s was undone, but reconciling the shared index failed: %v", currentSHA[:8], err)
		fmt.Fprintf(os.Stderr, "error: %s\n", detail)
		residue = recordAftercareFailure(residue, commit.StepIndexReconcile, detail)
	}

	// A conclusion's undo is partial by construction: the ref is back, the git
	// operation it concluded is not. Said on stderr unconditionally, like the
	// parent-bump note below -- it is a fact about what this undo did NOT do,
	// and withholding it under --quiet would let an operator believe the merge
	// is waiting to be concluded again.
	// A move's undo is partial in the other direction: undo reverses the
	// COMMIT, and it has never touched the working tree. The files are still at
	// the paths `safegit mv` put them at, which is the honest half of the
	// operation to leave standing -- and saying so is the difference between an
	// operator who knows where their files are and one who does not.
	if targetEntry.Op == mvOplogOp {
		fmt.Fprintf(os.Stderr, "note: the commit is reversed, but the files are still at their new paths -- undo moves\n")
		fmt.Fprintf(os.Stderr, "      a ref and never the working tree. Move them back yourself, or re-commit them\n")
		fmt.Fprintf(os.Stderr, "      where they are with 'safegit commit --moved'.\n")
	}

	if operation, isConclusion := conclusionOps[targetEntry.Op]; isConclusion {
		fmt.Fprintf(os.Stderr, "note: %s is reversed, but git's %s state is NOT restored -- MERGE_HEAD, the message draft\n", targetEntry.Op, operation)
		fmt.Fprintf(os.Stderr, "      and the conflict stages are gone, so the repository is idle rather than mid-%s.\n", operation)
		fmt.Fprintf(os.Stderr, "      Re-run the %s to get back to a state safegit %s-continue can conclude.\n", operation, operation)
	}

	// If the commit being undone triggered a parent bump, inform the user.
	if bumpSHA := findAssociatedParentBump(sgDir, targetEntry); bumpSHA != "" {
		fmt.Fprintf(os.Stderr, "note: undoing commit that triggered parent bump %s\n", bumpSHA[:8])
	}

	// A RETURN, not die(): the ref is already back where the oplog says, so a
	// parent bump that failed is one more piece of aftercare residue rather than
	// a reason to exit as though nothing happened. Returning also releases both
	// locks held here -- the worktree operation lock taken at the top and the ref
	// lock taken just above -- through their own defers, which os.Exit would
	// have run past.
	if err := maybeAutoBumpParent(ctx, flags, gitDir, targetSHA, "undo", ""); err != nil {
		residue = reportAftercareFailure(residue, stepParentBump, err)
	}

	// Log the undo to the oplog. A ROOT undo deletes the ref rather than moving
	// it, and says so: without that, the readers of this log walk past an undo
	// whose new tip is empty, find the commit it reversed, and report safegit's
	// own deletion as a ref that moved behind safegit's back.
	undoExtra := map[string]interface{}{
		"ref":      ref,
		"undoneOp": targetEntry.Op,
		"sha":      targetSHA,
		"oldSha":   currentSHA,
		"count":    count,
	}
	if isRootUndo {
		undoExtra["deleted"] = true
	}
	_ = oplog.Append(sgDir, oplog.Entry{Op: "undo", Extra: undoExtra})

	flags.payload(undoPayloadFor(flags, ref, targetEntry.Op, targetSHA, currentSHA, count, isRootUndo, residue))

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
	return aftercareExit(residue)
}

// refuseUnaccountedRange is the check that stands between undo's arithmetic and
// the branch it is about to move.
//
// Everything above it is oplog arithmetic: count back N recorded operations,
// take what the Nth one was built on top of, and that is the rollback target.
// Nothing in it looks at the branch, so a commit safegit did not record --
// a plain `git commit`, a rebase's replayed commits, the commits git's own
// sequencer makes for a queue -- is simply in the way, and moving the ref past it
// takes it out of the branch's history.
//
// The compare-and-swap alone cannot answer this. It pins the NEWEST recorded
// tip, so it notices a foreign commit sitting on top and nothing else: one
// safegit commit above a foreign one puts the pin back in agreement with the
// branch, and a multi-step undo then walks the ref straight past the foreign
// commit and reports success. That was live, and it destroyed both flavors.
//
// So the question asked here is the whole one: every commit the branch would
// LOSE -- the first-parent walk from the rollback target to where the ref
// actually stands -- must be one the oplog says this undo is reversing. The
// walk is first-parent because a merge's other side was never made by the
// operations being reversed: undoing a merge commit undoes the merge, not the
// branch that was merged in.
//
// It refuses through die(), so the same verdict reaches a --dry-run: a preview
// that announced a rollback the real run refuses was the third half of this
// same defect. The compare-and-swap stays where it is as the final race guard
// -- this check is what produces an honest message.
func refuseUnaccountedRange(ctx context.Context, ref, targetSHA, pinnedSHA string, reversing map[string]bool, all []oplog.Entry, sessionID string, bypassSession bool) {
	branchSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("reading where %s actually stands: %v", refShortName(ref), err))
	}

	lost, err := git.FirstParentRange(ctx, targetSHA, branchSHA)
	if err != nil {
		die(exitcode.General, fmt.Sprintf("listing the commits undo would move %s back over: %v", refShortName(ref), err))
	}

	var foreign []string
	for _, sha := range lost {
		if !reversing[sha] {
			foreign = append(foreign, sha)
		}
	}
	if len(foreign) > 0 {
		die(exitcode.General, unaccountedRangeMessage(ctx, ref, targetSHA, foreign, all, sessionID, bypassSession))
	}

	// Nothing foreign in the range, but the branch is not where the log's last
	// recorded operation left it: something moved it sideways or backwards --
	// a reset, a force-update, an amend from outside safegit. The
	// compare-and-swap would refuse this moments later with a plumbing message,
	// and a preview would not refuse it at all.
	if pinnedSHA != "" && branchSHA != pinnedSHA {
		die(exitcode.General, fmt.Sprintf(
			"%s is at %s, but safegit's log says its last recorded operation left it at %s\n"+
				"  something moved the branch that undo has no record of -- a reset, a force-update, an amend made outside safegit.\n"+
				"  undo will not roll back a branch it cannot account for. Inspect the branch (git log, git reflog) and move it yourself.",
			refShortName(ref), shortSHA(branchSHA), shortSHA(pinnedSHA)))
	}
}

// unaccountedRangeMessage renders the refusal: every commit in the way, then
// what undo can and cannot do about it.
func unaccountedRangeMessage(ctx context.Context, ref, targetSHA string, foreign []string, all []oplog.Entry, sessionID string, bypassSession bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "undo would move %s back over %d commit(s) safegit did not create:\n", refShortName(ref), len(foreign))
	for _, sha := range foreign {
		fmt.Fprintf(&b, "  %s\n", describeCommit(ctx, sha))
	}
	fmt.Fprintf(&b, "  undo reverses the operations safegit recorded in its own log, and none of the commits above is one of them.\n")
	fmt.Fprintf(&b, "  A commit git authored -- a plain `git commit`, a passthrough cherry-pick, the conclusion of a queued\n")
	fmt.Fprintf(&b, "  sequence git committed itself -- is outside undo's reach, and rolling %s back to %s would drop it\n",
		refShortName(ref), rollbackTargetText(targetSHA))
	fmt.Fprintf(&b, "  out of the branch's history. Reverse those commits with git, or move them onto a branch of their own first.")

	// One of them may be safegit's after all -- another session's. That is a
	// different situation with a real way out, so it is said rather than left
	// under the git-authored sentence.
	if !bypassSession {
		if other := otherSessionCommit(foreign, all, sessionID); other != "" {
			fmt.Fprintf(&b, "\n  (%s was recorded by another safegit session; --bypass-session widens undo to every session's operations)",
				shortSHA(other))
		}
	}
	return b.String()
}

// rollbackTargetText names where the branch would have gone, including the
// root-undo case where the answer is "nowhere -- the ref is deleted".
func rollbackTargetText(targetSHA string) string {
	if targetSHA == "" {
		return "nothing (the branch ref would be deleted)"
	}
	return shortSHA(targetSHA)
}

// otherSessionCommit reports the first foreign commit that IS in safegit's
// oplog under a different session ID, or "" when none is.
func otherSessionCommit(foreign []string, all []oplog.Entry, sessionID string) string {
	for _, sha := range foreign {
		for _, e := range all {
			if e.Extra == nil || e.SessionID == sessionID {
				continue
			}
			if oplog.TipSHA(e.Extra) == sha {
				return sha
			}
		}
	}
	return ""
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
