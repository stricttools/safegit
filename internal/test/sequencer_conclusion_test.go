package test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// The three flat conclusion commands -- merge-continue, cherry-pick-continue
// and revert-continue -- and the engine they share.
//
// The merge route's own end-to-end assertions live in commit_merge_state_test.go
// (TestMergeCanBeConcludedThroughSafegit, which the route table now reaches).
// What is here is everything that file does not cover: the multi-parent case,
// author preservation on the pick/revert pair, the two completeness refusals,
// the file form of the declaration, the empty merge, the residue guarantee, and
// each refusal a conclusion can give against a state it cannot conclude.

// conclusionSession is the handshake these tests spawn safegit with, so undo is
// scoped to the operations they themselves performed.
var conclusionSession = []string{"CLAUDE_CODE_SESSION_ID=conclusion-test"}

// pickFixture is a repository parked in a conflicted SINGLE cherry-pick or
// revert: the operation git stopped before committing, plus the identity
// recorded on the commit it was applying.
type pickFixture struct {
	dir string
	// source is the commit the operation is applying, whose author a conclusion
	// must preserve.
	source string
	// author is the identity recorded on that commit.
	authorName  string
	authorEmail string
	// tip is the branch tip the conclusion commits onto.
	tip string
}

// newConflictedPickRepo builds a repository parked in a conflicted single
// cherry-pick ("cherry-pick") or revert ("revert").
//
// The commit the operation applies is authored by somebody OTHER than whoever
// runs the test, which is what makes author preservation observable at all: a
// conclusion that quietly re-authored the commit would be indistinguishable
// from one that preserved it if both identities were the same.
func newConflictedPickRepo(t *testing.T, verb string) pickFixture {
	t.Helper()
	dir := newRepo(t)

	const (
		otherName  = "Original Author"
		otherEmail = "original@example.com"
	)
	otherEnv := append([]string{
		"GIT_AUTHOR_NAME=" + otherName,
		"GIT_AUTHOR_EMAIL=" + otherEmail,
	}, conclusionSession...)

	testutil.WriteFile(t, dir, "c.txt", "l1\nbase\nl3\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "c.txt")

	var source string
	switch verb {
	case "cherry-pick":
		// The picked commit lives on a side branch that main never saw.
		testutil.Git(t, dir, "branch", "side")
		testutil.Git(t, dir, "switch", "side")
		testutil.WriteFile(t, dir, "c.txt", "l1\nside\nl3\n")
		source = safegitCommitEnv(t, dir, otherEnv, "the side change", "c.txt")
		testutil.Git(t, dir, "switch", "main")
		testutil.WriteFile(t, dir, "c.txt", "l1\nmain\nl3\n")
		safegitCommitEnv(t, dir, conclusionSession, "the main change", "c.txt")
	case "revert":
		// The reverted commit is on main, with a later edit over it so the
		// inverse patch cannot apply cleanly.
		testutil.WriteFile(t, dir, "c.txt", "l1\nthe change\nl3\n")
		source = safegitCommitEnv(t, dir, otherEnv, "the change to undo", "c.txt")
		testutil.WriteFile(t, dir, "c.txt", "l1\nlater\nl3\n")
		safegitCommitEnv(t, dir, conclusionSession, "a later edit", "c.txt")
	default:
		t.Fatalf("unknown verb %q", verb)
	}

	tip := testutil.Rev(t, dir, "HEAD")

	args := []string{verb}
	if verb == "revert" {
		args = append(args, "--no-edit")
	}
	args = append(args, source)
	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, args...)
	if code == 0 {
		t.Fatalf("safegit %s %s succeeded; the fixture needs a conflict\nstdout=%s stderr=%s", verb, source, stdout, stderr)
	}
	if !strings.Contains(stdout+stderr, "CONFLICT") {
		t.Fatalf("safegit %s did not report a conflict (code %d)\nstdout=%s stderr=%s", verb, code, stdout, stderr)
	}

	return pickFixture{dir: dir, source: source, authorName: otherName, authorEmail: otherEmail, tip: tip}
}

// stateFileNames are every file and directory a conclusion must leave behind
// none of. The set is git's own vocabulary, spelled out here rather than read
// from internal/sequencer so that the test states the guarantee independently
// of the code that implements it.
//
// The last two are not removed the same way, and the difference is the point:
// MERGE_RR is rerere's resolution index and is deleted with the rest, while
// MERGE_AUTOSTASH holds a stash-shaped commit and must be CONSUMED -- applied
// to the working tree, or stored as a real stash entry -- before it goes.
// Deleting that one unapplied is the data loss, so its absence here is only
// half the guarantee; the other half is asserted in sequencer_autostash_test.go,
// where the restored content itself is checked.
var stateFileNames = []string{
	"MERGE_HEAD", "MERGE_MODE", "MERGE_MSG",
	"CHERRY_PICK_HEAD", "REVERT_HEAD", "AUTO_MERGE", "sequencer",
	"MERGE_RR", "MERGE_AUTOSTASH",
}

