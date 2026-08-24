package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	"github.com/smm-h/safegit/internal/trailer"
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
	// declines are the checks this conclusion did NOT make -- today, the marker
	// verification over a path carrying the committed exemption. An unreported
	// skip is a clean verdict over unchecked content.
	declines []declinedCheck
	// author is the identity the concluding commit RECORDS as its author, nil
	// for a merge. Whether it was preserved from the source commit or is the
	// operator's own is op.preservesSourceAuthor's answer, not a property of
	// this field.
	author  *git.AuthorInfo
	cleared bool
}

// preservesSourceAuthor reports whether this conclusion records the identity of
// the commit the operation is applying.
//
// It is git's own division and not a safegit policy: a cherry-pick applies
// SOMEBODY ELSE'S change, so their authorship travels with it and the committer
// is whoever ran the command; a revert is a NEW change of the reverter's own --
// undoing something is your decision, not the original author's -- so git's
// revert authors it as the operator, and so does this.
func (op continueOp) preservesSourceAuthor() bool { return op.kind == sequencer.KindCherryPick }

// reportsAuthor reports the two conclusions whose commit RECORDS an identity of
// its own, and which therefore carry the author member in their payload: a
// cherry-pick preserves the picked commit's author, a revert records the
// operator. A merge conclusion records neither, which is why its schema has no
// such member rather than a null one.
func (op continueOp) reportsAuthor() bool {
	return op.kind == sequencer.KindCherryPick || op.kind == sequencer.KindRevert
}

// conclusionAuthorship resolves the two author facts a conclusion needs.
//
// They are two facts because they can differ. `pinned` is the identity handed
// to the commit pipeline, non-nil only where safegit imposes one; `recorded` is
// the identity the resulting commit carries, which is what the report and the
// payload state. For a cherry-pick they are the same value; for a revert
// nothing is imposed and the recorded identity is the one git will use, asked
// of git rather than assumed; a merge has neither.
func (op continueOp) conclusionAuthorship(ctx context.Context, state sequencer.State) (pinned, recorded *git.AuthorInfo, err error) {
	if op.preservesSourceAuthor() {
		info, err := sequencer.SourceAuthor(ctx, state)
		if err != nil {
			return nil, nil, err
		}
		return &info, &info, nil
	}
	if op.kind != sequencer.KindRevert {
		return nil, nil, nil
	}
	info, err := git.ConfiguredAuthor(ctx)
	if err != nil {
		return nil, nil, err
	}
	return nil, &info, nil
}

