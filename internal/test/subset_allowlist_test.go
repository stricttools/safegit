package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The subset boundary, from the operator's side.
//
// Every command that forwards a git command line validates it against an
// explicit ALLOWLIST before anything runs: the options safegit can honor are
// listed, and everything else is refused. The direction is what these tests
// pin. A refusal LIST lets an option safegit has never considered through to
// git, where it changes what git does while safegit's checks and its record of
// the operation are still written for something else; an allowlist cannot.
//
// Two kinds of refusal come out of it and both are exit 2:
//
//   - a NAMED capability refuses with its own reason, because "safegit does not
//     select merge strategies, and here is why" is an answer an operator can act
//     on;
//   - anything else refuses with the subset law itself.
//
// Each refusal is a divergence from git and each needs its row in
// docs/divergences.md.

// assertSubsetRefusal runs one command line and requires the subset refusal:
// exit 2, the absent capability named, and the divergences catalog pointed at.
func assertSubsetRefusal(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runSafegit(t, dir, args...)
	if code != exitcode.Usage {
		t.Fatalf("safegit %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
			strings.Join(args, " "), code, exitcode.Usage, stdout, stderr)
	}
	if !strings.Contains(stderr, "does not support") {
		t.Errorf("the refusal does not name the absent capability:\n%s", stderr)
	}
	if !strings.Contains(stderr, "docs/divergences.md") {
		t.Errorf("the refusal does not point at the divergences catalog:\n%s", stderr)
	}
	return stderr
}

// newTwoCommitRepo is the fixture the reset and switch cases share: two commits
// on main, a second branch, and a tag naming the tip.
func newTwoCommitRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	safegitCommit(t, dir, "second", "a.txt")
	testutil.Git(t, dir, "branch", "other")
	testutil.Git(t, dir, "tag", "v1")
	return dir
}

// TestMergeAllowlistRefusesWhatItDoesNotList: merge's own two additions to the
// refused set (--no-verify and the signing flags), plus an option nobody has
// considered at all, which is the whole point of the default-deny direction.
func TestMergeAllowlistRefusesWhatItDoesNotList(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"no-verify", []string{"merge", "--no-verify", "feature"}, "commit-msg"},
		{"gpg-sign", []string{"merge", "-S", "feature"}, "sign"},
		{"an option safegit never considered", []string{"merge", "--verify-signatures", "feature"}, "does not support"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newMergeableRepo(t)
			tip := testutil.Rev(t, dir, "HEAD")

			stderr := assertSubsetRefusal(t, dir, tc.args...)
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("the refusal does not say %q:\n%s", tc.says, stderr)
			}
			if head := testutil.Rev(t, dir, "HEAD"); head != tip {
				t.Errorf("the refused merge moved HEAD to %s (was %s)", head, tip)
			}
		})
	}
}

// TestMergeAllowlistPassesAnAllowedForm is the control: an option ON the list
// still works, so the allowlist refuses the unlisted rather than everything.
func TestMergeAllowlistPassesAnAllowedForm(t *testing.T) {
	dir := newMergeableRepo(t)
	if _, stderr, code := runSafegit(t, dir, "merge", "--no-ff", "--signoff", "feature"); code != 0 {
		t.Fatalf("an allowed merge form was refused (code %d): %s", code, stderr)
	}
	if parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD")); len(parents) != 2 {
		t.Errorf("the allowed merge produced %d parent(s), want 2", len(parents))
	}
}

// TestPickAndRevertRefuseStrategySelection: the strategy flags are refused on
// cherry-pick and revert exactly as they are on merge, and for the same reason.
// A pick computed with a non-default strategy parks a conflict the conclusion's
// own checks cannot read -- no AUTO_MERGE to reconstruct the markers from -- so
// letting one through would leave the protection over the staged result with a
// hole in it.
func TestPickAndRevertRefuseStrategySelection(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		for _, opt := range [][]string{{"--strategy", "resolve"}, {"-X", "ours"}} {
			t.Run(verb+" "+opt[0], func(t *testing.T) {
				dir, first, _ := newPickableRepo(t)
				tip := testutil.Rev(t, dir, "HEAD")
				target := "HEAD"
				if verb == "cherry-pick" {
					target = first
				}

				args := append([]string{verb}, opt...)
				args = append(args, target)
				stderr := assertSubsetRefusal(t, dir, args...)
				if !strings.Contains(stderr, "strateg") {
					t.Errorf("the refusal does not name the strategy capability:\n%s", stderr)
				}
				if head := testutil.Rev(t, dir, "HEAD"); head != tip {
					t.Errorf("the refused %s moved HEAD to %s (was %s)", verb, head, tip)
				}
			})
		}
	}
}

// TestPickAndRevertRefuseAnUnlistedOption: the default-deny half on the two
// commands whose refusal lists already existed.
func TestPickAndRevertRefuseAnUnlistedOption(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			dir, first, _ := newPickableRepo(t)
			target := "HEAD"
			if verb == "cherry-pick" {
				target = first
			}
			assertSubsetRefusal(t, dir, verb, "--allow-empty-message", target)
		})
	}
}

