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
// The resolution is settled, so the tests assert it directly: the store is a
// DIRECTORY that discovery walks whole, at any depth, and a hook is addressed
// by its store-relative name. Every basename is therefore installable and
// discoverable, and an install that reported success must produce a hook that
// `hook list` names and `hook run` executes.

// TestHookInstallArbitraryBasenameIsDiscoverable: `hook install my-check.sh`
// installs a hook under that name and it runs. Reporting success while
// installing something no code path can ever see was the defect; the store
// walking whole is what makes the name a non-question.
func TestHookInstallArbitraryBasenameIsDiscoverable(t *testing.T) {
	dir := newRepo(t)

	src := filepath.Join(dir, "hooksrc", "my-check.sh")
	marker := filepath.Join(dir, "my-check-ran.txt")
	writeHookScript(t, src, "printf ran > "+marker)

	stdout, stderr, code := runSafegit(t, dir, "hook", "install", src)
	if code != 0 {
		t.Fatalf("hook install refused an ordinary basename (exit %d): %s", code, stderr)
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