// conclusionMovedRecords returns the move records the concluding commit
// carries, minted here rather than read off anything the caller said.
//
// It is a REVERT's alone, and it is the only place a commit's records are
// derived from another commit's: reverting a commit that declared a move undoes
// that move, so the revert declares the move back -- the same pair with its
// sides swapped, under a FRESH id, because a different commit making a
// different claim about a different step is a different record.
//
// Retractions are deliberately not inverted. A retraction says "that record was
// wrong"; reverting the commit that said so does not make the record right
// again, and resurrecting a claim nobody restated would be safegit deciding
// something for the operator. Only Moved: records get inverses.
//
// A CHERRY-PICK gets none: it re-applies somebody's change, so any record on
// the source commit describes a move this commit is repeating rather than
// undoing -- and whether the same move happened again is a fact about trees the
// operator is the one to state. A merge has no source commit at all.
func (op continueOp) conclusionMovedRecords(ctx context.Context, state sequencer.State) ([]string, error) {
	if op.kind != sequencer.KindRevert || state.Source == "" {
		return nil, nil
	}
	info, err := git.ParseCommit(ctx, state.Source)
	if err != nil {
		return nil, fmt.Errorf("reading the message of the commit being reverted (%s): %w", shortSHA(state.Source), err)
	}
	var lines []string
	for _, r := range trailer.ReadMoves(info.Message).Records {
		inverse, err := trailer.NewRecord(r.New, r.Old)
		if err != nil {
			return nil, fmt.Errorf("inverting the move record %s on %s: %w", r.ID, shortSHA(state.Source), err)
		}
		lines = append(lines, trailer.RecordLine(inverse))
	}
	return lines, nil
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
	// The shapes safegit cannot have started, refused before anything about the
	// operator's own command line is looked at: whose operation this is does not
	// depend on what they declared.
	if code := op.refuseRawGitShape(state); code != 0 {
		return code
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
	// The other raw-git shape, which needs the conflict read first: a content
	// conflict git recorded without the tree the marker verification reads.
	if code := op.refuseUnreadableConflict(ctx, sides); code != 0 {
		return code
	}
	if code := op.checkCompleteness(ctx, state, sides, declared); code != 0 {
		return code
	}
	// The resolutions name the right paths; now the content those paths carry
	// has to be free of the conflict itself. See sequencer_markers.go.
	declines, code := op.verifyMarkers(ctx, state, sides, declared)
	if code != 0 {
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

	pinned, recorded, err := op.conclusionAuthorship(ctx, state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	movedRecords, err := op.conclusionMovedRecords(ctx, state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg, RefUpdate: effectsRefUpdate{flags}}
	result, err := p.Execute(ctx, commit.CommitRequest{
		Message:      message,
		Trailers:     trailers,
		MovedRecords: movedRecords,
		DryRun:       flags.dryRun,
		ExtraParents: state.MergeHeads,
		IndexBase:    commit.IndexBaseSharedIndex,
		IndexEdits:   edits,
		Author:       pinned,
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

	out := conclusionResult{state: state, commit: result, declared: declared, declines: declines, author: recorded}

	exit := exitcode.OK
	if !flags.dryRun {
		if err := finishConclusion(ctx, gitDir, state, result, edits, sides, declared); err != nil {
			die(exitcode.General, err.Error())
		}
		out.cleared = true

		if err := maybeAutoBumpParent(ctx, flags, gitDir, result.SHA, op.command, firstLine(message)); err != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
		}

		// LAST, and after the state files are gone: the commit is made and the
		// operation is over, so whatever this does to the working tree can no
		// longer leave the repository mid-merge. A failure here is reported and
		// carried out in the exit code rather than thrown -- the commit stands
		// either way, and the operator has to be told that it does.
		exit = consumeAutostash(ctx, gitDir, state)
	}

	op.report(flags, out)
	return exit
}

// parkedConclusion describes one IMMEDIATE conclusion: the operation git has
// just computed and parked, concluded on the spot rather than left for the
// operator to conclude by hand.
//
// It is the second door into the engine above, and the one the RESTRUCTURED
// commands come in by. `safegit merge` and `safegit revert` split their work
// where git itself splits it -- a `--no-commit` compute step that stages the
// result, then a conclusion that turns the staged result into a commit -- so
// the commit they produce is the pipeline's, with safegit's trailers, the
// repository's commit-msg hook, the compare-and-swap ref update and the state
// cleanup. Nothing here is a second implementation of any of that: it is the
// same sequence runContinue runs, with the differences that are genuinely
// per-command lifted into this struct.
type parkedConclusion struct {
	// op selects the engine's per-operation vocabulary: which state kind is
	// being concluded, whose author identity is recorded, how the incoming side
	// is described.
	op continueOp
	// oplogOp names the operation in the op log, and parentBumpOp names it in
	// the "Operation:" trailer of a parent-submodule bump. They are ONE name
	// for a merge, which is the command's own; a restructured revert still
	// records its pipeline entry under the conclusion command's name while
	// naming itself `revert` to the parent bump, so the two are separate
	// fields rather than one with a caveat.
	oplogOp      string
	parentBumpOp string
	// messages replace git's own draft, exactly as the conclusion commands' -m
	// does. Empty means the draft git wrote during the compute step, which is
	// where an operator's own `-m` has already been recorded.
	messages []string
	trailers []string
	// allowEmpty carries the pipeline's tree-unchanged exemption. A merge
	// commit records its parents whether or not the tree changed; a revert that
	// changes nothing is a revert of something already absent.
	allowEmpty bool
	// onEmpty words the refusal for a conclusion whose result changes nothing.
	// It is per-command because the ways out are, and it is only ever called
	// when allowEmpty is false.
	onEmpty func() int
}

// concludeParkedOperation turns the state git just parked into a commit,
// through the engine `safegit merge-continue` and its siblings run.
//
// ok reports whether the conclusion happened; a false ok has already printed
// its own refusal and exit carries the code. A TRUE ok can still carry a
// nonzero exit: restoring an autostash can fail after a commit that stands, and
// the caller reports the conclusion either way.
func concludeParkedOperation(flags globalFlags, gitDir, sgDir string, state sequencer.State, req parkedConclusion) (out conclusionResult, exit int, ok bool) {
	ctx := flags.ctx()
	op := req.op

	if _, err := git.HeadRef(ctx); err != nil {
		return out, op.refuseDetachedHead(state), false
	}

	// A clean compute step leaves no unmerged paths, so the declaration is
	// empty and both checks pass over an empty set -- but they are RUN, not
	// skipped, because a repository can be mid-operation with foreign unmerged
	// entries from something else, and the conclusion refusing to guess is the
	// same answer here as it is behind the -continue commands.
	sides, err := conflict.Stages(ctx, "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the conflicted paths from the index: %v\n", err)
		return out, exitcode.General, false
	}
	var declared []resolution
	if code := op.checkCompleteness(ctx, state, sides, declared); code != 0 {
		return out, code, false
	}
	declines, code := op.verifyMarkers(ctx, state, sides, declared)
	if code != 0 {
		return out, code, false
	}

	message, err := op.conclusionMessage(ctx, state, req.messages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return out, exitcode.General, false
	}
	pinned, recorded, err := op.conclusionAuthorship(ctx, state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return out, exitcode.General, false
	}
	// Undoing a move is a move: a revert inverts the records on the commit it
	// undoes, from the same one place the -continue command inverts them, so a
	// revert that hit a conflict and one that did not declare the same thing.
	movedRecords, err := op.conclusionMovedRecords(ctx, state)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return out, exitcode.General, false
	}

	cfg, err := loadConfig(flags, gitDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: loading config: %v\n", err)
		return out, exitcode.General, false
	}

	p := &commit.Pipeline{SafegitDir: sgDir, Config: *cfg, RefUpdate: effectsRefUpdate{flags}}
	result, err := p.Execute(ctx, commit.CommitRequest{
		Message:      message,
		Trailers:     req.trailers,
		MovedRecords: movedRecords,
		DryRun:       flags.dryRun,
		ExtraParents: state.MergeHeads,
		IndexBase:    commit.IndexBaseSharedIndex,
		Author:       pinned,
		OplogOp:      req.oplogOp,
		AllowEmpty:   req.allowEmpty,
		Sequencer:    &coord.SequencerContext{Kind: state.Kind},
	})
	if err != nil {
		if errors.Is(err, commit.ErrTreeUnchanged) {
			return out, req.onEmpty(), false
		}
		die(pipelineExitCode(err), err.Error())
	}

	out = conclusionResult{state: state, commit: result, declared: declared, declines: declines, author: recorded}

	exit = exitcode.OK
	if !flags.dryRun {
		if err := finishConclusion(ctx, gitDir, state, result, nil, sides, declared); err != nil {
			die(exitcode.General, err.Error())
		}
		out.cleared = true

		if err := maybeAutoBumpParent(ctx, flags, gitDir, result.SHA, req.parentBumpOp, firstLine(message)); err != nil {
			die(exitcode.General, fmt.Sprintf("auto-bump parent: %v", err))
		}

		// LAST, and after the state files are gone -- the same order and the
		// same reason as runContinue's.
		exit = consumeAutostash(ctx, gitDir, state)
	}
	return out, exit, true
}

// consumeAutostash puts back the uncommitted work git set aside before this
// merge began, which is what `git merge --continue` does with MERGE_AUTOSTASH
// and what safegit's conclusion owes an operator who reached the conflict
// through `git merge --autostash` (or merge.autoStash, or `git pull
// --autostash`).
//
// The file is a POINTER TO CONTENT HELD NOWHERE ELSE, which is why it is not in
// the state-file set Cleanup removes: deleting it unapplied silently reverts the
// operator's working tree to committed content, with nothing on screen saying
// so. It is consumed here instead, and only ever after the apply has put the
// work somewhere it can be reached from.
//
// The failure path is git's own: an apply that conflicts leaves the conflict in
// the working tree, and the stash commit is STORED on refs/stash so it has a
// name (`stash@{0}`) once the file goes. safegit differs from git in one thing
// only -- git returns success there and safegit exits nonzero, because a
// conclusion that could not restore the operator's work is not a clean outcome.
//
// Everything it prints goes to stderr, unconditionally: this is the same class
// of fact as undo's "the merge state is NOT restored" note, --quiet is a request
// for less chatter rather than for the whereabouts of one's own work to be
// withheld, and in machine mode stdout belongs to the envelope.
func consumeAutostash(ctx context.Context, gitDir string, state sequencer.State) int {
	if state.Kind != sequencer.KindMerge || state.Autostash == "" {
		return exitcode.OK
	}
	path := filepath.Join(gitDir, sequencer.FileMergeAutostash)

	gitSaid, applyErr := git.StashApply(ctx, state.Autostash)
	if applyErr == nil {
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			fmt.Fprintf(os.Stderr, "error: the autostash was applied but %s could not be removed: %v\n", path, rmErr)
			fmt.Fprintf(os.Stderr, "  remove it by hand; leaving it there would make the next conclusion apply the same work twice\n")
			return exitcode.General
		}
		// The apply is itself a merge, so it leaves the same residue any merge
		// leaves -- AUTO_MERGE, and MERGE_RR where rerere is on. The conclusion
		// ran it, so the conclusion owns what it left: the removal set is the
		// merge set, from the one place that declares it, and it is idempotent
		// over the members that are already gone. It is done only on a SUCCESSFUL
		// apply; after a conflicting one that residue belongs to the conflict now
		// sitting in the working tree.
		if err := sequencer.Cleanup(gitDir, sequencer.KindMerge); err != nil {
			fmt.Fprintf(os.Stderr, "error: the autostash was applied but its own leftover state could not be removed: %v\n", err)
			return exitcode.General
		}
		fmt.Fprintf(os.Stderr, "Applied autostash.\n")
		return exitcode.OK
	}

	fmt.Fprintf(os.Stderr, "error: applying the autostash resulted in conflicts; the merge commit was created and stands\n")
	// git's own report of what it could not merge, verbatim and indented under
	// the line above. Where git printed nothing at all, the wrapped error is the
	// only account of the failure there is.
	if gitSaid == "" {
		gitSaid = applyErr.Error()
	}
	for _, line := range strings.Split(strings.TrimRight(gitSaid, "\n"), "\n") {
		fmt.Fprintf(os.Stderr, "  %s\n", line)
	}

	if storeErr := git.StashStore(ctx, state.Autostash, "autostash"); storeErr != nil {
		// Nothing was stored, so the file is the only name the work has left and
		// it stays exactly where it is.
		fmt.Fprintf(os.Stderr, "  it could not be stored as a stash entry either: %v\n", storeErr)
		fmt.Fprintf(os.Stderr, "  your changes are the commit %s, still recorded in %s. Recover them with:\n", state.Autostash, path)
		fmt.Fprintf(os.Stderr, "    git stash apply %s\n", state.Autostash)
		return exitcode.General
	}
	if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
		fmt.Fprintf(os.Stderr, "  your changes are safe in the stash, but %s could not be removed: %v\n", path, rmErr)
		fmt.Fprintf(os.Stderr, "  remove it by hand; the work is recorded twice until you do\n")
		return exitcode.General
	}
	fmt.Fprintf(os.Stderr, "  your changes are safe in the stash, as stash@{0} (commit %s).\n", state.Autostash)
	fmt.Fprintf(os.Stderr, "  run 'git stash pop' or 'git stash drop' at any time\n")
	return exitcode.General
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

// refuseRawGitShape refuses the states safegit cannot have STARTED, and
// therefore does not conclude: a sequencer queue, and an octopus merge.
//
// Both are shapes only raw git can create now -- safegit's cherry-pick and
// revert apply one commit and its merge takes one branch -- and both are
// refused rather than attempted, because concluding either would do something
// the operator did not ask for:
//
//   - a QUEUE holds git's remaining commands, and the queue directory is part of
//     the state a conclusion REMOVES, so finishing the current step natively
//     would throw the rest of the sequence away;
//   - an OCTOPUS has more sides than every check safegit makes over a merge is
//     written against, and its index stages describe only the last pairwise
//     step, so the completeness and marker checks would be a verdict about part
//     of the merge presented as a verdict about all of it.
//
// The way out comes from the single way-out authority, which names git's own
// conclusion for exactly these states, so the refusal and every other message
// about them agree.
//
// DIVERGENCE: git concludes a queue and an octopus with its own `--continue`;
// safegit refuses both and says so. Two rows for docs/divergences.md.
func (op continueOp) refuseRawGitShape(state sequencer.State) int {
	var what, why string
	switch {
	case state.Queued:
		what = "a " + state.Kind.String() + " sequence"
		why = "git's sequencer holds a QUEUE of commands here, and safegit did not start it: safegit's " +
			state.Kind.String() + " applies one commit and authors the result itself.\n" +
			"  Concluding one step of a queue would throw the rest of it away, because the queue is part of\n" +
			"  the state a conclusion removes."
	case op.kind == sequencer.KindMerge && len(state.MergeHeads) > 1:
		what = "an octopus merge"
		why = "safegit's merge brings in ONE branch, so an octopus is a merge only raw git can start.\n" +
			"  Every check safegit makes over a merge is written against two sides, and an octopus's index\n" +
			"  stages describe only its last pairwise step: a verdict over them would be a verdict about\n" +
			"  part of the merge, reported as one about all of it."
	default:
		return 0
	}

	fmt.Fprintf(os.Stderr, "error: safegit %s does not conclude %s\n", op.command, what)
	fmt.Fprintf(os.Stderr, "  %s\n", why)
	fmt.Fprintf(os.Stderr, "  Finish what git started, with git:\n")
	renderWayOut(coord.WayOutOf(state))
	fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
	return exitcode.CoordinationBusy
}

// refuseUnreadableConflict refuses a merge whose CONTENT CONFLICT git recorded
// without an AUTO_MERGE tree.
//
// On the git version safegit requires, the default merge strategy always writes
// AUTO_MERGE beside a conflict: it is the tree holding what git put in the
// working tree, and it is what the marker verification reads to tell a conflict
// block GIT wrote from one that was already in the file before the merge began.
// A content conflict without it was computed by another strategy -- one safegit
// does not select and whose staged result it cannot check -- so the conclusion
// refuses instead of committing content nothing verified.
//
// It is scoped to CONTENT conflicts (both sides present) because those are the
// only ones the marker check reads AUTO_MERGE for; an add/add or modify/delete
// conflict carries no merged text to compare against.
//
// DIVERGENCE: git concludes a conflict computed by any strategy; safegit
// concludes only the ones the default strategy recorded. One row for
// docs/divergences.md.
func (op continueOp) refuseUnreadableConflict(ctx context.Context, sides map[string]conflict.Sides) int {
	if op.kind != sequencer.KindMerge {
		return 0
	}
	var contentConflicts []string
	for path, s := range sides {
		if s.ContentConflict() {
			contentConflicts = append(contentConflicts, path)
		}
	}
	if len(contentConflicts) == 0 {
		return 0
	}
	if _, present, err := conflict.AutoMergeTree(ctx); err != nil || present {
		return 0
	}
	sort.Strings(contentConflicts)

	fmt.Fprintf(os.Stderr, "error: safegit %s cannot conclude this merge: git recorded no AUTO_MERGE for it\n", op.command)
	fmt.Fprintf(os.Stderr, "  %s carries a content conflict, and on the git version safegit requires the default merge\n", contentConflicts[0])
	fmt.Fprintf(os.Stderr, "  strategy always records AUTO_MERGE beside one -- the tree safegit reads to tell a conflict\n")
	fmt.Fprintf(os.Stderr, "  block git wrote from one that was already in the file. A conflict without it was computed\n")
	fmt.Fprintf(os.Stderr, "  by another strategy, which safegit's merge does not select and cannot check the result of.\n")
	fmt.Fprintf(os.Stderr, "  Finish what git started, with git:\n")
	// git's commands, named here rather than read from the way-out authority.
	// That authority answers from the STATE FILES, and the fact this refusal
	// turns on is not among them: AUTO_MERGE is a ref, so telling its presence
	// apart from its absence takes a git call the filesystem-only state reader
	// deliberately does not make. Every other message about this repository
	// still names safegit's own conclusion, which is what an operator should
	// reach for -- and reaching for it produces exactly this refusal.
	fmt.Fprintf(os.Stderr, "    conclude it:  git merge --continue\n")
	fmt.Fprintf(os.Stderr, "    abandon it:   git merge --abort\n")
	fmt.Fprintf(os.Stderr, "  safegit implements a deliberate subset of git; see docs/divergences.md.\n")
	return exitcode.CoordinationBusy
}

// renderWayOut prints the two commands that end a state, from the single
// way-out authority.
func renderWayOut(w coord.WayOut) {
	if w.Conclude != "" {
		fmt.Fprintf(os.Stderr, "    conclude it:  %s\n", w.Conclude)
	}
	if w.Abandon != "" {
		fmt.Fprintf(os.Stderr, "    abandon it:   %s\n", w.Abandon)
	}
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
