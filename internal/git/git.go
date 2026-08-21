// Package git wraps os/exec calls to the git binary and is the sole interface through which safegit interacts with git plumbing commands.
// All functions shell out to git and return structured results; no other package may invoke git directly.
package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/gitexec"
	"github.com/smm-h/safegit/internal/gitversion"
)

// WithDir returns a context that carries git directory overrides. All git
// functions that receive this context will automatically set GIT_DIR,
// GIT_WORK_TREE, and cmd.Dir on the subprocess, targeting the specified
// repo regardless of the process's current working directory.
//
// The override itself lives in internal/gitexec, the one place that builds a
// git subprocess; this is the plumbing interface's spelling of it.
func WithDir(ctx context.Context, gitDir, workTree string) context.Context {
	return gitexec.WithDir(ctx, gitDir, workTree)
}

// WithRoot returns a context carrying the repository-root working-directory
// pin. See gitexec.WithRoot for what the pin is for.
func WithRoot(ctx context.Context, root string) context.Context {
	return gitexec.WithRoot(ctx, root)
}

// Version returns the version of the git binary safegit is running against,
// parsed. It is the one place a caller asks; a feature with a version floor
// compares this against its floor via gitversion.Require.
func Version(ctx context.Context) (gitversion.Version, error) {
	out, stderr, err := Run(ctx, "--version")
	if err != nil {
		return gitversion.Version{}, fmt.Errorf("running git --version: %w: %s", err, strings.TrimSpace(stderr))
	}
	return gitversion.Parse(out)
}

// Run executes a git command and returns stdout, stderr, and any error.
func Run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	return RunWithEnv(ctx, nil, args...)
}

// RunWithEnv executes a git command with additional environment variables.
func RunWithEnv(ctx context.Context, env []string, args ...string) (stdout, stderr string, err error) {
	return runCaptured(ctx, gitexec.Spec{Args: args, Env: env}, nil)
}

// RunWithEnvStdin executes a git command with environment variables and stdin data.
func RunWithEnvStdin(ctx context.Context, env []string, stdin []byte, args ...string) (stdout, stderr string, err error) {
	return runCaptured(ctx, gitexec.Spec{Args: args, Env: env}, stdin)
}

