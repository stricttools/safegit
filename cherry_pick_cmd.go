package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/strictcli/go/strictcli"
)

// `safegit cherry-pick <commit>`, restructured.
//
// As a plain passthrough, GIT authored whatever commits came out of the
// operator's command line: they carried none of safegit's trailers, safegit's
// commit-time machinery never ran, `safegit undo` could not reverse them, and
// git's own operation state was left in the git directory for the next
// `safegit commit` to refuse over.
//
// The restructure splits the operation where git itself splits it, exactly as
// the restructured merge and revert do: `git cherry-pick --no-commit` COMPUTES
// the pick and stages its result, and the conclusion engine (the one behind
// `safegit cherry-pick-continue`) turns that staged result into a commit --
// pipeline-authored, trailers injected, commit-msg hook run, ref moved under
// compare-and-swap, state files removed. The AUTHOR is preserved from the
// commit being applied and the committer is whoever ran the command, which is
// git's own division and not a safegit policy.
//
// THE PARK FILE is what this restructure needed and the revert's did not.
// `git cherry-pick --no-commit` records NOTHING about the commit it is
// applying: no CHERRY_PICK_HEAD, on the clean path or the conflicted one (git
// writes that file only when git is going to make the commit itself). Every
// piece of machinery around a parked pick keys on it -- the conclusion's own
// state read, the preserved identity, the per-path conflict listing, `git
// status`, `git cherry-pick --abort`. So safegit writes it after the compute
// step, uniformly on both paths, through sequencer.MarkCherryPick. The parked
// state is then exactly what a conflicted pick looks like to git, and every
// existing path works on it unchanged.
//
// (`git revert --no-commit` DOES write REVERT_HEAD on both paths --
// probe-verified -- which is why revert_cmd.go never needed this. The park-file
// write is the pick's own addition, not a shared step.)
//
// The command line is narrower than git's, under the subset law -- see
// cherryPickRefusedOptions and refuseUnsupportedCherryPick.

// cherryPickRefusedOptions are the `git cherry-pick` options safegit's
// cherry-pick does not implement, each with the reason it is absent.
//
// It is a refusal list rather than an allowlist for the reason merge's is: an
// option safegit has not considered still reaches the compute step, where it
// can change how the pick is COMPUTED but never who authors the commit -- the
// compute step is pinned to `--no-commit`, so git cannot commit whatever else
// is on the command line. The two entries that would break that promise, `--ff`
// and `--commit`, are refused here for exactly that reason.
//
// DIVERGENCE: every entry here is one git capability safegit deliberately does
// not have, and each needs its row in docs/divergences.md.
var cherryPickRefusedOptions = []struct {
	names []string
	why   string
}{
	{
		[]string{"--skip"},
		"--skip moves past one commit of a SEQUENCE and keeps the rest going, and safegit's cherry-pick has no sequence to keep going: it picks one commit. Abandon the pick with 'git cherry-pick --abort' and pick the commits you do want, one invocation each",
	},
	{
		[]string{"-e", "--edit"},
		"--edit opens an editor, and safegit's commit surface has none; conclude with 'safegit cherry-pick-continue -m' to give the commit a message of your own",
	},
	{
		[]string{"-S", "--gpg-sign"},
		"safegit's commit pipeline does not sign commits, so a signature asked for here would simply not be on the result; nothing is silently dropped, so the request is refused instead",
	},
	{
		[]string{"--ff"},
		"--ff lets git move the branch onto the picked commit outright, which is a ref move outside safegit's compare-and-swap and a commit safegit did not author. safegit's cherry-pick always makes a commit of its own",
	},
	{
		[]string{"--commit"},
		"--commit is the opposite of the --no-commit the compute step is pinned to, and passing it would hand the commit back to git -- authored by git, with none of safegit's trailers, and moving the ref outside safegit's compare-and-swap",
	},
	{
		[]string{"--cleanup"},
		"--cleanup governs how git strips a message at ITS commit time, and safegit's pipeline is what commits here; the message it takes is git's draft with its comment block stripped, or the text you pass to 'safegit cherry-pick-continue -m'",
	},
	{
		[]string{"--allow-empty", "--allow-empty-message", "--keep-redundant-commits", "--empty"},
		"these govern what git commits when a pick produces nothing, and safegit's pipeline refuses a commit that changes nothing outright; there is no flag here that turns that refusal off",
	},
}

