package gitexec

import (
	"strings"
	"testing"
)

func TestSubcommandExtraction(t *testing.T) {
	cases := []struct {
		argv []string
		want string
		ok   bool
	}{
		{[]string{"status", "--porcelain"}, "status", true},
		{[]string{"git", "--no-optional-locks", "rev-parse", "HEAD"}, "rev-parse", true},
		{[]string{"--no-optional-locks", "ls-tree", "-r", "-z", "HEAD"}, "ls-tree", true},
		{[]string{"--version"}, "--version", true},
		{[]string{"-c", "core.hooksPath=/dev/null", "status"}, "status", true},
		// --attr-source in both spellings: the joined one is a single element
		// the scan skips, and the separate one takes the tree name as its value
		// so the tree can never be read as the subcommand.
		{[]string{"--attr-source=HEAD^{tree}", "check-attr", "-z", "--all", "--", "f"}, "check-attr", true},
		{[]string{"--attr-source", "HEAD^{tree}", "check-attr", "-z", "--all", "--", "f"}, "check-attr", true},
		{[]string{}, "", false},
		{[]string{"--no-optional-locks"}, "", false},
	}
	for _, c := range cases {
		got, ok := Subcommand(c.argv)
		if got != c.want || ok != c.ok {
			t.Errorf("Subcommand(%v) = (%q, %v), want (%q, %v)", c.argv, got, ok, c.want, c.ok)
		}
	}
}

func TestValidateAcceptsDeclaredVerbs(t *testing.T) {
	for _, v := range Verbs() {
		if err := Validate([]string{v.Name}); err != nil {
			t.Errorf("Validate refused the declared verb %q: %v", v.Name, err)
		}
	}
}

func TestValidateRefusesUndeclaredVerb(t *testing.T) {
	err := Validate([]string{"filter-branch"})
	if err == nil {
		t.Fatal("Validate accepted an undeclared subcommand")
	}
	if !strings.Contains(err.Error(), "classification table") {
		t.Errorf("the refusal must point at the table; got %v", err)
	}
}

func TestFlagConditionalEffects(t *testing.T) {
	cases := []struct {
		argv []string
		want Effect
	}{
		{[]string{"hash-object", "--", "f.txt"}, ObserveOnly},
		{[]string{"hash-object", "-w", "--", "f.txt"}, MutatesObjects},
		{[]string{"read-tree", "HEAD"}, MutatesIndex},
		{[]string{"read-tree", "--reset", "-u", "HEAD"}, MutatesIndex | MutatesWorktree},
		{[]string{"reset", "--soft", "HEAD~1"}, MutatesRefs | MutatesIndex},
		{[]string{"reset", "--hard", "HEAD"}, MutatesRefs | MutatesIndex | MutatesWorktree},
		{[]string{"reflog", "show", "--format=%H"}, ObserveOnly},
		{[]string{"reflog", "expire", "--expire=now", "--all"}, MutatesRefs},
		{[]string{"tag", "-l"}, ObserveOnly},
		{[]string{"tag", "-d", "v1"}, MutatesObjects | MutatesRefs},
		{[]string{"symbolic-ref", "HEAD"}, ObserveOnly},
		{[]string{"stash", "list", "--format=%H %gd"}, ObserveOnly},
		{[]string{"notes", "list"}, ObserveOnly},
		{[]string{"remote", "get-url", "origin"}, ObserveOnly},
		{[]string{"submodule", "foreach", "--quiet", "echo $sm_path"}, ObserveOnly},
	}
	for _, c := range cases {
		got, err := EffectsOf(c.argv)
		if err != nil {
			t.Errorf("EffectsOf(%v): %v", c.argv, err)
			continue
		}
		if got != c.want {
			t.Errorf("EffectsOf(%v) = %v, want %v", c.argv, got, c.want)
		}
	}
}

// TestWritesWorktreeView is the view the guarded passthroughs' uncommitted-work
// check reads. The reset and bisect rows are the reason it exists: both used to
// be re-derived by hand in the handlers, and both hand-written versions were
// wrong.
func TestWritesWorktreeView(t *testing.T) {
	writes := [][]string{
		{"reset", "--hard", "HEAD"},
		{"reset", "--merge", "HEAD~1"},
		{"reset", "--keep", "HEAD~1"},
		{"bisect", "start", "HEAD", "HEAD~5"},
		{"bisect", "good"},
		{"bisect", "bad"},
		{"bisect", "old"},
		{"bisect", "new"},
		{"bisect", "skip"},
		{"bisect", "run", "make", "test"},
		{"bisect", "replay", "log.txt"},
		{"bisect", "reset"},
		{"switch", "other"},
		{"merge", "topic"},
		{"rebase", "main"},
		{"cherry-pick", "abc1234"},
		{"revert", "abc1234"},
		{"read-tree", "--reset", "-u", "HEAD"},
	}
	for _, argv := range writes {
		if !WritesWorktree(argv) {
			t.Errorf("WritesWorktree(%v) = false, want true", argv)
		}
	}
	observes := [][]string{
		{"reset", "--soft", "HEAD~1"},
		{"reset", "HEAD~1"},
		{"bisect", "log"},
		{"bisect", "terms"},
		{"bisect", "view"},
		{"status", "--porcelain"},
		{"commit-tree", "abc", "-m", "x"},
		{"update-ref", "refs/heads/x", "abc", "def"},
	}
	for _, argv := range observes {
		if WritesWorktree(argv) {
			t.Errorf("WritesWorktree(%v) = true, want false", argv)
		}
	}
	// Default-deny, the WritesObjects precedent: an argv the table does not
	// declare is never assumed harmless.
	if !WritesWorktree([]string{"filter-branch", "--all"}) {
		t.Error("an undeclared argv must be treated as writing the working tree")
	}
}

