package commit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/testutil"
)

// The RefUpdate port is REQUIRED, not optional. A pipeline built without one
// refuses the operation rather than reaching around it and calling git itself --
// reaching around it is exactly how a preview would move a ref for real, so the
// refusal is what keeps the single mint site single.
//
// These pins also record WHERE the refusal happens, which is not at the top of
// Execute: the port is only consulted at Step 7, after Phase A has staged, run
// write-tree and built the commit object. So an EXECUTING run with no port
// leaves those objects in the repository's own store (nothing referenced them,
// so they are unreferenced loose objects awaiting the next prune), while a
// PREVIEW with no port leaves the store byte-identical because Phase A ran
// inside the object quarantine. Both refuse; they differ only in what Phase A
// left behind.

// countObjects returns the number of entries under .git/objects, directories
// included, which is the coarse "did this write objects" signal these pins need.
func countObjects(t *testing.T, repoDir string) int {
	t.Helper()
	root := filepath.Join(repoDir, ".git", "objects")
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return n
}

// assertNoRefUpdateRefusal checks the shape of the refusal itself: a
// *CommitError carrying exitcode.General, whose message says the pipeline was
// built without a RefUpdate and names the ref it therefore cannot move.
func assertNoRefUpdateRefusal(t *testing.T, err error, ref string) {
	t.Helper()
	if err == nil {
		t.Fatal("a pipeline with no RefUpdate accepted the operation; it must refuse")
	}
	var ce *CommitError
	if !errors.As(err, &ce) {
		t.Fatalf("refusal is %T, want a *CommitError: %v", err, err)
	}
	if ce.Code != exitcode.General {
		t.Errorf("refusal code = %d, want exitcode.General (%d)", ce.Code, exitcode.General)
	}
	if !strings.Contains(ce.Message, "built without a RefUpdate") {
		t.Errorf("refusal must say the pipeline was built without a RefUpdate, got: %q", ce.Message)
	}
	if !strings.Contains(ce.Message, ref) {
		t.Errorf("refusal must name the ref it cannot move (%s), got: %q", ref, ce.Message)
	}
}

func TestPipelineWithoutRefUpdateRefusesToCommit(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "orphan.txt"), []byte("orphan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	before, err := git.RevParse(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	objectsBefore := countObjects(t, dir)

	// No RefUpdate: the field is left at its zero value on purpose.
	p := &Pipeline{SafegitDir: sgDir, Config: repo.DefaultConfig()}
	result, err := p.Execute(ctx, CommitRequest{
		Message: "no port to move a ref with",
		Files:   []string{"orphan.txt"},
	})
	if result != nil {
		t.Errorf("refused commit still returned a result: %+v", result)
	}
	assertNoRefUpdateRefusal(t, err, "refs/heads/main")

	after, err := git.RevParse(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the ref moved despite the refusal: before=%s, after=%s", before, after)
	}

	// What IS, rather than what would be tidier: the refusal comes at the ref
	// update, so Phase A's objects were already written to the repository's own
	// store. If this ever fails because the count is unchanged, the port check
	// moved earlier -- update this pin to say so.
	if got := countObjects(t, dir); got <= objectsBefore {
		t.Errorf("object store entries = %d, want more than the %d before the refused run: "+
			"Phase A builds the blob, tree and commit before the RefUpdate port is consulted",
			got, objectsBefore)
	}
}

func TestPipelineWithoutRefUpdateRefusesAPreviewToo(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "orphan.txt"), []byte("orphan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	objectsBefore := countObjects(t, dir)

	p := &Pipeline{SafegitDir: sgDir, Config: repo.DefaultConfig()}
	_, err := p.Execute(ctx, CommitRequest{
		Message: "no port, and only a preview",
		Files:   []string{"orphan.txt"},
		DryRun:  true,
	})
	// A preview performs no ref update, but it still MINTS one, so the missing
	// port is a refusal here as well rather than a silently successful preview
	// of a commit the real run could not make.
	assertNoRefUpdateRefusal(t, err, "refs/heads/main")

	if got := countObjects(t, dir); got != objectsBefore {
		t.Errorf("object store entries = %d, want the %d it had before: a preview's Phase A "+
			"writes into the quarantine, so a refused preview leaves the store untouched",
			got, objectsBefore)
	}
}

func TestAmendWithoutRefUpdateRefuses(t *testing.T) {
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "extra.txt"), []byte("extra\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	before, err := git.RevParse(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}

	// Amend and reword share the same port, so they share the same refusal.
	p := &Pipeline{SafegitDir: sgDir, Config: repo.DefaultConfig()}
	_, amendErr := p.Amend(ctx, AmendRequest{
		Message:   "amended without a port",
		FileSpecs: []FileSpec{{Path: "extra.txt"}},
	})
	assertNoRefUpdateRefusal(t, amendErr, "refs/heads/main")

	_, rewordErr := p.Reword(ctx, RewordRequest{Message: "reworded without a port"})
	assertNoRefUpdateRefusal(t, rewordErr, "refs/heads/main")

	after, err := git.RevParse(ctx, "refs/heads/main")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the ref moved despite the refusals: before=%s, after=%s", before, after)
	}
}
