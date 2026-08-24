package gitexec

import (
	"strings"
	"testing"
)

// The single-authorship boundary's own tests: the invariant is "git never
// authors a commit through safegit, except `safegit rebase`", and it is enforced
// at Validate, which every constructed argv passes. What is pinned here is the
// refusal, the one door that lifts it, and the suppressing tokens that make an
// argv harmless -- each keyed on an argv a production call site really builds,
// so a table row cannot be right in the abstract and wrong for safegit.

// TestValidateRefusesAnAuthoringArgvWithNoDoor is the refusal itself.
func TestValidateRefusesAnAuthoringArgvWithNoDoor(t *testing.T) {
	for _, argv := range [][]string{
		// git's own commit verb, which safegit's pipeline never uses.
		{"commit", "-m", "x"},
		{"commit", "--amend", "-m", "x"},
		// The three operations safegit computes with --no-commit and concludes
		// itself: without the suppressor, git would author the result.
		{"merge", "topic"},
		{"merge", "--no-ff", "topic"},
		{"cherry-pick", "abc1234"},
		{"revert", "abc1234"},
		// The forwarded --continue. It is git's own commit-making invocation for
		// all three, so it suppresses nothing and no production site may build
		// it: safegit's own conclusion commands do that work.
		{"merge", "--continue"},
		{"cherry-pick", "--continue"},
		{"revert", "--continue"},
		// A rebase from a site that did not declare the door.
		{"rebase", "main"},
	} {
		err := Validate(NoDoor, argv)
		if err == nil {
			t.Errorf("Validate admitted `git %s` with no declared door: git would author the commit",
				strings.Join(argv, " "))
			continue
		}
		if !strings.Contains(err.Error(), "AUTHOR") {
			t.Errorf("the refusal of `git %s` does not say what it is about: %v", strings.Join(argv, " "), err)
		}
	}
}

// TestValidateRefusesEveryAuthoringVerbBare is the other side of the exclusion
// TestValidateAcceptsDeclaredVerbs makes: every verb the table marks Authors is
// refused in its bare form, so a verb cannot be excluded from the vocabulary
// test without being covered here.
func TestValidateRefusesEveryAuthoringVerbBare(t *testing.T) {
	authoring := 0
	for _, v := range Verbs() {
		if !v.Authors {
			continue
		}
		authoring++
		if err := Validate(NoDoor, []string{v.Name}); err == nil {
			t.Errorf("Validate admitted a bare `git %s`, which the table says can author a commit", v.Name)
		}
	}
	if authoring == 0 {
		t.Error("no verb in the table is marked Authors: the boundary would refuse nothing at all")
	}
}

// TestSuppressingTokensAdmitTheArgvSafegitBuilds: the argv every restructured
// command really runs, admitted because the suppressor is on it. These are the
// compute steps -- git works the operation out and stages it, and safegit's
// pipeline authors the commit.
func TestSuppressingTokensAdmitTheArgvSafegitBuilds(t *testing.T) {
	for _, argv := range [][]string{
		// merge_cmd.go's compute step and its recorded dry-run argv.
		{"merge", "--no-ff", "--no-commit", "topic"},
		// cherry_pick_cmd.go's and revert_cmd.go's compute steps.
		{"cherry-pick", "--no-commit", "abc1234"},
		{"revert", "--no-commit", "abc1234"},
		// The state-control forms, which end an operation and author nothing.
		{"merge", "--abort"},
		{"merge", "--quit"},
		{"cherry-pick", "--abort"},
		{"cherry-pick", "--quit"},
		{"revert", "--abort"},
		{"revert", "--quit"},
		// The operator's own --no-commit, forwarded as a guarded passthrough.
		{"cherry-pick", "-n", "abc1234"},
		{"revert", "-n", "abc1234"},
		// backup.go's restore: a fast-forward moves a ref onto a commit that
		// already exists, and refuses outright where it cannot.
		{"merge", "--ff-only", "FETCH_HEAD"},
	} {
		if err := Validate(NoDoor, argv); err != nil {
			t.Errorf("Validate refused `git %s`, which cannot author a commit: %v",
				strings.Join(argv, " "), err)
		}
	}
}

// TestMergeDoesNotReadDashNAsNoCommit is the reason SuppressedBy is per verb
// rather than one shared list: `-n` is --no-commit on cherry-pick and revert,
// and --no-stat on merge. A shared list would read `git merge -n topic` as a
// merge that cannot commit, which is the opposite of what git does with it.
func TestMergeDoesNotReadDashNAsNoCommit(t *testing.T) {
	if err := Validate(NoDoor, []string{"merge", "-n", "topic"}); err == nil {
		t.Error("`git merge -n topic` was admitted: -n is --no-stat on merge, and this merge would still commit")
	}
	if err := Validate(NoDoor, []string{"cherry-pick", "-n", "abc1234"}); err != nil {
		t.Errorf("`git cherry-pick -n` was refused: -n IS --no-commit there: %v", err)
	}
}

// TestTheRebaseDoorAdmitsTheReplay: the one declared door, and every rebase
// spelling the passthrough forwards through it.
func TestTheRebaseDoorAdmitsTheReplay(t *testing.T) {
	for _, argv := range [][]string{
		{"rebase", "main"},
		{"rebase", "--onto", "main", "topic"},
		{"rebase", "-i", "main"},
		{"rebase", "--continue"},
		{"rebase", "--skip"},
		{"rebase", "--abort"},
	} {
		if err := Validate(DoorRebasePassthrough, argv); err != nil {
			t.Errorf("the rebase door refused `git %s`: %v", strings.Join(argv, " "), err)
		}
	}
}

