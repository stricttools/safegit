package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING: .git/MERGE_AUTOSTASH left behind by an EARLIER crashed conclusion is
// consumed by the next unrelated merge.
//
// sequencer.Read attaches whatever MERGE_AUTOSTASH holds to whatever merge is in
// flight, and consumeAutostash (sequencer_continue.go) applies it on the way out
// of any merge conclusion. Nothing ties the stash-shaped commit to the merge
// being concluded, so a stale file makes a later conclusion apply somebody
// else's work into the working tree and announce "Applied autostash." -- a
// working-tree write nobody asked for, in a file the merge never touched.
//
// RULED TARGET: a conclusion must not consume an autostash that does not belong
// to the current merge. The companion ruling is that `doctor --action diagnose`
// reports an ORPHANED MERGE_AUTOSTASH -- one present with no merge in flight --
// which it does not report at all today.

// staleAutostashFixture is a repository whose git directory carries a stale
// MERGE_AUTOSTASH from an operation that is long over, parked in a NEW
// conflicted merge that never had an autostash of its own.
type staleAutostashFixture struct {
	dir string
	// unrelated is the path the stale stash touches. The new merge does not
	// touch it, so any change to it came from the stale stash.
	unrelated string
	// committed is what unrelated holds in the commit the fixture ends on, and
	// therefore what it must still hold after a conclusion that consumes
	// nothing.
	committed string
	// stale is the stash-shaped commit recorded in MERGE_AUTOSTASH.
	stale string
}

func newStaleAutostashRepo(t *testing.T) staleAutostashFixture {
	t.Helper()
	dir := newRepo(t)

	const committed = "the committed content\n"
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nbase\nl3\n")
	testutil.WriteFile(t, dir, "unrelated.txt", committed)
	safegitCommitEnv(t, dir, conclusionSession, "base", "conflicted.txt", "unrelated.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "-q", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nfeature\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "feature edit", "conflicted.txt")

	testutil.Git(t, dir, "switch", "-q", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "main edit", "conflicted.txt")

	// The residue of an earlier, unrelated autostashed merge whose conclusion
	// was killed before it consumed the file: a real stash-shaped commit, and
	// the file naming it, with the working tree otherwise clean.
	testutil.WriteFile(t, dir, "unrelated.txt", "work from an operation that is long over\n")
	stale := strings.TrimSpace(testutil.GitOut(t, dir, "stash", "create"))
	if stale == "" {
		t.Fatal("git stash create produced no commit; the fixture needs a stash-shaped commit to plant")
	}
	testutil.WriteFile(t, dir, "unrelated.txt", committed)
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Fatalf("the working tree must be clean before the new merge:\n%s", status)
	}
	testutil.WriteFileAt(t, filepath.Join(dir, ".git", "MERGE_AUTOSTASH"), stale+"\n")

	// A NEW merge, which conflicts. It never had an autostash of its own.
	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge", "feature")
	if code == 0 {
		t.Fatalf("safegit merge feature succeeded; the fixture needs a conflict\nstdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Fatalf("safegit merge feature did not report a conflict (code %d)\nstdout=%s stderr=%s", code, stdout, stderr)
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")) {
		t.Fatal("the new merge removed the planted MERGE_AUTOSTASH; the fixture no longer produces the case")
	}
	testutil.WriteFile(t, dir, "conflicted.txt", "l1\nresolved\nl3\n")

	return staleAutostashFixture{dir: dir, unrelated: "unrelated.txt", committed: committed, stale: stale}
}

// TestConclusionDoesNotConsumeAStaleAutostash: the conclusion of a merge that
// never had an autostash must not apply one it found lying about.
func TestConclusionDoesNotConsumeAStaleAutostash(t *testing.T) {
	fx := newStaleAutostashRepo(t)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree")
	combined := stdout + stderr

	if got := readWorktree(t, fx.dir, fx.unrelated); got != fx.committed {
		t.Errorf("%s = %q, want the committed content %q: the conclusion applied a stash that belongs to no merge of its own (exit %d)\n%s",
			fx.unrelated, got, fx.committed, code, combined)
	}
	if strings.Contains(combined, "Applied autostash") {
		t.Errorf("the conclusion announced that it applied an autostash the merge never had:\n%s", combined)
	}
}

// TestDoctorReportsAnOrphanedAutostash is the companion: a MERGE_AUTOSTASH with
// no merge in flight names a stash-shaped commit whose content lives nowhere
// else, and it is exactly the residue the crash above leaves. doctor is where a
// repository's leftovers are named, and this one is not named at all.
func TestDoctorReportsAnOrphanedAutostash(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "committed\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "f.txt")

	testutil.WriteFile(t, dir, "f.txt", "uncommitted work held nowhere else\n")
	stale := strings.TrimSpace(testutil.GitOut(t, dir, "stash", "create"))
	if stale == "" {
		t.Fatal("git stash create produced no commit")
	}
	testutil.WriteFile(t, dir, "f.txt", "committed\n")
	testutil.WriteFileAt(t, filepath.Join(dir, ".git", "MERGE_AUTOSTASH"), stale+"\n")

	// The orphan condition: the file is there and no merge is.
	if testutil.FileExists(filepath.Join(dir, ".git", "MERGE_HEAD")) {
		t.Fatal("the fixture must have no merge in flight")
	}

	stdout, stderr, _ := runSafegitEnv(t, dir, conclusionSession, "doctor", "--action", "diagnose")
	combined := stdout + stderr
	if !strings.Contains(combined, "MERGE_AUTOSTASH") {
		t.Errorf("doctor does not report the orphaned MERGE_AUTOSTASH (%s), whose content is held nowhere else:\n%s",
			stale, combined)
	}

	// The diagnosis is worth nothing if the file is quietly gone instead.
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")); err != nil {
		t.Errorf("diagnose removed MERGE_AUTOSTASH: %v", err)
	}
}
