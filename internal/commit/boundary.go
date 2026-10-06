package commit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/git"
)

// Repository boundaries: the directories where this repository stops and
// another one begins.
//
// A path below one is another repository's content, and git has no coherent
// answer for it in this repository: `git add` of a file inside an untracked
// nested repository exits 0 and stages nothing, a file inside a submodule is a
// raw "is in submodule" fatal, and a move into a submodule replaces the gitlink
// with a directory. safegit asks the question itself, of every path a caller
// names, before anything is staged or moved. The same predicate decides where
// a directory expansion stops, so a named path and an expanded one can never
// disagree about which repository a file belongs to.

// BoundaryShape is what makes a directory a repository boundary.
type BoundaryShape int

const (
	// NestedRepository is a directory git itself recognizes as a repository
	// -- its .git is a valid git directory, or a gitfile pointing at one --
	// that this repository records nowhere.
	NestedRepository BoundaryShape = iota + 1
	// RecordedGitlink is a directory the base tree records as a gitlink: a
	// submodule, with or without a .gitmodules entry, checked out or not.
	RecordedGitlink
	// RegisteredSubmodule is a directory .gitmodules registers as a submodule
	// path that the base tree does not record yet.
	RegisteredSubmodule
)

// RepositoryBoundary is one boundary directory.
type RepositoryBoundary struct {
	// Dir is the boundary directory, repo-relative.
	Dir   string
	Shape BoundaryShape
	// rev is the revision whose tree records a RecordedGitlink.
	rev string
}

// Describe names the boundary's shape in a refusal: "<dir> is <Describe()>".
func (b RepositoryBoundary) Describe() string {
	switch b.Shape {
	case RecordedGitlink:
		return "a submodule recorded as a gitlink in " + describeBase(b.rev)
	case RegisteredSubmodule:
		return "a submodule registered in .gitmodules"
	default:
		return "a separate git repository (it has its own .git)"
	}
}

// RepositoryBoundaries answers boundary questions about one repository against
// one base tree. Every answer is cached, so asking about many paths under the
// same directories costs one look per directory.
type RepositoryBoundaries struct {
	ctx      context.Context
	repoRoot string
	rev      string
	tree     *treeIndex

	registeredLoaded bool
	registered       map[string]bool
	repositories     map[string]bool
}

// NewRepositoryBoundaries is the predicate for a caller outside this package,
// judged against the tree of rev (an empty rev is an unborn branch, which
// records no gitlink).
func NewRepositoryBoundaries(ctx context.Context, repoRoot, rev string) *RepositoryBoundaries {
	return newRepositoryBoundaries(ctx, repoRoot, rev, newTreeIndex(ctx, rev))
}

func newRepositoryBoundaries(ctx context.Context, repoRoot, rev string, tree *treeIndex) *RepositoryBoundaries {
	return &RepositoryBoundaries{ctx: ctx, repoRoot: repoRoot, rev: rev, tree: tree, repositories: make(map[string]bool)}
}

// Above returns the outermost boundary among the directories above a
// repo-relative path, walked from the repository root down. A path spelled with
// a trailing slash names what is under its final component, so that component
// counts as above it -- the reading LinkAbove gives the same spelling.
//
// The tree and .gitmodules are asked at every level. The disk is asked only
// while the walk is still on real directories: past a missing entry there is
// nothing to find, and past a link or a file what the filesystem shows is not
// this repository's.
func (b *RepositoryBoundaries) Above(rel string) (RepositoryBoundary, bool, error) {
	parts := strings.Split(rel, "/")
	onDisk := true
	for i := 1; i < len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		if bd, ok, err := b.recorded(dir); err != nil || ok {
			return bd, ok, err
		}
		if !onDisk {
			continue
		}
		info, err := os.Lstat(git.Anchor(b.repoRoot, dir))
		if err != nil || !info.IsDir() {
			onDisk = false
			continue
		}
		if ok, err := b.isRepository(dir); err != nil || ok {
			return RepositoryBoundary{Dir: dir, Shape: NestedRepository}, ok, err
		}
	}
	return RepositoryBoundary{}, false, nil
}

// At reports whether a repo-relative directory is itself a boundary. It is the
// question a directory expansion asks of every directory it is about to
// descend into.
func (b *RepositoryBoundaries) At(dir string) (RepositoryBoundary, bool, error) {
	if bd, ok, err := b.recorded(dir); err != nil || ok {
		return bd, ok, err
	}
	if !b.realDirectory(dir) {
		return RepositoryBoundary{}, false, nil
	}
	ok, err := b.isRepository(dir)
	if err != nil || !ok {
		return RepositoryBoundary{}, false, err
	}
	return RepositoryBoundary{Dir: dir, Shape: NestedRepository}, true, nil
}