// TestConditionalTokenNeverMatchesTheVerbItself pins the reason argvHas skips
// everything up to and including the subcommand: `git prune` and `git push`
// are verbs in their own right AND conditional tokens of other verbs.
func TestConditionalTokenNeverMatchesTheVerbItself(t *testing.T) {
	got, err := EffectsOf([]string{"prune", "--expire=now"})
	if err != nil {
		t.Fatalf("EffectsOf: %v", err)
	}
	if got != MutatesObjects {
		t.Errorf("EffectsOf(prune) = %v, want %v", got, MutatesObjects)
	}
	got, err = EffectsOf([]string{"push", "--force-with-lease", "origin", "HEAD"})
	if err != nil {
		t.Fatalf("EffectsOf: %v", err)
	}
	if got != MutatesRefs|Network {
		t.Errorf("EffectsOf(push) = %v, want %v", got, MutatesRefs|Network)
	}
}

// TestWritesObjectsView is the view Phase 3.1's object quarantine reads.
func TestWritesObjectsView(t *testing.T) {
	writes := [][]string{
		{"hash-object", "-w", "--stdin"},
		{"commit-tree", "abc", "-m", "x"},
		{"write-tree"},
		{"mktree"},
		{"add", "--", "f.txt"},
		{"fetch", "origin"},
		{"repack", "-a", "-d"},
	}
	for _, argv := range writes {
		if !WritesObjects(argv) {
			t.Errorf("WritesObjects(%v) = false, want true", argv)
		}
	}
	observes := [][]string{
		{"hash-object", "--", "f.txt"},
		{"rev-parse", "HEAD"},
		{"cat-file", "-p", "abc"},
		{"ls-tree", "-r", "HEAD"},
		{"update-ref", "refs/heads/x", "abc", "def"},
	}
	for _, argv := range observes {
		if WritesObjects(argv) {
			t.Errorf("WritesObjects(%v) = true, want false", argv)
		}
	}
}

// TestUnknownArgvIsNeverAssumedHarmless: the two views disagree on purpose for
// an argv the table does not declare.
func TestUnknownArgvIsNeverAssumedHarmless(t *testing.T) {
	argv := []string{"filter-branch", "--all"}
	if !WritesObjects(argv) {
		t.Error("an undeclared argv must be treated as writing objects")
	}
	if IsObserveOnly(argv) {
		t.Error("an undeclared argv must never be treated as observe-only")
	}
}

// TestObserveOnlyView is the view Phase 3.3's observe allowlist reads.
func TestObserveOnlyView(t *testing.T) {
	for _, argv := range [][]string{
		{"status", "--porcelain"},
		{"diff", "HEAD", "--name-status"},
		{"ls-files", "--others", "--exclude-standard"},
		{"for-each-ref", "--format=%(refname)"},
		{"merge-base", "--is-ancestor", "a", "b"},
		{"check-ignore", "-q", "--", "f"},
		// --no-index changes which answer check-ignore gives, never what it
		// does: both spellings only read.
		{"check-ignore", "--no-index", "-q", "--", "f"},
		// The conflict-attribute resolver's own two reads: check-attr answers
		// from a tree, stripspace filters a message on stdin.
		{"check-attr", "-z", "conflict-marker-size", "--", "f"},
		{"stripspace", "--strip-comments"},
	} {
		if !IsObserveOnly(argv) {
			t.Errorf("IsObserveOnly(%v) = false, want true", argv)
		}
	}
	for _, argv := range [][]string{
		{"ls-remote", "origin", "refs/heads/main"},
		{"update-index", "--skip-worktree", "f"},
		{"rm", "--cached", "--", "f"},
		// merge-file and config are declared by their WIDER spelling: -p sends
		// the merge result to stdout but the bare form overwrites a file, and
		// `config <name> <value>` sets without any option to key on. Neither
		// may become an allowlisted observe on the strength of how safegit
		// happens to spell it today.
		{"merge-file", "-p", "ours", "base", "theirs"},
		{"config", "--get", "merge.conflictStyle"},
	} {
		if IsObserveOnly(argv) {
			t.Errorf("IsObserveOnly(%v) = true, want false", argv)
		}
	}
}

// TestTableIsWellFormed keeps the single authority readable: no duplicate
// verbs, no empty names, and every conditional entry carries tokens, effects
// and a reason.
func TestTableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range Verbs() {
		if v.Name == "" {
			t.Error("a verb with an empty name is declared")
		}
		if seen[v.Name] {
			t.Errorf("verb %q is declared twice", v.Name)
		}
		seen[v.Name] = true
		for _, c := range v.Conditional {
			if len(c.Tokens) == 0 {
				t.Errorf("verb %q has a conditional effect with no tokens", v.Name)
			}
			if c.Effects == ObserveOnly {
				t.Errorf("verb %q has a conditional effect that adds nothing", v.Name)
			}
			if strings.TrimSpace(c.Why) == "" {
				t.Errorf("verb %q has a conditional effect with no reason", v.Name)
			}
		}
	}
}

func TestEffectString(t *testing.T) {
	if got := ObserveOnly.String(); got != "observe-only" {
		t.Errorf("ObserveOnly.String() = %q", got)
	}
	if got := (MutatesObjects | Network).String(); got != "objects|network" {
		t.Errorf("(objects|network).String() = %q", got)
	}
}
