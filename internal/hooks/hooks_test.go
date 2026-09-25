package hooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc123 refs/heads/main def456\n"), 30, DefaultStopCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !exitedWith(results[0], 0) {
		t.Errorf("expected exit code 0, got %v", results[0].ExitCode)
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
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc123 refs/heads/main def456\n"), 30, DefaultStopCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Should abort after first failure -- only one result
	if len(results) != 1 {
		t.Fatalf("expected 1 result (abort on failure), got %d", len(results))
	}
	if !exitedWith(results[0], 1) {
		t.Errorf("expected exit code 1, got %v", results[0].ExitCode)
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
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc123 refs/heads/main def456\n"), 1, DefaultStopCap, nil)
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
	if results[0].ExitCode != nil {
		t.Errorf("a hook the timeout killed has no exit status, got %d", *results[0].ExitCode)
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
	results, err := Run(ctx, store(gitDir), []byte("refs/heads/main abc def456\n"), 30, DefaultStopCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !exitedWith(results[0], 0) {
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

// TestSlowReaderLosesNoHookOutput: a hook that prints and exits at once must
// have every byte forwarded, however late the reader gets to the pipe. The
// process finishing is not the output finishing: the read ends are safegit's
// own pipes rather than os/exec's, so waiting on the process closes nothing,
// and the output is read to its end before the hook counts as finished.
func TestSlowReaderLosesNoHookOutput(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	restore := SetOutput(&outBuf, &errBuf)
	defer restore()

	prev := beforeStdoutRead
	beforeStdoutRead = func() { time.Sleep(500 * time.Millisecond) }
	defer func() { beforeStdoutRead = prev }()

	gitDir := setupGitDir(t)
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	writeHook(t, hookPath, "#!/bin/sh\necho hello-from-hook\n")

	results, err := Run(context.Background(), store(gitDir), nil, 30, DefaultStopCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !exitedWith(results[0], 0) {
		t.Fatalf("unexpected result: %+v", results)
	}
	if got := outBuf.String(); got != "hello-from-hook\n" {
		t.Errorf("hook stdout = %q, want %q", got, "hello-from-hook\n")
	}
}

// TestHookWhoseBackgroundChildHoldsStdoutFailsWithoutWaiting: a hook that exits
// while a background child it started still runs, holding its stdout, has left
// a process behind. That is a failed run the moment the hook ends -- not a hook
// that keeps running until the child exits or the timeout fires: the child is
// named and stopped, which is what ends the output.
func TestHookWhoseBackgroundChildHoldsStdoutFailsWithoutWaiting(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	restore := SetOutput(&outBuf, &errBuf)
	defer restore()

	gitDir := setupGitDir(t)
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	hookPath := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	writeHook(t, hookPath, "#!/bin/sh\nsleep 60 2>/dev/null &\necho $! > '"+pidFile+"'\necho started\nexit 0\n")

	start := time.Now()
	results, err := Run(context.Background(), store(gitDir), nil, 30, DefaultStopCap, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	pid := readPid(t, pidFile)
	defer syscall.Kill(pid, syscall.SIGKILL)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.TimedOut || !r.Failed() {
		t.Errorf("a hook that left a child running must fail on its own, not time out: %+v", r)
	}
	if len(r.Leftovers) != 1 || r.Leftovers[0].PID != pid || !r.Leftovers[0].Killed {
		t.Errorf("the result must name the background child %d as killed: %+v", pid, r.Leftovers)
	}
	if elapsed > 10*time.Second {
		t.Errorf("the run took %v; a leftover must be stopped, not waited for", elapsed)
	}
	if got := outBuf.String(); got != "started\n" {
		t.Errorf("hook stdout = %q, want %q", got, "started\n")
	}
	if !waitGone(pid, 5*time.Second) {
		t.Errorf("the background child %d is still running", pid)
	}
}

// slowWriter counts what it is given and takes its time over every write, like
// a destination that drains slowly.
type slowWriter struct {
	n     int
	delay time.Duration
}

func (w *slowWriter) Write(b []byte) (int, error) {
	time.Sleep(w.delay)
	w.n += len(b)
	return len(b), nil
}

// TestSlowDestinationLosesNoHookOutput: once the hook has ended, the output
// still in the pipe is forwarded in full however slowly the destination takes
// it. Only a pipe nobody is writing to and nobody has closed -- a process safegit
// could not reach holding it -- is given up on, never a slow reader.
func TestSlowDestinationLosesNoHookOutput(t *testing.T) {
	out := &slowWriter{delay: 400 * time.Millisecond}
	restore := SetOutput(out, &bytes.Buffer{})
	defer restore()

	gitDir := setupGitDir(t)
	const size = 256 * 1024
	writeHook(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"),
		"#!/bin/sh\nhead -c "+strconv.Itoa(size)+" /dev/zero | tr '\\0' a\n")

	results, err := Run(context.Background(), store(gitDir), nil, 60, DefaultStopCap, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Failed() {
		t.Fatalf("unexpected result: %+v", results)
	}
	if out.n != size {
		t.Errorf("forwarded %d bytes of the hook's %d", out.n, size)
	}
}

func readPid(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// waitGone reports whether pid stops existing within d. An orphan is reparented
// and reaped asynchronously, so a signalled child can linger briefly.
func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// exitedWith reports whether a run ended with the hook's own exit status code.
func exitedWith(r HookResult, code int) bool {
	return r.ExitCode != nil && *r.ExitCode == code
}

// TestHookThatCannotStartRecordsTheStartError: a hook exec refuses to start
// never ran, so it has no exit status; the run records why it could not start
// and fails. Each case is a file exec itself refuses: one without an execute
// bit (discovery refuses those before a run, so this reaches the runner
// directly), one whose #! line names an interpreter that does not exist, and
// one that is neither a script with a #! line nor a program.
func TestHookThatCannotStartRecordsTheStartError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		mode    os.FileMode
		want    string
	}{
		{"not executable", "#!/bin/sh\nexit 0\n", 0o644, "exec: permission denied"},
		{"missing interpreter", "#!/nonexistent/interpreter\nexit 0\n", 0o755, "exec: no such file or directory"},
		{"malformed", "\x00\x01\x02 not a program\n", 0o755, "exec: exec format error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pre-pre-push")
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			r, err := RunSingle(context.Background(), path, nil, 30, DefaultStopCap, nil)
			if err != nil {
				t.Fatalf("RunSingle: %v", err)
			}
			if r.StartError != tc.want {
				t.Errorf("StartError = %q, want %q", r.StartError, tc.want)
			}
			if r.ExitCode != nil {
				t.Errorf("a hook that never started has no exit status, got %d", *r.ExitCode)
			}
			if !r.Failed() {
				t.Error("a hook that could not start must fail the run")
			}
		})
	}
}

// TestHookWhoseInterpreterExitsNonzeroIsNotAStartError: `#!/usr/bin/env
// missingprog` starts -- env runs, fails to find the program, and exits 127 --
// so it is an ordinary nonzero exit, not a start error.
func TestHookWhoseInterpreterExitsNonzeroIsNotAStartError(t *testing.T) {
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skip("/usr/bin/env is not present")
	}
	path := filepath.Join(t.TempDir(), "pre-pre-push")
	writeHook(t, path, "#!/usr/bin/env safegit-test-missing-program\nexit 0\n")
	r, err := RunSingle(context.Background(), path, nil, 30, DefaultStopCap, nil)
	if err != nil {
		t.Fatalf("RunSingle: %v", err)
	}
	if r.StartError != "" {
		t.Errorf("StartError = %q, want none: env started and exited", r.StartError)
	}
	if !exitedWith(r, 127) {
		t.Errorf("ExitCode = %v, want 127", r.ExitCode)
	}
}
