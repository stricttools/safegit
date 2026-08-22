package trailer

import (
	"fmt"
	"strings"
	"testing"
)

// rec builds a record with a readable id, so a failure names the record that
// misbehaved instead of 26 random characters. The ids only have to be distinct
// and valid for the projection, which never mints one.
func rec(t *testing.T, name, old, new string) Record {
	t.Helper()
	id := strings.ToUpper(name)
	for len(id) < idLength {
		id += "0"
	}
	id = id[:idLength]
	if !ValidID(id) {
		t.Fatalf("test id %q for %q is not a valid record id", id, name)
	}
	if err := ValidatePair(old, new); err != nil {
		t.Fatalf("test record %q: %v", name, err)
	}
	return Record{ID: id, Old: old, New: new}
}

// commit assembles one Commit from a single parent tree, its own tree, and what
// it declared.
func commit(id string, parent, own []string, records []Record, retractions ...string) Commit {
	return Commit{
		ID:      id,
		Moves:   Moves{Records: records, Retractions: retractions},
		Parents: []Tree{NewPathSet(parent...)},
		Tree:    NewPathSet(own...),
	}
}

func TestForwardFollowsAMultiHopChain(t *testing.T) {
	chain := []Commit{
		commit("c1", []string{"a.txt"}, []string{"b.txt"}, []Record{rec(t, "r1", "a.txt", "b.txt")}),
		commit("c2", []string{"b.txt"}, []string{"c.txt"}, []Record{rec(t, "r2", "b.txt", "c.txt")}),
		commit("c3", []string{"c.txt"}, []string{"d/e.txt"}, []Record{rec(t, "r3", "c.txt", "d/e.txt")}),
	}
	p := Forward("a.txt", chain)
	if p.Path != "d/e.txt" {
		t.Errorf("followed a.txt to %q, want d/e.txt", p.Path)
	}
	if !p.Present {
		t.Error("the end path is in the last tree but Present is false")
	}
	if len(p.Hops) != 3 {
		t.Fatalf("recorded %d hops, want 3: %+v", len(p.Hops), p.Hops)
	}
	if p.Hops[0].From != "a.txt" || p.Hops[0].To != "b.txt" || p.Hops[0].Commit != "c1" {
		t.Errorf("first hop = %+v", p.Hops[0])
	}
	if p.Hops[2].To != "d/e.txt" {
		t.Errorf("last hop = %+v", p.Hops[2])
	}
	// A path no record mentions is carried across unchanged.
	if q := Forward("untouched.txt", chain); q.Path != "untouched.txt" || len(q.Hops) != 0 {
		t.Errorf("an unmentioned path projected to %+v", q)
	}
}

func TestForwardIgnoresARecordTheParentTreeContradicts(t *testing.T) {
	// The record claims a move out of a path the parent never held. It is a
	// claim about nothing, so it never applies -- even though the commit's own
	// tree does hold the destination.
	garbage := commit("c1", []string{"real.txt"}, []string{"real.txt", "invented-dest.txt"},
		[]Record{rec(t, "r1", "never-existed.txt", "invented-dest.txt")})
	p := Forward("never-existed.txt", []Commit{garbage})
	if p.Path != "never-existed.txt" || len(p.Hops) != 0 {
		t.Errorf("a record about a path the parent never held was applied: %+v", p)
	}
	if p.Present {
		t.Error("Present is true for a path the last tree does not hold")
	}
}

func TestForwardIgnoresASubtreeRecordNamingANeverExistedPrefix(t *testing.T) {
	c := commit("c1", []string{"kept/x.txt"}, []string{"kept/x.txt", "dest/x.txt"},
		[]Record{rec(t, "r1", "invented/", "dest/")})
	p := Forward("invented/x.txt", []Commit{c})
	if len(p.Hops) != 0 {
		t.Errorf("a subtree record whose prefix names nothing in the parent was applied: %+v", p)
	}
}

func TestForwardIgnoresARecordWhoseAnswerIsNotInTheTree(t *testing.T) {
	// The old path was there and is gone, but the destination the record names
	// is nowhere in the commit -- the content went somewhere else, or nowhere.
	// The record is a claim the tree does not bear out.
	c := commit("c1", []string{"a.txt"}, []string{"elsewhere.txt"},
		[]Record{rec(t, "r1", "a.txt", "b.txt")})
	p := Forward("a.txt", []Commit{c})
	if p.Path != "a.txt" || len(p.Hops) != 0 {
		t.Errorf("a record whose destination is absent from the tree was applied: %+v", p)
	}
}

