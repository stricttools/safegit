// Package index manages per-invocation temporary git indexes so each safegit invocation stages into its own index seeded from HEAD, avoiding contention.
// No safegit operation writes to the shared .git/index; all staging goes through temporary indexes created here.
package index

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/procutil"
)

// TmpIndex represents a per-invocation temporary index directory.
type TmpIndex struct {
	Dir       string // .git/safegit/tmp/<pid>-<random>/
	IndexPath string // Dir + "/index"
}

// newDir creates the per-invocation directory every constructor here hands
// back, named <pid>-<random> so GarbageCollect can tell whose it is, and
// returns the directory and the index path inside it. baseDir is .git/safegit
// for an executing run; a preview passes an OS temp directory so that nothing
// is written inside .git/.
func newDir(baseDir string) (dir, indexPath string, err error) {
	// Generate 4 random bytes -> 8 hex chars
	var rndBytes [4]byte
	if _, err := rand.Read(rndBytes[:]); err != nil {
		return "", "", fmt.Errorf("generating random suffix: %w", err)
	}
	rnd := hex.EncodeToString(rndBytes[:])

	dirName := fmt.Sprintf("%d-%s", os.Getpid(), rnd)
	dir = filepath.Join(baseDir, "tmp", dirName)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("creating tmp index dir: %w", err)
	}
	return dir, filepath.Join(dir, "index"), nil
}

// New creates a temporary index directory under baseDir/tmp/ and seeds the
// index from the given treeish.
func New(ctx context.Context, baseDir string, treeish string) (*TmpIndex, error) {
	dir, indexPath, err := newDir(baseDir)
	if err != nil {
		return nil, err
	}

	// Seed from the given treeish via git read-tree
	if err := git.ReadTree(ctx, indexPath, treeish); err != nil {
		// Clean up on failure
		os.RemoveAll(dir)
		return nil, fmt.Errorf("seeding index from %s: %w", treeish, err)
	}

	return &TmpIndex{Dir: dir, IndexPath: indexPath}, nil
}

// NewEmpty creates a temporary index directory with an empty index (no tree).
// Used for root commits in repos with no prior commits. baseDir has the same
// meaning as in New.
func NewEmpty(baseDir string) (*TmpIndex, error) {
	dir, indexPath, err := newDir(baseDir)
	if err != nil {
		return nil, err
	}
	// Empty index: just create the dir, git add will initialize the index file
	return &TmpIndex{Dir: dir, IndexPath: indexPath}, nil
}

// NewFromFile creates a temporary index directory whose index starts as a byte
// copy of an existing index file -- in practice the repository's shared
// .git/index, which is where git records a conflict resolution the operator has
// staged. Copying is what keeps safegit's promise never to write to that file:
// the copy is what gets staged into and written out as a tree, and the original
// is only ever read.
//
// The copy carries whatever the source held, unmerged stage entries included; a
// caller that copies a conflicted index and then asks for a tree gets git's own
// refusal to write one, which is the honest answer.
func NewFromFile(baseDir, srcIndexPath string) (*TmpIndex, error) {
	dir, indexPath, err := newDir(baseDir)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(srcIndexPath)
	if err != nil {
		if os.IsNotExist(err) {
			// No index file at all is git's empty index, not a failure.
			return &TmpIndex{Dir: dir, IndexPath: indexPath}, nil
		}
		os.RemoveAll(dir)
		return nil, fmt.Errorf("reading %s: %w", srcIndexPath, err)
	}
	if err := os.WriteFile(indexPath, data, 0644); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("copying %s: %w", srcIndexPath, err)
	}

	return &TmpIndex{Dir: dir, IndexPath: indexPath}, nil
}

// Cleanup removes the temporary index directory.
func (t *TmpIndex) Cleanup() error {
	return os.RemoveAll(t.Dir)
}

// GarbageCollectPlan reports the tmp directories whose owning PID is no longer
// alive, as full PATHS, and removes nothing.
//
// It is the whole scanner: the caller decides what to do with what it found.
// That split is what lets `safegit doctor` diagnose with it, preview with it,
// and repair with it -- the repair removing each planned path through the
// effects handle, so a dry run records the removals it would make instead of
// making them, and machine mode carries them. A scanner that removed as it
// walked could not be asked what it would do without doing it.
func GarbageCollectPlan(safegitDir string) ([]string, error) {
	tmpBase := filepath.Join(safegitDir, "tmp")

	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading tmp dir: %w", err)
	}

	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, ok := parsePIDFromDirName(entry.Name())
		if !ok {
			continue
		}
		if !processAlive(pid) {
			paths = append(paths, filepath.Join(tmpBase, entry.Name()))
		}
	}
	return paths, nil
}

// parsePIDFromDirName extracts the PID from a directory name of format "<pid>-<random>".
func parsePIDFromDirName(name string) (int, bool) {
	parts := strings.SplitN(name, "-", 2)
	if len(parts) != 2 {
		return 0, false
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive checks if a process with the given PID exists.
func processAlive(pid int) bool {
	return procutil.ProcessAlive(pid)
}
