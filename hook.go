package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/git"
	"github.com/stricttools/safegit/internal/hooks"
	"github.com/stricttools/safegit/internal/lock"
	"github.com/stricttools/safegit/internal/repo"
	"github.com/stricttools/strictcli/go/strictcli"
)

// hookStore names this repository's hook stores for the hooks package: the work
// tree holding the tracked store, and the SHARED git directory holding the live
// one and the legacy location. The work tree is the dispatch's own pinned root,
// so a hook command run from a subdirectory reads the same store as one run
// from the top. It is empty in a repository that has no work tree, where there
// is no tracked store to read.
//
// Every caller resolves the shared git dir itself, through sharedGitDir below,
// and passes it in -- the resolution costs a git call, and one command must not
// pay for it more than once.
func hookStore(flags globalFlags, sharedDir string) hooks.Store {
	return hooks.Store{Worktree: flags.root.resolve(), SharedGitDir: sharedDir}
}

// sharedGitDir resolves the repository's common git directory, which is where
// the live hook store and the legacy location both sit. In a linked worktree it
// is NOT the worktree's own git dir: hooks are repository-level policy, exactly
// like the ref locks under the same .git/safegit, so every worktree must reach
// the same store.
func sharedGitDir(flags globalFlags, gitDir string) string {
	return repo.SharedGitDir(flags.ctx(), gitDir)
}

// hookDiscoveryExit maps a hook-discovery failure onto its exit code and dies.
//
// The states discovery refuses map onto two registered codes, because the
// remedies are different commands: hooks left in the pre-migration location need
// `hook migrate`, a hook that is not executable needs a chmod (and, in the
// checkout-provided store, a commit of the mode change). The non-executable
// refusal is one code over two typed errors -- the store decides the remedy's
// wording, not the verdict. Anything else is a plain failure to read the store.
func hookDiscoveryExit(err error) int {
	var legacy *hooks.LegacyLocationError
	var tracked *hooks.TrackedNotExecutableError
	var local *hooks.LocalNotExecutableError
	switch {
	case errors.As(err, &legacy):
		strictcli.ExitNow(exitcode.HooksNotMigrated, exitMessage(legacy))
		return exitcode.HooksNotMigrated
	case errors.As(err, &tracked):
		strictcli.ExitNow(exitcode.HookNotExecutable, exitMessage(tracked))
		return exitcode.HookNotExecutable
	case errors.As(err, &local):
		strictcli.ExitNow(exitcode.HookNotExecutable, exitMessage(local))
		return exitcode.HookNotExecutable
	default:
		strictcli.ExitNow(exitcode.General, fmt.Sprintf("discovering hooks: %v", err))
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

	locations, err := hooks.Enumerate(hookStore(flags, sharedGitDir(flags, gitDir)))
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}

	if len(locations) == 0 {
		outf(flags, "no pre-pre-push hooks found")
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
		outf(flags, "  %s  [%s, %s]  %s", loc.Rel, loc.Origin, state, loc.Path)
		if loc.Origin == hooks.OriginLegacy {
			legacy = append(legacy, loc.Path)
		}
	}
	outf(flags, "%d hook(s)", len(locations))

	// The listing is printed first and the refusal comes after it: the operator
	// needs to SEE what is in the legacy location before being told to move it.
	if len(legacy) > 0 {
		e := &hooks.LegacyLocationError{Paths: legacy}
		errorf(flags, "%v", e)
		return exitcode.HooksNotMigrated
	}
	return 0
}

