package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
)

// `safegit revert` of a SINGLE commit, restructured.
//
// As a plain passthrough, git authored the commit: it carried none of safegit's
// trailers, none of safegit's commit-time machinery ran, the author was
// whoever ran the command rather than the commit being reverted, and git left
// its AUTO_MERGE behind.
//
// All of it goes away by splitting the operation where git itself splits it:
// `git revert --no-commit` COMPUTES the inverse patch and stages it, and the
// conclusion engine (the same one behind `safegit revert-continue`) turns that
// staged result into a commit -- pipeline-authored, trailers injected,
// commit-msg hook run, author preserved.
//
// The state cleanup is not a nicety of that split but a requirement of it. A
// plumbing conclusion of `revert --no-commit` leaves REVERT_HEAD, MERGE_MSG and
// AUTO_MERGE in place (git only removes them when git itself commits), and
// REVERT_HEAD is what the commit pipeline reads as an operation in flight: a
// restructured revert that skipped the cleanup would refuse every later
// `safegit commit` until somebody deleted the files by hand. finishConclusion,
// which this path shares with the three conclusion commands, removes the whole
// set.
//
// A CONFLICT in the compute step needs no special handling at all: git leaves
// exactly the state a plain conflicted revert leaves (probe-verified: the same
// unmerged stages, REVERT_HEAD, MERGE_MSG carrying the "# Conflicts:" block,
// AUTO_MERGE), safegit reports git's own exit code and names the way out, and
// `safegit revert-continue` concludes it -- the same engine reached by the
// other door.

// revertConclusionFlags are the options a restructured revert can honor, i.e.
// the ones whose whole effect happens in the COMPUTE step or in the message
// draft git writes there.
//
// It is an allowlist rather than a list of exclusions, and that direction is
// the point: an option safegit has not considered -- one git grows next year --
// leaves the command line as an ordinary passthrough, which is exactly what it
// was before this restructure existed. Nothing is ever silently dropped.
//
// Why each is here:
//
//	--no-edit                   the restructured form never opens an editor anyway
//	-s/--signoff                git writes the trailer into MERGE_MSG (probe-verified)
//	-m/--mainline               selects the parent to revert against, in the compute step
//	--strategy, -X              the merge machinery's own options, in the compute step
//	--rerere-autoupdate         rerere runs during the compute step
//	--reference                 changes how git words MERGE_MSG
//
// Deliberately ABSENT, each because the conclusion cannot reproduce it:
// -e/--edit (there is no editor in safegit's commit surface), -n/--no-commit
// (the operator asked for exactly the staged result the restructure would
// commit), -S/--gpg-sign (the pipeline does not sign), --cleanup (message
// cleanup happens at git's commit time, which does not happen here), and every
// sequencer verb (--continue/--skip/--abort/--quit), which creates no commit.
var revertConclusionFlags = map[string]bool{
	"--no-edit":              true,
	"-s":                     true,
	"--signoff":              true,
	"--no-signoff":           true,
	"-m":                     true,
	"--mainline":             true,
	"--strategy":             true,
	"-X":                     true,
	"--strategy-option":      true,
	"--rerere-autoupdate":    true,
	"--no-rerere-autoupdate": true,
	"--reference":            true,
	"--no-reference":         true,
}

// runRevert dispatches `safegit revert`: the restructured single-commit form
// where safegit can author the commit, and the unchanged guarded passthrough
// everywhere else.
func runRevert(flags globalFlags, args []string) int {
	if !revertIsRestructurable(flags, args) {
		return runGuardedPassthrough(flags, "revert", args)
	}
	return runRestructuredRevert(flags, args)
}

