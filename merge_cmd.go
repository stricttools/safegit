package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
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
// mergeSubset (subset_allowlist.go) and refuseUnsupportedMerge.

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

// mergeSelectorOptions are the operator's fast-forward and commit selections.
// safegit answers both questions itself, so they are removed from the argv the
// compute step forwards -- which carries `--no-ff --no-commit` of its own and
// would otherwise be handed a contradiction.
//
// `--commit` is not among them: it is refused by name (mergeSubset), so it
// never reaches an argv there is anything to remove it from.
var mergeSelectorOptions = map[string]bool{
	"--ff":        true,
	"--no-ff":     true,
	"--ff-only":   true,
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

	// The allowlist covers EVERY route, the state-control forms included: an
	// option safegit does not implement is refused whichever verb it accompanies.
	if code := mergeSubset.refuseUnsupportedOptions(parsed); code != 0 {
		return code
	}
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
// failed" and an operator has to be able to tell them apart. Each needs its
// entry in the divergences catalog.
func refuseUnsupportedMerge(parsed gitArgs) int {
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

// octopusReason is the one sentence that says why an octopus is not in the
// subset, spelled once so anything else that has to refuse one can say the
// same thing.
//
// DIVERGENCE: git merges any number of branches in one commit; safegit merges
// one. Cataloged in docs/divergences.md as "A merge has exactly one other
// side".
const octopusReason = "a conclusion has one staged result to check and one message to write, however many sides went into it, and every check safegit makes over a merge is written against two"

// fetchHeadName is the one merge argument that does not name one side.
//
// Every other revision resolves to a single commit, so COUNTING the revisions
// on the command line answers "how many sides is this merge" -- which is what
// refuseUnsupportedMerge does. FETCH_HEAD is git's one exception: when it is
// the sole argument, and spelled exactly this way, git expands it into every
// branch the fetch marked for merging (builtin/merge.c's collect_parents), so
// one token can be an octopus.
const fetchHeadName = "FETCH_HEAD"

// forMergeHeads counts the sides FETCH_HEAD names.
//
// The file holds one record per fetched ref -- `<sha>\t<flag>\t<description>`
// -- and the flag is `not-for-merge` on every ref the fetch brought in for a
// remote-tracking branch alone. Counting LINES would be wrong and would refuse
// the commonest pull there is: a stock refspec fetch writes a line per remote
// branch and marks all but the current branch's upstream not-for-merge.
func forMergeHeads(gitDir string) (int, error) {
	data, err := os.ReadFile(filepath.Join(gitDir, "FETCH_HEAD"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if fields := strings.Split(line, "\t"); len(fields) > 1 && fields[1] == "not-for-merge" {
			continue
		}
		n++
	}
	return n, nil
}

// refuseFetchHeadOctopus refuses a merge of a FETCH_HEAD that names more than
// one side.
//
// It is asked BEFORE the fast-forward decision, and that ordering is the whole
// point: safegit answers the ancestry question by resolving FETCH_HEAD to a
// commit, and `git rev-parse FETCH_HEAD` reads the FIRST line of the file. A
// check made after it would let the fast-forward arm move the branch onto one
// fetched head and drop the rest without a word, while the merge arm authored a
// commit with a parent per side -- both from a command line whose other
// spelling (`safegit merge br1 br2`) is a refusal.
//
// `safegit pull` inherits it: its merge step is this one with FETCH_HEAD as the
// incoming side, and it runs after the fetch that wrote the file.
//
// An unreadable or absent FETCH_HEAD is left to git, which is the same
// convention the ancestry questions follow for an unresolvable revision: the
// compute step below produces git's own verdict on the argument rather than one
// safegit invented.
//
// DIVERGENCE: git merges every head FETCH_HEAD names, in one octopus commit;
// safegit refuses. Cataloged in docs/divergences.md as "A fetch that marked
// several branches is not a merge safegit will make".
func refuseFetchHeadOctopus(gitDir, other, command string) int {
	if other != fetchHeadName {
		return 0
	}
	heads, err := forMergeHeads(gitDir)
	if err != nil || heads < 2 {
		return 0
	}

	fmt.Fprintf(os.Stderr, "error: safegit %s cannot merge FETCH_HEAD: the fetch marked %d branches for merging\n", command, heads)
	fmt.Fprintf(os.Stderr, "  FETCH_HEAD is the one name git expands into several sides -- every branch the fetch\n")
	fmt.Fprintf(os.Stderr, "  marked for merging becomes a parent -- so one argument here asks for an octopus merge,\n")
	fmt.Fprintf(os.Stderr, "  and merging several branches in one commit is not part of safegit's subset:\n")
	fmt.Fprintf(os.Stderr, "  %s.\n", octopusReason)
	fmt.Fprintf(os.Stderr, "  Bring them in one at a time: 'safegit merge <branch>' per side, or 'safegit pull <remote> <branch>'\n")
	fmt.Fprintf(os.Stderr, "  per branch. See docs/divergences.md.\n")
	return exitcode.Usage
}

// refuseUnrelatedHistories refuses a merge whose two sides share no commit at
// all, before anything computes.
//
// THE PREDICATE is two questions, and dropping either one breaks something:
//
//   - HEAD RESOLVES. On an UNBORN branch there is no commit to take a merge
//     base from, and a merge into one is a plain fast-forward that must keep
//     working. A predicate written as "merge-base failed" rather than this one
//     would refuse every unborn merge safegit deliberately supports.
//   - `merge-base` REPORTS NO BASE -- exit 1 with no base, which is git's own
//     answer to this exact question. A merge-base that could not be computed at
//     all (an unresolvable argument) is not an answer, and is left to git,
//     which is the convention every other verdict here follows.
//
// git's own refusal is the bare `fatal: refusing to merge unrelated histories`,
// which names nothing an operator can act on. This one names the import route
// instead: raw git computes the merge, safegit's conclusion commits it.
//
// THE DIAGNOSIS BRANCHES, because "no merge base" has a second producer. On a
// SHALLOW clone the base may exist and simply lie below the fetch depth, where
// this object store cannot see it -- `merge-base` exits 1 there exactly as it
// does for two unrelated roots. The refusal is right either way (raw git refuses
// the same merge for the same reason), but the never-connected diagnosis and the
// import route it recommends are both FALSE for that repository, so they are
// spoken only when the repository is not shallow; a shallow one is told to
// deepen the clone and try again.
//
// It is called from BOTH of merge's paths, exactly as refuseFetchHeadOctopus
// is, because the shared merge path holds no preview branch -- and `pull`
// inherits the real-mode one, its merge step being this one.
//
// DIVERGENCE: git merges unrelated histories when told to; safegit refuses the
// flag and the shape. Cataloged in docs/divergences.md as "Merging unrelated
// histories is refused, flag and pre-flight both".
func refuseUnrelatedHistories(ctx context.Context, other, command string) int {
	if git.HeadIsUnborn(ctx) {
		return 0
	}
	have, ok := git.HaveMergeBase(ctx, "HEAD", other)
	if !ok || have {
		return 0
	}

	if git.IsShallowRepository(ctx) {
		fmt.Fprintf(os.Stderr, "error: safegit %s cannot merge %s: no merge base is visible in this repository\n", command, other)
		fmt.Fprintf(os.Stderr, "  this is a SHALLOW clone -- part of its history was never fetched -- so the two sides\n")
		fmt.Fprintf(os.Stderr, "  may well connect below the fetch depth, where nothing here can see it. git's own\n")
		fmt.Fprintf(os.Stderr, "  words for what it finds are \"refusing to merge unrelated histories\", and it refuses\n")
		fmt.Fprintf(os.Stderr, "  this merge too; what safegit will not do is guess which of the two states you are in.\n")
		fmt.Fprintf(os.Stderr, "  Fetch the rest of the history, then run the same command again:\n")
		fmt.Fprintf(os.Stderr, "    git fetch --unshallow\n")
		fmt.Fprintf(os.Stderr, "  If the merge is still refused afterwards, the two sides really do share no commit,\n")
		fmt.Fprintf(os.Stderr, "  and that refusal names the route for the deliberate import.\n")
		fmt.Fprintf(os.Stderr, "  See docs/divergences.md.\n")
		return exitcode.General
	}

	fmt.Fprintf(os.Stderr, "error: safegit %s cannot merge %s: the two sides share no commit at all\n", command, other)
	fmt.Fprintf(os.Stderr, "  there is no merge base between this branch and %s -- they are separate histories that\n", other)
	fmt.Fprintf(os.Stderr, "  were never connected -- so the merge would record a second root rather than bring two\n")
	fmt.Fprintf(os.Stderr, "  lines of work together. git's own words for it are \"refusing to merge unrelated\n")
	fmt.Fprintf(os.Stderr, "  histories\", and git offers --allow-unrelated-histories to elect it anyway; safegit\n")
	fmt.Fprintf(os.Stderr, "  does not, because the state is nearly always reached by accident: a wrong remote, a\n")
	fmt.Fprintf(os.Stderr, "  wrong branch, or a repository re-initialized over another.\n")
	fmt.Fprintf(os.Stderr, "  For the deliberate import -- bringing another project's history in, which a repository\n")
	fmt.Fprintf(os.Stderr, "  does once in its life -- compute it with git and commit it with safegit:\n")
	fmt.Fprintf(os.Stderr, "    git merge --no-commit --allow-unrelated-histories %s\n", other)
	fmt.Fprintf(os.Stderr, "    safegit merge-continue\n")
	fmt.Fprintf(os.Stderr, "  See docs/divergences.md.\n")
	return exitcode.General
}

// runRestructuredMerge is the whole flow.
func runRestructuredMerge(flags globalFlags, args []string, parsed gitArgs) int {
	// FIRST, and before the repository is touched at all: a command line
	// safegit itself refuses is refused without a lock, without an
	// auto-initialization and without a git call.
	if code := refuseUnsupportedMerge(parsed); code != 0 {
		return code
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

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

	// Defense in depth. A parked MERGE dirties the working tree by construction,
	// so coordGuard above already refuses the common case with the listing of
	// what is in it -- but a parked state of ANOTHER kind whose result equals the
	// current tree leaves the tree clean, and merge's compute step would then run
	// against it. The refusal is structural rather than a consequence of the
	// dirt.
	if code := refuseComputeOverInFlight(gitDir, "merge"); code != 0 {
		return code
	}

	if flags.dryRun {
		// A merge safegit refuses is refused whether or not the run was going to
		// happen: a preview of a command that cannot run is not a preview of
		// anything. Asked here because the preview never reaches performMerge,
		// which is where the same refusal covers every real run and `pull`.
		if code := refuseFetchHeadOctopus(gitDir, parsed.Revisions[0], "merge"); code != 0 {
			return code
		}
		// And the unborn forms, in the same order the real run asks them, so the
		// preview REFUSES exactly the command lines the run refuses. Without it
		// the preview would reach previewMerge's unborn short-circuit and answer
		// "a fast-forward" for a command line that cannot run at all.
		if code := refuseUnbornMergeForm(flags.ctx(), parsed.Has("--no-ff"), parsed.Has("--no-commit"), "--no-ff", "--no-commit", unbornMergeWayOut); code != 0 {
			return code
		}
		// And the unrelated-histories pre-flight, for the same reason and from
		// the same place: performMerge is where every real run meets it, and a
		// preview never reaches performMerge. Nothing is recorded here -- a
		// preview writes no oplog entry, refusal or not.
		if code := refuseUnrelatedHistories(flags.ctx(), parsed.Revisions[0], "merge"); code != 0 {
			return code
		}

		// No git merge runs: the invocation is recorded, and the outcome it
		// would have is COMPUTED with git's own merge engine instead of guessed.
		//
		// What is RECORDED is the compute step's argv, which is what the execute
		// path runs: --no-ff and --no-commit are safegit's and always present,
		// and the caller's own fast-forward and commit selections never reach
		// git. A would-do log saying `git merge feature` would describe a
		// mutation nothing performs.
		//
		// WHETHER it is recorded is decided further down, by the preview itself.
		// Two answers never reach this argv at all: a FAST-FORWARD, where
		// performMerge takes its fast-forward arm first, and an `--ff-only`
		// REFUSAL, where it exits before the compute step. So the record is handed
		// to previewSequencerOperation as a closure rather than emitted here. See
		// previewMerge.
		recorded := append([]string{"merge", "--no-ff", "--no-commit"}, withoutMergeSelectors(args)...)
		return previewSequencerOperation(flags, "merge", args, recorded)
	}

	other := parsed.Revisions[0]
	payload, ok, exit := performMerge(flags, gitDir, sgDir, readOplogPosition(flags), mergeRequest{
		other:        other,
		computeArgs:  withoutMergeSelectors(args),
		noFF:         parsed.Has("--no-ff"),
		ffOnly:       parsed.Has("--ff-only"),
		park:         parsed.Has("--no-commit"),
		op:           "merge",
		extraBase:    map[string]interface{}{"branch": other},
		ffOnlyFlag:   "--ff-only",
		ffOnlyWayOut: fmt.Sprintf("  Re-run without --ff-only to make one, or rebase this branch onto %s instead.\n", other),
		noFFFlag:     "--no-ff",
		parkFlag:     "--no-commit",
		unbornWayOut: unbornMergeWayOut,
		headline: func(out conclusionResult) string {
			return "merged " + short(other, secondParentOf(out))
		},
	})
	if ok {
		flags.payload(payload)
	}
	return exit
}

// mergeRequest is one merge to perform, whatever command asked for it.
//
// It exists because `safegit pull` is `safegit merge` with the incoming side
// fetched first: the fast-forward decision, the compare-and-swap ref move, the
// index-and-worktree sync, the compute step pinned to `--no-ff --no-commit` and
// the immediate conclusion are one implementation, and the differences between
// the two commands are the fields below rather than a second copy of all of it.
type mergeRequest struct {
	// other is the revision being merged in: a branch name for `merge`,
	// FETCH_HEAD for `pull`.
	other string
	// computeArgs are the arguments forwarded after `merge --no-ff --no-commit`.
	computeArgs []string
	// noFF, ffOnly and park are safegit's reading of what the caller asked for,
	// resolved from an argv for `merge` and from --merge-strategy for `pull`.
	noFF, ffOnly, park bool
	// op names the operation in the op log AND in the pipeline's own entry, so
	// one operation writes one entry under the command's own name.
	op string
	// extraBase is the per-command half of the oplog entry: the branch a merge
	// named, the remote and branch a pull fetched from.
	extraBase map[string]interface{}
	// ffOnlyFlag is how the caller's operator SPELLED the fast-forward-only
	// declaration, and ffOnlyWayOut is the line that says what to do instead.
	// Both differ per command, and a refusal naming a flag the operator did not
	// type is a refusal they cannot act on.
	ffOnlyFlag   string
	ffOnlyWayOut string
	// noFFFlag and parkFlag are the same thing for the other two selections,
	// which the unborn-branch refusal names, and unbornWayOut is that refusal's
	// advice line -- a pull asks for a fast-forward by naming another merge
	// strategy rather than by leaving a flag off. `pull` has no parking form, so
	// its parkFlag is empty and its park field is never set.
	noFFFlag, parkFlag string
	unbornWayOut       string
	// headline words the one-line human summary of a merge that made a commit.
	headline func(out conclusionResult) string
}

// oplogExtra builds this request's oplog fields. An empty outcome leaves the
// field to appendOperationEntry, which writes the ok/failed pair; a named one
// says which of the several ways a merge can end this was.
func (req mergeRequest) oplogExtra(outcome string) map[string]interface{} {
	extra := make(map[string]interface{}, len(req.extraBase)+1)
	for k, v := range req.extraBase {
		extra[k] = v
	}
	if outcome != "" {
		extra["outcome"] = outcome
	}
	return extra
}

// performMerge is the merge itself, from the fast-forward decision to the
// commit, shared by `safegit merge` and `safegit pull`.
//
// The caller has already taken the operation lock, run the coordination check
// and answered the auto-bump question; what happens here is the operation.
//
// ok reports whether there is a payload to report: the refusal paths and the
// conflict path produce none, and their exit code is the whole answer.
func performMerge(flags globalFlags, gitDir, sgDir string, pos oplogPosition, req mergeRequest) (mergePayload, bool, int) {
	ctx := flags.ctx()
	other := req.other

	// FIRST, because the ancestry question below cannot ask it: FETCH_HEAD may
	// name several sides, and resolving it to a commit answers about the first
	// one alone. See refuseFetchHeadOctopus.
	if code := refuseFetchHeadOctopus(gitDir, req.other, req.op); code != 0 {
		return mergePayload{}, false, code
	}

	// BEFORE the fast-forward arm, because the two forms refused here are
	// exactly the ones that would skip it and hand an unborn head to git's merge
	// machinery, which is fatal there. `pull` asks this same question before its
	// fetch, so its refusal costs no network round-trip; the copy here covers
	// every other route into a merge.
	//
	// It is RECORDED, unlike the command-line refusals above, and the line
	// between them is the one the --ff-only neighbor draws: those are about what
	// was typed, this is about where the branch stands, and only the second is a
	// fact about the repository an audit trail is for.
	if code := refuseUnbornMergeForm(ctx, req.noFF, req.park, req.noFFFlag, req.parkFlag, req.unbornWayOut); code != 0 {
		appendOperationEntry(flags, sgDir, req.op, pos, false, req.oplogExtra(oplogOutcomeFailed))
		return mergePayload{}, false, code
	}

	// BEFORE the ancestry questions and the compute step: two histories that
	// share no commit have no fast-forward to find between them either, and
	// git's own refusal names nothing an operator can act on. RECORDED, on the
	// same line the --ff-only neighbor draws -- no merge base is a fact about
	// where the branches stand rather than about what was typed.
	if code := refuseUnrelatedHistories(ctx, other, req.op); code != 0 {
		appendOperationEntry(flags, sgDir, req.op, pos, false, req.oplogExtra(oplogOutcomeFailed))
		return mergePayload{}, false, code
	}

	// What safegit decides for itself, and it decides it BEFORE git runs.
	//
	// An unresolvable revision is deliberately left to git: `safegit merge
	// no-such-ref` must exit with git's own verdict on that argument, not with
	// a message safegit invented, so the ancestry questions are simply not
	// asked and the compute step below produces git's error.
	// An UNBORN branch is a fast-forward from nothing, and it has to be read
	// that way rather than left to git: `git merge --no-ff` into an empty head
	// is a fatal error ("Non-fast-forward commit does not make sense into an
	// empty head"), so the compute step cannot serve this case at all. The
	// compare-and-swap below is pinned to the zero object name, git's own
	// spelling for "this ref must not exist yet".
	var upToDate, fastForward bool
	otherSHA, resolveErr := git.RevParse(ctx, other+"^{commit}")
	switch {
	case resolveErr != nil:
	case pos.oldTip == "":
		fastForward = true
	default:
		upToDate, _ = git.IsAncestorOf(ctx, otherSHA, pos.oldTip)
		if !upToDate {
			fastForward, _ = git.IsAncestorOf(ctx, pos.oldTip, otherSHA)
		}
	}

	// DIVERGENCE: git's own --ff-only refusal is git's; this one is safegit's,
	// and it exists because letting git decide would let git move the ref --
	// outside safegit's compare-and-swap -- in the race where the branches stop
	// being diverged between the check and the run. Cataloged in
	// docs/divergences.md as "The fast-forward-only refusal is safegit's, not
	// git's", exit code included: safegit's General rather than git's 128.
	if req.ffOnly && resolveErr == nil && !upToDate && !fastForward {
		fmt.Fprintf(os.Stderr, "error: %s is not a fast-forward of %s, and %s was given\n",
			other, refShortName(pos.ref), req.ffOnlyFlag)
		fmt.Fprintf(os.Stderr, "  the two branches have both moved on, so bringing them together needs a merge commit.\n")
		fmt.Fprint(os.Stderr, req.ffOnlyWayOut)
		// Recorded, like every other verdict on the merge itself. The
		// command-line refusals above record nothing, and the line between them
		// is where the answer comes from: those are about what was typed, this
		// one is about where the branch stands, and only the second is a fact
		// about the repository that an audit trail is for.
		appendOperationEntry(flags, sgDir, req.op, pos, false, req.oplogExtra(oplogOutcomeFailed))
		return mergePayload{}, false, exitcode.General
	}

	// The fast-forward is taken only where nothing the operator said asks for a
	// commit. `--no-commit` is such a request: it says the operator wants to
	// look at the result before it becomes anything, and a fast-forward that
	// moved the branch silently would deny exactly that. A DETACHED HEAD is
	// excluded too -- there is no ref to compare-and-swap -- and falls through
	// to the compute step, where the conclusion refuses with the remedy.
	//
	// DIVERGENCE: `git merge --no-commit` fast-forwards anyway, because git
	// takes the fast-forward before the flag is consulted. safegit parks a
	// merge instead, so `--no-commit` means the same thing in every case an
	// operator can reach. Cataloged in docs/divergences.md as "A parked merge
	// stays parked, even when it could have fast-forwarded".
	if fastForward && !req.noFF && !req.park && pos.ref != "" {
		return fastForwardMerge(flags, gitDir, sgDir, pos, req, otherSHA)
	}

	// The compute step. --no-ff and --no-commit are safegit's, always; the
	// caller's own fast-forward and commit selections never reach it.
	computeArgs := append([]string{"merge", "--no-ff", "--no-commit"}, req.computeArgs...)
	if code := runGitMutation(flags, gitexec.NoDoor, computeArgs...); code != 0 {
		// A conflict lands here, and it is not an error path in any sense that
		// needs handling: the repository holds the ordinary conflicted-merge
		// state and `safegit merge-continue` concludes it. announceWayOut says
		// exactly that, from the single way-out authority.
		appendOperationEntry(flags, sgDir, req.op, pos, false, req.oplogExtra(oplogOutcomeFailed))
		announceWayOut(flags, gitDir)
		return mergePayload{}, false, code
	}

	state, err := sequencer.Read(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the merge state git just wrote: %v\n", err)
		return mergePayload{}, false, exitcode.General
	}
	if state.Kind != sequencer.KindMerge {
		// git had nothing to merge -- the branch already contains the other
		// side -- and said so on its own output. Nothing moved.
		appendOperationEntry(flags, sgDir, req.op, pos, true, req.oplogExtra(mergeOutcomeUpToDate))
		return reportMergeWithoutCommit(flags, req, pos, mergeOutcomeUpToDate, pos.oldTip)
	}

	if req.park {
		// Parked on purpose. The entry records no new tip, because none exists:
		// an empty new tip is what keeps a non-move invisible to the readers
		// that treat the newest recorded tip as where safegit left the branch.
		appendOperationEntry(flags, sgDir, req.op, pos, false, req.oplogExtra(mergeOutcomeParked))
		announceWayOut(flags, gitDir)
		return reportMergeWithoutCommit(flags, req, pos, mergeOutcomeParked, "")
	}

	// Clean, and nothing asked for it to be left in flight: conclude it now,
	// through the same engine `safegit merge-continue` runs. The message is
	// git's own MERGE_MSG -- which already holds the operator's `-m` text
	// verbatim when they passed one -- so nothing is re-derived here.
	out, exit, ok := concludeParkedOperation(flags, gitDir, sgDir, state, parkedConclusion{
		op: mergeContinueOp,
		// The COMMAND's own name on both, so one operation writes one entry and
		// a parent bump says which command moved the gitlink. For `pull` that
		// is `pull`, not the merge inside it.
		oplogOp:      req.op,
		parentBumpOp: req.op,
		// A merge commit records its parents whether or not it changes a single
		// byte, so the pipeline's tree-unchanged refusal does not apply and
		// onEmpty is unreachable -- which is why no merge caller declares one,
		// and why the empty-park auto-clean behind it never runs for a merge.
		allowEmpty: true,
	})
	if !ok {
		return mergePayload{}, false, exit
	}

	payload := mergePayload{
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
		Residue:        orEmptyResidue(out.residue),
		DryRun:         flags.dryRun,
	}
	mergeContinueOp.renderHuman(flags, out, req.headline(out))
	return payload, true, exit
}

// fastForwardMerge moves the branch onto the incoming tip and puts the index
// and the working tree in step with it.
//
// The ref move is the pipeline's own compare-and-swap port, so it is minted
// through the effects handle and pinned to the tip that was there a moment ago:
// a branch another session moved in between is a refusal, never a clobber.
func fastForwardMerge(flags globalFlags, gitDir, sgDir string, pos oplogPosition, req mergeRequest, otherSHA string) (mergePayload, bool, int) {
	ctx := flags.ctx()
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return mergePayload{}, false, exitcode.General
	}

	timeout := time.Duration(cfg.Lock.AcquireTimeoutSeconds) * time.Second
	lk, err := lock.Acquire(repo.SharedSafegitDir(ctx, gitDir), sgDir, pos.ref, "merge", timeout)
	if err != nil {
		if lock.IsTimeout(err) {
			fmt.Fprintf(os.Stderr, "error: acquiring lock on %s: %v\n", pos.ref, err)
			return mergePayload{}, false, exitcode.LockTimeout
		}
		fmt.Fprintf(os.Stderr, "error: acquiring lock on %s: %v\n", pos.ref, err)
		return mergePayload{}, false, exitcode.General
	}
	defer lk.Release()

	// Re-read the tip UNDER the lock. The ancestry decision was made before it
	// was taken, and the compare-and-swap has to be pinned to what is there
	// now rather than to what was there then. An UNBORN branch is pinned to the
	// zero object name, which asserts the ref still does not exist.
	current, err := git.RevParse(ctx, pos.ref)
	born := err == nil
	switch {
	case !born && pos.oldTip == "":
		current = git.ZeroSHA
	case !born:
		fmt.Fprintf(os.Stderr, "error: reading where %s stands: %v\n", refShortName(pos.ref), err)
		return mergePayload{}, false, exitcode.General
	case current != pos.oldTip:
		from := shortSHA(pos.oldTip)
		if pos.oldTip == "" {
			from = "not existing at all"
		}
		fmt.Fprintf(os.Stderr, "error: %s moved from %s to %s while the merge was being worked out\n",
			refShortName(pos.ref), from, shortSHA(current))
		fmt.Fprintf(os.Stderr, "  nothing was merged. Re-run the merge against the branch as it stands now.\n")
		appendOperationEntry(flags, sgDir, req.op, pos, false, req.oplogExtra(oplogOutcomeFailed))
		return mergePayload{}, false, exitcode.CASExhausted
	}

	if err := (effectsRefUpdate{flags}).Update(ctx, pos.ref, otherSHA, current); err != nil {
		fmt.Fprintf(os.Stderr, "error: fast-forwarding %s: %v\n", refShortName(pos.ref), err)
		appendOperationEntry(flags, sgDir, req.op, pos, false, req.oplogExtra(oplogOutcomeFailed))
		return mergePayload{}, false, exitcode.General
	}

	// Not optional: a ref move alone leaves the INVERSE of the incoming diff
	// staged and the incoming files missing from disk. The same primitive
	// scrub's post-rewrite sync uses, including its protection of tracked
	// files that are also gitignored.
	if _, err := git.SyncMainIndexWithWorktree(ctx, otherSHA); err != nil {
		appendOperationEntry(flags, sgDir, req.op, pos, true, req.oplogExtra(mergeOutcomeFastForward))
		// The commit-stands family, by the same reasoning that puts every other
		// member in it: the ref move is real and a step after it did not finish.
		// A refusal's answer -- no payload, the undifferentiated General -- would
		// tell a machine consumer nothing about a branch that has moved, and the
		// guess a script makes from a 1 is to retry an operation that happened.
		detail := fmt.Sprintf("%s was fast-forwarded to %s and stands there, but %s failed: %v",
			refShortName(pos.ref), shortSHA(otherSHA), stepFastForwardSync, err)
		fmt.Fprintf(os.Stderr, "error: %s was fast-forwarded to %s and stands there, but putting the index and the\n",
			refShortName(pos.ref), shortSHA(otherSHA))
		fmt.Fprintf(os.Stderr, "  working tree in step with it failed: %v\n", err)
		fmt.Fprintf(os.Stderr, "  until that is done, git reports the incoming changes as staged deletions.\n")
		return reportMergeWithoutCommit(flags, req, pos, mergeOutcomeFastForward, otherSHA,
			residueEntry{Step: stepFastForwardSync, Detail: detail})
	}

	appendOperationEntry(flags, sgDir, req.op, pos, true, req.oplogExtra(mergeOutcomeFastForward))
	return reportMergeWithoutCommit(flags, req, pos, mergeOutcomeFastForward, otherSHA)
}

// reportMergeWithoutCommit reports the outcomes that created no commit: a
// fast-forward, a merge left parked, and a branch that was already up to date.
//
// residue is what this outcome owed after its ref move and did not finish,
// which only the fast-forward can carry: nothing moved on the other two. An
// empty list is the ordinary case and exits OK; anything in it carries the
// commit-stands family code, exactly as the commit-authoring routes' does.
func reportMergeWithoutCommit(flags globalFlags, req mergeRequest, pos oplogPosition, outcome, newTip string, residue ...residueEntry) (mergePayload, bool, int) {
	payload := mergePayload{
		Operation:      "merge",
		Outcome:        outcome,
		Ref:            pos.ref,
		SHA:            strPtr(newTip),
		Parents:        []string{},
		Tree:           nil,
		Files:          []string{},
		StateCleared:   false,
		Attempts:       0,
		DeclinedChecks: []declinedCheck{},
		Residue:        orEmptyResidue(residue),
		DryRun:         flags.dryRun,
	}
	if flags.silent() {
		return payload, true, aftercareExit(residue)
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
	return payload, true, aftercareExit(residue)
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

// strPtr renders the payload's nullable members. An outcome that created no
// commit has no tree, and one that moved no ref has no new tip; both are null
// rather than empty, because a consumer reading "" would have to know it means
// "there was none".
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

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
	// Residue is every step this merge owed AFTER its ref move and did not
	// finish, never nil -- the steps after the commit on the merge-commit
	// outcome, and the index-and-working-tree sync on a fast-forward, whose ref
	// move creates no commit but owes the same aftercare. An empty list is a
	// merge that finished everything it owed; anything in it is what the
	// commit-stands exit code is about, and without it an envelope carrying that
	// code says only state_cleared:false.
	//
	// There is no autostash member beside it, unlike the conclusion commands'
	// payload. This command's coordination check refuses a dirty working tree
	// before git runs at all, and `--autostash` is refused outright, so a merge
	// safegit starts can carry no autostash by construction -- a member that
	// could only ever report "none" would be an answer to a question this
	// command cannot be asked.
	Residue []residueEntry `json:"residue"`
	DryRun  bool           `json:"dry_run"`
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
		"residue": strictcli.SchemaArray(strictcli.SchemaObject(
			map[string]interface{}{
				"step":   strictcli.SchemaType("string"),
				"detail": strictcli.SchemaType("string"),
			},
			[]string{"step", "detail"},
			false,
		)),
		"dry_run": strictcli.SchemaType("boolean"),
	},
	[]string{"operation", "outcome", "ref", "sha", "parents", "tree", "files", "state_cleared", "attempts", "declined_checks", "residue", "dry_run"},
	false,
)

// mergeHelp is the command's registered help text.
const mergeHelp = "merge one branch into the current one, and author the result: safegit decides the fast-forward itself and moves the ref under compare-and-swap, or runs git's merge machinery with --no-ff --no-commit and commits the staged result through its own pipeline -- so a merge safegit performed carries safegit's trailers, ran the repository's commit-msg hook and is reversible with 'safegit undo'. A merge git stops on a conflict parks, and 'safegit merge-continue' concludes it; 'safegit merge --continue' is refused and names that command. The command line is a deliberate subset of git's: exactly one branch (no octopus), no strategy selection, no --squash, no --edit and no --autostash. --no-commit computes the merge and leaves it parked even when it is clean. On an UNBORN branch -- one with no commits yet -- a merge can only be a fast-forward, so --no-ff and --no-commit are both refused there before git runs, with the reason"
