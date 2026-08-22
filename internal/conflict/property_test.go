package conflict_test

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/testutil"
)

// The fidelity property the marker verification's hard refusal rests on:
// given the index stages and the attributes that were in force, git's own
// merge-file reproduces the conflict-marked file git wrote, BYTE FOR BYTE,
// across every combination of conflict style and marker size, over conflicts
// whose shape the test did not choose by hand.
//
// Why it has to be a property rather than the handful of fixed cases in
// conflict_test.go: the verification refuses a conclusion outright when it
// cannot obtain the emitted regions, and it treats a reproduced region as
// authoritative evidence about the operator's file. Both are only honest if
// reproduction is exact for conflicts nobody tuned it against.
//
// THE LABELS. Marker lines carry names, so byte identity depends on getting
// them right, and git records them differently per operation:
//
//   - A CHERRY-PICK and a REVERT derive their labels from the commit being
//     applied, which is recorded in the operation's own state. The test asks
//     conflict.PickLabels / conflict.RevertLabels -- the same call the
//     conclusion engine makes -- so those rows test the production path end to
//     end.
//   - A MERGE's `theirs` label is the name the operator typed on the command
//     line, and git records it NOWHERE machine-readable. The test SUPPLIES it,
//     because it is the side that typed it. Production cannot, which is exactly
//     why a merge whose AUTO_MERGE is missing is refused rather than
//     reconstructed: the merge rows here prove the reconstruction engine is
//     faithful given the name, not that safegit can recover the name.
//
// Randomization is seeded from each subtest's own name, so a failure is
// reproducible from the failing row alone and the recorded baseline is stable,
// while the 36 rows still exercise 36 different conflict shapes.

func TestReconstructionIsByteIdenticalAcrossTheConfigMatrix(t *testing.T) {
	styles := []string{"", "diff3", "zdiff3"}
	markerSizes := []string{"", "3", "12", "20"}
	operations := []string{"merge", "cherry-pick", "revert"}

	for _, style := range styles {
		for _, size := range markerSizes {
			for _, op := range operations {
				name := fmt.Sprintf("%s/%s/%s", styleName(style), sizeName(size), op)
				t.Run(name, func(t *testing.T) {
					rng := rand.New(rand.NewSource(seedFor(name)))
					dir := testutil.InitBareRepo(t)
					testutil.Chdir(t, dir)
					ctx := context.Background()

					if style != "" {
						testutil.Git(t, dir, "config", "merge.conflictStyle", style)
					}

					base, ours, theirs := randomConflictingRevisions(rng)

					// The attributes have to be committed before the conflict:
					// the resolver reads them from the first parent's tree.
					attrFile := ""
					if size != "" {
						attrFile = "*.txt conflict-marker-size=" + size + "\n"
						testutil.WriteFile(t, dir, ".gitattributes", attrFile)
					}
					testutil.WriteFile(t, dir, "f.txt", base)
					addPaths := []string{"f.txt"}
					if attrFile != "" {
						addPaths = append(addPaths, ".gitattributes")
					}
					testutil.Git(t, dir, append([]string{"add"}, addPaths...)...)
					testutil.Git(t, dir, "commit", "-q", "-m", "base")

					var labels conflict.Labels
					var err error
					switch op {
					case "merge":
						testutil.Git(t, dir, "switch", "-q", "-c", "feature")
						testutil.WriteFile(t, dir, "f.txt", theirs)
						testutil.Git(t, dir, "commit", "-q", "-am", "theirs")
						testutil.Git(t, dir, "switch", "-q", "main")
						testutil.WriteFile(t, dir, "f.txt", ours)
						testutil.Git(t, dir, "commit", "-q", "-am", "ours")
						mustConflict(t, dir, "merge", "feature")
						// Supplied, because git recorded it nowhere: "feature" is
						// the name this test typed.
						labels, err = conflict.MergeLabels(ctx, "feature", mergeBases(t, dir))
					case "cherry-pick":
						testutil.Git(t, dir, "switch", "-q", "-c", "feature")
						testutil.WriteFile(t, dir, "f.txt", theirs)
						testutil.Git(t, dir, "commit", "-q", "-am", "the picked subject")
						source := testutil.Rev(t, dir, "HEAD")
						testutil.Git(t, dir, "switch", "-q", "main")
						testutil.WriteFile(t, dir, "f.txt", ours)
						testutil.Git(t, dir, "commit", "-q", "-am", "ours")
						mustConflict(t, dir, "cherry-pick", source)
						labels, err = conflict.PickLabels(ctx, source)
					case "revert":
						// The reverted commit sits between the base and a later
						// edit that touches the same lines, so the inverse patch
						// cannot apply cleanly.
						testutil.WriteFile(t, dir, "f.txt", theirs)
						testutil.Git(t, dir, "commit", "-q", "-am", "the reverted subject")
						source := testutil.Rev(t, dir, "HEAD")
						testutil.WriteFile(t, dir, "f.txt", ours)
						testutil.Git(t, dir, "commit", "-q", "-am", "a later edit")
						mustConflict(t, dir, "revert", "--no-edit", source)
						labels, err = conflict.RevertLabels(ctx, source)
					}
					if err != nil {
						t.Fatalf("deriving the labels: %v", err)
					}

					stages, err := conflict.Stages(ctx, "")
					if err != nil {
						t.Fatal(err)
					}
					sides, ok := stages["f.txt"]
					if !ok {
						t.Fatalf("f.txt is not unmerged; the generated revisions did not conflict\nbase=%q\nours=%q\ntheirs=%q", base, ours, theirs)
					}
					attrs, err := conflict.Resolve(ctx, "HEAD", []string{"f.txt"})
					if err != nil {
						t.Fatal(err)
					}
					if size != "" {
						want, _ := strconv.Atoi(size)
						if attrs["f.txt"].MarkerSize != want {
							t.Fatalf("the resolver reports marker size %d, want the committed %d", attrs["f.txt"].MarkerSize, want)
						}
					}

					got, conflicted, err := conflict.Reconstruct(ctx, sides, attrs["f.txt"], labels)
					if err != nil {
						t.Fatal(err)
					}
					if !conflicted {
						t.Errorf("the reconstruction of a conflicted path reported a clean merge")
					}

					want, present, err := conflict.AutoMergeBlob(ctx, "f.txt")
					if err != nil {
						t.Fatal(err)
					}
					if !present {
						t.Fatalf("no AUTO_MERGE content to compare against (git recorded none for this %s)", op)
					}
					if string(got) != string(want) {
						t.Errorf("reconstruction is not byte-identical to AUTO_MERGE (seed %d)\n got: %q\nwant: %q\nbase=%q ours=%q theirs=%q",
							seedFor(name), got, want, base, ours, theirs)
					}
					// AUTO_MERGE is what git put on disk, so this is also the
					// file the operator is editing.
					if onDisk := worktreeFile(t, dir, "f.txt"); string(got) != onDisk {
						t.Errorf("reconstruction differs from the working-tree file (seed %d)\n got: %q\nwant: %q", seedFor(name), got, onDisk)
					}
					// The marker size really shaped the bytes, so a row whose
					// attribute was ignored cannot pass silently.
					if size != "" {
						n, _ := strconv.Atoi(size)
						if !strings.Contains(string(want), strings.Repeat("<", n)) {
							t.Errorf("the recorded conflict carries no %d-character opening marker:\n%s", n, want)
						}
					}
				})
			}
		}
	}
}

