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

// Concluding a QUEUED cherry-pick or revert.
//
// `git cherry-pick <a> <b>` and `git revert <a> <b>` put a QUEUE in
// .git/sequencer and stop at the first command that conflicts. safegit cannot
// conclude one step of that natively: its conclusion removes the operation's
// whole state-file set, which for a queue includes the queue itself, so the
// remaining commands would be destroyed by the act of concluding the current
// one.
//
// So safegit does its own half and hands the rest to git: the operator declares
// the same --resolve set, safegit runs the same completeness and
// conflict-marker checks, stages the resolutions into a COPY of the shared
// index, and runs `git <verb> --continue` with GIT_INDEX_FILE pointing at that
// copy. git commits what the copy holds, finishes the queue, and cleans up its
// own state; safegit then adopts the copy as the shared index, because the file
// git was handed is what the repository's index now means.
//
// The tests below assert each half of that: the queue really completes, the
// shared index really ends clean, the output really says git authored the
// commits, and the three things safegit cannot honor for a delegated
// conclusion (-m, --trailer, --dry-run) are refused rather than ignored.

// queuedFixture is a repository parked mid-queue: two commands queued, the
// first one conflicted.
type queuedFixture struct {
	dir string
	// tip is the branch tip before the conclusion.
	tip string
	// conflicted is the path the queue stopped on.
	conflicted string
	// clean is the path the queue's SECOND command touches. It is what proves
	// the rest of the queue ran: it can only be in the tree if git got past the
	// conflicted step.
	clean string
}

// newQueuedPickRepo builds a repository parked in a conflicted two-command
// cherry-pick or revert queue.
//
// For the cherry-pick, `side one` conflicts with main's edit of c.txt and
// `side two` adds d.txt cleanly on top. For the revert, two commits on main are
// reverted newest-first; the older one conflicts because a later edit sits over
// it, and the newer one reverts cleanly.
//
// RAW GIT creates the queue, deliberately. `safegit cherry-pick` and `safegit
// revert` each take exactly one commit and author the result themselves, so a
// multi-command sequence is a state only git can now put a repository in --
// which is precisely the state the delegated conclusion exists to finish, and
// it is unaffected by which tool wrote the queue.
func newQueuedPickRepo(t *testing.T, verb string) queuedFixture {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "base\n")
	testutil.WriteFile(t, dir, "d.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "c.txt", "d.txt")

	var argv []string
	switch verb {
	case "cherry-pick":
		testutil.Git(t, dir, "branch", "side")
		testutil.WriteFile(t, dir, "c.txt", "main\n")
		safegitCommitEnv(t, dir, conclusionSession, "main", "c.txt")

		testutil.Git(t, dir, "switch", "side")
		testutil.WriteFile(t, dir, "c.txt", "side one\n")
		first := safegitCommitEnv(t, dir, conclusionSession, "side one", "c.txt")
		testutil.WriteFile(t, dir, "d.txt", "side two\n")
		second := safegitCommitEnv(t, dir, conclusionSession, "side two", "d.txt")
		testutil.Git(t, dir, "switch", "main")
		argv = []string{"cherry-pick", first, second}

	case "revert":
		testutil.WriteFile(t, dir, "c.txt", "the change to undo\n")
		older := safegitCommitEnv(t, dir, conclusionSession, "older change", "c.txt")
		testutil.WriteFile(t, dir, "d.txt", "the clean change\n")
		newer := safegitCommitEnv(t, dir, conclusionSession, "newer change", "d.txt")
		// A later edit over c.txt is what makes the older revert conflict.
		testutil.WriteFile(t, dir, "c.txt", "a later edit\n")
		safegitCommitEnv(t, dir, conclusionSession, "a later edit", "c.txt")
		argv = []string{"revert", "--no-edit", newer, older}

	default:
		t.Fatalf("unknown verb %q", verb)
	}

	tip := testutil.Rev(t, dir, "HEAD")
	if out, code := testutil.GitTry(t, dir, argv...); code == 0 {
		t.Fatalf("the fixture needs a mid-queue conflict from `git %s`: %s", strings.Join(argv, " "), out)
	}
	if !testutil.FileExists(filepath.Join(dir, ".git", "sequencer")) {
		t.Fatalf("the fixture must leave a sequencer queue behind (%s)", strings.Join(argv, " "))
	}

	return queuedFixture{dir: dir, tip: tip, conflicted: "c.txt", clean: "d.txt"}
}

