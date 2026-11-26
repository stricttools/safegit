package hooks

import (
	"context"
	"errors"
	"github.com/stricttools/safegit/internal/procutil"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Real native executable helpers, not an MSYS shell or Unix PID emulation.
func init() {
	mode := os.Getenv("SAFEGIT_WINDOWS_CONTAINMENT_HELPER")
	switch mode {
	case "child":
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	case "ok":
		os.Exit(0)
	case "breakaway":
		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		cmd.Env = append(os.Environ(), "SAFEGIT_WINDOWS_CONTAINMENT_HELPER=child")
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB}
		if err := cmd.Start(); err == nil {
			cmd.Process.Kill()
			cmd.Wait()
			os.Exit(99)
		} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			os.Exit(98)
		}
		os.Exit(0)
	case "fail":
		os.Exit(17)
	case "sleep":
		time.Sleep(5 * time.Minute)
		os.Exit(0)
	case "leftover":
		exe, _ := os.Executable()
		cmd := exec.Command(exe)
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "SAFEGIT_WINDOWS_CONTAINMENT_HELPER=") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		cmd.Env = append(cmd.Env, "SAFEGIT_WINDOWS_CONTAINMENT_HELPER=child")
		// No standard-stream inheritance: pipe-holder-only containment misses this.
		if cmd.Start() != nil {
			os.Exit(99)
		}
		if os.WriteFile(os.Getenv("SAFEGIT_WINDOWS_CHILD_PID"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600) != nil {
			os.Exit(98)
		}
		os.Exit(0)
	}
}
func TestWindowsNativeHookJob(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ok", "fail", "sleep", "leftover", "breakaway"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			env := []string{"SAFEGIT_WINDOWS_CONTAINMENT_HELPER=" + mode, "SAFEGIT_WINDOWS_CHILD_PID=" + pidFile}
			start := time.Now()
			result, err := RunSingle(context.Background(), exe, nil, 1, MinStopCap, env, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > MinStopCap+3*time.Second {
				t.Fatal("stop exceeded cap")
			}
			switch mode {
			case "ok", "breakaway":
				if result.Failed() {
					t.Fatalf("clean hook failed: %+v", result)
				}
			case "fail":
				if !exitedWith(result, 17) || !result.Failed() {
					t.Fatalf("failure not preserved: %+v", result)
				}
			case "sleep":
				if !result.TimedOut || !result.Failed() {
					t.Fatalf("timeout not enforced: %+v", result)
				}
			case "leftover":
				raw, err := os.ReadFile(pidFile)
				if err != nil {
					t.Fatal(err)
				}
				pid, _ := strconv.Atoi(string(raw))
				if procutil.ProcessAlive(pid) {
					p, _ := os.FindProcess(pid)
					p.Kill()
					t.Fatalf("detached-stream child %d survived job", pid)
				}
				if len(result.Leftovers) != 1 || result.Leftovers[0].PID != pid || !result.Leftovers[0].Killed || !result.Failed() {
					t.Fatalf("leftover not named and rejected: %+v", result)
				}
			}
		})
	}
}
func TestWindowsNativeHookCancellation(t *testing.T) {
	exe, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	r, err := RunSingle(ctx, exe, nil, 300, MinStopCap, []string{"SAFEGIT_WINDOWS_CONTAINMENT_HELPER=sleep"}, func(string) {})
	if err != nil || !r.Failed() || r.TimedOut {
		t.Fatalf("cancel result %+v, %v", r, err)
	}
}

func TestWindowsHookExecuteACLIsEnforced(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	hook := filepath.Join(LocalDir(gitDir), "pre-pre-push")
	if err := os.MkdirAll(filepath.Dir(hook), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls.exe", hook, "/deny", "*"+user.User.Sid.String()+":(X)").CombinedOutput(); err != nil {
		t.Fatalf("deny execute ACL: %v: %s", err, out)
	}
	_, err = Discover(Store{SharedGitDir: gitDir})
	var denied *LocalNotExecutableError
	if !errors.As(err, &denied) {
		t.Fatalf("execute-denied hook was not refused: %v", err)
	}
	result, err := RunSingle(context.Background(), hook, nil, 10, MinStopCap, nil, func(string) {})
	if err != nil || result.StartError == "" || !result.Failed() {
		t.Fatalf("RunSingle bypassed execute ACL: %+v %v", result, err)
	}
}
