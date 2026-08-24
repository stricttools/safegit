package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitversion"
	"github.com/smm-h/safegit/internal/hooks"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/safegit/internal/submodule"
	"github.com/smm-h/strictcli/go/strictcli"
)

type checkResult struct {
	Name   string
	Status string // "ok", "warn", "error"
	Detail string
}

// doctorEnv is everything a registered check may look at. It is built once per
// doctor run so a check function takes no other arguments and can be added,
// removed or reordered without touching any other check.
type doctorEnv struct {
	ctx context.Context
	// worktree is the repository's work tree, empty when it has none. Checks
	// that look at anything committed -- the hook store above all -- need it.
	worktree string
	gitDir   string
	// sharedGitDir is the COMMON git dir (repo.SharedGitDir): the same one in
	// an ordinary repository, the main one in a linked worktree. Everything
	// that is repository-level rather than checkout-level -- the live hook
	// store, the legacy hook location, the ref locks -- is anchored there, so a
	// check that read gitDir instead would call a linked worktree healthy while
	// the repository's pushes refuse.
	sharedGitDir string
	sgDir        string
	inited       bool
}

// doctorFinding is one check's outcome.
//
// A check that found nothing to say (its precondition does not exist in this
// repo -- no HEAD ref, no hook directory) returns findingNone and is reported
// as nothing at all. Otherwise the finding is either ok or a failure carrying
// the check's declared severity, which a finding may override when the reason
// for failing is graver than the check's ordinary one.
type doctorFinding struct {
	reported bool
	ok       bool
	status   string // overrides the check's Severity when non-empty
	detail   string
}

func findingNone() doctorFinding { return doctorFinding{} }

func findingOK(detail string) doctorFinding {
	return doctorFinding{reported: true, ok: true, detail: detail}
}

func findingFail(format string, args ...interface{}) doctorFinding {
	return doctorFinding{reported: true, detail: fmt.Sprintf(format, args...)}
}

// findingAt is findingFail with an explicit status instead of the check's
// declared severity.
func findingAt(status, format string, args ...interface{}) doctorFinding {
	return doctorFinding{reported: true, status: status, detail: fmt.Sprintf(format, args...)}
}

// resolveFinding turns a check plus its finding into the status doctor
// reports, and whether it reports anything at all. Passing ok wins over the
// declared severity; a finding's own status overrides it.
func resolveFinding(c doctorCheck, f doctorFinding) (string, bool) {
	if !f.reported {
		return "", false
	}
	if f.ok {
		return "ok", true
	}
	if f.status != "" {
		return f.status, true
	}
	return c.Severity, true
}

// doctorCheck is one registered diagnostic.
//
// Severity is the status a failing finding carries: "warn" for advisory
// findings the operator may live with, "error" for ones that mean safegit
// cannot work correctly here.
//
// RequiresInit skips the check entirely when .git/safegit/ has not been
// created yet -- for those checks there is nothing to diagnose, not a passing
// state to report.
type doctorCheck struct {
	Name         string
	Severity     string
	RequiresInit bool
	Fn           func(env doctorEnv) doctorFinding
}

// doctorChecks is the check registry: doctor runs exactly these, in order.
// Adding a diagnostic is one entry plus one function, never an edit to the
// reporting loop.
var doctorChecks = []doctorCheck{
	{Name: "initialized", Severity: "error", Fn: checkInitialized},
	{Name: "tmp_dirs", Severity: "warn", RequiresInit: true, Fn: checkTmpDirs},
	{Name: "stale_locks", Severity: "warn", RequiresInit: true, Fn: checkStaleLocks},
	{Name: "config", Severity: "warn", RequiresInit: true, Fn: checkConfig},
	{Name: "oplog", Severity: "error", RequiresInit: true, Fn: checkOplog},
	{Name: "bypass_detect", Severity: "warn", RequiresInit: true, Fn: checkBypassDetect},
	{Name: "filesystem", Severity: "warn", Fn: checkFilesystemRegistered},
	// Not RequiresInit: what a repository CONTAINS is readable whether or not
	// safegit has state here yet, and an unreadable answer is the same problem
	// either way.
	{Name: "submodules", Severity: "error", Fn: checkSubmodules},
	{Name: "hook_perms", Severity: "error", RequiresInit: true, Fn: checkHookPerms},
	// Not RequiresInit: hooks can sit in the pre-migration location in a
	// repository whose .git/safegit was later removed, and a push there
	// re-creates the state directory and then refuses on exactly this.
	{Name: "hooks_migrated", Severity: "error", Fn: checkHooksMigrated},
	{Name: "native_hooks", Severity: "warn", Fn: checkUnusedNativeHooks},
	{Name: "git_version", Severity: "warn", Fn: checkGitVersion},
	// Not RequiresInit: MERGE_AUTOSTASH is git's own file, and the work it names
	// is unreachable whether or not safegit has state in this repository.
	{Name: "merge_autostash", Severity: "warn", Fn: checkOrphanedAutostash},
	{Name: "legacy_scrub_policies", Severity: "error", RequiresInit: true, Fn: checkLegacyScrubPolicies},
}

