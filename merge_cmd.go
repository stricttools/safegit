package main

import (
	"fmt"
	"os"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// `safegit merge <branch>`, restructured.
//
// As a plain passthrough, GIT authored whatever commit came out of the
// operator's command line: it carried none of safegit's trailers, safegit's
// commit-time machinery never ran, `safegit undo` could not reverse it, and
// git's AUTO_MERGE was left sitting in the git directory.
//
// The restructure splits the operation where git itself splits it, exactly as
// the restructured revert does: `git merge --no-commit` COMPUTES the merge and
// stages its result, and the conclusion engine (the one behind `safegit
// merge-continue`) turns that staged result into a commit -- pipeline-authored,
// trailers injected, commit-msg hook run, ref moved under compare-and-swap,
// state files removed.
//
// Three outcomes, and safegit decides which one applies rather than letting git
// decide it:
//
//   - FAST-FORWARD. safegit answers the ancestry question itself, moves the ref
//     with the pipeline's own compare-and-swap, and then puts the index and the
//     working tree in step with the new tip. That last step is not a nicety: a
//     bare ref move leaves the INVERSE of the incoming diff staged (probed), so
//     a fast-forward without it reports every incoming file as a staged
//     deletion.
//   - MERGE COMMIT. The compute step runs with BOTH `--no-ff` and
//     `--no-commit`, always, whatever the operator passed. `--no-commit` alone
//     cannot stop a fast-forward -- git takes it before the flag is consulted --
//     so an ff-ness check that raced a concurrent tip move would let git move
//     the ref outside safegit's compare-and-swap. `--no-ff` makes that
//     impossible: git always parks, and the conclusion's own compare-and-swap
//     then catches any movement.
//   - CONFLICT. The merge parks exactly as it always did, git's narration
//     reaches the operator on the streams it always did, and `safegit
//     merge-continue` concludes it.
//
// The command line is narrower than git's, under the subset law -- see
// mergeRefusedOptions and refuseUnsupportedMerge.

// The oplog outcomes a merge records, beyond the shared ok/failed pair. Each
// says what happened to the branch, because "ok" alone cannot tell a merge
// commit from a fast-forward from a merge deliberately left parked.
const (
	// mergeOutcomeFastForward: the ref moved onto commits git created. There is
	// no safegit commit here, which is why `safegit undo` will not reverse one.
	mergeOutcomeFastForward = "fast-forward"
	// mergeOutcomeParked: the merge was computed and left in flight at the
	// operator's request (`--no-commit`). Nothing moved.
	mergeOutcomeParked = "parked"
	// mergeOutcomeUpToDate: the branch already contained the other side, so git
	// had nothing to merge. Nothing moved.
	mergeOutcomeUpToDate = "up-to-date"
	// mergeOutcomeCommit: a merge commit was created. It is the outcome the
	// payload reports; the oplog entry for it is the PIPELINE's own, which
	// carries no outcome field because a commit entry exists only when the
	// commit does.
	mergeOutcomeCommit = "merge-commit"
)

// mergeRefusedOptions are the `git merge` options safegit's merge does not
// implement, each with the reason it is absent.
//
// It is a refusal list rather than an allowlist because an option safegit has
// not considered still reaches git's compute step, where it can change how the
// merge is COMPUTED but never who authors the commit -- the compute step is
// pinned to `--no-ff --no-commit`, so git cannot commit whatever else is on the
// command line.
var mergeRefusedOptions = []struct {
	names []string
	why   string
}{
	{
		[]string{"-s", "--strategy"},
		"safegit's merge does not select a merge strategy: the conclusion's completeness and conflict-marker checks are written against what the default strategy stages, and a strategy they cannot read would be protected by nothing",
	},
	{
		[]string{"-X", "--strategy-option"},
		"safegit's merge does not forward strategy options, for the same reason it does not select a strategy: they change what is staged, and the checks over the staged result cannot see the change",
	},
	{
		[]string{"--squash"},
		"--squash stages a merge's result without recording its parents, which is a commit with a merge's content and none of its history; safegit's merge records a merge commit or nothing",
	},
	{
		[]string{"-e", "--edit"},
		"--edit opens an editor, and safegit's commit surface has none; pass -m to give the merge commit its message",
	},
	{
		[]string{"--autostash"},
		"--autostash is a dead flag through safegit: the coordination check refuses a dirty working tree before git runs, so a merge never reaches git with anything to stash -- and in a shared worktree those changes may be another session's work. Commit your changes first",
	},
}

// mergeSelectorOptions are the operator's fast-forward and commit selections.
// safegit answers both questions itself, so they are removed from the argv the
// compute step forwards -- which carries `--no-ff --no-commit` of its own and
// would otherwise be handed a contradiction.
var mergeSelectorOptions = map[string]bool{
	"--ff":        true,
	"--no-ff":     true,
	"--ff-only":   true,
	"--commit":    true,
	"--no-commit": true,
}

// runMerge dispatches `safegit merge`.
//
// The state-control forms are the one route that is still a guarded
// passthrough: they author nothing, so nothing about single authorship is at
// stake in them. `--continue` is refused inside, by name, because concluding a
// merge is safegit's own job.
func runMerge(flags globalFlags, args []string) int {
	parsed := parseGitArgs("merge", args)
	if parsed.Has("--continue", "--abort", "--quit") {
		return runGuardedPassthrough(flags, "merge", args)
	}
	return runRestructuredMerge(flags, args, parsed)
}

// refuseUnsupportedMerge refuses the command lines safegit's merge does not
// implement, before any lock is taken and before any git runs.
//
// Every refusal is parser-shaped (exit 2) and names the capability that is
// absent, because "safegit does not do this" is a different answer from "this
// failed" and an operator has to be able to tell them apart. Each has its entry
// in the divergences catalog.
func refuseUnsupportedMerge(parsed gitArgs) int {
	for _, refused := range mergeRefusedOptions {
		if o, ok := parsed.Find(refused.names...); ok {
			fmt.Fprintf(os.Stderr, "error: safegit merge does not support %s\n", o.Name)
			fmt.Fprintf(os.Stderr, "  %s.\n", refused.why)
			fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
			return exitcode.Usage
		}
	}

	if len(parsed.AfterDoubleDash) > 0 {
		fmt.Fprintf(os.Stderr, "error: safegit merge takes no pathspec\n")
		fmt.Fprintf(os.Stderr, "  a pathspec limits a merge to part of the tree, and the result is a commit whose\n")
		fmt.Fprintf(os.Stderr, "  parents claim a merge that only partly happened. Merge the whole branch.\n")
		return exitcode.Usage
	}

	switch len(parsed.Revisions) {
	case 1:
	case 0:
		fmt.Fprintf(os.Stderr, "error: safegit merge names no branch to merge\n")
		fmt.Fprintf(os.Stderr, "  usage: safegit merge <branch>\n")
		return exitcode.Usage
	default:
		fmt.Fprintf(os.Stderr, "error: safegit merge takes exactly one branch, and this names %d\n", len(parsed.Revisions))
		fmt.Fprintf(os.Stderr, "  merging several branches in one commit -- an octopus merge -- is not part of\n")
		fmt.Fprintf(os.Stderr, "  safegit's subset: %s.\n", octopusReason)
		fmt.Fprintf(os.Stderr, "  Merge them one at a time. See docs/divergences.md.\n")
		return exitcode.Usage
	}

	if parsed.Has("--ff-only") && parsed.Has("--no-ff") {
		fmt.Fprintf(os.Stderr, "error: --ff-only and --no-ff say opposite things about the same merge\n")
		fmt.Fprintf(os.Stderr, "  --ff-only refuses anything but a fast-forward; --no-ff refuses the fast-forward itself.\n")
		return exitcode.Usage
	}
	return 0
}

// octopusReason is the one sentence that says why, shared by the refusal above
// and by the conclusion's own refusal of a raw-git octopus.
const octopusReason = "a conclusion has one staged result to check and one message to write, however many sides went into it, and every check safegit makes over a merge is written against two"

// runRestructuredMerge is the whole flow.
func runRestructuredMerge(flags globalFlags, args []string, parsed gitArgs) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	if code := refuseUnsupportedMerge(parsed); code != 0 {
		return code
	}

	// A merge concluded in a submodule moves the parent's gitlink exactly as an
	// ordinary commit does, so the parent must have answered the auto-bump
	// question before anything is written.
	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		return exitcode.General
	}

	release, code := acquireOperationLock(flags, gitDir, "merge")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "merge"); code != 0 {
		return code
	}

	if flags.dryRun {
		// No git merge runs: the invocation is recorded, and the outcome it
		// would have is COMPUTED with git's own merge engine instead of guessed.
		return previewSequencerOperation(flags, "merge", args)
	}

	ctx := flags.ctx()
	pos := readOplogPosition(flags)
	other := parsed.Revisions[0]

	// What safegit decides for itself, and it decides it BEFORE git runs.
	//
	// An unresolvable revision is deliberately left to git: `safegit merge
	// no-such-ref` must exit with git's own verdict on that argument, not with
	// a message safegit invented, so the ancestry questions are simply not
	// asked and the compute step below produces git's error.
	var upToDate, fastForward bool
	otherSHA, resolveErr := git.RevParse(ctx, other+"^{commit}")
	if resolveErr == nil && pos.oldTip != "" {
		upToDate, _ = git.IsAncestorOf(ctx, otherSHA, pos.oldTip)
		if !upToDate {
			fastForward, _ = git.IsAncestorOf(ctx, pos.oldTip, otherSHA)
		}
	}

	if parsed.Has("--ff-only") && resolveErr == nil && !upToDate && !fastForward {
		fmt.Fprintf(os.Stderr, "error: %s is not a fast-forward of %s, and --ff-only was given\n",
			other, refShortName(pos.ref))
		fmt.Fprintf(os.Stderr, "  the two branches have both moved on, so bringing them together needs a merge commit.\n")
		fmt.Fprintf(os.Stderr, "  Re-run without --ff-only to make one, or rebase this branch onto %s instead.\n", other)
		appendOperationEntry(flags, sgDir, "merge", pos, false, mergeExtra(other, oplogOutcomeFailed))
		return exitcode.General
	}

	// The fast-forward is taken only where nothing the operator said asks for a
	// commit. `--no-commit` is such a request: it says the operator wants to
	// look at the result before it becomes anything, and a fast-forward that
	// moved the branch silently would deny exactly that. A DETACHED HEAD is
	// excluded too -- there is no ref to compare-and-swap -- and falls through
	// to the compute step, where the conclusion refuses with the remedy.
	if fastForward && !parsed.Has("--no-ff") && !parsed.Has("--no-commit") && pos.ref != "" {
		return fastForwardMerge(flags, gitDir, sgDir, pos, other, otherSHA)
	}

	// The compute step. --no-ff and --no-commit are safegit's, always; the
	// operator's own fast-forward and commit selections are removed from the
	// argv rather than forwarded into a contradiction.
	computeArgs := append([]string{"merge", "--no-ff", "--no-commit"}, withoutMergeSelectors(args)...)
	if code := runGitMutation(flags, computeArgs...); code != 0 {
		// A conflict lands here, and it is not an error path in any sense that
		// needs handling: the repository holds the ordinary conflicted-merge
		// state and `safegit merge-continue` concludes it. announceWayOut says
		// exactly that, from the single way-out authority.
		appendOperationEntry(flags, sgDir, "merge", pos, false, mergeExtra(other, oplogOutcomeFailed))
		announceWayOut(flags, gitDir)
		return code
	}

	state, err := sequencer.Read(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the merge state git just wrote: %v\n", err)
		return exitcode.General
	}
	if state.Kind != sequencer.KindMerge {
		// git had nothing to merge -- the branch already contains the other
		// side -- and said so on its own output. Nothing moved.
		appendOperationEntry(flags, sgDir, "merge", pos, true, mergeExtra(other, mergeOutcomeUpToDate))
		reportMergeWithoutCommit(flags, pos, mergeOutcomeUpToDate, pos.oldTip)
		return exitcode.OK
	}

	if parsed.Has("--no-commit") {
		// Parked on purpose. The entry records no new tip, because none exists:
		// an empty new tip is what keeps a non-move invisible to the readers
		// that treat the newest recorded tip as where safegit left the branch.
		appendOperationEntry(flags, sgDir, "merge", pos, false, mergeExtra(other, mergeOutcomeParked))
		announceWayOut(flags, gitDir)
		reportMergeWithoutCommit(flags, pos, mergeOutcomeParked, "")
		return exitcode.OK
	}

	// Clean, and nothing asked for it to be left in flight: conclude it now,
	// through the same engine `safegit merge-continue` runs. The message is
	// git's own MERGE_MSG -- which already holds the operator's `-m` text
	// verbatim when they passed one -- so nothing is re-derived here.
	out, exit, ok := concludeParkedOperation(flags, gitDir, sgDir, state, parkedConclusion{
		op:           mergeContinueOp,
		oplogOp:      "merge",
		parentBumpOp: "merge",
		// A merge commit records its parents whether or not it changes a single
		// byte, so the pipeline's tree-unchanged refusal does not apply and
		// onEmpty is unreachable.
		allowEmpty: true,
	})
	if !ok {
		return exit
	}

	flags.payload(mergePayload{
		Operation:      "merge",
		Outcome:        mergeOutcomeCommit,
		Ref:            out.commit.Ref,
		SHA:            realSHA(flags, out.commit.SHA),
		Parents:        orEmpty(out.commit.Parents),
		Tree:           strPtr(out.commit.Tree),
		Files:          orEmpty(out.commit.Files),
		StateCleared:   out.cleared,
		Attempts:       out.commit.Attempts,
		DeclinedChecks: orEmptyDeclines(out.declines),
		DryRun:         flags.dryRun,
	})
	mergeContinueOp.renderHuman(flags, out, "merged "+short(other, secondParentOf(out)))
	return exit
}

