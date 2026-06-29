package git

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

func TestRun(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	stdout, _, err := Run(context.Background(), "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(stdout)
	// Resolve symlinks for comparison (t.TempDir may use /tmp which is a symlink)
	wantResolved, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("RepoRoot = %q, want %q", gotResolved, wantResolved)
	}
}

func TestRunWithEnv(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	// GIT_AUTHOR_NAME should be visible in commit output
	stdout, _, err := RunWithEnv(context.Background(),
		[]string{"GIT_AUTHOR_NAME=EnvTest"},
		"log", "--format=%an", "-1")
	if err != nil {
		t.Fatal(err)
	}
	// The initial commit was made with "Test", not "EnvTest",
	// so this just checks the command runs without error.
	if strings.TrimSpace(stdout) == "" {
		t.Error("expected non-empty output from git log")
	}
}

func TestRepoRoot(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	root, err := RepoRoot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantResolved, _ := filepath.EvalSymlinks(dir)
	gotResolved, _ := filepath.EvalSymlinks(root)
	if gotResolved != wantResolved {
		t.Errorf("RepoRoot = %q, want %q", gotResolved, wantResolved)
	}
}

func TestHeadRef(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	ref, err := HeadRef(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ref != "refs/heads/main" {
		t.Errorf("HeadRef = %q, want refs/heads/main", ref)
	}
}

func TestRevParse(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	sha, err := RevParse(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Errorf("RevParse(HEAD) returned %q, want 40-char SHA", sha)
	}
}

func TestReadTreeAndWriteTree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	// Create a file, add it, commit it so HEAD has a non-empty tree
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644)
	exec.Command("git", "-C", dir, "add", "hello.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "add hello").Run()

	indexPath := filepath.Join(dir, "test-index")
	err := ReadTree(context.Background(), indexPath, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(indexPath); os.IsNotExist(err) {
		t.Fatal("ReadTree did not create index file")
	}

	treeSHA, err := WriteTree(context.Background(), indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(treeSHA) != 40 {
		t.Errorf("WriteTree returned %q, want 40-char SHA", treeSHA)
	}
}

func TestCommitTree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	// Get the tree SHA from HEAD
	headSHA, _ := RevParse(context.Background(), "HEAD")
	treeSHA, _, _ := Run(context.Background(), "rev-parse", "HEAD^{tree}")
	treeSHA = strings.TrimSpace(treeSHA)

	commitSHA, err := CommitTree(context.Background(), treeSHA, headSHA, "test commit")
	if err != nil {
		t.Fatal(err)
	}
	if len(commitSHA) != 40 {
		t.Errorf("CommitTree returned %q, want 40-char SHA", commitSHA)
	}
}

func TestUpdateRef(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	ctx := context.Background()
	headSHA, _ := RevParse(ctx, "HEAD")
	treeSHA, _, _ := Run(ctx, "rev-parse", "HEAD^{tree}")
	treeSHA = strings.TrimSpace(treeSHA)

	newCommit, _ := CommitTree(ctx, treeSHA, headSHA, "new commit")

	// CAS update: expect headSHA, set to newCommit
	err := UpdateRef(ctx, "refs/heads/main", newCommit, headSHA)
	if err != nil {
		t.Fatal(err)
	}

	// Verify
	currentSHA, _ := RevParse(ctx, "refs/heads/main")
	if currentSHA != newCommit {
		t.Errorf("after UpdateRef: ref = %q, want %q", currentSHA, newCommit)
	}

	// CAS failure: wrong old value
	err = UpdateRef(ctx, "refs/heads/main", headSHA, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Error("UpdateRef should fail with wrong old SHA")
	}
}

func TestGitDir(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)

	gitDir, err := GitDir(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(gitDir, ".git") {
		t.Errorf("GitDir = %q, expected to end with .git", gitDir)
	}
}

func TestParseCommit(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Make a second commit so HEAD has a parent
	Run(ctx, "commit", "--allow-empty", "-m", "second")
	sha, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Tree) != 40 {
		t.Errorf("Tree = %q, want 40-char SHA", info.Tree)
	}
	if len(info.Parents) != 1 {
		t.Errorf("Parents = %v, want exactly 1 parent", info.Parents)
	}
	if info.Author.Name != "Test" {
		t.Errorf("Author.Name = %q, want %q", info.Author.Name, "Test")
	}
	if info.Author.Email != "test@test.com" {
		t.Errorf("Author.Email = %q, want %q", info.Author.Email, "test@test.com")
	}
	if info.Committer.Name != "Test" {
		t.Errorf("Committer.Name = %q, want %q", info.Committer.Name, "Test")
	}
	if info.Message != "second" {
		t.Errorf("Message = %q, want %q", info.Message, "second")
	}
}

