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

// ErrTreeUnchanged is the cause behind the empty-commit refusal, so a caller
// can recognize that particular refusal without matching on its text.
//
// The refusal's own message names --allow-empty, which is the answer for
// `safegit commit`. It is not the answer for a caller that has no such flag: a
// conclusion command wraps this with the ways out that exist for IT rather than
// pointing at a flag it does not offer.
var ErrTreeUnchanged = errors.New("the commit's tree is identical to its parent's")

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

	// RefUpdate performs the compare-and-swap that makes each commit real, and
	// is the pipeline's only way to move a ref. It is required: a pipeline
	// without one refuses rather than reaching around it, because reaching
	// around it is exactly how a preview would move a ref for real.
	RefUpdate RefUpdate

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

	// Untrack lists paths to remove from the index while leaving them on disk.
	// Each must be tracked in the tree the commit is built on; one that is not
	// is a hard error rather than a silent no-op.
	Untrack []string

	// Moved carries the caller's declared moves, one "old -> new" pair each, in
	// the grammar internal/trailer defines. Each becomes a record in the commit
	// message after being checked against the repository; a declaration the
	// repository contradicts is a refusal, never a record. Nothing here is
	// inferred -- safegit detects no moves at all.
	Moved []string

	// MovedRecords carries move records the caller has ALREADY MINTED, as whole
	// trailer lines. They are written exactly where a --moved record is written
	// -- caller content, before the commit-msg hook -- and the pipeline neither
	// parses nor re-checks them.
	//
	// It exists for the two callers that hold facts this pipeline cannot ask the
	// repository for. `safegit mv` PERFORMS the moves it records, so by the time
	// the commit is built the world already agrees and the --moved check (whose
	// question is "is the old path gone?") is answering about a move that has
	// just happened rather than one being declared after the fact -- and under a
	// preview the renames were recorded rather than performed, so that check
	// would refuse a preview of a perfectly good move. A single-commit revert
	// emits the INVERSES of the records on the commit it is reverting, which are
	// facts about that commit's message and not about any tree.
	MovedRecords []string

	// ExtraParents names parents BEYOND the branch tip, in order, for a commit
	// with more than one -- a merge conclusion, whose second parent is the side
	// being merged in.
	//
	// The tip is always parent 0 and is resolved by the pipeline itself, because
	// it is also the value every compare-and-swap is made against: a caller that
	// supplied the whole parent list would be supplying a tip that another
	// session may have moved since it read it. So a caller names only what the
	// pipeline cannot resolve for itself.
	ExtraParents []string

	// IndexBase selects what the commit's temporary index starts from. The zero
	// value is the parent tree, which is every ordinary commit.
	IndexBase IndexBase

	// IndexEdits are already-decided index entries applied to the temporary
	// index right after it is seeded and before any path is staged. They are how
	// a conclusion's conflict resolutions reach the commit: decided once by the
	// caller against the shared index, applied here so they land inside a dry
	// run's object quarantine and are re-applied on every CAS retry.
	IndexEdits []IndexEdit

	// Author, when non-nil, pins the AUTHOR identity the commit records -- name,
	// email and date -- while the committer stays git's own configured identity
	// and the current time. It exists for the cherry-pick and revert
	// conclusions, which preserve the identity recorded on the commit the
	// operation was applying.
	Author *git.AuthorInfo

	// OplogOp names this operation in the op log. Empty means "commit", which is
	// every ordinary caller. A conclusion command names itself, so that `safegit
	// undo` can say what it is reversing -- and say that reversing it does not
	// put the concluded operation back in flight.
	OplogOp string

	// Sequencer declares that this caller is the conclusion path for an
	// operation git has in flight. Nil -- which is every ordinary caller --
	// means the commit is refused whenever git is mid-merge, mid-cherry-pick,
	// mid-revert, mid-rebase or mid-am. Only a command that FINISHES one of
	// those operations sets it, and it is checked against the state actually
	// on disk rather than taken on trust.
	Sequencer *coord.SequencerContext
}

