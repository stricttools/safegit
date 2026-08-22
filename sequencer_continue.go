package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	tomledit "github.com/smm-h/go-toml-edit"
	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
)

// The conclusion engine: one implementation behind the three flat commands
// `merge-continue`, `cherry-pick-continue` and `revert-continue`.
//
// git's model for finishing one of these operations is whole-index -- the state
// file names the other side, the index carries the operation's staged result,
// and `git commit` with no pathspec turns both into a commit. safegit's ordinary
// commit contract is the opposite (pathspec-only, temporary index seeded from
// the parent tree, one parent), which is why concluding an operation is a
// separate verb rather than a mode of `commit`: the two are different
// operations, and a flag that silently switched between them would be the
// ambiguity this tool exists to remove.
//
// What the engine does, in order:
//
//  1. Parses the declared resolutions, before any lock, so a malformed command
//     line is refused without touching the repository.
//  2. Takes the worktree operation lock (outermost; the pipeline's per-ref CAS
//     lock is taken inside it), then reads git's in-flight state under it.
//  3. Refuses the three states it cannot conclude: nothing in flight, another
//     operation in flight, a detached HEAD.
//  4. Checks the declared resolutions against the conflict actually in the
//     shared index -- every conflicted path named, and nothing else named --
//     and then verifies that the content they name carries no surviving
//     conflict marker.
//  5. Runs the commit pipeline with the shared index as its base, the state
//     file's commits as extra parents, and the resolutions as index edits.
//  6. Removes the operation's whole state-file set, reconciles the shared
//     index, and puts the WORKING TREE in step with the resolutions.

// resolutionChoice is one of the four things a conflicted path may be resolved
// to. The keywords are defined BY INDEX STAGE, not by operation folklore.
type resolutionChoice string

const (
	// resolveOurs is the stage-2 blob: the content the branch being committed
	// onto already had.
	resolveOurs resolutionChoice = "ours"
	// resolveTheirs is the stage-3 blob: the operation's incoming side. For a
	// revert that is the INVERSE patch's side -- the content the reverted
	// commit's parent held -- which is the classic confusion the per-path
	// listing below exists to remove.
	resolveTheirs resolutionChoice = "theirs"
	// resolveWorktree is the working-tree file's current content.
	resolveWorktree resolutionChoice = "worktree"
	// resolveDelete removes the path from the commit AND from the working tree,
	// which is what `git rm` does and what an operator who says "delete" means.
	resolveDelete resolutionChoice = "delete"
)

// resolutionChoices lists the accepted keywords in the order the help and the
// per-path listing render them.
var resolutionChoices = []resolutionChoice{resolveOurs, resolveTheirs, resolveWorktree, resolveDelete}

// resolutionSource records where a resolution was declared, so the payload can
// say it: the two inputs may be combined.
type resolutionSource string

const (
	resolutionFromFlag resolutionSource = "flag"
	resolutionFromFile resolutionSource = "file"
)

// resolution is one declared per-path resolution.
type resolution struct {
	Path   string
	Choice resolutionChoice
	Source resolutionSource
}

// parseResolveSelection reads one `--resolve` element, "path=choice".
//
// The split is on the LAST equals sign, so a path that itself contains one
// stays intact -- the same rule --hunks uses for its colon, and for the same
// reason: nothing on disk is consulted to decide how to read a command line.
//
// It is both the flag's registration-declared ValidateFn and the parser the
// handler uses, so the accepted language and the parsed language are one
// language by construction.
func parseResolveSelection(value string) (resolution, error) {
	eq := strings.LastIndex(value, "=")
	if eq < 0 {
		return resolution{}, fmt.Errorf("%q needs a path and a resolution separated by '=', as in 'path=theirs'", value)
	}
	path := value[:eq]
	if path == "" {
		return resolution{}, fmt.Errorf("%q names no path before the '='", value)
	}
	choice, err := parseResolutionChoice(value[eq+1:])
	if err != nil {
		return resolution{}, fmt.Errorf("invalid resolution in %q: %w", value, err)
	}
	return resolution{Path: path, Choice: choice, Source: resolutionFromFlag}, nil
}