// revertIsRestructurable decides which of the two forms an invocation gets.
//
// The question is answered from the command line plus one read-only git call
// (how many commits the revisions name), and every uncertain answer is "no":
// an unresolvable revision, an option outside the allowlist, a pathspec, or
// anything other than exactly one commit leaves the invocation exactly as it
// was before the restructure.
func revertIsRestructurable(flags globalFlags, args []string) bool {
	if len(args) == 0 {
		return false
	}
	parsed := parseGitArgs("revert", args)
	if len(parsed.AfterDoubleDash) > 0 || len(parsed.Revisions) == 0 {
		return false
	}
	for _, o := range parsed.Options {
		if !revertConclusionFlags[o.Name] {
			return false
		}
	}
	n, ok := countRevisions(flags, parsed.Revisions)
	return ok && n == 1
}

// countRevisions asks git how many commits a revision list names, which is the
// same question `git revert` itself answers when it decides between one commit
// and a queue: `--no-walk` shows the named commits, and has no effect on a
// range, which is walked. An unreadable revision is reported as not-counted
// rather than as zero -- git will produce its own error for it.
func countRevisions(flags globalFlags, revisions []string) (int, bool) {
	out, _, err := git.Run(flags.ctx(), append([]string{"rev-list", "--no-walk", "--count"}, revisions...)...)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, false
	}
	return n, true
}

// runRestructuredRevert computes the revert with git and commits it with
// safegit.
func runRestructuredRevert(flags globalFlags, args []string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		return exitcode.General
	}

	release, code := acquireOperationLock(flags, gitDir, "revert")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "revert"); code != 0 {
		return code
	}

	if flags.dryRun {
		// No git ran: the invocation is recorded in the would-do log, exactly as
		// the unrestructured passthrough records it.
		return runGitMutation(flags, append([]string{"revert"}, args...)...)
	}

	// The compute step. --no-commit is what makes the two halves separable: git
	// works out the inverse patch and stages it, and stops before the commit
	// that would otherwise be git's.
	if code := runPassthrough(flags, "revert", append([]string{"--no-commit"}, args...)); code != 0 {
		// A conflict lands here, and it is not an error path in any sense that
		// needs handling: the repository holds the ordinary conflicted-revert
		// state and `safegit revert-continue` concludes it. announceWayOut says
		// exactly that, from the single way-out authority.
		announceWayOut(flags, gitDir)
		return code
	}

	_ = oplog.Append(sgDir, oplog.Entry{
		Op: "revert",
		Extra: map[string]interface{}{
			"args": strings.Join(args, " "),
		},
	})

	return concludeComputedRevert(flags, gitDir, sgDir)
}

