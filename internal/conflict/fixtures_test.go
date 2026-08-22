package conflict_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// The fixtures here build REAL conflicted states by running the commands that
// produce them. The package under test reproduces what git wrote, so a fixture
// that fabricated the state would be measuring the fixture.

// newRepo creates a repository holding one committed file, f.txt, on main.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := testutil.InitBareRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")
	return dir
}

// conflictedMerge leaves the repository stopped on a content conflict in f.txt,
// having merged the branch "feature" into main.
func conflictedMerge(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	testutil.Git(t, dir, "switch", "-q", "-c", "feature")
	testutil.WriteFile(t, dir, "f.txt", "l1\nTHEIRS\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "theirs")
	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "l1\nOURS\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "ours")
	mustConflict(t, dir, "merge", "feature")
	return dir
}

// mustConflict runs a git command the fixture needs to FAIL, and fails the test
// when it succeeds -- a fixture whose conflict silently merged cleanly would
// make every assertion after it meaningless.
func mustConflict(t *testing.T, dir string, args ...string) {
	t.Helper()
	out, code := testutil.GitTry(t, dir, args...)
	if code == 0 {
		t.Fatalf("git %s was expected to conflict but succeeded:\n%s", strings.Join(args, " "), out)
	}
}

// worktreeFile returns the verbatim content of a repo-relative file.
func worktreeFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}
