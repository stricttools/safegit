package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var shimOnce sync.Once
var shimCache, shimBinary string
var shimError error

const nativeShimSource = `package main
import("os";"os/exec";"strconv";"syscall";"time";"os/signal")
func main(){
if len(os.Args)==3&&os.Args[1]=="--ctrl-break-target" { pid,e:=strconv.Atoi(os.Args[2]);if e!=nil{os.Exit(93)};dll:=syscall.NewLazyDLL("kernel32.dll");ok,_,_:=dll.NewProc("AttachConsole").Call(uintptr(pid));if ok==0{os.Exit(92)};signal.Ignore(os.Interrupt);handler:=syscall.NewCallback(func(code uint32)uintptr{return 1});dll.NewProc("SetConsoleCtrlHandler").Call(handler,1);ok,_,_ = dll.NewProc("GenerateConsoleCtrlEvent").Call(1,0);if ok==0{os.Exit(91)};time.Sleep(100*time.Millisecond);return }
 if len(os.Args)==2&&os.Args[1]=="--kill-native-parent" {pid,e:=strconv.Atoi(os.Getenv("SAFEGIT_NATIVE_PARENT_PID"));if e!=nil||pid<=0{os.Exit(97)};p,e:=os.FindProcess(pid);if e!=nil||p.Kill()!=nil{os.Exit(96)};return}
 self,_:=os.Executable();args:=append([]string{self+".sh"},os.Args[1:]...);shell,_:=os.ReadFile(self+".shell");cmd:=exec.Command(string(shell),args...)
 cmd.Env=append(os.Environ(),"SAFEGIT_NATIVE_PARENT_PID="+strconv.Itoa(os.Getppid()),"SAFEGIT_NATIVE_SHIM_EXE="+self)
 cmd.Stdin=os.Stdin;cmd.Stdout=os.Stdout;cmd.Stderr=os.Stderr
 if err:=cmd.Run();err!=nil{if e,ok:=err.(*exec.ExitError);ok{os.Exit(e.ExitCode())};os.Exit(95)}
}`

// WriteExecutable builds a native .exe fixture, not an extensionless file that
// Windows PATH lookup silently bypasses. Its script runs under native Git Bash.
// Crash injections explicitly target the proxy's native parent, never an MSYS PID.
func WriteExecutable(t *testing.T, path, script string) {
	t.Helper()
	shimOnce.Do(func() {
		shimCache, shimError = os.MkdirTemp("", "safegit-native-shim-")
		if shimError != nil {
			return
		}
		source := filepath.Join(shimCache, "main.go")
		shimError = os.WriteFile(source, []byte(nativeShimSource), 0600)
		if shimError != nil {
			return
		}
		shimBinary = filepath.Join(shimCache, "proxy.exe")
		cmd := exec.Command("go", "build", "-o", shimBinary, source)
		if out, e := cmd.CombinedOutput(); e != nil {
			shimError = &shimBuildError{e, string(out)}
		}
	})
	if shimError != nil {
		t.Fatal(shimError)
	}
	shell, err := exec.LookPath("sh.exe")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.ToLower(path), ".exe") {
		path += ".exe"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(shimBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0755); err != nil {
		t.Fatal(err)
	}
	script = FixtureScript(script)
	script = strings.ReplaceAll(script, "kill -9 $PPID", `"$SAFEGIT_NATIVE_SHIM_EXE" --kill-native-parent`)
	script = "#!/bin/sh\nexport SAFEGIT_NATIVE_SHELL=" + ShellPath(shell) + "\n" + strings.TrimPrefix(script, "#!/bin/sh\n")
	if err := os.WriteFile(path+".sh", []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	// The first proxy needs the shell before it has run its script. A sidecar is
	// passed through the parent test environment, scoped to this test process.
	if err := os.WriteFile(path+".shell", []byte(shell), 0600); err != nil {
		t.Fatal(err)
	}
}

type shimBuildError struct {
	err    error
	output string
}

func (e *shimBuildError) Error() string { return e.err.Error() + ": " + e.output }
func CleanupExecutables() {
	if shimCache != "" {
		os.RemoveAll(shimCache)
	}
}
