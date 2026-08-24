package test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// `safegit revert` is single-form, and every command line it cannot author is
// refused rather than handed to git.
//
// A plain passthrough revert left two things behind: a commit git authored,
// with none of safegit's trailers, and git's own operation state (REVERT_HEAD,
// AUTO_MERGE, MERGE_MSG), which the commit pipeline reads as an operation in
// flight -- so the NEXT `safegit commit` was refused until somebody deleted
// those files by hand.
//
// The restructured form splits the operation where git splits it: `git revert
// --no-commit` computes the inverse patch and stages it, and the conclusion
// engine commits it. There is no second arm any more: the passthrough that used
// to catch everything the restructure could not honor is gone, so a command
// line outside the subset meets a refusal that names what is absent. The tests
// below assert both halves, plus the two forms that author nothing and
// therefore stay plain passthroughs.

// revertSession is the handshake these tests spawn safegit with, so the session
// trailer is observable at all.
var revertSession = []string{"CLAUDE_CODE_SESSION_ID=revert-restructure-test"}

// newRevertRepo builds a repository with a commit worth reverting, authored by
// somebody else so that author preservation is observable.
func newRevertRepo(t *testing.T) (dir, target string) {
	t.Helper()
	dir = newRepo(t)

	otherEnv := append([]string{
		"GIT_AUTHOR_NAME=Original Author",
		"GIT_AUTHOR_EMAIL=original@example.com",
	}, revertSession...)

	testutil.WriteFile(t, dir, "r.txt", "before\n")
	safegitCommitEnv(t, dir, revertSession, "base", "r.txt")
	testutil.WriteFile(t, dir, "r.txt", "the change to undo\n")
	target = safegitCommitEnv(t, dir, otherEnv, "the change to undo", "r.txt")
	return dir, target
}

