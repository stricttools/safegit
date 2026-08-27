package main

import (
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/strictcli/go/strictcli"
)

// `safegit pull`: fetch, then the pipeline-authored merge.
//
// pull was already composed of the two steps rather than handed to `git pull`,
// which is what makes --merge-strategy a required declaration with no default:
// a pull here never consults git's own configuration to decide whether it may
// create a merge commit.
//
// What changed is the SECOND step. It used to be a plain `git merge`, so a pull
// that could not fast-forward ended in a commit GIT authored -- none of
// safegit's trailers, no commit-msg handling, nothing `safegit undo` could
// reverse, and git's AUTO_MERGE left in the git directory. It is now the same
// merge `safegit merge` performs (performMerge in merge_cmd.go): safegit
// decides the fast-forward itself and moves the ref under compare-and-swap, or
// computes with `--no-ff --no-commit` and commits the staged result through its
// own pipeline.
//
// The three strategy values are that merge's three questions, answered in
// advance: ff fast-forwards where it can and makes a merge commit where it
// cannot, ff-only refuses anything but a fast-forward, no-ff always makes a
// merge commit.

// pullMode is the caller's --merge-strategy, resolved.
type pullMode int

const (
	pullFFOnly pullMode = iota // ff-only: refuse anything but a fast-forward
	pullFF                     // ff: fast-forward when possible, merge commit otherwise
	pullNoFF                   // no-ff: always create a merge commit
)

// runPull fetches and then merges what was fetched.
func runPull(flags globalFlags, mode pullMode, remote, branch string, rebase bool) int {
	// FIRST, and before the repository is touched at all.
	if rebase {
		return refusePullRebase()
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	// A pull concluded in a submodule moves the parent's gitlink exactly as an
	// ordinary commit does, so the parent must have answered the auto-bump
	// question before anything is written.
	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		return exitcode.General
	}

	release, code := acquireOperationLock(flags, gitDir, "pull")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "pull"); code != 0 {
		return code
	}

	// Before the FETCH, not merely before the merge step: a pull that cannot
	// merge has no business going to the network first.
	//
	// A pull reaches the merge compute through this handler rather than through
	// `safegit merge`'s, so merge's own entry check does not cover it, and the
	// exposure is the same one. `git merge --no-ff --no-commit` over a parked
	// REVERT is not refused by git -- it reports "Automatic merge went well" and
	// exits 0 (probed; over a parked cherry-pick git does refuse) -- so without
	// this a pull computed the merge, committed it through the pipeline, and left
	// REVERT_HEAD orphaned for the next `safegit commit` to refuse over.
	if code := refuseComputeOverInFlight(gitDir, "pull"); code != 0 {
		return code
	}

	pos := readOplogPosition(flags)

	// Before the FETCH too, and for the same reason: `--merge-strategy no-ff`
	// asks for a merge commit, an unborn branch cannot carry one, and the field
	// it sets is exactly the one that skips the unborn fast-forward arm -- so
	// without this a pull would go to the network and then die on git's raw
	// fatal. performMerge asks the same question for every other route into a
	// merge; here it is asked early enough to cost nothing.
	//
	// It is asked before the dry-run branch as well: a preview of a command that
	// cannot run is not a preview of anything, which is the rule merge's own
	// preview follows. appendOperationEntry writes nothing in a dry run.
	if code := refuseUnbornMergeForm(flags.ctx(), mode == pullNoFF, false, "--merge-strategy no-ff", ""); code != 0 {
		appendOperationEntry(flags, sgDir, "pull", pos, false, pullExtraBase(remote, branch))
		return code
	}

	fetchArgs := []string{"fetch", remote}
	if branch != "" {
		fetchArgs = append(fetchArgs, branch)
	}

	if flags.dryRun {
		// The fetch is recorded rather than performed -- and that is precisely
		// why nothing about the merge can be said. What the second step would do
		// depends entirely on the commits the first step did not bring in: which
		// of a merge's outcomes applies, whether it conflicts, and where. A
		// preview that computed against a STALE FETCH_HEAD would answer a
		// question about a fetch that already happened, days ago, rather than
		// about this command.
		if code := runGitMutation(flags, gitexec.NoDoor, fetchArgs...); code != 0 {
			return code
		}
		infof(flags, "the merge that follows cannot be previewed: what it does depends on the commits the fetch\n")
		infof(flags, "would bring in, and a preview performs no fetch. Run 'safegit merge' after fetching to see it.\n")
		return exitcode.OK
	}

	if code := runGitMutation(flags, gitexec.NoDoor, fetchArgs...); code != 0 {
		appendOperationEntry(flags, sgDir, "pull", pos, false, pullExtraBase(remote, branch))
		return code
	}

	// What the fetch brought in, read once and reported: FETCH_HEAD is the only
	// name the incoming tip has, and it is overwritten by the next fetch.
	fetchHead, _ := git.RevParse(flags.ctx(), "FETCH_HEAD^{commit}")

	payload, ok, exit := performMerge(flags, gitDir, sgDir, pos, mergeRequest{
		other: "FETCH_HEAD",
		// The whole argv: no operator options reach a pull's merge step, because
		// pull declares flags rather than forwarding git's vocabulary.
		computeArgs:  []string{"FETCH_HEAD"},
		noFF:         mode == pullNoFF,
		ffOnly:       mode == pullFFOnly,
		op:           "pull",
		extraBase:    pullExtraBase(remote, branch),
		ffOnlyFlag:   "--merge-strategy ff-only",
		ffOnlyWayOut: "  Re-run with --merge-strategy ff or no-ff to make one, or rebase this branch onto the remote instead.\n",
		noFFFlag:     "--merge-strategy no-ff",
		// A pull has no parking form: --merge-strategy names three merge
		// selections and none of them leaves the result in flight.
		parkFlag: "",
		headline: func(conclusionResult) string {
			return "pulled " + pullSubject(remote, branch)
		},
	})
	if ok {
		flags.payload(pullPayload{
			Operation: "pull",
			Remote:    remote,
			Branch:    strPtr(branch),
			FetchHead: strPtr(fetchHead),
			Merge:     payload,
			DryRun:    flags.dryRun,
		})
	}
	return exit
}

