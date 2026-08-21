package commit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
)

// newPipeline creates a Pipeline with default config for the given safegitDir.
func newPipeline(sgDir string) *Pipeline {
	cfg := repo.DefaultConfig()
	return &Pipeline{SafegitDir: sgDir, Config: cfg}
}

// commitLandsOnBranch verifies that ref points to sha.
func commitLandsOnBranch(t *testing.T, ref, sha string) {
	t.Helper()
	got, err := git.RevParse(context.Background(), ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	if got != sha {
		t.Errorf("ref %s = %s, want %s", ref, got, sha)
	}
}

// treeHasFile checks that a commit's tree contains the given file path.
func treeHasFile(t *testing.T, commitSHA, path string) {
	t.Helper()
	ctx := context.Background()
	out, _, err := git.Run(ctx, "ls-tree", "-r", "--name-only", commitSHA)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == path {
			return
		}
	}
	t.Errorf("file %q not found in tree of %s; tree contents:\n%s", path, commitSHA, out)
}

// treeLacksFile checks that a commit's tree does NOT contain the given file.
func treeLacksFile(t *testing.T, commitSHA, path string) {
	t.Helper()
	ctx := context.Background()
	out, _, err := git.Run(ctx, "ls-tree", "-r", "--name-only", commitSHA)
	if err != nil {
		t.Fatalf("ls-tree: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == path {
			t.Errorf("file %q should NOT be in tree of %s", path, commitSHA)
			return
		}
	}
}

func TestBasicCommit(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Write a new file
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "add hello",
		Files:   []string{"hello.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", result.SHA)
	}
	if result.Ref != "refs/heads/main" {
		t.Errorf("Ref = %q, want refs/heads/main", result.Ref)
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", result.Attempts)
	}

	commitLandsOnBranch(t, "refs/heads/main", result.SHA)
	treeHasFile(t, result.SHA, "hello.txt")
}

func TestMultipleFiles(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	files := []string{"a.txt", "b.txt", "c.txt"}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(f+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "add three files",
		Files:   files,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, f := range files {
		treeHasFile(t, result.SHA, f)
	}
}

func TestDeletedFile(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// The seed.txt file was created by initTestRepo; delete it
	if err := os.Remove(filepath.Join(dir, "seed.txt")); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "delete seed",
		Files:   []string{"seed.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	treeLacksFile(t, result.SHA, "seed.txt")
}

func TestDeleteSafegitCommittedFile(t *testing.T) {
	// Regression test: a file committed via safegit (not regular git) must be
	// deletable via safegit. IsTracked must check HEAD, not the main index.
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Step 1: Create and commit a file via safegit
	filePath := filepath.Join(dir, "ephemeral.txt")
	if err := os.WriteFile(filePath, []byte("here today\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	addResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "add ephemeral",
		Files:   []string{"ephemeral.txt"},
	})
	if err != nil {
		t.Fatalf("add commit: %v", err)
	}
	treeHasFile(t, addResult.SHA, "ephemeral.txt")

	// Step 2: Delete the file from disk and commit the deletion via safegit
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	delResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "delete ephemeral",
		Files:   []string{"ephemeral.txt"},
	})
	if err != nil {
		t.Fatalf("delete commit: %v", err)
	}
	treeLacksFile(t, delResult.SHA, "ephemeral.txt")
}

func TestCASRetry(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "ours.txt"), []byte("ours\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Use a sync.Once so the hook only fires once (first attempt).
	// The hook advances the branch tip with a direct git commit,
	// causing a CAS miss on the first try.
	var once sync.Once
	p := newPipeline(sgDir)
	p.PhaseADone = func() {
		once.Do(func() {
			// Advance branch with a racing commit
			raceFile := filepath.Join(dir, "race.txt")
			os.WriteFile(raceFile, []byte("race\n"), 0644)
			cmd := exec.Command("git", "add", "race.txt")
			cmd.Dir = dir
			cmd.Run()
			cmd = exec.Command("git", "commit", "-m", "racing commit")
			cmd.Dir = dir
			cmd.Run()
		})
	}

	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "our commit",
		Files:   []string{"ours.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.Attempts < 2 {
		t.Errorf("Attempts = %d, want >= 2 (CAS retry should have happened)", result.Attempts)
	}

	commitLandsOnBranch(t, "refs/heads/main", result.SHA)
	treeHasFile(t, result.SHA, "ours.txt")
	// The racing commit's file should also be in the tree since we rebased on top of it
	treeHasFile(t, result.SHA, "race.txt")
}

