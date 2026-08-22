package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
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
		// because calling it claims the would-do render.
		//
		// What makes the flag a correct stand-in is that no argv built here can
		// reach Run's OBSERVE branch, which executes the child even in dry mode
		// and returns a settled Completed. safegit's proc-observe allowlist is
		// generated from the classification table's read view and admits only
		// verbs the table declares observe-only unconditionally; every argv
		// this function builds names a verb the same table declares mutating
		// (checkout, fetch, merge, rebase, reset, bisect, cherry-pick, revert).
		// The two sets are disjoint by construction, and
		// TestObserveAllowlistCannotAdmitAMutation checks the prefixes against
		// the argv this function actually builds rather than trusting the
		// reasoning.
		return 0
	}
	return done.ExitCode()
}

// coordGuard runs coord.Check and prints a refusal if dirty. It returns
// exitcode.CoordinationBusy when another operation owns the working tree,
// exitcode.OK when it is clean, and exitcode.General when the check itself
// could not be made.
//
// gitDir, not the safegit dir: the check reads git's own in-flight operation
// state so that a refusal issued mid-merge or mid-rebase names that operation
// and the command that ends it.
func coordGuard(flags globalFlags, gitDir, operation string) int {
	ctx := flags.ctx()
	dirty, err := coord.Check(ctx, gitDir)
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

// announceWayOut prints safegit's own next step when a passthrough failed and
// left git with an operation in flight.
//
// git's own failure text ends at "fix conflicts and then commit the result",
// and that instruction has no safegit-conformant execution: `safegit commit` is
// pathspec-only and refuses mid-merge, and `git commit` is unavailable to an
// agent under the git-add/git-commit blocking hooks. So safegit owes the
// operator the command that DOES conclude the operation, rendered from
// coord.WayOutOf -- the single authority every in-flight refusal already reads,
// so no two messages can name different commands for the same state.
//
// It prints on stderr, unconditionally: this is the tail of a failure, and an
// operator who asked for --quiet asked for less noise on the happy path, not
// for the way out to be withheld.
func announceWayOut(flags globalFlags, gitDir string) {
	if flags.dryRun {
		// Nothing ran, so nothing is in flight that this invocation put there.
		return
	}
	state, err := sequencer.Read(gitDir)
	if err != nil || !state.InProgress() {
		return
	}
	w := coord.WayOutOf(state)
	if w.Conclude == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "\nsafegit: %s is in progress.\n", state.String())
	fmt.Fprintf(os.Stderr, "  conclude it:  %s\n", w.Conclude)
	if w.Abandon != "" {
		fmt.Fprintf(os.Stderr, "  abandon it:   %s\n", w.Abandon)
	}
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

	release, code := acquireOperationLock(flags, gitDir, "checkout")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "checkout"); code != 0 {
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

	release, code := acquireOperationLock(flags, gitDir, "pull")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "pull"); code != 0 {
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

	release, code := acquireOperationLock(flags, gitDir, "merge")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "merge"); code != 0 {
		return code
	}

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: safegit merge <branch>")
		return exitcode.Usage
	}

	ctx := flags.ctx()
	if code := runGitMutation(flags, append([]string{"merge"}, args...)...); code != 0 {
		announceWayOut(flags, gitDir)
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

	release, code := acquireOperationLock(flags, gitDir, "rebase")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "rebase"); code != 0 {
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

	// Unconditionally, unlike the dirty-tree guard below: every reset moves
	// HEAD, and safegit cannot tell which forms are harmless without
	// re-deriving git's own argument vocabulary.
	release, code := acquireOperationLock(flags, gitDir, "reset")
	if code != 0 {
		return code
	}
	defer release()

	// Only guard --hard resets (those are the tree-mutating ones)
	isHard := false
	for _, a := range args {
		if a == "--hard" {
			isHard = true
			break
		}
	}

	if isHard {
		if code := coordGuard(flags, gitDir, "reset --hard"); code != 0 {
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

	// Unconditionally, for the same reason as reset: the subcommand list below
	// is an approximation of git's vocabulary and the lock must not depend on
	// it being complete.
	release, code := acquireOperationLock(flags, gitDir, "bisect")
	if code != 0 {
		return code
	}
	defer release()

	// Guard tree-moving subcommands (good, bad, reset, start with a rev)
	needsGuard := false
	if len(args) > 0 {
		switch args[0] {
		case "good", "bad", "old", "new", "reset", "start":
			needsGuard = true
		}
	}

	if needsGuard {
		if code := coordGuard(flags, gitDir, "bisect"); code != 0 {
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

	release, code := acquireOperationLock(flags, gitDir, gitCmd)
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, gitCmd); code != 0 {
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

	code = runPassthrough(flags, gitCmd, args)
	if code != 0 {
		announceWayOut(flags, gitDir)
	}

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
	return passthroughExitCode(git.RunPassthrough(ctx, append([]string{gitCmd}, args...)...))
}

// passthroughStdout names where a passthrough child's stdout goes.
//
// In machine mode safegit's stdout carries exactly one document -- the
// framework's envelope -- so a child writing its own progress there would put a
// second document beside it and break every consumer that parses the stream.
// git's output is not discarded for that: it goes to stderr, which is what
// `push` already does with git's stdout under --json.
func passthroughStdout(flags globalFlags) io.Writer {
	if flags.json {
		return os.Stderr
	}
	return os.Stdout
}

// passthroughExitCode turns the error of a passthrough git invocation into the
// process exit code safegit propagates: git's own code when git ran and said
// no, and exitcode.General when the failure happened before git could speak.
//
// It is shared by the guarded passthroughs and by the delegated conclusion, so
// "what does safegit exit when git exits N" has one answer for both.
func passthroughExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return exitcode.General
}
