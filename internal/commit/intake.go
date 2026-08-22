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
	// untrack makes this entry a removal from the index rather than a staging
	// of what is on disk. The file itself is left exactly where it is: this is
	// the "stop tracking it, keep it" half of a .gitignore cleanup, which
	// otherwise has no safegit-mediated form at all.
	untrack bool
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

// namesThroughLink reports whether an argument was written with a trailing
// separator, which is how a caller says "through the final component" rather
// than "the final component itself".
//
// It is the whole disambiguation for a directory symlink: `link` is the link
// object, `link/` is the directory it points at. Nothing on disk is consulted
// to decide which was meant -- the spelling decides, so the same argument means
// the same thing in every repository and from every directory.
func namesThroughLink(arg string) bool {
	return strings.HasSuffix(arg, "/") || strings.HasSuffix(arg, string(filepath.Separator))
}

// canonicalRel turns one caller-typed argument into its canonical
// repo-relative form. A relative argument resolves against the process working
// directory -- the caller's own shell -- not against the repository root.
//
// followFinal comes from namesThroughLink: with it false the FINAL component is
// never resolved, so a symlink named on the command line stays that symlink and
// is committed as the 120000 object it is rather than collapsing into whatever
// it points at.
func canonicalRel(repoRoot, arg string, followFinal bool) (string, error) {
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

	// Resolve the PARENT components so absPath matches repoRoot, which comes
	// from git rev-parse --show-toplevel (git resolves symlinks). On macOS,
	// /var is a symlink to /private/var, so without this, filepath.Rel
	// produces a path starting with ".." and the file is rejected as
	// outside the repository. The final component is left alone: resolving it
	// is what used to make a symlink argument uncommittable.
	absPath = resolveParentSymlinks(absPath)

	if followFinal {
		if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
			absPath = resolved
		}
	}

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

// resolveParentSymlinks resolves every symlink ABOVE the final component and
// leaves the final component itself untouched, existing or not.
//
// The predecessor resolved the whole path, final component included, which is
// why a symlink handed to safegit was committed as its target's content -- or,
// when the target was already committed unchanged, produced no commit at all.
// A symlink is an object in its own right; only its parents are directories
// whose spelling has to be reconciled with the one git reports.
func resolveParentSymlinks(absPath string) string {
	dir, base := filepath.Split(absPath)
	if base == "" {
		// A path that is nothing but separators (the filesystem root). There is
		// no final component to preserve.
		if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
			return resolved
		}
		return absPath
	}
	resolvedDir, err := filepath.EvalSymlinks(filepath.Clean(dir))
	if err != nil {
		return absPath
	}
	return filepath.Join(resolvedDir, base)
}

// escapingLinkTarget returns the target text of a symlink that points outside
// the repository, and the empty string for anything else -- a regular file, a
// symlink that stays inside, or a path that is not there at all.
//
// Such a link is committable: git records the link text and nothing more, and
// refusing it would make safegit stricter than git for no safety it can
// actually provide. What safegit does instead is say so once, because the
// object it just wrote resolves to nothing in anyone else's checkout.
func escapingLinkTarget(repoRoot, rel string) string {
	abs := git.Anchor(repoRoot, rel)
	info, err := os.Lstat(abs)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	target, err := os.Readlink(abs)
	if err != nil {
		return ""
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(abs), target)
	}
	resolved = filepath.Clean(resolved)
	within, err := filepath.Rel(repoRoot, resolved)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return target
	}
	return ""
}

// noticeEscapingLinks writes one stderr line per staged symlink whose target
// leaves the repository. It runs once per operation, after intake has settled,
// so a CAS retry cannot repeat it.
func noticeEscapingLinks(repoRoot string, paths []string) {
	for _, path := range paths {
		if target := escapingLinkTarget(repoRoot, path); target != "" {
			fmt.Fprintf(os.Stderr, "notice: %s is a symlink to %s, which is outside the repository; the commit records the link text, which will not resolve in another checkout\n", path, target)
		}
	}
}