func TestCASExhaustion(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	raceCounter := 0
	p := newPipeline(sgDir)
	// Force max attempts to 3 for faster test
	p.Config.Commit.CASMaxAttempts = 3
	p.PhaseADone = func() {
		// Every attempt gets a racing commit, so CAS always misses
		raceCounter++
		raceFile := filepath.Join(dir, "race"+string(rune('0'+raceCounter))+".txt")
		os.WriteFile(raceFile, []byte("race\n"), 0644)
		cmd := exec.Command("git", "add", raceFile)
		cmd.Dir = dir
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "racing "+string(rune('0'+raceCounter)))
		cmd.Dir = dir
		cmd.Run()
	}

	_, err := p.Execute(context.Background(), CommitRequest{
		Message: "doomed commit",
		Files:   []string{"file.txt"},
	})

	if err == nil {
		t.Fatal("expected CAS exhaustion error, got nil")
	}
	ce, ok := err.(*CommitError)
	if !ok {
		t.Fatalf("expected *CommitError, got %T: %v", err, err)
	}
	if ce.Code != exitcode.CASExhausted {
		t.Errorf("error code = %d, want %d", ce.Code, exitcode.CASExhausted)
	}
}

func TestNewUntrackedFile(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create a brand-new file that has never been tracked
	if err := os.WriteFile(filepath.Join(dir, "brand-new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "add brand-new file",
		Files:   []string{"brand-new.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	treeHasFile(t, result.SHA, "brand-new.txt")
	commitLandsOnBranch(t, "refs/heads/main", result.SHA)
}

func TestDryRun(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "dry.txt"), []byte("dry\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Capture the branch tip before the dry run
	beforeSHA, err := git.RevParse(context.Background(), "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "dry run commit",
		Files:   []string{"dry.txt"},
		DryRun:  true,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Commit object should exist but ref should NOT have moved
	if len(result.SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", result.SHA)
	}

	afterSHA, err := git.RevParse(context.Background(), "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if afterSHA != beforeSHA {
		t.Errorf("ref moved during dry run: before=%s, after=%s", beforeSHA, afterSHA)
	}
}

func TestAmend(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create initial commit with file via safegit
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	firstResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "first commit",
		Files:   []string{"file.txt"},
	})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Now amend: add a new file and change message
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("extra\n"), 0644); err != nil {
		t.Fatal(err)
	}

	amendResult, err := p.Amend(context.Background(), AmendRequest{
		Message:   "amended commit",
		FileSpecs: []FileSpec{{Path: "extra.txt"}},
	})
	if err != nil {
		t.Fatalf("Amend: %v", err)
	}

	// Parent should be the same as the original commit's parent
	if amendResult.Parent != firstResult.Parent {
		t.Errorf("parent changed: got %s, want %s", amendResult.Parent, firstResult.Parent)
	}

	// OldSHA should be the first commit
	if amendResult.OldSHA != firstResult.SHA {
		t.Errorf("OldSHA = %s, want %s", amendResult.OldSHA, firstResult.SHA)
	}

	// New commit should have both files in tree
	treeHasFile(t, amendResult.SHA, "file.txt")
	treeHasFile(t, amendResult.SHA, "extra.txt")

	// Verify message changed
	ctx := context.Background()
	out, _, err := git.Run(ctx, "log", "-1", "--format=%s", amendResult.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "amended commit" {
		t.Errorf("message = %q, want %q", strings.TrimSpace(out), "amended commit")
	}

	// Ref should point to new SHA
	commitLandsOnBranch(t, "refs/heads/main", amendResult.SHA)
}

func TestAmendKeepMessage(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create initial commit
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	_, err := p.Execute(context.Background(), CommitRequest{
		Message: "original message",
		Files:   []string{"file.txt"},
	})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Amend without -m: message should be preserved
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("extra\n"), 0644); err != nil {
		t.Fatal(err)
	}

	amendResult, err := p.Amend(context.Background(), AmendRequest{
		FileSpecs: []FileSpec{{Path: "extra.txt"}},
	})
	if err != nil {
		t.Fatalf("Amend: %v", err)
	}

	// Verify message is preserved
	ctx := context.Background()
	out, _, err := git.Run(ctx, "log", "-1", "--format=%s", amendResult.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "original message" {
		t.Errorf("message = %q, want %q", strings.TrimSpace(out), "original message")
	}

	// New file should be in tree
	treeHasFile(t, amendResult.SHA, "extra.txt")
	// Original file should still be in tree
	treeHasFile(t, amendResult.SHA, "file.txt")
}

func TestReword(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create a commit
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	firstResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "old message",
		Files:   []string{"file.txt"},
	})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Reword
	rewordResult, err := p.Reword(context.Background(), RewordRequest{
		Message: "new message",
	})
	if err != nil {
		t.Fatalf("Reword: %v", err)
	}

	// Tree should be unchanged
	if rewordResult.Tree != firstResult.Tree {
		t.Errorf("tree changed: got %s, want %s", rewordResult.Tree, firstResult.Tree)
	}

	// Parent should be unchanged
	if rewordResult.Parent != firstResult.Parent {
		t.Errorf("parent changed: got %s, want %s", rewordResult.Parent, firstResult.Parent)
	}

	// OldSHA should be the first commit
	if rewordResult.OldSHA != firstResult.SHA {
		t.Errorf("OldSHA = %s, want %s", rewordResult.OldSHA, firstResult.SHA)
	}

	// Verify message changed
	ctx := context.Background()
	out, _, err := git.Run(ctx, "log", "-1", "--format=%s", rewordResult.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "new message" {
		t.Errorf("message = %q, want %q", strings.TrimSpace(out), "new message")
	}

	// File should still be in tree
	treeHasFile(t, rewordResult.SHA, "file.txt")

	// Ref should point to new SHA
	commitLandsOnBranch(t, "refs/heads/main", rewordResult.SHA)
}

func TestAmendCASRetry(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create initial commit with file.txt via safegit
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("v1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	_, err := p.Execute(context.Background(), CommitRequest{
		Message: "first commit",
		Files:   []string{"file.txt"},
	})
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// Write files for the concurrent operations
	if err := os.WriteFile(filepath.Join(dir, "racer.txt"), []byte("racer\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("extra\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Race a raw git commit against a safegit amend.
	// The goroutine advances the branch tip via raw git, which should cause
	// a CAS miss in the amend path, triggering a retry.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		cmd := exec.Command("git", "add", "racer.txt")
		cmd.Dir = dir
		cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "racing commit")
		cmd.Dir = dir
		cmd.Run()
	}()

	amendResult, amendErr := p.Amend(context.Background(), AmendRequest{
		Message:   "amended commit",
		FileSpecs: []FileSpec{{Path: "extra.txt"}},
	})
	wg.Wait()

	if amendErr != nil {
		// "branch tip moved" style errors indicate the old CAS bug
		if strings.Contains(amendErr.Error(), "branch tip moved") {
			t.Fatalf("amend failed with old CAS bug: %v", amendErr)
		}
		// CAS exhaustion is acceptable in a tight race if the racer kept winning
		ce, ok := amendErr.(*CommitError)
		if ok && ce.Code == exitcode.CASExhausted {
			t.Logf("CAS exhausted (racer kept winning) -- acceptable in race test")
			return
		}
		t.Fatalf("Amend: %v", amendErr)
	}

	// If amend succeeded, verify the result is sound
	if len(amendResult.SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", amendResult.SHA)
	}
	commitLandsOnBranch(t, "refs/heads/main", amendResult.SHA)
	treeHasFile(t, amendResult.SHA, "extra.txt")

	if amendResult.Attempts > 1 {
		t.Logf("CAS retry worked: amend succeeded after %d attempts", amendResult.Attempts)
	} else {
		t.Logf("amend won the race on first attempt (Attempts=%d)", amendResult.Attempts)
	}
}