// assertNoSequencerResidue fails when any of git's operation state survives.
func assertNoSequencerResidue(t *testing.T, dir, context string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	var survivors []string
	for _, name := range stateFileNames {
		if _, err := os.Lstat(filepath.Join(gitDir, name)); err == nil {
			survivors = append(survivors, name)
		}
	}
	// AUTO_MERGE is a ref as well as a file on some layouts; ask git too.
	if out, ok := testutil.GitTryOut(t, dir, "rev-parse", "--verify", "--quiet", "AUTO_MERGE"); ok && strings.TrimSpace(out) != "" {
		survivors = append(survivors, "AUTO_MERGE (as a ref)")
	}
	if len(survivors) > 0 {
		t.Errorf("%s: git operation state survived the conclusion: %s", context, strings.Join(survivors, ", "))
	}
}

// TestOctopusConclusionCarriesEveryMergeHead pins the multi-parent rule that is
// merge-continue's alone: the parents are HEAD followed by EVERY line of
// MERGE_HEAD, so an octopus merge concludes as an octopus rather than losing
// every side but the first.
//
// The fixture is a CLEAN octopus stopped with --no-commit, because git's
// octopus strategy refuses to park a conflicted state at all ("Should not be
// doing an octopus") -- it aborts instead, leaving no MERGE_HEAD. So the only
// octopus a conclusion can ever see is a clean one.
func TestOctopusConclusionCarriesEveryMergeHead(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "a.txt")

	var sides []string
	for _, name := range []string{"b1", "b2"} {
		testutil.Git(t, dir, "branch", name)
		testutil.Git(t, dir, "switch", name)
		testutil.WriteFile(t, dir, name+".txt", name+"\n")
		sides = append(sides, safegitCommitEnv(t, dir, conclusionSession, name, name+".txt"))
		testutil.Git(t, dir, "switch", "main")
	}
	testutil.WriteFile(t, dir, "m.txt", "main\n")
	mainSHA := safegitCommitEnv(t, dir, conclusionSession, "main", "m.txt")

	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge", "--no-commit", "b1", "b2"); code != 0 {
		t.Fatalf("octopus merge --no-commit failed (code %d): %s", code, stderr)
	}
	// AssertMergeHead compares the whole file, which for an octopus holds one
	// object name per side, so the sides are checked individually here.
	mergeHead, err := os.ReadFile(filepath.Join(dir, ".git", "MERGE_HEAD"))
	if err != nil {
		t.Fatalf("the fixture must be parked in an octopus merge: %v", err)
	}
	for _, side := range sides {
		if !strings.Contains(string(mergeHead), side) {
			t.Fatalf("MERGE_HEAD does not name %s:\n%s", side, mergeHead)
		}
	}

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge-continue")
	if code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s\n%s", code, stderr, stdout)
	}

	head := testutil.Git(t, dir, "rev-parse", "HEAD")
	parents := testutil.Parents(t, dir, head)
	want := append([]string{mainSHA}, sides...)
	if len(parents) != len(want) {
		t.Fatalf("octopus conclusion has %d parent(s), want %d: %v", len(parents), len(want), parents)
	}
	for i := range want {
		if parents[i] != want[i] {
			t.Errorf("parent %d = %s, want %s", i, parents[i], want[i])
		}
	}

	// Every side's file has to be in the tree: the conclusion commits the
	// merge's whole staged result, not a rebuild from HEAD.
	paths := testutil.TreePaths(t, dir, head)
	for _, want := range []string{"a.txt", "m.txt", "b1.txt", "b2.txt"} {
		if !testutil.Contains(paths, want) {
			t.Errorf("octopus conclusion dropped %s (tree: %v)", want, paths)
		}
	}
	assertNoSequencerResidue(t, dir, "octopus merge")
}

