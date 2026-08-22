//go:build !windows

package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The oplog's integrity rests on this package: safegit's log lines have no size
// limit, so POSIX O_APPEND atomicity guarantees nothing about them and the
// advisory lock is the only thing standing between two concurrent appends and
// an interleaved, unparseable line. Everything below tests that lock directly
// rather than through a caller, because a lock that silently stopped locking
// would still let every caller's happy path pass.

// openLockTarget opens path the way LockedAppend does, so a test can drive
// lockFile/unlockFile against the same kind of descriptor the real code uses.
func openLockTarget(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// A first append has to create the file; mkdirAll additionally has to create
// the parent directory, which is what separates the two callers (the rewrite
// journal writes into a directory that may not exist yet, the oplog does not).
func TestLockedAppendCreatesFileAndParent(t *testing.T) {
	root := t.TempDir()

	flat := filepath.Join(root, "log.jsonl")
	if err := LockedAppend(flat, []byte("first\n"), false); err != nil {
		t.Fatalf("LockedAppend into an existing directory: %v", err)
	}
	if got := readFile(t, flat); got != "first\n" {
		t.Fatalf("file content = %q, want %q", got, "first\n")
	}

	nested := filepath.Join(root, "a", "b", "log.jsonl")
	if err := LockedAppend(nested, []byte("nested\n"), true); err != nil {
		t.Fatalf("LockedAppend with mkdirAll: %v", err)
	}
	if got := readFile(t, nested); got != "nested\n" {
		t.Fatalf("nested file content = %q, want %q", got, "nested\n")
	}

	// Without mkdirAll a missing parent is an error, not a silent creation.
	missing := filepath.Join(root, "c", "log.jsonl")
	if err := LockedAppend(missing, []byte("x\n"), false); err == nil {
		t.Fatal("LockedAppend into a missing directory succeeded; want an error")
	}
	if _, err := os.Stat(filepath.Dir(missing)); !os.IsNotExist(err) {
		t.Fatalf("the missing parent directory was created anyway (stat err = %v)", err)
	}
}

// Successive appends extend the file rather than truncating it, and the lock is
// released between calls -- a lock left held would deadlock the second call.
func TestLockedAppendAppendsAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	for i := 0; i < 3; i++ {
		if err := LockedAppend(path, []byte(fmt.Sprintf("line %d\n", i)), false); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	want := "line 0\nline 1\nline 2\n"
	if got := readFile(t, path); got != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}

// The exclusion itself: while one open file description holds the lock, a
// second one cannot take it, and the moment the first releases, the second
// acquires. Both halves are asserted, because a lock that never blocks and a
// lock that never releases both break the log in different ways.
func TestExclusiveLockBlocksSecondLockerUntilUnlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")
	first := openLockTarget(t, path)
	second := openLockTarget(t, path)

	if err := lockFile(first); err != nil {
		t.Fatalf("locking the first descriptor: %v", err)
	}

	acquired := make(chan error, 1)
	go func() { acquired <- lockFile(second) }()

	select {
	case err := <-acquired:
		t.Fatalf("the second locker acquired the lock while the first held it (err = %v)", err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := unlockFile(first); err != nil {
		t.Fatalf("unlocking the first descriptor: %v", err)
	}

	select {
	case err := <-acquired:
		if err != nil {
			t.Fatalf("the second locker failed after the first released: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second locker never acquired the lock after the first released it")
	}
	if err := unlockFile(second); err != nil {
		t.Fatalf("unlocking the second descriptor: %v", err)
	}
}

// Contenders serialize: no two goroutines are ever inside the locked region at
// the same time. The witness is a counter of how many holders are in the region
// at once -- it is incremented under the lock, held there long enough for a
// racing goroutine to be observed, and its high-water mark must be exactly 1.
func TestConcurrentLockersSerialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")

	var inRegion atomic.Int32
	var highWater atomic.Int32
	var wg sync.WaitGroup

	const contenders = 8
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
			if err != nil {
				t.Errorf("opening the lock target: %v", err)
				return
			}
			defer f.Close()
			if err := lockFile(f); err != nil {
				t.Errorf("locking: %v", err)
				return
			}
			now := inRegion.Add(1)
			for {
				max := highWater.Load()
				if now <= max || highWater.CompareAndSwap(max, now) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			inRegion.Add(-1)
			if err := unlockFile(f); err != nil {
				t.Errorf("unlocking: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := highWater.Load(); got != 1 {
		t.Fatalf("%d holders were inside the locked region at once; want 1", got)
	}
}

// The property the callers actually depend on: concurrent LockedAppend calls
// produce whole lines. Each contender writes a line far longer than any
// atomic-append guarantee would cover, so an unlocked append would interleave
// and the reassembled file would carry a line built from two writers.
func TestConcurrentLockedAppendKeepsLinesIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.jsonl")

	const contenders = 16
	const payload = 64 * 1024

	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			line := fmt.Sprintf("%02d", i) + strings.Repeat(fmt.Sprintf("%d", i%10), payload) + "\n"
			if err := LockedAppend(path, []byte(line), false); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSuffix(readFile(t, path), "\n"), "\n")
	if len(lines) != contenders {
		t.Fatalf("file holds %d lines, want %d -- appends interleaved", len(lines), contenders)
	}
	seen := make(map[string]bool, contenders)
	for _, line := range lines {
		if len(line) != 2+payload {
			t.Fatalf("a line is %d bytes, want %d -- it was written by more than one appender", len(line), 2+payload)
		}
		id := line[:2]
		if seen[id] {
			t.Fatalf("line id %s appears twice", id)
		}
		seen[id] = true
		body := line[2:]
		if strings.Trim(body, string(body[0])) != "" {
			t.Fatalf("line id %s carries mixed content -- two appenders wrote into it", id)
		}
	}
	if len(seen) != contenders {
		t.Fatalf("%d distinct lines survived, want %d", len(seen), contenders)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
