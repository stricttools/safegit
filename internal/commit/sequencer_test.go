package commit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/coord"
	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/repo"
	"github.com/smm-h/safegit/internal/sequencer"
	"github.com/smm-h/safegit/internal/testutil"
)

// conflictedMergeRepo leaves a repository stopped in a conflicted merge, with
// the conflict resolved in the working tree, and chdirs into it. It returns the
// safegit dir and the tip commit the merge was started from.
func conflictedMergeRepo(t *testing.T) (sgDir, tip string) {
	t.Helper()
	dir, _, sgDir := testutil.InitRepo(t, repo.Init)
	testutil.Chdir(t, dir)

	write := func(content string) {
		if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil && args[0] != "merge" {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}

	write("base\n")
	run("add", "c.txt")
	run("commit", "-m", "base")
	run("checkout", "-q", "-b", "side")
	write("side\n")
	run("commit", "-q", "-am", "side")
	run("checkout", "-q", "main")
	write("main\n")
	run("commit", "-q", "-am", "main")
	if out := run("merge", "side"); !strings.Contains(out, "CONFLICT") {
		t.Fatalf("the fixture needs a merge conflict; git said:\n%s", out)
	}
	write("resolved\n")

	return sgDir, strings.TrimSpace(run("rev-parse", "HEAD"))
}

// Every entry point into the pipeline refuses while a merge is in flight, and
// the refusal carries the coordination-busy code so a caller can act on it
// without reading the message.
func TestPipelineRefusesEveryEntryPointMidMerge(t *testing.T) {
	sgDir, tip := conflictedMergeRepo(t)
	p := newPipeline(sgDir)
	ctx := context.Background()

	calls := map[string]func() error{
		"commit": func() error {
			_, err := p.Execute(ctx, CommitRequest{Message: "m", Files: []string{"c.txt"}})
			return err
		},
		"commit --allow-empty": func() error {
			_, err := p.Execute(ctx, CommitRequest{Message: "m", AllowEmpty: true})
			return err
		},
		"amend": func() error {
			_, err := p.Amend(ctx, AmendRequest{Message: "m", FileSpecs: []FileSpec{{Path: "c.txt"}}})
			return err
		},
		"reword": func() error {
			_, err := p.Reword(ctx, RewordRequest{Message: "m"})
			return err
		},
		"commit --dry-run": func() error {
			_, err := p.Execute(ctx, CommitRequest{Message: "m", Files: []string{"c.txt"}, DryRun: true})
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("the pipeline ran mid-merge instead of refusing")
			}
			var ce *CommitError
			if !errors.As(err, &ce) {
				t.Fatalf("refusal is untyped (%T: %v)", err, err)
			}
			if ce.Code != exitcode.CoordinationBusy {
				t.Errorf("refusal exits %d, want %d (CoordinationBusy)", ce.Code, exitcode.CoordinationBusy)
			}
			if !strings.Contains(err.Error(), "merge") {
				t.Errorf("refusal does not name the merge: %v", err)
			}
		})
	}

	commitLandsOnBranch(t, "refs/heads/main", tip)
}

// The declared context is the seam Phase 6's conclusion commands commit
// through: with it, the same request the pipeline just refused runs.
func TestPipelineHonorsADeclaredSequencerContext(t *testing.T) {
	sgDir, tip := conflictedMergeRepo(t)
	p := newPipeline(sgDir)
	ctx := context.Background()

	declared := &coord.SequencerContext{Kind: sequencer.KindMerge}
	// A conclusion declares BOTH halves: the context, and the shared-index base
	// that says the commit's content is what that index holds. The base is what
	// makes this request a conclusion rather than an ordinary commit made during
	// a merge, so the exemption requires it -- without it the fixture's unmerged
	// index refuses. It also seeds the temp index from a copy of the shared index
	// instead of from the parent tree, which switches observed-move inference off
	// for this request (inference keys on the parent-tree base); this test asserts
	// parents and branch only, so nothing here reads that difference.
	result, err := p.Execute(ctx, CommitRequest{
		Message:   "conclude",
		Files:     []string{"c.txt"},
		Sequencer: declared,
		IndexBase: IndexBaseSharedIndex,
	})
	if err != nil {
		t.Fatalf("a declared merge conclusion was refused: %v", err)
	}
	if firstParent(result.Parents) != tip {
		t.Errorf("the commit's parent is %s, want the pre-merge tip %s", firstParent(result.Parents), tip)
	}
	commitLandsOnBranch(t, "refs/heads/main", result.SHA)

	// The declaration is checked, not trusted: naming a different operation is
	// itself a refusal, on the amend path as much as the commit path.
	if _, err := p.Amend(ctx, AmendRequest{
		Message:   "wrong declaration",
		FileSpecs: []FileSpec{{Path: "c.txt"}},
		Sequencer: &coord.SequencerContext{Kind: sequencer.KindRevert},
	}); err == nil {
		t.Error("an amend declaring a revert conclusion ran during a merge")
	}
}

// The unmerged-index exemption needs BOTH declarations, and this pins the half
// that a single field used to be enough for: a request carrying the sequencer
// context while its commit is built on the PARENT TREE is not a conclusion, and
// the guard refuses it.
//
// The amend and reword call sites are exactly that shape -- they pass the
// parent-tree base hard-coded -- so a declared context can no longer carry them
// past an index git itself refuses to commit beside. Both are pinned because
// both call the guard, and a future edit that restores the either-one form
// would go unnoticed on whichever one was left unpinned.
func TestADeclaredContextAloneDoesNotExemptTheUnmergedGuard(t *testing.T) {
	ctx := context.Background()

	refusals := map[string]func(p *Pipeline, declared *coord.SequencerContext) error{
		"amend": func(p *Pipeline, declared *coord.SequencerContext) error {
			_, err := p.Amend(ctx, AmendRequest{
				Message:   "amend during a merge",
				FileSpecs: []FileSpec{{Path: "c.txt"}},
				Sequencer: declared,
			})
			return err
		},
		"reword": func(p *Pipeline, declared *coord.SequencerContext) error {
			_, err := p.Reword(ctx, RewordRequest{Message: "reword during a merge", Sequencer: declared})
			return err
		},
	}
	for name, call := range refusals {
		t.Run(name, func(t *testing.T) {
			// A fresh fixture per arm: under a regression the first arm would
			// commit, and the second would then be judging a repository the
			// first one moved.
			sgDir, tip := conflictedMergeRepo(t)
			p := newPipeline(sgDir)

			// The declaration matches the merge git has in flight, so the
			// in-flight guard passes it through and the unmerged-index guard is
			// the one answering.
			err := call(p, &coord.SequencerContext{Kind: sequencer.KindMerge})
			if err == nil {
				t.Fatal("a declared context alone exempted the unmerged-index guard")
			}
			var ce *CommitError
			if !errors.As(err, &ce) {
				t.Fatalf("refusal is untyped (%T: %v)", err, err)
			}
			if ce.Code != exitcode.UnmergedIndex {
				t.Fatalf("refusal exits %d, want %d (UnmergedIndex): %v", ce.Code, exitcode.UnmergedIndex, err)
			}
			if !strings.Contains(err.Error(), "unmerged entries") {
				t.Errorf("refusal does not name the unmerged entries: %v", err)
			}

			commitLandsOnBranch(t, "refs/heads/main", tip)
		})
	}
}
