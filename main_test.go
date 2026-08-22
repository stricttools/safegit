package main

import (
	"reflect"
	"testing"

	"github.com/smm-h/safegit/internal/commit"
)

// TestGlobalsToFlagsJSONDoesNotImplyApproval pins what the deliberate
// confirmations rely on: machine mode never stands in for consent. Only a real
// --approve-consequential sets it.
//
// It also pins that machine mode no longer forges --quiet. It used to, so that
// safegit's own stdout writes could not corrupt the JSON document it printed
// itself; the framework's envelope is structurally exempt from quiet and is
// written by the framework, so the only thing left to suppress is safegit's
// direct printing -- which silent() does WITHOUT claiming the operator passed
// --quiet.
func TestGlobalsToFlagsJSONDoesNotImplyApproval(t *testing.T) {
	tests := []struct {
		name                     string
		quiet, approved, jsonOut bool
		wantApproved             bool
		wantQuiet                bool
		wantSilent               bool
	}{
		{"neither", false, false, false, false, false, false},
		{"json only", false, false, true, false, false, true},
		{"approved only", false, true, false, true, false, false},
		{"both", false, true, true, true, false, true},
		{"quiet only", true, false, false, false, true, true},
		{"quiet and json", true, false, true, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := newGlobalFlags(reservedFlags{quiet: tt.quiet, approved: tt.approved}, "", tt.jsonOut)
			if gf.approved != tt.wantApproved {
				t.Errorf("approved = %v, want %v", gf.approved, tt.wantApproved)
			}
			if gf.quiet != tt.wantQuiet {
				t.Errorf("quiet = %v, want %v", gf.quiet, tt.wantQuiet)
			}
			if gf.silent() != tt.wantSilent {
				t.Errorf("silent() = %v, want %v", gf.silent(), tt.wantSilent)
			}
		})
	}
}

// TestParseHunkSelection replaces the old TestIsHunkSpec, which pinned the
// disk-probing grammar's first stage: a predicate over the tail of a positional
// argument, asking whether it "looked like" a hunk spec. Nothing asks that any
// more. A hunk selection is announced by the flag it arrives on, and the only
// question left is what one element of that flag means -- which is what this
// pins, including the property that makes a colon in a filename harmless: the
// split is on the LAST colon, so everything before it is the path, verbatim.
func TestParseHunkSelection(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantPath  string
		wantHunks []int
		wantErr   bool
	}{
		{name: "single hunk", input: "file.txt:1", wantPath: "file.txt", wantHunks: []int{1}},
		{name: "list", input: "file.txt:1,3,5", wantPath: "file.txt", wantHunks: []int{1, 3, 5}},
		{name: "range", input: "src/main.go:2-4", wantPath: "src/main.go", wantHunks: []int{2, 3, 4}},
		{name: "mixed list and range", input: "a.go:1-3,5-7", wantPath: "a.go", wantHunks: []int{1, 2, 3, 5, 6, 7}},
		{name: "path containing a colon splits on the last one", input: "sprint:1:2,3", wantPath: "sprint:1", wantHunks: []int{2, 3}},
		{name: "path that looks like a hunk spec is still a path", input: "1,2:3", wantPath: "1,2", wantHunks: []int{3}},
		{name: "no colon at all", input: "file.txt", wantErr: true},
		{name: "no path before the colon", input: ":1,3", wantErr: true},
		{name: "empty selection", input: "file.txt:", wantErr: true},
		{name: "non-numeric selection", input: "file.txt:abc", wantErr: true},
		{name: "decimal selection", input: "file.txt:1.2", wantErr: true},
		{name: "spaced selection", input: "file.txt:1 2", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseHunkSelection(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseHunkSelection(%q) = %+v, want an error", tt.input, got)
				}
				// The flag's ValidateFn is the same parser, so a refusal here
				// is a refusal at parse time rather than inside a handler.
				if validateHunkSelection(tt.input) == nil {
					t.Errorf("validateHunkSelection(%q) accepted what parseHunkSelection rejected", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHunkSelection(%q) failed: %v", tt.input, err)
			}
			if got.Path != tt.wantPath {
				t.Errorf("parseHunkSelection(%q).Path = %q, want %q", tt.input, got.Path, tt.wantPath)
			}
			if !reflect.DeepEqual(got.Hunks, tt.wantHunks) {
				t.Errorf("parseHunkSelection(%q).Hunks = %v, want %v", tt.input, got.Hunks, tt.wantHunks)
			}
			if err := validateHunkSelection(tt.input); err != nil {
				t.Errorf("validateHunkSelection(%q) rejected what parseHunkSelection accepted: %v", tt.input, err)
			}
		})
	}
}

