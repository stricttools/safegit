package lock

import (
	"os"
	"os/signal"
	"sync"
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
			cleanupMu.Lock()
			for _, p := range pendingLocks {
				os.Remove(p)
			}
			cleanupMu.Unlock()
			os.Exit(1)
		}()
	})
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