// hookRun runs a specific hook by name (or all if no name given).
func hookRun(flags globalFlags, name string, stopCap time.Duration) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		errorf(flags, "%v", err)
		return exitcode.NotInitialized
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		errorf(flags, "loading config: %v", err)
		return exitcode.General
	}

	// Synthesize stdin from current branch state
	ctx := flags.ctx()
	hookStdin, err := synthesizeHookStdin(ctx)
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}

	timeoutSec := cfg.Hooks.PrePrePush.TimeoutSeconds

	hookEnv := []string{
		"SAFEGIT_REMOTE_NAME=origin",
		"SAFEGIT_REMOTE_URL=manual-run",
		"SAFEGIT_PHASE=pre-pre-push",
		fmt.Sprintf("SAFEGIT_HOOK_TIMEOUT_S=%d", timeoutSec),
	}

	store := hookStore(flags, sharedGitDir(flags, gitDir))

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
			errorf(flags, "hook %q not found", name)
			return exitcode.General
		}

		outf(flags, "running hook: %s", name)
		var r hooks.HookResult
		ran, sigExit, rErr := runHooksInterruptibly(flags, ctx, func(hctx context.Context) ([]hooks.HookResult, error) {
			var err error
			r, err = hooks.RunSingle(hctx, hookPath, hookStdin, timeoutSec, stopCap, hookEnv, flags.sc.Warn)
			if err != nil {
				return nil, err
			}
			return []hooks.HookResult{r}, nil
		})
		if sigExit != 0 {
			flags.payload(hookRunPayload{Hooks: hookRecords(ran)})
			return sigExit
		}
		if rErr != nil {
			// safegit could not run the hook under containment, so there is no
			// run to record; the payload still answers, with an empty list.
			flags.payload(hookRunPayload{Hooks: hookRecords(nil)})
			errorf(flags, "running hooks: %v", rErr)
			return exitcode.General
		}
		results := []hooks.HookResult{r}
		flags.payload(hookRunPayload{Hooks: hookRecords(results)})
		printLeftovers(flags, r)
		if code := hookRunsExit(flags, results); code != 0 {
			return code
		}
		outf(flags, "hook %s passed (%v)", name, r.Duration)
		return 0
	}

	// No name -- run all hooks
	hookPaths, dErr := hooks.Discover(store)
	if dErr != nil {
		return hookDiscoveryExit(dErr)
	}
	results, sigExit, rErr := runHooksInterruptibly(flags, ctx, func(hctx context.Context) ([]hooks.HookResult, error) {
		return hooks.RunAll(hctx, hookPaths, hookStdin, timeoutSec, stopCap, hookEnv, flags.sc.Warn)
	})
	if sigExit != 0 {
		// Interrupted: the runs so far are recorded, the interrupted one
		// with no exit status, and the signal's code is the verdict.
		flags.payload(hookRunPayload{Hooks: hookRecords(results)})
		return sigExit
	}
	if rErr != nil {
		// safegit could not run a hook under containment. The runs before it
		// are facts all the same, so the payload records them; the error text
		// and the exit code are the verdict.
		for _, r := range results {
			printLeftovers(flags, r)
		}
		flags.payload(hookRunPayload{Hooks: hookRecords(results)})
		errorf(flags, "running hooks: %v", rErr)
		return exitcode.General
	}

	flags.payload(hookRunPayload{Hooks: hookRecords(results)})

	if len(results) == 0 {
		outf(flags, "no hooks to run")
		return 0
	}

	for _, r := range results {
		printLeftovers(flags, r)
		status := "passed"
		if r.StartError != "" {
			status = fmt.Sprintf("could not be started (%s)", r.StartError)
		} else if r.TimedOut {
			status = "timed out"
		} else if r.ExitCode != nil && *r.ExitCode != 0 {
			status = fmt.Sprintf("failed (exit %d)", *r.ExitCode)
		} else if r.Failed() {
			status = "failed (left processes running)"
		}
		outf(flags, "  %s: %s (%v)", r.Name, status, r.Duration)
	}

	// The same rule push uses: the first run that did not pass decides the
	// code, so a timeout answers 21 rather than the generic 20.
	return hookRunsExit(flags, results)
}

// hookEnding says how a hook run ended, for the text output: "exit=N" for a
// hook that exited, "timed out" for one the timeout killed, which has no exit
// status of its own, and the reason for one that could not be started.
func hookEnding(r hooks.HookResult) string {
	switch {
	case r.StartError != "":
		return "could not be started: " + r.StartError
	case r.TimedOut:
		return "timed out"
	case r.ExitCode == nil:
		return "killed"
	default:
		return fmt.Sprintf("exit=%d", *r.ExitCode)
	}
}

