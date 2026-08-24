package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// initIndexTestRepo creates a bare repo and manually initializes the safegit
// tmp directory structure (without full repo.Init). Returns the .git dir path.
func initIndexTestRepo(t *testing.T) string {
	t.Helper()
	dir := testutil.InitBareRepo(t)

	// Manually create safegit directory structure (index package doesn't
	// need full repo.Init, just the tmp dir for temporary indexes).
	sgDir := filepath.Join(dir, ".git", "safegit")
	os.MkdirAll(filepath.Join(sgDir, "tmp"), 0755)

	return filepath.Join(dir, ".git")
}

func TestNew(t *testing.T) {
	gitDir := initIndexTestRepo(t)
	sgDir := filepath.Join(gitDir, "safegit")

	// Must chdir to the repo for git commands to work
	testutil.Chdir(t, filepath.Dir(gitDir))

	idx, err := New(context.Background(), sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Cleanup()

	// Verify dir was created
	if _, err := os.Stat(idx.Dir); os.IsNotExist(err) {
		t.Fatal("tmp index dir not created")
	}

	// Verify index file was created
	if _, err := os.Stat(idx.IndexPath); os.IsNotExist(err) {
		t.Fatal("index file not created")
	}

	// Verify the dir is under safegit/tmp/
	rel, _ := filepath.Rel(filepath.Join(sgDir, "tmp"), idx.Dir)
	if filepath.IsAbs(rel) || rel == ".." || len(rel) == 0 {
		t.Errorf("tmp index dir %s is not under safegit/tmp/", idx.Dir)
	}
}

func TestCleanup(t *testing.T) {
	gitDir := initIndexTestRepo(t)
	sgDir := filepath.Join(gitDir, "safegit")

	testutil.Chdir(t, filepath.Dir(gitDir))

	idx, err := New(context.Background(), sgDir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	dir := idx.Dir
	if err := idx.Cleanup(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("tmp dir should be removed after Cleanup")
	}
}

// TestGarbageCollectPlan: the scanner names the orphan directories and removes
// nothing. Removal belongs to the caller (doctor mints it through the effects
// handle), so what is pinned here is the judgement -- a dead owner's directory
// is named, a live owner's is not -- and that the walk itself leaves the tree
// exactly as it found it.
func TestGarbageCollectPlan(t *testing.T) {
	gitDir := initIndexTestRepo(t)
	sgDir := filepath.Join(gitDir, "safegit")
	tmpBase := filepath.Join(sgDir, "tmp")

	// Create a dir with a PID that definitely doesn't exist (PID 2 is kthreadd, but
	// use a very high PID that's unlikely to exist)
	deadDir := filepath.Join(tmpBase, "999999999-abcd1234")
	os.MkdirAll(deadDir, 0755)
	os.WriteFile(filepath.Join(deadDir, "index"), []byte("fake"), 0644)

	// Create a dir with our own PID (should be kept)
	aliveDir := filepath.Join(tmpBase, "1-abcd1234") // PID 1 is always alive (init)
	os.MkdirAll(aliveDir, 0755)

	planned, err := GarbageCollectPlan(sgDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(planned) != 1 || planned[0] != deadDir {
		t.Errorf("planned = %v, want exactly [%s]", planned, deadDir)
	}

	// The scanner removes nothing: both directories are still there.
	if _, err := os.Stat(deadDir); err != nil {
		t.Errorf("the scanner removed the dead PID dir: %v", err)
	}
	if _, err := os.Stat(aliveDir); os.IsNotExist(err) {
		t.Error("alive PID dir should not be removed")
	}

	// And what it named really is removable by the caller.
	if err := os.RemoveAll(planned[0]); err != nil {
		t.Fatalf("removing the planned path: %v", err)
	}
	if _, err := os.Stat(deadDir); !os.IsNotExist(err) {
		t.Error("dead PID dir should be gone once the caller removed the planned path")
	}
}

func TestGarbageCollectPlanEmptyTmp(t *testing.T) {
	gitDir := initIndexTestRepo(t)
	sgDir := filepath.Join(gitDir, "safegit")

	planned, err := GarbageCollectPlan(sgDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned) != 0 {
		t.Errorf("planned = %v, want none", planned)
	}
}

func TestParsePIDFromDirName(t *testing.T) {
	tests := []struct {
		name    string
		wantPID int
		wantOK  bool
	}{
		{"12345-abcdef01", 12345, true},
		{"1-ff", 1, true},
		{"notanumber-abc", 0, false},
		{"nodash", 0, false},
		{"-abc", 0, false},
		{"0-abc", 0, false},
		{"-1-abc", 0, false},
	}
	for _, tt := range tests {
		pid, ok := parsePIDFromDirName(tt.name)
		if ok != tt.wantOK || pid != tt.wantPID {
			t.Errorf("parsePIDFromDirName(%q) = (%d, %v), want (%d, %v)",
				tt.name, pid, ok, tt.wantPID, tt.wantOK)
		}
	}
}
