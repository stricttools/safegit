package commit

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
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

// The overlap rule itself is trailer.Overlap's, and is pinned in that package.
// What is pinned HERE is this caller's use of it: that a --moved declaration is
// handed over in the form the shared check takes, and that each verdict comes
// back as the refusal a --moved caller reads.

func TestOverlapRefusesEveryCollidingDeclarationPair(t *testing.T) {
	cases := []struct {
		name string
		a, b movedDeclaration
		want string
	}{
		{
			name: "nesting sources",
			a:    movedDeclaration{arg: "src/ -> lib/", old: "src/", new: "lib/"},
			b:    movedDeclaration{arg: "src/a -> other", old: "src/a", new: "other"},
			want: "the same source",
		},
		{
			name: "nesting destinations",
			a:    movedDeclaration{arg: "a -> lib/x", old: "a", new: "lib/x"},
			b:    movedDeclaration{arg: "b -> lib/", old: "b", new: "lib/"},
			want: "the same destination",
		},
		{
			name: "a chain",
			a:    movedDeclaration{arg: "p.txt -> q.txt", old: "p.txt", new: "q.txt"},
			b:    movedDeclaration{arg: "q.txt -> r.txt", old: "q.txt", new: "r.txt"},
			want: "moves a path the other moves away from",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := refuseOverlappingDeclarations([]movedDeclaration{c.a, c.b})
			if err == nil {
				t.Fatalf("%s and %s were accepted", c.a.arg, c.b.arg)
			}
			var ce *CommitError
			if !errors.As(err, &ce) || ce.Code != exitcode.Usage {
				t.Fatalf("refusal is %v, want a Usage CommitError", err)
			}
			if !strings.Contains(ce.Message, c.want) {
				t.Errorf("refusal is %q, want it to name %q", ce.Message, c.want)
			}
		})
	}

	unrelated := []movedDeclaration{
		{arg: "a -> b", old: "a", new: "b"},
		{arg: "c -> d", old: "c", new: "d"},
	}
	if err := refuseOverlappingDeclarations(unrelated); err != nil {
		t.Errorf("unrelated declarations were refused: %v", err)
	}
}