// runHooksInterruptibly runs pre-pre-push hooks so that a SIGINT or SIGTERM
// interrupts the run instead of cutting it short. A hook runs in its own
// process group, so a terminal's Ctrl-C reaches safegit and not the hook; left
// to the signal's default, safegit would exit and the hook, and everything it
// started, would keep running. Under the hold the signal cancels the run: the
// running hook is stopped the same way the timeout stops it, and what it left
// behind is swept; a second signal meanwhile is ignored.
//
// When a signal arrived, what the hooks left is named on stderr and the second
// result is the signal exit every command shares (128 + the signal number).
// The caller emits its payload -- every hook run so far, the interrupted one
// with no exit status -- and returns that code, which the framework exits
// with. The hold is deliberately never released then: a release would let the
// signal handler exit the process before the payload is written. With no
// signal, the second result is 0.
func runHooksInterruptibly(flags globalFlags, ctx context.Context, run func(context.Context) ([]hooks.HookResult, error)) ([]hooks.HookResult, int, error) {
	hctx, hold := lock.HoldInterrupts(ctx)
	results, err := run(hctx)
	if sig, interrupted := hold.Interrupted(); interrupted {
		for _, r := range results {
			printLeftovers(flags, r)
		}
		errorf(flags, "interrupted (%v) while the pre-pre-push hooks ran; the running hook was stopped", sig)
		return results, lock.SignalExitStatus(sig), err
	}
	hold.Release()
	return results, 0, err
}

// printLeftovers writes one error line to stderr per process a hook left
// running when it ended. Such a run is a failure even when the hook exited 0.
func printLeftovers(flags globalFlags, r hooks.HookResult) {
	for _, msg := range r.LeftoverMessages() {
		errorf(flags, "%s", msg)
	}
}

// hookRunsExit is the one rule `push` and `hook run` share for turning hook
// runs into an exit code: the code of the FIRST run, in run order, that did not
// pass -- PushHookTimeout when it timed out, PushHookFailed when it could not
// be started, exited nonzero or left a process behind -- and 0 when every run
// passed. It writes
// that run's verdict as an error, which is where the verdict lives: the payload
// records the runs, never whether they passed.
func hookRunsExit(flags globalFlags, results []hooks.HookResult) int {
	for _, r := range results {
		switch {
		case r.StartError != "":
			errorf(flags, "hook %s could not be started: %s", r.Name, r.StartError)
			return exitcode.PushHookFailed
		case r.TimedOut:
			errorf(flags, "hook %s timed out after %v", r.Name, r.Duration)
			return exitcode.PushHookTimeout
		case r.ExitCode != nil && *r.ExitCode != 0:
			errorf(flags, "hook %s failed (exit %d)", r.Name, *r.ExitCode)
			return exitcode.PushHookFailed
		case r.Failed():
			// A run that exited 0 and still failed left something behind;
			// printLeftovers has already named each process.
			return exitcode.PushHookFailed
		}
	}
	return 0
}

// hookRecord is one hook run as a machine payload records it: facts about the
// run only. Whether it passed is the exit code's to say, not a member's.
type hookRecord struct {
	Name string `json:"name"`
	// ExitCode is the hook's own exit status, and null when the hook has
	// none: it could not be started, or the timeout or an interruption
	// killed it.
	ExitCode *int `json:"exit_code"`
	// StartError says why the hook could not be started at all, and is null
	// for a hook that started.
	StartError *string `json:"start_error"`
	TimedOut   bool    `json:"timed_out"`
	// DurationMS is the run's wall-clock time in whole milliseconds.
	DurationMS int64 `json:"duration_ms"`
	// LeftoverProcesses are the processes the hook left running when it
	// ended; an empty list when it left none.
	LeftoverProcesses []hookLeftoverRecord `json:"leftover_processes"`
	// UnidentifiedLeftovers is true when something still held the hook's
	// output after it ended and safegit could not name it, which fails the
	// run; LeftoverIdentificationError then says why naming it failed, and is
	// null otherwise.
	UnidentifiedLeftovers       bool    `json:"unidentified_leftovers"`
	LeftoverIdentificationError *string `json:"leftover_identification_error"`
}

