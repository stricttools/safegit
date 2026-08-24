package commit

import (
	"sort"
	"strings"

	"github.com/smm-h/safegit/internal/trailer"
)

// Witnessed subtree collapse, and the cap on scattered inferred moves.
//
// A commit that moves a whole directory says ONE thing. Writing a record per
// file underneath the prefix would repeat that one statement once per file, and
// it would push an ordinary reorganization past the cap below for no reason.
// So where the delta FULLY WITNESSES a uniform prefix mapping, the per-file
// pairs collapse into the subtree form the record grammar already has -- the
// same form `--moved 'old/ -> new/'` writes.
//
// Both halves read only what the mint engine already listed (infer_moves.go):
// the per-attempt parent-tree and new-tree listings fence 4 built. Witnessing
// costs no further git invocation.

// moveInferenceCap is the number of inferred move records one commit may
// carry. Past it the commit records NONE of them and says so on stderr.
//
// A commit that moves twenty scattered files is not a commit whose moves
// safegit should be reconstructing from blob equality: at that scale a wrong
// pairing is both likelier and harder to notice, and the caller who really did
// perform that many moves can say so with --moved. A UNIFORM subtree move is
// not what this counts -- it collapses to ONE record first, so moving a
// thousand-file directory stays one record and stays well under the cap.
//
// The value is a judgement, held weakly, and it is a package constant rather
// than configuration on purpose: a knob would turn "how much is safegit allowed
// to infer" into a per-repository setting nobody reviews.
const moveInferenceCap = 20

// refusedOverCap is why a pair that cleared every fence still went unrecorded.
const refusedOverCap = "this commit carries more inferred moves than safegit records automatically"

// capRefusals turns a whole cleared pair set into refusals, which is what the
// cap does with one: past the cap a commit records none of them.
func capRefusals(pairs []trailer.Pair) []RefusedMove {
	refused := make([]RefusedMove, 0, len(pairs))
	for _, p := range pairs {
		refused = append(refused, RefusedMove{Old: []string{p.Old}, New: []string{p.New}, Reason: refusedOverCap})
	}
	return refused
}

// under returns every path in the tree beneath a directory prefix.
func (b *blobIndex) under(prefix string) []string {
	p := prefix + "/"
	i := sort.SearchStrings(b.sorted, p)
	var out []string
	for ; i < len(b.sorted) && strings.HasPrefix(b.sorted[i], p); i++ {
		out = append(out, b.sorted[i])
	}
	return out
}

// collapseSubtrees replaces a group of per-file pairs with ONE subtree record
// wherever the commit's delta FULLY WITNESSES a uniform prefix mapping.
//
// Fully witnessed means all three of these, and a group that misses any one of
// them stays as its per-file records:
//
//   - every pair in the group moves the same relative path under a prefix: the
//     old side is <old>/<rel> and the new side is <new>/<rel>, same <rel>;
//   - every path the PARENT tree held under <old> is one of those old sides.
//     A file left behind means the directory did not move, it was emptied out
//     in part, and a subtree record would claim more than happened;
//   - every path the NEW tree holds under <new> is one of those new sides.
//     Something else arriving in the destination means the destination is not
//     just the old directory under another name.
//
// Both listings are the ones fence 4 already built, so witnessing costs no
// further git invocation.
//
// The prefixes are tried SHALLOWEST FIRST, so a directory that moved whole
// collapses at its own root rather than at each of its subdirectories, and a
// group that collapses takes its pairs out of the pool before the next
// candidate is tried. A single pair never collapses: one file moving out of a
// one-file directory is a file move, and the file record says exactly that
// while the subtree record would say something broader on the same evidence.
func collapseSubtrees(pairs []trailer.Pair, parentTree, newTree *blobIndex) []trailer.Pair {
	if len(pairs) < 2 {
		return pairs
	}

	remaining := make(map[trailer.Pair]bool, len(pairs))
	for _, p := range pairs {
		remaining[p] = true
	}

	var collapsed []trailer.Pair
	for _, cand := range prefixCandidates(pairs) {
		group := coveredBy(cand, remaining)
		if len(group) < 2 {
			continue
		}
		if !witnessed(cand, group, parentTree, newTree) {
			continue
		}
		if err := trailer.ValidatePair(cand.old+"/", cand.new+"/"); err != nil {
			continue
		}
		for _, p := range group {
			delete(remaining, p)
		}
		collapsed = append(collapsed, trailer.Pair{Old: cand.old + "/", New: cand.new + "/"})
	}

	out := make([]trailer.Pair, 0, len(remaining)+len(collapsed))
	for _, p := range pairs {
		if remaining[p] {
			out = append(out, p)
		}
	}
	out = append(out, collapsed...)
	sort.Slice(out, func(i, j int) bool { return out[i].Old < out[j].Old })
	return out
}

