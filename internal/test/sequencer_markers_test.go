package test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// Marker verification: a conclusion may not commit the conflict it was asked to
// resolve.
//
// The check is layered and the tests here separate the layers:
//
//   - the REGION layer refuses a block git itself emitted, found again in the
//     content being committed;
//   - the STRUCTURAL layer refuses any complete conflict block that no side of
//     the conflict and no parent of the commit already carried, which is what
//     covers a hand-mangled marker and a path the operator staged themselves;
//   - the DIFFERENTIAL is what keeps a repository whose real content holds
//     marker-shaped lines committable.
//
// The one way past a rejection is the `safegit-conflict-markers` attribute,
// read from the first parent's tree, so an exemption necessarily predates the
// conflict it exempts.

// A complete conflict block used as ORDINARY FILE CONTENT by the tests below --
// a fixture, a piece of documentation about conflicts. Nothing about it is
// special except that it must survive a conclusion whenever a side of the
// conflict already carried it.
const parentalBlock = "<<<<<<< documented\nan example of a conflict\n=======\nas prose, not a conflict\n>>>>>>> documented\n"

// markerRepo is a repository parked in a conflicted merge whose file contents
// the test chose line by line.
type markerRepo struct {
	dir string
	// tip is the pre-merge tip of main: HEAD must still be there after any
	// refusal.
	tip string
}

// markerRepoOpts describes the three revisions of a conflicted merge.
type markerRepoOpts struct {
	// base is committed on main before the branches diverge.
	base map[string]string
	// ours and theirs are the two sides' rewrites, applied on main and on
	// feature respectively.
	ours   map[string]string
	theirs map[string]string
	// style, when set, configures merge.conflictStyle before the merge.
	style string
	// strategy, when set, is passed to `git merge -s`. The default (empty)
	// strategy is the one that records AUTO_MERGE.
	strategy string
}

// newMarkerRepo builds and parks the conflicted merge.
func newMarkerRepo(t *testing.T, opts markerRepoOpts) markerRepo {
	t.Helper()
	dir := newRepo(t)
	if opts.style != "" {
		testutil.Git(t, dir, "config", "merge.conflictStyle", opts.style)
	}

	writeAll := func(files map[string]string) []string {
		paths := make([]string, 0, len(files))
		for path, content := range files {
			testutil.WriteFile(t, dir, path, content)
			paths = append(paths, path)
		}
		sort.Strings(paths)
		return paths
	}

	safegitCommitEnv(t, dir, conclusionSession, "base", writeAll(opts.base)...)
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	safegitCommitEnv(t, dir, conclusionSession, "the feature side", writeAll(opts.theirs)...)
	testutil.Git(t, dir, "switch", "main")
	tip := safegitCommitEnv(t, dir, conclusionSession, "the main side", writeAll(opts.ours)...)

	args := []string{"merge"}
	if opts.strategy != "" {
		args = append(args, "-s", opts.strategy)
	}
	args = append(args, "feature")
	if out, code := testutil.GitTry(t, dir, args...); code == 0 {
		t.Fatalf("the fixture needs a conflict, but the merge succeeded:\n%s", out)
	}
	return markerRepo{dir: dir, tip: tip}
}

// assertRefusedNothingMoved checks that a refusal left the repository exactly
// as it was: the branch has not moved and the merge is still in flight, so the
// operator can fix the file and re-run.
func (r markerRepo) assertRefusedNothingMoved(t *testing.T) {
	t.Helper()
	if head := testutil.Rev(t, r.dir, "HEAD"); head != r.tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, r.tip)
	}
	if testutil.MergeStateGone(t, r.dir) {
		t.Error("the refusal destroyed the merge state it declined to conclude")
	}
}

