package main

import (
	"fmt"
	"strings"

	"github.com/smm-h/safegit/internal/exitcode"
)

// The generated health-check table in the commands guide is delimited by these
// markers. They live beside the check registry, because the generator
// (scripts/gen-doctor-table) and the freshness test (doctor_table_test.go)
// must agree on them exactly; a marker spelled twice is a marker that can be
// spelled two ways.
//
// This is the same mechanism the exit-code table uses, in the same document, and
// for the same reason: a table a human retypes is a table that drifts. The
// hand-typed version of this one carried a `warn` where the registry said
// `error`, and had no row at all for a registered check.
const (
	// doctorDocBeginMarker opens the generated region.
	doctorDocBeginMarker = "<!-- BEGIN generated doctor health-check table (scripts/gen-doctor-table) -->"
	// doctorDocEndMarker closes it.
	doctorDocEndMarker = "<!-- END generated doctor health-check table -->"
	// doctorDocPath is the file that carries the table. It is the same document
	// the exit-code table is generated into, derived from that path rather than
	// spelled a second time.
	doctorDocPath = exitcode.DocPath
)

// doctorHealthCheckTable renders the check registry as the three-column table
// the documentation carries. The generator and the freshness test both call it,
// so there is one rendering and one authority.
//
// A check with no Question is an error rather than an empty cell: the registry is
// where the documentation lives now, so an entry that documents nothing is an
// unfinished registration, surfaced when the table is rendered rather than
// shipped as a blank row.
func doctorHealthCheckTable() (string, error) {
	var b strings.Builder
	b.WriteString("| Check | Severity | Question |\n|-------|----------|----------|\n")
	for _, c := range doctorChecks {
		if strings.TrimSpace(c.Question) == "" {
			return "", fmt.Errorf("doctor check %q declares no Question, so the generated table would carry a blank row", c.Name)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", c.Name, c.Severity, c.Question)
	}
	return b.String(), nil
}

// renderDoctorDocument returns doc with the region between the markers replaced
// by the current registry table. It is the whole generation step: the generator
// writes the result and the freshness test compares it against what is on disk,
// so both answer from one implementation.
//
// A document missing either marker is an error rather than a silent no-op:
// there is nothing to keep in sync if the region is gone.
func renderDoctorDocument(doc string) (string, error) {
	begin := strings.Index(doc, doctorDocBeginMarker)
	end := strings.Index(doc, doctorDocEndMarker)
	if begin < 0 || end < 0 || end < begin {
		return "", fmt.Errorf("the document does not carry both health-check table markers (%s ... %s)", doctorDocBeginMarker, doctorDocEndMarker)
	}
	table, err := doctorHealthCheckTable()
	if err != nil {
		return "", err
	}
	return doc[:begin] + doctorDocBeginMarker + "\n\n" + table + "\n" + doc[end:], nil
}