// prefixMapping is one candidate "<old>/ -> <new>/" collapse.
type prefixMapping struct{ old, new string }

// prefixCandidates enumerates every prefix mapping the pairs suggest, shallowest
// first: for each pair, strip matching trailing components off both sides for as
// long as they match, and each strip is a candidate.
func prefixCandidates(pairs []trailer.Pair) []prefixMapping {
	seen := make(map[prefixMapping]bool)
	var out []prefixMapping
	for _, p := range pairs {
		oldParts := strings.Split(p.Old, "/")
		newParts := strings.Split(p.New, "/")
		for i, j := len(oldParts)-1, len(newParts)-1; i > 0 && j > 0; i, j = i-1, j-1 {
			if oldParts[i] != newParts[j] {
				break
			}
			cand := prefixMapping{
				old: strings.Join(oldParts[:i], "/"),
				new: strings.Join(newParts[:j], "/"),
			}
			if cand.old == cand.new || seen[cand] {
				continue
			}
			seen[cand] = true
			out = append(out, cand)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		di, dj := strings.Count(out[i].old, "/"), strings.Count(out[j].old, "/")
		if di != dj {
			return di < dj
		}
		if out[i].old != out[j].old {
			return out[i].old < out[j].old
		}
		return out[i].new < out[j].new
	})
	return out
}

// coveredBy returns the still-unconsumed pairs this prefix mapping accounts for:
// old under <old>/, new under <new>/, and the same relative path under each.
func coveredBy(cand prefixMapping, remaining map[trailer.Pair]bool) []trailer.Pair {
	var group []trailer.Pair
	for p := range remaining {
		oldRel, ok := relUnder(cand.old, p.Old)
		if !ok {
			continue
		}
		newRel, ok := relUnder(cand.new, p.New)
		if !ok || oldRel != newRel {
			continue
		}
		group = append(group, p)
	}
	sort.Slice(group, func(i, j int) bool { return group[i].Old < group[j].Old })
	return group
}

// relUnder returns a path's remainder below a directory prefix.
func relUnder(prefix, p string) (string, bool) {
	if !strings.HasPrefix(p, prefix+"/") {
		return "", false
	}
	return p[len(prefix)+1:], true
}

// witnessed is the "nothing left behind, nothing else arrived" half of the
// collapse predicate, asked of the two listings fence 4 already built.
func witnessed(cand prefixMapping, group []trailer.Pair, parentTree, newTree *blobIndex) bool {
	oldSide := make(map[string]bool, len(group))
	newSide := make(map[string]bool, len(group))
	for _, p := range group {
		oldSide[p.Old] = true
		newSide[p.New] = true
	}
	parentUnder := parentTree.under(cand.old)
	if len(parentUnder) != len(oldSide) {
		return false
	}
	for _, p := range parentUnder {
		if !oldSide[p] {
			return false
		}
	}
	newUnder := newTree.under(cand.new)
	if len(newUnder) != len(newSide) {
		return false
	}
	for _, p := range newUnder {
		if !newSide[p] {
			return false
		}
	}
	return true
}
