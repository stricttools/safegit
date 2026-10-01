package main

import (
	"context"
	"fmt"
	"time"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/lock"
	"github.com/stricttools/safegit/internal/repo"
	"github.com/stricttools/strictcli/go/strictcli"
)

// acquireOperationLock takes the worktree operation lock for op and returns the
// function that releases it. On failure it returns a nil release function and
// the exit code the caller must return.
//
// Every command that mutates THIS worktree goes through here: the guarded
// passthroughs (switch, pull, merge, rebase, reset, bisect, cherry-pick,
// revert), the commit pipeline's three entry points, and undo. Two safegit
// processes in one worktree therefore never run tree-mutating work at the same
// time, and -- the reason the commit family takes it too -- a passthrough
// cannot create sequencer state in the window between a commit's in-flight
// check and its ref update. Without that, the check would be a snapshot of a
// state another process is free to change a millisecond later.
//
// Ordering is declared once, in internal/lock: this lock is OUTERMOST, and the
// per-ref locks the commit pipeline and undo take are acquired inside it. Which
// is why it is taken HERE, in the command layer, and never inside the pipeline.
//
// It is held for the operation's full duration, including the editor session of
// an interactive rebase. A second safegit process waits
// lock.acquireTimeoutSeconds and then refuses with exitcode.LockTimeout, naming
// the holder -- it does not proceed concurrently.
//
// A dry run does not take it. A preview performs no mutation, so it has nothing
// to serialize against, and taking the lock would mean a command that promises
// to change nothing creating and removing a file inside .git/safegit.
func acquireOperationLock(flags globalFlags, gitDir, op string) (func(), int) {
	release, code, message := operationLock(flags, gitDir, op)
	if code != exitcode.OK {
		errorf(flags, "%s", message)
	}
	return release, code
}

// mustAcquireOperationLock is acquireOperationLock for a caller that ends the
// command when the lock cannot be taken: it returns the release function, or
// ends the command through strictcli.ExitNow with the code and reason.
func mustAcquireOperationLock(flags globalFlags, gitDir, op string) func() {
	release, code, message := operationLock(flags, gitDir, op)
	if code != exitcode.OK {
		strictcli.ExitNow(code, message)
	}
	return release
}

// operationLock takes the worktree operation lock and returns the release
// function, or a nil one with the exit code and the reason it could not.
func operationLock(flags globalFlags, gitDir, op string) (func(), int, string) {
	if flags.dryRun {
		return func() {}, exitcode.OK, ""
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		return nil, exitcode.General, fmt.Sprintf("loading config: %v", err)
	}
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second

	// The worktree-local safegit dir on both counts: the lock file lives with
	// the worktree it serializes (two worktrees of one repository work
	// independently), and so does the oplog a stale-lock recovery is recorded
	// in.
	sgDir := repo.SafegitDir(gitDir)
	lk, err := lock.Acquire(sgDir, sgDir, lock.OperationRef, op, timeout)
	if err != nil {
		if lock.IsTimeout(err) {
			return nil, exitcode.LockTimeout, fmt.Sprintf("%v\n  another safegit operation owns this worktree; wait for it, or release the lock with: safegit unlock %s", err, lock.OperationRef)
		}
		return nil, exitcode.General, fmt.Sprintf("acquiring the worktree operation lock: %v", err)
	}
	return func() { _ = lk.Release() }, exitcode.OK, ""
}

// acquireRewriteLock takes the repository-wide history-rewrite lock for one
// repository and returns it, dying with the exit code the failure calls for: a
// contended lock is LockTimeout (naming the holder), anything else is General.
//
// gitDir and sgDir name the repository whose lock is taken: the parent's for
// an ordinary rewrite, a submodule's own git and safegit directories when the
// operation reaches into one. Every one of the four rewriting commands
// (scrub file, scrub match, scrub run, author rewrite) takes it here.
//
// Ordering across repositories, declared here because this is where the second
// lock is taken: a rewrite that spans a parent and a submodule takes the
// PARENT's lock first and the submodule's second, and never the other way
// round. Nothing anywhere takes a submodule's rewrite lock before its parent's,
// so there is no cycle to close -- the same total-order argument internal/lock
// makes for the operation lock and the per-ref locks.
//
// Both are held for the whole two-repository operation, which is what makes the
// cleanliness re-check and the objects-before-refs publication exclusive on both
// sides rather than only on the parent's.
func acquireRewriteLock(ctx context.Context, flags globalFlags, gitDir, sgDir, op string) *lock.RefLock {
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		strictcli.ExitNow(exitcode.General, fmt.Sprintf("loading config: %v", err))
	}
	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	sharedDir := repo.SharedSafegitDir(ctx, gitDir)
	lk, err := lock.Acquire(sharedDir, sgDir, lock.RewriteRef, op, timeout)
	if err != nil {
		// The real error, not a fixed sentence: it names the ref and the
		// process still holding it, which is the only thing that tells the
		// operator what to look at. A timeout gets its own exit code so a
		// caller can tell contention apart from every other lock failure.
		if lock.IsTimeout(err) {
			strictcli.ExitNow(exitcode.LockTimeout, exitMessage(err))
		}
		strictcli.ExitNow(exitcode.General, fmt.Sprintf("acquiring rewrite lock: %v", err))
	}
	return lk
}
