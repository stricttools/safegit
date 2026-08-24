package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/smm-h/safegit/internal/commit"
	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
)

// The crash re-run: finishing a conclusion whose commit already stands.
//
// A conclusion moves the ref first and cleans up afterwards, so a process
// killed between the two leaves a commit on the branch and a repository git
// still calls mid-operation. The re-run's job is the cleanup alone -- and the
// question it has to answer is what the index and the working tree should end
// up holding for the paths that were conflicted.
//
// THE COMMIT ANSWERS IT. The killed run resolved those paths and committed the
// result, so the resolutions are not lost with the process: they are the
// commit's own tree. The re-run therefore reads them off it -- stage 0 for
// every path the commit carries, a removal for one it does not -- rather than
// off the command line, which is why a re-run that declares nothing finishes
// the cleanup completely instead of leaving the conflict's stages in the index
// under a message claiming everything is in step.
//
// What the operator declares is still READ, and it is checked rather than
// obeyed: a declaration naming a side the commit does not hold is a statement
// about content that was decided before this run started, and it is refused
// naming the commit that stands. Silently ignoring it would let an operator
// believe they had changed the outcome.

// committedSide is what the standing commit holds for one previously-conflicted
// path.
//
// An ABSENT path is not an error: a conclusion that resolved a path to `delete`
// (or to a side that deleted it) committed exactly that, and the cleanup owes
// the same removal to the index and to the working tree.
type committedSide struct {
	path string
	// present reports that the commit carries the path at all.
	present bool
	// mode and sha describe the committed entry, read for a present path only.
	mode string
	sha  string
	// content is the committed blob's bytes, used to compare against what is on
	// disk. It is nil for an absent path and for a gitlink, whose object is a
	// commit rather than a blob and whose working-tree state is the submodule's
	// own checkout.
	content []byte
}

// entry renders the committed side as the index vocabulary the shared
// materialization primitives take.
func (c committedSide) entry() *git.UnmergedEntry {
	if !c.present {
		return nil
	}
	return &git.UnmergedEntry{Mode: c.mode, SHA: c.sha, Path: c.path}
}

