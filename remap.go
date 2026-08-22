package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
)

// --remap-shas-in support: during a rewrite walk, files matching the given
// globs have full 40-hex commit hashes that appear as keys in the growing
// SHA map replaced with the rewritten SHAs. This keeps files that reference
// commit hashes (e.g. JSONL changelogs) self-consistent at every commit of
// the rewritten history.
//
// Limitation: remapping applies to the repository being walked only.
// Submodule history walks do not apply --remap-shas-in.

// hexRunRe matches maximal runs of 40+ lowercase hex characters. Only runs of
// exactly 40 characters are treated as commit-hash candidates: shorter runs
// (abbreviated hashes) never match, and longer runs (e.g. SHA-256 digests)
// are matched as one run and then skipped, so their 40-char prefixes are
// never corrupted.
var hexRunRe = regexp.MustCompile(`[0-9a-f]{40,}`)

// candidateClass is the stable classification of a 40-hex candidate that is
// NOT present in the SHA map.
type candidateClass int

const (
	// candidateOutsideRange resolves to a commit outside the rewrite range
	// (e.g. an ancestor of --from). Its SHA stays valid; leave untouched.
	candidateOutsideRange candidateClass = iota
	// candidateNonCommit resolves to a non-commit object (blob/tree/tag);
	// not a commit reference, leave untouched.
	candidateNonCommit
	// candidateStale does not resolve to any object; leave untouched and
	// count for the end-of-walk report.
	candidateStale
)

// remapState carries the per-walk state for --remap-shas-in. One instance per
// walkAndRewrite call; not safe for concurrent use (the walk is sequential).
type remapState struct {
	globs    []string
	rangeSet map[string]bool // all commit SHAs in the rewrite range

	// blobCache maps old blob SHA -> remapped blob SHA (same SHA when
	// unchanged). Caching is sound even though the SHA map grows during the
	// walk: once a blob is remapped successfully, every candidate in it had
	// a final state — SHA-map entries are never mutated after insertion,
	// out-of-range/stale/non-commit classifications are stable while old
	// objects still exist (pruning happens later, in Finalize), and any
	// in-range-but-unwalked candidate aborts the walk with a hard error
	// instead of producing a time-varying result.
	blobCache map[string]string

	classCache map[string]candidateClass // stable classifications (see above)
	stale      map[string]bool           // unresolvable candidates encountered
}

// newRemapState builds the walk-scoped remap state. rangeSHAs is the exact
// topo-ordered commit list handed to walkAndRewrite.
func newRemapState(globs []string, rangeSHAs []string) *remapState {
	rangeSet := make(map[string]bool, len(rangeSHAs))
	for _, sha := range rangeSHAs {
		rangeSet[sha] = true
	}
	return &remapState{
		globs:      globs,
		rangeSet:   rangeSet,
		blobCache:  make(map[string]string),
		classCache: make(map[string]candidateClass),
		stale:      make(map[string]bool),
	}
}

// matchAnyScope reports whether filePath matches any of the globs, using the
// same semantics as --scope (full path, then basename).
func matchAnyScope(globs []string, filePath string) bool {
	for _, g := range globs {
		if matchScope(g, filePath) {
			return true
		}
	}
	return false
}

// validateRemapGlobs dies with a usage error when any glob is malformed.
// Mirrors the --scope parse-time validation.
func validateRemapGlobs(globs []string) {
	for _, g := range globs {
		if _, err := path.Match(g, ""); err != nil {
			die(exitcode.Usage, fmt.Sprintf("invalid --remap-shas-in glob %q: %v", g, err))
		}
	}
}

// remapTree walks treeSHA recursively with path tracking and returns a new
// tree SHA with glob-matched blobs remapped (or the original SHA when nothing
// changed). pathPrefix is "" at the root and always ends in "/" otherwise.
//
// No tree-level cache is used: glob patterns can match directory components,
// so the same subtree mounted at different paths may remap differently — a
// tree-SHA-keyed cache would be unsound. The expensive per-blob work is
// cached in blobCache instead (see remapState).
//
// The second return value is the list of paths remapped, relative to treeSHA,
// which the caller adds to the commit's declared change set: a remapped file is
// a file the operation decided to change, and Tier A verification would
// otherwise read it as an unexplained change.
func (rs *remapState) remapTree(ctx context.Context, treeSHA, pathPrefix string, shaMap map[string]string) (string, []string, error) {
	entries, err := git.LsTree(ctx, treeSHA)
	if err != nil {
		return "", nil, fmt.Errorf("ls-tree %s: %w", treeSHA, err)
	}

	var changedPaths []string
	for i, e := range entries {
		switch e.ObjectType {
		case "blob":
			fullPath := pathPrefix + e.Path
			if !matchAnyScope(rs.globs, fullPath) {
				continue
			}
			newSHA, err := rs.remapBlob(ctx, e.SHA, shaMap)
			if err != nil {
				return "", nil, fmt.Errorf("remapping hashes in %s: %w", fullPath, err)
			}
			if newSHA != e.SHA {
				entries[i].SHA = newSHA
				changedPaths = append(changedPaths, e.Path)
			}
		case "tree":
			newSubSHA, subPaths, err := rs.remapTree(ctx, e.SHA, pathPrefix+e.Path+"/", shaMap)
			if err != nil {
				return "", nil, err
			}
			if newSubSHA != e.SHA {
				entries[i].SHA = newSubSHA
				for _, p := range subPaths {
					changedPaths = append(changedPaths, e.Path+"/"+p)
				}
			}
			// Gitlinks (ObjectType "commit") pass through untouched.
		}
	}

	if len(changedPaths) == 0 {
		return treeSHA, nil, nil
	}
	newTreeSHA, err := git.MkTree(ctx, entries)
	if err != nil {
		return "", nil, fmt.Errorf("mktree: %w", err)
	}
	return newTreeSHA, changedPaths, nil
}

