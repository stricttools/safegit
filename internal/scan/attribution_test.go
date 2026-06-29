package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

func TestAddAttribution(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Create a file with a secret and commit it.
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("token=SECRET_ATTR_123\nother=clean\n"), 0644)
	gitRun(t, dir, "add", "secret.txt")
	gitRun(t, dir, "commit", "-m", "add secret file")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_ATTR_123`)
	opts := entireHistoryOpts()

	results, err := ScanObjects(ctx, pattern, opts)
	if err != nil {
		t.Fatal(err)
	}

	// Should have at least one blob match with empty Path.
	var blobMatch *Match
	for i, m := range results.Matches {
		if m.ObjectType == "blob" {
			blobMatch = &results.Matches[i]
			break
		}
	}
	if blobMatch == nil {
		t.Fatal("expected at least one blob match before attribution")
	}
	if blobMatch.Path != "" {
		t.Errorf("Path should be empty before attribution, got %q", blobMatch.Path)
	}
	if blobMatch.CommitSHA != "" {
		t.Errorf("CommitSHA should be empty before attribution, got %q", blobMatch.CommitSHA)
	}

	// Run attribution.
	if err := AddAttribution(ctx, results, opts); err != nil {
		t.Fatal(err)
	}

	// Find the blob match again (same index).
	blobMatch = nil
	for i, m := range results.Matches {
		if m.ObjectType == "blob" {
			blobMatch = &results.Matches[i]
			break
		}
	}
	if blobMatch == nil {
		t.Fatal("blob match disappeared after attribution")
	}

	if blobMatch.Path != "secret.txt" {
		t.Errorf("Path = %q, want %q", blobMatch.Path, "secret.txt")
	}
	if blobMatch.CommitSHA == "" {
		t.Error("CommitSHA should be populated after attribution")
	}
}

func TestAddAttributionSubdirectory(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Create a file in a subdirectory.
	subDir := filepath.Join(dir, "config", "env")
	os.MkdirAll(subDir, 0755)
	os.WriteFile(filepath.Join(subDir, "prod.conf"), []byte("api_key=SECRET_SUBDIR_456\n"), 0644)
	gitRun(t, dir, "add", "config/env/prod.conf")
	gitRun(t, dir, "commit", "-m", "add nested config")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_SUBDIR_456`)
	opts := entireHistoryOpts()

	results, err := ScanObjects(ctx, pattern, opts)
	if err != nil {
		t.Fatal(err)
	}

	if err := AddAttribution(ctx, results, opts); err != nil {
		t.Fatal(err)
	}

	var blobMatch *Match
	for i, m := range results.Matches {
		if m.ObjectType == "blob" {
			blobMatch = &results.Matches[i]
			break
		}
	}
	if blobMatch == nil {
		t.Fatal("expected blob match")
	}
	if blobMatch.Path != "config/env/prod.conf" {
		t.Errorf("Path = %q, want %q", blobMatch.Path, "config/env/prod.conf")
	}
}

