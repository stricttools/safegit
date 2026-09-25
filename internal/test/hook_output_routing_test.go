package test

import (
	"path/filepath"
	"strings"
	"testing"
)

// A pre-pre-push hook's output goes to safegit's STDERR, whichever stream the
// hook wrote it to: safegit's stdout is a structured channel, carrying exactly
// one JSON envelope in machine mode, and an operator-supplied script must not be
// able to write into it. These tests run a hook that prints to its stdout under
// --json and require the whole of safegit's stdout to parse as one document,
// with the hook's line on stderr instead.

const hookStdoutLine = "hook-line-on-its-stdout"

// installStdoutHook installs a pre-pre-push hook that prints hookStdoutLine to
// its own stdout and exits 0.
func installStdoutHook(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "pre-pre-push")
	writeHookScript(t, src, "echo "+hookStdoutLine)
	if _, stderr, code := runSafegit(t, dir, "hook", "install", src); code != 0 {
		t.Fatalf("hook install failed (code %d): %s", code, stderr)
	}
}

// assertHookOutputRouted checks one machine-mode run: stdout is exactly one JSON
// document and does not carry the hook's line, and stderr does.
func assertHookOutputRouted(t *testing.T, stdout, stderr string) {
	t.Helper()
	if err := oneJSONDocument(stdout); err != nil {
		t.Errorf("stdout is not exactly one JSON document (%v):\n%s", err, stdout)
	}
	if strings.Contains(stdout, hookStdoutLine) {
		t.Errorf("the hook's stdout reached safegit's stdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, hookStdoutLine) {
		t.Errorf("the hook's stdout line is not on safegit's stderr:\n%s", stderr)
	}
}

func TestPushHookStdoutGoesToStderrInMachineMode(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	installStdoutHook(t, dir)

	stdout, stderr, code := runSafegit(t, dir, "--json", "push", "--refs", "head", "origin")
	if code != 0 {
		t.Fatalf("push --json failed (code %d): %s", code, stderr)
	}
	assertHookOutputRouted(t, stdout, stderr)
}

func TestHookRunStdoutGoesToStderrInMachineMode(t *testing.T) {
	dir := newRepo(t)
	installStdoutHook(t, dir)

	for _, args := range [][]string{
		{"--json", "hook", "run"},
		{"--json", "hook", "run", "pre-pre-push"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, code := runSafegit(t, dir, args...)
			if code != 0 {
				t.Fatalf("%v failed (code %d): %s", args, code, stderr)
			}
			assertHookOutputRouted(t, stdout, stderr)
		})
	}
}
