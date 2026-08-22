package lock

import (
	"os"
	"os/signal"
	"sync"

	"github.com/smm-h/safegit/internal/exitcode"
)

var (
	cleanupMu   sync.Mutex
	pendingLocks []string
	sigOnce     sync.Once
)

func registerCleanup(path string) {
	cleanupMu.Lock()
	pendingLocks = append(pendingLocks, path)
	cleanupMu.Unlock()

	sigOnce.Do(func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, cleanupSignals()...)
		go func() {
			<-c
			ReleasePending()
			os.Exit(exitcode.General)
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
func ReleasePending() {
	cleanupMu.Lock()
	defer cleanupMu.Unlock()
	for _, p := range pendingLocks {
		os.Remove(p)
	}
	pendingLocks = nil
}

func unregisterCleanup(path string) {
	cleanupMu.Lock()
	for i, p := range pendingLocks {
		if p == path {
			pendingLocks = append(pendingLocks[:i], pendingLocks[i+1:]...)
			break
		}
	}
	cleanupMu.Unlock()
}