// CheckedOut reports whether a boundary directory holds a repository on disk,
// which is what a command run inside it needs.
func (b *RepositoryBoundaries) CheckedOut(bd RepositoryBoundary) (bool, error) {
	if !b.realDirectory(bd.Dir) {
		return false, nil
	}
	return b.isRepository(bd.Dir)
}

// realDirectory reports whether a repo-relative path is a directory on disk,
// itself and every directory above it: a symlink to a repository is a link,
// committed as one, and never a boundary.
func (b *RepositoryBoundaries) realDirectory(rel string) bool {
	if underLink(b.repoRoot, rel) {
		return false
	}
	info, err := os.Lstat(git.Anchor(b.repoRoot, rel))
	return err == nil && info.IsDir()
}

// recorded answers from what this repository records: a gitlink in the base
// tree, or a submodule path in .gitmodules.
func (b *RepositoryBoundaries) recorded(dir string) (RepositoryBoundary, bool, error) {
	entry, inTree, err := b.tree.entry(dir)
	if err != nil {
		return RepositoryBoundary{}, false, err
	}
	if inTree && entry.ObjectType == "commit" {
		return RepositoryBoundary{Dir: dir, Shape: RecordedGitlink, rev: b.rev}, true, nil
	}
	if err := b.loadRegistered(); err != nil {
		return RepositoryBoundary{}, false, err
	}
	if b.registered[dir] {
		return RepositoryBoundary{Dir: dir, Shape: RegisteredSubmodule}, true, nil
	}
	return RepositoryBoundary{}, false, nil
}

// loadRegistered reads the submodule paths the working tree's .gitmodules
// registers, once. No .gitmodules registers nothing.
func (b *RepositoryBoundaries) loadRegistered() error {
	if b.registeredLoaded {
		return nil
	}
	b.registeredLoaded = true
	b.registered = make(map[string]bool)
	file := git.Anchor(b.repoRoot, ".gitmodules")
	if _, err := os.Lstat(file); err != nil {
		return nil
	}
	out, _, err := git.Run(b.ctx, "config", "--file", file, "--null", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// No key matched: a .gitmodules registering no path.
			return nil
		}
		return fmt.Errorf("reading the submodule paths registered in .gitmodules: %w", err)
	}
	for _, record := range strings.Split(out, "\x00") {
		_, value, ok := strings.Cut(record, "\n")
		if !ok || value == "" {
			continue
		}
		b.registered[path.Clean(strings.TrimSuffix(value, "/"))] = true
	}
	return nil
}

// isRepository asks git whether a repo-relative directory is a repository: its
// .git has to be a valid git directory or a gitfile leading to one, which is
// git's own test for a nested repository. Only a directory with a .git entry is
// asked about, so the common directory costs one Lstat.
func (b *RepositoryBoundaries) isRepository(dir string) (bool, error) {
	if known, ok := b.repositories[dir]; ok {
		return known, nil
	}
	dotGit := filepath.Join(git.Anchor(b.repoRoot, dir), ".git")
	is := false
	if _, err := os.Lstat(dotGit); err == nil {
		_, _, gerr := git.Run(b.ctx, "rev-parse", "--resolve-git-dir", dotGit)
		var exitErr *exec.ExitError
		switch {
		case gerr == nil:
			is = true
		case errors.As(gerr, &exitErr):
			// git's verdict: not a git directory, not a valid gitfile.
		default:
			return false, fmt.Errorf("asking git whether %s is a repository: %w", dir, gerr)
		}
	}
	b.repositories[dir] = is
	return is, nil
}

// shellQuote spells one word for a POSIX shell: as is when every character is
// inert, single-quoted otherwise.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_@%+=:,./-") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// BoundaryCommand is the printed way to run a safegit command inside a
// boundary: a subshell, so the caller's own directory is left alone, rooted at
// the boundary's absolute path, because the caller may be standing in any
// subdirectory. args are spelled as given; the caller quotes paths.
func BoundaryCommand(repoRoot string, bd RepositoryBoundary, args string) string {
	return fmt.Sprintf("(cd %s && safegit %s)", shellQuote(git.Anchor(repoRoot, bd.Dir)), args)
}

// ShellQuote is shellQuote for a caller outside this package.
func ShellQuote(s string) string { return shellQuote(s) }

// boundaryArg is one argument a commit names, as the boundary check sees it.
type boundaryArg struct {
	// arg is the argument as typed; rel its canonical repo-relative path.
	arg string
	rel string
	// hunks is a --hunks selection, untrack an --untrack argument; neither is
	// a plain positional path.
	hunks   []int
	untrack bool
}