// concludeComputedRevert turns the staged result of `git revert --no-commit`
// into a safegit commit, through the same engine `safegit revert-continue` runs.
//
// It reads the state git just wrote rather than anything it was told: the
// commit being reverted comes from REVERT_HEAD (so the author is preserved from
// the right commit) and the message from MERGE_MSG. Nothing here is a second
// implementation of the conclusion -- the resolution vocabulary, the marker
// verification and the state cleanup are the ones in sequencer_continue.go.
func concludeComputedRevert(flags globalFlags, gitDir, sgDir string) int {
	ctx := flags.ctx()
	op := revertContinueOp

	state, err := sequencer.Read(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the revert state git just wrote: %v\n", err)
		return exitcode.General
	}
	if state.Kind != sequencer.KindRevert {
		// git succeeded and left no revert in flight, which happens when the
		// revert produced nothing to do. There is no commit to make and no
		// state to clean up.
		fmt.Fprintf(os.Stderr, "error: git staged the revert but left no revert state behind (%s); nothing was committed\n", state.String())
		return exitcode.General
	}

	if _, err := git.HeadRef(ctx); err != nil {
		return op.refuseDetachedHead(state)
	}

	// A clean compute step leaves no unmerged paths, so the declaration is
	// empty and both checks pass over an empty set -- but they are run, not
	// skipped, because a repository can be mid-revert with foreign unmerged
	// entries from something else, and the conclusion refusing to guess is the
	// same answer here as it is behind revert-continue.
	sides, err := conflict.Stages(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the conflicted paths from the index: %v\n", err)
		return exitcode.General
	}
	var declared []resolution
	if code := op.checkCompleteness(ctx, state, sides, declared); code != 0 {
		return code
	}
	if code := op.verifyMarkers(ctx, state, sides, declared); code != 0 {
		return code
	}

	message, err := op.conclusionMessage(ctx, state, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	author, err := sequencer.SourceAuthor(ctx, state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return exitcode.General
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg, RefUpdate: effectsRefUpdate{flags}}
	result, err := p.Execute(ctx, commit.CommitRequest{
		Message:   message,
		IndexBase: commit.IndexBaseSharedIndex,
		Author:    &author,
		OplogOp:   op.command,
		Sequencer: &coord.SequencerContext{Kind: sequencer.KindRevert},
	})
	if err != nil {
		if errors.Is(err, commit.ErrTreeUnchanged) {
			return refuseEmptyRevert()
		}
		die(pipelineExitCode(err), err.Error())
	}

	out := conclusionResult{state: state, commit: result, declared: declared, author: &author}
	if err := finishConclusion(ctx, gitDir, state, result, nil, sides, declared); err != nil {
		die(exitcode.General, err.Error())
	}
	out.cleared = true

	if err := maybeAutoBumpParent(ctx, flags, gitDir, result.SHA, "revert", firstLine(message)); err != nil {
		die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
	}

	// Human output only: `revert` is a passthrough command and declares no
	// payload schema, so there is no machine document to emit here.
	op.renderHuman(flags, out, "reverted "+shortSHA(state.Source))
	return exitcode.OK
}

// refuseEmptyRevert covers a revert whose inverse patch changes nothing --
// the commit was already undone by something else. git refuses the same case,
// and the state it left is still in flight, so the message names the ways out
// that actually exist.
func refuseEmptyRevert() int {
	fmt.Fprintf(os.Stderr, "error: this revert produces no change: the commit's effect is already absent from the tree\n")
	fmt.Fprintf(os.Stderr, "  the revert is still in progress; drop it with:\n")
	fmt.Fprintf(os.Stderr, "    git revert --abort\n")
	return exitcode.General
}

// refuseOwnedConclusion refuses `safegit <verb> --continue` for the operations
// safegit concludes ITSELF, and names its own command instead.
//
// The dirty-tree guard already gives this refusal most of the time, because a
// repository mid-merge is dirty by construction -- but not always: a merge
// whose result equals the current tip leaves nothing for `git diff HEAD` to
// report, and `safegit merge --continue` would then pass the guard and let git
// author a commit with none of safegit's trailers and none of its commit-time
// machinery. Whether a conclusion is safegit's or git's must not depend on
// whether the merge happened to change a file.
//
// It is scoped to the three operations safegit owns, read from the single
// way-out authority rather than from a list here: a rebase's or a mailbox
// application's `--continue` is git's own and passes through untouched.
func refuseOwnedConclusion(flags globalFlags, gitDir, verb string, args []string) int {
	if !hasExactArg(args, "--continue") {
		return 0
	}
	state, err := sequencer.Read(gitDir)
	if err != nil || !state.InProgress() {
		// Nothing in flight: git's own "no ... in progress" is the honest
		// answer and safegit has nothing to add.
		return 0
	}
	w := coord.WayOutOf(state)
	if !strings.HasPrefix(w.Conclude, "safegit ") {
		return 0
	}

	fmt.Fprintf(os.Stderr, "error: safegit %s --continue does not conclude %s; safegit does\n", verb, state.String())
	fmt.Fprintf(os.Stderr, "  git's own --continue would commit the whole index itself, with none of safegit's trailers\n")
	fmt.Fprintf(os.Stderr, "  and none of its commit-time machinery. Use the command that does:\n")
	fmt.Fprintf(os.Stderr, "    conclude it:  %s\n", w.Conclude)
	if w.Abandon != "" {
		fmt.Fprintf(os.Stderr, "    abandon it:   %s\n", w.Abandon)
	}
	return exitcode.CoordinationBusy
}

// hasExactArg reports whether argv holds the given element verbatim, stopping
// at a bare `--` so a pathspec that happens to spell an option is not read as
// one.
func hasExactArg(args []string, want string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == want {
			return true
		}
	}
	return false
}
