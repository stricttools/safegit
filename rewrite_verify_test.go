package main

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
)

// TestVerifyIntendedChangesPassesWhatTheOperationDeclared is the false-positive
// half: a rewrite that changed exactly the path it said it would passes.
func TestVerifyIntendedChangesPassesWhatTheOperationDeclared(t *testing.T) {
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "keep.txt", "untouched\n")
	writeFile(t, dir, "secret.txt", "hunter2\n")
	old := commitAll(t, dir, ctx, "add files")

	oldInfo, err := git.ParseCommit(ctx, old)
	if err != nil {
		t.Fatalf("ParseCommit: %v", err)
	}

	// Rewrite secret.txt's blob in a new tree, exactly as a scrub walk does.
	newBlob, err := git.HashObjectWriteBytes(ctx, []byte("REDACTED\n"))
	if err != nil {
		t.Fatalf("writing the replacement blob: %v", err)
	}
	cache := make(map[string]string)
	newTree, err := replaceInTree(ctx, oldInfo.Tree, "secret.txt", newBlob, cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}
	newSHA, err := git.CommitTree(ctx, newTree, oldInfo.Parents, oldInfo.Message,
		&git.CommitIdentity{Author: oldInfo.Author, Committer: oldInfo.Committer})
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}

	shaMap := map[string]string{old: newSHA}
	intent := PerPathIntent()
	intent.Declare(old, []string{"secret.txt"}, false)

	if failures := verifyIntendedChanges(ctx, shaMap, intent); len(failures) != 0 {
		t.Errorf("a rewrite that did exactly what it declared must pass, got: %v", failures)
	}
}

// TestVerifyIntendedChangesCatchesAnUndeclaredPath is the half the check exists
// for: a second file changed in the same rewritten commit, which no operation
// asked for, is named and refused.
func TestVerifyIntendedChangesCatchesAnUndeclaredPath(t *testing.T) {
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "keep.txt", "untouched\n")
	writeFile(t, dir, "secret.txt", "hunter2\n")
	old := commitAll(t, dir, ctx, "add files")

	oldInfo, err := git.ParseCommit(ctx, old)
	if err != nil {
		t.Fatalf("ParseCommit: %v", err)
	}

	redacted, err := git.HashObjectWriteBytes(ctx, []byte("REDACTED\n"))
	if err != nil {
		t.Fatalf("writing the replacement blob: %v", err)
	}
	collateral, err := git.HashObjectWriteBytes(ctx, []byte("clobbered\n"))
	if err != nil {
		t.Fatalf("writing the collateral blob: %v", err)
	}

	cache := make(map[string]string)
	tree, err := replaceInTree(ctx, oldInfo.Tree, "secret.txt", redacted, cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}
	// The rewrite ALSO clobbers keep.txt -- the damage the preservation check
	// is here to refuse.
	cache = make(map[string]string)
	tree, err = replaceInTree(ctx, tree, "keep.txt", collateral, cache)
	if err != nil {
		t.Fatalf("replaceInTree: %v", err)
	}
	newSHA, err := git.CommitTree(ctx, tree, oldInfo.Parents, oldInfo.Message,
		&git.CommitIdentity{Author: oldInfo.Author, Committer: oldInfo.Committer})
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}

	shaMap := map[string]string{old: newSHA}
	intent := PerPathIntent()
	intent.Declare(old, []string{"secret.txt"}, false)

	failures := verifyIntendedChanges(ctx, shaMap, intent)
	if len(failures) == 0 {
		t.Fatal("a path changed that no operation declared must be refused")
	}
	joined := strings.Join(failures, "\n")
	if !strings.Contains(joined, "keep.txt") {
		t.Errorf("the failure must name the undeclared path, got: %s", joined)
	}
	if strings.Contains(joined, "secret.txt: ") {
		t.Errorf("the declared path must not be reported, got: %s", joined)
	}
}

// TestVerifyIntendedChangesCatchesADeclaredChangeThatDidNotHappen closes the
// other direction: an operation that decided to change a path and produced a
// commit without that change is a dropped rewrite, not a success.
func TestVerifyIntendedChangesCatchesADeclaredChangeThatDidNotHappen(t *testing.T) {
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "secret.txt", "hunter2\n")
	old := commitAll(t, dir, ctx, "add secret")

	oldInfo, err := git.ParseCommit(ctx, old)
	if err != nil {
		t.Fatalf("ParseCommit: %v", err)
	}
	// A "rewritten" commit with the SAME tree: only the message differs, so the
	// commit is genuinely a different object.
	newSHA, err := git.CommitTree(ctx, oldInfo.Tree, oldInfo.Parents, "add secret (reworded)\n",
		&git.CommitIdentity{Author: oldInfo.Author, Committer: oldInfo.Committer})
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}

	shaMap := map[string]string{old: newSHA}
	intent := PerPathIntent()
	intent.Declare(old, []string{"secret.txt"}, false)

	failures := verifyIntendedChanges(ctx, shaMap, intent)
	joined := strings.Join(failures, "\n")
	if !strings.Contains(joined, "does not") {
		t.Errorf("a declared change that did not happen must be reported, got: %s", joined)
	}
	if !strings.Contains(joined, "message was rewritten") {
		t.Errorf("an undeclared message rewrite must be reported too, got: %s", joined)
	}
}

// TestVerifyIntendedChangesTripwireOnADroppedCommit pins the rewrote-count
// tripwire: a commit the operation declared a change in, that produced no
// rewritten commit at all, is a refusal.
func TestVerifyIntendedChangesTripwireOnADroppedCommit(t *testing.T) {
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "secret.txt", "hunter2\n")
	old := commitAll(t, dir, ctx, "add secret")

	shaMap := map[string]string{old: old} // identity: nothing was rewritten
	intent := PerPathIntent()
	intent.Declare(old, []string{"secret.txt"}, false)

	failures := verifyIntendedChanges(ctx, shaMap, intent)
	if len(failures) != 1 {
		t.Fatalf("expected exactly the tripwire failure, got %v", failures)
	}
	if !strings.Contains(failures[0], "no rewritten commit was produced") {
		t.Errorf("failure = %q, want the dropped-commit tripwire", failures[0])
	}
}

// TestFinalizeRefusesAnUndeclaredIntent pins that the declaration is mandatory:
// a rewrite that reaches Finalize without one is refused rather than silently
// unverified.
func TestFinalizeRefusesAnUndeclaredIntent(t *testing.T) {
	dir, ctx, sgDir, result := setupFinalizeRepo(t)
	result.Intent = nil
	headBefore := gitInDir(t, dir, "rev-parse", "HEAD")

	err := result.Finalize(ctx, globalFlags{quiet: true}, "scrub file", RewriteHooks{})
	if err == nil {
		t.Fatal("a rewrite with no declared intent must not be finalized")
	}
	if !strings.Contains(err.Error(), "without declaring") {
		t.Errorf("error = %q, want it to name the missing declaration", err)
	}
	if got := gitInDir(t, dir, "rev-parse", "HEAD"); got != headBefore {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", headBefore, got)
	}
	if lines := readRewriteMapLines(t, sgDir); len(lines) != 0 {
		t.Errorf("the refusal must write no journal record, got %d", len(lines))
	}
}