// parseResolutionChoice validates one keyword.
func parseResolutionChoice(value string) (resolutionChoice, error) {
	for _, c := range resolutionChoices {
		if value == string(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("%q is not one of %s", value, choiceList())
}

// choiceList renders the accepted keywords for a message.
func choiceList() string {
	names := make([]string, len(resolutionChoices))
	for i, c := range resolutionChoices {
		names[i] = string(c)
	}
	return strings.Join(names, ", ")
}

// validateResolveSelection is the flag's per-element ValidateFn.
//
// It is declared on a repeatable STRING flag rather than on a dict flag, which
// would otherwise be the natural shape for `path=value` pairs: strictcli marks
// every dict flag repeatable and stores its value as a map, and the parser's
// repeatable branch type-asserts the stored value to a slice and silently
// SKIPS validation when the assertion fails -- so a dict flag's ValidateFn
// never runs, and Choices() panics on a dict flag, leaving no way at all to
// constrain the values. The string flag's per-element validator does run, so
// an unknown keyword is refused at parse time.
func validateResolveSelection(v interface{}) error {
	_, err := parseResolveSelection(v.(string))
	return err
}

// resolveFile is the `--resolve-file` TOML schema: an array of tables, each
// naming a path and the choice for it. Same conventions as the scrub-run
// recipe, which is the other TOML input safegit takes.
//
//	[[resolutions]]
//	path = "src/a.go"
//	choice = "theirs"
type resolveFile struct {
	Resolutions []resolveFileEntry `toml:"resolutions"`
}

type resolveFileEntry struct {
	Path   string `toml:"path"`
	Choice string `toml:"choice"`
}

// parseResolveFile reads and validates a --resolve-file.
func parseResolveFile(path string) ([]resolution, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the resolution file: %w", err)
	}
	var file resolveFile
	if err := tomledit.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parsing the resolution file %s: %w", path, err)
	}
	if len(file.Resolutions) == 0 {
		return nil, fmt.Errorf("%s declares no [[resolutions]] tables", path)
	}
	out := make([]resolution, 0, len(file.Resolutions))
	for i, e := range file.Resolutions {
		if e.Path == "" {
			return nil, fmt.Errorf("%s: resolution %d names no path", path, i)
		}
		choice, err := parseResolutionChoice(e.Choice)
		if err != nil {
			return nil, fmt.Errorf("%s: resolution %d for %s: %w", path, i, e.Path, err)
		}
		out = append(out, resolution{Path: e.Path, Choice: choice, Source: resolutionFromFile})
	}
	return out, nil
}

