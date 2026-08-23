package test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// FINDING 1 (second-campaign adversarial review): the one-JSON-document promise
// is broken on the six guarded commands that hand git to the effects handle with
// strictcli.Stream(true) -- checkout, merge, rebase, reset, bisect and pull.
// Stream(true) wires the child's stdout to os.Stdout unconditionally, so git's
// own progress ("Auto-merging ...", "CONFLICT ...", "HEAD is now at ...",
// "Bisecting: ...", "Fast-forward") is written to safegit's stdout BEFORE the
// framework's envelope. Under --json that stream is documented to carry exactly
// one document; with the child's text in front of it, stdout does not parse at
// all and every machine-mode consumer of these six commands breaks.
//
// RULED TARGET: the child's stdout re-routes the way the capture-mode commands
// already do -- coord_cmd.go's passthroughStdout() already answers os.Stderr in
// machine mode, and internal/git's RunPassthroughTo already takes the sink as a
// parameter -- so under --json git's output goes to stderr and the envelope is
// the sole stdout document. Nothing is discarded; it moves streams.
//
// These tests are RED on purpose until that routing exists.

// oneJSONDocument reports the error of parsing the whole of stdout as a single
// JSON object, or nil when it parses. It is deliberately stricter than
// json.Unmarshal alone: a decoder that stops after the first value would accept
// a stream with a second document appended, which is exactly the defect.
func oneJSONDocument(stdout string) error {
	dec := json.NewDecoder(strings.NewReader(stdout))
	var first map[string]interface{}
	if err := dec.Decode(&first); err != nil {
		return err
	}
	var trailing interface{}
	if err := dec.Decode(&trailing); err == nil {
		return errTrailingDocument
	}
	return nil
}

// errTrailingDocument is the verdict for a stdout that parsed one document and
// then had more to say.
var errTrailingDocument = trailingDocumentError{}

type trailingDocumentError struct{}

func (trailingDocumentError) Error() string {
	return "stdout carries more than one JSON document"
}

// newDivergedBranchRepo builds a repo whose main and feature branches both
// rewrote the same lines of the same file, so merging or rebasing one onto the
// other conflicts and git narrates the conflict on its stdout.
func newDivergedBranchRepo(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nbase\nline3\n")
	safegitCommit(t, dir, "base", "conflicted.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nfeature\nline3\n")
	safegitCommit(t, dir, "feature edit", "conflicted.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "conflicted.txt", "line1\nmain\nline3\n")
	safegitCommit(t, dir, "main edit", "conflicted.txt")

	return dir
}

// newBehindRemoteRepo builds a repo whose origin holds one commit the local
// branch does not, so a pull fast-forwards and git narrates the update
// ("Updating a..b", "Fast-forward", the diffstat) on its stdout.
func newBehindRemoteRepo(t *testing.T) string {
	t.Helper()
	dir, _ := newRepoWithRemote(t)
	commitFileIn(t, dir, "a.txt", "one\n", "first")
	testutil.Git(t, dir, "push", "origin", "main")
	commitFileIn(t, dir, "b.txt", "two\n", "second")
	testutil.Git(t, dir, "push", "origin", "main")
	// Drop the local branch back one commit: origin is now ahead by exactly the
	// commit the pull below will fast-forward onto.
	testutil.Git(t, dir, "reset", "--hard", "HEAD~1")
	return dir
}

// TestGuardedCommandsEmitExactlyOneJSONDocument runs each of the six guarded
// commands in machine mode on a fixture that makes git talk, and requires the
// whole of stdout to parse as one JSON object.
//
// Which cases actually exercise the leak is fixture-dependent, and that is
// stated per case below rather than assumed: merge and rebase on a conflict,
// reset --hard, bisect start and a fast-forwarding pull all put git text on
// stdout today. checkout is the one whose ordinary narration ("Switched to
// branch") git writes to stderr, so its case pins the parse on the same path
// without necessarily being red by itself -- it belongs in the table because
// the routing fix is one seam for all six and a later git version that moves a
// line to stdout must not be able to break machine mode unnoticed.
func TestGuardedCommandsEmitExactlyOneJSONDocument(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
		args  []string
		// leaks records whether this case is expected to carry git's own
		// stdout today. It is not asserted on: it documents which rows are the
		// live evidence for the finding.
		leaks bool
	}{
		{
			name:  "merge conflict",
			setup: newDivergedBranchRepo,
			args:  []string{"--json", "merge", "feature"},
			leaks: true,
		},
		{
			name:  "rebase conflict",
			setup: newDivergedBranchRepo,
			args:  []string{"--json", "rebase", "feature"},
			leaks: true,
		},
		{
			name:  "pull fast-forward",
			setup: newBehindRemoteRepo,
			args:  []string{"--json", "pull", "--merge-strategy", "ff-only", "origin", "main"},
			leaks: true,
		},
		{
			name: "checkout branch",
			setup: func(t *testing.T) string {
				dir := newRepo(t)
				testutil.Git(t, dir, "branch", "other")
				return dir
			},
			args:  []string{"--json", "checkout", "other"},
			leaks: false,
		},
		{
			name: "reset hard",
			setup: func(t *testing.T) string {
				dir := newRepo(t)
				testutil.WriteFile(t, dir, "a.txt", "one\n")
				safegitCommit(t, dir, "second", "a.txt")
				return dir
			},
			args:  []string{"--json", "reset", "--hard", "HEAD~1"},
			leaks: true,
		},
		{
			name: "bisect start",
			setup: func(t *testing.T) string {
				dir := newRepo(t)
				testutil.WriteFile(t, dir, "a.txt", "one\n")
				safegitCommit(t, dir, "second", "a.txt")
				testutil.WriteFile(t, dir, "b.txt", "two\n")
				safegitCommit(t, dir, "third", "b.txt")
				return dir
			},
			args:  []string{"--json", "bisect", "start", "HEAD", "HEAD~2"},
			leaks: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.setup(t)

			stdout, stderr, code := runSafegit(t, dir, tc.args...)
			if err := oneJSONDocument(stdout); err != nil {
				t.Errorf("machine-mode stdout is not one JSON document (%v); git's own output was written beside the envelope.\n  exit code: %d\n  stdout: %s\n  stderr: %s",
					err, code, oneLine(stdout), oneLine(stderr))
			}
		})
	}
}

// TestGuardedCommandChildOutputGoesToStderrInMachineMode is the other half of
// the routing: the child's text is not discarded when it moves off stdout. A
// conflicting merge's narration has to reach the operator somewhere, and stderr
// is where `push` and the delegated conclusion already put it.
func TestGuardedCommandChildOutputGoesToStderrInMachineMode(t *testing.T) {
	dir := newDivergedBranchRepo(t)

	stdout, stderr, _ := runSafegit(t, dir, "--json", "merge", "feature")
	if strings.Contains(stdout, "CONFLICT") {
		t.Errorf("git's conflict narration is on safegit's stdout, where only the envelope belongs: %s", oneLine(stdout))
	}
	if !strings.Contains(stderr, "CONFLICT") {
		t.Errorf("git's conflict narration reached neither stream; it must move to stderr, not be dropped.\n  stdout: %s\n  stderr: %s",
			oneLine(stdout), oneLine(stderr))
	}
}
