package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/testutil"
)

// A path inside another git repository is not this repository's to record.
// git's own answers to such a path are a silent no-op (`git add` of a file in
// an untracked nested repository exits 0 and stages nothing), a raw "is in
// submodule" fatal, or -- for a move into a submodule -- a commit that deletes
// the gitlink. safegit refuses every such named path itself, naming the
// boundary, before anything is staged or moved.
//
// The four shapes a boundary takes on disk and in the tree:
//
//   - nested: an untracked repository with a .git directory
//   - gitlink: the same repository, recorded as a gitlink with no .gitmodules
//   - submodule: a registered submodule, whose .git is a file
//   - gitfile: an untracked repository whose .git is a file pointing elsewhere

type boundaryFixture struct {
	shape string
	// inner is the boundary directory, repo-relative.
	inner string
	// describes is the shape's wording in the refusal.
	describes string
	// bumpsParent marks the shapes git reports a superproject for, where a
	// commit inside goes through safegit's commit.autoBumpParent decision.
	bumpsParent bool
}

var boundaryFixtures = []boundaryFixture{
	{shape: "nested", inner: "nested", describes: "a separate git repository (it has its own .git)"},
	{shape: "gitlink", inner: "nested", describes: "a submodule recorded as a gitlink in refs/heads/main", bumpsParent: true},
	{shape: "submodule", inner: "sub", describes: "a submodule recorded as a gitlink in refs/heads/main", bumpsParent: true},
	{shape: "gitfile", inner: "inner", describes: "a separate git repository (it has its own .git)"},
}

// initInnerRepository makes dir a repository with one committed file,
// "tracked". extraInit is appended to `git init` (a --separate-git-dir).
func initInnerRepository(t *testing.T, dir string, extraInit ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, dir, append([]string{"init", "-q", "--initial-branch=main"}, extraInit...)...)
	configureIdentity(t, dir)
	testutil.WriteFile(t, dir, "tracked", "n0\n")
	testutil.Git(t, dir, "add", "tracked")
	testutil.Git(t, dir, "commit", "-q", "-m", "inner initial")
}

func configureIdentity(t *testing.T, dir string) {
	t.Helper()
	testutil.Git(t, dir, "config", "user.email", "test@test.com")
	testutil.Git(t, dir, "config", "user.name", "Test")
}