// runCaptured builds one git subprocess through the execution boundary, runs it
// with stdout and stderr captured, and wraps a failure with the argv and git's
// own stderr. Every capturing git call in this package funnels through it.
func runCaptured(ctx context.Context, spec gitexec.Spec, stdin []byte) (stdout, stderr string, err error) {
	cmd, err := gitexec.Command(ctx, spec)
	if err != nil {
		return "", "", err
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	err = cmd.Run()
	stdout = outBuf.String()
	stderr = errBuf.String()

	if err != nil {
		err = fmt.Errorf("git %s: %w\nstderr: %s", strings.Join(spec.Args, " "), err, strings.TrimSpace(stderr))
	}
	return
}

// AnchorRoot returns the directory that repo-relative paths reported by git in
// this context resolve against.
//
// Pinning the git SUBPROCESS working directory does not change how Go resolves
// a relative path: an os.Lstat, os.ReadFile or os.WriteFile on a path git just
// listed still resolves against the PROCESS working directory. From a
// subdirectory the two disagree, and the file the syscall reaches is not the
// file git named -- which is how a protection that reads a git listing and then
// touches the filesystem silently protects nothing. Every filesystem syscall
// that consumes a git-listed path goes through Anchor(AnchorRoot(ctx), path).
//
// The order is most-specific-first: a context targeting another repository
// anchors at that repository's work tree, a pinned context at the pin, and an
// unpinned context at whatever the repository root is from here.
func AnchorRoot(ctx context.Context) (string, error) {
	if _, workTree, ok := gitexec.DirOverride(ctx); ok && workTree != "" {
		return workTree, nil
	}
	if root, ok := gitexec.Root(ctx); ok {
		return root, nil
	}
	return RepoRoot(ctx)
}

// Anchor joins a repo-relative path onto root. An absolute path is returned
// unchanged: a caller that already resolved a path must not have it re-rooted.
func Anchor(root, repoRelative string) string {
	if repoRelative == "" || filepath.IsAbs(repoRelative) {
		return repoRelative
	}
	return filepath.Join(root, repoRelative)
}

// RepoRoot returns the absolute path to the repository root.
func RepoRoot(ctx context.Context) (string, error) {
	out, _, err := Run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// GitDir returns the path to the .git directory.
func GitDir(ctx context.Context) (string, error) {
	out, _, err := Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ErrDetachedHead is returned when HEAD is not on a branch.
var ErrDetachedHead = fmt.Errorf("HEAD is detached (not on a branch); check out a branch first or use --branch")

// HeadRef returns the current branch ref (e.g. "refs/heads/main").
// Returns ErrDetachedHead if HEAD is not on a branch.
func HeadRef(ctx context.Context) (string, error) {
	out, _, err := Run(ctx, "symbolic-ref", "HEAD")
	if err != nil {
		return "", ErrDetachedHead
	}
	return strings.TrimSpace(out), nil
}

// RevParse resolves a revision to a full SHA.
func RevParse(ctx context.Context, rev string) (string, error) {
	out, _, err := Run(ctx, "rev-parse", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ReadTree populates a temporary index from a treeish (commit/tree SHA or ref).
func ReadTree(ctx context.Context, indexPath, treeish string) error {
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	_, _, err := RunWithEnv(ctx, env, "read-tree", treeish)
	return err
}

// WriteTree writes the index content as a tree object, returns the tree SHA.
func WriteTree(ctx context.Context, indexPath string) (string, error) {
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	out, _, err := RunWithEnv(ctx, env, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitTree creates a commit object from a tree SHA and parent, returns commit SHA.
// If parentSHA is empty, creates a root commit.
func CommitTree(ctx context.Context, treeSHA, parentSHA, message string) (string, error) {
	args := []string{"commit-tree", treeSHA, "-m", message}
	if parentSHA != "" {
		args = []string{"commit-tree", treeSHA, "-p", parentSHA, "-m", message}
	}
	out, _, err := Run(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ZeroSHA is git's "this object must not exist" convention: the all-zero object
// name. Passed to update-ref as the expected old value it means "create only" --
// git refuses with "reference already exists" when the ref is already there.
const ZeroSHA = "0000000000000000000000000000000000000000"

// ErrNoExpectedValue is returned by UpdateRef and DeleteRef when the caller
// supplies no expected old value.
//
// It used to mean "omit the old-value argument", which is git's spelling of an
// UNCONDITIONAL write: the ref moved to whatever the caller computed no matter
// what another process had done to it in the meantime. That is precisely the
// compare-and-swap safegit exists to provide, so the empty string is now a
// refusal rather than a mode. A caller that means "this ref must not exist yet"
// says so with ZeroSHA.
var ErrNoExpectedValue = errors.New("update-ref requires an expected old value; pass git.ZeroSHA to require that the ref does not exist yet")

// UpdateRef atomically updates a ref using compare-and-swap.
//
// oldSHA is the expected current value and is MANDATORY. Pass ZeroSHA to
// require that the ref does not exist yet, which git enforces by refusing with
// "reference already exists".
func UpdateRef(ctx context.Context, ref, newSHA, oldSHA string) error {
	if oldSHA == "" {
		return ErrNoExpectedValue
	}
	_, _, err := Run(ctx, "update-ref", ref, newSHA, oldSHA)
	return err
}

// DeleteRef atomically deletes a ref using compare-and-swap.
//
// oldSHA is the expected current value and is MANDATORY, for the same reason it
// is on UpdateRef: without it git deletes whatever the ref points at now.
func DeleteRef(ctx context.Context, ref, oldSHA string) error {
	if oldSHA == "" {
		return ErrNoExpectedValue
	}
	_, _, err := Run(ctx, "update-ref", "-d", ref, oldSHA)
	return err
}

// AddFile stages a file into a custom index.
func AddFile(ctx context.Context, indexPath, filePath string) error {
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	_, _, err := RunWithEnv(ctx, env, "add", "--", filePath)
	return err
}

// RmCached removes a file or directory from a custom index without touching the working tree.
func RmCached(ctx context.Context, indexPath, filePath string) error {
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	_, _, err := RunWithEnv(ctx, env, "rm", "--cached", "--", filePath)
	if err != nil && isDirectoryRmError(err) {
		_, _, err = RunWithEnv(ctx, env, "rm", "-r", "--cached", "--", filePath)
	}
	return err
}

func isDirectoryRmError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not removing") && strings.Contains(err.Error(), "recursively without -r")
}

// IsTracked checks whether a file is tracked by git (present in HEAD tree).
// Uses cat-file instead of ls-files because safegit never writes to the main
// index -- files committed via safegit exist in HEAD but not in .git/index.
func IsTracked(ctx context.Context, filePath string) (bool, error) {
	_, _, err := Run(ctx, "cat-file", "-e", "HEAD:"+filePath)
	if err != nil {
		// Non-zero exit means the object doesn't exist in HEAD
		return false, nil
	}
	return true, nil
}

// ListSkipWorktreeFiles returns the paths of all files with the skip-worktree
// flag set in the main index. It parses `git ls-files -v` output, selecting
// lines that start with "S " (the skip-worktree indicator).
func ListSkipWorktreeFiles(ctx context.Context) ([]string, error) {
	out, _, err := Run(ctx, "ls-files", "-v")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "S ") {
			files = append(files, line[2:])
		}
	}
	return files, nil
}

// ListTrackedIgnoredFiles returns the paths of all files that are tracked in
// the index but ignored by .gitignore rules. These are files that were once
// committed and later gitignored -- read-tree --reset -u would overwrite them,
// destroying local modifications (e.g., config files with secrets).
func ListTrackedIgnoredFiles(ctx context.Context) ([]string, error) {
	out, _, err := Run(ctx, "ls-files", "-i", "-c", "-z", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range strings.Split(out, "\x00") {
		if entry != "" {
			files = append(files, entry)
		}
	}
	return files, nil
}

// syncMainIndexInner updates the main .git/index to match the given treeish.
// When updateWorktree is true, it also checks out files into the working tree
// (using --reset -u), which is needed after history rewrites like scrub.
// Skip-worktree flags are preserved across the read-tree rebuild.
func syncMainIndexInner(ctx context.Context, treeish string, updateWorktree bool) error {
	// Collect skip-worktree files before read-tree clears the index.
	skipFiles, err := ListSkipWorktreeFiles(ctx)
	if err != nil {
		// Non-fatal: proceed without preservation.
		skipFiles = nil
	}

	// Empty treeish means root commit undo: clear the index entirely.
	if treeish == "" {
		_, _, err = Run(ctx, "read-tree", "--empty")
		if err != nil {
			return err
		}
	} else {
		args := []string{"read-tree"}
		if updateWorktree {
			args = append(args, "--reset", "-u")
		}
		args = append(args, treeish)

		_, _, err = Run(ctx, args...)
		if err != nil {
			return err
		}
	}

	// Restore skip-worktree flags.
	for _, f := range skipFiles {
		if _, _, rerr := Run(ctx, "update-index", "--skip-worktree", f); rerr != nil {
			// Non-fatal: log and continue with remaining files.
			fmt.Fprintf(os.Stderr, "safegit: warning: failed to restore skip-worktree on %s: %v\n", f, rerr)
		}
	}
	return nil
}

// SyncMainIndex updates the main .git/index to match the given treeish.
// This makes git status/diff reflect the committed state after safegit commits.
// Skip-worktree flags are preserved across the read-tree rebuild.
func SyncMainIndex(ctx context.Context, treeish string) error {
	return syncMainIndexInner(ctx, treeish, false)
}

// SyncMainIndexWithWorktree updates the main .git/index AND the working tree
// to match the given treeish. Uses --reset -u, so the working tree must be
// clean before calling. Needed after history rewrites (scrub) where committed
// blobs have changed and the working tree must reflect the new content.
//
// Tracked+gitignored files (committed then later gitignored, e.g., config
// files with secrets) are protected: skip-worktree is set before read-tree
// so --reset -u does not overwrite them. Pre-existing skip-worktree flags
// are also preserved.
//
// Returns the list of protected tracked+gitignored paths (empty if none).
func SyncMainIndexWithWorktree(ctx context.Context, treeish string) ([]string, error) {
	// 1. Save existing skip-worktree files.
	origSkip, err := ListSkipWorktreeFiles(ctx)
	if err != nil {
		// Non-fatal: proceed without preservation.
		origSkip = nil
	}
	origSkipSet := make(map[string]struct{}, len(origSkip))
	for _, f := range origSkip {
		origSkipSet[f] = struct{}{}
	}

	// 2. Collect tracked+gitignored paths.
	trackedIgnored, err := ListTrackedIgnoredFiles(ctx)
	if err != nil {
		// Non-fatal: fall through to fast path (no protection).
		trackedIgnored = nil
	}

	// 3. Fast path: no tracked+gitignored files.
	if len(trackedIgnored) == 0 {
		_, _, err = Run(ctx, "read-tree", "--reset", "-u", treeish)
		if err != nil {
			return nil, err
		}
		// Restore pre-existing skip-worktree flags.
		for _, f := range origSkip {
			if _, _, rerr := Run(ctx, "update-index", "--skip-worktree", f); rerr != nil {
				fmt.Fprintf(os.Stderr, "safegit: warning: failed to restore skip-worktree on %s: %v\n", f, rerr)
			}
		}
		return nil, nil
	}

	// 4. Slow path: save on-disk content of tracked+gitignored files.
	// git read-tree --reset -u does not respect skip-worktree when the
	// index blob differs from the tree blob, so skip-worktree alone is
	// insufficient. We save content before read-tree and restore after.
	//
	// The paths come from `git ls-files`, so they are repo-relative and must be
	// anchored before any filesystem syscall: resolving them against the process
	// working directory would reach the wrong file (or none) from a
	// subdirectory, and the protection would silently do nothing.
	anchor, aerr := AnchorRoot(ctx)
	if aerr != nil {
		return nil, fmt.Errorf("resolving repository root to protect tracked-but-ignored files: %w", aerr)
	}
	type savedFile struct {
		path    string
		content []byte
		mode    os.FileMode
	}
	var saved []savedFile
	for _, f := range trackedIgnored {
		abs := Anchor(anchor, f)
		info, serr := os.Lstat(abs)
		if serr != nil {
			continue // file doesn't exist on disk, nothing to save
		}
		if !info.Mode().IsRegular() {
			continue // skip symlinks, directories, etc.
		}
		content, rerr := os.ReadFile(abs)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "safegit: warning: failed to save %s before read-tree: %v\n", f, rerr)
			continue
		}
		saved = append(saved, savedFile{path: abs, content: content, mode: info.Mode().Perm()})
	}

	// Run read-tree --reset -u.
	_, _, err = Run(ctx, "read-tree", "--reset", "-u", treeish)
	if err != nil {
		return nil, err
	}

	// Restore on-disk content of tracked+gitignored files.
	for _, sf := range saved {
		if werr := os.WriteFile(sf.path, sf.content, sf.mode); werr != nil {
			fmt.Fprintf(os.Stderr, "safegit: warning: failed to restore %s after read-tree: %v\n", sf.path, werr)
		}
	}

	// Restore pre-existing skip-worktree flags.
	for _, f := range origSkip {
		if _, _, rerr := Run(ctx, "update-index", "--skip-worktree", f); rerr != nil {
			fmt.Fprintf(os.Stderr, "safegit: warning: failed to restore skip-worktree on %s: %v\n", f, rerr)
		}
	}

	return trackedIgnored, nil
}

// RunPassthrough executes a git command with stdin/stdout/stderr wired to
// the terminal (os.Stdin, os.Stdout, os.Stderr). It prepends --no-optional-locks
// like Run, but does not capture output -- suitable for interactive/pager commands.
//
// This is the route for argv the OPERATOR wrote (cherry-pick, revert), so it
// carries the declared operator-cwd exemption from the repository-root pin:
// git must resolve the operator's own pathspecs in the operator's own
// directory. A context-carried WithDir override still applies.
func RunPassthrough(ctx context.Context, args ...string) error {
	cmd, err := gitexec.Command(
		gitexec.WithoutRootPin(ctx, gitexec.ExemptGuardedPassthrough),
		gitexec.Spec{Args: args},
	)
	if err != nil {
		return err
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CommonGitDir returns the path to the shared .git directory.
// For normal repos this equals GitDir(); for worktrees it returns the main
// .git dir that is shared across all worktrees. Lock files should live here
// so that worktrees committing to the same branch serialize correctly.
func CommonGitDir(ctx context.Context) (string, error) {
	out, _, err := Run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommonGitDirOf returns the common git directory for a given gitDir.
//
// Unlike CommonGitDir, this does not depend on the process working directory:
// the repository is an argument, so the call goes through RunWithGitDir -- the
// declared explicit-directory exemption from the repository-root pin -- which
// sets GIT_DIR and runs git in that directory. An absolute gitDir therefore
// yields an absolute answer; a relative one yields an answer relative to gitDir
// itself, never to this process's working directory.
func CommonGitDirOf(ctx context.Context, gitDir string) (string, error) {
	out, _, err := RunWithGitDir(ctx, gitDir, "", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsIgnored checks whether a file matches a gitignore rule.
func IsIgnored(ctx context.Context, filePath string) (bool, error) {
	_, _, err := Run(ctx, "check-ignore", "-q", "--", filePath)
	if err != nil {
		// Exit code 1 means not ignored; exit code 128 means path error
		return false, nil
	}
	return true, nil
}

// IsAncestorOf checks whether commitSHA is an ancestor of (or equal to)
// descendantSHA. Uses git merge-base --is-ancestor which exits 0 if true,
// 1 if false, and other codes on error.
func IsAncestorOf(ctx context.Context, commitSHA, descendantSHA string) (bool, error) {
	_, _, err := Run(ctx, "merge-base", "--is-ancestor", commitSHA, descendantSHA)
	if err == nil {
		return true, nil
	}
	// Exit code 1 means "not an ancestor" -- that's a valid false result.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// CommitMessage returns the full commit message of the given revision.
func CommitMessage(ctx context.Context, rev string) (string, error) {
	out, _, err := Run(ctx, "log", "-1", "--format=%B", rev)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(out, "\n"), nil
}

// AuthorInfo holds the name, email, and raw git date for an author or committer.
type AuthorInfo struct {
	Name  string
	Email string
	Date  string // raw git date format: "1234567890 +0200"
}

// CommitInfo holds the parsed contents of a git commit object.
type CommitInfo struct {
	Tree      string
	Parents   []string
	Author    AuthorInfo
	Committer AuthorInfo
	Message   string
}

// parseIdentity parses a raw author/committer line value into AuthorInfo.
// Format: "Name Here <email@example.com> 1234567890 +0200"
func parseIdentity(raw string) AuthorInfo {
	// Name = everything before the last " <"
	// Email = content between "<" and ">"
	// Date = everything after "> "
	ltIdx := strings.LastIndex(raw, " <")
	if ltIdx < 0 {
		return AuthorInfo{Name: raw}
	}
	name := raw[:ltIdx]
	rest := raw[ltIdx+2:] // after " <"

	gtIdx := strings.Index(rest, ">")
	if gtIdx < 0 {
		return AuthorInfo{Name: name}
	}
	email := rest[:gtIdx]
	date := strings.TrimSpace(rest[gtIdx+1:])

	return AuthorInfo{Name: name, Email: email, Date: date}
}

// ParseCommit reads and parses a commit object by SHA using git cat-file.
func ParseCommit(ctx context.Context, sha string) (CommitInfo, error) {
	out, _, err := Run(ctx, "cat-file", "-p", sha)
	if err != nil {
		return CommitInfo{}, err
	}

	// Split into header section and body at the first blank line.
	var info CommitInfo
	headerEnd := strings.Index(out, "\n\n")
	var headerSection, body string
	if headerEnd < 0 {
		headerSection = out
	} else {
		headerSection = out[:headerEnd]
		body = out[headerEnd+2:] // skip the "\n\n"
	}

	// Trim exactly one trailing newline from the message if present.
	if strings.HasSuffix(body, "\n") {
		body = body[:len(body)-1]
	}
	info.Message = body

	// Parse header lines. Continuation lines (starting with space) belong
	// to the previous header and are skipped.
	lines := strings.Split(headerSection, "\n")
	for _, line := range lines {
		if len(line) > 0 && line[0] == ' ' {
			// Continuation of a multi-line header (e.g. gpgsig); skip.
			continue
		}
		spIdx := strings.IndexByte(line, ' ')
		if spIdx < 0 {
			continue
		}
		key := line[:spIdx]
		val := line[spIdx+1:]

		switch key {
		case "tree":
			info.Tree = val
		case "parent":
			info.Parents = append(info.Parents, val)
		case "author":
			info.Author = parseIdentity(val)
		case "committer":
			info.Committer = parseIdentity(val)
			// gpgsig and other unknown headers are ignored.
		}
	}

	return info, nil
}

// CommitTreeWithAuthor creates a commit object with explicit author and committer
// identity, returning the new commit SHA.
func CommitTreeWithAuthor(ctx context.Context, treeSHA string, parentSHAs []string, message string, author, committer AuthorInfo) (string, error) {
	args := []string{"commit-tree", treeSHA}
	for _, p := range parentSHAs {
		args = append(args, "-p", p)
	}
	args = append(args, "-m", message)

	env := []string{
		"GIT_AUTHOR_NAME=" + author.Name,
		"GIT_AUTHOR_EMAIL=" + author.Email,
		"GIT_AUTHOR_DATE=" + author.Date,
		"GIT_COMMITTER_NAME=" + committer.Name,
		"GIT_COMMITTER_EMAIL=" + committer.Email,
		"GIT_COMMITTER_DATE=" + committer.Date,
	}

	out, _, err := RunWithEnv(ctx, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// TreeEntry represents an entry from git ls-tree (blob, tree, or other object).
type TreeEntry struct {
	SHA        string // SHA of the object (blob or tree)
	Path       string // repo-relative path (full path for recursive, basename for non-recursive)
	Mode       string // file mode (e.g. "100644", "040000")
	ObjectType string // object type (e.g. "blob", "tree")
}

// parseLsTreeOutput parses NUL-delimited git ls-tree output into TreeEntry
// slices. When blobOnly is true, non-blob entries are skipped.
func parseLsTreeOutput(out string, blobOnly bool) []TreeEntry {
	if strings.TrimSpace(out) == "" {
		return nil
	}

	var entries []TreeEntry
	for _, raw := range strings.Split(out, "\x00") {
		if raw == "" {
			continue
		}
		// Format: "<mode> <type> <sha>\t<path>"
		tabIdx := strings.IndexByte(raw, '\t')
		if tabIdx < 0 {
			continue
		}
		path := raw[tabIdx+1:]
		meta := raw[:tabIdx] // "<mode> <type> <sha>"

		fields := strings.SplitN(meta, " ", 3)
		if len(fields) < 3 {
			continue
		}
		objType := fields[1]
		if blobOnly && objType != "blob" {
			continue
		}
		entries = append(entries, TreeEntry{
			SHA:        fields[2],
			Path:       path,
			Mode:       fields[0],
			ObjectType: objType,
		})
	}
	return entries
}

// LsTreeAll returns all blob entries in the given treeish, recursively.
// Empty trees return an empty slice, not an error.
//
// --full-tree is not optional here. Without it git resolves a tree listing
// against the process working directory PREFIX: from a subdirectory,
// `ls-tree <root-tree>` returns that subdirectory's entries with the prefix
// stripped, and a caller that rebuilds a tree from the result promotes the
// subdirectory to the repository root and deletes everything outside it.
// Pinning the working directory alone does not fix this, because the *WithDir
// family and any future caller can still run somewhere else; the flag makes the
// listing repository-rooted no matter where the process stands.
func LsTreeAll(ctx context.Context, treeish string) ([]TreeEntry, error) {
	out, _, err := Run(ctx, "ls-tree", "--full-tree", "-r", "-z", treeish)
	if err != nil {
		return nil, fmt.Errorf("ls-tree %s: %w", treeish, err)
	}
	return parseLsTreeOutput(out, true), nil
}

// LsTree returns all entries (blobs and subtrees) at one level of the given
// treeish, without recursing into subtrees. Each entry includes Mode and
// ObjectType so callers can distinguish blobs from trees.
// --full-tree is mandatory for the same reason it is on LsTreeAll: a listing
// resolved against the working-directory prefix is a listing of the wrong tree.
func LsTree(ctx context.Context, treeish string) ([]TreeEntry, error) {
	out, _, err := Run(ctx, "ls-tree", "--full-tree", "-z", treeish)
	if err != nil {
		return nil, fmt.Errorf("ls-tree %s: %w", treeish, err)
	}
	return parseLsTreeOutput(out, false), nil
}

// HashObject returns the blob SHA for a file without writing to the object store.
func HashObject(ctx context.Context, path string) (string, error) {
	out, _, err := Run(ctx, "hash-object", "--", path)
	if err != nil {
		return "", fmt.Errorf("hash-object %s: %w", path, err)
	}
	return strings.TrimSpace(out), nil
}

// HashObjectWrite hashes a file and writes the blob to the object store,
// returning the blob SHA.
func HashObjectWrite(ctx context.Context, path string) (string, error) {
	out, _, err := Run(ctx, "hash-object", "-w", "--", path)
	if err != nil {
		return "", fmt.Errorf("hash-object -w %s: %w", path, err)
	}
	return strings.TrimSpace(out), nil
}

// HashObjectWriteBytes writes in-memory bytes as a blob to the object store
// via git hash-object -w --stdin, returning the blob SHA.
func HashObjectWriteBytes(ctx context.Context, data []byte) (string, error) {
	out, _, err := RunWithEnvStdin(ctx, nil, data, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CatFileBlob reads blob content by SHA via git cat-file -p.
func CatFileBlob(ctx context.Context, sha string) ([]byte, error) {
	out, _, err := Run(ctx, "cat-file", "-p", sha)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// MkTree creates a tree object from a slice of TreeEntry values and returns
// the tree SHA. Each entry must have Mode, ObjectType, SHA, and Path
// populated. Input is piped to `git mktree` as "<mode> <type> <sha>\t<name>\n".
func MkTree(ctx context.Context, entries []TreeEntry) (string, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&buf, "%s %s %s\t%s\n", e.Mode, e.ObjectType, e.SHA, e.Path)
	}
	out, _, err := RunWithEnvStdin(ctx, nil, buf.Bytes(), "mktree")
	if err != nil {
		return "", fmt.Errorf("mktree: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// ObjectEntry holds one object read from a git cat-file --batch stream.
type ObjectEntry struct {
	SHA     string
	Type    string // "blob", "commit", or "tag" (trees are skipped)
	Size    int
	Content []byte
}

// ObjectIterator streams objects from a long-running git cat-file process.
type ObjectIterator struct {
	cmd    *exec.Cmd
	stdout *bufio.Reader
	stderr bytes.Buffer
}

// CatFileBatchAll starts a git cat-file --batch-all-objects --batch subprocess
// and returns an ObjectIterator for streaming the results. The caller must call
// Close() when done. Respects WithDir context overrides.
func CatFileBatchAll(ctx context.Context) (*ObjectIterator, error) {
	cmd, err := gitexec.Command(ctx, gitexec.Spec{
		Args: []string{"cat-file", "--batch-all-objects", "--batch"},
	})
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cat-file --batch-all-objects: stdout pipe: %w", err)
	}
	it := &ObjectIterator{
		cmd:    cmd,
		stdout: bufio.NewReaderSize(stdout, 256*1024),
	}
	cmd.Stderr = &it.stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cat-file --batch-all-objects: start: %w", err)
	}
	return it, nil
}

// CatFileBatchSHAs starts a git cat-file --batch subprocess that reads only
// the specified SHAs, and returns an ObjectIterator for streaming the results.
// Unlike CatFileBatchAll (which enumerates all objects), this feeds specific
// SHAs via stdin using bytes.NewReader to avoid pipe deadlock: if output
// exceeds the OS pipe buffer (~64KB), git blocks on stdout write while the
// caller is still writing to stdin. With bytes.NewReader, git reads stdin
// from memory at its own pace. The caller must call Close() when done.
func CatFileBatchSHAs(ctx context.Context, shas []string) (*ObjectIterator, error) {
	input := []byte(strings.Join(shas, "\n") + "\n")

	cmd, err := gitexec.Command(ctx, gitexec.Spec{Args: []string{"cat-file", "--batch"}})
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(input)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cat-file --batch: stdout pipe: %w", err)
	}
	it := &ObjectIterator{
		cmd:    cmd,
		stdout: bufio.NewReaderSize(stdout, 256*1024),
	}
	cmd.Stderr = &it.stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cat-file --batch: start: %w", err)
	}
	return it, nil
}

// Next reads the next non-tree object from the stream. Trees are silently
// skipped. Returns io.EOF when the stream ends.
func (it *ObjectIterator) Next() (*ObjectEntry, error) {
	for {
		// Read header line: "<sha> <type> <size>\n"
		line, err := it.stdout.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Wait for the process to finish.
				if waitErr := it.cmd.Wait(); waitErr != nil {
					return nil, fmt.Errorf("cat-file exited: %w\nstderr: %s", waitErr, strings.TrimSpace(it.stderr.String()))
				}
				return nil, io.EOF
			}
			return nil, fmt.Errorf("cat-file: read header: %w", err)
		}
		line = strings.TrimSuffix(line, "\n")

		fields := strings.SplitN(line, " ", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("cat-file: malformed header: %q", line)
		}
		sha := fields[0]
		objType := fields[1]
		size, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("cat-file: bad size in header %q: %w", line, err)
		}

		if objType == "tree" {
			// Skip tree objects: read and discard size bytes + trailing LF.
			if _, err := io.CopyN(io.Discard, it.stdout, int64(size)+1); err != nil {
				return nil, fmt.Errorf("cat-file: discard tree %s: %w", sha, err)
			}
			continue
		}

		// Read exactly size bytes of content.
		content := make([]byte, size)
		if _, err := io.ReadFull(it.stdout, content); err != nil {
			return nil, fmt.Errorf("cat-file: read content %s: %w", sha, err)
		}

		// Read and discard the trailing LF.
		if _, err := it.stdout.ReadByte(); err != nil {
			return nil, fmt.Errorf("cat-file: read trailing LF for %s: %w", sha, err)
		}

		return &ObjectEntry{
			SHA:     sha,
			Type:    objType,
			Size:    size,
			Content: content,
		}, nil
	}
}

// Close kills the subprocess if it is still running and waits for it to exit.
func (it *ObjectIterator) Close() error {
	if it.cmd.Process != nil {
		_ = it.cmd.Process.Kill()
	}
	// Wait collects the exit status; ignore the error since we killed it.
	_ = it.cmd.Wait()
	return nil
}

// RunWithGitDir executes a git command against a specific git directory and
// work tree, rather than relying on cwd-based discovery. Sets GIT_DIR,
// GIT_WORK_TREE, and cmd.Dir so both git and cwd-relative paths resolve
// against the target repo.
//
// It is one of the declared explicit-directory exemptions from the
// repository-root pin: the repository is an argument, not a discovery.
func RunWithGitDir(ctx context.Context, gitDir string, workTree string, args ...string) (stdout, stderr string, err error) {
	return runCaptured(ctx, gitexec.Spec{
		Args:     args,
		Exempt:   gitexec.ExemptRunWithGitDir,
		GitDir:   gitDir,
		WorkTree: workTree,
	}, nil)
}

// CatFileBatchAllWithDir starts a git cat-file --batch-all-objects --batch
// subprocess targeting a specific git directory. Returns an ObjectIterator for
// streaming the results. The caller must call Close() when done.
func CatFileBatchAllWithDir(ctx context.Context, gitDir string) (*ObjectIterator, error) {
	cmd, err := gitexec.Command(ctx, gitexec.Spec{
		Args:   []string{"cat-file", "--batch-all-objects", "--batch"},
		Exempt: gitexec.ExemptCatFileBatchAllWithDir,
		GitDir: gitDir,
		Dir:    gitDir,
	})
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cat-file --batch-all-objects: stdout pipe: %w", err)
	}
	it := &ObjectIterator{
		cmd:    cmd,
		stdout: bufio.NewReaderSize(stdout, 256*1024),
	}
	cmd.Stderr = &it.stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cat-file --batch-all-objects: start: %w", err)
	}
	return it, nil
}

// CatFileBatchSHAsWithDir starts a git cat-file --batch subprocess targeting a
// specific git directory, reading only the specified SHAs. Sets GIT_DIR so git
// resolves objects from the target repo rather than the cwd repo. The caller
// must call Close() when done.
func CatFileBatchSHAsWithDir(ctx context.Context, gitDir string, shas []string) (*ObjectIterator, error) {
	input := []byte(strings.Join(shas, "\n") + "\n")

	cmd, err := gitexec.Command(ctx, gitexec.Spec{
		Args:   []string{"cat-file", "--batch"},
		Exempt: gitexec.ExemptCatFileBatchSHAsWithDir,
		GitDir: gitDir,
		Dir:    gitDir,
	})
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(input)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("cat-file --batch (dir): stdout pipe: %w", err)
	}
	it := &ObjectIterator{
		cmd:    cmd,
		stdout: bufio.NewReaderSize(stdout, 256*1024),
	}
	cmd.Stderr = &it.stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cat-file --batch (dir): start: %w", err)
	}
	return it, nil
}

// SplitNonEmpty splits s by newlines and returns only non-empty lines.
func SplitNonEmpty(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

// ForEachRef runs git for-each-ref with the given format and optional ref
// prefixes (e.g. "refs/heads/", "refs/tags/"). Returns one line per ref.
func ForEachRef(ctx context.Context, format string, prefixes ...string) ([]string, error) {
	args := []string{"for-each-ref", "--format=" + format}
	args = append(args, prefixes...)
	stdout, _, err := Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return SplitNonEmpty(stdout), nil
}

// LsRemoteBulk runs git ls-remote against a remote with a pattern and returns
// a map of refname to SHA. The output format of git ls-remote is
// "<SHA>\t<refname>" per line; the map key is the refname.
func LsRemoteBulk(ctx context.Context, remote, pattern string) (map[string]string, error) {
	stdout, _, err := Run(ctx, "ls-remote", remote, pattern)
	if err != nil {
		return nil, fmt.Errorf("ls-remote %s %s: %w", remote, pattern, err)
	}
	result := make(map[string]string)
	for _, line := range SplitNonEmpty(stdout) {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			result[parts[1]] = parts[0] // refname -> SHA
		}
	}
	return result, nil
}
