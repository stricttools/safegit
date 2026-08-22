package test

import (
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// These tests are about the two things safegit does BETWEEN attempts: reading
// the remote, and re-reading it before a retry. Both are observations, and an
// observation that fails must say so rather than turn into a value.

// TestPushRefusesWhenTheRemoteCannotBeObserved pins the difference between "the
// remote does not have this ref" and "safegit could not find out".
//
// getRemoteSHA used to answer both with the null marker. Since the lease is
// pinned to whatever that answer is, a transient ls-remote failure became the
// EMPTY expectation -- git's spelling for "this ref must not exist yet" -- so a
// push to a ref that plainly does exist was refused as a stale lease, exit 41,
// with a message blaming a concurrent pusher who was never there.
//
// The shim fails ls-remote and nothing else, so the only thing wrong with the
// run is the observation.
func TestPushRefusesWhenTheRemoteCannotBeObserved(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "a", "a.txt")
	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin"); code != 0 {
		t.Fatalf("seeding the remote failed (code %d): %s", code, stderr)
	}

	testutil.WriteFile(t, dir, "b.txt", "b\n")
	safegitCommit(t, dir, "b", "b.txt")

	env, lsRemotes := gitShim(t, "ls-remote",
		`echo "fatal: unable to access 'origin': Could not resolve host: unreachable.invalid" >&2; exit 128`)

	_, stderr, code := runSafegitEnv(t, dir, env, "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")

	if code == exitcode.PushLeaseRejected {
		t.Fatalf("a failed observation was reported as a lease rejection (exit %d); it is not a verdict about the remote, it is not knowing one: %s",
			code, stderr)
	}
	if code != exitcode.PushFailed {
		t.Errorf("a remote that could not be read must exit %d (PushFailed), got %d; stderr: %s",
			exitcode.PushFailed, code, stderr)
	}
	if !strings.Contains(stderr, "ls-remote") && !strings.Contains(stderr, "reading") {
		t.Errorf("the refusal must name the failed observation rather than blame the remote's state; stderr: %s", stderr)
	}
	// git's own stderr is carried by the error the git call returns, so a
	// second copy appended on top of it printed the same sentence twice in one
	// message and read like two separate failures.
	if n := strings.Count(stderr, "unreachable.invalid"); n != 1 {
		t.Errorf("git's stderr appears %d times in the refusal, want once:\n%s", n, stderr)
	}
	if n := len(lsRemotes()); n == 0 {
		t.Error("the shim never intercepted an ls-remote, so the test proved nothing")
	}
}

// TestPushRetriesATransportFailureAndRePinsTheLease pins the retry loop itself.
//
// The loop was dead for the whole of safegit's life before the classification
// was moved onto git's own stderr: isTransportError was handed the effects
// handle's formatted error, which carries the argv and the exit code and none
// of the child's output, so no pattern could ever match and every failure broke
// out on the first attempt. Nothing in the suite noticed, because nothing
// exercised it -- a regression back to the dead loop would stay green.
//
// It also pins the harder half: the second attempt must carry a lease pinned to
// a FRESH observation. An expectation describes the remote at a moment; a retry
// that resent the first attempt's expectation would refuse a ref that is now
// fine, or assert a state safegit never saw.
func TestPushRetriesATransportFailureAndRePinsTheLease(t *testing.T) {
	dir, remoteDir, remoteBase := divergedFromRemote(t)
	remoteBefore := testutil.Rev(t, remoteDir, "refs/heads/main")
	want := testutil.Rev(t, dir, "refs/heads/main")

	// The first attempt moves the remote ref (so the fresh observation differs
	// from the first one) and then fails as a dropped connection. The second is
	// let through to the real git.
	env, pushes := gitShim(t, "push", `if [ "$ATTEMPT" = 1 ]; then
	  "$REALGIT" --git-dir=`+remoteDir+` update-ref refs/heads/main `+remoteBase+`
	  echo "fatal: unable to access 'origin': Connection reset by peer" >&2
	  exit 1
	fi`)

	_, stderr, code := runSafegitEnv(t, dir, env, "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("a transport failure must be retried and the retry must succeed (code %d): %s", code, stderr)
	}

	attempts := pushes()
	if len(attempts) != 2 {
		t.Fatalf("expected exactly two push attempts (one dropped, one retried), got %d:\n%s",
			len(attempts), strings.Join(attempts, "\n"))
	}

	// The first attempt was pinned to what the remote held before the shim moved
	// it; the second must be pinned to what it holds now.
	firstWant := "--force-with-lease=refs/heads/main:" + remoteBefore
	secondWant := "--force-with-lease=refs/heads/main:" + remoteBase
	if !strings.Contains(attempts[0], firstWant) {
		t.Errorf("the first attempt must pin the lease to the SHA observed before it (%s); argv was:\n%s", firstWant, attempts[0])
	}
	if !strings.Contains(attempts[1], secondWant) {
		t.Errorf("the retry must RE-PIN the lease to a fresh observation (%s), not resend the stale one; argv was:\n%s", secondWant, attempts[1])
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != want {
		t.Errorf("remote main is %s after the successful retry, want %s", got, want)
	}
}

// TestPushRefusesARetryWhenALocalRefMovedUnderTheHooks closes the window
// between the pre-pre-push hooks and the attempt that actually reaches the
// remote.
//
// The hooks are handed one line per ref, with the SHA safegit resolved before
// running them. A retry re-resolves everything -- it has to, for the lease --
// and a local ref that moved in the meantime would be pushed on the strength of
// a hook run that never saw it. safegit refuses instead: there is no hook
// re-run machinery, and pushing un-validated content is the one outcome the
// hooks exist to prevent.
func TestPushRefusesARetryWhenALocalRefMovedUnderTheHooks(t *testing.T) {
	dir, remoteDir, _ := divergedFromRemote(t)
	remoteBefore := testutil.Rev(t, remoteDir, "refs/heads/main")
	validated := testutil.Rev(t, dir, "refs/heads/main")

	// The first attempt fails as a dropped connection AND moves the local branch
	// back a commit, so the retry's re-resolution sees a ref the hooks never
	// validated.
	env, pushes := gitShim(t, "push", `if [ "$ATTEMPT" = 1 ]; then
	  "$REALGIT" --git-dir=`+dir+`/.git update-ref refs/heads/main refs/heads/main^
	  echo "fatal: unable to access 'origin': Connection reset by peer" >&2
	  exit 1
	fi`)

	_, stderr, code := runSafegitEnv(t, dir, env, "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != exitcode.PushFailed {
		t.Fatalf("a retry that would push an un-validated local ref must be refused with %d (PushFailed), got %d; stderr: %s",
			exitcode.PushFailed, code, stderr)
	}
	if n := len(pushes()); n != 1 {
		t.Errorf("the retry must be refused before a second push is attempted; %d pushes happened:\n%s",
			n, strings.Join(pushes(), "\n"))
	}
	if !strings.Contains(stderr, "refs/heads/main") {
		t.Errorf("the refusal must name the ref that moved; stderr: %s", stderr)
	}
	if !strings.Contains(stderr, validated) {
		t.Errorf("the refusal must name the SHA the hooks validated (%s); stderr: %s", validated, stderr)
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != remoteBefore {
		t.Errorf("the refused retry still moved the remote to %s (was %s)", got, remoteBefore)
	}
}