// refusePullRebase refuses `--rebase`, naming the two commands that do it.
//
// It is declared as a flag rather than left to the parser's unknown-flag
// refusal so that the answer says what to do instead: an operator reaching for
// `--rebase` wants a specific thing, and "unknown flag" does not tell them
// where it lives.
//
// DIVERGENCE: `git pull --rebase` is one command; safegit's is two, because a
// rebase is its own command with its own door -- git replays and authors the
// replayed commits there, which is the one place safegit lets it. Cataloged in
// docs/divergences.md as "`pull --rebase` is refused, naming the two commands".
func refusePullRebase() int {
	fmt.Fprintf(os.Stderr, "error: safegit pull does not support --rebase\n")
	fmt.Fprintf(os.Stderr, "  a pull's merge step is safegit's own -- it authors the commit -- while a rebase is git's\n")
	fmt.Fprintf(os.Stderr, "  replay from end to end, which is a different operation with a different door. Do the two:\n")
	fmt.Fprintf(os.Stderr, "    git fetch <remote>\n")
	fmt.Fprintf(os.Stderr, "    safegit rebase <remote>/<branch>\n")
	fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
	return exitcode.Usage
}

// pullExtraBase is the per-pull half of an oplog entry: where the commits came
// from. The branch is recorded as the empty string when none was named, which
// is what the operator said.
func pullExtraBase(remote, branch string) map[string]interface{} {
	return map[string]interface{}{"remote": remote, "branch": branch}
}

// pullSubject names what was pulled, for the human summary line.
func pullSubject(remote, branch string) string {
	if branch == "" {
		return remote
	}
	return remote + "/" + branch
}

// pullPayload is what `safegit pull` puts in the envelope's payload: what was
// fetched, and what the merge of it did.
//
// The merge half is the merge payload itself rather than a flattened copy of
// its members, because it is the same document `safegit merge` emits and a
// consumer that can read one can read the other. That is also where a pull's
// AFTERCARE is reported: every step a pull owes after its commit is the merge
// conclusion's, so `merge.residue` is the whole answer and a second copy of it
// out here would be two members claiming one fact.
type pullPayload struct {
	Operation string `json:"operation"`
	Remote    string `json:"remote"`
	// Branch is the remote branch that was named, and null when none was: the
	// fetch then follows the remote's configured refspec.
	Branch *string `json:"branch"`
	// FetchHead is the incoming tip the merge was performed against, and null
	// when the fetch brought in nothing nameable.
	FetchHead *string      `json:"fetch_head"`
	Merge     mergePayload `json:"merge"`
	DryRun    bool         `json:"dry_run"`
}

// pullPayloadSchema is pull's machine payload contract.
var pullPayloadSchema = strictcli.SchemaObject(
	map[string]interface{}{
		"operation":  strictcli.SchemaType("string"),
		"remote":     strictcli.SchemaType("string"),
		"branch":     strictcli.SchemaType("string", "null"),
		"fetch_head": strictcli.SchemaType("string", "null"),
		"merge":      mergePayloadSchema,
		"dry_run":    strictcli.SchemaType("boolean"),
	},
	[]string{"operation", "remote", "branch", "fetch_head", "merge", "dry_run"},
	false,
)

// pullHelp is the command's registered help text.
const pullHelp = "fetch from a remote and merge what was fetched, with the merge strategy stated explicitly: --merge-strategy is required and has no default, so a pull never depends on git's own configuration to decide whether it may create a merge commit. The merge step is safegit's own -- the same one 'safegit merge' performs -- so a pull that cannot fast-forward produces a commit carrying safegit's trailers, run through the repository's commit-msg hook and reversible with 'safegit undo'; a pull that can fast-forward moves the ref under compare-and-swap and puts the index and the working tree in step with it. A merge git stops on a conflict parks, and 'safegit merge-continue' concludes it. --rebase is refused and names the two commands that do it"