// TestConclusionRefusesSurvivingMarkers is the headline: a `worktree`
// resolution of a file the operator never actually edited is refused, with the
// path and the line the block starts on.
func TestConclusionRefusesSurvivingMarkers(t *testing.T) {
	fx := newMarkerRepo(t, markerRepoOpts{
		base:   map[string]string{"f.txt": "line1\nbase\nline3\n"},
		ours:   map[string]string{"f.txt": "line1\nmain\nline3\n"},
		theirs: map[string]string{"f.txt": "line1\nfeature\nline3\n"},
	})

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "f.txt=worktree")
	if code != exitcode.ConclusionMarkerSurvived {
		t.Fatalf("exit = %d, want %d (ConclusionMarkerSurvived):\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
	}
	// git wrote the block starting at line 2 of this three-line file.
	if !strings.Contains(stderr, "f.txt:2") {
		t.Errorf("the refusal does not name the path and line:\n%s", stderr)
	}
	if !strings.Contains(stderr, "the conflict git wrote here") {
		t.Errorf("the refusal does not say the surviving block is git's own:\n%s", stderr)
	}
	if !strings.Contains(stderr, "-safegit-conflict-markers") {
		t.Errorf("the refusal does not print the declaration that would exempt the path:\n%s", stderr)
	}
	fx.assertRefusedNothingMoved(t)
	if onDisk := worktreeText(t, fx.dir, "f.txt"); !strings.Contains(onDisk, "<<<<<<<") {
		t.Errorf("the refusal rewrote the working-tree file:\n%s", onDisk)
	}

	// A preview gives the same verdict: it would be worse than useless for
	// --dry-run to report a conclusion that the real run refuses.
	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--dry-run", "--resolve", "f.txt=worktree")
	if code != exitcode.ConclusionMarkerSurvived {
		t.Errorf("the preview exited %d, want the same refusal %d:\nstdout=%s\nstderr=%s",
			code, exitcode.ConclusionMarkerSurvived, stdout, stderr)
	}

	// Still concludable, both ways: by taking a side...
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "f.txt=ours"); code != 0 {
		t.Fatalf("a stage resolution must not be refused by the marker check (code %d): %s", code, stderr)
	}
}

// TestConclusionAcceptsAResolvedFile is the same fixture actually resolved: the
// check is not a blanket ban on the merge, it is a ban on the conflict.
func TestConclusionAcceptsAResolvedFile(t *testing.T) {
	fx := newMarkerRepo(t, markerRepoOpts{
		base:   map[string]string{"f.txt": "line1\nbase\nline3\n"},
		ours:   map[string]string{"f.txt": "line1\nmain\nline3\n"},
		theirs: map[string]string{"f.txt": "line1\nfeature\nline3\n"},
	})
	testutil.WriteFile(t, fx.dir, "f.txt", "line1\nmain and feature\nline3\n")

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "f.txt=worktree"); code != 0 {
		t.Fatalf("a resolved file must conclude (code %d): %s", code, stderr)
	}
	if blob := testutil.MustShow(t, fx.dir, "HEAD", "f.txt"); blob != "line1\nmain and feature\nline3\n" {
		t.Errorf("committed content = %q", blob)
	}
}

// TestMarkerShapedContentAParentCarriedPasses is the differential, in both of
// its forms: a block the CONFLICTED file already had, and a block that arrives
// wholesale from the incoming side of the merge.
//
// Without it, a repository that documents conflict markers -- or carries a test
// fixture containing them -- could never conclude a merge again.
func TestMarkerShapedContentAParentCarriedPasses(t *testing.T) {
	fx := newMarkerRepo(t, markerRepoOpts{
		base: map[string]string{
			"f.txt": "intro\n" + parentalBlock + "tail: base\n",
		},
		ours: map[string]string{
			"f.txt": "intro\n" + parentalBlock + "tail: main\n",
		},
		theirs: map[string]string{
			"f.txt": "intro\n" + parentalBlock + "tail: feature\n",
			// A path only the incoming side has, whose content is marker-shaped.
			// It is staged cleanly by the merge and is measured against the
			// MERGE_HEAD parent, not against the first parent that never had it.
			"fixture.txt": "a stored fixture:\n" + parentalBlock,
		},
	})

	// The tail conflict is resolved; the documented block stays exactly as both
	// sides always had it.
	testutil.WriteFile(t, fx.dir, "f.txt", "intro\n"+parentalBlock+"tail: both\n")

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "f.txt=worktree"); code != 0 {
		t.Fatalf("marker-shaped content a parent already carried must not be refused (code %d): %s", code, stderr)
	}
	if blob := testutil.MustShow(t, fx.dir, "HEAD", "f.txt"); !strings.Contains(blob, parentalBlock) {
		t.Errorf("the committed file lost the documented block:\n%s", blob)
	}
	if blob := testutil.MustShow(t, fx.dir, "HEAD", "fixture.txt"); !strings.Contains(blob, parentalBlock) {
		t.Errorf("the incoming side's fixture did not survive the conclusion:\n%s", blob)
	}
}

