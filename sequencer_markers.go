package main

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/sequencer"
)

// Marker verification: the check that stands between a declared resolution and
// the commit it produces, so a conclusion cannot record the conflict it was
// asked to resolve.
//
// It is LAYERED, and the two layers do different jobs:
//
//	STRUCTURAL (the verdict) -- does the content hold a complete conflict block
//	that no side of this conflict, and no parent of this commit, already had?
//	The DIFFERENTIAL against the stage 1/2/3 blobs, or against the parents'
//	blobs, is what makes the question safe to ask: a repository whose real
//	content carries marker-shaped lines -- documentation about conflicts, a
//	stored fixture -- stays committable, because a block a side already had is
//	attributed to that side instead of reported. What is left is a block that
//	came into being with this conflict, which is a refusal.
//
//	REGION SURVIVAL (the attribution) -- is that block one git ITSELF emitted
//	here? The emitted blocks are read from AUTO_MERGE, the tree git wrote them
//	into, or reproduced from the index stages with git's own merge-file where
//	the marker labels are recoverable. Knowing it makes the message exact
//	("the conflict git wrote here is still in the content being committed")
//	rather than merely structural.
//
// The layers are ordered this way because the differential is what makes EITHER
// of them correct. AUTO_MERGE holds the whole file git wrote, so it contains
// every marker-shaped block that was already in the file as ordinary content,
// alongside the ones git created; only subtracting what a side already carried
// separates the two. That is also why an absent AUTO_MERGE never weakens the
// verdict -- a block nobody had before is refused whether or not safegit can
// name who wrote it -- and why there is no separate refusal for it.
//
// There is NO escape flag. The one way past a rejection is a declaration in
// .gitattributes that predates the conflict (see markerExemptionAttr), and
// every rejection prints the exact line that would grant it.
//
// Scope per conflict kind falls out of the layers rather than being enumerated.
// A delete/modify conflict, a binary conflict and a path a custom merge driver
// resolved have nothing marked up to survive, so the region layer finds no
// emitted blocks for them and the structural layer carries the whole check.
// An add/add conflict is NOT in that group, whatever its name suggests: both
// sides are present, git marks it up exactly like any other content conflict
// and records it in AUTO_MERGE. An octopus merge is out of scope for a
// different reason: git aborts a conflicted octopus rather than parking one, so
// the only octopus a conclusion ever sees is clean.
//
// Content named by `--resolve path=ours|theirs` passes by construction rather
// than by exception: it IS one of the stage blobs the differential measures
// against, so every block in it is attributed to the side that committed it.
// The check still runs over it, because a rule with no special cases cannot
// have a special case that is wrong.

// markerExemptionAttr is the attribute a repository declares to say that a
// path's real content may hold marker-shaped lines.
//
// The declaration is git's own opt-out spelling -- `<path>
// -safegit-conflict-markers`, exactly like `-text` or `-merge` -- and it is
// read from the FIRST PARENT's tree. An exemption has to predate the conflict:
// an uncommitted .gitattributes edit made while the operation is in flight
// exempts nothing, and the working tree's .gitattributes may itself be
// conflicted at the moment the question is asked.
const markerExemptionAttr = "safegit-conflict-markers"

// markerViolation is one refusal-worthy block found in one path.
type markerViolation struct {
	path string
	line int
	// why is the sentence printed next to the location.
	why string
}

// verifyMarkers is the whole check, run after completeness and before the
// commit pipeline. It returns 0 when the conclusion may proceed.
func (op continueOp) verifyMarkers(ctx context.Context, state sequencer.State, sides map[string]conflict.Sides, declared []resolution) int {
	choices := make(map[string]resolutionChoice, len(declared))
	for _, r := range declared {
		choices[r.Path] = r.Choice
	}

	paths, err := verifiablePaths(ctx, sides, choices)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if len(paths) == 0 {
		return 0
	}

	// The first parent is the branch tip this conclusion commits onto, and it is
	// the tree every attribute question is answered from.
	const firstParent = "HEAD"
	attrs, err := conflict.Resolve(ctx, firstParent, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading the conflict attributes from the first parent's tree: %v\n", err)
		return exitcode.General
	}
	exempt, err := exemptPaths(ctx, firstParent, paths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}

	v := &markerCheck{op: op, state: state, sides: sides, choices: choices, attrs: attrs, parents: append([]string{firstParent}, state.MergeHeads...)}
	if err := v.readAutoMerge(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "error: reading what git recorded for this conflict: %v\n", err)
		return exitcode.General
	}

	var violations []markerViolation
	for _, path := range paths {
		if exempt[path] {
			continue
		}
		found, err := v.check(ctx, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: checking %s for conflict markers: %v\n", path, err)
			return exitcode.General
		}
		violations = append(violations, found...)
	}

	if len(violations) > 0 {
		return op.refuseSurvivingMarkers(violations)
	}
	return 0
}

