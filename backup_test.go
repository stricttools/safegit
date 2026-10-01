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

// captureConfirm runs fn through dispatch, with the reserved flags the CLI
// would set for approved and jsonOut. stdin is the null device, so a prompt
// declines instead of waiting on a terminal, and the prompt itself -- the one
// line safegit writes to stderr directly -- is captured too. The returned text
// is the prompt, then the run's stdout, then its stderr.
func captureConfirm(t *testing.T, approved, jsonOut bool, fn func(flags globalFlags) bool) (bool, string) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}

	var reserved []string
	if approved {
		reserved = append(reserved, "--approve-consequential")
	}
	if jsonOut {
		reserved = append(reserved, "--json")
	}

	var result bool
	origErr, origIn := os.Stderr, os.Stdin
	os.Stderr, os.Stdin = w, devNull
	res := dispatch(t, reserved, func(flags globalFlags) { result = fn(flags) })
	os.Stderr, os.Stdin = origErr, origIn

	w.Close()
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading captured output: %v", err)
	}
	r.Close()
	devNull.Close()

	return result, string(captured) + res.Stdout + res.Stderr
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
// --json refuses (it must never publish a branch on the operator's behalf), the
// blanket --approve-consequential does NOT answer this question, and the
// per-condition --allow-public-remote is the one thing that does.
func TestConfirmExposurePublicRequiresDeliberateConsent(t *testing.T) {
	withGhVisibility(t, func(ctx context.Context, slug string) (string, error) {
		return "PUBLIC\n", nil
	})
	const url = "https://github.com/owner/repo.git"

	confirm := func(approved, jsonOut, allowPublicRemote bool) (bool, string) {
		return captureConfirm(t, approved, jsonOut, func(flags globalFlags) bool {
			return confirmExposure(context.Background(), flags, "origin", url, allowPublicRemote)
		})
	}

	ok, out := confirm(false, false, false)
	if ok {
		t.Error("an unanswered prompt must decline the backup")
	}
	if !strings.Contains(out, "PUBLIC repository") {
		t.Errorf("expected the public-repository warning, got: %s", out)
	}

	ok, out = confirm(false, true, false)
	if ok {
		t.Error("--json must not answer the exposure confirmation")
	}
	if !strings.Contains(out, "--allow-public-remote") {
		t.Errorf("expected the refusal to name --allow-public-remote as the consent flag, got: %s", out)
	}

	// The decoupling: --approve-consequential says "yes, run this command",
	// which is not a statement about where the branch lands.
	if ok, out = confirm(true, true, false); ok {
		t.Error("--approve-consequential must not answer the exposure confirmation")
	}
	if !strings.Contains(out, "--allow-public-remote") {
		t.Errorf("the refusal must still name --allow-public-remote, got: %s", out)
	}

	if ok, _ = confirm(false, false, true); !ok {
		t.Error("--allow-public-remote must satisfy the exposure confirmation")
	}
	if ok, _ = confirm(false, true, true); !ok {
		t.Error("--allow-public-remote must satisfy the confirmation even with --json")
	}
}

// TestConfirmExposurePrivateAsksNothing: a forge-confirmed private remote is the
// ordinary case and must not prompt.
func TestConfirmExposurePrivateAsksNothing(t *testing.T) {
	withGhVisibility(t, func(ctx context.Context, slug string) (string, error) {
		return "PRIVATE\n", nil
	})

	ok, out := captureConfirm(t, false, false, func(flags globalFlags) bool {
		return confirmExposure(context.Background(), flags, "origin", "https://github.com/owner/repo.git", false)
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
		{"git@github.com:stricttools/safegit.git", "stricttools/safegit", true},
		{"git@github.com:stricttools/safegit", "stricttools/safegit", true},
		{"https://github.com/stricttools/safegit.git", "stricttools/safegit", true},
		{"https://github.com/stricttools/safegit", "stricttools/safegit", true},
		{"http://github.com/stricttools/safegit", "stricttools/safegit", true},
		{"ssh://git@github.com/stricttools/safegit.git", "stricttools/safegit", true},
		{"https://gitlab.com/stricttools/safegit.git", "", false},
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
		{"https://github.com/stricttools/safegit.git", true},
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