// legacyScrubPolicyFile is the JSONL policy log older published safegit
// versions wrote under .git/safegit/ after every `scrub match` and `scrub run`.
//
// Nothing reads it any more -- `scrub verify` is stateless and takes its
// patterns from the command line -- but a repository that was scrubbed by one
// of those versions still has the file, and every line of it holds the regex
// that scrub was given, which for a secret scrub is the secret itself, sitting
// in plaintext inside the repository the scrub was run to clean.
const legacyScrubPolicyFile = "scrub-policies.jsonl"

// printUninstallPlan enumerates what an uninstall is about to remove, one path
// per line, before anything is removed and whether or not this is a dry run.
//
// Uninstall is repository-wide: from a linked worktree it takes the main
// worktree's state and every other worktree's too. That is the part an operator
// has no reason to expect, so those entries are marked in the line itself
// rather than left to be inferred from the paths.
//
// It goes through outf: --quiet is about progress chatter, and a list of
// directories that are about to be deleted is the command's own statement of
// what it does. Machine mode suppresses it, where the envelope's preview
// carries the same set.
func printUninstallPlan(flags globalFlags, targets []repo.UninstallTarget) {
	verb := "will remove"
	if flags.dryRun {
		verb = "would remove"
	}
	outf(flags, "safegit uninstall %s %d path(s) from this repository:\n", verb, len(targets))
	foreign := 0
	for _, t := range targets {
		note := ""
		if t.Foreign {
			note = " -- NOT the worktree you are in"
			foreign++
		}
		outf(flags, "  %s  (%s%s)\n", t.Path, t.Label, note)
	}
	if foreign > 0 {
		outf(flags, "Includes %d path(s) outside the worktree you are in; uninstall is repository-wide.\n", foreign)
	}
}

// runDoctor returns the process exit code. A declined confirmation is a
// refusal, not a success: it exits nonzero so a script or agent cannot read
// "aborted" as "done".
func runDoctor(flags globalFlags, kwargs map[string]interface{}) int {
	// --action is required and closed over its three declared choices, so the
	// framework refuses anything else before dispatch. `diagnose` is the
	// read-only mode and needs no branch of its own: it is what the rest of
	// this function does.
	action := kwargs["action"].(string)
	fix := action == "fix"
	uninstall := action == "uninstall"

	gitDir := mustGitDir()

	// --uninstall: remove safegit from this REPOSITORY -- every worktree's state
	// directory and the shared store -- and exit.
	if uninstall {
		targets, err := repo.UninstallPlan(flags.ctx(), gitDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitcode.General
		}
		// The enumeration comes BEFORE the confirmation, because it is what the
		// confirmation is about: an operator standing in one worktree is
		// consenting to the removal of every other worktree's state too, and
		// cannot consent to what they have not been shown.
		printUninstallPlan(flags, targets)

		// doctor is not consequential at command granularity -- `--action diagnose` only
		// reads -- so the framework never prompts here and this seam is the only
		// gate. The condition is the --uninstall flag the caller typed, so the
		// blanket --approve-consequential is exactly the right consent for it.
		uninstallConsent := consent{granted: flags.approved, flag: "--approve-consequential"}
		if !confirmDeliberate(flags, uninstallConsent, "Remove safegit from this repository?") {
			infof(flags, "Aborted.\n")
			return exitcode.General
		}
		// Through the effects handle, so --dry-run records each removal instead
		// of performing it and the enumeration above is the whole of what a
		// preview does.
		fx := flags.effects()
		for _, t := range targets {
			if _, err := fx.Remove(t.Path, strictcli.Resource("safegit-state:"+t.Path)); err != nil {
				fmt.Fprintf(os.Stderr, "error: removing %s: %v\n", t.Path, err)
				return exitcode.General
			}
		}
		if !flags.silent() && !flags.dryRun {
			fmt.Println("safegit uninstalled")
		}
		return 0
	}

	ctx := flags.ctx()

	env := doctorEnv{
		ctx:          ctx,
		worktree:     flags.root.resolve(),
		gitDir:       gitDir,
		sharedGitDir: repo.SharedGitDir(ctx, gitDir),
		sgDir:        repo.SafegitDir(gitDir),
		inited:       repo.IsInitialized(gitDir),
	}

	checks := runDoctorChecks(env, flags.verbose)

	allOK := true
	for _, c := range checks {
		icon := "OK"
		switch c.Status {
		case "warn":
			icon = "WARN"
			allOK = false
		case "error":
			icon = "FAIL"
			allOK = false
		}
		if c.Detail != "" {
			outf(flags, "[%s] %s: %s\n", icon, c.Name, c.Detail)
		} else {
			outf(flags, "[%s] %s\n", icon, c.Name)
		}
	}
	if allOK && !flags.silent() {
		outf(flags, "all checks passed\n")
	}

	failed := failingChecks(checks)

	// --fix: run garbage collection and cleanup (formerly `safegit gc`).
	if fix && repo.IsInitialized(gitDir) {
		doctorFix(ctx, flags, gitDir)
		// The exit code answers "is this repository still broken", so after a
		// real fix it is decided by what the fix LEFT: a finding the cleanup
		// repaired must not keep the exit nonzero, and one it could not repair
		// must. A dry run repaired nothing, so its answer is the one above.
		if !flags.dryRun {
			env.inited = repo.IsInitialized(gitDir)
			failed = failingChecks(runDoctorChecks(env, false))
			if len(failed) > 0 {
				fmt.Fprintf(os.Stderr, "still failing after --action fix: %s\n", strings.Join(failed, ", "))
			}
		}
	}

	if len(failed) > 0 {
		return exitcode.DoctorFindings
	}
	return 0
}

