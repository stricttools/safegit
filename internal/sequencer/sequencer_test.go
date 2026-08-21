package sequencer_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/safegit/internal/testutil"
)

func TestReadReportsNoneInAQuietRepository(t *testing.T) {
	dir := newRepo(t)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindNone {
		t.Fatalf("Kind = %v, want %v", got.Kind, sequencer.KindNone)
	}
	if got.InProgress() {
		t.Fatal("InProgress is true with nothing in flight")
	}
	if got.MergeHeads != nil || got.Source != "" || got.Queued || got.MessageFile != "" {
		t.Fatalf("a none state carries data: %+v", got)
	}
}

func TestReadReportsMerge(t *testing.T) {
	dir, merged := conflictedMerge(t)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindMerge {
		t.Fatalf("Kind = %v, want merge", got.Kind)
	}
	if len(got.MergeHeads) != 1 || got.MergeHeads[0] != merged {
		t.Fatalf("MergeHeads = %v, want [%s]", got.MergeHeads, merged)
	}
	if got.MessageFile != filepath.Join(gitDir(dir), "MERGE_MSG") {
		t.Fatalf("MessageFile = %q, want the MERGE_MSG path", got.MessageFile)
	}
	body, err := os.ReadFile(got.MessageFile)
	if err != nil {
		t.Fatalf("reading the reported message file: %v", err)
	}
	if !strings.Contains(string(body), "Merge branch 'side'") {
		t.Fatalf("the reported message file does not hold git's merge message: %q", body)
	}
	if got.Queued {
		t.Fatal("a merge reported Queued")
	}
	if got.Backend != sequencer.BackendNone {
		t.Fatalf("Backend = %v on a merge", got.Backend)
	}
}

// An octopus merge is the case a single-line reader gets wrong: MERGE_HEAD
// carries one object name per merged branch, and a conclusion that reads only
// the first would drop parents silently.
func TestReadReportsEveryOctopusMergeHead(t *testing.T) {
	dir, heads := octopusMerge(t)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindMerge {
		t.Fatalf("Kind = %v, want merge", got.Kind)
	}
	if len(got.MergeHeads) != len(heads) {
		t.Fatalf("MergeHeads = %v (%d entries), want the %d merged commits %v",
			got.MergeHeads, len(got.MergeHeads), len(heads), heads)
	}
	for i, want := range heads {
		if got.MergeHeads[i] != want {
			t.Fatalf("MergeHeads[%d] = %s, want %s (order must follow the file)", i, got.MergeHeads[i], want)
		}
	}
	// The file really does carry several lines: a fixture that produced a
	// two-parent merge would make the assertion above meaningless.
	raw, err := os.ReadFile(filepath.Join(gitDir(dir), "MERGE_HEAD"))
	if err != nil {
		t.Fatalf("reading MERGE_HEAD: %v", err)
	}
	if lines := testutil.SplitLines(string(raw)); len(lines) != 3 {
		t.Fatalf("the fixture wrote %d MERGE_HEAD lines, want 3", len(lines))
	}
	if !strings.Contains(got.String(), "octopus") {
		t.Fatalf("String() = %q, want it to name the octopus merge", got.String())
	}
}

// The discriminator, from the side that must NOT be reported as queued.
func TestReadReportsSingleCherryPickAsNotQueued(t *testing.T) {
	dir, picked := singleCherryPick(t)

	// The shape the discriminator rests on: the head and the auto-merge
	// result, and no sequencer directory at all.
	mustBePresent(t, gitDir(dir), sequencer.FileCherryPickHead, sequencer.FileAutoMerge)
	mustBeAbsent(t, gitDir(dir), sequencer.DirSequencer)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindCherryPick {
		t.Fatalf("Kind = %v, want cherry-pick", got.Kind)
	}
	if got.Source != picked {
		t.Fatalf("Source = %s, want the picked commit %s", got.Source, picked)
	}
	if got.Queued {
		t.Fatal("a single cherry-pick was reported as queued")
	}
	if got.MessageFile != filepath.Join(gitDir(dir), "MERGE_MSG") {
		t.Fatalf("MessageFile = %q, want the MERGE_MSG path", got.MessageFile)
	}
}

