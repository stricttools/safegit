// Package gitversion parses the installed git's version and compares it
// against the per-feature floors safegit depends on.
//
// Every git capability safegit uses that is not ancient is declared here as a
// Feature with the git version that introduced it. A command that needs one
// calls Require, which produces an error naming both the feature and its
// floor, so an operator on an older git is told what to upgrade to and why --
// never handed a raw "unknown option" from git.
package gitversion

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a git version, compared major-then-minor-then-patch. Trailing
// vendor suffixes git appends on some platforms ("2.39.5 (Apple Git-154)",
// "2.42.0.windows.2") are not part of the ordering.
type Version struct {
	Major int
	Minor int
	Patch int
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare returns -1, 0 or 1 as v sorts before, equal to, or after o.
func (v Version) Compare(o Version) int {
	for _, pair := range [3][2]int{
		{v.Major, o.Major},
		{v.Minor, o.Minor},
		{v.Patch, o.Patch},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

// AtLeast reports whether v is o or newer.
func (v Version) AtLeast(o Version) bool { return v.Compare(o) >= 0 }

// Before reports whether v is older than o.
func (v Version) Before(o Version) bool { return v.Compare(o) < 0 }

// Parse reads a version out of `git --version` output ("git version 2.43.0")
// or a bare version string ("2.43.0", "2.39"). An absent patch component reads
// as 0. Anything after the numeric components is ignored, so vendor suffixes
// do not make a version unparseable.
func Parse(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	// Take the first line: `git --version` prints one, but a caller may hand
	// over a captured stdout with more.
	if i := strings.IndexAny(raw, "\r\n"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "git version "))
	if raw == "" {
		return Version{}, fmt.Errorf("cannot parse git version from %q", s)
	}

	// Cut at the first character that can start neither a number nor a
	// component separator, so "2.39.5 (Apple Git-154)" and "2.42.0.windows.2"
	// both reduce to their numeric head.
	fields := strings.Split(raw, ".")
	var nums []int
	for _, f := range fields {
		// Stop at the first non-numeric component ("windows" in
		// "2.42.0.windows.2"), and trim anything trailing on the last numeric
		// one ("5 (Apple" in "2.39.5 (Apple Git-154)").
		if i := strings.IndexFunc(f, func(r rune) bool { return r < '0' || r > '9' }); i >= 0 {
			f = f[:i]
		}
		if f == "" {
			break
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			break
		}
		nums = append(nums, n)
		if len(nums) == 3 {
			break
		}
	}
	if len(nums) == 0 {
		return Version{}, fmt.Errorf("cannot parse git version from %q", s)
	}

	v := Version{Major: nums[0]}
	if len(nums) > 1 {
		v.Minor = nums[1]
	}
	if len(nums) > 2 {
		v.Patch = nums[2]
	}
	return v, nil
}

// Feature is a git capability with the version that introduced it.
type Feature struct {
	// Name is what the operator sees: the git syntax safegit would run.
	Name string
	// Floor is the first git version that supports Name.
	Floor Version
	// Used names the safegit surface that needs it, for the refusal message.
	Used string
}

// The declared feature floors. Each is the version git's own release notes
// give for the capability.
var (
	// MergeTreeWriteTree is the real merge engine: computing a merge into an
	// object store without touching the index or the worktree.
	MergeTreeWriteTree = Feature{
		Name:  "git merge-tree --write-tree",
		Floor: Version{2, 38, 0},
		Used:  "safegit's own merge, cherry-pick and revert conclusion",
	}
	// AttrSource lets attribute lookups come from a tree instead of the
	// worktree, which is what makes a worktree-free merge honor .gitattributes.
	AttrSource = Feature{
		Name:  "git --attr-source",
		Floor: Version{2, 40, 0},
		Used:  "attribute-correct merges computed without a worktree",
	}
)

// Features returns every declared feature floor.
func Features() []Feature {
	return []Feature{MergeTreeWriteTree, AttrSource}
}

// HighestFloor returns the feature with the newest floor: the git version that
// makes every safegit feature available.
func HighestFloor() Feature {
	all := Features()
	highest := all[0]
	for _, f := range all[1:] {
		if highest.Floor.Before(f.Floor) {
			highest = f
		}
	}
	return highest
}

// Require returns nil when have satisfies f's floor, and an error naming the
// feature, the floor and the version actually found otherwise.
func Require(have Version, f Feature) error {
	if have.AtLeast(f.Floor) {
		return nil
	}
	return fmt.Errorf("%s requires git %s or newer (found git %s); it is what %s is built on", f.Name, f.Floor, have, f.Used)
}
