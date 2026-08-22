package commit

import (
	"fmt"
	"testing"
)

// TestCommitTrailersOrderingAndDedup pins the order a commit's trailers go on
// in, which is what decides what the repository's commit-msg hook sees: the
// caller's own --trailer values, then the records being carried forward from a
// message this operation is replacing, then the records it is declaring now.
// safegit's session trailer is not here at all -- it goes on after the hook.
func TestCommitTrailersOrderingAndDedup(t *testing.T) {
	user := []string{"Reviewed-by: Alice"}
	preserved := []string{"Moved: OLD a -> b", "Moved-Retract: GONE"}
	declared := []string{"Moved: NEW c -> d"}

	got := commitTrailers(user, preserved, declared)
	want := []string{"Reviewed-by: Alice", "Moved: OLD a -> b", "Moved-Retract: GONE", "Moved: NEW c -> d"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("commitTrailers = %q, want %q", got, want)
	}

	// A record being carried forward that this operation also declares is
	// carried once: two identical records would be two claims where the caller
	// made one.
	got = commitTrailers(nil, []string{"Moved: NEW c -> d"}, []string{"Moved: NEW c -> d"})
	if fmt.Sprint(got) != fmt.Sprint([]string{"Moved: NEW c -> d"}) {
		t.Errorf("a re-declared record was doubled: %q", got)
	}

	if got := commitTrailers(nil, nil, nil); len(got) != 0 {
		t.Errorf("commitTrailers of nothing = %q", got)
	}
}

func TestPreservedMovedLinesOnlyOnAReplacedMessage(t *testing.T) {
	msg := "subject\n\nMoved: 01ARZ3NDEKTSV4RRFFQ69G5FAV a.txt -> b.txt\n"
	if got := preservedMovedLines(msg, false); len(got) != 0 {
		t.Errorf("a message being KEPT still carried its records forward: %q", got)
	}
	got := preservedMovedLines(msg, true)
	if len(got) != 1 || got[0] != "Moved: 01ARZ3NDEKTSV4RRFFQ69G5FAV a.txt -> b.txt" {
		t.Errorf("a replaced message did not carry its record forward: %q", got)
	}
}

func TestMovedParentRevIsTheFirstParent(t *testing.T) {
	if got := movedParentRev(nil); got != "" {
		t.Errorf("a root commit's move base is %q, want the empty string", got)
	}
	if got := movedParentRev([]string{"aaa", "bbb"}); got != "aaa" {
		t.Errorf("a merge's move base is %q, want its first parent", got)
	}
}

// The path-nesting rule itself is trailer.Nests's, and is pinned in that
// package (TestNests). What is pinned HERE is the use of it: which SIDE of two
// declarations the overlap refusal names.

func TestOverlapNamesTheSideThatNests(t *testing.T) {
	source := overlapSide(movedDeclaration{old: "src/", new: "lib/"}, movedDeclaration{old: "src/a", new: "other"})
	if source != "the same source" {
		t.Errorf("nesting sources reported %q", source)
	}
	dest := overlapSide(movedDeclaration{old: "a", new: "lib/x"}, movedDeclaration{old: "b", new: "lib/"})
	if dest != "the same destination" {
		t.Errorf("nesting destinations reported %q", dest)
	}
	none := overlapSide(movedDeclaration{old: "a", new: "b"}, movedDeclaration{old: "c", new: "d"})
	if none != "" {
		t.Errorf("unrelated declarations reported an overlap: %q", none)
	}
}

func overlapSide(a, b movedDeclaration) string {
	side, _, _ := overlap(a, b)
	return side
}
