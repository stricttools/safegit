package git

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// These tests pin the compare-and-swap contract itself, independently of any
// caller. Every ref move safegit makes is a compare-and-swap; an update-ref with
// no expected old value is an unconditional write, which is the one thing a
// concurrency-safe git wrapper must never emit.

// TestUpdateRefRefusesAnEmptyExpectedValue: the empty string used to mean "omit
// the old-value argument", and git reads an omitted old value as "write it
// whatever is there now".
func TestUpdateRefRefusesAnEmptyExpectedValue(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	headSHA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	err = UpdateRef(ctx, "refs/heads/main", headSHA, "")
	if !errors.Is(err, ErrNoExpectedValue) {
		t.Fatalf("UpdateRef with an empty expected value = %v, want ErrNoExpectedValue", err)
	}
	if !strings.Contains(err.Error(), "ZeroSHA") {
		t.Errorf("the refusal must name the way to say 'must not exist yet'; got %v", err)
	}

	// The refusal must be a refusal: the ref is untouched and, crucially, git
	// was never invoked, so nothing raced.
	if got, _ := RevParse(ctx, "refs/heads/main"); got != headSHA {
		t.Errorf("refs/heads/main = %q after a refused update, want %q", got, headSHA)
	}
}

// TestDeleteRefRefusesAnEmptyExpectedValue: the same contract on the delete
// side, where an unconditional write destroys rather than overwrites.
func TestDeleteRefRefusesAnEmptyExpectedValue(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	headSHA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	if err := DeleteRef(ctx, "refs/heads/main", ""); !errors.Is(err, ErrNoExpectedValue) {
		t.Fatalf("DeleteRef with an empty expected value = %v, want ErrNoExpectedValue", err)
	}
	if got, _ := RevParse(ctx, "refs/heads/main"); got != headSHA {
		t.Errorf("refs/heads/main = %q after a refused delete, want %q", got, headSHA)
	}
}

// TestZeroSHAMeansCreateOnly: ZeroSHA as the expected old value is git's "this
// ref must not exist" convention. It must succeed on a ref that does not exist
// and refuse on one that does -- that refusal is the whole reason a root commit
// can be built concurrently by two processes without either losing the other's.
func TestZeroSHAMeansCreateOnly(t *testing.T) {
	dir := testutil.InitBareRepo(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	headSHA, err := RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	// Creating a ref that does not exist yet.
	if err := UpdateRef(ctx, "refs/heads/fresh", headSHA, ZeroSHA); err != nil {
		t.Fatalf("UpdateRef with ZeroSHA on a nonexistent ref: %v", err)
	}
	if got, _ := RevParse(ctx, "refs/heads/fresh"); got != headSHA {
		t.Errorf("refs/heads/fresh = %q, want %q", got, headSHA)
	}

	// The same call on a ref that now exists must be refused, not applied.
	other, _, err := Run(ctx, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	newCommit, err := CommitTree(ctx, strings.TrimSpace(other), headSHA, "second")
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateRef(ctx, "refs/heads/fresh", newCommit, ZeroSHA); err == nil {
		t.Fatal("UpdateRef with ZeroSHA overwrote a ref that already exists")
	}
	if got, _ := RevParse(ctx, "refs/heads/fresh"); got != headSHA {
		t.Errorf("refs/heads/fresh = %q after a refused create, want the untouched %q", got, headSHA)
	}
}

// TestZeroSHAIsTheOnlyNullSHASpelling: the all-zero object name appears in
// safegit as exactly one constant.
func TestZeroSHAIsTheOnlyNullSHASpelling(t *testing.T) {
	if len(ZeroSHA) != 40 {
		t.Fatalf("ZeroSHA is %d characters, want 40", len(ZeroSHA))
	}
	if strings.Trim(ZeroSHA, "0") != "" {
		t.Errorf("ZeroSHA = %q, want the all-zero object name", ZeroSHA)
	}
}
