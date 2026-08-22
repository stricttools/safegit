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
// The three are not interchangeable. A TRACKED hook is committed to the
// repository, so everyone who clones it gets it and disabling one means
// committing its deletion. A LOCAL hook lives in the tool-owned directory
// inside the git dir and belongs to this checkout alone. A LEGACY hook is one
// still sitting where safegit used to keep them, in git's own .git/hooks --
// discovery refuses to run from there, and `safegit hook migrate` relocates it.
type Origin string

const (
	OriginTracked Origin = "tracked"
	OriginLocal   Origin = "local"
	OriginLegacy  Origin = "legacy"
)

// Store names one repository's hook stores: its work tree (which holds the
// committed store) and its git directory (which holds the local one and the
// legacy location).
//
// Worktree is empty for a repository that has none -- a bare repository -- in
// which case there is no tracked store to read.
type Store struct {
	Worktree string
	GitDir   string
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

// TrackedDir is the committed hook store inside the work tree.
func TrackedDir(worktree string) string {
	if worktree == "" {
		return ""
	}
	return filepath.Join(worktree, ".safegit", "hooks")
}

// LocalDir is the tool-owned hook store inside the git directory. It is where
// `hook install` writes and where discovery runs hooks from.
func LocalDir(gitDir string) string {
	return filepath.Join(gitDir, "safegit", "hooks")
}

// LegacyFile and LegacyDir are the two names safegit used to keep its hooks
// under in git's own hook directory, before the tool-owned store existed. They
// are the ONLY safegit-owned names there -- everything else in .git/hooks is
// git's own -- which is what lets `hook migrate` relocate them unconditionally,
// with no content sniffing.
func LegacyFile(gitDir string) string {
	return filepath.Join(gitDir, "hooks", "pre-pre-push")
}

// LegacyDir is the directory half of the legacy location (see LegacyFile).
func LegacyDir(gitDir string) string {
	return filepath.Join(gitDir, "hooks", "pre-pre-push.d")
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

	found, err := walkStore(LocalDir(s.GitDir), OriginLocal)
	if err != nil {
		return nil, err
	}
	out = append(out, found...)

	legacy, err := Legacy(s.GitDir)
	if err != nil {
		return nil, err
	}
	return append(out, legacy...), nil
}

// Legacy enumerates the pre-migration location alone: the `pre-pre-push` file
// and everything under `pre-pre-push.d/` in git's own hook directory. Rel is
// relative to that directory, so it reads the same as the corresponding entry
// in a live store.
func Legacy(gitDir string) ([]Location, error) {
	base := filepath.Join(gitDir, "hooks")
	var out []Location

	if info, err := os.Stat(LegacyFile(gitDir)); err == nil && !info.IsDir() {
		out = append(out, Location{
			Path:       LegacyFile(gitDir),
			Rel:        "pre-pre-push",
			Origin:     OriginLegacy,
			Executable: isExecutable(info),
		})
	}

	nested, err := walkStore(LegacyDir(gitDir), OriginLegacy)
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
