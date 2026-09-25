//go:build !linux

package hooks

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

// TestKilledGroupMemberIsNamedWithThePartialNote drives the macOS sweep
// through its ps seam: a process in the hook's process group is killed, and
// the message naming it still says containment here is partial.
func TestKilledGroupMemberIsNamedWithThePartialNote(t *testing.T) {
	member := exec.Command("sleep", "60")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := member.Start(); err != nil {
		t.Fatal(err)
	}
	pid := member.Process.Pid
	go member.Wait()
	t.Cleanup(func() { member.Process.Kill() })

	prev := runPs
	runPs = func() ([]byte, error) { return []byte(fmt.Sprintf("%d %d sleep\n", pid, pid)), nil }
	defer func() { runPs = prev }()

	c := &container{self: -1, pgid: pid}
	left, unknown := c.sweep(startStop(DefaultStopCap), false)
	if unknown != "" || len(left) != 1 || left[0].PID != pid || !left[0].Killed {
		t.Fatalf("sweep = %+v, %q; want the group member, killed", left, unknown)
	}
	msgs := strings.Join(HookResult{Name: "release-check", Leftovers: left}.LeftoverMessages(), "\n")
	if !strings.Contains(msgs, "it was killed; "+uncontainedNote) {
		t.Errorf("messages = %q, want the killed member to carry %q", msgs, uncontainedNote)
	}
}