// runDoctorChecks runs the registry once and returns what it reported.
func runDoctorChecks(env doctorEnv, verbose bool) []checkResult {
	var checks []checkResult
	for _, c := range doctorChecks {
		if c.RequiresInit && !env.inited {
			continue
		}
		checkStart := time.Now()
		f := c.Fn(env)
		if status, reported := resolveFinding(c, f); reported {
			checks = append(checks, checkResult{Name: c.Name, Status: status, Detail: f.detail})
		}
		if verbose {
			fmt.Fprintf(os.Stderr, "  checked: %s (%v)\n", c.Name, time.Since(checkStart))
		}
	}
	return checks
}

// failingChecks names the ERROR-severity checks that failed.
//
// Warnings are deliberately not counted. A warn-severity finding is one an
// operator may live with -- an orphan tmp dir, a hook of git's own that safegit
// does not run -- and a doctor that exited nonzero for those would make the
// nonzero exit meaningless in exactly the repositories where it should mean
// something.
func failingChecks(checks []checkResult) []string {
	var failed []string
	for _, c := range checks {
		if c.Status == "error" {
			failed = append(failed, c.Name)
		}
	}
	return failed
}

// --- registered checks ------------------------------------------------------
//
// One function per entry in doctorChecks. Each reads only its doctorEnv and
// returns one finding, so checks never see each other.

func checkInitialized(env doctorEnv) doctorFinding {
	if env.inited {
		return findingOK("")
	}
	return findingFail("not initialized (run any safegit command to auto-init)")
}

func checkTmpDirs(env doctorEnv) doctorFinding {
	orphans, err := index.GarbageCollectDryRun(env.sgDir)
	if err != nil {
		return findingFail("%v", err)
	}
	if len(orphans) > 0 {
		return findingFail("%d orphan tmp dir(s) found (run 'safegit doctor --action fix' to clean)", len(orphans))
	}
	return findingOK("")
}

// checkStaleLocks scans BOTH lock trees -- the shared one holding ref and
// rewrite locks, and this worktree's own holding its operation lock -- and
// names what it found, so the finding tells the operator what to release rather
// than only how many things are wrong.
func checkStaleLocks(env doctorEnv) doctorFinding {
	found := scanLocks(lockTrees(env.ctx, env.gitDir))
	var parts []string
	if len(found.Stale) > 0 {
		parts = append(parts, fmt.Sprintf("%d stale lock(s): %s", len(found.Stale), strings.Join(found.Stale, ", ")))
	}
	if found.Temps > 0 {
		parts = append(parts, fmt.Sprintf("%d orphaned lock-publication temp file(s)", found.Temps))
	}
	if len(parts) > 0 {
		return findingFail("%s (run 'safegit doctor --action fix' to clean)", strings.Join(parts, "; "))
	}
	return findingOK("")
}

