package trailer

import (
	"sort"
	"strings"
	"testing"
)

func TestNewIDShapeAndValidity(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if len(id) != idLength {
		t.Errorf("id %q is %d characters, want %d", id, len(id), idLength)
	}
	if !ValidID(id) {
		t.Errorf("NewID produced %q, which ValidID rejects", id)
	}
	for _, banned := range []string{"I", "L", "O", "U"} {
		if strings.Contains(id, banned) {
			t.Errorf("id %q holds %q, which Crockford base32 excludes", id, banned)
		}
	}
}

func TestValidIDRefusals(t *testing.T) {
	good, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	cases := []string{
		"",
		good[:idLength-1],
		good + "0",
		strings.ToLower(good),
		strings.Repeat("I", idLength),
		strings.Repeat("!", idLength),
	}
	for _, c := range cases {
		if ValidID(c) {
			t.Errorf("ValidID accepted %q", c)
		}
	}
}

func TestNewIDIsUnique(t *testing.T) {
	const n = 20000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q after %d ids", id, i)
		}
		seen[id] = true
	}
}

// TestIDsSortChronologically is what the timestamp prefix is for: a set of
// records read out of a history has a natural order without carrying a date.
func TestIDsSortChronologically(t *testing.T) {
	var entropy [idRandomBytes]byte
	// Deliberately identical entropy: what is being tested is that the
	// TIMESTAMP decides the order, not that random bytes happened to.
	var ids []string
	for _, ms := range []uint64{0, 1, 999, 1000, 1 << 20, 1 << 32, 1<<40 - 1, 1<<48 - 1} {
		ids = append(ids, renderID(ms, entropy))
	}
	if !sort.StringsAreSorted(ids) {
		t.Errorf("ids minted at increasing times do not sort in that order: %q", ids)
	}
	// Highest entropy at an earlier millisecond still sorts before the next
	// millisecond: the timestamp is the leading field, not a tiebreak.
	var maxEntropy [idRandomBytes]byte
	for i := range maxEntropy {
		maxEntropy[i] = 0xff
	}
	earlier := renderID(500, maxEntropy)
	later := renderID(501, entropy)
	if !(earlier < later) {
		t.Errorf("%q (t=500, max entropy) does not sort before %q (t=501, zero entropy)", earlier, later)
	}
}

// TestRenderIDEncodesTheWholeTimestamp pins the encoding itself: an all-zero
// input renders as all zeros, and the 48-bit maximum renders as the highest
// value ten Crockford characters can hold below the 50-bit boundary.
func TestRenderIDEncodesTheWholeTimestamp(t *testing.T) {
	var zero [idRandomBytes]byte
	if got := renderID(0, zero); got != strings.Repeat("0", idLength) {
		t.Errorf("renderID(0, zero) = %q", got)
	}
	var full [idRandomBytes]byte
	for i := range full {
		full[i] = 0xff
	}
	got := renderID(1<<48-1, full)
	if want := "7" + strings.Repeat("Z", idLength-1); got != want {
		t.Errorf("renderID(max, max) = %q, want %q", got, want)
	}
}