// fastForwardMerge moves the branch onto the incoming tip and puts the index
// and the working tree in step with it.
//
// The ref move is the pipeline's own compare-and-swap port, so it is minted
// through the effects handle and pinned to the tip that was there a moment ago:
// a branch another session moved in between is a refusal, never a clobber.
func fastForwardMerge(flags globalFlags, gitDir, sgDir string, pos oplogPosition, other, otherSHA string) int {
	ctx := flags.ctx()
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return exitcode.General
	}

	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	lk, err := lock.Acquire(repo.SharedSafegitDir(ctx, gitDir), sgDir, pos.ref, "merge", timeout)
	if err != nil {
		if lock.IsTimeout(err) {
			fmt.Fprintf(os.Stderr, "error: acquiring lock on %s: %v\n", pos.ref, err)
			return exitcode.LockTimeout
		}
		fmt.Fprintf(os.Stderr, "error: acquiring lock on %s: %v\n", pos.ref, err)
		return exitcode.General
	}
	defer lk.Release()

	// Re-read the tip UNDER the lock. The ancestry decision was made before it
	// was taken, and the compare-and-swap has to be pinned to what is there
	// now rather than to what was there then.
	current, err := git.RevParse(ctx, pos.ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading where %s stands: %v\n", refShortName(pos.ref), err)
		return exitcode.General
	}
	if current != pos.oldTip {
		fmt.Fprintf(os.Stderr, "error: %s moved from %s to %s while the merge was being worked out\n",
			refShortName(pos.ref), shortSHA(pos.oldTip), shortSHA(current))
		fmt.Fprintf(os.Stderr, "  nothing was merged. Re-run the merge against the branch as it stands now.\n")
		appendOperationEntry(flags, sgDir, "merge", pos, false, mergeExtra(other, oplogOutcomeFailed))
		return exitcode.CASExhausted
	}

	if err := (effectsRefUpdate{flags}).Update(ctx, pos.ref, otherSHA, current); err != nil {
		fmt.Fprintf(os.Stderr, "error: fast-forwarding %s: %v\n", refShortName(pos.ref), err)
		appendOperationEntry(flags, sgDir, "merge", pos, false, mergeExtra(other, oplogOutcomeFailed))
		return exitcode.General
	}

	// Not optional: a ref move alone leaves the INVERSE of the incoming diff
	// staged and the incoming files missing from disk. The same primitive
	// scrub's post-rewrite sync uses, including its protection of tracked
	// files that are also gitignored.
	if _, err := git.SyncMainIndexWithWorktree(ctx, otherSHA); err != nil {
		appendOperationEntry(flags, sgDir, "merge", pos, true, mergeExtra(other, mergeOutcomeFastForward))
		fmt.Fprintf(os.Stderr, "error: %s was fast-forwarded to %s and stands there, but putting the index and the\n",
			refShortName(pos.ref), shortSHA(otherSHA))
		fmt.Fprintf(os.Stderr, "  working tree in step with it failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  until that is done, git reports the incoming changes as staged deletions.\n")
		return exitcode.General
	}

	appendOperationEntry(flags, sgDir, "merge", pos, true, mergeExtra(other, mergeOutcomeFastForward))
	reportMergeWithoutCommit(flags, pos, mergeOutcomeFastForward, otherSHA)
	return exitcode.OK
}

