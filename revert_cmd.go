package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// `safegit revert <commit>`, restructured -- and there is no second form.
//
// As a plain passthrough, git authored the commit: it carried none of safegit's
// trailers, none of safegit's commit-time machinery ran, and git left its
// AUTO_MERGE behind.
//
// All of it goes away by splitting the operation where git itself splits it:
// `git revert --no-commit` COMPUTES the inverse patch and stages it, and the
// conclusion engine (the same one behind `safegit revert-continue`) turns that
// staged result into a commit -- pipeline-authored, trailers injected,
// commit-msg hook run. The IDENTITY is deliberately unchanged from the
// passthrough: a revert is a new change of the reverter's own, so it records
// the operator, exactly as git's own revert does.
//
// What DID go away is the fallback arm. The restructure used to apply only
// where every flag was one it could honor and the revisions named exactly one
// commit, and everything else fell through to a passthrough where git authored
// the commits. That arm is deleted: a command line safegit cannot author is now
// refused, by name, rather than quietly handed to git -- see
// revertSubset (subset_allowlist.go) and refuseUnsupportedRevert.
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

// runRevert dispatches `safegit revert`.
//
// Two routes stay guarded passthroughs, and both for the same reason: they
// author nothing. The state-control verbs act on an operation git already has
// in flight, and `--no-commit` asks git to stage the inverse patch and stop.
// `--continue` is refused inside, by name, because concluding a revert is
// safegit's own job.
func runRevert(flags globalFlags, args []string) int {
	parsed := parseGitArgs("revert", args)

	// The allowlist covers EVERY route, the state-control forms included: an
	// option safegit does not implement is refused whichever verb it accompanies.
	if code := revertSubset.refuseUnsupportedOptions(parsed); code != 0 {
		return code
	}
	if parsed.Has("--continue", "--abort", "--quit") {
		return runGuardedPassthrough(flags, "revert", args)
	}
	if parsed.Has("-n", "--no-commit") {
		return runGuardedPassthrough(flags, "revert", args)
	}
	return runRestructuredRevert(flags, args, parsed)
}

// refuseUnsupportedRevert refuses the command lines safegit's revert does not
// implement, before any lock is taken and before any git runs.
//
// Every refusal is parser-shaped (exit 2) and names the capability that is
// absent, because "safegit does not do this" is a different answer from "this
// failed" and an operator has to be able to tell them apart. Each needs its
// entry in the divergences catalog.
func refuseUnsupportedRevert(parsed gitArgs) int {
	if len(parsed.AfterDoubleDash) > 0 {
		fmt.Fprintf(os.Stderr, "error: safegit revert takes no pathspec\n")
		fmt.Fprintf(os.Stderr, "  a pathspec undoes part of a commit and records a message claiming the whole of it.\n")
		fmt.Fprintf(os.Stderr, "  Revert the commit, or make the partial change yourself and commit it.\n")
		return exitcode.Usage
	}

	// BEFORE the count, because a rev-set is one argv token and counting tokens
	// would call `A..B` a single commit.
	for _, rev := range parsed.Revisions {
		if code := refuseRevisionSet("revert", rev); code != 0 {
			return code
		}
	}

	switch len(parsed.Revisions) {
	case 1:
	case 0:
		fmt.Fprintf(os.Stderr, "error: safegit revert names no commit to revert\n")
		fmt.Fprintf(os.Stderr, "  usage: safegit revert <commit>\n")
		return exitcode.Usage
	default:
		fmt.Fprintf(os.Stderr, "error: safegit revert takes exactly one commit, and this names %d\n", len(parsed.Revisions))
		fmt.Fprintf(os.Stderr, "  %s\n", sequentialFormReason("revert"))
		return exitcode.Usage
	}
	return 0
}

// runRestructuredRevert computes the revert with git and commits it with
// safegit.
func runRestructuredRevert(flags globalFlags, args []string, parsed gitArgs) int {
	// FIRST, and before the repository is touched at all: a command line
	// safegit itself refuses is refused without a lock, without an
	// auto-initialization and without a git call.
	if code := refuseUnsupportedRevert(parsed); code != 0 {
		return code
	}

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

	computeArgs := append([]string{"revert", "--no-commit"}, args...)

	if flags.dryRun {
		// The would-do record is the argv the EXECUTE path runs, which is the
		// compute step's and not the operator's: a preview that recorded a bare
		// `git revert` would describe the operation safegit stopped performing.
		return previewSequencerOperation(flags, "revert", args, computeArgs)
	}

	ctx := flags.ctx()
	pos := readOplogPosition(flags)
	reverted := parsed.Revisions[0]

	// The compute step. --no-commit is what makes the two halves separable: git
	// works out the inverse patch and stages it, and stops before the commit
	// that would otherwise be git's.
	if code := runGitMutation(flags, computeArgs...); code != 0 {
		// A conflict lands here, and it is not an error path in any sense that
		// needs handling: the repository holds the ordinary conflicted-revert
		// state and `safegit revert-continue` concludes it. announceWayOut says
		// exactly that, from the single way-out authority.
		//
		// A git refusal that staged nothing lands here too, and the difference
		// is the same one the cherry-pick draws: only a stopped operation is
		// worth recording as one.
		if pickLeftAConflict(ctx) {
			appendOperationEntry(flags, sgDir, "revert", pos, false, revertExtra(reverted))
		}
		announceWayOut(flags, gitDir)
		return code
	}

	return concludeComputedRevert(flags, gitDir, sgDir, reverted)
}

