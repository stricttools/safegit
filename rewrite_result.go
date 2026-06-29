package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
)

// AnnotationRewriteFunc rewrites tag annotation text after refs have been
// updated. It receives the old-to-new SHA map for commit reference remapping.
type AnnotationRewriteFunc func(ctx context.Context, shaMap map[string]string) error

// VerifyFunc performs command-specific post-rewrite verification (e.g.,
// re-scanning for secrets, comparing author snapshots).
type VerifyFunc func(ctx context.Context) error

// RewriteResult collects the outputs of a history rewrite so that Finalize
// can execute the shared post-rewrite pipeline (ref updates, cleanup,
// verification, oplog, push hint).
type RewriteResult struct {
	// Core rewrite outputs
	ShaMap         map[string]string // old SHA -> new SHA mapping
	TagRewrites    []TagRewrite      // tag rewrite records from updateRefs
	RewrittenCount int               // number of commits actually rewritten

	// Pre-rewrite state
	OldHeadSHA string // HEAD before the rewrite

	// Safegit paths
	SgDir string // .git/safegit directory path

	// Tagger identity for updateRefs (rewrite-author needs tagger matching;
	// zero values mean "no tagger matching", which is the default for scrub).
	TaggerOldName  string
	TaggerNewName  string
	TaggerOldEmail string
	TaggerNewEmail string

	// Oplog metadata
	OpName     string                 // operation name ("scrub-file", "scrub-match", "rewrite-author")
	OplogExtra map[string]interface{} // command-specific oplog fields

	// Post-Finalize outputs (populated by Finalize for callers to read)
	NewHeadSHA string // HEAD after the rewrite
	Ref        string // current ref name (e.g. "refs/heads/main" or "HEAD (detached)")
}

// Finalize runs the shared post-rewrite pipeline. The execution order is:
//
//  1. updateRefs — update branch and tag refs to point at rewritten commits
//  2. annotationRewriteFunc — rewrite tag annotation text (nil to skip)
//  3. SyncMainIndexWithWorktree — sync the shared index with rewritten HEAD
//  4. untrackProtectedPaths — remove tracked-but-gitignored files from index
//  5. cleanupAfterRewrite — expire tainted reflog entries, repack, prune
//  6. verifyFunc — command-specific verification (nil to skip)
//  7. Resolve new HEAD SHA
//  8. Resolve current ref
//  9. oplog.Append — record the operation
//  10. Push hint — print rlsbl-aware or default push instructions
func (r *RewriteResult) Finalize(ctx context.Context, flags globalFlags, cmd string, annotationRewriteFunc AnnotationRewriteFunc, verifyFunc VerifyFunc) error {
	// 1. Update refs (passes tagger identity for rewrite-author; zero values
	// for scrub commands mean "no tagger matching").
	infof(flags, "Updating refs...\n")
	tagRewrites, err := updateRefs(ctx, r.ShaMap, r.TaggerOldName, r.TaggerNewName, r.TaggerOldEmail, r.TaggerNewEmail, flags.verbose)
	if err != nil {
		return fmt.Errorf("updating refs: %w", err)
	}
	r.TagRewrites = tagRewrites

	// 2. Annotation rewriting (e.g., scrub-match rewrites tag annotation text)
	if annotationRewriteFunc != nil {
		if err := annotationRewriteFunc(ctx, r.ShaMap); err != nil {
			return fmt.Errorf("rewriting tag annotations: %w", err)
		}
	}

	// 3-4. Sync main index with working tree and untrack protected paths.
	// This must happen before cleanup so that the index no longer references
	// old (pre-rewrite) objects, allowing repack/prune to remove them.
	protectedPaths, syncErr := git.SyncMainIndexWithWorktree(ctx, "HEAD")
	if syncErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to sync main index: %v\n", syncErr)
	}
	untrackProtectedPaths(ctx, flags, protectedPaths)

	// 5. Post-rewrite cleanup: expire tainted reflog entries and prune old objects
	if err := cleanupAfterRewrite(ctx, flags, cmd, r.ShaMap, r.SgDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: post-rewrite cleanup: %v\n", err)
	}

	// 6. Command-specific verification
	if verifyFunc != nil {
		if err := verifyFunc(ctx); err != nil {
			return fmt.Errorf("verification failed: %w", err)
		}
	}

	// 7. Resolve new HEAD SHA
	newHeadSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolving new HEAD: %w", err)
	}
	r.NewHeadSHA = newHeadSHA

	// 8. Resolve current ref
	ref, _ := git.HeadRef(ctx)
	if ref == "" {
		ref = "HEAD (detached)"
	}
	r.Ref = ref

	// 9. Oplog entry
	extra := r.OplogExtra
	if extra == nil {
		extra = make(map[string]interface{})
	}
	extra["ref"] = ref
	extra["oldHead"] = r.OldHeadSHA
	extra["sha"] = newHeadSHA
	extra["rewritten"] = r.RewrittenCount
	_ = oplog.Append(r.SgDir, oplog.Entry{
		Op:    r.OpName,
		Extra: extra,
	})

	// 9.5. Auto-append scrub policy for scrub-match operations.
	if r.OpName == "scrub-match" {
		if patternStr, ok := extra["pattern"].(string); ok && patternStr != "" {
			policy := ScrubPolicy{
				Type:        "match",
				Pattern:     patternStr,
				Reason:      extraString(extra, "reason"),
				CreatedByOp: r.OpName,
			}
			if scopeStr := extraString(extra, "scope"); scopeStr != "" {
				policy.Scope = scopeStr
			}
			if err := appendScrubPolicy(r.SgDir, policy); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to append scrub policy: %v\n", err)
			}
		}
	}

	// 10. Push hint (rlsbl-aware)
	hint := pushHintForRepo(ctx)
	infof(flags, "\n%s\n", hint)

	return nil
}

// extraString extracts a string value from a map[string]interface{}, returning
// "" if the key is absent or not a string.
func extraString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// pushHintForRepo returns the appropriate push hint based on whether the repo
// is managed by rlsbl (release tooling).
func pushHintForRepo(ctx context.Context) string {
	root, err := git.RepoRoot(ctx)
	if err != nil {
		// Can't determine repo root; fall back to default hint.
		return "To update the remote:\n  safegit push --both-branches-and-tags --force-with-lease"
	}
	return pushHintForDir(root)
}

// pushHintForDir returns the push hint for a given directory. Separated from
// pushHintForRepo so it can be unit-tested without a live git repo.
func pushHintForDir(dir string) string {
	if isRlsblManaged(dir) {
		return "This repository is managed by a release tool. Complete the rewrite via your release tooling."
	}
	return "To update the remote:\n  safegit push --both-branches-and-tags --force-with-lease"
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
