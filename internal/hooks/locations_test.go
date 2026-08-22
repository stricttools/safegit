package hooks

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeFileMode writes content at path (creating parents) with an explicit mode.
func writeFileMode(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// rels renders the enumeration as "<origin>:<rel>" strings, which is what the
// assertions below compare -- both halves of a location's identity in the order
// the enumerator produced them.
func rels(locs []Location) []string {
	out := make([]string, 0, len(locs))
	for _, l := range locs {
		out = append(out, string(l.Origin)+":"+l.Rel)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEnumerateSeesWhatDiscoverFilters is the enumerator's whole contract: it
// is a LOCATION authority with no filters, so every file Discover refuses to
// run -- non-executable, dot-prefixed, tilde-suffixed, nested arbitrarily deep
// -- is still enumerated. `hook list` and `scan` depend on exactly this.
func TestEnumerateSeesWhatDiscoverFilters(t *testing.T) {
	gitDir := setupGitDir(t)
	local := LocalDir(gitDir)

	writeFileMode(t, filepath.Join(local, "pre-pre-push"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(local, "pre-pre-push.d", "01-runs"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(local, "pre-pre-push.d", "02-not-executable"), "#!/bin/sh\nexit 0\n", 0o644)
	writeFileMode(t, filepath.Join(local, "pre-pre-push.d", "03-backup~"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(local, "pre-pre-push.d", ".hidden"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(local, "nested", "deep", "checker"), "#!/bin/sh\nexit 0\n", 0o755)

	locs, err := Enumerate(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"local:nested/deep/checker",
		"local:pre-pre-push",
		"local:pre-pre-push.d/.hidden",
		"local:pre-pre-push.d/01-runs",
		"local:pre-pre-push.d/02-not-executable",
		"local:pre-pre-push.d/03-backup~",
	}
	if got := rels(locs); !equalStrings(got, want) {
		t.Errorf("Enumerate() = %v, want %v", got, want)
	}

	// Discover applies the eligibility rules on top of that same set.
	discovered, err := Discover(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	wantRun := []string{
		filepath.Join(local, "nested", "deep", "checker"),
		filepath.Join(local, "pre-pre-push"),
		filepath.Join(local, "pre-pre-push.d", "01-runs"),
	}
	if !equalStrings(discovered, wantRun) {
		t.Errorf("Discover() = %v, want %v", discovered, wantRun)
	}
}

// TestEnumerateRecordsExecutability pins the field `hook list` prints and
// doctor's permission check reads.
func TestEnumerateRecordsExecutability(t *testing.T) {
	gitDir := setupGitDir(t)
	writeFileMode(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"), "#!/bin/sh\n", 0o644)

	locs, err := Enumerate(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 {
		t.Fatalf("expected 1 location, got %v", rels(locs))
	}
	if locs[0].Executable {
		t.Error("a 0644 hook was reported as executable")
	}
}

// TestEnumerateTrackedBeforeLocal pins store-major ordering, which is the
// execution order Discover inherits: a repository's committed checks run before
// whatever this checkout added locally.
func TestEnumerateTrackedBeforeLocal(t *testing.T) {
	gitDir := setupGitDir(t)
	worktree := filepath.Dir(gitDir)

	writeFileMode(t, filepath.Join(TrackedDir(worktree), "pre-pre-push"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(TrackedDir(worktree), "pre-pre-push.d", "10-shared"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"), "#!/bin/sh\nexit 0\n", 0o755)

	locs, err := Enumerate(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"tracked:pre-pre-push",
		"tracked:pre-pre-push.d/10-shared",
		"local:pre-pre-push",
	}
	if got := rels(locs); !equalStrings(got, want) {
		t.Errorf("Enumerate() = %v, want %v", got, want)
	}

	// The colliding name runs BOTH times, tracked first: two stores, two
	// hooks, no precedence rule that would silently drop one.
	discovered, err := Discover(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	want = []string{
		filepath.Join(TrackedDir(worktree), "pre-pre-push"),
		filepath.Join(TrackedDir(worktree), "pre-pre-push.d", "10-shared"),
		filepath.Join(LocalDir(gitDir), "pre-pre-push"),
	}
	if !equalStrings(discovered, want) {
		t.Errorf("Discover() = %v, want %v", discovered, want)
	}
}

// TestEnumerateBareRepoHasNoTrackedStore: a repository with no work tree has
// nowhere to commit a hook to, and asking for one must not walk the process's
// own directory.
func TestEnumerateBareRepoHasNoTrackedStore(t *testing.T) {
	gitDir := setupGitDir(t)
	writeFileMode(t, filepath.Join(LocalDir(gitDir), "pre-pre-push"), "#!/bin/sh\n", 0o755)

	locs, err := Enumerate(Store{GitDir: gitDir})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rels(locs), []string{"local:pre-pre-push"}; !equalStrings(got, want) {
		t.Errorf("Enumerate() = %v, want %v", got, want)
	}
}

// TestDiscoverRefusesLegacyLocation: after the store moved, a hook left in
// git's own .git/hooks is a refusal naming the migration command, not a silent
// skip and not a second store to run from.
func TestDiscoverRefusesLegacyLocation(t *testing.T) {
	gitDir := setupGitDir(t)
	writeFileMode(t, LegacyFile(gitDir), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(LegacyDir(gitDir), "20-old"), "#!/bin/sh\nexit 0\n", 0o755)

	locs, err := Enumerate(store(gitDir))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"legacy:pre-pre-push", "legacy:pre-pre-push.d/20-old"}
	if got := rels(locs); !equalStrings(got, want) {
		t.Errorf("Enumerate() = %v, want %v", got, want)
	}

	_, err = Discover(store(gitDir))
	var legacy *LegacyLocationError
	if !errors.As(err, &legacy) {
		t.Fatalf("Discover() error = %v, want a *LegacyLocationError", err)
	}
	if len(legacy.Paths) != 2 {
		t.Errorf("the refusal names %d paths, want 2: %v", len(legacy.Paths), legacy.Paths)
	}
}

// TestDiscoverRefusesNonExecutableTrackedHook: a committed hook is disabled by
// committing its deletion, so a lost mode is an accident to report rather than
// an instruction to obey.
func TestDiscoverRefusesNonExecutableTrackedHook(t *testing.T) {
	gitDir := setupGitDir(t)
	worktree := filepath.Dir(gitDir)
	writeFileMode(t, filepath.Join(TrackedDir(worktree), "pre-pre-push"), "#!/bin/sh\n", 0o644)

	_, err := Discover(store(gitDir))
	var tracked *TrackedNotExecutableError
	if !errors.As(err, &tracked) {
		t.Fatalf("Discover() error = %v, want a *TrackedNotExecutableError", err)
	}

	// The naming filter comes first: an editor backup in the committed store is
	// not a hook, so its mode says nothing.
	if err := os.Remove(filepath.Join(TrackedDir(worktree), "pre-pre-push")); err != nil {
		t.Fatal(err)
	}
	writeFileMode(t, filepath.Join(TrackedDir(worktree), "pre-pre-push~"), "#!/bin/sh\n", 0o644)
	if _, err := Discover(store(gitDir)); err != nil {
		t.Errorf("a tilde-suffixed committed file was treated as a hook: %v", err)
	}
}

// TestDiscoverMultiResolvesTrackedStoresAcrossTheCascade: the cascade carries
// work-tree/git-dir PAIRS, so each repository in it contributes its committed
// hooks as well as its local ones -- which a cascade keyed on git directories
// alone could never see.
func TestDiscoverMultiResolvesTrackedStoresAcrossTheCascade(t *testing.T) {
	parentGitDir := setupGitDir(t)
	parentWorktree := filepath.Dir(parentGitDir)
	childGitDir := setupGitDir(t)
	childWorktree := filepath.Dir(childGitDir)

	writeFileMode(t, filepath.Join(TrackedDir(parentWorktree), "pre-pre-push"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(LocalDir(parentGitDir), "pre-pre-push"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(TrackedDir(childWorktree), "pre-pre-push"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFileMode(t, filepath.Join(LocalDir(childGitDir), "pre-pre-push"), "#!/bin/sh\nexit 0\n", 0o755)

	got, err := DiscoverMulti([]Store{
		{Worktree: parentWorktree, GitDir: parentGitDir},
		{Worktree: childWorktree, GitDir: childGitDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(TrackedDir(parentWorktree), "pre-pre-push"),
		filepath.Join(LocalDir(parentGitDir), "pre-pre-push"),
		filepath.Join(TrackedDir(childWorktree), "pre-pre-push"),
		filepath.Join(LocalDir(childGitDir), "pre-pre-push"),
	}
	if !equalStrings(got, want) {
		t.Errorf("DiscoverMulti() = %v, want %v", got, want)
	}
}

// TestPlanInstallRefusesExistingDestination pins the no-clobber rule the deleted
// placeholder path used to carry, now the install path's own: upgrading a hook
// is a remove followed by an install.
func TestPlanInstallRefusesExistingDestination(t *testing.T) {
	gitDir := setupGitDir(t)
	src := filepath.Join(t.TempDir(), "pre-pre-push")
	writeFileMode(t, src, "#!/bin/sh\nexit 0\n", 0o755)

	_, dest, err := PlanInstall(gitDir, src)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(LocalDir(gitDir), "pre-pre-push"); dest != want {
		t.Errorf("PlanInstall dest = %s, want %s", dest, want)
	}

	writeFileMode(t, dest, "#!/bin/sh\nexit 1\n", 0o755)
	if _, _, err := PlanInstall(gitDir, src); err == nil {
		t.Error("PlanInstall overwrote an existing hook instead of refusing")
	}
}
