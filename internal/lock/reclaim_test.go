package lock

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smm-h/safegit/internal/procutil"
	"github.com/smm-h/safegit/internal/testutil"
)

// deadPID is a pid no process on the machine can plausibly hold, which is what
// makes a planted lock genuinely stale.
const deadPID = 999999999

// TestReclaimLockedAbortsWhenPathReplaced is the authoritative regression pin
// for the stale-reclamation lock steal.
//
// Two contenders can both judge the same crashed holder's lock stale in the
// same instant. The first removes it and creates its own, live lock at the same
// path. The second must NOT then remove what it finds there: that file is the
// winner's live lock, not the stale one it judged, and deleting it leaves two
// processes each believing they hold the ref. The test reproduces exactly that
// interleaving deterministically, by performing the winner's remove-and-recreate
// between the loser's open and the loser's removal step.
func TestReclaimLockedAbortsWhenPathReplaced(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "main.lock")

	// The crashed holder's lock.
	plantLock(t, lp, deadPID, "")

	// The losing contender opens and flocks it, having judged it stale.
	loser, outcome := openForReclaim(lp)
	if loser == nil {
		t.Fatalf("openForReclaim declined a stale lock (outcome %v)", outcome)
	}

	// The winning contender gets there first: it removes the stale lock and
	// writes its own, held by a live process.
	if err := os.Remove(lp); err != nil {
		t.Fatalf("removing the stale lock: %v", err)
	}
	winner := testutil.SpawnSleeper(t)
	plantLock(t, lp, winner.Pid, trueStart(t, winner.Pid))
	fresh, err := os.ReadFile(lp)
	if err != nil {
		t.Fatalf("reading the winner's lock file: %v", err)
	}

	// Only now does the loser reach its removal step.
	got, pid := reclaimLocked(loser, lp)
	if got != reclaimRestart {
		t.Errorf("outcome = %d, want reclaimRestart (%d) -- the loser acted on a file it no longer held", got, reclaimRestart)
	}
	if pid != 0 {
		t.Errorf("reclaimed pid = %d, want 0 -- nothing was reclaimed", pid)
	}

	after, err := os.ReadFile(lp)
	if err != nil {
		t.Fatalf("the winner's live lock file was deleted by the losing reclaimer: %v", err)
	}
	if !bytes.Equal(fresh, after) {
		t.Errorf("the winner's lock file changed under it:\n got %q\nwant %q", after, fresh)
	}
}

// TestReclaimLockedRemovesStaleLock is the other half of the contract: when the
// path still names the file the reclaimer holds and that file is stale, it goes.
func TestReclaimLockedRemovesStaleLock(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, deadPID, "")

	f, outcome := openForReclaim(lp)
	if f == nil {
		t.Fatalf("openForReclaim declined a stale lock (outcome %v)", outcome)
	}
	got, pid := reclaimLocked(f, lp)
	if got != reclaimDone {
		t.Errorf("outcome = %d, want reclaimDone (%d)", got, reclaimDone)
	}
	if pid != deadPID {
		t.Errorf("reclaimed pid = %d, want %d -- the oplog record would name the wrong holder", pid, deadPID)
	}
	if _, err := os.Stat(lp); !os.IsNotExist(err) {
		t.Errorf("stale lock file survived reclamation: %v", err)
	}
}

// TestReclaimLockedLeavesLiveLock re-checks staleness through the descriptor:
// a live holder's lock is never removed, even by a reclaimer that got as far as
// taking its flock.
func TestReclaimLockedLeavesLiveLock(t *testing.T) {
	holder := testutil.SpawnSleeper(t)
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, holder.Pid, trueStart(t, holder.Pid))

	f, outcome := openForReclaim(lp)
	if f == nil {
		t.Fatalf("openForReclaim declined to open a lock file (outcome %v)", outcome)
	}
	got, _ := reclaimLocked(f, lp)
	if got != reclaimNone {
		t.Errorf("outcome = %d, want reclaimNone (%d)", got, reclaimNone)
	}
	if _, err := os.Stat(lp); err != nil {
		t.Errorf("live holder's lock file was removed: %v", err)
	}
}

