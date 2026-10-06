package commit

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stricttools/safegit/internal/repo"
	"github.com/stricttools/safegit/internal/testutil"
)

// initBoundaryInner makes dir a repository with one commit; extra is appended
// to `git init`.
func initBoundaryInner(t *testing.T, dir string, extra ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, append([]string{"init", "-q", "--initial-branch=main"}, extra...)...)
	testutil.Git(t, dir, "config", "user.email", "test@test.com")
	testutil.Git(t, dir, "config", "user.name", "Test")
	testutil.WriteFile(t, dir, "f", "f\n")
	testutil.Git(t, dir, "add", "f")
	testutil.Git(t, dir, "commit", "-q", "-m", "inner")
}

func TestRepositoryBoundaries(t *testing.T) {
	dir, _, _ := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	initBoundaryInner(t, filepath.Join(dir, "nested"))
	initBoundaryInner(t, filepath.Join(dir, "a", "gitfile"), "--separate-git-dir="+filepath.Join(t.TempDir(), "gd"))
	initBoundaryInner(t, filepath.Join(dir, "recorded"))
	testutil.Git(t, dir, "-c", "advice.addEmbeddedRepo=false", "add", "recorded")
	testutil.Git(t, dir, "commit", "-q", "-m", "record a gitlink")
	// A gitlink with no checkout on disk: the directory is not even there.
	testutil.Git(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+testutil.Rev(t, dir, "HEAD")+",absent")
	testutil.Git(t, dir, "commit", "-q", "-m", "record an absent gitlink")
	testutil.WriteFile(t, dir, ".gitmodules", "[submodule \"reg\"]\n\tpath = deep/reg\n\turl = ./nowhere\n")
	testutil.WriteFile(t, dir, "fake/.git", "")
	testutil.WriteFile(t, dir, "fake/x", "x\n")
	testutil.WriteFile(t, dir, "plain/x", "x\n")
	if err := os.Symlink(filepath.Join(dir, "nested"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}

	b := NewRepositoryBoundaries(ctx, dir, "refs/heads/main")

	above := []struct {
		rel   string
		dir   string
		shape BoundaryShape
	}{
		{"nested/f", "nested", NestedRepository},
		{"nested/d/z", "nested", NestedRepository},
		{"nested/", "nested", NestedRepository},
		{"a/gitfile/f", "a/gitfile", NestedRepository},
		{"recorded/f", "recorded", RecordedGitlink},
		{"absent/f", "absent", RecordedGitlink},
		{"deep/reg/f", "deep/reg", RegisteredSubmodule},
		{"nested", "", 0},
		{"recorded", "", 0},
		{"fake/x", "", 0},
		{"plain/x", "", 0},
		{"seed.txt", "", 0},
		{"", "", 0},
		// Beyond a link the disk is not this repository's; LinkAbove answers.
		{"link/f", "", 0},
	}
	for _, c := range above {
		bd, ok, err := b.Above(c.rel)
		if err != nil {
			t.Fatalf("Above(%q): %v", c.rel, err)
		}
		if ok != (c.dir != "") || bd.Dir != c.dir || (ok && bd.Shape != c.shape) {
			t.Errorf("Above(%q) = %+v, %v; want dir %q shape %d", c.rel, bd, ok, c.dir, c.shape)
		}
	}

	at := map[string]bool{
		"nested": true, "a/gitfile": true, "recorded": true, "absent": true, "deep/reg": true,
		"fake": false, "plain": false, "a": false, "link": false,
	}
	for rel, want := range at {
		if _, ok, err := b.At(rel); err != nil || ok != want {
			t.Errorf("At(%q) = %v, %v; want %v", rel, ok, err, want)
		}
	}

	for rel, want := range map[string]bool{"nested": true, "a/gitfile": true, "recorded": false, "fake": false, "plain/x": false} {
		if got, err := b.unrecordedRepositoryAt(rel); err != nil || got != want {
			t.Errorf("unrecordedRepositoryAt(%q) = %v, %v; want %v", rel, got, err, want)
		}
	}

	if bd, _, _ := b.Above("absent/f"); bd.Describe() != "a submodule recorded as a gitlink in refs/heads/main" {
		t.Errorf("Describe() = %q", bd.Describe())
	}
	if out, err := b.CheckedOut(RepositoryBoundary{Dir: "absent", Shape: RecordedGitlink}); err != nil || out {
		t.Errorf("CheckedOut(absent) = %v, %v; want false", out, err)
	}
	if out, err := b.CheckedOut(RepositoryBoundary{Dir: "recorded", Shape: RecordedGitlink}); err != nil || !out {
		t.Errorf("CheckedOut(recorded) = %v, %v; want true", out, err)
	}
}

func TestShellQuoteAndBoundaryCommitArgs(t *testing.T) {
	for in, want := range map[string]string{
		"plain/path.go": "plain/path.go",
		"two words":     "'two words'",
		"it's":          `'it'\''s'`,
		"":              "''",
		"$HOME":         "'$HOME'",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
	got := boundaryCommitArgs([]boundaryArg{
		{rel: "a b"},
		{rel: "h", hunks: []int{1, 3}},
		{rel: "u", untrack: true},
		{rel: "c"},
	})
	want := "commit -m <message> --hunks h:1,3 --untrack u -- 'a b' c"
	if got != want {
		t.Errorf("boundaryCommitArgs = %s\nwant                 %s", got, want)
	}
}