// TestCherryPickConclusionPreservesTheSourceAuthor: a conclusion records the
// identity of the commit being applied as the AUTHOR, and whoever ran the
// command as the committer -- which is what git's own cherry-pick does.
func TestCherryPickConclusionPreservesTheSourceAuthor(t *testing.T) {
	fx := newConflictedPickRepo(t, "cherry-pick")

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"cherry-pick-continue", "--resolve", "c.txt=theirs"); code != 0 {
		t.Fatalf("cherry-pick-continue failed (code %d): %s", code, stderr)
	}

	assertAuthorPreserved(t, fx)
	if blob := testutil.MustShow(t, fx.dir, "HEAD", "c.txt"); !strings.Contains(blob, "side") {
		t.Errorf("theirs on a cherry-pick must be the picked commit's content, got %q", blob)
	}
	assertNoSequencerResidue(t, fx.dir, "cherry-pick")
}

// TestRevertConclusionAuthorsAsTheOperator is the OPPOSITE guarantee on the
// revert side, and it is git's own revert semantics rather than a safegit
// choice: a cherry-pick applies somebody else's change, so their identity is
// preserved, while a revert is a NEW change of the reverter's own -- git
// authors it as whoever ran the command, and so does safegit's conclusion.
//
// Also asserted here: the stage semantics that make `theirs` mean the opposite
// of what the word suggests. A revert applies an INVERSE patch, so its stage 3
// is what the reverted commit's PARENT held.
func TestRevertConclusionAuthorsAsTheOperator(t *testing.T) {
	fx := newConflictedPickRepo(t, "revert")

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"revert-continue", "--resolve", "c.txt=theirs"); code != 0 {
		t.Fatalf("revert-continue failed (code %d): %s", code, stderr)
	}

	assertOperatorAuthored(t, fx)
	blob := testutil.MustShow(t, fx.dir, "HEAD", "c.txt")
	if !strings.Contains(blob, "base") {
		t.Errorf("theirs on a revert is the inverse patch's side -- what the reverted commit's parent held (%q); got %q", "base", blob)
	}
	if strings.Contains(blob, "the change") {
		t.Errorf("theirs on a revert must NOT be the reverted commit's own content: %q", blob)
	}
	assertNoSequencerResidue(t, fx.dir, "revert")
}

// assertAuthorPreserved checks the two identities on the concluding commit.
func assertAuthorPreserved(t *testing.T, fx pickFixture) {
	t.Helper()
	got := strings.TrimSpace(testutil.Git(t, fx.dir, "log", "-1", "--format=%an|%ae|%cn"))
	fields := strings.Split(got, "|")
	if len(fields) != 3 {
		t.Fatalf("unreadable identity line %q", got)
	}
	if fields[0] != fx.authorName || fields[1] != fx.authorEmail {
		t.Errorf("author = %s <%s>, want %s <%s>", fields[0], fields[1], fx.authorName, fx.authorEmail)
	}
	if fields[2] == fx.authorName {
		t.Errorf("committer = %s; the conclusion must record whoever RAN it as committer, not the source author", fields[2])
	}
}

// assertOperatorAuthored is the revert side's identity check: the conclusion
// records whoever ran it as BOTH author and committer, and the identity of the
// commit being reverted appears on neither.
func assertOperatorAuthored(t *testing.T, fx pickFixture) {
	t.Helper()
	got := strings.TrimSpace(testutil.Git(t, fx.dir, "log", "-1", "--format=%an|%ae|%cn|%ce"))
	fields := strings.Split(got, "|")
	if len(fields) != 4 {
		t.Fatalf("unreadable identity line %q", got)
	}
	if fields[0] == fx.authorName || fields[1] == fx.authorEmail {
		t.Errorf("author = %s <%s>; a revert is the reverter's own change and must not record the reverted commit's identity",
			fields[0], fields[1])
	}
	if fields[0] != fields[2] || fields[1] != fields[3] {
		t.Errorf("author %s <%s> and committer %s <%s> differ; a revert conclusion records the operator as both",
			fields[0], fields[1], fields[2], fields[3])
	}
}

