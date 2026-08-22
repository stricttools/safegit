package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit mv` is the move that performs itself: it renames the paths, records
// what it renamed, and commits the result in one invocation. Everything else in
// the move vocabulary is a DECLARATION about a move somebody already made
// (`--moved`); this is the one command that makes one.
//
// The properties pinned here are the ones the command exists for: nothing is
// touched until every pair has been checked, a failure part-way through puts
// back what it moved, a directory is one record however many files it holds,
// and a preview performs nothing at all.

// mvSeed builds a repository with two tracked files and a tracked directory.
func mvSeed(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	testutil.WriteFile(t, dir, "b.txt", "b\n")
	testutil.WriteFile(t, dir, "src/one.txt", "1\n")
	testutil.WriteFile(t, dir, "src/deep/two.txt", "2\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "a.txt", "b.txt", "src"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	return dir
}

// mvExists reports whether a repo-relative path is on disk.
func mvExists(t *testing.T, dir, rel string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, rel))
	return err == nil
}

// mvFoldsCase reports whether the filesystem holding dir treats two spellings of
// one name as the same file. mvSeed's a.txt is the probe: where A.TXT resolves,
// so does every other spelling.
//
// It is the condition the case-only branches divide on, asked of the filesystem
// itself rather than of core.ignorecase, because a test that forces the config
// key needs to know whether the world it is running in agrees with it.
func mvFoldsCase(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(dir, "A.TXT"))
	return err == nil
}

func TestMvMovesRecordsAndCommitsInOneInvocation(t *testing.T) {
	dir := mvSeed(t)

	stdout, stderr, code := runSafegit(t, dir, "mv", "-m", "move a", "a.txt -> sub/a.txt")
	if code != 0 {
		t.Fatalf("mv failed (code %d): %s\n%s", code, stderr, stdout)
	}

	if mvExists(t, dir, "a.txt") {
		t.Error("the old path is still on disk")
	}
	if !mvExists(t, dir, "sub/a.txt") {
		t.Error("the new path is not on disk")
	}

	msg := commitMessageOf(t, dir, "HEAD")
	records := movedRecordsIn(t, msg)
	if len(records) != 1 || records[0][1] != "a.txt -> sub/a.txt" {
		t.Fatalf("expected one record for the move, got %v in:\n%s", records, msg)
	}
	if !strings.HasPrefix(msg, "move a") {
		t.Errorf("the message is not the one given:\n%s", msg)
	}

	// The commit is the rename and nothing else: the blob is carried across
	// unchanged, so the two paths are the only thing that differs.
	status := testutil.Git(t, dir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-status", "HEAD")
	if !strings.Contains(status, "A\tsub/a.txt") || !strings.Contains(status, "D\ta.txt") {
		t.Errorf("the commit's paths are:\n%s", status)
	}
	if got := testutil.Git(t, dir, "show", "HEAD:sub/a.txt"); got != "a" {
		t.Errorf("the moved blob is %q, want the original content", got)
	}
	// Nothing is left staged or unstaged: the working tree and the commit agree.
	if porcelain := testutil.Git(t, dir, "status", "--porcelain"); porcelain != "" {
		t.Errorf("the working tree is dirty after a move:\n%s", porcelain)
	}
}

func TestMvMovesADirectoryAsOneSubtreeRecord(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move the directory", "src/ -> lib/"); code != 0 {
		t.Fatalf("subtree mv failed (code %d): %s", code, stderr)
	}

	if mvExists(t, dir, "src") {
		t.Error("the old directory is still on disk")
	}
	for _, rel := range []string{"lib/one.txt", "lib/deep/two.txt"} {
		if !mvExists(t, dir, rel) {
			t.Errorf("%s is not on disk", rel)
		}
	}

	records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(records) != 1 || records[0][1] != "src/ -> lib/" {
		t.Fatalf("expected one subtree record, got %v", records)
	}
	files := testutil.Git(t, dir, "diff-tree", "--no-commit-id", "--no-renames", "-r", "--name-only", "HEAD")
	if len(strings.Fields(files)) != 4 {
		t.Errorf("expected four changed paths (two removed, two added), got:\n%s", files)
	}
}

func TestMvMovesEveryPairInOneCommit(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "two moves",
		"a.txt -> x.txt", "b.txt -> y.txt"); code != 0 {
		t.Fatalf("multi-pair mv failed (code %d): %s", code, stderr)
	}

	records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(records) != 2 {
		t.Fatalf("expected two records, got %v", records)
	}
	pairs := map[string]bool{records[0][1]: true, records[1][1]: true}
	if !pairs["a.txt -> x.txt"] || !pairs["b.txt -> y.txt"] {
		t.Errorf("records are %v", records)
	}
	// One invocation, one commit -- the seed and this one.
	if n := len(strings.Split(strings.TrimSpace(testutil.Git(t, dir, "log", "--format=%H")), "\n")); n != 3 {
		t.Errorf("mv created %d commits above the seed, want one", n-2)
	}
	// Each record carries its own id.
	if records[0][0] == records[1][0] {
		t.Errorf("both records share the id %s", records[0][0])
	}
}

// TestMvChecksEveryPairBeforeMovingAnything is the atomicity property stated
// from the validation side: one bad pair in a set stops the whole invocation,
// and every pair that is wrong is named rather than only the first.
func TestMvChecksEveryPairBeforeMovingAnything(t *testing.T) {
	dir := mvSeed(t)

	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move",
		"a.txt -> x.txt", "nowhere.txt -> z.txt", "b.txt -> src/one.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d (MoveNotBorneOut): %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	for _, pair := range []string{"nowhere.txt -> z.txt", "b.txt -> src/one.txt"} {
		if !strings.Contains(stderr, pair) {
			t.Errorf("the refusal does not name %q: %s", pair, stderr)
		}
	}
	// The good pair was never performed: validation happens before the first
	// filesystem mutation, not pair by pair.
	if mvExists(t, dir, "x.txt") || !mvExists(t, dir, "a.txt") {
		t.Error("a refused invocation moved a file anyway")
	}
	assertNoCommitHappened(t, dir, "seed")
}

func TestMvRefusesADestinationThatAlreadyExists(t *testing.T) {
	dir := mvSeed(t)
	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move", "a.txt -> b.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "b.txt") {
		t.Errorf("the refusal does not name the destination: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

func TestMvRefusesAFileFormPairForADirectory(t *testing.T) {
	dir := mvSeed(t)
	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move", "src -> lib")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
	}
	if !strings.Contains(stderr, "src/ -> lib/") {
		t.Errorf("the refusal does not name the subtree spelling: %s", stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

func TestMvGrammarErrorsExitUsage(t *testing.T) {
	dir := mvSeed(t)
	for _, pair := range []string{"no arrow at all", "a.txt -> b -> c", "src/ -> lib", "a.txt -> a.txt"} {
		_, stderr, code := runSafegit(t, dir, "mv", "-m", "move", pair)
		if code != exitcode.Usage {
			t.Errorf("mv %q exited %d, want %d (Usage): %s", pair, code, exitcode.Usage, stderr)
		}
	}
	assertNoCommitHappened(t, dir, "seed")
}

func TestMvRefusesPairsThatSpeakForEachOther(t *testing.T) {
	dir := mvSeed(t)

	// Nested sources: two different fates for src/one.txt.
	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move", "src/ -> lib/", "src/one.txt -> other.txt")
	if code != exitcode.Usage {
		t.Fatalf("nested pairs exited %d, want %d: %s", code, exitcode.Usage, stderr)
	}

	// One path as both a destination and a source: the result would depend on
	// the order the pairs happened to be performed in.
	_, stderr, code = runSafegit(t, dir, "mv", "-m", "move", "a.txt -> c.txt", "c.txt -> d.txt")
	if code != exitcode.Usage {
		t.Fatalf("chained pairs exited %d, want %d: %s", code, exitcode.Usage, stderr)
	}
	assertNoCommitHappened(t, dir, "seed")
}

// The nesting rule applies WITHIN one pair as well as between two. A pair whose
// own two paths nest -- a directory moving into itself, a file moving onto the
// directory that holds it -- is the same contradiction the between-pairs
// refusal names, stated by one argument instead of two, so it is refused where
// that one is: at validation, as Usage, before any filesystem mutation.
//
// Before this rule the argument checks let it through and os.Rename refused it
// with EINVAL, which rolled back and exited General -- a repository verdict for
// an argument that never made sense.
func TestMvRefusesAPairWhoseOwnPathsNest(t *testing.T) {
	dir := mvSeed(t)

	for _, pair := range []string{
		"src/ -> src/sub/",     // a directory into itself
		"src/deep/ -> src/",    // a directory onto its own ancestor
		"src/one.txt -> src",   // a file onto the directory that holds it
		"a.txt -> a.txt/inner", // a file onto a path underneath itself
	} {
		_, stderr, code := runSafegit(t, dir, "mv", "-m", "move", pair)
		if code != exitcode.Usage {
			t.Errorf("mv %q exited %d, want %d (Usage): %s", pair, code, exitcode.Usage, stderr)
		}
		if !strings.Contains(stderr, "nest") {
			t.Errorf("mv %q refusal does not name the nesting: %s", pair, stderr)
		}
	}

	// Nothing was moved and nothing was committed: the refusal is an argument
	// verdict reached before the first rename.
	for _, rel := range []string{"a.txt", "src/one.txt", "src/deep/two.txt"} {
		if !mvExists(t, dir, rel) {
			t.Errorf("%s left its original path during a refused invocation", rel)
		}
	}
	if mvExists(t, dir, "src/sub") {
		t.Error("a refused invocation created the nested destination")
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestMvRollsBackCompletedRenamesOnAMidSequenceFailure covers the one failure
// validation cannot foresee: the filesystem refusing a rename the repository
// had no objection to. What was already moved goes back.
func TestMvRollsBackCompletedRenamesOnAMidSequenceFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not refuse a rename")
	}
	dir := mvSeed(t)

	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatalf("mkdir locked: %v", err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatalf("chmod locked: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move both",
		"a.txt -> moved-a.txt", "b.txt -> locked/b.txt")
	if code == 0 {
		t.Fatalf("a rename into a read-only directory succeeded: %s", stderr)
	}
	if mvExists(t, dir, "moved-a.txt") {
		t.Error("the completed rename was not rolled back")
	}
	if !mvExists(t, dir, "a.txt") {
		t.Error("the rolled-back file is not at its original path")
	}
	if !mvExists(t, dir, "b.txt") {
		t.Error("the file whose rename failed is not at its original path")
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestMvRefusesWhileGitHasAnOperationInFlight: the commit pipeline's own
// in-flight refusal runs when the commit is BUILT, which for this command is
// after the renames. So the check is taken up front instead -- otherwise a `mv`
// during a conflicted merge would move every file and then refuse to commit
// them, leaving a working tree that is mid-merge and half-moved at once.
func TestMvRefusesWhileGitHasAnOperationInFlight(t *testing.T) {
	dir := mvSeed(t)

	// A conflicted merge, left in flight.
	testutil.Git(t, dir, "checkout", "-q", "-b", "side")
	testutil.WriteFile(t, dir, "b.txt", "side\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "side edit", "--", "b.txt"); code != 0 {
		t.Fatalf("side commit failed (code %d): %s", code, stderr)
	}
	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "b.txt", "main\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "main edit", "--", "b.txt"); code != 0 {
		t.Fatalf("main commit failed (code %d): %s", code, stderr)
	}
	runSafegit(t, dir, "merge", "side")

	_, stderr, code := runSafegit(t, dir, "mv", "-m", "move during a merge", "a.txt -> x.txt")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("exit %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
	}
	if !mvExists(t, dir, "a.txt") || mvExists(t, dir, "x.txt") {
		t.Error("the refused mv moved a file anyway")
	}
}

func TestMvDryRunRecordsTheRenamesAndPerformsNothing(t *testing.T) {
	dir := mvSeed(t)

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "mv", "-m", "move a", "a.txt -> x.txt")
	if code != 0 {
		t.Fatalf("dry-run mv failed (code %d): %s", code, stderr)
	}

	log := wouldDoLog(stdout)
	if log == "" {
		t.Fatalf("a dry run must render the would-do log, got:\n%s", stdout)
	}
	if !strings.Contains(log, "rename:") {
		t.Errorf("the would-do log must record the rename, got: %s", log)
	}
	if !strings.Contains(log, "update-ref refs/heads/main <new-commit>") {
		t.Errorf("the would-do log must record the commit that would be made, got: %s", log)
	}

	if !mvExists(t, dir, "a.txt") || mvExists(t, dir, "x.txt") {
		t.Error("a dry run moved the file for real")
	}
	assertNoCommitHappened(t, dir, "seed")
}

// TestMvUndoReversesTheCommitAndLeavesTheFilesMoved pins undo's contract at the
// one command where it is most surprising: undo moves a ref, and it has never
// touched the working tree. The files stay where mv put them, and the output
// says so.
func TestMvUndoReversesTheCommitAndLeavesTheFilesMoved(t *testing.T) {
	dir := mvSeed(t)
	const session = "CLAUDE_CODE_SESSION_ID=mv-undo-session"

	if _, stderr, code := runSafegitEnv(t, dir, []string{session},
		"mv", "-m", "move a", "a.txt -> x.txt"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, []string{session}, "undo")
	if code != 0 {
		t.Fatalf("undo failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "undid mv") {
		t.Errorf("undo did not name the operation it reversed: %s", stdout)
	}
	assertNoCommitHappened(t, dir, "seed")

	// The commit is gone; the rename is not.
	if mvExists(t, dir, "a.txt") || !mvExists(t, dir, "x.txt") {
		t.Error("undo moved the files back, which it has no record to do")
	}
	if !strings.Contains(stderr, "still at") && !strings.Contains(stderr, "working tree") {
		t.Errorf("undo must say the files were not moved back, got: %s", stderr)
	}
}

// TestMvRenamesOnlyTheCase covers the pair a case-insensitive filesystem cannot
// perform directly. The two-step path is taken from core.ignorecase, which is
// set here deliberately so the branch is exercised wherever the suite runs.
func TestMvRenamesOnlyTheCase(t *testing.T) {
	for _, ignorecase := range []string{"false", "true"} {
		t.Run("ignorecase="+ignorecase, func(t *testing.T) {
			dir := mvSeed(t)
			if ignorecase == "false" && mvFoldsCase(t, dir) {
				// The key would be a lie here, and the destination check it
				// selects would then see the source itself sitting at the
				// destination. A repository that declares core.ignorecase=false
				// on a filesystem that folds case is misconfigured, and safegit
				// takes the repository's own word for which world it is in.
				t.Skip("the test filesystem folds case, so core.ignorecase=false does not describe it")
			}
			testutil.Git(t, dir, "config", "core.ignorecase", ignorecase)

			if _, stderr, code := runSafegit(t, dir, "mv", "-m", "capitalize", "a.txt -> A.txt"); code != 0 {
				t.Fatalf("case-only mv failed (code %d): %s", code, stderr)
			}

			records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
			if len(records) != 1 || records[0][1] != "a.txt -> A.txt" {
				t.Fatalf("records are %v", records)
			}
			tree := testutil.Git(t, dir, "ls-tree", "--name-only", "HEAD")
			if !strings.Contains(tree, "A.txt") || strings.Contains(tree, "a.txt") {
				t.Errorf("the tree still holds the old spelling:\n%s", tree)
			}
			// No temporary name survives the two-step rename.
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("reading the working tree: %v", err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			joined := strings.Join(names, " ")
			if !strings.Contains(joined, "A.txt") {
				t.Errorf("the working tree does not hold A.txt: %s", joined)
			}
			if strings.Contains(joined, "safegit-mv-") {
				t.Errorf("a temporary rename name was left behind: %s", joined)
			}
		})
	}
}

// TestMvOnACaseInsensitiveFilesystem is the same move on a filesystem that
// really does fold case, which is the condition the two-step rename exists for.
// It is skipped where such a filesystem is not available to the suite.
func TestMvOnACaseInsensitiveFilesystem(t *testing.T) {
	dir := mvSeed(t)
	if !mvFoldsCase(t, dir) {
		t.Skip("the test filesystem is case-sensitive; no case-insensitive fixture is available")
	}
	testutil.Git(t, dir, "config", "core.ignorecase", "true")

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "capitalize", "a.txt -> A.txt"); code != 0 {
		t.Fatalf("case-only mv failed (code %d): %s", code, stderr)
	}
	tree := testutil.Git(t, dir, "ls-tree", "--name-only", "HEAD")
	if !strings.Contains(tree, "A.txt") || strings.Contains(tree, "a.txt") {
		t.Errorf("the tree still holds the old spelling:\n%s", tree)
	}
}

// TestMvCaseOnlyRefusesAnOccupiedDestination is the destination check on the one
// pair that was exempt from it wholesale.
//
// The exemption exists because a case-only rename's destination LOOKS occupied
// by its own source -- but only where the filesystem folds case, which is where
// the two spellings really are one file. On a case-SENSITIVE filesystem they are
// two files, and skipping the check let os.Rename overwrite an untracked file
// holding content that exists nowhere else. The tree check below the disk check
// never covered this: it only sees paths tracked in HEAD.
func TestMvCaseOnlyRefusesAnOccupiedDestination(t *testing.T) {
	dir := mvSeed(t)
	if mvFoldsCase(t, dir) {
		t.Skip("the test filesystem folds case, so a.txt and A.txt are one file and there is no separate destination to occupy")
	}
	testutil.Git(t, dir, "config", "core.ignorecase", "false")

	const untracked = "work in progress, committed nowhere\n"
	if err := os.WriteFile(filepath.Join(dir, "A.txt"), []byte(untracked), 0o644); err != nil {
		t.Fatal(err)
	}
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "mv", "-m", "capitalize", "a.txt -> A.txt")
	if code != exitcode.MoveNotBorneOut {
		t.Fatalf("case-only mv onto an occupied destination: exit %d, want %d\nstdout: %s\nstderr: %s",
			code, exitcode.MoveNotBorneOut, stdout, stderr)
	}
	if !strings.Contains(stderr, "A.txt") {
		t.Errorf("the refusal should name the destination; stderr: %s", stderr)
	}

	got, err := os.ReadFile(filepath.Join(dir, "A.txt"))
	if err != nil || string(got) != untracked {
		t.Errorf("the untracked destination was destroyed: %q (err %v)", got, err)
	}
	if !mvExists(t, dir, "a.txt") {
		t.Error("the source was moved despite the refusal")
	}
	if after := testutil.Rev(t, dir, "HEAD"); after != before {
		t.Errorf("a commit was made despite the refusal: %s -> %s", before, after)
	}
}

// TestMvCreatesTheDestinationDirectory: a destination inside a directory that
// does not exist yet is an ordinary move, not a refusal. The directories are
// created through the effects handle, so a preview records them too, and a
// rollback removes the ones this invocation created.
func TestMvCreatesTheDestinationDirectory(t *testing.T) {
	dir := mvSeed(t)

	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move deep", "a.txt -> new/deeper/a.txt"); code != 0 {
		t.Fatalf("mv into a missing directory failed (code %d): %s", code, stderr)
	}
	if !mvExists(t, dir, "new/deeper/a.txt") {
		t.Error("the destination was not created")
	}

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "mv", "-m", "move deep", "b.txt -> other/b.txt")
	if code != 0 {
		t.Fatalf("dry-run mv failed (code %d): %s", code, stderr)
	}
	if log := wouldDoLog(stdout); !strings.Contains(log, "mkdir:") {
		t.Errorf("the would-do log must record the directory creation, got: %s", log)
	}
	if mvExists(t, dir, "other") {
		t.Error("a dry run created the destination directory for real")
	}
}
