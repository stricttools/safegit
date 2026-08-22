package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// What a scan's file matches say about WHERE they were found.
//
// A scan reports two kinds of place, and they need two coordinate systems: a
// work-tree file is named the way git names it, relative to the repository
// root, while a file inside the git directory has no repo-relative name at all.
// Mixing them silently would make `hooks/pre-commit` ambiguous between a hook
// inside .git and a tracked file of that name, so a git-dir coordinate carries
// an explicit marker.

// scanFileMatch is one file match as the machine payload renders it.
type scanFileMatch struct {
	Path     string `json:"path"`
	InGitDir bool   `json:"in_git_dir"`
	Line     int    `json:"line"`
}

// scanFileMatches runs a machine-mode scan and returns its file matches.
func scanFileMatches(t *testing.T, dir, pattern string) []scanFileMatch {
	t.Helper()
	stdout, stderr, code := runSafegit(t, dir, "--json", "scan", "--pattern", pattern)
	if code != 0 {
		t.Fatalf("scan failed (%d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	var payload struct {
		FileMatches []scanFileMatch `json:"file_matches"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &payload); err != nil {
		t.Fatalf("parsing the scan payload: %v\nraw: %s", err, stdout)
	}
	return payload.FileMatches
}

// TestScanFromASubdirectorySeesTheWholeWorkTree: the working-tree listing runs
// under the pinned context, so where the operator happened to stand does not
// narrow what a scan covers. A scan that silently missed a root-level secret
// when run from a subdirectory would be worse than no scan at all.
func TestScanFromASubdirectorySeesTheWholeWorkTree(t *testing.T) {
	dir := newRepo(t)

	const secret = "SCANCOORD_ROOT_LEVEL_SECRET"
	testutil.WriteFile(t, dir, "root-secret.txt", "token = "+secret+"\n")
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, dir, filepath.Join("sub", "deeper", "keep.txt"), "nothing here\n")
	safegitCommit(t, dir, "seed the scan fixture", "root-secret.txt", filepath.Join("sub", "deeper", "keep.txt"))

	matches := scanFileMatches(t, filepath.Join(dir, "sub", "deeper"), secret)
	var found *scanFileMatch
	for i := range matches {
		if matches[i].Path == "root-secret.txt" {
			found = &matches[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("a scan from a subdirectory missed the root-level secret; matches: %+v", matches)
	}
	if found.InGitDir {
		t.Error("a work-tree file was reported with a git-dir coordinate")
	}
}

// TestScanCoordinatesDistinguishWorkTreeFromGitDir: the same secret in a
// tracked file and in a hook inside the git directory comes back in two
// coordinate systems, each marked.
func TestScanCoordinatesDistinguishWorkTreeFromGitDir(t *testing.T) {
	dir := newRepo(t)

	const secret = "SCANCOORD_TWO_PLACES_SECRET"
	testutil.WriteFile(t, dir, "tracked.txt", "token = "+secret+"\n")
	safegitCommit(t, dir, "seed a tracked secret", "tracked.txt")
	writeHookScript(t, filepath.Join(localHookDir(dir), "pre-pre-push.d", "10-leaky"), "TOKEN="+secret)

	var worktree, gitdir *scanFileMatch
	matches := scanFileMatches(t, dir, secret)
	for i := range matches {
		switch {
		case matches[i].Path == "tracked.txt":
			worktree = &matches[i]
		case strings.Contains(matches[i].Path, "10-leaky"):
			gitdir = &matches[i]
		}
	}

	if worktree == nil {
		t.Fatalf("the tracked file was not reported repo-relative; matches: %+v", matches)
	}
	if worktree.InGitDir {
		t.Error("the tracked file was marked as living in the git directory")
	}

	if gitdir == nil {
		t.Fatalf("the hook inside the git directory was not reported; matches: %+v", matches)
	}
	if !gitdir.InGitDir {
		t.Error("a file inside the git directory was not marked as such")
	}
	if want := "safegit/hooks/pre-pre-push.d/10-leaky"; gitdir.Path != want {
		t.Errorf("git-dir coordinate = %q, want %q (relative to the git directory)", gitdir.Path, want)
	}
}

// TestScanSeesUncommittedCommittedStoreHooks: a hook written into the committed
// store runs on the next push whether or not it has been committed yet, so the
// sweep reads the store itself rather than relying on git's file listing.
func TestScanSeesUncommittedCommittedStoreHooks(t *testing.T) {
	dir := newRepo(t)

	const secret = "SCANCOORD_UNCOMMITTED_HOOK_SECRET"
	writeHookScript(t, filepath.Join(trackedHookDir(dir), "pre-pre-push"), "TOKEN="+secret)

	matches := scanFileMatches(t, dir, secret)
	var found *scanFileMatch
	for i := range matches {
		if strings.Contains(matches[i].Path, "pre-pre-push") {
			found = &matches[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("scan missed a secret in the committed hook store; matches: %+v", matches)
	}
	if found.InGitDir {
		t.Error("a work-tree hook was marked as living in the git directory")
	}
	if want := ".safegit/hooks/pre-pre-push"; found.Path != want {
		t.Errorf("work-tree coordinate = %q, want %q (relative to the repository root)", found.Path, want)
	}
}

// TestScanSkipsTheRewriteJournal: the journal holds object names and nothing
// else -- a scrub's replacement never reaches it -- and it grows without bound
// in a repository that rewrites often, so the sweep leaves it alone. The
// neighbouring state files are still read, which is what makes this a skip
// rather than an accident.
func TestScanSkipsTheRewriteJournal(t *testing.T) {
	dir := newRepo(t)

	// Auto-initialize .git/safegit before planting files inside it.
	runSafegit(t, dir, "config", "show")

	const secret = "SCANCOORD_JOURNAL_SECRET"
	sgDir := filepath.Join(dir, ".git", "safegit")
	if err := os.WriteFile(filepath.Join(sgDir, "rewrite-maps.jsonl"),
		[]byte(`{"note":"`+secret+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sgDir, "some-state.jsonl"),
		[]byte(`{"note":"`+secret+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches := scanFileMatches(t, dir, secret)
	for _, m := range matches {
		if strings.Contains(m.Path, "rewrite-maps.jsonl") {
			t.Errorf("the sweep read the rewrite journal: %+v", m)
		}
	}
	found := false
	for _, m := range matches {
		if strings.Contains(m.Path, "some-state.jsonl") {
			found = true
			if !m.InGitDir {
				t.Error("a safegit state file was not marked as living in the git directory")
			}
		}
	}
	if !found {
		t.Errorf("the sweep missed a safegit state file next to the journal; matches: %+v", matches)
	}
}
