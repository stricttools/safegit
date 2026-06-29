package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPushHintForDir_RlsblManaged(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".rlsbl"), 0o755); err != nil {
		t.Fatalf("creating .rlsbl dir: %v", err)
	}

	hint := pushHintForDir(dir)
	expected := "This repository is managed by a release tool. Complete the rewrite via your release tooling."
	if hint != expected {
		t.Errorf("expected rlsbl hint, got: %s", hint)
	}
}

func TestPushHintForDir_RlsblMonorepoManaged(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".rlsbl-monorepo"), 0o755); err != nil {
		t.Fatalf("creating .rlsbl-monorepo dir: %v", err)
	}

	hint := pushHintForDir(dir)
	expected := "This repository is managed by a release tool. Complete the rewrite via your release tooling."
	if hint != expected {
		t.Errorf("expected rlsbl hint, got: %s", hint)
	}
}

func TestPushHintForDir_NotManaged(t *testing.T) {
	dir := t.TempDir()

	hint := pushHintForDir(dir)
	expected := "To update the remote:\n  safegit push --both-branches-and-tags --force-with-lease"
	if hint != expected {
		t.Errorf("expected default push hint, got: %s", hint)
	}
}

func TestPushHintForDir_RlsblFileNotDir(t *testing.T) {
	// A regular file named .rlsbl should NOT trigger the rlsbl hint.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".rlsbl"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("creating .rlsbl file: %v", err)
	}

	hint := pushHintForDir(dir)
	expected := "To update the remote:\n  safegit push --both-branches-and-tags --force-with-lease"
	if hint != expected {
		t.Errorf("expected default push hint when .rlsbl is a file, got: %s", hint)
	}
}

func TestIsRlsblManaged(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(dir string) error
		expected bool
	}{
		{
			name:     "empty dir",
			setup:    func(dir string) error { return nil },
			expected: false,
		},
		{
			name: ".rlsbl dir",
			setup: func(dir string) error {
				return os.Mkdir(filepath.Join(dir, ".rlsbl"), 0o755)
			},
			expected: true,
		},
		{
			name: ".rlsbl-monorepo dir",
			setup: func(dir string) error {
				return os.Mkdir(filepath.Join(dir, ".rlsbl-monorepo"), 0o755)
			},
			expected: true,
		},
		{
			name: "both dirs",
			setup: func(dir string) error {
				if err := os.Mkdir(filepath.Join(dir, ".rlsbl"), 0o755); err != nil {
					return err
				}
				return os.Mkdir(filepath.Join(dir, ".rlsbl-monorepo"), 0o755)
			},
			expected: true,
		},
		{
			name: ".rlsbl is a regular file",
			setup: func(dir string) error {
				return os.WriteFile(filepath.Join(dir, ".rlsbl"), []byte("x"), 0o644)
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := tt.setup(dir); err != nil {
				t.Fatalf("setup: %v", err)
			}
			got := isRlsblManaged(dir)
			if got != tt.expected {
				t.Errorf("isRlsblManaged(%q) = %v, want %v", dir, got, tt.expected)
			}
		})
	}
}
