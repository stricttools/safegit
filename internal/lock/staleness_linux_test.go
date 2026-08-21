//go:build linux

package lock

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/testutil"
)

// TestIsStalePathologicalCommHolder covers a holder whose process name
// contains spaces and parentheses: the /proc/<pid>/stat parser must still read
// the right field, or every such holder would look like a reused PID and lose
// its lock.
//
// It lives in a linux-tagged file because the trap it exercises is a /proc
// format detail and the spawn helper only reports a child's comm on linux --
// on any other platform the assertion below has nothing to read and would fail
// rather than skip.
func TestIsStalePathologicalCommHolder(t *testing.T) {
	proc := testutil.SpawnPathologicalNameSleeper(t)
	if proc.Comm != testutil.PathologicalCommName {
		t.Fatalf("comm = %q, want %q -- the test is no longer exercising the trap", proc.Comm, testutil.PathologicalCommName)
	}
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, proc.Pid, trueStart(t, proc.Pid))
	setMtime(t, lp, time.Now().Add(-time.Hour))

	if IsStale(lp) {
		t.Errorf("lock held by live pid %d with comm %q reported stale", proc.Pid, proc.Comm)
	}
}
