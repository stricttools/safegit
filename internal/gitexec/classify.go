package gitexec

import (
	"fmt"
	"sort"
	"strings"
)

// Effect is a set of things one git invocation can do to a repository.
//
// The zero value, ObserveOnly, means the invocation changes nothing: no object
// is added or removed, no ref moves, no index or working-tree file is written
// and no remote is contacted.
type Effect uint16

const (
	// MutatesObjects: the invocation can add objects to, or remove objects
	// from, the object store.
	MutatesObjects Effect = 1 << iota
	// MutatesRefs: the invocation can create, move or delete a ref (local or,
	// for push, on a remote).
	MutatesRefs
	// MutatesIndex: the invocation can write a git index file.
	MutatesIndex
	// MutatesWorktree: the invocation can create, change or delete a file in
	// the working tree.
	MutatesWorktree
	// MutatesConfig: the invocation can write git configuration.
	MutatesConfig
	// Network: the invocation can contact a remote.
	Network
)

// ObserveOnly is the empty effect set.
const ObserveOnly Effect = 0

// Has reports whether every effect in want is present in e.
func (e Effect) Has(want Effect) bool { return e&want == want }

// String renders an effect set for diagnostics.
func (e Effect) String() string {
	if e == ObserveOnly {
		return "observe-only"
	}
	names := []struct {
		bit  Effect
		name string
	}{
		{MutatesObjects, "objects"},
		{MutatesRefs, "refs"},
		{MutatesIndex, "index"},
		{MutatesWorktree, "worktree"},
		{MutatesConfig, "config"},
		{Network, "network"},
	}
	var parts []string
	for _, n := range names {
		if e&n.bit != 0 {
			parts = append(parts, n.name)
		}
	}
	return strings.Join(parts, "|")
}

// ConditionalEffect adds effects to a verb only when one of Tokens appears in
// the argv. Tokens are matched literally against whole argv elements, so both
// option spellings ("-w", "--hard") and subcommand words ("expire", "delete")
// work.
type ConditionalEffect struct {
	Tokens  []string
	Effects Effect
	Why     string
}

// Verb is one git subcommand safegit is allowed to invoke, with what that
// invocation can do.
//
// Base holds the effects every invocation of the verb has; Conditional adds the
// effects that depend on the argv. Where a verb's effects cannot be decided
// from a single token, Base is deliberately the WIDER of the possibilities: a
// consumer that over-quarantines or under-permits is safe, one that
// under-quarantines is not.
type Verb struct {
	Name        string
	Base        Effect
	Conditional []ConditionalEffect
	// Subcommands is the complete subcommand vocabulary safegit admits for this
	// verb, where the verb HAS one and safegit forwards an operator's own choice
	// of it. It is empty for every verb safegit calls with argv it builds itself,
	// because there the vocabulary is the call site rather than a declaration.
	//
	// It exists so that "which subcommands may be typed" and "which of them
	// change the working tree" are two views of ONE declaration: the conditional
	// tokens are the mutating half of this list, and a subcommand that is in
	// neither is refused before git is started.
	Subcommands []string

	// Authors marks a verb whose invocation can make GIT author a commit.
	//
	// It is a field of its own rather than an Effect bit or a ConditionalEffect,
	// and deliberately so. The effect vocabulary is ADDITIVE and presence-only,
	// and Base is deliberately the WIDER of a verb's possibilities -- both are
	// right for "what may this invocation touch" and both are wrong for "may
	// git write a commit here", whose answer has to be exact in the other
	// direction: a false yes refuses a command safegit needs, and a false no
	// admits a second class of authorship. So the authoring fact reads its own
	// field and leaves ObservePrefixes' reasoning about conditionals untouched.
	Authors bool

	// SuppressedBy are the argv tokens whose PRESENCE takes the authoring away:
	// with any one of them after the subcommand, git cannot author a commit
	// through this invocation. Only a token whose presence is decisive belongs
	// here -- the absence of an option is not a token, and a token that merely
	// makes authoring unlikely is not a suppressor.
	//
	// The sets are per verb rather than shared, because the same spelling does
	// not mean the same thing across git's verbs: `-n` is `--no-commit` on
	// cherry-pick and revert, and `--no-stat` on merge. One shared list would
	// therefore read `git merge -n topic` as a merge that cannot commit, which
	// is the opposite of what git does with it.
	SuppressedBy []string

	Note string
}

