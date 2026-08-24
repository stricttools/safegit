package commit

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/trailer"
)

// Inferred moves: the records safegit mints for moves the caller did NOT
// declare.
//
// A declared move is the caller stating a fact (moved.go). An inferred one is
// safegit reading the commit's own delta and finding a fact the delta WITNESSES
// on its own: a path deleted and a path added carrying the same blob, where
// nothing else in either tree could be meant instead. The evidence is the
// commit's objects, never a similarity score -- safegit runs no rename
// detection and never will.
//
// Everything here is a FENCE. Each one exists to make the answer forced rather
// than probable, and a candidate that clears none of them mints nothing rather
// than minting a guess:
//
//  1. SUPPRESSION FIRST. A path a declared record names is out of both
//     candidate sets before pairing begins, and out of the tree listings fence 4
//     reads -- the human already resolved that question, and their answer
//     un-blocks the rest (a blob at two deleted paths, one of them declared,
//     leaves the other free to be judged on its own, which it cannot be while
//     the declared path still counts as a rival occurrence of the blob).
//  2. REGULAR FILES ONLY. Symlinks, gitlinks and type changes never pair; a
//     mode change between 100644 and 100755 across a pair is fine, because
//     making a moved file executable is still moving it.
//  3. ONE-TO-ONE EXACTNESS. A blob with two candidates on either side mints
//     nothing for that blob. There is no tie-break, because there is no
//     evidence a tie-break could read.
//  4. UNIQUENESS IN BOTH TREES. The deletion's blob has to sit at exactly one
//     path in the parent tree, and the addition's blob at no other path in the
//     commit's own tree. A blob that also lives elsewhere means the content was
//     never the identity of a path, so its disappearance from one and
//     appearance at another says nothing.
//
// What clears all four is collapsed where a whole subtree moved, and then faces
// the CAP: a commit carrying more inferred moves than moveInferenceCap records
// none of them and says so. Both live in infer_subtrees.go.
//
// The refusals are not silent. One aggregate line goes to stderr naming how
// many candidates were declined and pointing at --moved, because the answer to
// "safegit did not record my move" is always the same: declare it.

// regularFileModes are the two modes an inferred pair may carry on either side.
// Everything else -- a symlink, a gitlink, a tree -- is content whose identity
// is not its blob, and pairing it would be pairing something other than a file
// move.
var regularFileModes = map[string]bool{"100644": true, "100755": true}

// emptyBlobNames are the empty blob's object name under each hash algorithm git
// supports. The empty blob never pairs: every empty file in a repository shares
// one object name, so its presence at two paths is not evidence of anything.
//
// They are written out rather than computed because computing them means an
// extra git invocation on a path whose whole point is that it costs nothing
// when there is nothing to infer. Both are fixed forever by the hash
// algorithms themselves.
var emptyBlobNames = map[string]bool{
	"e69de29bb2d1d6434b8b29ae775ad8c2e48c5391":                         true, // sha1
	"473a0f4c3be8a93681a267e3b1e9a7dcda1185436fe141f7749120a303721813": true, // sha256
}

// RefusedMove is one move safegit's delta suggested and its fences declined to
// record: the paths on each side and why nothing was written.
//
// A refusal is reported rather than dropped, because the caller who performed
// the move needs to know safegit did not record it -- and the answer is always
// to declare it with --moved.
//
// Subphase 6.5 puts these on the commit payload; until then they ride the
// result struct and the aggregate stderr notice.
type RefusedMove struct {
	// Old and New are the candidate paths on each side. A one-to-one candidate
	// that a uniqueness fence turned down has one path in each; an ambiguous
	// blob has more than one on the side that was ambiguous.
	Old []string
	New []string
	// Reason says which fence declined it, in the words the notice would use.
	Reason string
}

// The reasons a candidate is refused, so a reader and a test name the same
// thing.
const (
	refusedAmbiguous       = "the same content is deleted from or added at more than one path"
	refusedParentNotUnique = "the deleted content also sits at another path in the parent tree"
	refusedNewNotUnique    = "the added content also sits at another path in this commit's tree"
	refusedOverlaps        = "the move would overlap a move this commit already states"
)

