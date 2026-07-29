// Package submodule enumerates initialized and deinitialized git submodules, detects parent repos, checks for nesting, and resolves paths through symlinks.
//
// This package intentionally does NOT import other internal/* packages to
// avoid import cycles. Git commands use exec.Command directly -- this is
// the bootstrap exception (same pattern as internal/testutil).
package submodule

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNestedSubmodules is returned when a submodule itself contains submodules.
var ErrNestedSubmodules = errors.New("nested submodules detected")

// SubmoduleInfo describes a discovered submodule within a parent repository.
type SubmoduleInfo struct {
	Name          string
	RelativePath  string
	WorkTreePath  string
	GitDir        string
	SafegitDir    string
	CommitSHA     string
	Initialized   bool
}

// Enumerate discovers all submodules in a repo. parentGitDir is the absolute
// path to the parent's .git directory (e.g. "/repo/.git").
//
// Initialized submodules are found via `git submodule foreach`. Deinitialized
// submodules are found by walking .git/modules/. The two sets are merged with
// initialized taking precedence in case of overlap.
//
// Returns an empty slice (not error) if no submodules exist.
func Enumerate(ctx context.Context, parentGitDir string) ([]SubmoduleInfo, error) {
	parentGitDir = cleanPath(parentGitDir)
	repoRoot := filepath.Dir(parentGitDir)

	initialized, err := enumerateInitialized(ctx, repoRoot, parentGitDir)
	if err != nil {
		return nil, fmt.Errorf("enumerating initialized submodules: %w", err)
	}

	deinitialized, err := enumerateDeinitialized(parentGitDir)
	if err != nil {
		return nil, fmt.Errorf("enumerating deinitialized submodules: %w", err)
	}

	// Merge: initialized wins over deinitialized for the same name.
	seen := make(map[string]struct{}, len(initialized))
	result := make([]SubmoduleInfo, 0, len(initialized)+len(deinitialized))
	for _, info := range initialized {
		seen[info.Name] = struct{}{}
		result = append(result, info)
	}
	for _, info := range deinitialized {
		if _, ok := seen[info.Name]; !ok {
			result = append(result, info)
		}
	}
	return result, nil
}

// CheckNested verifies that no initialized submodule itself contains nested
// submodules (indicated by a .gitmodules file in the submodule's working tree).
func CheckNested(ctx context.Context, parentGitDir string) error {
	subs, err := Enumerate(ctx, parentGitDir)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if !sub.Initialized || sub.WorkTreePath == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(sub.WorkTreePath, ".gitmodules")); err == nil {
			return fmt.Errorf("%w: submodule %q contains nested submodules", ErrNestedSubmodules, sub.RelativePath)
		}
	}
	return nil
}

func enumerateInitialized(ctx context.Context, repoRoot, parentGitDir string) ([]SubmoduleInfo, error) {
	out, err := runGit(ctx, repoRoot, "submodule", "foreach", "--quiet", "echo $sm_path")
	if err != nil {
		// "git submodule foreach" fails if no .gitmodules exists; treat as
		// zero submodules.
		return nil, nil
	}

	paths := splitLines(out)
	if len(paths) == 0 {
		return nil, nil
	}

	var result []SubmoduleInfo
	for _, relPath := range paths {
		absWork := cleanPath(filepath.Join(repoRoot, relPath))

		gitDir, err := resolveSubmoduleGitDir(ctx, absWork)
		if err != nil {
			return nil, fmt.Errorf("resolving git dir for %s: %w", relPath, err)
		}

		sha, err := resolveHead(ctx, absWork)
		if err != nil {
			return nil, fmt.Errorf("resolving HEAD for %s: %w", relPath, err)
		}

		// Submodule name: use the relative path within .git/modules/ if
		// the git dir lives there; otherwise fall back to relPath.
		name := nameFromGitDir(parentGitDir, gitDir)
		if name == "" {
			name = relPath
		}

		result = append(result, SubmoduleInfo{
			Name:         name,
			RelativePath: relPath,
			WorkTreePath: absWork,
			GitDir:       gitDir,
			SafegitDir:   filepath.Join(gitDir, "safegit"),
			CommitSHA:    sha,
			Initialized:  true,
		})
	}
	return result, nil
}

