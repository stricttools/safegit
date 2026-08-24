package main

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/gitexec"
)

// runGitMutation returns 0 on its --dry-run branch without reading the
// Completed's exit code, because in a dry run strictcli records the invocation
// instead of performing it and the carrier it hands back is unsettled -- asking
// it for an exit code panics. The framework exposes no settled-ness (Completed's
// field is unexported, its accessors panic rather than report, and
// Effects.Recorded() claims the would-do render as a side effect), so the guard
// has to key off safegit's own flag.
//
// That is correct only while no allowlisted prefix can match a runGitMutation
// argv. strictcli's Run takes an OBSERVE branch for any argv matching a prefix
// in the app-level proc-observe allowlist, and that branch EXECUTES the child
// even in dry mode and returns a settled Completed. An allowlist admitting a
// prefix a runGitMutation argv matched would therefore run git for real under
// --dry-run while runGitMutation reported 0 and threw git's own verdict away.
//
// This test binds the declared allowlist to that requirement, from both ends:
// every prefix must name a verb the classification table declares observe-only,
// and no prefix may match any argv runGitMutation builds.
func TestObserveAllowlistCannotAdmitAMutation(t *testing.T) {
	prefixes := gitexec.ObservePrefixes()
	if len(prefixes) == 0 {
		t.Fatal("the observe allowlist is empty, so this test proves nothing about what it admits")
	}

	// End one: every prefix is a read, by the table's own reading.
	for _, prefix := range prefixes {
		if len(prefix) < 2 {
			t.Errorf("prefix %v has fewer than two elements; a one-element prefix of just the binary matches every git invocation safegit makes", prefix)
			continue
		}
		if prefix[0] != gitexec.Binary {
			t.Errorf("prefix %v does not start with the git binary, so it can never match an effects-handle argv", prefix)
		}
		if !containsElement(prefix, "--no-optional-locks") {
			t.Errorf("prefix %v omits the global prefix element that every argv safegit builds carries", prefix)
		}
		if !gitexec.IsObserveOnly(prefix) {
			t.Errorf("prefix %v is allowlisted as an observe, but the classification table does not declare it observe-only", prefix)
		}
		if gitexec.WritesObjects(prefix) {
			t.Errorf("prefix %v is allowlisted as an observe but can write objects", prefix)
		}
	}

	// End two: the argv runGitMutation actually builds. The verbs are the ones
	// its callers pass -- switch, pull's fetch and merge, merge, rebase,
	// reset, bisect, and the recorded dry runs of cherry-pick and revert.
	for _, args := range [][]string{
		{"switch", "other"},
		{"fetch", "origin", "main"},
		{"merge", "--ff-only", "FETCH_HEAD"},
		{"merge", "topic"},
		{"rebase", "main"},
		{"reset", "--hard", "HEAD~1"},
		{"bisect", "start"},
		{"cherry-pick", "abc1234"},
		{"revert", "abc1234"},
	} {
		argv, err := gitexec.ArgvAny(gitexec.ExemptGitMutation, args...)
		if err != nil {
			t.Fatalf("building the argv for %v: %v", args, err)
		}
		elements := make([]string, len(argv))
		for i, a := range argv {
			elements[i] = a.(string)
		}
		if prefix, matched := matchingPrefix(prefixes, elements); matched {
			t.Errorf("`git %s` matches the allowlisted observe prefix %v: it would EXECUTE during a --dry-run "+
				"while runGitMutation's dry-run branch reported 0 and discarded git's exit code",
				strings.Join(args, " "), prefix)
		}
	}
}

// containsElement reports whether the list holds the element.
func containsElement(list []string, want string) bool {
	for _, e := range list {
		if e == want {
			return true
		}
	}
	return false
}

// matchingPrefix applies the framework's own matching rule: element-wise string
// equality against the leading elements of the argv.
func matchingPrefix(prefixes [][]string, argv []string) ([]string, bool) {
	for _, prefix := range prefixes {
		if len(prefix) > len(argv) {
			continue
		}
		match := true
		for i := range prefix {
			if argv[i] != prefix[i] {
				match = false
				break
			}
		}
		if match {
			return prefix, true
		}
	}
	return nil, false
}

// TestObserveAllowlistExcludesConditionallyMutatingVerbs pins the generation
// rule that keeps the list safe. `reflog` and `tag` read until a subcommand or
// option follows them, and the allowlist matches a PREFIX -- so allowlisting
// them would authorize `reflog expire --expire=now --all` (which a rewrite
// preview records) to execute in the middle of a dry run.
func TestObserveAllowlistExcludesConditionallyMutatingVerbs(t *testing.T) {
	prefixes := gitexec.ObservePrefixes()
	for _, verb := range []string{"reflog", "tag", "notes", "remote", "stash", "submodule", "symbolic-ref", "hash-object"} {
		for _, prefix := range prefixes {
			if prefix[len(prefix)-1] == verb {
				t.Errorf("%q is allowlisted as an observe, but the table gives it conditional effects: a prefix ending there admits the mutating spellings too", verb)
			}
		}
	}
}
