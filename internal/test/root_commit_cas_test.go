package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// Root-commit ref creation is the one place in the commit pipeline where the
// ref update is NOT a compare-and-swap.
//
//   - internal/git/git.go:177-184 -- UpdateRef appends the old-value argument
//     only when oldSHA != "". An empty oldSHA therefore produces
//     `git update-ref <ref> <new>`, an unconditional write, despite the doc
//     comment claiming "if empty, the ref must not exist".
//   - internal/commit/commit.go:324 -- the commit pipeline passes parentSHA,
//     which is "" for a root commit (set at commit.go:190-196 when the target
//     ref does not resolve).
//   - internal/commit/commit.go:307-312 -- the only protection is a re-check
//     that the ref still does not exist. That is a time-of-check/time-of-use
//     window, not a CAS: anything that creates the ref between the RevParse at
//     :309 and the update-ref at :324 is silently overwritten.
//
// The per-ref lock taken at commit.go:300 closes this window against other
// safegit processes on the same machine (TestRootCommitConcurrentSafegitBoth
// Land pins that), but it is a safegit-private lock: raw `git commit`, any
// other tool, and any hook writing the ref are all invisible to it. safegit's
// own docs treat raw-git writes as an expected event (doctor's bypass
// detection), so this is a reachable loss, not a theoretical one.
//
// git's convention for "create only" is the all-zeros old value, pinned by
// TestRootCommitZeroOldValueRefusesExistingRef below.

// rootCasNewUnbornRepo creates a temp git repo with NO commits, so refs/heads/main
// is unborn and the next safegit commit takes the root-commit path.
func rootCasNewUnbornRepo(t *testing.T) string {
	t.Helper()
	dir := evalTempDir(t)
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		testutil.Git(t, dir, args...)
	}
	return dir
}

// rootCasMakeRivalCommit builds a commit object that no ref points at, standing in
// for another writer's root commit. Objects survive without a ref, so it can be
// created before the ref exists and pointed at later.
func rootCasMakeRivalCommit(t *testing.T, dir string) string {
	t.Helper()
	blob := testutil.GitStdin(t, dir, "rival session work\n", "hash-object", "-w", "--stdin")
	tree := testutil.GitStdin(t, dir, fmt.Sprintf("100644 blob %s\trival.txt\n", blob), "mktree")
	return testutil.Git(t, dir, "commit-tree", tree, "-m", "rival root commit")
}

// rootCasInstallGitShim writes a `git` wrapper into its own directory and returns
// (shimDir, markerPath, argvPath). Prepended to PATH, the shim makes the
// TOCTOU window deterministic: the first time safegit runs
// `git update-ref refs/heads/... <sha> [...]` the shim points that ref at
// rivalSHA out of band and only then execs the real git with the original
// argv. That is exactly "another writer created the ref between the pre-check
// and the update-ref", with no timing dependence.
//
// It fires once (guarded by the marker file) so a retried attempt runs clean,
// and it records the argv it intercepted so the test can assert whether safegit
// passed an old value at all.
func rootCasInstallGitShim(t *testing.T, dir, rivalSHA string) (shimDir, markerPath, argvPath string) {
	t.Helper()

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locating real git: %v", err)
	}

	shimDir = filepath.Join(dir, "..", "rootcas-shim")
	if err := os.MkdirAll(shimDir, 0755); err != nil {
		t.Fatal(err)
	}
	markerPath = filepath.Join(shimDir, "fired")
	argvPath = filepath.Join(shimDir, "argv")

	script := fmt.Sprintf(`#!/bin/sh
REAL=%q
MARKER=%q
ARGV=%q
RIVAL=%q

seen=0
n=0
ref=""
for a in "$@"; do
  if [ "$seen" -eq 0 ]; then
    if [ "$a" = "update-ref" ]; then seen=1; fi
    continue
  fi
  n=$((n+1))
  if [ "$n" -eq 1 ]; then ref="$a"; fi
done

if [ "$seen" -eq 1 ] && [ ! -e "$MARKER" ]; then
  case "$ref" in
    refs/heads/*)
      printf '%%s\n' "$*" > "$ARGV"
      : > "$MARKER"
      "$REAL" update-ref "$ref" "$RIVAL"
      ;;
  esac
fi

exec "$REAL" "$@"
`, realGit, markerPath, argvPath, rivalSHA)

	shimPath := filepath.Join(shimDir, "git")
	if err := os.WriteFile(shimPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return shimDir, markerPath, argvPath
}

