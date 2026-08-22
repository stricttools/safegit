package test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
	"github.com/smm-h/safegit/internal/testutil"
	"github.com/smm-h/safegit/internal/trailer"
)

// A move record is a REFERENCE to a path. When a rewrite erases that path from
// history, the record still pointing at it is one more copy of the thing being
// erased -- in the one place a tree rewrite does not reach, the commit message.
//
// So `scrub file --delete` removes the records naming the path IN THE SAME
// REWRITE, and declares the message change so the rewrite's own verification
// accounts for it. `--replace-with` does not: replacing a file's contents keeps
// the path, so a record naming it is still true.

// seedMovedSecret builds a history whose move record names the path a scrub is
// about to reach for, and returns the repository and the record's id.
func seedMovedSecret(t *testing.T) (string, string) {
	t.Helper()
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "secret.env", "TOKEN=hunter2\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed the secret", "--", "secret.env"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "move the secret into config",
		"secret.env -> config/secret.env"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	records := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
	if len(records) != 1 {
		t.Fatalf("the fixture declared no record: %v", records)
	}
	return dir, records[0][0]
}

// moveCommitMessage finds the rewritten commit whose subject is the move's and
// returns its message.
func moveCommitMessage(t *testing.T, dir string) string {
	t.Helper()
	sha := strings.TrimSpace(testutil.Git(t, dir, "log", "--format=%H", "--grep", "move the secret into config"))
	if sha == "" {
		t.Fatal("the move commit is not in the rewritten history")
	}
	return commitMessageOf(t, dir, sha)
}

func TestScrubFileDeleteRemovesTheRecordsNamingThePath(t *testing.T) {
	dir, id := seedMovedSecret(t)

	stdout, stderr, code := runSafegit(t, dir, "scrub", "file", "--approve-consequential",
		"--delete", "--entire-history", "--reason", "the token leaked", "config/secret.env")
	if code != 0 {
		t.Fatalf("scrub file --delete failed (code %d): %s\n%s", code, stderr, stdout)
	}

	msg := moveCommitMessage(t, dir)
	if strings.Contains(msg, "Moved:") {
		t.Errorf("the record naming the erased path survived the rewrite:\n%s", msg)
	}
	if strings.Contains(msg, id) {
		t.Errorf("the erased record's id survived:\n%s", msg)
	}
	if !strings.HasPrefix(msg, "move the secret into config") {
		t.Errorf("the rewrite changed more of the message than the record:\n%s", msg)
	}
	// The rewrite's own verification accounts for the message change rather
	// than refusing it as a change nothing asked for.
	if strings.Contains(stderr, "no operation asked for") {
		t.Errorf("verification treated the record removal as undeclared: %s", stderr)
	}
	if !strings.Contains(stdout, "commit message") {
		t.Errorf("the summary does not report the message change:\n%s", stdout)
	}
}

func TestScrubFileReplaceWithKeepsTheRecords(t *testing.T) {
	dir, _ := seedMovedSecret(t)

	sanitized := filepath.Join(t.TempDir(), "sanitized.env")
	testutil.WriteFileAt(t, sanitized, "TOKEN=redacted\n")

	if _, stderr, code := runSafegit(t, dir, "scrub", "file", "--approve-consequential",
		"--replace-with", sanitized, "--entire-history", "--reason", "the token leaked",
		"config/secret.env"); code != 0 {
		t.Fatalf("scrub file --replace-with failed (code %d): %s", code, stderr)
	}

	msg := moveCommitMessage(t, dir)
	// The path is still there, holding sanitized content: a record naming it is
	// still a true statement about where the content went.
	if !strings.Contains(msg, "Moved:") {
		t.Errorf("a content replacement removed a record that is still true:\n%s", msg)
	}
	if got := testutil.Git(t, dir, "show", "HEAD:config/secret.env"); got != "TOKEN=redacted" {
		t.Errorf("the replacement content is %q", got)
	}
}

// TestScrubMatchRewritesInsideARecordWithoutBreakingIt is the trailer-aware
// half of the same problem. A commit message is not free text where a move
// record sits in it: the record's paths are C-quoted, and a substitution
// applied to the raw line can inject a delimiter, end a quoted region early or
// leave a backslash with nothing after it -- and what the rewrite produces is
// then a claim nobody can read.
//
// The replacement here injects a double quote, which is the quoting
// delimiter itself: applied to the raw line it opens a quoted region that never
// closes. Applied to the DECODED path and re-encoded, it is an ordinary path
// that happens to need quoting.
func TestScrubMatchRewritesInsideARecordWithoutBreakingIt(t *testing.T) {
	dir := newRepo(t)
	testutil.WriteFile(t, dir, "secret.txt", "value: hunter2\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add the file", "--", "secret.txt"); code != 0 {
		t.Fatalf("seed commit failed (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "put it under config",
		"secret.txt -> config/secret.txt"); code != 0 {
		t.Fatalf("mv failed (code %d): %s", code, stderr)
	}
	id := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))[0][0]

	if _, stderr, code := runSafegit(t, dir, "scrub", "match", "--approve-consequential",
		"--pattern", "secret", "--replace", `a"b`, "--entire-history",
		"--reason", "the name leaked"); code != 0 {
		t.Fatalf("scrub match failed (code %d): %s", code, stderr)
	}

	sha := strings.TrimSpace(testutil.Git(t, dir, "log", "--format=%H", "--grep", "put it under config"))
	if sha == "" {
		t.Fatal("the move commit is not in the rewritten history")
	}
	msg := commitMessageOf(t, dir, sha)

	moves := trailer.ReadMoves(msg)
	if len(moves.Malformed) != 0 {
		t.Fatalf("the rewrite produced a malformed record %v in:\n%s", moves.Malformed, msg)
	}
	if len(moves.Records) != 1 {
		t.Fatalf("expected one readable record in:\n%s", msg)
	}
	r := moves.Records[0]
	if r.ID != id {
		t.Errorf("the record's id was rewritten: %q, want %q", r.ID, id)
	}
	if r.Old != `a"b.txt` || r.New != `config/a"b.txt` {
		t.Errorf("the record's paths are %q -> %q, want the substituted ones", r.Old, r.New)
	}
	if strings.Contains(msg, "secret") {
		t.Errorf("the pattern survived in the message:\n%s", msg)
	}
}