// TestEmittedBlockIsRefusedEvenWhereMarkersAreOrdinary is the region layer
// earning its place: in a file that legitimately carries a marker block, the
// block GIT wrote is still refused, and the refusal points at git's block
// rather than at the documented one.
func TestEmittedBlockIsRefusedEvenWhereMarkersAreOrdinary(t *testing.T) {
	fx := newMarkerRepo(t, markerRepoOpts{
		base:   map[string]string{"f.txt": "intro\n" + parentalBlock + "tail: base\n"},
		ours:   map[string]string{"f.txt": "intro\n" + parentalBlock + "tail: main\n"},
		theirs: map[string]string{"f.txt": "intro\n" + parentalBlock + "tail: feature\n"},
	})

	// The file is left exactly as git wrote it: the documented block, then
	// git's own block over the conflicting tail.
	onDisk := worktreeText(t, fx.dir, "f.txt")
	emittedLine := 0
	for i, line := range strings.Split(onDisk, "\n") {
		if strings.HasPrefix(line, "<<<<<<< HEAD") {
			emittedLine = i + 1
		}
	}
	if emittedLine == 0 {
		t.Fatalf("the fixture must leave git's own block on disk:\n%s", onDisk)
	}

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "f.txt=worktree")
	if code != exitcode.ConclusionMarkerSurvived {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
	}
	if want := fmt.Sprintf("f.txt:%d", emittedLine); !strings.Contains(stderr, want) {
		t.Errorf("the refusal does not point at git's own block (%s):\n%s", want, stderr)
	}
	if strings.Contains(stderr, "f.txt:2") {
		t.Errorf("the refusal reported the documented block at line 2:\n%s", stderr)
	}
	fx.assertRefusedNothingMoved(t)
}

// TestMarkerExemptionMustPredateTheConflict pins both halves of the declared
// exemption: committed in the first parent it works, written into the working
// tree while the merge is in flight it does not.
func TestMarkerExemptionMustPredateTheConflict(t *testing.T) {
	const declaration = "f.txt -safegit-conflict-markers\n"

	t.Run("committed", func(t *testing.T) {
		fx := newMarkerRepo(t, markerRepoOpts{
			base: map[string]string{
				".gitattributes": declaration,
				"f.txt":          "line1\nbase\nline3\n",
			},
			ours:   map[string]string{"f.txt": "line1\nmain\nline3\n"},
			theirs: map[string]string{"f.txt": "line1\nfeature\nline3\n"},
		})

		if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree"); code != 0 {
			t.Fatalf("a committed exemption must let the conclusion through (code %d): %s", code, stderr)
		}
		// The exemption means exactly what it says: the markers are committed.
		if blob := testutil.MustShow(t, fx.dir, "HEAD", "f.txt"); !strings.Contains(blob, "<<<<<<<") {
			t.Errorf("the exempted path was rewritten rather than committed as-is:\n%s", blob)
		}
	})

	t.Run("uncommitted", func(t *testing.T) {
		fx := newMarkerRepo(t, markerRepoOpts{
			base:   map[string]string{"f.txt": "line1\nbase\nline3\n"},
			ours:   map[string]string{"f.txt": "line1\nmain\nline3\n"},
			theirs: map[string]string{"f.txt": "line1\nfeature\nline3\n"},
		})
		// Written mid-conflict, never committed: it says nothing about a
		// conflict that already exists.
		testutil.WriteFile(t, fx.dir, ".gitattributes", declaration)

		_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree")
		if code != exitcode.ConclusionMarkerSurvived {
			t.Fatalf("an uncommitted declaration must exempt nothing; exit = %d:\n%s", code, stderr)
		}
		if !strings.Contains(stderr, "COMMITTED") {
			t.Errorf("the refusal does not say the declaration has to be committed:\n%s", stderr)
		}
		fx.assertRefusedNothingMoved(t)
	})
}

