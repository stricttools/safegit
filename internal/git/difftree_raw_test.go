package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// DiffTree reports the RAW delta -- modes and blob names on both sides -- and
// not just the names that changed. Every field is pinned here, per status, in
// the two shapes a caller can get one: a diff of two real trees, and the
// synthesized listing a root commit produces.
//
// The blob names are what a move-record inference is built from: a deletion and
// an addition carrying one blob name is the raw material. A test that pinned
// only Status and Path would let an abbreviated or empty object name through
// and the inference would silently pair nothing.

func changedByPath(changed []ChangedPath) map[string]ChangedPath {
	byPath := make(map[string]ChangedPath, len(changed))
	for _, c := range changed {
		byPath[c.Path] = c
	}
	return byPath
}

func assertChanged(t *testing.T, got ChangedPath, want ChangedPath) {
	t.Helper()
	if got.Status != want.Status {
		t.Errorf("%s: Status = %q, want %q", want.Path, got.Status, want.Status)
	}
	if got.SrcMode != want.SrcMode {
		t.Errorf("%s: SrcMode = %q, want %q", want.Path, got.SrcMode, want.SrcMode)
	}
	if got.DstMode != want.DstMode {
		t.Errorf("%s: DstMode = %q, want %q", want.Path, got.DstMode, want.DstMode)
	}
	if got.SrcSHA != want.SrcSHA {
		t.Errorf("%s: SrcSHA = %q, want %q", want.Path, got.SrcSHA, want.SrcSHA)
	}
	if got.DstSHA != want.DstSHA {
		t.Errorf("%s: DstSHA = %q, want %q", want.Path, got.DstSHA, want.DstSHA)
	}
}

// trimNL strips the trailing newline git's plumbing writes, so an object name
// this test hands back to git is exact.
func trimNL(s string) string { return strings.TrimSpace(s) }

func TestDiffTreeRawFieldsPerStatus(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Tree 1: gone.txt (to be deleted), same.txt (to be exec-bit flipped),
	// edited.txt (content change).
	testutil.WriteFile(t, dir, "gone.txt", "gone\n")
	testutil.WriteFile(t, dir, "same.txt", "same\n")
	testutil.WriteFile(t, dir, "edited.txt", "one\n")
	testutil.Git(t, dir, "update-index", "--add", "gone.txt", "same.txt", "edited.txt")
	tree1 := testutil.GitOut(t, dir, "write-tree")
	tree1 = trimNL(tree1)

	goneSHA := trimNL(testutil.GitOut(t, dir, "rev-parse", tree1+":gone.txt"))
	sameSHA := trimNL(testutil.GitOut(t, dir, "rev-parse", tree1+":same.txt"))
	editedOld := trimNL(testutil.GitOut(t, dir, "rev-parse", tree1+":edited.txt"))

	// Tree 2: gone.txt deleted, added.txt carrying gone.txt's exact blob,
	// same.txt executable, edited.txt rewritten.
	testutil.Git(t, dir, "update-index", "--force-remove", "gone.txt")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, dir, "added.txt", "gone\n")
	testutil.WriteFile(t, dir, "edited.txt", "two\n")
	testutil.Git(t, dir, "update-index", "--add", "added.txt", "edited.txt")
	testutil.Git(t, dir, "update-index", "--add", "--chmod=+x", "same.txt")
	tree2 := trimNL(testutil.GitOut(t, dir, "write-tree"))

	editedNew := trimNL(testutil.GitOut(t, dir, "rev-parse", tree2+":edited.txt"))

	changed, err := DiffTree(ctx, tree1, tree2)
	if err != nil {
		t.Fatal(err)
	}
	byPath := changedByPath(changed)
	if len(byPath) != 4 {
		t.Fatalf("DiffTree reported %d paths (%v), want 4", len(byPath), byPath)
	}

	assertChanged(t, byPath["gone.txt"], ChangedPath{
		Status: "D", Path: "gone.txt",
		SrcMode: "100644", DstMode: ZeroMode,
		SrcSHA: goneSHA, DstSHA: ZeroSHA,
	})
	assertChanged(t, byPath["added.txt"], ChangedPath{
		Status: "A", Path: "added.txt",
		SrcMode: ZeroMode, DstMode: "100644",
		SrcSHA: ZeroSHA, DstSHA: goneSHA,
	})
	assertChanged(t, byPath["same.txt"], ChangedPath{
		Status: "M", Path: "same.txt",
		SrcMode: "100644", DstMode: "100755",
		SrcSHA: sameSHA, DstSHA: sameSHA,
	})
	assertChanged(t, byPath["edited.txt"], ChangedPath{
		Status: "M", Path: "edited.txt",
		SrcMode: "100644", DstMode: "100644",
		SrcSHA: editedOld, DstSHA: editedNew,
	})

	// The object names are full, never abbreviated: a shortened name is not
	// something two sides of a diff can be compared by.
	for path, c := range byPath {
		if c.SrcSHA != ZeroSHA && len(c.SrcSHA) != len(ZeroSHA) {
			t.Errorf("%s: SrcSHA %q is abbreviated", path, c.SrcSHA)
		}
		if c.DstSHA != ZeroSHA && len(c.DstSHA) != len(ZeroSHA) {
			t.Errorf("%s: DstSHA %q is abbreviated", path, c.DstSHA)
		}
	}
}