// collectResolutions merges the flag and file forms into one declared set.
//
// The two may be combined; a path named in both, or twice in either, is a hard
// error naming the path. There is no precedence rule to remember, because two
// statements about one path are a contradiction rather than an override.
func collectResolutions(flagValues []string, filePath string) ([]resolution, error) {
	var all []resolution
	for _, v := range flagValues {
		r, err := parseResolveSelection(v)
		if err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if filePath != "" {
		fromFile, err := parseResolveFile(filePath)
		if err != nil {
			return nil, err
		}
		all = append(all, fromFile...)
	}

	seen := make(map[string]resolution, len(all))
	for _, r := range all {
		if prev, dup := seen[r.Path]; dup {
			return nil, fmt.Errorf("%s is resolved twice: %s (%s) and %s (%s); name each conflicted path once",
				r.Path, prev.Choice, prev.Source, r.Choice, r.Source)
		}
		seen[r.Path] = r
	}
	return all, nil
}

// continueOp is one conclusion command: the operation it concludes, its own
// name, and the wording its per-path listing uses for the incoming side.
type continueOp struct {
	// kind is the sequencer state this command concludes, and the declaration it
	// hands the commit pipeline.
	kind sequencer.Kind
	// command is the command's own name, as an operator types it.
	command string
	// oursText and theirsText describe, in this operation's own terms, what the
	// two stage keywords concretely resolve to. describeIncoming fills in the
	// source commit where the operation has one.
	oursText string
}

var (
	mergeContinueOp = continueOp{
		kind:     sequencer.KindMerge,
		command:  "merge-continue",
		oursText: "the content this branch already had, before the merge",
	}
	cherryPickContinueOp = continueOp{
		kind:     sequencer.KindCherryPick,
		command:  "cherry-pick-continue",
		oursText: "your branch's current content",
	}
	revertContinueOp = continueOp{
		kind:     sequencer.KindRevert,
		command:  "revert-continue",
		oursText: "your branch's current content",
	}
)

// theirsText describes the incoming side in this operation's own terms. It is
// the mitigation the plan puts AT THE DECISION POINT rather than in skimmable
// help: `theirs` on a revert is the inverse patch's side, which is what the
// commit being reverted UNDID, and an operator reasoning from the word alone
// gets it backwards.
func (op continueOp) theirsText(ctx context.Context, state sequencer.State) string {
	switch op.kind {
	case sequencer.KindMerge:
		if len(state.MergeHeads) == 1 {
			if short, err := git.AbbrevSHA(ctx, state.MergeHeads[0]); err == nil {
				return "the content merged in from " + short
			}
		}
		return "the content merged in from the other side"
	case sequencer.KindCherryPick:
		if described, err := conflict.Describe(ctx, state.Source); err == nil {
			return "the result of applying " + described
		}
		return "the result of applying the cherry-picked commit"
	case sequencer.KindRevert:
		if described, err := conflict.Describe(ctx, state.Source); err == nil {
			return "the result of UNDOING " + described
		}
		return "the result of UNDOING the reverted commit"
	}
	return "the operation's incoming side"
}

// conclusionResult is what a completed (or previewed) conclusion reports.
type conclusionResult struct {
	state    sequencer.State
	commit   *commit.CommitResult
	declared []resolution
	author   *git.AuthorInfo
	cleared  bool
}

// runContinue is the whole conclusion flow, shared by the three commands.
//
// It returns the process exit code and never panics on a repository state it
// does not recognize: every state it cannot conclude is a refusal naming the
// state and the command that CAN conclude it.
func runContinue(flags globalFlags, op continueOp, messages []string, trailers []string, resolveFlags []string, resolveFilePath string) int {
	gitDir := mustGitDir()
	if err := ensureInitialized(flags, gitDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.NotInitialized
	}

	// Before any lock: a command line safegit itself rejects is refused without
	// the repository being touched at all.
	declared, err := collectResolutions(resolveFlags, resolveFilePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.Usage
	}

	sgDir := repo.SafegitDir(gitDir)
	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return exitcode.General
	}

	// A conclusion in a submodule moves the parent's gitlink exactly as an
	// ordinary commit does, so the parent must have answered the auto-bump
	// question before anything is written.
	if err := requireAutoBumpDecision(flags.ctx(), flags); err != nil {
		fmt.Fprintf(os.Stderr, "error: auto-bump parent: %v\n", err)
		return exitcode.General
	}

	// Outermost, and taken BEFORE the state is read, so no passthrough in this
	// worktree can create or conclude an operation between the read and the ref
	// update. The pipeline's per-ref CAS lock is taken inside it.
	release, code := acquireOperationLock(flags, gitDir, op.command)
	if code != 0 {
		return code
	}
	defer release()

	ctx := flags.ctx()

	state, err := sequencer.Read(gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading git's in-flight operation state: %v\n", err)
		return exitcode.General
	}
	if code := op.refuseWrongState(ctx, gitDir, state); code != 0 {
		return code
	}
	if state.Queued {
		return delegateQueuedSequence(op, state)
	}
	if _, err := git.HeadRef(ctx); err != nil {
		return op.refuseDetachedHead(state)
	}

	// The conflict as it actually stands, read from the shared index safegit
	// only ever reads.
	sides, err := conflict.Stages(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the conflicted paths from the index: %v\n", err)
		return exitcode.General
	}
	if code := op.checkCompleteness(ctx, state, sides, declared); code != 0 {
		return code
	}
	// The resolutions name the right paths; now the content those paths carry
	// has to be free of the conflict itself. See sequencer_markers.go.
	if code := op.verifyMarkers(ctx, state, sides, declared); code != 0 {
		return code
	}

	edits, err := indexEditsFor(sides, declared)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	message, err := op.conclusionMessage(ctx, state, messages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.Usage
	}

	var author *git.AuthorInfo
	if op.kind == sequencer.KindCherryPick || op.kind == sequencer.KindRevert {
		info, err := sequencer.SourceAuthor(ctx, state)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitcode.General
		}
		author = &info
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg, RefUpdate: effectsRefUpdate{flags}}
	result, err := p.Execute(ctx, commit.CommitRequest{
		Message:      message,
		Trailers:     trailers,
		DryRun:       flags.dryRun,
		ExtraParents: state.MergeHeads,
		IndexBase:    commit.IndexBaseSharedIndex,
		IndexEdits:   edits,
		Author:       author,
		OplogOp:      op.command,
		// A merge commit records its parents whether or not it changes a single
		// byte, so the pipeline's tree-unchanged refusal does not apply to one.
		// The cherry-pick and revert conclusions produce ordinary single-parent
		// commits and keep the refusal.
		AllowEmpty: op.kind == sequencer.KindMerge,
		// The declared bypass of the mid-operation refusal. It is verified
		// against the state on disk, not taken on trust.
		Sequencer: &coord.SequencerContext{Kind: op.kind},
	})
	if err != nil {
		// The empty-commit refusal names --allow-empty, a flag these commands do
		// not have. A conclusion that produces nothing is a real situation with
		// its own ways out, so it gets its own message rather than a pointer at
		// something the operator cannot pass.
		if errors.Is(err, commit.ErrTreeUnchanged) {
			return op.refuseEmptyConclusion()
		}
		die(pipelineExitCode(err), err.Error())
	}

	out := conclusionResult{state: state, commit: result, declared: declared, author: author}

	if !flags.dryRun {
		if err := finishConclusion(ctx, gitDir, state, result, edits, sides, declared); err != nil {
			die(exitcode.General, err.Error())
		}
		out.cleared = true

		if err := maybeAutoBumpParent(ctx, flags, gitDir, result.SHA, op.command, firstLine(message)); err != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
		}
	}

	op.report(flags, out)
	return exitcode.OK
}