// randomConflictingRevisions generates three revisions of a text file whose two
// edits are guaranteed to conflict: both sides rewrite the same base lines with
// different content, with side-only edits elsewhere so the surrounding hunks
// differ from run to run.
func randomConflictingRevisions(rng *rand.Rand) (base, ours, theirs string) {
	lines := 8 + rng.Intn(12)
	baseLines := make([]string, lines)
	for i := range baseLines {
		baseLines[i] = randomLine(rng, "base")
	}

	oursLines := append([]string(nil), baseLines...)
	theirsLines := append([]string(nil), baseLines...)

	// One to three lines both sides rewrite differently: the conflict itself.
	for _, i := range rng.Perm(lines)[:1+rng.Intn(3)] {
		oursLines[i] = randomLine(rng, "ours")
		theirsLines[i] = randomLine(rng, "theirs")
	}
	// Side-only edits, which git merges cleanly and which therefore vary the
	// non-conflicted parts of the file.
	for _, i := range rng.Perm(lines)[:rng.Intn(3)] {
		if oursLines[i] == baseLines[i] && theirsLines[i] == baseLines[i] {
			oursLines[i] = randomLine(rng, "ours-only")
		}
	}

	return join(baseLines), join(oursLines), join(theirsLines)
}

// randomLine builds one line, occasionally holding characters that shape a
// merge: leading whitespace, a trailing space, a marker-looking prefix that is
// too short to be a marker.
func randomLine(rng *rand.Rand, tag string) string {
	words := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta"}
	line := fmt.Sprintf("%s %s %d", tag, words[rng.Intn(len(words))], rng.Intn(1000))
	switch rng.Intn(6) {
	case 0:
		line = "\t" + line
	case 1:
		line += " "
	case 2:
		line = "<< " + line
	}
	return line
}

func join(lines []string) string { return strings.Join(lines, "\n") + "\n" }

// seedFor derives a stable seed from a subtest name, so every row uses a
// different conflict shape and every failure is reproducible from its own name.
func seedFor(name string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func styleName(style string) string {
	if style == "" {
		return "default-style"
	}
	return style
}

func sizeName(size string) string {
	if size == "" {
		return "default-size"
	}
	return "size" + size
}
