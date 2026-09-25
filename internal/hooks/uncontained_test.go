package hooks

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// Where safegit cannot be a child subreaper (macOS), a process that left the
// hook's process group is out of reach. What safegit can still do is NAME
// every process that holds the hook's output pipes after the group was killed,
// by reading lsof, and say that it is still running. These tests drive that
// path through its seam, so it is exercised without a real macOS.

// lsofPipes is lsof -F output in the shape macOS prints for pipes: each end's
// own kernel address as its device, and the peer end's address as its name.
// Process 100 is safegit holding the read ends (fds 5 and 7); process 200
// holds the write end of the stdout pipe; 300 holds an unrelated pipe; 400
// holds the write end of the stderr pipe twice (two fds).
const lsofPipes = `p100
csafegit
f5
tPIPE
d0xaaaa
n->0xbbbb
f7
tPIPE
d0xeeee
n->0xffff
p200
cnode
f1
tPIPE
d0xbbbb
n->0xaaaa
p300
cother
f1
tPIPE
d0xcccc
n->0xdddd
p400
cpython3
f1
tPIPE
d0xffff
n->0xeeee
f2
tPIPE
d0xffff
n->0xeeee
`

func TestPipeHoldersNamesEveryProcessHoldingTheWriteEnds(t *testing.T) {
	lsof := func() ([]byte, error) { return []byte(lsofPipes), nil }
	holders, reason := pipeHolders(lsof, 100, []int{5, 7})
	if reason != "" {
		t.Fatalf("unexpected failure: %s", reason)
	}
	want := []LeftoverProcess{{PID: 200, Command: "node"}, {PID: 400, Command: "python3"}}
	if len(holders) != len(want) {
		t.Fatalf("holders = %+v, want %+v", holders, want)
	}
	for i := range want {
		if holders[i] != want[i] {
			t.Errorf("holder %d = %+v, want %+v", i, holders[i], want[i])
		}
	}
}

func TestPipeHoldersSaysWhyWhenLsofIsMissing(t *testing.T) {
	lsof := func() ([]byte, error) { return nil, &exec.Error{Name: "lsof", Err: exec.ErrNotFound} }
	holders, reason := pipeHolders(lsof, 100, []int{5})
	if len(holders) != 0 {
		t.Errorf("no holders can be named without lsof, got %+v", holders)
	}
	if !strings.Contains(reason, "lsof") || !strings.Contains(reason, "not found") {
		t.Errorf("the reason must name lsof and why it failed: %q", reason)
	}
}

func TestPipeHoldersSaysWhyWhenLsofFails(t *testing.T) {
	lsof := func() ([]byte, error) { return nil, errors.New("exit status 1") }
	_, reason := pipeHolders(lsof, 100, []int{5})
	if !strings.Contains(reason, "exit status 1") {
		t.Errorf("the reason must carry lsof's failure: %q", reason)
	}
}

// TestPipeHoldersSaysWhyWhenItsOwnReadEndIsNotListed: an lsof output that does
// not show safegit's own read end cannot tell which pipe is the hook's, and must
// say so rather than report that nobody holds it.
func TestPipeHoldersSaysWhyWhenItsOwnReadEndIsNotListed(t *testing.T) {
	lsof := func() ([]byte, error) { return []byte(lsofPipes), nil }
	holders, reason := pipeHolders(lsof, 999, []int{5})
	if len(holders) != 0 || reason == "" {
		t.Errorf("holders = %+v, reason = %q; want none and a reason", holders, reason)
	}
}

// TestUncontainedMessagesStateThePartialContainment: the error text for a
// process safegit could not stop names it and says it is still running and why.
func TestUncontainedMessagesStateThePartialContainment(t *testing.T) {
	r := HookResult{Name: "release-check", Leftovers: []LeftoverProcess{{PID: 48213, Command: "node"}}}
	msgs := strings.Join(r.LeftoverMessages(), "\n")
	want := "hook release-check left process 48213 (node) running after it ended; it is still running; " + uncontainedNote
	if !strings.Contains(msgs, want) {
		t.Errorf("messages = %q, want %q", msgs, want)
	}
	if !r.Failed() {
		t.Error("a hook that left a process running must be a failed run")
	}

	r = HookResult{Name: "release-check", LeftoverUnknown: "lsof could not be run: executable file not found in $PATH"}
	msgs = strings.Join(r.LeftoverMessages(), "\n")
	for _, want := range []string{"release-check", "could not name", "lsof could not be run", "may still be running", uncontainedNote} {
		if !strings.Contains(msgs, want) {
			t.Errorf("messages %q do not say %q", msgs, want)
		}
	}
	if !r.Failed() {
		t.Error("a hook that left an unnamed process holding its output must be a failed run")
	}
}

// TestPsGroupMembersNamesTheGroupsProcesses: the group-member listing reads
// POSIX `ps -A -o pid= -o pgid= -o comm=` output and keeps the hook's group.
func TestPsGroupMembersNamesTheGroupsProcesses(t *testing.T) {
	out := "    1     1 /sbin/launchd\n  501   500 sleep\n  502   500 /usr/local/bin/node\n  600   600 zsh\n"
	got := parsePsGroup([]byte(out), 500)
	want := []LeftoverProcess{{PID: 501, Command: "sleep"}, {PID: 502, Command: "node"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("member %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestKilledLeftoverCarriesThePartialContainmentNote: where containment is
// partial (macOS), a process safegit found in the hook's group and killed is
// named with the same note as one still running -- that it was killed says
// nothing about a process that left the group, which safegit cannot reach.
// Where containment is complete (Linux), a killed process carries no note.
func TestKilledLeftoverCarriesThePartialContainmentNote(t *testing.T) {
	r := HookResult{Name: "release-check", Leftovers: []LeftoverProcess{{PID: 48213, Command: "node", Killed: true}}}
	base := "hook release-check left process 48213 (node) running after it ended; it was killed"

	msgs := leftoverMessages(r, true)
	if len(msgs) != 1 || msgs[0] != base+"; "+uncontainedNote {
		t.Errorf("partial containment: messages = %q, want [%q]", msgs, base+"; "+uncontainedNote)
	}
	msgs = leftoverMessages(r, false)
	if len(msgs) != 1 || msgs[0] != base {
		t.Errorf("complete containment: messages = %q, want [%q]", msgs, base)
	}
}