func TestParseCommitRootCommit(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Find the root commit (the initial --allow-empty commit)
	out, _, err := Run(ctx, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rootSHA := strings.TrimSpace(out)

	info, err := ParseCommit(ctx, rootSHA)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Parents) != 0 {
		t.Errorf("Root commit Parents = %v, want empty", info.Parents)
	}
}

func TestParseCommitMerge(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create a branch from the initial commit, make commits on each, merge
	Run(ctx, "checkout", "-b", "feature")
	Run(ctx, "commit", "--allow-empty", "-m", "feature commit")
	Run(ctx, "checkout", "main")
	Run(ctx, "commit", "--allow-empty", "-m", "main commit")
	Run(ctx, "merge", "feature", "--no-ff", "-m", "merge")

	sha, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Parents) != 2 {
		t.Errorf("Merge commit Parents = %v (len %d), want 2 parents", info.Parents, len(info.Parents))
	}
}

func TestParseCommitMultiLineMessage(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create a commit with a multi-line message including an internal blank line
	wantMsg := "line1\n\nline3"
	Run(ctx, "commit", "--allow-empty", "-m", wantMsg)

	sha, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}

	if info.Message != wantMsg {
		t.Errorf("Message = %q, want %q", info.Message, wantMsg)
	}
}

func TestCommitTreeWithAuthor(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	treeSHA, _, err := Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	treeSHA = strings.TrimSpace(treeSHA)

	headSHA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	author := AuthorInfo{Name: "Alice Author", Email: "alice@example.com", Date: "1700000000 +0000"}
	committer := AuthorInfo{Name: "Bob Committer", Email: "bob@example.com", Date: "1700000001 +0100"}

	newSHA, err := CommitTreeWithAuthor(ctx, treeSHA, []string{headSHA}, "custom commit", author, committer)
	if err != nil {
		t.Fatal(err)
	}
	if len(newSHA) != 40 {
		t.Fatalf("CommitTreeWithAuthor returned %q, want 40-char SHA", newSHA)
	}

	info, err := ParseCommit(ctx, newSHA)
	if err != nil {
		t.Fatal(err)
	}

	if info.Author.Name != author.Name {
		t.Errorf("Author.Name = %q, want %q", info.Author.Name, author.Name)
	}
	if info.Author.Email != author.Email {
		t.Errorf("Author.Email = %q, want %q", info.Author.Email, author.Email)
	}
	if info.Author.Date != author.Date {
		t.Errorf("Author.Date = %q, want %q", info.Author.Date, author.Date)
	}
	if info.Committer.Name != committer.Name {
		t.Errorf("Committer.Name = %q, want %q", info.Committer.Name, committer.Name)
	}
	if info.Committer.Email != committer.Email {
		t.Errorf("Committer.Email = %q, want %q", info.Committer.Email, committer.Email)
	}
	if info.Committer.Date != committer.Date {
		t.Errorf("Committer.Date = %q, want %q", info.Committer.Date, committer.Date)
	}
	if info.Message != "custom commit" {
		t.Errorf("Message = %q, want %q", info.Message, "custom commit")
	}
}

