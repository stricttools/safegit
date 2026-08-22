package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
)

// Concluding a QUEUED cherry-pick or revert: safegit checks, git commits.
//
// A queue is `git cherry-pick <a> <b>` or `git revert <a> <b>` -- git's
// sequencer holds a list of commands and stops on the first one that conflicts.
// Concluding one step of it natively is not an option: safegit's conclusion
// removes the operation's whole state-file set, and for a queue that set
// includes the queue, so the remaining commands would be thrown away by the act
// of concluding the current one.
//
// So the current step is handed back to git, with one substitution: safegit
// stages the operator's declared resolutions into a COPY of the shared index
// and points git at the copy through GIT_INDEX_FILE. git commits what the copy
// holds, runs the rest of the queue and cleans up its own state, and safegit's
// promise never to stage into .git/index is kept. What git leaves in that copy
// afterwards -- HEAD's tree when the queue finished, the NEXT conflict's stages
// when it stopped again -- is then adopted as the shared index, because the
// file git was handed IS the index for the operation it just performed.
//
// What the operator loses by this, and what the report therefore says out loud:
// the commits are git's, so they carry none of safegit's trailers, safegit's
// own commit-msg handling never runs, and `safegit undo` will not reverse them.

// delegateQueuedSequence concludes a queued cherry-pick or revert through git's
// own `--continue`.
//
// It is reached with the same checks the native path makes already done: the
// declared resolutions name exactly the conflicted paths, and the content they
// name carries no surviving conflict region.
func delegateQueuedSequence(
	flags globalFlags,
	op continueOp,
	gitDir, sgDir string,
	state sequencer.State,
	sides map[string]conflict.Sides,
	declared []resolution,
	edits []commit.IndexEdit,
	messages, trailers []string,
) int {
	ctx := flags.ctx()
	verb := strings.TrimSuffix(op.command, "-continue")

	// git writes these commits, so a message or a trailer safegit was handed
	// has nowhere to go. Dropping them silently would be the worst of the three
	// available behaviors -- the operator would believe a message was used.
	if len(messages) > 0 || len(trailers) > 0 {
		fmt.Fprintf(os.Stderr, "error: -m and --trailer cannot be honored when concluding %s\n", state.String())
		fmt.Fprintf(os.Stderr, "  git authors the commits of a queued sequence itself, from its own message drafts;\n")
		fmt.Fprintf(os.Stderr, "  re-run without them, or conclude the queue commit by commit with 'git %s --continue'\n", verb)
		return exitcode.Usage
	}

	// A preview cannot be honest here. Concluding a queue means running git's
	// own `--continue`, which commits, advances the queue and may commit
	// several more times; there is no computation that stands in for it, and
	// performing it is exactly what --dry-run promises not to do. This is the
	// per-INVOCATION form of the framework's own per-command dry-run refusal
	// (strictcli's WithDryRunUnsupported), which is why it exits the same code
	// that refusal does; when the framework grows a per-invocation form, this
	// moves onto it unchanged.
	if flags.dryRun {
		fmt.Fprintf(os.Stderr, "error: --dry-run is not supported for %s\n", state.String())
		fmt.Fprintf(os.Stderr, "  concluding a queued sequence hands the rest of the queue to git's own '%s --continue',\n", verb)
		fmt.Fprintf(os.Stderr, "  which safegit cannot run in a preview -- there is nothing to show short of performing it.\n")
		fmt.Fprintf(os.Stderr, "  A SINGLE %s (no queue) previews normally.\n", verb)
		return exitcode.General
	}

	tmp, err := index.NewFromFile(sgDir, filepath.Join(gitDir, "index"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: copying the shared index for the delegated conclusion: %v\n", err)
		return exitcode.General
	}
	defer func() { _ = tmp.Cleanup() }()

	if err := commit.ApplyIndexEditsTo(ctx, tmp.IndexPath, edits); err != nil {
		fmt.Fprintf(os.Stderr, "error: staging the declared resolutions into the index copy: %v\n", err)
		return exitcode.General
	}

	// The working tree goes FIRST here, unlike a native conclusion, where it is
	// the last step after a real commit. It is not a choice: git's `--continue`
	// checks the working tree against the index it was given before it applies
	// the queue's remaining commands, and a file still carrying conflict markers
	// while the index holds the resolved blob is "local changes would be
	// overwritten". Nothing is lost when the delegation then fails -- the files
	// hold exactly what the operator declared, which is what `git checkout
	// --ours` would have put there anyway.
	if err := materializeResolutions(ctx, sides, declared); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	before, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the branch tip before the delegated conclusion: %v\n", err)
		return exitcode.General
	}

	// --no-edit rather than an editor override: git's sequencer would otherwise
	// open an editor for the commit being concluded, and safegit's conclusion
	// surface has no editor anywhere -- the message is git's own draft, exactly
	// as it is on the native path.
	//
	// git's own progress goes to stderr in machine mode, so the envelope stays
	// the only document on stdout.
	runErr := git.RunPassthroughTo(ctx,
		[]string{"GIT_INDEX_FILE=" + tmp.IndexPath},
		passthroughStdout(flags),
		verb, "--continue", "--no-edit")

	// Whatever happened, the index git was given is now the truth about this
	// repository, and the shared index is stale (git never wrote it). Adopting
	// it is mandatory in BOTH outcomes and is done before anything is reported.
	adoptErr := git.AdoptIndexFrom(ctx, tmp.IndexPath)

	after, revErr := git.RevParse(ctx, "HEAD")
	if revErr != nil {
		fmt.Fprintf(os.Stderr, "error: reading the branch tip after the delegated conclusion: %v\n", revErr)
		return exitcode.General
	}
	created := countCommitsBetween(flags, before, after)

	if before != after {
		_ = oplog.Append(repo.SafegitDir(gitDir), oplog.Entry{
			// Deliberately NOT op.command: the undo registry keys on that name,
			// and these commits are git's. An op name outside the registry is
			// skipped by undo, which is the honest behavior for a commit safegit
			// did not create and cannot reconstruct.
			Op: op.command + "-delegated",
			Extra: map[string]interface{}{
				"operation": op.kind.String(),
				"from":      before,
				"to":        after,
				"commits":   created,
			},
		})
	}

	if adoptErr != nil {
		fmt.Fprintf(os.Stderr, "error: %s ran, but putting the shared index in step with it failed: %v\n", verb, adoptErr)
		return exitcode.General
	}

	// Read BEFORE the two outcomes part company: what git left in flight is a
	// fact about the repository either way, and the stopped-again path reports
	// it exactly as the finished one does.
	remaining, err := sequencer.Read(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: re-reading git's in-flight operation state after the conclusion: %v\n", err)
		return exitcode.General
	}

	if runErr != nil {
		// git said what went wrong on its own stderr, and it said nothing about
		// the commits it MADE first: a queue that stops on its next conflict
		// has still concluded the step it was given. Reporting them is not
		// optional -- they are on the branch, they are git's, and a caller that
		// has to re-read the ref to discover them was told less than safegit
		// knows. The way out of the state git left comes after, from the single
		// way-out authority.
		reportDelegated(flags, op, delegatedOutcome{
			head:         after,
			created:      created,
			declared:     declared,
			stateCleared: !remaining.InProgress(),
			stoppedAgain: true,
		})
		announceWayOut(flags, gitDir)
		return passthroughExitCode(runErr)
	}

	// git removes its own state when a queue finishes, but it leaves
	// .git/AUTO_MERGE behind -- the same leftover a concluded rebase leaves,
	// recorded in internal/git's delegation probes. A lone AUTO_MERGE is
	// residue of a FINISHED operation and nothing reads it as evidence of one
	// in flight, but a conclusion promises to leave no state behind whoever
	// wrote the commits, so the same cleanup the native path runs is run here.
	// It is guarded on nothing being in flight: if git stopped again, that
	// AUTO_MERGE belongs to the conflict it stopped on, and the removal set for
	// a queue includes the queue itself.
	if !remaining.InProgress() {
		if err := sequencer.Cleanup(gitDir, state.Kind); err != nil {
			fmt.Fprintf(os.Stderr, "error: the %s completed, but removing the leftover state failed: %v\n", verb, err)
			return exitcode.General
		}
	}

	if err := maybeAutoBumpParent(ctx, flags, gitDir, after, op.command, "concluded "+state.String()); err != nil {
		die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
	}

	reportDelegated(flags, op, delegatedOutcome{
		head:         after,
		created:      created,
		declared:     declared,
		stateCleared: !remaining.InProgress(),
	})
	return exitcode.OK
}

