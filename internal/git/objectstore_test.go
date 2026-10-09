package git

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stricttools/safegit/internal/testutil"
)

// storeFixture builds a repository whose trees hold every entry shape a tree
// reader has to decode -- an executable, a symlink, a gitlink, nested
// directories, and names with a leading quote, a backslash, a space, and a
// newline -- plus a tree written with a non-canonical file mode, which git's
// own readers canonicalize. It returns the repository directory and the
// literal tree's SHA.
func storeFixture(t *testing.T, objectFormat string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	git := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--initial-branch=main", "--object-format="+objectFormat)
	git("", "config", "user.email", "test@test.com")
	git("", "config", "user.name", "Test")
	write := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("plain.txt", "plain\n", 0o644)
	write("run.sh", "#!/bin/sh\n", 0o755)
	write("sub/deep/x.txt", "deep\n", 0o644)
	write("\"quoted", "q\n", 0o644)
	write("back\\slash", "b\n", 0o644)
	write("with space", "s\n", 0o644)
	write("new\nline", "n\n", 0o644)
	if err := os.Symlink("plain.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	git("", "add", "-A")
	git("", "commit", "-q", "-m", "fixture")
	head := git("", "rev-parse", "HEAD")
	git("", "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	git("", "commit", "-q", "-m", "gitlink")

	// A tree with mode 100664, which only an old git or a hand-made object
	// carries.
	blob := git("odd\n", "hash-object", "-w", "--stdin")
	raw, err := hex.DecodeString(blob)
	if err != nil {
		t.Fatal(err)
	}
	var tree bytes.Buffer
	tree.WriteString("100664 odd.txt\x00")
	tree.Write(raw)
	literal := git(tree.String(), "hash-object", "-t", "tree", "-w", "--literally", "--stdin")
	return dir, literal
}

// TestObjectStoreAnswersAsTheSpawningReadersDo pins the store to the readers
// it replaces: for every object a rewrite reads, the store's answer and the
// one-process-per-object answer are the same, and a tree it writes back has
// the SHA mktree gives it.
func TestObjectStoreAnswersAsTheSpawningReadersDo(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			dir, literal := storeFixture(t, format)
			testutil.Chdir(t, dir)
			plain := context.Background()
			stored, store := WithObjectStore(plain)
			defer store.Close()

			head, err := RevParse(plain, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			for _, sha := range []string{head, head + "~1"} {
				want, err := ParseCommit(plain, sha)
				if err != nil {
					t.Fatal(err)
				}
				got, err := ParseCommit(stored, sha)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("ParseCommit(%s): store %+v, spawn %+v", sha, got, want)
				}
			}

			subTree, err := RevParse(plain, "HEAD:sub")
			if err != nil {
				t.Fatal(err)
			}
			for _, treeish := range []string{head, "HEAD", subTree, literal} {
				want, err := LsTree(plain, treeish)
				if err != nil {
					t.Fatal(err)
				}
				got, err := LsTree(stored, treeish)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("LsTree(%s):\nstore %+v\nspawn %+v", treeish, got, want)
				}
				wantSHA, err := MkTree(plain, want)
				if err != nil {
					t.Fatal(err)
				}
				gotSHA, err := MkTree(stored, got)
				if err != nil {
					t.Fatal(err)
				}
				if gotSHA != wantSHA {
					t.Errorf("MkTree of %s: store %s, spawn %s", treeish, gotSHA, wantSHA)
				}
			}

			emptyWant, err := MkTree(plain, nil)
			if err != nil {
				t.Fatal(err)
			}
			emptyGot, err := MkTree(stored, nil)
			if err != nil {
				t.Fatal(err)
			}
			if emptyGot != emptyWant {
				t.Errorf("empty tree: store %s, spawn %s", emptyGot, emptyWant)
			}

			entries, err := LsTree(plain, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if e.ObjectType != "blob" {
					continue
				}
				want, err := CatFileBlob(plain, e.SHA)
				if err != nil {
					t.Fatal(err)
				}
				got, err := CatFileBlob(stored, e.SHA)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("CatFileBlob(%s): store %q, spawn %q", e.Path, got, want)
				}
			}

			missing := strings.Repeat("0", len(head)-1) + "1"
			for _, name := range []string{head, subTree, entries[0].SHA, missing} {
				want, err := ObjectType(plain, name)
				if err != nil {
					t.Fatal(err)
				}
				got, err := ObjectType(stored, name)
				if err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("ObjectType(%s): store %q, spawn %q", name, got, want)
				}
			}
			if typ, _ := ObjectType(stored, missing); typ != "" {
				t.Errorf("ObjectType of a missing object = %q, want \"\"", typ)
			}

			if _, err := ParseCommit(stored, subTree); err == nil {
				t.Error("ParseCommit of a tree through the store succeeded; it must refuse a non-commit")
			}
		})
	}
}