// resolveFiles turns the caller's file specs into the canonical set of paths to
// stage, against the tree the commit is actually built on.
//
// baseRev is that tree's revision: the target branch's tip for a commit, the
// tip being replaced for an amend, and the empty string for an unborn ref. Every
// tracked-path judgement is made against it, because a path's presence in HEAD
// says nothing about a commit built on another branch.
func (p *Pipeline) resolveFiles(ctx context.Context, repoRoot, baseRev string, specs []FileSpec, untrack []string) (*intake, error) {
	in := &intake{}
	tree := newTreeIndex(ctx, baseRev)
	seen := make(map[string]bool)
	skipped := make(map[string]bool)
	var links []string

	// The untrack targets are resolved first, so the staging loop below can see
	// which paths are being removed: naming one explicitly on both sides is a
	// contradiction, while an expansion that happens to sweep one up just
	// leaves it to the removal.
	dropped, err := p.resolveUntrack(ctx, repoRoot, baseRev, tree, in, seen, untrack)
	if err != nil {
		return nil, err
	}

	for _, spec := range specs {
		rel, err := canonicalRel(repoRoot, spec.Path, namesThroughLink(spec.Path))
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
			if dropped[rel] {
				return nil, &CommitError{
					Code: exitcode.Usage,
					Message: fmt.Sprintf("%s is named both as a file to commit and in --untrack; "+
						"say one or the other", spec.Path),
				}
			}
			if err := p.validateNamedPath(ctx, repoRoot, rel, baseRev, spec); err != nil {
				return nil, err
			}
			links = append(links, rel)
			if !seen[rel] {
				seen[rel] = true
				in.entries = append(in.entries, intakeEntry{path: rel, hunks: spec.Hunks, src: srcIdx})
			}
			continue
		}

		expanded, err := p.expandDirectory(ctx, repoRoot, rel, tree)
		if err != nil {
			return nil, err
		}
		for _, ig := range expanded.ignored {
			if !skipped[ig] {
				skipped[ig] = true
				in.skipped = append(in.skipped, ig)
			}
		}
		links = append(links, expanded.links...)
		if len(expanded.members) == 0 {
			return nil, &CommitError{
				Code: exitcode.PathMatchedNothing,
				Message: fmt.Sprintf("nothing to commit for %s: the directory holds no files on disk "+
					"and no paths in %s", spec.Path, describeBase(baseRev)),
			}
		}
		for _, m := range expanded.members {
			if !seen[m] {
				seen[m] = true
				in.entries = append(in.entries, intakeEntry{path: m, src: srcIdx})
			}
		}
	}

	sort.Strings(in.skipped)
	noticeEscapingLinks(repoRoot, links)
	return in, nil
}

// resolveUntrack turns the --untrack arguments into removal entries and returns
// the set of paths they cover.
//
// The scope is general: any tracked path can be untracked, not only a
// gitignored one. What keeps a typo from silently doing nothing is the
// tracked-in-parent check -- a target the commit's parent does not carry cannot
// be removed from anything, so naming it is a hard error rather than a no-op
// that leaves the caller believing a cleanup happened.
//
// The gitignored-path refusal that guards ordinary arguments deliberately does
// NOT apply here. Untracking a path BECAUSE it is now ignored is the whole
// point of the flag; what the refusal still blocks is the opposite direction,
// adding ignored content to a commit.
func (p *Pipeline) resolveUntrack(
	ctx context.Context,
	repoRoot, baseRev string,
	tree *treeIndex,
	in *intake,
	seen map[string]bool,
	untrack []string,
) (map[string]bool, error) {
	dropped := make(map[string]bool)
	for _, arg := range untrack {
		rel, err := canonicalRel(repoRoot, arg, namesThroughLink(arg))
		if err != nil {
			return nil, err
		}

		targets, isDir, err := p.untrackTargets(rel, tree)
		if err != nil {
			return nil, err
		}
		if len(targets) == 0 {
			return nil, &CommitError{
				Code: exitcode.PathMatchedNothing,
				Message: fmt.Sprintf("nothing to untrack for %s: it is not tracked in %s, so there is "+
					"no index entry to remove", arg, describeBase(baseRev)),
			}
		}

		// Untracking a path nothing ignores is legal and occasionally what
		// someone means -- but far more often the .gitignore edit that belongs
		// with it was forgotten, and without it the next commit that names the
		// path puts it straight back.
		//
		// The question is asked of the ignore RULES alone (MatchesIgnoreRules,
		// which passes --no-index), never of git's index-aware check: an
		// --untrack target is tracked by definition and normally still in the
		// index, and the index-aware answer for such a path is always "not
		// ignored", which would fire this notice precisely when the target IS
		// covered by a pattern. An error means the answer is unknown, and an
		// unknown answer is not worth a line that might be the opposite of the
		// truth.
		if ignored, ierr := git.MatchesIgnoreRules(ctx, rel); ierr == nil && !ignored {
			fmt.Fprintf(os.Stderr, "notice: %s is not gitignored; it stops being tracked, but nothing "+
				"stops it from being committed again -- add a .gitignore pattern if that is what you meant\n", arg)
		}

		srcIdx := len(in.sources)
		in.sources = append(in.sources, intakeSource{arg: arg, path: rel, dir: isDir})
		for _, target := range targets {
			dropped[target] = true
			if seen[target] {
				continue
			}
			seen[target] = true
			in.entries = append(in.entries, intakeEntry{path: target, src: srcIdx, untrack: true})
		}
	}
	return dropped, nil
}

