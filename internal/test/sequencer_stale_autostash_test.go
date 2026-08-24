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

// newOrphanedAutostashRepo plants the residue itself: a real stash-shaped
// commit holding work that is in no commit and on no ref, and the
// MERGE_AUTOSTASH file naming it, with no merge in flight. It returns the
// repository and that commit.
func newOrphanedAutostashRepo(t *testing.T) (dir, stale string) {
	t.Helper()
	dir = newRepo(t)
	testutil.WriteFile(t, dir, "f.txt", "committed\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "f.txt")

	testutil.WriteFile(t, dir, "f.txt", "uncommitted work held nowhere else\n")
	stale = strings.TrimSpace(testutil.GitOut(t, dir, "stash", "create"))
	if stale == "" {
		t.Fatal("git stash create produced no commit")
	}
	testutil.WriteFile(t, dir, "f.txt", "committed\n")
	testutil.WriteFileAt(t, filepath.Join(dir, ".git", "MERGE_AUTOSTASH"), stale+"\n")

	// The orphan condition: the file is there and no merge is.
	if testutil.FileExists(filepath.Join(dir, ".git", "MERGE_HEAD")) {
		t.Fatal("the fixture must have no merge in flight")
	}
	return dir, stale
}

// TestDoctorFixStoresAnOrphanedAutostashAsAStashEntry is the repair half of the
// same finding. Reporting the orphan is worth something only if there is a way
// out of it, and the way out cannot be "remove the file": the object name in
// that file is the ONLY name the work has, so a removal-first repair hands the
// next `git gc` the operator's uncommitted work.
//
// So the repair STORES the commit on refs/stash first -- after which it is
// stash@{0} and every ordinary stash command reaches it -- and only then removes
// the file.
func TestDoctorFixStoresAnOrphanedAutostashAsAStashEntry(t *testing.T) {
	dir, stale := newOrphanedAutostashRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "doctor", "--action", "fix")
	if code != 0 {
		t.Fatalf("doctor --action fix exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// The work has a name it will keep.
	list := testutil.GitOut(t, dir, "stash", "list", "--format=%H")
	if !strings.Contains(list, stale) {
		t.Errorf("the orphaned autostash (%s) is not on refs/stash after the repair:\n%s", stale, list)
	}
	// And the content really is the work, not an empty entry.
	if shown := testutil.GitOut(t, dir, "show", stale+":f.txt"); !strings.Contains(shown, "uncommitted work held nowhere else") {
		t.Errorf("the stored stash entry does not hold the work it was made from:\n%s", shown)
	}

	// Only now is the file gone.
	if testutil.FileExists(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("doctor --action fix left MERGE_AUTOSTASH in place")
	}
	if !strings.Contains(stdout, "MERGE_AUTOSTASH") {
		t.Errorf("the repair says nothing about what it did with the autostash:\n%s", stdout)
	}
}

// TestDoctorFixPreviewLeavesTheOrphanedAutostashAlone: the preview performs
// neither half -- no stash entry, and above all the file still there.
func TestDoctorFixPreviewLeavesTheOrphanedAutostashAlone(t *testing.T) {
	dir, stale := newOrphanedAutostashRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "doctor", "--action", "fix", "--dry-run")
	if code != 0 {
		t.Fatalf("doctor --action fix --dry-run exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "MERGE_AUTOSTASH")) {
		t.Error("the preview removed MERGE_AUTOSTASH")
	}
	if list := testutil.GitOut(t, dir, "stash", "list", "--format=%H"); strings.Contains(list, stale) {
		t.Errorf("the preview stored the stash entry it only promised:\n%s", list)
	}
	if !strings.Contains(stdout, "would store") {
		t.Errorf("the preview does not say it would store the orphaned autostash:\n%s", stdout)
	}
}

// TestDoctorReportsAnOrphanedAutostash is the companion: a MERGE_AUTOSTASH with
// no merge in flight names a stash-shaped commit whose content lives nowhere
// else, and it is exactly the residue the crash above leaves. doctor is where a
// repository's leftovers are named, and this one is not named at all.
func TestDoctorReportsAnOrphanedAutostash(t *testing.T) {
	dir, stale := newOrphanedAutostashRepo(t)

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