// IndexBase selects what a commit's temporary index starts from.
//
// It is an explicit input rather than something inferred, because the two
// answers mean different things about where the commit's content came from:
// the parent tree plus the paths the caller named, or a resolution the operator
// already staged.
type IndexBase int

const (
	// IndexBaseParentTree seeds the temporary index from the tree of the commit
	// being built on -- the branch tip, or an empty index on an unborn ref. It
	// is the zero value and what every ordinary commit uses: the commit contains
	// the parent's content with the named paths applied over it.
	IndexBaseParentTree IndexBase = iota

	// IndexBaseSharedIndex seeds the temporary index from a copy of the
	// repository's shared index (.git/index), so that whatever is staged there
	// becomes the commit's content. It exists for the conclusion of an operation
	// git has in flight, where the conflict resolution the operator staged lives
	// in that index and nowhere else. The copy is a copy: the shared index is
	// read and never written.
	IndexBaseSharedIndex
)

// CommitResult is the JSON-serializable output of a successful commit.
type CommitResult struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
	// Parents is the commit's parent list in order, empty for a root commit and
	// longer than one for a merge.
	Parents  []string `json:"parents"`
	Tree     string   `json:"tree"`
	Attempts int      `json:"attempts"`

	// Files lists the repo-relative paths this commit changed relative to its
	// parent, sorted, derived from the objects themselves. It is what every
	// count safegit reports comes from: the arguments say what was asked for,
	// this says what the commit holds.
	Files []string `json:"files"`

	// SkippedIgnored lists the gitignored repo-relative paths a directory
	// expansion passed over. Nil when nothing was skipped.
	SkippedIgnored []string `json:"skippedIgnored,omitempty"`
}

