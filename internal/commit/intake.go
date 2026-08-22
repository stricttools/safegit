package commit

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
)

// Argument intake: the one place a caller's file arguments become paths the
// pipeline stages.
//
// Every path leaves here in ONE canonical form -- repo-relative, slash
// separated, no "./" and no trailing slash -- and stays in that form for the
// rest of the pipeline. An absolute path is derived only at a syscall boundary,
// through git.Anchor, and never stored. Mixing the two forms is how a listing
// git produced ends up naming a different file from the one a syscall reaches.
//
// A named DIRECTORY expands here, at intake, into the union of what is on disk
// under it and what the commit's parent tree holds under it. The tree half is
// not an optimization: without it a directory whose files were deleted from
// disk contributes nothing at all, and the deletions the caller asked to commit
// are silently dropped.

// intakeSource is one argument the caller typed, kept so a path that turns out
// to contribute nothing can be reported by the name it was asked for rather
// than by whatever it expanded into.
type intakeSource struct {
	// arg is the argument verbatim, as it appeared on the command line.
	arg string
	// path is its canonical repo-relative form. The empty string is the
	// repository root itself, which is what naming "." resolves to.
	path string
	// dir records that this source expanded as a directory, which is what
	// makes a changed path underneath it count as its contribution.
	dir bool
}

// intakeEntry is one path the pipeline will stage.
type intakeEntry struct {
	// path is canonical repo-relative.
	path string
	// hunks selects hunks within the file; nil stages the whole file.
	hunks []int
	// src indexes the intake's sources: which argument produced this entry.
	src int
}

// intake is everything argument resolution produced.
type intake struct {
	sources []intakeSource
	entries []intakeEntry
	// skipped lists gitignored paths that a directory expansion passed over.
	// Expansion produces names the caller never typed, so an ignored file
	// under one is skipped rather than refused -- but silently skipping it
	// would leave a machine consumer no way to know, so it is carried here and
	// published in the command's payload.
	skipped []string
}

// unmatchedSource returns the first argument that contributed nothing to the
// given set of changed paths, if there is one.
//
// Naming a path is a statement that it belongs in the commit. When it turns out
// to change nothing -- a typo, a file already committed with this exact
// content, a directory that is empty on disk and absent from the tree -- the
// honest answer is a refusal naming that argument, not a commit that quietly
// contains something else.
func (in *intake) unmatchedSource(changed []git.ChangedPath) (intakeSource, bool) {
	for _, src := range in.sources {
		if !src.contributed(changed) {
			return src, true
		}
	}
	return intakeSource{}, false
}

// contributed reports whether any changed path is this source's own doing: the
// path itself, or -- for a directory -- anything underneath it. The repository
// root, which is what naming "." resolves to, is contributed to by any change
// at all.
func (s intakeSource) contributed(changed []git.ChangedPath) bool {
	if s.path == "" {
		return len(changed) > 0
	}
	prefix := s.path + "/"
	for _, c := range changed {
		if c.Path == s.path {
			return true
		}
		if s.dir && strings.HasPrefix(c.Path, prefix) {
			return true
		}
	}
	return false
}

// treeIndex is the commit's base tree, read at most once and then answered
// from memory: which paths it holds, and with what kind of entry.
type treeIndex struct {
	ctx     context.Context
	rev     string
	loaded  bool
	entries map[string]git.TreeEntry
	// sorted holds the tree's paths in sorted order, so a prefix query is a
	// binary search rather than a scan.
	sorted []string
}

func newTreeIndex(ctx context.Context, rev string) *treeIndex {
	return &treeIndex{ctx: ctx, rev: rev}
}

// load reads the tree once. An empty rev is an unborn ref: no tree, no entries.
func (t *treeIndex) load() error {
	if t.loaded {
		return nil
	}
	t.loaded = true
	t.entries = make(map[string]git.TreeEntry)
	if t.rev == "" {
		return nil
	}
	entries, err := git.LsTreeRecursive(t.ctx, t.rev)
	if err != nil {
		return err
	}
	for _, e := range entries {
		t.entries[e.Path] = e
		t.sorted = append(t.sorted, e.Path)
	}
	sort.Strings(t.sorted)
	return nil
}

