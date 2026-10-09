package scan

import (
	"regexp"
	"strings"
	"testing"
)

// TestPrefilterNeverHidesAMatch: an object the prefilter passes over must
// hold no match of the pattern. Every pattern is run against every text, and
// wherever the pattern matches, the prefilter must let the text through.
func TestPrefilterNeverHidesAMatch(t *testing.T) {
	patterns := []string{
		`hunter2`,
		`widgetworks|gadgetlane`,
		`secret-[0-9]+`,
		`(?:abc)+def`,
		`x{2,}y`,
		`(?i)Secret`,
		`a|b*`,
		`[A-Z]{3}`,
		`(?s)begin.*end`,
		`^start`,
		`caf\x{e9}`,
		`\x{fffd}`,
		`(token|key)=\w+`,
	}
	texts := []string{
		"", "hunter2", "xx hunter2 yy", "WIDGETWORKS", "the gadgetlane repo",
		"secret-42", "secret-", "abcabcdef", "xxy", "xy", "SECRET", "a", "bbb",
		"ABC", "begin\nmiddle\nend", "start here", "café", "caf\xe9", "\xff",
		"token=abc", "key=", "nothing at all",
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		for _, text := range texts {
			if re.MatchString(text) && !mayMatch(re, []byte(text)) {
				t.Errorf("pattern %q matches %q, but the prefilter passes it over", p, text)
			}
		}
	}
}

// TestPrefilterRulesOutTextsWithoutTheLiterals: the patterns scrubs are
// written with get a prefilter, and it rules out a text holding none of
// their literals.
func TestPrefilterRulesOutTextsWithoutTheLiterals(t *testing.T) {
	for _, p := range []string{`hunter2`, `widgetworks|gadgetlane`, `secret-[0-9]+`, `(token|key)=\w+`} {
		re := regexp.MustCompile(p)
		if prefilterFor(re) == nil {
			t.Errorf("pattern %q gets no prefilter", p)
			continue
		}
		if mayMatch(re, []byte(strings.Repeat("unrelated text ", 100))) {
			t.Errorf("pattern %q: the prefilter lets through a text holding none of its literals", p)
		}
	}
	for _, p := range []string{`(?i)secret`, `[A-Z]{3}`, `a|b*`, `\x{fffd}`} {
		if prefilterFor(regexp.MustCompile(p)) != nil {
			t.Errorf("pattern %q gets a prefilter, but some match of it holds no fixed string", p)
		}
	}
}