func TestCrossBranchPreservesMainIndex(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create a feature branch (from current HEAD)
	ctx := context.Background()
	cmd := exec.Command("git", "branch", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch feature: %v\n%s", err, out)
	}

	// Stage a file in the main index via raw git (simulating another session)
	if err := os.WriteFile(filepath.Join(dir, "staged.txt"), []byte("staged\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "add", "staged.txt")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add staged.txt: %v\n%s", err, out)
	}

	// Commit a different file to the feature branch via safegit
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	result, err := p.Execute(ctx, CommitRequest{
		Message: "feature commit",
		Files:   []string{"feature.txt"},
		Branch:  "feature",
	})
	if err != nil {
		t.Fatalf("cross-branch commit: %v", err)
	}
	treeHasFile(t, result.SHA, "feature.txt")

	// Verify the staged file is still in the main index (git diff --cached)
	out, cErr := exec.Command("git", "diff", "--cached", "--name-only").Output()
	if cErr != nil {
		t.Fatalf("git diff --cached: %v", cErr)
	}
	cachedFiles := strings.TrimSpace(string(out))
	if !strings.Contains(cachedFiles, "staged.txt") {
		t.Errorf("staged.txt should still be in main index after cross-branch commit; cached files: %q", cachedFiles)
	}
}

