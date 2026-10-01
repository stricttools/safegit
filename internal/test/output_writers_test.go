package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// directWriteExemptions are the writes to stdout and stderr that are allowed to
// bypass the framework's writers, counted per file and per rule as
// `--lint-framework-use` reports them. Each one carries output safegit relays
// rather than writes itself, or the one interactive prompt:
//
//   - backup.go: git's own stderr from a failed fast-forward, relayed verbatim.
//   - coord_cmd.go: a guarded passthrough's captured git output, relayed (one
//     stderr write), and the sink a passthrough child's stdout goes to (stdout
//     at a terminal, stderr under --json).
//   - internal/commit/hooks.go: the repository's own commit hooks' output.
//   - internal/git/git.go: an operator-typed passthrough's git output, wired to
//     the terminal.
//   - internal/hooks/hooks.go: a pre-pre-push hook's output, forwarded to stderr.
//   - main.go: the confirmation prompt, which waits on a terminal.
//   - push.go: git push's own output, relayed.
var directWriteExemptions = map[string]int{
	"backup.go stderr-write":                1,
	"coord_cmd.go stderr-write":             2,
	"coord_cmd.go stdout-write":             1,
	"internal/commit/hooks.go stderr-write": 2,
	"internal/git/git.go stdout-write":      1,
	"internal/git/git.go stderr-write":      1,
	"internal/hooks/hooks.go stderr-write":  2,
	"main.go stderr-write":                  1,
	"push.go stdout-write":                  1,
	"push.go stderr-write":                  2,
}

// TestSafegitWritesOnlyThroughTheFramework: everything safegit itself says goes
// through the framework's writers, so it is prefixed once, hidden by --quiet
// where it should be, and lands in the --json document. The framework's own
// scan finds every direct write; what it finds must be exactly the relayed
// output and the prompt above.
func TestSafegitWritesOnlyThroughTheFramework(t *testing.T) {
	evalTempDir(t)
	cmd := exec.Command(safegitBin, "--lint-framework-use")
	cmd.Dir = projectRoot()
	cmd.Env = controlledEnv(t)
	out, _ := cmd.CombinedOutput()

	finding := regexp.MustCompile(`^(\S+?):\d+: (stdout-write|stderr-write): `)
	got := map[string]int{}
	for _, line := range strings.Split(string(out), "\n") {
		if m := finding.FindStringSubmatch(line); m != nil {
			got[filepath.ToSlash(m[1])+" "+m[2]]++
		}
	}
	for key, n := range got {
		if want := directWriteExemptions[key]; n != want {
			t.Errorf("%s: %d direct write(s), %d exempt; write through infof, outf, debugf, warnf or errorf instead", key, n, want)
		}
	}
	for key, want := range directWriteExemptions {
		if got[key] == 0 {
			t.Errorf("%s: exempt %d time(s) but the scan finds none; remove the exemption", key, want)
		}
	}
}

// TestJSONRunWritesNothingOutsideTheDocument: under --json a successful
// commit writes no stderr at all, and the line it prints at a terminal is an
// info diagnostic in the document.
func TestJSONRunWritesNothingOutsideTheDocument(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "--json", "commit", "-m", "add a", "--", "a.txt")
	if code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("a --json commit wrote to stderr: %q", stderr)
	}
	env := decodeEnvelope(t, stdout)
	found := false
	for _, d := range env.Diagnostics {
		if d["level"] == "info" && strings.HasPrefix(d["message"], "[main ") {
			found = true
		}
	}
	if !found {
		t.Errorf("the commit line is not an info diagnostic: %v", env.Diagnostics)
	}
}

// TestQuietHidesProgress: --quiet hides a commit's progress lines entirely.
func TestQuietHidesProgress(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0644); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSafegit(t, dir, "--quiet", "commit", "-m", "add a", "--", "a.txt")
	if code != 0 {
		t.Fatalf("commit failed (%d): %s", code, stderr)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("a quiet commit wrote output: stdout %q, stderr %q", stdout, stderr)
	}
}

// TestFrameworkPrefixesAppearOnce: an error and a warning each carry the
// framework's prefix once, never a hand-written second one.
func TestFrameworkPrefixesAppearOnce(t *testing.T) {
	dir := newRepo(t)
	_, stderr, _ := runSafegit(t, dir, "commit", "--", "missing.txt")
	if strings.Contains(stderr, "error: error:") {
		t.Errorf("an error carries its prefix twice: %s", stderr)
	}

	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "abs")); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := runSafegit(t, dir, "commit", "--allow-non-portable-targets", "-m", "link", "--", "abs")
	if code != 0 {
		t.Fatalf("elected commit failed (%d): %s", code, stderr)
	}
	if !strings.Contains(stderr, "warning: abs is a symlink to /etc/hostname") || strings.Contains(stderr, "warning: notice:") {
		t.Errorf("want one framework-prefixed warning for the elected link, got: %s", stderr)
	}
}
