package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
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
func previewSequencerOperation(flags globalFlags, verb string, args []string) int {
	// The refusal comes before anything else, including the would-do record: a
	// preview that cannot be computed is a refusal of the whole invocation, and
	// a would-do log listing a command safegit just declined to preview would
	// state something that is not going to happen.
	parsed := parseGitArgs(verb, args)
	if reason := previewRefusal(verb, parsed); reason != "" {
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
	if code := runGitMutation(flags, append([]string{verb}, args...)...); code != 0 {
		return code
	}

	ctx, _, cleanup, err := commit.BeginPreview(flags.ctx(), true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	defer cleanup()

	if verb == "merge" {
		return previewMerge(flags, ctx, parsed)
	}
	return previewReplay(flags, ctx, verb, parsed)
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
// merge-tree argv is fixed (--write-tree, a merge base, two commits), so the
// question for any option is only whether it is one of the few that reach the
// merge machinery.
func previewRefusal(verb string, parsed gitArgs) string {
	if len(parsed.AfterDoubleDash) > 0 {
		return "it carries a pathspec, which limits what the operation touches and which merge-tree has no way to express"
	}

	if o, ok := parsed.Find("-s", "--strategy"); ok && verb == "merge" && !isOrtStrategy(o.Value) {
		return fmt.Sprintf("%q selects a merge strategy other than ort, and merge-tree implements only ort", o.Raw)
	}
	if o, ok := parsed.Find("--strategy"); ok && verb != "merge" && !isOrtStrategy(o.Value) {
		return fmt.Sprintf("%q selects a merge strategy other than ort, and merge-tree implements only ort", o.Raw)
	}
	if o, ok := parsed.Find("-X", "--strategy-option"); ok {
		return fmt.Sprintf("%q changes how the merge resolves, and safegit's preview does not forward strategy options to the merge it computes", o.Raw)
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

// isOrtStrategy reports whether a --strategy value names the strategy
// merge-tree implements. The default (an unset value) is ort, and `recursive`
// is git's own long-standing alias for it.
func isOrtStrategy(value string) bool {
	switch value {
	case "", "ort", "recursive":
		return true
	}
	return false
}

// previewMerge computes what `safegit merge <branch>` would do.
//
// The three outcomes git itself distinguishes are distinguished here, because
// they are different answers and only one of them is a merge: already up to
// date, a fast-forward, and a real merge. Where the operator asked for
// --ff-only and the merge is not a fast-forward, the honest preview is that
// git would REFUSE, not that the merge is clean.
func previewMerge(flags globalFlags, ctx context.Context, parsed gitArgs) int {
	if len(parsed.Revisions) > 1 {
		fmt.Fprintf(os.Stderr, "error: --dry-run cannot preview an octopus merge: merge-tree computes one merge of two commits\n")
		return exitcode.General
	}
	other := parsed.Revisions[0]

	otherSHA, err := git.RevParse(ctx, other+"^{commit}")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s does not name a commit: %v\n", other, err)
		return exitcode.General
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading HEAD: %v\n", err)
		return exitcode.General
	}

	upToDate, err := git.IsAncestorOf(ctx, otherSHA, head)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if upToDate {
		flags.printf("would merge %s: already up to date, nothing to do\n", short(other, otherSHA))
		return exitcode.OK
	}

	fastForward, err := git.IsAncestorOf(ctx, head, otherSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if fastForward && !parsed.Has("--no-ff") {
		flags.printf("would merge %s: a fast-forward, no merge commit and no merge to compute\n", short(other, otherSHA))
		return exitcode.OK
	}

	result, err := git.MergeTree(ctx, "", head, otherSHA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if !result.Conflicted && parsed.Has("--ff-only") {
		flags.printf("would merge %s: REFUSED -- --ff-only was given and this is not a fast-forward\n", short(other, otherSHA))
		return exitcode.OK
	}
	reportPreviewOutcome(flags, "merge", short(other, otherSHA), result)
	return exitcode.OK
}

// previewReplay computes what a cherry-pick or a revert would do, one queued
// commit at a time.
//
// A queue is replayed the way git replays it: each step is computed on top of
// the previous step's result, so the preview stops exactly where git would stop
// and names the commit it stopped on. Building the intermediate commit object
// is what makes the chain possible, and it costs nothing outside the preview --
// the object goes into the quarantine with everything else merge-tree writes.
func previewReplay(flags globalFlags, ctx context.Context, verb string, parsed gitArgs) int {
	commits, err := revListNoWalk(ctx, parsed.Revisions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving the commits to %s: %v\n", verb, err)
		return exitcode.General
	}
	if len(commits) == 0 {
		fmt.Fprintf(os.Stderr, "error: %s names no commit\n", strings.Join(parsed.Revisions, " "))
		return exitcode.General
	}

	mainline := ""
	if o, ok := parsed.Find("-m", "--mainline"); ok {
		mainline = o.Value
	}

	ours, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading HEAD: %v\n", err)
		return exitcode.General
	}

	for i, c := range commits {
		base, theirs, err := replaySides(ctx, verb, c, mainline)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitcode.General
		}

		result, err := git.MergeTree(ctx, base, ours, theirs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitcode.General
		}

		described := describeCommit(ctx, c)
		if result.Conflicted {
			if i > 0 {
				flags.printf("would %s %d commit(s), then stop at %s\n", verb, i, described)
			}
			reportPreviewOutcome(flags, verb, described, result)
			return exitcode.OK
		}
		if i == len(commits)-1 {
			if len(commits) > 1 {
				flags.printf("would %s %d commit(s) cleanly, ending at %s\n", verb, len(commits), described)
				flags.printf(" resulting tree: %s\n", result.Tree)
				return exitcode.OK
			}
			reportPreviewOutcome(flags, verb, described, result)
			return exitcode.OK
		}

		// Not the last step: the next one is computed on top of this result, so
		// it needs a commit to stand on. It is built in the quarantine and
		// never referenced by anything.
		ours, err = git.CommitTree(ctx, result.Tree, []string{ours}, "preview of "+verb+" "+shortSHA(c), nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: building the intermediate commit for the preview: %v\n", err)
			return exitcode.General
		}
	}
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
		flags.printf("would %s %s cleanly\n", verb, what)
		flags.printf(" resulting tree: %s\n", result.Tree)
		return
	}
	flags.printf("would %s %s: CONFLICT in %d path(s)\n", verb, what, len(result.Paths))
	for _, p := range result.Paths {
		flags.printf("  %s\n", p)
	}
	flags.printf(" the operation would stop here for you to resolve them\n")
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

// revListNoWalk resolves the commits an operation would process, in the order
// git would process them: `--no-walk` shows the named commits and has no effect
// on a range, which is walked -- the same reading `git cherry-pick` and
// `git revert` give their own arguments.
func revListNoWalk(ctx context.Context, revisions []string) ([]string, error) {
	out, _, err := git.Run(ctx, append([]string{"rev-list", "--no-walk"}, revisions...)...)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

// short renders a revision the operator typed alongside what it resolved to,
// unless the two are the same thing.
func short(typed, sha string) string {
	if typed == sha {
		return shortSHA(sha)
	}
	return fmt.Sprintf("%s (%s)", typed, shortSHA(sha))
}