func TestAddAttributionUnreachableBlob(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Create a file with a secret, commit, then amend to remove it.
	os.WriteFile(filepath.Join(dir, "temp.txt"), []byte("password=SECRET_ORPHAN_789\n"), 0644)
	gitRun(t, dir, "add", "temp.txt")
	gitRun(t, dir, "commit", "-m", "add temp")

	// Amend to replace the secret -- old blob becomes unreachable.
	os.WriteFile(filepath.Join(dir, "temp.txt"), []byte("password=REDACTED\n"), 0644)
	gitRun(t, dir, "add", "temp.txt")
	gitRun(t, dir, "commit", "--amend", "-m", "redacted")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_ORPHAN_789`)
	opts := entireHistoryOpts()

	results, err := ScanObjects(ctx, pattern, opts)
	if err != nil {
		t.Fatal(err)
	}

	// Should find the unreachable blob.
	var unreachableMatch *Match
	for i, m := range results.Matches {
		if m.ObjectType == "blob" && !m.Reachable {
			unreachableMatch = &results.Matches[i]
			break
		}
	}
	if unreachableMatch == nil {
		t.Fatal("expected unreachable blob match")
	}

	if err := AddAttribution(ctx, results, opts); err != nil {
		t.Fatal(err)
	}

	// Unreachable blobs are not in rev-list output, so Path/CommitSHA stay empty.
	if unreachableMatch.Path != "" {
		t.Errorf("unreachable blob Path = %q, want empty", unreachableMatch.Path)
	}
	if unreachableMatch.CommitSHA != "" {
		t.Errorf("unreachable blob CommitSHA = %q, want empty", unreachableMatch.CommitSHA)
	}
}

func TestAddAttributionNilResults(t *testing.T) {
	ctx := context.Background()
	// Should not panic or error on nil results.
	if err := AddAttribution(ctx, nil, ScanOpts{}); err != nil {
		t.Errorf("AddAttribution(nil) = %v, want nil", err)
	}
}

func TestAddAttributionNoBlobs(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Commit with secret in message only (no blob match).
	gitRun(t, dir, "commit", "--allow-empty", "-m", "deploy SECRET_NOMATCH_111")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_NOMATCH_111`)
	opts := entireHistoryOpts()

	results, err := ScanObjects(ctx, pattern, opts)
	if err != nil {
		t.Fatal(err)
	}

	// Only commit matches, no blobs. AddAttribution should be a no-op.
	for _, m := range results.Matches {
		if m.ObjectType == "blob" {
			t.Fatal("unexpected blob match in message-only test")
		}
	}

	if err := AddAttribution(ctx, results, opts); err != nil {
		t.Fatal(err)
	}
}

func TestScanNonObjectsConfig(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	gitDir := filepath.Join(dir, ".git")

	// Plant a secret in .git/config (simulating a remote URL with a token).
	configPath := filepath.Join(gitDir, "config")
	existing, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	secretLine := "\n[remote \"origin\"]\n\turl = https://token:SECRET_CONFIG_999@github.com/user/repo.git\n"
	os.WriteFile(configPath, append(existing, []byte(secretLine)...), 0644)

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_CONFIG_999`)

	matches, err := ScanNonObjects(ctx, pattern, gitDir)
	if err != nil {
		t.Fatal(err)
	}

	var configMatch *Match
	for i, m := range matches {
		if strings.Contains(m.Path, "config") && !strings.Contains(m.Path, "hooks") {
			configMatch = &matches[i]
			break
		}
	}
	if configMatch == nil {
		t.Fatal("expected match in .git/config")
	}
	if configMatch.ObjectType != "file" {
		t.Errorf("ObjectType = %q, want %q", configMatch.ObjectType, "file")
	}
	if configMatch.Line == 0 {
		t.Error("expected non-zero line number")
	}
	if !contains(configMatch.Context, "<MATCH>") {
		t.Errorf("Context %q does not contain <MATCH>", configMatch.Context)
	}
}

func TestScanNonObjectsHook(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	gitDir := filepath.Join(dir, ".git")

	// Create a hook script with a secret.
	hooksDir := filepath.Join(gitDir, "hooks")
	os.MkdirAll(hooksDir, 0755)
	hookContent := "#!/bin/sh\ncurl -H 'Authorization: SECRET_HOOK_888' https://example.com\n"
	hookPath := filepath.Join(hooksDir, "pre-commit")
	os.WriteFile(hookPath, []byte(hookContent), 0755)

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_HOOK_888`)

	matches, err := ScanNonObjects(ctx, pattern, gitDir)
	if err != nil {
		t.Fatal(err)
	}

	var hookMatch *Match
	for i, m := range matches {
		if strings.Contains(m.Path, "pre-commit") {
			hookMatch = &matches[i]
			break
		}
	}
	if hookMatch == nil {
		t.Fatal("expected match in .git/hooks/pre-commit")
	}
	if hookMatch.ObjectType != "file" {
		t.Errorf("ObjectType = %q, want %q", hookMatch.ObjectType, "file")
	}
	if hookMatch.Line != 2 {
		t.Errorf("Line = %d, want 2", hookMatch.Line)
	}
}

