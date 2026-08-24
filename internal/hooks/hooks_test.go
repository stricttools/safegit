package hooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupGitDir creates a temporary .git structure with git's own hooks directory
// and safegit's tool-owned live hook store.
func setupGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "hooks"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(LocalDir(gitDir), 0755); err != nil {
		t.Fatal(err)
	}
	return gitDir
}

// store is the Store for a git dir whose work tree is its parent directory,
// which is the shape setupGitDir builds.
func store(gitDir string) Store {
	return Store{Worktree: filepath.Dir(gitDir), SharedGitDir: gitDir}
}

// writeHook writes an executable script to the given path.
func writeHook(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSingleHook(t *testing.T) {
	gitDir := setupGitDir(t)
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	writeHook(t, hookPath, "#!/bin/sh\nexit 0\n")

	hooks, err := Discover(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hooks))
	}
	if hooks[0] != hookPath {
		t.Fatalf("expected %s, got %s", hookPath, hooks[0])
	}
}

func TestDiscoverDirectory(t *testing.T) {
	gitDir := setupGitDir(t)
	dDir := filepath.Join(LocalDir(gitDir), "pre-pre-push.d")
	if err := os.MkdirAll(dDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write hooks in non-lexical order to verify sorting
	writeHook(t, filepath.Join(dDir, "02-lint"), "#!/bin/sh\nexit 0\n")
	writeHook(t, filepath.Join(dDir, "01-test"), "#!/bin/sh\nexit 0\n")
	writeHook(t, filepath.Join(dDir, "03-build"), "#!/bin/sh\nexit 0\n")

	hooks, err := Discover(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 3 {
		t.Fatalf("expected 3 hooks, got %d", len(hooks))
	}
	// Verify lexical order
	if filepath.Base(hooks[0]) != "01-test" {
		t.Errorf("first hook should be 01-test, got %s", filepath.Base(hooks[0]))
	}
	if filepath.Base(hooks[1]) != "02-lint" {
		t.Errorf("second hook should be 02-lint, got %s", filepath.Base(hooks[1]))
	}
	if filepath.Base(hooks[2]) != "03-build" {
		t.Errorf("third hook should be 03-build, got %s", filepath.Base(hooks[2]))
	}
}

// TestRefuseNonExecutableLocalHook: a missing execute bit in the live store is
// a refusal, not a filter. Discovery answers for the whole set, so the healthy
// hook standing beside the offender is not handed back either -- running half an
// operator's checks would be worse than running none of them and saying so.
func TestRefuseNonExecutableLocalHook(t *testing.T) {
	gitDir := setupGitDir(t)
	dDir := filepath.Join(LocalDir(gitDir), "pre-pre-push.d")
	if err := os.MkdirAll(dDir, 0755); err != nil {
		t.Fatal(err)
	}

	// One executable, one not
	writeHook(t, filepath.Join(dDir, "01-good"), "#!/bin/sh\nexit 0\n")
	// Write non-executable file
	bad := filepath.Join(dDir, "02-bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := Discover(store(gitDir))
	var local *LocalNotExecutableError
	if !errors.As(err, &local) {
		t.Fatalf("Discover() error = %v, want a *LocalNotExecutableError", err)
	}
	if len(hooks) != 0 {
		t.Errorf("a refusing discovery handed back %d hook(s): %v", len(hooks), hooks)
	}
	if len(local.Paths) != 1 || local.Paths[0] != bad {
		t.Errorf("the refusal names %v, want just %s", local.Paths, bad)
	}
	if !strings.Contains(local.Error(), "chmod") {
		t.Errorf("the refusal must state the chmod remedy: %s", local.Error())
	}
}

func TestSkipDotFiles(t *testing.T) {
	gitDir := setupGitDir(t)
	dDir := filepath.Join(LocalDir(gitDir), "pre-pre-push.d")
	if err := os.MkdirAll(dDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeHook(t, filepath.Join(dDir, ".hidden"), "#!/bin/sh\nexit 0\n")
	writeHook(t, filepath.Join(dDir, "backup~"), "#!/bin/sh\nexit 0\n")
	writeHook(t, filepath.Join(dDir, "good-hook"), "#!/bin/sh\nexit 0\n")

	hooks, err := Discover(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook (skip . and ~ files), got %d", len(hooks))
	}
	if filepath.Base(hooks[0]) != "good-hook" {
		t.Errorf("expected good-hook, got %s", filepath.Base(hooks[0]))
	}
}

func TestRunSuccess(t *testing.T) {
	gitDir := setupGitDir(t)
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	writeHook(t, hookPath, "#!/bin/sh\necho running\nexit 0\n")

	ctx := context.Background()
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc123 refs/heads/main def456\n"), 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ExitCode != 0 {
		t.Errorf("expected exit code 0, got %d", results[0].ExitCode)
	}
	if results[0].TimedOut {
		t.Error("should not have timed out")
	}
}

func TestRunFailure(t *testing.T) {
	gitDir := setupGitDir(t)
	dDir := filepath.Join(LocalDir(gitDir), "pre-pre-push.d")
	if err := os.MkdirAll(dDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeHook(t, filepath.Join(dDir, "01-fail"), "#!/bin/sh\nexit 1\n")
	writeHook(t, filepath.Join(dDir, "02-never"), "#!/bin/sh\nexit 0\n")

	ctx := context.Background()
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc123 refs/heads/main def456\n"), 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Should abort after first failure -- only one result
	if len(results) != 1 {
		t.Fatalf("expected 1 result (abort on failure), got %d", len(results))
	}
	if results[0].ExitCode != 1 {
		t.Errorf("expected exit code 1, got %d", results[0].ExitCode)
	}
}

func TestRunTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	gitDir := setupGitDir(t)
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	// Hook that sleeps indefinitely (well, 60s -- longer than our timeout)
	writeHook(t, hookPath, "#!/bin/sh\nsleep 60\n")

	ctx := context.Background()
	start := time.Now()
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc123 refs/heads/main def456\n"), 1, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].TimedOut {
		t.Error("expected hook to time out")
	}
	if results[0].ExitCode != 21 {
		t.Errorf("expected exit code 21, got %d", results[0].ExitCode)
	}
	// Should complete within timeout + grace + some slack (1s + 5s + 2s margin)
	if elapsed > 8*time.Second {
		t.Errorf("timeout took too long: %v", elapsed)
	}
}

func TestSetOutputCapturesHookOutput(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	restore := SetOutput(&outBuf, &errBuf)
	defer restore()

	gitDir := setupGitDir(t)
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	writeHook(t, hookPath, "#!/bin/sh\necho hello-from-hook\necho oops >&2\n")

	ctx := context.Background()
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc def456\n"), 30, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", results)
	}

	if got := outBuf.String(); !strings.Contains(got, "hello-from-hook") {
		t.Errorf("expected stdout to contain 'hello-from-hook', got %q", got)
	}
	if got := errBuf.String(); !strings.Contains(got, "oops") {
		t.Errorf("expected stderr to contain 'oops', got %q", got)
	}
}

// TestDiscoverWritesNoWarningOfItsOwn: discovery states its verdict by
// RETURNING it. The non-executable local hook used to produce a `skipping`
// warning on the package's stderr and then let the push proceed; it is a typed
// refusal now, and a refusal a caller maps to an exit code must not also be
// half-announced behind that caller's back.
func TestDiscoverWritesNoWarningOfItsOwn(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	restore := SetOutput(&outBuf, &errBuf)
	defer restore()

	gitDir := setupGitDir(t)
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := Discover(store(gitDir))
	var local *LocalNotExecutableError
	if !errors.As(err, &local) {
		t.Fatalf("Discover() error = %v, want a *LocalNotExecutableError", err)
	}
	if len(hooks) != 0 {
		t.Fatalf("expected 0 hooks, got %d", len(hooks))
	}
	if !strings.Contains(local.Error(), hookPath) {
		t.Errorf("the refusal must name the offending hook, got %q", local.Error())
	}

	if got := outBuf.String() + errBuf.String(); got != "" {
		t.Errorf("discovery wrote %q; its answer is the returned error, not output", got)
	}
}
