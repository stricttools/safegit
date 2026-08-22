package gitexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envValue returns the LAST value assigned to name in a subprocess environment,
// which is the one the process sees: Go's exec dedups a duplicated name keeping
// the final occurrence.
func envValue(env []string, name string) (string, bool) {
	value, found := "", false
	for _, e := range env {
		if n, v, ok := strings.Cut(e, "="); ok && n == name {
			value, found = v, true
		}
	}
	return value, found
}

// altEntries splits a GIT_ALTERNATE_OBJECT_DIRECTORIES value.
func altEntries(value string) []string {
	return strings.Split(value, string(os.PathListSeparator))
}

func TestQuarantineRedirectsObjectWritesAndKeepsTheRepositoryReadable(t *testing.T) {
	ctx := WithObjectQuarantine(context.Background(), "/preview/objects", "/repo/.git/objects")
	cmd, err := Command(ctx, Spec{Args: []string{"write-tree"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if got, ok := envValue(cmd.Env, "GIT_OBJECT_DIRECTORY"); !ok || got != "/preview/objects" {
		t.Errorf("GIT_OBJECT_DIRECTORY = %q (present %v), want the quarantine", got, ok)
	}
	alt, ok := envValue(cmd.Env, "GIT_ALTERNATE_OBJECT_DIRECTORIES")
	if !ok {
		t.Fatal("a quarantined invocation must still be able to READ the repository's objects")
	}
	if entries := altEntries(alt); len(entries) != 1 || entries[0] != "/repo/.git/objects" {
		t.Errorf("alternates = %q, want just the repository's object store", alt)
	}
}

// The quarantine reaches the explicit-directory sites too -- the submodule scan
// and cat-file paths, which do not take the repository-root pin. Skipping them
// would leave a submodule's object store writable during a preview of the
// parent, which is exactly the escape the quarantine exists to close. The
// spec's OWN repository has to stay readable, so its object store joins the
// alternates.
func TestQuarantineAppliesToExplicitDirectorySites(t *testing.T) {
	ctx := WithObjectQuarantine(context.Background(), "/preview/objects", "/repo/.git/objects")
	cmd, err := Command(ctx, Spec{
		Args:   []string{"cat-file", "--batch-all-objects", "--batch"},
		Exempt: ExemptCatFileBatchAllWithDir,
		GitDir: "/repo/.git/modules/sub",
		Dir:    "/repo/.git/modules/sub",
	})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if got, _ := envValue(cmd.Env, "GIT_OBJECT_DIRECTORY"); got != "/preview/objects" {
		t.Errorf("GIT_OBJECT_DIRECTORY = %q, want the quarantine at an explicit-directory site too", got)
	}
	alt, _ := envValue(cmd.Env, "GIT_ALTERNATE_OBJECT_DIRECTORIES")
	want := "/repo/.git/modules/sub/objects"
	found := false
	for _, e := range altEntries(alt) {
		if e == want {
			found = true
		}
	}
	if !found {
		t.Errorf("alternates = %q, want it to include the spec's own object store %q", alt, want)
	}
}

// A blind append would clobber an inherited value, because Go's exec keeps the
// LAST occurrence of a name: the merged value has to carry both.
func TestQuarantineMergesInheritedAlternates(t *testing.T) {
	t.Setenv("GIT_ALTERNATE_OBJECT_DIRECTORIES", "/inherited/one"+string(os.PathListSeparator)+"/inherited/two")

	ctx := WithObjectQuarantine(context.Background(), "/preview/objects", "/repo/.git/objects")
	cmd, err := Command(ctx, Spec{Args: []string{"add", "--", "a.txt"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	alt, _ := envValue(cmd.Env, "GIT_ALTERNATE_OBJECT_DIRECTORIES")
	got := altEntries(alt)
	want := []string{"/inherited/one", "/inherited/two", "/repo/.git/objects"}
	if !equalStrings(got, want) {
		t.Errorf("alternates = %v, want %v (the inherited entries must survive)", got, want)
	}
}

// The enforcement: in a preview, an invocation the classification table says
// can write objects may not run without a quarantine. It is a refusal rather
// than a warning because the damage is invisible -- the objects land in the
// repository as unreferenced loose objects and nothing reports it.
func TestPreviewRefusesAnUnquarantinedObjectWrite(t *testing.T) {
	ctx := WithPreview(context.Background())
	for _, args := range [][]string{
		{"write-tree"},
		{"commit-tree", "abc123"},
		{"add", "--", "a.txt"},
		{"apply", "--cached", "patch"},
		{"hash-object", "-w", "a.txt"},
		{"mktree"},
	} {
		if _, err := Command(ctx, Spec{Args: args}); err == nil {
			t.Errorf("`git %s` ran in a preview with no quarantine installed", strings.Join(args, " "))
		} else if !strings.Contains(err.Error(), "object quarantine") {
			t.Errorf("the refusal for `git %s` must say what is missing, got: %v", strings.Join(args, " "), err)
		}
	}
}

// The same argv is fine once a quarantine is installed, and fine outside a
// preview: the refusal is scoped to the combination.
func TestObjectWritesAreFineWhenQuarantinedOrExecuting(t *testing.T) {
	if _, err := Command(WithObjectQuarantine(context.Background(), "/preview/objects", "/repo/.git/objects"),
		Spec{Args: []string{"write-tree"}}); err != nil {
		t.Errorf("a quarantined preview write must be allowed: %v", err)
	}
	if _, err := Command(context.Background(), Spec{Args: []string{"write-tree"}}); err != nil {
		t.Errorf("an executing run writes objects for real and must be allowed: %v", err)
	}
}

// A preview still READS: the refusal reads the table's object-writing view, not
// "everything in a preview".
func TestPreviewAllowsReadsWithoutAQuarantine(t *testing.T) {
	ctx := WithPreview(context.Background())
	for _, args := range [][]string{
		{"rev-parse", "HEAD"},
		{"ls-files"},
		{"cat-file", "-p", "HEAD"},
		{"hash-object", "a.txt"}, // no -w: computes a SHA, writes nothing
	} {
		if _, err := Command(ctx, Spec{Args: args}); err != nil {
			t.Errorf("`git %s` reads only and must run in a preview: %v", strings.Join(args, " "), err)
		}
	}
}

// An undeclared subcommand reports as object-writing (WritesObjects errs that
// way on purpose), so a preview refuses it rather than letting an unclassified
// invocation through. The vocabulary check refuses it first; either refusal is
// correct, and this pins that one of them fires.
func TestPreviewRefusesAnUndeclaredInvocation(t *testing.T) {
	if _, err := Command(WithPreview(context.Background()), Spec{Args: []string{"filter-branch"}}); err == nil {
		t.Error("an undeclared subcommand ran in a preview")
	}
}

// A quarantine cannot be smuggled in through Spec.Env: the boundary owns object
// -store targeting, and the presence of a quarantine is what the preview
// refusal reads.
func TestObjectDirectoryInSpecEnvIsRefused(t *testing.T) {
	_, err := Command(context.Background(), Spec{
		Args: []string{"write-tree"},
		Env:  []string{"GIT_OBJECT_DIRECTORY=" + filepath.Join(os.TempDir(), "smuggled")},
	})
	if err == nil {
		t.Fatal("Spec.Env set GIT_OBJECT_DIRECTORY and the boundary allowed it")
	}
	if !strings.Contains(err.Error(), "GIT_OBJECT_DIRECTORY") {
		t.Errorf("the refusal must name the variable, got: %v", err)
	}
}

func TestWithoutAQuarantineNothingIsAdded(t *testing.T) {
	cmd, err := Command(context.Background(), Spec{Args: []string{"status"}})
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	for _, name := range []string{"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES"} {
		if _, ok := envValue(cmd.Env, name); ok && cmd.Env != nil {
			// cmd.Env is nil for a spec with no overrides at all; when it is
			// set, neither variable may come from the boundary.
			if _, inherited := os.LookupEnv(name); !inherited {
				t.Errorf("%s was set on an unquarantined invocation", name)
			}
		}
	}
}
