package main

import (
	"flag"
	"os"
	"testing"
)

// updateDoctorTable is the GENERATOR's switch, and the reason the generator is a
// test rather than a `go run` script like the exit-code table's.
//
// The check registry lives in package main, next to the check functions it binds,
// and no external program can import package main. The two honest ways out were
// to split the registry's metadata into an importable package -- leaving the
// names, severities and functions to be kept aligned across two lists, which is
// the drift class this work exists to kill, in a new place -- or to run the
// generator from inside the package that holds the registry. This is the second.
// The script under scripts/ is still the entry point an operator runs; it invokes
// this one test with this flag.
var updateDoctorTable = flag.Bool("update-doctor-table", false,
	"rewrite the generated health-check table in the commands guide from the doctor check registry (used by scripts/gen-doctor-table)")

// TestDoctorHealthCheckTableIsGenerated is the freshness check for the generated
// health-check table, and -- under -update-doctor-table -- the generator itself.
// The registry is the single authority; without the flag this fails when the
// table on disk no longer matches what the registry renders, which is exactly the
// drift it exists to prevent (the hand-typed table carried a `warn` where the
// registry said `error`, and had no row for one registered check at all).
//
// Whole documents are compared, so a drifted row, a moved marker and a hand edit
// inside the region all fail the same way, with one instruction.
func TestDoctorHealthCheckTableIsGenerated(t *testing.T) {
	raw, err := os.ReadFile(doctorDocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", doctorDocPath, err)
	}
	want, err := renderDoctorDocument(string(raw))
	if err != nil {
		t.Fatalf("%s: %v", doctorDocPath, err)
	}

	if *updateDoctorTable {
		if want == string(raw) {
			t.Logf("%s: already current", doctorDocPath)
			return
		}
		if err := os.WriteFile(doctorDocPath, []byte(want), 0o644); err != nil {
			t.Fatalf("writing %s: %v", doctorDocPath, err)
		}
		t.Logf("%s: health-check table regenerated", doctorDocPath)
		return
	}

	if string(raw) != want {
		table, renderErr := doctorHealthCheckTable()
		if renderErr != nil {
			t.Fatalf("rendering the registry: %v", renderErr)
		}
		t.Errorf("the health-check table in %s is stale; run scripts/gen-doctor-table\n--- from the registry ---\n%s",
			doctorDocPath, table)
	}
}

// TestEveryDoctorCheckDeclaresAQuestion states the registration requirement on
// its own, so an entry that documents nothing fails with its own name rather
// than as a rendering error inside the freshness check.
func TestEveryDoctorCheckDeclaresAQuestion(t *testing.T) {
	if _, err := doctorHealthCheckTable(); err != nil {
		t.Error(err)
	}
	for _, c := range doctorChecks {
		if c.Severity != "warn" && c.Severity != "error" {
			t.Errorf("doctor check %q declares severity %q, which is neither warn nor error", c.Name, c.Severity)
		}
	}
}
