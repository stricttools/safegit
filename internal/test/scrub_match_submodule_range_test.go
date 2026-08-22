package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `scrub match --from <sha>` is a BOUNDED request, and a submodule is a
// repository of its own: the boundary the operator named is a commit of the
// PARENT's history and means nothing inside the submodule. The mapping that
// gives it meaning there is the gitlink -- the submodule commit the boundary
// commit records -- and it is the same mechanism `scrub file` uses.
//
// The submodule walk used to ignore the boundary entirely and rewrite each
// submodule's whole history, so a bounded request silently became an unbounded
// rewrite of another repository. These tests pin the three outcomes: a boundary
// that maps and covers everything the pattern touches succeeds, a boundary that
// maps but leaves the pattern behind is REFUSED (the same verdict the parent
// gives for the same situation) rather than quietly widened, and a boundary
// that cannot be mapped at all is a hard error that rewrites nothing.

var submoduleRangeEnv = []string{"CLAUDE_CODE_SESSION_ID=submodule-range-test"}

// submoduleRangeFixture builds a parent and a submodule whose histories
// interleave, so a parent commit can be named as a range boundary and mapped
// into the submodule through its gitlink.
//
// The submodule holds three commits. firstSubSecret decides whether the FIRST
// one -- the only one that ends up outside the range -- carries the pattern:
// that is the difference between a bounded scrub that covers everything it
// claims to and one that does not. The parent records the submodule's second
// commit at parentBoundary, and its third at HEAD.
func submoduleRangeFixture(t *testing.T, firstSubSecret string) (parentDir, subDir, subFirst, parentInitial, parentBoundary string) {
	t.Helper()
	parentDir, _, subDir = newRepoWithSubmoduleSecret(t, firstSubSecret, "secret.txt")
	subFirst = testutil.Rev(t, subDir, "HEAD")
	parentInitial = revListReverse(t, parentDir)[0]

	commitSub := func(content, message string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(subDir, "secret.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitIn(t, subDir, "add", "secret.txt")
		runGitIn(t, subDir, "commit", "-m", message)
	}

	commitSub("SENSITIVE_DATA_HERE second\n", "sub second")
	runGitIn(t, parentDir, "add", "mysub")
	runGitIn(t, parentDir, "commit", "-m", "point at sub second")
	parentBoundary = testutil.Rev(t, parentDir, "HEAD")

	commitSub("SENSITIVE_DATA_HERE third\n", "sub third")
	runGitIn(t, parentDir, "add", "mysub")
	runGitIn(t, parentDir, "commit", "-m", "point at sub third")

	return parentDir, subDir, subFirst, parentInitial, parentBoundary
}

// subRootCommit returns the root commit of the submodule's current history. It
// is the sharpest single statement of whether the whole history was rewritten:
// a rewrite that reached the root gives the root a new SHA.
func subRootCommit(t *testing.T, subDir string) string {
	t.Helper()
	out := testutil.GitOut(t, subDir, "rev-list", "--max-parents=0", "HEAD")
	return strings.TrimSpace(out)
}

func scrubMatchFrom(t *testing.T, parentDir, from string) (stdout, stderr string, code int) {
	t.Helper()
	return runSafegitEnv(t, parentDir, submoduleRangeEnv,
		"--approve-consequential", "scrub", "match",
		"--pattern", "SENSITIVE_DATA_HERE",
		"--replace", "REDACTED",
		"--reason", "submodule range test",
		"--from", from,
	)
}

// TestScrubMatchBoundsTheSubmoduleWalkToTheGitlink is the ordinary bounded
// case: everything the pattern touches is inside the range, so the scrub
// succeeds -- and the submodule commit OUTSIDE the range comes through with the
// SHA it had, which is what "bounded" means for the repository that never named
// the boundary.
func TestScrubMatchBoundsTheSubmoduleWalkToTheGitlink(t *testing.T) {
	parentDir, subDir, subFirst, _, boundary := submoduleRangeFixture(t, "HARMLESS_FIRST")

	_, stderr, code := scrubMatchFrom(t, parentDir, boundary)
	if code != 0 {
		t.Fatalf("a bounded scrub whose range covers every match must succeed, got %d: %s", code, stderr)
	}

	if got := subRootCommit(t, subDir); got != subFirst {
		t.Errorf("the submodule's out-of-range root commit was rewritten: it is %s, want %s", got, subFirst)
	}
	content, ok := testutil.Show(t, subDir, subFirst, "secret.txt")
	if !ok {
		t.Fatalf("the out-of-range submodule commit %s no longer carries secret.txt", subFirst)
	}
	if !strings.Contains(content, "HARMLESS_FIRST") {
		t.Errorf("the out-of-range submodule commit's content changed: %q", content)
	}
	head, ok := testutil.Show(t, subDir, "HEAD", "secret.txt")
	if !ok || !strings.Contains(head, "REDACTED") {
		t.Errorf("the in-range submodule commits were not scrubbed; HEAD holds %q", head)
	}
}

// TestScrubMatchRefusesRatherThanWidenTheSubmoduleRange is the escalation
// itself. The pattern is in the submodule commit BEFORE the boundary, so a
// bounded scrub cannot remove it -- and the answer to that is the refusal the
// parent already gives for the same situation, never a quiet rewrite of the
// submodule's whole history.
func TestScrubMatchRefusesRatherThanWidenTheSubmoduleRange(t *testing.T) {
	parentDir, subDir, subFirst, _, boundary := submoduleRangeFixture(t, "SENSITIVE_DATA_HERE first")

	parentHead := testutil.Rev(t, parentDir, "HEAD")
	subHead := testutil.Rev(t, subDir, "HEAD")

	_, stderr, code := scrubMatchFrom(t, parentDir, boundary)
	if code == 0 {
		t.Fatalf("a bounded scrub that leaves the pattern in an out-of-range submodule commit must not report success: %s", stderr)
	}
	if code != exitcode.RewriteRefused {
		t.Errorf("the refusal must exit %d (RewriteRefused), got %d; stderr: %s",
			exitcode.RewriteRefused, code, stderr)
	}
	if got := testutil.Rev(t, subDir, "HEAD"); got != subHead {
		t.Errorf("the submodule was rewritten anyway: HEAD is %s, want %s", got, subHead)
	}
	if got := subRootCommit(t, subDir); got != subFirst {
		t.Errorf("the submodule's whole history was rewritten: root is %s, want %s", got, subFirst)
	}
	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHead {
		t.Errorf("the parent moved even though the operation was refused: HEAD is %s, want %s", got, parentHead)
	}
}

// TestScrubMatchRefusesAnUnmappableSubmoduleBoundary covers the boundary that
// has no meaning in the submodule at all: the parent commit named by --from
// predates the submodule, so it records no gitlink to map. Widening to the
// submodule's whole history is exactly the escalation that is banned, so this
// is a hard error -- and it happens before anything is rewritten anywhere.
func TestScrubMatchRefusesAnUnmappableSubmoduleBoundary(t *testing.T) {
	parentDir, subDir, subFirst, parentInitial, _ := submoduleRangeFixture(t, "SENSITIVE_DATA_HERE first")

	parentHead := testutil.Rev(t, parentDir, "HEAD")
	subHead := testutil.Rev(t, subDir, "HEAD")

	_, stderr, code := scrubMatchFrom(t, parentDir, parentInitial)
	if code == 0 {
		t.Fatalf("a --from the submodule cannot be mapped to must not silently rewrite its whole history: %s", stderr)
	}
	if !strings.Contains(stderr, "mysub") {
		t.Errorf("the refusal must name the submodule it could not map the boundary into; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "--entire-history") {
		t.Errorf("the refusal must name the fix; stderr: %s", stderr)
	}
	if got := testutil.Rev(t, subDir, "HEAD"); got != subHead {
		t.Errorf("the submodule was rewritten despite the refusal: HEAD is %s, want %s", got, subHead)
	}
	if got := subRootCommit(t, subDir); got != subFirst {
		t.Errorf("the submodule's whole history was rewritten despite the refusal: root is %s, want %s", got, subFirst)
	}
	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHead {
		t.Errorf("the parent moved despite the refusal: HEAD is %s, want %s", got, parentHead)
	}
}