func TestCommitTreeWithAuthorMultipleParents(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create two commits to use as parents
	Run(ctx, "commit", "--allow-empty", "-m", "commit A")
	shaA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	Run(ctx, "commit", "--allow-empty", "-m", "commit B")
	shaB, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	treeSHA, _, err := Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	treeSHA = strings.TrimSpace(treeSHA)

	author := AuthorInfo{Name: "Test", Email: "test@test.com", Date: "1700000000 +0000"}
	committer := author

	newSHA, err := CommitTreeWithAuthor(ctx, treeSHA, []string{shaA, shaB}, "multi-parent", author, committer)
	if err != nil {
		t.Fatal(err)
	}

	info, err := ParseCommit(ctx, newSHA)
	if err != nil {
		t.Fatal(err)
	}

	if len(info.Parents) != 2 {
		t.Fatalf("Parents = %v (len %d), want 2 parents", info.Parents, len(info.Parents))
	}

	parentSet := map[string]bool{info.Parents[0]: true, info.Parents[1]: true}
	if !parentSet[shaA] {
		t.Errorf("Parents %v does not contain shaA %s", info.Parents, shaA)
	}
	if !parentSet[shaB] {
		t.Errorf("Parents %v does not contain shaB %s", info.Parents, shaB)
	}
}

func TestIsAncestorOfTrue(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// InitBareRepo creates one commit. Add two more so we have 3 total.
	first, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	Run(ctx, "commit", "--allow-empty", "-m", "second")
	Run(ctx, "commit", "--allow-empty", "-m", "third")
	third, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	got, err := IsAncestorOf(ctx, first, third)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("IsAncestorOf(first, third) = false, want true")
	}
}

func TestIsAncestorOfSameCommit(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	sha, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	got, err := IsAncestorOf(ctx, sha, sha)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Errorf("IsAncestorOf(sha, sha) = false, want true (commit is its own ancestor)")
	}
}

func TestIsAncestorOfFalse(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create a branch from the initial commit and commit on it.
	Run(ctx, "checkout", "-b", "feature")
	Run(ctx, "commit", "--allow-empty", "-m", "feature commit")
	featureSHA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Switch back to main and commit there.
	Run(ctx, "checkout", "main")
	Run(ctx, "commit", "--allow-empty", "-m", "main commit")
	mainSHA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// feature commit is NOT an ancestor of main HEAD (they diverged).
	got, err := IsAncestorOf(ctx, featureSHA, mainSHA)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Errorf("IsAncestorOf(featureSHA, mainSHA) = true, want false")
	}
}

