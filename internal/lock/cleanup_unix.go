//go:build !windows

package lock

import (
	"os"
	"syscall"
)

// cleanupSignals returns the signals that should trigger lock cleanup.
func cleanupSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
