package main

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stricttools/safegit/internal/hooks"
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
// timed_out true, and nothing stands in for the missing status.
func TestHookRecordOfATimedOutHookHasNoExitCode(t *testing.T) {
	m := decodeRecord(t, hooks.HookResult{Name: "10-lint", Duration: time.Second, TimedOut: true})
	if got, ok := m["exit_code"]; !ok || string(got) != "null" {
		t.Errorf("exit_code = %q (present %v), want null", got, ok)
	}
	if got := string(m["timed_out"]); got != "true" {
		t.Errorf("timed_out = %s, want true", got)
	}

	three := 3
	m = decodeRecord(t, hooks.HookResult{Name: "20-test", Duration: time.Second, ExitCode: &three})
	if got := string(m["exit_code"]); got != "3" {
		t.Errorf("exit_code of a hook that exited 3 = %s, want 3", got)
	}
}

// snakeCase matches a JSON member name in the repository's convention.
var snakeCase = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// TestHookRecordMembersAreSnakeCase: every member of the emitted hook record,
// its leftover entries included, is snake_case and declared by the schema, and
// the internal run result carries no JSON spelling of its own -- hookRecord is
// the one form a hook run is emitted in, so a second set of member names on
// hooks.HookResult could only ever disagree with it.
func TestHookRecordMembersAreSnakeCase(t *testing.T) {
	recs := hookRecords([]hooks.HookResult{{
		Name:      "10-lint",
		Leftovers: []hooks.LeftoverProcess{{PID: 1, Command: "sleep"}},
	}})
	raw, err := json.Marshal(recs[0])
	if err != nil {
		t.Fatal(err)
	}
	var rec map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	var left []map[string]json.RawMessage
	if err := json.Unmarshal(rec["leftover_processes"], &left); err != nil || len(left) != 1 {
		t.Fatalf("leftover_processes = %s (%v)", rec["leftover_processes"], err)
	}
	for _, m := range []map[string]json.RawMessage{rec, left[0]} {
		for k := range m {
			if !snakeCase.MatchString(k) {
				t.Errorf("emitted member %q is not snake_case", k)
			}
		}
	}
	var required []string
	for _, k := range hookRecordSchema["required"].([]interface{}) {
		required = append(required, k.(string))
	}
	if len(required) != len(rec) {
		t.Errorf("the record emits %d members and the schema requires %d: %s", len(rec), len(required), raw)
	}
	for _, k := range required {
		if _, ok := rec[k]; !ok {
			t.Errorf("the schema requires %q and the record does not emit it: %s", k, raw)
		}
	}

	for _, typ := range []reflect.Type{reflect.TypeOf(hooks.HookResult{}), reflect.TypeOf(hooks.LeftoverProcess{})} {
		for i := 0; i < typ.NumField(); i++ {
			if tag, ok := typ.Field(i).Tag.Lookup("json"); ok {
				t.Errorf("%s.%s carries json tag %q; the emitted form is hookRecord alone", typ.Name(), typ.Field(i).Name, tag)
			}
		}
	}
}

// TestLeftoverStateIsNullWithoutProc: a leftover's state is the letter /proc
// reported when safegit found it, and null where there was none to read -- the
// platform has no /proc.
func TestLeftoverStateIsNullWithoutProc(t *testing.T) {
	recs := hookRecords([]hooks.HookResult{{
		Name: "10-lint",
		Leftovers: []hooks.LeftoverProcess{
			{PID: 1, Command: "sleep", FoundState: "S"},
			{PID: 2, Command: "node"},
		},
	}})
	left := recs[0].LeftoverProcesses
	if len(left) != 2 {
		t.Fatalf("leftover_processes = %+v", left)
	}
	if left[0].State == nil || *left[0].State != "S" {
		t.Errorf("state = %v, want \"S\"", left[0].State)
	}
	if left[1].State != nil {
		t.Errorf("state = %q, want null", *left[1].State)
	}
	raw, err := json.Marshal(left[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"state":null`) {
		t.Errorf("a leftover with no state must carry state null: %s", raw)
	}
}