// revertExtra is the per-revert half of an oplog entry: which commit was named.
// The outcome field is appendOperationEntry's own.
func revertExtra(reverted string) map[string]interface{} {
	return map[string]interface{}{"commit": reverted}
}

// concludeComputedRevert turns the staged result of `git revert --no-commit`
// into a safegit commit, through the same engine `safegit revert-continue` runs.
//
// It reads the state git just wrote rather than anything it was told: the
// commit being reverted comes from REVERT_HEAD (so the report names the right
// commit) and the message from MERGE_MSG. Nothing here is a second
// implementation of the conclusion -- the resolution vocabulary, the marker
// verification and the state cleanup are the ones in sequencer_continue.go.
func concludeComputedRevert(flags globalFlags, gitDir, sgDir, reverted string) int {
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

	// The conclusion itself is the shared one. What is revert's own is stated
	// here and nowhere else: a revert that changes nothing has its own refusal,
	// the oplog entry and the parent bump both name the operation `revert` --
	// one operation, one entry, under the COMMAND's own name -- and the identity
	// recorded is the OPERATOR's, git's own revert semantics, resolved inside
	// the engine from the same one place `revert-continue` resolves it.
	out, exit, ok := concludeParkedOperation(flags, gitDir, sgDir, state, parkedConclusion{
		op:           op,
		oplogOp:      "revert",
		parentBumpOp: "revert",
		onEmpty:      refuseEmptyRevert,
	})
	if !ok {
		return exit
	}

	flags.payload(revertPayload{
		Operation:      "revert",
		Source:         state.Source,
		Ref:            out.commit.Ref,
		SHA:            realSHA(flags, out.commit.SHA),
		Parents:        orEmpty(out.commit.Parents),
		Tree:           out.commit.Tree,
		Files:          orEmpty(out.commit.Files),
		Author:         reportedAuthor(out.author),
		StateCleared:   out.cleared,
		Attempts:       out.commit.Attempts,
		DeclinedChecks: orEmptyDeclines(out.declines),
		DryRun:         flags.dryRun,
	})
	op.renderHuman(flags, out, "reverted "+short(reverted, state.Source))
	return exit
}

// revertPayload is what `safegit revert` puts in the envelope's payload.
//
// It follows the conclusion payload -- the members revert-continue reports --
// minus the resolution members, which a revert safegit itself started never has
// (it concludes a clean result; a conflicted one is not concluded here at all),
// and minus the queue members, which a single-form command cannot produce.
// Reusing revert-continue's schema was not an option: it requires the queue
// members, and a document that filled them in would state something false about
// every run.
//
// What it adds is `source`: the commit that was reverted is the one fact about
// this operation the other members cannot express, because a revert's parents
// name the branch it went onto and not the change it undid.
type revertPayload struct {
	Operation string `json:"operation"`
	// Source is the commit that was reverted, as it was resolved rather than as
	// it was typed.
	Source string `json:"source"`
	Ref    string `json:"ref"`
	// SHA is the commit that was created, and null under --dry-run.
	SHA     *string  `json:"sha"`
	Parents []string `json:"parents"`
	Tree    string   `json:"tree"`
	Files   []string `json:"files"`
	// Author is the identity the commit RECORDS, which for a revert is the
	// OPERATOR on both fields: undoing something is your own new change.
	Author         continueAuthor  `json:"author"`
	StateCleared   bool            `json:"state_cleared"`
	Attempts       int             `json:"attempts"`
	DeclinedChecks []declinedCheck `json:"declined_checks"`
	DryRun         bool            `json:"dry_run"`
}

// revertPayloadSchema is revert's machine payload contract.
var revertPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"operation": strictcli.SchemaType("string"),
		"source":    strictcli.SchemaType("string"),
		"ref":       strictcli.SchemaType("string"),
		"sha":       strictcli.SchemaType("string", "null"),
		"parents":   strictcli.SchemaArray(strictcli.SchemaType("string")),
		"tree":      strictcli.SchemaType("string"),
		"files":     strictcli.SchemaArray(strictcli.SchemaType("string")),
		"author": strictcli.SchemaObject(
			map[string]interface{}{
				"name":  strictcli.SchemaType("string"),
				"email": strictcli.SchemaType("string"),
			},
			[]string{"name", "email"},
			false,
		),
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
	[]string{"operation", "source", "ref", "sha", "parents", "tree", "files", "author", "state_cleared", "attempts", "declined_checks", "dry_run"},
	false,
)

// revertHelp is the command's registered help text.
const revertHelp = "revert ONE commit by applying its inverse patch, and author the result: git computes the inverse with --no-commit and safegit commits the staged result through its own pipeline -- so a revert safegit performed carries safegit's trailers, ran the repository's commit-msg hook, is reversible with 'safegit undo', and declares the INVERSE of every move record the reverted commit declared. YOU are recorded as both author and committer, because a revert is your own new change rather than the reverted author's; that is git's own division and the opposite of what a cherry-pick does. A revert git stops on a conflict parks, and 'safegit revert-continue' concludes it; 'safegit revert --continue' is refused and names that command. The command line is a deliberate subset of git's: exactly one commit named as a commit (a range or any other revision set is refused, because a range hands the operation to git's sequencer even when it holds one commit), no --edit, no --commit, no --cleanup and no signing. --abort, --quit and --no-commit stay plain passthroughs, because they author nothing"

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
