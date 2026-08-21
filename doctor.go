package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitversion"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/submodule"
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
	ctx    context.Context
	flags  globalFlags
	gitDir string
	sgDir  string
	inited bool
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
	{Name: "hook_perms", Severity: "warn", RequiresInit: true, Fn: checkHookPerms},
	{Name: "git_version", Severity: "warn", Fn: checkGitVersion},
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

	// --uninstall: remove safegit from this repo and exit.
	if uninstall {
		// doctor is not consequential at command granularity -- `--action diagnose` only
		// reads -- so the framework never prompts here and this seam is the only
		// gate. The condition is the --uninstall flag the caller typed, so the
		// blanket --approve-consequential is exactly the right consent for it.
		uninstallConsent := consent{granted: flags.approved, flag: "--approve-consequential"}
		if !confirmDeliberate(flags, uninstallConsent, "Remove safegit from this repository?") {
			infof(flags, "Aborted.\n")
			return 1
		}
		if err := repo.Uninstall(gitDir); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if !flags.silent() {
			fmt.Println("safegit uninstalled")
		}
		return 0
	}

	ctx := flags.ctx()

	env := doctorEnv{
		ctx:    ctx,
		flags:  flags,
		gitDir: gitDir,
		sgDir:  repo.SafegitDir(gitDir),
		inited: repo.IsInitialized(gitDir),
	}

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
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  checked: %s (%v)\n", c.Name, time.Since(checkStart))
		}
	}

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

	// --fix: run garbage collection and cleanup (formerly `safegit gc`).
	if fix && repo.IsInitialized(gitDir) {
		doctorFix(ctx, flags, gitDir)
	}
	return 0
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

// checkStaleLocks scans the shared safegit dir so worktree locks are found.
func checkStaleLocks(env doctorEnv) doctorFinding {
	staleCount := countStaleLocks(repo.SharedSafegitDir(env.ctx, env.gitDir))
	if staleCount > 0 {
		return findingFail("%d stale lock(s) found", staleCount)
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
// A corrupted oplog makes the comparison impossible, and that is exactly when
// the answer is most wanted -- so it is reported as a failing finding of this
// check rather than silently disabling it.
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
		return findingNone()
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

// checkHookPerms reports non-executable hooks in pre-pre-push.d/, which git
// would silently never run.
func checkHookPerms(env doctorEnv) doctorFinding {
	hookDir := filepath.Join(env.gitDir, "hooks", "pre-pre-push.d")
	entries, readErr := os.ReadDir(hookDir)
	if readErr != nil {
		// No hook directory at all: nothing to report either way.
		return findingNone()
	}
	var nonExec []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasSuffix(e.Name(), "~") {
			continue
		}
		info, sErr := e.Info()
		if sErr != nil {
			continue
		}
		if info.Mode()&0111 == 0 {
			nonExec = append(nonExec, e.Name())
		}
	}
	if len(nonExec) > 0 {
		return findingFail("%d non-executable hook(s) in pre-pre-push.d/: %s", len(nonExec), strings.Join(nonExec, ", "))
	}
	return findingOK("")
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

// doctorFix performs cleanup: orphan tmp dirs, legacy queue dir and stale
// locks. With --dry-run it only reports what would be done.
func doctorFix(ctx context.Context, flags globalFlags, gitDir string) {
	sgDir := repo.SafegitDir(gitDir)

	if flags.dryRun {
		orphanDirs, err := index.GarbageCollectDryRun(sgDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		// Check for legacy queue directory.
		queueDir := filepath.Join(sgDir, "queue")
		hasLegacyQueue := false
		if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
			hasLegacyQueue = true
		}

		// Count stale locks in the shared safegit dir (covers worktrees).
		sharedDir := repo.SharedSafegitDir(ctx, gitDir)
		staleLocks := countStaleLocks(sharedDir)

		if !flags.silent() {
			fmt.Printf("would remove %d orphan tmp dir(s)\n", len(orphanDirs))
			if hasLegacyQueue {
				fmt.Println("would remove legacy queue directory")
			}
			if staleLocks > 0 {
				fmt.Printf("would remove %d stale lock(s)\n", staleLocks)
			}
		}
	} else {
		// Actual cleanup.
		removed, err := index.GarbageCollect(sgDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		// Clean up legacy queue directory (removed in v0.2).
		queueDir := filepath.Join(sgDir, "queue")
		queueRemoved := false
		if info, err := os.Stat(queueDir); err == nil && info.IsDir() {
			os.RemoveAll(queueDir)
			queueRemoved = true
		}

		// Clean stale locks in the shared safegit dir (covers worktrees).
		sharedDir := repo.SharedSafegitDir(ctx, gitDir)
		staleCleaned := removeStaleLocks(sharedDir)

		if !flags.silent() {
			fmt.Printf("removed %d orphan tmp dir(s)\n", removed)
			if queueRemoved {
				fmt.Println("removed legacy queue directory")
			}
			if staleCleaned > 0 {
				fmt.Printf("removed %d stale lock(s)\n", staleCleaned)
			}
		}
	}

	// Submodule safegit directory cleanup (runs in both dry-run and normal mode;
	// doctorFixSubmodule handles dry-run internally).
	submodules, enumErr := submodule.Enumerate(ctx, gitDir)
	if enumErr != nil && !flags.silent() {
		fmt.Fprintf(os.Stderr, "warning: enumerating submodules: %v\n", enumErr)
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
func doctorFixSubmodule(flags globalFlags, name, sgDir string) {
	if flags.dryRun {
		orphans, err := index.GarbageCollectDryRun(sgDir)
		if err != nil && !flags.silent() {
			fmt.Fprintf(os.Stderr, "warning: [%s] scanning orphan tmp dirs: %v\n", name, err)
		}
		staleLocks := countStaleLocks(sgDir)
		if !flags.silent() {
			if len(orphans) > 0 {
				fmt.Printf("[%s] would remove %d orphan tmp dir(s)\n", name, len(orphans))
			}
			if staleLocks > 0 {
				fmt.Printf("[%s] would remove %d stale lock(s)\n", name, staleLocks)
			}
		}
		return
	}

	removed, err := index.GarbageCollect(sgDir)
	if err != nil && !flags.silent() {
		fmt.Fprintf(os.Stderr, "warning: [%s] cleaning orphan tmp dirs: %v\n", name, err)
	}
	staleCleaned := removeStaleLocks(sgDir)

	if !flags.silent() {
		if removed > 0 {
			fmt.Printf("[%s] removed %d orphan tmp dir(s)\n", name, removed)
		}
		if staleCleaned > 0 {
			fmt.Printf("[%s] removed %d stale lock(s)\n", name, staleCleaned)
		}
	}
}

// countStaleLocks counts stale lock files under sgDir/locks/.
func countStaleLocks(sgDir string) int {
	count := 0
	locksRoot := filepath.Join(sgDir, "locks")
	_ = filepath.Walk(locksRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".lock") {
			return nil
		}
		stale, sErr := lock.IsStale(path)
		if sErr == nil && stale {
			count++
		}
		return nil
	})
	return count
}

// removeStaleLocks removes stale lock files under sgDir/locks/ and returns the
// count removed.
func removeStaleLocks(sgDir string) int {
	removed := 0
	locksRoot := filepath.Join(sgDir, "locks")
	_ = filepath.Walk(locksRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".lock") {
			return nil
		}
		stale, sErr := lock.IsStale(path)
		if sErr == nil && stale {
			if os.Remove(path) == nil {
				removed++
			}
		}
		return nil
	})
	return removed
}