// TestRevertConclusionPayloadReportsTheOperatorAsAuthor pins the machine-mode
// half of the same fact: the payload's `author` member is the identity the
// commit RECORDS, so for a revert it is the operator's -- a consumer reading it
// as "the reverted commit's author" would be reading a different fact.
func TestRevertConclusionPayloadReportsTheOperatorAsAuthor(t *testing.T) {
	fx := newConflictedPickRepo(t, "revert")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"--json", "revert-continue", "--resolve", "c.txt=theirs")
	if code != exitcode.OK {
		t.Fatalf("revert-continue under --json failed (code %d): %s", code, stderr)
	}

	var envelope struct {
		Payload struct {
			Author struct {
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"author"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("the envelope is not JSON (%v):\n%s", err, stdout)
	}

	recorded := strings.TrimSpace(testutil.Git(t, fx.dir, "log", "-1", "--format=%an|%ae"))
	if got := envelope.Payload.Author.Name + "|" + envelope.Payload.Author.Email; got != recorded {
		t.Errorf("payload author = %q, want the identity the commit records (%q)", got, recorded)
	}
	if envelope.Payload.Author.Email == fx.authorEmail {
		t.Errorf("payload author = %q, which is the REVERTED commit's identity", envelope.Payload.Author.Email)
	}
}

// TestConclusionRefusesAnUnresolvedPath: omitting a conflicted path is a hard
// error listing it, and the listing states per path what each keyword
// concretely resolves to. For a revert that is the whole mitigation for the
// `theirs` confusion, so the wording is asserted here rather than left to help
// text nobody reads at the moment of choosing.
func TestConclusionRefusesAnUnresolvedPath(t *testing.T) {
	fx := newConflictedPickRepo(t, "revert")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "revert-continue")
	if code != exitcode.ConclusionUnresolved {
		t.Fatalf("exit = %d, want %d (ConclusionUnresolved)\nstdout=%s stderr=%s", code, exitcode.ConclusionUnresolved, stdout, stderr)
	}
	if !strings.Contains(stderr, "c.txt") {
		t.Errorf("the refusal does not name the unresolved path:\n%s", stderr)
	}
	for _, want := range []string{"c.txt=ours", "c.txt=theirs", "c.txt=worktree", "c.txt=delete"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the listing does not offer --resolve '%s':\n%s", want, stderr)
		}
	}
	if !strings.Contains(stderr, "UNDOING") {
		t.Errorf("a revert's listing must say that theirs UNDOES the commit, at the moment of choosing:\n%s", stderr)
	}
	short := strings.TrimSpace(testutil.Git(t, fx.dir, "rev-parse", "--short", fx.source))
	if !strings.Contains(stderr, short) {
		t.Errorf("the listing does not name the commit being undone (%s):\n%s", short, stderr)
	}

	// Nothing moved and the operation is still in flight, so the same command
	// with the missing entry concludes it.
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
	}
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"revert-continue", "--resolve", "c.txt=theirs"); code != 0 {
		t.Fatalf("the refusal must be recoverable by naming the path (code %d): %s", code, stderr)
	}
}

// TestConclusionRefusesAPathThatIsNotConflicted is the other half of
// completeness: a resolution naming a path git did not leave unmerged is a hard
// error listing it, not a silent no-op.
func TestConclusionRefusesAPathThatIsNotConflicted(t *testing.T) {
	fx := newConflictedPickRepo(t, "cherry-pick")

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"cherry-pick-continue", "--resolve", "c.txt=theirs", "--resolve", "not-conflicted.txt=ours")
	if code != exitcode.ConclusionUnresolved {
		t.Fatalf("exit = %d, want %d (ConclusionUnresolved): %s", code, exitcode.ConclusionUnresolved, stderr)
	}
	if !strings.Contains(stderr, "not-conflicted.txt") {
		t.Errorf("the refusal does not name the stray path:\n%s", stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
	}
}

