package scan

import (
	"bytes"
	"regexp"
	"regexp/syntax"
	"sync"
	"unicode/utf8"
)

// A scan matches its pattern against every object in the store, and nearly
// every object holds no match. Most scrub patterns are literals or
// alternations of literals, and every match of such a pattern contains one of
// a few fixed strings, which bytes.Index finds far faster than the regexp
// engine can rule a match out. requiredLiterals derives those strings from the
// pattern, when it has them, and an object holding none of them is passed over
// without running the pattern at all. An object holding one is matched in
// full, so the matches reported are the pattern's own.

// prefilters caches each pattern's required literals; a scan consults it once
// per object.
var prefilters sync.Map // *regexp.Regexp -> [][]byte (nil: no prefilter)

// prefilterFor returns the literals one of which every match of pattern
// contains, or nil when the pattern has no such set.
func prefilterFor(pattern *regexp.Regexp) [][]byte {
	if v, ok := prefilters.Load(pattern); ok {
		return v.([][]byte)
	}
	var lits [][]byte
	if re, err := syntax.Parse(pattern.String(), syntax.Perl); err == nil {
		if set, ok := requiredLiterals(re.Simplify()); ok {
			for _, s := range set {
				lits = append(lits, []byte(s))
			}
		}
	}
	prefilters.Store(pattern, lits)
	return lits
}

// mayMatch reports whether content can hold a match of pattern: false only
// when the pattern has required literals and content holds none of them.
func mayMatch(pattern *regexp.Regexp, content []byte) bool {
	lits := prefilterFor(pattern)
	if lits == nil {
		return true
	}
	for _, lit := range lits {
		if bytes.Contains(content, lit) {
			return true
		}
	}
	return false
}

// requiredLiterals returns a set of non-empty strings such that every match
// of re contains at least one of them, and false when re has no such set it
// can name (a branch that can match without a fixed string, a case-folded
// literal, an empty match).
func requiredLiterals(re *syntax.Regexp) ([]string, bool) {
	switch re.Op {
	case syntax.OpLiteral:
		if re.Flags&syntax.FoldCase != 0 || len(re.Rune) == 0 {
			return nil, false
		}
		for _, r := range re.Rune {
			// The engine reads an invalid byte as U+FFFD, so a pattern
			// naming U+FFFD can match bytes that do not spell it.
			if r == utf8.RuneError {
				return nil, false
			}
		}
		return []string{string(re.Rune)}, true
	case syntax.OpCapture:
		return requiredLiterals(re.Sub[0])
	case syntax.OpPlus:
		return requiredLiterals(re.Sub[0])
	case syntax.OpRepeat:
		if re.Min < 1 {
			return nil, false
		}
		return requiredLiterals(re.Sub[0])
	case syntax.OpConcat:
		// Every part of a concatenation is in every match, so any one part's
		// set will do; the one whose shortest literal is longest rules out
		// the most.
		var best []string
		bestLen := 0
		for _, sub := range re.Sub {
			set, ok := requiredLiterals(sub)
			if !ok {
				continue
			}
			if n := shortest(set); n > bestLen {
				best, bestLen = set, n
			}
		}
		return best, best != nil
	case syntax.OpAlternate:
		// A match is a match of one branch, so every branch must name its
		// literals.
		var all []string
		for _, sub := range re.Sub {
			set, ok := requiredLiterals(sub)
			if !ok {
				return nil, false
			}
			all = append(all, set...)
		}
		return all, len(all) > 0
	}
	return nil, false
}

func shortest(set []string) int {
	n := -1
	for _, s := range set {
		if n < 0 || len(s) < n {
			n = len(s)
		}
	}
	return n
}