func checkConfig(env doctorEnv) doctorFinding {
	cfg, err := repo.LoadConfig(env.gitDir)
	if err != nil {
		// An unreadable config is not advisory: every command reads it.
		return findingAt("error", "%v", err)
	}
	if cfg.SchemaVersion != 1 {
		return findingFail("unknown schema version %d", cfg.SchemaVersion)
	}
	return findingOK("")
}

// checkOplog reports whether the operation log reads back completely. Lines
// that do not parse mean recorded operations that can no longer be read, which
// is what undo and bypass detection both depend on.
func checkOplog(env doctorEnv) doctorFinding {
	entries, skipped, err := oplog.Read(env.sgDir)
	if err != nil {
		return findingFail("reading %s: %v", oplog.Path(env.sgDir), err)
	}
	if skipped > 0 {
		return findingFail("%d unparseable line(s) in %s; undo refuses on this log", skipped, oplog.Path(env.sgDir))
	}
	return findingOK(fmt.Sprintf("%d entries", len(entries)))
}

// checkBypassDetect compares the oplog's last ref-update against the actual
// tip: a divergence means something other than safegit moved the ref.
//
// A corrupted oplog, or a ref the oplog names that no longer resolves, makes
// the comparison impossible, and that is exactly when the answer is most
// wanted -- so both are reported as failing findings of this check rather than
// silently disabling it. The one silent case is a HEAD that names no ref at
// all -- a detached HEAD -- where there is no precondition to check rather
// than a failure to report. (An UNBORN branch does name a ref, so it reaches
// the resolve below; it only reports when the oplog also holds a tip for that
// ref, which is the branch-deleted-outside-safegit case.)
func checkBypassDetect(env doctorEnv) doctorFinding {
	ref, refErr := git.HeadRef(env.ctx)
	if refErr != nil || ref == "" {
		return findingNone()
	}
	lastEntry, entryErr := oplog.LastRefUpdate(env.sgDir, ref)
	if entryErr != nil {
		return findingAt("error", "cannot compare %s against the oplog: %v", refShortName(ref), entryErr)
	}
	if lastEntry == nil {
		return findingOK("no oplog entries for current ref")
	}
	sha := oplog.TipSHA(lastEntry.Extra)
	if sha == "" {
		return findingNone()
	}
	tipSHA, tipErr := git.RevParse(env.ctx, ref)
	if tipErr != nil {
		// The oplog says safegit put a tip on this ref, so the ref refusing to
		// resolve is itself the bypass signal -- a branch deleted or reset
		// outside safegit is exactly what this check exists to notice. Staying
		// silent here would disable the check precisely when it has something
		// to say.
		return findingAt("error", "cannot resolve %s, which the oplog records a tip for: %v", refShortName(ref), tipErr)
	}
	if tipSHA != sha {
		return findingFail("tip of %s (%s) diverged from last oplog entry (%s); raw git may have been used", refShortName(ref), tipSHA[:8], sha[:8])
	}
	return findingOK("")
}

// checkFilesystemRegistered adapts the platform-specific network-filesystem
// probe to the registry's finding shape.
func checkFilesystemRegistered(env doctorEnv) doctorFinding {
	r := checkFilesystem(env.gitDir)
	if r.Status == "ok" {
		return findingOK(r.Detail)
	}
	return findingAt(r.Status, "%s", r.Detail)
}

// checkSubmodules reports a submodule enumeration safegit cannot perform.
//
// Everything that has to know what this repository contains asks the same
// enumerator: `doctor --action fix` cleans each submodule's own state directory
// with it, and a scrub decides what it rewrites with it. A failure is therefore
// not advice -- the answer to it is a repair, never a smaller scope quietly
// taken -- so it reports at error severity and the exit code carries it.
//
// A repository with no submodules has no precondition to report on and says
// nothing, rather than adding a line to every doctor run.
func checkSubmodules(env doctorEnv) doctorFinding {
	subs, err := submodule.Enumerate(env.ctx, env.gitDir)
	if err != nil {
		return findingFail("enumerating submodules: %v", err)
	}
	if len(subs) == 0 {
		return findingNone()
	}
	return findingOK(fmt.Sprintf("%d enumerated", len(subs)))
}

