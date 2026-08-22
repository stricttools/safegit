// Package commit implements the two-phase commit pipeline: a parallel-safe staging phase and a serialized ref-update phase with CAS retries.
// Phase A (parallel-safe): tmp index, validate, stage, write-tree, commit-tree.
// Phase B (serialized): ref lock, CAS check with retry, update-ref, oplog.
package commit

import (
	"context"
	"errors"
	"fmt"
	mrand "math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/stage"
	"github.com/smm-h/safegit/internal/trailer"
)

// CommitError carries a structured exit code alongside the error message.
//
// It reaches the caller wrapped as often as not -- a staging failure is
// annotated with the path it happened on before it leaves the pipeline -- so
// callers must find it with errors.As, never with a bare type assertion.
type CommitError struct {
	Code    int
	Message string
	// Err is the underlying cause, when there is one, so a caller can still
	// ask errors.Is about sentinels like stage.ErrBinaryFile after the code
	// has been attached.
	Err error
}

// Error returns the error message.
func (e *CommitError) Error() string { return e.Message }

// Unwrap exposes the underlying cause to errors.Is/errors.As.
func (e *CommitError) Unwrap() error { return e.Err }

// stagingHunksError annotates a hunk-staging failure with the file it happened
// on and, for failures that have their own exit code, attaches that code.
//
// A binary file is the one such failure today: a hunk spec against it is not a
// general error but a specific refusal ("this file can only be staged whole"),
// and it exits exitcode.BinaryHunkSpec so a caller can act on it. The returned
// error WRAPS the CommitError rather than being one, which is why every reader
// of these errors uses errors.As.
func stagingHunksError(absPath string, err error) error {
	if errors.Is(err, stage.ErrBinaryFile) {
		err = &CommitError{Code: exitcode.BinaryHunkSpec, Message: err.Error(), Err: err}
	}
	return fmt.Errorf("staging hunks of %s: %w", absPath, err)
}

// Pipeline orchestrates the full commit flow.
type Pipeline struct {
	SafegitDir string
	Config     repo.Config

	// PhaseADone is called (if non-nil) after Phase A completes but before
	// the ref lock is acquired. Used by tests to inject concurrent commits.
	PhaseADone func()
}

// FileSpec describes a file with optional hunk selection for staging.
type FileSpec struct {
	Path  string
	Hunks []int // nil = whole file, non-nil = selected hunk indices (1-based)
}

// CommitRequest holds all inputs for a single commit operation.
type CommitRequest struct {
	Message    string
	Files      []string   // plain file paths (whole-file staging)
	FileSpecs  []FileSpec // files with optional hunk selection (takes priority over Files)
	Branch     string     // empty = current branch
	Trailers   []string   // user-provided trailers ("Key: Value" format)
	AllowEmpty bool
	DryRun     bool

	// Sequencer declares that this caller is the conclusion path for an
	// operation git has in flight. Nil -- which is every ordinary caller --
	// means the commit is refused whenever git is mid-merge, mid-cherry-pick,
	// mid-revert, mid-rebase or mid-am. Only a command that FINISHES one of
	// those operations sets it, and it is checked against the state actually
	// on disk rather than taken on trust.
	Sequencer *coord.SequencerContext
}

// CommitResult is the JSON-serializable output of a successful commit.
type CommitResult struct {
	SHA      string `json:"sha"`
	Ref      string `json:"ref"`
	Parent   string `json:"parent"`
	Tree     string `json:"tree"`
	Attempts int    `json:"attempts"`

	// AutoStagedDeletions lists repo-relative paths of files that were
	// automatically staged as deletions (e.g., by move detection). Nil
	// when no auto-staged deletions occurred.
	AutoStagedDeletions []string `json:"autoStagedDeletions,omitempty"`
}