// rootCasUpdateRefArgs returns the tokens the shim recorded, with git's leading
// global options stripped, i.e. ["update-ref", "<ref>", "<new>", "<old>"?].
func rootCasUpdateRefArgs(t *testing.T, argvPath string) []string {
	t.Helper()
	raw, err := os.ReadFile(argvPath)
	if err != nil {
		t.Fatalf("reading recorded argv: %v", err)
	}
	tokens := strings.Fields(strings.TrimSpace(string(raw)))
	for i, tok := range tokens {
		if tok == "update-ref" {
			return tokens[i:]
		}
	}
	t.Fatalf("recorded argv has no update-ref token: %q", string(raw))
	return nil
}

// TestRootCommitDoesNotClobberRefCreatedInWindow drives a root commit while
// another writer creates the target ref inside the pre-check/update-ref window.
// The losing side must never be silently overwritten: either safegit refuses,
// or it retries and builds on top of what it found. Both acceptable outcomes
// leave the rival commit reachable from the branch.
func TestRootCommitDoesNotClobberRefCreatedInWindow(t *testing.T) {
	dir := rootCasNewUnbornRepo(t)

	if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("my work\n"), 0644); err != nil {
		t.Fatal(err)
	}

	rival := rootCasMakeRivalCommit(t, dir)
	shimDir, markerPath, argvPath := rootCasInstallGitShim(t, dir, rival)

	pathEnv := "PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH")
	stdout, stderr, code := runSafegitNoConsent(t, dir, []string{pathEnv},
		"commit", "-m", "root commit", "--", "mine.txt")

	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("the git shim never intercepted an update-ref on refs/heads/*, so no race was injected; "+
			"safegit stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}

	args := rootCasUpdateRefArgs(t, argvPath)
	t.Logf("safegit issued: git %s (safegit exit=%d)", strings.Join(args, " "), code)

	// Direct pin of the defect: the ref update carried no expected old value,
	// so git performed an unconditional write. git's create-only convention is
	// the all-zeros old value (see TestRootCommitZeroOldValueRefusesExistingRef).
	if len(args) < 4 {
		t.Errorf("root-commit ref update has no CAS: safegit ran `git %s` with no old-value argument, "+
			"which is an unconditional write (internal/git/git.go:177-184 omits the argument when oldSHA is empty, "+
			"and internal/commit/commit.go:324 passes an empty parentSHA for root commits); "+
			"expected the all-zeros old value so git refuses when the ref already exists",
			strings.Join(args, " "))
	}

	// Behavioural pin: whatever the outcome, the commit created inside the
	// window must still be reachable.
	head, headCode := testutil.GitTry(t, dir, "rev-parse", "refs/heads/main")
	if headCode != 0 {
		t.Fatalf("refs/heads/main does not resolve after the run: %s", head)
	}
	// GitTry returns git's output verbatim; the SHA is passed on as an argv
	// argument below, where a trailing newline would make git reject it.
	head = strings.TrimSpace(head)
	if _, ancCode := testutil.GitTry(t, dir, "merge-base", "--is-ancestor", rival, head); ancCode != 0 {
		reflog, _ := testutil.GitTry(t, dir, "reflog", "show", "main")
		t.Errorf("silent overwrite: the commit created inside the pre-check/update-ref window (%s) is no longer "+
			"reachable from refs/heads/main (%s); safegit exited %d claiming success.\n"+
			"stdout=%q\nstderr=%q\nreflog:\n%s",
			rival[:8], head[:8], code, stdout, stderr, reflog)
	}
}

