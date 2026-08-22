// Amend and Reword implement tip-commit rewriting with CAS safety.
package commit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/index"
	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/trailer"
)

// AmendRequest holds inputs for an amend operation.
type AmendRequest struct {
	Message   string     // empty = keep existing message
	FileSpecs []FileSpec // files to stage into the amended commit
	Branch    string     // target branch ref; empty = HEAD
	Trailers  []string   // user-provided trailers ("Key: Value" format)
	DryRun    bool

	// Sequencer is the same declared input CommitRequest carries: nil for
	// every ordinary caller, set only by a command that concludes the
	// operation git has in flight.
	Sequencer *coord.SequencerContext
}

// AmendResult is the JSON-serializable output of a successful amend.
type AmendResult struct {
	SHA      string `json:"sha"`
	Ref      string `json:"ref"`
	Parent   string `json:"parent"`
	Tree     string `json:"tree"`
	OldSHA   string `json:"oldSha"`
	Attempts int    `json:"attempts"`

	// SkippedIgnored lists the gitignored repo-relative paths a directory
	// expansion passed over. Nil when nothing was skipped.
	SkippedIgnored []string `json:"skippedIgnored,omitempty"`
}

// Amend rewrites the tip of the current branch with new files staged.
// Uses tmp index seeded from HEAD, stages files, builds a new commit with
// parent = HEAD^ and lock-and-CAS updates the ref.
func (p *Pipeline) Amend(ctx context.Context, req AmendRequest) (*AmendResult, error) {
	if err := guardSequencer(ctx, req.Sequencer, "amend"); err != nil {
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

	if req.Branch != "" {
		if _, err := git.RevParse(ctx, ref); err != nil {
			return nil, fmt.Errorf("branch %q does not exist", req.Branch)
		}
	}

	if len(req.FileSpecs) == 0 {
		return nil, fmt.Errorf("no files specified for amend")
	}

	// An amend's temporary index is seeded from the tip it REPLACES, so that
	// tip -- not HEAD -- is the tree its arguments are judged and expanded
	// against. The two differ on every cross-branch amend.
	files, err := p.resolveFiles(ctx, repoRoot, baseRev(ctx, ref), req.FileSpecs)
	if err != nil {
		return nil, err
	}

	maxAttempts := p.Config.Commit.CASMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryAmend(ctx, ref, repoRoot, files, req, attempt)
		if err != nil {
			return nil, err
		}
		if !retry {
			return result, nil
		}
		casRetryJitter()
	}

	return nil, &CommitError{
		Code:    exitcode.CASExhausted,
		Message: fmt.Sprintf("CAS convergence failure after %d attempts on %s", maxAttempts, ref),
	}
}