// boundaryGroup is every argument that belongs to one boundary.
type boundaryGroup struct {
	bd    RepositoryBoundary
	named []string
	// inner holds the arguments below the boundary, each rel re-rooted at it.
	inner []boundaryArg
	// self holds the arguments naming an unrecorded nested repository itself.
	self []string
}

// refuseInsideOtherRepositories refuses, in one error, every named argument
// that belongs to another repository: one below a boundary, and one naming an
// unrecorded nested repository itself, which `git add` would record as an
// embedded gitlink with no .gitmodules entry. Naming a submodule's own gitlink
// stays a path of this repository -- it is how its pointer moves.
//
// Offenders are grouped by boundary, each argument named as typed, and each
// group carries the command that commits its paths where they belong.
func refuseInsideOtherRepositories(repoRoot string, bounds *RepositoryBoundaries, args []boundaryArg) error {
	var groups []*boundaryGroup
	byDir := make(map[string]*boundaryGroup)
	for _, a := range args {
		bd, below, err := bounds.Above(a.rel)
		if err != nil {
			return err
		}
		self := false
		if !below {
			nested, err := bounds.unrecordedRepositoryAt(a.rel)
			if err != nil {
				return err
			}
			if !nested {
				continue
			}
			bd, self = RepositoryBoundary{Dir: a.rel, Shape: NestedRepository}, true
		}
		g := byDir[bd.Dir]
		if g == nil {
			g = &boundaryGroup{bd: bd}
			byDir[bd.Dir] = g
			groups = append(groups, g)
		}
		g.named = append(g.named, a.arg)
		if self {
			g.self = append(g.self, a.arg)
			continue
		}
		inner := a
		inner.rel = strings.TrimPrefix(a.rel, bd.Dir+"/")
		g.inner = append(g.inner, inner)
	}
	if len(groups) == 0 {
		return nil
	}

	lines := []string{"refusing paths that belong to another git repository; a commit of this repository never records them:"}
	anyInner := false
	for _, g := range groups {
		lines = append(lines, fmt.Sprintf("  %s is %s; named here as %s", g.bd.Dir, g.bd.Describe(), strings.Join(g.named, ", ")))
		if len(g.inner) > 0 {
			anyInner = true
			checkedOut, err := bounds.CheckedOut(g.bd)
			if err != nil {
				return err
			}
			if checkedOut {
				lines = append(lines, "    commit them in that repository: "+BoundaryCommand(repoRoot, g.bd, boundaryCommitArgs(g.inner)))
			} else {
				lines = append(lines, fmt.Sprintf("    %s is not checked out in this working tree, so nothing under it can be committed from here", g.bd.Dir))
			}
		}
		for _, s := range g.self {
			lines = append(lines, fmt.Sprintf("    %s names that repository itself, which is not a submodule of this one: "+
				"leave it out of this commit, or add it to .gitignore", s))
		}
	}
	if anyInner {
		lines = append(lines, "  or leave them out of this commit.")
	}
	return &CommitError{Code: exitcode.PathMatchedNothing, Message: strings.Join(lines, "\n")}
}

// unrecordedRepositoryAt reports whether a repo-relative path is itself a
// nested repository this repository records nowhere -- neither a gitlink in the
// base tree nor a submodule in .gitmodules.
func (b *RepositoryBoundaries) unrecordedRepositoryAt(rel string) (bool, error) {
	if rel == "" {
		return false, nil
	}
	bd, ok, err := b.At(rel)
	if err != nil || !ok {
		return false, err
	}
	return bd.Shape == NestedRepository, nil
}

// boundaryCommitArgs spells the commit that records the same arguments inside
// the boundary, each path re-rooted there.
func boundaryCommitArgs(args []boundaryArg) string {
	// The verb is joined on at the end: this is a safegit command line for a
	// person to run, and a slice opening with it reads as a git argv.
	words := []string{"-m", "<message>"}
	var positional []string
	for _, a := range args {
		switch {
		case a.untrack:
			words = append(words, "--untrack", shellQuote(a.rel))
		case a.hunks != nil:
			nums := make([]string, len(a.hunks))
			for i, h := range a.hunks {
				nums[i] = strconv.Itoa(h)
			}
			words = append(words, "--hunks", shellQuote(a.rel+":"+strings.Join(nums, ",")))
		default:
			positional = append(positional, shellQuote(a.rel))
		}
	}
	if len(positional) > 0 {
		words = append(append(words, "--"), positional...)
	}
	return "commit " + strings.Join(words, " ")
}
