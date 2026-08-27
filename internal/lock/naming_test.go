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
	if !ReclaimIfStale(dead, os.Remove) {
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
	if ReclaimIfStale(live.LockPath, os.Remove) {
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

// A holder releases the lock IT published, never whatever happens to sit at
// the path by then.
//
// The situation is reachable: `safegit unlock <ref>` force-releases
// unconditionally (it is the recovery path of last resort, and must work even
// where flock does not), a third process then wins the freed path, and the
// original holder finally finishes. Removing by path alone would delete the
// newcomer's live lock and let two operations mutate the ref at once.
//
// Both removal paths are covered, because both used to remove by path: the
// holder's own Release, and the ReleasePending sweep that die() runs on the
// exit paths that never unwind.
func TestReleaseLeavesAReplacedLockAlone(t *testing.T) {
	// replaceOutOfBand force-releases a held lock and publishes a different
	// file at the same path -- the operator-forced unlock plus a third party's
	// fresh lock, compressed.
	replaceOutOfBand := func(t *testing.T, base, ref string, held *RefLock) os.FileInfo {
		t.Helper()
		if err := ForceRelease(base, ref, os.Remove); err != nil {
			t.Fatalf("force-releasing the held lock: %v", err)
		}
		newcomer, err := tryCreate(held.LockPath, "newcomer")
		if err != nil {
			t.Fatalf("publishing the newcomer's lock: %v", err)
		}
		return newcomer.info
	}

	assertNewcomerSurvives := func(t *testing.T, path string, newcomer os.FileInfo) {
		t.Helper()
		current, err := os.Stat(path)
		if err != nil {
			t.Fatalf("the newcomer's lock was removed by the previous holder: %v", err)
		}
		if !os.SameFile(newcomer, current) {
			t.Error("the file at the lock path is no longer the newcomer's lock")
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("cleaning up the newcomer's lock: %v", err)
		}
	}

	// The stat half of the identity is not proof on its own: a filesystem may
	// hand the freed inode number straight back to the newcomer, and then
	// os.SameFile answers "ours" about somebody else's live lock. Whether that
	// happens is the filesystem's choice -- ext4 reuses, btrfs never does -- so
	// the sibling test above can only exercise this hazard where the machine
	// happens to reuse, and passed for a whole campaign on a machine that does
	// not while failing the moment CI ran it on one that does.
	//
	// This test removes the luck: it hands releaseIfOurs a publication whose
	// stat identity IS the newcomer's file -- exactly what inode reuse produces
	// -- while the record is the one the previous holder wrote. The record is
	// what must decide.
	t.Run("ReleaseUnderInodeReuse", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "contended"+lockSuffix)

		holder, err := tryCreate(path, "holder")
		if err != nil {
			t.Fatalf("publishing the holder's lock: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("force-releasing the holder's lock: %v", err)
		}
		newcomer, err := tryCreate(path, "newcomer")
		if err != nil {
			t.Fatalf("publishing the newcomer's lock: %v", err)
		}

		// The publication a holder would carry on a filesystem that recycled
		// the inode: the newcomer's stat, the holder's record.
		reused := publication{info: newcomer.info, record: holder.record}
		if err := releaseIfOurs(path, reused); err != nil {
			t.Errorf("releasing a lock that was force-released out from under it: %v", err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the newcomer's lock was removed on a recycled inode: %v", err)
		}

		// The control: the holder's own publication still releases its own lock.
		if err := releaseIfOurs(path, newcomer); err != nil {
			t.Errorf("releasing the newcomer's own lock: %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("a holder failed to release its own lock (err=%v)", err)
		}
	})

	t.Run("Release", func(t *testing.T) {
		base := t.TempDir()
		const ref = "refs/heads/contended"
		held, err := Acquire(base, base, ref, "test", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		newcomer := replaceOutOfBand(t, base, ref, held)

		if err := held.Release(); err != nil {
			t.Errorf("Release reported an error for a lock that was force-released out from under it: %v", err)
		}
		assertNewcomerSurvives(t, held.LockPath, newcomer)
	})

	t.Run("ReleasePending", func(t *testing.T) {
		base := t.TempDir()
		const ref = "refs/heads/contended"
		held, err := Acquire(base, base, ref, "test", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		// A second, untouched lock proves the sweep still does its job: it
		// must remove this one while leaving the newcomer alone.
		own, err := Acquire(base, base, OperationRef, "test", time.Second)
		if err != nil {
			t.Fatal(err)
		}
		newcomer := replaceOutOfBand(t, base, ref, held)

		ReleasePending()

		if _, err := os.Stat(own.LockPath); !os.IsNotExist(err) {
			t.Errorf("ReleasePending did not remove this process's own lock (err=%v)", err)
		}
		assertNewcomerSurvives(t, held.LockPath, newcomer)
	})
}
