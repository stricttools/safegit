//go:build ignore

// gen-exit-table writes the exit-code reference table in
// docs/commands-guide.md from internal/exitcode's registry, so the
// documentation cannot drift from the code.
//
// Run it through the wrapper after changing the registry:
//
//	scripts/gen-exit-table
//
// The rendering itself lives in internal/exitcode (RenderDocument), because
// the freshness test in package main renders the same document and compares it
// against the file. A registry change that was not regenerated therefore fails
// `go test .` rather than silently shipping a stale table.
package main

import (
	"fmt"
	"os"

	"github.com/smm-h/safegit/internal/exitcode"
)

func main() {
	raw, err := os.ReadFile(exitcode.DocPath)
	if err != nil {
		fail(err)
	}
	updated, err := exitcode.RenderDocument(string(raw))
	if err != nil {
		fail(fmt.Errorf("%s: %w", exitcode.DocPath, err))
	}
	if updated == string(raw) {
		fmt.Println(exitcode.DocPath + ": already current")
		return
	}
	if err := os.WriteFile(exitcode.DocPath, []byte(updated), 0644); err != nil {
		fail(err)
	}
	fmt.Println(exitcode.DocPath + ": exit-code table regenerated")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
