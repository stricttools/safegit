package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/scan"
)

// verifyScrubbedFileContent is `scrub file`'s Tier A content check: it reads
// the target path out of every REWRITTEN commit and holds it to what the mode
// promised.
//
//   - delete: the path must be absent from every rewritten tree.
//   - replace: wherever the path is present it must hold the replacement blob,
//     and it must never hold one of the blobs the scrub replaced.
//
// The "never an old blob" half is the security property and is checked in both
// modes; the exact-content half is skipped for a target that a --remap-shas-in
// glob also rewrites, because the remap deliberately edits that file after the
// replacement and the two would contradict each other.
func verifyScrubbedFileContent(ctx context.Context, shaMap map[string]string, filePath, mode, newBlobSHA string, oldBlobSHAs map[string]bool, remapGlobs []string) error {
	remapped := matchAnyScope(remapGlobs, filePath)

	var failures []string
	for oldSHA, newSHA := range shaMap {
		if oldSHA == newSHA {
			continue
		}
		got := lookupBlobAtPath(ctx, newSHA, filePath)
		switch {
		case mode == "remove":
			if got != "" {
				failures = append(failures, fmt.Sprintf(
					"commit %s: %q is still present in the rewritten commit", shortSHA(oldSHA), filePath))
			}
		case got == "":
			// The path is not in this commit at all, which is what a commit
			// from before the file existed looks like.
		case oldBlobSHAs[got]:
			failures = append(failures, fmt.Sprintf(
				"commit %s: %q still holds the blob the scrub was replacing (%s)",
				shortSHA(oldSHA), filePath, shortSHA(got)))
		case got != newBlobSHA && !remapped:
			failures = append(failures, fmt.Sprintf(
				"commit %s: %q holds %s, not the replacement blob %s",
				shortSHA(oldSHA), filePath, shortSHA(got), shortSHA(newBlobSHA)))
		}
	}
	if len(failures) == 0 {
		return nil
	}
	sort.Strings(failures)
	return fmt.Errorf("%s", strings.Join(failures, "\n  "))
}

// verifyPatternAbsentFromTips is the Tier A pattern check: it scans exactly the
// objects the rewritten history is made of -- reached from the tips the WALK
// produced (RefUpdatePlan.WalkedTips) -- and refuses when the pattern is still
// there.
//
// It runs BEFORE any ref moves, which is the whole point: the rewritten
// commits are unreachable objects at that moment, so the whole-store scan
// cannot tell "the secret survived the rewrite" from "the secret is in the old
// history that has not been pruned yet". Scanning the new tips asks only about
// the history that is about to become the repository's, and a failure costs
// nothing because nothing has moved.
//
// The tips are the walked ones and no others. A secret sitting on a branch or a
// stale remote-tracking ref the walk never visited is not a defect in what this
// rewrite produced, so refusing over it would abort a correct rewrite and blame
// it for objects it never touched. That content is still in the repository,
// which is Tier B's whole-store question -- and Tier B names the refs holding
// it.
//
// When scope is set, blob matches outside it are expected to survive (the
// operation never claimed to touch them); commit messages and tag annotations
// are always in scope.
//
// The scope filter here is NARROWER than Tier B's (verifySecretRemovedScoped),
// and the difference is forced rather than chosen. This one has only the
// ATTRIBUTED path to judge a blob by, and attribution records one path per blob
// even though a blob can sit at several. Tier B additionally consults a
// scoped-blob set built from `rev-list --all --objects`, which cannot be built
// here: at this point the rewritten commits are unreachable objects and the refs
// still name the PRE-rewrite history, so that enumeration would answer about the
// history this check exists to replace.
//
// The consequence is that the asymmetry errs PERMISSIVE here: a blob whose
// recorded path is out of scope while another of its paths is in scope passes
// this check and is caught by Tier B instead. That escalates the outcome from a
// refusal (exit 30, nothing happened) to a finding (exit 31, the rewrite stands
// and is reported incomplete) -- never to silence.
func verifyPatternAbsentFromTips(ctx context.Context, pattern *regexp.Regexp, scope *string, tips []string) error {
	if len(tips) == 0 {
		return nil
	}

	opts := scan.ScanOpts{Tips: tips}
	results, err := scan.ScanObjects(ctx, pattern, opts)
	if err != nil {
		return fmt.Errorf("scanning the rewritten history: %w", err)
	}
	if len(results.Matches) == 0 {
		return nil
	}

	if scope != nil {
		if err := scan.AddAttribution(ctx, results, opts); err != nil {
			return fmt.Errorf("attributing matches in the rewritten history: %w", err)
		}
	}

	var failures []scan.Match
	for _, m := range results.Matches {
		if scope != nil && m.ObjectType == "blob" && !matchScope(*scope, m.Path) {
			continue
		}
		failures = append(failures, m)
	}
	if len(failures) == 0 {
		return nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "the pattern is still present in %d object(s) of the rewritten history:", len(failures))
	for _, m := range failures {
		where := m.Path
		if where == "" {
			where = m.ObjectType
		}
		fmt.Fprintf(&sb, "\n    %s %s (%s, line %d): %s", m.ObjectType, shortSHA(m.SHA), where, m.Line, m.Context)
	}
	return fmt.Errorf("%s", sb.String())
}

