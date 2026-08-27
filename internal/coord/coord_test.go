package coord

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
)

func TestCleanRepo(t *testing.T) {
	dir, gitDir, _ := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	ds, err := Check(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds != nil {
		t.Errorf("expected nil DirtyState on clean repo, got %+v", ds)
	}
}

func TestDirtyModified(t *testing.T) {
	dir, gitDir, _ := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Modify the tracked file
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("modified\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ds, err := Check(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds == nil {
		t.Fatal("expected non-nil DirtyState for modified file")
	}
	if len(ds.ModifiedFiles) == 0 {
		t.Fatal("expected at least one modified file")
	}

	// Verify seed.txt appears in the output
	found := false
	for _, f := range ds.ModifiedFiles {
		if strings.Contains(f, "seed.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("seed.txt not found in ModifiedFiles: %v", ds.ModifiedFiles)
	}
}

func TestDirtyUntracked(t *testing.T) {
	dir, gitDir, _ := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create an untracked file
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ds, err := Check(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if ds == nil {
		t.Fatal("expected non-nil DirtyState for untracked file")
	}

	found := false
	for _, f := range ds.ModifiedFiles {
		if strings.Contains(f, "scratch.txt") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("scratch.txt not found in ModifiedFiles: %v", ds.ModifiedFiles)
	}
}

// The two unborn arms. Before the empty-tree substitution the check itself
// failed here -- `git diff HEAD` is fatal in a repository with no commits -- so
// every guarded command refused with git's own "ambiguous argument 'HEAD'"
// before its handler decided anything. What the check owes an unborn branch is
// the same verdict it gives a born one: clean is clean, and dirt is listed.

func TestCleanUnbornRepo(t *testing.T) {
	dir, gitDir := testutil.InitUnbornRepo(t)
	testutil.Chdir(t, dir)

	ds, err := Check(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("Check on an unborn branch: %v", err)
	}
	if ds != nil {
		t.Errorf("expected nil DirtyState on a clean unborn repo, got %+v", ds)
	}
}

func TestDirtyUnbornRepo(t *testing.T) {
	dir, gitDir := testutil.InitUnbornRepo(t)
	testutil.Chdir(t, dir)

	// One staged path and one untracked path: on an unborn branch a staged
	// addition is the only kind of tracked change there is, and it must show up
	// exactly as it would against a born HEAD.
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("staged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, "add", "staged.txt")
	if err := os.WriteFile(filepath.Join(dir, "scratch.txt"), []byte("scratch\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ds, err := Check(context.Background(), gitDir)
	if err != nil {
		t.Fatalf("Check on an unborn branch: %v", err)
	}
	if ds == nil {
		t.Fatal("expected non-nil DirtyState on a dirty unborn repo")
	}
	joined := strings.Join(ds.ModifiedFiles, "\n")
	for _, want := range []string{"staged.txt", "scratch.txt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%s missing from ModifiedFiles: %v", want, ds.ModifiedFiles)
		}
	}
}

func TestRefuseMessage(t *testing.T) {
	ds := &DirtyState{
		ModifiedFiles: []string{" M src/foo.go", "?? scratch.txt"},
	}

	msg := ds.Refuse("switch")

	// Check key parts of the message
	checks := []string{
		"refusing switch",
		"Modified files:",
		" M src/foo.go",
		"?? scratch.txt",
		"Suggestion:",
		"safegit commit",
	}
	for _, want := range checks {
		if !strings.Contains(msg, want) {
			t.Errorf("Refuse message missing %q.\nGot:\n%s", want, msg)
		}
	}

	// --force is no longer an option; verify it's not mentioned
	if strings.Contains(msg, "--force") {
		t.Errorf("Refuse message should not mention --force.\nGot:\n%s", msg)
	}
}
