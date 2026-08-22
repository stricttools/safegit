package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
	"github.com/smm-h/safegit/internal/trailer"
)

// TierAFunc is a command's own pre-refs verification. It runs while the
// rewritten commits are still unreachable objects, and it receives the ref
// update plan so it can inspect exactly what is about to become the
// repository's history -- the new commit tips and the new tag objects -- none
// of which any ref points at yet. A non-nil error refuses the whole rewrite.
type TierAFunc func(ctx context.Context, plan *RefUpdatePlan) error

// TierBFunc is a command's own post-cleanup verification. It runs after the
// refs have moved and the object store has been swept, so its findings cannot
// undo anything: a non-nil error is REPORTED and turns the command's exit code
// nonzero, with the rewrite standing.
type TierBFunc func(ctx context.Context) error

// RewriteHooks are the two verification hooks and the tag-annotation transform
// a rewrite hands to Finalize. Every field may be nil except where a command's
// own contract requires it.
type RewriteHooks struct {
	// AnnotateTag transforms the body of each annotated tag. Nil means the
	// rewrite does not touch tag annotations. The new tag objects it produces
	// are written BEFORE Tier A runs (so Tier A can read them); the refs that
	// point at them move with every other ref, after Tier A has passed.
	AnnotateTag TagBodyTransformFunc

	// TierA is the command's pre-refs verification (see TierAFunc).
	TierA TierAFunc

	// TierB is the command's post-cleanup verification (see TierBFunc).
	TierB TierBFunc
}

// RewriteResult collects the outputs of a history rewrite so that Finalize
// can execute the shared post-rewrite pipeline (verification, ref updates,
// cleanup, oplog, push hint).
type RewriteResult struct {
	// Core rewrite outputs
	ShaMap         map[string]string // old SHA -> new SHA mapping
	TagRewrites    []TagRewrite      // tag rewrite records from the ref update plan
	RewrittenCount int               // number of commits actually rewritten

	// Intent is what the rewrite DECLARED it would change, per commit. Tier A
	// checks the rewritten commits against it. It is mandatory: a rewrite that
	// leaves it unset is refused rather than silently unverified.
	Intent *RewriteIntent

	// Pre-rewrite state
	OldHeadSHA string // HEAD before the rewrite

	// Safegit paths
	SgDir string // .git/safegit directory path

	// Audit reason for the persisted rewrite-map record (empty for commands
	// without a --reason flag, e.g. author rewrite).
	Reason string

	// Tagger identity for the ref update plan (rewrite-author needs tagger
	// matching; zero values mean "no tagger matching", the default for scrub).
	TaggerOldName  string
	TaggerNewName  string
	TaggerOldEmail string
	TaggerNewEmail string

	// Oplog metadata
	OpName     string                 // operation name ("scrub-file", "scrub-match", "rewrite-author")
	OplogExtra map[string]interface{} // command-specific oplog fields

	// Post-Finalize outputs (populated by Finalize for callers to read)
	NewHeadSHA        string            // HEAD after the rewrite
	Ref               string            // current ref name (e.g. "refs/heads/main" or "HEAD (detached)")
	PreRewriteRemotes map[string]string // refs/remotes/* refname -> SHA before the refs moved
	CleanupOK         bool              // true when post-rewrite cleanup fully succeeded
	CleanupErrors     []string          // cleanup failure descriptions (empty when CleanupOK)

	// Tier B outcome: what post-rewrite verification found, with the rewrite
	// standing. Non-empty means the command exits RewriteIncomplete.
	TierBFailures []string
	// SyncSkipped records that the working-tree sync was deliberately not
	// performed because foreign staged state appeared while the rewrite ran.
	SyncSkipped bool

	// Tag-annotation pass outputs
	TagsRewrittenCount    int          // number of tag annotations rewritten
	AnnotationTagRewrites []TagRewrite // tag annotation rewrite records

	// Post-execution metrics (populated by executeScrubRecipe for callers)
	BlobsReplaced    int // number of blobs replaced
	MessagesModified int // number of commit messages modified
}

// rewriteRefusal is a Tier A verdict: the rewrite was refused before anything
// moved. It is a distinct type so a caller can exit with the code that says
// "nothing happened" rather than the general failure code.
type rewriteRefusal struct{ msg string }

func (e *rewriteRefusal) Error() string { return e.msg }

