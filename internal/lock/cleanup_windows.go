//go:build windows

package lock

import "os"

// cleanupSignals returns the signals that should trigger lock cleanup.
// Windows has no SIGTERM; only Interrupt (Ctrl+C) is catchable.
func cleanupSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
