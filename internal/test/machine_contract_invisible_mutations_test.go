package test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/lock"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING 3 (second-campaign adversarial review), under the all-seven ruling:
// six more commands change the world without leaving a single machine-readable
// trace. Every one of them performs its mutation outside the effects handle --
// a bare os.Remove, a direct git.Run, a dry-run branch that prints a sentence
// and returns -- so `--json` answers preview:[] whether the run previewed the
// change or performed it.
//
//	unlock         force-releases a lock file (os.Remove via lock.ForceRelease)
//	doctor fix     removes stale locks, orphan tmp dirs and orphaned
//	               lock-publication temp files (os.Remove / os.RemoveAll)
//	backup backup  pushes the branch to its remote slot; the preview returns
//	               before the push is ever minted
//	backup restore fast-forwards the branch onto the fetched slot; same shape
//	commit (in a submodule) previews the child's own ref update but says
//	               nothing about the PARENT commit the same run would spawn
//
// RULED TARGET: each of these mutations is minted through the effects handle,
// so it appears in the envelope's effect records in both modes -- recorded
// under --dry-run, performed on a real run. The assertions below are
// deliberately about PRESENCE and identification, not about exact argv: what is
// broken is that the machine-readable effects are empty, and the fix is that
// they are not.
//
// These tests are RED on purpose until that exists.

// effectDetails returns every effect record's detail string, whatever its kind.
// Unlike procMutations it does not presume the mutation is a subprocess: a lock
// release is a file removal, and the fix is free to mint it as one.
func effectDetails(env machineEnvelope) []string {
	out := make([]string, 0, len(env.Preview))
	for _, rec := range env.Preview {
		detail, _ := rec["detail"].(string)
		out = append(out, detail)
	}
	return out
}

// anyDetailContains reports whether some effect record's detail mentions needle.
func anyDetailContains(env machineEnvelope, needle string) bool {
	for _, detail := range effectDetails(env) {
		if strings.Contains(detail, needle) {
			return true
		}
	}
	return false
}

// plantStaleLockFile writes a lock file naming a PID that cannot be alive, which
// is the state `unlock` releases and `doctor --action fix` sweeps. It returns
// the lock file's path.
func plantStaleLockFile(t *testing.T, dir, ref string) string {
	t.Helper()
	sgDir := repo.SafegitDir(filepath.Join(dir, ".git"))
	lockPath := lock.Path(sgDir, ref)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		t.Fatalf("creating the lock directory: %v", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	content := fmt.Sprintf("pid=999999999\nts=2026-01-01T00:00:00Z\nop=commit\nhost=%s\n", hostname)
	if err := os.WriteFile(lockPath, []byte(content), 0644); err != nil {
		t.Fatalf("writing the lock file: %v", err)
	}
	return lockPath
}

// plantOrphanedPublicationTemp writes a lock-publication temporary file that a
// killed process would have left behind: dead holder, and old enough to be past
// the publication grace. It returns the file's path.
func plantOrphanedPublicationTemp(t *testing.T, dir string) string {
	t.Helper()
	sgDir := repo.SafegitDir(filepath.Join(dir, ".git"))
	lockDir := filepath.Dir(lock.Path(sgDir, "refs/heads/main"))
	if err := os.MkdirAll(lockDir, 0755); err != nil {
		t.Fatalf("creating the lock directory: %v", err)
	}
	tempPath := filepath.Join(lockDir, ".main.lock.tmp-111111")
	if err := os.WriteFile(tempPath, []byte("pid=999999999\nop=commit\n"), 0644); err != nil {
		t.Fatalf("writing the publication temp: %v", err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(tempPath, old, old); err != nil {
		t.Fatalf("ageing the publication temp: %v", err)
	}
	return tempPath
}

// TestUnlockRecordsTheLockRelease: releasing a lock file is a mutation of the
// repository's coordination state, and it is exactly the kind of thing an agent
// driving safegit in machine mode needs to see it did.
func TestUnlockRecordsTheLockRelease(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"dry run", []string{"--json", "--dry-run", "unlock", "main"}},
		{"real", []string{"--json", "unlock", "main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			lockPath := plantStaleLockFile(t, dir, "refs/heads/main")

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if code != 0 {
				t.Fatalf("unlock failed (%d): %s", code, stderr)
			}
			env := decodeEnvelope(t, stdout)
			if len(env.Preview) == 0 {
				t.Fatalf("unlock recorded no effects at all; the lock release is invisible in machine mode: %s", stdout)
			}
			if !anyDetailContains(env, lockPath) && !anyDetailContains(env, "main.lock") {
				t.Errorf("no effect record names the lock that was released (%s), got %v", lockPath, effectDetails(env))
			}
		})
	}
}

// TestDoctorFixRecordsItsRemovals: `doctor --action fix` is a sweep, and a sweep
// is precisely the operation whose extent nobody can reconstruct afterwards. It
// removes files today and reports the count in human text only.
func TestDoctorFixRecordsItsRemovals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"dry run", []string{"--json", "--dry-run", "doctor", "--action", "fix"}},
		{"real", []string{"--json", "doctor", "--action", "fix"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newRepo(t)
			lockPath := plantStaleLockFile(t, dir, "refs/heads/main")
			tempPath := plantOrphanedPublicationTemp(t, dir)

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if code != 0 {
				t.Fatalf("doctor --action fix failed (%d): %s", code, stderr)
			}
			env := decodeEnvelope(t, stdout)
			if len(env.Preview) == 0 {
				t.Fatalf("doctor --action fix recorded no effects at all; its removals are invisible in machine mode: %s", stdout)
			}
			for what, path := range map[string]string{
				"the stale lock":                lockPath,
				"the orphaned publication temp": tempPath,
			} {
				if !anyDetailContains(env, path) && !anyDetailContains(env, filepath.Base(path)) {
					t.Errorf("no effect record names %s (%s), got %v", what, path, effectDetails(env))
				}
			}
		})
	}
}

