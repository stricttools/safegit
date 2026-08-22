//go:build !windows

package lock

import (
	"os"
	"syscall"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
)

// fakeSignal is an os.Signal that is not a syscall.Signal, which is the only
// input for which the status cannot be computed.
type fakeSignal struct{}

func (fakeSignal) String() string { return "fake" }
func (fakeSignal) Signal()        {}

// TestSignalExitStatusFollowsTheUnixConvention pins the computation the
// lock-cleanup handler exits with: 128 + the signal number, for every signal
// the handler registers. The numbers are the shell convention's, so they are
// deliberately absent from internal/exitcode -- see that package's doc.
func TestSignalExitStatusFollowsTheUnixConvention(t *testing.T) {
	for _, tc := range []struct {
		sig        os.Signal
		wantStatus int
	}{
		{syscall.SIGINT, 128 + int(syscall.SIGINT)},
		{syscall.SIGTERM, 128 + int(syscall.SIGTERM)},
		{os.Interrupt, 128 + int(syscall.SIGINT)},
	} {
		if got := signalExitStatus(tc.sig); got != tc.wantStatus {
			t.Errorf("signalExitStatus(%v) = %d, want %d", tc.sig, got, tc.wantStatus)
		}
	}

	// The concrete numbers, spelled out once: a reader of a CI log sees these,
	// not an expression.
	if got := signalExitStatus(syscall.SIGTERM); got != 143 {
		t.Errorf("a SIGTERM exit is %d, want 143", got)
	}
	if got := signalExitStatus(syscall.SIGINT); got != 130 {
		t.Errorf("a SIGINT exit is %d, want 130", got)
	}

	// Every signal the handler actually registers is numberable, so the
	// fallback below is unreachable in production.
	for _, sig := range cleanupSignals() {
		if _, ok := sig.(syscall.Signal); !ok {
			t.Errorf("cleanupSignals returned %v, which carries no signal number", sig)
		}
	}
	if got := signalExitStatus(fakeSignal{}); got != exitcode.General {
		t.Errorf("a signal with no number exits %d, want the general failure code %d", got, exitcode.General)
	}
}
