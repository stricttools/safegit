//go:build linux

package test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
)

// When safegit cannot run a hook under containment at all -- here, nothing the
// second hook needs a descriptor for can be opened, because the first hook
// lowered safegit's open-file limit below what it already holds -- the run fails with safegit's
// own error, exit 1, and the payload is still emitted with every hook run
// recorded before it: the runs are facts, the error text and the exit code are
// the verdict.
func TestHookRunsRecordedBeforeAContainmentErrorAreEmitted(t *testing.T) {
	if _, err := exec.LookPath("prlimit"); err != nil {
		t.Skip("prlimit is not installed")
	}
	dir, remote := newRepoWithRemote(t)
	installDirHook(t, dir, "10-a", "prlimit --pid $PPID --nofile=4:4")
	installDirHook(t, dir, "20-b", "true")

	for _, args := range [][]string{
		{"--json", "push", "--refs", "head", "origin"},
		{"--json", "hook", "run"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := runSafegit(t, dir, args...)
			if code != exitcode.General {
				t.Fatalf("exit = %d, want %d; stderr: %s", code, exitcode.General, stderr)
			}
			if !strings.Contains(stderr, "error: running hooks: hook 20-b: ") || !strings.Contains(stderr, "too many open files") {
				t.Errorf("the error must name the hook safegit could not run and why; stderr:\n%s", stderr)
			}
			hooks := payloadHooks(t, stdout)
			if len(hooks) != 1 {
				t.Fatalf("hooks = %d entries, want 1 (the run before the error)", len(hooks))
			}
			checkEntry(t, hooks[0], "10-a", 0, false)
			assertNoVerdictMember(t, stdout)
		})
	}
	if branches := remoteBranches(t, remote); len(branches) != 0 {
		t.Errorf("a run that could not finish its hooks must push nothing; the remote has %v", branches)
	}
}