// checkHookPerms reports hooks whose mode says they cannot run.
//
// It reads the location enumerator, so it sees exactly the set discovery sees
// -- both stores, at any depth -- rather than re-deriving one directory's
// layout and going quietly blind to the rest. Both stores report at ERROR
// severity, because push refuses on either: a repository holding one of these
// is stopped rather than degraded, and reporting that as advice would
// understate a push that is already failing. The two branches differ only in
// the remedy they state -- the checkout-provided store needs the mode change
// committed as well.
func checkHookPerms(env doctorEnv) doctorFinding {
	locations, err := hooks.Enumerate(hooks.Store{Worktree: env.worktree, SharedGitDir: env.sharedGitDir})
	if err != nil {
		return findingFail("%v", err)
	}
	if len(locations) == 0 {
		// No hooks anywhere: nothing to report either way.
		return findingNone()
	}
	var local, tracked []string
	for _, loc := range locations {
		if loc.Executable || !loc.IsHookName() || loc.Origin == hooks.OriginLegacy {
			continue
		}
		if loc.Origin == hooks.OriginTracked {
			tracked = append(tracked, loc.Rel)
		} else {
			local = append(local, loc.Rel)
		}
	}
	if len(tracked) > 0 {
		return findingFail("%d hook(s) in the checkout's .safegit/hooks are not executable, which every push refuses on: %s (chmod +x and commit the mode change)",
			len(tracked), strings.Join(tracked, ", "))
	}
	if len(local) > 0 {
		return findingFail("%d hook(s) in %s are not executable, which every push refuses on: %s (chmod +x, or remove the hook if it is meant to be gone)",
			len(local), hooks.LocalDir(env.sharedGitDir), strings.Join(local, ", "))
	}
	return findingOK("")
}

// checkHooksMigrated reports hooks still sitting in the pre-migration location.
//
// It is an error rather than advice because of what it costs: every push, and
// every `hook run`, refuses outright while they are there. A repository in that
// state is not degraded, it is stopped, and one command fixes it.
//
// The location is the COMMON git dir's hook directory, which is the same one
// every worktree of the repository refuses on -- reading a linked worktree's own
// git dir found an empty directory and reported the repository healthy.
func checkHooksMigrated(env doctorEnv) doctorFinding {
	legacy, err := hooks.Legacy(env.sharedGitDir)
	if err != nil {
		return findingFail("%v", err)
	}
	if len(legacy) == 0 {
		return findingOK("")
	}
	var names []string
	for _, loc := range legacy {
		names = append(names, loc.Rel)
	}
	return findingFail("%d hook(s) are still in %s: %s (run 'safegit hook migrate'; every push refuses until then)",
		len(legacy), filepath.Join(env.sharedGitDir, "hooks"), strings.Join(names, ", "))
}

// checkUnusedNativeHooks names the repository's own git hooks that safegit
// never executes.
//
// It is a stated fact rather than a fault: git still runs every one of them for
// anyone using git directly, and a repository is free to keep them. What it
// prevents is the silent surprise -- a `prepare-commit-msg` hook that has
// always shaped commit messages simply does not run under `safegit commit`,
// because safegit never opens an editor, and nothing said so until now.
//
// The directory is git's own answer (core.hooksPath and linked worktrees both
// move it), which is the SAME resolution the commit pipeline runs hooks
// through, so the two can never disagree about which files are in question.
func checkUnusedNativeHooks(env doctorEnv) doctorFinding {
	dir, err := git.HooksDir(env.ctx)
	if err != nil {
		return findingFail("resolving git's hook directory: %v", err)
	}
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		// No hook directory at all: nothing to report either way.
		return findingNone()
	}

	runs := map[string]bool{}
	for _, name := range commit.NativeHooks() {
		runs[name] = true
	}

	var unused []string
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir(), strings.HasSuffix(name, ".sample"),
			strings.HasPrefix(name, "."), strings.HasSuffix(name, "~"),
			runs[name],
			// safegit's own former hook names are the migration check's
			// subject, not this one's.
			name == "pre-pre-push":
			continue
		}
		info, sErr := e.Info()
		if sErr != nil || info.Mode()&0111 == 0 {
			// git ignores a hook it cannot execute, so its absence from
			// safegit's runs is no surprise to report.
			continue
		}
		unused = append(unused, name)
	}
	if len(unused) == 0 {
		return findingOK("")
	}
	return findingFail("%d git hook(s) in %s that safegit never runs: %s (safegit runs only %s; git still runs the rest when you use git directly)",
		len(unused), dir, strings.Join(unused, ", "), strings.Join(commit.NativeHooks(), ", "))
}

