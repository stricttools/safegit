package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
)

func runUnlock(flags globalFlags, ref string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 4
	}
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	sharedDir := repo.SharedSafegitDir(context.Background(), gitDir)

	// Check if lock exists (locks live under the shared safegit dir)
	lp := filepath.Join(sharedDir, "locks", ref+".lock")
	if _, err := os.Stat(lp); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: no lock held on %s\n", ref)
		return 1
	}

	// Always check liveness -- refuse to release locks held by live processes
	stale, err := lock.IsStale(lp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot check lock status: %v\n", err)
		return 1
	}
	if !stale {
		pid, _ := lock.ParsePID(lp)
		fmt.Fprintf(os.Stderr, "error: lock on %s is held by a live process (pid %d); kill the process or wait for it to finish\n", ref, pid)
		return 1
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
		return 1
	}

	if !flags.silent() {
		fmt.Printf("lock on %s released\n", refShortName(ref))
	}
	return 0
}