// runCherryPick dispatches `safegit cherry-pick`.
//
// Two routes stay guarded passthroughs, and both for the same reason: they
// author nothing, so nothing about single authorship is at stake in them. The
// state-control verbs act on an operation git already has in flight, and
// `--no-commit` asks git to stage the pick and stop -- which is what it did
// before the restructure existed. `--continue` is refused inside, by name,
// because concluding a cherry-pick is safegit's own job.
func runCherryPick(flags globalFlags, args []string) int {
	parsed := parseGitArgs("cherry-pick", args)
	if parsed.Has("--continue", "--abort", "--quit") {
		return runGuardedPassthrough(flags, "cherry-pick", args)
	}
	if parsed.Has("-n", "--no-commit") {
		return runGuardedPassthrough(flags, "cherry-pick", args)
	}
	return runRestructuredCherryPick(flags, args, parsed)
}

// refuseUnsupportedCherryPick refuses the command lines safegit's cherry-pick
// does not implement, before any lock is taken and before any git runs.
//
// Every refusal is parser-shaped (exit 2) and names the capability that is
// absent, because "safegit does not do this" is a different answer from "this
// failed" and an operator has to be able to tell them apart. Each needs its
// entry in the divergences catalog.
func refuseUnsupportedCherryPick(parsed gitArgs) int {
	for _, refused := range cherryPickRefusedOptions {
		if o, ok := parsed.Find(refused.names...); ok {
			fmt.Fprintf(os.Stderr, "error: safegit cherry-pick does not support %s\n", o.Name)
			fmt.Fprintf(os.Stderr, "  %s.\n", refused.why)
			fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
			return exitcode.Usage
		}
	}

	if len(parsed.AfterDoubleDash) > 0 {
		fmt.Fprintf(os.Stderr, "error: safegit cherry-pick takes no pathspec\n")
		fmt.Fprintf(os.Stderr, "  a pathspec applies part of a commit and records a message claiming the whole of it.\n")
		fmt.Fprintf(os.Stderr, "  Pick the commit, or make the partial change yourself and commit it.\n")
		return exitcode.Usage
	}

	// BEFORE the count, because a rev-set is one argv token and counting tokens
	// would call `main..side` a single commit.
	for _, rev := range parsed.Revisions {
		if code := refuseRevisionSet("cherry-pick", rev); code != 0 {
			return code
		}
	}

	switch len(parsed.Revisions) {
	case 1:
	case 0:
		fmt.Fprintf(os.Stderr, "error: safegit cherry-pick names no commit to pick\n")
		fmt.Fprintf(os.Stderr, "  usage: safegit cherry-pick <commit>\n")
		return exitcode.Usage
	default:
		fmt.Fprintf(os.Stderr, "error: safegit cherry-pick takes exactly one commit, and this names %d\n", len(parsed.Revisions))
		fmt.Fprintf(os.Stderr, "  %s\n", sequentialFormReason("cherry-pick"))
		return exitcode.Usage
	}
	return 0
}

// refuseRevisionSet refuses a revision argument that is a RANGE or another
// rev-set spelling rather than the name of one commit.
//
// It is a refusal on the OPERATORS rather than on how many arguments were
// typed, and that is the whole point: `git cherry-pick A..B` hands the
// operation to git's sequencer -- queue directory and all -- even where the
// range holds a single commit (probed). A check that counted argv tokens would
// let exactly that command line through as "one commit".
//
// DIVERGENCE: git's cherry-pick and revert take the full revision-set grammar;
// safegit's take the name of one commit. Needs its row in docs/divergences.md.
func refuseRevisionSet(verb, rev string) int {
	var operator string
	switch {
	case strings.Contains(rev, "..."):
		operator = "..."
	case strings.Contains(rev, ".."):
		operator = ".."
	case strings.HasPrefix(rev, "^"):
		operator = "a leading ^"
	case strings.HasSuffix(rev, "^!"):
		operator = "^!"
	case strings.HasSuffix(rev, "^@"):
		operator = "^@"
	default:
		return 0
	}

	fmt.Fprintf(os.Stderr, "error: safegit %s does not take a commit range or a revision set (%q uses %s)\n", verb, rev, operator)
	fmt.Fprintf(os.Stderr, "  a range puts git's own sequencer in charge -- it queues the commits and authors every\n")
	fmt.Fprintf(os.Stderr, "  commit it makes -- and it does so even where the range holds a single commit.\n")
	fmt.Fprintf(os.Stderr, "  %s\n", sequentialFormReason(verb))
	return exitcode.Usage
}

