package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// The two repairs `safegit doctor --action fix` makes to GIT's own leftovers,
// as opposed to safegit's own state: an orphaned autostash, whose content no
// ref reaches, and an orphaned unmerged index, which git and safegit both
// refuse every commit over.
//
// Both are minted through the effects handle, like every other mutation the fix
// makes, so a preview records the invocations and performs none of them.

// runRepairGit mints one git invocation of a doctor repair.
//
// The working directory is DECLARED rather than inherited: the effects handle
// starts the process, so it cannot take the repository-root pin gitexec.Command
// applies, and these argv name working-tree paths. Passing the root explicitly
// is the pin's own value, stated at the one site that needs it.
func runRepairGit(flags globalFlags, worktree, resource string, args ...string) (strictcli.Completed, error) {
	argv, err := gitexec.ArgvAny(gitexec.ExemptDoctorRepair, gitexec.NoDoor, args...)
	if err != nil {
		return strictcli.Completed{}, err
	}
	return flags.effects().Run(argv, strictcli.Cwd(worktree), strictcli.Resource(resource))
}

// --- the orphaned autostash --------------------------------------------------

// fixOrphanedAutostash gives an orphaned MERGE_AUTOSTASH's work a name it will
// keep, and then removes the file.
//
// The ORDER is the whole safety of it. Until the commit is on refs/stash the
// only name it has is the object name inside the file about to be removed, and
// nothing else in the repository reaches it -- a `git gc` after a removal-first
// repair would take the operator's uncommitted work with it. So: store, and only
// if that succeeded, remove.
func fixOrphanedAutostash(flags globalFlags, found *autostashRepair) {
	if found == nil {
		return
	}
	if found.sha == "" {
		fmt.Fprintf(os.Stderr, "warning: %s holds no object name; it is left in place rather than removed\n", found.path)
		return
	}

	message := fmt.Sprintf("safegit doctor: recovered from an orphaned %s", sequencer.FileMergeAutostash)
	if _, err := runRepairGit(flags, flags.root.resolve(), "stash:"+found.sha,
		"stash", "store", "-m", message, found.sha); err != nil {
		fmt.Fprintf(os.Stderr, "error: storing %s as a stash entry: %v\n"+
			"  %s is left in place: removing it would leave the commit with no name at all\n",
			found.sha, err, found.path)
		return
	}
	if err := mintedRemover(flags, "git-state:")(found.path); err != nil {
		fmt.Fprintf(os.Stderr, "error: removing %s after storing its commit as a stash entry: %v\n", found.path, err)
		return
	}

	if flags.silent() {
		return
	}
	if flags.dryRun {
		fmt.Printf("would store the orphaned %s (%s) as a stash entry and remove the file\n",
			sequencer.FileMergeAutostash, shortSHA(found.sha))
		return
	}
	fmt.Printf("stored the orphaned %s (%s) as stash entry '%s' and removed the file\n",
		sequencer.FileMergeAutostash, shortSHA(found.sha), message)
}

// --- the orphaned unmerged index ---------------------------------------------

// unmergedPath is one path of an orphaned unmerged index and what the repair
// does with it: stage the working tree's own content at stage 0, or -- when the
// path is not on disk at all -- drop it from the index entirely.
type unmergedPath struct {
	path string
	// onDisk is false when the working tree does not hold the path, which is
	// what resolving the conflict by DELETING it looks like: the entry is
	// removed rather than a blob staged for it.
	onDisk bool
}

// unmergedRepair is an unmerged index with no operation in flight to resolve it.
type unmergedRepair struct {
	paths []unmergedPath
}

