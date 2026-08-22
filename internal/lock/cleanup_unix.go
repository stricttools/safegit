//go:build !windows

package lock

import (
	"os"
	"syscall"

	"github.com/smm-h/safegit/internal/exitcode"
)

// cleanupSignals returns the signals that should trigger lock cleanup.
func cleanupSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// signalExitStatus is the exit status of a process a signal ended: the Unix
// convention 128 + the signal number, so a SIGTERM exits 143 and a SIGINT 130.
// safegit adopts it rather than exiting one of its own registered codes because
// the number belongs to the convention, not to safegit -- which is why
// internal/exitcode records it as a carve-out and not as a registry row.
//
// The fallback is unreachable from this package's own registration:
// cleanupSignals returns syscall signals only. It exists because os.Signal is
// an interface, so the conversion has to state what it does when the assertion
// fails, and a signal safegit cannot number is an ordinary failed exit.
func signalExitStatus(sig os.Signal) int {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return exitcode.General
	}
	return 128 + int(s)
}