// refuse builds a Tier A refusal from a list of findings.
func refuse(headline string, findings []string) error {
	var b strings.Builder
	b.WriteString(headline)
	for _, f := range findings {
		b.WriteString("\n  " + f)
	}
	b.WriteString("\n\nNothing was changed: no ref moved, no tag moved, and no rewrite-journal record was written. " +
		"The repository is exactly as it was before this command ran.")
	return &rewriteRefusal{msg: b.String()}
}

// refuseCorruptRecord is the refusal for a message transform that would write a
// move record nothing can read (see trailer.RecordTransformError).
//
// It is the same verdict Tier A gives and it is given at the same guarantee: it
// happens while the walk is still writing unreachable objects, so no ref has
// moved, no tag has moved and no journal record exists. An error that is not a
// record-transform refusal is passed through untouched.
//
// The record is never written broken and never dropped either. Dropping it
// would delete a claim the operator never retracted, and writing it would leave
// a line the decoder refuses -- inert, so nothing would ever report it again.
func refuseCorruptRecord(sha string, err error) error {
	var bad *trailer.RecordTransformError
	if !errors.As(err, &bad) {
		return err
	}
	return refuse(fmt.Sprintf("refusing to rewrite: on commit %s the substitution would turn a move record "+
		"into a line nothing can read.", sha), []string{
		"the record as written:  " + bad.Line,
		"the transform's result: " + bad.Result,
		"which is not a move:    " + bad.Err.Error(),
		"",
		"A record is a whole claim, so it is neither written broken nor dropped in silence. Either:",
		"  - choose a replacement that leaves the pair a move: two different paths, both or neither",
		"    ending in a slash, and neither of them empty;",
		"  - erase the path from history with 'safegit scrub file --delete <path>', which removes the",
		"    records naming it as part of that rewrite; or",
		"  - retract the record in a commit of its own first, so this rewrite has nothing to transform.",
	})
}

// dieFinalize exits with the exit code a rewrite failure calls for: a refusal
// says nothing happened (RewriteRefused), anything else is a failure partway
// through the shared pipeline (General). prefix names the repository the
// failure belongs to when a command rewrites more than one.
//
// Both seams that can refuse route through here -- the walk, which refuses a
// commit message it cannot transform without breaking a record, and Finalize's
// own Tier A.
func dieFinalize(prefix string, err error) {
	msg := err.Error()
	if prefix != "" {
		msg = prefix + ": " + msg
	}
	var refusal *rewriteRefusal
	if errors.As(err, &refusal) {
		die(exitcode.RewriteRefused, msg)
	}
	die(exitcode.General, msg)
}

// TierBExit returns the exit code the command should return. A Tier B finding
// means the rewrite stands but something after it did not complete, which is
// RewriteIncomplete; otherwise the caller's own code is preserved.
func (r *RewriteResult) TierBExit(prior int) int {
	if len(r.TierBFailures) > 0 {
		return exitcode.RewriteIncomplete
	}
	return prior
}

// Finalize runs the shared post-rewrite pipeline. The whole ordering exists to
// put every refusable check BEFORE the first irreversible act, which is the
// moment a ref moves:
//
//  1. Plan every ref update, writing the new tag objects the plan needs.
//     Objects only -- nothing is reachable, nothing has moved.
//  2. TIER A, all hard refusals, original history untouched:
//     a. the preservation check -- every rewritten commit against what the
//     operation declared it would change, plus the rewrote-count tripwire;
//     b. the command's own Tier A hook (content verification, pattern absence
//     over the new commit set), which reads the plan;
//     c. the cleanliness re-check under the rewrite lock -- foreign working
//     tree or index state that appeared WHILE the rewrite ran.
//  3. Capture pre-rewrite remote-tracking state and persist the rewrite-map
//     "start" record. From here on a crash is recoverable from the journal
//     rather than invisible -- and an abort above never wrote one, so an
//     aborted rewrite can never read as a crashed one.
//  4. Apply the ref update plan. THIS is the irreversible step.
//  5. Persist the rewrite-map "refs" record (all tag rewrites).
//  6. Re-check the working tree once more and either sync it to the new HEAD
//     or SKIP the sync, printing what to do -- foreign staged state is never
//     overwritten.
//  7. untrackProtectedPaths -- remove tracked-but-gitignored files from index
//  8. cleanupAfterRewrite -- expire tainted reflog entries, repack, prune
//  9. TIER B: stale-ref pointers plus the command's own hook. Findings are
//     recorded and reported; the rewrite stands and the exit code turns
//     nonzero via TierBExit.
//  10. Resolve the new HEAD and ref, persist the "complete" record, append the
//     oplog entry, print the push hint.
//
// Steps 1-2 are prepare; steps 3-10 are publish. The two halves are separate
// methods because a rewrite that spans TWO repositories -- a submodule scrub,
// which rewrites the submodule and then the parent gitlinks that point at it --
// has to prepare BOTH before publishing EITHER. Finalize is the single-repository
// spelling of prepare-then-publish, and it is what every other rewrite calls.
func (r *RewriteResult) Finalize(ctx context.Context, flags globalFlags, cmd string, hooks RewriteHooks) error {
	plan, err := r.prepare(ctx, flags, hooks)
	if err != nil {
		return err
	}
	return r.publish(ctx, flags, cmd, hooks, plan)
}

