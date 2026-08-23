package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING 2 (second-campaign adversarial review): undo's effects record is
// empty in BOTH modes.
//
//   - `undo --json --dry-run` answers preview:[] while its human line promises
//     a rollback ("would undo 1 operation(s) on main / <a> -> <b>"). A machine
//     consumer reading the envelope is told the run would change nothing.
//   - `undo --json` (real) answers preview:[] and payload:null even though the
//     branch ref moved. The mutation happened and left no machine-readable
//     trace at all.
//
// The cause is that undo calls git.UpdateRef / git.DeleteRef directly rather
// than minting the ref move through the effects handle, and its dry-run branch
// returns after printing text.
//
// RULED TARGET: a real would-do record for the ref move, carrying the argv
// shape the execute path runs -- `update-ref <ref> <new> <old>` for an ordinary
// undo and `update-ref -d <ref> <old>` for a root undo -- with REAL SHAs in
// both, because unlike a commit's preview undo knows every value in advance:
// the rollback target and the compare-and-swap pin both come out of the oplog.
// The record is marked recorded:true under --dry-run and recorded:false on a
// real run, like every other minted mutation. Plus a payload, so a machine
// consumer learns what was undone.
//
// These tests are RED on purpose until that exists.

// undoFixture commits one file through safegit on top of newRepo's initial
// commit and returns the parent (the rollback target) and the new tip (the
// compare-and-swap pin) -- the two SHAs undo's recorded argv must carry.
func undoFixture(t *testing.T) (dir, parent, tip string) {
	t.Helper()
	dir = newRepo(t)
	parent = testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	tip = safegitCommit(t, dir, "undo me", "a.txt")
	if parent == tip {
		t.Fatalf("fixture: HEAD did not advance, both are %s", tip)
	}
	return dir, parent, tip
}

// TestUndoDryRunRecordsTheRefUpdate: the preview must say what it would change.
func TestUndoDryRunRecordsTheRefUpdate(t *testing.T) {
	dir, parent, tip := undoFixture(t)

	stdout, stderr, code := runSafegit(t, dir, "--json", "--dry-run", "undo", "--bypass-session")
	if code != 0 {
		t.Fatalf("undo --json --dry-run failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)
	if !env.DryRun {
		t.Error("dry_run = false under --dry-run")
	}

	mutations := procMutations(env)
	if len(mutations) != 1 {
		t.Fatalf("an undo preview recorded %d subprocess mutations, want exactly 1 (the ref move it promises in its human line): %v",
			len(mutations), env.Preview)
	}
	want := "update-ref refs/heads/main " + parent + " " + tip
	if detail, _ := mutations[0]["detail"].(string); !strings.Contains(detail, want) {
		t.Errorf("the recorded ref move must be %q with both real SHAs, got %q", want, detail)
	}
	if recorded, _ := mutations[0]["recorded"].(bool); !recorded {
		t.Error("a preview's effect is not marked recorded, which means it was performed")
	}

	if now := testutil.Rev(t, dir, "HEAD"); now != tip {
		t.Errorf("the undo preview moved HEAD: %s -> %s", tip, now)
	}
}

// TestUndoRecordsTheRefUpdateAndCarriesAPayload: the same record on the execute
// side, where the ref really moves, plus the payload that tells a machine
// consumer what was undone.
func TestUndoRecordsTheRefUpdateAndCarriesAPayload(t *testing.T) {
	dir, parent, tip := undoFixture(t)

	stdout, stderr, code := runSafegit(t, dir, "--json", "undo", "--bypass-session")
	if code != 0 {
		t.Fatalf("undo --json failed (%d): %s", code, stderr)
	}
	env := decodeEnvelope(t, stdout)

	mutations := procMutations(env)
	if len(mutations) != 1 {
		t.Fatalf("an executing undo recorded %d subprocess mutations, want exactly 1: %v", len(mutations), env.Preview)
	}
	want := "update-ref refs/heads/main " + parent + " " + tip
	if detail, _ := mutations[0]["detail"].(string); !strings.Contains(detail, want) {
		t.Errorf("the recorded ref move must be %q, got %q", want, detail)
	}
	if recorded, _ := mutations[0]["recorded"].(bool); recorded {
		t.Error("an executing run's effect is marked recorded, which means it was not performed")
	}

	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		t.Errorf("undo carries no payload, so a machine consumer cannot learn what was undone: %s", stdout)
	}

	// The counterpart: the mutation the envelope must describe really happened.
	if now := testutil.Rev(t, dir, "HEAD"); now != parent {
		t.Errorf("undo did not roll the branch back: HEAD = %s, want %s", now, parent)
	}
}

// newRootCommitRepo creates a repo whose only commit is a root commit made
// through safegit, so the oplog entry records an empty parent and undo takes
// the ref-DELETION path.
func newRootCommitRepo(t *testing.T) (dir, root string) {
	t.Helper()
	dir = evalTempDir(t)
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		testutil.Git(t, dir, args...)
	}
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	root = safegitCommit(t, dir, "root commit", "a.txt")
	return dir, root
}

// TestRootUndoRecordsTheRefDeletion: a root undo has no rollback target -- the
// branch ref goes away entirely -- so its recorded argv is git's deletion form,
// `update-ref -d <ref> <old>`, with the compare-and-swap pin still real. Both
// modes are pinned: the shape a preview promises and the shape the execute path
// runs are the same argv, which is the whole reason to record it.
func TestRootUndoRecordsTheRefDeletion(t *testing.T) {
	for _, tc := range []struct {
		name         string
		args         []string
		wantRecorded bool
	}{
		{"dry run", []string{"--json", "--dry-run", "undo", "--bypass-session"}, true},
		{"real", []string{"--json", "undo", "--bypass-session"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, root := newRootCommitRepo(t)

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if code != 0 {
				t.Fatalf("root undo failed (%d): %s", code, stderr)
			}
			env := decodeEnvelope(t, stdout)

			mutations := procMutations(env)
			if len(mutations) != 1 {
				t.Fatalf("a root undo recorded %d subprocess mutations, want exactly 1 (the ref deletion): %v",
					len(mutations), env.Preview)
			}
			want := "update-ref -d refs/heads/main " + root
			if detail, _ := mutations[0]["detail"].(string); !strings.Contains(detail, want) {
				t.Errorf("the recorded ref deletion must be %q, got %q", want, detail)
			}
			if recorded, _ := mutations[0]["recorded"].(bool); recorded != tc.wantRecorded {
				t.Errorf("recorded = %v, want %v", recorded, tc.wantRecorded)
			}
		})
	}
}
