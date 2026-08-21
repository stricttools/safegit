// Package hooks discovers and executes pre-pre-push hooks that run before any network I/O, solving the SSH timeout problem when checks are long-running.
package hooks

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// stdout and stderr are the default output writers for hook execution.
// Tests can override them via SetOutput to capture output.
var (
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

// SetOutput overrides the package-level stdout and stderr writers.
// Returns a restore function that resets them to their previous values.
func SetOutput(out, err io.Writer) func() {
	prevOut, prevErr := stdout, stderr
	stdout, stderr = out, err
	return func() { stdout, stderr = prevOut, prevErr }
}

// HookResult holds the outcome of running a single hook.
type HookResult struct {
	Name     string        `json:"name"`
	ExitCode int           `json:"exitCode"`
	Duration time.Duration `json:"duration"`
	TimedOut bool          `json:"timedOut,omitempty"`
}

// Discover finds pre-pre-push hooks in .git/hooks/.
// Returns executable hook paths in execution order:
// 1. .git/hooks/pre-pre-push (single file)
// 2. .git/hooks/pre-pre-push.d/* (lexical order, skip dot-prefixed and tilde-suffixed)
func Discover(gitDir string) ([]string, error) {
	hooksDir := filepath.Join(gitDir, "hooks")
	var hooks []string

	// Single-file hook
	single := filepath.Join(hooksDir, "pre-pre-push")
	if info, err := os.Stat(single); err == nil && !info.IsDir() {
		if isExecutable(info) {
			hooks = append(hooks, single)
		} else {
			fmt.Fprintf(stderr, "warning: %s exists but is not executable, skipping\n", single)
		}
	}

	// Directory-based hooks
	dirPath := filepath.Join(hooksDir, "pre-pre-push.d")
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		// Directory doesn't exist -- that's fine
		if os.IsNotExist(err) {
			return hooks, nil
		}
		return hooks, fmt.Errorf("reading pre-pre-push.d: %w", err)
	}

	// Collect and sort lexically
	var names []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, "~") {
			continue
		}
		if e.IsDir() {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := filepath.Join(dirPath, name)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if isExecutable(info) {
			hooks = append(hooks, p)
		} else {
			fmt.Fprintf(stderr, "warning: %s is not executable, skipping\n", p)
		}
	}

	return hooks, nil
}

// DiscoverMulti discovers hooks across multiple git directories, concatenating
// results in order. The first gitDir's hooks come first. This supports hook
// cascading from parent repos into submodule pushes.
func DiscoverMulti(gitDirs []string) ([]string, error) {
	var all []string
	for _, gd := range gitDirs {
		found, err := Discover(gd)
		if err != nil {
			return nil, fmt.Errorf("discovering hooks in %s: %w", gd, err)
		}
		all = append(all, found...)
	}
	return all, nil
}

// Run executes all discovered hooks sequentially with the given stdin.
// On non-zero exit, remaining hooks are skipped. On timeout: SIGTERM, 5s grace, SIGKILL.
func Run(ctx context.Context, gitDir string, stdin []byte, timeoutSec int, env []string) ([]HookResult, error) {
	hooks, err := Discover(gitDir)
	if err != nil {
		return nil, err
	}
	return RunAll(ctx, hooks, stdin, timeoutSec, env)
}

// RunAll executes the given hook paths sequentially with the given stdin.
// On non-zero exit, remaining hooks are skipped. On timeout: SIGTERM, 5s grace, SIGKILL.
func RunAll(ctx context.Context, hookPaths []string, stdin []byte, timeoutSec int, env []string) ([]HookResult, error) {
	if len(hookPaths) == 0 {
		return nil, nil
	}

	var results []HookResult
	for _, hookPath := range hookPaths {
		result := runOne(ctx, hookPath, stdin, timeoutSec, env)
		results = append(results, result)
		if result.ExitCode != 0 {
			break // abort on first failure
		}
	}
	return results, nil
}

// RunSingle executes a single hook by path.
func RunSingle(ctx context.Context, hookPath string, stdin []byte, timeoutSec int, env []string) HookResult {
	return runOne(ctx, hookPath, stdin, timeoutSec, env)
}

