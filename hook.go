package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/strictcli/go/strictcli"
)

// hookStore names this repository's hook stores for the hooks package: the work
// tree holding the committed store, and the git directory holding the
// tool-owned one. The work tree is the dispatch's own pinned root, so a hook
// command run from a subdirectory reads the same store as one run from the top.
// It is empty in a repository that has no work tree, where there is no
// committed store to read.
func hookStore(flags globalFlags, gitDir string) hooks.Store {
	return hooks.Store{Worktree: flags.root.resolve(), GitDir: gitDir}
}

// hookDiscoveryExit maps a hook-discovery failure onto its exit code and dies.
//
// The two states discovery refuses each have their own registered code, because
// the remedies are different commands: hooks left in the pre-migration location
// need `hook migrate`, a committed hook that is not executable needs a chmod and
// a commit. Anything else is a plain failure to read the store.
func hookDiscoveryExit(err error) int {
	var legacy *hooks.LegacyLocationError
	var tracked *hooks.TrackedNotExecutableError
	switch {
	case errors.As(err, &legacy):
		die(exitcode.HooksNotMigrated, legacy.Error())
		return exitcode.HooksNotMigrated
	case errors.As(err, &tracked):
		die(exitcode.TrackedHookNotExecutable, tracked.Error())
		return exitcode.TrackedHookNotExecutable
	default:
		die(exitcode.General, fmt.Sprintf("discovering hooks: %v", err))
		return exitcode.General
	}
}