// initRepoWithSubdir creates a repo with a file and a subdirectory containing
// another file. Returns the repo dir. Tree structure:
//
//	hello.txt   (blob, "hello\n")
//	sub/        (tree)
//	  world.txt (blob, "world\n")
func initRepoWithSubdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "world.txt"), []byte("world\n"), 0644)
	for _, args := range [][]string{
		{"git", "add", "hello.txt", "sub/world.txt"},
		{"git", "commit", "-m", "initial with subdir"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestLsTreeAllPopulatesMode(t *testing.T) {
	dir := initRepoWithSubdir(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	entries, err := LsTreeAll(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("LsTreeAll returned %d entries, want 2", len(entries))
	}

	// LsTreeAll is recursive and blob-only, so we get hello.txt and sub/world.txt
	for _, e := range entries {
		if e.Mode == "" {
			t.Errorf("entry %q has empty Mode", e.Path)
		}
		if e.ObjectType != "blob" {
			t.Errorf("entry %q has ObjectType %q, want blob", e.Path, e.ObjectType)
		}
		if e.SHA == "" {
			t.Errorf("entry %q has empty SHA", e.Path)
		}
		if len(e.SHA) != 40 {
			t.Errorf("entry %q SHA = %q, want 40-char SHA", e.Path, e.SHA)
		}
		// Regular files should be 100644
		if e.Mode != "100644" {
			t.Errorf("entry %q has Mode %q, want 100644", e.Path, e.Mode)
		}
	}
}

func TestLsTreeReturnsOneLevelWithSubtrees(t *testing.T) {
	dir := initRepoWithSubdir(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	entries, err := LsTree(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// At the top level we should see: hello.txt (blob) and sub (tree)
	if len(entries) != 2 {
		t.Fatalf("LsTree returned %d entries, want 2", len(entries))
	}

	entryMap := make(map[string]TreeEntry)
	for _, e := range entries {
		entryMap[e.Path] = e
	}

	hello, ok := entryMap["hello.txt"]
	if !ok {
		t.Fatal("missing hello.txt entry")
	}
	if hello.ObjectType != "blob" {
		t.Errorf("hello.txt ObjectType = %q, want blob", hello.ObjectType)
	}
	if hello.Mode != "100644" {
		t.Errorf("hello.txt Mode = %q, want 100644", hello.Mode)
	}
	if len(hello.SHA) != 40 {
		t.Errorf("hello.txt SHA = %q, want 40-char SHA", hello.SHA)
	}

	sub, ok := entryMap["sub"]
	if !ok {
		t.Fatal("missing sub entry")
	}
	if sub.ObjectType != "tree" {
		t.Errorf("sub ObjectType = %q, want tree", sub.ObjectType)
	}
	if sub.Mode != "040000" {
		t.Errorf("sub Mode = %q, want 040000", sub.Mode)
	}
	if len(sub.SHA) != 40 {
		t.Errorf("sub SHA = %q, want 40-char SHA", sub.SHA)
	}
}

func TestLsTreeSubtreeEntries(t *testing.T) {
	dir := initRepoWithSubdir(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Get the subtree SHA from the top-level listing
	entries, err := LsTree(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var subTreeSHA string
	for _, e := range entries {
		if e.Path == "sub" && e.ObjectType == "tree" {
			subTreeSHA = e.SHA
		}
	}
	if subTreeSHA == "" {
		t.Fatal("could not find sub tree SHA")
	}

	// LsTree the subtree directly
	subEntries, err := LsTree(ctx, subTreeSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(subEntries) != 1 {
		t.Fatalf("LsTree(sub) returned %d entries, want 1", len(subEntries))
	}
	if subEntries[0].Path != "world.txt" {
		t.Errorf("sub entry Path = %q, want world.txt", subEntries[0].Path)
	}
	if subEntries[0].ObjectType != "blob" {
		t.Errorf("sub entry ObjectType = %q, want blob", subEntries[0].ObjectType)
	}
}

func TestHashObjectWrite(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create a file
	testFile := filepath.Join(dir, "hashme.txt")
	os.WriteFile(testFile, []byte("hash me\n"), 0644)

	// Hash without writing (existing function)
	shaNoWrite, err := HashObject(ctx, testFile)
	if err != nil {
		t.Fatal(err)
	}

	// Hash with writing
	shaWrite, err := HashObjectWrite(ctx, testFile)
	if err != nil {
		t.Fatal(err)
	}

	// SHAs must match
	if shaNoWrite != shaWrite {
		t.Errorf("HashObject = %q, HashObjectWrite = %q, want equal", shaNoWrite, shaWrite)
	}

	// Verify the blob exists in the object store via cat-file
	out, _, err := Run(ctx, "cat-file", "-p", shaWrite)
	if err != nil {
		t.Fatalf("cat-file -p %s failed: %v (blob not persisted)", shaWrite, err)
	}
	if out != "hash me\n" {
		t.Errorf("cat-file -p returned %q, want %q", out, "hash me\n")
	}
}

func TestMkTree(t *testing.T) {
	dir := initRepoWithSubdir(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Get the current top-level tree entries
	entries, err := LsTree(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Reconstruct the same tree with MkTree
	treeSHA, err := MkTree(ctx, entries)
	if err != nil {
		t.Fatal(err)
	}
	if len(treeSHA) != 40 {
		t.Fatalf("MkTree returned %q, want 40-char SHA", treeSHA)
	}

	// The reconstructed tree should match the original HEAD tree
	origTree, _, err := Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	origTree = strings.TrimSpace(origTree)

	if treeSHA != origTree {
		t.Errorf("MkTree produced %q, want HEAD tree %q", treeSHA, origTree)
	}

	// Verify by listing the new tree
	newEntries, err := LsTree(ctx, treeSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(newEntries) != len(entries) {
		t.Fatalf("new tree has %d entries, want %d", len(newEntries), len(entries))
	}
}

func TestMkTreeRoundTrip(t *testing.T) {
	dir := initRepoWithSubdir(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Read the top-level tree
	entries, err := LsTree(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Create a new blob to replace hello.txt
	newFile := filepath.Join(dir, "newhello.txt")
	os.WriteFile(newFile, []byte("modified hello\n"), 0644)
	newBlobSHA, err := HashObjectWrite(ctx, newFile)
	if err != nil {
		t.Fatal(err)
	}

	// Modify the entries: replace hello.txt's blob SHA
	var modified []TreeEntry
	for _, e := range entries {
		if e.Path == "hello.txt" {
			modified = append(modified, TreeEntry{
				SHA:        newBlobSHA,
				Path:       e.Path,
				Mode:       e.Mode,
				ObjectType: e.ObjectType,
			})
		} else {
			modified = append(modified, e)
		}
	}

	// Create the new tree
	newTreeSHA, err := MkTree(ctx, modified)
	if err != nil {
		t.Fatal(err)
	}

	// It should differ from the original tree
	origTree, _, _ := Run(ctx, "rev-parse", "HEAD^{tree}")
	origTree = strings.TrimSpace(origTree)
	if newTreeSHA == origTree {
		t.Error("modified tree SHA equals original tree SHA, expected different")
	}

	// Read back the new tree and verify the modification took effect
	readBack, err := LsTree(ctx, newTreeSHA)
	if err != nil {
		t.Fatal(err)
	}

	entryMap := make(map[string]TreeEntry)
	for _, e := range readBack {
		entryMap[e.Path] = e
	}

	hello, ok := entryMap["hello.txt"]
	if !ok {
		t.Fatal("hello.txt missing from rebuilt tree")
	}
	if hello.SHA != newBlobSHA {
		t.Errorf("hello.txt SHA = %q, want %q", hello.SHA, newBlobSHA)
	}

	// Verify the sub tree entry was preserved unchanged
	sub, ok := entryMap["sub"]
	if !ok {
		t.Fatal("sub missing from rebuilt tree")
	}
	if sub.ObjectType != "tree" {
		t.Errorf("sub ObjectType = %q, want tree", sub.ObjectType)
	}

	// Verify sub's contents are intact by listing it
	subEntries, err := LsTree(ctx, sub.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(subEntries) != 1 || subEntries[0].Path != "world.txt" {
		t.Errorf("sub tree contents unexpected: %+v", subEntries)
	}
}

func TestLsTreeEmptyTree(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// HEAD from InitBareRepo is an empty commit (--allow-empty), so the tree is empty
	entries, err := LsTree(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("LsTree on empty tree returned %d entries, want 0", len(entries))
	}
}

func TestMkTreeEmptyEntries(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// MkTree with no entries should produce the empty tree
	treeSHA, err := MkTree(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The well-known SHA of the empty tree
	emptyTree, _, _ := Run(ctx, "rev-parse", "HEAD^{tree}")
	emptyTree = strings.TrimSpace(emptyTree)

	if treeSHA != emptyTree {
		t.Errorf("MkTree(nil) = %q, want empty tree %q", treeSHA, emptyTree)
	}
}

func TestHashObjectWriteBytes(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	data := []byte("hello from memory\n")
	sha, err := HashObjectWriteBytes(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Fatalf("HashObjectWriteBytes returned %q, want 40-char SHA", sha)
	}

	// Read back via CatFileBlob and verify round-trip
	got, err := CatFileBlob(ctx, sha)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Errorf("CatFileBlob returned %q, want %q", got, data)
	}
}

func TestHashObjectWriteBytesEmpty(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	sha, err := HashObjectWriteBytes(ctx, []byte{})
	if err != nil {
		t.Fatal(err)
	}

	// Compute expected empty blob SHA dynamically (works for both SHA-1 and SHA-256 repos)
	expectedSHA, _, err := RunWithEnvStdin(ctx, nil, []byte{}, "hash-object", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	expectedSHA = strings.TrimSpace(expectedSHA)

	if sha != expectedSHA {
		t.Errorf("HashObjectWriteBytes(empty) = %q, want %q", sha, expectedSHA)
	}
}

func TestCatFileBlob(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Create a file, add it, commit it, then read its blob via CatFileBlob
	content := []byte("cat-file test content\n")
	os.WriteFile(filepath.Join(dir, "cattest.txt"), content, 0644)
	exec.Command("git", "-C", dir, "add", "cattest.txt").Run()
	exec.Command("git", "-C", dir, "commit", "-m", "add cattest").Run()

	// Get the blob SHA from the committed tree
	out, _, err := Run(ctx, "rev-parse", "HEAD:cattest.txt")
	if err != nil {
		t.Fatal(err)
	}
	blobSHA := strings.TrimSpace(out)

	got, err := CatFileBlob(ctx, blobSHA)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Errorf("CatFileBlob = %q, want %q", got, content)
	}
}

func TestCatFileBlobNotFound(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	// Use a syntactically valid but nonexistent SHA
	_, err := CatFileBlob(ctx, "0000000000000000000000000000000000000000")
	if err == nil {
		t.Error("CatFileBlob with nonexistent SHA should return error")
	}
}

// initRepoWithCommits creates a repo with multiple commits and files, suitable
// for cat-file --batch-all-objects tests. Returns the repo dir and a map of
// known blob SHAs to their content.
func initRepoWithCommits(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create 3 commits with different files.
	files := map[string]string{
		"a.txt": "alpha\n",
		"b.txt": "bravo\n",
		"c.txt": "charlie\n",
	}
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
		for _, args := range [][]string{
			{"git", "add", name},
			{"git", "commit", "-m", "add " + name},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %v\n%s", args, err, out)
			}
		}
	}

	// Collect known blob SHAs.
	knownBlobs := make(map[string]string)
	for name, content := range files {
		cmd := exec.Command("git", "rev-parse", "HEAD:"+name)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("rev-parse HEAD:%s: %v", name, err)
		}
		knownBlobs[strings.TrimSpace(string(out))] = content
	}
	return dir, knownBlobs
}

func TestCatFileBatchAllEnumerates(t *testing.T) {
	dir, _ := initRepoWithCommits(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	it, err := CatFileBatchAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	counts := map[string]int{}
	for {
		entry, err := it.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		counts[entry.Type]++
	}

	// 3 commits, at least 3 blobs (a.txt, b.txt, c.txt)
	if counts["commit"] < 3 {
		t.Errorf("commit count = %d, want >= 3", counts["commit"])
	}
	if counts["blob"] < 3 {
		t.Errorf("blob count = %d, want >= 3", counts["blob"])
	}
	// Trees should never appear (they are skipped).
	if counts["tree"] != 0 {
		t.Errorf("tree count = %d, want 0 (trees should be skipped)", counts["tree"])
	}
}

func TestCatFileBatchAllSkipsTrees(t *testing.T) {
	dir, _ := initRepoWithCommits(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	it, err := CatFileBatchAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	for {
		entry, err := it.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if entry.Type == "tree" {
			t.Fatalf("got tree entry %s, expected trees to be skipped", entry.SHA)
		}
	}
}

func TestCatFileBatchAllContent(t *testing.T) {
	dir, knownBlobs := initRepoWithCommits(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	it, err := CatFileBatchAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	found := make(map[string]bool)
	for {
		entry, err := it.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if entry.Type != "blob" {
			continue
		}
		if expected, ok := knownBlobs[entry.SHA]; ok {
			if string(entry.Content) != expected {
				t.Errorf("blob %s content = %q, want %q", entry.SHA, entry.Content, expected)
			}
			found[entry.SHA] = true
		}
	}

	for sha := range knownBlobs {
		if !found[sha] {
			t.Errorf("known blob %s not found in iteration", sha)
		}
	}
}

func TestCatFileBatchAllIncludesUnreachable(t *testing.T) {
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Create a commit, then amend it to make the original unreachable.
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original\n"), 0644)
	for _, args := range [][]string{
		{"git", "add", "file.txt"},
		{"git", "commit", "-m", "original"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Record the original commit SHA before amending.
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	originalSHA := strings.TrimSpace(string(out))

	// Amend the commit (makes the original commit unreachable).
	amendCmd := exec.Command("git", "commit", "--amend", "-m", "amended")
	amendCmd.Dir = dir
	if out, err := amendCmd.CombinedOutput(); err != nil {
		t.Fatalf("amend failed: %v\n%s", err, out)
	}

	testutil.Chdir(t, dir)
	ctx := context.Background()

	it, err := CatFileBatchAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer it.Close()

	foundUnreachable := false
	for {
		entry, err := it.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		if entry.SHA == originalSHA {
			foundUnreachable = true
			break
		}
	}

	if !foundUnreachable {
		t.Errorf("unreachable commit %s not found in --batch-all-objects output", originalSHA)
	}
}

func TestCatFileBatchAllClose(t *testing.T) {
	dir, _ := initRepoWithCommits(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	it, err := CatFileBatchAll(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Read just one object, then close immediately.
	entry, err := it.Next()
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected at least one object")
	}

	// Close should not panic or leak.
	if err := it.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

// TestWithDirTargetsSecondRepo creates two repos and verifies that WithDir
// makes git commands target the second repo from the first repo's CWD.
func TestWithDirTargetsSecondRepo(t *testing.T) {
	// Create repo A (our CWD).
	dirA := testutil.InitBareRepo(t)
	testutil.Chdir(t, dirA)

	// Create repo B (the target repo).
	dirB := testutil.InitBareRepo(t)

	// Make a distinct commit in repo B so its HEAD differs from repo A.
	cmdB := exec.Command("git", "commit", "--allow-empty", "-m", "repo-b-commit")
	cmdB.Dir = dirB
	if out, err := cmdB.CombinedOutput(); err != nil {
		t.Fatalf("commit in repo B failed: %v\n%s", err, out)
	}

	ctx := context.Background()

	// Get repo A's HEAD (from CWD).
	headA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Get repo B's HEAD using RunWithGitDir for reference.
	gitDirB := filepath.Join(dirB, ".git")
	refOut, _, err := RunWithGitDir(ctx, gitDirB, dirB, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	headB := strings.TrimSpace(refOut)

	// Heads must differ (different repos, different commit history).
	if headA == headB {
		t.Fatal("repo A and B have the same HEAD; test setup is broken")
	}

	// Now use WithDir to target repo B while CWD is repo A.
	subCtx := WithDir(ctx, gitDirB, dirB)
	headViaCtx, err := RevParse(subCtx, "HEAD")
	if err != nil {
		t.Fatalf("RevParse with WithDir failed: %v", err)
	}

	if headViaCtx != headB {
		t.Errorf("WithDir RevParse = %q, want repo B HEAD %q (got repo A HEAD %q)", headViaCtx, headB, headA)
	}

	// Also verify RepoRoot returns repo B's path.
	rootViaCtx, err := RepoRoot(subCtx)
	if err != nil {
		t.Fatalf("RepoRoot with WithDir failed: %v", err)
	}
	wantRoot, _ := filepath.EvalSymlinks(dirB)
	gotRoot, _ := filepath.EvalSymlinks(rootViaCtx)
	if gotRoot != wantRoot {
		t.Errorf("WithDir RepoRoot = %q, want %q", gotRoot, wantRoot)
	}
}

// TestWithDirDoesNotAffectRunWithGitDir verifies that RunWithGitDir still
// works correctly even when the context has a WithDir override (RunWithGitDir
// should use its own explicit args, not the context).
func TestWithDirDoesNotAffectRunWithGitDir(t *testing.T) {
	dirA := testutil.InitBareRepo(t)
	dirB := testutil.InitBareRepo(t)
	testutil.Chdir(t, dirA)

	// Make a distinct commit in repo B.
	cmdB := exec.Command("git", "commit", "--allow-empty", "-m", "repo-b-only")
	cmdB.Dir = dirB
	if out, err := cmdB.CombinedOutput(); err != nil {
		t.Fatalf("commit in repo B failed: %v\n%s", err, out)
	}

	ctx := context.Background()
	gitDirA := filepath.Join(dirA, ".git")
	gitDirB := filepath.Join(dirB, ".git")

	// Set WithDir to repo A.
	subCtx := WithDir(ctx, gitDirA, dirA)

	// RunWithGitDir targeting repo B should still return repo B's HEAD.
	refOut, _, err := RunWithGitDir(subCtx, gitDirB, dirB, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	headB := strings.TrimSpace(refOut)

	// Verify it's really repo B's HEAD (not repo A's).
	headA, _ := RevParse(ctx, "HEAD")
	if headB == headA {
		t.Error("RunWithGitDir returned repo A's HEAD instead of repo B's HEAD")
	}
}
