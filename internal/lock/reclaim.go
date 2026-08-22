package lock

import (
	"errors"
	"os"
	"syscall"
)

// reclaimOutcome is what a reclamation attempt concluded about a lock file
// whose holder looked stale.
type reclaimOutcome int

const (
	// reclaimNone: nothing was removed and nothing about the situation has
	// changed. Either the lock turned out not to be stale after all, or another
	// contender is mid-reclaim on this very file. Wait and try again.
	reclaimNone reclaimOutcome = iota

	// reclaimRestart: nothing was removed, but the file at the path is gone or
	// is no longer the file we opened -- a contender reclaimed it first. Re-run
	// the acquire attempt immediately; the atomic create settles the rest.
	reclaimRestart

	// reclaimDone: a stale lock was removed while its flock was held. The path
	// is free for whoever wins the next create.
	reclaimDone
)

// openForReclaim opens the lock file at path and takes an exclusive,
// non-blocking flock on it. A non-nil file means the caller holds that flock
// and must hand the file to reclaimLocked, which closes it. A nil file means no
// attempt is possible right now and the returned outcome says what to do
// instead.
//
// The flock is what serializes reclamation between contenders: without it, two
// processes that both judged the same lock stale would both remove it, and the
// second removal would delete the fresh lock the first one had already created.
//
// Any failure other than "the file is gone" or "someone else holds the flock"
// -- a permission problem, a filesystem without flock -- yields reclaimNone, so
// an unreclaimable lock is waited on and eventually times out rather than being
// removed on a guess.
func openForReclaim(path string) (*os.File, reclaimOutcome) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Already reclaimed by someone else; race for creation instead.
			return nil, reclaimRestart
		}
		return nil, reclaimNone
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			// Another contender is reclaiming this same file right now. It will
			// finish; waiting is cheaper and safer than racing it.
			return nil, reclaimNone
		}
		return nil, reclaimNone
	}
	return f, reclaimNone
}

// ReclaimIfStale removes the lock file at path if, and only if, its holder is
// genuinely gone -- the same judgement Acquire makes, under the same flock and
// the same inode identity re-check. It reports whether the file was removed.
//
// It is the authority for every unattended removal of a lock nobody asked about
// by name: `safegit doctor --action fix` sweeps the locks subtree through it
// rather than judging staleness and then calling os.Remove, because between
// those two steps another process can reclaim the same stale lock and publish
// its own live one at that path, and the bare remove would delete THAT.
//
// A false result is never a verdict that the lock is live: it also covers "the
// path is already gone", "another contender holds the flock right now" and "this
// filesystem cannot flock". All of those mean leave it alone and look again
// later, which is the fail-closed direction.
func ReclaimIfStale(path string) bool {
	if !IsStale(path) {
		return false
	}
	f, _ := openForReclaim(path)
	if f == nil {
		return false
	}
	outcome, _ := reclaimLocked(f, path)
	return outcome == reclaimDone
}

// reclaimLocked finishes a reclamation attempt on a lock file the caller has
// already opened and flocked with openForReclaim, and closes it (which releases
// the flock). It returns the outcome and, on reclaimDone, the pid of the stale
// holder whose lock was removed.
//
// Holding the flock is not by itself enough. The file we hold may have been
// unlinked already by a contender that reclaimed the lock first and created a
// fresh, live one at the same path -- our flock then guards a detached inode
// nobody else can see, and removing the path would delete that contender's live
// lock. Two checks close that window:
//
//   - the path is re-stat'd and compared against the open descriptor's own
//     inode, so removal only ever happens when the path still names the exact
//     file we hold; and
//   - staleness is re-judged from the descriptor's contents, never from a fresh
//     read of the path.
//
// Together with the flock those make the removal safe: any other reclaimer is
// either blocked on our flock -- and will fail the identity check itself once it
// gets in, because by then we have unlinked the file it opened -- or already
// sees the path gone.
func reclaimLocked(f *os.File, path string) (reclaimOutcome, int) {
	defer f.Close()

	held, err := f.Stat()
	if err != nil {
		return reclaimRestart, 0
	}
	atPath, err := os.Stat(path)
	if err != nil || !os.SameFile(held, atPath) {
		// The path no longer names the file we hold: someone reclaimed it
		// first. Removing now would delete their lock, not the stale one.
		return reclaimRestart, 0
	}

	stale, pid := staleFile(f, path)
	if !stale {
		return reclaimNone, 0
	}
	if err := os.Remove(path); err != nil {
		// Could not remove it -- wait it out rather than spinning.
		return reclaimNone, 0
	}
	return reclaimDone, pid
}
