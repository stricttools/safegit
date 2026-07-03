package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
)

// gitInDir runs a git command in dir and fails the test on error.
func gitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// readRewriteMapLines reads all JSONL lines from <sgDir>/rewrite-maps.jsonl
// into generic maps. Fails the test if the file cannot be parsed.
func readRewriteMapLines(t *testing.T, sgDir string) []map[string]interface{} {
	t.Helper()
	f, err := os.Open(rewriteMapsPath(sgDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("opening rewrite maps: %v", err)
	}
	defer f.Close()

	var lines []map[string]interface{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("parsing rewrite map line %q: %v", raw, err)
		}
		lines = append(lines, m)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading rewrite maps: %v", err)
	}
	return lines
}

// setupFinalizeRepo creates a repo with two commits, rewrites the second
// commit's message via walkAndRewrite, and returns everything needed to call
// Finalize: the repo dir, context, sgDir, and a populated RewriteResult.
func setupFinalizeRepo(t *testing.T) (string, context.Context, string, *RewriteResult) {
	t.Helper()
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	c1 := commitAll(t, dir, ctx, "first")
	writeFile(t, dir, "a.txt", "two\n")
	c2 := commitAll(t, dir, ctx, "second with SECRET")

	// Remote-tracking ref pointing at the pre-rewrite head.
	gitInDir(t, dir, "update-ref", "refs/remotes/origin/main", c2)

	shas := []string{c1, c2}
	shaMap, rewritten, err := walkAndRewrite(ctx, shas, func(ctx context.Context, sha string, info git.CommitInfo, remappedParents []string) (CommitTransform, error) {
		var xform CommitTransform
		if strings.Contains(info.Message, "SECRET") {
			xform.Message = strings.ReplaceAll(info.Message, "SECRET", "REDACTED")
		}
		return xform, nil
	}, false)
	if err != nil {
		t.Fatalf("walkAndRewrite: %v", err)
	}
	if rewritten != 1 {
		t.Fatalf("expected 1 rewritten commit, got %d", rewritten)
	}

	sgDir := filepath.Join(dir, ".git", "safegit")
	result := &RewriteResult{
		ShaMap:         shaMap,
		RewrittenCount: rewritten,
		OldHeadSHA:     c2,
		SgDir:          sgDir,
		Reason:         "unit test rewrite",
		OpName:         "scrub-file",
	}
	return dir, ctx, sgDir, result
}

// TestFinalizeWritesStartRecordBeforeVerifyFailure simulates a failure late in
// the Finalize pipeline (injected failing verify step) and asserts that the
// start and refs records were already persisted, while the complete record is
// absent. This proves the write-at-entry ordering: a crash at any step after
// entry leaves the commit map recoverable.
func TestFinalizeWritesStartRecordBeforeVerifyFailure(t *testing.T) {
	_, ctx, sgDir, result := setupFinalizeRepo(t)
	oldHead := result.OldHeadSHA

	failingVerify := func(ctx context.Context) error {
		return fmt.Errorf("injected verification failure")
	}
	flags := globalFlags{quiet: true}
	err := result.Finalize(ctx, flags, "scrub file", nil, failingVerify)
	if err == nil {
		t.Fatal("Finalize should have returned the injected verification error")
	}

	lines := readRewriteMapLines(t, sgDir)
	if len(lines) != 2 {
		t.Fatalf("expected 2 rewrite map lines (start, refs), got %d: %v", len(lines), lines)
	}
	if lines[0]["phase"] != "start" {
		t.Errorf("line 0 phase = %v, want start", lines[0]["phase"])
	}
	if lines[1]["phase"] != "refs" {
		t.Errorf("line 1 phase = %v, want refs", lines[1]["phase"])
	}
	if lines[0]["old_head"] != oldHead {
		t.Errorf("start old_head = %v, want %v", lines[0]["old_head"], oldHead)
	}
	if lines[0]["reason"] != "unit test rewrite" {
		t.Errorf("start reason = %v, want unit test rewrite", lines[0]["reason"])
	}

	// The commit map must contain the non-identity mapping for the rewritten
	// commit and nothing else.
	commitMap, ok := lines[0]["commit_map"].(map[string]interface{})
	if !ok {
		t.Fatalf("start commit_map is %T, want object", lines[0]["commit_map"])
	}
	if len(commitMap) != 1 {
		t.Fatalf("commit_map has %d entries, want 1: %v", len(commitMap), commitMap)
	}
	newSHA, ok := commitMap[oldHead].(string)
	if !ok || newSHA == oldHead {
		t.Errorf("commit_map[%s] = %v, want a different new SHA", oldHead, commitMap[oldHead])
	}

	// The pre-rewrite remote-tracking state must record the OLD SHA.
	remotes, ok := lines[0]["pre_rewrite_remotes"].(map[string]interface{})
	if !ok {
		t.Fatalf("start pre_rewrite_remotes is %T, want object", lines[0]["pre_rewrite_remotes"])
	}
	if remotes["refs/remotes/origin/main"] != oldHead {
		t.Errorf("pre_rewrite_remotes[origin/main] = %v, want %v", remotes["refs/remotes/origin/main"], oldHead)
	}

	// Both persisted lines must share the same ID.
	if lines[0]["id"] == "" || lines[0]["id"] != lines[1]["id"] {
		t.Errorf("start/refs ids differ: %v vs %v", lines[0]["id"], lines[1]["id"])
	}
}