// unmergedCount reports how many unmerged slots the SHARED index holds. A
// delegated conclusion has to leave none: git wrote the copy, never this file,
// so an index still carrying the conflict is the exact staleness the adoption
// step exists to remove.
func unmergedCount(t *testing.T, dir string) int {
	t.Helper()
	out := testutil.Git(t, dir, "ls-files", "-u")
	if strings.TrimSpace(out) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

// TestQueuedCherryPickIsDelegatedAndCompletes: the whole delegated flow for a
// cherry-pick queue.
func TestQueuedCherryPickIsDelegatedAndCompletes(t *testing.T) {
	assertQueueDelegates(t, "cherry-pick", "cherry-pick-continue")
}

// TestQueuedRevertIsDelegatedAndCompletes is the same flow for a revert queue.
// It is a separate test rather than a table row for the same reason the two
// commands are separate commands: the state files, the message drafts and the
// stage meanings differ, and a shared assertion helper that passed for one and
// silently skipped the other would prove nothing about this one.
func TestQueuedRevertIsDelegatedAndCompletes(t *testing.T) {
	assertQueueDelegates(t, "revert", "revert-continue")
}

// assertQueueDelegates runs one verb's queue to completion through safegit and
// asserts every half of the contract.
func assertQueueDelegates(t *testing.T, verb, command string) {
	t.Helper()
	fx := newQueuedPickRepo(t, verb)

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		command, "--resolve", fx.conflicted+"=theirs")
	if code != exitcode.OK {
		t.Fatalf("%s of a queue failed (code %d)\nstdout=%s\nstderr=%s", command, code, stdout, stderr)
	}

	// The output names the delegation. An operator who does not learn that git
	// authored these commits will look for safegit's trailers on them.
	for _, want := range []string{"git", verb + " --continue"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q authored the commits:\n%s", want, stdout)
		}
	}
	// The consequence of the delegation is on STDERR and unconditional -- see
	// TestDelegatedConclusionAlwaysSaysWhoAuthoredTheCommits.
	if !strings.Contains(stderr, "undo") {
		t.Errorf("the report does not say safegit undo cannot reverse git's commits:\n%s", stderr)
	}

	// The queue completed: BOTH commands are on the branch. The clean path can
	// only be in the tree if git got past the conflicted step.
	if head := testutil.Rev(t, fx.dir, "HEAD"); head == fx.tip {
		t.Fatalf("HEAD did not move: the delegation created nothing")
	}
	paths := testutil.TreePaths(t, fx.dir, testutil.Rev(t, fx.dir, "HEAD"))
	if !testutil.Contains(paths, fx.clean) {
		t.Errorf("the queue's second command did not run: %s is not in the tree (%v)", fx.clean, paths)
	}
	if n := len(revListReverse(t, fx.dir)); n < 2 {
		t.Errorf("the branch holds %d commits, want the queue's commits on top of the fixture", n)
	}

	// git cleaned its own state, and the queue is gone with it.
	assertNoSequencerResidue(t, fx.dir, command+" of a queue")

	// The shared index is in step: git wrote only safegit's copy, so without
	// the adoption step the conflict would still be sitting here.
	if n := unmergedCount(t, fx.dir); n != 0 {
		t.Errorf("the shared index still holds %d unmerged slot(s) after the delegated conclusion", n)
	}
	if status := testutil.Git(t, fx.dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the working tree is not clean after the delegated conclusion:\n%s", status)
	}

	// A subsequent safegit commit works, which is the practical statement of
	// "no state was left behind".
	testutil.WriteFile(t, fx.dir, "after.txt", "after\n")
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"commit", "-m", "after the delegated conclusion", "--", "after.txt"); code != 0 {
		t.Fatalf("a commit after the delegated conclusion failed (code %d): %s", code, stderr)
	}
}

