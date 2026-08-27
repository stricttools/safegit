package main

import (
	"context"
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/gitversion"
)

// Honest previews for merge, cherry-pick and revert.
//
// `--dry-run` on these three used to record the invocation in the would-do log
// and say nothing else, which answered the only question an operator asks --
// "will this conflict, and where?" -- with silence. The answer is computable
// exactly: `git merge-tree --write-tree` runs git's own merge engine into the
// object store, touching neither the index nor the working tree, and reports
// the resulting tree plus every path it left unmerged.
//
// All three operations are the same computation with different arguments,
// which is the reason one function serves them: a merge of X takes git's own
// merge base, a cherry-pick of C replays C over HEAD with C's parent as the
// base, and a revert of C replays C's PARENT over HEAD with C as the base --
// the inverse patch, expressed as a merge. Both forms are probe-verified
// against the real operation's index stages (see the tests).
//
// Two properties hold throughout:
//
//   - The computation runs under the preview object quarantine, because
//     merge-tree writes real objects. The execution boundary refuses an
//     object-writing invocation on a previewing context that carries no
//     quarantine, so forgetting it is loud rather than silent.
//   - A command line whose tree computation the preview cannot REPRODUCE is
//     refused rather than previewed. See previewRefusal.

// previewSequencerOperation is the --dry-run path of merge, cherry-pick and
// revert. It returns the exit code the command should return.
//
// recordedArgv is the argv the EXECUTE path would hand git, which the framework
// records in the would-do log. It is a parameter rather than something derived
// here because it differs per caller: the restructured revert computes with
// `git revert --no-commit ...`, and a preview whose log said `git revert ...`
// would record a mutation nothing performs.
//
// It is recorded CONDITIONALLY, and the condition belongs to the computation
// below rather than to any caller: a merge that turns out to be a fast-forward
// reaches no compute step in a real run either, so it records nothing. See the
// closure built below and previewMerge's own comment.
func previewSequencerOperation(flags globalFlags, verb string, args []string, recordedArgv []string) int {
	// The refusal comes before anything else, including the would-do record: a
	// preview that cannot be computed is a refusal of the whole invocation, and
	// a would-do log listing a command safegit just declined to preview would
	// state something that is not going to happen.
	parsed := parseGitArgs(verb, args)
	if reason := previewRefusal(flags.ctx(), verb, parsed); reason != "" {
		fmt.Fprintf(os.Stderr, "error: --dry-run cannot preview this %s: %s\n", verb, reason)
		fmt.Fprintf(os.Stderr, "  safegit's preview computes the real outcome with git's own merge engine\n")
		fmt.Fprintf(os.Stderr, "  (git merge-tree --write-tree), and a preview computed under different rules\n")
		fmt.Fprintf(os.Stderr, "  than the real run would use is not a preview of this command.\n")
		fmt.Fprintf(os.Stderr, "  Re-run without --dry-run, or without the option named above.\n")
		// The same code the framework's own per-command dry-run refusal exits
		// with (strictcli's WithDryRunUnsupported). This is the per-INVOCATION
		// form of that refusal, which the framework has no way to express yet;
		// giving it a different number would make one situation report two
		// answers depending on which layer noticed it. When the framework grows
		// a per-invocation form, this moves onto it unchanged.
		return exitcode.General
	}

	// The invocation is recorded in the framework's would-do log, exactly as it
	// was before there was anything to compute: the preview below is an
	// addition to the effects regime's answer, not a replacement for it.
	//
	// It is handed DOWN rather than emitted here, because whether the real run
	// issues this git command at all is a question only the computation below
	// can answer. A merge that turns out to be a FAST-FORWARD never reaches
	// git's merge machinery -- safegit moves the ref itself -- and neither does
	// one safegit REFUSES for `--ff-only`, which it decides before git runs. In
	// both cases recording the compute step would describe a subprocess nothing
	// performs. Every other path records it, the failing ones included: `safegit
	// merge no-such-ref` really does hand that argument to git in a real run, and
	// the preview reporting git's own resolution error first does not change what
	// the run would have done.
	record := func() int { return runGitMutation(flags, gitexec.NoDoor, recordedArgv...) }

	ctx, _, cleanup, err := commit.BeginPreview(flags.ctx(), true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	defer cleanup()

	if verb == "merge" {
		return previewMerge(flags, ctx, parsed, record)
	}
	return previewReplay(flags, ctx, verb, parsed, record)
}

// strategyOptionArgv collects every strategy option on the command line, ready
// to hand to merge-tree.
//
// EVERY occurrence, because git honors every one of them: the argv reader's
// Find returns only the first, so a single read would silently drop the second
// half of `-X ours -X ignore-space-change` and preview a tree the real run does
// not produce. And as SEPARATE elements, name then value, rather than the Raw
// member the messages quote: merge-tree rejects the space-joined `-X ours` as
// one argument (probed).
//
// The two spellings are two distinct option NAMES rather than one folded into
// the other, which is why both are asked for here.
func strategyOptionArgv(parsed gitArgs) []string {
	var out []string
	for _, o := range parsed.Options {
		if o.Name != "-X" && o.Name != "--strategy-option" {
			continue
		}
		out = append(out, o.Name, o.Value)
	}
	return out
}

// previewStrategyOptionFloor is the version floor the forwarding above needs,
// asked only where the forwarding happens.
//
// merge-tree learned `-X` in git 2.43, which is newer than the 2.38 floor
// `--write-tree` itself carries, so an operator on a git in between can RUN a
// merge with a strategy option while its preview cannot be computed. The
// question is therefore CONDITIONAL: a command line carrying no strategy option
// is previewed on every git that meets the older floor, and never refused for
// wanting the newer one.
//
// requireFloor is the check itself rather than a call reached for inside,
// because on a git that satisfies the floor a real check can only answer yes --
// and the conditional would then be untestable, both branches looking the same
// from outside. Production passes the standard floor-check path, which is the
// one authority on what safegit's git floors are.
func previewStrategyOptionFloor(parsed gitArgs, requireFloor func() error) string {
	if len(strategyOptionArgv(parsed)) == 0 {
		return ""
	}
	if err := requireFloor(); err != nil {
		return err.Error()
	}
	return ""
}

// previewRefusal decides whether this command line's outcome is computable, and
// returns the operator-facing reason when it is not.
//
// The rule is a CRITERION, not a list of known-bad flags: an option is refused
// when it changes how the TREE is computed and safegit's merge-tree invocation
// does not carry it, because dropping it would compute a different tree than
// the run being previewed. An option that only affects how the commit is
// created or how git reports itself -- --no-ff, --no-edit, --signoff, -m, the
// message flags -- changes no tree and is accepted.
//
// Applying that criterion needs no list of git's whole vocabulary: safegit's
// merge-tree argv carries --write-tree, a merge base, two commits and the
// STRATEGY OPTIONS the command line asked for, so the question for any other
// option is only whether it is one of the few that reach the merge machinery.
//
// Strategy options themselves are FORWARDED rather than refused (see
// strategyOptionArgv): previewing the unoptioned merge instead would answer a
// different command line without saying so. What they need is a newer git than
// the rest of the preview does, which is the one refusal left in this family.
func previewRefusal(ctx context.Context, verb string, parsed gitArgs) string {
	if len(parsed.AfterDoubleDash) > 0 {
		return "it carries a pathspec, which limits what the operation touches and which merge-tree has no way to express"
	}

	if reason := previewStrategyOptionFloor(parsed, func() error {
		return git.RequireFeature(ctx, gitversion.MergeTreeStrategyOption)
	}); reason != "" {
		return reason
	}
	if o, ok := parsed.Find("--squash"); ok {
		return fmt.Sprintf("%q stages the merge's result instead of committing it, and what it leaves behind is a staged index rather than the tree merge-tree computes", o.Raw)
	}
	if o, ok := parsed.Find("--continue", "--skip", "--abort", "--quit"); ok {
		return fmt.Sprintf("%q operates on an operation git already has in flight, which is a different question from what this command line would do", o.Raw)
	}
	if len(parsed.Revisions) == 0 {
		return "it names no commit to " + verb
	}
	return ""
}

// previewMerge computes what `safegit merge <branch>` would do.
//
// The three outcomes git itself distinguishes are distinguished here, because
// they are different answers and only one of them is a merge: already up to
// date, a fast-forward, and a real merge. Where the operator asked for
// --ff-only and the merge is not a fast-forward, the honest preview is that
// git would REFUSE, not that the merge is clean.
//
// recordCompute emits the would-do record for the compute step, and it is
// emitted on every path but the THREE answers the real run reaches without
// running git's merge machinery at all: the two fast-forwards, which safegit
// performs itself with a compare-and-swap, and the `--ff-only` refusal, which
// safegit decides before git runs. See previewSequencerOperation, where the
// closure is built and the rule stated.
//
// UP TO DATE is deliberately not one of them, and the difference is where the
// answer comes from: the real run finds that out by RUNNING the compute step
// and reading the state git left behind, so its record is true.
func previewMerge(flags globalFlags, ctx context.Context, parsed gitArgs, recordCompute func() int) int {
	// record emits the compute step's would-do record and hands back the exit
	// code the caller meant to return, so a path that records reads as one line.
	record := func(code int) int {
		if c := recordCompute(); c != 0 {
			return c
		}
		return code
	}

	if len(parsed.Revisions) > 1 {
		fmt.Fprintf(os.Stderr, "error: --dry-run cannot preview an octopus merge: merge-tree computes one merge of two commits\n")
		return record(exitcode.General)
	}
	other := parsed.Revisions[0]

	otherSHA, err := git.RevParse(ctx, other+"^{commit}")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s does not name a commit: %v\n", other, err)
		return record(exitcode.General)
	}

	// An UNBORN branch, and the answer is known without computing anything: a
	// merge into a branch with no commits is a fast-forward by definition. It
	// cannot be computed here in any case -- the ancestry questions below need a
	// commit on both sides, and merge-tree rejects a side that is not one -- and
	// the empty tree is no substitute, because a merge has no merge base to give
	// it.
	//
	// The question asked is whether the branch is UNBORN, not whether HEAD
	// happens to be unresolvable. They are not the same question: a HEAD that
	// fails to resolve for some other reason would otherwise be answered with an
	// unborn-branch sentence describing a repository state that is not the one
	// in front of the operator. It is also the predicate every other unborn path
	// in safegit asks, so the preview agrees with the refusals instead of
	// carrying a second test of its own. A HEAD that is not unborn and still
	// does not resolve falls through to the RevParse below and gets git's own
	// error.
	//
	// This is reached only by the PLAIN unborn merge. `--no-ff` and
	// `--no-commit` are refused before the preview runs, exactly as the real run
	// refuses them, so no command line the real run would refuse is previewed as
	// a fast-forward here.
	//
	// NO RECORD: the real run takes the unborn fast-forward arm, which is a
	// compare-and-swap pinned to the zero object name and a sync. No git merge.
	if git.HeadIsUnborn(ctx) {
		infof(flags, "would merge %s: a fast-forward onto an unborn branch, no merge commit and no merge to compute\n", short(other, otherSHA))
		return exitcode.OK
	}

	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return record(exitcode.General)
	}

	upToDate, err := git.IsAncestorOf(ctx, otherSHA, head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return record(exitcode.General)
	}
	if upToDate {
		infof(flags, "would merge %s: already up to date, nothing to do\n", short(other, otherSHA))
		return record(exitcode.OK)
	}

	fastForward, err := git.IsAncestorOf(ctx, head, otherSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return record(exitcode.General)
	}
	// NO RECORD, for the reason above: safegit performs this one itself.
	//
	// The predicate is the PREVIEW's, and it is the same one that decides the
	// sentence printed here -- so the record follows the answer the operator is
	// given rather than a second reading of the command line.
	if fastForward && !parsed.Has("--no-ff") {
		infof(flags, "would merge %s: a fast-forward, no merge commit and no merge to compute\n", short(other, otherSHA))
		return exitcode.OK
	}

	// --ff-only is decided BEFORE the merge, because that is where git decides
	// it: the branches have diverged, so git aborts with "Not possible to
	// fast-forward" and never runs its merge engine at all. Asking merge-tree
	// first and reporting a conflict would answer a question this command line
	// does not reach -- and would tell the operator to resolve conflicts in a
	// merge that is not going to start.
	// NO RECORD either, and for the same reason one step further along: this is a
	// REFUSAL that safegit makes itself, before git runs. performMerge exits here
	// without ever building the compute step's argv, so recording it would name a
	// subprocess nothing issues -- the property every other safegit refusal
	// already has, that a refused invocation leaves no would-do entry behind.
	if parsed.Has("--ff-only") {
		infof(flags, "would merge %s: REFUSED -- --ff-only was given and this is not a fast-forward\n", short(other, otherSHA))
		return exitcode.OK
	}

	if code := recordCompute(); code != 0 {
		return code
	}
	result, err := git.MergeTree(ctx, "", head, otherSHA, strategyOptionArgv(parsed)...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	reportPreviewOutcome(flags, "merge", short(other, otherSHA), result)
	return exitcode.OK
}

