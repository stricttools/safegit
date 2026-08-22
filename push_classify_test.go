package main

import (
	"strings"
	"testing"
)

// safegit decides what to do with a failed push by reading GIT'S OWN stderr:
// a lease rejection is terminal, a transport failure is retried, everything
// else is a verdict. Both classifiers are substring matches over text that also
// carries REF NAMES, so a pattern short enough to appear inside a branch name
// turns an operator's naming choice into a retry policy.
//
// These tests pin both directions: the phrases git really emits must classify,
// and a rejection line about a ref named after one of those phrases must not.

// gitTransportFailures are real stderr texts, captured from git 2.54.0 against
// unreachable remotes (curl-backed https, git's native transport, and OpenSSH).
// They are the evidence the pattern list is written from.
var gitTransportFailures = map[string]string{
	"https unresolvable host": "fatal: unable to access 'https://nonexistent.invalid.test/x.git/': Could not resolve host: nonexistent.invalid.test\n",
	"native refused":          "fatal: unable to connect to 127.0.0.1:\n127.0.0.1[0: 127.0.0.1]: errno=Connection refused\n",
	"https refused":           "fatal: unable to access 'https://127.0.0.1:1/x.git/': Failed to connect to 127.0.0.1 port 1 after 0 ms: Could not connect to server\n",
	"ssh refused":             "ssh: connect to host 127.0.0.1 port 1: Connection refused\nfatal: Could not read from remote repository.\n",
	"ssh unresolvable host":   "ssh: Could not resolve hostname nonexistent.invalid.test: Name or service not known\nfatal: Could not read from remote repository.\n",
	"tls wrong version":       "fatal: unable to access 'https://example.com:80/x.git/': TLS connect error: error:0A00010B:SSL routines::wrong version number\n",
	// Not from the local probe -- these need a connection that dies mid-pack,
	// which is not cheap to stage -- but they are git's own long-standing
	// spellings for a transfer that was cut off.
	"reset mid-transfer": "fatal: unable to access 'https://example.com/x.git/': Recv failure: Connection reset by peer\n",
	"early eof":          "remote: fatal: early EOF\nfatal: the remote end hung up unexpectedly\n",
	// Also not from the local probe: it needs a GnuTLS build of curl, which
	// this machine's git is not linked against. The spelling is GnuTLS's own
	// and curl reports it verbatim.
	"gnutls handshake": "fatal: unable to access 'https://example.com/x.git/': gnutls_handshake() failed: The TLS connection was non-properly terminated.\n",
}

// refNamedAfterAPattern builds the rejection git prints for a non-fast-forward
// push of a branch whose name is one of the words the old pattern list matched
// bare. Nothing here is a transport failure; the push reached the remote and
// was refused.
func refNamedAfterAPattern(name string) string {
	return "To /srv/git/repo.git\n" +
		" ! [rejected]        " + name + " -> " + name + " (non-fast-forward)\n" +
		"error: failed to push some refs to '/srv/git/repo.git'\n" +
		"hint: Updates were rejected because the tip of your current branch is behind\n"
}

func TestIsTransportErrorAcceptsWhatGitActuallyEmits(t *testing.T) {
	for name, stderrText := range gitTransportFailures {
		t.Run(name, func(t *testing.T) {
			if !isTransportError(stderrText) {
				t.Errorf("a real transport failure was not classified as one, so the push is not retried:\n%s", stderrText)
			}
		})
	}
}

func TestIsTransportErrorIgnoresRefNames(t *testing.T) {
	// Every one of these is a legal git branch name, and every one of them was
	// a bare pattern in the transport list.
	for _, ref := range []string{"EOF", "SSL", "TLS", "transport", "eof", "ssl-migration", "tls", "transport-refactor", "gnutls_handshake"} {
		t.Run(ref, func(t *testing.T) {
			stderrText := refNamedAfterAPattern(ref)
			if isTransportError(stderrText) {
				t.Errorf("a branch named %q turned a non-fast-forward rejection into a retried transport error:\n%s", ref, stderrText)
			}
		})
	}
}

