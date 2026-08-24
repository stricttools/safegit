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
	"github.com/smm-h/safegit/internal/gitexec"
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

	// AllowEscapingTargets is the same input CommitRequest carries: the
	// election to record a symlink whose target leaves the repository, which
	// intake refuses without it.
	AllowEscapingTargets bool

	// Untrack is the same input CommitRequest carries: paths to remove from the
	// index while leaving them on disk, each of which must be tracked in the
	// tip being replaced.
	Untrack []string

	// Moved is the same input CommitRequest carries: declared moves, one
	// "old -> new" pair each. They are judged against the FIRST PARENT of the
	// tip being replaced, because the amended commit describes that same step.
	Moved []string

	// MovedRetract is the same input CommitRequest carries: the ids of move
	// records this commit retracts, judged against the same first parent.
	MovedRetract []string

	// Sequencer is the same declared input CommitRequest carries: nil for
	// every ordinary caller, set only by a command that concludes the
	// operation git has in flight.
	Sequencer *coord.SequencerContext
}

// AmendResult is the JSON-serializable output of a successful amend.
type AmendResult struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
	// Parents is the parent list the amended commit inherited from the commit it
	// replaced -- all of them, so amending a merge leaves it a merge.
	Parents  []string `json:"parents"`
	Tree     string   `json:"tree"`
	OldSHA   string   `json:"oldSha"`
	Attempts int      `json:"attempts"`

	// Files lists the repo-relative paths this amend changed relative to the
	// tip it replaced, sorted, derived from the objects themselves.
	Files []string `json:"files"`

	// SkippedIgnored lists the gitignored repo-relative paths a directory
	// expansion passed over. Nil when nothing was skipped.
	SkippedIgnored []string `json:"skippedIgnored,omitempty"`

	// MovedRecords lists the move records THIS amend put on the commit: the
	// caller's declarations, then what inference minted from the authoring
	// event's own delta. The records PRESERVED from the message being replaced
	// are not among them -- they were reported by the operation that wrote them,
	// and an amend that repeated them would look like an amend that minted them.
	MovedRecords []trailer.Record `json:"movedRecords,omitempty"`

	// RefusedMoves and MovesOverCap are the same facts CommitResult carries, for
	// the amend's own delta.
	RefusedMoves []RefusedMove `json:"refusedMoves,omitempty"`
	MovesOverCap int           `json:"movesOverCap,omitempty"`
}

