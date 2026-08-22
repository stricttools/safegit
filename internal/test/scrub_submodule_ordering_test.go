package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A submodule scrub is one operation across TWO repositories: it rewrites the
// submodule's commits and the parent commits whose gitlinks point at them. The
// tests here are about the ORDER those two repositories are written in.
//
// The property they pin is objects-before-refs across both: every rewritten
// commit exists as an unreachable object, both histories are verified, and only
// then does either repository's refs move -- submodule first. Two consequences
// follow, and each has its own test below:
//
//   - a verification failure on EITHER side leaves BOTH repositories untouched;
//   - any instant at which the process can die reads as either "nothing
//     happened" or "the journal says what happened", never as a parent pointing
//     at submodule commits no ref keeps alive.

var submoduleOrderingEnv = []string{"CLAUDE_CODE_SESSION_ID=submodule-ordering-test"}

// runGitIn runs a git command in dir and fails the test if it does not succeed.
func runGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// submoduleScrubFixture builds the repository pair every test in this file
// scrubs.
//
// The submodule holds two commits: the first writes the secret, the second
// replaces it on disk, so a `scrub file --replace-with` over the whole submodule
// history genuinely rewrites both. The parent holds three, the last of which
// records the submodule's second commit as its gitlink. Both repositories carry
// a tag on a commit the rewrite moves, so the tags are part of what the end
// state pins.
func submoduleScrubFixture(t *testing.T) (parentDir, subDir, subGitDir, subGitSafegitDir, firstSubCommit string) {
	t.Helper()
	parentDir, _, subDir = newRepoWithSubmoduleSecret(t, "SENSITIVE_DATA_HERE", "secret.txt")

	firstSubCommit = testutil.Rev(t, subDir, "HEAD")
	runGitIn(t, subDir, "tag", "sub-v1", firstSubCommit)

	if err := os.WriteFile(filepath.Join(subDir, "secret.txt"), []byte("CLEANED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitIn(t, subDir, "config", "user.email", "test@test.com")
	runGitIn(t, subDir, "config", "user.name", "Test")
	runGitIn(t, subDir, "add", "secret.txt")
	runGitIn(t, subDir, "commit", "-m", "commit replacement")

	runGitIn(t, parentDir, "add", "mysub")
	runGitIn(t, parentDir, "commit", "-m", "update submodule ref")
	runGitIn(t, parentDir, "tag", "parent-v1", "HEAD")

	subGitDir = submoduleGitDir(t, subDir)
	subGitSafegitDir = filepath.Join(subGitDir, "safegit")
	return
}

// scrubSubmoduleFile is the command every test here runs: replace the
// submodule's secret.txt across the submodule's whole history, which moves the
// parent's gitlinks with it.
func scrubSubmoduleFile(t *testing.T, parentDir string, extraEnv []string) (stdout, stderr string, code int) {
	t.Helper()
	return runSafegitEnv(t, parentDir, append(append([]string{}, submoduleOrderingEnv...), extraEnv...),
		"--approve-consequential", "scrub", "file",
		"--replace-with", "mysub/secret.txt",
		"mysub/secret.txt",
		"--entire-history",
		"--reason", "submodule ordering test",
	)
}

// journalPhases lists the phase of every record in a rewrite journal, in file
// order, so a test can state the sequence it expects as one comparison.
func journalPhases(lines []rewriteMapLine) []string {
	phases := make([]string, 0, len(lines))
	for _, l := range lines {
		p, _ := l["phase"].(string)
		phases = append(phases, p)
	}
	return phases
}

func samePhases(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// refsSnapshot records everything about one repository that the rewrite is
// supposed to move, so a test can assert "untouched" as a single comparison
// rather than a list of rev-parses.
type refsSnapshot struct {
	head string
	tag  string
}

func snapshotRefs(t *testing.T, dir, tag string) refsSnapshot {
	t.Helper()
	return refsSnapshot{
		head: testutil.Rev(t, dir, "HEAD"),
		tag:  testutil.Rev(t, dir, tag),
	}
}

func requireUntouched(t *testing.T, what string, dir, tag string, before refsSnapshot) {
	t.Helper()
	after := snapshotRefs(t, dir, tag)
	if after.head != before.head {
		t.Errorf("%s HEAD moved (%s -> %s); it must be exactly where it was",
			what, before.head[:12], after.head[:12])
	}
	if after.tag != before.tag {
		t.Errorf("%s tag %s moved (%s -> %s); it must be exactly where it was",
			what, tag, before.tag[:12], after.tag[:12])
	}
}

// TestSubmoduleScrubParentVerificationFailureLeavesBothUntouched is the
// property the restructure exists for. The parent's Tier A is made to refuse
// after BOTH histories have been rewritten as objects: foreign work appears in
// the parent's working tree while the rewrite is running, which is exactly what
// the cleanliness re-check refuses to publish over.
//
// Before the restructure the submodule was finalized before the parent walk even
// began, so this refusal would have left a published submodule and an
// unpublished parent -- a repository pair that no longer agreed with itself.
func TestSubmoduleScrubParentVerificationFailureLeavesBothUntouched(t *testing.T) {
	parentDir, subDir, _, subSgDir, _ := submoduleScrubFixture(t)

	parentBefore := snapshotRefs(t, parentDir, "parent-v1")
	subBefore := snapshotRefs(t, subDir, "sub-v1")

	// The parent's commits are rewritten with no GIT_DIR set (the submodule's
	// walk sets one), so this snippet fires exactly once the operation has moved
	// on from the submodule's objects to the parent's -- after every submodule
	// commit has been written and before anything has been verified.
	env, _ := gitShim(t, "commit-tree",
		`if [ -z "$GIT_DIR" ]; then echo foreign > `+filepath.Join(parentDir, "foreign.txt")+`; fi`)

	_, stderr, code := scrubSubmoduleFile(t, parentDir, env)
	if code == 0 {
		t.Fatalf("the parent's cleanliness check must refuse; stderr:\n%s", stderr)
	}

	requireUntouched(t, "submodule", subDir, "sub-v1", subBefore)
	requireUntouched(t, "parent", parentDir, "parent-v1", parentBefore)

	if lines := readRewriteMapsAt(t, subSgDir); len(lines) != 0 {
		t.Errorf("the submodule journal must be empty after a refusal, got %v", journalPhases(lines))
	}
	if lines := readRewriteMaps(t, parentDir); len(lines) != 0 {
		t.Errorf("the parent journal must be empty after a refusal, got %v", journalPhases(lines))
	}
}

// TestSubmoduleScrubHappyPathEndState pins the end state of a successful
// submodule scrub outright, rather than by comparison against how the old
// ordering behaved -- that behavior no longer exists. Both repositories' refs
// and tags sit on rewritten commits, both journals carry start/refs/complete in
// that order, and both report cleanup done.
func TestSubmoduleScrubHappyPathEndState(t *testing.T) {
	parentDir, subDir, _, subSgDir, _ := submoduleScrubFixture(t)

	parentBefore := snapshotRefs(t, parentDir, "parent-v1")
	subBefore := snapshotRefs(t, subDir, "sub-v1")

	stdout, stderr, code := scrubSubmoduleFile(t, parentDir, nil)
	if code != 0 {
		t.Fatalf("scrub failed (code %d)\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	// Refs and tags moved, on both sides, onto commits that are part of the new
	// history rather than dangling leftovers.
	for _, tc := range []struct {
		what   string
		dir    string
		tag    string
		before refsSnapshot
	}{
		{"submodule", subDir, "sub-v1", subBefore},
		{"parent", parentDir, "parent-v1", parentBefore},
	} {
		after := snapshotRefs(t, tc.dir, tc.tag)
		if after.head == tc.before.head {
			t.Errorf("%s HEAD did not move", tc.what)
		}
		if after.tag == tc.before.tag {
			t.Errorf("%s tag %s did not move", tc.what, tc.tag)
		}
		cmd := exec.Command("git", "merge-base", "--is-ancestor", after.tag, after.head)
		cmd.Dir = tc.dir
		if err := cmd.Run(); err != nil {
			t.Errorf("%s tag %s (%s) is not in the rewritten history under HEAD",
				tc.what, tc.tag, after.tag[:12])
		}
	}

	// The parent's gitlink names a commit the submodule actually has.
	gitlink := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	cmd := exec.Command("git", "cat-file", "-e", gitlink+"^{commit}")
	cmd.Dir = subDir
	if err := cmd.Run(); err != nil {
		t.Errorf("the parent's gitlink %s does not name a commit in the submodule", gitlink[:12])
	}

	// Both journals, in order, with cleanup reported done.
	for _, tc := range []struct {
		what  string
		lines []rewriteMapLine
	}{
		{"submodule", readRewriteMapsAt(t, subSgDir)},
		{"parent", readRewriteMaps(t, parentDir)},
	} {
		phases := journalPhases(tc.lines)
		if !samePhases(phases, "start", "refs", "complete") {
			t.Errorf("%s journal phases = %v, want start/refs/complete", tc.what, phases)
			continue
		}
		if ok, _ := tc.lines[2]["cleanup_ok"].(bool); !ok {
			t.Errorf("%s journal reports cleanup_ok=false: %v", tc.what, tc.lines[2]["cleanup_errors"])
		}
	}
}

// killSafegitAt builds a git shim snippet that kills the safegit process the
// instant a matching git invocation is about to run, and refuses to run it.
//
// The shim is exec'd by safegit itself, so $PPID inside it IS safegit: no
// process-tree walking is needed. Killing and then refusing (rather than
// killing alone) is what makes the injection deterministic -- the real git is
// never reached, so a ref transaction that was about to happen definitely does
// not happen, and the on-disk state is exactly the state a crash at that instant
// leaves behind.
const killSafegit = `kill -9 $PPID; exit 1`

// TestSubmoduleScrubCrashAfterSubmoduleObjectsLeavesBothUntouched injects a
// crash at the first boundary: every submodule commit has been rewritten as an
// object, and the parent's own rewrite is just starting. Nothing has been
// verified and no ref has moved, so the whole pair must read as untouched.
func TestSubmoduleScrubCrashAfterSubmoduleObjectsLeavesBothUntouched(t *testing.T) {
	parentDir, subDir, _, subSgDir, _ := submoduleScrubFixture(t)

	parentBefore := snapshotRefs(t, parentDir, "parent-v1")
	subBefore := snapshotRefs(t, subDir, "sub-v1")

	// GIT_DIR is set for every submodule call and unset for the parent's, so the
	// first commit-tree without one is the first parent commit being written.
	env, _ := gitShim(t, "commit-tree", `if [ -z "$GIT_DIR" ]; then `+killSafegit+`; fi`)

	if _, _, code := scrubSubmoduleFile(t, parentDir, env); code == 0 {
		t.Fatal("the injected crash must not produce a successful run")
	}

	requireUntouched(t, "submodule", subDir, "sub-v1", subBefore)
	requireUntouched(t, "parent", parentDir, "parent-v1", parentBefore)

	if lines := readRewriteMapsAt(t, subSgDir); len(lines) != 0 {
		t.Errorf("no submodule journal record may exist yet, got %v", journalPhases(lines))
	}
	if lines := readRewriteMaps(t, parentDir); len(lines) != 0 {
		t.Errorf("no parent journal record may exist yet, got %v", journalPhases(lines))
	}
}

// TestSubmoduleScrubCrashBeforeAnyRefMovesIsJournalExplainable injects a crash
// at the second boundary: BOTH histories are written and verified, the
// submodule's journal start record has been persisted, and the submodule's very
// first ref update is about to run.
//
// No ref has moved anywhere, and the one thing on disk that says a rewrite was
// under way is the submodule's start record -- a start with no refs and no
// complete, which is precisely how a crashed rewrite is meant to read.
func TestSubmoduleScrubCrashBeforeAnyRefMovesIsJournalExplainable(t *testing.T) {
	parentDir, subDir, _, subSgDir, _ := submoduleScrubFixture(t)

	parentBefore := snapshotRefs(t, parentDir, "parent-v1")
	subBefore := snapshotRefs(t, subDir, "sub-v1")

	// The submodule publishes first, so the first update-ref of the whole
	// operation is the submodule's -- and it carries a GIT_DIR.
	env, _ := gitShim(t, "update-ref", `if [ -n "$GIT_DIR" ]; then `+killSafegit+`; fi`)

	if _, _, code := scrubSubmoduleFile(t, parentDir, env); code == 0 {
		t.Fatal("the injected crash must not produce a successful run")
	}

	requireUntouched(t, "submodule", subDir, "sub-v1", subBefore)
	requireUntouched(t, "parent", parentDir, "parent-v1", parentBefore)

	subPhases := journalPhases(readRewriteMapsAt(t, subSgDir))
	if !samePhases(subPhases, "start") {
		t.Errorf("submodule journal phases = %v, want a lone start (a crashed rewrite)", subPhases)
	}
	if lines := readRewriteMaps(t, parentDir); len(lines) != 0 {
		t.Errorf("the parent journal must still be empty, got %v", journalPhases(lines))
	}
}

// TestSubmoduleScrubCrashBetweenFinalizesLeavesParentUntouched injects a crash
// at the third boundary: the submodule is fully published and the parent's own
// refs are about to move.
//
// This is the one window where the two repositories legitimately disagree, and
// it is the window that has to be JOURNAL-EXPLAINABLE rather than untouched.
// The submodule has moved forward -- including its post-rewrite cleanup, which
// prunes the commits it replaced -- so the parent's gitlinks name submodule
// commits that no longer resolve. What makes that recoverable instead of lost is
// the submodule's own journal: its completed record set carries the whole
// old-to-new commit map, so every stale gitlink the parent still holds has its
// replacement written down. The parent's side is the ordinary crashed-rewrite
// reading: a start record with no refs and no complete.
func TestSubmoduleScrubCrashBetweenFinalizesLeavesParentUntouched(t *testing.T) {
	parentDir, subDir, _, subSgDir, _ := submoduleScrubFixture(t)

	parentBefore := snapshotRefs(t, parentDir, "parent-v1")
	subBefore := snapshotRefs(t, subDir, "sub-v1")

	// The parent's ref updates are the ones with no GIT_DIR.
	env, _ := gitShim(t, "update-ref", `if [ -z "$GIT_DIR" ]; then `+killSafegit+`; fi`)

	if _, _, code := scrubSubmoduleFile(t, parentDir, env); code == 0 {
		t.Fatal("the injected crash must not produce a successful run")
	}

	requireUntouched(t, "parent", parentDir, "parent-v1", parentBefore)

	subAfter := snapshotRefs(t, subDir, "sub-v1")
	if subAfter.head == subBefore.head {
		t.Error("the submodule was published before the crash, so its HEAD must have moved")
	}
	if subAfter.tag == subBefore.tag {
		t.Error("the submodule was published before the crash, so its tag must have moved")
	}

	subLines := readRewriteMapsAt(t, subSgDir)
	subPhases := journalPhases(subLines)
	if !samePhases(subPhases, "start", "refs", "complete") {
		t.Fatalf("submodule journal phases = %v, want a completed rewrite", subPhases)
	}

	// The parent still records the OLD submodule commit. Whether that commit
	// survived the submodule's prune is not the guarantee -- the journal is: the
	// submodule's commit map has to name the replacement for it.
	gitlink := lsTreeSHA(t, lsTreeEntry(t, parentDir, "mysub"))
	commitMap, _ := subLines[0]["commit_map"].(map[string]interface{})
	replacement, ok := commitMap[gitlink].(string)
	if !ok {
		t.Fatalf("the parent's gitlink %s is not a key of the submodule's journalled commit map, "+
			"so nothing on disk explains what it should become; the map holds %d entries",
			gitlink[:12], len(commitMap))
	}
	cmd := exec.Command("git", "cat-file", "-e", replacement+"^{commit}")
	cmd.Dir = subDir
	if err := cmd.Run(); err != nil {
		t.Errorf("the journal maps the parent's gitlink %s to %s, which is not a commit in the submodule",
			gitlink[:12], replacement[:12])
	}
	parentPhases := journalPhases(readRewriteMaps(t, parentDir))
	if !samePhases(parentPhases, "start") {
		t.Errorf("parent journal phases = %v, want a lone start (a crashed rewrite)", parentPhases)
	}
}