// delegatedOutcome is what a completed delegation reports.
type delegatedOutcome struct {
	// head is the branch tip after git finished, and the only commit name a
	// delegation can honestly report: git may have made several.
	head string
	// created is how many commits the branch gained.
	created int
	// declared is the resolution set safegit staged into the index copy.
	declared []resolution
	// stateCleared is READ from the git directory afterwards, never assumed:
	// git removes its own state when the queue finishes and leaves it when the
	// queue stops again.
	stateCleared bool
	// stoppedAgain reports that git's own `--continue` ended NONZERO because
	// the queue stopped on a further conflict. It is what makes the report
	// readable alongside a nonzero exit: head and created then describe the
	// commits git made BEFORE it stopped.
	stoppedAgain bool
}

// countCommitsBetween reports how many commits the branch gained. A count that
// cannot be taken is reported as -1 rather than as a plausible number, and the
// renderer says so.
func countCommitsBetween(flags globalFlags, before, after string) int {
	if before == after {
		return 0
	}
	out, _, err := git.Run(flags.ctx(), "rev-list", "--count", before+".."+after)
	if err != nil {
		return -1
	}
	// strconv rather than Sscanf, which accepts trailing garbage ("5abc" would
	// parse as 5) and would turn an unexpected output shape into a plausible
	// number instead of the "could not be counted" the renderer states.
	n, cerr := strconv.Atoi(strings.TrimSpace(out))
	if cerr != nil {
		return -1
	}
	return n
}