// Execute runs the full two-phase commit pipeline.
// On CAS miss it retries from Phase A up to Config.Commit.CASMaxAttempts times.
func (p *Pipeline) Execute(ctx context.Context, req CommitRequest) (*CommitResult, error) {
	// Before anything else, including a dry run: a preview of a commit safegit
	// would refuse must be the refusal, not a rehearsal of the wrong commit.
	if err := guardSequencer(ctx, req.Sequencer, "commit"); err != nil {
		return nil, err
	}

	// The preview area, opened before any object-writing call: from here on
	// every git subprocess this operation builds writes its objects into the
	// quarantine instead of into the repository.
	ctx, previewArea, previewCleanup, err := BeginPreview(ctx, req.DryRun)
	if err != nil {
		return nil, err
	}
	defer previewCleanup()

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

	// Resolve, canonicalize and expand the arguments once, before the retry
	// loop, against the tree this commit is built on -- the TARGET branch's
	// tip, which is not HEAD when --branch names another branch.
	files, err := p.resolveFiles(ctx, repoRoot, baseRev(ctx, ref), fileSpecs, req.Untrack)
	if err != nil {
		return nil, err
	}

	// The declared moves, checked and turned into records once -- before the
	// retry loop, so a refusal happens before anything is staged and so every
	// attempt writes the same record with the same id.
	//
	// The parent of an ordinary commit is the tip it is built on, which is the
	// same revision intake judged the file arguments against.
	movedTrailers, err := resolveMoved(ctx, repoRoot, baseRev(ctx, ref), req.Moved)
	if err != nil {
		return nil, err
	}
	// Already-minted records go on first, in the order the caller gave them:
	// they are the caller's own statement, and the pipeline adds nothing to it.
	movedTrailers = append(append([]string{}, req.MovedRecords...), movedTrailers...)

	// The repository's own hooks, prepared once for the whole operation: they
	// run at most once each no matter how many attempts the CAS loop takes.
	hooks, err := newNativeHooks(ctx, repoRoot, p.SafegitDir, req.DryRun)
	if err != nil {
		return nil, err
	}
	defer hooks.cleanup()

	maxAttempts := p.Config.Commit.CASMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryCommit(ctx, ref, repoRoot, previewArea, files, movedTrailers, req, hooks, attempt)
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
// under. An executing run uses .git/safegit, whose tmp/ subdirectory the doctor
// garbage-collects. A dry run stages inside the preview area BeginPreview
// opened instead: the preview still stages, writes the tree and builds the
// commit object exactly as the real run would, and .git/safegit is left alone
// -- including not being created at all in a repo where safegit has never run.
//
// The area is removed by the caller that opened it, once, when the operation
// ends; the temp index inside it is still cleaned up per attempt.
func (p *Pipeline) indexBaseDir(previewArea string) string {
	if previewArea != "" {
		return previewArea
	}
	return p.SafegitDir
}

// newTmpIndex creates the per-invocation index a commit stages into, from
// whichever base the request selected. baseDir is where the index directory
// itself lives (see indexBaseDir); base is what its content starts as.
func (p *Pipeline) newTmpIndex(ctx context.Context, baseDir string, base IndexBase, isRootCommit bool, parentSHA string) (*index.TmpIndex, error) {
	switch base {
	case IndexBaseSharedIndex:
		gitDir, err := git.GitDir(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving git dir: %w", err)
		}
		return index.NewFromFile(baseDir, filepath.Join(gitDir, "index"))
	case IndexBaseParentTree:
		if isRootCommit {
			return index.NewEmpty(baseDir)
		}
		return index.New(ctx, baseDir, parentSHA)
	default:
		return nil, fmt.Errorf("unknown index base %d", int(base))
	}
}

// tryCommit runs one attempt of the two-phase pipeline.
// Returns (result, false, nil) on success, (nil, true, nil) on CAS miss,
// or (nil, false, err) on hard failure.
func (p *Pipeline) tryCommit(
	ctx context.Context,
	ref, repoRoot, previewArea string,
	files *intake,
	movedTrailers []string,
	req CommitRequest,
	hooks *nativeHooks,
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
	tmpIdx, err := p.newTmpIndex(ctx, p.indexBaseDir(previewArea), req.IndexBase, isRootCommit, parentSHA)
	if err != nil {
		return nil, false, fmt.Errorf("creating tmp index: %w", err)
	}
	defer tmpIdx.Cleanup()

	// Step 1.5: the caller's already-decided index edits, before anything is
	// staged over them. Inside the retry loop because the index is created fresh
	// on every attempt, and inside the preview quarantine for the same reason
	// staging is: a `git add` here writes a blob.
	if err := applyIndexEdits(ctx, tmpIdx.IndexPath, repoRoot, req.IndexEdits); err != nil {
		return nil, false, err
	}

	// Step 2: Stage files into tmp index (with optional hunk selection).
	// Paths are canonical repo-relative and become absolute only here, at the
	// syscall boundary.
	//
	// Staging is NOT minted through the effects handle, and hunk staging could
	// not be: it feeds the computed patch to `git apply --cached` on stdin, and
	// the handle's Run takes an argv and nothing else -- there is no stdin to
	// hand it. Nothing rests on that, because staging is not a mutation the
	// preview has to withhold. It runs for real in both modes, and in dry mode it
	// runs inside the object quarantine (see BeginPreview) against a temp index
	// under the preview area, so every object it writes is thrown away with the
	// area and the repository's own store never sees it. The one mutation this
	// pipeline must withhold in a preview is the ref update at Step 7, which is
	// the single mint site.
	if err := p.stageAll(ctx, tmpIdx.IndexPath, repoRoot, files); err != nil {
		return nil, false, err
	}

	// Step 2.5: the repository's pre-commit hook, against the tmp index so it
	// sees exactly what this commit stages. Skipped under --dry-run, and run
	// only on the first attempt -- see nativeHooks.
	if err := hooks.preCommit(ctx, tmpIdx.IndexPath); err != nil {
		return nil, false, err
	}

	// Step 3: Build tree
	treeSHA, err := git.WriteTree(ctx, tmpIdx.IndexPath)
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.WriteTree, Message: fmt.Sprintf("write-tree failed: %v", err)}
	}

	// Step 3.5: What this commit actually contains, read off the objects
	// themselves. Every count and every path safegit reports comes from here --
	// never from the arguments, which say what was ASKED for and not what the
	// commit holds.
	parentTree := ""
	if !isRootCommit {
		parentTree, err = p.parentTreeSHA(ctx, parentSHA)
		if err != nil {
			return nil, false, fmt.Errorf("resolving parent tree: %w", err)
		}
	}
	changed, err := git.DiffTree(ctx, parentTree, treeSHA)
	if err != nil {
		return nil, false, fmt.Errorf("comparing the new tree against %s: %w", refOrEmptyTree(isRootCommit, ref), err)
	}

	// An argument that changed nothing is a refusal naming that argument.
	if unmatched := files.unmatchedSources(changed); len(unmatched) > 0 {
		return nil, false, unmatchedSourceError(unmatched, refOrEmptyTree(isRootCommit, ref))
	}

	// Check for empty commit (tree unchanged). Root commits are never empty.
	if !req.AllowEmpty && !isRootCommit && treeSHA == parentTree {
		return nil, false, &CommitError{
			Code:    exitcode.General,
			Message: "nothing to commit (tree unchanged); use --allow-empty to override",
			Err:     ErrTreeUnchanged,
		}
	}

	// Step 3.6: the commit-msg hook, on the user's message with the user's own
	// trailers and move records already on it, before safegit's own session
	// trailer goes on, adopting whatever the hook left in the file. It comes
	// after the refusals above so that a commit safegit is about to refuse never
	// sets an operator's message hook running -- the same order git uses, which
	// stops at "nothing to commit" before it asks for a message.
	message, err := hooks.commitMsg(ctx, tmpIdx.IndexPath,
		trailer.AppendCustom(req.Message, commitTrailers(req.Trailers, nil, movedTrailers)))
	if err != nil {
		return nil, false, err
	}

	// Step 4: Build commit object. The tip is parent 0 -- it is also what every
	// CAS below is made against -- and a caller concluding a merge names the
	// other side in ExtraParents.
	parents := commitParents(parentSHA, req.ExtraParents)
	var identity *git.CommitIdentity
	if req.Author != nil {
		// Author only: the committer is whoever is running the command, which is
		// what git's own cherry-pick records too.
		identity = &git.CommitIdentity{Author: *req.Author}
	}
	commitSHA, err := git.CommitTree(ctx, treeSHA, parents, trailer.Inject(message), identity)
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.CommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	// Hook for tests to inject concurrent commits between Phase A and Phase B
	if p.PhaseADone != nil {
		p.PhaseADone()
	}

	// --- Phase B: serialized (per-ref lock + CAS) ---
	//
	// A preview takes NO lock and makes no re-read: there is nothing to
	// protect, and a lock file created by a run that promises to change nothing
	// would be a change. It joins the executing path at the ref update itself,
	// which it records instead of performing.

	// A root commit has no parent, and the empty parentSHA used to reach
	// update-ref as an omitted old value, which is an UNCONDITIONAL write: the
	// re-resolve below closes nothing if a ref created in the window between it
	// and the update is overwritten without complaint. ZeroSHA is git's "must
	// not exist" expectation, so the create is conditional too; git refuses with
	// "reference already exists", which isTransientRefError already classifies
	// as retryable, so the attempt loops and re-reads the ref exactly as a
	// losing CAS on a normal commit does.
	expected := parentSHA
	if isRootCommit {
		expected = git.ZeroSHA
	}

	if !req.DryRun {
		// Step 5: Acquire ref lock.
		//
		// Not minted through the effects handle -- like the op-log append at
		// Step 8, and for the same reason: the handle's method set is closed and
		// has no shape for either. The lock's whole meaning is that creating it
		// succeeds for exactly one process, and a write() of the same path would
		// be a different operation with none of that guarantee. The gap is
		// recorded in todo/effects-handle-closed-method-set.md, and it costs a
		// preview nothing: neither this lock nor that append is ever reached by
		// one. The ref update at Step 7 is the mutation a preview must withhold,
		// and it is the one that IS minted.
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
	}

	// Step 7: Update ref -- compare-and-swap for both cases, through the
	// caller's RefUpdate. It is the single mint site: in an executing run it
	// performs this very invocation, and in a preview it records it and answers
	// nil, so the loop ends here with nothing moved.
	if err := p.updateRef(ctx, ref, commitSHA, expected); err != nil {
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref failed: %w", err)
	}

	if req.DryRun {
		return &CommitResult{
			SHA:            commitSHA,
			Ref:            ref,
			Parents:        parents,
			Tree:           treeSHA,
			Attempts:       attempt,
			Files:          changedPaths(changed),
			SkippedIgnored: files.skipped,
		}, false, nil
	}

	// Step 8: Append op log (lock released by defer).
	//
	// Off the effects handle for the same reason as the ref lock: the closed
	// method set has no append, and a write() would rewrite the file a
	// concurrent safegit may be appending to. A preview never reaches this line,
	// so nothing about the preview's honesty rests on it.
	//
	// Recorded BEFORE the index is reconciled, so that a reconciliation failure
	// -- which is fatal, below -- still leaves a commit `safegit undo` can
	// reverse. The two steps are independent; only the crash-in-between case
	// can tell them apart.
	oplogOp := req.OplogOp
	if oplogOp == "" {
		oplogOp = "commit"
	}
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: oplogOp,
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

	// Step 10: the post-commit hook, once the commit is real and nothing can
	// take it back.
	hooks.postCommit(ctx)

	return &CommitResult{
		SHA:            commitSHA,
		Ref:            ref,
		Parents:        parents,
		Tree:           treeSHA,
		Attempts:       attempt,
		Files:          changedPaths(changed),
		SkippedIgnored: files.skipped,
	}, false, nil
}

