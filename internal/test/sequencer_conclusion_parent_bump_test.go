package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING: a conclusion PREVIEW in a submodule recorded nothing for the parent
// bump.
//
// A conclusion that commits in a submodule whose parent has commit.autoBumpParent
// on spawns a SECOND commit, in a SECOND repository, moving the parent's gitlink.
// The conclusion commands skipped their whole aftercare under --dry-run
// (`if !flags.dryRun` around concludeAftercare), so the preview described one
// commit where the run makes two -- the same defect the ordinary commit path was
// fixed for, at the two doors into the conclusion engine: the -continue command,
// and the re-run that finds a crashed conclusion's commit already standing.
//
// RULED TARGET: the dry path reaches the parent-bump record, and ONLY that.
// The other aftercare steps -- the state-file removals, the index and
// working-tree writes, the autostash -- are execute-path effects a preview
// neither performs nor records, so nothing here invents a record for them.

// newConflictedMergeInSubmodule parks a parent repository's submodule in a
// conflicted merge, with the parent's auto-bump answer set to `true`.
//
// The order is deliberate: every commit the fixture makes happens while the
// parent's answer is still `false`, so the gitlink is left BEHIND the submodule's
// branch and a conclusion really would bump it. Enabling auto-bump last is what
// makes the preview below a preview of a bump that would happen.
func newConflictedMergeInSubmodule(t *testing.T) (parentDir string, fx conflictedMergeFixture) {
	t.Helper()
	parentDir, _ = newRepoWithSubmodule(t)
	subDir := prepSubmoduleForCommit(t, parentDir)
	fx = newConflictedMergeRepo(t, conflictedMergeOpts{
		dir:           subDir,
		env:           conclusionSession,
		cleanSideFile: true,
	})
	enableAutoBump(t, parentDir)
	return parentDir, fx
}

// parentBumpAfterRefUpdate is the assertion both previews below make: among the
// subprocess mutations the envelope carries, the parent bump is present and it
// comes AFTER the ref update, because that is the order the execute path
// performs them in.
func parentBumpAfterRefUpdate(t *testing.T, env machineEnvelope) {
	t.Helper()
	mutations := procMutations(env)
	refIdx, bumpIdx := -1, -1
	for i, rec := range mutations {
		if grant, _ := rec["grant"].(string); grant == "parent-bump" {
			if bumpIdx < 0 {
				bumpIdx = i
			}
			continue
		}
		if detail, _ := rec["detail"].(string); strings.Contains(detail, "update-ref") && refIdx < 0 {
			refIdx = i
		}
	}
	if bumpIdx < 0 {
		t.Fatalf("the preview carries no record of the parent bump the run would spawn (the %q grant the execute path uses): %v",
			"parent-bump", effectDetails(env))
	}
	if refIdx < 0 {
		t.Fatalf("the preview carries no record of the conclusion's own ref update: %v", effectDetails(env))
	}
	if bumpIdx < refIdx {
		t.Errorf("the parent bump is recorded at %d, before the ref update at %d; the execute path moves the ref first: %v",
			bumpIdx, refIdx, effectDetails(env))
	}
	for i, rec := range mutations {
		if recorded, _ := rec["recorded"].(bool); !recorded {
			t.Errorf("record %d is not marked recorded, which means the preview performed it: %v", i, rec)
		}
	}
}

// assertPreviewInventedNoAftercareRecords pins the other half of the ruling: the
// aftercare steps a preview does NOT perform get no record either. A record for
// a removal the run withheld would be a lie of the same kind as the missing bump.
func assertPreviewInventedNoAftercareRecords(t *testing.T, env machineEnvelope) {
	t.Helper()
	for _, detail := range effectDetails(env) {
		for _, name := range []string{"MERGE_HEAD", "MERGE_MSG", "MERGE_MODE", "AUTO_MERGE", "MERGE_AUTOSTASH"} {
			if strings.Contains(detail, name) {
				t.Errorf("the preview records an aftercare effect it does not perform (%s): %s", name, detail)
			}
		}
	}
}

// parentBumpDetail returns the recorded argv of the parent bump the preview
// carries, failing when there is none.
func parentBumpDetail(t *testing.T, env machineEnvelope) string {
	t.Helper()
	for _, rec := range procMutations(env) {
		if grant, _ := rec["grant"].(string); grant == "parent-bump" {
			detail, _ := rec["detail"].(string)
			return detail
		}
	}
	t.Fatalf("the preview carries no parent-bump record: %v", effectDetails(env))
	return ""
}