// TestConclusionReadsAResolveFile pins the file form, and that a path named by
// BOTH forms is a hard error rather than one form quietly winning.
func TestConclusionReadsAResolveFile(t *testing.T) {
	fx := newConflictedPickRepo(t, "cherry-pick")

	recipe := filepath.Join(fx.dir, "resolutions.toml")
	if err := os.WriteFile(recipe, []byte("[[resolutions]]\npath = \"c.txt\"\nchoice = \"theirs\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Named by both forms: a contradiction, refused before anything is written.
	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"cherry-pick-continue", "--resolve", "c.txt=ours", "--resolve-file", recipe)
	if code != exitcode.Usage {
		t.Fatalf("a path resolved twice must exit %d (Usage), got %d: %s", exitcode.Usage, code, stderr)
	}
	if !strings.Contains(stderr, "c.txt") || !strings.Contains(stderr, "twice") {
		t.Errorf("the refusal does not say which path was resolved twice:\n%s", stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
	}

	// The file form alone concludes the operation.
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"cherry-pick-continue", "--resolve-file", recipe); code != 0 {
		t.Fatalf("--resolve-file failed (code %d): %s", code, stderr)
	}
	if blob := testutil.MustShow(t, fx.dir, "HEAD", "c.txt"); !strings.Contains(blob, "side") {
		t.Errorf("the file's resolution was not applied: %q", blob)
	}
	assertNoSequencerResidue(t, fx.dir, "resolve-file")
}

// TestEmptyMergeIsConcludedWithoutAFlag: a merge whose result equals HEAD's own
// tree still records its parents, so the pipeline's tree-unchanged refusal does
// not apply to a merge conclusion and no flag is needed to say so.
func TestEmptyMergeIsConcludedWithoutAFlag(t *testing.T) {
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "a.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "a.txt")

	// Both branches make the IDENTICAL change independently, so the merge is
	// clean AND its result is byte-identical to what main already has. That is
	// the empty merge: two real parents, nothing to record between them.
	testutil.Git(t, dir, "branch", "side")
	testutil.Git(t, dir, "switch", "side")
	testutil.WriteFile(t, dir, "a.txt", "the same change\n")
	safegitCommitEnv(t, dir, conclusionSession, "side makes the change", "a.txt")
	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "a.txt", "the same change\n")
	safegitCommitEnv(t, dir, conclusionSession, "main makes the same change", "a.txt")
	tip := testutil.Rev(t, dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge", "--no-commit", "side"); code != 0 {
		t.Fatalf("merge --no-commit failed (code %d): %s", code, stderr)
	}

	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge-continue")
	if code != 0 {
		t.Fatalf("an empty merge must conclude without any flag (code %d): %s\n%s", code, stderr, stdout)
	}

	head := testutil.Git(t, dir, "rev-parse", "HEAD")
	if parents := testutil.Parents(t, dir, head); len(parents) != 2 {
		t.Errorf("the empty merge has %d parent(s), want 2: %v", len(parents), parents)
	}
	if newTree, oldTree := testutil.Git(t, dir, "rev-parse", "HEAD^{tree}"), testutil.Git(t, dir, "rev-parse", tip+"^{tree}"); newTree != oldTree {
		t.Errorf("the empty merge changed the tree: %s -> %s", oldTree, newTree)
	}
	assertNoSequencerResidue(t, dir, "empty merge")
}

// TestEmptyPickConclusionIsRefused is the counterpart to the empty MERGE: a
// merge commit records its parents whether or not the tree moved, but a
// cherry-pick or revert that produces nothing produced nothing, and there is no
// --allow-empty on these commands. The refusal must therefore name the ways out
// that exist rather than a flag the operator cannot pass.
func TestEmptyPickConclusionIsRefused(t *testing.T) {
	fx := newConflictedPickRepo(t, "cherry-pick")

	// ours is the branch's own content, so resolving the only conflicted path to
	// it leaves the tree exactly as it was.
	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "cherry-pick-continue", "--resolve", "c.txt=ours")
	if code == 0 {
		t.Fatalf("an empty cherry-pick conclusion must be refused")
	}
	if strings.Contains(stderr, "--allow-empty") {
		t.Errorf("the refusal points at a flag cherry-pick-continue does not have:\n%s", stderr)
	}
	if !strings.Contains(stderr, "--skip") || !strings.Contains(stderr, "--abort") {
		t.Errorf("the refusal does not name the ways out that do exist:\n%s", stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
	}
	// Still concludable the other way.
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "cherry-pick-continue", "--resolve", "c.txt=theirs"); code != 0 {
		t.Fatalf("the refusal must leave the operation concludable (code %d): %s", code, stderr)
	}
}

// TestConclusionLeavesNoSequencerResidue is the guarantee the whole subphase
// exists for: after ANY conclusion, none of git's operation state survives AND
// an ordinary `safegit commit` works again. A leftover state file is not
// cosmetic -- it makes every later commit refuse.
func TestConclusionLeavesNoSequencerResidue(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true, resolveInTree: true})
		if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
			"merge-continue", "--resolve", "conflicted.txt=worktree"); code != 0 {
			t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
		}
		assertConcludedRepoIsUsable(t, fx.dir, "merge")
	})

	for _, verb := range []string{"cherry-pick", "revert"} {
		t.Run(verb, func(t *testing.T) {
			fx := newConflictedPickRepo(t, verb)
			// theirs, not ours: resolving every path to what the branch already
			// has leaves the tree unchanged, which a cherry-pick or revert
			// conclusion refuses (see TestEmptyPickConclusionIsRefused).
			if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
				verb+"-continue", "--resolve", "c.txt=theirs"); code != 0 {
				t.Fatalf("%s-continue failed (code %d): %s", verb, code, stderr)
			}
			assertConcludedRepoIsUsable(t, fx.dir, verb)
		})
	}
}