// updateRef mints the commit family's ref update through the caller-supplied
// RefUpdate, refusing outright when there is none.
//
// The refusal is what keeps the seam honest: a pipeline that fell back to
// calling git itself would move refs in a preview, which is the whole thing the
// mint exists to prevent.
func (p *Pipeline) updateRef(ctx context.Context, ref, newSHA, expected string) error {
	if p.RefUpdate == nil {
		return &CommitError{
			Code:    exitcode.General,
			Message: "the commit pipeline was built without a RefUpdate; it has no way to move " + ref,
		}
	}
	return p.RefUpdate.Update(ctx, ref, newSHA, expected)
}

// baseRev names the revision whose tree an operation is built on, or the empty
// string when that ref has no commit yet. It is the tree every intake
// judgement is made against.
func baseRev(ctx context.Context, ref string) string {
	if _, err := git.RevParse(ctx, ref); err != nil {
		return ""
	}
	return ref
}

// stageAll stages every resolved path into the temporary index. It is the one
// place a canonical repo-relative path becomes an absolute one, which is what
// keeps the two spellings from mixing anywhere else in the pipeline.
func (p *Pipeline) stageAll(ctx context.Context, indexPath, repoRoot string, files *intake) error {
	for _, entry := range files.entries {
		if entry.untrack {
			// The one staging action that deliberately ignores the working
			// tree: the file stays exactly where it is, and only the index
			// entry goes. It takes the repo-relative path, not the absolute
			// one, because it is not reaching for the file at all.
			if err := git.DropFromIndex(ctx, indexPath, entry.path); err != nil {
				return fmt.Errorf("untracking %s: %w", entry.path, err)
			}
			continue
		}
		absPath := git.Anchor(repoRoot, entry.path)
		if entry.hunks != nil {
			if err := stage.StageHunks(ctx, indexPath, absPath, entry.hunks); err != nil {
				return stagingHunksError(absPath, err)
			}
			continue
		}
		if err := p.stageFile(ctx, indexPath, absPath); err != nil {
			return fmt.Errorf("staging %s: %w", absPath, err)
		}
	}
	return nil
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

// commitParents renders the parent list a commit object is written with: the
// tip first, because that is also the value the ref update is made against,
// then whatever else the caller named. An empty tip is an unborn ref, which has
// no parents at all.
func commitParents(tipSHA string, extra []string) []string {
	var parents []string
	if tipSHA != "" {
		parents = append(parents, tipSHA)
	}
	return append(parents, extra...)
}

// firstParent is the parent list's head, or "" for a root commit. It is what
// the oplog records: undo needs the value the ref is put back to, which is the
// first parent and never the others.
func firstParent(parents []string) string {
	if len(parents) == 0 {
		return ""
	}
	return parents[0]
}