// prepare is Finalize's pre-refs half: steps 1 and 2. When it returns, the
// rewritten commits and any new tag objects exist as UNREACHABLE objects, every
// refusable check has passed, and nothing in the repository has moved -- no ref,
// no tag, and no rewrite-journal record. A returned error therefore means the
// repository is exactly as it was, which is what lets a caller prepare several
// repositories and only then publish them.
func (r *RewriteResult) prepare(ctx context.Context, flags globalFlags, hooks RewriteHooks) (*RefUpdatePlan, error) {
	if err := r.Intent.validate(); err != nil {
		return nil, err
	}

	// 1. Plan the ref updates. Tag objects are written here; no ref moves.
	plan, err := planRefUpdates(ctx, r.ShaMap, r.TaggerOldName, r.TaggerNewName, r.TaggerOldEmail, r.TaggerNewEmail, hooks.AnnotateTag, flags.verbose)
	if err != nil {
		return nil, fmt.Errorf("planning ref updates: %w", err)
	}
	r.TagRewrites = plan.TagRewrites
	r.AnnotationTagRewrites = plan.AnnotationTagRewrites
	r.TagsRewrittenCount = plan.TagsRewritten

	// 2a. The preservation check.
	infof(flags, "Verifying the rewrite before it is published...\n")
	if failures := verifyIntendedChanges(ctx, r.ShaMap, r.Intent); len(failures) > 0 {
		return nil, refuse("the rewrite did not do what the operation declared it would:", failures)
	}

	// 2b. The command's own pre-refs verification.
	if hooks.TierA != nil {
		if err := hooks.TierA(ctx, plan); err != nil {
			return nil, refuse("the rewritten history failed verification:", []string{err.Error()})
		}
	}

	// 2c. The cleanliness re-check, under the rewrite lock. The rewrite refused
	// to start on a dirty tree, so anything here appeared while it was running.
	foreign, err := foreignWorktreeState(ctx, r.OldHeadSHA)
	if err != nil {
		return nil, err
	}
	if len(foreign) > 0 {
		return nil, refuse("the working tree changed while the rewrite was running, so publishing it would overwrite work this command did not make:", foreign)
	}

	return plan, nil
}