// assertConcludedRepoIsUsable checks the residue guarantee and its consequence.
func assertConcludedRepoIsUsable(t *testing.T, dir, context string) {
	t.Helper()
	assertNoSequencerResidue(t, dir, context)

	testutil.WriteFile(t, dir, "after.txt", "written after the conclusion\n")
	stdout, stderr, code := runSafegitEnv(t, dir, conclusionSession, "commit", "-m", "after the conclusion", "--", "after.txt")
	if code != 0 {
		t.Fatalf("%s: a commit after the conclusion was refused (code %d): %s\n%s", context, code, stderr, stdout)
	}
	if paths := testutil.TreePaths(t, dir, "HEAD"); !testutil.Contains(paths, "after.txt") {
		t.Errorf("%s: the later commit does not contain after.txt (tree: %v)", context, paths)
	}
}

// TestNothingInProgressMentionsALoneAutoMerge: a LONE AUTO_MERGE is residue of
// an operation that already FINISHED -- git leaves one behind when a rebase
// concludes normally -- so it is never a refusal of its own. It is mentioned
// informationally in the nothing-in-progress refusal, because an operator who
// found the file in .git deserves to be told why it does not count.
func TestNothingInProgressMentionsALoneAutoMerge(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "a.txt", "one\n")
	safegitCommitEnv(t, dir, conclusionSession, "one", "a.txt")

	// Without it: the plain refusal, and no mention.
	_, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge-continue")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("exit = %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
	}
	if !strings.Contains(stderr, "no operation is in progress") {
		t.Errorf("the refusal does not say nothing is in progress:\n%s", stderr)
	}
	if strings.Contains(stderr, "AUTO_MERGE") {
		t.Errorf("there is no AUTO_MERGE, so the refusal must not mention one:\n%s", stderr)
	}

	// With a lone AUTO_MERGE: still a nothing-in-progress refusal, never an
	// AUTO_MERGE refusal, and the note explains it.
	testutil.Git(t, dir, "update-ref", "AUTO_MERGE", testutil.Rev(t, dir, "HEAD^{tree}"))
	_, stderr, code = runSafegitEnv(t, dir, conclusionSession, "merge-continue")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("a lone AUTO_MERGE must not change the verdict; exit = %d, want %d: %s", code, exitcode.CoordinationBusy, stderr)
	}
	if !strings.Contains(stderr, "no operation is in progress") {
		t.Errorf("a lone AUTO_MERGE must still read as nothing in progress:\n%s", stderr)
	}
	if !strings.Contains(stderr, "AUTO_MERGE") {
		t.Errorf("the refusal should mention the AUTO_MERGE it found, informationally:\n%s", stderr)
	}
}

// TestWrongContinueCommandNamesTheStateAndTheRightCommand: running one
// conclusion command against another operation's state is a hard error that
// names what IS in flight and the command that concludes it.
func TestWrongContinueCommandNamesTheStateAndTheRightCommand(t *testing.T) {
	fx := newConflictedPickRepo(t, "cherry-pick")

	_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "merge-continue", "--resolve", "c.txt=ours")
	if code != exitcode.CoordinationBusy {
		t.Fatalf("exit = %d, want %d (CoordinationBusy): %s", code, exitcode.CoordinationBusy, stderr)
	}
	if !strings.Contains(stderr, "cherry-pick") {
		t.Errorf("the refusal does not name the state actually in flight:\n%s", stderr)
	}
	if !strings.Contains(stderr, "safegit cherry-pick-continue") {
		t.Errorf("the refusal does not name the command that concludes it:\n%s", stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != fx.tip {
		t.Errorf("HEAD moved to %s despite the refusal (was %s)", head, fx.tip)
	}
}

// TestConclusionDetachedHeadGuidanceWorks refuses on a detached HEAD AND
// verifies the remedy it prints.
//
// The guidance is deliberately not `git switch -c`: git refuses that outright
// mid-operation ("cannot switch branch while merging"), which the test records
// as a fact rather than assuming. Creating the ref and re-pointing HEAD works,
// preserves every conflict stage, and the conclusion then succeeds.
func TestConclusionDetachedHeadGuidanceWorks(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "c.txt", "base\n")
	safegitCommitEnv(t, dir, conclusionSession, "base", "c.txt")
	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "c.txt", "feature\n")
	safegitCommitEnv(t, dir, conclusionSession, "feature", "c.txt")
	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "c.txt", "main\n")
	safegitCommitEnv(t, dir, conclusionSession, "main", "c.txt")

	testutil.Git(t, dir, "switch", "--detach", "HEAD")
	detached := testutil.Rev(t, dir, "HEAD")
	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge", "feature"); code == 0 {
		t.Fatalf("the fixture needs a conflict: %s", stderr)
	}

	_, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge-continue", "--resolve", "c.txt=ours")
	if code == 0 {
		t.Fatalf("merge-continue succeeded on a detached HEAD")
	}
	if !strings.Contains(stderr, "detached") {
		t.Errorf("the refusal does not say HEAD is detached:\n%s", stderr)
	}
	for _, want := range []string{"git branch <name>", "git symbolic-ref HEAD refs/heads/<name>"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not print the remedy %q:\n%s", want, stderr)
		}
	}

	// The recorded fact the guidance is built on.
	if out, gitCode := testutil.GitTry(t, dir, "switch", "-c", "rescue"); gitCode == 0 {
		t.Errorf("git switch -c now works mid-merge; the refusal's guidance can be simplified:\n%s", out)
	} else if !strings.Contains(out, "cannot switch branch while merging") {
		t.Errorf("git refused the switch for an unexpected reason: %s", oneLine(out))
	}

	// The remedy the refusal actually prints.
	testutil.Git(t, dir, "branch", "rescue")
	testutil.Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/rescue")
	testutil.AssertMergeHead(t, dir, testutil.Rev(t, dir, "feature"), "the remedy must preserve the merge state")

	if _, stderr, code := runSafegitEnv(t, dir, conclusionSession, "merge-continue", "--resolve", "c.txt=ours"); code != 0 {
		t.Fatalf("the printed remedy did not make the merge concludable (code %d): %s", code, stderr)
	}
	if parents := testutil.Parents(t, dir, testutil.Rev(t, dir, "HEAD")); len(parents) != 2 || parents[0] != detached {
		t.Errorf("the conclusion after the remedy has parents %v, want 2 with %s first", parents, detached)
	}
	assertNoSequencerResidue(t, dir, "detached-HEAD remedy")
}