// checkGitVersion reports the git version safegit found against the highest
// floor any safegit feature declares, so an operator learns about a too-old
// git here rather than from the one command that needs it.
func checkGitVersion(env doctorEnv) doctorFinding {
	v, err := git.Version(env.ctx)
	if err != nil {
		return findingFail("%v", err)
	}
	highest := gitversion.HighestFloor()
	if v.Before(highest.Floor) {
		return findingFail("git %s is older than %s, required by %s", v, highest.Floor, highest.Name)
	}
	return findingOK(fmt.Sprintf("git %s (highest feature floor: %s for %s)", v, highest.Floor, highest.Name))
}

// checkOrphanedAutostash reports a MERGE_AUTOSTASH with no merge in flight.
//
// The file names a stash-shaped commit holding uncommitted work, and it is the
// ONE piece of a merge's state whose content lives nowhere else -- no branch
// reaches it, `git stash list` does not show it, and a `git gc` that prunes it
// takes the work with it. A merge that was abandoned, or a conclusion that was
// killed before it consumed the file, leaves exactly this.
//
// It reports rather than repairs, and deliberately removes nothing: a diagnosis
// that quietly deleted the only name a piece of work has left would be the loss
// it exists to prevent.
func checkOrphanedAutostash(env doctorEnv) doctorFinding {
	path := filepath.Join(env.gitDir, sequencer.FileMergeAutostash)
	if _, err := os.Stat(path); err != nil {
		// No file: nothing to say, rather than a state to report as healthy.
		return findingNone()
	}
	state, err := sequencer.Read(env.gitDir)
	if err != nil {
		return findingFail("%s is present and git's in-flight state could not be read: %v",
			sequencer.FileMergeAutostash, err)
	}
	if state.Kind == sequencer.KindMerge {
		return findingOK(fmt.Sprintf("%s belongs to the merge in flight", sequencer.FileMergeAutostash))
	}

	sha := "an unreadable object name"
	if raw, err := os.ReadFile(path); err == nil {
		if trimmed := strings.TrimSpace(string(raw)); trimmed != "" {
			sha = trimmed
		}
	}
	return findingFail("%s is present with no merge in flight: it names %s, a stash-shaped commit whose "+
		"content no ref reaches (recover it with 'git stash apply %s', then remove %s)",
		sequencer.FileMergeAutostash, sha, sha, path)
}

// checkLegacyScrubPolicies reports a leftover scrub-policies.jsonl.
//
// It is an error rather than a warning because of what the file CONTAINS: one
// verbatim scrub pattern per line. A repository that was scrubbed to remove a
// credential is very likely holding that credential in this file, so leaving it
// in place keeps the leak the scrub was run to end.
func checkLegacyScrubPolicies(env doctorEnv) doctorFinding {
	p := filepath.Join(env.sgDir, legacyScrubPolicyFile)
	if _, err := os.Stat(p); err != nil {
		return findingNone()
	}
	return findingFail("%s is left over from an older safegit; nothing reads it and every line holds a verbatim scrub pattern, which for a secret scrub is the secret itself (run 'safegit doctor --action fix' to delete it)", p)
}

