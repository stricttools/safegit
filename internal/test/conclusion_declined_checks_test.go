package test

import (
	"encoding/json"
	"strings"
	"testing"
)

// The marker verification skips a path carrying the committed
// `-safegit-conflict-markers` exemption. The skip used to be SILENT, so a
// conclusion reported a clean verdict over content nothing had looked at. It is
// now carried out to the report as a DECLINED CHECK, in both channels.

// declinedCheckPayload is the payload members this test reads: the list of
// checks the conclusion did NOT make.
type declinedCheckPayload struct {
	DeclinedChecks []struct {
		Check  string `json:"check"`
		Path   string `json:"path"`
		Reason string `json:"reason"`
	} `json:"declined_checks"`
}

// TestConclusionReportsTheDeclinedMarkerCheck: a path carrying the committed
// `-safegit-conflict-markers` exemption is skipped by the marker verification.
// The skip was SILENT, so a conclusion reported a clean verdict over content
// nothing had checked. It is now carried out to the report, in both channels.
func TestConclusionReportsTheDeclinedMarkerCheck(t *testing.T) {
	const declaration = "f.txt -safegit-conflict-markers\n"
	opts := markerRepoOpts{
		base: map[string]string{
			".gitattributes": declaration,
			"f.txt":          "line1\nbase\nline3\n",
		},
		ours:   map[string]string{"f.txt": "line1\nmain\nline3\n"},
		theirs: map[string]string{"f.txt": "line1\nfeature\nline3\n"},
	}

	t.Run("text", func(t *testing.T) {
		fx := newMarkerRepo(t, opts)
		stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree")
		if code != 0 {
			t.Fatalf("the exempted conclusion must succeed (code %d): %s", code, stderr)
		}
		if !strings.Contains(stdout, "declined") {
			t.Errorf("the report does not say a check was declined:\nstdout=%s", stdout)
		}
		if !strings.Contains(stdout, "f.txt") {
			t.Errorf("the report does not name the path whose check was declined:\nstdout=%s", stdout)
		}
		if !strings.Contains(stdout, markerExemptionAttrName) {
			t.Errorf("the report does not name the declaration that declined the check:\nstdout=%s", stdout)
		}
	})

	t.Run("payload", func(t *testing.T) {
		fx := newMarkerRepo(t, opts)
		stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"--json", "merge-continue", "--resolve", "f.txt=worktree")
		if code != 0 {
			t.Fatalf("the exempted conclusion must succeed (code %d): %s", code, stderr)
		}
		var p declinedCheckPayload
		if err := json.Unmarshal([]byte(jsonPayload(t, stdout)), &p); err != nil {
			t.Fatalf("decoding the conclusion payload: %v\nstdout=%s", err, stdout)
		}
		if len(p.DeclinedChecks) == 0 {
			t.Fatalf("the payload carries no declined_checks member:\nstdout=%s", stdout)
		}
		found := false
		for _, d := range p.DeclinedChecks {
			if d.Path == "f.txt" {
				found = true
				if d.Check == "" || d.Reason == "" {
					t.Errorf("a declined check must name the check and the reason: %+v", d)
				}
			}
		}
		if !found {
			t.Errorf("declined_checks does not name f.txt: %+v", p.DeclinedChecks)
		}
	})
}

// markerExemptionAttrName is the attribute spelling the report has to print, so
// an operator reading "declined" can find the declaration that caused it.
const markerExemptionAttrName = "safegit-conflict-markers"