// finishConclusion removes the concluded operation's state and puts the shared
// index and the working tree back in step with the new tip.
//
// The order is deliberate. The state files go first: the operation is over the
// moment its commit exists, and a crash between here and the reconcile leaves a
// repository that is merely out of step with its index rather than one git still
// considers mid-merge.
//
// The index then needs the resolutions applied to it before it is reconciled.
// ReconcileMainIndex deliberately PRESERVES unmerged stages -- a conflict
// another session is resolving has to survive somebody else's commit -- so
// reconciling an index that still holds this operation's stages would leave a
// repository sitting on the merge commit and still reporting the merge's
// conflict. Applying the same edits first turns each of those paths into an
// ordinary stage-0 entry that the new tip's tree already explains, which the
// reconcile then folds away.
//
// The working tree goes LAST, because it is the only step whose failure leaves
// nothing inconsistent behind: the commit is real, the state is gone and the
// index matches it, so a file that could not be written is one named error
// about one path rather than a repository stuck mid-operation.
func finishConclusion(ctx context.Context, gitDir string, state sequencer.State, result *commit.CommitResult, edits []commit.IndexEdit, sides map[string]conflict.Sides, declared []resolution) error {
	if err := sequencer.Cleanup(gitDir, state.Kind); err != nil {
		return fmt.Errorf("commit %s was created, but removing the %s state files failed: %w", shortSHA(result.SHA), state.Kind, err)
	}

	if err := commit.ApplyIndexEditsTo(ctx, "", edits); err != nil {
		return fmt.Errorf("commit %s was created, but resolving the shared index failed: %w", shortSHA(result.SHA), err)
	}

	if err := git.ReconcileMainIndex(ctx, firstParentOf(result), "HEAD"); err != nil {
		return fmt.Errorf("commit %s was created, but reconciling the shared index failed: %w", shortSHA(result.SHA), err)
	}

	if err := materializeResolutions(ctx, sides, declared); err != nil {
		return fmt.Errorf("commit %s was created, but %w", shortSHA(result.SHA), err)
	}
	return nil
}

