package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/strictcli/go/strictcli"
)

// runGitMutation runs a tree- or ref-mutating git command through the effects
// handle, streaming git's own output straight to the terminal, and returns
// git's own exit code. Routing it here is what makes --dry-run honest for these
// commands: the invocation is recorded in the would-do log and git is never
// started.
// The argv is built by internal/gitexec, safegit's single git-execution
// boundary, so it carries the same --no-optional-locks prefix every other git
// invocation does and its subcommand is checked against the one classification
// table. The invocation is exempt from the repository-root pin: these are the
// operator's own arguments, and git must read any pathspec in them in the
// directory the operator typed it in.
//
// Check(false) is what makes the propagation possible: the framework's checked
// form turns a nonzero child into an error string and hands back an unsettled
// Completed, so git's own code is unreachable there. Unchecked, the code rides
// the Completed and safegit passes it on unchanged. A nonzero return is
// therefore git's verdict, not safegit's; exitcode.General is reserved for the
// two failures that happen before git runs (argv construction, effects-handle
// refusal), and those print their reason because no child ever spoke.
func runGitMutation(flags globalFlags, args ...string) int {
	argv, err := gitexec.ArgvAny(gitexec.ExemptGitMutation, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	done, err := flags.effects().Run(argv, strictcli.Stream(true), strictcli.Check(false))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if flags.dryRun {
		// The invocation was recorded instead of performed: no child process
		// ran, so the Completed is unsettled and reading its exit code would
		// panic.
		//
		// This keys off the flag rather than off the carrier because strictcli
		// exposes no settled-ness: Completed's `settled` field is unexported
		// and its only public methods (ExitCode, Stdout, Stderr) panic instead
		// of reporting it, and Effects.Recorded() cannot serve as a probe
		// because calling it claims the would-do render. The coupling that
		// makes the flag a correct stand-in is that safegit declares NO
		// app-level proc-observe allowlist, so no argv reaches Run's observe
		// branch -- which executes the child even in dry mode and returns a
		// settled Completed.
		//
		// The hazard, if that ever changes: an allowlisted prefix matching a
		// runGitMutation argv would run git for real during --dry-run and this
		// branch would report success while discarding git's own exit code.
		// TestSafegitDeclaresNoProcObserveAllowlist pins the premise, and the
		// execution log's Phase 3.3 note records the same requirement for
		// whoever declares the allowlist.
		return 0
	}
	return done.ExitCode()
}

// coordGuard runs coord.Check and prints a refusal if dirty. It returns
// exitcode.CoordinationBusy when another operation owns the working tree,
// exitcode.OK when it is clean, and exitcode.General when the check itself
// could not be made.
func coordGuard(flags globalFlags, sgDir, operation string) int {
	ctx := flags.ctx()
	dirty, err := coord.Check(ctx, sgDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if dirty != nil {
		fmt.Fprint(os.Stderr, dirty.Refuse(operation))
		return exitcode.CoordinationBusy
	}
	return 0
}

func runCheckout(flags globalFlags, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("checkout [git checkout args...]", "Checkout a ref (guarded: checks for uncommitted work).")
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "checkout"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit checkout <ref>")
		return exitcode.Usage
	}

	// Capture old HEAD for oplog
	ctx := flags.ctx()
	oldHead, _ := git.RevParse(ctx, "HEAD")

	if code := runGitMutation(flags, append([]string{"checkout"}, args...)...); code != 0 {
		return code
	}
	if flags.dryRun {
		return 0
	}

	newHead, _ := git.RevParse(ctx, "HEAD")
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "checkout",
		Extra: map[string]interface{}{
			"ref":  args[0],
			"from": oldHead,
			"to":   newHead,
		},
	})
	return 0
}

// pullMode represents the fast-forward merge strategy for pull.
type pullMode int

const (
	pullFFOnly pullMode = iota // --ff-only: fail if not fast-forward
	pullFF                     // --ff: fast-forward if possible, merge commit otherwise
	pullNoFF                   // --no-ff: always create a merge commit
)

func runPull(flags globalFlags, mode pullMode, remote string, branch string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "pull"); code != 0 {
		return code
	}

	// Step 1: fetch
	fetchArgs := []string{"fetch", remote}
	if branch != "" {
		fetchArgs = append(fetchArgs, branch)
	}
	if code := runGitMutation(flags, fetchArgs...); code != 0 {
		return code
	}

	// Step 2: merge
	mergeArgs := []string{"merge"}
	switch mode {
	case pullFFOnly:
		mergeArgs = append(mergeArgs, "--ff-only")
	case pullNoFF:
		mergeArgs = append(mergeArgs, "--no-ff")
	case pullFF:
		// git's default: fast-forward if possible, merge commit otherwise
	}
	mergeTarget := "FETCH_HEAD"
	mergeArgs = append(mergeArgs, mergeTarget)
	if code := runGitMutation(flags, mergeArgs...); code != 0 {
		return code
	}
	if flags.dryRun {
		return 0
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "pull",
		Extra: map[string]interface{}{
			"remote": remote,
			"branch": branch,
		},
	})
	return 0
}