// newBoundaryRepo builds the outer repository with one boundary of the given
// shape, then leaves three kinds of change inside it: a new file (n), an edit
// to its tracked file (tracked), and a new file in a new directory (d/z).
func newBoundaryRepo(t *testing.T, f boundaryFixture) string {
	t.Helper()
	dir := newRepo(t)
	innerAbs := filepath.Join(dir, f.inner)
	switch f.shape {
	case "nested":
		initInnerRepository(t, innerAbs)
	case "gitlink":
		initInnerRepository(t, innerAbs)
		testutil.Git(t, dir, "-c", "advice.addEmbeddedRepo=false", "add", f.inner)
		testutil.Git(t, dir, "commit", "-q", "-m", "record the gitlink")
	case "submodule":
		origin := filepath.Join(evalTempDir(t), "origin")
		initInnerRepository(t, origin)
		testutil.Git(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", origin, f.inner)
		testutil.Git(t, dir, "commit", "-q", "-m", "add the submodule")
		configureIdentity(t, innerAbs)
	case "gitfile":
		gitDir := filepath.Join(evalTempDir(t), "inner.git")
		initInnerRepository(t, innerAbs, "--separate-git-dir="+gitDir)
	default:
		t.Fatalf("unknown boundary shape %q", f.shape)
	}
	testutil.WriteFile(t, dir, f.inner+"/n", "new\n")
	testutil.WriteFile(t, dir, f.inner+"/tracked", "n0\nmod\n")
	testutil.WriteFile(t, dir, f.inner+"/d/z", "z\n")
	return dir
}

// boundaryState is what a refused command must leave exactly as it was.
type boundaryState struct {
	head  string
	stage string
}

func captureBoundaryState(t *testing.T, dir, inner string) boundaryState {
	t.Helper()
	return boundaryState{
		head:  testutil.Rev(t, dir, "HEAD"),
		stage: testutil.GitOut(t, dir, "ls-files", "--stage", "--", inner),
	}
}

func (s boundaryState) assertUnchanged(t *testing.T, dir, inner, what string) {
	t.Helper()
	after := captureBoundaryState(t, dir, inner)
	if after.head != s.head {
		t.Errorf("%s: HEAD moved from %s to %s", what, s.head, after.head)
	}
	if after.stage != s.stage {
		t.Errorf("%s: the index entry for %s changed from %q to %q", what, inner, s.stage, after.stage)
	}
}

func TestCommitRefusesPathsInsideAnotherRepository(t *testing.T) {
	for _, f := range boundaryFixtures {
		f := f
		cases := []struct {
			name string
			args []string
		}{
			{"new file", []string{"commit", "-m", "x", "--", f.inner + "/n"}},
			{"tracked file", []string{"commit", "-m", "x", "--", f.inner + "/tracked"}},
			{"file in a directory", []string{"commit", "-m", "x", "--", f.inner + "/d/z"}},
			{"directory", []string{"commit", "-m", "x", "--", f.inner + "/d"}},
			{"mixed with a change of this repository", []string{"commit", "-m", "x", "--", "seed.txt", f.inner + "/n"}},
			{"amend", []string{"commit", "--amend", "-m", "x", "--", f.inner + "/n"}},
			{"hunks", []string{"commit", "-m", "x", "--hunks", f.inner + "/tracked:1"}},
			{"untrack", []string{"commit", "-m", "x", "--untrack", f.inner + "/tracked"}},
		}
		for _, c := range cases {
			c := c
			t.Run(f.shape+"/"+c.name, func(t *testing.T) {
				dir := newBoundaryRepo(t, f)
				testutil.WriteFile(t, dir, "seed.txt", "seed changed\n")
				before := captureBoundaryState(t, dir, f.inner)

				_, stderr, code := runSafegit(t, dir, c.args...)
				if code != exitcode.PathMatchedNothing {
					t.Fatalf("exit %d, want %d (PathMatchedNothing): %s", code, exitcode.PathMatchedNothing, stderr)
				}
				if !strings.Contains(stderr, f.inner+" is "+f.describes) {
					t.Errorf("refusal does not name the boundary %q as %q:\n%s", f.inner, f.describes, stderr)
				}
				before.assertUnchanged(t, dir, f.inner, c.name)
			})
		}
	}
}

// A refusal that groups two boundaries names every argument under the
// boundary it lies in, and offers each its own command.
func TestCommitBoundaryRefusalGroupsOffendersByBoundary(t *testing.T) {
	dir := newBoundaryRepo(t, boundaryFixtures[2]) // sub
	initInnerRepository(t, filepath.Join(dir, "nested"))
	testutil.WriteFile(t, dir, "nested/n", "new\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "sub/n", "nested/n", "sub/d/z")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	for _, want := range []string{
		"sub is a submodule recorded as a gitlink in refs/heads/main; named here as sub/n, sub/d/z",
		"(cd " + filepath.Join(dir, "sub") + " && safegit commit -m <message> -- n d/z)",
		"nested is a separate git repository (it has its own .git); named here as nested/n",
		"(cd " + filepath.Join(dir, "nested") + " && safegit commit -m <message> -- n)",
		"or leave them out of this commit",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("refusal lacks %q:\n%s", want, stderr)
		}
	}
}