// TestMarkerVerificationHonorsTheMarkerSizeAndStyle: the check reads the same
// per-path marker size and conflict style git wrote with, from the first
// parent's tree.
//
// Both directions are asserted, because only the pair is evidence: a
// twelve-character diff3 block git wrote is refused, and a DEFAULT-length block
// in the same file -- which is not a marker where the attributes say twelve --
// is not.
func TestMarkerVerificationHonorsTheMarkerSizeAndStyle(t *testing.T) {
	opts := markerRepoOpts{
		base: map[string]string{
			".gitattributes": "*.txt conflict-marker-size=12\n",
			"f.txt":          "line1\nbase\nline3\n",
		},
		ours:   map[string]string{"f.txt": "line1\nmain\nline3\n"},
		theirs: map[string]string{"f.txt": "line1\nfeature\nline3\n"},
		style:  "diff3",
	}

	t.Run("the twelve-character block git wrote is refused", func(t *testing.T) {
		fx := newMarkerRepo(t, opts)
		onDisk := worktreeText(t, fx.dir, "f.txt")
		if !strings.Contains(onDisk, "<<<<<<<<<<<<") || !strings.Contains(onDisk, "||||||||||||") {
			t.Fatalf("the fixture must produce twelve-character diff3 markers:\n%s", onDisk)
		}

		_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree")
		if code != exitcode.ConclusionMarkerSurvived {
			t.Fatalf("exit = %d, want %d:\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
		}
		fx.assertRefusedNothingMoved(t)
	})

	t.Run("a default-length block is not a marker at size twelve", func(t *testing.T) {
		fx := newMarkerRepo(t, opts)
		// Resolved -- and the resolution happens to contain a seven-character
		// block, which this path's attributes say is ordinary text.
		testutil.WriteFile(t, fx.dir, "f.txt", "line1\nresolved\n"+parentalBlock+"line3\n")

		if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree"); code != 0 {
			t.Fatalf("a block shorter than the declared marker size must not be a conflict (code %d): %s", code, stderr)
		}
	})
}

// TestDeleteModifyConclusionSkipsTheRegionLayer: a delete/modify conflict has
// stages but nothing marked up, so there is no emitted block to look for -- and
// the structural layer still refuses a block the operator introduced.
func TestDeleteModifyConclusionSkipsTheRegionLayer(t *testing.T) {
	newDeleteModify := func(t *testing.T) markerRepo {
		t.Helper()
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "f.txt", "base\n")
		safegitCommitEnv(t, dir, conclusionSession, "base", "f.txt")
		testutil.Git(t, dir, "branch", "feature")
		testutil.Git(t, dir, "switch", "feature")
		testutil.Git(t, dir, "rm", "-q", "f.txt")
		testutil.Git(t, dir, "commit", "-q", "-m", "feature deletes it")
		testutil.Git(t, dir, "switch", "main")
		testutil.WriteFile(t, dir, "f.txt", "main changed it\n")
		tip := safegitCommitEnv(t, dir, conclusionSession, "main modifies it", "f.txt")
		if out, code := testutil.GitTry(t, dir, "merge", "feature"); code == 0 {
			t.Fatalf("the fixture needs a delete/modify conflict:\n%s", out)
		}
		return markerRepo{dir: dir, tip: tip}
	}

	t.Run("concludes", func(t *testing.T) {
		fx := newDeleteModify(t)
		if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree"); code != 0 {
			t.Fatalf("a delete/modify conclusion must not be refused (code %d): %s", code, stderr)
		}
	})

	t.Run("garbage markers are still refused", func(t *testing.T) {
		fx := newDeleteModify(t)
		testutil.WriteFile(t, fx.dir, "f.txt", "main changed it\n"+parentalBlock)

		_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree")
		if code != exitcode.ConclusionMarkerSurvived {
			t.Fatalf("exit = %d, want %d:\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
		}
		if !strings.Contains(stderr, "no side of this conflict already carried") {
			t.Errorf("the refusal does not attribute the block to the structural layer:\n%s", stderr)
		}
		fx.assertRefusedNothingMoved(t)
	})
}

