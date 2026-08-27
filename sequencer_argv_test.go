package main

import "testing"

// valueFlags lists the options that take their value as the NEXT argv element.
// An option whose value git accepts only ATTACHED must never be in it: an entry
// would make the reader swallow the element after the flag, which on these verbs
// is a revision.
//
// The rule was stated in a comment and violated by one row at the same time --
// `--gpg-sign` on rebase, an attached-only optional-value flag. It produced no
// operator-visible bug (the option was refused as unlisted before the parse
// mattered), but a map that contradicts its own rule is one edit away from
// producing one, which is what this pin is for.
var attachedOnlyOptionalValueOptions = []string{
	"-S", "--gpg-sign",
	"--log",
	"-r", "--rebase-merges",
}

func TestNoAttachedOnlyOptionIsReadAsTakingTheNextElement(t *testing.T) {
	for verb, flags := range valueFlags {
		for _, name := range attachedOnlyOptionalValueOptions {
			if flags[name] {
				t.Errorf("valueFlags[%q] lists %s, whose value git accepts only attached: reading it as consuming the next element swallows a revision", verb, name)
			}
		}
	}
}

// And the consequence the rebase topology flag depends on: the upstream after
// it is read as an upstream rather than as the flag's value.
func TestTheRebaseTopologyFlagDoesNotSwallowTheUpstream(t *testing.T) {
	parsed := parseGitArgs("rebase", []string{"--rebase-merges", "main"})
	if len(parsed.Revisions) != 1 || parsed.Revisions[0] != "main" {
		t.Fatalf("revisions = %v, want [main]", parsed.Revisions)
	}
	if o, ok := parsed.Find("--rebase-merges"); !ok || o.Value != "" {
		t.Errorf("the flag parsed as %+v, want the name with no value", o)
	}

	// Attached, the value is the flag's own and no revision is lost.
	attached := parseGitArgs("rebase", []string{"--rebase-merges=rebase-cousins", "main"})
	if len(attached.Revisions) != 1 || attached.Revisions[0] != "main" {
		t.Fatalf("revisions = %v, want [main]", attached.Revisions)
	}
	if o, ok := attached.Find("--rebase-merges"); !ok || o.Value != "rebase-cousins" {
		t.Errorf("the attached form parsed as %+v, want value rebase-cousins", o)
	}
}
