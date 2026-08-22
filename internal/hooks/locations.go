package hooks

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Origin says which store a hook location came from.
//
// The three are not interchangeable. A TRACKED hook is repository-provided: it
// sits in the checkout's .safegit/hooks, so everyone who clones the repository
// gets it and disabling one means committing its deletion. A LOCAL hook lives
// in the tool-owned directory under the common git dir and belongs to this
// repository's own state, shared by every worktree of it. A LEGACY hook is one
// still sitting where safegit used to keep them, in git's own .git/hooks --
// discovery refuses to run from there, and `safegit hook migrate` relocates it.
//
// # Tracked membership is the directory, not git
//
// A location is TRACKED because it is IN .safegit/hooks on disk, never because
// git tracks it: an uncommitted -- even gitignored -- executable file in that
// directory runs on the next push exactly like a committed one. Probing
// git-tracked-ness instead would make a hook an operator just wrote silently
// invisible to `hook list` and to discovery while it kept running, which is a
// worse failure than the one it would prevent.
//
// # The execution boundary
//
// `safegit push` executes the scripts in the checkout's .safegit/hooks, so
// cloning a repository and pushing from that checkout runs the repository's
// committed code. Execution happens only on push and on `safegit hook run` --
// an operator action with push intent -- never on clone, fetch, checkout or any
// inspection command, and `hook list` names every location with its origin
// precisely so the set can be read before anything is pushed.
type Origin string

const (
	OriginTracked Origin = "tracked"
	OriginLocal   Origin = "local"
	OriginLegacy  Origin = "legacy"
)

// Store names one repository's hook stores: its work tree (which holds the
// tracked store) and its COMMON git directory (which holds the live store and
// the legacy location).
//
// Worktree is empty for a repository that has none -- a bare repository -- in
// which case there is no tracked store to read.
type Store struct {
	// Worktree is this checkout's work tree. The tracked store is checkout
	// content, so it is per-worktree by nature: a linked worktree runs the
	// hooks ITS checkout has.
	Worktree string
	// SharedGitDir is the repository's COMMON git directory, never a linked
	// worktree's own -- repo.SharedGitDir is the resolution every caller uses.
	// The live store is repository-level policy, exactly like the ref locks
	// that already live under the same directory, so a hook installed from one
	// worktree is the hook every worktree runs. Git's hook directory is common
	// as well, which puts the legacy location here too.
	SharedGitDir string
}

// Location is one file found in a hook store. It is a LOCATION and nothing
// more: whether the file is eligible to run is Discover's question, not this
// type's, so a non-executable file, an editor backup and a dot-file are all
// enumerated exactly like any other entry.
type Location struct {
	// Path is the absolute path of the file.
	Path string
	// Rel is the path relative to its store's root, slash-separated. It is the
	// name a hook is addressed by: `pre-pre-push`, `pre-pre-push.d/20-lint`.
	Rel string
	// Origin is the store the file was found in.
	Origin Origin
	// Executable reports whether any execute bit is set.
	Executable bool
}

// TrackedDir is the repository-provided hook store inside the work tree. Its
// membership is the directory itself: a file there runs whether or not git
// tracks it (see Origin).
func TrackedDir(worktree string) string {
	if worktree == "" {
		return ""
	}
	return filepath.Join(worktree, ".safegit", "hooks")
}

// LocalDir is the tool-owned live hook store under the COMMON git directory. It
// is where `hook install` writes and where discovery runs hooks from.
//
// The argument is the shared git dir (repo.SharedGitDir), never a linked
// worktree's own: the store is one repository-wide answer, alongside the ref
// locks in the same .git/safegit.
func LocalDir(sharedGitDir string) string {
	return filepath.Join(sharedGitDir, "safegit", "hooks")
}

// LegacyFile and LegacyDir are the two names safegit used to keep its hooks
// under in git's own hook directory, before the tool-owned store existed. They
// are the ONLY safegit-owned names there -- everything else in .git/hooks is
// git's own -- which is what lets `hook migrate` relocate them unconditionally,
// with no content sniffing.
//
// The argument is the shared git dir for the same reason git's own hook
// directory is common in a linked worktree: there is one such location per
// repository, and every worktree must reach the same one. It is deliberately
// NOT git's resolved hook directory (core.hooksPath): safegit only ever wrote
// these two names into <common>/hooks, so a repository that redirects
// core.hooksPath still has its legacy hooks here.
func LegacyFile(sharedGitDir string) string {
	return filepath.Join(sharedGitDir, "hooks", "pre-pre-push")
}