// untrackTargets resolves one --untrack argument against the tree the commit is
// built on: the exact path when the tree carries one, every path underneath it
// when the argument names a directory, and nothing at all when the tree has
// neither -- which is what makes a typo a refusal.
//
// A gitlink is skipped under a directory argument for the same reason expansion
// skips one: naming a directory above a submodule must not move another
// repository's pointer. Naming the gitlink itself still untracks it.
func (p *Pipeline) untrackTargets(rel string, tree *treeIndex) (targets []string, isDir bool, err error) {
	if rel != "" {
		if _, inTree, eerr := tree.entry(rel); eerr != nil {
			return nil, false, eerr
		} else if inTree {
			return []string{rel}, false, nil
		}
	}

	under, err := tree.under(rel)
	if err != nil {
		return nil, false, err
	}
	for _, path := range under {
		entry, ok, eerr := tree.entry(path)
		if eerr != nil {
			return nil, false, eerr
		}
		if ok && entry.ObjectType == "commit" {
			continue
		}
		targets = append(targets, path)
	}
	return targets, true, nil
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
func (p *Pipeline) validateNamedPath(ctx context.Context, repoRoot, rel, baseRev string, spec FileSpec) error {
	arg := spec.Path
	abs := git.Anchor(repoRoot, rel)
	if info, err := os.Lstat(abs); err == nil && info.Mode()&os.ModeSymlink != 0 && spec.Hunks != nil {
		// A symlink's content is its target path: one line, produced by the
		// filesystem, with no hunks to choose between. git reports no diff
		// hunks for it either, so a selection could only ever select nothing.
		return &CommitError{
			Code: exitcode.SymlinkHunkSpec,
			Message: fmt.Sprintf("%s is a symlink, which has no hunks to select: a symlink is committed whole, "+
				"as the link text it holds", arg),
		}
	}
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

// expansion is what a directory argument produced: the paths to stage, the
// gitignored paths passed over, and the symlinks seen on the way, which are the
// only members worth asking about an escaping target.
type expansion struct {
	members []string
	ignored []string
	links   []string
}

// expandDirectory returns every path under a directory prefix that the commit
// should stage: the union of what is on disk and what the base tree holds, so
// that a file deleted from disk is included as the deletion it is.
func (p *Pipeline) expandDirectory(ctx context.Context, repoRoot, prefix string, tree *treeIndex) (*expansion, error) {
	found := make(map[string]bool)

	fromTree, err := tree.under(prefix)
	if err != nil {
		return nil, err
	}
	for _, path := range fromTree {
		entry, ok, eerr := tree.entry(path)
		if eerr != nil {
			return nil, eerr
		}
		// A gitlink under the prefix is a submodule boundary: naming a
		// directory above it must not move another repository's pointer.
		if ok && entry.ObjectType == "commit" {
			continue
		}
		found[path] = true
	}

	walked, err := walkForCommit(ctx, repoRoot, prefix)
	if err != nil {
		return nil, err
	}
	for _, path := range walked.files {
		found[path] = true
	}

	members := make([]string, 0, len(found))
	for path := range found {
		members = append(members, path)
	}
	sort.Strings(members)
	return &expansion{members: members, ignored: walked.ignored, links: walked.links}, nil
}

// diskWalk is what one directory walk saw: the files to stage, the gitignored
// paths skipped, and which of the files are symlinks (known from the directory
// listing, so recording them costs no extra syscall).
type diskWalk struct {
	files   []string
	ignored []string
	links   []string
}

// walkForCommit lists the files on disk under a repo-relative directory prefix,
// skipping git's own directory, everything the ignore rules exclude, and
// anything inside a nested repository.
//
// The ignore question is asked once per directory level, for that level's whole
// listing at once, so an ignored directory is answered for as a directory and
// never descended into -- which is both what git does and the difference
// between one question and one per file inside a build output tree.
func walkForCommit(ctx context.Context, repoRoot, prefix string) (*diskWalk, error) {
	walk := &diskWalk{}
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
			return nil, fmt.Errorf("reading directory %s: %w", absDir, rerr)
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
			return nil, ierr
		}

		for i, e := range kept {
			path := candidates[i]
			if excluded[path] {
				walk.ignored = append(walk.ignored, path)
				continue
			}
			// A symlink is never descended into, whatever it points at: it is
			// one object, and following it would sweep another part of the
			// filesystem into this directory's expansion under names that do
			// not exist there.
			if e.Type()&os.ModeSymlink != 0 {
				walk.files = append(walk.files, path)
				walk.links = append(walk.links, path)
				continue
			}
			if e.IsDir() {
				if isNestedRepository(git.Anchor(repoRoot, path)) {
					continue
				}
				queue = append(queue, path)
				continue
			}
			walk.files = append(walk.files, path)
		}
	}
	return walk, nil
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
