package main

import "testing"

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
