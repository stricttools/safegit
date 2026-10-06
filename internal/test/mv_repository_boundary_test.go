package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/testutil"
)

// safegit mv refuses a pair with a side inside another git repository, with
// the fixtures of commit_repository_boundary_test.go. Moving into a submodule
// or a gitlink used to exit 0 and commit a tree with the gitlink replaced by a
// directory.

func TestMvAcrossARepositoryBoundaryIsRefused(t *testing.T) {
	for _, f := range boundaryFixtures {
		f := f
		pairs := []struct {
			name, pair, src, dst string
		}{
			{"out of", f.inner + "/n -> moved", f.inner + "/n", "moved"},
			{"into", "seed.txt -> " + f.inner + "/seed.txt", "seed.txt", f.inner + "/seed.txt"},
			{"within", f.inner + "/tracked -> " + f.inner + "/renamed", f.inner + "/tracked", f.inner + "/renamed"},
		}
		for _, p := range pairs {
			p := p
			t.Run(f.shape+"/"+p.name, func(t *testing.T) {
				dir := newBoundaryRepo(t, f)
				before := captureBoundaryState(t, dir, f.inner)
				_, stderr, code := runSafegit(t, dir, "mv", "-m", "x", p.pair)
				if code != exitcode.MoveNotBorneOut {
					t.Fatalf("exit %d, want %d (MoveNotBorneOut): %s", code, exitcode.MoveNotBorneOut, stderr)
				}
				if !strings.Contains(stderr, f.inner+" is "+f.describes) {
					t.Errorf("refusal does not name the boundary:\n%s", stderr)
				}
				if !mvExists(t, dir, p.src) || mvExists(t, dir, p.dst) {
					t.Errorf("something moved on disk: %s present=%v, %s present=%v",
						p.src, mvExists(t, dir, p.src), p.dst, mvExists(t, dir, p.dst))
				}
				before.assertUnchanged(t, dir, f.inner, "mv "+p.pair)
			})
		}
	}
}

// A move whose two sides lie in the same repository is one that repository
// can make, and the printed command makes it.
func TestMvWithinAnotherRepositoryFixInstructionWorks(t *testing.T) {
	for _, f := range boundaryFixtures {
		f := f
		t.Run(f.shape, func(t *testing.T) {
			dir := newBoundaryRepo(t, f)
			innerAbs := filepath.Join(dir, f.inner)
			testutil.WriteFile(t, dir, f.inner+"/tracked", "n0\n")
			_, stderr, code := runSafegit(t, dir, "mv", "-m", "x", f.inner+"/tracked -> "+f.inner+"/renamed")
			if code != exitcode.MoveNotBorneOut {
				t.Fatalf("exit %d, want %d: %s", code, exitcode.MoveNotBorneOut, stderr)
			}
			line := boundaryFixCommand(t, stderr, f.inner)
			out, code := runPrintedCommand(t, dir, line)
			if f.bumpsParent {
				if code == 0 || !strings.Contains(out, "commit.autoBumpParent not configured") {
					t.Fatalf("%s: exit %d, want the commit.autoBumpParent refusal:\n%s", line, code, out)
				}
				if _, stderr, code := runSafegit(t, dir, "config", "set", "commit.autoBumpParent", "true"); code != 0 {
					t.Fatalf("config set failed (%d): %s", code, stderr)
				}
				out, code = runPrintedCommand(t, dir, line)
			}
			if code != 0 {
				t.Fatalf("%s: exit %d:\n%s", line, code, out)
			}
			paths := testutil.TreePaths(t, innerAbs, "HEAD")
			if !testutil.Contains(paths, "renamed") || testutil.Contains(paths, "tracked") {
				t.Errorf("the inner repository did not record the move: %v", paths)
			}
		})
	}
}
