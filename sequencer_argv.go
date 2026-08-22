package main

import (
	"strings"
)

// Reading the operator's own argv for merge, cherry-pick and revert.
//
// Two features need to know what an operator typed rather than just forwarding
// it: the restructured single-commit revert, which safegit only authors itself
// when every flag present is one it can honor, and the honest --dry-run
// preview, which replays the operation with `git merge-tree` and must refuse
// rather than preview when a flag changes how the merge is computed.
//
// The grammar here is git's own for these three verbs, spelled out because it
// is closed and stable: what is needed is only which flags CONSUME THE NEXT
// ARGUMENT, so that `git revert -m 2 <sha>` is read as one option and one
// revision instead of two revisions. Nothing here interprets a flag's meaning;
// the two callers do that against their own criteria.

// valueFlags lists, per verb, the options that take their value as the NEXT
// argv element. The attached forms (`--strategy=ort`, `-m2`) need no entry:
// they are one element and are recognized structurally.
//
// Optional-value options (`-S[<keyid>]`, `--gpg-sign[=<keyid>]`,
// `--log[=<n>]`) are deliberately ABSENT: git accepts their value only
// attached, so treating them as consuming the next element would swallow a
// revision.
var valueFlags = map[string]map[string]bool{
	"merge": {
		"-m": true, "--message": true,
		"-F": true, "--file": true,
		"-s": true, "--strategy": true,
		"-X": true, "--strategy-option": true,
		"--cleanup": true, "--into-name": true,
	},
	"cherry-pick": {
		"-m": true, "--mainline": true,
		"-X": true, "--strategy-option": true,
		"--strategy": true, "--cleanup": true,
	},
	"revert": {
		"-m": true, "--mainline": true,
		"-X": true, "--strategy-option": true,
		"--strategy": true, "--cleanup": true,
	},
}

// gitOption is one option as the operator wrote it.
type gitOption struct {
	// Name is the option including its dashes, without any attached value:
	// "--strategy" for both `--strategy=ort` and `--strategy ort`, "-X" for
	// both `-Xours` and `-X ours`.
	Name string
	// Value is the option's argument when it has one, in either spelling, and
	// "" for a flag that takes none.
	Value string
	// Raw is the argv element (or elements, joined by a space) the option came
	// from, for a message that quotes what was actually typed.
	Raw string
}

// gitArgs is one operator command line, split into what it says.
type gitArgs struct {
	Options   []gitOption
	Revisions []string
	// AfterDoubleDash holds everything past a bare `--`. For these three verbs
	// that is a pathspec, which none of safegit's readings can interpret, so a
	// command line carrying one is simply not one they act on.
	AfterDoubleDash []string
}

// Has reports whether any of the named options is present. Names are matched
// exactly as spelled, so a caller asks for every spelling it cares about.
func (a gitArgs) Has(names ...string) bool {
	for _, o := range a.Options {
		for _, n := range names {
			if o.Name == n {
				return true
			}
		}
	}
	return false
}

// Find returns the first occurrence of any of the named options.
func (a gitArgs) Find(names ...string) (gitOption, bool) {
	for _, o := range a.Options {
		for _, n := range names {
			if o.Name == n {
				return o, true
			}
		}
	}
	return gitOption{}, false
}

// parseGitArgs splits one verb's operator argv into options and revisions.
//
// It is deliberately permissive about options it does not know: an unrecognized
// long or short option is recorded as a flag taking no value. Both callers then
// decide from the RECORDED set, and both of their decisions fail safe -- the
// revert restructure only acts on an allowlist of options it can honor, and the
// preview refuses anything outside the set it can represent -- so an option git
// grows tomorrow makes safegit forward the command line unchanged rather than
// misread it.
func parseGitArgs(verb string, args []string) gitArgs {
	takesValue := valueFlags[verb]
	var out gitArgs

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--":
			out.AfterDoubleDash = append(out.AfterDoubleDash, args[i+1:]...)
			return out

		case strings.HasPrefix(arg, "--"):
			name, value, attached := strings.Cut(arg, "=")
			if attached {
				out.Options = append(out.Options, gitOption{Name: name, Value: value, Raw: arg})
				continue
			}
			if takesValue[name] && i+1 < len(args) {
				out.Options = append(out.Options, gitOption{Name: name, Value: args[i+1], Raw: arg + " " + args[i+1]})
				i++
				continue
			}
			out.Options = append(out.Options, gitOption{Name: name, Raw: arg})

		case len(arg) > 1 && strings.HasPrefix(arg, "-"):
			// A short option, possibly with its value attached (`-Xours`) and
			// possibly a cluster of value-less flags (`-en`). The value-taking
			// name is the first two characters; if that name takes a value and
			// more characters follow, the rest is the value.
			name := arg[:2]
			if takesValue[name] {
				if len(arg) > 2 {
					out.Options = append(out.Options, gitOption{Name: name, Value: arg[2:], Raw: arg})
					continue
				}
				if i+1 < len(args) {
					out.Options = append(out.Options, gitOption{Name: name, Value: args[i+1], Raw: arg + " " + args[i+1]})
					i++
					continue
				}
			}
			// Not a value-taking option: record every letter of the cluster as
			// its own flag, so `-en` is seen as -e and -n rather than as one
			// unknown option.
			for _, c := range arg[1:] {
				out.Options = append(out.Options, gitOption{Name: "-" + string(c), Raw: arg})
			}

		default:
			out.Revisions = append(out.Revisions, arg)
		}
	}
	return out
}
