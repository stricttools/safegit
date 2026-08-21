package exitcode

import (
	"fmt"
	"strings"
)

// The generated exit-code table in docs/commands-guide.md is delimited by these
// markers. They live here, next to the registry, because the generator
// (scripts/gen-exit-table) and the freshness test (exit_table_test.go) must
// agree on them exactly; a marker spelled twice is a marker that can be spelled
// two ways.
const (
	// DocBeginMarker opens the generated region.
	DocBeginMarker = "<!-- BEGIN generated exit-code table (scripts/gen-exit-table) -->"
	// DocEndMarker closes it.
	DocEndMarker = "<!-- END generated exit-code table -->"
	// DocPath is the file that carries the generated table, relative to the
	// repository root.
	DocPath = "docs/commands-guide.md"
)

// RenderDocument returns doc with the region between the markers replaced by
// the current registry table. It is the whole generation step: the generator
// writes the result, and the freshness test compares the result against what
// is on disk, so both answer from one implementation.
//
// A document missing either marker is an error rather than a silent no-op:
// there is nothing to keep in sync if the region is gone.
func RenderDocument(doc string) (string, error) {
	begin := strings.Index(doc, DocBeginMarker)
	end := strings.Index(doc, DocEndMarker)
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("the document does not carry both exit-code table markers (%s ... %s)", DocBeginMarker, DocEndMarker)
	}
	return doc[:begin] + DocBeginMarker + "\n\n" + MarkdownTable() + "\n" + doc[end:], nil
}