// moveInference is the once-per-operation state the inference keeps across
// compare-and-swap attempts. One instance per commit operation, exactly like
// nativeHooks and for a related reason: the commit-msg hook runs once and its
// answer is cached, so the records in that cached message were minted once too.
//
// THE COMPARE-AND-SWAP RULE. Inference reads the ATTEMPT'S OWN delta, which a
// concurrent session moving the ref can change underneath it. So attempt 1's
// inferred pair set is retained HERE, as data, and every later attempt
// recomputes and compares against it:
//
//   - the same set -- the ordinary case -- proceeds with the ids minted on
//     attempt 1, so the record a commit carries does not depend on how many
//     attempts it took;
//   - a DIFFERENT set aborts the operation, because the cached message already
//     holds attempt 1's records and they are no longer what the delta
//     witnesses. Re-running is the answer, and it says so.
//
// The comparison is over the RETAINED PAIRS, never over the cached message
// text: a rewriting commit-msg hook may have reordered or stripped the record
// lines, and a text comparison would abort a commit that was never in a race.
type moveInference struct {
	// enabled is false where inference does not apply at all -- see
	// newMoveInference.
	enabled bool

	// declared is the pair set the caller already stated, whose paths are
	// suppressed before any pairing happens.
	declared []trailer.Pair

	// done marks that attempt 1 has run and the three fields below hold its
	// answer.
	done    bool
	pairs   []trailer.Pair
	lines   []string
	refused []RefusedMove

	// capped is how many records the commit would have carried when the cap
	// turned them all down, and zero otherwise.
	capped int
}

// newMoveInference prepares inference for one commit operation.
//
// Two requests get none of it:
//
//   - a SHARED-INDEX commit (a conclusion of a merge, cherry-pick or revert).
//     Its content is whatever the operator staged in the repository's index,
//     which git filled from an operation that may have moved content for
//     reasons of its own; the delta is not this caller's authorship and pairing
//     it would attribute someone else's move to this commit. The one place a
//     conclusion DOES carry records is the revert inverse, which is minted from
//     the reverted commit's own message and arrives as MovedRecords.
//   - an operation with no delta to read at all, which is nothing today.
//
// AMEND AND REWORD reach none of this: they have their own attempt loop
// (tryAmend, tryReword) and mint nothing for now. Subphase 6.4 gives amend its
// own arm -- inference against the AUTHORING EVENT's delta, the new tree
// against the replaced tip's first parent -- and reword keeps minting nothing,
// because rewording changes no tree.
func newMoveInference(req CommitRequest, movedTrailers []string) *moveInference {
	return &moveInference{
		enabled:  req.IndexBase == IndexBaseParentTree,
		declared: declaredPairs(movedTrailers),
	}
}

// declaredPairs reads the pairs out of the move records this commit already
// carries -- the caller's --moved declarations, and the already-minted records
// `safegit mv` and a single revert hand in. Both are statements about this
// commit's own moves, and both suppress.
func declaredPairs(movedTrailers []string) []trailer.Pair {
	if len(movedTrailers) == 0 {
		return nil
	}
	var pairs []trailer.Pair
	for _, kv := range trailer.ParseTrailerBlock(strings.Join(movedTrailers, "\n")) {
		if kv.Key != trailer.MovedKey {
			continue
		}
		record, err := trailer.ParseRecord(kv.Value)
		if err != nil {
			// A line this version cannot parse suppresses nothing. It is the
			// caller's own content and is written to the message either way;
			// treating an unreadable line as covering some path would be
			// guessing which one.
			continue
		}
		pairs = append(pairs, trailer.Pair{Old: record.Old, New: record.New})
	}
	return pairs
}