// verbs is the argv classification table: the single authority over the git
// vocabulary safegit uses. Command and ArgvAny refuse any argv naming a
// subcommand absent from it, so adding a git call to safegit means declaring it
// here first.
//
// Four views read this one table: Validate (the execution boundary's own
// vocabulary check), WritesObjects (which invocations touch the object store),
// WritesWorktree (which invocations the uncommitted-work guard must refuse) and
// IsObserveOnly (which invocations change nothing).
var verbs = []Verb{
	{
		Name: "--version",
		Base: ObserveOnly,
		Note: "a global option that is the whole invocation; safegit reports the git version it found",
	},
	{
		Name: "add",
		Base: MutatesObjects | MutatesIndex,
		Note: "safegit only ever runs it against a per-invocation temporary index via GIT_INDEX_FILE",
	},
	{
		Name: "apply",
		Base: MutatesObjects | MutatesIndex | MutatesWorktree,
		Note: "safegit uses --cached only, which stages into an index instead of the working tree; the base set stays the wider one",
	},
	{
		Name: "bisect",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{
				Tokens:  []string{"start", "good", "bad", "old", "new", "skip", "run", "replay", "reset"},
				Effects: MutatesRefs | MutatesIndex | MutatesWorktree,
				Why:     "the STEPPING vocabulary moves HEAD and checks another commit out (and writes refs/bisect/*); `bisect terms`, `bisect log` and `bisect view` only report on a bisect already in progress",
			},
		},
		Subcommands: []string{
			// The stepping half, which is the conditional tokens above.
			"start", "good", "bad", "old", "new", "skip", "run", "replay", "reset",
			// The reporting half: these read a bisect already in progress.
			"terms", "log", "view",
		},
		Note: "guarded passthrough; the operator's own argv, whose subcommand must be one of Subcommands",
	},
	{Name: "cat-file", Base: ObserveOnly},
	{
		Name: "check-attr",
		Base: ObserveOnly,
		Note: "answers what .gitattributes says about a path; every spelling only reads, including --attr-source=<tree>, which chooses WHICH attributes file is read and never writes one",
	},
	{Name: "check-ignore", Base: ObserveOnly},
	{
		Name: "config",
		Base: MutatesConfig,
		Note: "safegit only ever reads, with `config --get`, but the SET form is two bare positionals (`git config merge.conflictStyle diff3`) and no single token tells it apart from a read; the base set is therefore the wider one",
	},
	{
		Name:    "cherry-pick",
		Base:    MutatesObjects | MutatesRefs | MutatesIndex | MutatesWorktree,
		Authors: true,
		// `-n` is cherry-pick's own spelling of --no-commit. `--continue` is
		// deliberately absent: git's --continue is exactly the invocation that
		// authors the commit.
		SuppressedBy: []string{"-n", "--no-commit", "--abort", "--quit"},
		Note:         "safegit computes a pick with --no-commit and commits the staged result itself; the state-control forms stay a guarded passthrough",
	},
	{
		Name:    "commit",
		Base:    MutatesObjects | MutatesRefs | MutatesIndex,
		Authors: true,
		Note:    "safegit's own pipeline never uses it -- it builds commits from commit-tree plus a compare-and-swap update-ref, precisely so it never writes the shared index -- but the verb is part of the vocabulary the boundary classifies, and repository fixtures reach it through the plumbing interface. It declares no suppressor: there is no shape of `git commit` safegit has any use for",
	},
	{Name: "commit-tree", Base: MutatesObjects},
	{Name: "diff", Base: ObserveOnly},
	{
		Name: "diff-index",
		Base: ObserveOnly,
		Note: "compares the index and working tree against a commit; it writes nothing (refreshing the index's stat cache is opt-in via --refresh, which safegit never passes)",
	},
	{Name: "diff-tree", Base: ObserveOnly},
	{Name: "fetch", Base: MutatesObjects | MutatesRefs | Network},
	{Name: "for-each-ref", Base: ObserveOnly},
	{
		Name: "hash-object",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{Tokens: []string{"-w"}, Effects: MutatesObjects, Why: "-w writes the blob to the object store; without it hash-object only computes the SHA"},
		},
	},
	{Name: "log", Base: ObserveOnly},
	{Name: "ls-files", Base: ObserveOnly},
	{Name: "ls-remote", Base: Network},
	{Name: "ls-tree", Base: ObserveOnly},
	{
		Name:    "merge",
		Base:    MutatesObjects | MutatesRefs | MutatesIndex | MutatesWorktree,
		Authors: true,
		// --ff-only is a suppressor because a fast-forward moves a ref onto a
		// commit that ALREADY EXISTS, and where no fast-forward is possible git
		// refuses instead of merging: the flag admits `backup restore`'s
		// fast-forward without admitting anything that writes a commit object.
		// `-n` is deliberately NOT here: on merge it means --no-stat.
		SuppressedBy: []string{"--no-commit", "--abort", "--quit", "--ff-only"},
		Note:         "safegit computes a merge with --no-ff --no-commit and commits the staged result itself; the state-control forms stay a guarded passthrough",
	},
	{Name: "merge-base", Base: ObserveOnly},
	{
		Name: "merge-tree",
		Base: MutatesObjects,
		Note: "safegit runs it with --write-tree to compute a merge, a cherry-pick or a revert for a --dry-run preview; it writes the resulting tree (and, for a conflicted merge, the marker-carrying blobs) into the object store, which is why a preview must run it under an object quarantine. It touches no ref, no index and no working-tree file",
	},
	{
		Name: "merge-file",
		Base: MutatesWorktree,
		Note: "safegit only ever runs it with -p, which writes the three-way merge result to stdout; without -p merge-file OVERWRITES its first file argument, and no conditional token can be trusted to tell the two apart (the absence of an option is not a token), so the base set stays the wider one. It writes no objects in either spelling",
	},
	{Name: "mktree", Base: MutatesObjects},
	{
		Name: "notes",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{
				Tokens:  []string{"add", "append", "copy", "edit", "remove", "prune"},
				Effects: MutatesObjects | MutatesRefs,
				Why:     "these notes subcommands rewrite refs/notes/*; safegit only reads with `notes list`",
			},
		},
	},
	{
		Name: "prune",
		Base: MutatesObjects,
		Note: "removes unreachable objects from the store",
	},
	{Name: "push", Base: MutatesRefs | Network},
	{
		Name: "read-tree",
		Base: MutatesIndex,
		Conditional: []ConditionalEffect{
			{Tokens: []string{"-u"}, Effects: MutatesWorktree, Why: "-u checks the tree out into the working tree"},
		},
	},
	{
		Name:    "rebase",
		Base:    MutatesObjects | MutatesRefs | MutatesIndex | MutatesWorktree,
		Authors: true,
		// The state-control forms end a rebase without replaying anything.
		// --continue and --skip are absent on purpose: both resume the replay,
		// and the replay is where git writes commits. They do not need to be
		// suppressors, because the rebase door admits them.
		SuppressedBy: []string{"--abort", "--quit"},
		Note:         "guarded passthrough; the operator's own argv, and the ONE declared door through which git authors commits behind a safegit command name -- see the door table in authoring.go",
	},
	{
		Name: "reflog",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{Tokens: []string{"expire", "delete"}, Effects: MutatesRefs, Why: "both drop reflog entries, which is what makes rewritten commits unreachable"},
		},
	},
	{
		Name: "remote",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{
				Tokens:  []string{"add", "remove", "rename", "set-url", "set-head", "set-branches", "prune"},
				Effects: MutatesConfig | MutatesRefs,
				Why:     "safegit only reads with `remote get-url`",
			},
		},
	},
	{Name: "repack", Base: MutatesObjects},
	{
		Name: "reset",
		Base: MutatesRefs | MutatesIndex,
		Conditional: []ConditionalEffect{
			{Tokens: []string{"--hard", "--merge", "--keep"}, Effects: MutatesWorktree, Why: "these three reset modes overwrite working-tree files; --soft and --mixed move the ref and the index and never touch the tree (--mixed is reset's base classification, so it needs no entry of its own)"},
		},
		Note: "guarded passthrough; the operator's own argv",
	},
	{
		Name:    "revert",
		Base:    MutatesObjects | MutatesRefs | MutatesIndex | MutatesWorktree,
		Authors: true,
		// `-n` is revert's own spelling of --no-commit, as it is on cherry-pick.
		SuppressedBy: []string{"-n", "--no-commit", "--abort", "--quit"},
		Note:         "safegit computes an inverse patch with --no-commit and commits the staged result itself; the state-control forms stay a guarded passthrough",
	},
	{Name: "rev-list", Base: ObserveOnly},
	{Name: "rev-parse", Base: ObserveOnly},
	{
		Name: "rm",
		Base: MutatesIndex | MutatesWorktree,
		Note: "safegit uses --cached, which leaves the working tree alone; the base set stays the wider one",
	},
	{
		Name: "stash",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{
				Tokens:  []string{"push", "save", "pop", "apply", "drop", "clear", "store", "create", "branch"},
				Effects: MutatesObjects | MutatesRefs | MutatesIndex | MutatesWorktree,
				Why:     "safegit only reads with `stash list`",
			},
		},
	},
	{Name: "status", Base: ObserveOnly},
	{
		Name: "stripspace",
		Base: ObserveOnly,
		Note: "a filter: it reads a message on stdin and writes the cleaned message to stdout, touching no repository state at all",
	},
	{
		Name: "submodule",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{
				Tokens:  []string{"add", "init", "update", "deinit", "sync", "set-url", "set-branch", "absorbgitdirs"},
				Effects: MutatesObjects | MutatesConfig | MutatesWorktree | Network,
				Why:     "safegit only enumerates with `submodule foreach`",
			},
		},
	},
	{
		Name: "switch",
		Base: MutatesRefs | MutatesIndex | MutatesWorktree,
		Note: "branch navigation, and the ONLY spelling of it safegit invokes: `git checkout` is absent from this table because safegit's own checkout is gone, and with it the file-restoration form that made checkout two commands under one name",
	},
	{
		Name: "symbolic-ref",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{Tokens: []string{"-d", "--delete"}, Effects: MutatesRefs, Why: "safegit only reads HEAD"},
		},
	},
	{
		Name: "tag",
		Base: ObserveOnly,
		Conditional: []ConditionalEffect{
			{
				Tokens:  []string{"-d", "--delete", "-a", "--annotate", "-s", "--sign", "-m", "-f", "--force"},
				Effects: MutatesObjects | MutatesRefs,
				Why:     "safegit only lists with `tag -l`",
			},
		},
	},
	{Name: "update-index", Base: MutatesIndex},
	{Name: "update-ref", Base: MutatesRefs},
	{
		Name: "var",
		Base: ObserveOnly,
		Note: "safegit asks only for GIT_AUTHOR_IDENT, git's own resolution of the identity it would author a commit with; `var` reports a value and writes nothing whatever is asked for",
	},
	{Name: "write-tree", Base: MutatesObjects},
}