// verifiablePaths is every path this conclusion will RECORD: the conflicted
// ones, plus everything else whose index entry differs from the first parent.
//
// The second half is not padding. An operator who resolved a conflict the way
// git's own documentation says to -- edit the file, `git add` it -- leaves a
// path with no stages, no `--resolve` entry, and the markers still in it. It is
// in the index, it is in the commit, and only this listing reaches it.
//
// A path resolved to `delete` is left out: it has no content in the commit.
func verifiablePaths(ctx context.Context, sides map[string]conflict.Sides, choices map[string]resolutionChoice) ([]string, error) {
	seen := make(map[string]bool, len(sides))
	for path := range sides {
		seen[path] = true
	}
	changed, err := git.IndexPathsChangedFrom(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	for _, path := range changed {
		seen[path] = true
	}

	paths := make([]string, 0, len(seen))
	for path := range seen {
		if choices[path] == resolveDelete {
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

// exemptPaths answers which paths carry the declared exemption, from the first
// parent's tree.
func exemptPaths(ctx context.Context, attrSource string, paths []string) (map[string]bool, error) {
	answers, err := git.CheckAttr(ctx, attrSource, []string{markerExemptionAttr}, paths)
	if err != nil {
		return nil, fmt.Errorf("reading the %s attribute from %s: %w", markerExemptionAttr, attrSource, err)
	}
	exempt := make(map[string]bool, len(paths))
	for _, path := range paths {
		// git spells an explicitly unset attribute "unset", which is the whole
		// vocabulary of the declaration: unspecified means the path is checked,
		// and so does any value, so a typo can never silently exempt anything.
		exempt[path] = answers[path][markerExemptionAttr] == "unset"
	}
	return exempt, nil
}

// markerCheck carries the per-conclusion facts the per-path check needs.
type markerCheck struct {
	op      continueOp
	state   sequencer.State
	sides   map[string]conflict.Sides
	choices map[string]resolutionChoice
	attrs   map[string]conflict.Attrs
	// parents are the commit's parents, in order, which is what "a parent
	// already had this block" is measured against for a path with no stages.
	parents []string
	// autoMergePresent records whether git wrote an AUTO_MERGE tree for this
	// operation at all, asked once rather than per path.
	autoMergePresent bool
}

func (v *markerCheck) readAutoMerge(ctx context.Context) error {
	_, present, err := conflict.AutoMergeTree(ctx)
	if err != nil {
		return err
	}
	v.autoMergePresent = present
	return nil
}

// check runs the verification over one path.
//
// The two early exits are not optimization details. A content holding NO
// complete block holds no surviving conflict, and a content whose every block
// a side already carried holds none either -- in both cases the path is clean
// under the whole check, and the more expensive questions (what did git emit
// here, what do the other parents hold) are never asked.
func (v *markerCheck) check(ctx context.Context, path string) ([]markerViolation, error) {
	content, present, err := v.committedContent(ctx, path)
	if err != nil || !present {
		return nil, err
	}
	size := v.attrs[path].MarkerSize
	regions := conflict.Regions(content, size)
	if len(regions) == 0 {
		return nil, nil
	}

	bases, err := v.baseBlobs(ctx, path)
	if err != nil {
		return nil, err
	}
	newBlocks := unattributed(regions, bases, size)
	if len(newBlocks) == 0 {
		return nil, nil
	}

	// Every one of these is a refusal. Asking what git emitted only decides
	// which sentence each gets, so the emitted set is read here, once the
	// verdict is already known.
	emitted, err := v.emittedBlocks(ctx, path, bases, size)
	if err != nil {
		return nil, err
	}
	violations := make([]markerViolation, 0, len(newBlocks))
	for _, r := range newBlocks {
		why := "a complete conflict block that no side of this conflict already carried"
		if emitted[string(r.Bytes)] {
			why = "the conflict git wrote here is still in the content being committed"
		}
		violations = append(violations, markerViolation{path: path, line: r.StartLine, why: why})
	}
	return violations, nil
}

// unattributed keeps the blocks no base content already carried.
func unattributed(regions []conflict.Region, bases [][]byte, size int) []conflict.Region {
	var out []conflict.Region
	for _, r := range regions {
		if !regionInAny(bases, r, size) {
			out = append(out, r)
		}
	}
	return out
}

// committedContent reads the bytes this path will be committed with, and says
// where they came from.
//
// A content that cannot be read is reported absent rather than as an error: a
// `worktree` resolution naming a file that is not there is refused moments
// later by the staging that has to hash it, with a message about the real
// problem, and nothing is committed unverified either way.
func (v *markerCheck) committedContent(ctx context.Context, path string) (content []byte, present bool, err error) {
	switch v.choices[path] {
	case resolveOurs:
		blob, err := stageBlob(ctx, v.sides[path].Ours)
		return blob, blob != nil, err
	case resolveTheirs:
		blob, err := stageBlob(ctx, v.sides[path].Theirs)
		return blob, blob != nil, err
	case resolveWorktree:
		// AnchorRoot, not the process working directory: a path git listed is
		// relative to the repository, and reading it with a syscall from a
		// subdirectory would reach a different file or none at all.
		root, err := git.AnchorRoot(ctx)
		if err != nil {
			return nil, false, err
		}
		data, err := os.ReadFile(git.Anchor(root, path))
		if err != nil {
			return nil, false, nil
		}
		return data, true, nil
	}

	// No resolution names it, so what gets committed is what the index holds:
	// a path git merged cleanly, or one the operator staged themselves with
	// `git add` after editing it, which is where a forgotten marker hides in
	// plain sight -- that path is no longer unmerged, so nothing else in the
	// conclusion has anything to say about it.
	sha, err := git.RevParse(ctx, ":0:"+path)
	if err != nil {
		return nil, false, nil
	}
	blob, err := git.CatFileBlob(ctx, sha)
	if err != nil {
		return nil, false, err
	}
	return blob, true, nil
}

// stageBlob reads one stage's content; an absent stage means the side deleted
// the path, so there is no content to check.
func stageBlob(ctx context.Context, e *git.UnmergedEntry) ([]byte, error) {
	if e == nil || e.Mode == gitlinkMode {
		return nil, nil
	}
	return git.CatFileBlob(ctx, e.SHA)
}

// emittedBlocks returns, as a set of verbatim block bytes, the conflict blocks
// git CREATED for a path when it wrote the file.
//
// The subtraction is the whole point. AUTO_MERGE holds the entire file git
// wrote, so its blocks include every marker-shaped block the file already had
// as ordinary content; those are not git's work and a conclusion that keeps
// them is keeping the file's own text. What git created is what is left after
// the same differential the verdict uses.
//
// The recorded answer comes first, because it is not a reproduction of
// anything: AUTO_MERGE holds the exact bytes git put in the working tree. Where
// git recorded none, git's own merge-file over the index stages produces the
// same bytes -- which is what
// TestReconstructionIsByteIdenticalAcrossTheConfigMatrix exists to prove -- and
// it is available exactly when the marker LABELS can be derived, which is a
// property of the operation rather than of a failure:
//
//   - a cherry-pick and a revert label their markers from the commit being
//     applied, which their state file names;
//   - a MERGE labels its incoming side with the name the operator typed, which
//     git records nowhere machine-readable, so no reproduction of a merge's
//     conflicted file can be byte-identical.
//
// An empty answer is therefore not a weaker check but a less specific message:
// the blocks it would have named are refused anyway for being blocks no side
// carried.
func (v *markerCheck) emittedBlocks(ctx context.Context, path string, bases [][]byte, size int) (map[string]bool, error) {
	content, ok, err := v.emittedContent(ctx, path)
	if err != nil || !ok {
		return nil, err
	}
	blocks := make(map[string]bool)
	for _, r := range unattributed(conflict.Regions(content, size), bases, size) {
		blocks[string(r.Bytes)] = true
	}
	return blocks, nil
}

// emittedContent is the conflict-marked file git wrote for a path: what it
// recorded, or what its own merge engine reproduces from the stages.
func (v *markerCheck) emittedContent(ctx context.Context, path string) ([]byte, bool, error) {
	if v.autoMergePresent {
		blob, ok, err := conflict.AutoMergeBlob(ctx, path)
		if err != nil {
			return nil, false, err
		}
		if ok {
			return blob, true, nil
		}
	}

	s, conflicted := v.sides[path]
	if !conflicted || !s.ContentConflict() {
		// Nothing git could have marked up: a clean path, a delete/modify
		// conflict, one side of an add/delete.
		return nil, false, nil
	}

	labels, derivable, err := v.op.reconstructionLabels(ctx, v.state)
	if err != nil || !derivable {
		return nil, false, err
	}
	content, _, err := conflict.Reconstruct(ctx, s, v.attrs[path], labels)
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

// reconstructionLabels derives the marker labels for this operation, and says
// when they cannot be derived at all.
func (op continueOp) reconstructionLabels(ctx context.Context, state sequencer.State) (labels conflict.Labels, derivable bool, err error) {
	switch op.kind {
	case sequencer.KindCherryPick:
		l, err := conflict.PickLabels(ctx, state.Source)
		return l, err == nil, err
	case sequencer.KindRevert:
		l, err := conflict.RevertLabels(ctx, state.Source)
		return l, err == nil, err
	}
	// A merge's incoming label is the name the operator typed, and it is gone.
	return conflict.Labels{}, false, nil
}

// baseBlobs are the contents a block may legitimately have come from: the
// conflict's own three stages where the path is unmerged, and otherwise every
// parent of the commit being made.
//
// Every parent, not only the first: a merge records the incoming side as a
// parent too, and a file that arrived wholesale from it carries whatever that
// side committed. Measuring against the first parent alone would refuse a merge
// for bringing in a path whose committed content contains marker-shaped lines,
// which is the very thing the differential exists to permit.
func (v *markerCheck) baseBlobs(ctx context.Context, path string) ([][]byte, error) {
	if s, conflicted := v.sides[path]; conflicted {
		var blobs [][]byte
		for _, e := range []*git.UnmergedEntry{s.Base, s.Ours, s.Theirs} {
			blob, err := stageBlob(ctx, e)
			if err != nil {
				return nil, err
			}
			if blob != nil {
				blobs = append(blobs, blob)
			}
		}
		return blobs, nil
	}

	var blobs [][]byte
	for _, parent := range v.parents {
		sha, err := git.RevParse(ctx, parent+":"+path)
		if err != nil {
			// The path does not exist on that parent, which is an answer.
			continue
		}
		blob, err := git.CatFileBlob(ctx, sha)
		if err != nil {
			return nil, err
		}
		blobs = append(blobs, blob)
	}
	return blobs, nil
}

// regionInAny reports whether any of the base contents already carried this
// exact block. The comparison is byte-exact on purpose: a block that was edited
// is not the block that was there.
func regionInAny(bases [][]byte, r conflict.Region, size int) bool {
	for _, base := range bases {
		if _, found := conflict.ContainsRegion(base, r.Bytes); found {
			return true
		}
		// A block at the very end of a file has no trailing newline there while
		// the content being committed may have gained one, so the same block is
		// also looked for as the base's own parsed regions.
		for _, b := range conflict.Regions(base, size) {
			if string(b.Bytes) == string(r.Bytes) {
				return true
			}
		}
	}
	return false
}

// refuseSurvivingMarkers is the rejection: every location, then both ways out.
func (op continueOp) refuseSurvivingMarkers(violations []markerViolation) int {
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].path != violations[j].path {
			return violations[i].path < violations[j].path
		}
		return violations[i].line < violations[j].line
	})

	fmt.Fprintf(os.Stderr, "error: %d conflict marker block(s) survive in what this %s would commit:\n", len(violations), op.kind)
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s:%d  %s\n", v.path, v.line, v.why)
	}
	fmt.Fprintf(os.Stderr, "  nothing was committed and the %s is still in progress. Edit the file(s) and re-run,\n", op.kind)
	fmt.Fprintf(os.Stderr, "  or take one side whole, which cannot carry a marker:\n")
	fmt.Fprintf(os.Stderr, "    safegit %s --resolve '%s=ours'   (or =theirs)\n", op.command, violations[0].path)
	printMarkerExemption(violations[0].path)
	return exitcode.ConclusionMarkerSurvived
}

// printMarkerExemption prints the one-line declaration that would exempt a
// path, which every rejection owes the operator.
func printMarkerExemption(path string) {
	fmt.Fprintf(os.Stderr, "  or, if this path's real content contains marker-shaped lines, declare it in .gitattributes:\n")
	fmt.Fprintf(os.Stderr, "    %s -%s\n", path, markerExemptionAttr)
	fmt.Fprintf(os.Stderr, "    (read from the first parent's tree, so the declaration must be COMMITTED before the conflict)\n")
}
