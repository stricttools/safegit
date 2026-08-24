package gitexec

import (
	"strings"
	"testing"
)

// TestDirPinExemptionTableIsEnumerated is the enumerating test the declared
// exemption table exists for. The whole point of the table is that the set of
// sites escaping the repository-root pin is CLOSED and visible; a new entry
// must be added here deliberately, with its kind, rather than appearing in a
// diff nobody reads.
func TestDirPinExemptionTableIsEnumerated(t *testing.T) {
	want := map[ExemptionID]ExemptionKind{
		// Sites handed a repository as an argument.
		ExemptRunWithGitDir:           KindExplicitDir,
		ExemptCatFileBatchAllWithDir:  KindExplicitDir,
		ExemptCatFileBatchSHAsWithDir: KindExplicitDir,
		ExemptSubmoduleRunGit:         KindExplicitDir,
		ExemptAutoBumpParentPointer:   KindExplicitDir,
		// Sites forwarding the operator's own argv.
		ExemptGuardedPassthrough: KindOperatorCwd,
		ExemptGitMutation:        KindOperatorCwd,
		// Argv the strictcli effects handle runs, not safegit.
		ExemptGitPush:              KindEffectsHandle,
		ExemptCommitRefUpdate:      KindEffectsHandle,
		ExemptUndoRefUpdate:        KindEffectsHandle,
		ExemptBackupFetch:          KindEffectsHandle,
		ExemptDoctorRepair:         KindEffectsHandle,
		ExemptHistoryRewriteRecord: KindEffectsHandle,
	}

	got := DirPinExemptions()
	if len(got) != len(want) {
		t.Errorf("the exemption table has %d entries, this test enumerates %d", len(got), len(want))
	}
	seen := map[ExemptionID]bool{}
	for _, e := range got {
		seen[e.ID] = true
		kind, ok := want[e.ID]
		if !ok {
			t.Errorf("undeclared exemption %q in the table; add it here with its kind and justify it", e.ID)
			continue
		}
		if e.Kind != kind {
			t.Errorf("exemption %q is declared %q, this test expects %q", e.ID, e.Kind, kind)
		}
		if strings.TrimSpace(e.Reason) == "" {
			t.Errorf("exemption %q carries no reason", e.ID)
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("exemption %q is enumerated here but missing from the table", id)
		}
	}
}

// TestExemptionKindsAreClosed pins the three kinds themselves.
func TestExemptionKindsAreClosed(t *testing.T) {
	allowed := map[ExemptionKind]bool{
		KindExplicitDir: true, KindOperatorCwd: true, KindEffectsHandle: true,
	}
	for _, e := range DirPinExemptions() {
		if !allowed[e.Kind] {
			t.Errorf("exemption %q declares the unknown kind %q", e.ID, e.Kind)
		}
	}
}

// TestDirPinExemptionsIsACopy: the table is the boundary's, not a caller's.
func TestDirPinExemptionsIsACopy(t *testing.T) {
	got := DirPinExemptions()
	got[0].Reason = "tampered"
	if DirPinExemptions()[0].Reason == "tampered" {
		t.Error("DirPinExemptions handed out the package's own slice")
	}
}

func TestMustBeExemptPanicsOnUndeclared(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("MustBeExempt accepted an undeclared identifier")
		}
	}()
	MustBeExempt(ExemptionID("nope"), KindOperatorCwd)
}