// previewReplay computes what a cherry-pick or a revert would do.
//
// ONE commit, because that is all either command takes: a multi-commit argv and
// every range spelling are refused before a preview is ever reached, so there is
// no queue to replay here and no intermediate commit to build. What the
// computation needs is the two sides that express the operation as a merge --
// see replaySides.
//
// recordCompute is emitted unconditionally here, and that is the whole
// difference from previewMerge: a cherry-pick and a revert have no
// fast-forward arm, so a real run always hands the operation to
// `git <verb> --no-commit` whatever the outcome turns out to be.
func previewReplay(flags globalFlags, ctx context.Context, verb string, parsed gitArgs, recordCompute func() int) int {
	if code := recordCompute(); code != 0 {
		return code
	}
	if len(parsed.Revisions) != 1 {
		// Unreachable through the commands, which refuse any other count before
		// the preview: stated rather than assumed, so a future caller that gets
		// here is told what it did instead of silently previewing the first one.
		fmt.Fprintf(os.Stderr, "error: safegit %s previews exactly one commit, and this names %d\n", verb, len(parsed.Revisions))
		return exitcode.General
	}

	c, err := git.RevParse(ctx, parsed.Revisions[0]+"^{commit}")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving the commit to %s: %v\n", verb, err)
		return exitcode.General
	}

	mainline := ""
	if o, ok := parsed.Find("-m", "--mainline"); ok {
		mainline = o.Value
	}

	// Our side of the replay. On an UNBORN branch there is no commit to name it
	// with, and the EMPTY TREE is the honest stand-in: it is what the branch
	// holds, and merge-tree accepts a tree for a side WHEN a merge base is given
	// -- which a replay always has, because the base is the source commit's
	// parent (a pick) or the source commit itself (a revert).
	ours, err := git.HeadTreeish(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading HEAD: %v\n", err)
		return exitcode.General
	}

	base, theirs, err := replaySides(ctx, verb, c, mainline)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	result, err := git.MergeTree(ctx, base, ours, theirs, strategyOptionArgv(parsed)...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	reportPreviewOutcome(flags, verb, describeCommit(ctx, c), result)
	return exitcode.OK
}