// TestDelegatedConclusionAlwaysSaysWhoAuthoredTheCommits pins the one fact a
// delegated conclusion may not withhold: these commits are git's, so they carry
// none of safegit's trailers and `safegit undo` will not reverse them.
//
// It is the same class of fact as undo's own "the merge state is NOT restored"
// note -- something the operation did NOT do, which an operator who does not
// hear it will assume was done -- so it is written the same way: stderr,
// unconditionally. --quiet still silences the ordinary report.
func TestDelegatedConclusionAlwaysSaysWhoAuthoredTheCommits(t *testing.T) {
	fx := newQueuedPickRepo(t, "cherry-pick")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--quiet", "cherry-pick-continue", "--resolve", fx.conflicted+"=theirs")
	if code != exitcode.OK {
		t.Fatalf("the delegated conclusion failed (code %d): %s", code, stderr)
	}
	for _, want := range []string{"these commits are git's", "undo"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("--quiet suppressed the delegation fact (%q):\n%s", want, stderr)
		}
	}
	// safegit's own report is chatter and goes; git's passthrough output on
	// stdout is git's, and --quiet was never a claim about it.
	for _, gone := range []string{"git concluded the queued", "staged into safegit's index copy"} {
		if strings.Contains(stdout, gone) {
			t.Errorf("--quiet did not suppress safegit's own report (%q):\n%s", gone, stdout)
		}
	}
}

// TestDelegatedConclusionPayloadStatesTheDelegation pins the machine-mode
// answer: a consumer must be able to tell a git-authored conclusion from a
// safegit-authored one without parsing prose, and the pipeline members it would
// otherwise read (sha, tree, parents) must be ABSENT rather than null, because
// safegit created no commit for them to describe.
func TestDelegatedConclusionPayloadStatesTheDelegation(t *testing.T) {
	fx := newQueuedPickRepo(t, "cherry-pick")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--json", "cherry-pick-continue", "--resolve", fx.conflicted+"=theirs")
	if code != exitcode.OK {
		t.Fatalf("delegated conclusion under --json failed (code %d): %s", code, stderr)
	}

	var envelope struct {
		Payload map[string]interface{} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("the envelope is not JSON (%v):\n%s", err, stdout)
	}
	p := envelope.Payload
	if p == nil {
		t.Fatalf("the envelope carries no payload:\n%s", stdout)
	}

	if delegated, _ := p["queue_delegated"].(bool); !delegated {
		t.Errorf("queue_delegated = %v, want true", p["queue_delegated"])
	}
	head, _ := p["head"].(string)
	if head != testutil.Rev(t, fx.dir, "HEAD") {
		t.Errorf("head = %q, want the branch tip %s", head, testutil.Rev(t, fx.dir, "HEAD"))
	}
	if n, _ := p["commits_created"].(float64); int(n) != 2 {
		t.Errorf("commits_created = %v, want 2 (the conflicted step plus the queued one)", p["commits_created"])
	}
	if cleared, _ := p["state_cleared"].(bool); !cleared {
		t.Errorf("state_cleared = %v, want true once the queue finished", p["state_cleared"])
	}
	for _, absent := range []string{"sha", "tree", "parents", "files", "attempts", "author"} {
		if _, present := p[absent]; present {
			t.Errorf("the delegated payload carries %q (%v); safegit created no commit for it to describe",
				absent, p[absent])
		}
	}
}

