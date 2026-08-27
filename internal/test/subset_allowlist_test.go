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

// TestPickAndRevertRefuseStrategySelection: strategy SELECTION is refused on
// cherry-pick and revert exactly as it is on merge, and for the same reason. A
// pick computed with a non-default strategy parks a conflict the conclusion's
// own checks cannot read -- no AUTO_MERGE to reconstruct the markers from -- so
// letting one through would leave the protection over the staged result with a
// hole in it.
//
// Strategy OPTIONS are the other half of that vocabulary and are HONORED: they
// tune the same ort compute and leave every one of those checks reading what it
// reads today (see TestStrategyOptionsAreHonoredOnAllThreeVerbs). Only the long
// spelling is exercised here, because on these two verbs `-s` is git's own
// spelling for signoff.
func TestPickAndRevertRefuseStrategySelection(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		for _, opt := range [][]string{{"--strategy", "resolve"}} {
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

// TestTheRerereAutoUpdateFlagIsRefusedOnAllThreeVerbs: asking git to stage a
// remembered resolution during the compute step is refused on every verb that
// computes, because what it stages is a resolution nobody made in this
// operation -- and safegit's conclusion is built on the operator declaring each
// conflicted path.
func TestTheRerereAutoUpdateFlagIsRefusedOnAllThreeVerbs(t *testing.T) {
	for _, tc := range []struct {
		verb string
		args []string
	}{
		{"merge", []string{"merge", "--rerere-autoupdate", "feature"}},
		{"cherry-pick", []string{"cherry-pick", "--rerere-autoupdate", "HEAD"}},
		{"revert", []string{"revert", "--rerere-autoupdate", "HEAD"}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			dir := newMergeableRepo(t)
			tip := testutil.Rev(t, dir, "HEAD")

			stderr := assertSubsetRefusal(t, dir, tc.args...)
			if !strings.Contains(stderr, "rerere") {
				t.Errorf("the refusal does not name the capability:\n%s", stderr)
			}
			if !strings.Contains(stderr, "remembered resolution") {
				t.Errorf("the refusal does not give the remembered-resolution reason:\n%s", stderr)
			}
			// And it names the catalog entry that states the whole answer,
			// including the config route it does NOT close.
			if !strings.Contains(stderr, "The rerere auto-update flag is refused, and its config key is not") {
				t.Errorf("the refusal does not name the catalog entry:\n%s", stderr)
			}
			if head := testutil.Rev(t, dir, "HEAD"); head != tip {
				t.Errorf("the refused %s moved HEAD to %s (was %s)", tc.verb, head, tip)
			}
		})
	}
}

// TestTheNegativeRerereSpellingIsStillAllowed is the other half of the row
// split, and the reason it looks confusing is worth stating: the NEGATIVE
// spelling turns the objected-to mechanism OFF, and it is an operator's only
// per-run switch against the `rerere.autoUpdate` config key, which safegit
// still honors.
func TestTheNegativeRerereSpellingIsStillAllowed(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		dir := newMergeableRepo(t)
		if _, stderr, code := runSafegit(t, dir, "merge", "--no-rerere-autoupdate", "feature"); code != 0 {
			t.Fatalf("merge --no-rerere-autoupdate was refused (code %d): %s", code, stderr)
		}
	})
	t.Run("cherry-pick", func(t *testing.T) {
		dir, first, _ := newPickableRepo(t)
		if _, stderr, code := runSafegitEnv(t, dir, pickSession, "cherry-pick", "--no-rerere-autoupdate", first); code != 0 {
			t.Fatalf("cherry-pick --no-rerere-autoupdate was refused (code %d): %s", code, stderr)
		}
	})
	t.Run("revert", func(t *testing.T) {
		dir, _, _ := newPickableRepo(t)
		if _, stderr, code := runSafegitEnv(t, dir, pickSession, "revert", "--no-rerere-autoupdate", "HEAD"); code != 0 {
			t.Fatalf("revert --no-rerere-autoupdate was refused (code %d): %s", code, stderr)
		}
	})
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

// newBranchWithAMergeRepo builds a branch whose own history contains a merge
// commit, and an upstream that has moved on -- the one shape where preserving
// merge topology through a rebase is a different answer from flattening it.
func newBranchWithAMergeRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommit(t, dir, "base", "base.txt")

	testutil.Git(t, dir, "branch", "topic")
	testutil.Git(t, dir, "branch", "feature")

	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "feature.txt", "feature\n")
	safegitCommit(t, dir, "the feature side", "feature.txt")

	testutil.Git(t, dir, "switch", "topic")
	testutil.WriteFile(t, dir, "topic.txt", "topic\n")
	safegitCommit(t, dir, "the topic side", "topic.txt")
	testutil.Git(t, dir, "merge", "--no-ff", "-m", "merge feature into topic", "feature")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "main.txt", "main\n")
	safegitCommit(t, dir, "the upstream moves on", "main.txt")

	testutil.Git(t, dir, "switch", "topic")
	return dir
}