// TestAddAddConclusionVerifiesTheEmittedRegion records what an add/add conflict
// actually is: both sides present and no merge base, which git marks up exactly
// like any other content conflict and records in AUTO_MERGE. The region layer
// therefore DOES apply to it.
func TestAddAddConclusionVerifiesTheEmittedRegion(t *testing.T) {
	newAddAdd := func(t *testing.T) markerRepo {
		t.Helper()
		dir := newRepo(t)
		testutil.WriteFile(t, dir, "keep.txt", "base\n")
		safegitCommitEnv(t, dir, conclusionSession, "base", "keep.txt")
		testutil.Git(t, dir, "branch", "feature")
		testutil.Git(t, dir, "switch", "feature")
		testutil.WriteFile(t, dir, "new.txt", "the feature version\n")
		safegitCommitEnv(t, dir, conclusionSession, "feature adds it", "new.txt")
		testutil.Git(t, dir, "switch", "main")
		testutil.WriteFile(t, dir, "new.txt", "the main version\n")
		tip := safegitCommitEnv(t, dir, conclusionSession, "main adds it", "new.txt")
		if out, code := testutil.GitTry(t, dir, "merge", "feature"); code == 0 {
			t.Fatalf("the fixture needs an add/add conflict:\n%s", out)
		}
		return markerRepo{dir: dir, tip: tip}
	}

	t.Run("the emitted block is refused", func(t *testing.T) {
		fx := newAddAdd(t)
		_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "new.txt=worktree")
		if code != exitcode.ConclusionMarkerSurvived {
			t.Fatalf("exit = %d, want %d:\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
		}
		if !strings.Contains(stderr, "new.txt:1") {
			t.Errorf("the refusal does not name the path and line:\n%s", stderr)
		}
		fx.assertRefusedNothingMoved(t)
	})

	t.Run("a resolved add/add concludes", func(t *testing.T) {
		fx := newAddAdd(t)
		testutil.WriteFile(t, fx.dir, "new.txt", "both versions, merged by hand\n")
		if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "new.txt=worktree"); code != 0 {
			t.Fatalf("a resolved add/add must conclude (code %d): %s", code, stderr)
		}
	})
}

// TestConclusionChecksAPathTheOperatorStagedThemselves is the case the
// structural layer's whole "every staged path" scope exists for.
//
// An operator who follows git's own instructions -- edit the file, `git add` it
// -- leaves a path that is no longer unmerged: it has no stages, no --resolve
// entry names it, and the completeness check has nothing to say about it. Only
// a check over what the commit will RECORD reaches it.
func TestConclusionChecksAPathTheOperatorStagedThemselves(t *testing.T) {
	fx := newMarkerRepo(t, markerRepoOpts{
		base:   map[string]string{"f.txt": "line1\nbase\nline3\n"},
		ours:   map[string]string{"f.txt": "line1\nmain\nline3\n"},
		theirs: map[string]string{"f.txt": "line1\nfeature\nline3\n"},
	})

	// Staged with the markers still in it, which clears the index's unmerged
	// stages and leaves nothing for a resolution to name.
	testutil.Git(t, fx.dir, "add", "f.txt")

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "merge-continue")
	if code != exitcode.ConclusionMarkerSurvived {
		t.Fatalf("exit = %d, want %d (a staged path is still checked):\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
	}
	if !strings.Contains(stderr, "f.txt:2") {
		t.Errorf("the refusal does not name the path and line:\n%s", stderr)
	}
	fx.assertRefusedNothingMoved(t)

	// Resolved and re-staged, the same command concludes.
	testutil.WriteFile(t, fx.dir, "f.txt", "line1\nmain and feature\nline3\n")
	testutil.Git(t, fx.dir, "add", "f.txt")
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "merge-continue"); code != 0 {
		t.Fatalf("the resolved-and-staged path must conclude (code %d): %s", code, stderr)
	}
}