// A QUEUED cherry-pick or revert is not concluded natively -- concluding one
// step and removing the state-file set would take the rest of the queue with it
// -- so it is DELEGATED to git's own --continue instead. Every assertion about
// that delegation lives in sequencer_delegation_test.go; this file covers the
// single-operation conclusions the pipeline writes itself.

// TestUndoOfAConclusionSaysTheStateIsNotRestored: undo reverses the ref move a
// conclusion made, and says plainly that it does not put the operation back in
// flight. Nothing in the oplog records what the removed state files held, so an
// operator told only "undid merge-continue" would go looking for a merge that
// is not there.
func TestUndoOfAConclusionSaysTheStateIsNotRestored(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true, resolveInTree: true})
	before := testutil.Rev(t, fx.dir, "HEAD")

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree"); code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}
	merged := testutil.Rev(t, fx.dir, "HEAD")
	if merged == before {
		t.Fatal("the conclusion did not move HEAD")
	}

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "undo")
	if code != 0 {
		t.Fatalf("undo of a conclusion failed (code %d): %s", code, stderr)
	}
	if head := testutil.Rev(t, fx.dir, "HEAD"); head != before {
		t.Errorf("after undo HEAD = %s, want the pre-conclusion tip %s", head, before)
	}
	combined := stdout + stderr
	if !strings.Contains(combined, "merge-continue") {
		t.Errorf("undo does not name what it reversed:\n%s", combined)
	}
	if !strings.Contains(combined, "NOT restored") {
		t.Errorf("undo must say git's operation state is not restored:\n%s", combined)
	}
	// And it really is not: the repository is idle, not mid-merge.
	assertNoSequencerResidue(t, fx.dir, "after undoing a conclusion")
}

// TestTwoSessionsRacingAConclusion: the worktree operation lock serializes two
// safegit processes concluding the same merge. Exactly one commits; the other
// waits for the lock and then refuses coherently, because by the time it looks
// there is no merge left to conclude. Neither produces a second merge commit.
func TestTwoSessionsRacingAConclusion(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true, resolveInTree: true})
	before := testutil.Rev(t, fx.dir, "HEAD")

	type outcome struct {
		code   int
		stderr string
	}
	results := make([]outcome, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
				"merge-continue", "--resolve", "conflicted.txt=worktree")
			results[i] = outcome{code: code, stderr: stderr}
		}(i)
	}
	wg.Wait()

	winners := 0
	for _, r := range results {
		if r.code == 0 {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("%d of 2 racing conclusions succeeded, want exactly 1:\n%s\n%s", winners, results[0].stderr, results[1].stderr)
	}

	for _, r := range results {
		if r.code == 0 {
			continue
		}
		// The loser's refusal has to be one safegit chose, not a crash or a
		// half-written commit: it finds nothing in flight, or the lock timed
		// out, and either way it says so.
		if r.code != exitcode.CoordinationBusy && r.code != exitcode.LockTimeout {
			t.Errorf("the losing conclusion exited %d; want %d (nothing in flight) or %d (lock timeout):\n%s",
				r.code, exitcode.CoordinationBusy, exitcode.LockTimeout, r.stderr)
		}
	}

	// One merge commit, on top of the pre-merge tip.
	head := testutil.Rev(t, fx.dir, "HEAD")
	parents := testutil.Parents(t, fx.dir, head)
	if len(parents) != 2 || parents[0] != before {
		t.Errorf("HEAD has parents %v, want exactly 2 with %s first", parents, before)
	}
	if count := strings.Count(testutil.Git(t, fx.dir, "log", "--format=%H", before+"..HEAD"), "\n"); count != 1 {
		t.Errorf("%d commits were created by the race, want 1", count)
	}
	assertNoSequencerResidue(t, fx.dir, "after a raced conclusion")
}

