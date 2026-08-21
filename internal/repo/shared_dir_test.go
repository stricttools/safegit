package repo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/testutil"
)

// linkedWorktree adds a linked worktree to repoDir and returns the worktree's
// working directory and its git directory (the .git FILE in a linked worktree
// points at <main>/.git/worktrees/<name>, which is the directory safegit is
// handed when it runs there).
func linkedWorktree(t *testing.T, repoDir, name string) (wtDir, wtGitDir string) {
	t.Helper()
	// The worktree lives beside the repository, not inside it, so nothing in it
	// can be mistaken for content of the main working tree.
	wtDir = filepath.Join(filepath.Dir(repoDir), name)
	testutil.Git(t, repoDir, "worktree", "add", "-b", name, wtDir)

	data, err := os.ReadFile(filepath.Join(wtDir, ".git"))
	if err != nil {
		t.Fatalf("reading the linked worktree's .git file: %v", err)
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		t.Fatalf("linked worktree .git file is %q, want a %q line", line, prefix)
	}
	return wtDir, strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

// TestSharedSafegitDirLinkedWorktree pins the answer safegit's ref locking
// depends on: every worktree of one repository must resolve to ONE safegit
// directory under the common git dir, so a commit in the main worktree and a
// commit in a linked worktree contend for the same ref lock. A per-worktree
// answer would let both take "the" lock and race on the ref.
func TestSharedSafegitDirLinkedWorktree(t *testing.T) {
	repoDir, mainGitDir, _ := testutil.InitRepo(t, Init)
	_, wtGitDir := linkedWorktree(t, repoDir, "linked")

	want := filepath.Join(mainGitDir, "safegit")

	if got := SharedSafegitDir(context.Background(), wtGitDir); got != want {
		t.Errorf("SharedSafegitDir(linked worktree gitdir) = %q, want the main repo's %q", got, want)
	}
	// The main worktree must land on the same directory, and the safegit-dir
	// spelling of the argument (callers pass both forms) must too.
	if got := SharedSafegitDir(context.Background(), mainGitDir); got != want {
		t.Errorf("SharedSafegitDir(main gitdir) = %q, want %q", got, want)
	}
	if got := SharedSafegitDir(context.Background(), filepath.Join(wtGitDir, "safegit")); got != want {
		t.Errorf("SharedSafegitDir(linked worktree safegit dir) = %q, want %q", got, want)
	}
}

// TestSharedSafegitDirIsCwdIndependent: the answer for a linked worktree is the
// same from every working directory the operator might have invoked safegit
// from.
func TestSharedSafegitDirIsCwdIndependent(t *testing.T) {
	repoDir, mainGitDir, _ := testutil.InitRepo(t, Init)
	wtDir, wtGitDir := linkedWorktree(t, repoDir, "linked")

	want := filepath.Join(mainGitDir, "safegit")

	// Three unrelated working directories: a temp dir outside the repository,
	// the main working tree, and the linked working tree.
	for _, cwd := range []string{t.TempDir(), repoDir, wtDir} {
		testutil.Chdir(t, cwd)
		if got := SharedSafegitDir(context.Background(), wtGitDir); got != want {
			t.Errorf("from cwd %s: SharedSafegitDir = %q, want %q", cwd, got, want)
		}
	}
}

// TestSharedSafegitDirAnchorsRelativeAnswerOnTheGitDir covers the join that
// handles a RELATIVE --git-common-dir answer, which is the branch a process-cwd
// anchor (filepath.Abs) would get wrong.
//
// git spells the common dir relatively whenever it was told about it
// relatively, and it answers in the directory it is RUN in -- which for
// CommonGitDirOf is the git directory it was handed, never this process's
// working directory. The fixture forces that spelling with GIT_COMMON_DIR="."
// (a true statement about a non-worktree repository: its common dir IS its git
// dir) while the process sits somewhere else entirely, so the two anchors give
// different answers and the test can tell them apart.
func TestSharedSafegitDirAnchorsRelativeAnswerOnTheGitDir(t *testing.T) {
	_, gitDir, _ := testutil.InitRepo(t, Init)

	t.Setenv("GIT_COMMON_DIR", ".")
	testutil.Chdir(t, t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	commonDir, err := git.CommonGitDirOf(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("CommonGitDirOf: %v", err)
	}
	if filepath.IsAbs(commonDir) {
		t.Fatalf("the fixture no longer produces a relative --git-common-dir answer (got %q); "+
			"without one this test does not reach the join it exists to cover", commonDir)
	}

	want := filepath.Join(gitDir, "safegit")
	got := SharedSafegitDir(context.Background(), gitDir)
	if got != want {
		t.Errorf("SharedSafegitDir with a relative %q answer = %q, want %q", commonDir, got, want)
	}
	if strings.HasPrefix(got, cwd) {
		t.Errorf("SharedSafegitDir = %q, which is anchored on the process working directory %s; "+
			"a relative answer belongs to the git directory git was run in", got, cwd)
	}
}
