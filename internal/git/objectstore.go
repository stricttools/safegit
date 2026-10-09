package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/stricttools/safegit/internal/gitexec"
)

// ObjectStore reads and writes the objects of one repository through two
// long-running git processes -- `cat-file --batch` for every read and
// `mktree -z --batch` for every tree write -- instead of one git process per
// object.
//
// A history rewrite reads every commit and many trees and writes a tree for
// every directory it changes, at every commit. One process per object made a
// rewrite of a repository with a few thousand commits cost hundreds of
// thousands of process starts; the store makes it a handful.
//
// A store is attached to a context with WithObjectStore, and ParseCommit,
// LsTree, CatFileBlob, MkTree, and ObjectType route through it for as long as
// the context they are handed targets the same repository the store was
// started for: same directory override, same root pin, same object
// quarantine. A context that targets anything else (a submodule's WithDir, a
// preview's quarantine added later) does not see the store and runs its own
// git process, so an object can never be read from, or written to, the wrong
// repository.
type ObjectStore struct {
	ctx      context.Context
	identity string

	mu     sync.Mutex
	reader *batchProcess
	trees  *batchProcess
	closed bool
}

// batchProcess is one long-running git process fed on stdin and read on
// stdout.
type batchProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr bytes.Buffer
}

type storeKey struct{}

// errObjectMissing is a read of an object the repository does not hold.
var errObjectMissing = errors.New("no such object")

// WithObjectStore returns a context carrying a new object store for the
// repository ctx targets, and the store. The caller must Close the store when
// the work that uses it is done. The git processes start on first use.
func WithObjectStore(ctx context.Context) (context.Context, *ObjectStore) {
	s := &ObjectStore{ctx: ctx, identity: storeIdentity(ctx)}
	return context.WithValue(ctx, storeKey{}, s), s
}

// storeIdentity names the repository and object store a context's git
// processes reach.
func storeIdentity(ctx context.Context) string {
	gitDir, workTree, dirOK := gitexec.DirOverride(ctx)
	root, rootOK := gitexec.Root(ctx)
	qDir, qAlt, qOK := gitexec.ObjectQuarantine(ctx)
	return fmt.Sprintf("dir=%q,%q,%v root=%q,%v quarantine=%q,%q,%v preview=%v",
		gitDir, workTree, dirOK, root, rootOK, qDir, qAlt, qOK, gitexec.InPreview(ctx))
}

// storeFor returns the store attached to ctx when ctx targets the repository
// the store was started for, and nil otherwise.
func storeFor(ctx context.Context) *ObjectStore {
	s, ok := ctx.Value(storeKey{}).(*ObjectStore)
	if !ok || s == nil {
		return nil
	}
	if storeIdentity(ctx) != s.identity {
		return nil
	}
	return s
}

// Close ends the store's git processes. Using the store after Close is an
// error.
func (s *ObjectStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	var errs []string
	for _, p := range []*batchProcess{s.reader, s.trees} {
		if p == nil {
			continue
		}
		if err := p.close(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	s.reader, s.trees = nil, nil
	if len(errs) > 0 {
		return fmt.Errorf("closing the object store: %s", strings.Join(errs, "; "))
	}
	return nil
}

func startBatch(ctx context.Context, args ...string) (*batchProcess, error) {
	cmd, err := gitexec.Command(ctx, gitexec.Spec{Args: args})
	if err != nil {
		return nil, err
	}
	p := &batchProcess{cmd: cmd}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("git %s: stdin pipe: %w", strings.Join(args, " "), err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("git %s: stdout pipe: %w", strings.Join(args, " "), err)
	}
	cmd.Stderr = &p.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git %s: start: %w", strings.Join(args, " "), err)
	}
	p.stdin = stdin
	p.stdout = bufio.NewReaderSize(stdout, 256*1024)
	return p, nil
}

func (p *batchProcess) close() error {
	_ = p.stdin.Close()
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(p.cmd.Args[1:], " "), err, strings.TrimSpace(p.stderr.String()))
	}
	return nil
}

// failure wraps an I/O error on a batch process with what git said on stderr.
func (p *batchProcess) failure(what string, err error) error {
	return fmt.Errorf("git %s: %s: %w: %s", strings.Join(p.cmd.Args[1:], " "), what, err, strings.TrimSpace(p.stderr.String()))
}

func (s *ObjectStore) readerProcess() (*batchProcess, error) {
	if s.closed {
		return nil, fmt.Errorf("the object store is closed")
	}
	if s.reader == nil {
		p, err := startBatch(s.ctx, "cat-file", "--batch")
		if err != nil {
			return nil, err
		}
		s.reader = p
	}
	return s.reader, nil
}

func (s *ObjectStore) treeProcess() (*batchProcess, error) {
	if s.closed {
		return nil, fmt.Errorf("the object store is closed")
	}
	if s.trees == nil {
		p, err := startBatch(s.ctx, "mktree", "-z", "--batch")
		if err != nil {
			return nil, err
		}
		s.trees = p
	}
	return s.trees, nil
}

// object is one object as cat-file --batch returns it.
type object struct {
	sha     string
	objType string
	content []byte
}

