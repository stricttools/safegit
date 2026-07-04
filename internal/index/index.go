// Package index manages per-invocation temporary git indexes so each safegit invocation stages into its own index seeded from HEAD, avoiding contention.
// No safegit operation writes to the shared .git/index; all staging goes through temporary indexes created here.
package index

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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

// New creates a temporary index directory and seeds the index from the given treeish.
// The directory name is <pid>-<random> where random is 4 bytes hex.
func New(ctx context.Context, safegitDir string, treeish string) (*TmpIndex, error) {
	tmpBase := filepath.Join(safegitDir, "tmp")

	// Generate 4 random bytes -> 8 hex chars
	var rndBytes [4]byte
	if _, err := rand.Read(rndBytes[:]); err != nil {
		return nil, fmt.Errorf("generating random suffix: %w", err)
	}
	rnd := hex.EncodeToString(rndBytes[:])

	dirName := fmt.Sprintf("%d-%s", os.Getpid(), rnd)
	dir := filepath.Join(tmpBase, dirName)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating tmp index dir: %w", err)
	}

	indexPath := filepath.Join(dir, "index")

	// Seed from the given treeish via git read-tree
	if err := git.ReadTree(ctx, indexPath, treeish); err != nil {
		// Clean up on failure
		os.RemoveAll(dir)
		return nil, fmt.Errorf("seeding index from %s: %w", treeish, err)
	}

	return &TmpIndex{Dir: dir, IndexPath: indexPath}, nil
}

// NewEmpty creates a temporary index directory with an empty index (no tree).
// Used for root commits in repos with no prior commits.
func NewEmpty(safegitDir string) (*TmpIndex, error) {
	tmpBase := filepath.Join(safegitDir, "tmp")

	var rndBytes [4]byte
	if _, err := rand.Read(rndBytes[:]); err != nil {
		return nil, fmt.Errorf("generating random suffix: %w", err)
	}
	rnd := hex.EncodeToString(rndBytes[:])

	dirName := fmt.Sprintf("%d-%s", os.Getpid(), rnd)
	dir := filepath.Join(tmpBase, dirName)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating tmp index dir: %w", err)
	}

	indexPath := filepath.Join(dir, "index")
	// Empty index: just create the dir, git add will initialize the index file
	return &TmpIndex{Dir: dir, IndexPath: indexPath}, nil
}

// Cleanup removes the temporary index directory.
func (t *TmpIndex) Cleanup() error {
	return os.RemoveAll(t.Dir)
}

// GarbageCollect removes tmp directories whose owning PID is no longer alive.
// Returns the count of directories removed.
func GarbageCollect(safegitDir string) (removed int, err error) {
	tmpBase := filepath.Join(safegitDir, "tmp")

	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading tmp dir: %w", err)
	}

	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pid, ok := parsePIDFromDirName(entry.Name())
		if !ok {
			continue
		}

		if !processAlive(pid) {
			dirPath := filepath.Join(tmpBase, entry.Name())
			if rmErr := os.RemoveAll(dirPath); rmErr != nil {
				errs = append(errs, fmt.Errorf("removing %s: %w", dirPath, rmErr))
				continue
			}
			removed++
		}
	}

	if len(errs) > 0 {
		return removed, errors.Join(errs...)
	}
	return removed, nil
}

// GarbageCollectDryRun reports orphan tmp directories without removing them.
// Returns the directory names that would be cleaned.
func GarbageCollectDryRun(safegitDir string) ([]string, error) {
	tmpBase := filepath.Join(safegitDir, "tmp")

	entries, err := os.ReadDir(tmpBase)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading tmp dir: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, ok := parsePIDFromDirName(entry.Name())
		if !ok {
			continue
		}
		if !processAlive(pid) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
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
