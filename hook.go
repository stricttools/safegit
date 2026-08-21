package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/strictcli/go/strictcli"
)

// hookList discovers and lists all pre-pre-push hooks.
func hookList(flags globalFlags) int {
	gitDir := mustGitDir()

	discovered, err := hooks.Discover(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	if len(discovered) == 0 {
		outf(flags, "no pre-pre-push hooks found\n")
		return 0
	}

	for _, h := range discovered {
		outf(flags, "  %s  (%s)\n", filepath.Base(h), h)
	}
	outf(flags, "%d hook(s)\n", len(discovered))
	return 0
}

// hookRun runs a specific hook by name (or all if no name given).
func hookRun(flags globalFlags, name string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return exitcode.General
	}

	// Synthesize stdin from current branch state
	ctx := flags.ctx()
	hookStdin, err := synthesizeHookStdin(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	timeoutSec := cfg.Hooks.PrePrePush.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = 1800
	}

	hookEnv := []string{
		"SAFEGIT_REMOTE_NAME=origin",
		"SAFEGIT_REMOTE_URL=manual-run",
		"SAFEGIT_PHASE=pre-pre-push",
		fmt.Sprintf("SAFEGIT_HOOK_TIMEOUT_S=%d", timeoutSec),
	}

	if name != "" {
		// Run a specific hook by name
		discovered, dErr := hooks.Discover(gitDir)
		if dErr != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", dErr)
			return exitcode.General
		}

		var hookPath string
		for _, h := range discovered {
			if filepath.Base(h) == name {
				hookPath = h
				break
			}
		}
		if hookPath == "" {
			fmt.Fprintf(os.Stderr, "hook %q not found\n", name)
			return exitcode.General
		}

		outf(flags, "running hook: %s\n", name)
		r := hooks.RunSingle(ctx, hookPath, hookStdin, timeoutSec, hookEnv)
		if r.TimedOut {
			fmt.Fprintf(os.Stderr, "hook %s timed out\n", name)
			return exitcode.PushHookTimeout
		}
		if r.ExitCode != 0 {
			fmt.Fprintf(os.Stderr, "hook %s failed (exit %d)\n", name, r.ExitCode)
			return exitcode.PushHookFailed
		}
		outf(flags, "hook %s passed (%v)\n", name, r.Duration)
		return 0
	}

	// No name -- run all hooks
	results, rErr := hooks.Run(ctx, gitDir, hookStdin, timeoutSec, hookEnv)
	if rErr != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", rErr)
		return exitcode.General
	}

	if len(results) == 0 {
		outf(flags, "no hooks to run\n")
		return 0
	}

	failed := false
	for _, r := range results {
		status := "passed"
		if r.TimedOut {
			status = "timed out"
			failed = true
		} else if r.ExitCode != 0 {
			status = fmt.Sprintf("failed (exit %d)", r.ExitCode)
			failed = true
		}
		outf(flags, "  %s: %s (%v)\n", r.Name, status, r.Duration)
	}

	if failed {
		return exitcode.PushHookFailed
	}
	return 0
}

// hookInstall copies a hook file to .git/hooks/ and makes it executable.
func hookInstall(flags globalFlags, srcPath string) int {
	gitDir := mustGitDir()

	// Reading the source is not an effect; the three mutations that follow are,
	// so `hook install --dry-run` records them and installs nothing.
	data, dest, err := hooks.PlanInstall(gitDir, srcPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	fx := flags.effects()
	if _, err := fx.Mkdir(filepath.Dir(dest)); err != nil {
		fmt.Fprintf(os.Stderr, "error: creating hooks dir: %v\n", err)
		return exitcode.General
	}
	if _, err := fx.Write(dest, data, strictcli.Resource("safegit-hook:"+dest)); err != nil {
		fmt.Fprintf(os.Stderr, "error: writing hook file: %v\n", err)
		return exitcode.General
	}
	if _, err := fx.Chmod(dest, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: making hook executable: %v\n", err)
		return exitcode.General
	}

	name := filepath.Base(srcPath)
	if !flags.silent() && !flags.dryRun {
		fmt.Printf("installed hook: %s\n", name)
	}
	return 0
}

// synthesizeHookStdin builds hook stdin from the current branch's state.
func synthesizeHookStdin(ctx context.Context) ([]byte, error) {
	headRef, err := git.HeadRef(ctx)
	if err != nil {
		return nil, fmt.Errorf("cannot determine current branch (detached HEAD?)")
	}

	localSHA, err := git.RevParse(ctx, headRef)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", headRef, err)
	}

	// The remote-side SHA a synthesized pre-push line reports: the all-zero
	// object name, git's "this ref does not exist there" convention.
	line := fmt.Sprintf("%s %s %s %s\n", headRef, localSHA, headRef, git.ZeroSHA)
	return []byte(line), nil
}