// verifySecretRemoved re-scans all git objects for the given pattern after a
// scrub rewrite and cleanup. If any matches survive, it returns an error
// listing where they were found, naming the refs that still reach them. A nil
// return means the secret is fully gone.
func verifySecretRemoved(ctx context.Context, pattern *regexp.Regexp) error {
	results, err := scan.ScanObjects(ctx, pattern, scan.ScanOpts{EntireHistory: true})
	if err != nil {
		return fmt.Errorf("re-scan failed: %w", err)
	}
	if len(results.Matches) == 0 {
		return nil
	}
	return fmt.Errorf("%s", describeSurvivingMatches(ctx, results.Matches))
}

// describeSurvivingMatches is Tier B's rendering of what still holds the
// pattern after the rewrite: the object lines, plus the refs that still reach
// each reachable object.
//
// Naming the refs is the whole point of the tier split. The rewrite walked one
// history; anything the pattern survives on lives on refs the walk never
// visited, and the operator cannot act on "blob 2b12e0c6 survived" but can act
// on "refs/heads/old still holds it".
func describeSurvivingMatches(ctx context.Context, matches []scan.Match) string {
	wanted := make(map[string]bool)
	for _, m := range matches {
		if m.Reachable {
			wanted[m.SHA] = true
		}
	}
	refsBySHA, attributionErr := refsHoldingObjects(ctx, wanted)
	return renderSurvivingMatches(matches, refsBySHA, attributionErr)
}

// renderSurvivingMatches formats surviving matches, one line per match, each
// followed by the refs that reach it when refsBySHA knows any. attributionErr
// is stated rather than swallowed: "no refs listed" must never be readable as
// "no ref holds it" when the lookup is what failed.
func renderSurvivingMatches(matches []scan.Match, refsBySHA map[string][]string, attributionErr error) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "secret still present in %d object(s):\n", len(matches))
	holders := make(map[string]bool)
	for _, m := range matches {
		reachable := "unreachable"
		if m.Reachable {
			reachable = "reachable"
		}
		fmt.Fprintf(&sb, "  %s %s (%s, line %d): %s\n",
			m.ObjectType, shortSHA(m.SHA), reachable, m.Line, m.Context)
		refs := refsBySHA[m.SHA]
		if len(refs) > 0 {
			fmt.Fprintf(&sb, "    still reachable from: %s\n", strings.Join(refs, ", "))
			for _, r := range refs {
				holders[r] = true
			}
		}
	}
	if attributionErr != nil {
		fmt.Fprintf(&sb, "  (could not determine which refs still hold this content: %v)\n", attributionErr)
	}
	if len(holders) > 0 {
		names := make([]string, 0, len(holders))
		for r := range holders {
			names = append(names, r)
		}
		sort.Strings(names)
		fmt.Fprintf(&sb, "This content is still on history this rewrite did not walk: %s\n", strings.Join(names, ", "))
		sb.WriteString("The rewrite covered this history only; scrub each of those refs as well.\n")
	}
	return sb.String()
}

