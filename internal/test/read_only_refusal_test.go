package test

import (
	"github.com/stricttools/safegit/internal/testutil"
	"path/filepath"
	"strings"
	"testing"
)

// Functional refusal coverage for inspection commands: invalid input is not a
// successful empty answer, and no branch or working-tree content is modified.
func TestReadOnlyCommandRefusals(t *testing.T) {
	for _, name := range []string{"author-list-outside-repository", "hook-list-outside-repository", "config-show-outside-repository", "config-get-unknown-key", "scan-invalid-regexp", "backup-list-missing-local-remote"} {
		t.Run(name, func(t *testing.T) {
			dir := newRepo(t)
			cwd := dir
			var args []string
			fragment := ""
			switch name {
			case "author-list-outside-repository":
				cwd = evalTempDir(t)
				args = []string{"author", "list"}
				fragment = "not a git repository"
			case "hook-list-outside-repository":
				cwd = evalTempDir(t)
				args = []string{"hook", "list"}
				fragment = "not a git repository"
			case "config-show-outside-repository":
				cwd = evalTempDir(t)
				args = []string{"config", "show"}
				fragment = "not a git repository"
			case "config-get-unknown-key":
				args = []string{"config", "get", "no.such.setting"}
				fragment = "unknown config key"
			case "scan-invalid-regexp":
				args = []string{"scan", "--pattern", "["}
				fragment = "["
			case "backup-list-missing-local-remote":
				args = []string{"backup", "list", filepath.Join(t.TempDir(), "absent.git")}
				fragment = "absent.git"
			}
			before := testutil.Rev(t, dir, "HEAD")
			status := testutil.GitOut(t, dir, "status", "--porcelain")
			out, err, code := runSafegit(t, cwd, args...)
			if code == 0 || !strings.Contains(out+err, fragment) {
				t.Fatalf("refusal missing for %v: code=%d stdout=%s stderr=%s", args, code, out, err)
			}
			if after := testutil.Rev(t, dir, "HEAD"); after != before {
				t.Fatalf("inspection refusal moved HEAD: %s -> %s", before, after)
			}
			if after := testutil.GitOut(t, dir, "status", "--porcelain"); after != status {
				t.Fatalf("inspection refusal changed checkout: %q -> %q", status, after)
			}
		})
	}
}
