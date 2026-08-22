package lock

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A publication temporary sibling must never be classified as a lock, and a
// lock must never be classified as a temp. Everything that walks a locks/
// subtree -- doctor's diagnose scan, doctor's cleanup -- keys off these two
// predicates, so the two name shapes have to stay disjoint by construction.
func TestLockAndPublicationTempNamesAreDisjoint(t *testing.T) {
	for _, name := range []string{"main.lock", "safegit-operation.lock", "v1.0.lock"} {
		if !IsLockFile(name) {
			t.Errorf("IsLockFile(%q) = false, want true", name)
		}
		if IsPublicationTemp(name) {
			t.Errorf("IsPublicationTemp(%q) = true, want false", name)
		}
	}
	for _, name := range []string{".main.lock.tmp-123456", ".operation.lock.tmp-0"} {
		if IsLockFile(name) {
			t.Errorf("IsLockFile(%q) = true, want false", name)
		}
		if !IsPublicationTemp(name) {
			t.Errorf("IsPublicationTemp(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"main", "notes.txt", ".hidden"} {
		if IsLockFile(name) || IsPublicationTemp(name) {
			t.Errorf("%q classified as a lock or a temp", name)
		}
	}
}

// The predicate must agree with what tryCreate actually writes, not with a
// second guess at its spelling. This creates a real temp through the real
// publication path and checks the name the filesystem ends up holding.
func TestPublicationTempNameMatchesTheWriter(t *testing.T) {
	dir := t.TempDir()
	lockFile := filepath.Join(dir, "main.lock")

	f, err := os.CreateTemp(dir, publicationTempPrefix(filepath.Base(lockFile)))
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(f.Name())
	f.Close()

	if !IsPublicationTemp(name) {
		t.Errorf("the writer produced %q, which IsPublicationTemp does not recognize", name)
	}
	if IsLockFile(name) {
		t.Errorf("the writer produced %q, which IsLockFile mistakes for a lock", name)
	}
	if !strings.HasPrefix(name, ".") {
		t.Errorf("publication temp %q is not hidden", name)
	}
}

// Path and NameFromPath are inverses over every name safegit locks under,
// including the two tool-owned pseudo-refs.
func TestLockPathRoundTripsThroughNameFromPath(t *testing.T) {
	base := t.TempDir()
	for _, ref := range []string{"refs/heads/main", "refs/heads/feature", "refs/tags/v1", RewriteRef, OperationRef} {
		p := Path(base, ref)
		if got := NameFromPath(base, p); got != ref {
			t.Errorf("NameFromPath(Path(%q)) = %q, want %q", ref, got, ref)
		}
	}
	if got := NameFromPath(base, filepath.Join(base, "locks", "refs", "heads", ".main.lock.tmp-1")); got != "" {
		t.Errorf("NameFromPath on a publication temp = %q, want \"\"", got)
	}
	if got := NameFromPath(base, filepath.Join(base, "elsewhere", "main.lock")); got != "" {
		t.Errorf("NameFromPath outside the locks subtree = %q, want \"\"", got)
	}
}

// ReclaimIfStale removes a dead holder's lock and leaves a live holder's lock
// alone. It is the authority doctor's cleanup goes through.
func TestReclaimIfStaleRemovesOnlyDeadHolders(t *testing.T) {
	base := t.TempDir()

	dead := Path(base, "refs/heads/dead")
	if err := os.MkdirAll(filepath.Dir(dead), 0755); err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	if err := os.WriteFile(dead, []byte("pid=999999999\nop=commit\nhost="+host+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !ReclaimIfStale(dead) {
		t.Error("ReclaimIfStale did not reclaim a lock whose holder is gone")
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the stale lock survived reclamation (err=%v)", err)
	}

	live, err := Acquire(base, base, "refs/heads/live", "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Release()
	if ReclaimIfStale(live.LockPath) {
		t.Error("ReclaimIfStale removed a lock this very process holds")
	}
	if _, err := os.Stat(live.LockPath); err != nil {
		t.Errorf("the live lock was removed: %v", err)
	}
}

// ReleasePending covers the exit paths that never unwind: after it runs, no
// lock this process took is still on disk.
func TestReleasePendingRemovesHeldLocks(t *testing.T) {
	base := t.TempDir()

	first, err := Acquire(base, base, "refs/heads/one", "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(base, base, OperationRef, "test", time.Second)
	if err != nil {
		t.Fatal(err)
	}

	ReleasePending()

	for _, lk := range []*RefLock{first, second} {
		if _, err := os.Stat(lk.LockPath); !os.IsNotExist(err) {
			t.Errorf("%s survived ReleasePending (err=%v)", lk.Ref, err)
		}
	}
	// A second call is a no-op rather than an error, and a later Release on an
	// already-removed lock must not panic.
	ReleasePending()
	_ = first.Release()
}