// TestMergeContinuePreviewInASubmoduleRecordsTheParentBump: the -continue door.
func TestMergeContinuePreviewInASubmoduleRecordsTheParentBump(t *testing.T) {
	parentDir, fx := newConflictedMergeInSubmodule(t)
	subHead := testutil.Rev(t, fx.dir, "HEAD")
	parentHead := testutil.Rev(t, parentDir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--json", "--dry-run", "merge-continue", "--resolve", "conflicted.txt=ours")
	if code != 0 {
		t.Fatalf("merge-continue --json --dry-run in a submodule exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	env := decodeEnvelope(t, stdout)
	parentBumpAfterRefUpdate(t, env)
	assertPreviewInventedNoAftercareRecords(t, env)

	// The ORDINARY door keeps the placeholder: the conclusion's own commit does
	// not exist yet -- the object a real run builds carries the committer
	// timestamp -- so no preview can name it, and the placeholder is the honest
	// value. The crash re-stand door below is the opposite case.
	if detail := parentBumpDetail(t, env); !strings.Contains(detail, "Triggered-by: <new-commit>") {
		t.Errorf("the -continue preview's parent bump must carry the placeholder, since the commit it triggers on does not exist yet: %s", detail)
	}

	// Neither repository moved, and the merge is still in flight: a preview
	// performs no aftercare at all.
	if now := testutil.Rev(t, fx.dir, "HEAD"); now != subHead {
		t.Errorf("the preview moved the submodule's HEAD: %s -> %s", subHead, now)
	}
	if now := testutil.Rev(t, parentDir, "HEAD"); now != parentHead {
		t.Errorf("the preview committed in the parent: %s -> %s", parentHead, now)
	}
	if _, err := os.Stat(filepath.Join(submoduleGitDir(t, fx.dir), "MERGE_HEAD")); err != nil {
		t.Errorf("the preview removed the merge's state files: %v", err)
	}
	if n := unmergedCount(t, fx.dir); n == 0 {
		t.Error("the preview resolved the unmerged index; it writes nothing")
	}
}

// TestCrashReStandPreviewInASubmoduleRecordsTheParentBump: the SECOND door, where
// the conclusion's commit already stands and what is left is the aftercare a
// crash interrupted. The bump is exactly the part of that aftercare that was
// never made, so a preview of the re-run must describe it.
func TestCrashReStandPreviewInASubmoduleRecordsTheParentBump(t *testing.T) {
	parentDir, fx := newConflictedMergeInSubmodule(t)

	// The conclusion, then the crash. The parent's answer goes back to `false`
	// for the real run, so the commit that stands left the gitlink behind --
	// which is the state whose preview must promise the bump.
	if _, stderr, code := runSafegit(t, parentDir, "config", "set", "commit.autoBumpParent", "false"); code != 0 {
		t.Fatalf("disabling auto-bump for the fixture's real conclusion: %s", stderr)
	}
	snap := snapshotCrashWindow(t, fx.dir)
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=ours"); code != 0 {
		t.Fatalf("the fixture's merge-continue failed (code %d): %s", code, stderr)
	}
	concluded := testutil.Rev(t, fx.dir, "HEAD")
	restoreCrashWindow(t, fx.dir, snap)
	enableAutoBump(t, parentDir)

	parentHead := testutil.Rev(t, parentDir, "HEAD")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--json", "--dry-run", "merge-continue", "--resolve", "conflicted.txt=ours")
	if code != 0 {
		t.Fatalf("the crash re-run's preview exited %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	env := decodeEnvelope(t, stdout)

	// No ref update here: the commit already stands, so the bump is the ONLY
	// mutation the re-run would make -- and a preview that recorded nothing at
	// all described a run that does something.
	mutations := procMutations(env)
	found := false
	for _, rec := range mutations {
		if grant, _ := rec["grant"].(string); grant == "parent-bump" {
			found = true
			if recorded, _ := rec["recorded"].(bool); !recorded {
				t.Error("the parent bump is not marked recorded, which means the preview performed it")
			}
		}
	}
	if !found {
		t.Errorf("the crash re-run's preview carries no parent-bump record; the bump is the one mutation left to make: %v",
			effectDetails(env))
	}
	assertPreviewInventedNoAftercareRecords(t, env)

	// The Triggered-by value is the STOOD commit's real object name, not the
	// placeholder. The placeholder stands where a preview CANNOT know the name --
	// a commit that does not exist yet -- and this door authors nothing: the
	// conclusion's commit is already on the branch and was read off it before the
	// record was made. A preview that hid a value it held would understate the
	// argv the run performs.
	bumpDetail := parentBumpDetail(t, env)
	if !strings.Contains(bumpDetail, "Triggered-by: "+concluded) {
		t.Errorf("the parent bump's Triggered-by must carry the stood commit %s, got: %s", concluded, bumpDetail)
	}
	if strings.Contains(bumpDetail, "<new-commit>") {
		t.Errorf("the parent bump records the new-commit placeholder where the re-run knows the stood commit's real object name: %s", bumpDetail)
	}

	if now := testutil.Rev(t, fx.dir, "HEAD"); now != concluded {
		t.Errorf("the preview moved the submodule's HEAD: %s -> %s", concluded, now)
	}
	if now := testutil.Rev(t, parentDir, "HEAD"); now != parentHead {
		t.Errorf("the preview committed in the parent: %s -> %s", parentHead, now)
	}
}
