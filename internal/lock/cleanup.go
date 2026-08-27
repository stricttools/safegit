package lock

import (
	"os"
	"os/signal"
	"sync"
)

// heldLock is one lock this process still holds: where it is, and which file
// it published there. The identity travels with the path so that the
// no-unwind exit paths remove locks under the same rule RefLock.Release does
// -- see releaseIfOurs.
type heldLock struct {
	path      string
	published publication
}

var (
	cleanupMu    sync.Mutex
	pendingLocks []heldLock
	sigOnce      sync.Once
)

func registerCleanup(path string, published publication) {
	cleanupMu.Lock()
	pendingLocks = append(pendingLocks, heldLock{path: path, published: published})
	cleanupMu.Unlock()

	sigOnce.Do(func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, cleanupSignals()...)
		go func() {
			sig := <-c
			ReleasePending()
			// A process a signal ended exits 128 + the signal number, which is
			// the convention every shell, supervisor and CI runner already reads
			// (a SIGTERM is 143, a SIGINT 130). The number is not safegit's to
			// choose, so it is a carve-out from the exit-code registry rather
			// than a row in it -- stated in internal/exitcode's package doc
			// beside the git-passthrough carve-out.
			os.Exit(signalExitStatus(sig))
		}()
	})
}

// ReleasePending removes every lock this process still holds.
//
// It exists for the exit paths that do not unwind. A deferred Release covers a
// function that returns; it does not cover os.Exit, which safegit's die() and
// several handlers reach on a refusal. Without this, a command that acquired a
// lock and then died on an unrelated error would leave its lock file behind for
// the next contender to wait out and for doctor to report -- recoverable, since
// the holder is dead and the lock is therefore stale, but noise the process
// itself can prevent.
//
// It is safe to call when no lock is held, and safe to call twice.
//
// Each removal is identity-checked exactly as RefLock.Release is: a lock this
// process was force-released out of, and which another process has since
// re-taken, is left alone rather than deleted out from under its new holder.
func ReleasePending() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	for _, held := range pendingLocks {
		_ = releaseIfOurs(held.path, held.published)
	}
	pendingLocks = nil
}

func unregisterCleanup(path string) {
	cleanupMu.Lock()
	for i, p := range pendingLocks {
		if p.path == path {
			pendingLocks = append(pendingLocks[:i], pendingLocks[i+1:]...)
			break
		}
	}
	cleanupMu.Unlock()
}
