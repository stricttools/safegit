package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
)

// These tests are about the LEASE safegit pins onto `git push --force-with-lease`.
//
// A bare `--force-with-lease` asks git to compare the remote ref against the
// REMOTE-TRACKING ref for it. Tags have no remote-tracking refs, so for a tag
// git has nothing to compare against, zeroes the expectation, and refuses to
// move a tag that already exists on the remote. That refusal made safegit's own
// post-scrub instruction ("push the rewritten tags") unsatisfiable through
// safegit. The fix is a per-ref expectation pinned to the SHA safegit itself
// observed: `--force-with-lease=<remoteRef>:<observedSHA>`, with the empty
// expectation (`<remoteRef>:`) meaning "this ref must not exist yet".

// TestGitBareLeaseCannotForcePushAMovedTag records, against the git binary the
// suite actually runs, the premise the pinned lease exists to work around: a
// bare --force-with-lease refuses to move an existing remote TAG, and the
// refusal's signature is "(stale info)".
//
// It is a pin on git, not on safegit: safegit's classification of a lease
// rejection keys off exactly this text, so a git release that respells it must
// fail here rather than silently turn every lease rejection into a retried
// transport error.
func TestGitBareLeaseCannotForcePushAMovedTag(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	testutil.Git(t, dir, "tag", "v1.0")
	testutil.Git(t, dir, "push", "origin", "refs/tags/v1.0:refs/tags/v1.0")

	testutil.WriteFile(t, dir, "second.txt", "second\n")
	testutil.Git(t, dir, "add", "second.txt")
	testutil.Git(t, dir, "commit", "-m", "second")
	testutil.Git(t, dir, "tag", "-f", "v1.0")

	out, code := testutil.GitTry(t, dir, "push", "--force-with-lease",
		"origin", "refs/tags/v1.0:refs/tags/v1.0")
	if code == 0 {
		t.Fatalf("premise gone: a bare --force-with-lease moved an existing remote tag; output: %s", out)
	}
	if !strings.Contains(out, "stale info") {
		t.Errorf("git's lease-rejection signature is no longer %q; safegit classifies on it. Output was:\n%s", "stale info", out)
	}

	// The remote tag is untouched, which is what makes the refusal a problem
	// rather than a formality.
	if got := testutil.Rev(t, remoteDir, "refs/tags/v1.0"); got == testutil.Rev(t, dir, "refs/tags/v1.0") {
		t.Error("the refused push moved the remote tag anyway")
	}
}

// TestPushTagsForceWithLeaseUpdatesMovedTag is the behaviour the pinned lease
// buys: after a history rewrite moves the local tags, `safegit push --refs tags
// --force-with-lease` must actually publish them.
func TestPushTagsForceWithLeaseUpdatesMovedTag(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	testutil.Git(t, dir, "tag", "v1.0")
	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "tags", "origin"); code != 0 {
		t.Fatalf("seeding the remote tag failed (code %d): %s", code, stderr)
	}

	testutil.WriteFile(t, dir, "second.txt", "second\n")
	safegitCommit(t, dir, "second", "second.txt")
	testutil.Git(t, dir, "tag", "-f", "v1.0")
	want := testutil.Rev(t, dir, "refs/tags/v1.0")

	_, stderr, code := runSafegit(t, dir, "--approve-consequential", "push",
		"--refs", "tags", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("force-pushing a moved tag failed (code %d): %s", code, stderr)
	}
	if got := testutil.Rev(t, remoteDir, "refs/tags/v1.0"); got != want {
		t.Errorf("remote tag v1.0 is %s, want %s", got, want)
	}
}

// TestPushLeaseCreatesANewRef covers the other end of the expectation: a ref
// that does not exist on the remote yet is pinned to the EMPTY expectation
// ("must not exist"), which a creation satisfies. The internal null-SHA marker
// for "absent" must never reach git as a literal 0000... expectation, which no
// ref can ever match.
func TestPushLeaseCreatesANewRef(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "a", "a.txt")
	want := testutil.Rev(t, dir, "HEAD")

	_, stderr, code := runSafegit(t, dir, "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("force-pushing a branch the remote does not have failed (code %d): %s", code, stderr)
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != want {
		t.Errorf("remote main is %s, want %s", got, want)
	}
}

