package test

import (
	"regexp"
	"sort"
	"testing"

	"github.com/smm-h/safegit/internal/testutil"
)

// SPEC PIN -- deliberately failing until the ruled behavior is implemented.
//
// The finding, in two halves.
//
// (1) Passthrough entries do not record branch positions. `safegit commit`
// writes an oplog entry carrying "ref" (the branch it moved), "parent" (the tip
// it moved from) and "sha" (the tip it moved to) -- internal/commit/commit.go's
// Step 8. safegit's own merge/rebase/pull/reset/bisect and the guarded
// passthroughs (cherry-pick, revert) write entries from coord_cmd.go that carry
// none of that: merge records only {"branch", "result"}, rebase only
// {"upstream"}, pull only {"remote", "branch"}, reset and bisect and every
// guarded passthrough only {"args"} -- a verbatim copy of the operator's argv.
// Not one of them names the ref that moved or the position it moved from, so
// the audit trail cannot say where a branch was before a merge, and undo
// arithmetic and bypass detection have nothing to work from.
//
// (2) A failed guarded passthrough logs an entry indistinguishable from a
// successful one. runGuardedPassthrough (coord_cmd.go) appends its entry
// UNCONDITIONALLY, after the nonzero-code branch has already run
// announceWayOut. A cherry-pick git refused and a cherry-pick git applied
// produce the same {"op":"cherry-pick","extra":{"args":...}} shape, so the log
// records an operation that did not happen exactly as if it had.
//
// The ruling: every operation records the branch and its before/after
// positions, and entries record outcome, so the audit trail never records
// operations that did not happen as if they did. The exact spelling of the
// outcome field is the implementation's choice -- a "status", an "ok" boolean,
// a marker present only on failure, an empty new tip -- so the second test
// below pins DISTINGUISHABILITY rather than any particular field name.
//
// Any existing test that pins the sparse entry shape -- anything asserting a
// merge entry carries exactly {"branch","result"}, or that a passthrough entry
// carries exactly {"args"}, or oplog.hasTipSHA/TipSHA's multi-spelling key
// search in internal/oplog/oplog.go, which exists only because each op invented
// its own new-tip key -- is a SANCTIONED REWRITE at implementation time.

// oplogExtra returns an oplog entry's "extra" object, or nil when it has none.
func oplogExtra(entry map[string]interface{}) map[string]interface{} {
	extra, _ := entry["extra"].(map[string]interface{})
	return extra
}

// oplogExtraString returns the first of the given keys present in extra with a
// non-empty string value, plus the key it came from. Several keys are accepted
// per position because the production code already treats them as synonyms:
// oplog.TipSHA reads a new tip from "sha", "to" or "result" precisely because
// commit, checkout and merge each spell it differently.
func oplogExtraString(extra map[string]interface{}, keys ...string) (value, key string) {
	for _, k := range keys {
		if v, ok := extra[k].(string); ok && v != "" {
			return v, k
		}
	}
	return "", ""
}

