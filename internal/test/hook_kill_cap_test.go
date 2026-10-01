package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hookKillCapCommands are the two commands that run pre-pre-push hooks, each
// with the arguments that make it run 10-mark.
var hookKillCapCommands = [][]string{
	{"push", "--refs", "head", "origin"},
	{"hook", "run"},
	{"hook", "run", "10-mark"},
}

// The cap on stopping a hook and the processes it started is --hook-kill-cap-s,
// in whole seconds, bound to the environment variable SAFEGIT_HOOK_KILL_CAP_S.
// A whole number outside 11..1800 is refused before any hook runs, naming
// where the value came from -- the variable and its value when the environment
// set it -- and the allowed range; the remedy the refusal names (unset the
// variable) lets the same command run its hooks under the default cap.
func TestHookKillCapOutOfRangeFromTheEnvironmentIsRefused(t *testing.T) {
	for _, value := range []string{"-5", "0", "10", "1801"} {
		for _, args := range hookKillCapCommands {
			t.Run(strings.Join(args, " ")+" value="+value, func(t *testing.T) {
				dir, remote := newRepoWithRemote(t)
				marker := filepath.Join(evalTempDir(t), "hook-ran")
				installDirHook(t, dir, "10-mark", "touch "+marker)

				_, stderr, code := runSafegitEnv(t, dir, []string{"SAFEGIT_HOOK_KILL_CAP_S=" + value}, args...)
				if code != 1 {
					t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
				}
				want := `SAFEGIT_HOOK_KILL_CAP_S="` + value + `" is not allowed`
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr does not name the variable and its value as %s:\n%s", want, stderr)
				}
				if !strings.Contains(stderr, "a whole number of seconds from 11 to 1800") || !strings.Contains(stderr, "unset the variable") {
					t.Errorf("stderr does not name the allowed range and the remedy:\n%s", stderr)
				}
				if _, err := os.Stat(marker); err == nil {
					t.Errorf("the hook ran although the cap was refused")
				}
				if branches := remoteBranches(t, remote); len(branches) != 0 {
					t.Errorf("a refused push must push nothing; the remote has %v", branches)
				}

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

// The same range check on the flag names the flag, and omitting the flag --
// the remedy it names -- runs the hooks under the default cap.
func TestHookKillCapOutOfRangeFromTheFlagIsRefused(t *testing.T) {
	for _, args := range hookKillCapCommands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir, _ := newRepoWithRemote(t)
			marker := filepath.Join(evalTempDir(t), "hook-ran")
			installDirHook(t, dir, "10-mark", "touch "+marker)

			withFlag := append(append([]string{}, args...), "--hook-kill-cap-s", "10")
			_, stderr, code := runSafegit(t, dir, withFlag...)
			if code != 1 {
				t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "--hook-kill-cap-s 10 is not allowed") || !strings.Contains(stderr, "omit the flag") {
				t.Errorf("stderr does not name the flag, its value and the remedy:\n%s", stderr)
			}
			if strings.Contains(stderr, "SAFEGIT_HOOK_KILL_CAP_S") {
				t.Errorf("a value from the flag must not be blamed on the variable:\n%s", stderr)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Errorf("the hook ran although the cap was refused")
			}
			if _, stderr, code := runSafegit(t, dir, args...); code != 0 {
				t.Errorf("without the flag: exit %d, want 0; stderr:\n%s", code, stderr)
			}
		})
	}
}

// A value that is not a plain whole number is refused by the CLI framework
// itself, naming the variable it came from.
func TestHookKillCapThatIsNotAnIntegerIsRefused(t *testing.T) {
	for _, value := range []string{"abc", "", "1.5", "+30", "030", "99999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			dir, _ := newRepoWithRemote(t)
			marker := filepath.Join(evalTempDir(t), "hook-ran")
			installDirHook(t, dir, "10-mark", "touch "+marker)

			_, stderr, code := runSafegitEnv(t, dir, []string{"SAFEGIT_HOOK_KILL_CAP_S=" + value}, "hook", "run")
			if code != 1 {
				t.Errorf("exit %d, want 1; stderr:\n%s", code, stderr)
			}
			if !strings.Contains(stderr, "SAFEGIT_HOOK_KILL_CAP_S") || !strings.Contains(stderr, "expected integer") {
				t.Errorf("stderr does not refuse the value as an integer from the variable:\n%s", stderr)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Errorf("the hook ran although the cap was refused")
			}
		})
	}
}

// The ends of the allowed range are accepted from either source, and the hooks
// run under them.
func TestHookKillCapInRangeIsAccepted(t *testing.T) {
	for _, value := range []string{"11", "60", "1800"} {
		t.Run(value, func(t *testing.T) {
			dir, _ := newRepoWithRemote(t)
			marker := filepath.Join(evalTempDir(t), "hook-ran")
			installDirHook(t, dir, "10-mark", "touch "+marker)

			_, stderr, code := runSafegitEnv(t, dir, []string{"SAFEGIT_HOOK_KILL_CAP_S=" + value}, "hook", "run")
			if code != 0 {
				t.Fatalf("variable: exit %d, want 0; stderr:\n%s", code, stderr)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("the hook did not run: %v", err)
			}
			if _, stderr, code := runSafegit(t, dir, "hook", "run", "--hook-kill-cap-s", value); code != 0 {
				t.Fatalf("flag: exit %d, want 0; stderr:\n%s", code, stderr)
			}
		})
	}
}

// The help of both commands documents the flag, the variable bound to it and
// the default it falls back to.
func TestHookKillCapHelp(t *testing.T) {
	dir := newRepo(t)
	for _, cmd := range [][]string{{"push", "--help"}, {"hook", "run", "--help"}} {
		stdout, stderr, code := runSafegit(t, dir, cmd...)
		if code != 0 {
			t.Fatalf("%v failed (%d): %s", cmd, code, stderr)
		}
		for _, want := range []string{"--hook-kill-cap-s", "SAFEGIT_HOOK_KILL_CAP_S", "omitted means 60"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%v does not say %q:\n%s", cmd, want, stdout)
			}
		}
	}
}