// runOne executes a single hook, streaming stdout/stderr to the package-level writers.
// Respects timeout: SIGTERM then SIGKILL after 5s grace.
func runOne(ctx context.Context, hookPath string, stdin []byte, timeoutSec int, env []string) HookResult {
	name := filepath.Base(hookPath)
	start := time.Now()

	cmd := exec.Command(hookPath)
	cmd.Stdin = strings.NewReader(string(stdin))
	cmd.Stderr = stderr
	cmd.Dir = filepath.Dir(hookPath)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	// Set process group so we can signal the entire group (Unix only)
	setProcGroup(cmd)

	// Pipe stdout to check the first line for timeout override
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}

	if err := cmd.Start(); err != nil {
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}

	// Stream stdout in a goroutine. Check the first line for timeout override
	// and signal the effective timeout via channel.
	timeoutCh := make(chan int, 1)
	ioDone := make(chan struct{})
	go func() {
		defer close(ioDone)
		reader := bufio.NewReader(stdoutPipe)
		firstLine, err := reader.ReadString('\n')
		if err == nil {
			override := parseTimeoutOverride(firstLine)
			if override > 0 {
				timeoutCh <- override
			} else {
				timeoutCh <- 0
				fmt.Fprint(stdout, firstLine)
			}
		} else {
			timeoutCh <- 0
			if firstLine != "" {
				fmt.Fprint(stdout, firstLine)
			}
		}
		// Stream remaining stdout
		io.Copy(stdout, reader)
	}()

	// Determine effective timeout: use override if received quickly, else default
	effectiveTimeout := timeoutSec
	select {
	case override := <-timeoutCh:
		if override > 0 {
			effectiveTimeout = override
		}
	case <-time.After(2 * time.Second):
		// Hook hasn't printed anything in 2s -- use default timeout
	}

	// Wait for process completion with timeout
	procDone := make(chan error, 1)
	go func() {
		procDone <- cmd.Wait()
	}()

	timeout := time.Duration(effectiveTimeout) * time.Second
	select {
	case err := <-procDone:
		<-ioDone // wait for IO streaming to finish
		duration := time.Since(start)
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return HookResult{Name: name, ExitCode: exitErr.ExitCode(), Duration: duration}
			}
			return HookResult{Name: name, ExitCode: 1, Duration: duration}
		}
		return HookResult{Name: name, ExitCode: 0, Duration: duration}

	case <-time.After(timeout):
		// Timeout: SIGTERM the process group
		if cmd.Process != nil {
			killGroup(cmd, syscall.SIGTERM)
		}

		// Grace period: 5 seconds
		select {
		case <-procDone:
			// Terminated gracefully
		case <-time.After(5 * time.Second):
			// SIGKILL the process group
			if cmd.Process != nil {
				killGroup(cmd, syscall.SIGKILL)
			}
			<-procDone
		}
		<-ioDone

		// ExitCode is the HOOK's status, not safegit's, so it is deliberately
		// not an internal/exitcode constant. A killed hook has no status of its
		// own; 21 is a synthetic marker chosen to read the same as safegit's
		// own hook-timeout code in the "exit=%d" line callers print. Nothing
		// branches on it -- TimedOut is what decides the caller's exit code.
		return HookResult{Name: name, ExitCode: 21, Duration: time.Since(start), TimedOut: true}

	case <-ctx.Done():
		// Parent context cancelled
		if cmd.Process != nil {
			killGroup(cmd, syscall.SIGKILL)
		}
		<-procDone
		<-ioDone
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}
}

// parseTimeoutOverride checks if a line is "# safegit: timeout=NNN" and returns the value.
// Returns 0 if not a valid override.
func parseTimeoutOverride(line string) int {
	line = strings.TrimSpace(line)
	const prefix = "# safegit: timeout="
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	valStr := strings.TrimPrefix(line, prefix)
	val, err := strconv.Atoi(valStr)
	if err != nil || val <= 0 {
		return 0
	}
	return val
}

// isExecutable checks if a file has any execute permission bit set.
func isExecutable(info os.FileInfo) bool {
	return info.Mode()&0111 != 0
}

// PlanInstall reads the hook source and resolves the destination path, without
// mutating anything. Callers mint the mkdir/write/chmod themselves so a dry run
// can record the install instead of performing it.
func PlanInstall(gitDir, srcPath string) (data []byte, dest string, err error) {
	data, err = os.ReadFile(srcPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading hook file: %w", err)
	}
	return data, filepath.Join(gitDir, "hooks", filepath.Base(srcPath)), nil
}

// Install copies a hook file to .git/hooks/ and makes it executable.
func Install(gitDir, srcPath string) error {
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading hook file: %w", err)
	}

	destName := filepath.Base(srcPath)
	dest := filepath.Join(hooksDir, destName)
	if err := os.WriteFile(dest, data, 0755); err != nil {
		return fmt.Errorf("writing hook file: %w", err)
	}
	return nil
}

// InstallPlaceholder writes a no-op pre-pre-push hook if one doesn't already exist.
func InstallPlaceholder(gitDir string) error {
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return fmt.Errorf("creating hooks dir: %w", err)
	}

	dest := filepath.Join(hooksDir, "pre-pre-push")
	if _, err := os.Stat(dest); err == nil {
		// Already exists, don't overwrite
		return nil
	}

	placeholder := `#!/bin/sh
# Installed by safegit. This is a no-op placeholder.
# Add your pre-push validators here, or use .git/hooks/pre-pre-push.d/
exit 0
`
	if err := os.WriteFile(dest, []byte(placeholder), 0755); err != nil {
		return fmt.Errorf("writing placeholder hook: %w", err)
	}
	return nil
}
