package commit

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
)

// The compare-and-swap rule for inferred move records.
//
// Inference reads the ATTEMPT'S OWN delta. The declared records are resolved
// once before the retry loop and cannot change; an inferred one can, because a
// concurrent session moving the ref changes the parent tree the attempt
// compares against -- and therefore what the delta witnesses.
//
// The commit-msg hook already ran on attempt 1's message, records and all, and
// its answer is cached for every later attempt. So the two possible outcomes
// are the two pinned here: the recomputed set is the same and the operation
// proceeds with attempt 1's ids, or it differs and the operation aborts rather
// than committing a message whose records the delta no longer bears out.
//
// These tests live in this package rather than in the integration suite
// because the race has to be DETERMINISTIC: PhaseADone fires between the tree
// being built and the compare-and-swap, which is the exact window, and no
// arrangement of two real processes can hit it reliably.

// racingCommit advances the branch tip with a plain git commit, which is what a
// concurrent session looks like from inside the retry loop.
func racingCommit(t *testing.T, dir, path, content, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", path}, {"commit", "-m", message}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// A retry whose recomputed inference matches attempt 1's proceeds, and the
// record the commit carries is the one minted on attempt 1 -- the id included,
// because a commit's records must not depend on how many attempts it took.
func TestInferenceSurvivesACASRetryThatChangesNothing(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("content nothing else holds\n"), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	if _, err := p.Execute(context.Background(), CommitRequest{Message: "seed", Files: []string{"a.txt"}}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}

	// The racing commit touches an unrelated path, so the delta this commit
	// witnesses is the same on both attempts.
	var once sync.Once
	p.PhaseADone = func() {
		once.Do(func() { racingCommit(t, dir, "unrelated.txt", "unrelated\n", "racing") })
	}

	result, err := p.Execute(context.Background(), CommitRequest{
		Message: "move a to b",
		Files:   []string{"a.txt", "b.txt"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Attempts < 2 {
		t.Fatalf("Attempts = %d, want >= 2 (the fixture needs a CAS retry)", result.Attempts)
	}

	msg := commitMessageOfSHA(t, result.SHA)
	if !strings.Contains(msg, "a.txt -> b.txt") {
		t.Errorf("the retried commit lost its inferred record; message:\n%s", msg)
	}
	if got := strings.Count(msg, "Moved: "); got != 1 {
		t.Errorf("the retried commit carries %d records, want 1; message:\n%s", got, msg)
	}
}

// A retry whose recomputed inference DIFFERS aborts. Here the racing commit
// puts a second copy of the moved content into the parent tree, so on attempt 2
// the parent-tree uniqueness fence turns the pair down and the delta no longer
// witnesses the move attempt 1's message already records.
func TestInferenceAbortsWhenARetryChangesWhatTheDeltaWitnesses(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	const content = "content nothing else holds\n"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	p := newPipeline(sgDir)
	if _, err := p.Execute(context.Background(), CommitRequest{Message: "seed", Files: []string{"a.txt"}}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}

	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")); err != nil {
		t.Fatal(err)
	}

	var once sync.Once
	p.PhaseADone = func() {
		once.Do(func() { racingCommit(t, dir, "elsewhere.txt", content, "racing copy") })
	}

	_, err := p.Execute(context.Background(), CommitRequest{
		Message: "move a to b",
		Files:   []string{"a.txt", "b.txt"},
	})
	if err == nil {
		t.Fatal("expected the operation to abort, got a commit")
	}
	if !strings.Contains(err.Error(), "run the command again") {
		t.Errorf("the abort does not advise a re-run: %v", err)
	}

	// Nothing was committed by THIS operation: the tip is the racing commit,
	// which is the only thing that moved the branch.
	msg := commitMessageOfSHA(t, headSHA(t))
	if !strings.Contains(msg, "racing copy") {
		t.Errorf("the tip is not the racing commit; message:\n%s", msg)
	}
	if strings.Contains(msg, "move a to b") {
		t.Errorf("the aborted operation committed anyway; tip message:\n%s", msg)
	}
}

// commitMessageOfSHA reads a commit object's message, which is where a record
// lives -- not `git log`'s rendering, which re-wraps.
func commitMessageOfSHA(t *testing.T, sha string) string {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "commit", sha)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cat-file commit %s: %v", sha, err)
	}
	raw := string(out)
	blank := strings.Index(raw, "\n\n")
	if blank < 0 {
		t.Fatalf("commit %s has no message: %q", sha, raw)
	}
	return raw[blank+2:]
}

func headSHA(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}