// replaySides names the merge base and the incoming side that express one
// cherry-pick or revert as a merge.
//
// The two are mirror images, which is the whole difference between the
// operations:
//
//	cherry-pick C    base = C's parent, incoming = C          (apply C's change)
//	revert C         base = C,          incoming = C's parent (undo C's change)
//
// mainline, when given, selects WHICH parent of a merge commit stands in for
// "C's parent" -- the same thing `-m` means to git.
func replaySides(ctx context.Context, verb, c, mainline string) (base, theirs string, err error) {
	parentRef := c + "^"
	if mainline != "" {
		parentRef = c + "^" + mainline
	}
	parent, err := git.RevParse(ctx, parentRef+"^{commit}")
	if err != nil {
		return "", "", fmt.Errorf("resolving the parent of %s (%s): %w", shortSHA(c), parentRef, err)
	}
	if verb == "revert" {
		return c, parent, nil
	}
	return parent, c, nil
}

// reportPreviewOutcome prints the computed answer for one operation.
func reportPreviewOutcome(flags globalFlags, verb, what string, result git.MergeTreeResult) {
	if !result.Conflicted {
		infof(flags, "would %s %s cleanly\n", verb, what)
		infof(flags, " resulting tree: %s\n", result.Tree)
		return
	}
	infof(flags, "would %s %s: CONFLICT in %d path(s)\n", verb, what, len(result.Paths))
	for _, p := range result.Paths {
		infof(flags, "  %s\n", p)
	}
	infof(flags, " the operation would stop here for you to resolve them\n")
}

// describeCommit renders a commit as an operator recognizes it, falling back to
// the abbreviated object name when the subject cannot be read. It is the same
// rendering the conflict listings use, from the same one place.
func describeCommit(ctx context.Context, sha string) string {
	described, err := conflict.Describe(ctx, sha)
	if err != nil || described == "" {
		return shortSHA(sha)
	}
	return described
}

// short renders a revision the operator typed alongside what it resolved to,
// unless the two are the same thing.
func short(typed, sha string) string {
	if typed == sha {
		return shortSHA(sha)
	}
	return fmt.Sprintf("%s (%s)", typed, shortSHA(sha))
}
