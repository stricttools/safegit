package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/hooks"
)

// TestPushPayloadHasNoHookCount: the number of hooks that ran is the length of
// the hooks list, and the payload carries no second member restating it.
func TestPushPayloadHasNoHookCount(t *testing.T) {
	raw, err := json.Marshal(buildPushPayload(globalFlags{}, "origin", nil, false, []hooks.HookResult{{Name: "10-lint"}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pre_pre_push_hooks_run") {
		t.Errorf("the push payload carries pre_pre_push_hooks_run; len(hooks) is the count:\n%s", raw)
	}
	schema, err := json.Marshal(pushPayloadSchema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(schema), "pre_pre_push_hooks_run") {
		t.Errorf("the push payload schema declares pre_pre_push_hooks_run:\n%s", schema)
	}
}
