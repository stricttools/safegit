package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
)

func runUnlock(flags globalFlags, ref string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	sharedDir := repo.SharedSafegitDir(flags.ctx(), gitDir)

	// Check if lock exists (locks live under the shared safegit dir)
	lp := filepath.Join(sharedDir, "locks", ref+".lock")
	if _, err := os.Stat(lp); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: no lock held on %s\n", ref)
		return exitcode.General
	}

	// Always check liveness -- refuse to release locks held by live processes
	if !lock.IsStale(lp) {
		pid, _ := lock.ParsePID(lp)
		fmt.Fprintf(os.Stderr, "error: lock on %s is held by a live process (pid %d); kill the process or wait for it to finish\n", ref, pid)
		return exitcode.General
	}

	if flags.dryRun {
		if !flags.silent() {
			fmt.Printf("would release lock on %s\n", refShortName(ref))
		}
		return 0
	}

	// Release the lock
	if err := lock.ForceRelease(sharedDir, ref); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	if !flags.silent() {
		fmt.Printf("lock on %s released\n", refShortName(ref))
	}
	return 0
}