// committedSides reads, for every path the conflict holds stages for, what the
// commit that already stands carries at that path.
//
// The paths are sorted so that every message, every index edit and every
// working-tree write this drives happens in one stable order.
func committedSides(ctx context.Context, commitSHA string, sides map[string]conflict.Sides) ([]committedSide, error) {
	paths := make([]string, 0, len(sides))
	for path := range sides {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	entries, err := git.LsTreePathsRecursive(ctx, commitSHA, paths)
	if err != nil {
		return nil, fmt.Errorf("reading what the commit %s that already concluded this operation holds for the conflicted paths: %w",
			shortSHA(commitSHA), err)
	}
	byPath := make(map[string]git.TreeEntry, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e
	}

	out := make([]committedSide, 0, len(paths))
	for _, path := range paths {
		e, present := byPath[path]
		side := committedSide{path: path, present: present}
		if present {
			side.mode, side.sha = e.Mode, e.SHA
			if e.Mode != gitlinkMode {
				content, err := git.CatFileBlob(ctx, e.SHA)
				if err != nil {
					return nil, fmt.Errorf("reading what commit %s holds for %s: %w", shortSHA(commitSHA), path, err)
				}
				side.content = content
			}
		}
		out = append(out, side)
	}
	return out, nil
}

// crashIndexEdits turns the committed sides into the edits the shared index
// needs: the committed blob at stage 0, or the path removed outright, which is
// what turns three unmerged stages into an entry the reconcile can fold away.
func crashIndexEdits(committed []committedSide) []commit.IndexEdit {
	edits := make([]commit.IndexEdit, 0, len(committed))
	for _, c := range committed {
		if !c.present {
			edits = append(edits, commit.IndexEdit{Kind: commit.IndexEditRemove, Path: c.path})
			continue
		}
		edits = append(edits, commit.IndexEdit{Kind: commit.IndexEditBlob, Path: c.path, Mode: c.mode, SHA: c.sha})
	}
	return edits
}

// materializeCommitted writes the committed content of each previously-
// conflicted path into the working tree, through the same primitives a
// conclusion's own resolutions go through -- so a symlink is written as a link,
// a gitlink is left to the submodule's checkout, and a path the commit does not
// carry is removed from disk exactly as `delete` removes one.
//
// It runs only after the overwrite check has passed, and never under --dry-run.
func materializeCommitted(ctx context.Context, committed []committedSide) error {
	if len(committed) == 0 {
		return nil
	}
	root, err := git.AnchorRoot(ctx)
	if err != nil {
		return fmt.Errorf("resolving the directory git's paths are relative to, to write the concluded files: %w", err)
	}
	for _, c := range committed {
		if err := writeStageToWorktree(ctx, git.Anchor(root, c.path), c.entry()); err != nil {
			return fmt.Errorf("%s: %w", c.path, err)
		}
	}
	return nil
}

// refuseContradictedDeclaration refuses a re-run whose declaration names a side
// the standing commit does not hold.
//
// The commit embodies the resolutions already, so nothing a declaration says
// can change what is committed: the only two honest answers are "it agrees,
// and is therefore moot" and "it disagrees, and the run must say so". Silently
// accepting a contradicting declaration would leave an operator believing they
// had chosen a side that was decided before their command ran.
func (op continueOp) refuseContradictedDeclarations(ctx context.Context, stood string, sides map[string]conflict.Sides, committed []committedSide, declared []resolution) int {
	if len(declared) == 0 {
		return 0
	}
	if code := op.refuseStrayResolutions(sides, declared); code != 0 {
		return code
	}

	held := make(map[string]committedSide, len(committed))
	for _, c := range committed {
		held[c.path] = c
	}

	type contradiction struct {
		path   string
		choice resolutionChoice
		says   string
	}
	var found []contradiction
	for _, r := range declared {
		c := held[r.Path]
		content, present, err := declaredContent(ctx, r, sides[r.Path])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitcode.General
		}
		if present == c.present && (!present || bytes.Equal(content, c.content)) {
			continue
		}
		says := "the content it names"
		switch {
		case !present:
			says = "the path removed"
		case !c.present:
			says = "the path kept"
		}
		found = append(found, contradiction{path: r.Path, choice: r.Choice, says: says})
	}
	if len(found) == 0 {
		return 0
	}
	sort.Slice(found, func(i, j int) bool { return found[i].path < found[j].path })

	fmt.Fprintf(os.Stderr, "error: %d declared resolution(s) name something commit %s does not hold:\n", len(found), shortSHA(stood))
	for _, f := range found {
		fmt.Fprintf(os.Stderr, "  %s  (--resolve '%s=%s' wants %s)\n", f.path, f.path, f.choice, f.says)
	}
	fmt.Fprintf(os.Stderr, "  This %s was already concluded by commit %s, which stands on the branch: its tree IS the\n", op.kind, shortSHA(stood))
	fmt.Fprintf(os.Stderr, "  resolution, and no declaration made now can change it. What is left is the cleanup a killed\n")
	fmt.Fprintf(os.Stderr, "  run did not finish, and nothing was cleaned up here.\n")
	fmt.Fprintf(os.Stderr, "  Finish it with the declarations dropped:\n")
	fmt.Fprintf(os.Stderr, "    safegit %s\n", op.command)
	fmt.Fprintf(os.Stderr, "  To end up with different content, finish the cleanup FIRST -- 'safegit undo' refuses while git\n")
	fmt.Fprintf(os.Stderr, "  still calls this repository mid-%s -- and then either commit the change on top of it, or undo\n", op.kind)
	fmt.Fprintf(os.Stderr, "  the conclusion and make the %s again.\n", op.kind)
	return exitcode.ConclusionUnresolved
}

// declaredContent renders what one declared resolution says the path should
// hold: a stage's blob, the file on disk, or nothing at all.
func declaredContent(ctx context.Context, r resolution, s conflict.Sides) (content []byte, present bool, err error) {
	switch r.Choice {
	case resolveDelete:
		return nil, false, nil
	case resolveOurs, resolveTheirs:
		e := s.Ours
		if r.Choice == resolveTheirs {
			e = s.Theirs
		}
		if e == nil {
			// The side that deleted the path: resolving to it removes the path.
			return nil, false, nil
		}
		if e.Mode == gitlinkMode {
			// A submodule pointer has no blob to compare; the object name is the
			// whole of what the entry says.
			return []byte(e.SHA), true, nil
		}
		blob, err := git.CatFileBlob(ctx, e.SHA)
		if err != nil {
			return nil, false, fmt.Errorf("reading the index stage %s names for %s: %w", r.Choice, r.Path, err)
		}
		return blob, true, nil
	case resolveWorktree:
		root, err := git.AnchorRoot(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("resolving the directory git's paths are relative to, to read %s: %w", r.Path, err)
		}
		content, present, err := worktreeContent(git.Anchor(root, r.Path))
		if err != nil {
			return nil, false, fmt.Errorf("reading %s to compare it with what was committed: %w", r.Path, err)
		}
		return content, present, nil
	}
	return nil, false, fmt.Errorf("internal: %s carries an unrecognized resolution %q", r.Path, r.Choice)
}