// TestReconstructionSuppliesTheEmittedBlocksWhenAutoMergeIsGone exercises the
// other source of git's emitted blocks, end to end and in production code.
//
// A cherry-pick labels its conflict markers from the commit being applied,
// which its state file names, so its conflicted file can be reproduced from the
// index stages with git's own merge engine. Removing the recorded AUTO_MERGE
// leaves reproduction as the only way to know what git wrote, and the refusal
// still says so exactly -- which it can only do if the reproduction is
// byte-identical to what the operator has on disk.
func TestReconstructionSuppliesTheEmittedBlocksWhenAutoMergeIsGone(t *testing.T) {
	fx := newConflictedPickRepo(t, "cherry-pick")
	if err := os.Remove(filepath.Join(fx.dir, ".git", "AUTO_MERGE")); err != nil {
		t.Fatalf("removing the recorded conflicted file: %v", err)
	}

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"cherry-pick-continue", "--resolve", "c.txt=worktree")
	if code != exitcode.ConclusionMarkerSurvived {
		t.Fatalf("exit = %d, want %d:\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
	}
	if !strings.Contains(stderr, "the conflict git wrote here") {
		t.Errorf("the refusal gave the merely structural sentence, so the reproduction did not match git's own block:\n%s", stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
	}
}

// TestVerificationHoldsWithoutAnAutoMergeToReadFrom: a merge run with a
// non-default strategy records NO AUTO_MERGE, and a merge's marker labels are
// the name the operator typed, which git stores nowhere -- so safegit can
// neither read nor reproduce the file git wrote for it.
//
// The verdict is unaffected, which is the property this test exists to pin: a
// complete block no side of the conflict carried is refused whether or not
// safegit can name who wrote it, and a resolved file concludes normally.
func TestVerificationHoldsWithoutAnAutoMergeToReadFrom(t *testing.T) {
	opts := markerRepoOpts{
		base:     map[string]string{"f.txt": "line1\nbase\nline3\n"},
		ours:     map[string]string{"f.txt": "line1\nmain\nline3\n"},
		theirs:   map[string]string{"f.txt": "line1\nfeature\nline3\n"},
		strategy: "resolve",
	}

	t.Run("the surviving block is still refused", func(t *testing.T) {
		fx := newMarkerRepo(t, opts)
		if _, ok := testutil.GitTryOut(t, fx.dir, "rev-parse", "--verify", "--quiet", "AUTO_MERGE"); ok {
			t.Skip("this git records an AUTO_MERGE for the resolve strategy, so the fixture no longer produces the state under test")
		}

		_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree")
		if code != exitcode.ConclusionMarkerSurvived {
			t.Fatalf("exit = %d, want %d:\n%s", code, exitcode.ConclusionMarkerSurvived, stderr)
		}
		if !strings.Contains(stderr, "f.txt:2") {
			t.Errorf("the refusal does not name the path and line:\n%s", stderr)
		}
		// The message is the structural one: with nothing recorded to compare
		// against, safegit says what it knows and not more.
		if !strings.Contains(stderr, "no side of this conflict already carried") {
			t.Errorf("the refusal claims knowledge it cannot have here:\n%s", stderr)
		}
		fx.assertRefusedNothingMoved(t)
	})

	t.Run("a resolved file still concludes", func(t *testing.T) {
		fx := newMarkerRepo(t, opts)
		testutil.WriteFile(t, fx.dir, "f.txt", "line1\nmain and feature\nline3\n")

		if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "f.txt=worktree"); code != 0 {
			t.Fatalf("a resolved file must conclude even with no AUTO_MERGE (code %d): %s", code, stderr)
		}
		if blob := testutil.MustShow(t, fx.dir, "HEAD", "f.txt"); strings.Contains(blob, "<<<<<<<") {
			t.Errorf("the concluded commit carries conflict markers:\n%s", blob)
		}
	})
}
