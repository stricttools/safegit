package test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

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
	return testutil.Rev(t, dir, "HEAD")
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
	testutil.Git(t, ".", "clone", "--quiet", remoteDir, other)
	testutil.Git(t, other, "config", "user.email", "other@test.com")
	testutil.Git(t, other, "config", "user.name", "Other")
	if err := os.WriteFile(filepath.Join(other, "elsewhere.txt"), []byte("work from another machine\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, other, "add", "elsewhere.txt")
	testutil.Git(t, other, "commit", "-m", "work from another machine")
	testutil.Git(t, other, "push", remoteDir, "HEAD:refs/backups/"+branch)
	return testutil.Git(t, other, "rev-parse", "HEAD")
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
	base := testutil.Rev(t, dir, "HEAD")
	head := commitFileIn(t, dir, "work.txt", "work\n", "add work")

	if _, stderr, code := runSafegit(t, dir, "backup", "backup", "origin"); code != 0 {
		t.Fatalf("backup failed (code %d): %s", code, stderr)
	}

	// Simulate losing local work (e.g. a fresh clone of a stale state).
	testutil.Git(t, dir, "reset", "--hard", base)
	if got := testutil.Rev(t, dir, "HEAD"); got != base {
		t.Fatalf("reset failed: HEAD = %s", got)
	}

	stdout, stderr, code := runSafegit(t, dir, "backup", "restore", "origin")
	if code != 0 {
		t.Fatalf("backup restore failed (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != head {
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
	if got := testutil.Rev(t, dir, "HEAD"); got != head {
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

// TestBackupUnconsentedContactsNoRemote: `backup backup` is NOT consequential
// (it writes only the tool-owned refs/backups namespace under a
// --force-with-lease), so the framework never prompts for it. safegit's own
// exposure confirmation is the gate: a remote whose visibility cannot be
// proven private is refused, nothing is pushed, and no remote is contacted.
func TestBackupUnconsentedContactsNoRemote(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")

	stdout, stderr, code := runSafegitNoConsent(t, dir, nil, "backup", "backup", "cloudy")
	if code == 0 {
		t.Fatalf("an unconsented backup must not succeed: stdout=%s stderr=%s", stdout, stderr)
	}
	if strings.Contains(stderr, "Could not resolve host") {
		t.Errorf("the refusal must precede any network contact, got: %s", stderr)
	}
}

// TestBackupJSONIsNotConsent: --json produces machine-readable output and says
// nothing about consent. It must never stand in for the --approve-consequential the confirm
// protocol asks for; the remote is unreachable, so a bypass would show up as a
// network error.
func TestBackupJSONIsNotConsent(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")

	stdout, stderr, code := runSafegitNoConsent(t, dir, nil, "--json", "backup", "backup", "cloudy")
	if code == 0 {
		t.Fatalf("--json must not answer the confirmation (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if strings.Contains(stderr, "Could not resolve host") {
		t.Errorf("--json was taken as consent and the remote was contacted: %s", stderr)
	}
}

// TestBackupAllowPublicRemoteSatisfiesExposureConfirmation: the exposure
// question is about the target, so its consent flag is --allow-public-remote,
// not the blanket --approve-consequential. The remote refuses connections
// instantly, so reaching a network error is the proof that the confirmation was
// satisfied rather than declined.
func TestBackupAllowPublicRemoteSatisfiesExposureConfirmation(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "remote", "add", "refused", "git://127.0.0.1:1/owner/repo.git")

	stdout, stderr, code := runSafegit(t, dir, "backup", "backup", "--allow-public-remote", "refused")
	if code == 0 {
		t.Fatalf("--allow-public-remote should proceed past the confirmation and fail on the unreachable remote; stdout=%s stderr=%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "listing ") {
		t.Errorf("expected a remote-listing failure (proving the confirmation was passed), got: %s", stderr)
	}
	if strings.Contains(stdout, "Aborted") {
		t.Errorf("--allow-public-remote must not decline the confirmation, got: %s", stdout)
	}
}

// TestBackupDryRunMakesNoNetworkContact: the preview is built from local state
// only, so an unreachable remote previews cleanly, nothing is asked, and no
// network call is made.
func TestBackupDryRunMakesNoNetworkContact(t *testing.T) {
	dir := newRepo(t)
	testutil.Git(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "backup", "backup", "cloudy")
	if code != 0 {
		t.Fatalf("dry run against an unreachable remote must preview, not fail (code %d): stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "refs/backups/main") {
		t.Errorf("expected the slot ref in the preview, got: %s", stdout)
	}
	if strings.Contains(stdout, "[y/N]") {
		t.Errorf("dry run must not prompt, got: %s", stdout)
	}
	if strings.Contains(stderr, "cannot determine") {
		t.Errorf("dry run must not run the exposure classification, got: %s", stderr)
	}
	if strings.Contains(stderr, "listing ") || strings.Contains(stderr, "fetching ") {
		t.Errorf("dry run contacted the remote: %s", stderr)
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
	if !strings.Contains(stdout, "Would back up") || !strings.Contains(stdout, "refs/backups/main") {
		t.Errorf("expected a preview line naming the slot, got: %s", stdout)
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
	base := testutil.Rev(t, dir, "HEAD")
	commitFileIn(t, dir, "work.txt", "work\n", "add work")
	if _, stderr, code := runSafegit(t, dir, "backup", "backup", "origin"); code != 0 {
		t.Fatalf("backup failed (code %d): %s", code, stderr)
	}
	testutil.Git(t, dir, "reset", "--hard", base)

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "backup", "restore", "origin")
	if code != 0 {
		t.Fatalf("dry-run restore failed (code %d): %s", code, stderr)
	}
	if !strings.Contains(stdout, "Would fast-forward") {
		t.Errorf("expected a preview line, got: %s", stdout)
	}
	if got := testutil.Rev(t, dir, "HEAD"); got != base {
		t.Errorf("dry-run restore moved HEAD: %s -> %s", base, got)
	}
}

// TestBackupWritesOplogEntries: both mutating backup operations are auditable.
func TestBackupWritesOplogEntries(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	base := testutil.Rev(t, dir, "HEAD")
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

	testutil.Git(t, dir, "reset", "--hard", base)
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
	testutil.Git(t, dir, "checkout", "--detach")

	_, stderr, code := runSafegit(t, dir, "backup", "backup", "origin")
	if code == 0 {
		t.Fatal("backup with a detached HEAD should fail")
	}
	if !strings.Contains(stderr, "detached") {
		t.Errorf("expected a detached-HEAD message, got: %s", stderr)
	}
}

// TestApproveConsequentialDoesNotConsentToAnUnprovenRemote pins the ratified
// property that a non-interactive run cannot publish a branch to a remote it
// cannot prove is private without saying so deliberately.
//
// The framework's --approve-consequential answers "yes, run this command". The
// exposure question is about the TARGET, not the command -- it is discovered by
// probing the remote at run time, so a caller may not know the answer when it
// composes the command line. Letting the blanket flag answer it meant every
// script and agent that passed --approve-consequential silently consented to a
// public push. The per-condition --allow-public-remote is the only thing that
// answers it now.
func TestApproveConsequentialDoesNotConsentToAnUnprovenRemote(t *testing.T) {
	newUnprovenRemoteRepo := func(t *testing.T) string {
		t.Helper()
		dir := newRepo(t)
		commitFileEnv(t, dir, confirmEnv, "file.txt", "content\n", "add file")
		// Not a forge safegit can classify, and unreachable -- so if the run
		// gets past the exposure gate it fails at the network, which is exactly
		// the difference the assertions below key on.
		testutil.Git(t, dir, "remote", "add", "cloudy", "https://example.invalid/owner/repo.git")
		return dir
	}

	t.Run("blanket consent refuses", func(t *testing.T) {
		dir := newUnprovenRemoteRepo(t)
		_, stderr, code := runSafegitNoConsent(t, dir, confirmEnv,
			"--json", "--approve-consequential", "backup", "backup", "cloudy")
		if code == 0 {
			t.Errorf("--approve-consequential must not consent to an unproven remote; stderr: %s", stderr)
		}
		if !strings.Contains(stderr, "--allow-public-remote") {
			t.Errorf("the refusal must name --allow-public-remote as the consent flag, got: %s", stderr)
		}
		if strings.Contains(stderr, "Could not resolve host") {
			t.Errorf("the refusal must precede any network contact, got: %s", stderr)
		}
	})

	t.Run("the per-condition flag consents", func(t *testing.T) {
		dir := newUnprovenRemoteRepo(t)
		_, stderr, _ := runSafegitNoConsent(t, dir, confirmEnv,
			"--json", "backup", "backup", "--allow-public-remote", "cloudy")
		if strings.Contains(stderr, "--allow-public-remote") {
			t.Errorf("--allow-public-remote must answer the exposure question, got: %s", stderr)
		}
		// The remote does not exist, so the run must have reached the network.
		if !strings.Contains(stderr, "example.invalid") {
			t.Errorf("the consented backup must reach the remote, got: %s", stderr)
		}
	})
}
