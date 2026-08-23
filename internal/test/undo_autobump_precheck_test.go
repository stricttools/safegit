package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING: `safegit undo` skips the auto-bump pre-check every other
// commit-family command runs.
//
// commit, mv, amend, reword and the three conclusions all call
// requireAutoBumpDecision BEFORE they touch a ref, so a submodule whose parent
// has not answered the commit.autoBumpParent question is refused with nothing
// written. undo.go calls only maybeAutoBumpParent, and it calls it AFTER the ref
// has already been moved back -- so the one outcome that refusal exists to
// prevent is exactly what undo produces: a submodule whose branch moved and a
// parent whose gitlink did not, reported as an error.
//
// CURRENT BEHAVIOR, probed: undo exits nonzero with "error: auto-bump parent:
// commit.autoBumpParent not configured in parent repo", and the submodule's HEAD
// has ALREADY moved back by then. So the exit code is right and the ordering is
// wrong, which is what this test discriminates on.
//
// RULED TARGET: undo refuses up front, like every other commit-family command.

// autobumpUndoSession scopes the undo to the commit this test made.
var autobumpUndoSession = []string{"CLAUDE_CODE_SESSION_ID=undo-autobump-test"}

// clearAutoBumpDecision removes the commit.autoBumpParent key from a
// repository's safegit config, which is the state the pre-check exists for: the
// question has not been ANSWERED, as distinct from having been answered false.
//
// There is no `config unset` command, so the file is edited directly.
func clearAutoBumpDecision(t *testing.T, repoDir string) {
	t.Helper()
	path := filepath.Join(repoDir, ".git", "safegit", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	section, ok := doc["commit"].(map[string]interface{})
	if !ok {
		t.Fatalf("%s has no commit section to clear:\n%s", path, data)
	}
	if _, present := section["autoBumpParent"]; !present {
		t.Fatalf("%s does not carry commit.autoBumpParent, so there is nothing to clear:\n%s", path, data)
	}
	delete(section, "autoBumpParent")

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("re-rendering %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TestUndoRefusesBeforeMovingTheRefWhenTheParentHasNotAnsweredAutoBump: the
// control is `safegit commit`, which refuses with the submodule untouched. undo
// must refuse the same way.
func TestUndoRefusesBeforeMovingTheRefWhenTheParentHasNotAnsweredAutoBump(t *testing.T) {
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir) // answers the question with false

	// A safegit commit in the submodule, made while the question IS answered, so
	// there is an oplog entry for undo to reverse.
	testutil.WriteFile(t, subDir, "file.txt", "the change to undo\n")
	safegitCommitEnv(t, subDir, autobumpUndoSession, "sub change", "file.txt")
	subTip := testutil.Rev(t, subDir, "HEAD")
	parentTip := testutil.Rev(t, parentDir, "HEAD")

	// The parent forgets its answer, which is the state the pre-check is for.
	clearAutoBumpDecision(t, parentDir)

	// CONTROL: an ordinary commit refuses before writing anything. This is
	// existing behavior and pins what undo is being measured against.
	testutil.WriteFile(t, subDir, "other.txt", "another change\n")
	_, stderr, code := runSafegitEnv(t, subDir, autobumpUndoSession,
		"commit", "-m", "the control", "--", "other.txt")
	if code == 0 {
		t.Fatalf("the control failed: safegit commit no longer refuses an unanswered auto-bump question")
	}
	if !strings.Contains(stderr, "commit.autoBumpParent not configured") {
		t.Fatalf("the control refused for a different reason:\n%s", stderr)
	}
	if head := testutil.Rev(t, subDir, "HEAD"); head != subTip {
		t.Fatalf("the control moved the submodule's HEAD to %s (was %s)", head, subTip)
	}

	// THE FINDING: undo must refuse the same way -- up front, with the ref
	// untouched.
	stdout, stderr, code := runSafegitEnv(t, subDir, autobumpUndoSession, "undo")
	if code == 0 {
		t.Errorf("undo succeeded although the parent has not answered the auto-bump question\nstdout=%s", stdout)
	}
	if head := testutil.Rev(t, subDir, "HEAD"); head != subTip {
		t.Errorf("undo moved the submodule's HEAD to %s (was %s) and only then refused; the refusal must come first\nstderr=%s",
			head, subTip, stderr)
	}
	if head := testutil.Rev(t, parentDir, "HEAD"); head != parentTip {
		t.Errorf("the parent's HEAD moved to %s (was %s) despite the unanswered question", head, parentTip)
	}
	if !strings.Contains(stderr, "commit.autoBumpParent not configured") {
		t.Errorf("undo's refusal does not name the unanswered question:\n%s", stderr)
	}
}
