package test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE FINDING: a pre-pre-push hook can silently rewrite its own timeout. The
// first line the hook writes to stdout is intercepted by internal/hooks/hooks.go
// runOne, handed to parseTimeoutOverride, and when it matches the exact literal
// `# safegit: timeout=N` (leading/trailing space trimmed, N a positive integer)
// the parsed N REPLACES the configured hooks.preprepush.timeoutSeconds for that
// hook. The replacement is uncapped in both directions: a hook may shorten a
// generous configured budget or extend a deliberately tight one, and the
// configured value the operator set is simply discarded. The same line is then
// SWALLOWED -- runOne forwards the first stdout line only when the parse fails,
// so an operator watching the push never sees the line that changed the budget,
// and the JSON payload carries no record of it either.
//
// THE RULING: the timeout-override protocol is DELETED. The configured timeout
// is the only timeout; nothing a hook writes can change it. The magic line
// becomes inert -- ordinary hook output, forwarded verbatim like any other line
// the hook prints, with no interception and no swallowing. A hook that needs a
// different budget gets it from configuration (or from the SAFEGIT_HOOK_TIMEOUT_S
// value safegit already exports into the hook environment), never by printing at
// safegit.
//
// SANCTIONED REWRITES at implementation time: the existing coverage that pins
// the override protocol is expected to be deleted along with the protocol, and
// deleting it is part of implementing this ruling rather than a regression --
// internal/hooks/hooks_test.go TestParseTimeoutOverride (the table asserting
// `# safegit: timeout=60` parses to 60, `timeout=300\n` to 300, and the
// 0/-1/abc rejections) together with parseTimeoutOverride itself, and the
// override paragraphs in docs/architecture.md and docs/divergences.md.
//
// Both tests below are red today.

// TestHookTimeoutOverrideNoLongerShortensTheBudget: a hook whose first stdout
// line asks for a 1-second budget, under a configured 60-second one, must run to
// completion and let the push proceed.
//
// Red today: the override wins, the hook is SIGTERMed at ~1s, and the push exits
// 21 (PushHookTimeout) with `hook pre-pre-push timed out after ~1s` on stderr --
// so the witness the hook writes after its sleep never appears.
func TestHookTimeoutOverrideNoLongerShortensTheBudget(t *testing.T) {
	dir, remoteDir := newRepoWithRemote(t)

	// A budget far longer than the hook needs. Only the magic line can make this
	// push time out, which is what makes the failure below attributable.
	if _, stderr, code := runSafegit(t, dir, "config", "set", "hooks.preprepush.timeoutSeconds", "60"); code != 0 {
		t.Fatalf("config set failed (code %d): %s", code, stderr)
	}

	// The witness lives outside the repository: a hook runs with its own
	// directory as the working directory, so the path must be absolute, and a
	// file written into the work tree would dirty it for no reason.
	witness := filepath.Join(evalTempDir(t), "hook-finished.txt")

	// The magic line must be the hook's FIRST stdout line and must be terminated
	// by a newline -- runOne reads it with bufio ReadString('\n') and only parses
	// a line it read cleanly. `echo` gives exactly that.
	writeHookScript(t, filepath.Join(localHookDir(dir), "pre-pre-push"),
		"echo '# safegit: timeout=1'\n"+
			"sleep 3\n"+
			"printf finished > "+witness)

	stdout, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin")

	if code != 0 {
		t.Errorf("push exited %d; the configured 60s budget must stand and the hook must be allowed to finish.\nstderr: %s\nstdout: %s",
			code, stderr, stdout)
	}
	if _, err := os.Stat(witness); err != nil {
		t.Errorf("the hook was cut short (%v): it never reached the line after its 3s sleep, so the printed `# safegit: timeout=1` replaced the configured budget", err)
	}
	if branches := remoteBranches(t, remoteDir); len(branches) == 0 {
		t.Errorf("nothing reached the remote; the push did not proceed past the hook")
	}
}

// TestHookTimeoutOverrideLineIsForwardedNotSwallowed: the same literal, in a
// hook that exits immediately so nothing can time out, must reach the operator
// as ordinary hook output.
//
// Red today: runOne consumes the first stdout line and forwards it only when the
// parse FAILS, so this line is parsed, acted on, and dropped -- stdout carries
// the hook's second line and not its first.
func TestHookTimeoutOverrideLineIsForwardedNotSwallowed(t *testing.T) {
	dir, _ := newRepoWithRemote(t)

	if _, stderr, code := runSafegit(t, dir, "config", "set", "hooks.preprepush.timeoutSeconds", "60"); code != 0 {
		t.Fatalf("config set failed (code %d): %s", code, stderr)
	}

	const magic = "# safegit: timeout=1"
	const ordinary = "second line from the hook"
	writeHookScript(t, filepath.Join(localHookDir(dir), "pre-pre-push"),
		"echo '"+magic+"'\n"+
			"echo '"+ordinary+"'")

	stdout, stderr, code := runSafegit(t, dir, "push", "--refs", "head", "origin")
	if code != 0 {
		t.Fatalf("push failed (code %d): %s", code, stderr)
	}

	out := stdout + stderr

	// The control: hook stdout is forwarded at all. Without it, the assertion
	// below would pass just as well against a hook that never ran or a harness
	// that discards hook output wholesale.
	if !strings.Contains(out, ordinary) {
		t.Fatalf("the hook's ordinary line %q is missing, so hook output is not being observed here at all; the swallowing assertion would prove nothing.\nstdout: %s\nstderr: %s",
			ordinary, stdout, stderr)
	}

	if !strings.Contains(out, magic) {
		t.Errorf("the line %q was swallowed: it is inert output now and must be forwarded verbatim like every other line the hook prints.\nstdout: %s\nstderr: %s",
			magic, stdout, stderr)
	}
}