// TestResetRefusesThePathspecFormAndInteractiveSelection: a pathspec reset
// manipulates the shared index safegit owns, and --patch is an editor session.
// Both are outside the subset; the mode flags with a commit are inside it.
func TestResetRefusesThePathspecFormAndInteractiveSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"pathspec after a double dash", []string{"reset", "HEAD~1", "--", "a.txt"}, "pathspec"},
		{"pathspec without a double dash", []string{"reset", "a.txt"}, "pathspec"},
		{"patch selection", []string{"reset", "--patch"}, "does not support"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newTwoCommitRepo(t)
			tip := testutil.Rev(t, dir, "HEAD")

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if code != exitcode.Usage {
				t.Fatalf("safegit %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
					strings.Join(tc.args, " "), code, exitcode.Usage, stdout, stderr)
			}
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("the refusal does not say %q:\n%s", tc.says, stderr)
			}
			if !strings.Contains(stderr, "docs/divergences.md") {
				t.Errorf("the refusal does not point at the divergences catalog:\n%s", stderr)
			}
			if head := testutil.Rev(t, dir, "HEAD"); head != tip {
				t.Errorf("the refused reset moved HEAD to %s (was %s)", head, tip)
			}
		})
	}
}

// TestResetPassesAnAllowedMode is the control: the five modes with a commit
// argument are the allowlist, and one of them still resets.
func TestResetPassesAnAllowedMode(t *testing.T) {
	dir := newTwoCommitRepo(t)
	first := testutil.Rev(t, dir, "HEAD~1")

	if _, stderr, code := runSafegit(t, dir, "reset", "--hard", "HEAD~1"); code != 0 {
		t.Fatalf("an allowed reset form was refused (code %d): %s", code, stderr)
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != first {
		t.Errorf("the allowed reset left HEAD at %s, want %s", head, first)
	}
}

// TestBisectRefusesASubcommandOutsideTheClassifiedVocabulary: bisect's
// allowlist is the subcommand vocabulary the classification table declares, so
// a word outside it never reaches git.
func TestBisectRefusesASubcommandOutsideTheClassifiedVocabulary(t *testing.T) {
	dir := newTwoCommitRepo(t)
	assertSubsetRefusal(t, dir, "bisect", "frobnicate")
}

// TestBisectPassesAClassifiedSubcommand is the control.
func TestBisectPassesAClassifiedSubcommand(t *testing.T) {
	dir := newTwoCommitRepo(t)
	if _, stderr, code := runSafegit(t, dir, "bisect", "start"); code != 0 {
		t.Fatalf("`bisect start` was refused (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "bisect", "reset"); code != 0 {
		t.Fatalf("`bisect reset` was refused (code %d): %s", code, stderr)
	}
}

// TestRebaseRefusesTheApplyBackendAndItsOptions: the rebase door is git's own
// replay, and it is declared for the merge backend's interactive and
// non-interactive forms. The apply backend, per-commit command execution, merge
// preservation and the root rewrite are outside it.
func TestRebaseRefusesTheApplyBackendAndItsOptions(t *testing.T) {
	for _, args := range [][]string{
		{"rebase", "--apply", "side"},
		{"rebase", "--whitespace=fix", "side"},
		{"rebase", "--exec", "make test", "side"},
		{"rebase", "--rebase-merges", "side"},
		{"rebase", "--root"},
	} {
		t.Run(args[1], func(t *testing.T) {
			dir, _, _ := newPickableRepo(t)
			tip := testutil.Rev(t, dir, "HEAD")

			assertSubsetRefusal(t, dir, args...)
			if head := testutil.Rev(t, dir, "HEAD"); head != tip {
				t.Errorf("the refused rebase moved HEAD to %s (was %s)", head, tip)
			}
		})
	}
}

// TestRebasePassesAnAllowedForm is the control: an upstream, which is the door
// the campaign deliberately keeps open.
func TestRebasePassesAnAllowedForm(t *testing.T) {
	dir, _, _ := newPickableRepo(t)
	if _, stderr, code := runSafegit(t, dir, "rebase", "side"); code != 0 {
		t.Fatalf("an allowed rebase form was refused (code %d): %s", code, stderr)
	}
	if !testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "one.txt") {
		t.Error("the rebase did not replay onto the side branch")
	}
}

// TestPickAndRevertSubsetRefusalsApplyToAPreviewToo: an option safegit's
// cherry-pick and revert do not implement is refused whether or not the run was
// going to happen, and nothing is recorded in the would-do log for it.
//
// These used to be preview-specific refusals ("safegit's preview does not
// forward strategy options"), which said the outcome could not be COMPUTED. It
// is the wrong answer now that the option cannot be RUN.
func TestPickAndRevertSubsetRefusalsApplyToAPreviewToo(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			dir, first, _ := newPickableRepo(t)
			target := "HEAD"
			if verb == "cherry-pick" {
				target = first
			}

			stdout, stderr, code := runSafegit(t, dir, "--dry-run", verb, "-Xtheirs", target)
			if code != exitcode.Usage {
				t.Fatalf("a preview of an unsupported %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
					verb, code, exitcode.Usage, stdout, stderr)
			}
			if !strings.Contains(stderr, "does not support") {
				t.Errorf("the refusal does not name the absent capability:\n%s", stderr)
			}
			if strings.Contains(stdout, "run: git") {
				t.Errorf("the refused invocation was still recorded as a would-do:\n%s", stdout)
			}
		})
	}
}