// entry returns the tree entry at an exact path.
func (t *treeIndex) entry(path string) (git.TreeEntry, bool, error) {
	if err := t.load(); err != nil {
		return git.TreeEntry{}, false, err
	}
	e, ok := t.entries[path]
	return e, ok, nil
}

// under returns every tree path inside the given directory prefix. The empty
// prefix is the repository root and matches everything.
func (t *treeIndex) under(prefix string) ([]string, error) {
	if err := t.load(); err != nil {
		return nil, err
	}
	if prefix == "" {
		out := make([]string, len(t.sorted))
		copy(out, t.sorted)
		return out, nil
	}
	p := prefix + "/"
	i := sort.SearchStrings(t.sorted, p)
	var out []string
	for ; i < len(t.sorted) && strings.HasPrefix(t.sorted[i], p); i++ {
		out = append(out, t.sorted[i])
	}
	return out, nil
}

// canonicalRel turns one caller-typed argument into its canonical
// repo-relative form. A relative argument resolves against the process working
// directory -- the caller's own shell -- not against the repository root.
func canonicalRel(repoRoot, arg string) (string, error) {
	var absPath string
	if filepath.IsAbs(arg) {
		absPath = filepath.Clean(arg)
	} else {
		var err error
		absPath, err = filepath.Abs(arg)
		if err != nil {
			return "", fmt.Errorf("resolving path %s: %w", arg, err)
		}
	}

	// Resolve symlinks so absPath matches repoRoot, which comes from
	// git rev-parse --show-toplevel (git resolves symlinks). On macOS,
	// /var is a symlink to /private/var, so without this, filepath.Rel
	// produces a path starting with ".." and the file is rejected as
	// outside the repository.
	absPath = resolveSymlinks(absPath)

	rel, err := filepath.Rel(repoRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file %s is outside the repository", arg)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		// The repository root itself. Canonically the empty prefix, so every
		// path in the repository is "under" it.
		return "", nil
	}
	return rel, nil
}

// resolveFiles turns the caller's file specs into the canonical set of paths to
// stage, against the tree the commit is actually built on.
//
// baseRev is that tree's revision: the target branch's tip for a commit, the
// tip being replaced for an amend, and the empty string for an unborn ref. Every
// tracked-path judgement is made against it, because a path's presence in HEAD
// says nothing about a commit built on another branch.
func (p *Pipeline) resolveFiles(ctx context.Context, repoRoot, baseRev string, specs []FileSpec) (*intake, error) {
	in := &intake{}
	tree := newTreeIndex(ctx, baseRev)
	seen := make(map[string]bool)
	skipped := make(map[string]bool)

	for _, spec := range specs {
		rel, err := canonicalRel(repoRoot, spec.Path)
		if err != nil {
			return nil, err
		}

		srcIdx := len(in.sources)
		src := intakeSource{arg: spec.Path, path: rel}

		isDir, err := p.namesADirectory(ctx, repoRoot, rel, spec.Hunks != nil, tree)
		if err != nil {
			return nil, err
		}
		src.dir = isDir
		in.sources = append(in.sources, src)

		if !isDir {
			if err := p.validateNamedPath(ctx, repoRoot, rel, baseRev, spec.Path); err != nil {
				return nil, err
			}
			if !seen[rel] {
				seen[rel] = true
				in.entries = append(in.entries, intakeEntry{path: rel, hunks: spec.Hunks, src: srcIdx})
			}
			continue
		}

		members, ignored, err := p.expandDirectory(ctx, repoRoot, rel, tree)
		if err != nil {
			return nil, err
		}
		for _, ig := range ignored {
			if !skipped[ig] {
				skipped[ig] = true
				in.skipped = append(in.skipped, ig)
			}
		}
		if len(members) == 0 {
			return nil, &CommitError{
				Code: exitcode.PathMatchedNothing,
				Message: fmt.Sprintf("nothing to commit for %s: the directory holds no files on disk "+
					"and no paths in %s", spec.Path, describeBase(baseRev)),
			}
		}
		for _, m := range members {
			if !seen[m] {
				seen[m] = true
				in.entries = append(in.entries, intakeEntry{path: m, src: srcIdx})
			}
		}
	}

	sort.Strings(in.skipped)
	return in, nil
}