// TestDelegatedConclusionStopsAtTheNextConflict: a queue whose SECOND command
// also conflicts stops again, and the repository it leaves behind is
// continuable -- the new conflict is in the shared index, not stranded in the
// index copy safegit handed git.
//
// This is the case that decides how the shared index is put back in step. git
// writes the next conflict's stages into the copy only, so anything that
// rebuilt the shared index from the new HEAD would report a clean index for a
// repository that is genuinely mid-conflict.
func TestDelegatedConclusionStopsAtTheNextConflict(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "base\n")
	testutil.WriteFile(t, dir, "d.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "c.txt", "d.txt")

	testutil.Git(t, dir, "branch", "side")
	testutil.WriteFile(t, dir, "c.txt", "main c\n")
	testutil.WriteFile(t, dir, "d.txt", "main d\n")
	safegitCommitEnv(t, dir, conclusionSession, "main edits both", "c.txt", "d.txt")

	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "c.txt", "side c\n")
	first := safegitCommitEnv(t, dir, conclusionSession, "side c", "c.txt")
	testutil.WriteFile(t, dir, "d.txt", "side d\n")
	second := safegitCommitEnv(t, dir, conclusionSession, "side d", "d.txt")
	testutil.Git(t, dir, "switch", "main")

	// Raw git: a multi-command queue is a state only git can create now.
	if out, code := testutil.GitTry(t, dir, "cherry-pick", first, second); code == 0 {
		t.Fatalf("the fixture needs a first conflict: %s", out)
	}

	_, stderr, code := runSafegitEnv(t, dir, conclusionSession,
		"cherry-pick-continue", "--resolve", "c.txt=theirs")
	if code == 0 {
		t.Fatalf("the second command conflicts too, so the delegation must report git's failure: %s", stderr)
	}
	// safegit says what is in flight now, from the single way-out authority.
	if !strings.Contains(stderr, "cherry-pick-continue") {
		t.Errorf("the report does not name the command that concludes the new stop:\n%s", stderr)
	}

	// The FIRST command was committed, so the branch moved.
	if !testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "c.txt") {
		t.Error("the conflicted step was not committed")
	}
	if got := testutil.MustShow(t, dir, "HEAD", "c.txt"); got != "side c\n" {
		t.Errorf("HEAD:c.txt = %q, want the resolution staged into the index copy", got)
	}

	// And the new conflict is readable from the shared index, which is what
	// makes the next conclusion possible at all.
	if n := unmergedCount(t, dir); n == 0 {
		t.Fatal("the shared index holds no unmerged slots, but git stopped on a conflict in d.txt")
	}
	if !strings.Contains(testutil.Git(t, dir, "ls-files", "-u"), "d.txt") {
		t.Errorf("the shared index does not hold the NEW conflict:\n%s", testutil.Git(t, dir, "ls-files", "-u"))
	}

	// Concluding again finishes the queue: the flow composes with itself.
	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession,
		"cherry-pick-continue", "--resolve", "d.txt=theirs"); code != 0 {
		t.Fatalf("the second delegated conclusion failed (code %d): %s", code, stderr)
	}
	assertNoSequencerResidue(t, dir, "the second delegated conclusion")
	if n := unmergedCount(t, dir); n != 0 {
		t.Errorf("the shared index still holds %d unmerged slot(s) once the queue finished", n)
	}
}

// newThreeStepQueue parks a repository in a cherry-pick queue of three
// commands whose FIRST and SECOND both conflict, so concluding the first stops
// git again with one commit made and one command still queued.
//
// It is the shape a mid-queue stop needs and the two-command fixture cannot
// give: there is a commit git created, a conflict it stopped on, and a further
// command behind that one.
func newThreeStepQueue(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "c.txt", "base\n")
	testutil.WriteFile(t, dir, "d.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "c.txt", "d.txt")

	testutil.Git(t, dir, "branch", "side")
	testutil.WriteFile(t, dir, "c.txt", "main c\n")
	testutil.WriteFile(t, dir, "d.txt", "main d\n")
	safegitCommitEnv(t, dir, conclusionSession, "main edits both", "c.txt", "d.txt")

	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "c.txt", "side c\n")
	first := safegitCommitEnv(t, dir, conclusionSession, "side c", "c.txt")
	testutil.WriteFile(t, dir, "d.txt", "side d\n")
	second := safegitCommitEnv(t, dir, conclusionSession, "side d", "d.txt")
	testutil.WriteFile(t, dir, "e.txt", "side e\n")
	third := safegitCommitEnv(t, dir, conclusionSession, "side e", "e.txt")
	testutil.Git(t, dir, "switch", "main")

	// Raw git: a multi-command queue is a state only git can create now.
	if out, code := testutil.GitTry(t, dir, "cherry-pick", first, second, third); code == 0 {
		t.Fatalf("the fixture needs the queue to stop on its first command: %s", out)
	}
	return dir
}

