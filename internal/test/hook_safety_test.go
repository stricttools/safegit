package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Three properties of the hook subsystem that nothing currently tests, each
// asserted here as the contract rather than as any particular fix:
//
//  1. `hook install` writes the SOURCE's basename into .git/hooks, so a script
//     named `pre-commit` silently replaces the repository's real native git
//     hook -- the same file safegit itself executes before every commit
//     (internal/commit/commit.go runPreCommitHook). Installing a safegit hook
//     must never destroy an unrelated native hook.
//  2. `doctor --action uninstall` promises to "remove all safegit hooks and
//     metadata from this repository entirely", but the uninstall deleted only
//     .git/safegit/ and the shared lock directory. A hook safegit itself
//     installed survived the uninstall and kept running on every push.
//  3. `safegit scan` sweeps the hook directory non-recursively and skips
//     directory entries, so a secret inside a hook under `pre-pre-push.d/`
//     escapes the scan that covers its siblings one directory up -- even though
//     hook discovery treats both locations as hooks.
//
// All three are resolved by one move -- safegit's hooks into its own store --
// and the tests now assert that resolution: (1) the basenames cannot collide,
// so the install succeeds and git's hook is untouched; (2) uninstall takes the
// store with the rest of .git/safegit; (3) the sweep reaches the whole
// discovery set at any depth.

var hookSafetyEnv = []string{"CLAUDE_CODE_SESSION_ID=hook-safety-test"}

