// Package hooks discovers and executes pre-pre-push hooks that run before any network I/O, solving the SSH timeout problem when checks are long-running.
package hooks

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
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

// LegacyLocationError reports hooks still sitting in the pre-migration
// location, git's own .git/hooks. Discovery refuses rather than running them:
// running from both places would make the store safegit executes from depend on
// where a file happened to be left, and silently skipping them would stop an
// operator's checks without saying so.
type LegacyLocationError struct {
	// Paths are the absolute paths found, in enumeration order.
	Paths []string
}

func (e *LegacyLocationError) Error() string {
	return fmt.Sprintf("%d hook(s) are still in the pre-migration location (%s); "+
		"run `safegit hook migrate` to move them into the tool-owned hook store", len(e.Paths), strings.Join(e.Paths, ", "))
}

// TrackedNotExecutableError reports a repository-provided hook whose mode says
// it cannot run. It is a refusal, not a skip: a hook in the checkout's
// .safegit/hooks is disabled by REMOVING it (and committing that), so a mode
// that silently disabled one would turn an accidentally lost executable bit --
// a checkout on a filesystem without modes, a patch applied by a tool that
// drops them -- into checks that quietly stopped running.
type TrackedNotExecutableError struct {
	// Paths are the absolute paths of the offending repository-provided hooks.
	Paths []string
}

func (e *TrackedNotExecutableError) Error() string {
	return fmt.Sprintf("%d repository-provided hook(s) in the checkout's .safegit/hooks are not executable: %s; "+
		"run `chmod +x` on each and COMMIT the mode change (such a hook is disabled by deleting the file and committing that, never by dropping its mode)",
		len(e.Paths), strings.Join(e.Paths, ", "))
}

// LocalNotExecutableError reports a hook in the tool-owned live store whose
// mode says it cannot run. It is the live store's half of the same rule the
// checkout-provided store has always been held to: a hook is disabled by
// REMOVING it, so a mode that silently disabled one would turn an accident --
// an editor that rewrote the file, a patch tool that dropped the bit, a copy
// across a filesystem without modes -- into checks that quietly stopped
// running. It used to be a skip with a warning on stderr, which is git's own
// stance for its own hooks; the two stores answering the same accident
// differently is what that stance cost, so it is a refusal now.
type LocalNotExecutableError struct {
	// Paths are the absolute paths of the offending hooks.
	Paths []string
}

func (e *LocalNotExecutableError) Error() string {
	return fmt.Sprintf("%d hook(s) in the tool-owned hook store are not executable: %s; "+
		"run `chmod +x` on each, or remove the hook (`safegit hook remove <name>`) if it is meant to be gone -- a hook is disabled by removing it, never by dropping its mode",
		len(e.Paths), strings.Join(e.Paths, ", "))
}

// Discover returns the hooks to execute, in execution order: the tracked store
// first, then the local one, each in Rel order. It is the
// execution-eligibility layer over Enumerate, and the only place that decides
// what "eligible" means.
//
// Two states are refusals rather than filters -- a hook left in the legacy
// location, and a discovered hook that is not executable -- and each comes back
// as a typed error so a caller can map it to an exit code. The non-executable
// refusal is store-independent: the live store and the checkout-provided one
// answer a missing execute bit the same way, in their own typed errors because
// the remedies differ by a commit, and never as a silent skip.
//
// What this returns is a list of scripts the caller will EXECUTE, drawn partly
// from the checkout's own content -- see Origin for the boundary that governs
// when that content runs.
func Discover(s Store) ([]string, error) {
	all, err := Enumerate(s)
	if err != nil {
		return nil, err
	}

	var legacy, trackedNonExec, localNonExec []string
	var hooks []string
	for _, loc := range all {
		if loc.Origin == OriginLegacy {
			legacy = append(legacy, loc.Path)
			continue
		}
		if !loc.IsHookName() {
			continue
		}
		if loc.Executable {
			hooks = append(hooks, loc.Path)
			continue
		}
		if loc.Origin == OriginTracked {
			trackedNonExec = append(trackedNonExec, loc.Path)
			continue
		}
		localNonExec = append(localNonExec, loc.Path)
	}

	if len(legacy) > 0 {
		return nil, &LegacyLocationError{Paths: legacy}
	}
	if len(trackedNonExec) > 0 {
		return nil, &TrackedNotExecutableError{Paths: trackedNonExec}
	}
	if len(localNonExec) > 0 {
		return nil, &LocalNotExecutableError{Paths: localNonExec}
	}
	return hooks, nil
}