// TestSingleCommitRevertIsPipelineAuthored is the headline of the restructure:
// safegit writes the commit, so it carries safegit's trailers; git's state
// files are gone; and the next safegit commit is not refused.
func TestSingleCommitRevertIsPipelineAuthored(t *testing.T) {
	dir, target := newRevertRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, revertSession, "revert", "--no-edit", target)
	if code != exitcode.OK {
		t.Fatalf("safegit revert failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}

	// safegit's own trailer: the commit went through the pipeline, not through
	// git's commit path.
	msg := commitMessage(t, dir, "HEAD")
	if !strings.Contains(msg, "Claude-Code-Session-Id: revert-restructure-test") {
		t.Errorf("the revert commit carries no safegit trailer, so git authored it:\n%s", msg)
	}
	// git's own draft is still the message.
	if !strings.Contains(msg, `Revert "the change to undo"`) {
		t.Errorf("the revert commit does not carry git's message draft:\n%s", msg)
	}
	// The identity is the OPERATOR's, on both fields: a revert is a new change
	// of the reverter's own, which is what git's own revert records. The
	// identity of the commit being reverted appears on neither field.
	identity := strings.TrimSpace(testutil.GitOut(t, dir, "log", "-1", "--format=%an <%ae>|%cn <%ce>"))
	author, committer, _ := strings.Cut(identity, "|")
	if strings.Contains(author, "original@example.com") {
		t.Errorf("author = %q; a revert must not record the identity of the commit it undoes", author)
	}
	if author != committer {
		t.Errorf("author = %q, committer = %q; a revert records the operator as both", author, committer)
	}
	// And the revert actually reverted.
	if got := testutil.MustShow(t, dir, "HEAD", "r.txt"); got != "before\n" {
		t.Errorf("HEAD:r.txt = %q, want the content from before the reverted commit", got)
	}

	assertNoSequencerResidue(t, dir, "a single-commit safegit revert")

	// The practical consequence, and the reason the residue matters: the next
	// commit is not refused.
	testutil.WriteFile(t, dir, "after.txt", "after\n")
	if _, stderr, code := runSafegitEnv(t, dir, revertSession,
		"commit", "-m", "after the revert", "--", "after.txt"); code != 0 {
		t.Fatalf("a commit after the revert was refused (code %d): %s", code, stderr)
	}
}

// TestConflictedSingleRevertComposesWithRevertContinue: the compute step is
// allowed to fail. When it does, the operator gets the ordinary conflicted
// revert -- git's own state, git's own exit code -- and revert-continue
// concludes it through the same engine the clean path used.
func TestConflictedSingleRevertComposesWithRevertContinue(t *testing.T) {
	dir, target := newRevertRepo(t)
	// A later edit over the same line is what makes the inverse patch conflict.
	testutil.WriteFile(t, dir, "r.txt", "a later edit\n")
	safegitCommitEnv(t, dir, revertSession, "a later edit", "r.txt")
	tip := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegitEnv(t, dir, revertSession, "revert", "--no-edit", target)
	if code == 0 {
		t.Fatalf("the fixture needs a conflicted revert: %s", stderr)
	}
	if !strings.Contains(stderr, "safegit revert-continue") {
		t.Errorf("the conflicted revert does not name the command that concludes it:\n%s", stderr)
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "REVERT_HEAD")) {
		t.Fatal("the conflicted revert left no REVERT_HEAD: the state is not git's ordinary one")
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Errorf("HEAD moved despite the conflict")
	}

	stdout, stderr, code := runSafegitEnv(t, dir, revertSession,
		"revert-continue", "--resolve", "r.txt=theirs")
	if code != exitcode.OK {
		t.Fatalf("revert-continue of the conflicted revert failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if got := testutil.MustShow(t, dir, "HEAD", "r.txt"); got != "before\n" {
		t.Errorf("HEAD:r.txt = %q, want the reverted content", got)
	}
	assertNoSequencerResidue(t, dir, "revert-continue after a conflicted safegit revert")
}

// TestSingleRevertWritesExactlyOneOplogEntry: one operation, one entry, under
// the COMMAND's own op name.
//
// The restructure used to write two: the compute step appended an entry of its
// own naming the argv, and the conclusion's pipeline appended a second under
// the conclusion command's name. The first recorded no commit and could not be
// undone, so the audit trail carried an operation that was really half of
// another one.
func TestSingleRevertWritesExactlyOneOplogEntry(t *testing.T) {
	dir, target := newRevertRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, dir, revertSession, "revert", "--no-edit", target); code != 0 {
		t.Fatalf("safegit revert failed (code %d): %s", code, stderr)
	}
	head := testutil.Rev(t, dir, "HEAD")

	entries := oplogEntries(t, dir, "revert")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one revert oplog entry, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if got, _ := oplogExtraString(extra, "ref"); got != "refs/heads/main" {
		t.Errorf("the revert entry records ref %q, want refs/heads/main; extra=%v", got, extra)
	}
	if got, _ := oplogExtraString(extra, "parent"); got != tip {
		t.Errorf("the revert entry records old tip %q, want %q; extra=%v", got, tip, extra)
	}
	if got, _ := oplogExtraString(extra, "sha", "to", "result"); got != head {
		t.Errorf("the revert entry records new tip %q, want %q; extra=%v", got, head, extra)
	}
	if n := len(oplogEntries(t, dir, "revert-continue")); n != 0 {
		t.Errorf("the revert recorded %d revert-continue entries; the command records under its OWN name", n)
	}

	// And the one entry is the undoable one.
	if _, stderr, code := runSafegitEnv(t, dir, revertSession, "undo"); code != 0 {
		t.Fatalf("undo of a pipeline-authored revert failed (code %d): %s", code, stderr)
	}
	if back := testutil.Rev(t, dir, "HEAD"); back != tip {
		t.Errorf("undo left HEAD at %s, want the pre-revert tip %s", back, tip)
	}
}

