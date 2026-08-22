package conflict_test

import (
	"context"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/conflict"
	"github.com/smm-h/safegit/internal/testutil"
)

func TestRegionsFindsACompleteBlock(t *testing.T) {
	content := "head\n<<<<<<< HEAD\nmine\n=======\nyours\n>>>>>>> feature\ntail\n"

	got := conflict.Regions([]byte(content), conflict.DefaultMarkerSize)
	if len(got) != 1 {
		t.Fatalf("found %d region(s), want 1: %+v", len(got), got)
	}
	if got[0].StartLine != 2 || got[0].EndLine != 6 {
		t.Errorf("region spans lines %d-%d, want 2-6", got[0].StartLine, got[0].EndLine)
	}
	want := "<<<<<<< HEAD\nmine\n=======\nyours\n>>>>>>> feature\n"
	if string(got[0].Bytes) != want {
		t.Errorf("region bytes = %q, want %q", got[0].Bytes, want)
	}
}

// TestRegionsIgnoresAnIncompleteBlock is what keeps prose about conflict markers
// committable: a file that shows an opening marker, or a separator, without the
// whole block is not a conflict.
func TestRegionsIgnoresAnIncompleteBlock(t *testing.T) {
	for name, content := range map[string]string{
		"opener alone":          "<<<<<<< HEAD\nmine\n",
		"opener and separator":  "<<<<<<< HEAD\nmine\n=======\nyours\n",
		"closer alone":          ">>>>>>> feature\n",
		"closer before opener":  ">>>>>>> feature\n<<<<<<< HEAD\nmine\n",
		"closer with no middle": "<<<<<<< HEAD\n>>>>>>> feature\n",
		"markers mid-line":      "prefix <<<<<<< HEAD\nmine\nprefix =======\nyours\nprefix >>>>>>> f\n",
		"run too short":         "<<<<<< HEAD\nmine\n======\nyours\n>>>>>> feature\n",
	} {
		if got := conflict.Regions([]byte(content), conflict.DefaultMarkerSize); len(got) != 0 {
			t.Errorf("%s: found %d region(s), want none: %+v", name, len(got), got)
		}
	}
}

func TestRegionsHonorsTheMarkerSize(t *testing.T) {
	long := "<<<<<<<<<<<< HEAD\nmine\n============\nyours\n>>>>>>>>>>>> feature\n"

	// Twelve-character markers are a region at size 12 and also at the default
	// 7, because the marker length is a minimum -- a longer run is still a
	// marker, which is how git's own detection reads it.
	if got := conflict.Regions([]byte(long), 12); len(got) != 1 {
		t.Errorf("at size 12: found %d region(s), want 1", len(got))
	}
	if got := conflict.Regions([]byte(long), conflict.DefaultMarkerSize); len(got) != 1 {
		t.Errorf("at the default size: found %d region(s), want 1", len(got))
	}
	// Default-length markers are NOT a region where the attributes say 12.
	short := "<<<<<<< HEAD\nmine\n=======\nyours\n>>>>>>> feature\n"
	if got := conflict.Regions([]byte(short), 12); len(got) != 0 {
		t.Errorf("at size 12, 7-character markers found %d region(s), want none", len(got))
	}
}

func TestRegionsFindsSeveralBlocksAndDiff3Bases(t *testing.T) {
	content := "a\n<<<<<<< HEAD\nmine\n||||||| base\nwas\n=======\nyours\n>>>>>>> feature\nb\n" +
		"<<<<<<< HEAD\nmine2\n=======\nyours2\n>>>>>>> feature\n"

	got := conflict.Regions([]byte(content), conflict.DefaultMarkerSize)
	if len(got) != 2 {
		t.Fatalf("found %d region(s), want 2: %+v", len(got), got)
	}
	if !strings.Contains(string(got[0].Bytes), "||||||| base") {
		t.Errorf("the diff3 base section is not part of the region: %q", got[0].Bytes)
	}
	if got[1].StartLine != 10 {
		t.Errorf("the second region starts at line %d, want 10", got[1].StartLine)
	}
}

func TestRegionsHandlesAMissingFinalNewline(t *testing.T) {
	content := "<<<<<<< HEAD\nmine\n=======\nyours\n>>>>>>> feature"

	got := conflict.Regions([]byte(content), conflict.DefaultMarkerSize)
	if len(got) != 1 {
		t.Fatalf("found %d region(s), want 1", len(got))
	}
	if string(got[0].Bytes) != content {
		t.Errorf("region bytes = %q, want the whole content %q", got[0].Bytes, content)
	}
}

func TestContainsRegionIsAnchoredToALineStart(t *testing.T) {
	block := []byte("<<<<<<< HEAD\nmine\n=======\nyours\n>>>>>>> feature\n")

	line, found := conflict.ContainsRegion([]byte("one\ntwo\n"+string(block)+"tail\n"), block)
	if !found || line != 3 {
		t.Errorf("found=%v line=%d, want true at line 3", found, line)
	}
	if _, found := conflict.ContainsRegion([]byte("prefix "+string(block)), block); found {
		t.Error("a block starting mid-line was reported as present")
	}
	if _, found := conflict.ContainsRegion([]byte("something else\n"), block); found {
		t.Error("an absent block was reported as present")
	}
}

// TestRegionsMatchWhatGitEmitted ties the parser to reality: the regions found
// in the file git wrote are the same bytes the reconstruction produces, so the
// verification's two sources of truth agree on where a region begins and ends.
func TestRegionsMatchWhatGitEmitted(t *testing.T) {
	dir := conflictedMerge(t)
	testutil.Chdir(t, dir)
	ctx := context.Background()

	blob, ok, err := conflict.AutoMergeBlob(ctx, "f.txt")
	if err != nil || !ok {
		t.Fatalf("no AUTO_MERGE content: ok=%v err=%v", ok, err)
	}
	emitted := conflict.Regions(blob, conflict.DefaultMarkerSize)
	if len(emitted) != 1 {
		t.Fatalf("git's own conflicted file holds %d region(s), want 1:\n%s", len(emitted), blob)
	}

	onDisk := []byte(worktreeFile(t, dir, "f.txt"))
	line, found := conflict.ContainsRegion(onDisk, emitted[0].Bytes)
	if !found {
		t.Fatalf("the region git emitted is not found in the file git wrote:\n%s", onDisk)
	}
	if line != emitted[0].StartLine {
		t.Errorf("the region starts at line %d on disk and line %d in AUTO_MERGE", line, emitted[0].StartLine)
	}

	// A resolved file no longer contains it, which is the whole signal.
	resolved := []byte("l1\nresolved\nl3\n")
	if _, found := conflict.ContainsRegion(resolved, emitted[0].Bytes); found {
		t.Error("a resolved file was reported as still holding the emitted region")
	}
}
