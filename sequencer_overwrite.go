package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
)

// The overwrite check: what a conclusion is allowed to destroy in the working
// tree.
//
// A stage resolution WRITES the chosen blob over the file on disk, which is
// git's own `checkout --ours` idiom, and `delete` removes it, which is `git rm`.
// Both are the right behavior for a file that still holds what git put there --
// and both silently destroy an operator's own hand edit, whose content is in no
// commit, no stage and no stash, and is therefore gone for good.
//
// So the conclusion asks first, per path, whether the file on disk is content
// this operation can account for. The ACCEPTED SET is:
//
//   - the conflict's three index stages (base, ours, theirs). One of them is
//     what an operator picked with `git checkout --ours` or `--theirs`, and one
//     of them is what git left on disk for every conflict kind that has no
//     marked-up emission at all -- a delete/modify conflict, a binary conflict,
//     a path a merge driver resolved. The check is COMPLETE for those kinds by
//     nature rather than by exemption.
//   - the blob git itself wrote into the working tree, read VERBATIM out of
//     AUTO_MERGE (`AUTO_MERGE:<path>`). It is used directly and never
//     reconstructed: reconstruction cannot reproduce the marker labels git
//     emits for a rename-mediated conflict, whose labels carry the path, so a
//     reconstructed comparison would call git's own untouched output a hand
//     edit.
//
// The check is TOTAL over everything safegit concludes -- no skip arm, and no
// "the check could not be made" fallback. The one shape that would need one, a
// content conflict git recorded no AUTO_MERGE for, never reaches here: safegit's
// merge cannot start one (it selects no strategy) and a conclusion refuses one
// found in the repository (refuseUnreadableConflict).
//
// `--discard-unmatched-worktree` elects the destruction. It is a consent flag
// rather than an escape hatch because no other route expresses "yes, throw my
// hand edits away": `worktree` keeps them, and every other keyword now refuses.
//
// The comparison is BYTE-EXACT against the blob, with no clean filter applied.
// In a repository that normalizes line endings or runs a clean filter over a
// path, git's own checkout of a stage can therefore differ from the blob it
// came from, and the file reads as a hand edit -- a refusal that names the file
// and the way past it, rather than a destroyed edit. That is the direction this
// check is meant to err in, and hashing the file through the filter chain would
// be a second, filter-shaped answer to a question about content.

// overwriteVictim is one path whose materialization would destroy content on
// disk that no side of the conflict accounts for.
type overwriteVictim struct {
	path string
	// choice is the resolution that would do it, so the refusal can say which
	// declaration to change.
	choice resolutionChoice
	// deletes reports that the effect is a removal rather than an overwrite:
	// `delete`, or a stage the conflict does not have.
	deletes bool
}

// refuseWorktreeOverwrite is the per-path verdict loop, run over the whole
// declared set after the completeness and marker checks and before anything is
// committed.
//
// It is deliberately NOT part of the marker verification, which asks a
// different question over a different set: that pass excludes a path resolved to
// `delete` (it contributes no content to the commit) and honors the committed
// marker exemption (a declaration about a file's own text). Neither has anything
// to do with what is about to be written over, so this pass shares nothing with
// it but the stage blobs.
func (op continueOp) refuseWorktreeOverwrite(ctx context.Context, sides map[string]conflict.Sides, declared []resolution, discard bool) int {
	victims, err := unmatchedWorktreeFiles(ctx, sides, declared)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return exitcode.General
	}
	if len(victims) == 0 {
		return 0
	}
	if discard {
		// Elected, and said out loud: the flag answers the question, it does not
		// make the question go away.
		for _, v := range victims {
			verb := "overwritten"
			if v.deletes {
				verb = "deleted"
			}
			fmt.Fprintf(os.Stderr, "note: %s is %s as --discard-unmatched-worktree elects; its content matched no side of the conflict\n", v.path, verb)
		}
		return 0
	}

	fmt.Fprintf(os.Stderr, "error: %d working-tree file(s) hold content no side of this %s accounts for:\n", len(victims), op.kind)
	for _, v := range victims {
		effect := "overwrite it with the stage's content"
		if v.deletes {
			effect = "delete it"
		}
		fmt.Fprintf(os.Stderr, "  %s  (--resolve '%s=%s' would %s)\n", v.path, v.path, v.choice, effect)
	}
	fmt.Fprintf(os.Stderr, "  The file matches neither an index stage nor what git wrote there, so it is a hand edit:\n")
	fmt.Fprintf(os.Stderr, "  its content is in no commit, no stage and no stash, and nothing could bring it back.\n")
	fmt.Fprintf(os.Stderr, "  Nothing was committed and the %s is still in progress. Keep the edit:\n", op.kind)
	fmt.Fprintf(os.Stderr, "    safegit %s --resolve '%s=worktree'\n", op.command, victims[0].path)
	fmt.Fprintf(os.Stderr, "  or, to throw that content away deliberately:\n")
	fmt.Fprintf(os.Stderr, "    safegit %s ... --discard-unmatched-worktree\n", op.command)
	return exitcode.ConclusionWouldOverwrite
}