// remapBlob reads a blob, remaps 40-hex commit hashes in its content, writes
// the new blob when changed, and returns the resulting blob SHA. Binary blobs
// are skipped (returned unchanged).
func (rs *remapState) remapBlob(ctx context.Context, blobSHA string, shaMap map[string]string) (string, error) {
	if cached, ok := rs.blobCache[blobSHA]; ok {
		return cached, nil
	}

	content, err := git.CatFileBlob(ctx, blobSHA)
	if err != nil {
		return "", fmt.Errorf("reading blob %s: %w", blobSHA, err)
	}
	if isBinaryContent(content) {
		rs.blobCache[blobSHA] = blobSHA
		return blobSHA, nil
	}

	newContent, err := rs.remapContent(ctx, content, shaMap)
	if err != nil {
		return "", err
	}
	if bytes.Equal(content, newContent) {
		rs.blobCache[blobSHA] = blobSHA
		return blobSHA, nil
	}

	newSHA, err := git.HashObjectWriteBytes(ctx, newContent)
	if err != nil {
		return "", fmt.Errorf("writing remapped blob: %w", err)
	}
	rs.blobCache[blobSHA] = newSHA
	return newSHA, nil
}

// remapContent replaces exactly-40-hex candidates that are non-identity keys
// in shaMap with their rewritten SHAs. Candidates absent from shaMap are
// classified: inside the rewrite range is a hard error (see classify),
// everything else is left untouched.
func (rs *remapState) remapContent(ctx context.Context, content []byte, shaMap map[string]string) ([]byte, error) {
	locs := hexRunRe.FindAllIndex(content, -1)
	if len(locs) == 0 {
		return content, nil
	}

	var out []byte
	last := 0
	changed := false
	for _, loc := range locs {
		if loc[1]-loc[0] != 40 {
			// Longer hex run (e.g. SHA-256): not a SHA-1 commit hash.
			continue
		}
		cand := string(content[loc[0]:loc[1]])

		if mapped, ok := shaMap[cand]; ok {
			if mapped == cand {
				// Identity entry: in range, already walked, unchanged.
				continue
			}
			out = append(out, content[last:loc[0]]...)
			out = append(out, mapped...)
			last = loc[1]
			changed = true
			continue
		}

		if _, err := rs.classify(ctx, cand); err != nil {
			return nil, err
		}
		// All non-error classes mean "leave untouched."
	}

	if !changed {
		return content, nil
	}
	out = append(out, content[last:]...)
	return out, nil
}

// classify resolves a candidate that is not in the SHA map. A commit inside
// the rewrite range that has not been walked yet is a hard error: the walk is
// parents-before-children, so an ancestor reference would already be mapped —
// an unmapped in-range reference means the file names a commit that is NOT an
// ancestor of the commit containing it, and no self-consistent remap exists.
func (rs *remapState) classify(ctx context.Context, cand string) (candidateClass, error) {
	if rs.rangeSet[cand] {
		return 0, fmt.Errorf(
			"--remap-shas-in: found full hash %s of a commit inside the rewrite range that has not been rewritten yet at this point of the walk; "+
				"this means the file references a commit that is not an ancestor of the commit containing the reference "+
				"(e.g. a hash written before that commit existed, or a cross-branch reference) — "+
				"no self-consistent remap is possible; aborting before any refs were changed", cand)
	}
	if class, ok := rs.classCache[cand]; ok {
		return class, nil
	}

	// During the walk old objects still exist (pruning happens in Finalize),
	// so object-store lookups see the pre-rewrite world.
	var class candidateClass
	out, _, err := git.Run(ctx, "cat-file", "-t", cand)
	switch {
	case err != nil:
		class = candidateStale
		rs.stale[cand] = true
	case strings.TrimSpace(out) == "commit":
		class = candidateOutsideRange
	default:
		class = candidateNonCommit
	}
	rs.classCache[cand] = class
	return class, nil
}

// reportStale prints the end-of-walk summary of unresolvable candidates.
// Non-fatal by design: stale hashes (e.g. references to commits scrubbed in a
// previous rewrite) are left untouched.
func (rs *remapState) reportStale(flags globalFlags) {
	if rs == nil || len(rs.stale) == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "note: --remap-shas-in: %d unresolvable 40-hex hash(es) left untouched (stale or foreign references)\n", len(rs.stale))
	if flags.verbose {
		list := make([]string, 0, len(rs.stale))
		for cand := range rs.stale {
			list = append(list, cand)
		}
		sort.Strings(list)
		for _, cand := range list {
			fmt.Fprintf(os.Stderr, "  %s\n", cand)
		}
	}
}