// TestADoorOpensOneVerbAndNothingElse: a door is permission for one operation.
// Naming the rebase door on another verb is refused even where that verb's own
// shape authors nothing, because the site is claiming a permission it was not
// given.
func TestADoorOpensOneVerbAndNothingElse(t *testing.T) {
	for _, argv := range [][]string{
		{"commit", "-m", "x"},
		{"merge", "topic"},
		{"merge", "--abort"},
		{"status", "--porcelain"},
	} {
		err := Validate(DoorRebasePassthrough, argv)
		if err == nil {
			t.Errorf("the rebase door admitted `git %s`", strings.Join(argv, " "))
			continue
		}
		if !strings.Contains(err.Error(), "may not name a door") {
			t.Errorf("the refusal of `git %s` under the wrong door is the wrong one: %v",
				strings.Join(argv, " "), err)
		}
	}
}

// TestAnUndeclaredDoorIsRefused: the table is the only way a site can let git
// author, exactly as the directory-pin exemption table is the only way a site
// can escape the root pin.
func TestAnUndeclaredDoorIsRefused(t *testing.T) {
	err := Validate(DoorID("main.invented"), []string{"rebase", "main"})
	if err == nil {
		t.Fatal("Validate accepted an undeclared door")
	}
	if !strings.Contains(err.Error(), "authoring.go") {
		t.Errorf("the refusal must point at the door table; got %v", err)
	}
}

// TestVocabularyRefusalBeatsTheAuthoringRefusal: an undeclared subcommand is
// reported as one. Authoring's default-deny would otherwise answer first and
// send a reader to the door table for a verb that simply is not in the
// vocabulary.
func TestVocabularyRefusalBeatsTheAuthoringRefusal(t *testing.T) {
	err := Validate(NoDoor, []string{"filter-branch", "--all"})
	if err == nil {
		t.Fatal("Validate accepted an undeclared subcommand")
	}
	if !strings.Contains(err.Error(), "classification table") {
		t.Errorf("the refusal must be the vocabulary one; got %v", err)
	}
}

// TestAuthoringViewDefaultsToDeny: the same default WritesObjects and
// WritesWorktree take. An unknown invocation is never assumed harmless.
func TestAuthoringViewDefaultsToDeny(t *testing.T) {
	if !Authoring([]string{"filter-branch", "--all"}) {
		t.Error("an undeclared argv must be treated as one that can author a commit")
	}
	if !Authoring([]string{"--no-optional-locks"}) {
		t.Error("an argv naming no subcommand must be treated as one that can author a commit")
	}
	if Authoring([]string{"status", "--porcelain"}) {
		t.Error("`git status` cannot author a commit")
	}
}

// TestAuthoringDoorTableIsEnumerated is the enumerating test the door table
// exists for: the set of sites through which git may author a commit behind a
// safegit command name is CLOSED and visible. A new door must be added here
// deliberately, with the verbs it opens, rather than appearing in a diff nobody
// reads.
func TestAuthoringDoorTableIsEnumerated(t *testing.T) {
	want := map[DoorID][]string{
		DoorRebasePassthrough: {"rebase"},
	}

	got := AuthoringDoors()
	if len(got) != len(want) {
		t.Errorf("the door table has %d entries, this test enumerates %d", len(got), len(want))
	}
	seen := map[DoorID]bool{}
	for _, d := range got {
		seen[d.ID] = true
		verbs, ok := want[d.ID]
		if !ok {
			t.Errorf("undeclared door %q in the table; add it here with the verbs it opens and justify it", d.ID)
			continue
		}
		if strings.Join(d.Verbs, ",") != strings.Join(verbs, ",") {
			t.Errorf("door %q opens %v, this test expects %v", d.ID, d.Verbs, verbs)
		}
		if strings.TrimSpace(d.Reason) == "" {
			t.Errorf("door %q carries no reason", d.ID)
		}
		for _, v := range d.Verbs {
			if _, declared := Lookup(v); !declared {
				t.Errorf("door %q opens %q, which the classification table does not declare", d.ID, v)
			}
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("door %q is enumerated here but missing from the table", id)
		}
	}
}

// TestAuthoringDoorsIsACopy: the table is the boundary's, not a caller's.
func TestAuthoringDoorsIsACopy(t *testing.T) {
	got := AuthoringDoors()
	got[0].Reason = "tampered"
	if AuthoringDoors()[0].Reason == "tampered" {
		t.Error("AuthoringDoors handed out the package's own slice")
	}
}

// TestSuppressorsAreDeclaredOnAuthoringVerbsOnly keeps the two fields paired: a
// suppressing token on a verb that cannot author is a declaration with nothing
// to suppress, and reads as protection that is not there.
func TestSuppressorsAreDeclaredOnAuthoringVerbsOnly(t *testing.T) {
	for _, v := range Verbs() {
		if len(v.SuppressedBy) > 0 && !v.Authors {
			t.Errorf("verb %q declares suppressing tokens %v but is not marked Authors", v.Name, v.SuppressedBy)
		}
		if v.Authors && strings.TrimSpace(v.Note) == "" {
			t.Errorf("verb %q can author a commit and carries no note saying how safegit uses it", v.Name)
		}
	}
}
