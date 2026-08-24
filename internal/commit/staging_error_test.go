package commit

import (
	"errors"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/stage"
)

// A hunk-staging failure that is not one of the recognized refusals still has to
// name the file it happened on: the stage package is handed the absolute path
// and returns git's reason alone, so the repo-relative spelling exists only
// here. This is the only pin on that wrap for the general failure -- the
// integration pin (TestStagingFailureNamesTheRepoRelativePath) drives it through
// the binary-file refusal, which carries its own exit code and takes the other
// branch of this function.
//
// It became reachable when the silent --3way retry inside stage.ApplyPatch was
// deleted: a patch that does not apply used to be merged in on a second attempt
// and reported as success.
func TestStagingHunksErrorNamesTheRepoRelativePath(t *testing.T) {
	underlying := errors.New("patch apply failed: error: sub/a.go: patch does not apply")

	err := stagingHunksError("sub/a.go", underlying)

	if !strings.Contains(err.Error(), "sub/a.go") {
		t.Errorf("the failure does not name the file: %v", err)
	}
	if !errors.Is(err, underlying) {
		t.Errorf("the wrap dropped git's own reason: %v", err)
	}
	var ce *CommitError
	if errors.As(err, &ce) {
		t.Errorf("a general staging failure was given the code of a specific refusal: %d", ce.Code)
	}
}

// The binary-file refusal keeps its own exit code through the same wrap, which
// is what lets a caller act on it rather than reading an undifferentiated
// failure.
func TestStagingHunksErrorKeepsTheBinaryRefusalCode(t *testing.T) {
	err := stagingHunksError("sub/blob.bin", stage.ErrBinaryFile)

	var ce *CommitError
	if !errors.As(err, &ce) {
		t.Fatalf("the binary refusal lost its code: %v", err)
	}
	if ce.Code != exitcode.BinaryHunkSpec {
		t.Errorf("the binary refusal exits %d, want %d", ce.Code, exitcode.BinaryHunkSpec)
	}
	if !strings.Contains(err.Error(), "sub/blob.bin") {
		t.Errorf("the refusal does not name the file: %v", err)
	}
}
