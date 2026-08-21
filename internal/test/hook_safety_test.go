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
//     metadata from this repository entirely", but repo.Uninstall deletes only
//     .git/safegit/ and the shared lock directory. A hook safegit itself
//     installed survives the uninstall and keeps running on every push.
//  3. `safegit scan` sweeps .git/hooks/* non-recursively and skips directories,
//     so a secret inside a hook under .git/hooks/pre-pre-push.d/ escapes the
//     scan that covers its siblings one directory up -- even though hook
//     discovery treats both locations as hooks.
//
// The tests assert the observable contract, not an implementation: an install
// that REFUSES the colliding basename satisfies (1) exactly as well as one that
// renames or namespaces it, and (3) is satisfied by any sweep that reaches the
// discovery set, wherever that set lives.

var hookSafetyEnv = []string{"CLAUDE_CODE_SESSION_ID=hook-safety-test"}

// hookSafetyWriteScript writes an executable /bin/sh script at path (creating
// parent directories) whose body is the given lines.
func hookSafetyWriteScript(t *testing.T, path string, body string) {
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
// there. `hook install` may refuse the collision, or install somewhere that
// does not collide -- what it must not do is overwrite the operator's existing
// .git/hooks/pre-commit and report success.
//
// The collision is not hypothetical: safegit's own commit pipeline executes
// .git/hooks/pre-commit (internal/commit/commit.go runPreCommitHook), so the
// clobbered file is one safegit itself depends on.
func TestHookInstallDoesNotClobberNativeGitHook(t *testing.T) {
	dir := newRepo(t)

	nativePath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	nativeMarker := filepath.Join(dir, "native-pre-commit-ran.txt")
	hookSafetyWriteScript(t, nativePath, "printf native > "+nativeMarker)
	nativeBefore, err := hookSafetyRead(t, nativePath)
	if err != nil {
		t.Fatalf("reading the native hook we just wrote: %v", err)
	}

	// A safegit hook source that happens to carry the same basename.
	srcPath := filepath.Join(dir, "hooksrc", "pre-commit")
	srcMarker := filepath.Join(dir, "installed-hook-ran.txt")
	hookSafetyWriteScript(t, srcPath, "printf installed > "+srcMarker)
	srcBody, err := hookSafetyRead(t, srcPath)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv, "hook", "install", srcPath)
	if code != 0 {
		// Acceptable outcome: install refuses to take over a native hook name.
		// It must say so rather than failing for an unrelated reason.
		if stderr == "" {
			t.Fatalf("hook install failed with exit %d but printed no explanation", code)
		}
		t.Logf("hook install refused the colliding basename (exit %d): %s", code, stderr)
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

	// Evidence of the consequence, not a second assertion: whichever script now
	// occupies .git/hooks/pre-commit is the one safegit runs when it commits.
	if err := os.WriteFile(filepath.Join(dir, "probe.txt"), []byte("probe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, cErr, cCode := runSafegitEnv(t, dir, hookSafetyEnv, "commit", "-m", "probe", "--", "probe.txt"); cCode != 0 {
		t.Logf("probe commit exited %d: %s", cCode, cErr)
	}
	_, nativeRan := hookSafetyRead(t, nativeMarker)
	_, installedRan := hookSafetyRead(t, srcMarker)
	t.Logf("after a safegit commit: native hook ran=%v, installed hook ran=%v",
		nativeRan == nil, installedRan == nil)
}

// TestHookInstallLeavesUnrelatedNativeHookAlone is the control: installing a
// source whose basename does NOT collide with a native hook must leave that
// native hook untouched. It isolates the defect above to the basename
// collision, proving install is not simply destroying the hooks directory.
func TestHookInstallLeavesUnrelatedNativeHookAlone(t *testing.T) {
	dir := newRepo(t)

	nativePath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	hookSafetyWriteScript(t, nativePath, "true")
	nativeBefore, err := hookSafetyRead(t, nativePath)
	if err != nil {
		t.Fatal(err)
	}

	srcPath := filepath.Join(dir, "hooksrc", "pre-pre-push")
	hookSafetyWriteScript(t, srcPath, "true")

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
// The manually-placed pre-pre-push.d entry below is deliberately NOT asserted
// on: an operator-authored script raises an authority question (whose file is
// it to delete?) that this contract does not settle. Its fate is logged as
// evidence for that separate decision.
func TestDoctorUninstallRemovesInstalledHooks(t *testing.T) {
	dir := newRepo(t)

	srcPath := filepath.Join(dir, "hooksrc", "pre-pre-push")
	hookSafetyWriteScript(t, srcPath, "true")
	if _, stderr, code := runSafegitEnv(t, dir, hookSafetyEnv, "hook", "install", srcPath); code != 0 {
		t.Fatalf("hook install failed (code %d): %s", code, stderr)
	}

	installedPath := filepath.Join(dir, ".git", "hooks", "pre-pre-push")
	if _, err := os.Stat(installedPath); err != nil {
		t.Fatalf("precondition: hook install did not produce %s: %v", installedPath, err)
	}

	// An operator-authored hook alongside it, for evidence only.
	operatorPath := filepath.Join(dir, ".git", "hooks", "pre-pre-push.d", "20-operator")
	hookSafetyWriteScript(t, operatorPath, "true")

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

	_, operatorErr := os.Stat(operatorPath)
	t.Logf("operator-authored %s after uninstall: present=%v", operatorPath, operatorErr == nil)
}

// TestScanSeesHooksInPrePrePushDir: `safegit scan` sweeps non-object files
// including .git/hooks, but internal/scan/nonobject.go ScanNonObjects skips
// directory entries, so a hook under .git/hooks/pre-pre-push.d/ is never read.
// Both locations are hooks as far as hook discovery is concerned
// (internal/hooks/hooks.go Discover reads exactly these two places), so a
// secret in one is as reachable as a secret in the other and the scan must see
// both.
func TestScanSeesHooksInPrePrePushDir(t *testing.T) {
	dir := newRepo(t)

	const secret = "HOOKSAFETY_LEAK_TOKEN_NESTED"
	leakyPath := filepath.Join(dir, ".git", "hooks", "pre-pre-push.d", "leaky.sh")
	hookSafetyWriteScript(t, leakyPath, "TOKEN="+secret+"; export TOKEN")

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
	hookSafetyWriteScript(t, hookPath, "TOKEN="+secret+"; export TOKEN")

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