// refuseCrashWorktreeOverwrite is the overwrite refusal (exit 27) on the crash
// path.
//
// It asks the same question the declared path's own check asks -- would this
// write destroy content held in no commit, no stage and no stash -- over the
// writes the standing commit drives. The accepted set is the same one, widened
// by the COMMITTED content itself: a file already holding what the commit holds
// is a file the write leaves exactly as it is, which is the ordinary state a
// crash leaves when the killed run got as far as the working tree.
//
// The way out differs from the declared path's, and it has to: `worktree` is
// not available here, because the commit already stands and a declaration
// cannot change it. Either the edit is put somewhere reachable first, or
// --discard-unmatched-worktree elects its destruction.
func (op continueOp) refuseCrashWorktreeOverwrite(ctx context.Context, stood string, sides map[string]conflict.Sides, committed []committedSide, discard bool) int {
	root, err := git.AnchorRoot(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolving the directory git's paths are relative to, to read what is on disk: %v\n", err)
		return exitcode.General
	}

	var victims []overwriteVictim
	for _, c := range committed {
		onDisk, present, err := worktreeContent(git.Anchor(root, c.path))
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: reading %s to see what would be destroyed: %v\n", c.path, err)
			return exitcode.General
		}
		if !present {
			// Nothing on disk to destroy.
			continue
		}
		accepted, err := acceptedWorktreeContents(ctx, c.path, sides[c.path])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return exitcode.General
		}
		if c.content != nil {
			accepted = append(accepted, c.content)
		}
		matched := false
		for _, a := range accepted {
			if bytes.Equal(a, onDisk) {
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		victims = append(victims, overwriteVictim{path: c.path, deletes: !c.present})
	}
	if len(victims) == 0 {
		return 0
	}
	sort.Slice(victims, func(i, j int) bool { return victims[i].path < victims[j].path })

	if discard {
		for _, v := range victims {
			verb := "overwritten"
			if v.deletes {
				verb = "deleted"
			}
			fmt.Fprintf(os.Stderr, "note: %s is %s as --discard-unmatched-worktree elects; its content matched no side of the conflict and not what was committed\n", v.path, verb)
		}
		return 0
	}

	fmt.Fprintf(os.Stderr, "error: %d working-tree file(s) hold content neither this %s nor the commit that concluded it accounts for:\n",
		len(victims), op.kind)
	for _, v := range victims {
		effect := fmt.Sprintf("overwrite it with what commit %s holds", shortSHA(stood))
		if v.deletes {
			effect = fmt.Sprintf("delete it, which is what commit %s did with the path", shortSHA(stood))
		}
		fmt.Fprintf(os.Stderr, "  %s  (finishing the cleanup would %s)\n", v.path, effect)
	}
	fmt.Fprintf(os.Stderr, "  The file matches no index stage, nothing git wrote there and nothing the commit holds, so it is\n")
	fmt.Fprintf(os.Stderr, "  a hand edit: its content is in no commit, no stage and no stash, and nothing could bring it back.\n")
	fmt.Fprintf(os.Stderr, "  Nothing was cleaned up and commit %s still stands. Put that content somewhere you can reach it\n", shortSHA(stood))
	fmt.Fprintf(os.Stderr, "  (commit it on a branch of its own, or copy it aside) and run the command again -- or, to throw it\n")
	fmt.Fprintf(os.Stderr, "  away deliberately:\n")
	fmt.Fprintf(os.Stderr, "    safegit %s --discard-unmatched-worktree\n", op.command)
	return exitcode.ConclusionWouldOverwrite
}