// TestPushMultiRefFailurePushesNothing pins --atomic: when one ref of a
// multi-ref push is refused, none of the others reach the remote either. A
// partial push is the state nobody can reason about -- half a release's
// branches published, half refused.
func TestPushMultiRefFailurePushesNothing(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// Two branches on the remote: main, and a diverged one.
	testutil.Git(t, dir, "branch", "feature")
	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "branches", "origin"); code != 0 {
		t.Fatalf("seeding the remote branches failed (code %d): %s", code, stderr)
	}
	remoteMainBefore := testutil.Rev(t, remoteDir, "refs/heads/main")

	// feature diverges from what the remote holds (a rewritten commit, not a
	// descendant), so an ordinary push of it must be refused.
	testutil.WriteFile(t, dir, "f.txt", "f\n")
	safegitCommit(t, dir, "on feature", "f.txt")
	testutil.Git(t, dir, "branch", "-f", "feature", "HEAD")
	testutil.Git(t, dir, "push", "origin", "refs/heads/feature:refs/heads/feature")
	testutil.Git(t, dir, "branch", "-f", "feature", remoteMainBefore)

	// main, meanwhile, is a plain fast-forward that WOULD be accepted alone.
	testutil.WriteFile(t, dir, "m.txt", "m\n")
	safegitCommit(t, dir, "on main", "m.txt")

	_, stderr, code := runSafegit(t, dir, "push", "--refs", "branches", "origin")
	if code == 0 {
		t.Fatalf("a push with a non-fast-forward ref must fail; stderr: %s", stderr)
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != remoteMainBefore {
		t.Errorf("the refused multi-ref push still moved remote main to %s (was %s); --atomic was not in effect", got, remoteMainBefore)
	}
}

// TestPushForcedMultiRefArgvIsPinnedAndAtomic pins the argv itself, read off a
// dry run's would-do log: one expectation per ref, pinned to the SHA safegit
// observed, the EMPTY expectation for the ref the remote does not have, and
// --atomic because more than one ref is in flight.
//
// The preview records the argv the execute path would run, so this is the one
// place the whole composed command line is asserted rather than inferred from
// its effects.
func TestPushForcedMultiRefArgvIsPinnedAndAtomic(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	// main exists on the remote; the tag does not.
	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "a", "a.txt")
	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin"); code != 0 {
		t.Fatalf("seeding the remote failed (code %d): %s", code, stderr)
	}
	observed := testutil.Rev(t, dir, "refs/heads/main")
	testutil.Git(t, dir, "tag", "v9.9")

	stdout, stderr, code := runSafegit(t, dir, "--dry-run", "push", "--refs", "both", "--force-with-lease", "origin")
	if code != 0 {
		t.Fatalf("dry-run force-push failed (code %d): %s", code, stderr)
	}
	log := wouldDoLog(stdout)
	for _, want := range []string{
		"push --atomic",
		"--force-with-lease=refs/heads/main:" + observed,
		"--force-with-lease=refs/tags/v9.9: ",
		"origin refs/heads/main:refs/heads/main refs/tags/v9.9:refs/tags/v9.9",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the recorded push argv must contain %q; the log was:\n%s", want, log)
		}
	}
}

// gitShim installs a `git` wrapper ahead of the real one on the spawned
// safegit's PATH. Every invocation whose argv contains the bare word `sub`
// records its whole argv on a log file and then runs `before` (a shell snippet,
// with REALGIT bound to the actual git binary and ATTEMPT bound to the ordinal
// of this interception, counting from 1) before handing off to the real git
// with the original arguments. A snippet that exits itself replaces the real
// invocation instead of preceding it, which is how a test fabricates a failure.
//
// It is how a test reaches INTO the window between safegit observing the remote
// and safegit pushing: the snippet runs after the observation and before the
// push, which is exactly the concurrent-pusher race the lease exists to refuse.
// It returns the environment entries to hand runSafegitEnv, plus a func
// returning one entry per interception, in order, each the argv git was called
// with -- so a test can assert both HOW MANY attempts happened and WHAT each
// one asked for, from one recording.
func gitShim(t *testing.T, sub, before string) (env []string, calls func() []string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("locating the real git binary: %v", err)
	}
	shimDir := t.TempDir()
	argvLog := filepath.Join(shimDir, "argv-log")

	read := func() []string {
		data, err := os.ReadFile(argvLog)
		if err != nil {
			return nil
		}
		var out []string
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) != "" {
				out = append(out, line)
			}
		}
		return out
	}

	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"" + sub + "\" ]; then\n" +
		"    printf '%s\\n' \"$*\" >> " + argvLog + "\n" +
		"    ATTEMPT=$(wc -l < " + argvLog + " | tr -d ' ')\n" +
		"    REALGIT=" + realGit + "\n" +
		before + "\n" +
		"    break\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	return []string{"PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH")}, read
}

