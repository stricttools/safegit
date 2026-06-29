package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// initRepo creates a temp git repo with git config and returns the dir.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "--initial-branch=main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	return dir
}

// gitRun runs a git command in the given directory.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func TestScanObjectsFindsBlob(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Create a file containing a secret.
	os.WriteFile(filepath.Join(dir, "config.txt"), []byte("token=SECRET_123\nother=stuff\n"), 0644)
	gitRun(t, dir, "add", "config.txt")
	gitRun(t, dir, "commit", "-m", "add config")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_123`)

	results, err := ScanObjects(ctx, pattern)
	if err != nil {
		t.Fatal(err)
	}

	// Should find at least one match in a blob.
	var blobMatches []Match
	for _, m := range results.Matches {
		if m.ObjectType == "blob" {
			blobMatches = append(blobMatches, m)
		}
	}
	if len(blobMatches) == 0 {
		t.Fatal("expected at least one blob match, got none")
	}

	m := blobMatches[0]
	if m.Line != 1 {
		t.Errorf("Line = %d, want 1", m.Line)
	}
	if !m.Reachable {
		t.Error("expected match to be reachable")
	}
	if m.SHA == "" {
		t.Error("expected non-empty SHA")
	}
	// Context should contain <MATCH> and not the actual secret.
	if m.Context == "" {
		t.Error("expected non-empty context")
	}
	if !contains(m.Context, "<MATCH>") {
		t.Errorf("Context %q does not contain <MATCH>", m.Context)
	}
	if contains(m.Context, "SECRET_123") {
		t.Errorf("Context %q leaks the actual secret", m.Context)
	}
}

func TestScanObjectsFindsCommitMessage(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Create a commit whose message contains the secret.
	gitRun(t, dir, "commit", "--allow-empty", "-m", "deploy with SECRET_123 token")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_123`)

	results, err := ScanObjects(ctx, pattern)
	if err != nil {
		t.Fatal(err)
	}

	var commitMatches []Match
	for _, m := range results.Matches {
		if m.ObjectType == "commit" {
			commitMatches = append(commitMatches, m)
		}
	}
	if len(commitMatches) == 0 {
		t.Fatal("expected at least one commit match, got none")
	}

	m := commitMatches[0]
	if m.ObjectType != "commit" {
		t.Errorf("ObjectType = %q, want commit", m.ObjectType)
	}
	if !m.Reachable {
		t.Error("expected commit match to be reachable")
	}
	if !contains(m.Context, "<MATCH>") {
		t.Errorf("Context %q does not contain <MATCH>", m.Context)
	}
}

func TestScanObjectsSkipsBinary(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Create a binary file containing the secret pattern after some NUL bytes.
	binaryContent := make([]byte, 256)
	binaryContent[0] = 0x00 // NUL byte makes it binary
	copy(binaryContent[10:], []byte("SECRET_123"))
	os.WriteFile(filepath.Join(dir, "binary.dat"), binaryContent, 0644)
	gitRun(t, dir, "add", "binary.dat")
	gitRun(t, dir, "commit", "-m", "add binary")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_123`)

	results, err := ScanObjects(ctx, pattern)
	if err != nil {
		t.Fatal(err)
	}

	// The binary blob should be skipped, not matched.
	for _, m := range results.Matches {
		if m.ObjectType == "blob" && m.SHA != "" {
			// If we got a blob match, it should NOT be from the binary file.
			// We check that no match context contains the binary file's content.
			// The commit message does not contain SECRET_123, so there should be
			// no blob matches at all.
			t.Errorf("unexpected blob match: %+v", m)
		}
	}

	if results.Skipped == 0 {
		t.Error("expected at least one skipped binary blob, got 0")
	}
}

func TestScanObjectsFindsUnreachable(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Create a file with the secret and commit it.
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("key=SECRET_123\n"), 0644)
	gitRun(t, dir, "add", "secret.txt")
	gitRun(t, dir, "commit", "-m", "add secret")

	// Now amend the commit, replacing the secret. The old blob becomes unreachable.
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("key=REDACTED\n"), 0644)
	gitRun(t, dir, "add", "secret.txt")
	gitRun(t, dir, "commit", "--amend", "-m", "redacted")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_123`)

	results, err := ScanObjects(ctx, pattern)
	if err != nil {
		t.Fatal(err)
	}

	// The old blob containing SECRET_123 should still be found (unreachable).
	var unreachableMatches []Match
	for _, m := range results.Matches {
		if m.ObjectType == "blob" && !m.Reachable {
			unreachableMatches = append(unreachableMatches, m)
		}
	}
	if len(unreachableMatches) == 0 {
		t.Fatal("expected at least one unreachable blob match, got none")
	}
}

