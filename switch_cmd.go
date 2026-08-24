package main

import (
	"context"
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/repo"
)

// `safegit switch <branch>` -- and there is no `safegit checkout`.
//
// git's checkout is two commands wearing one name: it MOVES HEAD, and it
// RESTORES files from a commit over whatever the working tree holds. The second
// one destroys uncommitted work with no record anywhere -- in a shared worktree,
// work that may be another session's -- which is why the whole spelling is gone
// rather than guarded. safegit implements the first one only, under git's own
// name for it, so the destructive form is not refused: it is inexpressible.
//
// What is left is deliberately narrow. A branch that exists, or `-c <new>` to
// make one, and nothing else:
//
//   - a TAG, an OBJECT NAME or any other commit-ish detaches HEAD, and a
//     detached HEAD is a state safegit refuses to commit in everywhere else. The
//     flag `--detach` guards nothing here, because the ARGUMENT is what detaches;
//   - `--force`/`--discard-changes` and `--merge` exist to carry, or throw away,
//     a dirty working tree, and the coordination check refuses a dirty tree
//     before git runs -- the same shape as merge's `--autostash`;
//   - `-C` re-points a branch that already exists, which is a ref move dressed
//     as navigation;
//   - `--orphan` starts a history with no parent, which is a repository-shaped
//     decision rather than a step between branches.
//
// DIVERGENCE: git has `checkout` and `switch`; safegit has `switch`, takes a
// branch name, and has no file-restoration mode at all. Both halves need their
// row in docs/divergences.md.

// switchSubset is `safegit switch`'s slice of `git switch`.
var switchSubset = argvSubset{
	command: "switch",
	allowed: []string{
		// Branch creation, switch's own spelling. It replaces checkout's -b.
		"-c", "--create",
	},
	refused: []refusedCapability{
		{
			[]string{"--detach"},
			"safegit switch moves HEAD onto a BRANCH; a detached HEAD is the state safegit's own commit, conclusion and undo paths refuse, and its guide teaches getting out of. Use 'git switch --detach' when you deliberately want one",
		},
		{
			[]string{"-C", "--force-create"},
			"-C re-points a branch that already exists at wherever you are standing, which is a ref move dressed as navigation and outside safegit's compare-and-swap; make a new branch with -c, or move the existing one deliberately with git",
		},
		{
			[]string{"-f", "--force", "--discard-changes"},
			"--discard-changes throws away uncommitted work to make the switch possible, and in a shared worktree that work may be another session's. safegit's coordination check refuses a dirty tree before git runs; commit what you have first",
		},
		{
			[]string{"--orphan"},
			"--orphan starts a branch with no history at all, which is a decision about the repository rather than a step between branches; make one with git if you mean it",
		},
		{
			[]string{"-m", "--merge"},
			"--merge is a dead flag through safegit: it exists to carry a dirty working tree across the switch by merging it, and the coordination check refuses a dirty tree before git runs. Commit your changes first",
		},
	},
}

// runSwitch dispatches `safegit switch`.
func runSwitch(flags globalFlags, args []string) int {
	parsed := parseGitArgs("switch", args)

	// FIRST, and before the repository is touched at all: a command line safegit
	// itself refuses is refused without a lock and without a git call.
	if code := refuseUnsupportedSwitch(parsed); code != 0 {
		return code
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	release, code := acquireOperationLock(flags, gitDir, "switch")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "switch"); code != 0 {
		return code
	}

	ctx := flags.ctx()

	// The one refusal that needs the repository: whether the argument names a
	// branch. It is made under the lock, like every other state reading.
	if len(parsed.Revisions) == 1 {
		if code := refuseNonBranch(ctx, parsed.Revisions[0]); code != 0 {
			return code
		}
	}

	// Where HEAD stands before the navigation. A branch CREATION comes from
	// nowhere: the ref did not exist, and the zero SHA is git's own name for that.
	oldHead, _ := git.RevParse(ctx, "HEAD")
	if createsBranch(args) {
		oldHead = git.ZeroSHA
	}

	if code := runGitMutation(flags, gitexec.NoDoor, append([]string{"switch"}, args...)...); code != 0 {
		// The ref name is the one HEAD still points at: the navigation did not
		// happen, so the operator's argument names nowhere this repository went.
		failedRef, _ := git.HeadRef(ctx)
		appendNavigationEntry(flags, sgDir, "switch", failedRef, oldHead, "", false, nil)
		return code
	}
	if flags.dryRun {
		return 0
	}

	// The RESOLVED full ref name, not the operator's argument. That argument was
	// whatever they typed -- for a `-c` form, the literal flag string -- and an
	// audit trail naming a flag as the ref it moved onto is worse than one naming
	// nothing.
	newRef, _ := git.HeadRef(ctx)
	newHead, _ := git.RevParse(ctx, "HEAD")
	appendNavigationEntry(flags, sgDir, "switch", newRef, oldHead, newHead, true, nil)
	return 0
}

