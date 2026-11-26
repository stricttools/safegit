package testutil

import (
	"golang.org/x/sys/windows"
	"os/exec"
	"testing"
)

// SetExecutable changes only fixtures' native execution permission. os.Chmod
// cannot express an execute denial on Windows, so use the current user's ACL.
func SetExecutable(t *testing.T, path string, enabled bool) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := "*" + user.User.Sid.String()
	args := []string{path, "/deny", sid + ":(X)"}
	if enabled {
		args = []string{path, "/remove:d", sid}
	}
	if out, err := exec.Command("icacls.exe", args...).CombinedOutput(); err != nil {
		t.Fatalf("setting execute permission on %s: %v: %s", path, err, out)
	}
}

func SetDirectoryWritable(t *testing.T, path string, enabled bool) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := "*" + user.User.Sid.String()
	args := []string{path, "/deny", sid + ":(WD,AD)"}
	if enabled {
		args = []string{path, "/remove:d", sid}
	}
	if out, err := exec.Command("icacls.exe", args...).CombinedOutput(); err != nil {
		t.Fatalf("setting directory write ACL: %v %s", err, out)
	}
}
