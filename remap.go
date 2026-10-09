package main

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/git"
	"github.com/stricttools/strictcli/go/strictcli"
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

	// treeCache maps (old tree SHA, path the tree sits at) to its remap
	// result. It is sound for the same reason blobCache is: a tree's result is
	// a function of its path and of its blobs' results, and those are final
	// once computed. The path is part of the key because the globs can match
	// directory components, so one subtree mounted at two paths may remap
	// differently.
	treeCache map[treeAtPath]treeRewrite

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
		treeCache:  make(map[treeAtPath]treeRewrite),
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
			strictcli.ExitNow(exitcode.Usage, fmt.Sprintf("invalid --remap-shas-in glob %q: %v", g, err))
		}
	}
}

// treeAtPath is a tree as it sits at one path of a commit's tree.
type treeAtPath struct {
	sha, pathPrefix string
}

// remapTree walks treeSHA recursively with path tracking and returns a new
// tree SHA with glob-matched blobs remapped (or the original SHA when nothing
// changed). pathPrefix is "" at the root and always ends in "/" otherwise.
//
// It descends only into the directories a glob can reach (see
// globsCanReach), and caches each result per tree and path (see
// remapState.treeCache): a walk visits every commit, and reading every tree
// of every commit made a remapping rewrite cost one git read per tree per
// commit.
//
// The second return value is the list of paths remapped, relative to treeSHA,
// which the caller adds to the commit's declared change set: a remapped file is
// a file the operation decided to change, and Tier A verification would
// otherwise read it as an unexplained change.
func (rs *remapState) remapTree(ctx context.Context, treeSHA, pathPrefix string, shaMap map[string]string) (string, []string, error) {
	key := treeAtPath{sha: treeSHA, pathPrefix: pathPrefix}
	if cached, ok := rs.treeCache[key]; ok {
		return cached.SHA, cached.Paths, nil
	}
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
			dir := pathPrefix + e.Path + "/"
			if !globsCanReach(rs.globs, dir) {
				continue
			}
			newSubSHA, subPaths, err := rs.remapTree(ctx, e.SHA, dir, shaMap)
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
		rs.treeCache[key] = treeRewrite{SHA: treeSHA}
		return treeSHA, nil, nil
	}
	newTreeSHA, err := git.MkTree(ctx, entries)
	if err != nil {
		return "", nil, fmt.Errorf("mktree: %w", err)
	}
	rs.treeCache[key] = treeRewrite{SHA: newTreeSHA, Paths: changedPaths}
	return newTreeSHA, changedPaths, nil
}

// globsCanReach reports whether any glob can match a path inside the
// directory dir ("a/b/", always ending in "/"), under matchScope's rules: the
// full path, then the basename.
//
// A glob with no "/" can match a basename at any depth, so it reaches every
// directory. A glob with a "/" can match only a full path, because a basename
// holds no "/" for it to match; path.Match never lets a wildcard cross a "/",
// so such a glob matches a path only segment by segment, and it reaches dir
// only when it has more segments than dir and its leading segments match
// dir's. A glob holding a character class or an escape is taken to reach
// every directory, rather than reasoning about what its "/" means.
func globsCanReach(globs []string, dir string) bool {
	dirSegs := strings.Split(strings.TrimSuffix(dir, "/"), "/")
	for _, g := range globs {
		if !strings.Contains(g, "/") || strings.ContainsAny(g, "[\\") {
			return true
		}
		globSegs := strings.Split(g, "/")
		if len(globSegs) <= len(dirSegs) {
			continue
		}
		reaches := true
		for i, seg := range dirSegs {
			if ok, _ := path.Match(globSegs[i], seg); !ok {
				reaches = false
				break
			}
		}
		if reaches {
			return true
		}
	}
	return false
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
	objType, err := git.ObjectType(ctx, cand)
	switch {
	case err != nil:
		return 0, fmt.Errorf("--remap-shas-in: reading the type of %s: %w", cand, err)
	case objType == "":
		class = candidateStale
		rs.stale[cand] = true
	case objType == "commit":
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
	var report strings.Builder
	fmt.Fprintf(&report, "--remap-shas-in: %d unresolvable 40-hex hash(es) left untouched (stale or foreign references)", len(rs.stale))
	if flags.verbose {
		list := make([]string, 0, len(rs.stale))
		for cand := range rs.stale {
			list = append(list, cand)
		}
		sort.Strings(list)
		for _, cand := range list {
			fmt.Fprintf(&report, "\n  %s", cand)
		}
	}
	warnf(flags, "%s", report.String())
}
