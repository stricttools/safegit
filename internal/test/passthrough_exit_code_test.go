package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// A guarded wrapper around a git command owns exactly two things: the checks it
// runs before git starts, and the record it writes after git finishes. The exit
// code in between is git's verdict, and safegit passes it on unchanged.
//
// This used to be false for every command routed through runGitMutation
// (switch, pull, merge, rebase, reset, bisect): a nonzero child was collapsed
// into a single hardcoded 1, so a caller could not tell git's "these refs do
// not merge" (1) from git's "that argument is not a ref at all" (128). Scripts
// that branch on git's codes read the wrong answer.
//
// Each case runs the identical command line in two identical repositories --
// plain git in one, safegit in the other -- and requires the same exit code
// from both. Nothing here pins a number, so the test keeps meaning whatever
// codes the installed git chooses; what it does require is that the failing
// cases collectively produce at least one code that is neither 0 nor 1, since
// only such a case can tell propagation apart from the old hardcoded 1.

func TestPassthroughExitCodeIsGits(t *testing.T) {
	cases := []struct {
		what        string
		argv        []string
		wantFailure bool
	}{
		// runGitMutation seam: the six commands whose exit code was hardcoded.
		{"switch to a branch that does not exist", []string{"switch", "no-such-ref"}, true},
		{"merge of a ref that does not exist", []string{"merge", "no-such-ref"}, true},
		{"rebase onto an upstream that does not exist", []string{"rebase", "no-such-upstream"}, true},
		{"hard reset to a ref that does not exist", []string{"reset", "--hard", "no-such-ref"}, true},
		{"bisect marked against a ref that does not exist", []string{"bisect", "bad", "no-such-ref"}, true},
		// runPassthrough seam: a guarded passthrough, which already forwarded
		// git's code. Kept here so both seams are pinned by the same rule.
		{"cherry-pick of a commit that does not exist", []string{"cherry-pick", "no-such-commit"}, true},
		// A command that succeeds must still agree: propagation is not "always
		// nonzero".
		{"switch to the branch already switched to", []string{"switch", "main"}, false},
	}

	var failingCodes []int

	for _, tc := range cases {
		t.Run(tc.what, func(t *testing.T) {
			gitRepo := newRepo(t)
			sgRepo := newRepo(t)

			gitOut, gitCode := testutil.GitTry(t, gitRepo, tc.argv...)
			_, sgErr, sgCode := runSafegit(t, sgRepo, tc.argv...)

			if tc.wantFailure && gitCode == 0 {
				t.Fatalf("fixture is wrong: plain git %s succeeded: %s",
					strings.Join(tc.argv, " "), oneLine(gitOut))
			}
			if !tc.wantFailure && gitCode != 0 {
				t.Fatalf("fixture is wrong: plain git %s failed (%d): %s",
					strings.Join(tc.argv, " "), gitCode, oneLine(gitOut))
			}
			if sgCode != gitCode {
				t.Fatalf("safegit %s exited %d; plain git exited %d.\n  git said:     %s\n  safegit said: %s",
					strings.Join(tc.argv, " "), sgCode, gitCode, oneLine(gitOut), oneLine(sgErr))
			}
			t.Logf("%s: both exited %d", strings.Join(tc.argv, " "), gitCode)
			if tc.wantFailure {
				failingCodes = append(failingCodes, gitCode)
			}
		})
	}

	distinguishing := false
	for _, c := range failingCodes {
		if c != 0 && c != 1 {
			distinguishing = true
		}
	}
	if !distinguishing {
		t.Errorf("every failing case exited 1 (%v), so this test cannot tell git's code from safegit's old hardcoded 1; pick a case whose git exit code differs", failingCodes)
	}
}
