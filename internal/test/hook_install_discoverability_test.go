package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hook subsystem has two halves that must agree: `hook install` decides
// where a script is written, and hook discovery decides which paths are hooks.
// Today install writes the source's own basename into the hooks directory while
// discovery recognizes only `pre-pre-push` and entries under `pre-pre-push.d/`,
// so installing `my-check.sh` reports success and produces a file that `hook
// list` never lists and `hook run`/`push` never execute -- a silent no-op.
//
// The tests below assert the contract rather than either fix: after a
// SUCCESSFUL install the script must be discoverable and runnable. An install
// that refuses an unusable basename is equally acceptable, so a hard error is
// accepted as long as it is a real nonzero exit with an explanation. What is
// not acceptable is exit 0 plus an invisible file.
//
// The tests deliberately assert nothing about WHICH directory the hook lands
// in: whether the hooks area stays at .git/hooks or moves under .git/safegit is
// a separate open question, and this contract holds either way.

// TestHookInstallArbitraryBasenameIsDiscoverable: `hook install my-check.sh`
// must either make the script discoverable (list names it, run executes it) or
// refuse outright. Reporting success while installing something no code path
// can ever see is the defect.
func TestHookInstallArbitraryBasenameIsDiscoverable(t *testing.T) {
	dir := newRepo(t)

	src := filepath.Join(dir, "hooksrc", "my-check.sh")
	marker := filepath.Join(dir, "my-check-ran.txt")
	writeHookScript(t, src, "printf ran > "+marker)

	stdout, stderr, code := runSafegit(t, dir, "hook", "install", src)
	if code != 0 {
		// Acceptable outcome: install refuses a basename discovery cannot see.
		// It must say so rather than failing for some unrelated reason.
		if stderr == "" {
			t.Fatalf("hook install failed with exit %d but printed no explanation", code)
		}
		t.Logf("hook install refused the source basename (exit %d): %s", code, stderr)
		return
	}

	// Install reported success, so the hook must actually be a hook.
	listOut, listErr, listCode := runSafegit(t, dir, "hook", "list")
	if listCode != 0 {
		t.Fatalf("hook list failed (code %d): %s", listCode, listErr)
	}
	if !strings.Contains(listOut, "my-check.sh") {
		t.Errorf("hook install reported success (%q) but hook list does not show the hook.\nlist stdout:\n%s",
			strings.TrimSpace(stdout), listOut)
	}

	runOut, runErr, runCode := runSafegit(t, dir, "hook", "run")
	if runCode != 0 {
		t.Fatalf("hook run failed (code %d): %s", runCode, runErr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("hook install reported success but hook run never executed the hook (marker %s absent: %v).\nrun stdout:\n%s",
			marker, err, runOut)
	}
}

// TestHookInstallDiscoverableBasenameControl pins the other side of the same
// contract: a source already named `pre-pre-push` installs into the
// discoverable namespace and runs. It isolates the defect above to the
// basename, proving the install/list/run machinery itself works.
func TestHookInstallDiscoverableBasenameControl(t *testing.T) {
	dir := newRepo(t)

	src := filepath.Join(dir, "hooksrc", "pre-pre-push")
	marker := filepath.Join(dir, "pre-pre-push-ran.txt")
	writeHookScript(t, src, "printf ran > "+marker)

	_, stderr, code := runSafegit(t, dir, "hook", "install", src)
	if code != 0 {
		t.Fatalf("hook install failed (code %d): %s", code, stderr)
	}

	listOut, listErr, listCode := runSafegit(t, dir, "hook", "list")
	if listCode != 0 {
		t.Fatalf("hook list failed (code %d): %s", listCode, listErr)
	}
	if !strings.Contains(listOut, "pre-pre-push") {
		t.Errorf("hook list does not show the installed pre-pre-push hook:\n%s", listOut)
	}

	_, runErr, runCode := runSafegit(t, dir, "hook", "run")
	if runCode != 0 {
		t.Fatalf("hook run failed (code %d): %s", runCode, runErr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("hook run did not execute the installed pre-pre-push hook (marker %s absent: %v)", marker, err)
	}
}