// doctorFix performs cleanup: orphan tmp dirs, legacy queue dir, the legacy
// scrub-policy file and stale locks. With --dry-run it only reports what would
// be done.
func doctorFix(ctx context.Context, flags globalFlags, gitDir string) {
	sgDir := repo.SafegitDir(gitDir)

	if flags.dryRun {
		orphanDirs, err := index.GarbageCollectDryRun(sgDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(exitcode.General)
		}

		// Check for legacy queue directory.
		queueDir := filepath.Join(sgDir, "queue")
		hasLegacyQueue := false
		if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
			hasLegacyQueue = true
		}

		// Check for the legacy scrub-policy file.
		legacyPolicies := filepath.Join(sgDir, legacyScrubPolicyFile)
		hasLegacyPolicies := false
		if _, err := os.Stat(legacyPolicies); err == nil {
			hasLegacyPolicies = true
		}

		// Both lock trees: the shared one and this worktree's own.
		found := scanLocks(lockTrees(ctx, gitDir))

		if !flags.silent() {
			fmt.Printf("would remove %d orphan tmp dir(s)\n", len(orphanDirs))
			if hasLegacyQueue {
				fmt.Println("would remove legacy queue directory")
			}
			if hasLegacyPolicies {
				fmt.Printf("would remove legacy scrub-policy file %s\n", legacyPolicies)
			}
			if len(found.Stale) > 0 {
				fmt.Printf("would remove %d stale lock(s): %s\n", len(found.Stale), strings.Join(found.Stale, ", "))
			}
			if found.Temps > 0 {
				fmt.Printf("would remove %d orphaned lock-publication temp file(s)\n", found.Temps)
			}
		}
	} else {
		// Actual cleanup.
		removed, err := index.GarbageCollect(sgDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(exitcode.General)
		}

		// Clean up legacy queue directory (removed in v0.2).
		queueDir := filepath.Join(sgDir, "queue")
		queueRemoved := false
		if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
			os.RemoveAll(queueDir)
			queueRemoved = true
		}

		// Delete the legacy scrub-policy file. It is removed rather than
		// migrated: nothing reads it, and its content is exactly what should
		// not be sitting on disk.
		legacyPolicies := filepath.Join(sgDir, legacyScrubPolicyFile)
		policiesRemoved := false
		if _, err := os.Stat(legacyPolicies); err == nil {
			if rmErr := os.Remove(legacyPolicies); rmErr != nil {
				fmt.Fprintf(os.Stderr, "warning: removing %s: %v\n", legacyPolicies, rmErr)
			} else {
				policiesRemoved = true
			}
		}

		// Both lock trees: the shared one and this worktree's own.
		cleaned := cleanLocks(lockTrees(ctx, gitDir))

		if !flags.silent() {
			fmt.Printf("removed %d orphan tmp dir(s)\n", removed)
			if queueRemoved {
				fmt.Println("removed legacy queue directory")
			}
			if policiesRemoved {
				fmt.Printf("removed legacy scrub-policy file %s\n", legacyPolicies)
			}
			if len(cleaned.Stale) > 0 {
				fmt.Printf("removed %d stale lock(s): %s\n", len(cleaned.Stale), strings.Join(cleaned.Stale, ", "))
			}
			if cleaned.Temps > 0 {
				fmt.Printf("removed %d orphaned lock-publication temp file(s)\n", cleaned.Temps)
			}
		}
	}

	// Submodule safegit directory cleanup (runs in both dry-run and normal mode;
	// doctorFixSubmodule handles dry-run internally).
	// A failed enumeration empties this cleanup's scope: nothing below runs, and
	// the run would otherwise report the repairs it did make with no sign that
	// the submodule half never happened. It is stated as an error, unconditional
	// of --quiet (which is about progress chatter), and the `submodules` check
	// carries the same failure into the exit code.
	submodules, enumErr := submodule.Enumerate(ctx, gitDir)
	if enumErr != nil {
		fmt.Fprintf(os.Stderr, "error: enumerating submodules: %v\n"+
			"No submodule state directory was cleaned.\n", enumErr)
	}
	for _, sub := range submodules {
		if _, err := os.Stat(sub.SafegitDir); os.IsNotExist(err) {
			continue
		}
		doctorFixSubmodule(flags, sub.Name, sub.SafegitDir)
	}
}

// doctorFixSubmodule cleans orphan tmp dirs and stale locks in a submodule's
// safegit directory.
//
// One directory rather than two: a submodule is enumerated by its own safegit
// dir, and a submodule that is itself checked out into linked worktrees is
// reached by running doctor inside it.
func doctorFixSubmodule(flags globalFlags, name, sgDir string) {
	dirs := []string{sgDir}

	if flags.dryRun {
		orphans, err := index.GarbageCollectDryRun(sgDir)
		if err != nil && !flags.silent() {
			fmt.Fprintf(os.Stderr, "warning: [%s] scanning orphan tmp dirs: %v\n", name, err)
		}
		found := scanLocks(dirs)
		if !flags.silent() {
			if len(orphans) > 0 {
				fmt.Printf("[%s] would remove %d orphan tmp dir(s)\n", name, len(orphans))
			}
			if len(found.Stale) > 0 {
				fmt.Printf("[%s] would remove %d stale lock(s): %s\n", name, len(found.Stale), strings.Join(found.Stale, ", "))
			}
			if found.Temps > 0 {
				fmt.Printf("[%s] would remove %d orphaned lock-publication temp file(s)\n", name, found.Temps)
			}
		}
		return
	}

	removed, err := index.GarbageCollect(sgDir)
	if err != nil && !flags.silent() {
		fmt.Fprintf(os.Stderr, "warning: [%s] cleaning orphan tmp dirs: %v\n", name, err)
	}
	cleaned := cleanLocks(dirs)

	if !flags.silent() {
		if removed > 0 {
			fmt.Printf("[%s] removed %d orphan tmp dir(s)\n", name, removed)
		}
		if len(cleaned.Stale) > 0 {
			fmt.Printf("[%s] removed %d stale lock(s): %s\n", name, len(cleaned.Stale), strings.Join(cleaned.Stale, ", "))
		}
		if cleaned.Temps > 0 {
			fmt.Printf("[%s] removed %d orphaned lock-publication temp file(s)\n", name, cleaned.Temps)
		}
	}
}