// TestFinalizeWritesCompleteRecord runs the full Finalize pipeline and checks
// all three phase records, including new_head and cleanup status.
func TestFinalizeWritesCompleteRecord(t *testing.T) {
	dir, ctx, sgDir, result := setupFinalizeRepo(t)
	oldHead := result.OldHeadSHA

	flags := globalFlags{quiet: true}
	if err := result.Finalize(ctx, flags, "scrub file", nil, nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	lines := readRewriteMapLines(t, sgDir)
	if len(lines) != 3 {
		t.Fatalf("expected 3 rewrite map lines, got %d", len(lines))
	}
	for i, phase := range []string{"start", "refs", "complete"} {
		if lines[i]["phase"] != phase {
			t.Errorf("line %d phase = %v, want %s", i, lines[i]["phase"], phase)
		}
		if lines[i]["id"] != lines[0]["id"] {
			t.Errorf("line %d id = %v, want %v", i, lines[i]["id"], lines[0]["id"])
		}
	}

	newHead := gitInDir(t, dir, "rev-parse", "HEAD")
	if lines[2]["new_head"] != newHead {
		t.Errorf("complete new_head = %v, want %v", lines[2]["new_head"], newHead)
	}
	if lines[2]["cleanup_ok"] != true {
		t.Errorf("complete cleanup_ok = %v, want true", lines[2]["cleanup_ok"])
	}
	if result.NewHeadSHA != newHead {
		t.Errorf("result.NewHeadSHA = %v, want %v", result.NewHeadSHA, newHead)
	}
	if !result.CleanupOK {
		t.Errorf("result.CleanupOK = false, want true (errors: %v)", result.CleanupErrors)
	}

	// The remote-tracking ref was rewritten by updateRefs (destroying the old
	// value locally) -- the persisted record must still hold the old SHA.
	remoteNow := gitInDir(t, dir, "rev-parse", "refs/remotes/origin/main")
	if remoteNow == oldHead {
		t.Error("refs/remotes/origin/main was not rewritten; test setup is not exercising remote-ref rewriting")
	}
	remotes := lines[0]["pre_rewrite_remotes"].(map[string]interface{})
	if remotes["refs/remotes/origin/main"] != oldHead {
		t.Errorf("persisted pre-rewrite remote = %v, want old head %v", remotes["refs/remotes/origin/main"], oldHead)
	}
}

// TestFinalizeNoRewritesWritesNoRecords: an all-identity SHA map must not
// pollute the rewrite maps file.
func TestFinalizeNoRewritesWritesNoRecords(t *testing.T) {
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	c1 := commitAll(t, dir, ctx, "first")

	sgDir := filepath.Join(dir, ".git", "safegit")
	result := &RewriteResult{
		ShaMap:     map[string]string{c1: c1},
		OldHeadSHA: c1,
		SgDir:      sgDir,
		OpName:     "scrub-file",
	}
	if err := result.Finalize(ctx, globalFlags{quiet: true}, "scrub file", nil, nil); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if lines := readRewriteMapLines(t, sgDir); len(lines) != 0 {
		t.Errorf("expected no rewrite map lines for identity-only map, got %d", len(lines))
	}
}

// TestCaptureRemoteTrackingStateSkipsSymbolicHead verifies that
// refs/remotes/<remote>/HEAD is excluded from the snapshot.
func TestCaptureRemoteTrackingStateSkipsSymbolicHead(t *testing.T) {
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	c1 := commitAll(t, dir, ctx, "first")

	gitInDir(t, dir, "update-ref", "refs/remotes/origin/main", c1)
	gitInDir(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	remotes, err := captureRemoteTrackingState(ctx)
	if err != nil {
		t.Fatalf("captureRemoteTrackingState: %v", err)
	}
	if remotes["refs/remotes/origin/main"] != c1 {
		t.Errorf("origin/main = %v, want %v", remotes["refs/remotes/origin/main"], c1)
	}
	if _, ok := remotes["refs/remotes/origin/HEAD"]; ok {
		t.Error("symbolic origin/HEAD should be excluded from the snapshot")
	}
}
