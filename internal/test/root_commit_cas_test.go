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

// Root-commit ref creation used to be the one place in the commit pipeline
// where the ref update was NOT a compare-and-swap.
//
//   - git.UpdateRef appended the old-value argument only when oldSHA != "". An
//     empty oldSHA therefore produced `git update-ref <ref> <new>`, an
//     unconditional write, despite the doc comment claiming "if empty, the ref
//     must not exist".
//   - commit.tryCommit passed parentSHA, which is "" for a root commit (it is
//     set empty when the target ref does not resolve).
//   - The only protection was a re-check that the ref still did not exist --
//     a time-of-check/time-of-use window, not a CAS: anything that created the
//     ref between that RevParse and the update-ref was silently overwritten.
//     The per-ref lock closed the window against other safegit processes on the
//     same machine (TestRootCommitConcurrentSafegitBothLand pins that), but it
//     is a safegit-private lock, invisible to raw `git commit`, to any other
//     tool, and to any hook writing the ref -- and safegit's own docs treat
//     raw-git writes as an expected event (doctor's bypass detection), so the
//     loss was reachable rather than theoretical.
//
// Both halves are now closed. git.UpdateRef REFUSES an empty expected old value
// (git.ErrNoExpectedValue), and commit.tryCommit substitutes git.ZeroSHA for the
// empty parentSHA on the root-commit path -- git's "create only" convention,
// pinned by TestRootCommitZeroOldValueRefusesExistingRef below. A losing race is
// then git's "reference already exists", which the retry loop classifies as
// transient and re-attempts from Phase A.
//
// These tests assert the closed behaviour.

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
	// Stdout only: callers pass this SHA to update-ref and to safegit itself.
	return strings.TrimSpace(testutil.GitOut(t, dir, "commit-tree", tree, "-m", "rival root commit"))
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
	stdout, stderr, code := runSafegitEnv(t, dir, []string{pathEnv},
		"commit", "-m", "root commit", "--", "mine.txt")

	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("the git shim never intercepted an update-ref on refs/heads/*, so no race was injected; "+
			"safegit stdout=%q stderr=%q code=%d", stdout, stderr, code)
	}

	args := rootCasUpdateRefArgs(t, argvPath)
	t.Logf("safegit issued: git %s (safegit exit=%d)", strings.Join(args, " "), code)

	// Direct pin: the root-commit ref update carries an expected old value, so
	// git performs a compare-and-swap rather than an unconditional write. git's
	// create-only convention is the all-zeros old value (see
	// TestRootCommitZeroOldValueRefusesExistingRef).
	if len(args) < 4 {
		t.Errorf("root-commit ref update has no CAS: safegit ran `git %s` with no old-value argument, "+
			"which is an unconditional write; commit.tryCommit must substitute git.ZeroSHA for the empty "+
			"parentSHA on the root-commit path, and git.UpdateRef must refuse an empty expected old value, "+
			"so that git refuses when the ref already exists",
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
// convention, which is the mechanism the fix rests on: an all-zeros old value
// means "this ref must not exist", and git refuses with "reference already
// exists" when it does. safegit's retry loop classifies that message as
// transient (commit.isTransientRefError), so a losing root commit retries from
// Phase A and builds on the winner instead of failing outright.
func TestRootCommitZeroOldValueRefusesExistingRef(t *testing.T) {
	dir := rootCasNewUnbornRepo(t)

	const zeroSHA = "0000000000000000000000000000000000000000"

	first := rootCasMakeRivalCommit(t, dir)
	// Stdout only: this SHA is fed straight back into update-ref below, so a
	// stray stderr line mixed into it would corrupt the argument.
	second := strings.TrimSpace(testutil.GitOut(t, dir, "commit-tree", first+"^{tree}", "-p", first, "-m", "second"))

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
//
// FLAKE WATCHLIST -- certified load-sensitive, not racy.
//
// This test has failed spuriously under load. The cause is the shared temporary
// filesystem, not anything it pins. Every repository this package builds lives
// under $TMPDIR, and TestMain links the safegit binary there too. On a machine
// whose /tmp is a tmpfs sized against RAM, several concurrent `go test`
// processes exhaust it: that was reproduced here as
// "link: mapping output file failed: disk quota exceeded" out of the TestMain
// build, with eight concurrent runs sharing a 12 GB tmpfs already 80% full. The
// same exhaustion reaches a RUNNING racer as an errno on any write safegit makes
// -- the per-invocation temp index, a loose object, the lock file, the oplog --
// and safegit then exits nonzero, which is exactly the shape this test reports
// as "racer N failed".
//
// A lost update or a lock defect is not reachable by interleaving here: the lock
// file is created O_CREAT|O_EXCL so exactly one racer ever holds the ref,
// staleness is decided by the holder's pid liveness rather than by any age or
// deadline (internal/lock.staleFromFields), and the loser's ref re-check plus the
// ZeroSHA CAS send it back through Phase A with the winner's commit as its
// parent. Both files therefore reach HEAD's tree in either order.
//
// Evidence: 1924 iterations under -race with no failure -- 1324 targeted plus a
// final round of 600 across six concurrent processes -- including 400 pinned to
// two CPUs against sixteen spinning load generators (about a tenfold slowdown,
// against a 30-second lock acquisition timeout) and four full-package race runs
// under eight-way load. Pointing $TMPDIR off the tmpfs removed every failure
// that was seen.
//
// So do not weaken these assertions and do not add a retry. A failure here is
// first a question about free space on $TMPDIR.
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
