package commit

import (
	"context"
	"errors"
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
	// The abort has its own registered code. It is the one purely TRANSIENT
	// refusal in the commit family -- nothing is wrong with the command, and
	// running it again is the whole remedy -- and General would leave a caller
	// unable to tell it from a failure that will happen again.
	var cerr *CommitError
	if !errors.As(err, &cerr) {
		t.Fatalf("the abort is not a CommitError, so it carries no exit code: %v", err)
	}
	if cerr.Code != exitcode.MoveWitnessChanged {
		t.Errorf("the abort exits %d, want MoveWitnessChanged (%d): %v",
			cerr.Code, exitcode.MoveWitnessChanged, err)
	}
	// And it NAMES the pair whose witness changed, because "something changed"
	// is not something a caller can check against their own working tree.
	if !strings.Contains(err.Error(), "a.txt -> b.txt") {
		t.Errorf("the abort does not name the pair that changed: %v", err)
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

// The refusals and the cap are the WINNING attempt's, not the first attempt's.
//
// The PAIRS are deliberately attempt 1's -- their ids are already in the cached
// message, so a commit's records must not depend on how many attempts it took,
// and a set that differs aborts the operation outright. The refusals are the
// opposite case: they are not on the commit, they describe what the delta the
// commit was BUILT FROM did not single out, and that delta is the winning
// attempt's. Retaining attempt 1's answer would report a candidate against a
// tree the commit was never built on.
//
// This drives the inference type directly, because the state is per operation
// and the two attempts have to differ in exactly one way. Both deltas leave the
// same (empty) pair set, so the operation proceeds; they name different paths
// on the ambiguous side. Neither reaches a tree listing -- an ambiguous blob is
// turned down before fence 4 needs either tree.
func TestTheRefusalSetIsTheWinningAttemptsNotTheFirsts(t *testing.T) {
	const blob = "1111111111111111111111111111111111111111"
	del := func(path string) git.ChangedPath {
		return git.ChangedPath{Status: "D", Path: path, SrcMode: "100644", SrcSHA: blob,
			DstMode: "000000", DstSHA: git.ZeroSHA}
	}
	add := func(path string) git.ChangedPath {
		return git.ChangedPath{Status: "A", Path: path, DstMode: "100644", DstSHA: blob,
			SrcMode: "000000", SrcSHA: git.ZeroSHA}
	}

	m := newAmendMoveInference()
	ctx := context.Background()

	first := []git.ChangedPath{del("x1.txt"), del("x2.txt"), add("y1.txt"), add("y2.txt")}
	if _, err := m.records(ctx, first, "", "", nil, nil); err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if len(m.refused) != 1 || strings.Join(m.refused[0].Old, ",") != "x1.txt,x2.txt" {
		t.Fatalf("attempt 1 refused %+v, want the ambiguous blob at both deleted paths", m.refused)
	}

	// The retry's delta drops one of the deleted paths. The blob is still
	// ambiguous -- one deletion, two additions -- so the pair set is empty on
	// both attempts and the operation proceeds.
	second := []git.ChangedPath{del("x1.txt"), add("y1.txt"), add("y2.txt")}
	if _, err := m.records(ctx, second, "", "", nil, nil); err != nil {
		t.Fatalf("attempt 2: %v", err)
	}
	if len(m.refused) != 1 {
		t.Fatalf("refused = %+v, want one entry", m.refused)
	}
	if got := strings.Join(m.refused[0].Old, ","); got != "x1.txt" {
		t.Errorf("refused names %q, want the winning attempt's x1.txt alone", got)
	}
}