// TestBuildFileSpecs replaces the old TestParseFileSpecs, whose whole subject
// was a positional argument being reinterpreted as path-plus-hunks. It cannot
// be: a positional path is literal, always, and the cases below pin that.
//
// Two contradiction cases that used to be refused here are now carried through
// unchanged, deliberately: whether two arguments name the same file is a
// question about canonical paths, not about the strings a caller typed, so it
// is decided in intake and only there. The pins for the refusal itself are the
// integration tests in internal/test/commit_hunks_conflict_test.go, which run
// the conflicting spellings against a real repository.
func TestBuildFileSpecs(t *testing.T) {
	tests := []struct {
		name    string
		files   []string
		hunks   []string
		want    []commit.FileSpec
		wantErr bool
	}{
		{
			name:  "plain file",
			files: []string{"file.txt"},
			want:  []commit.FileSpec{{Path: "file.txt", Hunks: nil}},
		},
		{
			name:  "a colon in a positional is part of the filename",
			files: []string{"file.txt:1,3"},
			want:  []commit.FileSpec{{Path: "file.txt:1,3", Hunks: nil}},
		},
		{
			name:  "hunk selection comes from the flag",
			hunks: []string{"file.txt:1,3"},
			want:  []commit.FileSpec{{Path: "file.txt", Hunks: []int{1, 3}}},
		},
		{
			name:  "positionals first, then the selections, in the order given",
			files: []string{"a.go"},
			hunks: []string{"b.go:1,2", "c.go:2-3"},
			want: []commit.FileSpec{
				{Path: "a.go", Hunks: nil},
				{Path: "b.go", Hunks: []int{1, 2}},
				{Path: "c.go", Hunks: []int{2, 3}},
			},
		},
		{
			name: "empty",
			want: []commit.FileSpec{},
		},
		{
			name:  "the same path whole and in hunks parses; intake decides the contradiction",
			files: []string{"a.go"},
			hunks: []string{"a.go:1"},
			want: []commit.FileSpec{
				{Path: "a.go", Hunks: nil},
				{Path: "a.go", Hunks: []int{1}},
			},
		},
		{
			name:  "one path twice in hunks parses; intake decides the contradiction",
			hunks: []string{"a.go:1", "a.go:3"},
			want: []commit.FileSpec{
				{Path: "a.go", Hunks: []int{1}},
				{Path: "a.go", Hunks: []int{3}},
			},
		},
		{
			name:    "a malformed element is refused here too",
			hunks:   []string{"a.go"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildFileSpecs(tt.files, tt.hunks)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("buildFileSpecs(%v, %v) = %+v, want an error", tt.files, tt.hunks, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("buildFileSpecs(%v, %v) failed: %v", tt.files, tt.hunks, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildFileSpecs(%v, %v) = %+v, want %+v", tt.files, tt.hunks, got, tt.want)
			}
		})
	}
}

func TestRefShortName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"refs/heads/main", "main"},
		{"refs/heads/feature/xyz", "feature/xyz"},
		{"main", "main"},
		{"refs/heads/", ""},
		{"refs/tags/v1.0", "refs/tags/v1.0"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := refShortName(tt.input)
			if got != tt.want {
				t.Errorf("refShortName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"multi-line", "one\ntwo", "one"},
		{"single line", "single", "single"},
		{"empty string", "", ""},
		{"trailing newline", "hello\n", "hello"},
		{"three lines", "first\nsecond\nthird", "first"},
		{"only newline", "\n", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstLine(tt.input)
			if got != tt.want {
				t.Errorf("firstLine(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