func TestCommitDirectoryDeletion(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	// Create a directory with two files and commit them
	subDir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(subDir, name), []byte(name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	p := newPipeline(sgDir)
	addResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "add subdir",
		Files:   []string{"subdir/one.txt", "subdir/two.txt"},
	})
	if err != nil {
		t.Fatalf("add commit: %v", err)
	}
	treeHasFile(t, addResult.SHA, "subdir/one.txt")
	treeHasFile(t, addResult.SHA, "subdir/two.txt")

	// Delete the entire directory from disk
	if err := os.RemoveAll(subDir); err != nil {
		t.Fatal(err)
	}

	// Commit the deletion by specifying the directory path
	delResult, err := p.Execute(context.Background(), CommitRequest{
		Message: "delete subdir",
		Files:   []string{"subdir"},
	})
	if err != nil {
		t.Fatalf("delete commit: %v", err)
	}

	// Verify neither file remains in the tree
	treeLacksFile(t, delResult.SHA, "subdir/one.txt")
	treeLacksFile(t, delResult.SHA, "subdir/two.txt")
}

func TestPreCommitHook(t *testing.T) {
	dir, gitDir, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	t.Run("no hook", func(t *testing.T) {
		// Commit should succeed when no hook exists
		if err := os.WriteFile(filepath.Join(dir, "nohook.txt"), []byte("ok\n"), 0644); err != nil {
			t.Fatal(err)
		}
		p := newPipeline(sgDir)
		result, err := p.Execute(context.Background(), CommitRequest{
			Message: "no hook present",
			Files:   []string{"nohook.txt"},
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		treeHasFile(t, result.SHA, "nohook.txt")
	})

	// Create hooks directory
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	hookPath := filepath.Join(hooksDir, "pre-commit")

	t.Run("hook passes", func(t *testing.T) {
		// Install a hook that succeeds (exit 0)
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pass.txt"), []byte("pass\n"), 0644); err != nil {
			t.Fatal(err)
		}
		p := newPipeline(sgDir)
		result, err := p.Execute(context.Background(), CommitRequest{
			Message: "hook passes",
			Files:   []string{"pass.txt"},
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		treeHasFile(t, result.SHA, "pass.txt")
	})

	t.Run("hook fails", func(t *testing.T) {
		// Install a hook that fails (exit 1)
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "fail.txt"), []byte("fail\n"), 0644); err != nil {
			t.Fatal(err)
		}
		p := newPipeline(sgDir)
		_, err := p.Execute(context.Background(), CommitRequest{
			Message: "hook fails",
			Files:   []string{"fail.txt"},
		})
		if err == nil {
			t.Fatal("expected error from failing pre-commit hook, got nil")
		}
		if !strings.Contains(err.Error(), "pre-commit hook failed") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("hook always runs", func(t *testing.T) {
		// Failing hook must always block the commit (no bypass)
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "force.txt"), []byte("force\n"), 0644); err != nil {
			t.Fatal(err)
		}
		p := newPipeline(sgDir)
		_, err := p.Execute(context.Background(), CommitRequest{
			Message: "hook should block",
			Files:   []string{"force.txt"},
		})
		if err == nil {
			t.Fatal("expected error from failing pre-commit hook, got nil")
		}
		if !strings.Contains(err.Error(), "pre-commit hook failed") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("hook skipped with DryRun", func(t *testing.T) {
		// Failing hook should be skipped for dry runs
		if err := os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "dryrun.txt"), []byte("dryrun\n"), 0644); err != nil {
			t.Fatal(err)
		}
		p := newPipeline(sgDir)
		_, err := p.Execute(context.Background(), CommitRequest{
			Message: "dry run skips hook",
			Files:   []string{"dryrun.txt"},
			DryRun:  true,
		})
		if err != nil {
			t.Fatalf("Execute with DryRun: %v", err)
		}
	})

	t.Run("hook sees staged files via GIT_INDEX_FILE", func(t *testing.T) {
		// Install a hook that verifies the staged file is visible
		hookScript := `#!/bin/sh
staged=$(git diff --cached --name-only)
if echo "$staged" | grep -q "visible.txt"; then
  exit 0
else
  echo "pre-commit: visible.txt not found in staged files: $staged" >&2
  exit 1
fi
`
		if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "visible.txt"), []byte("visible\n"), 0644); err != nil {
			t.Fatal(err)
		}
		p := newPipeline(sgDir)
		result, err := p.Execute(context.Background(), CommitRequest{
			Message: "hook sees staged files",
			Files:   []string{"visible.txt"},
		})
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		treeHasFile(t, result.SHA, "visible.txt")
	})
}

