package gitversion

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Version
	}{
		{"git version 2.43.0", Version{2, 43, 0}},
		{"git version 2.43.0\n", Version{2, 43, 0}},
		{"2.43.0", Version{2, 43, 0}},
		{"2.39", Version{2, 39, 0}},
		{"3", Version{3, 0, 0}},
		// Vendor suffixes must not make a version unparseable.
		{"git version 2.39.5 (Apple Git-154)", Version{2, 39, 5}},
		{"git version 2.42.0.windows.2", Version{2, 42, 0}},
		{"git version 2.45.2.rc1", Version{2, 45, 2}},
		// A fourth component is not part of the ordering.
		{"2.40.1.100", Version{2, 40, 1}},
	}
	for _, tc := range cases {
		got, err := Parse(tc.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseRejectsNonVersions(t *testing.T) {
	for _, in := range []string{"", "   ", "git version ", "git version unknown", "not a version"} {
		if v, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", in, v)
		}
	}
}

func TestCompareOrdersComponentsMajorFirst(t *testing.T) {
	cases := []struct {
		a, b Version
		want int
	}{
		{Version{2, 38, 0}, Version{2, 38, 0}, 0},
		{Version{2, 37, 9}, Version{2, 38, 0}, -1},
		{Version{2, 38, 1}, Version{2, 38, 0}, 1},
		{Version{1, 99, 99}, Version{2, 0, 0}, -1},
		{Version{2, 40, 0}, Version{2, 4, 0}, 1}, // not string ordering
	}
	for _, tc := range cases {
		if got := tc.a.Compare(tc.b); got != tc.want {
			t.Errorf("%v.Compare(%v) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := tc.a.AtLeast(tc.b); got != (tc.want >= 0) {
			t.Errorf("%v.AtLeast(%v) = %v", tc.a, tc.b, got)
		}
		if got := tc.a.Before(tc.b); got != (tc.want < 0) {
			t.Errorf("%v.Before(%v) = %v", tc.a, tc.b, got)
		}
	}
}

func TestRequireNamesTheFeatureAndItsFloor(t *testing.T) {
	old := Version{2, 30, 0}
	err := Require(old, MergeTreeWriteTree)
	if err == nil {
		t.Fatalf("git %v must not satisfy the %s floor", old, MergeTreeWriteTree.Name)
	}
	for _, want := range []string{MergeTreeWriteTree.Name, MergeTreeWriteTree.Floor.String(), old.String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should mention %q, got: %v", want, err)
		}
	}

	if err := Require(MergeTreeWriteTree.Floor, MergeTreeWriteTree); err != nil {
		t.Errorf("the floor version itself must satisfy the floor: %v", err)
	}
	if err := Require(Version{2, 99, 0}, AttrSource); err != nil {
		t.Errorf("a newer git must satisfy every floor: %v", err)
	}
}

func TestHighestFloorIsTheNewestDeclaredFloor(t *testing.T) {
	highest := HighestFloor()
	for _, f := range Features() {
		if highest.Floor.Before(f.Floor) {
			t.Errorf("HighestFloor() = %s (%s), but %s declares a newer floor %s",
				highest.Name, highest.Floor, f.Name, f.Floor)
		}
	}
	// A git at the highest floor satisfies every declared feature.
	for _, f := range Features() {
		if err := Require(highest.Floor, f); err != nil {
			t.Errorf("git %s should satisfy every floor: %v", highest.Floor, err)
		}
	}
}

func TestEveryFeatureIsFullyDeclared(t *testing.T) {
	for _, f := range Features() {
		if f.Name == "" || f.Used == "" {
			t.Errorf("feature %+v is missing its name or its use", f)
		}
		if f.Floor == (Version{}) {
			t.Errorf("feature %q declares no floor", f.Name)
		}
	}
}
