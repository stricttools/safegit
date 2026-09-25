package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/hooks"
)

// decodeRecord renders one hook run through hookRecords and decodes its JSON,
// so the assertions read the members a machine consumer reads.
func decodeRecord(t *testing.T, r hooks.HookResult) map[string]json.RawMessage {
	t.Helper()
	recs := hookRecords([]hooks.HookResult{r})
	if len(recs) != 1 {
		t.Fatalf("hookRecords returned %d records, want 1", len(recs))
	}
	raw, err := json.Marshal(recs[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestHookRecordStatesUnidentifiedLeftovers: where safegit could not name what
// still held a hook's output -- on macOS, the lsof it names them with was not
// found or failed -- the run fails with exit 20, and the record says so as a
// fact: unidentified_leftovers is true and leftover_identification_error
// carries why identification failed. The reason is the one the lsof seam
// produces for a missing lsof.
func TestHookRecordStatesUnidentifiedLeftovers(t *testing.T) {
	reason := "lsof, which names them, was not found: exec: \"lsof\": executable file not found in $PATH"
	m := decodeRecord(t, hooks.HookResult{Name: "10-lint", Duration: time.Second, LeftoverUnknown: reason})
	if got := string(m["unidentified_leftovers"]); got != "true" {
		t.Errorf("unidentified_leftovers = %s, want true", got)
	}
	var msg *string
	if err := json.Unmarshal(m["leftover_identification_error"], &msg); err != nil || msg == nil || *msg != reason {
		t.Errorf("leftover_identification_error = %s, want %q", m["leftover_identification_error"], reason)
	}
	if !strings.Contains(string(m["leftover_processes"]), "[]") {
		t.Errorf("leftover_processes = %s, want an empty list", m["leftover_processes"])
	}
}

// TestHookRecordStatesIdentifiedLeftoversAsFalse: a run with nothing
// unidentified carries the two members as false and null, never absent.
func TestHookRecordStatesIdentifiedLeftoversAsFalse(t *testing.T) {
	m := decodeRecord(t, hooks.HookResult{Name: "10-lint", Duration: time.Second})
	if got := string(m["unidentified_leftovers"]); got != "false" {
		t.Errorf("unidentified_leftovers = %q, want false", got)
	}
	if got, ok := m["leftover_identification_error"]; !ok || string(got) != "null" {
		t.Errorf("leftover_identification_error = %q (present %v), want null", got, ok)
	}
}

// TestHookRecordOfATimedOutHookHasNoExitCode: a timed-out hook was killed, so it
// has no exit status of its own. Its record carries exit_code null and
// timed_out true; safegit's own exit code 21 is not the hook's.
func TestHookRecordOfATimedOutHookHasNoExitCode(t *testing.T) {
	m := decodeRecord(t, hooks.HookResult{Name: "10-lint", Duration: time.Second, TimedOut: true, ExitCode: 21})
	if got, ok := m["exit_code"]; !ok || string(got) != "null" {
		t.Errorf("exit_code = %q (present %v), want null", got, ok)
	}
	if got := string(m["timed_out"]); got != "true" {
		t.Errorf("timed_out = %s, want true", got)
	}

	m = decodeRecord(t, hooks.HookResult{Name: "20-test", Duration: time.Second, ExitCode: 3})
	if got := string(m["exit_code"]); got != "3" {
		t.Errorf("exit_code of a hook that exited 3 = %s, want 3", got)
	}
}