// refuseUnsupportedSwitch refuses the command lines safegit's switch does not
// implement: the options outside its allowlist, a pathspec, and any argument
// count other than the one form it takes.
func refuseUnsupportedSwitch(parsed gitArgs) int {
	if code := switchSubset.refuseUnsupportedOptions(parsed); code != 0 {
		return code
	}

	if len(parsed.AfterDoubleDash) > 0 {
		fmt.Fprintf(os.Stderr, "error: safegit switch takes no pathspec\n")
		fmt.Fprintf(os.Stderr, "  a path after a switch is git's checkout of files OVER the working tree, which destroys\n")
		fmt.Fprintf(os.Stderr, "  uncommitted work with no record anywhere -- in a shared worktree, possibly somebody\n")
		fmt.Fprintf(os.Stderr, "  else's. safegit has no such command at all; see docs/divergences.md.\n")
		return exitcode.Usage
	}

	creating, _ := parsed.Find("-c", "--create")
	switch {
	case creating.Name != "" && len(parsed.Revisions) > 0:
		fmt.Fprintf(os.Stderr, "error: safegit switch -c takes the new branch's name and nothing else\n")
		fmt.Fprintf(os.Stderr, "  the new branch starts where you are standing; a start point elsewhere is a branch\n")
		fmt.Fprintf(os.Stderr, "  creation rather than a step between branches. Make it with 'git branch <name> <start>'\n")
		fmt.Fprintf(os.Stderr, "  and switch to it. See docs/divergences.md.\n")
		return exitcode.Usage
	case creating.Name != "":
		if creating.Value == "" {
			fmt.Fprintf(os.Stderr, "error: safegit switch %s names no branch to create\n", creating.Name)
			fmt.Fprintf(os.Stderr, "  usage: safegit switch -c <new-branch>\n")
			return exitcode.Usage
		}
	case len(parsed.Revisions) == 0:
		fmt.Fprintf(os.Stderr, "error: safegit switch names no branch to switch to\n")
		fmt.Fprintf(os.Stderr, "  usage: safegit switch <branch>, or safegit switch -c <new-branch>\n")
		return exitcode.Usage
	case len(parsed.Revisions) > 1:
		fmt.Fprintf(os.Stderr, "error: safegit switch takes exactly one branch, and this names %d\n", len(parsed.Revisions))
		fmt.Fprintf(os.Stderr, "  usage: safegit switch <branch>\n")
		return exitcode.Usage
	}
	return 0
}

// refuseNonBranch refuses an argument that resolves to a commit but is not a
// branch.
//
// An argument that resolves to NOTHING is deliberately left to git, which is
// the convention merge and cherry-pick already follow: `safegit switch
// no-such-thing` must exit with git's own verdict on that argument rather than
// with a message safegit invented about branches.
func refuseNonBranch(ctx context.Context, arg string) int {
	if _, err := git.RevParse(ctx, "refs/heads/"+arg); err == nil {
		return 0
	}
	if _, err := git.RevParse(ctx, arg+"^{commit}"); err != nil {
		return 0
	}

	fmt.Fprintf(os.Stderr, "error: safegit switch does not support %s: it is a commit, not a branch\n", arg)
	fmt.Fprintf(os.Stderr, "  switching onto anything but a branch DETACHES HEAD, and a detached HEAD is the state\n")
	fmt.Fprintf(os.Stderr, "  safegit's commit, conclusion and undo paths all refuse. Make a branch there and switch\n")
	fmt.Fprintf(os.Stderr, "  to it:\n")
	fmt.Fprintf(os.Stderr, "    git branch <name> %s\n", arg)
	fmt.Fprintf(os.Stderr, "    safegit switch <name>\n")
	fmt.Fprintf(os.Stderr, "  or, when a detached HEAD is what you deliberately want:\n")
	fmt.Fprintf(os.Stderr, "    git switch --detach %s\n", arg)
	fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
	return exitcode.Usage
}

// createsBranch reports whether a switch argv creates the ref it moves onto, in
// which case the position it came from is the zero SHA rather than a commit: the
// ref did not exist.
//
// It recognizes the whole creation family, including the spellings safegit
// refuses (-C, --orphan): the answer is a fact about the argv, and a helper that
// knew only the accepted spellings would be one refusal away from being wrong.
func createsBranch(args []string) bool {
	for _, a := range args {
		switch a {
		case "-c", "--create", "-C", "--force-create", "--orphan":
			return true
		}
	}
	return false
}

// switchHelp is the command's registered help text.
const switchHelp = "switch to another BRANCH, guarded twice before git runs: the worktree operation lock, held for the whole command, and then a check for uncommitted work. The command line is a deliberate subset of git's: an existing branch name, or -c to create one, and nothing else. A tag, an object name or any other commit-ish is refused, because switching onto one detaches HEAD -- the state safegit's commit, conclusion and undo paths all refuse -- and the refusal names the raw-git command for the rare deliberate case. --detach, -C, --force/--discard-changes, --orphan and --merge are refused, each naming why. There is NO file mode and no 'safegit checkout': git's checkout of files over the working tree destroys uncommitted work with no record anywhere, so safegit does not implement it at all"