// planUnmergedRepair reports the orphaned unmerged index this repository
// carries, or nil when there is none.
//
// ONLY when git has nothing in flight. An unmerged index during a merge, a pick
// or a revert is that operation's own working state, and the conclusion
// commands are what resolve it -- re-staging underneath one would destroy the
// conflict it is waiting to be told about. What this repairs is the state left
// when the operation's state files are gone and its index entries are not: a
// crash, a hand-deleted MERGE_HEAD, one of the several ways `git merge --abort`
// leaves half its work behind. git refuses every commit there, and so does
// safegit (exit 28), which is why the refusal names this repair.
func planUnmergedRepair(ctx context.Context, gitDir, worktree string) (*unmergedRepair, error) {
	if worktree == "" {
		// No working tree, so no content to re-stage from and no index a commit
		// would be built beside.
		return nil, nil
	}
	state, err := sequencer.Read(gitDir)
	if err != nil {
		return nil, fmt.Errorf("reading git's in-flight state: %w", err)
	}
	if state.InProgress() {
		return nil, nil
	}

	entries, err := git.UnmergedStages(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("reading the index's unmerged entries: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// One repair per PATH, in the order git listed them: a conflicted path
	// occupies up to three stages, and all three are one decision.
	seen := map[string]bool{}
	repair := &unmergedRepair{}
	for _, e := range entries {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		_, statErr := os.Lstat(filepath.Join(worktree, e.Path))
		repair.paths = append(repair.paths, unmergedPath{path: e.Path, onDisk: statErr == nil})
	}
	return repair, nil
}

// fixOrphanedUnmergedIndex re-stages the working tree's own content over an
// orphaned unmerged index, which is what makes the exit-28 refusal's promise
// true: after this, a commit in this repository succeeds.
//
// It takes the WORKTREE OPERATION LOCK, because it is a second writer of the
// shared index -- the conclusion's own reconciliation is the first -- and the
// two must never interleave. The lock is not taken in a preview, which writes
// nothing.
func fixOrphanedUnmergedIndex(flags globalFlags, gitDir string, repair *unmergedRepair) {
	if repair == nil || len(repair.paths) == 0 {
		return
	}
	worktree := flags.root.resolve()

	release, code := acquireOperationLock(flags, gitDir, "doctor")
	if code != 0 {
		fmt.Fprintf(os.Stderr, "error: the unmerged index was left alone: another safegit operation owns this worktree\n")
		return
	}
	defer release()

	paths := repair.paths
	if !flags.dryRun {
		// Re-read under the lock, so what is repaired is the index as it stands
		// now rather than as it stood when the plan was made. A preview keeps
		// the plan's own answer: it holds no lock, and re-reading would only
		// describe a different moment.
		fresh, err := planUnmergedRepair(flags.ctx(), gitDir, worktree)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: re-reading the unmerged index: %v\n", err)
			return
		}
		if fresh == nil || len(fresh.paths) == 0 {
			// Somebody resolved it between the plan and the lock. Nothing to do
			// and nothing to report.
			return
		}
		paths = fresh.paths
	}

	// `update-index --add` is git's own way to resolve a conflicted path: it
	// hashes what the working tree holds, writes the blob, and replaces stages
	// 1/2/3 with one stage-0 entry. It is a single invocation with nothing in it
	// a preview would have to guess at -- no object name to placeholder -- and it
	// gets the cases a hand-rolled hash-and-cacheinfo pair gets wrong: the file
	// mode, the clean filters the repository declares, and a SYMLINK, which is
	// stored as its own link text rather than as the content it points at.
	var repaired, dropped []string
	for _, p := range paths {
		if !p.onDisk {
			if _, err := runRepairGit(flags, worktree, "index-entry:"+p.path,
				"update-index", "--force-remove", "--", p.path); err != nil {
				fmt.Fprintf(os.Stderr, "error: dropping %s from the index: %v\n", p.path, err)
				continue
			}
			dropped = append(dropped, p.path)
			continue
		}
		if _, err := runRepairGit(flags, worktree, "index-entry:"+p.path,
			"update-index", "--add", "--", p.path); err != nil {
			fmt.Fprintf(os.Stderr, "error: staging %s: %v\n", p.path, err)
			continue
		}
		repaired = append(repaired, p.path)
	}

	if flags.silent() {
		return
	}
	verb, dropVerb := "re-staged", "dropped"
	if flags.dryRun {
		verb, dropVerb = "would re-stage", "would drop"
	}
	if len(repaired) > 0 {
		fmt.Printf("%s %d unmerged path(s) from the working tree: %s\n", verb, len(repaired), strings.Join(repaired, ", "))
	}
	if len(dropped) > 0 {
		fmt.Printf("%s %d unmerged path(s) that are gone from disk: %s\n", dropVerb, len(dropped), strings.Join(dropped, ", "))
	}
}