func enumerateDeinitialized(parentGitDir string) ([]SubmoduleInfo, error) {
	modulesDir := filepath.Join(parentGitDir, "modules")
	if _, err := os.Stat(modulesDir); os.IsNotExist(err) {
		return nil, nil
	}

	var result []SubmoduleInfo
	err := walkModulesDir(modulesDir, "", func(name, gitDir string) {
		result = append(result, SubmoduleInfo{
			Name:        name,
			GitDir:      gitDir,
			SafegitDir:  filepath.Join(gitDir, "safegit"),
			Initialized: false,
		})
	})
	return result, err
}

// walkModulesDir recursively walks .git/modules/ to find submodule git dirs.
// A directory is a submodule git dir if it contains a HEAD file.
func walkModulesDir(dir, prefix string, fn func(name, gitDir string)) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		childDir := filepath.Join(dir, entry.Name())
		childName := entry.Name()
		if prefix != "" {
			childName = prefix + "/" + entry.Name()
		}

		headPath := filepath.Join(childDir, "HEAD")
		if _, err := os.Stat(headPath); err == nil {
			fn(childName, childDir)
		}

		// Recurse into nested modules (e.g. .git/modules/sub/modules/nested).
		nestedModules := filepath.Join(childDir, "modules")
		if info, err := os.Stat(nestedModules); err == nil && info.IsDir() {
			if err := walkModulesDir(nestedModules, childName, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

// nameFromGitDir extracts the submodule name from its git dir path relative
// to parentGitDir/modules/. Returns empty string if the git dir doesn't live
// under parentGitDir/modules/.
func nameFromGitDir(parentGitDir, gitDir string) string {
	modulesDir := filepath.Join(parentGitDir, "modules")
	rel, err := filepath.Rel(modulesDir, gitDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// DetectParent checks whether the current working directory is inside a git
// submodule and returns information about the parent repo.
//
// Returns ("", "", false) if not inside a submodule. Returns the immediate
// parent only; callers recurse if needed for nested submodules.
func DetectParent(ctx context.Context) (parentGitDir string, submodulePath string, ok bool) {
	out, err := runGit(ctx, "", "rev-parse", "--show-superproject-working-tree")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", "", false
	}

	parentWorkTree := cleanPath(strings.TrimSpace(out))

	pgd, err := runGit(ctx, parentWorkTree, "rev-parse", "--git-dir")
	if err != nil {
		return "", "", false
	}
	parentGitDir = strings.TrimSpace(pgd)
	if !filepath.IsAbs(parentGitDir) {
		parentGitDir = filepath.Join(parentWorkTree, parentGitDir)
	}
	parentGitDir = cleanPath(parentGitDir)

	// Determine this submodule's relative path within the parent.
	cwd, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	cwd = cleanPath(cwd)
	rel, err := filepath.Rel(parentWorkTree, cwd)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", false
	}
	submodulePath = rel

	return parentGitDir, submodulePath, true
}

// resolveSubmoduleGitDir runs `git rev-parse --git-dir` inside the submodule
// working tree and returns the absolute path.
func resolveSubmoduleGitDir(ctx context.Context, workTree string) (string, error) {
	out, err := runGit(ctx, workTree, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	gd := strings.TrimSpace(out)
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(workTree, gd)
	}
	gd = filepath.Clean(gd)
	return cleanPath(gd), nil
}

func resolveHead(ctx context.Context, workTree string) (string, error) {
	out, err := runGit(ctx, workTree, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// runGit executes a git command in the given directory. If dir is empty, the
// command inherits the current working directory.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	fullArgs := append([]string{"--no-optional-locks"}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	if dir != "" {
		cmd.Dir = dir
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(args, " "), err, strings.TrimSpace(errBuf.String()))
	}
	return outBuf.String(), nil
}

// cleanPath resolves symlinks in p, returning the resolved path or the
// original unchanged if resolution fails. This ensures consistent paths on
// systems where temp directories are symlinked (e.g. macOS /tmp -> /private/...).
func cleanPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func splitLines(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
