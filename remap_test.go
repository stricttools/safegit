package main

import (
	"context"
	"strings"
	"testing"
)

const (
	remapOldSHA = "1111111111111111111111111111111111111111"
	remapNewSHA = "2222222222222222222222222222222222222222"
)

// TestRemapContentReplacesMappedShas: full 40-hex candidates present in the
// SHA map as non-identity entries are replaced; identity entries are left.
func TestRemapContentReplacesMappedShas(t *testing.T) {
	rs := newRemapState([]string{"*.jsonl"}, []string{remapOldSHA})
	shaMap := map[string]string{remapOldSHA: remapNewSHA}

	content := []byte(`{"commits":["` + remapOldSHA + `"]}` + "\n")
	out, err := rs.remapContent(context.Background(), content, shaMap)
	if err != nil {
		t.Fatalf("remapContent: %v", err)
	}
	want := `{"commits":["` + remapNewSHA + `"]}` + "\n"
	if string(out) != want {
		t.Errorf("remapContent = %q, want %q", out, want)
	}
}

// TestRemapContentLeavesIdentityMappings: identity entries (walked, unchanged
// commits) are left untouched without any object-store lookup.
func TestRemapContentLeavesIdentityMappings(t *testing.T) {
	rs := newRemapState(nil, []string{remapOldSHA})
	shaMap := map[string]string{remapOldSHA: remapOldSHA}

	content := []byte("ref " + remapOldSHA + " end\n")
	out, err := rs.remapContent(context.Background(), content, shaMap)
	if err != nil {
		t.Fatalf("remapContent: %v", err)
	}
	if string(out) != string(content) {
		t.Errorf("identity-mapped SHA must be untouched, got %q", out)
	}
}

// TestRemapContentLeavesAbbreviatedAndLongerHex: sub-40 abbreviations never
// match; 40+ runs (e.g. SHA-256) are skipped whole, protecting their 40-char
// prefixes.
func TestRemapContentLeavesAbbreviatedAndLongerHex(t *testing.T) {
	rs := newRemapState(nil, nil)
	shaMap := map[string]string{remapOldSHA: remapNewSHA}

	abbrev := remapOldSHA[:12]
	longHex := remapOldSHA + "abcdef0123456789abcdef01" // 64 hex chars starting with the mapped SHA
	content := []byte("a " + abbrev + " b " + longHex + " c\n")
	out, err := rs.remapContent(context.Background(), content, shaMap)
	if err != nil {
		t.Fatalf("remapContent: %v", err)
	}
	if string(out) != string(content) {
		t.Errorf("abbreviated/longer hex must be untouched, got %q", out)
	}
}

// TestRemapContentInRangeUnmappedIsHardError: a candidate inside the rewrite
// range that is not yet in the SHA map means the file references a commit
// that is not an ancestor of the commit being rewritten — a hard error.
// (The range check precedes any git lookup, so no repo is needed.)
func TestRemapContentInRangeUnmappedIsHardError(t *testing.T) {
	rs := newRemapState(nil, []string{remapOldSHA})
	shaMap := map[string]string{} // candidate not walked yet

	content := []byte("ref " + remapOldSHA + "\n")
	_, err := rs.remapContent(context.Background(), content, shaMap)
	if err == nil {
		t.Fatal("expected hard error for in-range unmapped candidate")
	}
	if !strings.Contains(err.Error(), "rewrite range") || !strings.Contains(err.Error(), "not an ancestor") {
		t.Errorf("error should explain the non-ancestor-reference cause, got: %v", err)
	}
}

// TestRemapClassifyOutsideRangeAndStale exercises the git-backed
// classification: a real commit outside the range is left untouched with no
// error; an unresolvable hash is left untouched and counted as stale.
func TestRemapClassifyOutsideRangeAndStale(t *testing.T) {
	dir, ctx := initTestRepo(t)
	writeFile(t, dir, "a.txt", "one\n")
	realCommit := commitAll(t, dir, ctx, "real commit outside range")

	rs := newRemapState(nil, nil) // empty range: nothing is in-range
	shaMap := map[string]string{}

	stale := strings.Repeat("deadbeef", 5)
	content := []byte("real " + realCommit + " stale " + stale + "\n")
	out, err := rs.remapContent(ctx, content, shaMap)
	if err != nil {
		t.Fatalf("remapContent: %v", err)
	}
	if string(out) != string(content) {
		t.Errorf("outside-range and stale candidates must be untouched, got %q", out)
	}
	if !rs.stale[stale] {
		t.Errorf("stale hash %s should be counted, stale set: %v", stale, rs.stale)
	}
	if rs.stale[realCommit] {
		t.Errorf("resolvable commit %s must not be counted as stale", realCommit)
	}
	if len(rs.stale) != 1 {
		t.Errorf("expected exactly 1 stale entry, got %d", len(rs.stale))
	}
}

// TestMatchAnyScope covers full-path and basename glob semantics shared with
// --scope.
func TestMatchAnyScope(t *testing.T) {
	tests := []struct {
		globs    []string
		path     string
		expected bool
	}{
		{[]string{"*.jsonl"}, ".rlsbl/changes/unreleased.jsonl", true},
		{[]string{".rlsbl/changes/*.jsonl"}, ".rlsbl/changes/unreleased.jsonl", true},
		{[]string{"*.jsonl", "*.md"}, "CHANGELOG.md", true},
		{[]string{"*.jsonl"}, "notes.txt", false},
		{nil, "anything", false},
	}
	for _, tt := range tests {
		if got := matchAnyScope(tt.globs, tt.path); got != tt.expected {
			t.Errorf("matchAnyScope(%v, %q) = %v, want %v", tt.globs, tt.path, got, tt.expected)
		}
	}
}
