package git

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/testutil"
)

// reconcilePropertyFixedSeed pins the randomized round-trip test's generator.
// Zero means "derive one from the clock and log it"; set it to a seed a failure
// reported to replay that exact run.
const reconcilePropertyFixedSeed int64 = 0

// --- fixture helpers -------------------------------------------------------

// writeIndexInfo feeds lines to `git update-index --index-info`, the same
// plumbing the helper under test uses to replay a delta -- here it builds the
// state the helper is then measured against.
func writeIndexInfo(t *testing.T, ctx context.Context, lines []string) {
	t.Helper()
	if len(lines) == 0 {
		return
	}
	if _, stderr, err := RunWithEnvStdin(ctx, nil, []byte(strings.Join(lines, "")), "update-index", "--index-info"); err != nil {
		t.Fatalf("building the fixture index: %v\nstderr: %s\ninput: %q", err, stderr, strings.Join(lines, ""))
	}
}

// lsFilesStaged returns `git ls-files -s -z` verbatim: the byte string these
// tests compare before and after a reconcile.
func lsFilesStaged(t *testing.T, ctx context.Context) string {
	t.Helper()
	out, stderr, err := Run(ctx, "ls-files", "-s", "-z")
	if err != nil {
		t.Fatalf("git ls-files -s -z: %v\nstderr: %s", err, stderr)
	}
	return out
}

// readableStaged renders ls-files -s -z output for a failure message.
func readableStaged(raw string) string {
	records := strings.Split(raw, "\x00")
	var kept []string
	for _, r := range records {
		if r != "" {
			kept = append(kept, fmt.Sprintf("%q", r))
		}
	}
	return strings.Join(kept, "\n  ")
}

// emptyIndex clears the shared index so a fixture starts from nothing.
func emptyIndex(t *testing.T, ctx context.Context) {
	t.Helper()
	if _, stderr, err := Run(ctx, "read-tree", "--empty"); err != nil {
		t.Fatalf("clearing the index: %v\nstderr: %s", err, stderr)
	}
}

// blob writes content into the object store and returns its object name.
func blob(t *testing.T, ctx context.Context, content string) string {
	t.Helper()
	sha, err := HashObjectWriteBytes(ctx, []byte(content))
	if err != nil {
		t.Fatalf("hash-object %q: %v", content, err)
	}
	return sha
}