// readObject reads one object by name, whatever its type. A missing object
// is an error, as it is for `git cat-file -p`.
func (s *ObjectStore) readObject(name string) (object, error) {
	if name == "" || strings.ContainsAny(name, "\n\x00") {
		return object{}, fmt.Errorf("cat-file: invalid object name %q", name)
	}
	p, err := s.readerProcess()
	if err != nil {
		return object{}, err
	}
	if _, err := io.WriteString(p.stdin, name+"\n"); err != nil {
		return object{}, p.failure("write request", err)
	}
	header, err := p.stdout.ReadString('\n')
	if err != nil {
		return object{}, p.failure("read header", err)
	}
	header = strings.TrimSuffix(header, "\n")
	// A name git cannot resolve is echoed back as given, spaces and all.
	if header == name+" missing" {
		return object{}, fmt.Errorf("git cat-file: object %s: %w", name, errObjectMissing)
	}
	if header == name+" ambiguous" {
		return object{}, fmt.Errorf("git cat-file: object name %s is ambiguous", name)
	}
	fields := strings.Split(header, " ")
	if len(fields) != 3 {
		return object{}, fmt.Errorf("git cat-file --batch: malformed header %q for %s", header, name)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil {
		return object{}, fmt.Errorf("git cat-file --batch: bad size in header %q: %w", header, err)
	}
	content := make([]byte, size+1)
	if _, err := io.ReadFull(p.stdout, content); err != nil {
		return object{}, p.failure("read content of "+fields[0], err)
	}
	if content[size] != '\n' {
		return object{}, fmt.Errorf("git cat-file --batch: object %s is not followed by a newline", fields[0])
	}
	return object{sha: fields[0], objType: fields[1], content: content[:size]}, nil
}

// read returns the content of the object name, which must be of type want.
func (s *ObjectStore) read(name, want string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, err := s.readObject(name)
	if err != nil {
		return nil, err
	}
	if obj.objType != want {
		return nil, fmt.Errorf("object %s is a %s, not a %s", name, obj.objType, want)
	}
	return obj.content, nil
}

// objectType returns the type of the object name.
func (s *ObjectStore) objectType(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	obj, err := s.readObject(name)
	if errors.Is(err, errObjectMissing) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return obj.objType, nil
}

// lsTree lists the entries of a tree, peeling a commit or a tag to its tree
// the way `git ls-tree` does, and reports them as LsTree does.
func (s *ObjectStore) lsTree(treeish string) ([]TreeEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := treeish
	for range 16 {
		obj, err := s.readObject(name)
		if err != nil {
			return nil, fmt.Errorf("ls-tree %s: %w", treeish, err)
		}
		switch obj.objType {
		case "tree":
			entries, err := parseTreeContent(obj.content, len(obj.sha)/2)
			if err != nil {
				return nil, fmt.Errorf("ls-tree %s: %w", treeish, err)
			}
			return entries, nil
		case "commit":
			name = headerValue(obj.content, "tree")
		case "tag":
			name = headerValue(obj.content, "object")
		default:
			return nil, fmt.Errorf("ls-tree %s: not a tree object", treeish)
		}
		if name == "" {
			return nil, fmt.Errorf("ls-tree %s: a %s with no target", treeish, obj.objType)
		}
	}
	return nil, fmt.Errorf("ls-tree %s: too many levels of tags", treeish)
}

// headerValue returns the value of the first header line key of a commit or
// tag object, or "".
func headerValue(content []byte, key string) string {
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" {
			return ""
		}
		if v, ok := strings.CutPrefix(line, key+" "); ok {
			return v
		}
	}
	return ""
}

// parseTreeContent decodes a raw tree object into the entries `git ls-tree
// --full-tree -z` reports for it: the mode canonicalized and printed as six
// octal digits, the object type derived from the mode, and the name as the
// path. hashLen is the repository's binary object id length.
func parseTreeContent(content []byte, hashLen int) ([]TreeEntry, error) {
	var entries []TreeEntry
	for len(content) > 0 {
		sp := bytes.IndexByte(content, ' ')
		if sp < 0 {
			return nil, fmt.Errorf("malformed tree entry: no mode")
		}
		mode, err := strconv.ParseUint(string(content[:sp]), 8, 32)
		if err != nil {
			return nil, fmt.Errorf("malformed tree entry mode %q: %w", content[:sp], err)
		}
		rest := content[sp+1:]
		nul := bytes.IndexByte(rest, 0)
		if nul < 0 || len(rest) < nul+1+hashLen {
			return nil, fmt.Errorf("malformed tree entry: truncated")
		}
		name := string(rest[:nul])
		sha := hex.EncodeToString(rest[nul+1 : nul+1+hashLen])
		content = rest[nul+1+hashLen:]

		canon, objType := canonicalMode(uint32(mode))
		entries = append(entries, TreeEntry{
			SHA:        sha,
			Path:       name,
			Mode:       fmt.Sprintf("%06o", canon),
			ObjectType: objType,
		})
	}
	return entries, nil
}