// TestObjectStoreSeesObjectsWrittenAfterItStarted: a rewrite reads back trees
// and commits it has just written through other processes, so the long-running
// reader must see objects that did not exist when it started.
func TestObjectStoreSeesObjectsWrittenAfterItStarted(t *testing.T) {
	dir, _ := storeFixture(t, "sha1")
	testutil.Chdir(t, dir)
	stored, store := WithObjectStore(context.Background())
	defer store.Close()

	if _, err := LsTree(stored, "HEAD"); err != nil {
		t.Fatal(err)
	}
	sha, err := HashObjectWriteBytes(context.Background(), []byte("written later\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := CatFileBlob(stored, sha)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "written later\n" {
		t.Errorf("read %q", got)
	}
}

// TestObjectStoreIsNotUsedForAnotherRepository: a context derived from the
// store's context that targets another repository must not reach the store,
// or a submodule read would be answered from the parent's objects.
func TestObjectStoreIsNotUsedForAnotherRepository(t *testing.T) {
	parent, _ := storeFixture(t, "sha1")
	other, _ := storeFixture(t, "sha1")
	testutil.Chdir(t, parent)
	stored, store := WithObjectStore(context.Background())
	defer store.Close()

	if storeFor(stored) != store {
		t.Fatal("the context the store was made for does not reach it")
	}
	retargeted := WithDir(stored, filepath.Join(other, ".git"), other)
	if storeFor(retargeted) != nil {
		t.Fatal("a context retargeted with WithDir still reaches the parent's store")
	}
	blob, err := HashObjectWriteBytes(retargeted, []byte("only in the other repository\n"))
	if err != nil {
		t.Fatal(err)
	}
	if typ, err := ObjectType(retargeted, blob); err != nil || typ != "blob" {
		t.Fatalf("ObjectType in the retargeted repository = %q, %v", typ, err)
	}
	if typ, err := ObjectType(stored, blob); err != nil || typ != "" {
		t.Fatalf("ObjectType through the parent's store = %q, %v; want missing", typ, err)
	}
}

// TestChangedPathNamesMatchesDiffTree: the store's tree comparison names the
// paths diff-tree names, across every change shape -- modification, addition,
// deletion, a file becoming a directory, a symlink becoming a file, a mode
// change, and a gitlink moving.
func TestChangedPathNamesMatchesDiffTree(t *testing.T) {
	dir, _ := storeFixture(t, "sha1")
	testutil.Chdir(t, dir)
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	before := run("rev-parse", "HEAD^{tree}")
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sub/deep/x.txt", "changed\n")
	write("added/new.txt", "new\n")
	run("rm", "-q", "with space")
	run("rm", "-q", "plain.txt")
	write("plain.txt/now-a-dir.txt", "dir\n")
	run("rm", "-q", "link")
	write("link", "a file now\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("update-index", "--add", "--cacheinfo", "160000,"+run("rev-parse", "HEAD")+",module")
	after := run("write-tree")

	ctx := context.Background()
	want, err := ChangedPathNames(ctx, before, after)
	if err != nil {
		t.Fatal(err)
	}
	stored, store := WithObjectStore(ctx)
	defer store.Close()
	got, err := ChangedPathNames(stored, before, after)
	if err != nil {
		t.Fatal(err)
	}
	set := func(paths []string) map[string]bool {
		m := map[string]bool{}
		for _, p := range paths {
			m[p] = true
		}
		return m
	}
	if !reflect.DeepEqual(set(got), set(want)) {
		t.Errorf("store %v\ndiff-tree %v", got, want)
	}
	if len(want) < 7 {
		t.Errorf("the fixture changed only %v; it is meant to cover every change shape", want)
	}
}