// TestBackupDryRunRecordsThePush: the preview returns before the push is minted,
// so an envelope that promises a backup carries no effect describing it. Only
// the dry-run form is pinned here, which needs no network at all: the remote is
// a bare repository on disk and the preview contacts nothing.
func TestBackupDryRunRecordsThePush(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	commitFileIn(t, dir, "a.txt", "one\n", "first")

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "backup", "backup")
	if code != 0 {
		t.Fatalf("backup backup --json --dry-run failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if len(env.Preview) == 0 {
		t.Fatalf("a backup preview recorded no effects; the push it promises is invisible in machine mode: %s", stdout)
	}
	if !anyDetailContains(env, "push") {
		t.Errorf("no effect record describes the push, got %v", effectDetails(env))
	}
	if !anyDetailContains(env, "refs/backups/main") {
		t.Errorf("no effect record names the backup slot the push would write, got %v", effectDetails(env))
	}
}

// TestBackupRestoreDryRunRecordsTheFastForward: same shape on the restore side,
// where the mutation is the fast-forward merge onto the fetched slot.
func TestBackupRestoreDryRunRecordsTheFastForward(t *testing.T) {
	dir, _ := newRepoWithRemote(t)
	commitFileIn(t, dir, "a.txt", "one\n", "first")
	// A real backup first, so a slot exists for the restore to read: the
	// preview refuses outright when the branch has none.
	if _, stderr, code := runSafegit(t, dir, "backup", "backup"); code != 0 {
		t.Fatalf("fixture backup failed (%d): %s", code, stderr)
	}

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "backup", "restore")
	if code != 0 {
		t.Fatalf("backup restore --json --dry-run failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if len(env.Preview) == 0 {
		t.Fatalf("a restore preview recorded no effects; the fast-forward it promises is invisible in machine mode: %s", stdout)
	}
	if !anyDetailContains(env, "merge") {
		t.Errorf("no effect record describes the fast-forward merge, got %v", effectDetails(env))
	}
}

// TestSubmoduleCommitPreviewRecordsTheParentBump: committing in a submodule with
// commit.autoBumpParent on spawns a SECOND commit, in the parent repository,
// through the effects handle under the declared "parent-bump" grant. The
// preview records the child's own ref update and stops there, so the envelope
// describes one commit where the run would make two -- and the human note that
// does mention the parent ("parent: would bump ... pointer") is written through
// the writer machine mode silences, so a machine consumer never sees it either.
func TestSubmoduleCommitPreviewRecordsTheParentBump(t *testing.T) {
	parentDir, _ := newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	enableAutoBump(t, parentDir)

	// A real submodule commit while the parent's gitlink stays behind, so the
	// preview below is of a commit that really would bump the parent.
	testutil.WriteFile(t, subDir, "file.txt", "real content\n")
	if _, stderr, code := runSafegit(t, subDir, "commit", "-m", "real sub change", "--", "file.txt"); code != 0 {
		t.Fatalf("fixture submodule commit failed (%d): %s", code, stderr)
	}
	enableAutoBump(t, parentDir)

	testutil.WriteFile(t, subDir, "file.txt", "previewed content\n")
	stdout, stderr, code := runSafegit(t, subDir, "--json", "--dry-run", "commit", "-m", "previewed", "--", "file.txt")
	if code != 0 {
		t.Fatalf("submodule commit --json --dry-run failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)

	var bump map[string]interface{}
	for _, rec := range env.Preview {
		if grant, _ := rec["grant"].(string); grant == "parent-bump" {
			bump = rec
			break
		}
	}
	if bump == nil {
		t.Fatalf("the preview carries no record of the parent bump the run would spawn (the \"parent-bump\" grant the execute path uses): %v", env.Preview)
	}
	if recorded, _ := bump["recorded"].(bool); !recorded {
		t.Error("the parent bump is not marked recorded, which means the preview performed it")
	}
}