func TestScanObjectsNoMatch(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	os.WriteFile(filepath.Join(dir, "clean.txt"), []byte("nothing interesting here\n"), 0644)
	gitRun(t, dir, "add", "clean.txt")
	gitRun(t, dir, "commit", "-m", "clean commit")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_123`)

	results, err := ScanObjects(ctx, pattern)
	if err != nil {
		t.Fatal(err)
	}

	if len(results.Matches) != 0 {
		t.Errorf("expected no matches, got %d: %+v", len(results.Matches), results.Matches)
	}
	if results.Scanned == 0 {
		t.Error("expected scanned > 0")
	}
}

func TestIsBinary(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty", []byte{}, false},
		{"text only", []byte("hello world\n"), false},
		{"nul at start", []byte{0x00, 'a', 'b'}, true},
		{"nul in middle", append([]byte("hello"), 0x00, 'w', 'o', 'r', 'l', 'd'), true},
		{"nul beyond check limit", func() []byte {
			// Fill with non-zero bytes, then place a NUL past the check window.
			b := make([]byte, binaryCheckSize+1)
			for i := range b {
				b[i] = 'A'
			}
			b[binaryCheckSize] = 0x00
			return b
		}(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isBinary(tt.data)
			if got != tt.want {
				t.Errorf("isBinary(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestBuildContext(t *testing.T) {
	line := "prefix-SECRET_123-suffix"
	ctx := buildContext(line, 7, 17) // SECRET_123 starts at 7, ends at 17
	if !contains(ctx, "<MATCH>") {
		t.Errorf("context %q missing <MATCH>", ctx)
	}
	if contains(ctx, "SECRET_123") {
		t.Errorf("context %q leaks secret", ctx)
	}
	if !contains(ctx, "prefix-") {
		t.Errorf("context %q missing prefix", ctx)
	}
	if !contains(ctx, "-suffix") {
		t.Errorf("context %q missing suffix", ctx)
	}
}

func TestBuildContextLongLine(t *testing.T) {
	// Line longer than 2*contextRadius around the match.
	padding := make([]byte, 100)
	for i := range padding {
		padding[i] = 'x'
	}
	line := string(padding) + "SECRET_123" + string(padding)
	matchStart := 100
	matchEnd := 110

	ctx := buildContext(line, matchStart, matchEnd)
	if !contains(ctx, "...") {
		t.Errorf("context %q should contain ellipsis for truncation", ctx)
	}
	if !contains(ctx, "<MATCH>") {
		t.Errorf("context %q missing <MATCH>", ctx)
	}
}

func TestExtractBody(t *testing.T) {
	// Commit object format: headers, blank line, body.
	raw := []byte("tree abc123\nauthor Test\n\nThis is the message body\n")
	body := extractBody(raw)
	if string(body) != "This is the message body\n" {
		t.Errorf("extractBody = %q, want %q", body, "This is the message body\n")
	}
}

func TestExtractBodyNoBody(t *testing.T) {
	raw := []byte("tree abc123\nauthor Test")
	body := extractBody(raw)
	if body != nil {
		t.Errorf("extractBody = %q, want nil", body)
	}
}

// contains is a small helper to check substring presence.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestFindMatchesMultiLine(t *testing.T) {
	// Multi-line pattern spanning two lines: (?s)foo.*bar where foo and bar
	// are on different lines.
	content := []byte("prefix\nbegin foo secret\nbar suffix\ntrailer\n")
	pattern := regexp.MustCompile(`(?s)foo.*bar`)

	matches := findMatches("abc123", "blob", content, pattern, true)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}

	m := matches[0]
	// "foo" starts on line 2 (after "prefix\n").
	if m.Line != 2 {
		t.Errorf("Line = %d, want 2", m.Line)
	}
	if !contains(m.Context, "<MATCH>") {
		t.Errorf("Context %q missing <MATCH>", m.Context)
	}
	// Context should come from the first line of the match.
	// "begin " precedes "foo" on that line and should appear.
	if !contains(m.Context, "begin ") {
		t.Errorf("Context %q should contain text before match on the first line", m.Context)
	}
	// "bar" is on a subsequent line and should not appear in context.
	if contains(m.Context, "bar") {
		t.Errorf("Context %q should not contain text from subsequent match lines", m.Context)
	}
}

func TestFindMatchesCRLFLineEndings(t *testing.T) {
	content := []byte("line1\r\ntoken=SECRET_123\r\nline3\r\n")
	pattern := regexp.MustCompile(`SECRET_123`)

	matches := findMatches("abc123", "blob", content, pattern, true)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}

	m := matches[0]
	// With \r\n, the \n is still the line delimiter for counting.
	// "line1\r\n" has one \n, so SECRET_123 is on line 2.
	if m.Line != 2 {
		t.Errorf("Line = %d, want 2", m.Line)
	}
	if !contains(m.Context, "<MATCH>") {
		t.Errorf("Context %q missing <MATCH>", m.Context)
	}
	if contains(m.Context, "SECRET_123") {
		t.Errorf("Context %q leaks the secret", m.Context)
	}
}

func TestFindMatchesNoTrailingNewline(t *testing.T) {
	content := []byte("first line\ntoken=SECRET_123")
	pattern := regexp.MustCompile(`SECRET_123`)

	matches := findMatches("abc123", "blob", content, pattern, true)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}

	m := matches[0]
	if m.Line != 2 {
		t.Errorf("Line = %d, want 2", m.Line)
	}
	if !contains(m.Context, "<MATCH>") {
		t.Errorf("Context %q missing <MATCH>", m.Context)
	}
}

func TestFindMatchesEmptyContent(t *testing.T) {
	content := []byte("")
	pattern := regexp.MustCompile(`SECRET_123`)

	matches := findMatches("abc123", "blob", content, pattern, true)
	if len(matches) != 0 {
		t.Errorf("expected 0 matches for empty content, got %d", len(matches))
	}
}

func TestBuildContextFromContent(t *testing.T) {
	content := []byte("line one\nprefix-SECRET_123-suffix\nline three\n")
	// SECRET_123 starts at byte offset 16 (after "line one\nprefix-"), ends at 26.
	matchStart := 16
	matchEnd := 26

	ctx := buildContextFromContent(content, matchStart, matchEnd)
	if !contains(ctx, "<MATCH>") {
		t.Errorf("context %q missing <MATCH>", ctx)
	}
	if contains(ctx, "SECRET_123") {
		t.Errorf("context %q leaks secret", ctx)
	}
	if !contains(ctx, "prefix-") {
		t.Errorf("context %q missing prefix", ctx)
	}
	if !contains(ctx, "-suffix") {
		t.Errorf("context %q missing suffix", ctx)
	}
}

func TestBuildContextFromContentMultiLineMatch(t *testing.T) {
	content := []byte("aaa\nsome foo secret\nbar end\nzzz\n")
	// "aaa\n" = offsets 0-3
	// "some foo secret\n" = offsets 4-19 (\n at 19)
	// "bar end\n" = offsets 20-27
	// Match spans from "foo" at offset 9 to end of "bar" at offset 23.
	matchStart := 9
	matchEnd := 23

	ctx := buildContextFromContent(content, matchStart, matchEnd)
	if !contains(ctx, "<MATCH>") {
		t.Errorf("context %q missing <MATCH>", ctx)
	}
	// For multi-line matches, context comes from the first line only.
	// The first line is "some foo secret" (offset 4..19). relStart=5, relEnd=min(19,15)=15.
	// So before="some " and the matched portion of this line is replaced by <MATCH>.
	if !contains(ctx, "some ") {
		t.Errorf("context %q should contain text before match on first line", ctx)
	}
	// "bar" is on a subsequent line and should not appear.
	if contains(ctx, "bar") {
		t.Errorf("context %q should not contain text from subsequent match lines", ctx)
	}
}