// TestOpenForReclaimVanishedLock covers the lock that is already gone by the
// time the reclaimer looks: nothing to reclaim, just retry the create.
func TestOpenForReclaimVanishedLock(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "main.lock")

	f, outcome := openForReclaim(lp)
	if f != nil {
		f.Close()
		t.Fatal("openForReclaim returned a file for a path that does not exist")
	}
	if outcome != reclaimRestart {
		t.Errorf("outcome = %d, want reclaimRestart (%d)", outcome, reclaimRestart)
	}
}

// TestOpenForReclaimYieldsToAnotherReclaimer pins the serialization: while one
// reclaimer holds the lock file's flock, a second gets no attempt at all and is
// told to wait rather than to race.
func TestOpenForReclaimYieldsToAnotherReclaimer(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "main.lock")
	plantLock(t, lp, deadPID, "")

	first, outcome := openForReclaim(lp)
	if first == nil {
		t.Fatalf("openForReclaim declined a stale lock (outcome %v)", outcome)
	}
	defer first.Close()

	second, outcome := openForReclaim(lp)
	if second != nil {
		second.Close()
		t.Fatal("two reclaimers held the lock file's flock at once")
	}
	if outcome != reclaimNone {
		t.Errorf("outcome = %d, want reclaimNone (%d)", outcome, reclaimNone)
	}
	if _, err := os.Stat(lp); err != nil {
		t.Errorf("the blocked reclaimer removed the lock file anyway: %v", err)
	}
}

// TestTryCreateReportsExistAsErrExist pins the error contract Acquire reads:
// the publish step must report an already-held lock as os.ErrExist, or every
// contended acquire would abort instead of waiting.
func TestTryCreateReportsExistAsErrExist(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "main.lock")
	if err := tryCreate(lp, "commit"); err != nil {
		t.Fatalf("tryCreate: %v", err)
	}
	err := tryCreate(lp, "commit")
	if err == nil {
		t.Fatal("tryCreate overwrote an existing lock file")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("err = %v, which does not match os.ErrExist -- Acquire would abort instead of waiting", err)
	}
}

// TestTryCreateLeavesNoTemporaryFiles keeps the staging file from becoming
// litter in the locks tree, whether the publish succeeded or lost the race.
func TestTryCreateLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	lp := filepath.Join(dir, "main.lock")

	if err := tryCreate(lp, "commit"); err != nil {
		t.Fatalf("tryCreate: %v", err)
	}
	if err := tryCreate(lp, "commit"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second tryCreate: err = %v, want os.ErrExist", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading lock dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "main.lock" {
		t.Errorf("lock dir holds %v, want exactly [main.lock]", names)
	}
}

// TestTryCreatePublishesCompleteLockFile pins the atomicity the reclamation
// rules depend on: a lock file that exists at all must already name its holder.
// A reader that catches a lock file between its creation and its first write
// finds no pid= line, concludes a holder crashed mid-create, and reclaims a lock
// whose owner is alive -- so that state must never be observable. Creating the
// file in place and writing afterwards makes it observable on every single
// acquire.
//
// The reader races the creator, so this is a detector rather than a proof; it
// reports within a few iterations against an in-place create-then-write.
func TestTryCreatePublishesCompleteLockFile(t *testing.T) {
	lp := filepath.Join(t.TempDir(), "main.lock")

	var (
		mu       sync.Mutex
		observed []byte
		found    bool
	)
	done := make(chan struct{})
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			raw, err := os.ReadFile(lp)
			if err != nil {
				continue // absent is a fine thing to observe
			}
			if len(raw) == 0 || !bytes.Contains(raw, []byte("pid=")) {
				mu.Lock()
				if !found {
					found, observed = true, raw
				}
				mu.Unlock()
				return
			}
		}
	}()

	for i := 0; i < 1000; i++ {
		if err := tryCreate(lp, "commit"); err != nil {
			close(done)
			reader.Wait()
			t.Fatalf("tryCreate: %v", err)
		}
		if err := os.Remove(lp); err != nil {
			close(done)
			reader.Wait()
			t.Fatalf("removing lock file: %v", err)
		}
	}
	close(done)
	reader.Wait()

	mu.Lock()
	defer mu.Unlock()
	if found {
		t.Errorf("a lock file with no holder was observable at %s: %q -- a contender would reclaim a live lock", lp, observed)
	}
}

