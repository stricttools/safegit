package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// withGhVisibility replaces the gh visibility probe for the duration of a test,
// so the classification branches can be driven without gh or a network.
func withGhVisibility(t *testing.T, fn func(ctx context.Context, slug string) (string, error)) {
	t.Helper()
	prev := ghRepoVisibility
	ghRepoVisibility = fn
	t.Cleanup(func() { ghRepoVisibility = prev })
}

// captureConfirm runs fn with stdout and stderr redirected into one pipe and
// stdin pointed at the null device, so a confirmation prompt neither pollutes
// the test log nor waits on a terminal that is not there.
func captureConfirm(t *testing.T, fn func() bool) (bool, string) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}

	origOut, origErr, origIn := os.Stdout, os.Stderr, os.Stdin
	os.Stdout, os.Stderr, os.Stdin = w, w, devNull
	result := fn()
	os.Stdout, os.Stderr, os.Stdin = origOut, origErr, origIn

	w.Close()
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured output: %v", err)
	}
	r.Close()
	devNull.Close()

	return result, string(captured)
}

// testFlags builds globalFlags the way the CLI does, so the --json/--yes
// coupling under test is the real one.
func testFlags(yes, jsonOut bool) globalFlags {
	return newGlobalFlags(reservedFlags{yes: yes}, "", jsonOut)
}

func TestClassifyRemoteGithubVisibility(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		err      error
		want     remoteExposure
		wantSlug string
	}{
		{"public", "PUBLIC\n", nil, exposurePublic, "owner/repo"},
		{"public lowercase", "public", nil, exposurePublic, "owner/repo"},
		{"private", "PRIVATE\n", nil, exposurePrivate, "owner/repo"},
		{"internal", "INTERNAL\n", nil, exposurePrivate, "owner/repo"},
		{"probe fails", "", errors.New("gh: not authenticated"), exposureUnknown, "owner/repo"},
		{"unrecognized answer", "MYSTERY\n", nil, exposureUnknown, "owner/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotSlug string
			withGhVisibility(t, func(ctx context.Context, slug string) (string, error) {
				gotSlug = slug
				return tt.out, tt.err
			})
			if got := classifyRemote(context.Background(), "https://github.com/owner/repo.git"); got != tt.want {
				t.Errorf("classifyRemote = %v, want %v", got, tt.want)
			}
			if gotSlug != tt.wantSlug {
				t.Errorf("probed slug = %q, want %q", gotSlug, tt.wantSlug)
			}
		})
	}
}

func TestClassifyRemoteSkipsProbe(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want remoteExposure
	}{
		{"local path", "/srv/git/repo.git", exposureLocal},
		{"file url", "file:///srv/git/repo.git", exposureLocal},
		{"non-github host", "https://gitlab.com/owner/repo.git", exposureUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probed := false
			withGhVisibility(t, func(ctx context.Context, slug string) (string, error) {
				probed = true
				return "PUBLIC", nil
			})
			if got := classifyRemote(context.Background(), tt.url); got != tt.want {
				t.Errorf("classifyRemote(%q) = %v, want %v", tt.url, got, tt.want)
			}
			if probed {
				t.Errorf("classifyRemote(%q) ran the gh probe", tt.url)
			}
		})
	}
}

// TestConfirmExposurePublicRequiresDeliberateConsent covers the branch a public
// forge repository takes: the warning fires, an unanswerable prompt declines,
// --json refuses (it must never publish a branch on the operator's behalf), and
// an explicit --yes is the one thing that consents.
func TestConfirmExposurePublicRequiresDeliberateConsent(t *testing.T) {
	withGhVisibility(t, func(ctx context.Context, slug string) (string, error) {
		return "PUBLIC\n", nil
	})
	const url = "https://github.com/owner/repo.git"

	confirm := func(flags globalFlags) (bool, string) {
		return captureConfirm(t, func() bool {
			return confirmExposure(context.Background(), flags, "origin", url)
		})
	}

	ok, out := confirm(testFlags(false, false))
	if ok {
		t.Error("an unanswered prompt must decline the backup")
	}
	if !strings.Contains(out, "PUBLIC repository") {
		t.Errorf("expected the public-repository warning, got: %s", out)
	}

	ok, out = confirm(testFlags(false, true))
	if ok {
		t.Error("--json must not answer the exposure confirmation")
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("expected the refusal to name --yes as the consent flag, got: %s", out)
	}

	if ok, _ = confirm(testFlags(true, false)); !ok {
		t.Error("an explicit --yes must satisfy the exposure confirmation")
	}
	if ok, _ = confirm(testFlags(true, true)); !ok {
		t.Error("an explicit --yes must satisfy the confirmation even with --json")
	}
}

// TestConfirmExposurePrivateAsksNothing: a forge-confirmed private remote is the
// ordinary case and must not prompt.
func TestConfirmExposurePrivateAsksNothing(t *testing.T) {
	withGhVisibility(t, func(ctx context.Context, slug string) (string, error) {
		return "PRIVATE\n", nil
	})

	ok, out := captureConfirm(t, func() bool {
		return confirmExposure(context.Background(), testFlags(false, false), "origin", "https://github.com/owner/repo.git")
	})
	if !ok {
		t.Error("a private remote must be backed up without confirmation")
	}
	if out != "" {
		t.Errorf("a private remote must produce no warning or prompt, got: %s", out)
	}
}

func TestGithubSlug(t *testing.T) {
	tests := []struct {
		url      string
		wantSlug string
		wantOK   bool
	}{
		{"git@github.com:smm-h/safegit.git", "smm-h/safegit", true},
		{"git@github.com:smm-h/safegit", "smm-h/safegit", true},
		{"https://github.com/smm-h/safegit.git", "smm-h/safegit", true},
		{"https://github.com/smm-h/safegit", "smm-h/safegit", true},
		{"http://github.com/smm-h/safegit", "smm-h/safegit", true},
		{"ssh://git@github.com/smm-h/safegit.git", "smm-h/safegit", true},
		{"https://gitlab.com/smm-h/safegit.git", "", false},
		{"/srv/git/safegit.git", "", false},
		{"https://github.com/smm-h", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		slug, ok := githubSlug(tt.url)
		if ok != tt.wantOK || slug != tt.wantSlug {
			t.Errorf("githubSlug(%q) = (%q, %v), want (%q, %v)", tt.url, slug, ok, tt.wantSlug, tt.wantOK)
		}
	}
}

func TestIsNetworkRemote(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://github.com/smm-h/safegit.git", true},
		{"http://example.com/repo.git", true},
		{"ssh://git@example.com/repo.git", true},
		{"git://example.com/repo.git", true},
		{"git@example.com:owner/repo.git", true},
		{"/srv/git/safegit.git", false},
		{"../sibling-repo", false},
		{"file:///srv/git/safegit.git", false},
		{"C:/repos/safegit", false},
	}
	for _, tt := range tests {
		if got := isNetworkRemote(tt.url); got != tt.want {
			t.Errorf("isNetworkRemote(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestBackupRef(t *testing.T) {
	if got := backupRef("main"); got != "refs/backups/main" {
		t.Errorf("backupRef(main) = %q", got)
	}
	if got := backupRef("feature/x"); got != "refs/backups/feature/x" {
		t.Errorf("backupRef(feature/x) = %q", got)
	}
}
