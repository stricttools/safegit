package hooks

import (
	"bufio"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Windows has no POSIX execute bit. Enforce the native FILE_EXECUTE ACL even
// for interpreted scripts (whose interpreter would otherwise only need READ).
// Discovery refuses denied checks; it never silently skips them.
func isExecutable(path string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(p, windows.FILE_EXECUTE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return false
	}
	windows.CloseHandle(h)
	return true
}

func hookCommand(path string) (*exec.Cmd, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !isExecutable(path, info) {
		return nil, windows.ERROR_ACCESS_DENIED
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".exe", ".com":
		return exec.Command(path), nil
	case ".ps1":
		return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-File", path), nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return nil, syscall.ENOEXEC
	}
	line := strings.TrimSuffix(scanner.Text(), "\r")
	if !strings.HasPrefix(line, "#!") {
		return nil, syscall.ENOEXEC
	}
	args := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	if len(args) == 0 {
		return nil, syscall.ENOEXEC
	}
	interpreter := args[0]
	args = args[1:]
	// Git for Windows supplies these native interpreters, not WSL. Resolve the
	// conventional Unix shebang spellings without assuming a drive root /bin.
	switch interpreter {
	case "/bin/sh", "/usr/bin/sh":
		interpreter = "sh.exe"
	case "/bin/bash", "/usr/bin/bash":
		interpreter = "bash.exe"
	case "/usr/bin/env", "/bin/env":
		interpreter = "env.exe"
	}
	resolved, err := hookInterpreter(interpreter)
	if err != nil {
		return nil, err
	}
	args = append(args, path)
	return exec.Command(resolved, args...), nil
}

// Start suspended: not a single instruction of hook code can run before job
// assignment. No breakaway flag is granted to descendants.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
}

// Windows has no SIGTERM. A stop forcibly terminates the root; containment
// terminates the job as well, including descendants with redirected streams.
func killGroup(cmd *exec.Cmd, sig syscall.Signal) error { return cmd.Process.Kill() }

func hookInterpreter(name string) (string, error) {
	if resolved, err := exec.LookPath(name); err == nil {
		return resolved, nil
	}
	// Git for Windows normally adds only its cmd directory to PATH. Its shell
	// is still available under the installation; do not require a global PATH edit.
	if name != "sh.exe" && name != "bash.exe" && name != "env.exe" {
		return exec.LookPath(name)
	}
	git, err := exec.LookPath("git.exe")
	if err != nil {
		return "", err
	}
	base := filepath.Dir(git)
	for _, rel := range []string{filepath.Join("..", "bin"), filepath.Join("..", "..", "bin"), filepath.Join("..", "usr", "bin"), filepath.Join("..", "..", "usr", "bin")} {
		candidate := filepath.Clean(filepath.Join(base, rel, name))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return exec.LookPath(name)
}