// namesADirectory decides whether an argument is a directory to expand or a
// single path to stage.
//
// A submodule is never a directory here. Its gitlink is one entry in the parent
// tree -- a commit pointer, not a subtree -- and safegit stages it only when the
// caller names it, never as a by-product of naming something above it. A nested
// repository that is not yet a gitlink is treated the same way: expansion stops
// at the boundary rather than sweeping another repository's working tree into
// this repository's commit.
func (p *Pipeline) namesADirectory(ctx context.Context, repoRoot, rel string, hasHunks bool, tree *treeIndex) (bool, error) {
	if hasHunks {
		// A hunk selection is a statement about one file's content.
		return false, nil
	}
	if rel == "" {
		// The repository root.
		return true, nil
	}

	_, inTree, err := tree.entry(rel)
	if err != nil {
		return false, err
	}
	if inTree {
		// An exact tree entry is a blob, a symlink or a gitlink -- never a
		// directory to expand.
		return false, nil
	}

	abs := git.Anchor(repoRoot, rel)
	info, statErr := os.Lstat(abs)
	onDiskDir := statErr == nil && info.IsDir()
	if onDiskDir {
		if isNestedRepository(abs) {
			return false, nil
		}
		return true, nil
	}

	// Not on disk, no entry of its own: a directory only if the base tree holds
	// paths underneath it, which is what a directory whose every file was
	// deleted looks like.
	under, err := tree.under(rel)
	if err != nil {
		return false, err
	}
	return len(under) > 0, nil
}

// validateNamedPath applies the rules that hold for a path the caller named
// explicitly (as opposed to one an expansion produced).
func (p *Pipeline) validateNamedPath(ctx context.Context, repoRoot, rel, baseRev, arg string) error {
	abs := git.Anchor(repoRoot, rel)
	if _, err := os.Lstat(abs); os.IsNotExist(err) {
		// Absent from disk: the only coherent reading is a deletion, which
		// requires the path to be in the tree the commit is built on.
		tracked, terr := git.IsTracked(ctx, baseRev, rel)
		if terr != nil {
			return fmt.Errorf("checking tracked status of %s: %w", arg, terr)
		}
		if !tracked {
			return &CommitError{
				Code: exitcode.PathMatchedNothing,
				Message: fmt.Sprintf("file %s does not exist and is not tracked in %s",
					arg, describeBase(baseRev)),
			}
		}
		return nil
	}

	// Present on disk: an explicitly named gitignored path is a refusal. It is
	// a refusal only HERE -- an expansion skips ignored paths instead, because
	// they are names the caller never typed.
	if ignored, _ := git.IsIgnored(ctx, rel); ignored {
		return fmt.Errorf("file %s is gitignored", arg)
	}
	return nil
}

// expandDirectory returns every path under a directory prefix that the commit
// should stage: the union of what is on disk and what the base tree holds, so
// that a file deleted from disk is included as the deletion it is.
//
// It also returns the gitignored paths it passed over.
func (p *Pipeline) expandDirectory(ctx context.Context, repoRoot, prefix string, tree *treeIndex) (members, ignored []string, err error) {
	found := make(map[string]bool)

	fromTree, err := tree.under(prefix)
	if err != nil {
		return nil, nil, err
	}
	for _, path := range fromTree {
		entry, ok, eerr := tree.entry(path)
		if eerr != nil {
			return nil, nil, eerr
		}
		// A gitlink under the prefix is a submodule boundary: naming a
		// directory above it must not move another repository's pointer.
		if ok && entry.ObjectType == "commit" {
			continue
		}
		found[path] = true
	}

	fromDisk, ignoredOnDisk, err := walkForCommit(ctx, repoRoot, prefix)
	if err != nil {
		return nil, nil, err
	}
	for _, path := range fromDisk {
		found[path] = true
	}

	members = make([]string, 0, len(found))
	for path := range found {
		members = append(members, path)
	}
	sort.Strings(members)
	return members, ignoredOnDisk, nil
}

