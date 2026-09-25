package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SAFEGIT_HOOK_KILL_CAP_S sets the cap on stopping a hook and the processes it
// started, in whole seconds. A value safegit cannot honor -- not a whole
// number, below the minimum or above the maximum -- is refused before any
// hook runs, naming the variable, the value and the allowed range, so an
// operator never runs under a cap other than the one they asked for.
func TestHookKillCapOutOfRangeIsRefusedBeforeAnyHookRuns(t *testing.T) {
	for _, value := range []string{"abc", "1.5", "", "-5", "0", "10", "1801", "99999999999999999999"} {
		for _, args := range [][]string{
			{"push", "--refs", "head", "origin"},
			{"hook", "run"},
			{"hook", "run", "10-mark"},
		} {
			t.Run(strings.Join(args, " ")+" value="+value, func(t *testing.T) {
				dir, remote := newRepoWithRemote(t)
				marker := filepath.Join(evalTempDir(t), "hook-ran")
				installDirHook(t, dir, "10-mark", "touch "+marker)

				_, stderr, code := runSafegitEnv(t, dir, []string{"SAFEGIT_HOOK_KILL_CAP_S=" + value}, args...)
				if code != 1 {
					t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
				}
				want := `SAFEGIT_HOOK_KILL_CAP_S="` + value + `"`
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr does not name the variable and its value as %s:\n%s", want, stderr)
				}
				if !strings.Contains(stderr, "a whole number of seconds from 11 to 1800") {
					t.Errorf("stderr does not name the allowed range:\n%s", stderr)
				}
				if _, err := os.Stat(marker); err == nil {
					t.Errorf("the hook ran although the cap was refused")
				}
				if branches := remoteBranches(t, remote); len(branches) != 0 {
					t.Errorf("a refused push must push nothing; the remote has %v", branches)
				}

				// The remedy the refusal names: unset the variable, and the
				// same command runs its hooks under the default cap.
				if _, stderr, code := runSafegit(t, dir, args...); code != 0 {
					t.Errorf("with the variable unset: exit %d, want 0; stderr:\n%s", code, stderr)
				}
				if _, err := os.Stat(marker); err != nil {
					t.Errorf("with the variable unset the hook did not run: %v", err)
				}
			})
		}
	}
}

// The ends of the allowed range are accepted, and the hooks run under them.
func TestHookKillCapInRangeIsAccepted(t *testing.T) {
	for _, value := range []string{"11", "60", "1800"} {
		t.Run(value, func(t *testing.T) {
			dir, _ := newRepoWithRemote(t)
			marker := filepath.Join(evalTempDir(t), "hook-ran")
			installDirHook(t, dir, "10-mark", "touch "+marker)

			_, stderr, code := runSafegitEnv(t, dir, []string{"SAFEGIT_HOOK_KILL_CAP_S=" + value}, "hook", "run")
			if code != 0 {
				t.Fatalf("exit %d, want 0; stderr:\n%s", code, stderr)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("the hook did not run: %v", err)
			}
		})
	}
}