// records returns the move-record lines this attempt contributes, minting them
// on the first attempt and re-checking every later one against that answer.
//
// parentTreeSHA is the tree of the commit being built on, empty for a root
// commit; newTreeSHA is the tree just written.
func (m *moveInference) records(ctx context.Context, changed []git.ChangedPath, parentTreeSHA, newTreeSHA string) ([]string, error) {
	if !m.enabled {
		return nil, nil
	}

	pairs, refused, capped, err := inferMoves(ctx, changed, m.declared, parentTreeSHA, newTreeSHA)
	if err != nil {
		return nil, err
	}

	if !m.done {
		m.done = true
		m.pairs = pairs
		m.refused = refused
		m.capped = capped
		lines := make([]string, 0, len(pairs))
		for _, p := range pairs {
			record, err := trailer.NewRecord(p.Old, p.New)
			if err != nil {
				// Every pair was validated before it got here, so this is an id
				// minting failure and nothing else.
				return nil, fmt.Errorf("minting a move record for %s: %w", trailer.EncodePair(p.Old, p.New), err)
			}
			// SEAM for subphase 6.4: the `observed` origin token goes on the
			// record HERE, once Record carries an Origin field and the encoder
			// emits it. Until then a minted record is written in the same
			// spelling as a declared one -- the grammar reserves the origin slot
			// and refuses a bare keyword standing in it, so writing the token
			// early would produce a record this version cannot read back.
			lines = append(lines, trailer.RecordLine(record))
		}
		m.lines = lines
		m.notice()
		return m.lines, nil
	}

	if !samePairs(m.pairs, pairs) {
		return nil, &CommitError{
			Code: exitcode.General,
			Message: "another session moved the branch while this commit was being built, and the moves this " +
				"commit's own delta witnesses changed with it; the message was already composed against the " +
				"earlier answer, so nothing was committed -- run the command again",
		}
	}
	return m.lines, nil
}

// notice writes the one aggregate stderr line an operation gets, if it has
// anything to say. Once per operation, never once per attempt, and never
// suppressed: a caller whose move went unrecorded learns it here or not at all.
func (m *moveInference) notice() {
	switch {
	case m.capped > 0:
		fmt.Fprintf(os.Stderr, "notice: this commit's delta witnesses %d moves, more than the %d safegit "+
			"records on its own; none were recorded -- declare the ones you mean with --moved 'old -> new'\n",
			m.capped, moveInferenceCap)
	case len(m.refused) > 0:
		fmt.Fprintf(os.Stderr, "notice: %d possible move(s) in this commit were not recorded, because the "+
			"repository does not single them out; declare the ones you mean with --moved 'old -> new'\n",
			len(m.refused))
	}
}