// materializeResolutions writes each declared resolution into the WORKING TREE,
// so the file on disk holds what was just committed.
//
// This is git's own idiom, and the reason it is not optional: `git checkout
// --ours <path>` replaces the file on disk, `git rm <path>` deletes it, and an
// operator who says `--resolve x=ours` means the same thing by it. A conclusion
// that resolved only the index would leave the marker-carrying file sitting in
// the working tree, one `safegit commit -- x` away from committing the very
// conflict markers the conclusion just resolved away.
//
// It runs on a SUCCEEDED conclusion only, never on a refusal and never under
// --dry-run, because it is called from finishConclusion, which itself runs
// nowhere else. A preview says what it would write instead.
//
// `worktree` is exempt by definition -- the file on disk IS the resolution --
// and a gitlink is skipped because a submodule's working-tree state is the
// submodule's own checkout, not a blob this repository can write.
func materializeResolutions(ctx context.Context, sides map[string]conflict.Sides, declared []resolution) error {
	if len(declared) == 0 {
		return nil
	}
	root, err := git.AnchorRoot(ctx)
	if err != nil {
		return fmt.Errorf("resolving the directory git's paths are relative to, to write the resolved files: %w", err)
	}

	for _, r := range declared {
		s := sides[r.Path]
		abs := git.Anchor(root, r.Path)
		var writeErr error
		switch r.Choice {
		case resolveWorktree:
			continue
		case resolveDelete:
			writeErr = removeWorktreeFile(abs)
		case resolveOurs:
			writeErr = writeStageToWorktree(ctx, abs, s.Ours)
		case resolveTheirs:
			writeErr = writeStageToWorktree(ctx, abs, s.Theirs)
		default:
			return fmt.Errorf("internal: %s carries an unrecognized resolution %q", r.Path, r.Choice)
		}
		if writeErr != nil {
			return fmt.Errorf("writing the resolved content of %s into the working tree failed: %w", r.Path, writeErr)
		}
	}
	return nil
}

// writeStageToWorktree puts one stage's blob on disk. An ABSENT stage is the
// side that deleted the path, so resolving to it removes the file -- exactly
// what the same absent stage does to the index entry.
func writeStageToWorktree(ctx context.Context, abs string, e *git.UnmergedEntry) error {
	if e == nil {
		return removeWorktreeFile(abs)
	}
	switch e.Mode {
	case gitlinkMode:
		// A submodule's own checkout, not this repository's to write.
		return nil
	case symlinkMode:
		target, err := git.CatFileBlob(ctx, e.SHA)
		if err != nil {
			return err
		}
		if err := removeWorktreeFile(abs); err != nil {
			return err
		}
		return os.Symlink(string(target), abs)
	}

	content, err := git.CatFileBlob(ctx, e.SHA)
	if err != nil {
		return err
	}
	perm := os.FileMode(0644)
	if e.Mode == executableMode {
		perm = 0755
	}
	if err := os.WriteFile(abs, content, perm); err != nil {
		return err
	}
	// WriteFile leaves an existing file's mode alone, and a conflict can change
	// the executable bit, so the mode is set explicitly rather than inherited
	// from whatever the conflicted file happened to be.
	return os.Chmod(abs, perm)
}