// A root commit has no tree to compare against, so DiffTree synthesizes the
// answer from a listing. The synthesized entries carry the same fields in the
// same spelling as git's own raw output, absent sides included.
func TestDiffTreeRawFieldsRootCommitSynthesis(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "plain.txt", "plain\n")
	testutil.WriteFile(t, dir, "runme.sh", "#!/bin/sh\n")
	testutil.Git(t, dir, "update-index", "--add", "plain.txt")
	testutil.Git(t, dir, "update-index", "--add", "--chmod=+x", "runme.sh")
	tree := trimNL(testutil.GitOut(t, dir, "write-tree"))

	plainSHA := trimNL(testutil.GitOut(t, dir, "rev-parse", tree+":plain.txt"))
	runmeSHA := trimNL(testutil.GitOut(t, dir, "rev-parse", tree+":runme.sh"))

	changed, err := DiffTree(ctx, "", tree)
	if err != nil {
		t.Fatal(err)
	}
	byPath := changedByPath(changed)
	if len(byPath) != 2 {
		t.Fatalf("DiffTree reported %d paths (%v), want 2", len(byPath), byPath)
	}
	assertChanged(t, byPath["plain.txt"], ChangedPath{
		Status: "A", Path: "plain.txt",
		SrcMode: ZeroMode, DstMode: "100644",
		SrcSHA: ZeroSHA, DstSHA: plainSHA,
	})
	assertChanged(t, byPath["runme.sh"], ChangedPath{
		Status: "A", Path: "runme.sh",
		SrcMode: ZeroMode, DstMode: "100755",
		SrcSHA: ZeroSHA, DstSHA: runmeSHA,
	})
}

// A path git has to C-quote in its non-z output still comes back as the name it
// really is, and its raw fields come back beside it.
func TestDiffTreeRawFieldsQuotedPath(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	const weird = "a b\tc\xc3\xa9.txt"
	testutil.WriteFile(t, dir, weird, "content\n")
	testutil.Git(t, dir, "update-index", "--add", "--", weird)
	tree := trimNL(testutil.GitOut(t, dir, "write-tree"))

	changed, err := DiffTree(ctx, "", tree)
	if err != nil {
		t.Fatal(err)
	}
	byPath := changedByPath(changed)
	c, ok := byPath[weird]
	if !ok {
		t.Fatalf("DiffTree did not report %q; it reported %v", weird, byPath)
	}
	if c.DstMode != "100644" || c.DstSHA == "" || c.DstSHA == ZeroSHA {
		t.Errorf("%q: DstMode = %q, DstSHA = %q", weird, c.DstMode, c.DstSHA)
	}
}
