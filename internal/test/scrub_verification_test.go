package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// tierEnv is the session handshake for the two-tier verification tests.
var tierEnv = []string{"CLAUDE_CODE_SESSION_ID=scrub-verification-test"}

// refSnapshot records every ref and its SHA, so a test can assert that a
// refused rewrite moved nothing at all rather than only checking HEAD.
func refSnapshot(t *testing.T, dir string) string {
	t.Helper()
	return testutil.Git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
}

// TestScrubFileMistypedTargetRefusesBeforeAnythingMoves pins the Tier A
// contract in full: a target that appears in no commit is a typo, the command
// refuses with the code that means "nothing happened", and nothing did -- no
// ref moved, no rewrite-journal record was written, and the history is
// byte-identical.
func TestScrubFileMistypedTargetRefusesBeforeAnythingMoves(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, tierEnv, "secret.txt", "hunter2\n", "add secret")
	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, tierEnv, "secret.txt", "REDACTED\n", "commit replacement")

	refsBefore := refSnapshot(t, dir)
	headBefore := testutil.Rev(t, dir, "HEAD")

	// secret.txt exists; secrets.txt does not. The replacement source is a real
	// file, so the only thing wrong with this command is the target.
	_, stderr, code := runSafegitEnv(t, dir, tierEnv, "--approve-consequential",
		"scrub", "file", "--replace-with", "secret.txt",
		"--from", initialSHA, "--reason", "mistyped target", "secrets.txt")

	if code != exitcode.RewriteRefused {
		t.Fatalf("a target absent from all history must exit %d (RewriteRefused), got %d: %s",
			exitcode.RewriteRefused, code, stderr)
	}
	if !strings.Contains(stderr, "secrets.txt") {
		t.Errorf("the refusal must name the target that was not found; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "Nothing was changed") {
		t.Errorf("the refusal must say that nothing was changed; stderr: %s", stderr)
	}

	if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
		t.Errorf("HEAD moved despite the refusal: %s -> %s", headBefore, got)
	}
	if got := refSnapshot(t, dir); got != refsBefore {
		t.Errorf("refs moved despite the refusal:\nbefore:\n%s\nafter:\n%s", refsBefore, got)
	}
	if lines := readRewriteMaps(t, dir); len(lines) != 0 {
		t.Errorf("a refused rewrite must write no journal record (a start record with no complete reads as a crash), got %d: %v", len(lines), lines)
	}
	if !secretSurvives(t, dir) {
		t.Error("the original history must be untouched after a refusal")
	}
}

