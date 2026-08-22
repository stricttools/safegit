package git

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// RECORDED-FACT probes for the merge-tree argv the honest previews are built
// on. Nothing documents the mapping from a cherry-pick or a revert to a
// three-way merge, so it was derived by experiment and is kept as an assertion
// rather than as prose:
//
//	cherry-pick C    merge-tree --merge-base=C^  HEAD C     (apply C's change)
//	revert C         merge-tree --merge-base=C   HEAD C^    (undo C's change)
//
// The two are mirror images, and swapping them produces a plausible-looking
// answer that is the opposite of the truth -- which is exactly why the check
// below compares against the REAL operation's index stages, mode, object and
// stage number, rather than against a remembered rule.

// stageLines reads the unmerged slots of an index as comparable strings.
func stageLines(t *testing.T, ctx context.Context, indexPath string) []string {
	t.Helper()
	entries, err := UnmergedStages(ctx, indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range entries {
		lines = append(lines, e.Mode+" "+e.SHA+" "+string(rune('0'+e.Stage))+"\t"+e.Path)
	}
	sort.Strings(lines)
	return lines
}

// TestMergeTreeReplaysACherryPickExactly builds a conflicted cherry-pick for
// real and asserts merge-tree's computed conflict is the same conflict, slot
// for slot.
func TestMergeTreeReplaysACherryPickExactly(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")
	testutil.Git(t, dir, "branch", "side")
	testutil.WriteFile(t, dir, "f.txt", "MAIN\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "main")
	testutil.Git(t, dir, "switch", "-q", "side")
	testutil.WriteFile(t, dir, "f.txt", "SIDE\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "side")
	pick := testutil.Rev(t, dir, "HEAD")
	testutil.Git(t, dir, "switch", "-q", "main")
	head := testutil.Rev(t, dir, "HEAD")

	// The computed answer, before anything has happened to the repository.
	computed, err := MergeTree(ctx, pick+"^", head, pick)
	if err != nil {
		t.Fatalf("merge-tree replay of a cherry-pick: %v", err)
	}
	if !computed.Conflicted {
		t.Fatal("the replay reports a clean merge; the fixture conflicts")
	}

	// The real answer.
	if _, code := testutil.GitTry(t, dir, "cherry-pick", pick); code == 0 {
		t.Fatal("the fixture cherry-pick was expected to conflict")
	}
	// The paths the real operation left unmerged are the paths the replay
	// named, and the stage count matches: three slots for one content conflict.
	real := stageLines(t, ctx, "")
	if len(real) != 3 {
		t.Fatalf("the real cherry-pick left %d unmerged slots, want 3:\n%s", len(real), strings.Join(real, "\n"))
	}
	if len(computed.Paths) != 1 || computed.Paths[0] != "f.txt" {
		t.Errorf("the replay names %v, want [f.txt]", computed.Paths)
	}
	for _, line := range real {
		if !strings.HasSuffix(line, "\tf.txt") {
			t.Errorf("the real cherry-pick conflicted on a path the replay did not name: %q", line)
		}
	}
}

// TestMergeTreeReplaysARevertExactly is the mirror probe: a revert of C is C's
// PARENT merged onto HEAD with C as the base, and the conflict it computes is
// the conflict the real revert produces.
func TestMergeTreeReplaysARevertExactly(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "f.txt", "l1\nl2\nl3\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")
	testutil.WriteFile(t, dir, "f.txt", "CHANGED\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "the change")
	target := testutil.Rev(t, dir, "HEAD")
	testutil.WriteFile(t, dir, "f.txt", "LATER\nl2\nl3\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "a later edit")
	head := testutil.Rev(t, dir, "HEAD")

	computed, err := MergeTree(ctx, target, head, target+"^")
	if err != nil {
		t.Fatalf("merge-tree replay of a revert: %v", err)
	}
	if !computed.Conflicted {
		t.Fatal("the replay reports a clean merge; the fixture conflicts")
	}
	if len(computed.Paths) != 1 || computed.Paths[0] != "f.txt" {
		t.Errorf("the replay names %v, want [f.txt]", computed.Paths)
	}

	if _, code := testutil.GitTry(t, dir, "revert", "--no-edit", target); code == 0 {
		t.Fatal("the fixture revert was expected to conflict")
	}
	if got := stageLines(t, ctx, ""); len(got) != 3 {
		t.Errorf("the real revert left %d unmerged slots, want 3", len(got))
	}
}

// TestMergeTreeSwappedSidesAreNotTheSameAnswer proves the mapping above is not
// vacuous: computing a revert with the CHERRY-PICK sides produces a different
// answer, so a test that passed with the two swapped would be proving nothing.
func TestMergeTreeSwappedSidesAreNotTheSameAnswer(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "f.txt", "one\n")
	testutil.Git(t, dir, "add", "f.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")
	testutil.WriteFile(t, dir, "f.txt", "two\n")
	testutil.Git(t, dir, "commit", "-q", "-am", "the change")
	target := testutil.Rev(t, dir, "HEAD")
	head := target

	// Reverting the tip is clean: the result is the parent's content.
	asRevert, err := MergeTree(ctx, target, head, target+"^")
	if err != nil {
		t.Fatal(err)
	}
	// Replaying it as a cherry-pick of itself is a no-op: the tip's own tree.
	asPick, err := MergeTree(ctx, target+"^", head, target)
	if err != nil {
		t.Fatal(err)
	}
	if asRevert.Tree == asPick.Tree {
		t.Fatal("the two mappings produce the same tree, so nothing distinguishes them")
	}
	parentTree, err := RevParse(ctx, target+"^^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if asRevert.Tree != parentTree {
		t.Errorf("the revert replay produced %s, want the parent's tree %s", asRevert.Tree, parentTree)
	}
	headTree, err := RevParse(ctx, head+"^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	if asPick.Tree != headTree {
		t.Errorf("the cherry-pick replay produced %s, want the unchanged tree %s", asPick.Tree, headTree)
	}
}

// TestMergeTreeReportsACleanMerge pins the other verdict, including that a
// clean merge still yields a real tree object.
func TestMergeTreeReportsACleanMerge(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	testutil.Git(t, dir, "add", "a.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "base")
	testutil.Git(t, dir, "branch", "side")
	testutil.WriteFile(t, dir, "m.txt", "m\n")
	testutil.Git(t, dir, "add", "m.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "main")
	testutil.Git(t, dir, "switch", "-q", "side")
	testutil.WriteFile(t, dir, "s.txt", "s\n")
	testutil.Git(t, dir, "add", "s.txt")
	testutil.Git(t, dir, "commit", "-q", "-m", "side")
	testutil.Git(t, dir, "switch", "-q", "main")

	result, err := MergeTree(ctx, "", "main", "side")
	if err != nil {
		t.Fatal(err)
	}
	if result.Conflicted {
		t.Fatalf("a merge of disjoint additions reported a conflict on %v", result.Paths)
	}
	if len(result.Tree) != 40 && len(result.Tree) != 64 {
		t.Errorf("Tree = %q, want a full object name", result.Tree)
	}
	if len(result.Paths) != 0 {
		t.Errorf("a clean merge reported conflicted paths: %v", result.Paths)
	}
}