// publish is Finalize's apply half: steps 3 to 10, against a plan `prepare`
// produced. Everything it does is irreversible from the ref update onward, so it
// is only ever called once every repository the operation touches has passed
// prepare.
func (r *RewriteResult) publish(ctx context.Context, flags globalFlags, cmd string, hooks RewriteHooks, plan *RefUpdatePlan) error {
	// 3. Persist the rewrite-map "start" record before anything moves.
	preRemotes, err := captureRemoteTrackingState(ctx)
	if err != nil {
		return fmt.Errorf("capturing pre-rewrite remote-tracking state: %w", err)
	}
	r.PreRewriteRemotes = preRemotes

	commitMap := make(map[string]string)
	for old, new_ := range r.ShaMap {
		if old != new_ {
			commitMap[old] = new_
		}
	}

	// A rewrite can move refs even when every commit maps to itself: the
	// annotation pass rewrites tag objects whose bodies matched, and the plan
	// can rewrite an annotated tag on tagger identity alone. Refs must never
	// move unrecorded, so a start record is written whenever there is anything
	// to move. Pure no-ops (identity map AND no ref moves) stay recordless.
	allTagRewrites := make([]TagRewrite, 0, len(r.TagRewrites)+len(r.AnnotationTagRewrites))
	allTagRewrites = append(allTagRewrites, r.TagRewrites...)
	allTagRewrites = append(allTagRewrites, r.AnnotationTagRewrites...)

	var mapID string
	if len(commitMap) > 0 || len(allTagRewrites) > 0 {
		mapID = newRewriteMapID(r.OldHeadSHA)
		start := RewriteMapStart{
			Phase:             rewriteMapPhaseStart,
			ID:                mapID,
			Op:                r.OpName,
			Reason:            r.Reason,
			CreatedAt:         nowRFC3339(),
			OldHead:           r.OldHeadSHA,
			CommitMap:         commitMap,
			PreRewriteRemotes: preRemotes,
		}
		if err := appendRewriteMapRecord(r.SgDir, start); err != nil {
			return err
		}
	}

	// 4. Move the refs.
	infof(flags, "Updating refs...\n")
	if err := applyRefUpdates(ctx, plan, flags.verbose); err != nil {
		return fmt.Errorf("updating refs: %w", err)
	}

	// 5. Persist the "refs" record with every tag rewrite (ref-level plus
	// annotation-pass rewrites).
	if mapID != "" {
		refsRecord := RewriteMapRefs{
			Phase:       rewriteMapPhaseRefs,
			ID:          mapID,
			CreatedAt:   nowRFC3339(),
			TagRewrites: allTagRewrites,
		}
		if err := appendRewriteMapRecord(r.SgDir, refsRecord); err != nil {
			return err
		}
	}

	// 6-7. Sync the shared index and working tree with the rewritten HEAD, and
	// untrack protected paths. This must happen before cleanup so that the
	// index no longer references old (pre-rewrite) objects, allowing
	// repack/prune to remove them.
	//
	// The residual cleanliness check is the last thing between a concurrent
	// session's staged work and a `read-tree --reset -u` that would destroy it.
	// Tier A already refused if anything was there; this catches the narrow
	// window since. The refs stand either way -- skipping the sync leaves the
	// working tree as it is and says what to run.
	residual, err := foreignWorktreeState(ctx, r.OldHeadSHA)
	if err != nil {
		r.recordTierB(fmt.Sprintf("could not re-check the working tree before syncing it: %v", err))
	} else if len(residual) > 0 {
		r.SyncSkipped = true
		r.recordTierB(fmt.Sprintf("the working tree acquired changes while the refs were moving, so it was NOT synced to the rewritten history (%d path(s))", len(residual)))
		fmt.Fprintf(os.Stderr, "The refs now point at the rewritten history, but the working tree was left alone:\n")
		for _, line := range residual {
			fmt.Fprintf(os.Stderr, "  %s\n", line)
		}
		fmt.Fprintf(os.Stderr, "Nothing was overwritten. Deal with those changes, then run:\n")
		fmt.Fprintf(os.Stderr, "  git read-tree --reset -u HEAD\n")
	} else {
		protectedPaths, syncErr := git.SyncMainIndexWithWorktree(ctx, "HEAD")
		if syncErr != nil {
			r.recordTierB(fmt.Sprintf("syncing the working tree to the rewritten history: %v", syncErr))
		}
		untrackProtectedPaths(ctx, flags, protectedPaths)
	}

	// 8. Post-rewrite cleanup: expire tainted reflog entries and prune old
	// objects. Failures stay non-fatal (warnings) but are captured
	// machine-readably in CleanupOK/CleanupErrors for orchestrators.
	cleanupErrors, cleanupErr := cleanupAfterRewrite(ctx, flags, cmd, r.ShaMap, allTagRewrites, r.SgDir)
	if cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "warning: post-rewrite cleanup: %v\n", cleanupErr)
		cleanupErrors = append(cleanupErrors, cleanupErr.Error())
	}
	r.CleanupErrors = cleanupErrors
	r.CleanupOK = len(cleanupErrors) == 0

	// 9. Tier B: the shared stale-pointer check plus the command's own.
	for _, f := range verifyRefsRemapped(ctx, r.ShaMap) {
		r.recordTierB(f)
	}
	if hooks.TierB != nil {
		if err := hooks.TierB(ctx); err != nil {
			r.recordTierB(err.Error())
		}
	}

	// 10. Resolve new HEAD and ref.
	newHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolving new HEAD: %w", err)
	}
	r.NewHeadSHA = newHeadSHA

	ref, _ := git.HeadRef(ctx)
	if ref == "" {
		ref = "HEAD (detached)"
	}
	r.Ref = ref

	// Persist the "complete" record with the new HEAD and cleanup status.
	if mapID != "" {
		completeErrors := r.CleanupErrors
		if completeErrors == nil {
			completeErrors = []string{}
		}
		complete := RewriteMapComplete{
			Phase:         rewriteMapPhaseComplete,
			ID:            mapID,
			CreatedAt:     nowRFC3339(),
			NewHead:       newHeadSHA,
			CleanupOK:     r.CleanupOK,
			CleanupErrors: completeErrors,
		}
		if err := appendRewriteMapRecord(r.SgDir, complete); err != nil {
			return err
		}
	}

	// Oplog entry.
	extra := r.OplogExtra
	if extra == nil {
		extra = make(map[string]interface{})
	}
	extra["ref"] = ref
	extra["oldHead"] = r.OldHeadSHA
	extra["sha"] = newHeadSHA
	extra["rewritten"] = r.RewrittenCount
	if hooks.AnnotateTag != nil {
		extra["tagsRewritten"] = r.TagsRewrittenCount
	}
	_ = oplog.Append(r.SgDir, oplog.Entry{
		Op:    r.OpName,
		Extra: extra,
	})

	// Push hint (rlsbl-aware).
	hint := pushHintForRepo(ctx)
	infof(flags, "\n%s\n", hint)

	return nil
}