func TestScanNonObjectsHookSkipsSample(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	gitDir := filepath.Join(dir, ".git")

	// Create a .sample hook with a secret -- should be skipped.
	hooksDir := filepath.Join(gitDir, "hooks")
	os.MkdirAll(hooksDir, 0755)
	os.WriteFile(filepath.Join(hooksDir, "pre-commit.sample"),
		[]byte("#!/bin/sh\nSECRET_SAMPLE_777\n"), 0755)

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_SAMPLE_777`)

	matches, err := ScanNonObjects(ctx, pattern, gitDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range matches {
		if strings.Contains(m.Path, ".sample") {
			t.Errorf("should skip .sample hook files, but found match in %s", m.Path)
		}
	}
}

func TestScanNonObjectsWorkingTree(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	gitDir := filepath.Join(dir, ".git")

	// Create a tracked file with a secret in the working tree.
	os.WriteFile(filepath.Join(dir, "env.sh"), []byte("export API_KEY=SECRET_WT_666\n"), 0644)
	gitRun(t, dir, "add", "env.sh")
	gitRun(t, dir, "commit", "-m", "add env")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_WT_666`)

	matches, err := ScanNonObjects(ctx, pattern, gitDir)
	if err != nil {
		t.Fatal(err)
	}

	var wtMatch *Match
	for i, m := range matches {
		if strings.Contains(m.Path, "env.sh") {
			wtMatch = &matches[i]
			break
		}
	}
	if wtMatch == nil {
		t.Fatal("expected match in working tree file env.sh")
	}
	if wtMatch.ObjectType != "file" {
		t.Errorf("ObjectType = %q, want %q", wtMatch.ObjectType, "file")
	}
	if wtMatch.Line != 1 {
		t.Errorf("Line = %d, want 1", wtMatch.Line)
	}
}

func TestScanNonObjectsSkipsBinary(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	gitDir := filepath.Join(dir, ".git")

	// Create a binary file with the secret.
	binaryContent := make([]byte, 256)
	binaryContent[0] = 0x00
	copy(binaryContent[10:], []byte("SECRET_BIN_555"))
	os.WriteFile(filepath.Join(dir, "binary.dat"), binaryContent, 0644)
	gitRun(t, dir, "add", "binary.dat")
	gitRun(t, dir, "commit", "-m", "add binary")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_BIN_555`)

	matches, err := ScanNonObjects(ctx, pattern, gitDir)
	if err != nil {
		t.Fatal(err)
	}

	for _, m := range matches {
		if strings.Contains(m.Path, "binary.dat") {
			t.Error("should skip binary files, but found match in binary.dat")
		}
	}
}

func TestScanNonObjectsMissingGitDir(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	// Use a non-existent gitDir -- should not panic, should skip missing files.
	fakeGitDir := filepath.Join(dir, ".nonexistent-git")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET`)

	matches, err := ScanNonObjects(ctx, pattern, fakeGitDir)
	if err != nil {
		t.Fatal(err)
	}

	// Working tree files may still match (ls-files still works), but git internal
	// files should be gracefully skipped.
	_ = matches
}

// gitOutput runs a git command and returns its stdout.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func TestAddAttributionCommitSHAIsValid(t *testing.T) {
	dir := initRepo(t)
	testutil.Chdir(t, dir)

	os.WriteFile(filepath.Join(dir, "creds.txt"), []byte("password=SECRET_VALID_222\n"), 0644)
	gitRun(t, dir, "add", "creds.txt")
	gitRun(t, dir, "commit", "-m", "add creds")

	// Get the actual commit SHA for verification.
	commitSHA := gitOutput(t, dir, "rev-parse", "HEAD")

	ctx := context.Background()
	pattern := regexp.MustCompile(`SECRET_VALID_222`)
	opts := entireHistoryOpts()

	results, err := ScanObjects(ctx, pattern, opts)
	if err != nil {
		t.Fatal(err)
	}

	if err := AddAttribution(ctx, results, opts); err != nil {
		t.Fatal(err)
	}

	var blobMatch *Match
	for i, m := range results.Matches {
		if m.ObjectType == "blob" {
			blobMatch = &results.Matches[i]
			break
		}
	}
	if blobMatch == nil {
		t.Fatal("expected blob match")
	}

	// CommitSHA should be the full SHA of the commit we just created.
	if blobMatch.CommitSHA != commitSHA {
		t.Errorf("CommitSHA = %q, want %q", blobMatch.CommitSHA, commitSHA)
	}
}