// hookList lists every hook LOCATION, whether or not it would run.
//
// It reads the location enumerator rather than discovery on purpose: an
// operator asking what is installed is most often asking precisely about the
// hook that is NOT running, so a listing that quietly dropped non-executable
// entries would hide the answer. Origin and executability are shown for the
// same reason -- they are what decides whether and when each one runs.
func hookList(flags globalFlags) int {
	gitDir := mustGitDir()

	locations, err := hooks.Enumerate(hookStore(flags, gitDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	if len(locations) == 0 {
		outf(flags, "no pre-pre-push hooks found\n")
		return 0
	}

	var legacy []string
	for _, loc := range locations {
		state := "executable"
		if !loc.Executable {
			state = "NOT EXECUTABLE"
		}
		if !loc.IsHookName() {
			state = "not a hook (dot-prefixed or editor backup)"
		}
		outf(flags, "  %s  [%s, %s]  %s\n", loc.Rel, loc.Origin, state, loc.Path)
		if loc.Origin == hooks.OriginLegacy {
			legacy = append(legacy, loc.Path)
		}
	}
	outf(flags, "%d hook(s)\n", len(locations))

	// The listing is printed first and the refusal comes after it: the operator
	// needs to SEE what is in the legacy location before being told to move it.
	if len(legacy) > 0 {
		e := &hooks.LegacyLocationError{Paths: legacy}
		fmt.Fprintf(os.Stderr, "error: %v\n", e)
		return exitcode.HooksNotMigrated
	}
	return 0
}

// hookRun runs a specific hook by name (or all if no name given).
func hookRun(flags globalFlags, name string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
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

	store := hookStore(flags, gitDir)

	if name != "" {
		// Run a specific hook by name
		discovered, dErr := hooks.Discover(store)
		if dErr != nil {
			return hookDiscoveryExit(dErr)
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
	results, rErr := hooks.Run(ctx, store, hookStdin, timeoutSec, hookEnv)
	if rErr != nil {
		return hookDiscoveryExit(rErr)
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

// hookInstall copies a hook file into the tool-owned store and makes it
// executable.
func hookInstall(flags globalFlags, srcPath string) int {
	gitDir := mustGitDir()
	// The destination is inside .git/safegit, so the state directory has to
	// exist before anything is written into it -- installing into a repository
	// safegit had never touched used to depend on git's own hooks directory
	// already being there.
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}

	// Reading the source is not an effect; the three mutations that follow are,
	// so `hook install --dry-run` records them and installs nothing. An
	// existing destination is refused by PlanInstall -- before the preview as
	// well as before the install, since a preview that promised a write the
	// real run would refuse is a lie.
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

// hookRemove deletes one hook from the tool-owned live store, by name.
//
// The name may be the store-relative path (`pre-pre-push.d/20-lint`) or just
// the base name, which is what an operator reads off `hook list` and off the
// per-hook lines a push prints. A name that resolves to a COMMITTED hook is
// refused: that file is part of the repository's content and removing it means
// committing the deletion, which is a different act with a different audience.
func hookRemove(flags globalFlags, name string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}

	locations, err := hooks.Enumerate(hookStore(flags, gitDir))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	var local, tracked, legacy []hooks.Location
	for _, loc := range locations {
		if loc.Rel != name && filepath.Base(loc.Rel) != name {
			continue
		}
		switch loc.Origin {
		case hooks.OriginTracked:
			tracked = append(tracked, loc)
		case hooks.OriginLegacy:
			legacy = append(legacy, loc)
		default:
			local = append(local, loc)
		}
	}

	if len(tracked) > 0 {
		die(exitcode.General, fmt.Sprintf(
			"%s is a COMMITTED hook (%s); it is part of the repository's content, so removing it means committing the deletion: "+
				"delete the file and commit that change with `safegit commit`",
			name, tracked[0].Path))
		return exitcode.General
	}
	if len(local) == 0 {
		if len(legacy) > 0 {
			die(exitcode.HooksNotMigrated, fmt.Sprintf(
				"%s is still in the pre-migration location (%s); run `safegit hook migrate` first, then remove it",
				name, legacy[0].Path))
			return exitcode.HooksNotMigrated
		}
		die(exitcode.General, fmt.Sprintf("no installed hook named %q (run `safegit hook list` to see what is there)", name))
		return exitcode.General
	}
	if len(local) > 1 {
		var paths []string
		for _, loc := range local {
			paths = append(paths, loc.Rel)
		}
		die(exitcode.General, fmt.Sprintf("%q names %d hooks (%s); give the full name to say which one",
			name, len(local), strings.Join(paths, ", ")))
		return exitcode.General
	}

	target := local[0]
	fx := flags.effects()
	if _, err := fx.Remove(target.Path); err != nil {
		fmt.Fprintf(os.Stderr, "error: removing %s: %v\n", target.Path, err)
		return exitcode.General
	}
	if !flags.silent() && !flags.dryRun {
		fmt.Printf("removed hook: %s\n", target.Rel)
	}
	return 0
}

// hookMigrate moves safegit's hooks out of git's own hook directory and into
// the tool-owned store.
//
// The two names it moves -- the `pre-pre-push` file and the `pre-pre-push.d`
// directory -- are the only ones safegit ever wrote into .git/hooks, so they
// move UNCONDITIONALLY, with no look at what is inside them: a content sniff
// would have to guess about an operator's own script, and guessing wrong in
// either direction (moving a native hook, or leaving a safegit hook behind)
// is worse than moving exactly the two names safegit owns.
func hookMigrate(flags globalFlags) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}

	type move struct{ src, dest, label string }
	var moves []move
	for _, m := range []move{
		{hooks.LegacyFile(gitDir), filepath.Join(hooks.LocalDir(gitDir), "pre-pre-push"), "pre-pre-push"},
		{hooks.LegacyDir(gitDir), filepath.Join(hooks.LocalDir(gitDir), "pre-pre-push.d"), "pre-pre-push.d"},
	} {
		if _, err := os.Lstat(m.src); err != nil {
			continue
		}
		if _, err := os.Lstat(m.dest); err == nil {
			die(exitcode.General, fmt.Sprintf(
				"cannot migrate %s: %s already exists; merge the two by hand and remove the old one",
				m.src, m.dest))
			return exitcode.General
		}
		moves = append(moves, m)
	}

	if len(moves) == 0 {
		outf(flags, "nothing to migrate: no hooks in %s\n", filepath.Join(gitDir, "hooks"))
		return 0
	}

	fx := flags.effects()
	if _, err := fx.Mkdir(hooks.LocalDir(gitDir)); err != nil {
		fmt.Fprintf(os.Stderr, "error: creating %s: %v\n", hooks.LocalDir(gitDir), err)
		return exitcode.General
	}
	for _, m := range moves {
		if _, err := fx.Rename(m.src, m.dest); err != nil {
			fmt.Fprintf(os.Stderr, "error: moving %s: %v\n", m.src, err)
			return exitcode.General
		}
		if !flags.silent() && !flags.dryRun {
			fmt.Printf("migrated %s -> %s\n", m.label, m.dest)
		}
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