// LegacyDir is the directory half of the legacy location (see LegacyFile).
func LegacyDir(sharedGitDir string) string {
	return filepath.Join(sharedGitDir, "hooks", "pre-pre-push.d")
}

// Enumerate is the single authority for where hooks live.
//
// It walks all three stores recursively and returns every FILE it finds, with
// no executability, naming or extension filter of any kind: the answer is the
// set of locations, and every consumer that needs a narrower set derives it
// here rather than re-deriving the directory layout. `hook list` shows
// non-executable entries because it reads this; `Discover` runs a subset of it;
// `scan` sweeps all of it; doctor's permission check reads it.
//
// Order is store-major -- tracked, then local, then legacy -- and sorted by Rel
// within each store, which is the execution order Discover inherits.
//
// A store that does not exist contributes nothing and is not an error.
func Enumerate(s Store) ([]Location, error) {
	var out []Location

	if dir := TrackedDir(s.Worktree); dir != "" {
		found, err := walkStore(dir, OriginTracked)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}

	found, err := walkStore(LocalDir(s.SharedGitDir), OriginLocal)
	if err != nil {
		return nil, err
	}
	out = append(out, found...)

	legacy, err := Legacy(s.SharedGitDir)
	if err != nil {
		return nil, err
	}
	return append(out, legacy...), nil
}

// Legacy enumerates the pre-migration location alone: the `pre-pre-push` file
// and everything under `pre-pre-push.d/` in git's own hook directory, which is
// the COMMON one (see LegacyFile). Rel is relative to that directory, so it
// reads the same as the corresponding entry in a live store.
func Legacy(sharedGitDir string) ([]Location, error) {
	base := filepath.Join(sharedGitDir, "hooks")
	var out []Location

	if info, err := os.Stat(LegacyFile(sharedGitDir)); err == nil && !info.IsDir() {
		out = append(out, Location{
			Path:       LegacyFile(sharedGitDir),
			Rel:        "pre-pre-push",
			Origin:     OriginLegacy,
			Executable: isExecutable(info),
		})
	}

	nested, err := walkStore(LegacyDir(sharedGitDir), OriginLegacy)
	if err != nil {
		return nil, err
	}
	// walkStore reports paths relative to the directory it walked; the legacy
	// vocabulary addresses them under the .d name, relative to .git/hooks.
	for i := range nested {
		rel, relErr := filepath.Rel(base, nested[i].Path)
		if relErr != nil {
			return nil, fmt.Errorf("locating %s under %s: %w", nested[i].Path, base, relErr)
		}
		nested[i].Rel = filepath.ToSlash(rel)
	}
	return append(out, nested...), nil
}

// walkStore returns every file under root, recursively, sorted by the path
// relative to root. An absent root is an empty store, not an error.
func walkStore(root string, origin Origin) ([]Location, error) {
	if root == "" {
		return nil, nil
	}
	var out []Location
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		// os.Stat rather than d.Info: a symlinked hook is executable when its
		// TARGET is, which is what running it will find out.
		info, statErr := os.Stat(path)
		if statErr != nil {
			// A dangling symlink is a location that exists and cannot run. It
			// is reported as non-executable rather than dropped, so `hook list`
			// can show the operator what is there.
			out = append(out, Location{Path: path, Rel: relSlash(root, path), Origin: origin})
			return nil
		}
		if info.IsDir() {
			return nil
		}
		out = append(out, Location{
			Path:       path,
			Rel:        relSlash(root, path),
			Origin:     origin,
			Executable: isExecutable(info),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("enumerating hooks in %s: %w", root, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

// relSlash renders path relative to root with forward slashes, falling back to
// the base name when the two are unrelated (which the walk cannot produce).
func relSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

// IsHookName reports whether a location is a hook at all, by NAME alone:
// dot-prefixed files are hidden and tilde-suffixed ones are editor backups, and
// neither has ever been a hook. Executability is a separate question with a
// different answer per store, so it is not asked here.
func (l Location) IsHookName() bool {
	base := filepath.Base(l.Rel)
	return !strings.HasPrefix(base, ".") && !strings.HasSuffix(base, "~")
}