// mergeExtra is the per-merge half of an oplog entry: which branch was named,
// and which of a merge's several outcomes this was.
func mergeExtra(other, outcome string) map[string]interface{} {
	return map[string]interface{}{"branch": other, "outcome": outcome}
}

// reportMergeWithoutCommit reports the outcomes that created no commit: a
// fast-forward, a merge left parked, and a branch that was already up to date.
func reportMergeWithoutCommit(flags globalFlags, pos oplogPosition, outcome, newTip string) {
	flags.payload(mergePayload{
		Operation:      "merge",
		Outcome:        outcome,
		Ref:            pos.ref,
		SHA:            optionalSHA(newTip),
		Parents:        []string{},
		Tree:           nil,
		Files:          []string{},
		StateCleared:   false,
		Attempts:       0,
		DeclinedChecks: []declinedCheck{},
		DryRun:         flags.dryRun,
	})
	if flags.silent() {
		return
	}
	switch outcome {
	case mergeOutcomeFastForward:
		fmt.Printf("[%s %s] fast-forwarded\n", refShortName(pos.ref), shortSHA(newTip))
		fmt.Printf(" no merge commit was created, so there is nothing for 'safegit undo' to reverse\n")
	case mergeOutcomeParked:
		fmt.Printf("[%s] the merge is computed and staged; nothing is committed\n", refShortName(pos.ref))
	case mergeOutcomeUpToDate:
		fmt.Printf("[%s] already up to date\n", refShortName(pos.ref))
	}
}

