package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A scrub asks which submodules the repository has for one reason: they are
// part of what it rewrites. `scrub file` routes the whole operation into a
// submodule when the target path lives in one, and `scrub match` scans and
// rewrites every initialized submodule alongside the parent.
//
// The enumeration failing therefore narrows the SCOPE of the rewrite, and it
// used to do so silently: scrub.go dropped the error entirely and continued
// with no submodules at all, so a `scrub file` aimed at a path inside a
// submodule rewrote the parent's history as if that path were an ordinary file;
// the two scrub_match seams printed a warning and then did the same, and a
// warning that a rewrite has skipped the place a secret actually lives is not a
// cure. All three are hard errors now: the operator re-runs the scrub once the
// repository can be read, rather than being told afterwards that the secret is
// still in a submodule nobody looked at.
//
// The fixture makes the enumeration fail without any submodule being present:
// a regular file sits where .git/modules would be, so the deinitialized-half of
// the walk finds something it cannot read. That is a repository state safegit
// cannot interpret, which is exactly the class the refusal is for.
func unenumerableSubmodulesRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFileAt(t, filepath.Join(dir, "secret.env"), "TOKEN=abc123\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add the secret", "--", "secret.env"); code != 0 {
		t.Fatalf("seed commit failed (%d): %s", code, stderr)
	}

	testutil.WriteFileAt(t, filepath.Join(dir, ".git", "modules"), "not a directory\n")
	return dir
}

// assertEnumerationRefusal holds the whole refusal: a nonzero exit, a message
// that names what could not be read, and a history that did not move.
func assertEnumerationRefusal(t *testing.T, dir string, args ...string) {
	t.Helper()
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, args...)
	if code == 0 {
		t.Fatalf("safegit %s succeeded with submodules unenumerable\nstdout=%s\nstderr=%s",
			strings.Join(args, " "), stdout, stderr)
	}
	if !strings.Contains(stderr, "enumerating submodules") {
		t.Errorf("the refusal does not say what could not be read; stderr: %s", stderr)
	}
	if strings.Contains(stderr, "warning:") {
		t.Errorf("the failure is still reported as a warning; stderr: %s", stderr)
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("history moved despite the refusal: %s -> %s", before, after)
	}
}

// TestScrubFileRefusesWhenSubmodulesCannotBeEnumerated pins scrub.go's seam,
// the one that reported nothing at all.
func TestScrubFileRefusesWhenSubmodulesCannotBeEnumerated(t *testing.T) {
	dir := unenumerableSubmodulesRepo(t)

	assertEnumerationRefusal(t, dir, "scrub", "file", "--approve-consequential",
		"--delete", "--entire-history", "--reason", "the token leaked", "secret.env")
}

// TestScrubMatchPreviewRefusesWhenSubmodulesCannotBeEnumerated pins the dry-run
// seam. A preview whose scope is wrong is worse than no preview: it is the
// document the operator consents from.
func TestScrubMatchPreviewRefusesWhenSubmodulesCannotBeEnumerated(t *testing.T) {
	dir := unenumerableSubmodulesRepo(t)

	assertEnumerationRefusal(t, dir, "scrub", "match", "--dry-run",
		"--pattern", "abc123", "--replace", "REDACTED", "--entire-history",
		"--reason", "the token leaked")
}

// TestScrubMatchRefusesWhenSubmodulesCannotBeEnumerated pins the execute seam.
func TestScrubMatchRefusesWhenSubmodulesCannotBeEnumerated(t *testing.T) {
	dir := unenumerableSubmodulesRepo(t)

	assertEnumerationRefusal(t, dir, "scrub", "match", "--approve-consequential",
		"--pattern", "abc123", "--replace", "REDACTED", "--entire-history",
		"--reason", "the token leaked")
}