func runMerge(flags globalFlags, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("merge [git merge args...]", "Merge a branch (guarded: checks for uncommitted work).")
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "merge"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit merge <branch>")
		return exitcode.Usage
	}

	ctx := flags.ctx()
	if code := runGitMutation(flags, append([]string{"merge"}, args...)...); code != 0 {
		return code
	}
	if flags.dryRun {
		return 0
	}

	resultSHA, _ := git.RevParse(ctx, "HEAD")
	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "merge",
		Extra: map[string]interface{}{
			"branch": args[0],
			"result": resultSHA,
		},
	})
	return 0
}

func runRebase(flags globalFlags, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("rebase [git rebase args...]", "Rebase onto upstream (guarded: checks for uncommitted work).")
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, "rebase"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit rebase <upstream>")
		return exitcode.Usage
	}

	if code := runGitMutation(flags, append([]string{"rebase"}, args...)...); code != 0 {
		return code
	}
	if flags.dryRun {
		return 0
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "rebase",
		Extra: map[string]interface{}{
			"upstream": args[0],
		},
	})
	return 0
}

func runReset(flags globalFlags, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("reset [git reset args...]", "Reset HEAD (guarded for --hard).")
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	// Only guard --hard resets (those are the tree-mutating ones)
	isHard := false
	for _, a := range args {
		if a == "--hard" {
			isHard = true
			break
		}
	}

	if isHard {
		if code := coordGuard(flags, sgDir, "reset --hard"); code != 0 {
			return code
		}
	}

	if code := runGitMutation(flags, append([]string{"reset"}, args...)...); code != 0 {
		return code
	}
	if flags.dryRun {
		return 0
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "reset",
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return 0
}

func runBisect(flags globalFlags, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		commandHelp("bisect [git bisect args...]", "Bisect (guarded: checks for uncommitted work).")
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	// Guard tree-moving subcommands (good, bad, reset, start with a rev)
	needsGuard := false
	if len(args) > 0 {
		switch args[0] {
		case "good", "bad", "old", "new", "reset", "start":
			needsGuard = true
		}
	}

	if needsGuard {
		if code := coordGuard(flags, sgDir, "bisect"); code != 0 {
			return code
		}
	}

	if code := runGitMutation(flags, append([]string{"bisect"}, args...)...); code != 0 {
		return code
	}
	if flags.dryRun {
		return 0
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "bisect",
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return 0
}

// guardedHelp maps guarded passthrough commands to their help descriptions.
var guardedHelp = map[string]string{
	"cherry-pick": "Cherry-pick commits (guarded: checks for uncommitted work).",
	"revert":      "Revert commits (guarded: checks for uncommitted work).",
}

// runGuardedPassthrough runs a coordination check, then passes through to git.
func runGuardedPassthrough(flags globalFlags, gitCmd string, args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		desc := guardedHelp[gitCmd]
		if desc == "" {
			desc = fmt.Sprintf("Guarded wrapper around git %s.", gitCmd)
		}
		commandHelp(fmt.Sprintf("%s [git %s args...]", gitCmd, gitCmd), desc)
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := coordGuard(flags, sgDir, gitCmd); code != 0 {
		return code
	}

	if flags.dryRun {
		// No git ran: the invocation was recorded, so there is no foreign exit
		// code to propagate and only a framework failure can be nonzero.
		if code := runGitMutation(flags, append([]string{gitCmd}, args...)...); code != 0 {
			return code
		}
		return 0
	}

	code := runPassthrough(flags, gitCmd, args)

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: gitCmd,
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})
	return code
}

// runPassthrough executes a git command directly, forwarding all args.
//
// The context still carries the dispatch's repository-root pin; git.RunPassthrough
// suspends it under the declared operator-cwd exemption, because the argv here is
// the operator's own and any pathspec in it must mean what it meant where it was
// typed. Every other context-carried override still applies.
func runPassthrough(flags globalFlags, gitCmd string, args []string) int {
	ctx := flags.ctx()
	if err := git.RunPassthrough(ctx, append([]string{gitCmd}, args...)...); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		return exitcode.General
	}
	return 0
}