// TestRootCommitZeroOldValueRefusesExistingRef pins git's create-only
// convention, which is the mechanism the fix depends on: an all-zeros old value
// means "this ref must not exist", and git refuses with "reference already
// exists" when it does. safegit's retry loop already classifies that message as
// transient (internal/commit/commit.go:467-473 matches "cannot lock ref"), so a
// fixed root commit would retry from Phase A and build on the winner instead of
// failing outright.
func TestRootCommitZeroOldValueRefusesExistingRef(t *testing.T) {
	dir := rootCasNewUnbornRepo(t)

	const zeroSHA = "0000000000000000000000000000000000000000"

	first := rootCasMakeRivalCommit(t, dir)
	second := testutil.Git(t, dir, "commit-tree", first+"^{tree}", "-p", first, "-m", "second")

	// Ref absent: the create-only update must succeed.
	if out, code := testutil.GitTry(t, dir, "update-ref", "refs/heads/target", first, zeroSHA); code != 0 {
		t.Fatalf("create-only update-ref on an absent ref should succeed, got exit %d: %s", code, out)
	}
	if got := testutil.Git(t, dir, "rev-parse", "refs/heads/target"); got != first {
		t.Fatalf("refs/heads/target = %s, want %s", got, first)
	}

	// Ref present: the same update must be refused, leaving the ref untouched.
	out, code := testutil.GitTry(t, dir, "update-ref", "refs/heads/target", second, zeroSHA)
	if code == 0 {
		t.Fatalf("create-only update-ref on an existing ref should fail, got exit 0: %s", out)
	}
	if !strings.Contains(out, "reference already exists") {
		t.Errorf("expected git to refuse with \"reference already exists\", got: %s", out)
	}
	if !strings.Contains(out, "cannot lock ref") {
		t.Errorf("expected the refusal to contain \"cannot lock ref\" (the substring safegit's "+
			"isTransientRefError matches at internal/commit/commit.go:472), got: %s", out)
	}
	if got := testutil.Git(t, dir, "rev-parse", "refs/heads/target"); got != first {
		t.Fatalf("refused update still moved the ref: %s, want %s", got, first)
	}

	// And the shape safegit actually uses today -- no old value at all -- is an
	// unconditional write that overwrites the existing ref.
	if out, code := testutil.GitTry(t, dir, "update-ref", "refs/heads/target", second); code != 0 {
		t.Fatalf("unconditional update-ref should succeed, got exit %d: %s", code, out)
	}
	if got := testutil.Git(t, dir, "rev-parse", "refs/heads/target"); got != second {
		t.Fatalf("unconditional update-ref did not move the ref: %s, want %s", got, second)
	}
}

// TestRootCommitConcurrentSafegitBothLand pins the part that already works:
// two safegit processes racing to make the first commit on the same unborn ref
// are serialized by the per-ref lock (internal/commit/commit.go:300) plus the
// ref-still-absent re-check (:307-312), so the loser retries from Phase A and
// commits as a child of the winner. This is what bounds the defect above to
// writers that do not take safegit's lock.
func TestRootCommitConcurrentSafegitBothLand(t *testing.T) {
	dir := rootCasNewUnbornRepo(t)

	const n = 2
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("racer%d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	stdouts := make([]string, n)
	stderrs := make([]string, n)
	codes := make([]int, n)
	parallel(n, func(i int) {
		name := fmt.Sprintf("racer%d.txt", i)
		stdouts[i], stderrs[i], codes[i] = runSafegit(t, dir,
			"commit", "-m", "racing root commit "+name, "--", name)
	})

	for i := 0; i < n; i++ {
		if codes[i] != 0 {
			t.Errorf("racer %d failed (exit %d): stdout=%q stderr=%q", i, codes[i], stdouts[i], stderrs[i])
		}
	}

	if got := gitLog(t, dir, "main"); got != n {
		t.Errorf("expected %d commits on main (root + child), got %d", n, got)
	}

	tree := testutil.Git(t, dir, "ls-tree", "-r", "--name-only", "HEAD")
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("racer%d.txt", i)
		if !strings.Contains(tree, name) {
			t.Errorf("%s missing from HEAD tree; both racers' files must survive. tree:\n%s", name, tree)
		}
	}
}