func TestInitialCommit(t *testing.T) {
	// Create a git repo with NO initial commit (empty repo)
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Initialize safegit
	gitDir := filepath.Join(dir, ".git")
	if err := repo.Init(context.Background(), gitDir); err != nil {
		t.Fatalf("safegit init: %v", err)
	}
	sgDir := filepath.Join(gitDir, "safegit")
	testutil.Chdir(t, dir)

	// Create a file and commit via safegit pipeline
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	p := newPipeline(sgDir)
	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "initial commit via safegit",
		Files:   []string{"hello.txt"},
	})
	if err != nil {
		t.Fatalf("Execute on empty repo: %v", err)
	}

	// Verify the commit landed
	if len(result.SHA) != 40 {
		t.Errorf("SHA = %q, want 40-char hex", result.SHA)
	}
	commitLandsOnBranch(t, "refs/heads/main", result.SHA)
	treeHasFile(t, result.SHA, "hello.txt")

	// Verify it's a root commit (no parent)
	if result.Parent != "" {
		t.Errorf("Parent = %q, want empty (root commit)", result.Parent)
	}

	// Double-check: git log should show exactly one commit
	ctx := context.Background()
	out, _, err := git.Run(ctx, "rev-list", "--count", "HEAD")
	if err != nil {
		t.Fatalf("rev-list --count: %v", err)
	}
	if strings.TrimSpace(out) != "1" {
		t.Errorf("commit count = %s, want 1", strings.TrimSpace(out))
	}
}