// samePairs reports whether two inferred pair sets state the same thing. Both
// are produced in sorted order, so the comparison is elementwise.
func samePairs(a, b []trailer.Pair) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// inferMoves is the whole engine: candidates out of the raw delta, the four
// fences, the subtree collapse, and the cap.
//
// capped is the number of records the commit would have carried when the cap
// turned them all down, and zero otherwise.
func inferMoves(ctx context.Context, changed []git.ChangedPath, declared []trailer.Pair, parentTreeSHA, newTreeSHA string) (pairs []trailer.Pair, refused []RefusedMove, capped int, err error) {
	suppressed := suppressedPaths(declared)

	// Fence 1 and 2, applied while the candidate sets are built: a declared
	// path is out, and anything that is not a regular file never was in.
	deletions := map[string][]string{} // blob -> parent paths
	additions := map[string][]string{} // blob -> new paths
	for _, c := range changed {
		switch c.Status {
		case "D":
			if !regularFileModes[c.SrcMode] || emptyBlobNames[c.SrcSHA] || suppressed(c.Path) {
				continue
			}
			deletions[c.SrcSHA] = append(deletions[c.SrcSHA], c.Path)
		case "A":
			if !regularFileModes[c.DstMode] || emptyBlobNames[c.DstSHA] || suppressed(c.Path) {
				continue
			}
			additions[c.DstSHA] = append(additions[c.DstSHA], c.Path)
		}
	}

	// The zero-candidate fast path: no blob is on both sides, so there is
	// nothing to pair and no tree needs listing. This is every ordinary commit,
	// and it costs one map walk.
	var blobs []string
	for sha := range deletions {
		if _, ok := additions[sha]; ok {
			blobs = append(blobs, sha)
		}
	}
	if len(blobs) == 0 {
		return nil, nil, 0, nil
	}
	sort.Strings(blobs)

	// Fence 3, one-to-one exactness. A blob with more than one candidate on
	// either side mints nothing for that blob, and says so.
	type candidate struct{ old, new string }
	var candidates []candidate
	for _, sha := range blobs {
		dels, adds := deletions[sha], additions[sha]
		if len(dels) != 1 || len(adds) != 1 {
			sort.Strings(dels)
			sort.Strings(adds)
			refused = append(refused, RefusedMove{Old: dels, New: adds, Reason: refusedAmbiguous})
			continue
		}
		candidates = append(candidates, candidate{old: dels[0], new: adds[0]})
	}
	if len(candidates) == 0 {
		return nil, refused, 0, nil
	}

	// Fence 4 needs both trees seen whole. The listings are per ATTEMPT: the
	// parent tree is the one this attempt resolved, and the new tree is the one
	// this attempt just wrote, so neither can be the instance intake built
	// before the loop against a tip that has since moved.
	//
	// They are built with the SAME suppression the candidate sets were built
	// with, because suppression is one rule and not two: a path the caller
	// declared away is answered for, so it is not a rival occurrence of a blob
	// either. Without that, a declaration could never un-block anything -- the
	// blob it names still sat at two parent paths, and the fence turned the
	// other half down for a reason the human had already settled.
	parentTree, err := newBlobIndex(ctx, parentTreeSHA, suppressed)
	if err != nil {
		return nil, nil, 0, err
	}
	newTree, err := newBlobIndex(ctx, newTreeSHA, suppressed)
	if err != nil {
		return nil, nil, 0, err
	}

	for _, c := range candidates {
		blob := parentTree.blobAt(c.old)
		switch {
		case len(parentTree.pathsOf(blob)) != 1:
			refused = append(refused, RefusedMove{Old: []string{c.old}, New: []string{c.new}, Reason: refusedParentNotUnique})
		case len(newTree.pathsOf(newTree.blobAt(c.new))) != 1:
			refused = append(refused, RefusedMove{Old: []string{c.old}, New: []string{c.new}, Reason: refusedNewNotUnique})
		default:
			pairs = append(pairs, trailer.Pair{Old: c.old, New: c.new})
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Old < pairs[j].Old })

	// A delta that fully witnesses a uniform prefix mapping says one thing, so
	// it is written as one record -- before the cap counts, which is why moving
	// a large directory never reaches it.
	pairs = collapseSubtrees(pairs, parentTree, newTree)

	// Nothing safegit mints may contradict what the commit already states, or
	// another minted record. trailer.Overlap is the one authority for that
	// question, shared with the --moved and `safegit mv` spellings.
	pairs, overlapRefusals := dropOverlapping(pairs, declared)
	refused = append(refused, overlapRefusals...)

	if len(pairs) > moveInferenceCap {
		refused = append(refused, capRefusals(pairs)...)
		sortRefusals(refused)
		return nil, refused, len(pairs), nil
	}

	sortRefusals(refused)
	return pairs, refused, 0, nil
}

// sortRefusals puts the refusals in a stable order, so two runs of the same
// commit report them the same way.
func sortRefusals(refused []RefusedMove) {
	sort.SliceStable(refused, func(i, j int) bool {
		a, b := refused[i], refused[j]
		if len(a.Old) > 0 && len(b.Old) > 0 && a.Old[0] != b.Old[0] {
			return a.Old[0] < b.Old[0]
		}
		return a.Reason < b.Reason
	})
}

// suppressedPaths turns the declared records into the predicate that keeps
// their paths out of both candidate sets.
//
// A SUBTREE record suppresses every path under its prefix, on both sides: the
// record already speaks for all of them, and pairing a file underneath it would
// state the same move twice in two different shapes.
func suppressedPaths(declared []trailer.Pair) func(string) bool {
	if len(declared) == 0 {
		return func(string) bool { return false }
	}
	exact := make(map[string]bool)
	var prefixes []string
	for _, p := range declared {
		for _, side := range []string{p.Old, p.New} {
			if strings.HasSuffix(side, "/") {
				prefixes = append(prefixes, side)
				continue
			}
			exact[side] = true
		}
	}
	return func(path string) bool {
		if exact[path] {
			return true
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(path, prefix) {
				return true
			}
		}
		return false
	}
}