// publicationTempGrace is how long a lock-publication temporary file must have
// sat untouched before doctor calls it orphaned.
//
// Publication is create, write, chmod, close, link -- microseconds. A temp file
// older than this was left by a process that died in the middle of it. The grace
// exists only so that doctor can never delete a temp file another process is
// publishing through RIGHT NOW, which would turn that process's link(2) into a
// spurious hard failure. The staleness check is the primary evidence; this is
// the belt to its braces.
const publicationTempGrace = 5 * time.Minute

// lockScan is what one walk of the lock trees found.
type lockScan struct {
	// Stale names the locks whose holder is gone, in the same vocabulary
	// `safegit unlock` accepts, so the finding tells an operator what to type.
	Stale []string
	// Temps counts orphaned lock-publication temporary files: a kill between
	// creating one and linking it into place leaves a file that no lock walk
	// sees and that nothing ever cleans up.
	Temps int
}

// lockTrees returns every safegit directory whose locks/ subtree belongs to this
// repository, deduplicated.
//
// There are two, and outside a linked worktree they are the same directory: the
// SHARED one, which holds ref locks and the repository-wide rewrite lock so
// every worktree contends on one file, and the WORKTREE-LOCAL one, which holds
// this worktree's operation lock. A scan of only the shared tree -- which is
// what doctor did before the operation lock existed -- silently reports a
// worktree with a crashed operation as healthy.
func lockTrees(ctx context.Context, gitDir string) []string {
	shared := repo.SharedSafegitDir(ctx, gitDir)
	local := repo.SafegitDir(gitDir)
	if local == shared {
		return []string{shared}
	}
	return []string{shared, local}
}

// scanLocks walks every given lock tree and reports what it found. It removes
// nothing.
func scanLocks(dirs []string) lockScan {
	var found lockScan
	for _, sgDir := range dirs {
		locksRoot := filepath.Join(sgDir, "locks")
		_ = filepath.Walk(locksRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			switch {
			case lock.IsLockFile(info.Name()):
				if lock.IsStale(path) {
					name := lock.NameFromPath(sgDir, path)
					if name == "" {
						name = path
					}
					found.Stale = append(found.Stale, name)
				}
			case lock.IsPublicationTemp(info.Name()):
				if isOrphanedPublicationTemp(path, info) {
					found.Temps++
				}
			}
			return nil
		})
	}
	return found
}

// isOrphanedPublicationTemp reports whether a publication temporary file was
// left behind by a process that is gone, rather than being one a live process is
// publishing through at this moment.
//
// Both conditions must hold: the record in the file names no live holder (temps
// carry the same owner record the lock will, because the record is written
// before the link), and the file has sat untouched past the grace period.
func isOrphanedPublicationTemp(path string, info os.FileInfo) bool {
	if !lock.IsStale(path) {
		return false
	}
	return time.Since(info.ModTime()) > publicationTempGrace
}

// cleanLocks removes what scanLocks found: stale locks through the reclamation
// authority, orphaned publication temps directly.
//
// The stale locks go through lock.ReclaimIfStale rather than a bare os.Remove
// because doctor sweeps unattended: between judging a lock stale and deleting
// it, another process can reclaim that same lock and publish its own live one
// at the path, and the bare remove would delete THAT -- leaving two processes
// believing they hold the same ref. ReclaimIfStale re-judges under the lock
// file's own flock and against the open descriptor's inode, so it can only ever
// remove the exact stale file it judged.
//
// A temp file needs no such care: nothing acquires it, and the orphan test is
// what keeps a live publication out of the sweep.
func cleanLocks(dirs []string) lockScan {
	var removed lockScan
	for _, sgDir := range dirs {
		locksRoot := filepath.Join(sgDir, "locks")
		_ = filepath.Walk(locksRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			switch {
			case lock.IsLockFile(info.Name()):
				if lock.ReclaimIfStale(path) {
					name := lock.NameFromPath(sgDir, path)
					if name == "" {
						name = path
					}
					removed.Stale = append(removed.Stale, name)
				}
			case lock.IsPublicationTemp(info.Name()):
				if isOrphanedPublicationTemp(path, info) && os.Remove(path) == nil {
					removed.Temps++
				}
			}
			return nil
		})
	}
	return removed
}