// Execute runs the full two-phase commit pipeline.
// On CAS miss it retries from Phase A up to Config.Commit.CASMaxAttempts times.
func (p *Pipeline) Execute(ctx context.Context, req CommitRequest) (*CommitResult, error) {
	// Before anything else, including a dry run: a preview of a commit safegit
	// would refuse must be the refusal, not a rehearsal of the wrong commit.
	if err := guardSequencer(ctx, req.Sequencer, "commit"); err != nil {
		return nil, err
	}

	repoRoot, err := git.RepoRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving repo root: %w", err)
	}

	// Resolve target branch ref
	ref := req.Branch
	if ref == "" {
		ref, err = git.HeadRef(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving HEAD: %w", err)
		}
	}
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	// When --branch is explicit, require it to exist to prevent orphan branches from typos
	if req.Branch != "" {
		if _, err := git.RevParse(ctx, ref); err != nil {
			return nil, fmt.Errorf("branch %q does not exist (use 'safegit branch %s' to create it)", req.Branch, req.Branch)
		}
	}

	// Resolve FileSpecs from Files if FileSpecs not set
	fileSpecs := req.FileSpecs
	if len(fileSpecs) == 0 {
		fileSpecs = make([]FileSpec, len(req.Files))
		for i, f := range req.Files {
			fileSpecs[i] = FileSpec{Path: f}
		}
	}

	// Extract plain paths for validation
	filePaths := make([]string, len(fileSpecs))
	for i, fs := range fileSpecs {
		filePaths[i] = fs.Path
	}

	// Validate and normalize file paths before entering the retry loop
	absFiles, err := p.resolveFiles(ctx, repoRoot, filePaths)
	if err != nil {
		return nil, err
	}

	maxAttempts := p.Config.Commit.CASMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryCommit(ctx, ref, repoRoot, absFiles, fileSpecs, req, attempt)
		if err != nil {
			return nil, err
		}
		if !retry {
			return result, nil
		}
		// CAS miss -- jitter before retry to break thundering-herd stampedes
		casRetryJitter()
	}

	return nil, &CommitError{
		Code:    exitcode.CASExhausted,
		Message: fmt.Sprintf("CAS convergence failure after %d attempts on %s", maxAttempts, ref),
	}
}

// indexBaseDir returns the directory the per-invocation temp index is created
// under, plus a cleanup for that directory itself. An executing run uses
// .git/safegit, whose tmp/ subdirectory the doctor garbage-collects. A dry run
// promises to change nothing, so its temp index goes to an OS temp directory
// instead: the preview still stages, writes the tree and builds the commit
// object exactly as the real run would, and .git/safegit is left alone --
// including not being created at all in a repo where safegit has never run.
func (p *Pipeline) indexBaseDir(dryRun bool) (base string, cleanup func(), err error) {
	if !dryRun {
		return p.SafegitDir, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "safegit-preview-")
	if err != nil {
		return "", nil, fmt.Errorf("creating preview index dir: %w", err)
	}
	return dir, func() { os.RemoveAll(dir) }, nil
}