// The discriminator, from the side that must be reported as queued: the same
// CHERRY_PICK_HEAD and AUTO_MERGE as a single pick, plus git's sequencer queue.
func TestReadReportsQueuedCherryPickAsQueued(t *testing.T) {
	dir, stopped := queuedCherryPick(t)

	mustBePresent(t, gitDir(dir),
		sequencer.FileCherryPickHead, sequencer.FileAutoMerge, sequencer.DirSequencer)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindCherryPick {
		t.Fatalf("Kind = %v, want cherry-pick", got.Kind)
	}
	if got.Source != stopped {
		t.Fatalf("Source = %s, want the commit the sequence stopped on %s", got.Source, stopped)
	}
	if !got.Queued {
		t.Fatal("a multi-commit cherry-pick was not reported as queued")
	}
	if !strings.Contains(got.String(), "sequence") {
		t.Fatalf("String() = %q, want it to say the pick is a sequence", got.String())
	}
}

// The queue exists from the start of the sequence, not from its first success:
// stopping on the very first commit must still read as queued.
func TestReadReportsQueuedCherryPickStoppedOnItsFirstCommit(t *testing.T) {
	dir, stopped := queuedCherryPickStoppingOnFirst(t)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindCherryPick || !got.Queued {
		t.Fatalf("Kind = %v Queued = %v, want a queued cherry-pick", got.Kind, got.Queued)
	}
	if got.Source != stopped {
		t.Fatalf("Source = %s, want %s", got.Source, stopped)
	}
}