// TestDelegatedConclusionReportsTheCommitsItMadeBeforeStoppingAgain: a
// conclusion that stops on the queue's NEXT conflict still created commits, and
// said nothing about them at all -- the text report was skipped and the
// envelope carried a null payload at exit 1.
//
// The commits exist either way. A caller that has to re-read the branch to find
// out what a safegit command did to it is being told less than safegit knows.
func TestDelegatedConclusionReportsTheCommitsItMadeBeforeStoppingAgain(t *testing.T) {
	t.Run("the text report", func(t *testing.T) {
		dir := newThreeStepQueue(t)
		before := testutil.Rev(t, dir, "HEAD")

		stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession,
			"cherry-pick-continue", "--resolve", "c.txt=theirs")
		if code == 0 {
			t.Fatalf("the queue's second command conflicts, so this must exit nonzero:\n%s", stderr)
		}
		if head := testutil.Rev(t, dir, "HEAD"); head == before {
			t.Fatal("the conclusion committed nothing, so there is nothing to report")
		}

		if !strings.Contains(stdout, "1 commit(s) created") {
			t.Errorf("the report does not say how many commits git made before stopping:\n%s", stdout)
		}
		if !strings.Contains(stdout, "NOT finished") {
			t.Errorf("the report does not say the queue is unfinished:\n%s", stdout)
		}
		// The delegation's cost covers the commit that WAS made.
		if !strings.Contains(stderr, "these commits are git's") {
			t.Errorf("the report does not say who authored the commit it made:\n%s", stderr)
		}
		// And the way out of the state it left, which was already there.
		if !strings.Contains(stderr, "cherry-pick-continue") {
			t.Errorf("the report does not name the command that concludes the new stop:\n%s", stderr)
		}
	})

	t.Run("the payload", func(t *testing.T) {
		dir := newThreeStepQueue(t)

		stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession,
			"--json", "cherry-pick-continue", "--resolve", "c.txt=theirs")
		if code == 0 {
			t.Fatalf("the queue's second command conflicts, so this must exit nonzero:\n%s", stderr)
		}

		var envelope struct {
			ExitCode int                    `json:"exit_code"`
			Payload  map[string]interface{} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatalf("the envelope is not JSON (%v):\n%s", err, stdout)
		}
		if envelope.ExitCode == 0 {
			t.Errorf("the envelope reports exit_code 0 for a run that stopped on a conflict:\n%s", stdout)
		}
		p := envelope.Payload
		if p == nil {
			t.Fatalf("the envelope carries no payload for a run that created commits:\n%s", stdout)
		}
		if delegated, _ := p["queue_delegated"].(bool); !delegated {
			t.Errorf("queue_delegated = %v, want true", p["queue_delegated"])
		}
		if stopped, _ := p["stopped_again"].(bool); !stopped {
			t.Errorf("stopped_again = %v, want true", p["stopped_again"])
		}
		if cleared, _ := p["state_cleared"].(bool); cleared {
			t.Errorf("state_cleared = %v, but git stopped again and its state is still in place", p["state_cleared"])
		}
		if n, _ := p["commits_created"].(float64); int(n) != 1 {
			t.Errorf("commits_created = %v, want 1 (the concluded step)", p["commits_created"])
		}
		if head, _ := p["head"].(string); head != testutil.Rev(t, dir, "HEAD") {
			t.Errorf("head = %q, want the branch tip %s", head, testutil.Rev(t, dir, "HEAD"))
		}
	})

	// The queue still composes with itself afterwards: the report is an
	// addition to the stopped state, not a change to it.
	t.Run("it still continues", func(t *testing.T) {
		dir := newThreeStepQueue(t)
		if _, stderr, code := runSafegitEnv(t, dir, conclusionSession,
			"cherry-pick-continue", "--resolve", "c.txt=theirs"); code == 0 {
			t.Fatalf("the fixture needs the second stop: %s", stderr)
		}
		if _, stderr, code := runSafegitEnv(t, dir, conclusionSession,
			"cherry-pick-continue", "--resolve", "d.txt=theirs"); code != 0 {
			t.Fatalf("the second delegated conclusion failed (code %d): %s", code, stderr)
		}
		assertNoSequencerResidue(t, dir, "the finished three-command queue")
		if !testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "e.txt") {
			t.Error("the queue's third command did not run")
		}
	})
}