// TestScrubRunMultiOperationRecipePassesTierA is the false-positive test for
// the preservation check: a legitimate recipe that changes several files and a
// commit message in one pass must PASS Tier A. A check that refuses honest work
// is worse than no check.
func TestScrubRunMultiOperationRecipePassesTierA(t *testing.T) {
	dir := newRepo(t)

	commitFileEnv(t, dir, tierEnv, "alpha.txt", "alpha holds AAA_SECRET\n", "add alpha")
	commitFileEnv(t, dir, tierEnv, "beta.txt", "beta holds BBB_SECRET\n", "add beta")
	commitFileEnv(t, dir, tierEnv, "gamma.txt", "gamma holds AAA_SECRET and BBB_SECRET\n", "mentions AAA_SECRET in its message")

	recipe := writeRecipe(t, "multi.toml", `
[[operations]]
pattern = "AAA_SECRET"
replace = "AAA_REDACTED"

[[operations]]
pattern = "BBB_SECRET"
replace = "BBB_REDACTED"
`)

	stdout, stderr, code := runSafegitEnv(t, dir, tierEnv, "--approve-consequential",
		"scrub", "run", "--reason", "multi-operation recipe", "--entire-history", recipe)
	if code != 0 {
		t.Fatalf("a legitimate multi-operation recipe must pass verification (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}

	// Every operation's effect is present, and nothing else changed: seed.txt
	// is untouched in every commit.
	for _, sha := range revListReverse(t, dir) {
		for _, name := range []string{"alpha.txt", "beta.txt", "gamma.txt"} {
			content, ok := testutil.Show(t, dir, sha, name)
			if !ok {
				continue
			}
			if strings.Contains(content, "AAA_SECRET") || strings.Contains(content, "BBB_SECRET") {
				t.Errorf("commit %s: %s still holds a secret: %q", sha[:12], name, content)
			}
		}
		if content, ok := testutil.Show(t, dir, sha, "seed.txt"); ok && content != "seed\n" {
			t.Errorf("commit %s: seed.txt changed, which no operation asked for: %q", sha[:12], content)
		}
	}

	messages := testutil.Git(t, dir, "log", "--format=%s")
	if strings.Contains(messages, "AAA_SECRET") {
		t.Errorf("the commit message operation did not apply: %s", messages)
	}
	if !strings.Contains(messages, "AAA_REDACTED") {
		t.Errorf("the rewritten message should carry the replacement: %s", messages)
	}
}

// TestScrubMatchZeroCandidatesSaysSoAndSucceeds pins the other genuinely-empty
// case: a pattern nothing matches is a successful answer, stated in the terms
// the operator asked in, with the history untouched.
func TestScrubMatchZeroCandidatesSaysSoAndSucceeds(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, tierEnv, "clean.txt", "nothing sensitive here\n", "add clean file")
	headBefore := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, dir, tierEnv, "--approve-consequential",
		"scrub", "match", "--pattern", "NEVER_APPEARS_ANYWHERE", "--replace", "X",
		"--reason", "zero candidates", "--entire-history")
	if code != 0 {
		t.Fatalf("a pattern that matches nothing must succeed (code %d): stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "0 commits contained the pattern") {
		t.Errorf("the run must state that it found nothing, in commits; stdout: %s", stdout)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
		t.Errorf("HEAD moved for a scrub that matched nothing: %s -> %s", headBefore, got)
	}
}

// TestScrubSkipsWorktreeSyncWhenForeignStateAppears exercises the pre-sync
// residual check: work that appears between Tier A and the working-tree sync is
// never overwritten. The rewrite itself STANDS -- refs moved, the history is
// clean -- and the command exits RewriteIncomplete saying what to run.
//
// The interleaving is produced deterministically by git's own
// reference-transaction hook, which runs while safegit is moving the refs --
// exactly the window between the Tier A cleanliness check and the sync.
func TestScrubSkipsWorktreeSyncWhenForeignStateAppears(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, tierEnv, "secret.txt", "hunter2\n", "add secret")
	initialSHA := revListReverse(t, dir)[0]
	commitFileEnv(t, dir, tierEnv, "secret.txt", "REDACTED\n", "commit replacement")

	hook := filepath.Join(dir, ".git", "hooks", "reference-transaction")
	script := "#!/bin/sh\n" +
		"# Stand in for a concurrent session that stages work mid-rewrite.\n" +
		"if [ ! -e foreign.txt ]; then\n" +
		"  echo \"another session's work\" > foreign.txt\n" +
		"  git add foreign.txt\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatalf("installing the reference-transaction hook: %v", err)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, tierEnv, "--approve-consequential", "--json",
		"scrub", "file", "--replace-with", "secret.txt",
		"--from", initialSHA, "--reason", "foreign state mid-rewrite", "secret.txt")

	if code != exitcode.RewriteIncomplete {
		t.Fatalf("a skipped sync must exit %d (RewriteIncomplete), got %d: %s",
			exitcode.RewriteIncomplete, code, stderr)
	}
	if !strings.Contains(stderr, "was NOT synced") {
		t.Errorf("the operator must be told the working tree was not synced; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "read-tree --reset -u HEAD") {
		t.Errorf("the operator must be told what to run; stderr: %s", stderr)
	}

	// The same fact reaches a machine reader, not only the operator's terminal.
	var payload struct {
		SyncSkipped bool `json:"sync_skipped"`
	}
	if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &payload); err != nil {
		t.Fatalf("parsing the payload: %v\n%s", err, stdout)
	}
	if !payload.SyncSkipped {
		t.Errorf("the payload must report sync_skipped; stdout: %s", stdout)
	}

	// The foreign work is still there, untouched.
	content, err := os.ReadFile(filepath.Join(dir, "foreign.txt"))
	if err != nil {
		t.Fatalf("the foreign file must survive the rewrite: %v", err)
	}
	if !strings.Contains(string(content), "another session") {
		t.Errorf("foreign.txt content changed: %q", content)
	}

	// The rewrite STANDS: the refs point at the rewritten history.
	if secretSurvives(t, dir) {
		t.Error("the rewrite must stand -- the refs move even when the sync is skipped")
	}
}