// writeHookScript writes an executable /bin/sh script at path (creating parent
// directories) whose body is the given lines, followed by `exit 0`. Both hook
// suites in this package write their fixtures through it; a script that has to
// leave evidence behind passes a body like `printf ran > <absolute marker>`,
// absolute because a hook runs with its own directory as the working directory.
func writeHookScript(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// hookSafetyRead returns a file's contents, or "" plus the error when absent.
func hookSafetyRead(t *testing.T, path string) (string, error) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// hookSafetyFileMatchPaths runs `safegit --json scan --pattern <pattern>` and
// returns the `path` of every non-object (file) match.
func hookSafetyFileMatchPaths(t *testing.T, dir, pattern string) []string {
	t.Helper()
	stdout, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv, "--json", "scan", "--pattern", pattern)
	if code != 0 {
		t.Fatalf("scan failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	var result struct {
		FileMatches []struct {
			Path string `json:"path"`
		} `json:"file_matches"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &result); err != nil {
		t.Fatalf("failed to parse scan JSON: %v\nraw: %s", err, stdout)
	}
	paths := make([]string, 0, len(result.FileMatches))
	for _, m := range result.FileMatches {
		paths = append(paths, m.Path)
	}
	return paths
}

// TestHookInstallDoesNotClobberNativeGitHook: installing a script whose
// basename collides with a native git hook must not destroy the hook already
// there. The resolution is settled and it is the store move: safegit's hooks
// live in .git/safegit/hooks, git's live in .git/hooks, so the basename cannot
// collide at all and the install SUCCEEDS while the native hook is untouched.
//
// The collision was not hypothetical: safegit's own commit pipeline executes
// git's pre-commit hook (internal/commit/hooks.go), so the clobbered file was
// one safegit itself depends on.
func TestHookInstallDoesNotClobberNativeGitHook(t *testing.T) {
	dir := newRepo(t)

	nativePath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	nativeMarker := filepath.Join(dir, "native-pre-commit-ran.txt")
	writeHookScript(t, nativePath, "printf native > "+nativeMarker)
	nativeBefore, err := hookSafetyRead(t, nativePath)
	if err != nil {
		t.Fatalf("reading the native hook we just wrote: %v", err)
	}

	// A safegit hook source that happens to carry the same basename.
	srcPath := filepath.Join(dir, "hooksrc", "pre-commit")
	srcMarker := filepath.Join(dir, "installed-hook-ran.txt")
	writeHookScript(t, srcPath, "printf installed > "+srcMarker)
	srcBody, err := hookSafetyRead(t, srcPath)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv, "hook", "install", srcPath)
	if code != 0 {
		t.Fatalf("hook install refused a basename that no longer collides with anything (exit %d): %s", code, stderr)
	}
	// The installed copy is safegit's own, in safegit's own store.
	installed := filepath.Join(dir, ".git", "safegit", "hooks", "pre-commit")
	if body, err := hookSafetyRead(t, installed); err != nil {
		t.Errorf("install did not write %s: %v", installed, err)
	} else if body != srcBody {
		t.Errorf("the installed hook is not the source script:\nwant:\n%s\ngot:\n%s", srcBody, body)
	}

	nativeAfter, readErr := hookSafetyRead(t, nativePath)
	switch {
	case readErr != nil:
		t.Errorf("hook install (exit %d) deleted the repository's native pre-commit hook %s: %v",
			code, nativePath, readErr)
	case nativeAfter != nativeBefore:
		replacedWith := "unrecognized content"
		if nativeAfter == srcBody {
			replacedWith = "the verbatim contents of the installed source script"
		}
		t.Errorf("hook install (exit %d, stdout %q) overwrote the repository's native pre-commit hook with %s.\n"+
			"before:\n%s\nafter:\n%s",
			code, strings.TrimSpace(stdout), replacedWith, nativeBefore, nativeAfter)
	}

	// The consequence, asserted rather than logged: whichever script occupies
	// .git/hooks/pre-commit is the one safegit runs when it commits, and after
	// the store move that is still the REPOSITORY'S own hook. The installed
	// script is a safegit hook in safegit's own store and runs on a push, so a
	// commit must not reach it -- if it did, the basenames would have collided
	// after all, just one directory further along.
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, cErr, cCode := runSafegitEnv(t, dir, hookSafetyEnv, "commit", "-m", "probe", "--", "probe.txt"); cCode != 0 {
		t.Fatalf("the probe commit failed (exit %d): %s", cCode, cErr)
	}
	if body, err := hookSafetyRead(t, nativeMarker); err != nil {
		t.Errorf("the repository's own pre-commit hook did not run on a safegit commit: %v", err)
	} else if body != "native" {
		t.Errorf("the pre-commit marker holds %q, want %q -- some other script is at .git/hooks/pre-commit", body, "native")
	}
	if _, err := hookSafetyRead(t, srcMarker); err == nil {
		t.Error("the installed safegit hook ran on a commit; it belongs to the push path only")
	}
}

// TestHookInstallLeavesUnrelatedNativeHookAlone is the control: installing a
// source whose basename does NOT collide with a native hook must leave that
// native hook untouched. It isolates the defect above to the basename
// collision, proving install is not simply destroying the hooks directory.
func TestHookInstallLeavesUnrelatedNativeHookAlone(t *testing.T) {
	dir := newRepo(t)

	nativePath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	writeHookScript(t, nativePath, "true")
	nativeBefore, err := hookSafetyRead(t, nativePath)
	if err != nil {
		t.Fatal(err)
	}

	srcPath := filepath.Join(dir, "hooksrc", "pre-pre-push")
	writeHookScript(t, srcPath, "true")

	if _, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv, "hook", "install", srcPath); code != 0 {
		t.Fatalf("hook install failed (code %d): %s", code, stderr)
	}

	nativeAfter, err := hookSafetyRead(t, nativePath)
	if err != nil {
		t.Fatalf("native pre-commit hook disappeared: %v", err)
	}
	if nativeAfter != nativeBefore {
		t.Errorf("installing a non-colliding hook modified the native pre-commit hook.\nbefore:\n%s\nafter:\n%s",
			nativeBefore, nativeAfter)
	}
}

// TestDoctorUninstallRemovesInstalledHooks: `doctor --action uninstall`
// declares it will "remove all safegit hooks and metadata from this repository
// entirely" (the --action choice's own help text). A hook that safegit itself
// installed is unambiguously safegit's to remove -- leaving it behind means the
// uninstalled tool's checks keep running on every push, with no .git/safegit/
// left to explain where they came from.
//
// The manually-placed pre-pre-push.d entry below settles the question that used
// to be left open here -- whose file is an operator-authored script under
// safegit's store to delete? The answer follows from what an uninstall IS: a
// repository-wide removal of safegit's state directory, and everything inside
// it goes with it. The store is safegit's; a script an operator put in it is a
// script they asked safegit to run, and leaving it behind would leave a hook
// directory with nothing left to explain it. The one store an uninstall does
// NOT touch is the CHECKOUT's (.safegit/hooks in the work tree), which is the
// repository's committed content and somebody else's file to delete.
func TestDoctorUninstallRemovesInstalledHooks(t *testing.T) {
	dir := newRepo(t)

	srcPath := filepath.Join(dir, "hooksrc", "pre-pre-push")
	writeHookScript(t, srcPath, "true")
	if _, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv, "hook", "install", srcPath); code != 0 {
		t.Fatalf("hook install failed (code %d): %s", code, stderr)
	}

	installedPath := filepath.Join(dir, ".git", "safegit", "hooks", "pre-pre-push")
	if _, err := os.Stat(installedPath); err != nil {
		t.Fatalf("precondition: hook install did not produce %s: %v", installedPath, err)
	}

	// An operator-authored hook alongside it, inside safegit's own store.
	operatorPath := filepath.Join(dir, ".git", "safegit", "hooks", "pre-pre-push.d", "20-operator")
	writeHookScript(t, operatorPath, "true")

	stdout, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv,
		"doctor", "--action", "uninstall", "--approve-consequential")
	if code != 0 {
		t.Fatalf("doctor --action uninstall failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Control half of the same command: the metadata directory does go away.
	safegitDir := filepath.Join(dir, ".git", "safegit")
	if _, err := os.Stat(safegitDir); !os.IsNotExist(err) {
		t.Errorf("uninstall left %s behind (err=%v)", safegitDir, err)
	}

	if _, err := os.Stat(installedPath); err == nil {
		body, _ := hookSafetyRead(t, installedPath)
		t.Errorf("doctor --action uninstall (stdout %q) left the safegit-installed hook %s in place; it still runs on every push.\ncontents:\n%s",
			strings.TrimSpace(stdout), installedPath, body)
	} else if !os.IsNotExist(err) {
		t.Errorf("stat %s: %v", installedPath, err)
	}

	// The operator-authored entry goes too: it lives inside safegit's own store,
	// and the uninstall removes that store whole.
	if _, err := os.Stat(operatorPath); err == nil {
		t.Errorf("uninstall left the operator-authored hook %s inside safegit's store; it still runs on every push", operatorPath)
	} else if !os.IsNotExist(err) {
		t.Errorf("stat %s: %v", operatorPath, err)
	}
}

// TestScanSeesHooksInPrePrePushDir: `safegit scan` sweeps non-object files
// including the hook store, but internal/scan/nonobject.go ScanNonObjects
// skipped directory entries, so a hook under `pre-pre-push.d/` was never read.
// Both locations are hooks as far as hook discovery is concerned, so a secret
// in one is as reachable as a secret in the other and the scan must see both.
func TestScanSeesHooksInPrePrePushDir(t *testing.T) {
	dir := newRepo(t)

	const secret = "HOOKSAFETY_LEAK_TOKEN_NESTED"
	leakyPath := filepath.Join(dir, ".git", "safegit", "hooks", "pre-pre-push.d", "leaky.sh")
	writeHookScript(t, leakyPath, "TOKEN="+secret+"; export TOKEN")

	paths := hookSafetyFileMatchPaths(t, dir, secret)
	found := false
	for _, p := range paths {
		if strings.Contains(p, "leaky.sh") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("scan missed the secret in %s (a hook safegit discovers and runs); file matches: %v",
			leakyPath, paths)
	}
}

// TestScanSeesTopLevelPrePrePushHook is the control: the same secret in the
// single-file hook one directory up IS found, isolating the miss above to the
// directory-based half of the hook discovery set.
func TestScanSeesTopLevelPrePrePushHook(t *testing.T) {
	dir := newRepo(t)

	const secret = "HOOKSAFETY_LEAK_TOKEN_TOPLEVEL"
	hookPath := filepath.Join(dir, ".git", "hooks", "pre-pre-push")
	writeHookScript(t, hookPath, "TOKEN="+secret+"; export TOKEN")

	paths := hookSafetyFileMatchPaths(t, dir, secret)
	found := false
	for _, p := range paths {
		if strings.Contains(p, "pre-pre-push") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("scan missed the secret in the top-level hook %s; file matches: %v", hookPath, paths)
	}
}
