package main

import (
	"errors"
	"testing"
)

// The strategy-option floor is asked CONDITIONALLY.
//
// `git merge-tree` learned `-X` in git 2.43, which is newer than the 2.38 floor
// `--write-tree` itself carries, so a preview of a command line that forwards
// strategy options needs the newer git and one that does not must never be
// refused for wanting it. The conditional is what these pins hold: an
// optionless command line does not so much as ASK the floor question.
//
// The check is passed in rather than reached for, because on a git that
// satisfies the floor a real check can only ever answer yes -- and then the
// conditional itself would be untestable, since both branches would look the
// same from the outside.

// countingFloor records whether it was consulted and answers with err.
func countingFloor(err error) (func() error, *int) {
	calls := 0
	return func() error {
		calls++
		return err
	}, &calls
}

func TestTheStrategyOptionFloorIsNotAskedWithoutAStrategyOption(t *testing.T) {
	refused := errors.New("git merge-tree -X requires a newer git")

	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"a bare merge", []string{"feature"}},
		{"a merge whose options change no tree", []string{"--no-ff", "--signoff", "feature"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check, calls := countingFloor(refused)
			if reason := previewStrategyOptionFloor(parseGitArgs("merge", tc.argv), check); reason != "" {
				t.Errorf("an optionless preview was refused for the floor: %s", reason)
			}
			if *calls != 0 {
				t.Errorf("the floor question was asked %d time(s) for a command line carrying no strategy option", *calls)
			}
		})
	}
}

func TestTheStrategyOptionFloorIsAskedForEverySpelling(t *testing.T) {
	refused := errors.New("git merge-tree -X requires a newer git")

	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"the short spelling, detached", []string{"-X", "ours", "feature"}},
		{"the short spelling, attached", []string{"-Xours", "feature"}},
		{"the long spelling", []string{"--strategy-option=ours", "feature"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check, calls := countingFloor(refused)
			reason := previewStrategyOptionFloor(parseGitArgs("merge", tc.argv), check)
			if reason == "" {
				t.Errorf("a preview carrying a strategy option was not refused on a git below the floor")
			}
			if *calls != 1 {
				t.Errorf("the floor question was asked %d time(s), want 1", *calls)
			}
		})
	}
}

// And on a git that satisfies it, the very same command line previews.
func TestAStrategyOptionPreviewsOnAGitThatSatisfiesTheFloor(t *testing.T) {
	check, calls := countingFloor(nil)
	if reason := previewStrategyOptionFloor(parseGitArgs("merge", []string{"-X", "ours", "feature"}), check); reason != "" {
		t.Errorf("the preview was refused on a git that satisfies the floor: %s", reason)
	}
	if *calls != 1 {
		t.Errorf("the floor question was asked %d time(s), want 1", *calls)
	}
}

// TestEveryStrategyOptionIsCollected: the argv reader's Find returns the FIRST
// occurrence, and a preview that forwarded only that one would silently drop
// the second half of `-X ours -X ignore-space-change`.
func TestEveryStrategyOptionIsCollected(t *testing.T) {
	parsed := parseGitArgs("merge", []string{"-X", "ours", "--strategy-option=ignore-space-change", "-Xdiff-algorithm=patience", "feature"})
	got := strategyOptionArgv(parsed)
	want := []string{"-X", "ours", "--strategy-option", "ignore-space-change", "-X", "diff-algorithm=patience"}
	if len(got) != len(want) {
		t.Fatalf("collected %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("collected %v, want %v", got, want)
		}
	}
}