// divergedFromRemote builds the situation a force-push exists for: the remote
// holds a commit the local branch does not contain, and the local branch holds
// one the remote does not. It returns the repo, the bare remote, and the SHA
// the remote's main pointed at before the divergence.
func divergedFromRemote(t *testing.T) (dir, remoteDir, remoteBase string) {
	t.Helper()
	dir, remoteDir = newRepoWithRemote(t)

	testutil.WriteFile(t, dir, "a.txt", "a\n")
	safegitCommit(t, dir, "a", "a.txt")
	remoteBase = testutil.Rev(t, dir, "HEAD")
	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin"); code != 0 {
		t.Fatalf("seeding the remote failed (code %d): %s", code, stderr)
	}

	testutil.WriteFile(t, dir, "b.txt", "b\n")
	safegitCommit(t, dir, "b", "b.txt")
	if _, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin"); code != 0 {
		t.Fatalf("advancing the remote failed (code %d): %s", code, stderr)
	}

	// Rewrite the local branch so it is no longer a descendant of what the
	// remote holds: only a force can publish it now.
	testutil.Git(t, dir, "reset", "--hard", remoteBase)
	testutil.WriteFile(t, dir, "c.txt", "c\n")
	safegitCommit(t, dir, "c", "c.txt")
	return dir, remoteDir, remoteBase
}

// TestPushLeaseRefusesWhenTheRemoteMovedUnderUs is the race the lease exists
// for: safegit observes the remote, and someone else pushes before safegit's
// own push reaches it. The expectation safegit pinned no longer matches, so git
// refuses -- and the other session's commit survives.
//
// The shim moves the remote ref in exactly that window, so the race is
// deterministic rather than hoped for.
func TestPushLeaseRefusesWhenTheRemoteMovedUnderUs(t *testing.T) {
	dir, remoteDir, remoteBase := divergedFromRemote(t)

	env, pushes := gitShim(t, "push", `"$REALGIT" --git-dir=`+remoteDir+` update-ref refs/heads/main `+remoteBase)

	_, stderr, code := runSafegitEnv(t, dir, env, "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code != exitcode.PushLeaseRejected {
		t.Errorf("a lease rejection must exit %d (PushLeaseRejected), got %d; stderr: %s",
			exitcode.PushLeaseRejected, code, stderr)
	}
	if !strings.Contains(stderr, "moved") && !strings.Contains(stderr, "lease") {
		t.Errorf("the refusal must explain that the remote moved under the lease; stderr: %s", stderr)
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != remoteBase {
		t.Errorf("remote main is %s; the refused push must leave the other session's ref alone (%s)", got, remoteBase)
	}
	if n := len(pushes()); n != 1 {
		t.Errorf("a lease rejection is terminal, so exactly one push must be attempted; got %d", n)
	}
}

// TestPushLeaseRejectionIsNotRetried states the same property from the retry
// policy's side: a rejected lease is a verdict about the world, not a flaky
// connection, so it never enters the transport-retry loop. The repo is
// configured with several retry attempts precisely so a retried rejection would
// show up as a second push -- which, under this shim, would also SUCCEED (the
// re-observation would pin the ref the shim just wrote), silently overwriting
// the other session's work.
func TestPushLeaseRejectionIsNotRetried(t *testing.T) {
	dir, remoteDir, remoteBase := divergedFromRemote(t)
	if _, stderr, code := runSafegit(t, dir, "config", "set", "push.retryAttempts", "5"); code != 0 {
		t.Fatalf("setting push.retryAttempts failed (code %d): %s", code, stderr)
	}

	env, pushes := gitShim(t, "push", `"$REALGIT" --git-dir=`+remoteDir+` update-ref refs/heads/main `+remoteBase)

	_, stderr, code := runSafegitEnv(t, dir, env, "--approve-consequential", "push",
		"--refs", "head", "--force-with-lease", "origin")
	if code == 0 {
		t.Fatalf("the push must fail; stderr: %s", stderr)
	}
	if n := len(pushes()); n != 1 {
		t.Errorf("push.retryAttempts=5 must not apply to a lease rejection; %d pushes were attempted", n)
	}
	if got := testutil.Rev(t, remoteDir, "refs/heads/main"); got != remoteBase {
		t.Errorf("a retried lease rejection overwrote the other session's ref: remote main is %s, want %s", got, remoteBase)
	}
}
