package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Post-rewrite cleanup prunes the pre-rewrite objects, and the check that says
// whether it worked has to answer two different questions with one walk:
//
//   - an old object no ref reaches any more and that is STILL in the store is
//     residue. The rewrite stands, but the content the operator asked to remove
//     is still readable by SHA, so the command must exit nonzero saying so;
//   - an old object a SURVIVING ref still reaches is not residue at all. A
//     branch outside the walked range legitimately keeps its own history alive,
//     and prune is right to leave it.
//
// The check used to ask only "does the object still exist", which answers the
// first question with a warning nobody's exit code sees and the second one
// wrongly. These two tests are the pair: the finding, and the control that
// keeps the finding from firing on ordinary multi-branch repositories.

var cleanupResidueEnv = []string{"CLAUDE_CODE_SESSION_ID=cleanup-residue-test"}

// scrubFilePayload is as much of `scrub file`'s payload as this file reads.
type scrubFileCleanupPayload struct {
	CleanupOK     *bool    `json:"cleanup_ok"`
	CleanupErrors []string `json:"cleanup_errors"`
}

// keepAllPacks packs every object in the repository and marks the pack kept, so
// nothing already in it is ever pruned.
//
// A .keep file is git's own "another process owns this pack" marker: repack
// leaves such a pack alone and prune only ever touches loose objects. It is the
// realistic shape of the residue this check exists for -- a rewrite whose old
// commits the cleanup genuinely could not remove.
func keepAllPacks(t *testing.T, dir string) {
	t.Helper()
	testutil.Git(t, dir, "repack", "-a", "-d")
	packs, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	if err != nil {
		t.Fatal(err)
	}
	if len(packs) == 0 {
		t.Fatal("repack produced no pack, so nothing can be kept and the test would prove nothing")
	}
	for _, p := range packs {
		keep := strings.TrimSuffix(p, ".pack") + ".keep"
		if err := os.WriteFile(keep, []byte("held by the cleanup residue test\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRewriteExitsNonzeroWhenPreRewriteObjectsSurviveCleanup pins the finding.
//
// Every ref moves, so the pre-rewrite commits are unreachable -- and they are
// sitting in a kept pack, which prune will not touch. That is residue: the
// rewrite stands, and the command says so with RewriteIncomplete rather than
// reporting success and leaving a warning in the scrollback.
func TestRewriteExitsNonzeroWhenPreRewriteObjectsSurviveCleanup(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, cleanupResidueEnv, "a.txt", "a\n", "first")
	commitFileEnv(t, dir, cleanupResidueEnv, "b.txt", "b\n", "second")

	oldHead := testutil.Rev(t, dir, "HEAD")
	keepAllPacks(t, dir)

	_, stderr, code := runSafegitEnv(t, dir, cleanupResidueEnv,
		"--approve-consequential", "author", "rewrite",
		"--old-name", "Test", "--new-name", "Renamed")

	if code != exitcode.RewriteIncomplete {
		t.Errorf("a rewrite whose pre-rewrite objects survived cleanup must exit %d (RewriteIncomplete), got %d; stderr: %s",
			exitcode.RewriteIncomplete, code, stderr)
	}
	if !strings.Contains(stderr, "survived cleanup") {
		t.Errorf("the finding must say that pre-rewrite objects survived cleanup; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, oldHead[:8]) {
		t.Errorf("the finding must name the object that survived (%s); stderr: %s", oldHead[:8], stderr)
	}

	// The rewrite itself stands: this is a report about what is left over, not
	// an abort.
	if got := testutil.Rev(t, dir, "HEAD"); got == oldHead {
		t.Error("the rewrite was abandoned; a cleanup finding never rolls a rewrite back")
	}
}

// TestRewriteIgnoresOldObjectsASurvivingRefStillReaches is the control.
//
// The scrub walks HEAD, so the side branch keeps its own commits -- which are
// pre-rewrite objects the walk mapped to new ones on main. Prune leaves them
// alone because they are reachable, and that is correct: they are the side
// branch's history, not residue. Counting them would fail every ordinary
// multi-branch repository.
func TestRewriteIgnoresOldObjectsASurvivingRefStillReaches(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, cleanupResidueEnv, "secret.txt", "SENSITIVE_DATA_HERE\n", "add secret")
	commitFileEnv(t, dir, cleanupResidueEnv, "b.txt", "b\n", "second")

	// A branch off the current tip, with a commit of its own on top: its tip is
	// a commit the scrub never walks, so nothing remaps it, and it keeps every
	// commit under it alive.
	testutil.Git(t, dir, "branch", "side")
	testutil.Git(t, dir, "checkout", "side")
	commitFileEnv(t, dir, cleanupResidueEnv, "side.txt", "side\n", "side work")
	testutil.Git(t, dir, "checkout", "main")

	commitFileEnv(t, dir, cleanupResidueEnv, "c.txt", "c\n", "third")

	stdout, stderr, code := runSafegitEnv(t, dir, cleanupResidueEnv,
		"--json", "--approve-consequential", "scrub", "file",
		"--delete", "secret.txt",
		"--entire-history", "--reason", "old objects a side branch still reaches")

	if code != 0 {
		t.Fatalf("old commits a surviving branch reaches are not residue; the scrub exited %d: %s", code, stderr)
	}

	env := decodeEnvelope(t, stdout)
	var payload scrubFileCleanupPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("payload is not a scrub file result: %v\npayload: %s", err, env.Payload)
	}
	if payload.CleanupOK == nil {
		t.Fatal("payload carries no cleanup_ok for an executed rewrite")
	}
	if !*payload.CleanupOK {
		t.Errorf("cleanup_ok = false over commits a side branch legitimately keeps alive: %v", payload.CleanupErrors)
	}
	if strings.Contains(stderr, "survived cleanup") {
		t.Errorf("an old object a surviving ref still reaches was reported as residue; stderr: %s", stderr)
	}
}