// Naming an untracked nested repository's directory itself used to record an
// embedded gitlink with no .gitmodules entry, silently. It is a separate
// repository, not a submodule, and it is refused.
func TestCommitNamingANestedRepositoryItselfIsRefused(t *testing.T) {
	for _, f := range []boundaryFixture{boundaryFixtures[0], boundaryFixtures[3]} {
		for _, spelling := range []string{f.inner, f.inner + "/"} {
			t.Run(f.shape+"/"+spelling, func(t *testing.T) {
				dir := newBoundaryRepo(t, f)
				before := captureBoundaryState(t, dir, f.inner)
				_, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", spelling)
				if code != exitcode.PathMatchedNothing {
					t.Fatalf("exit %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
				}
				for _, want := range []string{f.inner + " is " + f.describes, "not a submodule", ".gitignore"} {
					if !strings.Contains(stderr, want) {
						t.Errorf("refusal lacks %q:\n%s", want, stderr)
					}
				}
				before.assertUnchanged(t, dir, f.inner, "commit -- "+spelling)
			})
		}
	}
}

func TestDeclaredMoveAcrossARepositoryBoundaryIsRefused(t *testing.T) {
	for _, f := range boundaryFixtures {
		f := f
		t.Run(f.shape+"/out of", func(t *testing.T) {
			dir := newBoundaryRepo(t, f)
			testutil.WriteFile(t, dir, "moved", "n0\n")
			before := captureBoundaryState(t, dir, f.inner)
			_, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--moved", f.inner+"/tracked -> moved", "--", "moved")
			if code != exitcode.MoveNotBorneOut {
				t.Fatalf("exit %d, want %d (MoveNotBorneOut): %s", code, exitcode.MoveNotBorneOut, stderr)
			}
			if !strings.Contains(stderr, f.inner+" is "+f.describes) {
				t.Errorf("refusal does not name the boundary:\n%s", stderr)
			}
			before.assertUnchanged(t, dir, f.inner, "--moved out of")
		})
		t.Run(f.shape+"/into", func(t *testing.T) {
			dir := newBoundaryRepo(t, f)
			if err := os.Rename(filepath.Join(dir, "seed.txt"), filepath.Join(dir, f.inner, "seed.txt")); err != nil {
				t.Fatal(err)
			}
			before := captureBoundaryState(t, dir, f.inner)
			_, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--moved", "seed.txt -> "+f.inner+"/seed.txt", "--", "seed.txt")
			if code != exitcode.MoveNotBorneOut {
				t.Fatalf("exit %d, want %d (MoveNotBorneOut): %s", code, exitcode.MoveNotBorneOut, stderr)
			}
			if !strings.Contains(stderr, f.inner+" is "+f.describes) {
				t.Errorf("refusal does not name the boundary:\n%s", stderr)
			}
			before.assertUnchanged(t, dir, f.inner, "--moved into")
		})
	}
}

// boundaryFixCommand pulls the printed subshell out of a refusal and fills in
// the message placeholder.
func boundaryFixCommand(t *testing.T, stderr, inner string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		i := strings.Index(line, "(cd ")
		if i < 0 || !strings.Contains(line, inner) {
			continue
		}
		cmd := strings.TrimSpace(line[i:])
		if !strings.Contains(cmd, "<message>") {
			t.Fatalf("the printed command has no message placeholder: %s", cmd)
		}
		return strings.Replace(cmd, "<message>", "'commit it where it belongs'", 1)
	}
	t.Fatalf("the refusal prints no subshell command for %s:\n%s", inner, stderr)
	return ""
}

// runPrintedCommand runs a printed command line through bash, from the outer
// repository, with this build of safegit first on PATH.
func runPrintedCommand(t *testing.T, dir, line string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", line)
	cmd.Dir = dir
	cmd.Env = controlledEnv(t, "PATH="+filepath.Dir(safegitBin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return string(out), exitErr.ExitCode()
		}
		t.Fatalf("running %q: %v", line, err)
	}
	return string(out), 0
}

// Both ways forward the refusal names have to work as printed: the subshell
// commits the paths in the repository they belong to, and the same commit
// without them goes through.
func TestCommitBoundaryRefusalFixInstructionsWork(t *testing.T) {
	for _, f := range boundaryFixtures {
		f := f
		t.Run(f.shape, func(t *testing.T) {
			dir := newBoundaryRepo(t, f)
			testutil.WriteFile(t, dir, "seed.txt", "seed changed\n")
			_, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "seed.txt", f.inner+"/n", f.inner+"/d/z")
			if code != exitcode.PathMatchedNothing {
				t.Fatalf("exit %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
			}
			line := boundaryFixCommand(t, stderr, f.inner)
			innerAbs := filepath.Join(dir, f.inner)
			innerHead := testutil.Rev(t, innerAbs, "HEAD")

			out, code := runPrintedCommand(t, dir, line)
			if f.bumpsParent {
				// The submodule shapes stop at safegit's own decision about
				// the parent's gitlink; its fix is printed in its refusal.
				if code == 0 || !strings.Contains(out, "commit.autoBumpParent not configured") {
					t.Fatalf("%s: exit %d, want the commit.autoBumpParent refusal:\n%s", line, code, out)
				}
				if _, stderr, code := runSafegit(t, dir, "config", "set", "commit.autoBumpParent", "true"); code != 0 {
					t.Fatalf("config set failed (%d): %s", code, stderr)
				}
				out, code = runPrintedCommand(t, dir, line)
			}
			if code != 0 {
				t.Fatalf("%s: exit %d:\n%s", line, code, out)
			}
			if testutil.Rev(t, innerAbs, "HEAD") == innerHead {
				t.Fatalf("%s: the inner repository's HEAD did not move:\n%s", line, out)
			}
			paths := testutil.TreePaths(t, innerAbs, "HEAD")
			for _, want := range []string{"n", "d/z"} {
				if !testutil.Contains(paths, want) {
					t.Errorf("inner HEAD lacks %s: %v", want, paths)
				}
			}

			if _, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "seed.txt"); code != 0 {
				t.Fatalf("the commit without the inner paths failed (%d): %s", code, stderr)
			}
		})
	}
}

