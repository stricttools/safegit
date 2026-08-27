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

// GitDir returns the ABSOLUTE path of the repository's git directory.
//
// Absolute because every consumer joins a state-file name onto it -- MERGE_HEAD,
// index, safegit/ -- and then reaches that path with a Go filesystem call, which
// resolves a relative path against the PROCESS working directory. Plain
// `rev-parse --git-dir` answers `.git` whenever git ran at the top of the work
// tree, and safegit's own context pins every git subprocess to the repository
// root, so from a subdirectory that answer names <subdir>/.git: a directory that
// does not exist. Every probe of it then reports "absent", which is the
// permissive answer in both places it is asked -- no operation in flight, and an
// empty index -- so a commit taken mid-merge from a subdirectory succeeded and
// dropped the merge's second parent.
//
// It is git's own canonicalized answer (`--absolute-git-dir`) rather than a
// filepath.Abs of the relative one, for the same reason ObjectsDir and HooksDir
// ask git: a linked worktree, a redirected git directory and a GIT_DIR override
// all break any join a caller could do itself.
func GitDir(ctx context.Context) (string, error) {
	out, _, err := Run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ObjectsDir returns the ABSOLUTE path of the repository's object store.
//
// Absolute because the answer is used as a GIT_ALTERNATE_OBJECT_DIRECTORIES
// entry, which git resolves against whatever directory the child process runs
// in -- and safegit's children run in several (the repository root under the
// pin, another repository entirely at the explicit-directory sites). A relative
// answer would name a different store depending on who read it.
//
// It is git's own answer rather than a join onto the git dir, so a linked
// worktree (whose objects live in the common git dir) and a repository whose
// object store is redirected both report the store git will actually use.
func ObjectsDir(ctx context.Context) (string, error) {
	out, _, err := Run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HooksDir returns the ABSOLUTE path of the directory git runs hooks from.
//
// It is git's own answer rather than a join onto the git dir, and it is the one
// place anything in safegit asks. Two configurations make the join wrong, and
// both are silent when it is: `core.hooksPath` redirects the directory
// entirely, and a LINKED WORKTREE's git dir (.git/worktrees/<name>) has no
// hooks/ of its own -- git runs the common git dir's hooks there. A caller
// joining paths itself would run nothing in the first case and nothing at all
// in the second, while git still ran the operator's hooks.
func HooksDir(ctx context.Context) (string, error) {
	out, _, err := Run(ctx, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
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

// EmptyTreeSHA is the object name of the tree with no entries, asked of git
// rather than spelled out.
//
// The two well-known constants (sha1's 4b825dc6... and sha256's 6ef19b41...)
// are deliberately NOT hardcoded here. Hardcoding them would save one
// subprocess on paths that are already cold, in exchange for a table that has
// to be extended by hand the day git gains another hash algorithm -- and the
// failure then is silent, a name that resolves to nothing in a repository the
// table does not know. `hash-object` computes the name from the algorithm the
// repository actually uses, so it self-adapts.
//
// It is `hash-object -t tree` on EMPTY STDIN rather than MkTree with no
// entries, which also yields the empty tree: mktree WRITES the object, which
// puts it in the class the preview quarantine exists for, while hash-object
// without -w computes the name and writes nothing at all. Empty stdin rather
// than /dev/null for the same reason every other hashing helper here takes
// bytes: safegit's hash-object callers hand git content, never a path (see the
// comment above HashObjectBytes), and /dev/null is not a path every platform
// has.
func EmptyTreeSHA(ctx context.Context) (string, error) {
	out, _, err := RunWithEnvStdin(ctx, nil, nil, "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", fmt.Errorf("asking git for the empty tree's object name: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// HeadTreeish names the tree to compare the working tree, the index or a
// conclusion's first parent against: `HEAD` where it resolves, and the EMPTY
// TREE where it does not.
//
// The second case is an UNBORN branch -- the state between `git init` and the
// first commit, and the state `safegit undo` of a root commit leaves behind.
// There is no HEAD there, and every git command that takes HEAD as a treeish is
// fatal, which is why the substitution is made here rather than left to each
// caller: a repository with no commits holds exactly the empty tree, so a diff
// against it reports precisely what a diff against HEAD reports on a born
// branch -- every staged addition, and nothing else.
//
// The test is `rev-parse --verify --quiet`: without --quiet git prints its
// "ambiguous argument" advice and exits 128, so the cheap question would answer
// with noise on stderr in the ordinary case this function exists for.
func HeadTreeish(ctx context.Context) (string, error) {
	if !HeadIsUnborn(ctx) {
		return "HEAD", nil
	}
	return EmptyTreeSHA(ctx)
}

// HeadIsUnborn reports whether HEAD names a branch that does not exist yet.
//
// It is the cheap question, asked with `rev-parse --verify --quiet`: without
// --quiet git prints its "ambiguous argument 'HEAD'" advice and exits 128, so
// the ordinary case this exists for would answer with noise on stderr.
//
// A bool rather than (bool, error), because every caller is already inside a
// repository safegit resolved a git directory for, and the only other way this
// invocation fails is a repository nothing else in the process could read
// either.
func HeadIsUnborn(ctx context.Context) bool {
	_, _, err := Run(ctx, "rev-parse", "--verify", "--quiet", "HEAD")
	return err != nil
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

// CommitIdentity pins the author and committer a commit is written with,
// timestamps included. A caller that has no identity to impose passes nil to
// CommitTree and gets git's own configured identity and the current time,
// which is what every ordinary commit wants; the rewrite paths, which must
// reproduce an existing commit's identity exactly, pass one.
type CommitIdentity struct {
	Author    AuthorInfo
	Committer AuthorInfo
}

// CommitTree creates a commit object from a tree SHA and its parents, in the
// order given, and returns the new commit SHA. An empty parents slice creates a
// root commit; more than one parent creates a merge commit, which is why the
// parameter is a slice rather than a single SHA -- a caller that rewrites a
// merge commit with one parent silently unmerges the branch.
//
// identity, when non-nil, pins the author and committer (see CommitIdentity).
func CommitTree(ctx context.Context, treeSHA string, parents []string, message string, identity *CommitIdentity) (string, error) {
	args := []string{"commit-tree", treeSHA}
	for _, p := range parents {
		if p == "" {
			continue
		}
		args = append(args, "-p", p)
	}
	args = append(args, "-m", message)

	var env []string
	if identity != nil {
		env = append(identityEnv(identity.Author, "GIT_AUTHOR"), identityEnv(identity.Committer, "GIT_COMMITTER")...)
	}

	out, _, err := RunWithEnv(ctx, env, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// identityEnv renders one identity as the environment variables git reads it
// from, omitting every field the caller left empty.
//
// The omission is what lets a caller pin ONE side. A conclusion of a cherry-pick
// or a revert preserves the author recorded on the source commit while the
// committer stays whoever is running the command, so it supplies an Author and
// no Committer -- and an empty GIT_COMMITTER_NAME would be a commit with no
// committer rather than git's own configured one. The rewrite paths, which set
// all six, are unaffected.
func identityEnv(id AuthorInfo, prefix string) []string {
	var env []string
	for _, field := range [][2]string{{"NAME", id.Name}, {"EMAIL", id.Email}, {"DATE", id.Date}} {
		if field[1] == "" {
			continue
		}
		env = append(env, prefix+"_"+field[0]+"="+field[1])
	}
	return env
}

// ZeroSHA is git's "this object must not exist" convention: the all-zero object
// name. Passed to update-ref as the expected old value it means "create only" --
// git refuses with "reference already exists" when the ref is already there.
const ZeroSHA = "0000000000000000000000000000000000000000"

// ZeroMode is how git's raw diff format spells the mode of a side that is not
// there: the addition's source, the deletion's destination.
const ZeroMode = "000000"

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
//
// An empty indexPath stages into the repository's shared index, the same
// convention UnmergedStages and SetIndexStage0 use.
func AddFile(ctx context.Context, indexPath, filePath string) error {
	var env []string
	if indexPath != "" {
		env = []string{"GIT_INDEX_FILE=" + indexPath}
	}
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

// DropFromIndex removes ONE exact path from a custom index, whether or not the
// file is still on disk and whatever its content is.
//
// It is deliberately not RmCached. `git rm --cached` is a porcelain safety
// check as much as a removal: it refuses a path whose indexed content differs
// from both the working file and HEAD, and it reads HEAD -- the repository's
// real HEAD, which on a cross-branch operation is not the tree the index was
// seeded from. Untracking a file that is meant to STAY on disk, usually with
// content that has moved on since it was committed, is exactly the shape that
// check refuses. `update-index --force-remove` states the intent directly: drop
// this index entry, touch nothing else.
//
// The path is repo-relative; git resolves it against the process working
// directory, which every safegit git call has pinned to the repository root.
func DropFromIndex(ctx context.Context, indexPath, repoRelPath string) error {
	env := []string{"GIT_INDEX_FILE=" + indexPath}
	_, _, err := RunWithEnv(ctx, env, "update-index", "--force-remove", "--", repoRelPath)
	return err
}

func isDirectoryRmError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not removing") && strings.Contains(err.Error(), "recursively without -r")
}

// IsTracked checks whether a file is tracked in the given revision's tree.
// Uses cat-file instead of ls-files because safegit never writes to the main
// index -- files committed via safegit exist in HEAD but not in .git/index.
//
// The revision is a PARAMETER rather than a hardcoded HEAD because the tree a
// path must be judged against is the tree the operation is built on, which is
// not always HEAD: a `commit --branch other` builds on other's tip, and an
// amend builds on the tip it replaces. Asking HEAD there decides the request
// against a tree the operation will never touch. An empty rev means there is no
// such tree yet (an unborn ref), where nothing is tracked.
func IsTracked(ctx context.Context, rev, filePath string) (bool, error) {
	if rev == "" {
		return false, nil
	}
	_, _, err := Run(ctx, "cat-file", "-e", rev+":"+filePath)
	if err != nil {
		// Non-zero exit means the object doesn't exist in that tree
		return false, nil
	}
	return true, nil
}

// ListSkipWorktreeFiles returns the paths of all files with the skip-worktree
// flag set in the main index. It parses `git ls-files -v -z` output, selecting
// records that start with "S " (the skip-worktree indicator).
//
// The NUL-delimited form is what makes the answer usable: without -z git
// C-quotes any path that is not plain ASCII, and the quoted spelling names no
// index entry, so restoring the flag afterwards would fail on exactly the paths
// that most need it.
func ListSkipWorktreeFiles(ctx context.Context) ([]string, error) {
	out, _, err := Run(ctx, "ls-files", "-v", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, record := range strings.Split(out, "\x00") {
		if strings.HasPrefix(record, "S ") {
			files = append(files, record[2:])
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
//
// ONE substitution is made on the caller's treeish, and its scope is narrow on
// purpose: a literal "HEAD" on an UNBORN branch becomes the empty tree, because
// `read-tree --reset -u HEAD` is fatal there and what the caller means -- put
// the index and the working tree in step with the committed state -- is the
// empty tree in a repository that has no commits. It applies to nothing else.
// An unresolvable treeish that is NOT literal HEAD stays a hard error, and must:
// substituting the empty tree for a failed resolution generally would
// `read-tree --reset -u` every tracked file out of the working tree, which is
// the opposite of what a caller passing a real SHA (a merge's incoming tip)
// asked for.
func SyncMainIndexWithWorktree(ctx context.Context, treeish string) ([]string, error) {
	if treeish == "HEAD" {
		resolved, err := HeadTreeish(ctx)
		if err != nil {
			return nil, fmt.Errorf("naming the tree to put the index and the working tree in step with: %w", err)
		}
		treeish = resolved
	}

	// 1. Save existing skip-worktree files. Restoring them afterwards is the
	// reconciliation authority's job (restoreSkipWorktree, the same one
	// ReconcileMainIndex uses), so a flag set by the operator is re-applied the
	// same way whatever moved the ref.
	origSkip, err := ListSkipWorktreeFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading skip-worktree flags before syncing the working tree: %w", err)
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
		return nil, restoreSkipWorktree(ctx, origSkip)
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

	return trackedIgnored, restoreSkipWorktree(ctx, origSkip)
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
	return RunPassthroughWithEnv(ctx, nil, args...)
}

// RunPassthroughWithEnv is RunPassthrough with extra environment entries, which
// is what lets a caller point git at an index file of safegit's own choosing
// (GIT_INDEX_FILE) while keeping safegit's promise never to write the shared
// one.
//
// GIT_INDEX_FILE is deliberately NOT among the environment entries the boundary
// refuses (that list is GIT_DIR, GIT_WORK_TREE, GIT_COMMON_DIR and
// GIT_OBJECT_DIRECTORY): naming an index file does not retarget the repository,
// and internal/git already reaches every temporary index this way.
//
// Streams, directory semantics and the declared exemption are identical to
// RunPassthrough's -- both are the same site, and a caller that adds an
// environment entry must not silently get different terminal or directory
// behavior.
func RunPassthroughWithEnv(ctx context.Context, env []string, args ...string) error {
	return RunPassthroughTo(ctx, env, os.Stdout, args...)
}

// RunPassthroughTo is RunPassthroughWithEnv with the child's STDOUT sink named
// by the caller.
//
// It exists for machine mode: under --json safegit's stdout carries exactly one
// document, the framework's envelope, and a passthrough child writing its own
// progress there would put a second document beside it. The caller passes
// os.Stderr instead, which is where `push` already routes git's stdout for the
// same reason. Stderr and stdin are wired to the terminal either way.
func RunPassthroughTo(ctx context.Context, env []string, stdout io.Writer, args ...string) error {
	cmd, err := gitexec.Command(
		gitexec.WithoutRootPin(ctx, gitexec.ExemptGuardedPassthrough),
		gitexec.Spec{Args: args, Env: env},
	)
	if err != nil {
		return err
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// CommonGitDirOf returns the common git directory for a given gitDir: the one
// every worktree of a repository shares. For a normal repository it equals
// gitDir; for a linked worktree it is the main .git dir. Lock files live there,
// so that worktrees committing to the same branch serialize correctly.
//
// The repository is an ARGUMENT rather than the process working directory, so
// the call goes through RunWithGitDir -- the declared explicit-directory
// exemption from the repository-root pin -- which sets GIT_DIR and runs git in
// that directory. An absolute gitDir therefore yields an absolute answer; a
// relative one yields an answer relative to gitDir itself, never to this
// process's working directory. There is deliberately no working-directory form:
// one existed, had no callers, and returned an answer whose meaning depended on
// where the process happened to stand.
func CommonGitDirOf(ctx context.Context, gitDir string) (string, error) {
	out, _, err := RunWithGitDir(ctx, gitDir, "", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsIgnored checks whether a file matches a gitignore rule.
//
// This is git's own question, index included: a path that is in the index is
// TRACKED, and check-ignore answers "not ignored" for it whatever the ignore
// rules say. That is the right answer for "may this path be added", which is
// what the callers of this function ask -- a tracked file matching an ignore
// pattern must stay committable. A caller asking the other question, whether
// the ignore rules cover a path at all, wants MatchesIgnoreRules.
func IsIgnored(ctx context.Context, filePath string) (bool, error) {
	_, _, err := Run(ctx, "check-ignore", "-q", "--", filePath)
	if err != nil {
		// Exit code 1 means not ignored; exit code 128 means path error
		return false, nil
	}
	return true, nil
}

// MatchesIgnoreRules reports whether the ignore rules cover a path, with the
// index left out of the question entirely.
//
// `--no-index` is the whole difference from IsIgnored, and it inverts the
// answer for exactly the paths that make the question worth asking: one that is
// still in the index. Plain check-ignore calls such a path not ignored because
// it is tracked, so asking it "is this path gitignored" about a path that is
// about to STOP being tracked yields the opposite of the truth. With
// --no-index only the patterns decide.
//
// Exit 1 with nothing on stderr is check-ignore's "no pattern matches", which
// is an answer; any other failure is a real one and is returned.
func MatchesIgnoreRules(ctx context.Context, filePath string) (bool, error) {
	_, stderr, err := Run(ctx, "check-ignore", "--no-index", "-q", "--", filePath)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 && strings.TrimSpace(stderr) == "" {
		return false, nil
	}
	return false, err
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

// HaveMergeBase reports whether two commits share any merge base at all.
//
// The three answers git's own `merge-base` gives are kept distinct, because
// only one of them means "these histories are unrelated": exit 0 with a base,
// exit 1 with none, and anything else -- an unresolvable argument, a broken
// object store -- which is not an answer to this question. ok is false for that
// third case, so a caller refuses on a FACT rather than on a failure that could
// mean anything.
func HaveMergeBase(ctx context.Context, a, b string) (have, ok bool) {
	_, _, err := Run(ctx, "merge-base", a, b)
	if err == nil {
		return true, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, true
	}
	return false, false
}

// AuthorInfo holds the name, email, and raw git date for an author or committer.
type AuthorInfo struct {
	Name  string
	Email string
	Date  string // raw git date format: "1234567890 +0200"
}

// FirstParentRange lists the commits a branch would LOSE by moving from `to`
// back to `from`, newest first. An empty `from` means the branch would lose
// everything reachable from `to`, which is what deleting the ref does.
//
// The walk is FIRST-PARENT, and that is the whole definition rather than a
// detail of it. A merge commit's second parent is the side that was merged IN:
// those commits were never made by the branch, and moving the branch back to
// the merge's first parent does not undo them -- it undoes the merge. Walking
// every parent would report a whole merged-in branch as commits the move
// discards, which is a different and untrue statement.
func FirstParentRange(ctx context.Context, from, to string) ([]string, error) {
	spec := to
	if from != "" {
		spec = from + ".." + to
	}
	out, _, err := Run(ctx, "rev-list", "--first-parent", spec)
	if err != nil {
		return nil, err
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			commits = append(commits, line)
		}
	}
	return commits, nil
}

// ConfiguredAuthor is the identity git itself would record as the AUTHOR of a
// commit created right now: `git var GIT_AUTHOR_IDENT`, which is git's own
// resolution of the environment, the repository config and the global config.
// Asking git is the point -- a reconstruction from `config --get user.name`
// would miss the GIT_AUTHOR_* environment and git's own fallbacks, and would
// therefore be able to disagree with the commit it claims to describe.
//
// The timestamp git includes is dropped: the identity is asked for so a report
// can name who a commit records, and a time resolved here is not the time the
// commit will carry.
func ConfiguredAuthor(ctx context.Context) (AuthorInfo, error) {
	out, _, err := Run(ctx, "var", "GIT_AUTHOR_IDENT")
	if err != nil {
		return AuthorInfo{}, fmt.Errorf("resolving the identity git would author with: %w", err)
	}
	id := parseIdentity(strings.TrimSpace(out))
	id.Date = ""
	return id, nil
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

// LsTreeRecursive returns EVERY entry in the given treeish, recursively:
// blobs, symlinks and gitlinks (submodule pointers, mode 160000, object type
// "commit"). LsTreeAll drops everything that is not a blob, which hides exactly
// the entries a caller that must not cross a submodule boundary needs to see.
//
// `ls-tree -r` never descends INTO a gitlink, so a submodule's own contents can
// never appear here -- the gitlink is reported as one entry and the recursion
// stops there.
func LsTreeRecursive(ctx context.Context, treeish string) ([]TreeEntry, error) {
	out, _, err := Run(ctx, "ls-tree", "--full-tree", "-r", "-z", treeish)
	if err != nil {
		return nil, fmt.Errorf("ls-tree %s: %w", treeish, err)
	}
	return parseLsTreeOutput(out, false), nil
}

// LsTreePathsRecursive returns the entries a treeish holds at EXACTLY the given
// repo-relative paths, recursively, and nothing else. A path the tree does not
// carry is simply absent from the answer, which is how a caller learns the tree
// does not hold it.
//
// It is the path-limited form of LsTreeRecursive, for a caller that wants a
// handful of named paths out of a tree rather than all of it. `--full-tree`
// makes both the listing and the pathspecs repository-rooted, so the answer does
// not depend on where the process stands -- the same reason it is mandatory on
// the two listings above.
//
// An empty path list returns nothing: `ls-tree` with no pathspec lists the whole
// tree, which is the opposite of what a caller asking about no paths means.
//
// Every path goes out under the `:(literal)` pathspec magic, which is not
// optional: a path is a NAME here, never a pattern. Without it a path beginning
// with a colon is read as pathspec magic of its own -- `:weird.txt` matches
// NOTHING and git exits 0 -- and a caller that reads an absent answer as "the
// tree does not carry this path" would act on a silent miss. Wildcards in a
// name are the same class of error in the other direction.
func LsTreePathsRecursive(ctx context.Context, treeish string, paths []string) ([]TreeEntry, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	args := append([]string{"ls-tree", "--full-tree", "-r", "-z", treeish, "--"}, literalPathspecs(paths)...)
	out, _, err := Run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("ls-tree %s: %w", treeish, err)
	}
	return parseLsTreeOutput(out, false), nil
}

// literalPathspecs renders repo-relative paths as pathspecs git matches by
// NAME: no wildcard expansion, and no leading colon read as magic.
func literalPathspecs(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, ":(literal)"+p)
	}
	return out
}

// ChangedPath is one entry of a recursive raw diff between two trees.
type ChangedPath struct {
	// Status is git's single-letter status code: A, M, D, T (type change), and
	// nothing else, because DiffTree turns rename detection off.
	Status string
	// Path is the repo-relative, slash-separated path that changed.
	Path string

	// SrcMode and DstMode are the six-digit file modes on each side of the
	// change, exactly as git's raw format writes them. The absent side of an
	// addition or a deletion is git's all-zero mode, "000000", rather than an
	// empty string: the raw format says so, and a reader comparing against a
	// real mode gets a value that can never be mistaken for one.
	SrcMode string
	DstMode string

	// SrcSHA and DstSHA are the blob names on each side, unabbreviated. The
	// absent side is ZeroSHA, again as the raw format writes it.
	//
	// They are what makes a raw diff more than a list of names: a deletion and
	// an addition carrying ONE blob name is the raw material a move-record
	// inference is built from. The pairing itself lives in internal/commit;
	// this package reports what git said and interprets nothing.
	SrcSHA string
	DstSHA string
}

// DiffTree lists every path that differs between two trees, recursively.
//
// It is the one place safegit asks git what a tree comparison contains, and the
// answer is what the commit pipeline reports as "the files in this commit":
// derived from the objects, never counted from the arguments a caller typed.
//
// GIT'S rename detection is deliberately OFF, and the qualification is the
// point: what this returns is the RAW delta -- a deletion and an addition, with
// the modes and blob names on both sides -- which is what a reviewer of the
// published commit sees and what safegit's own move inference reads
// (internal/commit/infer_moves.go). That inference pairs a deletion with an
// addition only where the objects leave one answer possible; a similarity score
// is an interpretation of CONTENT, and safegit asks git for none, here or
// anywhere.
//
// An empty fromTreeish means "compare against nothing": every path in the new
// tree is reported as an addition. That is the root-commit case, and it is
// spelled this way rather than with git's empty-tree constant so the function
// carries no assumption about the repository's hash algorithm.
func DiffTree(ctx context.Context, fromTreeish, toTreeish string) ([]ChangedPath, error) {
	if fromTreeish == "" {
		entries, err := LsTreeRecursive(ctx, toTreeish)
		if err != nil {
			return nil, err
		}
		changed := make([]ChangedPath, 0, len(entries))
		for _, e := range entries {
			// The synthesized side of a root commit is spelled the way git's raw
			// format spells an absent side, so a reader cannot tell a root
			// commit's additions from any other commit's by their shape.
			changed = append(changed, ChangedPath{
				Status:  "A",
				Path:    e.Path,
				SrcMode: ZeroMode,
				DstMode: e.Mode,
				SrcSHA:  ZeroSHA,
				DstSHA:  e.SHA,
			})
		}
		return changed, nil
	}

	out, _, err := Run(ctx, "diff-tree", "-r", "-z", "--no-commit-id", "--no-renames",
		"--raw", "--no-abbrev", fromTreeish, toTreeish)
	if err != nil {
		return nil, fmt.Errorf("diff-tree %s %s: %w", fromTreeish, toTreeish, err)
	}

	// -z raw output is a flat NUL-terminated stream of alternating metadata and
	// path fields, the metadata field being
	//
	//	:<srcmode> <dstmode> <srcsha> <dstsha> <status>
	//
	// Without -z git C-quotes any path that is not plain ASCII, which would
	// name no file at all; without --no-abbrev the object names come back
	// shortened, and a shortened name is not something to compare two sides of
	// a diff by.
	fields := strings.Split(out, "\x00")
	var changed []ChangedPath
	for i := 0; i+1 < len(fields); i += 2 {
		meta := fields[i]
		path := fields[i+1]
		if meta == "" || path == "" {
			continue
		}
		c, ok := parseRawDiffMeta(meta, path)
		if !ok {
			continue
		}
		changed = append(changed, c)
	}
	return changed, nil
}

// parseRawDiffMeta reads one raw-format metadata field into a ChangedPath. A
// field that does not carry the five expected parts is reported as unusable
// rather than half-filled: a half-filled entry would let a caller compare
// against an empty mode or an empty blob name and read the answer as a fact.
func parseRawDiffMeta(meta, path string) (ChangedPath, bool) {
	if !strings.HasPrefix(meta, ":") {
		return ChangedPath{}, false
	}
	parts := strings.Fields(meta[1:])
	if len(parts) != 5 {
		return ChangedPath{}, false
	}
	return ChangedPath{
		Status:  parts[4],
		Path:    path,
		SrcMode: parts[0],
		DstMode: parts[1],
		SrcSHA:  parts[2],
		DstSHA:  parts[3],
	}, true
}

// FilterIgnored returns the subset of the given repo-relative paths that git's
// ignore rules exclude, as a set. Directories may be passed too: an ignored
// directory answers for itself, so a caller walking a tree can stop there
// instead of asking about every file underneath it.
//
// One `check-ignore --stdin` invocation answers for the whole batch. git exits
// 1 when nothing in the batch is ignored, which is an answer and not a failure.
func FilterIgnored(ctx context.Context, paths []string) (map[string]bool, error) {
	ignored := make(map[string]bool)
	if len(paths) == 0 {
		return ignored, nil
	}

	var stdin bytes.Buffer
	for _, p := range paths {
		stdin.WriteString(p)
		stdin.WriteByte(0)
	}

	out, stderr, err := RunWithEnvStdin(ctx, nil, stdin.Bytes(), "check-ignore", "-z", "--stdin")
	if err != nil {
		var exitErr *exec.ExitError
		// Exit 1 with nothing on stderr is check-ignore's "none of these are
		// ignored"; any other failure is a real one.
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || strings.TrimSpace(stderr) != "" {
			return nil, err
		}
		return ignored, nil
	}

	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	return ignored, nil
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

// The path-taking hash-object helpers are deliberately absent. git resolves a
// path argument against the directory the child runs in, which safegit pins to
// the repository root -- so a path the OPERATOR typed would name a different
// file (or none) whenever the command was run from a subdirectory, silently.
// Every caller reads the bytes itself, where the anchoring is explicit, and
// hands them to HashObjectBytes or HashObjectWriteBytes.

// HashObjectBytes returns the blob SHA for in-memory bytes without writing
// anything to the object store -- the preview counterpart of
// HashObjectWriteBytes.
func HashObjectBytes(ctx context.Context, data []byte) (string, error) {
	out, _, err := RunWithEnvStdin(ctx, nil, data, "hash-object", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HashObjectBytesAsPath returns the blob SHA git would record for in-memory
// bytes if they lived at rel: the same answer as HashObjectBytes, except that
// git's clean filter and text attributes for rel are applied to the bytes
// first. It writes nothing.
//
// This is NOT one of the path-taking helpers the comment above rules out. The
// content still comes from the caller, on stdin; rel is a repo-relative NAME
// git looks attributes up under and never opens, so nothing here depends on
// where a file happens to be or on which directory the child runs in. It is
// what makes a content comparison filter-aware: on a checkout where git
// converts line endings, the bytes on disk differ from the blob and the file
// is still clean, and a comparison that hashed them raw would call it changed.
func HashObjectBytesAsPath(ctx context.Context, rel string, data []byte) (string, error) {
	out, _, err := RunWithEnvStdin(ctx, nil, data, "hash-object", "--path", rel, "--stdin")
	if err != nil {
		return "", err
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

// HashObjectWriteTag writes in-memory bytes as a TAG object to the object
// store, returning the tag object SHA. A rewritten annotated tag is a new tag
// object, so every site that reconstructs one goes through here rather than
// spelling the `-t tag` argv again.
func HashObjectWriteTag(ctx context.Context, content []byte) (string, error) {
	out, _, err := RunWithEnvStdin(ctx, nil, content, "hash-object", "-t", "tag", "-w", "--stdin")
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
// populated. Input is piped to `git mktree -z` as
// "<mode> <type> <sha>\t<name>\0" -- the same NUL-terminated encoding
// `ls-tree -z` produces, which is where every entry safegit writes back came
// from.
//
// The -z is not an optimization. Plain mktree input treats a path that starts
// with a double quote as a C-quoted string, so a repository holding a file
// whose name begins with one -- a legal name -- makes it refuse with "invalid
// quoting", and a path containing a backslash would be read as an escape. Under
// -z every path is taken literally, so the writer round-trips exactly what the
// reader parsed.
func MkTree(ctx context.Context, entries []TreeEntry) (string, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		fmt.Fprintf(&buf, "%s %s %s\t%s\x00", e.Mode, e.ObjectType, e.SHA, e.Path)
	}
	out, _, err := RunWithEnvStdin(ctx, nil, buf.Bytes(), "mktree", "-z")
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

// CommitMessage is one commit and the whole message it carries.
type CommitMessage struct {
	SHA     string
	Message string
}

// ReachableMessages returns every commit reachable from rev, newest first,
// with its full message.
//
// The delimiter is a NUL between commits (`log -z`), which is the only
// separator a commit message cannot contain: a message holds arbitrary text,
// blank lines and lines that look like whatever separator one might reach for,
// so any printable delimiter is a message somebody can write.
func ReachableMessages(ctx context.Context, rev string) ([]CommitMessage, error) {
	stdout, _, err := Run(ctx, "log", "-z", "--format=%H%n%B", rev)
	if err != nil {
		return nil, fmt.Errorf("reading the messages reachable from %s: %w", rev, err)
	}
	var out []CommitMessage
	for _, record := range strings.Split(stdout, "\x00") {
		if record == "" {
			continue
		}
		newline := strings.IndexByte(record, '\n')
		if newline < 0 {
			// A commit with no message at all: the record is the SHA alone.
			out = append(out, CommitMessage{SHA: record})
			continue
		}
		out = append(out, CommitMessage{SHA: record[:newline], Message: record[newline+1:]})
	}
	return out, nil
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