func TestForwardFoldsARetraction(t *testing.T) {
	moved := rec(t, "r1", "a.txt", "b.txt")
	chain := []Commit{
		commit("c1", []string{"a.txt"}, []string{"b.txt"}, []Record{moved}),
		commit("c2", []string{"b.txt"}, []string{"b.txt"}, nil, moved.ID),
	}
	p := Forward("a.txt", chain)
	if len(p.Hops) != 0 || p.Path != "a.txt" {
		t.Errorf("a retracted record was still applied: %+v", p)
	}
	// Without the retraction the same chain answers b.txt, which is what makes
	// the assertion above about the retraction and not about the fixture.
	without := []Commit{chain[0], commit("c2", []string{"b.txt"}, []string{"b.txt"}, nil)}
	if q := Forward("a.txt", without); q.Path != "b.txt" {
		t.Errorf("without the retraction the chain answers %q, want b.txt", q.Path)
	}
	// A retraction naming an id the chain does not carry changes nothing.
	stray := rec(t, "r9", "x", "y")
	inert := []Commit{chain[0], commit("c2", []string{"b.txt"}, []string{"b.txt"}, nil, stray.ID)}
	if q := Forward("a.txt", inert); q.Path != "b.txt" {
		t.Errorf("a retraction of an unrelated id changed the answer to %q", q.Path)
	}
}

func TestForwardReadsAReplacementInOneCommit(t *testing.T) {
	// The correction shape: a later commit retracts the wrong record and states
	// the right one in the same message.
	wrong := rec(t, "r1", "a.txt", "b.txt")
	right := rec(t, "r2", "a.txt", "c.txt")
	chain := []Commit{
		commit("c1", []string{"a.txt"}, []string{"c.txt"}, []Record{wrong}),
		{
			ID:      "c2",
			Moves:   Moves{Records: []Record{right}, Retractions: []string{wrong.ID}},
			Parents: []Tree{NewPathSet("c.txt")},
			Tree:    NewPathSet("c.txt"),
		},
	}
	// The replacement record is declared on c2 but describes c1's move, and c1
	// is where it has to apply -- which it does, because c1's parent held
	// a.txt and c1's own tree holds c.txt.
	p := Forward("a.txt", []Commit{
		{
			ID:      "c1",
			Moves:   Moves{Records: []Record{right}, Retractions: []string{wrong.ID}},
			Parents: chain[0].Parents,
			Tree:    chain[0].Tree,
		},
	})
	if p.Path != "c.txt" {
		t.Errorf("the replacement record answered %q, want c.txt", p.Path)
	}
	if q := Forward("a.txt", chain); q.Path != "a.txt" || len(q.Hops) != 0 {
		t.Errorf("the retracted record was still applied across the chain: %+v", q)
	}
}

func TestForwardLetsTheMergeTreeArbitrate(t *testing.T) {
	// Both sides of a merge moved one path, to two different names. Both
	// records are on the merge commit; both parents held the old path. Only the
	// name the merge kept can be the answer.
	ours := rec(t, "r1", "a.txt", "ours/a.txt")
	theirs := rec(t, "r2", "a.txt", "theirs/a.txt")
	merge := Commit{
		ID:      "m",
		Moves:   Moves{Records: []Record{ours, theirs}},
		Parents: []Tree{NewPathSet("a.txt"), NewPathSet("a.txt")},
		Tree:    NewPathSet("theirs/a.txt"),
	}
	p := Forward("a.txt", []Commit{merge})
	if p.Path != "theirs/a.txt" {
		t.Errorf("the merge answered %q; the merge tree holds theirs/a.txt", p.Path)
	}
	// Flip which name survived: the answer follows the tree, not the order the
	// records were written in.
	merge.Tree = NewPathSet("ours/a.txt")
	if q := Forward("a.txt", []Commit{merge}); q.Path != "ours/a.txt" {
		t.Errorf("with the other name in the merge tree the answer was %q", q.Path)
	}
	// A record made on the side that was merged IN still applies: its old path
	// is in the second parent, not the first.
	sideOnly := Commit{
		ID:      "m",
		Moves:   Moves{Records: []Record{rec(t, "r3", "side.txt", "side-moved.txt")}},
		Parents: []Tree{NewPathSet("main.txt"), NewPathSet("side.txt")},
		Tree:    NewPathSet("main.txt", "side-moved.txt"),
	}
	if q := Forward("side.txt", []Commit{sideOnly}); q.Path != "side-moved.txt" {
		t.Errorf("a record about the merged-in side answered %q", q.Path)
	}
}

func TestForwardDerivesSubtreeAnswersPerFile(t *testing.T) {
	c := commit("c1",
		[]string{"src/a.txt", "src/deep/b.txt", "src/gone.txt", "other.txt"},
		[]string{"lib/a.txt", "lib/deep/b.txt", "other.txt"},
		[]Record{rec(t, "r1", "src/", "lib/")})
	for from, want := range map[string]string{
		"src/a.txt":      "lib/a.txt",
		"src/deep/b.txt": "lib/deep/b.txt",
	} {
		if p := Forward(from, []Commit{c}); p.Path != want || !p.Present {
			t.Errorf("subtree record answered %q for %q (present %v), want %q", p.Path, from, p.Present, want)
		}
	}
	// A file under the prefix that was DELETED rather than moved gets no
	// answer: the per-file destination is not in the tree, so the claim is not
	// borne out for that file even though it is for its siblings.
	p := Forward("src/gone.txt", []Commit{c})
	if p.Path != "src/gone.txt" || len(p.Hops) != 0 || p.Present {
		t.Errorf("a deleted file under the moved prefix projected to %+v", p)
	}
	// A path outside the prefix is untouched.
	if q := Forward("other.txt", []Commit{c}); q.Path != "other.txt" {
		t.Errorf("a path outside the prefix moved to %q", q.Path)
	}
	// The prefix must match at a path boundary: src2/ is not under src/.
	boundary := commit("c1", []string{"src/a", "src2/a"}, []string{"lib/a", "src2/a"},
		[]Record{rec(t, "r1", "src/", "lib/")})
	if q := Forward("src2/a", []Commit{boundary}); q.Path != "src2/a" {
		t.Errorf("the prefix matched across a path boundary: %q", q.Path)
	}
}