// A boundary whose path needs shell quoting is printed quoted, and the line
// still works when run as printed.
func TestCommitBoundaryRefusalQuotesPathsForTheShell(t *testing.T) {
	dir := newRepo(t)
	initInnerRepository(t, filepath.Join(dir, "two words"))
	testutil.WriteFile(t, dir, "two words/it's here", "x\n")

	_, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "two words/it's here")
	if code != exitcode.PathMatchedNothing {
		t.Fatalf("exit %d, want %d: %s", code, exitcode.PathMatchedNothing, stderr)
	}
	want := "(cd '" + filepath.Join(dir, "two words") + "' && safegit commit -m <message> -- 'it'\\''s here')"
	if !strings.Contains(stderr, want) {
		t.Fatalf("refusal lacks the quoted command %q:\n%s", want, stderr)
	}
	line := boundaryFixCommand(t, stderr, "two words")
	if out, code := runPrintedCommand(t, dir, line); code != 0 {
		t.Fatalf("%s: exit %d:\n%s", line, code, out)
	}
	if !testutil.Contains(testutil.TreePaths(t, filepath.Join(dir, "two words"), "HEAD"), "it's here") {
		t.Fatalf("the printed command did not commit the file in the inner repository")
	}
}

// The refusal is for NAMED paths. A directory argument above a nested
// repository still expands around it, as it always has: the nested
// repository's files are skipped, and the rest of the directory commits.
func TestDirectoryExpansionStillSkipsNestedRepositories(t *testing.T) {
	for _, f := range boundaryFixtures {
		f := f
		t.Run(f.shape+"/root", func(t *testing.T) {
			dir := newBoundaryRepo(t, f)
			testutil.WriteFile(t, dir, "seed.txt", "seed changed\n")
			before := testutil.GitOut(t, dir, "ls-tree", "HEAD", "--", f.inner)
			if _, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "."); code != 0 {
				t.Fatalf("commit -- . failed (%d): %s", code, stderr)
			}
			if got := testutil.GitOut(t, dir, "ls-tree", "HEAD", "--", f.inner); got != before {
				t.Errorf("commit -- . changed the entry for %s from %q to %q", f.inner, before, got)
			}
			for _, p := range testutil.TreePaths(t, dir, "HEAD") {
				if strings.HasPrefix(p, f.inner+"/") {
					t.Errorf("commit -- . recorded %s from inside the boundary", p)
				}
			}
		})
	}
	t.Run("parent directory", func(t *testing.T) {
		dir := newRepo(t)
		initInnerRepository(t, filepath.Join(dir, "a", "nested"))
		testutil.WriteFile(t, dir, "a/nested/n", "new\n")
		testutil.WriteFile(t, dir, "a/f", "f\n")
		if _, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "a"); code != 0 {
			t.Fatalf("commit -- a failed (%d): %s", code, stderr)
		}
		paths := testutil.TreePaths(t, dir, "HEAD")
		if !testutil.Contains(paths, "a/f") {
			t.Errorf("commit -- a did not record a/f: %v", paths)
		}
		for _, p := range paths {
			if p == "a/nested" || strings.HasPrefix(p, "a/nested/") {
				t.Errorf("commit -- a recorded %s from the nested repository", p)
			}
		}
	})
}

// git's own test decides what a repository is, in a named path's check and in
// a directory expansion alike: a directory whose .git is an empty file is not
// one, so its files belong to this repository both ways.
func TestADirectoryWithAnInvalidDotGitIsNotABoundary(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "fake/.git", "")
	testutil.WriteFile(t, dir, "fake/f", "f\n")
	testutil.WriteFile(t, dir, "other/.git", "")
	testutil.WriteFile(t, dir, "other/g", "g\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "fake/f"); code != 0 {
		t.Fatalf("commit -- fake/f failed (%d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "x", "--", "other"); code != 0 {
		t.Fatalf("commit -- other failed (%d): %s", code, stderr)
	}
	paths := testutil.TreePaths(t, dir, "HEAD")
	for _, want := range []string{"fake/f", "other/g"} {
		if !testutil.Contains(paths, want) {
			t.Errorf("HEAD lacks %s: %v", want, paths)
		}
	}
}
