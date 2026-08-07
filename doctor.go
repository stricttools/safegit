package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
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

// runDoctor returns the process exit code. A declined confirmation is a
// refusal, not a success: it exits nonzero so a script or agent cannot read
// "aborted" as "done".
func runDoctor(flags globalFlags, kwargs map[string]interface{}) int {
	fix := kwargs["fix"].(bool)
	uninstall := kwargs["uninstall"].(bool)

	gitDir := mustGitDir(flags, "doctor")

	// --uninstall: remove safegit from this repo and exit.
	if uninstall {
		// doctor is not consequential at command granularity -- --diagnose only
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
		if !flags.quiet {
			fmt.Println("safegit uninstalled")
		}
		return 0
	}

	ctx := context.Background()

	var checks []checkResult
	var checkStart time.Time

	// Check 1: Is safegit initialized?
	checkStart = time.Now()
	if repo.IsInitialized(gitDir) {
		checks = append(checks, checkResult{Name: "initialized", Status: "ok"})
	} else {
		checks = append(checks, checkResult{Name: "initialized", Status: "error", Detail: "not initialized (run any safegit command to auto-init)"})
	}
	if flags.verbose {
		fmt.Fprintf(os.Stderr, "  checked: initialized (%v)\n", time.Since(checkStart))
	}

	sgDir := repo.SafegitDir(gitDir)

	// Check 2: Orphan tmp dirs (report only; --fix cleans them up)
	if repo.IsInitialized(gitDir) {
		checkStart = time.Now()
		orphans, err := index.GarbageCollectDryRun(sgDir)
		if err != nil {
			checks = append(checks, checkResult{Name: "tmp_dirs", Status: "warn", Detail: err.Error()})
		} else if len(orphans) > 0 {
			checks = append(checks, checkResult{
				Name:   "tmp_dirs",
				Status: "warn",
				Detail: fmt.Sprintf("%d orphan tmp dir(s) found (run 'safegit doctor --fix' to clean)", len(orphans)),
			})
		} else {
			checks = append(checks, checkResult{Name: "tmp_dirs", Status: "ok"})
		}
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  checked: tmp_dirs (%v)\n", time.Since(checkStart))
		}
	}

	// Check 3: Stale locks (scan the shared safegit dir so worktree locks are found)
	if repo.IsInitialized(gitDir) {
		checkStart = time.Now()
		sharedDir := repo.SharedSafegitDir(ctx, gitDir)
		staleCount := countStaleLocks(sharedDir)
		if staleCount > 0 {
			checks = append(checks, checkResult{
				Name:   "stale_locks",
				Status: "warn",
				Detail: fmt.Sprintf("%d stale lock(s) found", staleCount),
			})
		} else {
			checks = append(checks, checkResult{Name: "stale_locks", Status: "ok"})
		}
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  checked: stale_locks (%v)\n", time.Since(checkStart))
		}
	}

	// Check 4: Config readable
	if repo.IsInitialized(gitDir) {
		checkStart = time.Now()
		cfg, err := repo.LoadConfig(gitDir)
		if err != nil {
			checks = append(checks, checkResult{Name: "config", Status: "error", Detail: err.Error()})
		} else if cfg.SchemaVersion != 1 {
			checks = append(checks, checkResult{
				Name:   "config",
				Status: "warn",
				Detail: fmt.Sprintf("unknown schema version %d", cfg.SchemaVersion),
			})
		} else {
			checks = append(checks, checkResult{Name: "config", Status: "ok"})
		}
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  checked: config (%v)\n", time.Since(checkStart))
		}
	}

	// Check 6: Raw git bypass detection -- compare oplog's last ref-update against actual tip
	if repo.IsInitialized(gitDir) {
		checkStart = time.Now()
		ref, refErr := git.HeadRef(ctx)
		if refErr == nil && ref != "" {
			lastEntry, entryErr := oplog.LastRefUpdate(sgDir, ref)
			if entryErr == nil && lastEntry != nil {
				if sha := oplog.TipSHA(lastEntry.Extra); sha != "" {
					tipSHA, tipErr := git.RevParse(ctx, ref)
					if tipErr == nil && tipSHA != sha {
						checks = append(checks, checkResult{
							Name:   "bypass_detect",
							Status: "warn",
							Detail: fmt.Sprintf("tip of %s (%s) diverged from last oplog entry (%s); raw git may have been used", refShortName(ref), tipSHA[:8], sha[:8]),
						})
					} else if tipErr == nil {
						checks = append(checks, checkResult{Name: "bypass_detect", Status: "ok"})
					}
				}
			} else if entryErr == nil {
				// No oplog entries for this ref -- skip check
				checks = append(checks, checkResult{Name: "bypass_detect", Status: "ok", Detail: "no oplog entries for current ref"})
			}
		}
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  checked: bypass_detect (%v)\n", time.Since(checkStart))
		}
	}

	// Check 7: NFS/network filesystem detection (platform-specific)
	checkStart = time.Now()
	checks = append(checks, checkFilesystem(gitDir))
	if flags.verbose {
		fmt.Fprintf(os.Stderr, "  checked: filesystem (%v)\n", time.Since(checkStart))
	}

	// Check 8: Non-executable hooks in pre-pre-push.d/
	if repo.IsInitialized(gitDir) {
		checkStart = time.Now()
		hookDir := filepath.Join(gitDir, "hooks", "pre-pre-push.d")
		entries, readErr := os.ReadDir(hookDir)
		if readErr == nil {
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
				checks = append(checks, checkResult{
					Name:   "hook_perms",
					Status: "warn",
					Detail: fmt.Sprintf("%d non-executable hook(s) in pre-pre-push.d/: %s", len(nonExec), strings.Join(nonExec, ", ")),
				})
			} else {
				checks = append(checks, checkResult{Name: "hook_perms", Status: "ok"})
			}
		}
		if flags.verbose {
			fmt.Fprintf(os.Stderr, "  checked: hook_perms (%v)\n", time.Since(checkStart))
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
			fmt.Printf("[%s] %s: %s\n", icon, c.Name, c.Detail)
		} else {
			fmt.Printf("[%s] %s\n", icon, c.Name)
		}
	}
	if allOK && !flags.quiet {
		fmt.Println("all checks passed")
	}

	// --fix: run garbage collection and cleanup (formerly `safegit gc`).
	if fix && repo.IsInitialized(gitDir) {
		doctorFix(ctx, flags, gitDir)
	}
	return 0
}