// refsHoldingObjects answers, for a set of object SHAs, which refs still reach
// them: one `rev-list --objects` per ref, intersected with the set.
//
// It runs only on the failure path -- Tier B has already found surviving
// content -- so walking every ref once is paid exactly when there is something
// to report. Symbolic remote HEADs are skipped for the same reason the ref plan
// skips them: they name another ref that is listed on its own.
func refsHoldingObjects(ctx context.Context, wanted map[string]bool) (map[string][]string, error) {
	if len(wanted) == 0 {
		return nil, nil
	}
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname)", "refs/heads/", "refs/tags/", "refs/remotes/")
	if err != nil {
		return nil, fmt.Errorf("listing refs: %w", err)
	}

	byShA := make(map[string][]string)
	for _, refname := range git.SplitNonEmpty(out) {
		if strings.HasPrefix(refname, "refs/remotes/") && strings.HasSuffix(refname, "/HEAD") {
			continue
		}
		objects, _, err := git.Run(ctx, "rev-list", "--objects", refname)
		if err != nil {
			return byShA, fmt.Errorf("listing the objects %s reaches: %w", refname, err)
		}
		seen := make(map[string]bool)
		for _, line := range strings.Split(objects, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			sha := line
			if idx := strings.IndexByte(line, ' '); idx > 0 {
				sha = line[:idx]
			}
			if wanted[sha] && !seen[sha] {
				seen[sha] = true
				byShA[sha] = append(byShA[sha], refname)
			}
		}
	}
	return byShA, nil
}

// verifyOldBlobsRemoved checks that each of the given old blob SHAs no longer
// exists in the object store, unless it is still reachable from a current ref
// (which happens when the scrub range didn't cover all commits that reference
// the blob). Returns an error listing any unreachable-but-surviving blobs, or
// nil if all are gone or accounted for.
func verifyOldBlobsRemoved(ctx context.Context, oldBlobSHAs []string) error {
	// Build the set of all blob SHAs reachable from current refs.
	reachableBlobs, err := buildReachableObjectSet(ctx)
	if err != nil {
		return fmt.Errorf("building reachable blob set: %w", err)
	}

	var surviving []string
	for _, sha := range oldBlobSHAs {
		// If the blob is still reachable from a current ref (e.g., a branch
		// outside the scrub range), it's expected to survive.
		if reachableBlobs[sha] {
			continue
		}
		// Check if the object still exists in the store.
		_, _, err := git.Run(ctx, "cat-file", "-e", sha)
		if err == nil {
			// Object exists but is unreachable -- it should have been pruned.
			surviving = append(surviving, sha)
		}
	}
	if len(surviving) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%d old blob(s) still present in object store:\n", len(surviving)))
	for _, sha := range surviving {
		sb.WriteString(fmt.Sprintf("  blob %s\n", shortSHA(sha)))
	}
	return fmt.Errorf("%s", sb.String())
}

// buildReachableObjectSet returns the set of every object SHA reachable from
// any ref -- commits and trees as well as blobs. Uses git rev-list --all
// --objects, which lists all reachable objects with their paths (blobs have
// paths, commits/trees don't always).
//
// Both callers ask the same question of it: "is this pre-rewrite object one a
// surviving ref still legitimately reaches?" A yes means it is another branch's
// history rather than residue the cleanup failed to prune.
func buildReachableObjectSet(ctx context.Context) (map[string]bool, error) {
	stdout, _, err := git.Run(ctx, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, err
	}

	set := make(map[string]bool)
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Lines are "<sha>" (commits/trees) or "<sha> <path>" (blobs).
		sha := line
		if idx := strings.IndexByte(line, ' '); idx > 0 {
			sha = line[:idx]
		}
		set[sha] = true
	}
	return set, nil
}