// canonicalMode is git's canon_mode, which every tree reader in git applies:
// a regular file is 100644 or 100755 by its owner-execute bit, and every
// other mode is its file type alone.
func canonicalMode(mode uint32) (uint32, string) {
	const (
		typeMask = 0o170000
		regular  = 0o100000
		symlink  = 0o120000
		dir      = 0o040000
		gitlink  = 0o160000
	)
	switch mode & typeMask {
	case regular:
		if mode&0o100 != 0 {
			return regular | 0o755, "blob"
		}
		return regular | 0o644, "blob"
	case symlink:
		return symlink, "blob"
	case dir:
		return dir, "tree"
	default:
		return gitlink, "commit"
	}
}

// mkTree writes a tree object through the store's mktree process.
func (s *ObjectStore) mkTree(entries []TreeEntry) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.treeProcess()
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	for _, e := range entries {
		if strings.ContainsRune(e.Path, 0) || e.Path == "" {
			return "", fmt.Errorf("mktree: invalid entry name %q", e.Path)
		}
		fmt.Fprintf(&buf, "%s %s %s\t%s\x00", e.Mode, e.ObjectType, e.SHA, e.Path)
	}
	// An empty record ends the tree.
	buf.WriteByte(0)
	if _, err := p.stdin.Write(buf.Bytes()); err != nil {
		return "", p.failure("write tree", err)
	}
	line, err := p.stdout.ReadString('\n')
	if err != nil {
		return "", p.failure("read tree id", err)
	}
	return strings.TrimSuffix(line, "\n"), nil
}

// ObjectType returns the type of the object name ("commit", "tree", "blob",
// or "tag"). A name that names no object returns "" and a nil error: absence
// is an answer, and only a failure to ask is an error.
func ObjectType(ctx context.Context, name string) (string, error) {
	if s := storeFor(ctx); s != nil {
		return s.objectType(name)
	}
	if name == "" || strings.ContainsAny(name, "\n\x00") {
		return "", fmt.Errorf("cat-file: invalid object name %q", name)
	}
	out, _, err := RunWithEnvStdin(ctx, nil, []byte(name+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return "", err
	}
	return batchCheckType(strings.TrimSuffix(out, "\n"), name)
}

// batchCheckType reads the type out of one `cat-file --batch-check` (or
// --batch) header line, "" for a missing object.
func batchCheckType(header, name string) (string, error) {
	if header == name+" missing" {
		return "", nil
	}
	fields := strings.Split(header, " ")
	if len(fields) != 3 {
		return "", fmt.Errorf("git cat-file: unexpected answer %q for %s", header, name)
	}
	return fields[1], nil
}

// ChangedPathNames returns the paths at which two trees differ, recursively
// and without rename detection: the paths `git diff-tree -r --no-renames`
// names, in no particular order. Through an object store it compares the trees
// itself, descending only into subtrees whose ids differ, rather than starting
// a diff-tree process per pair.
func ChangedPathNames(ctx context.Context, fromTree, toTree string) ([]string, error) {
	if s := storeFor(ctx); s != nil {
		var out []string
		if err := s.changedPaths(fromTree, toTree, "", &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	changed, err := DiffTree(ctx, fromTree, toTree)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(changed))
	for _, c := range changed {
		out = append(out, c.Path)
	}
	return out, nil
}

// changedPaths appends to out every path below prefix at which the trees from
// and to differ.
func (s *ObjectStore) changedPaths(from, to, prefix string, out *[]string) error {
	if from == to {
		return nil
	}
	fromEntries, err := s.lsTree(from)
	if err != nil {
		return err
	}
	toEntries, err := s.lsTree(to)
	if err != nil {
		return err
	}
	byName := make(map[string]TreeEntry, len(toEntries))
	for _, e := range toEntries {
		byName[e.Path] = e
	}
	for _, f := range fromEntries {
		t, ok := byName[f.Path]
		delete(byName, f.Path)
		switch {
		case !ok:
			if err := s.leafPaths(f, prefix, out); err != nil {
				return err
			}
		case f.ObjectType == "tree" && t.ObjectType == "tree":
			if err := s.changedPaths(f.SHA, t.SHA, prefix+f.Path+"/", out); err != nil {
				return err
			}
		case f.SHA == t.SHA && f.Mode == t.Mode:
		default:
			if err := s.leafPaths(f, prefix, out); err != nil {
				return err
			}
			if err := s.leafPaths(t, prefix, out); err != nil {
				return err
			}
		}
	}
	for _, t := range toEntries {
		if _, ok := byName[t.Path]; ok {
			if err := s.leafPaths(t, prefix, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// leafPaths appends the path of a non-tree entry, or every non-tree path
// under a tree entry, once each.
func (s *ObjectStore) leafPaths(e TreeEntry, prefix string, out *[]string) error {
	if e.ObjectType != "tree" {
		if n := len(*out); n == 0 || (*out)[n-1] != prefix+e.Path {
			*out = append(*out, prefix+e.Path)
		}
		return nil
	}
	entries, err := s.lsTree(e.SHA)
	if err != nil {
		return err
	}
	for _, c := range entries {
		if err := s.leafPaths(c, prefix+e.Path+"/", out); err != nil {
			return err
		}
	}
	return nil
}