// tryCommit runs one attempt of the two-phase pipeline.
// Returns (result, false, nil) on success, (nil, true, nil) on CAS miss,
// or (nil, false, err) on hard failure.
func (p *Pipeline) tryCommit(
	ctx context.Context,
	ref, repoRoot string,
	absFiles []string,
	fileSpecs []FileSpec,
	req CommitRequest,
	attempt int,
) (*CommitResult, bool, error) {

	// --- Phase A: parallel-safe (no locks) ---

	// Resolve parent FIRST so tree and parent are always consistent.
	// If we resolved the parent after building the tree, another agent's
	// commit landing between index creation and RevParse would cause us
	// to create a commit whose tree is based on the old HEAD but whose
	// parent is the new HEAD -- silently dropping the other agent's files.
	parentSHA, err := git.RevParse(ctx, ref)
	isRootCommit := false
	if err != nil {
		// ref doesn't exist yet -- this is the initial commit (no parent)
		parentSHA = ""
		isRootCommit = true
	}

	// Step 1: Create per-invocation tmp index. For root commits, use an
	// empty tree; otherwise seed from the resolved parent.
	idxBase, idxBaseCleanup, err := p.indexBaseDir(req.DryRun)
	if err != nil {
		return nil, false, err
	}
	defer idxBaseCleanup()

	var tmpIdx *index.TmpIndex
	if isRootCommit {
		tmpIdx, err = index.NewEmpty(idxBase)
	} else {
		tmpIdx, err = index.New(ctx, idxBase, parentSHA)
	}
	if err != nil {
		return nil, false, fmt.Errorf("creating tmp index: %w", err)
	}
	defer tmpIdx.Cleanup()

	// Step 2: Stage files into tmp index (with optional hunk selection)
	for i, absPath := range absFiles {
		hunks := fileSpecs[i].Hunks
		if hunks != nil {
			// Hunk-level staging
			if err := stage.StageHunks(ctx, tmpIdx.IndexPath, absPath, hunks); err != nil {
				return nil, false, stagingHunksError(absPath, err)
			}
		} else {
			// Whole-file staging
			if err := p.stageFile(ctx, tmpIdx.IndexPath, absPath); err != nil {
				return nil, false, fmt.Errorf("staging %s: %w", absPath, err)
			}
		}
	}

	// Step 2.3: Detect moves (new files whose content matches a deleted file
	// in the parent tree) and auto-stage the corresponding deletions.
	autoStaged, err := detectMoves(ctx, parentSHA, tmpIdx.IndexPath, absFiles, fileSpecs, repoRoot)
	if err != nil {
		return nil, false, fmt.Errorf("detect moves: %w", err)
	}

	// Step 2.5: Run pre-commit hook (if present) against the tmp index.
	// Skipped for --dry-run and --force (matching git's --no-verify).
	if !req.DryRun {
		gitDir, err := git.GitDir(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("resolving git dir: %w", err)
		}
		if err := runPreCommitHook(ctx, gitDir, tmpIdx.IndexPath, repoRoot); err != nil {
			return nil, false, err
		}
	}

	// Step 3: Build tree
	treeSHA, err := git.WriteTree(ctx, tmpIdx.IndexPath)
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.WriteTree, Message: fmt.Sprintf("write-tree failed: %v", err)}
	}

	// Check for empty commit (tree unchanged). Root commits are never empty.
	if !req.AllowEmpty && !isRootCommit {
		parentTree, err := p.parentTreeSHA(ctx, parentSHA)
		if err != nil {
			return nil, false, fmt.Errorf("resolving parent tree: %w", err)
		}
		if treeSHA == parentTree {
			return nil, false, fmt.Errorf("nothing to commit (tree unchanged); use --allow-empty to override")
		}
	}

	// Step 4: Build commit object (with user trailers and session trailer)
	msg := trailer.AppendCustom(req.Message, req.Trailers)
	commitSHA, err := git.CommitTree(ctx, treeSHA, parentSHA, trailer.Inject(msg))
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.CommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	// Hook for tests to inject concurrent commits between Phase A and Phase B
	if p.PhaseADone != nil {
		p.PhaseADone()
	}

	// DryRun: return result without touching the ref
	if req.DryRun {
		return &CommitResult{
			SHA:                 commitSHA,
			Ref:                 ref,
			Parent:              parentSHA,
			Tree:                treeSHA,
			Attempts:            attempt,
			AutoStagedDeletions: autoStaged,
		}, false, nil
	}

	// --- Phase B: serialized (per-ref lock + CAS) ---

	// Step 5: Acquire ref lock
	lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	refLock, err := lock.Acquire(repo.SharedSafegitDir(ctx, p.SafegitDir), p.SafegitDir, ref, "commit", lockTimeout)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring lock on %s: %w", ref, err)
	}
	defer refLock.Release()

	// Step 6: Re-resolve parent (CAS check)
	if isRootCommit {
		// For root commits, verify the ref still doesn't exist
		if _, rerr := git.RevParse(ctx, ref); rerr == nil {
			// Someone else created the ref while we were building -- retry
			return nil, true, nil
		}
	} else {
		currentParent, err := git.RevParse(ctx, ref)
		if err != nil {
			return nil, false, fmt.Errorf("re-resolving %s for CAS: %w", ref, err)
		}
		if currentParent != parentSHA {
			return nil, true, nil
		}
	}

	// Step 7: Update ref -- compare-and-swap for both cases.
	//
	// A root commit has no parent, and the empty parentSHA used to reach
	// update-ref as an omitted old value, which is an UNCONDITIONAL write: the
	// re-resolve above closed nothing, because a ref created in the window
	// between it and this line was overwritten without complaint. ZeroSHA is
	// git's "must not exist" expectation, so the create is now conditional too;
	// git refuses with "reference already exists", which isTransientRefError
	// already classifies as retryable, so the attempt loops and re-reads the ref
	// exactly as a losing CAS on a normal commit does.
	expected := parentSHA
	if isRootCommit {
		expected = git.ZeroSHA
	}
	if err := git.UpdateRef(ctx, ref, commitSHA, expected); err != nil {
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref failed: %w", err)
	}

	// Step 8: Append op log (lock released by defer).
	//
	// Recorded BEFORE the index is reconciled, so that a reconciliation failure
	// -- which is fatal, below -- still leaves a commit `safegit undo` can
	// reverse. The two steps are independent; only the crash-in-between case
	// can tell them apart.
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: "commit",
		Extra: map[string]interface{}{
			"ref":      ref,
			"tree":     treeSHA,
			"parent":   parentSHA,
			"sha":      commitSHA,
			"attempts": attempt,
		},
	})

	// Step 9: Reconcile the shared index with the commit, preserving whatever
	// staged work the parent tip does not account for. Only when committing to
	// the current branch -- a cross-branch commit must not touch this working
	// tree's index at all.
	if headRef, herr := git.HeadRef(ctx); herr == nil && headRef == ref {
		if err := git.ReconcileMainIndex(ctx, parentSHA, "HEAD"); err != nil {
			return nil, false, fmt.Errorf("commit %s was created, but reconciling the shared index failed: %w", commitSHA[:8], err)
		}
	}

	return &CommitResult{
		SHA:                 commitSHA,
		Ref:                 ref,
		Parent:              parentSHA,
		Tree:                treeSHA,
		Attempts:            attempt,
		AutoStagedDeletions: autoStaged,
	}, false, nil
}