// TestRebasePassesTheTopologyPreservingForm: preserving merge topology is
// ALLOWED. The whole replay runs inside the one door where git authors the
// commits, which is verb-scoped and already admits everything a rebase makes --
// so a merge re-created by the replay is no more git's than a linear commit
// replayed beside it, and safegit checks neither.
func TestRebasePassesTheTopologyPreservingForm(t *testing.T) {
	for _, flag := range []string{"-r", "--rebase-merges", "--rebase-merges=no-rebase-cousins"} {
		t.Run(flag, func(t *testing.T) {
			dir := newBranchWithAMergeRepo(t)
			upstream := testutil.Rev(t, dir, "main")

			if _, stderr, code := runSafegit(t, dir, "rebase", flag, "main"); code != 0 {
				t.Fatalf("a topology-preserving rebase was refused (code %d): %s", code, stderr)
			}
			if !testutil.Contains(testutil.TreePaths(t, dir, "HEAD"), "main.txt") {
				t.Error("the rebase did not replay onto the upstream")
			}
			if parents := testutil.Parents(t, dir, "HEAD"); len(parents) != 2 {
				t.Errorf("the replayed tip has %d parent(s), want 2 -- the merge was flattened", len(parents))
			}
			if out := testutil.GitOut(t, dir, "merge-base", "--is-ancestor", upstream, "HEAD"); out != "" {
				t.Errorf("the upstream is not an ancestor of the replayed branch: %s", out)
			}
		})
	}
}

// TestPickAndRevertSubsetRefusalsApplyToAPreviewToo: an option safegit's
// cherry-pick and revert do not implement is refused whether or not the run was
// going to happen, and nothing is recorded in the would-do log for it.
//
// The option exercised here is strategy SELECTION, which safegit's cherry-pick
// and revert do not implement at all. It is spelled long: on these two verbs
// `-s` is signoff, so `-s resolve` would read as an allowed flag followed by a
// second revision rather than as the unsupported option under test.
func TestPickAndRevertSubsetRefusalsApplyToAPreviewToo(t *testing.T) {
	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			dir, first, _ := newPickableRepo(t)
			target := "HEAD"
			if verb == "cherry-pick" {
				target = first
			}

			stdout, stderr, code := runSafegit(t, dir, "--dry-run", verb, "--strategy", "resolve", target)
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

// A FRAMEWORK-OWNED flag written after the command name is a different refusal
// from the subset law, and it has to say so.
//
// `--dry-run`, `--json`, `--quiet`, `--verbose` and `--approve-consequential`
// are the framework's, pre-scanned out of argv wherever they appear -- with two
// boundaries, a bare `--` and a PASSTHROUGH COMMAND'S NAME. After that name the
// rest of the command line is git's own vocabulary, handed to safegit's
// git-shaped parser, which measures it against the command's allowlist and
// finds a flag that is not in it.
//
// The refusal itself stays: the flag really is not part of this command's git
// vocabulary, and forwarding it to git would be worse. What was wrong is the
// REASON it gave -- the subset law, "safegit implements a deliberate subset of
// git", said about a flag safegit implements on every command it has. So the
// refusal now names the route instead: write the flag before the command name.
func TestAFrameworkFlagAfterTheCommandNameNamesTheRoute(t *testing.T) {
	for _, flag := range []string{"--dry-run", "--json", "--quiet", "--verbose", "--approve-consequential"} {
		for _, verb := range []string{"merge", "cherry-pick", "revert", "switch", "rebase", "reset", "bisect"} {
			t.Run(flag+" after "+verb, func(t *testing.T) {
				dir := newTwoCommitRepo(t)
				stdout, stderr, code := runSafegit(t, dir, verb, flag)
				if code != exitcode.Usage {
					t.Fatalf("safegit %s %s exited %d, want %d (Usage)\nstdout=%s\nstderr=%s",
						verb, flag, code, exitcode.Usage, stdout, stderr)
				}
				// The route, spelled the way it is typed.
				if !strings.Contains(stderr, "safegit "+flag+" "+verb) {
					t.Errorf("the refusal does not spell the pre-command form (safegit %s %s):\n%s", flag, verb, stderr)
				}
				if !strings.Contains(stderr, "before the command name") {
					t.Errorf("the refusal does not name the route:\n%s", stderr)
				}
				// And it does NOT blame the subset law for a flag safegit has.
				if strings.Contains(stderr, "deliberate subset of git") {
					t.Errorf("the refusal cites the subset law for a flag safegit supports:\n%s", stderr)
				}
			})
		}
	}
}

// The counterpart: a flag that really is outside the subset still refuses with
// the subset law. The route message is scoped to the framework's own names and
// takes nothing else with it.
func TestAnOptionOutsideTheSubsetStillCitesTheSubsetLaw(t *testing.T) {
	dir := newTwoCommitRepo(t)
	stderr := assertSubsetRefusal(t, dir, "merge", "--squash", "other")
	if strings.Contains(stderr, "before the command name") {
		t.Errorf("an option outside the subset was answered with the framework-flag route:\n%s", stderr)
	}
}