// TestScrubFileEntireHistoryRewritesEveryCommit covers the range selector's
// second member on `scrub file`, which only `scrub match` and `scrub run`
// carried before.
func TestScrubFileEntireHistoryRewritesEveryCommit(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, tierEnv, "secret.txt", "hunter2 v1\n", "add secret")
	commitFileEnv(t, dir, tierEnv, "secret.txt", "hunter2 v2\n", "update secret")
	commitFileEnv(t, dir, tierEnv, "secret.txt", "REDACTED\n", "commit replacement")

	_, stderr, code := runSafegitEnv(t, dir, tierEnv, "--approve-consequential",
		"scrub", "file", "--replace-with", "secret.txt",
		"--entire-history", "--reason", "entire history file scrub", "secret.txt")
	if code != 0 {
		t.Fatalf("an --entire-history file scrub failed (code %d): %s", code, stderr)
	}

	for _, sha := range revListReverse(t, dir) {
		content, ok := testutil.Show(t, dir, sha, "secret.txt")
		if !ok {
			continue
		}
		if content != "REDACTED\n" {
			t.Errorf("commit %s: secret.txt = %q, want the replacement in every commit", sha[:12], content)
		}
	}
}

// TestScrubFileWithoutModeIsRefused pins that the mode is a declaration the
// parser enforces, not a guard the handler writes: neither --delete nor
// --replace-with means the command line is refused before anything runs.
func TestScrubFileWithoutModeIsRefused(t *testing.T) {
	dir := newRepo(t)
	commitFileEnv(t, dir, tierEnv, "secret.txt", "hunter2\n", "add secret")
	initialSHA := revListReverse(t, dir)[0]
	headBefore := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegitEnv(t, dir, tierEnv, "--approve-consequential",
		"scrub", "file", "--from", initialSHA, "--reason", "no mode", "secret.txt")
	if code == 0 {
		t.Fatalf("a scrub file with neither --delete nor --replace-with must be refused; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "mode") && !strings.Contains(stderr, "delete") && !strings.Contains(stderr, "replace-with") {
		t.Errorf("the refusal must name the missing selection; stderr: %s", stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != headBefore {
		t.Errorf("HEAD moved on a refused command line: %s -> %s", headBefore, got)
	}
}

// TestScrubFileModesFromSubdirectoryDoWhatTheySay is the regression test for
// the divergence the explicit modes removed: the mode used to be inferred by
// stat-ing the target path at the operator's working directory while the target
// itself was resolved at the repository root, so a root-level file scrubbed
// from a subdirectory was silently DELETED from history instead of replaced.
// Now each mode does what its name says, from anywhere in the repository.
func TestScrubFileModesFromSubdirectoryDoWhatTheySay(t *testing.T) {
	t.Run("replace", func(t *testing.T) {
		dir := newRepo(t)
		commitFileEnv(t, dir, tierEnv, "secret.txt", "hunter2\n", "add secret")
		commitFileEnv(t, dir, tierEnv, "sub/other.txt", "unrelated\n", "add subdir file")
		commitFileEnv(t, dir, tierEnv, "secret.txt", "REDACTED\n", "commit replacement")

		// From the subdirectory the replacement source is named relative to
		// HERE, while the target stays repository-relative.
		subDir := filepath.Join(dir, "sub")
		_, stderr, code := runSafegitEnv(t, subDir, tierEnv, "--approve-consequential",
			"scrub", "file", "--replace-with", "../secret.txt",
			"--entire-history", "--reason", "replace from a subdirectory", "secret.txt")
		if code != 0 {
			t.Fatalf("scrub file --replace-with from a subdirectory failed (code %d): %s", code, stderr)
		}

		for _, sha := range revListReverse(t, dir) {
			content, ok := testutil.Show(t, dir, sha, "secret.txt")
			if !ok {
				continue
			}
			if content != "REDACTED\n" {
				t.Errorf("commit %s: secret.txt = %q, want the replacement (a replace must never delete)", sha[:12], content)
			}
		}
	})

	t.Run("delete", func(t *testing.T) {
		dir := newRepo(t)
		commitFileEnv(t, dir, tierEnv, "secret.txt", "hunter2\n", "add secret")
		commitFileEnv(t, dir, tierEnv, "sub/other.txt", "unrelated\n", "add subdir file")

		subDir := filepath.Join(dir, "sub")
		_, stderr, code := runSafegitEnv(t, subDir, tierEnv, "--approve-consequential",
			"scrub", "file", "--delete",
			"--entire-history", "--reason", "delete from a subdirectory", "secret.txt")
		if code != 0 {
			t.Fatalf("scrub file --delete from a subdirectory failed (code %d): %s", code, stderr)
		}

		for _, sha := range revListReverse(t, dir) {
			if _, ok := testutil.Show(t, dir, sha, "secret.txt"); ok {
				t.Errorf("commit %s: secret.txt is still present after --delete", sha[:12])
			}
			if _, ok := testutil.Show(t, dir, sha, "sub/other.txt"); !ok {
				continue // the file did not exist yet in the earliest commits
			}
		}
	})
}

// TestScrubFileInSubmoduleRefusesAForeignFrom pins the end of a silent
// escalation: a --from the submodule cannot resolve (a parent-repository commit
// hash, which is what an operator naturally reaches for) used to be treated as
// "rewrite the submodule's ENTIRE history" without saying so -- a bounded
// request quietly became an unbounded rewrite. It is now a hard error that
// names both ways out, and nothing moves.
func TestScrubFileInSubmoduleRefusesAForeignFrom(t *testing.T) {
	parentDir, _, subDir := newRepoWithSubmoduleSecret(t, "SUBFROM_SECRET", "secret.txt")

	// Commit the replacement inside the submodule and record it in the parent,
	// so both trees are clean and the only problem is the --from.
	testutil.WriteFileAt(t, filepath.Join(subDir, "secret.txt"), "CLEANED\n")
	testutil.Git(t, subDir, "add", "secret.txt")
	testutil.Git(t, subDir, "commit", "-m", "commit replacement")
	testutil.Git(t, parentDir, "add", "mysub")
	testutil.Git(t, parentDir, "commit", "-m", "update submodule ref")

	parentHead := testutil.Rev(t, parentDir, "HEAD")
	subHead := testutil.Rev(t, subDir, "HEAD")

	// A PARENT commit hash names nothing inside the submodule.
	_, stderr, code := runSafegitEnv(t, parentDir, submoduleEnv, "--approve-consequential",
		"scrub", "file", "--replace-with", "mysub/secret.txt",
		"--from", parentHead, "--reason", "foreign from", "mysub/secret.txt")

	if code == 0 {
		t.Fatalf("a --from the submodule cannot resolve must be refused; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, "--entire-history") {
		t.Errorf("the refusal must name the way to rewrite the whole submodule history deliberately; stderr: %s", stderr)
	}
	if got := testutil.Rev(t, subDir, "HEAD"); got != subHead {
		t.Errorf("the submodule was rewritten despite the refusal: %s -> %s", subHead, got)
	}
	if got := testutil.Rev(t, parentDir, "HEAD"); got != parentHead {
		t.Errorf("the parent was rewritten despite the refusal: %s -> %s", parentHead, got)
	}
}
