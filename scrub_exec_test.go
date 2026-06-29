package main

import (
	"testing"
)

func TestEstimateCommitCount_EntireHistory(t *testing.T) {
	dir, ctx := initTestRepo(t)

	writeFile(t, dir, "a.txt", "one")
	commitAll(t, dir, ctx, "first")

	writeFile(t, dir, "b.txt", "two")
	commitAll(t, dir, ctx, "second")

	writeFile(t, dir, "c.txt", "three")
	commitAll(t, dir, ctx, "third")

	got := estimateCommitCount(ctx, "", true)
	if got != 3 {
		t.Fatalf("estimateCommitCount(entireHistory=true) = %d, want 3", got)
	}
}

func TestEstimateCommitCount_Range(t *testing.T) {
	dir, ctx := initTestRepo(t)

	writeFile(t, dir, "a.txt", "one")
	commitAll(t, dir, ctx, "first")

	writeFile(t, dir, "b.txt", "two")
	secondSHA := commitAll(t, dir, ctx, "second")

	writeFile(t, dir, "c.txt", "three")
	commitAll(t, dir, ctx, "third")

	writeFile(t, dir, "d.txt", "four")
	commitAll(t, dir, ctx, "fourth")

	// Range from secondSHA..HEAD is third + fourth = 2 commits,
	// plus 1 for inclusive of secondSHA = 3.
	got := estimateCommitCount(ctx, secondSHA, false)
	if got != 3 {
		t.Fatalf("estimateCommitCount(fromSHA=%s, entireHistory=false) = %d, want 3", secondSHA[:8], got)
	}
}

func TestEstimateCommitCount_NoFromSHA_NotEntireHistory(t *testing.T) {
	_, ctx := initTestRepo(t)

	// Both false and empty fromSHA: should return 0.
	got := estimateCommitCount(ctx, "", false)
	if got != 0 {
		t.Fatalf("estimateCommitCount(fromSHA='', entireHistory=false) = %d, want 0", got)
	}
}