// dropOverlapping removes any minted pair that overlaps a declared pair or
// another minted one, and reports each removal as a refusal.
//
// Suppression already keeps the ordinary collision out, so this fires on the
// shapes suppression cannot see -- a collapsed subtree prefix that turns out to
// nest with a declared path, most of all. The verdict is trailer.Overlap's,
// which is the same one --moved and `safegit mv` are held to.
func dropOverlapping(pairs, declared []trailer.Pair) ([]trailer.Pair, []RefusedMove) {
	var kept []trailer.Pair
	var refused []RefusedMove
	for _, p := range pairs {
		conflict := false
		for _, other := range declared {
			if kind, _, _ := trailer.Overlap(p, other); kind != trailer.NoOverlap {
				conflict = true
				break
			}
		}
		if !conflict {
			for _, other := range kept {
				if kind, _, _ := trailer.Overlap(p, other); kind != trailer.NoOverlap {
					conflict = true
					break
				}
			}
		}
		if conflict {
			refused = append(refused, RefusedMove{Old: []string{p.Old}, New: []string{p.New}, Reason: refusedOverlaps})
			continue
		}
		kept = append(kept, p)
	}
	return kept, refused
}

// blobIndex is one tree seen whole: which blob sits at a path, and which paths
// hold a blob. Both questions are asked once per attempt, against the trees
// that attempt actually resolved.
//
// The blob halves are the UNIQUENESS FENCES' view of the tree, and a path the
// caller declared away is not in it -- see newBlobIndex.
type blobIndex struct {
	at     map[string]string   // path -> blob
	byBlob map[string][]string // blob -> paths
	// every path the tree holds, sorted, for a prefix query -- NOT only the
	// regular files. The uniqueness fences ask about blobs, which only a
	// regular file has, but the subtree witness (infer_subtrees.go) asks
	// whether a prefix is EMPTY of everything else, and a symlink or a
	// submodule pointer left behind under the old prefix is exactly the thing
	// that makes a subtree record false.
	sorted []string
}

// newBlobIndex lists one tree. An empty treeish is a root commit's absent
// parent: no paths, no blobs, and no git invocation.
//
// suppressed is the declared-paths predicate. It keeps a declared path out of
// the BLOB halves -- the fences' view -- and never out of the whole-path
// listing, which is the subtree witness's (infer_subtrees.go): a declaration
// settles what happened to the path it names, but a path is still a path the
// old prefix held, and pretending otherwise would let a subtree record claim a
// directory moved whole when part of it went somewhere the caller declared.
func newBlobIndex(ctx context.Context, treeish string, suppressed func(string) bool) (*blobIndex, error) {
	b := &blobIndex{at: map[string]string{}, byBlob: map[string][]string{}}
	if treeish == "" {
		return b, nil
	}
	entries, err := git.LsTreeRecursive(ctx, treeish)
	if err != nil {
		return nil, fmt.Errorf("listing %s to check what moved: %w", treeish, err)
	}
	for _, e := range entries {
		b.sorted = append(b.sorted, e.Path)
		// Only regular files can be one side of an inferred move, so only they
		// are counted when asking whether a blob is unique.
		if !regularFileModes[e.Mode] || suppressed(e.Path) {
			continue
		}
		b.at[e.Path] = e.SHA
		b.byBlob[e.SHA] = append(b.byBlob[e.SHA], e.Path)
	}
	sort.Strings(b.sorted)
	return b, nil
}

func (b *blobIndex) blobAt(path string) string { return b.at[path] }

func (b *blobIndex) pathsOf(blob string) []string {
	if blob == "" {
		return nil
	}
	return b.byBlob[blob]
}