// walkForCommit lists the files on disk under a repo-relative directory prefix,
// skipping git's own directory, everything the ignore rules exclude, and
// anything inside a nested repository.
//
// The ignore question is asked once per directory level, for that level's whole
// listing at once, so an ignored directory is answered for as a directory and
// never descended into -- which is both what git does and the difference
// between one question and one per file inside a build output tree.
func walkForCommit(ctx context.Context, repoRoot, prefix string) (files, ignored []string, err error) {
	queue := []string{prefix}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]

		absDir := repoRoot
		if dir != "" {
			absDir = git.Anchor(repoRoot, dir)
		}
		listing, rerr := os.ReadDir(absDir)
		if rerr != nil {
			if os.IsNotExist(rerr) {
				// The directory exists only in the tree; its paths come from
				// there.
				continue
			}
			return nil, nil, fmt.Errorf("reading directory %s: %w", absDir, rerr)
		}

		candidates := make([]string, 0, len(listing))
		kept := make([]os.DirEntry, 0, len(listing))
		for _, e := range listing {
			if e.Name() == ".git" {
				continue
			}
			child := e.Name()
			if dir != "" {
				child = dir + "/" + child
			}
			candidates = append(candidates, child)
			kept = append(kept, e)
		}

		excluded, ierr := git.FilterIgnored(ctx, candidates)
		if ierr != nil {
			return nil, nil, ierr
		}

		for i, e := range kept {
			path := candidates[i]
			if excluded[path] {
				ignored = append(ignored, path)
				continue
			}
			if e.IsDir() {
				if isNestedRepository(git.Anchor(repoRoot, path)) {
					continue
				}
				queue = append(queue, path)
				continue
			}
			files = append(files, path)
		}
	}
	return files, ignored, nil
}

// isNestedRepository reports whether a directory carries its own .git, which
// makes it a submodule working tree or an unrelated repository sitting inside
// this one. Either way it is a boundary an expansion stops at.
func isNestedRepository(absDir string) bool {
	_, err := os.Lstat(filepath.Join(absDir, ".git"))
	return err == nil
}

// changedPaths reduces a name-status diff to its sorted paths. It always
// returns a non-nil slice, so a result carrying no changes serializes as an
// empty list rather than as null.
func changedPaths(changed []git.ChangedPath) []string {
	out := make([]string, 0, len(changed))
	for _, c := range changed {
		out = append(out, c.Path)
	}
	sort.Strings(out)
	return out
}

// unmatchedSourceError is the refusal for an argument that contributed nothing.
func unmatchedSourceError(src intakeSource, against string) error {
	what := "it"
	if src.dir {
		what = "everything under it"
	}
	return &CommitError{
		Code: exitcode.PathMatchedNothing,
		Message: fmt.Sprintf("nothing to commit for %s: staging %s leaves the tree of %s unchanged",
			src.arg, what, against),
	}
}

// refOrEmptyTree names what a new tree was compared against.
func refOrEmptyTree(isRootCommit bool, ref string) string {
	if isRootCommit {
		return "the empty tree (" + ref + " has no commit yet)"
	}
	return ref
}

// describeBase names the tree a judgement was made against, so a refusal says
// which tree decided it rather than leaving the caller to assume HEAD.
func describeBase(baseRev string) string {
	if baseRev == "" {
		return "the (empty) tree this commit is built on"
	}
	return baseRev
}