func (p *Pipeline) tryAmend(
	ctx context.Context,
	ref, repoRoot string,
	files *intake,
	req AmendRequest,
	attempt int,
) (*AmendResult, bool, error) {

	// Snapshot current tip SHA (the commit we're replacing).
	// Use ref (not "HEAD") so cross-branch amend resolves the correct tip.
	headSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s: %w", ref, err)
	}

	// Get parent of tip (ref^). For root commits, parentSHA is empty;
	// CommitTree handles this by omitting the -p flag.
	parentSHA, err := git.RevParse(ctx, ref+"^")
	if err != nil {
		parentSHA = ""
	}

	// Determine message: use provided or reuse existing
	message := req.Message
	if message == "" {
		msg, err := git.CommitMessage(ctx, ref)
		if err != nil {
			return nil, false, fmt.Errorf("reading %s commit message: %w", ref, err)
		}
		message = msg
	}

	// --- Phase A: create tmp index from the resolved tip and stage new files ---
	// Use headSHA (resolved above) instead of ref to avoid a TOCTOU race:
	// if the ref moves between RevParse and index creation, the tree would be
	// based on a different commit than headSHA, silently dropping files.
	idxBase, idxBaseCleanup, err := p.indexBaseDir(req.DryRun)
	if err != nil {
		return nil, false, err
	}
	defer idxBaseCleanup()

	tmpIdx, err := index.New(ctx, idxBase, headSHA)
	if err != nil {
		return nil, false, fmt.Errorf("creating tmp index: %w", err)
	}
	defer tmpIdx.Cleanup()

	if err := p.stageAll(ctx, tmpIdx.IndexPath, repoRoot, files); err != nil {
		return nil, false, err
	}

	// Build new tree
	treeSHA, err := git.WriteTree(ctx, tmpIdx.IndexPath)
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.WriteTree, Message: fmt.Sprintf("write-tree failed: %v", err)}
	}

	// What this amend actually changes, read off the objects: the new tree
	// against the tree of the tip being REPLACED. That is the delta the caller
	// asked for -- comparing against the parent instead would report the whole
	// content of the amended commit, most of which the amend did not touch.
	oldTree, err := p.parentTreeSHA(ctx, headSHA)
	if err != nil {
		return nil, false, fmt.Errorf("resolving the tree of %s: %w", headSHA, err)
	}
	changed, err := git.DiffTree(ctx, oldTree, treeSHA)
	if err != nil {
		return nil, false, fmt.Errorf("comparing the amended tree against %s: %w", ref, err)
	}
	if src, unmatched := files.unmatchedSource(changed); unmatched {
		return nil, false, unmatchedSourceError(src, ref)
	}

	// Create new commit with parent = HEAD^ (replacing HEAD),
	// injecting user trailers and session trailer.
	msg := trailer.AppendCustom(message, req.Trailers)
	commitSHA, err := git.CommitTree(ctx, treeSHA, parentSHA, trailer.Inject(msg))
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.CommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	if req.DryRun {
		return &AmendResult{
			SHA:            commitSHA,
			Ref:            ref,
			Parent:         parentSHA,
			Tree:           treeSHA,
			OldSHA:         headSHA,
			Attempts:       attempt,
			SkippedIgnored: files.skipped,
		}, false, nil
	}

	// --- Phase B: lock and CAS update ---
	lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	refLock, err := lock.Acquire(repo.SharedSafegitDir(ctx, p.SafegitDir), p.SafegitDir, ref, "amend", lockTimeout)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring lock on %s: %w", ref, err)
	}
	defer refLock.Release()

	// CAS: ref must still point at headSHA (the commit we're replacing)
	currentTip, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, false, fmt.Errorf("re-resolving %s for CAS: %w", ref, err)
	}
	if currentTip != headSHA {
		return nil, true, nil // CAS miss, retry
	}

	// Update ref: old = headSHA, new = commitSHA
	if err := git.UpdateRef(ctx, ref, commitSHA, headSHA); err != nil {
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	// Oplog, recorded before the index is reconciled: a reconciliation failure
	// is fatal, and the amend it followed must still be undoable.
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: "amend",
		Extra: map[string]interface{}{
			"ref":      ref,
			"tree":     treeSHA,
			"parent":   parentSHA,
			"sha":      commitSHA,
			"oldSha":   headSHA,
			"attempts": attempt,
		},
	})

	// Reconcile the shared index with the amended commit, preserving whatever
	// staged work the pre-amend tip does not account for. Only when amending
	// the current branch: a cross-branch amend must not touch this index.
	if headRef, herr := git.HeadRef(ctx); herr == nil && headRef == ref {
		if err := git.ReconcileMainIndex(ctx, headSHA, "HEAD"); err != nil {
			return nil, false, fmt.Errorf("amended commit %s was created, but reconciling the shared index failed: %w", commitSHA[:8], err)
		}
	}

	return &AmendResult{
		SHA:            commitSHA,
		Ref:            ref,
		Parent:         parentSHA,
		Tree:           treeSHA,
		OldSHA:         headSHA,
		Attempts:       attempt,
		SkippedIgnored: files.skipped,
	}, false, nil
}

// RewordRequest holds inputs for a reword operation.
type RewordRequest struct {
	Message  string   // required
	Branch   string   // target branch ref; empty = HEAD
	Trailers []string // user-provided trailers ("Key: Value" format)
	DryRun   bool

	// Sequencer is the same declared input CommitRequest carries: nil for
	// every ordinary caller, set only by a command that concludes the
	// operation git has in flight.
	Sequencer *coord.SequencerContext
}