// resolveFiles validates and returns absolute paths for all requested files.
// Relative paths are resolved against the current working directory (not the
// repo root), matching how users specify files from their shell.
func (p *Pipeline) resolveFiles(ctx context.Context, repoRoot string, files []string) ([]string, error) {
	abs := make([]string, 0, len(files))
	for _, f := range files {
		var absPath string
		if filepath.IsAbs(f) {
			absPath = filepath.Clean(f)
		} else {
			// Resolve relative to cwd, not repo root. When the user runs
			// safegit from a subdirectory, "file.txt" means "subdir/file.txt"
			// relative to the repo root.
			var err error
			absPath, err = filepath.Abs(f)
			if err != nil {
				return nil, fmt.Errorf("resolving path %s: %w", f, err)
			}
		}

		// Resolve symlinks so absPath matches repoRoot, which comes from
		// git rev-parse --show-toplevel (git resolves symlinks). On macOS,
		// /var is a symlink to /private/var, so without this, filepath.Rel
		// produces a path starting with ".." and the file is rejected as
		// outside the repository.
		absPath = resolveSymlinks(absPath)

		// Must be inside the repo
		rel, err := filepath.Rel(repoRoot, absPath)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("file %s is outside the repository", f)
		}

		exists := true
		if _, err := os.Lstat(absPath); os.IsNotExist(err) {
			exists = false
		}

		if !exists {
			// File doesn't exist on disk -- must be a tracked deletion
			tracked, err := git.IsTracked(ctx, rel)
			if err != nil {
				return nil, fmt.Errorf("checking tracked status of %s: %w", f, err)
			}
			if !tracked {
				return nil, fmt.Errorf("file %s does not exist and is not tracked by git", f)
			}
		} else {
			// Check gitignore
			ignored, _ := git.IsIgnored(ctx, rel)
			if ignored {
				return nil, fmt.Errorf("file %s is gitignored", f)
			}
		}

		abs = append(abs, absPath)
	}
	return abs, nil
}

// resolveSymlinks resolves symlinks in a path to produce a canonical absolute
// path. If the file doesn't exist, it resolves the parent directory instead
// (for new files or deletions). If the parent doesn't exist either, the path
// is returned unchanged (it will fail at the os.Lstat check later).
func resolveSymlinks(absPath string) string {
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		return resolved
	}
	// File may not exist yet; resolve parent directory instead
	dir := filepath.Dir(absPath)
	if resolvedDir, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolvedDir, filepath.Base(absPath))
	}
	return absPath
}

// stageFile stages a single file into the tmp index.
// Existing files are added; missing-but-tracked files are removed.
func (p *Pipeline) stageFile(ctx context.Context, indexPath, absPath string) error {
	if _, err := os.Lstat(absPath); os.IsNotExist(err) {
		return git.RmCached(ctx, indexPath, absPath)
	}
	return git.AddFile(ctx, indexPath, absPath)
}

// parentTreeSHA returns the tree SHA of a commit.
func (p *Pipeline) parentTreeSHA(ctx context.Context, commitSHA string) (string, error) {
	sha, err := git.RevParse(ctx, commitSHA+"^{tree}")
	if err != nil {
		return "", err
	}
	return sha, nil
}

// casRetryJitter sleeps for a random 1-10ms to break thundering-herd
// stampedes where all CAS-miss processes retry Phase A simultaneously
// and resolve the same parent.
func casRetryJitter() {
	jitter := time.Duration(1+mrand.Intn(10)) * time.Millisecond
	time.Sleep(jitter)
}

// isTransientRefError returns true if the error from git update-ref is a
// transient lock contention issue (git's own ref lock, not safegit's) that
// should be retried rather than treated as a hard failure.
func isTransientRefError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "cannot lock ref") || strings.Contains(msg, "Unable to create")
}

// runPreCommitHook runs .git/hooks/pre-commit with GIT_INDEX_FILE pointing
// at the tmp index so the hook sees the correct staged files. Returns nil if
// no hook exists (matching git's behavior). Returns an error if the hook
// exits non-zero, which aborts the commit.
func runPreCommitHook(ctx context.Context, gitDir, indexPath, repoRoot string) error {
	hookPath := filepath.Join(gitDir, "hooks", "pre-commit")
	info, err := os.Stat(hookPath)
	if err != nil {
		// No hook file -- nothing to run
		return nil
	}
	if info.Mode()&0111 == 0 {
		// Hook exists but is not executable -- skip (matches git behavior)
		return nil
	}

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pre-commit hook failed: %w", err)
	}
	return nil
}