// unmatchedWorktreeFiles is the verdict itself, separated from the wording.
func unmatchedWorktreeFiles(ctx context.Context, sides map[string]conflict.Sides, declared []resolution) ([]overwriteVictim, error) {
	var destructive []resolution
	for _, r := range declared {
		if r.Choice != resolveWorktree {
			destructive = append(destructive, r)
		}
	}
	if len(destructive) == 0 {
		return nil, nil
	}

	root, err := git.AnchorRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving the directory git's paths are relative to, to read what is on disk: %w", err)
	}

	var victims []overwriteVictim
	for _, r := range destructive {
		content, present, err := worktreeContent(git.Anchor(root, r.Path))
		if err != nil {
			return nil, fmt.Errorf("reading %s to see what would be destroyed: %w", r.Path, err)
		}
		if !present {
			// Nothing on disk to destroy: a path the operator already removed, or
			// a submodule's own checkout, which a conclusion never writes.
			continue
		}
		accepted, err := acceptedWorktreeContents(ctx, r.Path, sides[r.Path])
		if err != nil {
			return nil, err
		}
		matched := false
		for _, a := range accepted {
			if bytes.Equal(a, content) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		victims = append(victims, overwriteVictim{
			path:    r.Path,
			choice:  r.Choice,
			deletes: resolutionDeletes(r.Choice, sides[r.Path]),
		})
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].path < victims[j].path })
	return victims, nil
}

// resolutionDeletes reports whether materializing this resolution removes the
// file rather than writing one: `delete` always does, and a stage keyword does
// when the conflict has no such stage, which is the side that deleted the path.
func resolutionDeletes(choice resolutionChoice, s conflict.Sides) bool {
	switch choice {
	case resolveDelete:
		return true
	case resolveOurs:
		return s.Ours == nil
	case resolveTheirs:
		return s.Theirs == nil
	}
	return false
}

// worktreeContent reads what is on disk for one absolute path, the way the
// conclusion's own write would replace it.
//
// A SYMLINK is read as its target text, because that is what the index holds
// for one and what writeStageToWorktree writes back; following it would compare
// the wrong file entirely. A DIRECTORY is not content this repository writes --
// a submodule's checkout is the only way one is here -- and reports absent.
func worktreeContent(abs string) (content []byte, present bool, err error) {
	info, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(abs)
		if err != nil {
			return nil, false, err
		}
		return []byte(target), true, nil
	case info.IsDir():
		return nil, false, nil
	case !info.Mode().IsRegular():
		// A device node or socket is not something a stage blob describes, and
		// overwriting it is not a content loss this check can reason about.
		return nil, false, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// acceptedWorktreeContents is the set a file on disk may hold without the
// conclusion destroying anything: the conflict's stage blobs, plus the blob git
// itself recorded for the path in AUTO_MERGE.
func acceptedWorktreeContents(ctx context.Context, path string, s conflict.Sides) ([][]byte, error) {
	var accepted [][]byte
	for _, e := range []*git.UnmergedEntry{s.Base, s.Ours, s.Theirs} {
		blob, err := stageBlob(ctx, e)
		if err != nil {
			return nil, fmt.Errorf("reading the index stages of %s: %w", path, err)
		}
		if blob != nil {
			accepted = append(accepted, blob)
		}
	}

	emitted, present, err := conflict.AutoMergeBlob(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("reading what git recorded in AUTO_MERGE for %s: %w", path, err)
	}
	if present {
		accepted = append(accepted, emitted)
	}
	return accepted, nil
}