// hookLeftoverRecord is one process a hook left behind.
type hookLeftoverRecord struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	// Killed is true when safegit stopped the process, false when it is
	// still running.
	Killed bool `json:"killed"`
	// State is the process state letter /proc reported when safegit found the
	// process, and null where the platform has no /proc.
	State *string `json:"state"`
}

// hookRecordSchema declares one hookRecord. `push` and `hook run` both embed
// it, so the two payloads describe a hook run the same way.
var hookRecordSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"name":        strictcli.SchemaType("string"),
		"exit_code":   strictcli.SchemaType("integer", "null"),
		"timed_out":   strictcli.SchemaType("boolean"),
		"duration_ms": strictcli.SchemaType("integer"),
		"leftover_processes": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"pid":     strictcli.SchemaType("integer"),
				"command": strictcli.SchemaType("string"),
				"killed":  strictcli.SchemaType("boolean"),
				"state":   strictcli.SchemaType("string", "null"),
			},
			[]string{"pid", "command", "killed", "state"},
			false,
		)),
		"unidentified_leftovers":        strictcli.SchemaType("boolean"),
		"leftover_identification_error": strictcli.SchemaType("string", "null"),
		"start_error":                   strictcli.SchemaType("string", "null"),
	},
	[]string{"name", "exit_code", "start_error", "timed_out", "duration_ms", "leftover_processes", "unidentified_leftovers", "leftover_identification_error"},
	false,
)

// hookRecords renders hook runs into their payload records, in run order. It
// never returns nil, so a run that ran no hooks records an empty list rather
// than null.
func hookRecords(results []hooks.HookResult) []hookRecord {
	out := make([]hookRecord, 0, len(results))
	for _, r := range results {
		left := make([]hookLeftoverRecord, 0, len(r.Leftovers))
		for _, l := range r.Leftovers {
			rec := hookLeftoverRecord{PID: l.PID, Command: l.Command, Killed: l.Killed}
			if l.FoundState != "" {
				state := l.FoundState
				rec.State = &state
			}
			left = append(left, rec)
		}
		rec := hookRecord{
			Name:                  r.Name,
			TimedOut:              r.TimedOut,
			DurationMS:            r.Duration.Milliseconds(),
			LeftoverProcesses:     left,
			UnidentifiedLeftovers: r.LeftoverUnknown != "",
		}
		if r.ExitCode != nil {
			code := *r.ExitCode
			rec.ExitCode = &code
		}
		if r.LeftoverUnknown != "" {
			reason := r.LeftoverUnknown
			rec.LeftoverIdentificationError = &reason
		}
		if r.StartError != "" {
			reason := r.StartError
			rec.StartError = &reason
		}
		out = append(out, rec)
	}
	return out
}

// hookRunPayload is what `hook run` puts in the envelope's payload, for the
// single-hook and the all-hooks form alike.
type hookRunPayload struct {
	Hooks []hookRecord `json:"hooks"`
}

// hookRunPayloadSchema declares hookRunPayload.
var hookRunPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"hooks": strictcli.SchemaArray(hookRecordSchema),
	},
	[]string{"hooks"},
	false,
)

