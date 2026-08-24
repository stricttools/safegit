package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// FETCH_HEAD is the one argument that is not one side.
//
// Every other revision an operator can type resolves to a single commit, so
// counting the revisions on the command line answers "how many sides is this
// merge". FETCH_HEAD does not: git expands it into EVERY branch the fetch
// marked for merging, so `safegit merge FETCH_HEAD` after `git fetch origin br1
// br2` is one token asking for an octopus -- and an octopus is outside
// safegit's subset, refused by name when it is spelled as two arguments.
//
// Before the refusal below, the escape produced two distinct wrong answers on
// the two arms of the merge:
//
//   - the MERGE arm authored a three-parent commit through the pipeline, with
//     safegit's own trailers on it, from a command line whose octopus spelling
//     is a refusal;
//   - the FAST-FORWARD arm silently dropped every head but the first: safegit
//     resolves FETCH_HEAD to a commit to answer the ancestry question, and
//     `git rev-parse FETCH_HEAD` reads the first line of the file. The branch
//     moved onto one fetched head and the operator was told the merge
//     succeeded.
//
// The refusal is therefore read from the FILE and made BEFORE the ancestry
// question is asked, so both arms are covered by one check -- and `safegit
// pull`, whose merge step is this same one with FETCH_HEAD as its incoming
// side, inherits it.
//
// What must NOT be refused is the ordinary case, and it is the reason the check
// counts for-merge lines rather than lines: a stock refspec fetch writes one
// line per remote branch and marks all but the current branch's upstream
// `not-for-merge`, so an everyday `safegit pull` faces a FETCH_HEAD of several
// lines and exactly one side.

var fetchHeadSession = []string{"CLAUDE_CODE_SESSION_ID=merge-fetch-head-test"}

// newFetchedHeadsRepo builds a repository whose origin carries two branches off
// a shared base, fetches the ones named, and leaves main either diverged from
// them (so a merge cannot fast-forward) or exactly at the base (so it can).
func newFetchedHeadsRepo(t *testing.T, diverged bool, fetch ...string) string {
	t.Helper()
	dir, _ := newRepoWithRemote(t)

	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommitEnv(t, dir, fetchHeadSession, "base", "base.txt")
	testutil.Git(t, dir, "push", "origin", "main")

	testutil.Git(t, dir, "switch", "-c", "br1")
	testutil.WriteFile(t, dir, "one.txt", "one\n")
	safegitCommitEnv(t, dir, fetchHeadSession, "one side", "one.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.Git(t, dir, "switch", "-c", "br2")
	testutil.WriteFile(t, dir, "two.txt", "two\n")
	safegitCommitEnv(t, dir, fetchHeadSession, "the other side", "two.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.Git(t, dir, "push", "origin", "br1", "br2")

	// The two fetched branches keep no local names: what the merge is given is
	// the fetch's own record of them and nothing else.
	testutil.Git(t, dir, "branch", "-D", "br1")
	testutil.Git(t, dir, "branch", "-D", "br2")

	if diverged {
		testutil.WriteFile(t, dir, "local.txt", "local\n")
		safegitCommitEnv(t, dir, fetchHeadSession, "the local side", "local.txt")
	}

	testutil.Git(t, dir, append([]string{"fetch", "origin"}, fetch...)...)
	return dir
}

// forMergeLines returns the FETCH_HEAD lines the fetch marked for merging, so a
// fixture can state what it built rather than assume it.
func forMergeLines(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "FETCH_HEAD"))
	if err != nil {
		t.Fatalf("reading .git/FETCH_HEAD: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if fields := strings.Split(line, "\t"); len(fields) > 1 && fields[1] == "not-for-merge" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// assertOctopusRefusal is the shared verdict: safegit refused, said why in
// words the operator can act on, and left the repository exactly where it was.
func assertOctopusRefusal(t *testing.T, dir, stdout, stderr string, code, want int, before string) {
	t.Helper()
	if code != want {
		t.Fatalf("exited %d, want %d\nstdout=%s\nstderr=%s", code, want, stdout, stderr)
	}
	for _, says := range []string{"FETCH_HEAD", "octopus", "divergences.md"} {
		if !strings.Contains(stderr, says) {
			t.Errorf("the refusal does not say %q:\n%s", says, stderr)
		}
	}
	if head := testutil.Rev(t, dir, "HEAD"); head != before {
		t.Errorf("the refused merge moved HEAD to %s (was %s)", head, before)
	}
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refused merge left the working tree dirty:\n%s", status)
	}
	assertNoSequencerResidue(t, dir, "refused FETCH_HEAD octopus")
}

// TestMergeFetchHeadWithTwoForMergeHeadsIsRefused is the loud arm: main has
// moved on, so the merge cannot fast-forward and used to be CONCLUDED -- one
// pipeline-authored commit with three parents, out of a command line that says
// octopus in a spelling safegit's own refusal does not read.
func TestMergeFetchHeadWithTwoForMergeHeadsIsRefused(t *testing.T) {
	dir := newFetchedHeadsRepo(t, true, "br1", "br2")
	if n := len(forMergeLines(t, dir)); n != 2 {
		t.Fatalf("the fixture must fetch two branches marked for merging, got %d", n)
	}
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, fetchHeadSession, "merge", "FETCH_HEAD")
	assertOctopusRefusal(t, dir, stdout, stderr, code, exitcode.Usage, before)

	// The parent count is the defect stated directly: nothing with three
	// parents may come out of a merge safegit performed.
	if head := testutil.Rev(t, dir, "HEAD"); head == before {
		if parents := testutil.Parents(t, dir, head); len(parents) > 2 {
			t.Errorf("HEAD carries %d parents: %v", len(parents), parents)
		}
	}
}

// TestMergeFetchHeadOctopusIsRefusedBeforeTheFastForward is the silent arm, and
// the reason the check is made before the ancestry question rather than after
// it: main is strictly behind the FIRST fetched head, so safegit used to
// fast-forward onto that one and report success, with the second fetched head
// dropped without a word.
func TestMergeFetchHeadOctopusIsRefusedBeforeTheFastForward(t *testing.T) {
	dir := newFetchedHeadsRepo(t, false, "br1", "br2")
	before := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, fetchHeadSession, "merge", "FETCH_HEAD")
	assertOctopusRefusal(t, dir, stdout, stderr, code, exitcode.Usage, before)

	// Neither side arrived. The failure this pins is the one that reported
	// success while bringing in one of the two.
	for _, path := range []string{"one.txt", "two.txt"} {
		if testutil.FileExists(filepath.Join(dir, path)) {
			t.Errorf("the refused merge brought in %s", path)
		}
	}
	if strings.Contains(stdout, "fast-forwarded") {
		t.Errorf("the octopus was reported as a fast-forward:\n%s", stdout)
	}
}