// TestDelegatedConclusionRefusesWhatItCannotHonor: -m, --trailer and --dry-run
// have no honest meaning when git writes the commits, so each is refused
// naming its reason rather than silently dropped.
func TestDelegatedConclusionRefusesWhatItCannotHonor(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a message",
			args: []string{"cherry-pick-continue", "--resolve", "c.txt=theirs", "-m", "my own message"},
			want: "-m and --trailer cannot be honored",
		},
		{
			name: "a trailer",
			args: []string{"cherry-pick-continue", "--resolve", "c.txt=theirs", "--trailer", "Key: value"},
			want: "-m and --trailer cannot be honored",
		},
		{
			name: "a preview",
			args: []string{"--dry-run", "cherry-pick-continue", "--resolve", "c.txt=theirs"},
			want: "--dry-run is not supported",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newQueuedPickRepo(t, "cherry-pick")

			_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, tc.args...)
			if code == 0 {
				t.Fatalf("%s must be refused for a queued conclusion: %s", tc.name, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("the refusal does not state its reason (%q):\n%s", tc.want, stderr)
			}
			// Nothing ran: the queue is untouched and the branch has not moved.
			if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
				t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
			}
			if !testutil.FileExists(filepath.Join(fx.dir, ".git", "sequencer")) {
				t.Error("the refusal destroyed the queue it declined to conclude")
			}
		})
	}
}

// TestDelegatedConclusionRunsTheSameChecks: the delegation is not a bypass. The
// completeness refusal and the conflict-marker refusal are safegit's own, and
// they fire on a queued sequence exactly as they do on a single one -- before
// git is started at all.
func TestDelegatedConclusionRunsTheSameChecks(t *testing.T) {
	t.Run("an unresolved path", func(t *testing.T) {
		fx := newQueuedPickRepo(t, "cherry-pick")
		_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "cherry-pick-continue")
		if code != exitcode.ConclusionUnresolved {
			t.Fatalf("exit %d, want %d (ConclusionUnresolved): %s", code, exitcode.ConclusionUnresolved, stderr)
		}
		if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
			t.Errorf("HEAD moved despite the refusal")
		}
	})

	t.Run("a surviving conflict marker", func(t *testing.T) {
		fx := newQueuedPickRepo(t, "cherry-pick")
		// The conflicted file is left exactly as git wrote it -- markers and
		// all -- and then declared resolved from the working tree.
		_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"cherry-pick-continue", "--resolve", fx.conflicted+"=worktree")
		if code != exitcode.ConclusionMarkerSurvived {
			t.Fatalf("exit %d, want %d (ConclusionMarkerSurvived): %s", code, exitcode.ConclusionMarkerSurvived, stderr)
		}
		if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
			t.Errorf("HEAD moved despite the refusal")
		}
		if !testutil.FileExists(filepath.Join(fx.dir, ".git", "sequencer")) {
			t.Error("the refusal destroyed the queue")
		}
	})
}

// TestDelegatedConclusionRunsGitsOwnHooks records what git's own commit path
// does to the repository's hooks, because it is not what safegit's does and an
// operator relying on either needs the fact.
//
// Probed and pinned: the step being CONCLUDED goes through git's commit path
// and fires pre-commit, prepare-commit-msg, commit-msg and post-commit. Every
// FURTHER command in the queue is committed by the sequencer itself and fires
// only prepare-commit-msg and post-commit -- no pre-commit, no commit-msg. If a
// future git changes either set, this fails and the fact is re-recorded rather
// than remembered.
func TestDelegatedConclusionRunsGitsOwnHooks(t *testing.T) {
	fx := newQueuedPickRepo(t, "cherry-pick")

	log := filepath.Join(t.TempDir(), "hooks.log")
	hookDir := filepath.Join(fx.dir, ".git", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit"} {
		script := "#!/bin/sh\necho " + name + " >> " + log + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(hookDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"cherry-pick-continue", "--resolve", fx.conflicted+"=theirs"); code != 0 {
		t.Fatalf("the delegated conclusion failed (code %d): %s", code, stderr)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("git ran none of the repository's hooks: %v", err)
	}
	ran := strings.Fields(string(data))
	want := []string{
		// the conflicted step, committed through git's commit path
		"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit",
		// the queue's remaining command, committed by the sequencer
		"prepare-commit-msg", "post-commit",
	}
	if strings.Join(ran, " ") != strings.Join(want, " ") {
		t.Errorf("git's hook sequence changed.\n  ran:  %v\n  want: %v\n"+
			"This is a recorded fact about git, not a safegit rule: re-verify and re-record it.", ran, want)
	}
}