// hookInstall copies a hook file into the tool-owned store and makes it
// executable.
func hookInstall(flags globalFlags, srcPath string) int {
	gitDir := mustGitDir()
	// The destination is inside .git/safegit, so the state directory has to
	// exist before anything is written into it -- installing into a repository
	// safegit had never touched used to depend on git's own hooks directory
	// already being there.
	if err := ensureInitialized(flags, gitDir); err != nil {
		errorf(flags, "%v", err)
		return exitcode.NotInitialized
	}

	// Reading the source is not an effect; the three mutations that follow are,
	// so `hook install --dry-run` records them and installs nothing. An
	// existing destination is refused by PlanInstall -- before the preview as
	// well as before the install, since a preview that promised a write the
	// real run would refuse is a lie.
	data, dest, err := hooks.PlanInstall(sharedGitDir(flags, gitDir), srcPath)
	if err != nil {
		errorf(flags, "%v", err)
		return exitcode.General
	}
	fx := flags.effects()
	if _, err := fx.Mkdir(filepath.Dir(dest)); err != nil {
		errorf(flags, "creating hooks dir: %v", err)
		return exitcode.General
	}
	if _, err := fx.Write(dest, data, strictcli.Resource("safegit-hook:"+dest)); err != nil {
		errorf(flags, "writing hook file: %v", err)
		return exitcode.General
	}
	if _, err := fx.Chmod(dest, 0o755); err != nil {
		errorf(flags, "making hook executable: %v", err)
		return exitcode.General
	}

	name := filepath.Base(srcPath)
	if !flags.dryRun {
		infof(flags, "installed hook: %s", name)
	}
	return 0
}

