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

// dieFinalize exits with the exit code a Finalize failure calls for: a Tier A
// refusal says nothing happened (RewriteRefused), anything else is a failure
// partway through the shared pipeline (General). prefix names the repository
// the failure belongs to when a command finalizes more than one.
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
func (r *RewriteResult) Finalize(ctx context.Context, flags globalFlags, cmd string, hooks RewriteHooks) error {
	if err := r.Intent.validate(); err != nil {
		return err
	}

	// 1. Plan the ref updates. Tag objects are written here; no ref moves.
	plan, err := planRefUpdates(ctx, r.ShaMap, r.TaggerOldName, r.TaggerNewName, r.TaggerOldEmail, r.TaggerNewEmail, hooks.AnnotateTag, flags.verbose)
	if err != nil {
		return fmt.Errorf("planning ref updates: %w", err)
	}
	r.TagRewrites = plan.TagRewrites
	r.AnnotationTagRewrites = plan.AnnotationTagRewrites
	r.TagsRewrittenCount = plan.TagsRewritten

	// 2a. The preservation check.
	infof(flags, "Verifying the rewrite before it is published...\n")
	if failures := verifyIntendedChanges(ctx, r.ShaMap, r.Intent); len(failures) > 0 {
		return refuse("the rewrite did not do what the operation declared it would:", failures)
	}

	// 2b. The command's own pre-refs verification.
	if hooks.TierA != nil {
		if err := hooks.TierA(ctx, plan); err != nil {
			return refuse("the rewritten history failed verification:", []string{err.Error()})
		}
	}

	// 2c. The cleanliness re-check, under the rewrite lock. The rewrite refused
	// to start on a dirty tree, so anything here appeared while it was running.
	foreign, err := foreignWorktreeState(ctx, r.OldHeadSHA)
	if err != nil {
		return err
	}
	if len(foreign) > 0 {
		return refuse("the working tree changed while the rewrite was running, so publishing it would overwrite work this command did not make:", foreign)
	}

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

// recordTierB records one post-rewrite finding and prints it. Tier B findings
// never abort: they are what the command exits nonzero ABOUT.
func (r *RewriteResult) recordTierB(finding string) {
	r.TierBFailures = append(r.TierBFailures, finding)
	fmt.Fprintf(os.Stderr, "CRITICAL: %s\n", finding)
}

// pushHintForRepo returns the appropriate push hint based on whether the repo
// is managed by rlsbl (release tooling).
func pushHintForRepo(ctx context.Context) string {
	root, err := git.RepoRoot(ctx)
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
