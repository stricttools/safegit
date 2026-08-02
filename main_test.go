package main

import (
	"reflect"
	"testing"

	"github.com/smm-h/safegit/internal/commit"
)

// TestGlobalsToFlagsJSONImpliesYesButNotExplicitYes pins the distinction the
// deliberate confirmations rely on: --json pre-approves ordinary prompts, but
// only a real --yes counts as explicit consent.
func TestGlobalsToFlagsJSONImpliesYesButNotExplicitYes(t *testing.T) {
	globals := func(yes, jsonOut bool) map[string]interface{} {
		return map[string]interface{}{
			"quiet":       false,
			"verbose":     false,
			"dry_run":     false,
			"yes":         yes,
			"config_file": "",
			"json":        jsonOut,
		}
	}

	tests := []struct {
		name            string
		yes, jsonOut    bool
		wantYes         bool
		wantYesExplicit bool
	}{
		{"neither", false, false, false, false},
		{"json only", false, true, true, false},
		{"yes only", true, false, true, true},
		{"both", true, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gf := globalsToFlags(globals(tt.yes, tt.jsonOut))
			if gf.yes != tt.wantYes {
				t.Errorf("yes = %v, want %v", gf.yes, tt.wantYes)
			}
			if gf.yesExplicit != tt.wantYesExplicit {
				t.Errorf("yesExplicit = %v, want %v", gf.yesExplicit, tt.wantYesExplicit)
			}
		})
	}
}

func TestIsHunkSpec(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"1,3,5", true},
		{"2-4", true},
		{"1", true},
		{"10,20,30", true},
		{"1-3,5-7", true},
		{"", false},
		{"abc", false},
		{"1.2", false},
		{"1,a", false},
		{"hello", false},
		{" ", false},
		{"1 2", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isHunkSpec(tt.input)
			if got != tt.want {
				t.Errorf("isHunkSpec(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseFileSpecs(t *testing.T) {
	// parseFileSpecs calls fileExists internally; for paths that don't exist
	// on disk it will parse hunk suffixes normally.
	flags := globalFlags{}
	cmd := "commit"

	tests := []struct {
		name  string
		files []string
		want  []commit.FileSpec
	}{
		{
			name:  "plain file",
			files: []string{"file.txt"},
			want:  []commit.FileSpec{{Path: "file.txt", Hunks: nil}},
		},
		{
			name:  "file with hunk spec",
			files: []string{"file.txt:1,3"},
			want:  []commit.FileSpec{{Path: "file.txt", Hunks: []int{1, 3}}},
		},
		{
			name:  "file with non-hunk suffix treated as path",
			files: []string{"file.txt:abc"},
			want:  []commit.FileSpec{{Path: "file.txt:abc", Hunks: nil}},
		},
		{
			name:  "file with range hunk spec",
			files: []string{"src/main.go:2-4"},
			want:  []commit.FileSpec{{Path: "src/main.go", Hunks: []int{2, 3, 4}}},
		},
		{
			name:  "multiple files mixed",
			files: []string{"a.go", "b.go:1,2"},
			want: []commit.FileSpec{
				{Path: "a.go", Hunks: nil},
				{Path: "b.go", Hunks: []int{1, 2}},
			},
		},
		{
			name:  "empty list",
			files: []string{},
			want:  []commit.FileSpec{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseFileSpecs(tt.files, flags, cmd)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseFileSpecs(%v) = %+v, want %+v", tt.files, got, tt.want)
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