// Amend rewrites the tip of the current branch with new files staged.
// Uses tmp index seeded from HEAD, stages files, builds a new commit with
// parent = HEAD^ and lock-and-CAS updates the ref.
func (p *Pipeline) Amend(ctx context.Context, req AmendRequest) (*AmendResult, error) {
	if err := guardSequencer(ctx, req.Sequencer, "amend"); err != nil {
		return nil, err
	}
	// An amend builds on the parent tree, always, so the shared index's unmerged
	// entries can only be a conflict nobody resolved -- never this commit's own
	// content.
	if err := guardUnmergedIndex(ctx, req.Sequencer, IndexBaseParentTree); err != nil {
		return nil, err
	}

	// The preview area, opened before any object-writing call -- see
	// BeginPreview.
	ctx, previewArea, previewCleanup, err := BeginPreview(ctx, req.DryRun)
	if err != nil {
		return nil, err
	}
	defer previewCleanup()

	// AnchorRoot, not RepoRoot: the same anchoring authority the commit pipeline
	// uses, for the same filesystem work (see Pipeline.Execute).
	repoRoot, err := git.AnchorRoot(ctx)
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

	// An amend needs something to do. Declared moves count: an amend that names
	// no file but declares one is how a move committed WITHOUT its record gets
	// one -- the content is already right and only the message is missing the
	// declaration. It rebuilds the tip's own tree, which is the honest answer
	// for an operation that changes nothing but the message.
	if len(req.FileSpecs) == 0 && len(req.Untrack) == 0 && len(req.Moved) == 0 && len(req.MovedRetract) == 0 {
		return nil, fmt.Errorf("no files specified for amend")
	}

	// An amend's temporary index is seeded from the tip it REPLACES, so that
	// tip -- not HEAD -- is the tree its arguments are judged and expanded
	// against. The two differ on every cross-branch amend.
	files, err := p.resolveFiles(ctx, repoRoot, baseRev(ctx, ref), req.FileSpecs, req.Untrack, req.AllowEscapingTargets)
	if err != nil {
		return nil, err
	}

	// The declared moves, checked and turned into records once. Their base is
	// NOT the base above: the tip being replaced already holds the result of the
	// move, so the question "was the old path tracked" can only be asked of the
	// tip's own first parent -- see movedParentRev.
	movedTrailers, err := p.resolveAmendMoveTrailers(ctx, repoRoot, ref, req.Moved, req.MovedRetract)
	if err != nil {
		return nil, err
	}

	// One hook run per amend, not per CAS attempt -- see nativeHooks.
	hooks, err := newNativeHooks(ctx, repoRoot, p.SafegitDir, req.DryRun)
	if err != nil {
		return nil, err
	}
	defer hooks.cleanup()

	// The moves the amended commit's own authoring event witnesses. Once per
	// operation, exactly like the plain commit path's -- the ids are minted once
	// and attempt 1's answer is retained as data. See moveInference.
	inference := newAmendMoveInference()

	maxAttempts := p.Config.Commit.CASMaxAttempts

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryAmend(ctx, ref, repoRoot, previewArea, files, movedTrailers, req, hooks, inference, attempt)
		if err != nil {
			// Non-nil for the commit-stands verdict alone -- see PartialError.
			return result, err
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

// resolveAmendMoveTrailers checks and mints the declared moves and the
// retractions of an amend or a reword, against the first parent of the tip
// those operations replace.
//
// It is shared by both because both replace the tip: an amend and a reword
// build a commit on the same parents, so a move declared on either describes
// the same step out of that parent's tree. The two are resolved together
// because they are judged against the same base, which is read once.
func (p *Pipeline) resolveAmendMoveTrailers(ctx context.Context, repoRoot, ref string, moved, retract []string) ([]string, error) {
	if len(moved) == 0 && len(retract) == 0 {
		return nil, nil
	}
	tipSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", ref, err)
	}
	tip, err := git.ParseCommit(ctx, tipSHA)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", tipSHA, err)
	}
	parentRev := movedParentRev(tip.Parents)
	// The tip's own message is what the re-declaration refusal is asked of: an
	// amend reuses it, and a reword's -m carries its records forward, so a
	// record on it is still on the commit these declarations would join.
	lines, err := resolveMoved(ctx, repoRoot, parentRev, tip.Message, moved)
	if err != nil {
		return nil, err
	}
	retractLines, err := resolveMovedRetract(ctx, parentRev, retract)
	if err != nil {
		return nil, err
	}
	return append(lines, retractLines...), nil
}