// hookRemove deletes one hook from the tool-owned live store, by name.
//
// The name may be the store-relative path (`pre-pre-push.d/20-lint`) or just
// the base name, which is what an operator reads off `hook list` and off the
// per-hook lines a push prints. A name that resolves ONLY to a hook the
// checkout provides (.safegit/hooks) is refused: that file is part of the
// repository's content and removing it means deleting it and committing that,
// which is a different act with a different audience.
func hookRemove(flags globalFlags, name string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		errorf(flags, "%v", err)
		return exitcode.NotInitialized
	}

	locations, err := hooks.Enumerate(hookStore(flags, sharedGitDir(flags, gitDir)))
	if err != nil {
		errorf(flags, "%v", err)
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

	// A hook the checkout provides is only the answer when nothing in the live
	// store carries that name: the command removes from the live store, and a
	// name present in both stores names one hook this command can remove and one
	// it cannot. The one it cannot is stated rather than silently left behind.
	if len(local) == 0 && len(tracked) > 0 {
		strictcli.ExitNow(exitcode.General, fmt.Sprintf(
			"%s is a hook the checkout provides (%s); it is part of the repository's content, so removing it means committing the deletion: "+
				"delete the file and commit that change with `safegit commit`",
			name, tracked[0].Path))
		return exitcode.General
	}
	if len(local) == 0 {
		if len(legacy) > 0 {
			strictcli.ExitNow(exitcode.HooksNotMigrated, fmt.Sprintf(
				"%s is still in the pre-migration location (%s); run `safegit hook migrate` first, then remove it",
				name, legacy[0].Path))
			return exitcode.HooksNotMigrated
		}
		strictcli.ExitNow(exitcode.General, fmt.Sprintf("no installed hook named %q (run `safegit hook list` to see what is there)", name))
		return exitcode.General
	}
	if len(local) > 1 {
		var paths []string
		for _, loc := range local {
			paths = append(paths, loc.Rel)
		}
		strictcli.ExitNow(exitcode.General, fmt.Sprintf("%q names %d hooks (%s); give the full name to say which one",
			name, len(local), strings.Join(paths, ", ")))
		return exitcode.General
	}

	target := local[0]
	fx := flags.effects()
	if _, err := fx.Remove(target.Path); err != nil {
		errorf(flags, "removing %s: %v", target.Path, err)
		return exitcode.General
	}
	if !flags.dryRun {
		infof(flags, "removed hook: %s", target.Rel)
	}
	// The advisory is what keeps the removal from reading as "that name is gone
	// now", so a preview states it too: after the removal this command previews,
	// the repository-provided hook of the same name still runs. It is advice
	// about the store rather than a result, so it is progress text, which
	// --quiet suppresses.
	if len(tracked) > 0 {
		infof(flags, "note: %s also names a hook the checkout provides (%s), which still runs; removing that one means deleting the file and committing that", name, tracked[0].Path)
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
		errorf(flags, "%v", err)
		return exitcode.NotInitialized
	}

	// Both ends of every move are under the SHARED git dir: git's own hook
	// directory is common to every worktree, and so is the live store the hooks
	// move into, so migration run from a linked worktree relocates the
	// repository's hooks rather than looking into an empty directory of its own.
	shared := sharedGitDir(flags, gitDir)

	type move struct{ src, dest, label string }
	var moves []move
	for _, m := range []move{
		{hooks.LegacyFile(shared), filepath.Join(hooks.LocalDir(shared), "pre-pre-push"), "pre-pre-push"},
		{hooks.LegacyDir(shared), filepath.Join(hooks.LocalDir(shared), "pre-pre-push.d"), "pre-pre-push.d"},
	} {
		if _, err := os.Lstat(m.src); err != nil {
			continue
		}
		if _, err := os.Lstat(m.dest); err == nil {
			strictcli.ExitNow(exitcode.General, fmt.Sprintf(
				"cannot migrate %s: %s already exists; merge the two by hand and remove the old one",
				m.src, m.dest))
			return exitcode.General
		}
		moves = append(moves, m)
	}

	if len(moves) == 0 {
		outf(flags, "nothing to migrate: no hooks in %s", filepath.Join(shared, "hooks"))
		return 0
	}

	fx := flags.effects()
	if _, err := fx.Mkdir(hooks.LocalDir(shared)); err != nil {
		errorf(flags, "creating %s: %v", hooks.LocalDir(shared), err)
		return exitcode.General
	}
	for _, m := range moves {
		if _, err := fx.Rename(m.src, m.dest); err != nil {
			errorf(flags, "moving %s: %v", m.src, err)
			return exitcode.General
		}
		if !flags.dryRun {
			infof(flags, "migrated %s -> %s", m.label, m.dest)
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

// hookKillCapVariable is the environment variable --hook-kill-cap-s is bound
// to, through the framework's flag environment binding.
const hookKillCapVariable = "SAFEGIT_HOOK_KILL_CAP_S"

// hookKillCapHelp is --hook-kill-cap-s's help, shared by push and hook run.
var hookKillCapHelp = fmt.Sprintf("the cap on stopping a hook and the processes it started -- on timeout, on interruption, or for the processes a hook left when it ended -- in whole seconds from %d to %d: every process gets SIGTERM and the cap minus %ds to exit, then SIGKILL. When the flag is not given, the environment variable %s sets it; omitted means %d",
	int(hooks.MinStopCap/time.Second), int(hooks.MaxStopCap/time.Second), int(hooks.StopWindow/time.Second), hookKillCapVariable, int(hooks.DefaultStopCap/time.Second))

// hookKillCap resolves --hook-kill-cap-s for push and hook run, before anything
// runs: absent means the default, and a value outside the range ends the command
// naming where the value came from -- the flag, or the environment variable
// bound to it -- and the remedy for that source.
func hookKillCap(ctx *strictcli.Context, kwargs map[string]interface{}) time.Duration {
	v := kwargs["hook_kill_cap_s"]
	if v == nil {
		return hooks.DefaultStopCap
	}
	secs := v.(int)
	limit, err := hooks.StopCapFromSeconds(secs)
	if err == nil {
		return limit
	}
	given, remedy := fmt.Sprintf("--hook-kill-cap-s %d", secs), "omit the flag"
	if ctx.Source("hook_kill_cap_s") == "env" {
		given, remedy = fmt.Sprintf("%s=%q", hookKillCapVariable, strconv.Itoa(secs)), "unset the variable"
	}
	strictcli.ExitNow(exitcode.General, fmt.Sprintf("%s is not allowed: %v; %s for the default of %d",
		given, err, remedy, int(hooks.DefaultStopCap/time.Second)))
	return 0
}