// secondParentOf names the side that was merged in, for the human summary. It
// is read off the commit rather than off the operator's argument, so the line
// says which commit came in rather than which name was typed.
func secondParentOf(out conclusionResult) string {
	if out.commit == nil || len(out.commit.Parents) < 2 {
		return ""
	}
	return out.commit.Parents[1]
}

// withoutMergeSelectors removes the operator's fast-forward and commit
// selections from the argv the compute step forwards. Everything past a bare
// `--` is left alone, because there it is a path and not an option -- and a
// merge carrying one has already been refused.
func withoutMergeSelectors(args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...)
		}
		if mergeSelectorOptions[a] {
			continue
		}
		out = append(out, a)
	}
	return out
}

// strPtr and optionalSHA render the payload's two nullable members. An outcome
// that created no commit has no tree, and one that moved no ref has no new tip;
// both are null rather than empty, because a consumer reading "" would have to
// know it means "there was none".
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func optionalSHA(sha string) *string { return strPtr(sha) }

// mergePayload is what `safegit merge` puts in the envelope's payload.
//
// It follows the conclusion payload -- the members merge-continue reports --
// minus the resolution members, which a merge safegit itself started never has:
// it concludes a clean result, and a conflicted one is not concluded here at
// all. What it adds is `outcome`, because a merge has more than one way to
// succeed and the other members cannot be read without knowing which one
// happened: a fast-forward carries a new tip and no commit, a parked merge
// carries neither.
type mergePayload struct {
	Operation string `json:"operation"`
	// Outcome is one of merge-commit, fast-forward, parked, up-to-date.
	Outcome string `json:"outcome"`
	Ref     string `json:"ref"`
	// SHA is the tip the branch stands at after the operation: the merge commit
	// on the merge-commit outcome, the incoming tip on a fast-forward, and null
	// where nothing moved.
	SHA     *string  `json:"sha"`
	Parents []string `json:"parents"`
	// Tree is the merge commit's tree, and null where no commit was created.
	Tree           *string         `json:"tree"`
	Files          []string        `json:"files"`
	StateCleared   bool            `json:"state_cleared"`
	Attempts       int             `json:"attempts"`
	DeclinedChecks []declinedCheck `json:"declined_checks"`
	DryRun         bool            `json:"dry_run"`
}