// A conclusion is a COMMIT as far as the repository is concerned, and nothing
// about it being the end of a merge exempts it from what an ordinary commit
// goes through: the caller's own --trailer values reach the message, safegit's
// session trailer is injected, and the repository's pre-commit and commit-msg
// hooks both run. Nothing else asserts any of this on the conclusion route --
// the hook tests all go through `safegit commit`, so a conclusion that quietly
// skipped the repository's policy would have gone unnoticed.
func TestMergeConclusionCarriesTrailersAndRunsNativeHooks(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true, resolveInTree: true})

	marker := filepath.Join(fx.dir, "conclusion-hooks.txt")
	installHook(t, fx.dir, "pre-commit", "#!/bin/sh\necho pre-commit >> \""+marker+"\"\n")
	installHook(t, fx.dir, "commit-msg", "#!/bin/sh\necho commit-msg >> \""+marker+"\"\n")

	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession, "merge-continue",
		"--trailer", "Reviewed-by: Alice",
		"--resolve", "conflicted.txt=worktree"); code != 0 {
		t.Fatalf("merge-continue failed (code %d): %s", code, stderr)
	}

	ran := hookMarkerLines(t, marker)
	for _, want := range []string{"pre-commit", "commit-msg"} {
		if !testutil.Contains(ran, want) {
			t.Errorf("%s did not run on the conclusion; hooks that ran: %v", want, ran)
		}
	}

	msg := commitMessageOf(t, fx.dir, "HEAD")
	if !strings.Contains(msg, "Reviewed-by: Alice") {
		t.Errorf("the caller's own trailer did not survive the conclusion:\n%s", msg)
	}
	if !strings.Contains(msg, "Claude-Code-Session-Id: conclusion-test") {
		t.Errorf("the conclusion carries no session trailer:\n%s", msg)
	}
	assertNoSequencerResidue(t, fx.dir, "merge conclusion with hooks")
}

// The other half: a commit-msg hook that refuses is the repository saying no,
// and a conclusion obeys it the way a commit does. Nothing is committed, and
// the merge is left exactly as it was -- still parked, still concludable --
// rather than half-concluded or cleared.
func TestMergeConclusionAbortsOnACommitMsgRejection(t *testing.T) {
	fx := newConflictedMergeRepo(t, conflictedMergeOpts{env: conclusionSession, cleanSideFile: true, resolveInTree: true})
	before := testutil.Rev(t, fx.dir, "HEAD")

	hookPath := filepath.Join(fx.dir, ".git", "hooks", "commit-msg")
	installHook(t, fx.dir, "commit-msg", "#!/bin/sh\necho 'commit-msg: nope' >&2\nexit 1\n")

	stdout, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree")
	if code != exitcode.CommitHookRejected {
		t.Fatalf("exit %d, want %d (a hook refusal)\nstdout=%s stderr=%s",
			code, exitcode.CommitHookRejected, stdout, stderr)
	}
	if !strings.Contains(stderr, "commit-msg") {
		t.Errorf("the refusal does not name the hook: %s", stderr)
	}

	if head := testutil.Rev(t, fx.dir, "HEAD"); head != before {
		t.Errorf("HEAD moved to %s despite the hook refusal (was %s)", head, before)
	}
	testutil.AssertMergeHead(t, fx.dir, fx.featureSHA, "the merge must survive a refused conclusion")

	// And the merge is still concludable once the hook stops refusing.
	if err := os.Remove(hookPath); err != nil {
		t.Fatalf("removing the hook: %v", err)
	}
	if _, stderr, code := runSafegitEnv(t, fx.dir, conclusionSession,
		"merge-continue", "--resolve", "conflicted.txt=worktree"); code != 0 {
		t.Fatalf("the refusal left the merge unconcludable (code %d): %s", code, stderr)
	}
	assertNoSequencerResidue(t, fx.dir, "after a refused then accepted conclusion")
}