// RewordResult is the JSON-serializable output of a successful reword.
type RewordResult struct {
	SHA    string `json:"sha"`
	Ref    string `json:"ref"`
	Parent string `json:"parent"`
	Tree   string `json:"tree"`
	OldSHA string `json:"oldSha"`
}

// Reword rewrites only the commit message of the tip of the current branch.
// Tree and parent remain unchanged. Retries on CAS miss.
func (p *Pipeline) Reword(ctx context.Context, req RewordRequest) (*RewordResult, error) {
	if err := guardSequencer(ctx, req.Sequencer, "reword"); err != nil {
		return nil, err
	}

	if req.Message == "" {
		return nil, fmt.Errorf("reword requires a message (-m)")
	}

	// Resolve target branch ref
	ref := req.Branch
	if ref == "" {
		var err error
		ref, err = git.HeadRef(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving HEAD: %w", err)
		}
	}
	if !strings.HasPrefix(ref, "refs/") {
		ref = "refs/heads/" + ref
	}

	if req.Branch != "" {
		if _, err := git.RevParse(ctx, ref); err != nil {
			return nil, fmt.Errorf("branch %q does not exist", req.Branch)
		}
	}

	maxAttempts := p.Config.Commit.CASMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryReword(ctx, ref, req, attempt)
		if err != nil {
			return nil, err
		}
		if !retry {
			return result, nil
		}
		casRetryJitter()
	}

	return nil, &CommitError{
		Code:    exitcode.CASExhausted,
		Message: fmt.Sprintf("CAS convergence failure after %d attempts on %s", maxAttempts, ref),
	}
}

func (p *Pipeline) tryReword(
	ctx context.Context,
	ref string,
	req RewordRequest,
	attempt int,
) (*RewordResult, bool, error) {

	headSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s: %w", ref, err)
	}

	treeSHA, err := git.RevParse(ctx, ref+"^{tree}")
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s tree: %w", ref, err)
	}

	parentSHA, err := git.RevParse(ctx, ref+"^")
	if err != nil {
		parentSHA = ""
	}

	msg := trailer.AppendCustom(req.Message, req.Trailers)
	commitSHA, err := git.CommitTree(ctx, treeSHA, parentSHA, trailer.Inject(msg))
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.CommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	if req.DryRun {
		return &RewordResult{
			SHA:    commitSHA,
			Ref:    ref,
			Parent: parentSHA,
			Tree:   treeSHA,
			OldSHA: headSHA,
		}, false, nil
	}

	lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
	if lockTimeout <= 0 {
		lockTimeout = 30 * time.Second
	}
	refLock, err := lock.Acquire(repo.SharedSafegitDir(ctx, p.SafegitDir), p.SafegitDir, ref, "reword", lockTimeout)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring lock on %s: %w", ref, err)
	}
	defer refLock.Release()

	currentTip, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, false, fmt.Errorf("re-resolving %s for CAS: %w", ref, err)
	}
	if currentTip != headSHA {
		return nil, true, nil
	}

	if err := git.UpdateRef(ctx, ref, commitSHA, headSHA); err != nil {
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	// Oplog before reconciliation, for the same reason as amend: a fatal
	// reconciliation must leave an undoable reword behind it.
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: "reword",
		Extra: map[string]interface{}{
			"ref":    ref,
			"sha":    commitSHA,
			"oldSha": headSHA,
		},
	})

	if headRef, herr := git.HeadRef(ctx); herr == nil && headRef == ref {
		if err := git.ReconcileMainIndex(ctx, headSHA, "HEAD"); err != nil {
			return nil, false, fmt.Errorf("reworded commit %s was created, but reconciling the shared index failed: %w", commitSHA[:8], err)
		}
	}

	return &RewordResult{
		SHA:    commitSHA,
		Ref:    ref,
		Parent: parentSHA,
		Tree:   treeSHA,
		OldSHA: headSHA,
	}, false, nil
}