// TestMergeOplogEntryCarriesBranchPositions pins half (1) of the ruling on the
// merge path: a completed merge must record which ref moved and both of its
// positions, in the fields a commit entry already uses.
//
// Today this fails on the ref and the old tip: the merge entry has no "ref" key
// at all and no key naming the pre-merge tip. The new-tip assertion passes
// today under merge's own "result" spelling, which is deliberate -- that part
// of the ruling is already met and the test says so rather than manufacturing a
// failure.
func TestMergeOplogEntryCarriesBranchPositions(t *testing.T) {
	dir := newRepo(t)

	// A trivially mergeable branch: feature touches a file main never edits.
	testutil.WriteFile(t, dir, "base.txt", "base\n")
	safegitCommit(t, dir, "base", "base.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	testutil.WriteFile(t, dir, "feature.txt", "feature\n")
	safegitCommit(t, dir, "feature side", "feature.txt")

	testutil.Git(t, dir, "switch", "main")
	testutil.WriteFile(t, dir, "main.txt", "main\n")
	safegitCommit(t, dir, "main side", "main.txt")

	// The ref and the position the merge is about to move away from. The ref is
	// taken from the last commit entry rather than hardcoded, so this pins the
	// SAME spelling commit entries use rather than a guess at it.
	commits := oplogEntries(t, dir, "commit")
	if len(commits) == 0 {
		t.Fatal("fixture is wrong: no commit entries in the oplog to read the ref spelling from")
	}
	wantRef, _ := oplogExtraString(oplogExtra(commits[len(commits)-1]), "ref")
	if wantRef == "" {
		t.Fatal("fixture is wrong: the last commit entry carries no ref")
	}
	oldTip := testutil.Rev(t, dir, "HEAD")

	stdout, stderr, code := runSafegit(t, dir, "merge", "feature")
	if code != 0 {
		t.Fatalf("fixture is wrong: a trivially mergeable merge failed (code %d)\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	newTip := testutil.Rev(t, dir, "HEAD")
	if newTip == oldTip {
		t.Fatalf("fixture is wrong: the merge did not move %s off %s", wantRef, oldTip)
	}

	merges := oplogEntries(t, dir, "merge")
	if len(merges) != 1 {
		t.Fatalf("expected exactly one merge entry in the oplog, got %d", len(merges))
	}
	extra := oplogExtra(merges[0])
	if extra == nil {
		t.Fatal("the merge entry carries no extra object at all")
	}

	// The ref that moved.
	if got, _ := oplogExtraString(extra, "ref"); got != wantRef {
		t.Errorf("the merge entry records ref %q, want %q (the field commit entries populate); extra=%v",
			got, wantRef, extra)
	}
	// The position it moved FROM. "parent" is commit's spelling; "from" is
	// checkout's, and is accepted for the same reason oplog.TipSHA accepts
	// several spellings of the new tip.
	if got, key := oplogExtraString(extra, "parent", "from"); got != oldTip {
		t.Errorf("the merge entry records old tip %q (under %q), want %q; extra=%v",
			got, key, oldTip, extra)
	}
	// The position it moved TO.
	if got, key := oplogExtraString(extra, "sha", "to", "result"); got != newTip {
		t.Errorf("the merge entry records new tip %q (under %q), want %q; extra=%v",
			got, key, newTip, extra)
	}
}

// cherryPickFixture builds a repo whose "feature" branch tip cherry-picks onto
// main either cleanly or with a conflict, depending on conflicting. Both shapes
// are driven by the identical command line `safegit cherry-pick feature`, which
// is what lets the two resulting oplog entries be compared field for field: the
// operator argv the entry echoes under "args" is the same string in both.
func cherryPickFixture(t *testing.T, conflicting bool) string {
	t.Helper()
	dir := newRepo(t)

	testutil.WriteFile(t, dir, "shared.txt", "line1\nbase\nline3\n")
	safegitCommit(t, dir, "base", "shared.txt")

	testutil.Git(t, dir, "branch", "feature")
	testutil.Git(t, dir, "switch", "feature")
	if conflicting {
		testutil.WriteFile(t, dir, "shared.txt", "line1\nfeature\nline3\n")
		safegitCommit(t, dir, "feature edit", "shared.txt")
	} else {
		testutil.WriteFile(t, dir, "feature-only.txt", "only on feature\n")
		safegitCommit(t, dir, "feature edit", "feature-only.txt")
	}

	testutil.Git(t, dir, "switch", "main")
	if conflicting {
		testutil.WriteFile(t, dir, "shared.txt", "line1\nmain\nline3\n")
		safegitCommit(t, dir, "main edit", "shared.txt")
	} else {
		testutil.WriteFile(t, dir, "main-only.txt", "only on main\n")
		safegitCommit(t, dir, "main edit", "main-only.txt")
	}
	return dir
}

// shaLike reports whether a value is a hex object name. Positions differ
// between two separate fixture repositories for reasons that have nothing to do
// with outcome, so a difference in a SHA-shaped value is not evidence that the
// log distinguishes a performed operation from a refused one.
var shaLike = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

func isSHALike(v interface{}) bool {
	s, ok := v.(string)
	return ok && shaLike.MatchString(s)
}

// oplogComparableKeys returns the entry's key names -- top level plus one level
// into "extra" -- with the legitimately-varying ones removed: "ts" and "pid"
// vary per invocation and "sid" per session.
func oplogComparableKeys(entry map[string]interface{}) []string {
	var keys []string
	for k := range entry {
		switch k {
		case "ts", "pid", "sid", "extra":
			continue
		}
		keys = append(keys, k)
	}
	for k := range oplogExtra(entry) {
		keys = append(keys, "extra."+k)
	}
	sort.Strings(keys)
	return keys
}

// oplogComparableValue reads one of oplogComparableKeys' names out of an entry.
func oplogComparableValue(entry map[string]interface{}, key string) interface{} {
	if len(key) > 6 && key[:6] == "extra." {
		return oplogExtra(entry)[key[6:]]
	}
	return entry[key]
}

// TestFailedPassthroughOplogEntryIsDistinguishable pins half (2) of the ruling:
// the log must not record a passthrough git refused as if git had performed it.
//
// The assertion design. Two comparable fixtures run the byte-identical command
// line `safegit cherry-pick feature`; in one it applies, in the other it
// conflicts and exits nonzero. The test then accepts EITHER of the two shapes
// that satisfy the ruling:
//
//   - the failed run appends no entry at all -- nothing was recorded, so nothing
//     false was recorded; or
//   - the failed run's entry is distinguishable from the successful run's.
//
// "Distinguishable" is deliberately not tied to a field name, because the
// ruling leaves the spelling to the implementation. It means: after dropping
// the per-invocation fields (ts, pid, sid), either the two entries' key sets
// differ -- a marker present on one and not the other -- or some shared key
// holds different values that are not both object names. The SHA exclusion is
// what makes the assertion mean something: the two fixtures are separate
// repositories, so any recorded position differs between them for reasons
// unrelated to outcome, and a naive value comparison would go green on that
// alone. A "status"/"ok"/"performed"-style field, or an empty new tip where the
// success carries a real one, all satisfy it.
//
// Today this fails because both entries are exactly
// {"op":"cherry-pick","extra":{"args":"feature"}}: same keys, same values, no
// record anywhere that one of the two operations never happened.
func TestFailedPassthroughOplogEntryIsDistinguishable(t *testing.T) {
	okDir := cherryPickFixture(t, false)
	badDir := cherryPickFixture(t, true)

	if _, stderr, code := runSafegit(t, okDir, "cherry-pick", "feature"); code != 0 {
		t.Fatalf("fixture is wrong: the clean cherry-pick failed (code %d): %s", code, stderr)
	}
	stdout, stderr, code := runSafegit(t, badDir, "cherry-pick", "feature")
	if code == 0 {
		t.Fatalf("fixture is wrong: the conflicting cherry-pick succeeded\nstdout=%s\nstderr=%s", stdout, stderr)
	}

	okEntries := oplogEntries(t, okDir, "cherry-pick")
	if len(okEntries) != 1 {
		t.Fatalf("expected exactly one cherry-pick entry after the successful run, got %d", len(okEntries))
	}
	badEntries := oplogEntries(t, badDir, "cherry-pick")
	if len(badEntries) == 0 {
		// The other shape that satisfies the ruling: the refused operation was
		// never recorded, so the log cannot claim it happened.
		t.Log("the failed cherry-pick appended no oplog entry; the audit trail records nothing that did not happen")
		return
	}
	if len(badEntries) != 1 {
		t.Fatalf("expected at most one cherry-pick entry after the failed run, got %d", len(badEntries))
	}

	ok, bad := okEntries[0], badEntries[0]
	okKeys, badKeys := oplogComparableKeys(ok), oplogComparableKeys(bad)

	if len(okKeys) != len(badKeys) {
		return // a marker exists on one and not the other
	}
	for i := range okKeys {
		if okKeys[i] != badKeys[i] {
			return // ditto
		}
	}
	for _, k := range okKeys {
		okVal, badVal := oplogComparableValue(ok, k), oplogComparableValue(bad, k)
		if okVal == badVal {
			continue
		}
		if isSHALike(okVal) && isSHALike(badVal) {
			// Two different repositories hold different objects; that is not an
			// outcome distinction.
			continue
		}
		return // a non-positional field distinguishes performed from refused
	}

	t.Errorf("the failed cherry-pick's oplog entry is indistinguishable from the successful one; "+
		"nothing records that git refused the operation.\n  successful: %v\n  failed:     %v\n"+
		"  compared keys (ts/pid/sid dropped, object names excluded): %v",
		ok, bad, okKeys)
}