// A sequence whose current step the operator concluded by hand leaves the queue
// but no CHERRY_PICK_HEAD. git still considers the cherry-pick in progress, and
// so must the reader -- with no source commit to name.
func TestReadReportsAQueueWithNoCurrentStep(t *testing.T) {
	dir := queuedCherryPickWithMoreToDo(t)
	testutil.WriteFile(t, dir, "f.txt", "resolved\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "resolved by hand")

	mustBeAbsent(t, gitDir(dir), sequencer.FileCherryPickHead)
	mustBePresent(t, gitDir(dir), sequencer.DirSequencer)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindCherryPick {
		t.Fatalf("Kind = %v, want cherry-pick (the queue's todo says so)", got.Kind)
	}
	if !got.Queued {
		t.Fatal("Queued is false with a sequencer queue on disk")
	}
	if got.Source != "" {
		t.Fatalf("Source = %s, want empty: git is stopped on no commit", got.Source)
	}
	if !got.InProgress() {
		t.Fatal("InProgress is false mid-sequence")
	}
}

func TestReadReportsSingleRevert(t *testing.T) {
	dir, reverted := singleRevert(t)

	mustBePresent(t, gitDir(dir), sequencer.FileRevertHead, sequencer.FileAutoMerge)
	mustBeAbsent(t, gitDir(dir), sequencer.DirSequencer, sequencer.FileCherryPickHead)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindRevert {
		t.Fatalf("Kind = %v, want revert", got.Kind)
	}
	if got.Source != reverted {
		t.Fatalf("Source = %s, want the reverted commit %s", got.Source, reverted)
	}
	if got.Queued {
		t.Fatal("a single revert was reported as queued")
	}
}

func TestReadReportsQueuedRevert(t *testing.T) {
	dir, stopped := queuedRevert(t)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindRevert {
		t.Fatalf("Kind = %v, want revert", got.Kind)
	}
	if !got.Queued {
		t.Fatal("a multi-commit revert was not reported as queued")
	}
	if got.Source != stopped {
		t.Fatalf("Source = %s, want %s", got.Source, stopped)
	}
}

func TestReadReportsRebaseUnderTheMergeBackend(t *testing.T) {
	dir, branch := rebaseMergeBackend(t)

	mustBePresent(t, gitDir(dir), sequencer.DirRebaseMerge)
	mustBeAbsent(t, gitDir(dir), sequencer.DirRebaseApply)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindRebase {
		t.Fatalf("Kind = %v, want rebase", got.Kind)
	}
	if got.Backend != sequencer.BackendMerge {
		t.Fatalf("Backend = %v, want merge", got.Backend)
	}
	if got.HeadName != "refs/heads/"+branch {
		t.Fatalf("HeadName = %q, want refs/heads/%s", got.HeadName, branch)
	}
	if got.MessageFile != filepath.Join(gitDir(dir), sequencer.DirRebaseMerge, "message") {
		t.Fatalf("MessageFile = %q, want rebase-merge/message", got.MessageFile)
	}
}

func TestReadReportsRebaseUnderTheApplyBackend(t *testing.T) {
	dir, branch := rebaseApplyBackend(t)

	mustBePresent(t, gitDir(dir), sequencer.DirRebaseApply)
	mustBeAbsent(t, gitDir(dir), sequencer.DirRebaseMerge)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindRebase {
		t.Fatalf("Kind = %v, want rebase", got.Kind)
	}
	if got.Backend != sequencer.BackendApply {
		t.Fatalf("Backend = %v, want apply", got.Backend)
	}
	if got.HeadName != "refs/heads/"+branch {
		t.Fatalf("HeadName = %q, want refs/heads/%s", got.HeadName, branch)
	}
	if got.MessageFile != filepath.Join(gitDir(dir), sequencer.DirRebaseApply, "final-commit") {
		t.Fatalf("MessageFile = %q, want rebase-apply/final-commit", got.MessageFile)
	}
}

// An interactive rebase always runs the merge backend, which is what makes the
// merge backend the one an operator meets by default.
func TestReadReportsInteractiveRebaseUnderTheMergeBackend(t *testing.T) {
	dir := newRepo(t)
	divergeOnFile(t, dir, "side")
	testutil.Git(t, dir, "checkout", "-q", "side")

	out, code := testutil.GitTryEnv(t, dir, []string{"GIT_SEQUENCE_EDITOR=true", "GIT_EDITOR=true"},
		"rebase", "--interactive", "main")
	if code == 0 {
		t.Fatalf("the interactive rebase was expected to conflict but succeeded:\n%s", out)
	}

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindRebase || got.Backend != sequencer.BackendMerge {
		t.Fatalf("Kind = %v Backend = %v, want a merge-backend rebase", got.Kind, got.Backend)
	}
}

// A rebase started from a detached HEAD records the literal "detached HEAD" in
// head-name. That is not a ref, and reporting it as one would hand a caller a
// branch name that does not exist.
func TestReadReportsNoHeadNameForADetachedRebase(t *testing.T) {
	dir := newRepo(t)
	divergeOnFile(t, dir, "side")
	testutil.Git(t, dir, "checkout", "-q", "--detach", "side")
	mustConflict(t, dir, "rebase", "--merge", "main")

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindRebase {
		t.Fatalf("Kind = %v, want rebase", got.Kind)
	}
	if got.HeadName != "" {
		t.Fatalf("HeadName = %q, want empty for a detached rebase", got.HeadName)
	}
}

// `git am` writes into the same rebase-apply directory the apply-backend rebase
// uses. Reporting it as a rebase would name the wrong operation and, through a
// caller's message, the wrong way out of it.
func TestReadTellsAMailboxApplicationApartFromARebase(t *testing.T) {
	dir := mailboxApplication(t)

	mustBePresent(t, gitDir(dir), sequencer.DirRebaseApply)

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindAM {
		t.Fatalf("Kind = %v, want git am", got.Kind)
	}
	if got.Backend != sequencer.BackendApply {
		t.Fatalf("Backend = %v, want apply", got.Backend)
	}
	if got.String() != "a git am" {
		t.Fatalf("String() = %q", got.String())
	}
}

// A rebase and a merge can both be on disk after a crash. The reported answer
// must be the same one every time rather than whichever the filesystem happened
// to return first.
func TestReadPrefersAMergeOverALeftoverRebaseDirectory(t *testing.T) {
	dir, merged := conflictedMerge(t)
	if err := os.MkdirAll(filepath.Join(gitDir(dir), sequencer.DirRebaseMerge), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindMerge {
		t.Fatalf("Kind = %v, want merge (the declared probe order)", got.Kind)
	}
	if len(got.MergeHeads) != 1 || got.MergeHeads[0] != merged {
		t.Fatalf("MergeHeads = %v, want [%s]", got.MergeHeads, merged)
	}
}

// The state files are git's. Content git could not have written means something
// else wrote it, and guessing at it would be worse than saying so.
func TestReadRejectsAMalformedStateFile(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFileAt(t, filepath.Join(gitDir(dir), sequencer.FileMergeHead), "not-a-sha\n")

	_, err := sequencer.Read(gitDir(dir))
	if err == nil {
		t.Fatal("Read accepted a MERGE_HEAD that is not an object name")
	}
	if !strings.Contains(err.Error(), "not an object name") {
		t.Fatalf("error = %v, want it to name the problem", err)
	}
}

func TestReadRejectsAnUnrecognizedSequencerCommand(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFileAt(t, filepath.Join(gitDir(dir), sequencer.DirSequencer, "todo"), "wobble deadbeef\n")

	_, err := sequencer.Read(gitDir(dir))
	if err == nil {
		t.Fatal("Read accepted a sequencer todo with an unknown command")
	}
	if !strings.Contains(err.Error(), "wobble") {
		t.Fatalf("error = %v, want it to quote the command it did not recognize", err)
	}
}

// A sequencer directory with nothing in it is a leftover, not an operation.
func TestReadIgnoresAnEmptySequencerDirectory(t *testing.T) {
	dir := newRepo(t)
	if err := os.MkdirAll(filepath.Join(gitDir(dir), sequencer.DirSequencer), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Kind != sequencer.KindNone {
		t.Fatalf("Kind = %v, want none", got.Kind)
	}
}

// The git directory arrives as a parameter precisely so a linked worktree
// works: git writes these state files into the worktree's own git directory,
// and nothing in the package re-discovers a path.
func TestReadUsesTheGitDirectoryItIsGiven(t *testing.T) {
	dir := newRepo(t)
	divergeOnFile(t, dir, "side")

	linked := filepath.Join(filepath.Dir(dir), "linked")
	testutil.Git(t, dir, "worktree", "add", "-q", linked, "side")
	mustConflict(t, linked, "merge", "main")

	// The main worktree is quiet.
	main, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read(main): %v", err)
	}
	if main.Kind != sequencer.KindNone {
		t.Fatalf("the main worktree reports %v, want none", main.Kind)
	}

	// The linked worktree's git directory is a file pointing at the real one.
	linkedGitDir := strings.TrimSpace(testutil.GitOut(t, linked, "rev-parse", "--absolute-git-dir"))
	got, err := sequencer.Read(linkedGitDir)
	if err != nil {
		t.Fatalf("Read(linked): %v", err)
	}
	if got.Kind != sequencer.KindMerge {
		t.Fatalf("the linked worktree reports %v, want merge", got.Kind)
	}
	if want := testutil.Rev(t, dir, "main"); len(got.MergeHeads) != 1 || got.MergeHeads[0] != want {
		t.Fatalf("MergeHeads = %v, want [%s]", got.MergeHeads, want)
	}
}

func TestSourceAuthorReadsTheIdentityOfTheCommitBeingApplied(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "checkout", "-q", "-b", "side")
	testutil.WriteFile(t, dir, "f.txt", "side\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "side change",
		"--author", "Original Author <original@example.com>")
	side := testutil.Rev(t, dir, "HEAD")
	testutil.Git(t, dir, "checkout", "-q", "main")
	testutil.WriteFile(t, dir, "f.txt", "main\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main change")
	mustConflict(t, dir, "cherry-pick", side)

	state, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	ctx := gitContext(dir)
	author, err := sequencer.SourceAuthor(ctx, state)
	if err != nil {
		t.Fatalf("SourceAuthor: %v", err)
	}
	if author.Name != "Original Author" || author.Email != "original@example.com" {
		t.Fatalf("author = %+v, want the picked commit's own identity", author)
	}
	if author.Date == "" {
		t.Fatal("author date is empty")
	}
}

func TestSourceAuthorRefusesAStateWithNoSourceCommit(t *testing.T) {
	dir, _ := conflictedMerge(t)
	state, err := sequencer.Read(gitDir(dir))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := sequencer.SourceAuthor(gitContext(dir), state); err == nil {
		t.Fatal("SourceAuthor accepted a merge, which has no source commit")
	}
}

// gitContext targets a specific repository, the way every caller of internal/git
// outside the process's own working directory does.
func gitContext(repoDir string) context.Context {
	return git.WithDir(context.Background(), gitDir(repoDir), repoDir)
}