// removeWorktreeFile deletes a path from disk, treating an already-absent file
// as done. Empty parent directories are deliberately left behind: removing them
// could take a directory another session is using.
func removeWorktreeFile(abs string) error {
	if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// The index modes a resolution can carry, spelled once.
const (
	executableMode = "100755"
	symlinkMode    = "120000"
	gitlinkMode    = "160000"
)

// firstParentOf is the tip the conclusion was built on: the value the shared
// index was last in step with.
func firstParentOf(result *commit.CommitResult) string {
	if len(result.Parents) == 0 {
		return ""
	}
	return result.Parents[0]
}

// refuseWrongState covers the two states a conclusion command cannot be run
// against: nothing in flight, and an operation this command does not conclude.
// Both are the same verdict the commit pipeline's own declaration check gives,
// so both exit CoordinationBusy.
func (op continueOp) refuseWrongState(ctx context.Context, gitDir string, state sequencer.State) int {
	if state.InProgress() {
		if state.Kind == op.kind {
			return 0
		}
		fmt.Fprintf(os.Stderr, "error: safegit %s cannot conclude %s\n", op.command, state.String())
		w := coord.WayOutOf(state)
		if w.Conclude != "" {
			fmt.Fprintf(os.Stderr, "  conclude it:  %s\n", w.Conclude)
		}
		if w.Abandon != "" {
			fmt.Fprintf(os.Stderr, "  abandon it:   %s\n", w.Abandon)
		}
		return exitcode.CoordinationBusy
	}

	fmt.Fprintf(os.Stderr, "error: nothing to conclude: no operation is in progress\n")
	fmt.Fprintf(os.Stderr, "  safegit %s concludes %s that git stopped before committing\n", op.command, op.article())
	// A LONE AUTO_MERGE is residue of an operation that already finished --
	// git's own `rebase --continue` leaves one behind -- so it is not evidence
	// of anything in flight and never a refusal of its own. It is mentioned
	// here because an operator who just looked in .git and saw it deserves to
	// know why it does not count.
	if _, present, err := conflict.AutoMergeTree(ctx); err == nil && present {
		fmt.Fprintf(os.Stderr, "  (%s/AUTO_MERGE is present, but git leaves it behind when an operation FINISHES;\n", gitDir)
		fmt.Fprintf(os.Stderr, "   only an operation's own state files say one is in flight, and there are none)\n")
	}
	return exitcode.CoordinationBusy
}

// article names the operation the way the refusal sentence needs it.
func (op continueOp) article() string {
	switch op.kind {
	case sequencer.KindMerge:
		return "a merge"
	case sequencer.KindCherryPick:
		return "a cherry-pick"
	case sequencer.KindRevert:
		return "a revert"
	}
	return "an operation"
}

// refuseDetachedHead refuses a conclusion on a detached HEAD, with the way out.
//
// It is a refusal rather than a special case because safegit's commit pipeline
// is branch-shaped throughout: every commit is a compare-and-swap on a ref, and
// a detached HEAD has no ref to swap. Same exit code as undo's own
// detached-HEAD refusal, which is the same situation with the same remedy.
//
// The remedy is exact, and it is NOT `git switch -c`, which is what an operator
// would reach for first: git refuses that outright while an operation is in
// flight ("fatal: cannot switch branch while merging" / "while cherry-picking",
// probed on git 2.54, and the same for a revert). Creating the branch ref and
// re-pointing HEAD at it does the same job through two plumbing calls that
// touch neither the index nor the working tree, so every state file, every
// conflict stage and every resolution already made survives -- verified by
// TestConclusionDetachedHeadGuidanceWorks.
func (op continueOp) refuseDetachedHead(state sequencer.State) int {
	fmt.Fprintf(os.Stderr, "error: HEAD is detached; safegit %s commits onto a branch\n", op.command)
	fmt.Fprintf(os.Stderr, "  %s is still in progress and nothing has been lost. Put HEAD on a branch and conclude there:\n", state.String())
	fmt.Fprintf(os.Stderr, "    git branch <name>\n")
	fmt.Fprintf(os.Stderr, "    git symbolic-ref HEAD refs/heads/<name>\n")
	fmt.Fprintf(os.Stderr, "    safegit %s\n", op.command)
	fmt.Fprintf(os.Stderr, "  ('git switch -c <name>' does NOT work here: git refuses to switch branches mid-%s.\n", state.Kind)
	fmt.Fprintf(os.Stderr, "   The two commands above move HEAD without touching the index or the working tree.)\n")
	return exitcode.General
}

// refuseEmptyConclusion covers a cherry-pick or revert whose declared
// resolutions leave the tree exactly as it was.
//
// It cannot happen for a merge, whose conclusion allows an empty tree outright:
// a merge commit records its parents whether or not anything changed. For the
// other two, an empty result means the operation produced nothing -- every
// conflicted path was resolved to content the branch already had -- and git
// refuses the same case for the same reason. safegit offers no --allow-empty
// here, so the message names the ways out that do exist.
func (op continueOp) refuseEmptyConclusion() int {
	verb := strings.TrimSuffix(op.command, "-continue")
	fmt.Fprintf(os.Stderr, "error: this %s produces no change: the resolutions leave the tree exactly as it is\n", verb)
	fmt.Fprintf(os.Stderr, "  resolve at least one path to something the branch does not already have, or drop the operation:\n")
	fmt.Fprintf(os.Stderr, "    git %s --skip     # move past this commit, keeping the rest of the operation\n", verb)
	fmt.Fprintf(os.Stderr, "    git %s --abort    # throw the whole operation away\n", verb)
	return exitcode.General
}

// delegateQueuedSequence is where the queued cherry-pick and revert paths will
// hand off to git's own `--continue` (subphase 6.4). Until they do, a queued
// sequence is refused rather than half-concluded: concluding one step natively
// and removing the state-file set would take the rest of the queue with it.
func delegateQueuedSequence(op continueOp, state sequencer.State) int {
	fmt.Fprintf(os.Stderr, "error: safegit %s cannot yet conclude %s\n", op.command, state.String())
	fmt.Fprintf(os.Stderr, "  a QUEUED sequence is concluded by delegating to git's own '%s --continue', which safegit does not do yet\n", strings.TrimSuffix(op.command, "-continue"))
	fmt.Fprintf(os.Stderr, "  meanwhile: resolve the conflict and run 'git %s --continue' yourself, or abandon it with 'git %s --abort'\n",
		strings.TrimSuffix(op.command, "-continue"), strings.TrimSuffix(op.command, "-continue"))
	return exitcode.General
}

// checkCompleteness is the readable pre-pass in front of write-tree's own
// refusal: a conclusion must name every conflicted path and name nothing else.
//
// Both halves are hard errors listing the paths, and the omission half prints
// the per-path listing that says what each keyword concretely resolves to. That
// listing is the whole mitigation for the `theirs`-on-a-revert confusion: it is
// printed where the decision is made, not left in help text.
func (op continueOp) checkCompleteness(ctx context.Context, state sequencer.State, sides map[string]conflict.Sides, declared []resolution) int {
	named := make(map[string]bool, len(declared))
	for _, r := range declared {
		named[r.Path] = true
	}

	var stray []string
	for _, r := range declared {
		if _, conflicted := sides[r.Path]; !conflicted {
			stray = append(stray, r.Path)
		}
	}
	if len(stray) > 0 {
		sort.Strings(stray)
		fmt.Fprintf(os.Stderr, "error: %d path(s) are resolved but not conflicted:\n", len(stray))
		for _, p := range stray {
			fmt.Fprintf(os.Stderr, "  %s\n", p)
		}
		fmt.Fprintf(os.Stderr, "  a resolution names a path git left unmerged; paths are repository-relative\n")
		return exitcode.ConclusionUnresolved
	}

	var missing []string
	for path := range sides {
		if !named[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		return 0
	}
	sort.Strings(missing)

	fmt.Fprintf(os.Stderr, "error: %d conflicted path(s) have no resolution:\n", len(missing))
	fmt.Fprint(os.Stderr, op.conflictListing(ctx, state, sides, missing))
	fmt.Fprintf(os.Stderr, "  or write them into a file and pass --resolve-file:\n")
	fmt.Fprintf(os.Stderr, "    [[resolutions]]\n    path = %q\n    choice = \"theirs\"\n", missing[0])
	return exitcode.ConclusionUnresolved
}

// conflictListing renders, per path, exactly what each keyword resolves to for
// THIS operation, including what an absent stage means.
func (op continueOp) conflictListing(ctx context.Context, state sequencer.State, sides map[string]conflict.Sides, paths []string) string {
	oursText := op.oursText
	theirsText := op.theirsText(ctx, state)

	var b strings.Builder
	for _, path := range paths {
		s := sides[path]
		fmt.Fprintf(&b, "  %s\n", path)
		fmt.Fprintf(&b, "      --resolve '%s=ours'      %s\n", path, sideText(s.Ours != nil, oursText))
		fmt.Fprintf(&b, "      --resolve '%s=theirs'    %s\n", path, sideText(s.Theirs != nil, theirsText))
		fmt.Fprintf(&b, "      --resolve '%s=worktree'  the file as it stands in your working tree right now\n", path)
		fmt.Fprintf(&b, "      --resolve '%s=delete'    leave the path out of the commit and delete the file from disk\n", path)
		fmt.Fprintf(&b, "      (ours and theirs write the chosen content into the working tree too, as git's own checkout --ours does)\n")
	}
	return b.String()
}

// sideText renders one stage keyword's meaning, saying so when the stage is
// absent: a side that deleted the path resolves to the path being removed, and
// silently rendering "the content of..." for it would be a lie.
func sideText(present bool, text string) string {
	if !present {
		return "that side deleted the path, so this removes it from the commit"
	}
	return text
}

// indexEditsFor turns the declared resolutions into the pipeline's mechanical
// index edits, resolving each stage keyword to the blob it names HERE, where
// the conflict vocabulary lives.
func indexEditsFor(sides map[string]conflict.Sides, declared []resolution) ([]commit.IndexEdit, error) {
	edits := make([]commit.IndexEdit, 0, len(declared))
	for _, r := range declared {
		s, ok := sides[r.Path]
		if !ok {
			return nil, fmt.Errorf("internal: %s is resolved but carries no index stages", r.Path)
		}
		switch r.Choice {
		case resolveOurs:
			edits = append(edits, stageEdit(r.Path, s.Ours))
		case resolveTheirs:
			edits = append(edits, stageEdit(r.Path, s.Theirs))
		case resolveWorktree:
			edits = append(edits, commit.IndexEdit{Kind: commit.IndexEditWorktree, Path: r.Path})
		case resolveDelete:
			edits = append(edits, commit.IndexEdit{Kind: commit.IndexEditRemove, Path: r.Path})
		default:
			return nil, fmt.Errorf("internal: %s carries an unrecognized resolution %q", r.Path, r.Choice)
		}
	}
	return edits, nil
}

// stageEdit renders one stage as an index edit. An ABSENT stage is the side
// that deleted the path, so resolving to it removes the path -- which is what
// the listing said it would do.
func stageEdit(path string, e *git.UnmergedEntry) commit.IndexEdit {
	if e == nil {
		return commit.IndexEdit{Kind: commit.IndexEditRemove, Path: path}
	}
	return commit.IndexEdit{Kind: commit.IndexEditBlob, Path: path, Mode: e.Mode, SHA: e.SHA}
}

// conclusionMessage is the message the concluding commit carries: git's own
// draft with its comment block stripped, or the caller's -m values.
//
// The draft is MERGE_MSG (all three operations write it there), and it still
// carries the "# Conflicts:" comment block git would strip at commit time, so
// stripping it here is reproducing git's behavior rather than editing the
// operator's text.
func (op continueOp) conclusionMessage(ctx context.Context, state sequencer.State, messages []string) (string, error) {
	if len(messages) > 0 {
		return joinMessages(messages), nil
	}
	if state.MessageFile == "" {
		return "", fmt.Errorf("git left no message draft for this %s; pass -m to supply one", state.Kind)
	}
	raw, err := os.ReadFile(state.MessageFile)
	if err != nil {
		return "", fmt.Errorf("reading the message draft %s: %w", state.MessageFile, err)
	}
	stripped, err := git.StripComments(ctx, string(raw))
	if err != nil {
		return "", fmt.Errorf("stripping comments from %s: %w", state.MessageFile, err)
	}
	stripped = strings.TrimRight(stripped, "\n")
	if strings.TrimSpace(stripped) == "" {
		return "", fmt.Errorf("the message draft %s is empty once its comments are stripped; pass -m to supply one", state.MessageFile)
	}
	return stripped, nil
}
