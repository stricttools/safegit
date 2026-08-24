package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Navigation is `safegit switch`, and `checkout` is not a safegit command.
//
// git's checkout is two commands wearing one name: it moves HEAD, and it
// restores files from a commit over whatever the working tree holds. The second
// one destroys uncommitted work with no record anywhere, which is why the whole
// spelling is gone rather than guarded: `switch` has no file mode at all, so the
// destructive form is not refused, it is inexpressible.
//
// What switch accepts is an EXISTING BRANCH NAME, or `-c <new>` to create one.
// Every other argument -- a tag, an object name, any other commit-ish -- puts
// HEAD in the detached state safegit treats as a refusal state everywhere else,
// so it is refused here with the raw-git escape named for the rare deliberate
// case.

var switchSession = []string{"CLAUDE_CODE_SESSION_ID=switch-test"}

// TestSwitchMovesToAnExistingBranch is the whole supported form.
func TestSwitchMovesToAnExistingBranch(t *testing.T) {
	dir := newTwoCommitRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, switchSession, "switch", "other"); code != 0 {
		t.Fatalf("switch to an existing branch failed (code %d): %s", code, stderr)
	}
	if branch := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "other" {
		t.Fatalf("HEAD is on %q, want other", branch)
	}

	// The navigation is recorded under the command's own op name.
	entries := oplogEntries(t, dir, "switch")
	if len(entries) != 1 {
		t.Fatalf("expected exactly one switch oplog entry, got %d: %v", len(entries), entries)
	}
	extra := oplogExtra(entries[0])
	if got, _ := oplogExtraString(extra, "ref"); got != "refs/heads/other" {
		t.Errorf("the switch entry records ref %q, want refs/heads/other; extra=%v", got, extra)
	}
	if got, _ := oplogExtraString(extra, "observed_tip"); got != testutil.Rev(t, dir, "HEAD") {
		t.Errorf("the switch entry records observed_tip %q, want the tip HEAD stands at; extra=%v", got, extra)
	}
}

// TestSwitchCreatesABranchWithDashC: `-c` is switch's own spelling of branch
// creation, and it replaces checkout's `-b`.
func TestSwitchCreatesABranchWithDashC(t *testing.T) {
	dir := newTwoCommitRepo(t)

	if _, stderr, code := runSafegitEnv(t, dir, switchSession, "switch", "-c", "fresh"); code != 0 {
		t.Fatalf("switch -c failed (code %d): %s", code, stderr)
	}
	if branch := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "fresh" {
		t.Fatalf("HEAD is on %q, want fresh", branch)
	}
}

// TestSwitchRefusesEverythingThatIsNotABranch: a tag and an object name both
// resolve to a commit, and both would detach HEAD.
func TestSwitchRefusesEverythingThatIsNotABranch(t *testing.T) {
	for _, what := range []string{"tag", "object name"} {
		t.Run(what, func(t *testing.T) {
			dir := newTwoCommitRepo(t)
			before := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

			arg := "v1"
			if what == "object name" {
				arg = testutil.Rev(t, dir, "HEAD")
			}
			stdout, stderr, code := runSafegitEnv(t, dir, switchSession, "switch", arg)
			if code != exitcode.Usage {
				t.Fatalf("safegit switch %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
					arg, code, exitcode.Usage, stdout, stderr)
			}
			if !strings.Contains(stderr, "branch") {
				t.Errorf("the refusal does not say a branch is what switch takes:\n%s", stderr)
			}
			if !strings.Contains(stderr, "git switch") && !strings.Contains(stderr, "git checkout") {
				t.Errorf("the refusal does not name the raw-git escape for the deliberate case:\n%s", stderr)
			}
			if !strings.Contains(stderr, "docs/divergences.md") {
				t.Errorf("the refusal does not point at the divergences catalog:\n%s", stderr)
			}
			if now := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); now != before {
				t.Errorf("the refused switch moved HEAD onto %q (was %q)", now, before)
			}
		})
	}
}

// TestSwitchRefusesTheFlagsThatDetachOrDestroy: each of these is a capability
// safegit deliberately does not have, and each refuses by name.
func TestSwitchRefusesTheFlagsThatDetachOrDestroy(t *testing.T) {
	for _, args := range [][]string{
		{"switch", "--detach", "other"},
		{"switch", "-C", "other"},
		{"switch", "--force", "other"},
		{"switch", "--discard-changes", "other"},
		{"switch", "--orphan", "brandnew"},
		{"switch", "--merge", "other"},
	} {
		t.Run(args[1], func(t *testing.T) {
			dir := newTwoCommitRepo(t)
			before := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

			assertSubsetRefusal(t, dir, args...)
			if now := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); now != before {
				t.Errorf("the refused switch moved HEAD onto %q (was %q)", now, before)
			}
		})
	}
}

// TestSwitchTakesNoPathspec: the destructive `checkout -- <path>` shape is what
// the whole rename exists to make inexpressible, so the closest spelling
// anybody can type on switch is refused too.
func TestSwitchTakesNoPathspec(t *testing.T) {
	dir := newTwoCommitRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "uncommitted work nobody else has\n")

	stdout, stderr, code := runSafegitEnv(t, dir, switchSession, "switch", "--", "a.txt")
	if code == 0 {
		t.Fatalf("safegit switch -- a.txt succeeded\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if got := readWorktree(t, dir, "a.txt"); got != "uncommitted work nobody else has\n" {
		t.Errorf("a.txt was restored over the operator's work: %q", got)
	}
}

// TestCheckoutIsNotASafegitCommand: the rename is a REMOVAL, not an alias. A
// command line still typing `safegit checkout` has to fail rather than quietly
// do something.
func TestCheckoutIsNotASafegitCommand(t *testing.T) {
	dir := newTwoCommitRepo(t)
	before := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, switchSession, "checkout", "other")
	if code == 0 {
		t.Fatalf("safegit checkout still runs\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if now := testutil.Git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); now != before {
		t.Errorf("safegit checkout moved HEAD onto %q (was %q)", now, before)
	}
}
