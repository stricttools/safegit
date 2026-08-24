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