// testQuote is an INDEPENDENT C-quoter, deliberately not the production
// quotePathForIndexInfo: it spells the escapes git names (\n, \t, \") where the
// production one always reaches for octal. Both are valid input to git's
// unquoting, so a fixture written with this one and read back proves the two
// agree on what path was meant -- which a fixture built by calling the
// production quoter could never show.
func testQuote(path string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func fixtureLine(mode, sha string, stage int, path string) string {
	return fmt.Sprintf("%s %s %d\t%s\n", mode, sha, stage, testQuote(path))
}

func fixtureRemoval(path string) string {
	return fmt.Sprintf("0 %s\t%s\n", ZeroSHA, testQuote(path))
}

// --- direct tests ----------------------------------------------------------

// The unmerged-replay machinery: stage 1/2/3 entries must come out of a
// reconcile byte-identical, which is only possible if the replay clears the
// stage-0 entry the sync wrote before writing the stages back.
func TestReconcileMainIndexPreservesUnmergedStages(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	base := blob(t, ctx, "base\n")
	ours := blob(t, ctx, "ours\n")
	theirs := blob(t, ctx, "theirs\n")
	resolved := blob(t, ctx, "resolved\n")

	// A tip holding both paths at stage 0.
	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{
		fixtureLine("100644", base, 0, "conflict.txt"),
		fixtureLine("100644", base, 0, "quiet.txt"),
	})
	tree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	tip, err := CommitTree(ctx, strings.TrimSpace(tree), nil, "tip", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// The index a session mid-conflict holds: three stages on one path, an
	// unrelated resolved modification on the other.
	writeIndexInfo(t, ctx, []string{
		fixtureRemoval("conflict.txt"),
		fixtureLine("100644", base, 1, "conflict.txt"),
		fixtureLine("100644", ours, 2, "conflict.txt"),
		fixtureLine("100644", theirs, 3, "conflict.txt"),
		fixtureLine("100644", resolved, 0, "quiet.txt"),
	})
	want := lsFilesStaged(t, ctx)
	if !strings.Contains(want, " 3\tconflict.txt") {
		t.Fatalf("fixture: no stage-3 entry in the index:\n  %s", readableStaged(want))
	}

	if err := ReconcileMainIndex(ctx, tip, tip, nil); err != nil {
		t.Fatalf("ReconcileMainIndex: %v", err)
	}

	got := lsFilesStaged(t, ctx)
	if got != want {
		t.Fatalf("the index did not survive a reconcile byte-identically.\n  want:\n  %s\n  got:\n  %s",
			readableStaged(want), readableStaged(got))
	}

	// The stages must still be a conflict as far as git is concerned.
	unmerged, _, err := Run(ctx, "ls-files", "-u")
	if err != nil {
		t.Fatalf("git ls-files -u: %v", err)
	}
	if len(strings.Fields(unmerged)) == 0 {
		t.Errorf("git no longer reports a conflict after the reconcile: %q", unmerged)
	}
}

// Foreign staged work -- a modification, an addition and a staged deletion --
// survives a reconcile onto a DIFFERENT commit, and the commit's own content
// arrives alongside it.
func TestReconcileMainIndexPreservesForeignStagedWork(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	v1 := blob(t, ctx, "v1\n")
	v2 := blob(t, ctx, "v2\n")
	added := blob(t, ctx, "added\n")
	fromCommit := blob(t, ctx, "from the commit\n")

	// Tip: base.txt and doomed.txt.
	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{
		fixtureLine("100644", v1, 0, "base.txt"),
		fixtureLine("100644", v1, 0, "doomed.txt"),
	})
	tipTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	tip, err := CommitTree(ctx, strings.TrimSpace(tipTree), nil, "tip", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// A later commit that adds a path of its own and leaves the rest alone.
	writeIndexInfo(t, ctx, []string{fixtureLine("100644", fromCommit, 0, "committed.txt")})
	nextTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	next, err := CommitTree(ctx, strings.TrimSpace(nextTree), []string{tip}, "next", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// Back to the tip's index, then the three kinds of foreign staged work.
	if _, _, err := Run(ctx, "read-tree", tip); err != nil {
		t.Fatalf("read-tree tip: %v", err)
	}
	writeIndexInfo(t, ctx, []string{
		fixtureLine("100644", v2, 0, "base.txt"),  // staged modification
		fixtureLine("100755", added, 0, "new.sh"), // staged addition
		fixtureRemoval("doomed.txt"),              // staged deletion
	})

	if err := ReconcileMainIndex(ctx, tip, next, nil); err != nil {
		t.Fatalf("ReconcileMainIndex: %v", err)
	}

	slots, err := readIndexSlots(ctx)
	if err != nil {
		t.Fatalf("reading the index back: %v", err)
	}
	byPath := map[string]indexSlot{}
	for _, s := range slots {
		byPath[s.Path] = s
	}
	if got, ok := byPath["base.txt"]; !ok || got.SHA != v2 {
		t.Errorf("the staged modification of base.txt did not survive: %+v (want %s)", got, v2)
	}
	if got, ok := byPath["new.sh"]; !ok || got.SHA != added || got.Mode != "100755" {
		t.Errorf("the staged addition new.sh did not survive: %+v", got)
	}
	if got, ok := byPath["doomed.txt"]; ok {
		t.Errorf("the staged deletion of doomed.txt was undone by the reconcile: %+v", got)
	}
	if got, ok := byPath["committed.txt"]; !ok || got.SHA != fromCommit {
		t.Errorf("the commit's own path is missing from the reconciled index: %+v", got)
	}
}

// A path the operation staged itself is left as the sync wrote it, whatever the
// index held for it before. The staged deletion here is the shape an archiving
// deletion tool leaves behind (`git rm --cached` on a tracked file), and the
// operation then commits new bytes at that same path: replaying the deletion
// would un-commit it in the index while HEAD carried it.
func TestReconcileMainIndexDropsDeltaOfPathsTheOperationStaged(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	v1 := blob(t, ctx, "original\n")
	v2 := blob(t, ctx, "regenerated\n")
	other := blob(t, ctx, "theirs\n")

	// Tip: the path the operation will re-commit, plus one it never names.
	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{
		fixtureLine("100644", v1, 0, "mine.txt"),
		fixtureLine("100644", other, 0, "theirs.txt"),
	})
	tipTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	tip, err := CommitTree(ctx, strings.TrimSpace(tipTree), nil, "tip", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// The operation's commit: new bytes at mine.txt, theirs.txt untouched.
	writeIndexInfo(t, ctx, []string{fixtureLine("100644", v2, 0, "mine.txt")})
	nextTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	next, err := CommitTree(ctx, strings.TrimSpace(nextTree), []string{tip}, "next", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// The shared index as the deletion tool left it: both paths removed from
	// the index, neither removed from the commit's tree.
	if _, _, err := Run(ctx, "read-tree", tip); err != nil {
		t.Fatalf("read-tree tip: %v", err)
	}
	writeIndexInfo(t, ctx, []string{
		fixtureRemoval("mine.txt"),
		fixtureRemoval("theirs.txt"),
	})

	if err := ReconcileMainIndex(ctx, tip, next, []string{"mine.txt"}); err != nil {
		t.Fatalf("ReconcileMainIndex: %v", err)
	}

	slots, err := readIndexSlots(ctx)
	if err != nil {
		t.Fatalf("reading the index back: %v", err)
	}
	byPath := map[string]indexSlot{}
	for _, s := range slots {
		byPath[s.Path] = s
	}
	if got, ok := byPath["mine.txt"]; !ok || got.SHA != v2 {
		t.Errorf("the operation's own path was left staged for deletion: %+v (want %s)", got, v2)
	}
	if got, ok := byPath["theirs.txt"]; ok {
		t.Errorf("the staged deletion of a path the operation never named was undone: %+v", got)
	}
}

// The other half of the same rule: a SLOT the index holds for a path the
// operation staged is dropped too, not replayed on top of the sync. That slot
// is the shape `git mv old new` leaves at the new path when the file is then
// edited before being committed -- the pre-edit blob, which replaying would put
// back over the edited one the commit recorded.
func TestReconcileMainIndexDropsSlotOfPathsTheOperationStaged(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	original := blob(t, ctx, "original\n")
	edited := blob(t, ctx, "edited\n")
	theirs := blob(t, ctx, "theirs\n")
	theirsStaged := blob(t, ctx, "their staged work\n")

	// Tip: the path before the rename, plus one the operation never names.
	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{
		fixtureLine("100644", original, 0, "docs/a.md"),
		fixtureLine("100644", theirs, 0, "theirs.txt"),
	})
	tipTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	tip, err := CommitTree(ctx, strings.TrimSpace(tipTree), nil, "tip", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// The operation's commit: the edited bytes at the new path, the old path
	// gone, theirs.txt untouched.
	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{
		fixtureLine("100644", edited, 0, "docs/b.md"),
		fixtureLine("100644", theirs, 0, "theirs.txt"),
	})
	nextTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	next, err := CommitTree(ctx, strings.TrimSpace(nextTree), []string{tip}, "next", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// The shared index as `git mv` left it, plus another session's staged work
	// at a path the operation never named.
	if _, _, err := Run(ctx, "read-tree", tip); err != nil {
		t.Fatalf("read-tree tip: %v", err)
	}
	writeIndexInfo(t, ctx, []string{
		fixtureRemoval("docs/a.md"),
		fixtureLine("100644", original, 0, "docs/b.md"),
		fixtureLine("100644", theirsStaged, 0, "theirs.txt"),
	})

	if err := ReconcileMainIndex(ctx, tip, next, []string{"docs/a.md", "docs/b.md"}); err != nil {
		t.Fatalf("ReconcileMainIndex: %v", err)
	}

	slots, err := readIndexSlots(ctx)
	if err != nil {
		t.Fatalf("reading the index back: %v", err)
	}
	byPath := map[string]indexSlot{}
	for _, s := range slots {
		byPath[s.Path] = s
	}
	if got, ok := byPath["docs/b.md"]; !ok || got.SHA != edited {
		t.Errorf("the pre-edit blob the rename staged was replayed over the commit's own: %+v (want %s)", got, edited)
	}
	if got, ok := byPath["docs/a.md"]; ok {
		t.Errorf("the rename's old path came back into the index: %+v", got)
	}
	if got, ok := byPath["theirs.txt"]; !ok || got.SHA != theirsStaged {
		t.Errorf("the staged work of a path the operation never named was lost: %+v (want %s)", got, theirsStaged)
	}
}

// Skip-worktree flags are restored on paths the reconciled index still holds,
// and a path the sync legitimately dropped is not an error.
func TestReconcileMainIndexRestoresSkipWorktree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	content := blob(t, ctx, "content\n")

	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{
		fixtureLine("100644", content, 0, "kept.cfg"),
		fixtureLine("100644", content, 0, "dropped.cfg"),
	})
	tipTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	tip, err := CommitTree(ctx, strings.TrimSpace(tipTree), nil, "tip", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	// The next commit keeps only one of them.
	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{fixtureLine("100644", content, 0, "kept.cfg")})
	nextTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	next, err := CommitTree(ctx, strings.TrimSpace(nextTree), []string{tip}, "next", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	if _, _, err := Run(ctx, "read-tree", tip); err != nil {
		t.Fatalf("read-tree tip: %v", err)
	}
	if _, _, err := RunWithEnvStdin(ctx, nil, []byte("kept.cfg\x00dropped.cfg\x00"), "update-index", "--skip-worktree", "-z", "--stdin"); err != nil {
		t.Fatalf("setting skip-worktree: %v", err)
	}

	if err := ReconcileMainIndex(ctx, tip, next, nil); err != nil {
		t.Fatalf("ReconcileMainIndex: %v", err)
	}

	flagged, err := ListSkipWorktreeFiles(ctx)
	if err != nil {
		t.Fatalf("ListSkipWorktreeFiles: %v", err)
	}
	if !contains(flagged, "kept.cfg") {
		t.Errorf("skip-worktree was not restored on kept.cfg (flagged: %v)", flagged)
	}
	if contains(flagged, "dropped.cfg") {
		t.Errorf("dropped.cfg is flagged although the reconcile removed it from the index (flagged: %v)", flagged)
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Skip-worktree survives a reconcile onto the empty tree without error: there
// is no entry left to carry the flag, and demanding one would turn undoing a
// root commit into a failure.
func TestReconcileMainIndexToEmptyTreeClearsEverything(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	content := blob(t, ctx, "content\n")
	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{fixtureLine("100644", content, 0, "only.cfg")})
	tipTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	tip, err := CommitTree(ctx, strings.TrimSpace(tipTree), nil, "tip", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}
	if _, _, err := RunWithEnvStdin(ctx, nil, []byte("only.cfg\x00"), "update-index", "--skip-worktree", "-z", "--stdin"); err != nil {
		t.Fatalf("setting skip-worktree: %v", err)
	}

	if err := ReconcileMainIndex(ctx, tip, "", nil); err != nil {
		t.Fatalf("ReconcileMainIndex onto the empty tree: %v", err)
	}
	if got := lsFilesStaged(t, ctx); got != "" {
		t.Errorf("the index is not empty after a reconcile onto the empty tree:\n  %s", readableStaged(got))
	}
}

// An empty pre-operation tip -- the root-commit case -- makes the whole index
// delta, so nothing staged is lost when the first commit arrives.
func TestReconcileMainIndexWithNoPriorTipKeepsEverythingStaged(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	staged := blob(t, ctx, "staged\n")
	committed := blob(t, ctx, "committed\n")

	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{fixtureLine("100644", committed, 0, "root.txt")})
	rootTree, _, err := Run(ctx, "write-tree")
	if err != nil {
		t.Fatalf("write-tree: %v", err)
	}
	root, err := CommitTree(ctx, strings.TrimSpace(rootTree), nil, "root", nil)
	if err != nil {
		t.Fatalf("commit-tree: %v", err)
	}

	emptyIndex(t, ctx)
	writeIndexInfo(t, ctx, []string{fixtureLine("100644", staged, 0, "other.txt")})

	if err := ReconcileMainIndex(ctx, "", root, nil); err != nil {
		t.Fatalf("ReconcileMainIndex: %v", err)
	}

	slots, err := readIndexSlots(ctx)
	if err != nil {
		t.Fatalf("reading the index back: %v", err)
	}
	seen := map[string]string{}
	for _, s := range slots {
		seen[s.Path] = s.SHA
	}
	if seen["other.txt"] != staged {
		t.Errorf("the pre-root-commit staged path was dropped (index: %v)", seen)
	}
	if seen["root.txt"] != committed {
		t.Errorf("the root commit's own path is missing from the index (index: %v)", seen)
	}
}

// --- the randomized round-trip property ------------------------------------

// pathTokens are the segment shapes a path can be built from: ordinary names
// plus every spelling that has ever broken a line-oriented git reader.
var pathTokens = []string{
	"plain", "two words", "quote\"inside", "back\\slash", "héllo", "日本語",
	"\xff\xfe-not-utf8", "-leading-dash", "--force", "tab\there",
	"newline\nhere", "semi;colon", "star*glob", "hash#mark", "dollar$sign",
	"tick`quote", "'single'", "paren(s)", "brack[et]", "percent%", "at@sign",
	"trailing.dot.", "  leading-spaces", "colon:inside", "carriage\rreturn",
}

var pathModes = []string{"100644", "100755", "120000"}

// randomPaths builds n distinct paths, none of which is a directory prefix of
// another: directory segments are drawn from a "d"-prefixed namespace and leaf
// segments from an "f<i>"-prefixed one, so no full-segment prefix relation can
// arise (git cannot hold "a" as a file and "a/b" as another in one index).
func randomPaths(r *rand.Rand, n int) []string {
	paths := make([]string, 0, n)
	for i := 0; i < n; i++ {
		depth := r.Intn(3)
		var segs []string
		for d := 0; d < depth; d++ {
			segs = append(segs, "d"+pathTokens[r.Intn(len(pathTokens))])
		}
		segs = append(segs, fmt.Sprintf("f%d", i)+pathTokens[r.Intn(len(pathTokens))])
		paths = append(paths, strings.Join(segs, "/"))
	}
	return paths
}

// TestReconcileMainIndexRandomizedRoundTrip is the property the whole helper
// rests on: reconciling an index against the very tip it was built from must
// leave it byte-identical, whatever mixture of resolved entries, unmerged
// stages, staged deletions, file modes and hostile path spellings it holds.
//
// Reconciling onto the SAME tip is what makes the expected value exact rather
// than a second implementation of the helper's own logic: the read-tree
// restores the tip, and every difference the index carried has to be replayed
// back for the two ls-files outputs to match.
func TestReconcileMainIndexRandomizedRoundTrip(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	seed := reconcilePropertyFixedSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	t.Logf("generator seed %d (set reconcilePropertyFixedSeed to replay this run)", seed)
	master := rand.New(rand.NewSource(seed))

	iterations := 200
	if testing.Short() {
		iterations = 40
	}

	blobs := make([]string, 6)
	for i := range blobs {
		blobs[i] = blob(t, ctx, fmt.Sprintf("blob %d\n", i))
	}

	for iter := 0; iter < iterations; iter++ {
		iterSeed := master.Int63()
		r := rand.New(rand.NewSource(iterSeed))

		paths := randomPaths(r, 1+r.Intn(8))
		sort.Strings(paths)

		type shape struct {
			inTip   bool
			tipMode string
			tipSHA  string
			stages  []int // empty means no index slot at all
			mode    string
			shas    map[int]string
		}
		shapes := make(map[string]*shape, len(paths))
		var tipLines, indexLines []string

		for _, p := range paths {
			s := &shape{shas: map[int]string{}}
			s.inTip = r.Intn(4) > 0 // most paths exist in the tip
			s.tipMode = pathModes[r.Intn(len(pathModes))]
			s.tipSHA = blobs[r.Intn(len(blobs))]
			s.mode = pathModes[r.Intn(len(pathModes))]

			switch r.Intn(6) {
			case 0, 1:
				// Resolved and identical to the tip (when it is in the tip at
				// all): the entry the reconcile must leave alone.
				s.stages = []int{0}
				s.mode = s.tipMode
				s.shas[0] = s.tipSHA
			case 2, 3:
				// Resolved but different: a staged modification or addition.
				s.stages = []int{0}
				s.shas[0] = blobs[r.Intn(len(blobs))]
			case 4:
				// Unmerged: a non-empty subset of stages 1, 2, 3.
				for _, st := range []int{1, 2, 3} {
					if r.Intn(3) > 0 {
						s.stages = append(s.stages, st)
						s.shas[st] = blobs[r.Intn(len(blobs))]
					}
				}
				if len(s.stages) == 0 {
					s.stages = []int{2}
					s.shas[2] = blobs[r.Intn(len(blobs))]
				}
			case 5:
				// No slot at all. Against a tip that has the path this is a
				// staged deletion; against one that does not it is nothing.
				s.stages = nil
			}

			shapes[p] = s
			if s.inTip {
				tipLines = append(tipLines, fixtureLine(s.tipMode, s.tipSHA, 0, p))
			}
			for _, st := range s.stages {
				indexLines = append(indexLines, fixtureLine(s.mode, s.shas[st], st, p))
			}
		}

		// Build the tip commit from its own index state.
		emptyIndex(t, ctx)
		writeIndexInfo(t, ctx, tipLines)
		treeOut, stderr, err := Run(ctx, "write-tree")
		if err != nil {
			t.Fatalf("iteration %d (seed %d): write-tree: %v\nstderr: %s", iter, iterSeed, err, stderr)
		}
		tip, err := CommitTree(ctx, strings.TrimSpace(treeOut), nil, fmt.Sprintf("tip %d", iter), nil)
		if err != nil {
			t.Fatalf("iteration %d (seed %d): commit-tree: %v", iter, iterSeed, err)
		}

		// Build the index state under test.
		emptyIndex(t, ctx)
		writeIndexInfo(t, ctx, indexLines)

		want := lsFilesStaged(t, ctx)

		// The fixture's own quoting is verified here: every path the generator
		// meant to write must be a path git actually recorded. Without this, a
		// quoting bug shared by fixture and helper would round-trip happily.
		realized, err := readIndexSlots(ctx)
		if err != nil {
			t.Fatalf("iteration %d (seed %d): reading the fixture index: %v", iter, iterSeed, err)
		}
		for _, slot := range realized {
			if _, ok := shapes[slot.Path]; !ok {
				t.Fatalf("iteration %d (seed %d): the fixture wrote a path the generator never named: %q\n  generated: %q",
					iter, iterSeed, slot.Path, paths)
			}
		}

		if err := ReconcileMainIndex(ctx, tip, tip, nil); err != nil {
			t.Fatalf("iteration %d (seed %d): ReconcileMainIndex: %v\n  index was:\n  %s",
				iter, iterSeed, err, readableStaged(want))
		}

		got := lsFilesStaged(t, ctx)
		if got != want {
			t.Fatalf("iteration %d (seed %d): the index did not round-trip through a reconcile.\n  want:\n  %s\n  got:\n  %s",
				iter, iterSeed, readableStaged(want), readableStaged(got))
		}
	}
}

// The production quoter and git agree on every byte a path can hold: each
// spelling below is written through quotePathForIndexInfo and must come back
// out of the index unchanged.
func TestQuotePathForIndexInfoRoundTripsThroughGit(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	sha := blob(t, ctx, "content\n")
	emptyIndex(t, ctx)

	var lines []string
	want := make(map[string]bool)
	for i, token := range pathTokens {
		p := fmt.Sprintf("f%d", i) + token
		want[p] = true
		lines = append(lines, indexInfoLine(indexSlot{Mode: "100644", SHA: sha, Stage: 0, Path: p}))
	}
	if _, stderr, err := RunWithEnvStdin(ctx, nil, []byte(strings.Join(lines, "")), "update-index", "--index-info"); err != nil {
		t.Fatalf("update-index --index-info with production-quoted paths: %v\nstderr: %s", err, stderr)
	}

	slots, err := readIndexSlots(ctx)
	if err != nil {
		t.Fatalf("reading the index back: %v", err)
	}
	for _, s := range slots {
		if !want[s.Path] {
			t.Errorf("git recorded a path the quoter did not mean to write: %q", s.Path)
		}
		delete(want, s.Path)
	}
	for p := range want {
		t.Errorf("path %q never reached the index", p)
	}
}