// mergePayloadSchema is merge's machine payload contract.
var mergePayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"operation": strictcli.SchemaType("string"),
		"outcome":   strictcli.SchemaType("string"),
		"ref":       strictcli.SchemaType("string"),
		"sha":       strictcli.SchemaType("string", "null"),
		"parents":   strictcli.SchemaArray(strictcli.SchemaType("string")),
		"tree":      strictcli.SchemaType("string", "null"),
		"files":     strictcli.SchemaArray(strictcli.SchemaType("string")),
		"declined_checks": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"check":  strictcli.SchemaType("string"),
				"path":   strictcli.SchemaType("string"),
				"reason": strictcli.SchemaType("string"),
			},
			[]string{"check", "path", "reason"},
			false,
		)),
		"state_cleared": strictcli.SchemaType("boolean"),
		"attempts":      strictcli.SchemaType("integer"),
		"dry_run":       strictcli.SchemaType("boolean"),
	},
	[]string{"operation", "outcome", "ref", "sha", "parents", "tree", "files", "state_cleared", "attempts", "declined_checks", "dry_run"},
	false,
)

// mergeHelp is the command's registered help text.
const mergeHelp = "merge one branch into the current one, and author the result: safegit decides the fast-forward itself and moves the ref under compare-and-swap, or runs git's merge machinery with --no-ff --no-commit and commits the staged result through its own pipeline -- so a merge safegit performed carries safegit's trailers, ran the repository's commit-msg hook and is reversible with 'safegit undo'. A merge git stops on a conflict parks, and 'safegit merge-continue' concludes it; 'safegit merge --continue' is refused and names that command. The command line is a deliberate subset of git's: exactly one branch (no octopus), no strategy selection, no --squash, no --edit and no --autostash. --no-commit computes the merge and leaves it parked even when it is clean"