// TestSingleRevertCarriesAPayload: `revert` declares a payload schema of its
// own, so a machine-mode run says what it did rather than emitting a null
// payload beside a commit it made.
//
// The members are the conclusion payload's minus the resolution and queue
// members -- a revert safegit itself started has neither -- plus the reverted
// commit, which nothing else in the document names.
func TestSingleRevertCarriesAPayload(t *testing.T) {
	dir, target := newRevertRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, revertSession, "--json", "revert", "--no-edit", target)
	if code != exitcode.OK {
		t.Fatalf("safegit --json revert failed (code %d): %s", code, stderr)
	}
	var payload struct {
		Operation      string   `json:"operation"`
		Source         string   `json:"source"`
		Ref            string   `json:"ref"`
		SHA            *string  `json:"sha"`
		Parents        []string `json:"parents"`
		Tree           string   `json:"tree"`
		Files          []string `json:"files"`
		StateCleared   bool     `json:"state_cleared"`
		DeclinedChecks []struct {
			Check string `json:"check"`
		} `json:"declined_checks"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, stdout).Payload, &payload); err != nil {
		t.Fatalf("the revert payload does not parse: %v\nstdout=%s", err, stdout)
	}
	if payload.Operation != "revert" {
		t.Errorf("payload operation = %q, want revert", payload.Operation)
	}
	if payload.Source != target {
		t.Errorf("payload source = %q, want the reverted commit %s", payload.Source, target)
	}
	if payload.SHA == nil || *payload.SHA != testutil.Rev(t, dir, "HEAD") {
		t.Errorf("payload sha = %v, want the commit that was created", payload.SHA)
	}
	if payload.Ref != "refs/heads/main" {
		t.Errorf("payload ref = %q, want refs/heads/main", payload.Ref)
	}
	if !payload.StateCleared {
		t.Error("payload state_cleared = false, but the revert's state files were removed")
	}
	if !testutil.Contains(payload.Files, "r.txt") {
		t.Errorf("payload files = %v, want the reverted path", payload.Files)
	}
	if payload.DeclinedChecks == nil {
		t.Error("payload declined_checks is null; an empty list is the answer when nothing was declined")
	}
}

// TestRevertRefusesRawGitShapes pins the subset boundary. The git-authored
// passthrough arm is gone, so a command line safegit cannot author is a refusal
// naming the capability that is absent -- never a quiet handover to git.
func TestRevertRefusesRawGitShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(target string) []string
		says string
	}{
		{"two commits", func(tg string) []string { return []string{"revert", "--no-edit", "HEAD", "HEAD~1"} }, "one commit"},
		{"two-dot range", func(tg string) []string { return []string{"revert", "--no-edit", "HEAD~2..HEAD"} }, "range"},
		{"three-dot range", func(tg string) []string { return []string{"revert", "--no-edit", "HEAD~2...HEAD"} }, "range"},
		{"exclusion", func(tg string) []string { return []string{"revert", "--no-edit", "HEAD", "^HEAD~2"} }, "range"},
		{"skip", func(tg string) []string { return []string{"revert", "--skip"} }, "--skip"},
		{"edit", func(tg string) []string { return []string{"revert", "--edit", tg} }, "--edit"},
		{"gpg sign", func(tg string) []string { return []string{"revert", "--no-edit", "-S", tg} }, "sign"},
		{"commit override", func(tg string) []string { return []string{"revert", "--no-edit", "--commit", tg} }, "--commit"},
		{"cleanup", func(tg string) []string { return []string{"revert", "--no-edit", "--cleanup", "verbatim", tg} }, "--cleanup"},
		{"no commit named", func(tg string) []string { return []string{"revert", "--no-edit"} }, "names no commit"},
		{"pathspec", func(tg string) []string { return []string{"revert", "--no-edit", tg, "--", "r.txt"} }, "pathspec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, target := newRevertRepo(t)
			// A third commit, so HEAD~2 resolves in the range rows.
			testutil.WriteFile(t, dir, "extra.txt", "extra\n")
			safegitCommitEnv(t, dir, revertSession, "extra", "extra.txt")
			tip := testutil.Rev(t, dir, "HEAD")

			args := tc.args(target)
			stdout, stderr, code := runSafegitEnv(t, dir, revertSession, args...)
			if code != exitcode.Usage {
				t.Fatalf("safegit %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
					strings.Join(args, " "), code, exitcode.Usage, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("the refusal does not say %q:\n%s", tc.says, stderr)
			}
			if head := testutil.Rev(t, dir, "HEAD"); head != tip {
				t.Errorf("the refused revert moved HEAD to %s (was %s)", head, tip)
			}
			assertNoSequencerResidue(t, dir, "refused revert")
			if n := len(oplogEntries(t, dir, "revert")); n != 0 {
				t.Errorf("a refused command line recorded %d oplog entries; nothing happened to the repository", n)
			}
		})
	}
}

// TestRevertNoCommitStaysAPassthrough: `--no-commit` authors nothing, so it is
// forwarded to git unchanged -- the staged inverse patch and the state files
// are there for the operator to use, exactly as they were before the
// restructure existed.
func TestRevertNoCommitStaysAPassthrough(t *testing.T) {
	dir, target := newRevertRepo(t)
	tip := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, dir, revertSession,
		"revert", "--no-commit", "--no-edit", target); code != 0 {
		t.Fatalf("revert --no-commit failed (code %d): %s", code, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != tip {
		t.Error("--no-commit committed anyway: safegit must not claim this form")
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "REVERT_HEAD")) {
		t.Error("--no-commit left no REVERT_HEAD, so its staged result cannot be concluded")
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); !strings.Contains(status, "r.txt") {
		t.Errorf("--no-commit staged nothing:\n%s", status)
	}
}

// TestRevertPreviewRecordsTheArgvItWouldRun: the would-do log lists the
// mutations the execute path performs, and the execute path computes the revert
// with `--no-commit`. A preview that recorded a bare `git revert` would be
// describing the operation safegit stopped performing.
func TestRevertPreviewRecordsTheArgvItWouldRun(t *testing.T) {
	dir, target := newRevertRepo(t)

	stdout, stderr, code := runSafegitEnv(t, dir, revertSession, "--dry-run", "revert", "--no-edit", target)
	if code != 0 {
		t.Fatalf("revert --dry-run failed (code %d): %s", code, stderr)
	}
	log := wouldDoLog(stdout)
	if !strings.Contains(log, "revert --no-commit") {
		t.Errorf("the would-do log does not record the argv the run would use:\n%s", log)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head == "" {
		t.Error("the preview left no readable HEAD")
	}
}

// TestMergeContinuePassthroughNamesMergeContinue: `safegit merge --continue`
// is refused and names safegit's own command.
//
// Both halves are asserted because they are refused by different code. A
// repository mid-conflict is DIRTY, so the working-tree guard refuses it; a
// merge whose result equals the current tip is CLEAN, and only the
// operation-specific refusal catches that one. Whether a conclusion is
// safegit's or git's must not depend on whether the merge changed a file.
func TestMergeContinuePassthroughNamesMergeContinue(t *testing.T) {
	// A merge fixture whose conflict resolution can be made either different
	// from the tip or identical to it.
	fixture := func(t *testing.T) string {
		t.Helper()
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
		safegitCommitEnv(t, dir, revertSession, "base", "c.txt")
		testutil.Git(t, dir, "branch", "side")
		testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
		safegitCommitEnv(t, dir, revertSession, "main", "c.txt")
		testutil.Git(t, dir, "switch", "side")
		testutil.WriteFile(t, dir, "c.txt", "l1\nside\nl3\n")
		safegitCommitEnv(t, dir, revertSession, "side", "c.txt")
		testutil.Git(t, dir, "switch", "main")
		if _, stderr, code := runSafegitEnv(t, dir, revertSession, "merge", "side"); code == 0 {
			t.Fatalf("the fixture needs a conflicted merge: %s", stderr)
		}
		return dir
	}

	// GIT_EDITOR is set on purpose. Without it git's own `merge --continue`
	// fails on the test environment's dumb terminal, which would make this test
	// pass whether or not safegit refused anything -- the failure it asserts
	// has to be safegit's refusal, not git's missing editor.
	env := append([]string{"GIT_EDITOR=true"}, revertSession...)

	t.Run("with the conflict unresolved", func(t *testing.T) {
		dir := fixture(t)
		_, stderr, code := runSafegitEnv(t, dir, env, "merge", "--continue")
		if code != exitcode.CoordinationBusy {
			t.Fatalf("exit %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
		}
		if !strings.Contains(stderr, "safegit merge-continue") {
			t.Errorf("the refusal does not name safegit merge-continue:\n%s", stderr)
		}
	})

	t.Run("with the merge resolved to the current tip", func(t *testing.T) {
		dir := fixture(t)
		tip := testutil.Rev(t, dir, "HEAD")
		// Resolving every path to the branch's own content leaves nothing for
		// `git diff HEAD` to report, so the dirty-tree guard sees a clean tree.
		testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
		testutil.Git(t, dir, "update-index", "--add", "c.txt")
		if diff := testutil.Git(t, dir, "diff", "HEAD", "--name-status"); strings.TrimSpace(diff) != "" {
			t.Fatalf("the fixture must present a clean tree to the guard:\n%s", diff)
		}

		_, stderr, code := runSafegitEnv(t, dir, env, "merge", "--continue")
		if code == 0 {
			t.Fatalf("git authored the merge commit: safegit must own its own conclusions:\n%s", stderr)
		}
		// The operation-specific refusal, not the working-tree guard's: this
		// tree is clean, so only the former can have produced it.
		if !strings.Contains(stderr, "does not conclude") {
			t.Errorf("the refusal is not the operation-specific one:\n%s", stderr)
		}
		if !strings.Contains(stderr, "safegit merge-continue") {
			t.Errorf("the refusal does not name safegit merge-continue:\n%s", stderr)
		}
		if head := testutil.Rev(t, dir, "HEAD"); head != tip {
			t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, tip)
		}
		if !testutil.FileExists(filepath.Join(dir, ".git", "MERGE_HEAD")) {
			t.Error("the refusal destroyed the merge it declined to conclude")
		}

		// And safegit's own command does conclude it: the path was resolved in
		// the index already, so there is nothing left to declare, and an empty
		// merge needs no flag. The way out the refusal names actually works
		// from here.
		if _, stderr, code := runSafegitEnv(t, dir, revertSession, "merge-continue"); code != 0 {
			t.Fatalf("the command the refusal named failed (code %d): %s", code, stderr)
		}
		if parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD")); len(parents) != 2 {
			t.Errorf("the conclusion produced %d parent(s), want a merge commit's two", len(parents))
		}
	})
}

// TestPickAndRevertContinuePassthroughsNameSafegitsCommand: the same refusal
// for the other two operations safegit concludes, including a QUEUED sequence,
// which safegit now concludes too (by delegation).
func TestPickAndRevertContinuePassthroughsNameSafegitsCommand(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			fx := newQueuedPickRepo(t, verb)
			_, stderr, code := runSafegitEnv(t, fx.dir, revertSession, verb, "--continue")
			if code != exitcode.CoordinationBusy {
				t.Fatalf("exit %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
			}
			// The operation-specific refusal fires before the working-tree
			// guard, so its own wording is what an operator sees even though a
			// mid-conflict tree is dirty too.
			if !strings.Contains(stderr, "does not conclude") {
				t.Errorf("the refusal is not the operation-specific one:\n%s", stderr)
			}
			if !strings.Contains(stderr, "safegit "+verb+"-continue") {
				t.Errorf("the refusal does not name safegit %s-continue:\n%s", verb, stderr)
			}
		})
	}
}

// TestRebaseContinuePassthroughStillNamesGit is the control for the refusal
// above: the new refusal is keyed on WHO OWNS the conclusion, read from the
// single way-out authority, not on the word "--continue". safegit has no rebase
// conclusion, so nothing here may ever claim one.
//
// What a mid-rebase `safegit rebase --continue` gets today is the working-tree
// guard's refusal, which names `git rebase --continue` -- pre-existing
// behavior, and the reason this test asserts the message rather than success:
// the rebase conclusion is a declared non-goal of this campaign, and the only
// property being pinned is that safegit does not start claiming it.
func TestRebaseContinuePassthroughStillNamesGit(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
	safegitCommitEnv(t, dir, revertSession, "base", "c.txt")
	testutil.Git(t, dir, "branch", "topic")
	testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
	safegitCommitEnv(t, dir, revertSession, "main", "c.txt")
	testutil.Git(t, dir, "switch", "topic")
	testutil.WriteFile(t, dir, "c.txt", "l1\ntopic\nl3\n")
	safegitCommitEnv(t, dir, revertSession, "topic", "c.txt")

	if _, stderr, code := runSafegitEnv(t, dir, revertSession, "rebase", "main"); code == 0 {
		t.Fatalf("the fixture needs a conflicted rebase: %s", stderr)
	}
	testutil.WriteFile(t, dir, "c.txt", "l1\nresolved\nl3\n")
	testutil.Git(t, dir, "update-index", "--add", "c.txt")

	env := append([]string{"GIT_EDITOR=true"}, revertSession...)
	_, stderr, _ := runSafegitEnv(t, dir, env, "rebase", "--continue")
	if strings.Contains(stderr, "safegit does") || strings.Contains(stderr, "safegit rebase-continue") {
		t.Errorf("safegit claimed a rebase conclusion it does not have:\n%s", stderr)
	}
	if stderr != "" && !strings.Contains(stderr, "git rebase --continue") {
		t.Errorf("the refusal does not name git's own rebase conclusion:\n%s", stderr)
	}
}