func TestScrubFileDeleteLeavesUnrelatedRecordsAlone(t *testing.T) {
	dir, _ := seedMovedSecret(t)

	testutil.WriteFile(t, dir, "notes.txt", "notes\n")
	if _, stderr, code := runSafegit(t, dir, "commit", "-m", "add notes", "--", "notes.txt"); code != 0 {
		t.Fatalf("commit failed (code %d): %s", code, stderr)
	}
	if _, stderr, code := runSafegit(t, dir, "mv", "-m", "rename the notes", "notes.txt -> README.txt"); code != 0 {
		t.Fatalf("second mv failed (code %d): %s", code, stderr)
	}

	if _, stderr, code := runSafegit(t, dir, "scrub", "file", "--approve-consequential",
		"--delete", "--entire-history", "--reason", "the token leaked", "config/secret.env"); code != 0 {
		t.Fatalf("scrub failed (code %d): %s", code, stderr)
	}

	notes := strings.TrimSpace(testutil.Git(t, dir, "log", "--format=%H", "--grep", "rename the notes"))
	if notes == "" {
		t.Fatal("the unrelated move commit is not in the rewritten history")
	}
	if records := movedRecordsIn(t, commitMessageOf(t, dir, notes)); len(records) != 1 {
		t.Errorf("an unrelated record was removed: %v", records)
	}
}

// TestScrubMatchRefusesATransformThatBreaksARecord is the limit of the
// trailer-aware transform, and the refusal it produces.
//
// Rewriting the DECODED paths and re-encoding them keeps the QUOTING readable
// whatever the substitution did -- but the pair still has to be a move. Three
// substitution shapes leave something that is not one: both sides mapped onto a
// single path, a subtree marker eaten on one side only, and an emptied token.
// The line that would be written is one the decoder refuses, and because a
// malformed record is skipped by every reader, it would sit inert in the
// rewritten history with nothing ever reporting it again.
//
// So the whole rewrite is refused, at the same guarantee Tier A gives: exit 30,
// before any ref moves, with the original history exactly as it was.
func TestScrubMatchRefusesATransformThatBreaksARecord(t *testing.T) {
	cases := []struct {
		name       string
		move       string
		pattern    string
		replace    string
		wantResult string // the pair the refusal says would have been written
	}{
		{
			name:       "both paths become one",
			move:       "z.txt -> a.txt",
			pattern:    `[az]\.txt`,
			replace:    "q.txt",
			wantResult: "q.txt -> q.txt",
		},
		{
			name:       "the subtree marker survives on one side only",
			move:       "src/ -> lib/",
			pattern:    `src/`,
			replace:    "src",
			wantResult: "src -> lib/",
		},
		{
			name:       "a token is emptied",
			move:       "z.txt -> a.txt",
			pattern:    `z\.txt`,
			replace:    "",
			wantResult: `"" -> a.txt`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := newRepo(t)
			testutil.WriteFile(t, dir, "z.txt", "z\n")
			testutil.WriteFile(t, dir, "src/one.txt", "1\n")
			if _, stderr, code := runSafegit(t, dir, "commit", "-m", "seed", "--", "z.txt", "src"); code != 0 {
				t.Fatalf("seed commit failed (code %d): %s", code, stderr)
			}
			if _, stderr, code := runSafegit(t, dir, "mv", "-m", "declare the move", c.move); code != 0 {
				t.Fatalf("mv failed (code %d): %s", code, stderr)
			}
			head := testutil.Rev(t, dir, "HEAD")
			record := movedRecordsIn(t, commitMessageOf(t, dir, "HEAD"))
			if len(record) != 1 {
				t.Fatalf("the fixture declared no record: %v", record)
			}
			recordLine := "Moved: " + record[0][0] + " " + record[0][1]

			stdout, stderr, code := runSafegit(t, dir, "scrub", "match", "--approve-consequential",
				"--pattern", c.pattern, "--replace", c.replace, "--entire-history",
				"--reason", "a substitution that does not survive the record")
			if code != exitcode.RewriteRefused {
				t.Fatalf("a transform that breaks a record must exit %d (RewriteRefused), got %d\nstdout=%s\nstderr=%s",
					exitcode.RewriteRefused, code, stdout, stderr)
			}

			// The refusal names the commit, the record as written, and what the
			// transform would have produced.
			for _, want := range []string{head, recordLine, c.wantResult, "Nothing was changed"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("the refusal does not name %q:\n%s", want, stderr)
				}
			}

			// Nothing moved, and the record is exactly the one the commit
			// carried: no corrupt line was written anywhere.
			if got := testutil.Rev(t, dir, "HEAD"); got != head {
				t.Errorf("HEAD moved to %s, want %s: the refusal is supposed to precede every ref update", got, head)
			}
			msg := commitMessageOf(t, dir, "HEAD")
			moves := trailer.ReadMoves(msg)
			if len(moves.Malformed) != 0 {
				t.Errorf("a malformed record was written into history: %v\n%s", moves.Malformed, msg)
			}
			if len(moves.Records) != 1 || trailer.RecordLine(moves.Records[0]) != recordLine {
				t.Errorf("the original record did not survive intact:\n%s", msg)
			}
		})
	}
}