// byName indexes verbs for lookup.
var byName = func() map[string]Verb {
	m := make(map[string]Verb, len(verbs))
	for _, v := range verbs {
		m[v.Name] = v
	}
	return m
}()

// Verbs returns the declared vocabulary, sorted by name. It returns a copy.
func Verbs() []Verb {
	out := append([]Verb(nil), verbs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the declared verb by subcommand name.
func Lookup(name string) (Verb, bool) {
	v, ok := byName[name]
	return v, ok
}

// valueTakingGlobals are git global options whose VALUE is a separate argv
// element, so the element after them can never be the subcommand.
// --attr-source is here because the conflict-attribute resolver is the first
// site to use it: in its separate-element spelling the tree name would
// otherwise be read as the subcommand and refused as undeclared.
var valueTakingGlobals = map[string]bool{
	"-c": true, "-C": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--super-prefix": true,
	"--attr-source": true,
}

// Subcommand returns the git subcommand an argv names.
//
// The argv may or may not carry the binary name and the global prefix; both are
// skipped. A global option that IS the whole invocation (`git --version`) is
// returned as the subcommand, because that is how the table declares it.
func Subcommand(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "" {
			continue
		}
		if i == 0 && a == Binary {
			continue
		}
		if strings.HasPrefix(a, "-") {
			if _, ok := byName[a]; ok {
				return a, true
			}
			if valueTakingGlobals[a] {
				i++ // the next element is this option's value, never the verb
			}
			continue
		}
		return a, true
	}
	return "", false
}

// Validate errors when an argv names no subcommand at all, when it names one
// the classification table does not declare, and when its shape would let git
// author a commit from a call site that declares no door for it (see
// authoring.go).
//
// door is the call site's declared permission to let git author. Every site but
// one passes NoDoor.
func Validate(door DoorID, args []string) error {
	name, ok := Subcommand(args)
	if !ok {
		return &Error{Msg: "gitexec: git argv names no subcommand: " + strings.Join(args, " ")}
	}
	if _, ok := byName[name]; !ok {
		return &Error{Msg: fmt.Sprintf("gitexec: undeclared git subcommand %q in argv %q; declare it in the classification table (internal/gitexec/classify.go)", name, strings.Join(args, " "))}
	}
	return checkAuthoring(door, args)
}

// EffectsOf returns the effects a specific argv can have. It errors on an
// argv Validate would refuse.
func EffectsOf(args []string) (Effect, error) {
	name, ok := Subcommand(args)
	if !ok {
		return 0, &Error{Msg: "gitexec: git argv names no subcommand: " + strings.Join(args, " ")}
	}
	v, ok := byName[name]
	if !ok {
		return 0, &Error{Msg: fmt.Sprintf("gitexec: undeclared git subcommand %q", name)}
	}
	eff := v.Base
	for _, c := range v.Conditional {
		for _, tok := range c.Tokens {
			if argvHas(args, name, tok) {
				eff |= c.Effects
				break
			}
		}
	}
	return eff, nil
}

// argvHas reports whether tok appears in args after the subcommand. The
// subcommand element itself is skipped so a verb named like one of its own
// conditional tokens cannot match itself.
func argvHas(args []string, subcommand, tok string) bool {
	seen := false
	for _, a := range args {
		if !seen {
			if a == subcommand {
				seen = true
			}
			continue
		}
		if a == tok {
			return true
		}
	}
	return false
}

// WritesObjects reports whether an argv can change the object store. This is
// the view Phase 3.1's object quarantine reads. An argv the table does not
// declare reports true: an unknown invocation is never assumed harmless.
func WritesObjects(args []string) bool {
	eff, err := EffectsOf(args)
	if err != nil {
		return true
	}
	return eff.Has(MutatesObjects)
}

// WritesWorktree reports whether an argv can create, change or delete a file in
// the working tree. This is the view the guarded passthroughs' uncommitted-work
// check reads: a command that cannot touch the working tree has no reason to be
// refused over uncommitted work, and one that can must be refused whichever way
// it is spelled.
//
// Deriving it is the point. Two hand-written approximations of git's vocabulary
// lived in the handlers and both were wrong -- reset guarded only `--hard`, and
// bisect kept a subcommand list missing `skip`, `run` and `replay`. The table is
// the single authority over what a git invocation does, so the guard reads it
// rather than restating it.
//
// An argv the table does not declare reports TRUE, the same default-deny
// WritesObjects takes: an unknown invocation is never assumed harmless.
func WritesWorktree(args []string) bool {
	eff, err := EffectsOf(args)
	if err != nil {
		return true
	}
	return eff.Has(MutatesWorktree)
}

// IsObserveOnly reports whether an argv changes nothing at all. An argv the
// table does not declare reports false.
func IsObserveOnly(args []string) bool {
	eff, err := EffectsOf(args)
	if err != nil {
		return false
	}
	return eff == ObserveOnly
}

// ObservePrefixes renders the table's read view as the argv PREFIXES the
// framework's proc-observe allowlist takes: an effects-handle invocation whose
// argv starts with one of them is an observe, which executes even in a dry run
// and is never written to the would-do log.
//
// The list is GENERATED from the classification table rather than written out
// beside it, so a verb cannot become observe-authorized without the table
// saying it changes nothing. Two properties make the generation safe:
//
//   - A verb with any CONDITIONAL effect is excluded, however read-only its
//     base is. The allowlist matches a PREFIX, so `reflog` (observe-only until
//     `expire` follows it) would admit `reflog expire --all` -- a ref deletion
//     executing in the middle of a dry run. Only verbs that are observe-only
//     whatever follows them are admitted.
//   - Each prefix carries the binary and the global prefix, because that is
//     what an effects-handle argv literally starts with (see ArgvAny). A
//     two-element prefix of "git" plus the global option would match every git
//     invocation safegit makes, mutations included.
func ObservePrefixes() [][]string {
	var out [][]string
	for _, v := range Verbs() {
		if v.Base != ObserveOnly || len(v.Conditional) > 0 {
			continue
		}
		prefix := append([]string{Binary}, globalPrefix...)
		out = append(out, append(prefix, v.Name))
	}
	return out
}
