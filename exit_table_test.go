package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/smm-h/safegit/internal/exitcode"
)

// TestExitCodeTableIsGenerated is the freshness check for the generated
// documentation table. The registry is the single authority; this fails when
// the table on disk no longer matches what the registry renders, which is
// exactly the drift the generator exists to prevent.
func TestExitCodeTableIsGenerated(t *testing.T) {
	raw, err := os.ReadFile(exitcode.DocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", exitcode.DocPath, err)
	}
	// The same rendering the generator performs. Comparing whole documents
	// means a drifted table, a moved marker and a hand-edited row all fail the
	// same way, with one instruction.
	want, err := exitcode.RenderDocument(string(raw))
	if err != nil {
		t.Fatalf("%s: %v", exitcode.DocPath, err)
	}
	if string(raw) != want {
		t.Errorf("the exit-code table in %s is stale; run scripts/gen-exit-table\n--- from the registry ---\n%s",
			exitcode.DocPath, exitcode.MarkdownTable())
	}
}

// TestDocumentedExitCodesAreRegistered covers the per-command exit-code tables
// elsewhere in the guide, which are hand-written subsets of the registry. They
// may list fewer codes, but never a code the registry does not define and never
// a code whose number does not exist.
func TestDocumentedExitCodesAreRegistered(t *testing.T) {
	raw, err := os.ReadFile(exitcode.DocPath)
	if err != nil {
		t.Fatalf("reading %s: %v", exitcode.DocPath, err)
	}
	doc := string(raw)
	begin := strings.Index(doc, exitcode.DocBeginMarker)
	end := strings.Index(doc, exitcode.DocEndMarker)
	if begin >= 0 && end > begin {
		doc = doc[:begin] + doc[end:]
	}

	row := regexp.MustCompile(`(?m)^\| (\d+) \| `)
	for _, m := range row.FindAllStringSubmatch(doc, -1) {
		code, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if !exitcode.Defined(code) {
			t.Errorf("%s documents exit code %d, which internal/exitcode does not define", exitcode.DocPath, code)
		}
	}
}

// exitCodeAssertion matches the ways this repository's tests name a numeric
// exit code: a comparison against a code variable, and the want-value in the
// helpers that take one.
var exitCodeAssertion = regexp.MustCompile(
	`\b(?:code|exitCode|gotCode|wantCode)\b\s*(?:!=|==)\s*(\d+)|` +
		`\b(?:want|wantExit|wantCode)\s*:\s*(\d+)\b`)

// TestNoTestAssertsAnUnregisteredExitCode sweeps every _test.go file in the
// repository for a numeric exit-code assertion and requires the registry to
// define the number. A test that pins a code nothing registers is either
// testing a code that no longer exists or asserting one that was never
// declared; both are the drift this registry exists to stop.
//
// Zero and one are always legal (success and the general error), and the
// sweep deliberately reads source text: the assertions it polices are written
// as literals, so a literal is what it must look for.
func TestNoTestAssertsAnUnregisteredExitCode(t *testing.T) {
	root := "."
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata", "docs":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range exitCodeAssertion.FindAllStringSubmatch(line, -1) {
				text := m[1]
				if text == "" {
					text = m[2]
				}
				code, convErr := strconv.Atoi(text)
				if convErr != nil {
					continue
				}
				if !exitcode.Defined(code) {
					t.Errorf("%s:%d asserts exit code %d, which internal/exitcode does not define:\n\t%s",
						path, i+1, code, strings.TrimSpace(line))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the tree: %v", err)
	}
	if scanned == 0 {
		t.Fatal("the sweep found no test files, so it proved nothing")
	}
	t.Logf("swept %d test files for numeric exit-code assertions", scanned)
}
