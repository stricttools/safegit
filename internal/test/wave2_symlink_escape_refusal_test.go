package test

// Spec pin: committing a symlink whose target escapes the repository refuses.
//
// Finding (current behavior): `safegit commit` of a symlink whose target
// resolves outside the repository SUCCEEDS. intake.go's noticeEscapingLinks
// writes a single stderr line ("notice: <path> is a symlink to <target>, which
// is outside the repository; the commit records the link text, which will not
// resolve in another checkout") and the commit is written anyway, with the
// escaping link text recorded as a mode-120000 blob.
//
// Ruling (future behavior pinned here): the commit REFUSES, and the error names
// the escaping target. A dedicated flag will later elect committing such a link;
// that flag does not exist yet and is NOT pinned here -- only the refusal is.
//
// This pin deliberately contradicts the existing escaping-notice tests in
// internal/test/commit_symlink_test.go (notably
// TestCommitSymlinkEscapingTargetIsCommittedWithANotice). Those tests are
// sanctioned rewrites at implementation time: when the refusal is implemented,
// they are rewritten to assert the refusal rather than the notice.
//
// The non-escaping control below passes today and must keep passing: the ruling
// is scoped to targets that leave the repository, and an ordinary in-repository
// symlink stays committable.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// TestWave2CommitEscapingSymlinkIsRefused is the red pin: an escaping symlink
// must be refused, the refusal must name the target that leaves the repository,
// and HEAD must not move.
func TestWave2CommitEscapingSymlinkIsRefused(t *testing.T) {
	dir := newRepo(t)

	const target = "../elsewhere/secret.txt"
	if err := os.Symlink(target, filepath.Join(dir, "escapes")); err != nil {
		t.Fatalf("creating symlink: %v", err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "commit", "-m", "add escaping link", "--", "escapes")
	if code == 0 {
		t.Errorf("committing a symlink to %s exited 0; the escaping target must be refused\nstdout: %s\nstderr: %s",
			target, stdout, stderr)
	}
	if !strings.Contains(stderr, target) {
		t.Errorf("the refusal must name the escaping target %q; stderr:\n%s", target, stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", before, after)
	}
	if _, ok := testutil.Show(t, dir, "HEAD", "escapes"); ok {
		t.Error("the refused commit must not have recorded the escaping link")
	}
}

// TestWave2CommitNonEscapingSymlinkStillCommits is the control: the ruling is
// about targets that leave the repository, so a symlink pointing at an
// in-repository path must still commit, as the link object itself.
func TestWave2CommitNonEscapingSymlinkStillCommits(t *testing.T) {
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