func TestForwardPrefersTheLongestMatch(t *testing.T) {
	// A file record inside a subtree record wins for its own path, and the
	// subtree record still answers for everything else underneath.
	//
	// BOTH answers are in the tree -- lib/a.txt as well as special/a.txt -- so
	// nothing but the match length can decide between them. With only one of
	// them present the tree filter would pick the surviving answer whichever
	// order the candidates were tried in, and this would be a test of the tree
	// filter wearing the longest-match rule's name.
	c := commit("c1",
		[]string{"src/a.txt", "src/b.txt"},
		[]string{"lib/a.txt", "lib/b.txt", "special/a.txt"},
		[]Record{
			rec(t, "r1", "src/", "lib/"),
			rec(t, "r2", "src/a.txt", "special/a.txt"),
		})
	if p := Forward("src/a.txt", []Commit{c}); p.Path != "special/a.txt" {
		t.Errorf("the file record inside the subtree lost: answer was %q", p.Path)
	}
	if p := Forward("src/b.txt", []Commit{c}); p.Path != "lib/b.txt" {
		t.Errorf("the subtree record did not answer for its other file: %q", p.Path)
	}
	// Nested subtree records: the deeper prefix wins for what is under it.
	nested := commit("c1",
		[]string{"src/deep/x", "src/y"},
		[]string{"far/x", "lib/deep/x", "lib/y"},
		[]Record{
			rec(t, "r1", "src/", "lib/"),
			rec(t, "r2", "src/deep/", "far/"),
		})
	if p := Forward("src/deep/x", []Commit{nested}); p.Path != "far/x" {
		t.Errorf("the deeper prefix lost: answer was %q", p.Path)
	}
	if p := Forward("src/y", []Commit{nested}); p.Path != "lib/y" {
		t.Errorf("the outer prefix did not answer for its own file: %q", p.Path)
	}
}

func TestFileFormRecordNeverSpeaksForDescendants(t *testing.T) {
	// `a -> b` with no trailing slashes says a is now b, and says NOTHING
	// about anything under a -- even when the tree makes the parallel move
	// look obvious.
	c := commit("c1", []string{"a", "a/x"}, []string{"b", "b/x"},
		[]Record{rec(t, "r1", "a", "b")})
	if p := Forward("a", []Commit{c}); p.Path != "b" {
		t.Errorf("the file record did not answer for its own path: %q", p.Path)
	}
	p := Forward("a/x", []Commit{c})
	if p.Path != "a/x" || len(p.Hops) != 0 {
		t.Errorf("a file-form record spoke for a descendant: %+v", p)
	}
}

func TestForwardOnAnEmptyChainAnswersTheInput(t *testing.T) {
	p := Forward("a.txt", nil)
	if p.Path != "a.txt" || p.Present || len(p.Hops) != 0 {
		t.Errorf("an empty chain answered %+v", p)
	}
}

func TestRootCommitRecordsCannotApply(t *testing.T) {
	root := Commit{
		ID:      "r",
		Moves:   Moves{Records: []Record{rec(t, "r1", "a.txt", "b.txt")}},
		Parents: nil,
		Tree:    NewPathSet("b.txt"),
	}
	if p := Forward("a.txt", []Commit{root}); len(p.Hops) != 0 {
		t.Errorf("a root commit's record applied: %+v", p)
	}
}

func TestRetractedIDsCollectsTheWholeChain(t *testing.T) {
	a := rec(t, "r1", "a", "b")
	b := rec(t, "r2", "c", "d")
	chain := []Commit{
		commit("c1", nil, nil, nil, a.ID),
		commit("c2", nil, nil, nil, b.ID, a.ID),
	}
	got := RetractedIDs(chain)
	if len(got) != 2 || !got[a.ID] || !got[b.ID] {
		t.Errorf("RetractedIDs = %v", got)
	}
}

func TestPathSetHasUnderStopsAtPathBoundaries(t *testing.T) {
	s := NewPathSet("src/a", "src2/b", "top")
	cases := map[string]bool{"src": true, "src2": true, "top": false, "s": false, "": false}
	for prefix, want := range cases {
		if got := s.HasUnder(prefix); got != want {
			t.Errorf("HasUnder(%q) = %v, want %v", prefix, got, want)
		}
	}
	if !s.Has("top") || s.Has("nope") {
		t.Error(fmt.Sprint("Has answered wrongly for ", s))
	}
}
