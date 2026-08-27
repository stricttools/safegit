package test

// Spec pin: committing a symlink whose target text will not resolve in another
// checkout refuses.
//
// The rule as shipped: git stores a symlink as its target TEXT and nothing
// else, so the only question is what a checkout somewhere else makes of that
// text. Two shapes fail it -- an ABSOLUTE target, whether or not it lands
// inside this checkout, and a RELATIVE target that resolves outside the
// repository -- and `safegit commit` REFUSES both (exit 29), naming the literal
// target, with nothing staged and nothing committed.
// `--allow-non-portable-targets` is the election that records such a link
// anyway, restoring the one-line stderr notice the refusal replaced.
//
// This file holds the coarse pin: the refusal happens and HEAD does not move.
// The fine-grained assertions -- the literal target in the message, every
// offender named, the grouping by shape with a remedy per group, the election,
// the --amend path, and the directory-expansion route -- are in
// commit_symlink_test.go.
//
// The portable control below is why the pin is scoped rather than blanket: a
// relative target that resolves inside the repository stays committable, and so
// does one that traverses out of its own directory with `..` and comes back
// down (commit_symlink_test.go pins that one).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// TestWave2CommitNonPortableSymlinkIsRefused: a symlink whose target leaves the
// repository must be refused, the refusal must name that target, and HEAD must
// not move.
func TestWave2CommitNonPortableSymlinkIsRefused(t *testing.T) {
	dir := newRepo(t)

	const target = "../elsewhere/secret.txt"
	if err := os.Symlink(target, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add a link that leaves", "--", "escapes")
	if code == 0 {
		t.Errorf("committing a symlink to %s exited 0; a target outside the repository must be refused\nstdout: %s\nstderr: %s",
			target, stdout, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the target %q; stderr:\n%s", target, stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "escapes"); ok {
		t.Error("the refused commit must not have recorded the link")
	}
}

// TestWave2CommitPortableSymlinkStillCommits is the control: the rule is about
// target texts that will not resolve elsewhere, so a relative symlink pointing
// at an in-repository path must still commit, as the link object itself.
func TestWave2CommitPortableSymlinkStillCommits(t *testing.T) {
	dir := newRepo(t)

	// seed.txt is created and committed by newRepo, so the target exists and
	// stays well inside the repository.
	if err := os.Symlink("seed.txt", filepath.Join(dir, "inside")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add inside link", "--", "inside")
	if code != 0 {
		t.Fatalf("committing an in-repository symlink failed (code %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if mode := treeEntryMode(t, dir, "inside"); mode != "120000" {
		t.Errorf("expected HEAD entry %q with mode 120000, got mode %q; tree:\n%s", "inside", mode, lsTreeHEAD(t, dir))
	}
	if got := catFileBlob(t, dir, "inside"); got != "seed.txt" {
		t.Errorf("symlink blob = %q, want the link text %q", got, "seed.txt")
	}
}