func (p *Pipeline) tryAmend(
	ctx context.Context,
	ref, repoRoot, previewArea string,
	files *intake,
	movedTrailers []string,
	req AmendRequest,
	hooks *nativeHooks,
	inference *moveInference,
	attempt int,
) (*AmendResult, bool, error) {

	// Snapshot current tip SHA (the commit we're replacing).
	// Use ref (not "HEAD") so cross-branch amend resolves the correct tip.
	headSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s: %w", ref, err)
	}

	// The tip's parents, ALL of them, read off the commit object. `ref^` names
	// only the first, and an amend that kept only the first parent of a merge
	// would silently unmerge the branch that was merged in. A root commit has
	// none, which CommitTree renders as a root commit again.
	tip, err := git.ParseCommit(ctx, headSHA)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", headSHA, err)
	}
	parents := tip.Parents

	// Determine message: use provided or reuse existing
	message := req.Message
	if message == "" {
		message = strings.TrimRight(tip.Message, "\n")
	}

	// --- Phase A: create tmp index from the resolved tip and stage new files ---
	// Use headSHA (resolved above) instead of ref to avoid a TOCTOU race:
	// if the ref moves between RevParse and index creation, the tree would be
	// based on a different commit than headSHA, silently dropping files.
	tmpIdx, err := index.New(ctx, p.indexBaseDir(previewArea), headSHA)
	if err != nil {
		return nil, false, fmt.Errorf("creating tmp index: %w", err)
	}
	defer tmpIdx.Cleanup()

	if err := p.stageAll(ctx, tmpIdx.IndexPath, repoRoot, files); err != nil {
		return nil, false, err
	}

	// The repository's pre-commit hook, against what this amend stages; skipped
	// under --dry-run and run once per amend rather than once per CAS attempt.
	if err := hooks.preCommit(ctx, tmpIdx.IndexPath); err != nil {
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
	if unmatched := files.unmatchedSources(changed); len(unmatched) > 0 {
		return nil, false, unmatchedSourceError(unmatched, ref)
	}

	// The moves the AUTHORING EVENT witnesses. Not `changed` above, which
	// compares the amended tree against the REPLACED TIP and is the amend's own
	// edit: the commit that comes out describes the step from the tip's first
	// parent to itself, so that is the delta a record on it has to be read from.
	// It is a second per-attempt diff, taken against the trees this attempt
	// actually resolved.
	//
	// The records already on the message SUPPRESS -- they are preserved either
	// way (below), and a path one of them names is already answered for -- so
	// what this adds is only what the message does not already say.
	authoringTree := ""
	if len(parents) > 0 {
		authoringTree, err = p.parentTreeSHA(ctx, parents[0])
		if err != nil {
			return nil, false, fmt.Errorf("resolving the tree of %s: %w", parents[0], err)
		}
	}
	authored, err := git.DiffTree(ctx, authoringTree, treeSHA)
	if err != nil {
		return nil, false, fmt.Errorf("comparing the amended tree against %s: %w",
			refOrEmptyTree(len(parents) == 0, firstParent(parents)), err)
	}
	existingMoved := trailer.MovedLines(tip.Message)
	inferred, err := inference.records(ctx, authored, authoringTree, treeSHA,
		declaredPairs(append(append([]string{}, existingMoved...), movedTrailers...)))
	if err != nil {
		return nil, false, err
	}

	// The commit-msg hook, on the message with the user's own trailers and move
	// records on it and before safegit's session trailer goes on. A -m that
	// replaces the message carries the replaced message's move records forward:
	// dropping a record is a retraction the caller states, never a side effect
	// of rewording.
	// What this amend itself writes: the declarations it resolved and the records
	// it minted, in that order. The preserved lines are a separate argument
	// because they are a separate claim about who wrote them.
	amendMoved := append(append([]string{}, movedTrailers...), inferred...)
	trailers := commitTrailers(req.Trailers, preservedMovedLines(tip.Message, req.Message != ""), amendMoved)
	msg, err := hooks.commitMsg(ctx, tmpIdx.IndexPath, trailer.AppendCustom(message, trailers))
	if err != nil {
		return nil, false, err
	}

	// Create the replacement commit on the replaced commit's own parents.
	commitSHA, err := git.CommitTree(ctx, treeSHA, parents, trailer.Inject(msg), nil)
	if err != nil {
		return nil, false, &CommitError{Code: exitcode.CommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
	}

	// --- Phase B: lock and CAS update ---
	//
	// A preview takes no lock and re-reads nothing; it joins here, at the ref
	// update, which it records instead of performing.
	if !req.DryRun {
		lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
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
	}

	// Update ref: old = headSHA, new = commitSHA
	if err := p.updateRef(ctx, ref, commitSHA, headSHA); err != nil {
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	if req.DryRun {
		return &AmendResult{
			SHA:            commitSHA,
			Ref:            ref,
			Parents:        parents,
			Tree:           treeSHA,
			OldSHA:         headSHA,
			Attempts:       attempt,
			Files:          changedPaths(changed),
			SkippedIgnored: files.skipped,
			MovedRecords:   mintedRecords(amendMoved),
			RefusedMoves:   inference.refused,
			MovesOverCap:   inference.capped,
		}, false, nil
	}

	// Oplog, recorded before the index is reconciled: a reconciliation failure
	// is fatal, and the amend it followed must still be undoable.
	_ = oplog.Append(p.SafegitDir, oplog.Entry{
		Op: "amend",
		Extra: map[string]interface{}{
			"ref":      ref,
			"tree":     treeSHA,
			"parent":   firstParent(parents),
			"sha":      commitSHA,
			"oldSha":   headSHA,
			"attempts": attempt,
		},
	})

	// Reconcile the shared index with the amended commit, preserving whatever
	// staged work the pre-amend tip does not account for. Only when amending
	// the current branch: a cross-branch amend must not touch this index.
	result := &AmendResult{
		SHA:            commitSHA,
		Ref:            ref,
		Parents:        parents,
		Tree:           treeSHA,
		OldSHA:         headSHA,
		Attempts:       attempt,
		Files:          changedPaths(changed),
		SkippedIgnored: files.skipped,
		MovedRecords:   mintedRecords(amendMoved),
		RefusedMoves:   inference.refused,
		MovesOverCap:   inference.capped,
	}

	if headRef, herr := git.HeadRef(ctx); herr == nil && headRef == ref {
		if err := git.ReconcileMainIndex(ctx, headSHA, "HEAD"); err != nil {
			// The commit-stands verdict, with the result beside it -- see
			// PartialError.
			return result, false, &PartialError{SHA: commitSHA, Step: StepIndexReconcile, Err: err}
		}
	}

	// The post-commit hook, once the amended commit is the branch's tip.
	hooks.postCommit(ctx)

	return result, false, nil
}

// RewordRequest holds inputs for a reword operation.
type RewordRequest struct {
	Message  string   // required
	Branch   string   // target branch ref; empty = HEAD
	Trailers []string // user-provided trailers ("Key: Value" format)
	DryRun   bool

	// Moved is the same input AmendRequest carries. A reword is how a move that
	// was committed without its record gets one: the commit's content is
	// already right, and only the message is missing the declaration.
	Moved []string

	// MovedRetract is the same input AmendRequest carries: the ids of move
	// records this commit retracts.
	MovedRetract []string

	// Sequencer is the same declared input CommitRequest carries: nil for
	// every ordinary caller, set only by a command that concludes the
	// operation git has in flight.
	Sequencer *coord.SequencerContext
}

// RewordResult is the JSON-serializable output of a successful reword.
type RewordResult struct {
	// SHA is the reworded commit, and EMPTY under a dry run: a preview of a
	// reword is a pure computation that builds no commit object at all, so
	// there is no name to report. The SHA a real run produces could not be
	// predicted anyway -- it is a function of the committer timestamp.
	SHA string `json:"sha"`
	Ref string `json:"ref"`
	// Parents is the parent list the reworded commit inherited, all of it: a
	// reword changes a message and nothing else, least of all what is merged.
	Parents  []string `json:"parents"`
	Tree     string   `json:"tree"`
	OldSHA   string   `json:"oldSha"`
	Attempts int      `json:"attempts"`

	// MovedRecords lists the move records THIS reword put on the commit, which
	// are its declarations and nothing else: a reword changes no tree, so there
	// is no delta for inference to read and it mints none. The records carried
	// across from the replaced message are preserved and, exactly as on an
	// amend, are not reported here.
	MovedRecords []trailer.Record `json:"movedRecords,omitempty"`
}

// Reword rewrites only the commit message of the tip of the current branch.
// Tree and parent remain unchanged. Retries on CAS miss.
func (p *Pipeline) Reword(ctx context.Context, req RewordRequest) (*RewordResult, error) {
	if err := guardSequencer(ctx, req.Sequencer, "reword"); err != nil {
		return nil, err
	}
	// A reword rewrites a message and keeps the tip's tree, so it commits nothing
	// of the index -- but it is a commit built beside an unresolved conflict all
	// the same, and git refuses it for that reason too.
	if err := guardUnmergedIndex(ctx, req.Sequencer, IndexBaseParentTree); err != nil {
		return nil, err
	}

	if req.Message == "" {
		return nil, fmt.Errorf("reword requires a message (-m)")
	}

	// A reword's preview writes no objects (see tryReword), so it needs no
	// quarantine -- but it is still a preview, and marking it as one is what
	// makes the boundary refuse if anything on this path ever starts writing.
	if req.DryRun {
		ctx = gitexec.WithPreview(ctx)
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

	// AnchorRoot, not RepoRoot: the same anchoring authority the commit pipeline
	// uses (see Pipeline.Execute).
	repoRoot, err := git.AnchorRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving repo root: %w", err)
	}

	// The declared moves, checked and minted once, against the first parent of
	// the commit being reworded -- the same base an amend uses, for the same
	// reason.
	movedTrailers, err := p.resolveAmendMoveTrailers(ctx, repoRoot, ref, req.Moved, req.MovedRetract)
	if err != nil {
		return nil, err
	}

	// A reword is a commit as far as the repository's hooks are concerned, so it
	// runs the same three, once each -- see nativeHooks.
	hooks, err := newNativeHooks(ctx, repoRoot, p.SafegitDir, req.DryRun)
	if err != nil {
		return nil, err
	}
	defer hooks.cleanup()

	maxAttempts := p.Config.Commit.CASMaxAttempts

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, retry, err := p.tryReword(ctx, ref, movedTrailers, req, hooks, attempt)
		if err != nil {
			// Non-nil for the commit-stands verdict alone -- see PartialError.
			return result, err
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
	movedTrailers []string,
	req RewordRequest,
	hooks *nativeHooks,
	attempt int,
) (*RewordResult, bool, error) {

	headSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, false, fmt.Errorf("resolving %s: %w", ref, err)
	}

	// Tree and parents come off the commit object being reworded, all the
	// parents of it: a reword of a merge commit is still a merge commit.
	tip, err := git.ParseCommit(ctx, headSHA)
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", headSHA, err)
	}
	treeSHA, parents := tip.Tree, tip.Parents

	// A reword stages nothing, so the content the hooks inspect is the tip's own
	// tree. It is materialized as an index only when a hook that would read one
	// still has to run -- which a dry run never does, since it runs no hooks.
	hookIndex := ""
	if hooks.wantsIndex() {
		tmpIdx, err := index.New(ctx, p.indexBaseDir(""), treeSHA)
		if err != nil {
			return nil, false, fmt.Errorf("creating tmp index: %w", err)
		}
		defer tmpIdx.Cleanup()
		hookIndex = tmpIdx.IndexPath
	}

	if err := hooks.preCommit(ctx, hookIndex); err != nil {
		return nil, false, err
	}

	// A reword always replaces the message (-m is required), so the reworded
	// commit's existing move records are always carried forward.
	rewordTrailers := commitTrailers(req.Trailers, preservedMovedLines(tip.Message, true), movedTrailers)
	msg, err := hooks.commitMsg(ctx, hookIndex, trailer.AppendCustom(req.Message, rewordTrailers))
	if err != nil {
		return nil, false, err
	}

	// A preview of a reword is a PURE COMPUTATION: everything it reports -- the
	// ref, the tree, the parents, the commit being replaced -- was read off the
	// existing tip, and the one thing a commit-tree would add is a SHA that
	// depends on the committer timestamp and so cannot be the SHA the real run
	// will produce. Building the object anyway would write it into the
	// repository (or, since the quarantine, into a throwaway store) to compute
	// a number nothing may report. So a preview builds nothing, locks nothing
	// and re-reads nothing; it joins the executing path at the ref update,
	// which it records instead of performing.
	commitSHA := ""
	if !req.DryRun {
		commitSHA, err = git.CommitTree(ctx, treeSHA, parents, trailer.Inject(msg), nil)
		if err != nil {
			return nil, false, &CommitError{Code: exitcode.CommitTree, Message: fmt.Sprintf("commit-tree failed: %v", err)}
		}

		lockTimeout := time.Duration(p.Config.Lock.AcquireTimeoutSeconds) * time.Second
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
	}

	if err := p.updateRef(ctx, ref, commitSHA, headSHA); err != nil {
		if isTransientRefError(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("update-ref CAS failed: %w", err)
	}

	if req.DryRun {
		return &RewordResult{
			Ref:          ref,
			MovedRecords: mintedRecords(movedTrailers),
			Parents:      parents,
			Tree:         treeSHA,
			OldSHA:       headSHA,
			Attempts:     attempt,
		}, false, nil
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

	result := &RewordResult{
		SHA:          commitSHA,
		MovedRecords: mintedRecords(movedTrailers),
		Ref:          ref,
		Parents:      parents,
		Tree:         treeSHA,
		OldSHA:       headSHA,
		Attempts:     attempt,
	}

	if headRef, herr := git.HeadRef(ctx); herr == nil && headRef == ref {
		if err := git.ReconcileMainIndex(ctx, headSHA, "HEAD"); err != nil {
			// The commit-stands verdict, with the result beside it -- see
			// PartialError.
			return result, false, &PartialError{SHA: commitSHA, Step: StepIndexReconcile, Err: err}
		}
	}

	// The post-commit hook, once the reworded commit is the branch's tip.
	hooks.postCommit(ctx)

	return result, false, nil
}