// TestAcquireContendedStaleReclamationKeepsExclusion is the end-to-end form:
// several contenders meet one genuinely stale lock at the same instant, so they
// all judge it reclaimable together -- the interleaving the reclamation flock
// exists to serialize. Every holder re-reads its own lock file the moment it
// acquires, so a losing contender that deleted or overwrote the winner's file
// is reported by the winner.
//
// Whether the bad interleaving actually occurs is up to the scheduler, so this
// is a best-effort detector, not a proof.
// TestReclaimLockedAbortsWhenPathReplaced is the authoritative regression pin.
func TestAcquireContendedStaleReclamationKeepsExclusion(t *testing.T) {
	const (
		rounds     = 6
		contenders = 6
		ref        = "refs/heads/main"
	)

	// The identity every holder's own lock file must carry. Left empty on a
	// platform that cannot report start times, where the field is absent by
	// design and there is nothing to compare.
	wantPID := strconv.Itoa(os.Getpid())
	wantStart := ""
	if s, err := procutil.StartTime(os.Getpid()); err == nil {
		wantStart = strconv.FormatUint(s.Ticks, 10)
	}

	var (
		mu       sync.Mutex
		problems []string
		held     atomic.Int32
		maxHeld  atomic.Int32
	)
	report := func(format string, args ...interface{}) {
		mu.Lock()
		problems = append(problems, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	for round := 0; round < rounds; round++ {
		sgDir := setupSafegitDir(t)
		lp := lockPath(sgDir, ref)
		if err := os.MkdirAll(filepath.Dir(lp), 0755); err != nil {
			t.Fatalf("creating lock dir: %v", err)
		}
		// One crashed holder's lock, in front of everyone at once.
		plantLock(t, lp, deadPID, "")

		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				op := fmt.Sprintf("commit-%d-%d", round, i)
				<-start

				lk, err := Acquire(sgDir, sgDir, ref, op, 3*time.Second)
				if err != nil {
					return // losing the race and timing out is a legal outcome
				}
				n := held.Add(1)
				for {
					m := maxHeld.Load()
					if n <= m || maxHeld.CompareAndSwap(m, n) {
						break
					}
				}

				fields, ferr := parseLockFields(lk.LockPath)
				switch {
				case ferr != nil:
					report("holder %s found its own lock file unreadable: %v", op, ferr)
				case fields["op"] != op:
					report("holder %s found op=%q in its own lock file -- another contender replaced it", op, fields["op"])
				case fields["pid"] != wantPID:
					report("holder %s found pid=%q in its own lock file, want %q", op, fields["pid"], wantPID)
				case wantStart != "" && fields["start"] != wantStart:
					report("holder %s found start=%q in its own lock file, want %q", op, fields["start"], wantStart)
				}

				time.Sleep(time.Millisecond)
				// Decrement before releasing: the reverse order would let the
				// next acquirer's increment overlap ours and invent a
				// violation. This way the counter can only under-report.
				held.Add(-1)
				lk.Release()
			}(i)
		}
		close(start)
		wg.Wait()

		if _, err := os.Stat(lp); !os.IsNotExist(err) {
			report("round %d ended with a lock file still present: %v", round, err)
		}
	}

	if n := maxHeld.Load(); n > 1 {
		report("%d contenders held the same ref at once", n)
	}
	for _, p := range problems {
		t.Error(p)
	}
}