// DiscoverMulti discovers hooks across several repositories, concatenating the
// results in order: the first store's hooks run first. It is what a push from a
// submodule uses to run the parent's hooks before its own.
//
// The parameter is a store per repository rather than a git directory, because
// each repository's tracked hooks live in its WORK TREE -- a cascade keyed on
// git directories alone could never see them.
func DiscoverMulti(stores []Store) ([]string, error) {
	var all []string
	for _, s := range stores {
		found, err := Discover(s)
		if err != nil {
			return nil, fmt.Errorf("discovering hooks in %s: %w", s.SharedGitDir, err)
		}
		all = append(all, found...)
	}
	return all, nil
}

// Run executes all discovered hooks sequentially with the given stdin.
// On non-zero exit, remaining hooks are skipped. On timeout: SIGTERM, 5s grace, SIGKILL.
func Run(ctx context.Context, s Store, stdin []byte, timeoutSec int, env []string) ([]HookResult, error) {
	hooks, err := Discover(s)
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

	// Hook stdout is forwarded verbatim, every line of it. Nothing a hook
	// writes is inspected, and nothing a hook writes can change the budget it
	// runs under: the configured timeout is the only timeout, and a hook that
	// needs a different one reads SAFEGIT_HOOK_TIMEOUT_S from its environment
	// and is configured, never self-declared on stdout.
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}

	if err := cmd.Start(); err != nil {
		return HookResult{Name: name, ExitCode: 1, Duration: time.Since(start)}
	}

	// Stream stdout in a goroutine, so a hook that writes a lot is never
	// blocked on a full pipe while this function waits on the clock.
	ioDone := make(chan struct{})
	go func() {
		defer close(ioDone)
		io.Copy(stdout, stdoutPipe)
	}()

	// Wait for process completion with timeout
	procDone := make(chan error, 1)
	go func() {
		procDone <- cmd.Wait()
	}()

	timeout := time.Duration(timeoutSec) * time.Second
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

		// ExitCode is the HOOK's status, not safegit's. A killed hook has no
		// status of its own, so the marker is deliberately chosen to READ the
		// same as safegit's own hook-timeout code in the "exit=%d" line callers
		// print -- which is why it is taken from the registry rather than
		// written as a bare 21 that duplicates the constant by value. Nothing
		// branches on it: TimedOut is what decides the caller's exit code.
		return HookResult{Name: name, ExitCode: exitcode.PushHookTimeout, Duration: time.Since(start), TimedOut: true}

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

// isExecutable checks if a file has any execute permission bit set.
func isExecutable(info os.FileInfo) bool {
	return info.Mode()&0111 != 0
}

// PlanInstall reads the hook source and resolves the destination inside the
// tool-owned live store under the shared git dir, without mutating anything.
// Callers mint the mkdir/write/chmod themselves so a dry run can record the
// install instead of performing it.
//
// An existing destination is REFUSED rather than overwritten: an install that
// silently replaced a hook could destroy the operator's own script (and, back
// when the store was git's own .git/hooks, a native git hook safegit itself
// runs). Upgrading a hook is `safegit hook remove <name>` followed by an
// install, which says out loud that the old one is going away.
func PlanInstall(sharedGitDir, srcPath string) (data []byte, dest string, err error) {
	data, err = os.ReadFile(srcPath)
	if err != nil {
		return nil, "", fmt.Errorf("reading hook file: %w", err)
	}
	dest = filepath.Join(LocalDir(sharedGitDir), filepath.Base(srcPath))
	if _, statErr := os.Lstat(dest); statErr == nil {
		return nil, "", fmt.Errorf("%s already exists; remove it first (`safegit hook remove %s`) -- install never overwrites a hook",
			dest, filepath.Base(srcPath))
	}
	return data, dest, nil
}
