package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
)

// lockTarget is one resolved unlock argument: which lock the operator named and
// which of the two lock trees holds it.
type lockTarget struct {
	// name is the string lock.Acquire was called with -- a ref, or one of the
	// tool-owned pseudo-refs.
	name string
	// base is the safegit directory whose locks/ subtree the file lives in.
	base string
	// display is how the target is echoed back to the operator.
	display string
}

// pseudoRefLocks are the tool-owned lock names that are not refs. Each declares
// which lock tree holds it, because that is the one thing a caller cannot
// deduce from the name: a rewrite changes object names for every worktree of
// the repository and so locks in the shared directory, while an operation locks
// only the worktree it runs in.
//
// The table is what makes these locks addressable at all. Before it, unlock
// prefixed refs/heads/ onto any argument without a refs/ prefix, so
// `safegit unlock safegit/rewrite` looked for a lock on the branch
// refs/heads/safegit/rewrite and reported that no lock was held -- the one lock
// an operator is most likely to need released after a crashed history rewrite
// was unreachable.
var pseudoRefLocks = map[string]struct {
	shared bool
	what   string
}{
	lock.RewriteRef:   {shared: true, what: "the repository-wide history-rewrite lock"},
	lock.OperationRef: {shared: false, what: "this worktree's operation lock"},
}

// resolveLockTarget applies the unlock naming grammar to one argument:
//
//   - a name starting with "safegit/" is a tool-owned pseudo-ref, looked up in
//     pseudoRefLocks; an unknown one is an error listing the known names rather
//     than a silent reinterpretation as a branch;
//   - a name starting with "refs/" is a full ref name, taken as written;
//   - anything else is a branch shorthand and means refs/heads/<name>.
func resolveLockTarget(flags globalFlags, gitDir, arg string) (lockTarget, error) {
	sharedDir := repo.SharedSafegitDir(flags.ctx(), gitDir)

	if strings.HasPrefix(arg, "safegit/") {
		spec, known := pseudoRefLocks[arg]
		if !known {
			names := make([]string, 0, len(pseudoRefLocks))
			for n := range pseudoRefLocks {
				names = append(names, n)
			}
			sort.Strings(names)
			return lockTarget{}, fmt.Errorf("unknown safegit lock %q; the tool-owned lock names are: %s",
				arg, strings.Join(names, ", "))
		}
		base := repo.SafegitDir(gitDir)
		if spec.shared {
			base = sharedDir
		}
		return lockTarget{name: arg, base: base, display: arg}, nil
	}

	ref := arg
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}
	return lockTarget{name: ref, base: sharedDir, display: refShortName(ref)}, nil
}

func runUnlock(flags globalFlags, ref string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}

	target, err := resolveLockTarget(flags, gitDir, ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.Usage
	}

	lp := lock.Path(target.base, target.name)
	if _, err := os.Stat(lp); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "error: no lock held on %s\n", target.display)
		return exitcode.General
	}

	// Always check liveness -- refuse to release locks held by live processes
	if !lock.IsStale(lp) {
		pid, _ := lock.ParsePID(lp)
		fmt.Fprintf(os.Stderr, "error: lock on %s is held by a live process (pid %d); kill the process or wait for it to finish\n", target.display, pid)
		return exitcode.General
	}

	if flags.dryRun {
		if !flags.silent() {
			fmt.Printf("would release lock on %s\n", target.display)
		}
		return 0
	}

	// Deliberately the unconditional removal, not the flock-and-identity-checked
	// reclamation doctor sweeps through.
	//
	// This is an operator naming ONE lock and asking for it to be released, and
	// it is the recovery path of last resort: on a filesystem where flock(2) does
	// not work, reclamation cannot succeed at all, contenders time out instead of
	// reclaiming, and an identity-checked unlock would leave the operator with no
	// way to clear the lock. The staleness check above still stands between this
	// and a live holder; what the weaker stance gives up is only the narrow window
	// in which another process reclaims the same stale lock between that check and
	// this removal. doctor, which sweeps unattended and by the hundred, takes the
	// strict path instead.
	if err := lock.ForceRelease(target.base, target.name); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	if !flags.silent() {
		fmt.Printf("lock on %s released\n", target.display)
	}
	return 0
}