// doctorFix performs cleanup: orphan tmp dirs, legacy queue dir, and oplog
// rotation. With --dry-run it only reports what would be done.
func doctorFix(ctx context.Context, flags globalFlags, gitDir string) {
	sgDir := repo.SafegitDir(gitDir)
	cfg, _ := loadConfig(flags, gitDir)

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

		logSizeMB := float64(0)
		logSize, _ := oplog.LogSize(sgDir)
		if logSize > 0 {
			logSizeMB = float64(logSize) / (1024 * 1024)
		}
		maxMB := 100
		if cfg != nil && cfg.Log.MaxSizeMB > 0 {
			maxMB = cfg.Log.MaxSizeMB
		}
		wouldRotate := logSize >= int64(maxMB)*1024*1024

		if !flags.quiet {
			fmt.Printf("would remove %d orphan tmp dir(s)\n", len(orphanDirs))
			if hasLegacyQueue {
				fmt.Println("would remove legacy queue directory")
			}
			if staleLocks > 0 {
				fmt.Printf("would remove %d stale lock(s)\n", staleLocks)
			}
			fmt.Printf("log size: %.1f MB (max: %d MB)", logSizeMB, maxMB)
			if wouldRotate {
				fmt.Print(" -- would rotate")
			}
			fmt.Println()
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

		// Log rotation.
		maxMB := 100
		if cfg != nil && cfg.Log.MaxSizeMB > 0 {
			maxMB = cfg.Log.MaxSizeMB
		}
		rotated, rotErr := oplog.Rotate(sgDir, maxMB)
		if rotErr != nil && !flags.quiet {
			fmt.Fprintf(os.Stderr, "warning: log rotation failed: %v\n", rotErr)
		}

		// Clean stale locks in the shared safegit dir (covers worktrees).
		sharedDir := repo.SharedSafegitDir(ctx, gitDir)
		staleCleaned := removeStaleLocks(sharedDir)

		if !flags.quiet {
			fmt.Printf("removed %d orphan tmp dir(s)\n", removed)
			if queueRemoved {
				fmt.Println("removed legacy queue directory")
			}
			if staleCleaned > 0 {
				fmt.Printf("removed %d stale lock(s)\n", staleCleaned)
			}
			if rotated {
				fmt.Println("log rotated (old log saved as log.1)")
			}
		}
	}

	// Submodule safegit directory cleanup (runs in both dry-run and normal mode;
	// doctorFixSubmodule handles dry-run internally).
	submodules, enumErr := submodule.Enumerate(ctx, gitDir)
	if enumErr != nil && !flags.quiet {
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
		if err != nil && !flags.quiet {
			fmt.Fprintf(os.Stderr, "warning: [%s] scanning orphan tmp dirs: %v\n", name, err)
		}
		staleLocks := countStaleLocks(sgDir)
		if !flags.quiet {
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
	if err != nil && !flags.quiet {
		fmt.Fprintf(os.Stderr, "warning: [%s] cleaning orphan tmp dirs: %v\n", name, err)
	}
	staleCleaned := removeStaleLocks(sgDir)

	if !flags.quiet {
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
