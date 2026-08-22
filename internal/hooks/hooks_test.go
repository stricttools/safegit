package hooks

import (
	"bytes"
	"context"
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

func TestSkipNonExecutable(t *testing.T) {
	gitDir := setupGitDir(t)
	dDir := filepath.Join(LocalDir(gitDir), "pre-pre-push.d")
	if err := os.MkdirAll(dDir, 0755); err != nil {
		t.Fatal(err)
	}

	// One executable, one not
	writeHook(t, filepath.Join(dDir, "01-good"), "#!/bin/sh\nexit 0\n")
	// Write non-executable file
	if err := os.WriteFile(filepath.Join(dDir, "02-bad"), []byte("#!/bin/sh\nexit 0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := Discover(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook (skip non-executable), got %d", len(hooks))
	}
	if filepath.Base(hooks[0]) != "01-good" {
		t.Errorf("expected 01-good, got %s", filepath.Base(hooks[0]))
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

func TestParseTimeoutOverride(t *testing.T) {
	tests := []struct {
		line string
		want int
	}{
		{"# safegit: timeout=60", 60},
		{"# safegit: timeout=300\n", 300},
		{"# safegit: timeout=0", 0},
		{"# safegit: timeout=-1", 0},
		{"# safegit: timeout=abc", 0},
		{"not a directive", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := parseTimeoutOverride(tt.line)
		if got != tt.want {
			t.Errorf("parseTimeoutOverride(%q) = %d, want %d", tt.line, got, tt.want)
		}
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

func TestSetOutputCapturesDiscoverWarning(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	restore := SetOutput(&outBuf, &errBuf)
	defer restore()

	gitDir := setupGitDir(t)
	// Write a non-executable hook to trigger the warning
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0644); err != nil {
		t.Fatal(err)
	}

	hooks, err := Discover(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(hooks) != 0 {
		t.Fatalf("expected 0 hooks, got %d", len(hooks))
	}

	if got := errBuf.String(); !strings.Contains(got, "not executable") {
		t.Errorf("expected stderr warning about non-executable hook, got %q", got)
	}
}
