package main

import "testing"

// TestDoctorCheckRegistryIsWellFormed pins the registry's own rules, so a later
// subphase that adds a check gets told immediately when its entry is
// malformed rather than producing a silently mis-reported finding.
func TestDoctorCheckRegistryIsWellFormed(t *testing.T) {
	if len(doctorChecks) == 0 {
		t.Fatal("the doctor check registry is empty")
	}
	seen := map[string]bool{}
	for _, c := range doctorChecks {
		if c.Name == "" {
			t.Error("a registered check has no name")
			continue
		}
		if seen[c.Name] {
			t.Errorf("duplicate check name %q: two checks would report under one name", c.Name)
		}
		seen[c.Name] = true
		switch c.Severity {
		case "warn", "error":
		default:
			t.Errorf("check %q declares severity %q; want warn or error", c.Name, c.Severity)
		}
		if c.Fn == nil {
			t.Errorf("check %q has no function", c.Name)
		}
	}
}

// TestDoctorFindingStatusResolution pins how a finding becomes a reported
// status: ok wins over the declared severity, a plain failure takes the
// declared severity, an explicit status overrides it, and findingNone reports
// nothing at all.
func TestDoctorFindingStatusResolution(t *testing.T) {
	cases := []struct {
		name     string
		severity string
		finding  doctorFinding
		wantRep  bool
		want     string
	}{
		{"ok", "warn", findingOK(""), true, "ok"},
		{"fail takes severity", "warn", findingFail("x"), true, "warn"},
		{"fail takes error severity", "error", findingFail("x"), true, "error"},
		{"override", "warn", findingAt("error", "x"), true, "error"},
		{"none", "warn", findingNone(), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, reported := resolveFinding(doctorCheck{Name: "x", Severity: tc.severity}, tc.finding)
			if reported != tc.wantRep {
				t.Fatalf("reported = %v, want %v", reported, tc.wantRep)
			}
			if reported && status != tc.want {
				t.Errorf("status = %q, want %q", status, tc.want)
			}
		})
	}
}
