package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file holds the canonical git and filesystem helpers every suite in the
// repository shares. They are deliberately free of any safegit dependency --
// they only run git and touch files -- so unit tests in internal/* can use them
// as freely as the integration suite in internal/test. Anything that builds or
// runs the safegit binary belongs in internal/test instead, which is the only
// package that has that binary.
//
// The runners come in a small, explicit family rather than one function with
// options, because the differences (trimmed or verbatim output, fatal or
// exit-code-returning) are what call sites actually vary on:
//
//	Git        trimmed combined output, fatal on a nonzero exit
//	GitRaw     verbatim combined output, fatal on a nonzero exit
//	GitTry     verbatim combined output plus the exit code, never fatal
//	GitTryEnv  GitTry with extra environment entries
//	GitStdin   trimmed stdout with data on stdin, fatal on a nonzero exit
//	GitTryOut  verbatim stdout plus whether git exited zero, never fatal

// Git runs git in dir, fails the test on a nonzero exit, and returns the
// combined output with surrounding whitespace trimmed.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(GitRaw(t, dir, args...))
}

// GitRaw is Git without the trim: the combined output exactly as git wrote it.
// Use it wherever the trailing newline (or its absence) is part of what the
// test asserts.
func GitRaw(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s failed: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return string(out)
}

// GitTry runs git in dir and returns its combined output and exit code without
// failing the test: fixtures routinely depend on commands that exit nonzero by
// design (a conflicting merge, a refused amend). Only a failure to start the
// process at all fails the test.
func GitTry(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	return GitTryEnv(t, dir, nil, args...)
}

// GitTryEnv is GitTry with extra "KEY=value" entries appended to the inherited
// environment -- needed for calls that would otherwise open an editor.
func GitTryEnv(t *testing.T, dir string, extraEnv []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
		code = exitErr.ExitCode()
	}
	return string(out), code
}

// GitStdin runs git in dir with stdin data, fails the test on a nonzero exit,
// and returns trimmed stdout (stderr is left to the test's own output).
func GitStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(string(out))
}

// GitTryOut runs git in dir and returns its verbatim stdout plus whether git
// exited zero. Use it to probe for something that may legitimately be absent.
func GitTryOut(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// WriteFile writes content at a repo-relative path, creating parent
// directories. It is the one spelling for "put this file in the repo".
func WriteFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	WriteFileAt(t, filepath.Join(dir, rel), content)
}

// WriteFileAt is WriteFile for a path the caller has already joined.
func WriteFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// TreePaths returns every path in a revision's tree, repo-relative and
// recursive. --full-tree keeps the answer independent of the directory git is
// invoked from. An empty tree yields nil.
func TreePaths(t *testing.T, dir, rev string) []string {
	t.Helper()
	return SplitLines(Git(t, dir, "ls-tree", "-r", "--full-tree", "--name-only", rev))
}

// SplitLines splits s on newlines, dropping empty lines. An empty string
// yields nil, so a caller can range over the result without a length check.
func SplitLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// Contains reports whether haystack holds needle.
func Contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Rev resolves a revision to its full SHA, failing the test when it does not
// resolve.
func Rev(t *testing.T, dir, rev string) string {
	t.Helper()
	return Git(t, dir, "rev-parse", rev)
}

// RevTry resolves a revision to its full SHA, returning "" when the revision
// does not exist (an unborn branch, a ref another test has yet to create).
func RevTry(t *testing.T, dir, rev string) string {
	t.Helper()
	out, ok := GitTryOut(t, dir, "rev-parse", rev)
	if !ok {
		return ""
	}
	return strings.TrimSpace(out)
}

// Parents returns the parent SHAs of a commit, in order. A commit that cannot
// be read at all fails the test; a root commit yields an empty slice.
func Parents(t *testing.T, dir, ref string) []string {
	t.Helper()
	fields := strings.Fields(Git(t, dir, "rev-list", "--parents", "-n", "1", ref))
	if len(fields) == 0 {
		t.Fatalf("rev-list --parents produced nothing for %s", ref)
	}
	return fields[1:]
}

// Show returns the verbatim content of a repo-relative path at rev, and
// whether that path exists in that revision.
func Show(t *testing.T, dir, rev, path string) (string, bool) {
	t.Helper()
	return GitTryOut(t, dir, "show", rev+":"+path)
}

// MustShow is Show for a path the test requires to be present.
func MustShow(t *testing.T, dir, rev, path string) string {
	t.Helper()
	content, ok := Show(t, dir, rev, path)
	if !ok {
		t.Fatalf("%s is absent from %s", path, rev)
	}
	return content
}

// MergeStateGone reports whether git considers a merge concluded, i.e. whether
// .git/MERGE_HEAD is absent.
func MergeStateGone(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD"))
	return os.IsNotExist(err)
}

// AssertMergeHead fails unless the repository is mid-merge with MERGE_HEAD
// naming want. context names the moment being asserted, so a failure says
// which step of a fixture or which post-condition broke.
func AssertMergeHead(t *testing.T, dir, want, context string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".git", "MERGE_HEAD"))
	if err != nil {
		t.Fatalf("%s: reading .git/MERGE_HEAD: %v", context, err)
	}
	if got := strings.TrimSpace(string(data)); got != want {
		t.Fatalf("%s: MERGE_HEAD = %s, want %s", context, got, want)
	}
}

// FileExists reports whether path exists.
func FileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