// TestMergeFetchHeadWithOneForMergeHeadStillWorks is the control on both arms:
// the ordinary FETCH_HEAD merge -- one branch fetched, one side to bring in --
// is untouched by the refusal.
func TestMergeFetchHeadWithOneForMergeHeadStillWorks(t *testing.T) {
	t.Run("merge commit", func(t *testing.T) {
		dir := newFetchedHeadsRepo(t, true, "br1")
		before := testutil.Rev(t, dir, "HEAD")
		fetched := testutil.Rev(t, dir, "FETCH_HEAD")

		stdout, stderr, code := runSafegitEnv(t, dir, fetchHeadSession, "merge", "FETCH_HEAD")
		if code != exitcode.OK {
			t.Fatalf("a single-head FETCH_HEAD merge exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
		}
		parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD"))
		if len(parents) != 2 || parents[0] != before || parents[1] != fetched {
			t.Fatalf("the merge commit has parents %v, want [%s %s]", parents, before, fetched)
		}
		if msg := commitMessageOf(t, dir, "HEAD"); !strings.Contains(msg, "Claude-Code-Session-Id: merge-fetch-head-test") {
			t.Errorf("the merge commit carries no session trailer, so git authored it:\n%s", msg)
		}
	})

	t.Run("fast-forward", func(t *testing.T) {
		dir := newFetchedHeadsRepo(t, false, "br1")
		fetched := testutil.Rev(t, dir, "FETCH_HEAD")

		stdout, stderr, code := runSafegitEnv(t, dir, fetchHeadSession, "merge", "FETCH_HEAD")
		if code != exitcode.OK {
			t.Fatalf("a single-head FETCH_HEAD fast-forward exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
		}
		if head := testutil.Rev(t, dir, "HEAD"); head != fetched {
			t.Errorf("the fast-forward left HEAD at %s, want the fetched head %s", head, fetched)
		}
		if !testutil.FileExists(filepath.Join(dir, "one.txt")) {
			t.Errorf("the fast-forward did not put the incoming file in the working tree")
		}
	})
}

// TestPullWithAStockRefspecStillMerges is the control that says what the check
// counts. A stock `git fetch origin` writes one FETCH_HEAD line per remote
// branch and marks every one of them `not-for-merge` except the current
// branch's upstream -- so an everyday pull meets a multi-line FETCH_HEAD with
// exactly one side, and a check that counted LINES would refuse it.
func TestPullWithAStockRefspecStillMerges(t *testing.T) {
	dir := newFetchedHeadsRepo(t, false, "br1")
	// The upstream the stock refspec marks for merging.
	testutil.Git(t, dir, "config", "branch.main.remote", "origin")
	testutil.Git(t, dir, "config", "branch.main.merge", "refs/heads/main")
	testutil.Git(t, dir, "fetch", "origin")

	lines, err := os.ReadFile(filepath.Join(dir, ".git", "FETCH_HEAD"))
	if err != nil {
		t.Fatalf("reading .git/FETCH_HEAD: %v", err)
	}
	if !strings.Contains(string(lines), "not-for-merge") {
		t.Fatalf("the fixture needs a stock-refspec FETCH_HEAD carrying not-for-merge lines:\n%s", lines)
	}
	if n := len(forMergeLines(t, dir)); n != 1 {
		t.Fatalf("the fixture must leave exactly one side marked for merging, got %d:\n%s", n, lines)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, fetchHeadSession, "pull", "--merge-strategy", "ff", "origin")
	if code != exitcode.OK {
		t.Fatalf("an ordinary pull exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
}

// installOctopusRaceShim puts a `git` on PATH that appends a SECOND for-merge
// line to FETCH_HEAD the moment safegit's compute step runs, and then execs the
// real git.
//
// It exists to reach a state that safegit's own front check makes unreachable:
// the check reads FETCH_HEAD, git reads it again a moment later, and between
// those two reads a concurrent fetch can turn one side into several. The shim
// is that concurrent fetch, made deterministic -- the same device
// root_commit_cas_test.go uses to open a compare-and-swap window on demand.
//
// It returns the PATH entry and the marker file that says the race was really
// injected.
func installOctopusRaceShim(t *testing.T, dir, secondSHA string) (pathEnv, markerPath string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locating git: %v", err)
	}
	shimDir := t.TempDir()
	markerPath = filepath.Join(shimDir, "injected")
	gitDir := filepath.Join(dir, ".git")

	script := fmt.Sprintf(`#!/bin/sh
REAL=%q
MARKER=%q
GITDIR=%q
SECOND=%q
for a in "$@"; do
  if [ "$a" = "merge" ]; then
    printf '%%s\t\tbranch '"'"'br2'"'"' of origin\n' "$SECOND" >> "$GITDIR/FETCH_HEAD"
    : > "$MARKER"
    break
  fi
done
exec "$REAL" "$@"
`, realGit, markerPath, gitDir, secondSHA)

	shimPath := filepath.Join(shimDir, "git")
	if err := os.WriteFile(shimPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return "PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH"), markerPath
}

// TestParkedOctopusIsRefusedAndTheParkIsUndone is the second line of the
// defence, and the one that makes the guarantee structural rather than
// argument-shaped: whatever safegit BELIEVED it was computing, the conclusion
// looks at the state git actually parked, and it refuses to author a commit
// from a merge with more sides than every check over it is written against.
//
// Because safegit itself parked that state moments earlier -- the operator
// asked for a merge, not for a repository left mid-merge -- the refusal undoes
// the park: the state files go, and the index and the working tree return to
// where they stood.
func TestParkedOctopusIsRefusedAndTheParkIsUndone(t *testing.T) {
	dir := newFetchedHeadsRepo(t, true, "br1")
	before := testutil.Rev(t, dir, "HEAD")
	second := testutil.Rev(t, dir, "origin/br2")

	pathEnv, markerPath := installOctopusRaceShim(t, dir, second)
	stdout, stderr, code := runSafegitEnv(t, dir, append(fetchHeadSession, pathEnv), "merge", "FETCH_HEAD")

	if !testutil.FileExists(markerPath) {
		t.Fatalf("the shim never saw a compute step, so no octopus was injected\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if code == exitcode.OK {
		t.Fatalf("the parked octopus was concluded (exit 0)\nstdout=%s\nstderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "octopus") {
		t.Errorf("the refusal does not name the octopus:\n%s", stderr)
	}

	if head := testutil.Rev(t, dir, "HEAD"); head != before {
		t.Errorf("the refused conclusion moved HEAD to %s (was %s)", head, before)
	}
	if !testutil.MergeStateGone(t, dir) {
		t.Errorf("the refusal left the operator mid-merge: .git/MERGE_HEAD is still there")
	}
	assertNoSequencerResidue(t, dir, "refused parked octopus")
	if status := testutil.Git(t, dir, "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the refusal left the index and working tree carrying the computed merge:\n%s", status)
	}
}
