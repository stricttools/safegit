package test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs a git command in dir and fails the test if it errors.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteRefSHA returns the SHA a bare remote holds for a ref, or "" if absent.
func remoteRefSHA(t *testing.T, remoteDir, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref)
	cmd.Dir = remoteDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// commitFileIn writes a file and commits it with safegit.
func commitFileIn(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "-m", msg, "--", name)
	if code != 0 {
		t.Fatalf("commit %q failed (code %d): %s", msg, code, stderr)
	}
	return revParseHEAD(t, dir)
}

// oplogEntries returns every oplog entry of the given op in a repo.
func oplogEntries(t *testing.T, dir, op string) []map[string]interface{} {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, ".git", "safegit", "log"))
	if err != nil {
		t.Fatalf("opening oplog: %v", err)
	}
	defer f.Close()

	var found []map[string]interface{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 4096)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry map[string]interface{}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["op"] == op {
			found = append(found, entry)
		}
	}
	return found
}

// pushForeignBackup makes a commit in a second clone of the remote and pushes
// it into the backup slot, simulating a backup taken on another machine.
func pushForeignBackup(t *testing.T, remoteDir, branch string) string {
	t.Helper()
	other := evalTempDir(t)
	gitIn(t, ".", "clone", "--quiet", remoteDir, other)
	gitIn(t, other, "config", "user.email", "other@test.com")
	gitIn(t, other, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(other, "elsewhere.txt"), []byte("work from another machine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "elsewhere.txt")
	gitIn(t, other, "commit", "-m", "work from another machine")
	gitIn(t, other, "push", remoteDir, "HEAD:refs/backups/"+branch)
	return gitIn(t, other, "rev-parse", "HEAD")
}

// TestBackupCreatesFirstSlotAndLists: with no slot on the remote, the backup is
// created (the lease expects the ref to be absent) and shows up in list.
func TestBackupCreatesFirstSlotAndLists(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	head := commitFileIn(t, dir, "work.txt", "work\n", "add work")

	if got := remoteRefSHA(t, remoteDir, "refs/backups/main"); got != "" {
		t.Fatalf("remote already has a backup slot: %s", got)
	}

	stdout, stderr, code := runSafegit(t, dir, "backup", "backup", "origin")
	if code != 0 {
		t.Fatalf("backup failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	// A local filesystem remote must not trigger the public-repo confirmation.
	if strings.Contains(stderr, "warning:") {
		t.Errorf("local remote should not warn about visibility, got: %s", stderr)
	}

	if got := remoteRefSHA(t, remoteDir, "refs/backups/main"); got != head {
		t.Errorf("backup slot = %q, want HEAD %q", got, head)
	}
	// refs/heads must be untouched: a backup is not a push.
	if got := remoteRefSHA(t, remoteDir, "refs/heads/main"); got != "" {
		t.Errorf("backup pushed refs/heads/main (%s); it must only write refs/backups", got)
	}

	stdout, stderr, code = runSafegit(t, dir, "backup", "list", "origin")
	if code != 0 {
		t.Fatalf("backup list failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "main") || !strings.Contains(stdout, head[:12]) {
		t.Errorf("backup list output missing the slot; got: %s", stdout)
	}
}

// TestBackupListEmptyRemote: an empty namespace is reported, not an error.
func TestBackupListEmptyRemote(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	stdout, stderr, code := runSafegit(t, dir, "backup", "list", "origin")
	if code != 0 {
		t.Fatalf("backup list failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "no backups") {
		t.Errorf("expected an empty-namespace message, got: %s", stdout)
	}
}

// TestBackupRestoreRoundTrip: backup, lose the local commits, restore them.
func TestBackupRestoreRoundTrip(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	base := revParseHEAD(t, dir)
	head := commitFileIn(t, dir, "work.txt", "work\n", "add work")

	if _, stderr, code := runSafegit(t, dir, "backup", "backup", "origin"); code != 0 {
		t.Fatalf("backup failed (code %d): %s", code, stderr)
	}

	// Simulate losing local work (e.g. a fresh clone of a stale state).
	gitIn(t, dir, "reset", "--hard", base)
	if got := revParseHEAD(t, dir); got != base {
		t.Fatalf("reset failed: HEAD = %s", got)
	}

	stdout, stderr, code := runSafegit(t, dir, "backup", "restore", "origin")
	if code != 0 {
		t.Fatalf("backup restore failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if got := revParseHEAD(t, dir); got != head {
		t.Errorf("HEAD after restore = %s, want %s", got, head)
	}
	if _, err := os.Stat(filepath.Join(dir, "work.txt")); err != nil {
		t.Errorf("restored commit's file is missing from the working tree: %v", err)
	}
}

// TestBackupRefusesDivergedSlot: a slot holding commits absent from the local
// history is a hard stop, and the slot is left untouched.
func TestBackupRefusesDivergedSlot(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	foreign := pushForeignBackup(t, remoteDir, "main")

	commitFileIn(t, dir, "local.txt", "local\n", "local work")

	stdout, stderr, code := runSafegit(t, dir, "backup", "backup", "origin")
	if code == 0 {
		t.Fatalf("backup should refuse a diverged slot; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "remote backup contains work not in your current history") {
		t.Errorf("expected the divergence message, got: %s", stderr)
	}
	if !strings.Contains(stderr, "--overwrite-remote-backup") {
		t.Errorf("expected the remediation flag in the message, got: %s", stderr)
	}
	if got := remoteRefSHA(t, remoteDir, "refs/backups/main"); got != foreign {
		t.Errorf("refused backup still changed the slot: %s -> %s", foreign, got)
	}
}

// TestBackupOverwritesDivergedSlotWithFlag: the qualified flag is the only way
// past the divergence check, and it leases on the SHA observed during the run.
func TestBackupOverwritesDivergedSlotWithFlag(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	pushForeignBackup(t, remoteDir, "main")

	head := commitFileIn(t, dir, "local.txt", "local\n", "local work")

	_, stderr, code := runSafegit(t, dir, "backup", "backup", "--overwrite-remote-backup", "origin")
	if code != 0 {
		t.Fatalf("confirmed overwrite failed (code %d): %s", code, stderr)
	}
	if got := remoteRefSHA(t, remoteDir, "refs/backups/main"); got != head {
		t.Errorf("slot after confirmed overwrite = %s, want %s", got, head)
	}
}

// TestBackupSecondBackupFastForwards: re-backing up the same branch after more
// commits replaces the slot without needing any override.
func TestBackupSecondBackupFastForwards(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	commitFileIn(t, dir, "one.txt", "one\n", "first")
	if _, stderr, code := runSafegit(t, dir, "backup", "backup", "origin"); code != 0 {
		t.Fatalf("first backup failed (code %d): %s", code, stderr)
	}
	second := commitFileIn(t, dir, "two.txt", "two\n", "second")
	if _, stderr, code := runSafegit(t, dir, "backup", "backup", "origin"); code != 0 {
		t.Fatalf("second backup failed (code %d): %s", code, stderr)
	}
	if got := remoteRefSHA(t, remoteDir, "refs/backups/main"); got != second {
		t.Errorf("slot = %s, want %s", got, second)
	}
}

// TestBackupRestoreRefusesNonFastForward: local commits are never discarded by
// a restore.
func TestBackupRestoreRefusesNonFastForward(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	pushForeignBackup(t, remoteDir, "main")

	head := commitFileIn(t, dir, "local.txt", "local\n", "local work")

	_, stderr, code := runSafegit(t, dir, "backup", "restore", "origin")
	if code == 0 {
		t.Fatal("restore should refuse a non-fast-forward backup")
	}
	if !strings.Contains(stderr, "fast-forward") {
		t.Errorf("expected a fast-forward explanation, got: %s", stderr)
	}
	if got := revParseHEAD(t, dir); got != head {
		t.Errorf("HEAD moved despite the refused restore: %s -> %s", head, got)
	}
}

// TestBackupRestoreWithoutSlotErrors: restoring a branch that was never backed
// up names the missing slot.
func TestBackupRestoreWithoutSlotErrors(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	_, stderr, code := runSafegit(t, dir, "backup", "restore", "origin")
	if code == 0 {
		t.Fatal("restore without a slot should fail")
	}
	if !strings.Contains(stderr, "no backup slot") {
		t.Errorf("expected a missing-slot message, got: %s", stderr)
	}
}

// TestBackupUnknownVisibilityRequiresConfirmation: a networked remote that
// cannot be proven private is only backed up after a confirmation. The test
// runs without a terminal, so the prompt is declined and nothing is pushed --
// which also proves the check happens before any network contact.
func TestBackupUnknownVisibilityRequiresConfirmation(t *testing.T) {
	dir := newRepo(t)
	gitIn(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")

	stdout, stderr, code := runSafegit(t, dir, "backup", "backup", "cloudy")
	if code != 0 {
		t.Fatalf("declining the prompt should exit 0 (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "cannot determine") {
		t.Errorf("expected a visibility warning, got: %s", stderr)
	}
	if !strings.Contains(stdout, "Aborted") {
		t.Errorf("expected the abort notice, got: %s", stdout)
	}
}

// TestBackupDryRunTouchesNothing: the preview reports the push and the plain
// git equivalent, and creates no slot.
func TestBackupDryRunTouchesNothing(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	commitFileIn(t, dir, "work.txt", "work\n", "add work")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "backup", "backup", "origin")
	if code != 0 {
		t.Fatalf("dry-run backup failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Would create backup slot") {
		t.Errorf("expected a preview line, got: %s", stdout)
	}
	if !strings.Contains(stdout, "force-with-lease") {
		t.Errorf("expected the plain-git equivalent in the preview, got: %s", stdout)
	}
	if got := remoteRefSHA(t, remoteDir, "refs/backups/main"); got != "" {
		t.Errorf("dry run created a backup slot: %s", got)
	}
}

// TestBackupRestoreDryRunTouchesNothing: the restore preview leaves HEAD alone.
func TestBackupRestoreDryRunTouchesNothing(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	base := revParseHEAD(t, dir)
	commitFileIn(t, dir, "work.txt", "work\n", "add work")
	if _, stderr, code := runSafegit(t, dir, "backup", "backup", "origin"); code != 0 {
		t.Fatalf("backup failed (code %d): %s", code, stderr)
	}
	gitIn(t, dir, "reset", "--hard", base)

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "backup", "restore", "origin")
	if code != 0 {
		t.Fatalf("dry-run restore failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Would fast-forward") {
		t.Errorf("expected a preview line, got: %s", stdout)
	}
	if got := revParseHEAD(t, dir); got != base {
		t.Errorf("dry-run restore moved HEAD: %s -> %s", base, got)
	}
}

// TestBackupWritesOplogEntries: both mutating backup operations are auditable.
func TestBackupWritesOplogEntries(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	base := revParseHEAD(t, dir)
	head := commitFileIn(t, dir, "work.txt", "work\n", "add work")

	if _, stderr, code := runSafegit(t, dir, "backup", "backup", "origin"); code != 0 {
		t.Fatalf("backup failed (code %d): %s", code, stderr)
	}
	entries := oplogEntries(t, dir, "backup")
	if len(entries) != 1 {
		t.Fatalf("want 1 backup oplog entry, got %d", len(entries))
	}
	extra, ok := entries[0]["extra"].(map[string]interface{})
	if !ok {
		t.Fatal("backup oplog entry has no extra map")
	}
	if extra["ref"] != "refs/backups/main" {
		t.Errorf("oplog ref = %v, want refs/backups/main", extra["ref"])
	}
	if extra["sha"] != head {
		t.Errorf("oplog sha = %v, want %s", extra["sha"], head)
	}
	if extra["remote"] != "origin" {
		t.Errorf("oplog remote = %v, want origin", extra["remote"])
	}

	gitIn(t, dir, "reset", "--hard", base)
	if _, stderr, code := runSafegit(t, dir, "backup", "restore", "origin"); code != 0 {
		t.Fatalf("restore failed (code %d): %s", code, stderr)
	}
	restores := oplogEntries(t, dir, "backup-restore")
	if len(restores) != 1 {
		t.Fatalf("want 1 backup-restore oplog entry, got %d", len(restores))
	}
	rextra, ok := restores[0]["extra"].(map[string]interface{})
	if !ok {
		t.Fatal("backup-restore oplog entry has no extra map")
	}
	if rextra["to"] != head {
		t.Errorf("oplog restore target = %v, want %s", rextra["to"], head)
	}
	if rextra["from"] != base {
		t.Errorf("oplog restore origin = %v, want %s", rextra["from"], base)
	}
}

// TestEmptyLeaseRejectsConcurrentSlot pins the git behaviour the first-backup
// path relies on: --force-with-lease=<ref>: (empty expectation) means "this ref
// must not exist", so a slot created between safegit's ls-remote and its push
// is rejected instead of clobbered.
func TestEmptyLeaseRejectsConcurrentSlot(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)
	commitFileIn(t, dir, "local.txt", "local\n", "local work")

	// Another machine wins the race and creates the slot first.
	foreign := pushForeignBackup(t, remoteDir, "main")

	cmd := exec.Command("git", "push",
		"--force-with-lease=refs/backups/main:", "origin", "HEAD:refs/backups/main")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("empty-expectation lease accepted a push onto an existing slot:\n%s", out)
	}
	if got := remoteRefSHA(t, remoteDir, "refs/backups/main"); got != foreign {
		t.Errorf("slot changed despite the rejected push: %s -> %s", foreign, got)
	}
}

// TestBackupDetachedHeadErrors: slots are per branch.
func TestBackupDetachedHeadErrors(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	gitIn(t, dir, "checkout", "--detach")

	_, stderr, code := runSafegit(t, dir, "backup", "backup", "origin")
	if code == 0 {
		t.Fatal("backup with a detached HEAD should fail")
	}
	if !strings.Contains(stderr, "detached") {
		t.Errorf("expected a detached-HEAD message, got: %s", stderr)
	}
}
