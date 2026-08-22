package lock

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A lock that changes hands quickly must not starve its waiters.
//
// Each waiter's poll interval escalates to the one-second cap within six
// polls. If it stayed there, a lock held for a few milliseconds at a time would
// sit idle for most of every second and the queue would drain at roughly one
// waiter per second no matter how many were waiting -- which is what made a
// worktree-wide commit lock time out under a few dozen concurrent commits while
// the actual work took under two seconds.
//
// Twenty contenders, each holding for 20ms, is 0.4s of work. The bound below is
// an order of magnitude above that and an order of magnitude below the ~20s the
// un-reset backoff produces, so it separates the two behaviors without being
// sensitive to machine speed.
func TestBusyLockDoesNotStarveWaiters(t *testing.T) {
	base := t.TempDir()
	const contenders = 20
	const hold = 20 * time.Millisecond

	var wg sync.WaitGroup
	errs := make([]error, contenders)
	start := time.Now()

	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lk, err := Acquire(base, base, "refs/heads/busy", "test", 60*time.Second)
			if err != nil {
				errs[i] = err
				return
			}
			time.Sleep(hold)
			errs[i] = lk.Release()
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, err := range errs {
		if err != nil {
			t.Errorf("contender %d: %v", i, err)
		}
	}
	if limit := 5 * time.Second; elapsed > limit {
		t.Errorf("%d contenders holding %v each took %v (limit %v): the backoff is not "+
			"resetting when the lock changes hands, so waiters poll once a second on a lock "+
			"that is free most of the time", contenders, hold, elapsed, limit)
	}
	if _, err := os.Stat(Path(base, "refs/heads/busy")); !os.IsNotExist(err) {
		t.Errorf("the lock file survived every contender releasing it (err=%v)", err)
	}
}

// The identity comparison decides when the backoff resets, so its edges are
// pinned directly: the first observation is not a change, a republished lock is,
// and a lock that vanished is.
func TestHolderIdentityChangeDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.lock")
	if err := os.WriteFile(path, []byte("pid=1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	first := readHolderIdentity(path)
	if !first.known {
		t.Fatal("an existing lock file yielded an unknown identity")
	}
	if first.changedFrom(holderIdentity{}) != true {
		t.Error("the first observation of a present lock must count as a change")
	}
	if readHolderIdentity(path).changedFrom(first) {
		t.Error("an unchanged lock file was reported as a different holder")
	}

	// A new holder always publishes a new file, which is a new inode.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if readHolderIdentity(path).changedFrom(first) != true {
		t.Error("a lock that vanished was not reported as a change")
	}
	if err := os.WriteFile(path, []byte("pid=2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !readHolderIdentity(path).changedFrom(first) {
		t.Error("a republished lock file was not reported as a different holder")
	}
}