// sequentialFormReason is the one sentence that says what to do instead of
// naming several commits, spelled once so cherry-pick and revert say the same
// thing.
//
// DIVERGENCE: git applies any number of commits in one invocation; safegit
// applies one. Needs its row in docs/divergences.md.
func sequentialFormReason(verb string) string {
	return fmt.Sprintf("safegit %s applies ONE commit and authors the result itself; run it once per commit, in the order you want them applied. See docs/divergences.md.", verb)
}

// runRestructuredCherryPick is the whole flow.
func runRestructuredCherryPick(flags globalFlags, args []string, parsed gitArgs) int {
	// FIRST, and before the repository is touched at all: a command line
	// safegit itself refuses is refused without a lock, without an
	// auto-initialization and without a git call.
	if code := refuseUnsupportedCherryPick(parsed); code != 0 {
		return code
	}

	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}
	sgDir := repo.SafegitDir(gitDir)

	// A pick concluded in a submodule moves the parent's gitlink exactly as an
	// ordinary commit does, so the parent must have answered the auto-bump
	// question before anything is written.
	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		return exitcode.General
	}

	release, code := acquireOperationLock(flags, gitDir, "cherry-pick")
	if code != 0 {
		return code
	}
	defer release()

	if code := coordGuard(flags, gitDir, "cherry-pick"); code != 0 {
		return code
	}

	if flags.dryRun {
		// No git cherry-pick runs: the invocation is recorded, and the outcome
		// it would have is COMPUTED with git's own merge engine instead of
		// guessed.
		return previewSequencerOperation(flags, "cherry-pick", args, append([]string{"cherry-pick"}, args...))
	}

	ctx := flags.ctx()
	pos := readOplogPosition(flags)
	picked := parsed.Revisions[0]

	// The commit being applied, resolved BEFORE the compute step because the
	// park file has to name it.
	//
	// An unresolvable revision is deliberately left to git: `safegit cherry-pick
	// no-such-commit` must exit with git's own verdict on that argument, not
	// with a message safegit invented, so the failure is carried rather than
	// reported and the compute step below produces git's error.
	sourceSHA, resolveErr := git.RevParse(ctx, picked+"^{commit}")

	// The compute step. --no-commit is what makes the two halves separable: git
	// works out the pick and stages it, and stops before the commit that would
	// otherwise be git's.
	code = runGitMutation(flags, append([]string{"cherry-pick", "--no-commit"}, args...)...)
	if code != 0 {
		// Two very different situations arrive here, and only one of them
		// parked anything: a CONFLICT, which is the ordinary way a pick stops
		// and which `safegit cherry-pick-continue` concludes, and a git refusal
		// that staged nothing at all (an unreadable revision, a repository git
		// would not touch). Unmerged entries in the index are what tells them
		// apart, and writing the park file over the second would leave a
		// repository mid-pick with nothing to conclude.
		if !pickLeftAConflict(ctx) {
			return code
		}
		if err := parkComputedPick(gitDir, sourceSHA, resolveErr); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitcode.General
		}
		appendOperationEntry(flags, sgDir, "cherry-pick", pos, false, pickExtra(picked))
		announceWayOut(flags, gitDir)
		return code
	}

	if err := parkComputedPick(gitDir, sourceSHA, resolveErr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	state, err := sequencer.Read(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the cherry-pick state: %v\n", err)
		return exitcode.General
	}
	if state.Kind != sequencer.KindCherryPick {
		fmt.Fprintf(os.Stderr, "error: the computed cherry-pick is not in flight (%s); nothing was committed\n", state.String())
		return exitcode.General
	}

	// Clean, and nothing asked for it to be left in flight: conclude it now,
	// through the same engine `safegit cherry-pick-continue` runs. The message
	// is git's own MERGE_MSG draft, which already carries whatever the compute
	// step put there (an `-x` reference line, a signoff trailer), so nothing is
	// re-derived here.
	out, exit, ok := concludeParkedOperation(flags, gitDir, sgDir, state, parkedConclusion{
		op:           cherryPickContinueOp,
		oplogOp:      "cherry-pick",
		parentBumpOp: "cherry-pick",
		onEmpty:      refuseEmptyCherryPick,
	})
	if !ok {
		return exit
	}

	flags.payload(cherryPickPayload{
		Operation:      "cherry-pick",
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
	cherryPickContinueOp.renderHuman(flags, out, "picked "+short(picked, state.Source))
	return exit
}

// parkComputedPick writes the park file for a pick git has just computed.
//
// The resolution error is carried in rather than checked at the call site
// because the two are one decision: the file must name the commit being
// applied, so a pick git applied whose revision safegit could not read is a
// state safegit cannot park -- and saying so is better than writing a file that
// names nothing.
func parkComputedPick(gitDir, sourceSHA string, resolveErr error) error {
	if resolveErr != nil {
		return fmt.Errorf("git applied the cherry-pick, but the commit it applied could not be resolved: %w", resolveErr)
	}
	if err := sequencer.MarkCherryPick(gitDir, sourceSHA); err != nil {
		return fmt.Errorf("recording which commit the cherry-pick is applying: %w", err)
	}
	return nil
}

// pickLeftAConflict reports whether the compute step stopped on a conflict, as
// opposed to failing before it applied anything. The index is the only witness:
// git's exit code is 1 for both a conflict and several refusals.
func pickLeftAConflict(ctx context.Context) bool {
	sides, err := conflict.Stages(ctx, "")
	return err == nil && len(sides) > 0
}

// pickExtra is the per-pick half of an oplog entry: which commit was named. The
// outcome field is appendOperationEntry's own.
func pickExtra(picked string) map[string]interface{} {
	return map[string]interface{}{"commit": picked}
}

// refuseEmptyCherryPick covers a pick whose change the branch already carries.
// git refuses the same case, and the state it left is still in flight, so the
// message names the ways out that actually exist.
func refuseEmptyCherryPick() int {
	fmt.Fprintf(os.Stderr, "error: this cherry-pick produces no change: the branch already carries the commit's effect\n")
	fmt.Fprintf(os.Stderr, "  the pick is still in progress; drop it with:\n")
	fmt.Fprintf(os.Stderr, "    git cherry-pick --abort\n")
	return exitcode.General
}

// reportedAuthor renders the identity a payload states, empty where the
// operation recorded none.
func reportedAuthor(info *git.AuthorInfo) continueAuthor {
	if info == nil {
		return continueAuthor{}
	}
	return continueAuthor{Name: info.Name, Email: info.Email}
}

// cherryPickPayload is what `safegit cherry-pick` puts in the envelope's
// payload.
//
// It follows the conclusion payload -- the members cherry-pick-continue reports
// -- minus the resolution members, which a pick safegit itself started never
// has (it concludes a clean result; a conflicted one is not concluded here at
// all), and minus the queue members, which a single-form command cannot
// produce. What it adds is `source`: the commit that was picked is the one fact
// about this operation the other members cannot express, because a pick's
// parents name the branch it went onto and not the change it brought.
type cherryPickPayload struct {
	Operation string `json:"operation"`
	// Source is the commit that was picked, as it was resolved rather than as
	// it was typed.
	Source string `json:"source"`
	Ref    string `json:"ref"`
	// SHA is the commit that was created, and null under --dry-run.
	SHA     *string  `json:"sha"`
	Parents []string `json:"parents"`
	Tree    string   `json:"tree"`
	Files   []string `json:"files"`
	// Author is the identity the commit RECORDS, preserved from the picked
	// commit; the committer is whoever ran the command.
	Author         continueAuthor  `json:"author"`
	StateCleared   bool            `json:"state_cleared"`
	Attempts       int             `json:"attempts"`
	DeclinedChecks []declinedCheck `json:"declined_checks"`
	DryRun         bool            `json:"dry_run"`
}

// cherryPickPayloadSchema is cherry-pick's machine payload contract.
var cherryPickPayloadSchema = strictcli.SchemaObject(
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

// cherryPickHelp is the command's registered help text.
const cherryPickHelp = "apply ONE commit onto the current branch, and author the result: git computes the pick with --no-commit and safegit commits the staged result through its own pipeline -- so a pick safegit performed carries safegit's trailers, ran the repository's commit-msg hook and is reversible with 'safegit undo'. The AUTHOR is preserved from the commit being applied and the committer is you, which is git's own division. A pick git stops on a conflict parks -- safegit writes CHERRY_PICK_HEAD itself, because 'git cherry-pick --no-commit' does not -- and 'safegit cherry-pick-continue' concludes it; 'safegit cherry-pick --continue' is refused and names that command. The command line is a deliberate subset of git's: exactly one commit named as a commit (a range or any other revision set is refused, because a range hands the operation to git's sequencer even when it holds one commit), no --edit, no --ff, no --commit, no --cleanup, no signing and no empty-commit flags. --abort, --quit and --no-commit stay plain passthroughs, because they author nothing"