func TestIsTransportErrorIgnoresVerdicts(t *testing.T) {
	verdicts := map[string]string{
		"empty":            "",
		"non-fast-forward": refNamedAfterAPattern("main"),
		"lease rejected": "To /srv/git/repo.git\n" +
			" ! [rejected]        v1.0 -> v1.0 (stale info)\n" +
			"error: failed to push some refs to '/srv/git/repo.git'\n",
		"permission denied": "remote: Permission to owner/repo.git denied to someone.\n" +
			"fatal: unable to access 'https://github.com/owner/repo.git/': The requested URL returned error: 403\n",
		"hook declined": "remote: error: hook declined to update refs/heads/main\n" +
			"To /srv/git/repo.git\n ! [remote rejected] main -> main (hook declined)\n",
	}
	for name, stderrText := range verdicts {
		t.Run(name, func(t *testing.T) {
			if isTransportError(stderrText) {
				t.Errorf("a verdict about the push was classified as a transport failure and would be retried:\n%s", stderrText)
			}
		})
	}
}

// TestLeaseRejectedNeedsTheParenthesesAndTheForce pins both anchors.
//
// git's rejection reads `(stale info)` -- the parentheses are part of the
// per-ref status marker, not decoration -- and only a push that SENT a lease
// can be refused for one. Without the second anchor an unforced push whose
// rejection merely mentions the words would exit 41 with a message about a
// lease it never pinned.
func TestLeaseRejectedNeedsTheParenthesesAndTheForce(t *testing.T) {
	rejection := "To /srv/git/repo.git\n" +
		" ! [rejected]        v1.0 -> v1.0 (stale info)\n" +
		"error: failed to push some refs to '/srv/git/repo.git'\n"

	if !leaseRejected(rejection, true) {
		t.Error("a forced push refused with (stale info) is a lease rejection")
	}
	if leaseRejected(rejection, false) {
		t.Error("an unforced push sends no lease, so it cannot be lease-rejected")
	}

	// A branch called `stale info` is impossible (refs cannot contain spaces),
	// but a commit subject echoed into output is not, and neither is prose.
	unparenthesized := "remote: warning: stale info about refs/heads/main was cached\n" +
		"To /srv/git/repo.git\n ! [rejected]        main -> main (non-fast-forward)\n"
	if leaseRejected(unparenthesized, true) {
		t.Errorf("only git's parenthesized (stale info) marker is a lease rejection:\n%s", unparenthesized)
	}
}

// TestEveryTransportPatternIsMultiWord pins the invariant the list is written
// under, so a single-token pattern cannot be added back without the test that
// would notice: git's stderr echoes REF NAMES, a ref name cannot contain a
// space, and a pattern that could BE a ref name turns an operator's naming
// choice into a retry policy.
func TestEveryTransportPatternIsMultiWord(t *testing.T) {
	for _, p := range transportPatterns {
		if !strings.Contains(p, " ") {
			t.Errorf("transport pattern %q is a single token, so a ref of that name would classify its own rejection as a transport failure", p)
		}
	}
}

// TestAtomicIsDecidedInOnePlace pins the rule --atomic is derived from, so the
// argv and the payload cannot disagree about whether a push was
// all-or-nothing.
func TestAtomicIsDecidedInOnePlace(t *testing.T) {
	one := []pushRefInfo{{LocalRef: "refs/heads/main", RemoteRef: "refs/heads/main"}}
	two := append(append([]pushRefInfo{}, one...), pushRefInfo{LocalRef: "refs/tags/v1", RemoteRef: "refs/tags/v1"})

	for _, tc := range []struct {
		name string
		refs []pushRefInfo
		want bool
	}{
		{"single ref", one, false},
		{"two refs", two, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pushIsAtomic(tc.refs); got != tc.want {
				t.Errorf("pushIsAtomic = %v, want %v", got, tc.want)
			}
			argv := buildGitPushArgs("origin", tc.refs, false)
			inArgv := false
			for _, a := range argv {
				if a == "--atomic" {
					inArgv = true
				}
			}
			if inArgv != tc.want {
				t.Errorf("--atomic in argv = %v, want %v; argv: %v", inArgv, tc.want, argv)
			}
			if got := buildPushPayload(globalFlags{}, "origin", tc.refs, false, 0, nil).Atomic; got != tc.want {
				t.Errorf("payload Atomic = %v, want %v", got, tc.want)
			}
		})
	}
}