// pendingRewrite is one repository's rewrite, carried between the prepare and
// publish halves so that several repositories can be verified before any of them
// is published.
//
// It exists for the submodule scrubs: rewriting a file inside a submodule
// rewrites the submodule's commits AND the parent commits whose gitlinks point
// at them, and a verification failure on either side has to leave both sides
// untouched. Ctx is that repository's git context (the parent's plain context,
// or git.WithDir for a submodule), and Label names the repository in a failure
// message.
type pendingRewrite struct {
	Label  string
	Ctx    context.Context
	Result *RewriteResult
	Hooks  RewriteHooks

	plan *RefUpdatePlan
}

// prepareAll runs the pre-refs half over every rewrite, in order. Nothing has
// moved in ANY of the repositories when it returns, whether it succeeds or
// fails: that is the whole reason the halves are separate. On failure it returns
// the label of the repository that refused, for the caller's error message.
func prepareAll(flags globalFlags, rewrites []*pendingRewrite) (string, error) {
	for _, pr := range rewrites {
		plan, err := pr.Result.prepare(pr.Ctx, flags, pr.Hooks)
		if err != nil {
			return pr.Label, err
		}
		pr.plan = plan
	}
	return "", nil
}

// publishAll runs the apply half over every rewrite, in the order given.
//
// The order is the caller's declaration of which repository has to be readable
// first: a submodule scrub publishes the SUBMODULE before the parent, so a crash
// between the two leaves a parent whose gitlinks still name commits that exist,
// rather than a parent pointing into a submodule that has not moved yet.
func publishAll(flags globalFlags, cmd string, rewrites []*pendingRewrite) (string, error) {
	for _, pr := range rewrites {
		// Publishing prints the same lines for every repository, so a
		// multi-repository operation says which one it is about to publish.
		// The primary rewrite carries no label and needs no heading.
		if pr.Label != "" {
			infof(flags, "Publishing the %s rewrite...\n", pr.Label)
		}
		if err := pr.Result.publish(pr.Ctx, flags, cmd, pr.Hooks, pr.plan); err != nil {
			return pr.Label, err
		}
	}
	return "", nil
}

// recordTierB records one post-rewrite finding and prints it. Tier B findings
// never abort: they are what the command exits nonzero ABOUT.
func (r *RewriteResult) recordTierB(finding string) {
	r.TierBFailures = append(r.TierBFailures, finding)
	fmt.Fprintf(os.Stderr, "CRITICAL: %s\n", finding)
}

// pushHintForRepo returns the appropriate push hint based on whether the repo
// is managed by rlsbl (release tooling).
func pushHintForRepo(ctx context.Context) string {
	// AnchorRoot, not RepoRoot: the hint is decided by a filesystem probe
	// (.rlsbl/ under the root), and the repository that probe belongs to is the
	// one this context targets -- a submodule's own rewrite must read the
	// submodule's root, not whatever repository the process happens to stand in.
	root, err := git.AnchorRoot(ctx)
	if err != nil {
		// Can't determine repo root; fall back to default hint.
		return "To update the remote:\n  safegit push --refs both --force-with-lease"
	}
	return pushHintForDir(root)
}

// pushHintForDir returns the push hint for a given directory. Separated from
// pushHintForRepo so it can be unit-tested without a live git repo.
func pushHintForDir(dir string) string {
	if isRlsblManaged(dir) {
		return "This repository is managed by a release tool. Complete the rewrite via your release tooling."
	}
	return "To update the remote:\n  safegit push --refs both --force-with-lease"
}

// isRlsblManaged checks whether a directory contains .rlsbl/ or .rlsbl-monorepo/.
func isRlsblManaged(dir string) bool {
	for _, name := range []string{".rlsbl", ".rlsbl-monorepo"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
