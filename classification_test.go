package main

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/smm-h/strictcli/go/strictcli"
	"github.com/smm-h/stricttest/go/hygiene"
)

// This file pins safegit's CLI surface: every command's strictcli effect
// classification, its `consequential` declaration, whether it accepts
// --dry-run, and the group tree it hangs off. The tables below ARE the
// specification -- registering a command without adding its row fails, and
// changing a row is the deliberate edit a reclassification requires.
//
// Reasoning follows the strictcli effects contract (§1: `read_only` means the
// command performs no user-visible or consequential mutation; §8.1:
// `consequential` means the act is worth interrupting someone for; the dry-run
// baseline is that a preview is possible, and WithDryRunUnsupported is the only
// opt-out).
//
// Read-only (9): version, scan, config show, config get, author list,
// author check, backup list, hook list, scrub verify. Each one inspects the
// object store, the config file or the remote's ref list and writes nothing;
// `backup list` is a network read (git ls-remote) but still a read.
//
// Mutating and NOT consequential (18):
//
//   - commit -- creates a commit and moves a ref. The routine operation this
//     tool exists for, and `safegit undo` reverses it from the oplog.
//   - checkout, merge, rebase, reset, bisect, cherry-pick, revert -- guarded
//     passthroughs. Each moves HEAD, refs or the working tree and then hands
//     off to git; git itself does not interrupt for any of them, and safegit's
//     coordination guard runs first.
//   - push -- publishes local refs. Publishing is what the command is for, and
//     --force-with-lease refuses rather than clobbers. A FORCED push is another
//     matter, but it is consequential conditionally -- on one flag, not on the
//     command -- which strictcli cannot yet declare, so it is confirmed at
//     safegit's own confirmDeliberate seam, answered by --approve-consequential
//     because the condition is a flag the caller typed.
//   - pull -- fetch plus merge, fast-forward-only by default.
//   - backup backup -- pushes one branch into the tool-owned refs/backups
//     namespace under a lease. The question worth interrupting for is not "back
//     up" but "back up to a remote that may be PUBLIC", which is a property of
//     the target rather than of the command, so it is gated per-condition by
//     --allow-public-remote instead of by this declaration.
//   - backup restore -- fast-forward only; refuses whenever the local branch
//     carries commits the backup does not.
//   - config set -- writes one key into .git/safegit/config.json.
//   - hook run -- executes the installed pre-pre-push hook scripts.
//   - hook install -- copies a script into .git/safegit/hooks and chmods it.
//   - doctor -- repairs or uninstalls. --diagnose only reads, so the command as
//     a whole is not worth an unconditional interruption; the destructive case
//     is --uninstall, gated at flag granularity by safegit's own seam.
//   - undo -- reverses the last oplog operation. It is the recovery path, and
//     it refuses operations it does not own.
//   - unlock -- removes one stale lock file, and refuses while its holder is
//     alive.
//
// Mutating AND consequential (4): scrub file, scrub match, scrub run,
// author rewrite. Each rewrites history: every commit from the rewrite point
// forward gets a new SHA, the change is not in the oplog, and anyone who
// already pulled the old history has to recover by hand.
//
// dry_run_supported=false (1): hook run. Its whole job is executing arbitrary
// operator-supplied scripts; safegit cannot know what a hook does, and the
// effects handle's `run` carries no stdin parameter, so the hook invocation
// cannot be minted either. A preview that printed a plausible-looking plan
// would be inventing one, so --dry-run is refused instead.
var classification = map[string]struct {
	effect          string
	consequential   bool
	dryRunSupported bool
	passthrough     bool
}{
	"commit":         {strictcli.EffectMutating, false, true, false},
	"checkout":       {strictcli.EffectMutating, false, true, true},
	"merge":          {strictcli.EffectMutating, false, true, true},
	"rebase":         {strictcli.EffectMutating, false, true, true},
	"reset":          {strictcli.EffectMutating, false, true, true},
	"bisect":         {strictcli.EffectMutating, false, true, true},
	"cherry-pick":    {strictcli.EffectMutating, false, true, true},
	"revert":         {strictcli.EffectMutating, false, true, true},
	"push":           {strictcli.EffectMutating, false, true, false},
	"pull":           {strictcli.EffectMutating, false, true, false},
	"doctor":         {strictcli.EffectMutating, false, true, false},
	"undo":           {strictcli.EffectMutating, false, true, false},
	"unlock":         {strictcli.EffectMutating, false, true, false},
	"scan":           {strictcli.EffectReadOnly, false, true, false},
	"version":        {strictcli.EffectReadOnly, false, true, false},
	"author.list":    {strictcli.EffectReadOnly, false, true, false},
	"author.check":   {strictcli.EffectReadOnly, false, true, false},
	"author.rewrite": {strictcli.EffectMutating, true, true, false},
	"backup.backup":  {strictcli.EffectMutating, false, true, false},
	"backup.list":    {strictcli.EffectReadOnly, false, true, false},
	"backup.restore": {strictcli.EffectMutating, false, true, false},
	"config.show":    {strictcli.EffectReadOnly, false, true, false},
	"config.get":     {strictcli.EffectReadOnly, false, true, false},
	"config.set":     {strictcli.EffectMutating, false, true, false},
	"hook.list":      {strictcli.EffectReadOnly, false, true, false},
	"hook.run":       {strictcli.EffectMutating, false, false, false},
	"hook.install":   {strictcli.EffectMutating, false, true, false},
	"scrub.file":     {strictcli.EffectMutating, true, true, false},
	"scrub.match":    {strictcli.EffectMutating, true, true, false},
	"scrub.run":      {strictcli.EffectMutating, true, true, false},
	"scrub.verify":   {strictcli.EffectReadOnly, false, true, false},
}

// groupTree pins the five command groups and their members, so that moving a
// command between groups, or adding a sixth group, is a deliberate edit here.
var groupTree = map[string][]string{
	"author": {"check", "list", "rewrite"},
	"backup": {"backup", "list", "restore"},
	"config": {"get", "set", "show"},
	"hook":   {"install", "list", "run"},
	"scrub":  {"file", "match", "run", "verify"},
}

// collectCommands flattens the app's command tree into dotted paths.
func collectCommands(app *strictcli.App) map[string]*strictcli.Command {
	out := map[string]*strictcli.Command{}
	for name, cmd := range app.Commands() {
		out[name] = cmd
	}
	var walk func(prefix string, g *strictcli.Group)
	walk = func(prefix string, g *strictcli.Group) {
		for name, cmd := range g.Commands {
			out[prefix+name] = cmd
		}
		for name, sub := range g.Groups {
			walk(prefix+name+".", sub)
		}
	}
	for name, g := range app.Groups() {
		walk(name+".", g)
	}
	return out
}

func TestCommandClassificationIsPinned(t *testing.T) {
	hygiene.Isolate(t, hygiene.Preserve(hygiene.GoPath, hygiene.GoModCache, hygiene.GoCache))

	cmds := collectCommands(newApp())

	var names []string
	for name := range cmds {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		want, ok := classification[name]
		if !ok {
			t.Errorf("command %q is registered but has no pinned classification; add a row to classification with the reasoning", name)
			continue
		}
		got := cmds[name]
		if got.Effect != want.effect {
			t.Errorf("command %q: effect = %q, pinned %q", name, got.Effect, want.effect)
		}
		if got.Consequential != want.consequential {
			t.Errorf("command %q: consequential = %v, pinned %v", name, got.Consequential, want.consequential)
		}
		if got.DryRunSupported != want.dryRunSupported {
			t.Errorf("command %q: dry_run_supported = %v, pinned %v", name, got.DryRunSupported, want.dryRunSupported)
		}
		if got.Passthrough != want.passthrough {
			t.Errorf("command %q: passthrough = %v, pinned %v", name, got.Passthrough, want.passthrough)
		}
	}
	for name := range classification {
		if _, ok := cmds[name]; !ok {
			t.Errorf("classification pins %q but no such command is registered", name)
		}
	}
}

// TestDryRunUnsupportedCarriesAReason pins the framework's own rule at
// safegit's surface: a command that refuses --dry-run must say why, and a
// read_only command may not refuse it at all (it changes nothing, so there is
// no preview to misrepresent).
func TestDryRunUnsupportedCarriesAReason(t *testing.T) {
	hygiene.Isolate(t, hygiene.Preserve(hygiene.GoPath, hygiene.GoModCache, hygiene.GoCache))

	for name, cmd := range collectCommands(newApp()) {
		if cmd.DryRunSupported {
			continue
		}
		if cmd.DryRunUnsupportedReason == "" {
			t.Errorf("command %q refuses --dry-run without a reason", name)
		}
		if cmd.Effect == strictcli.EffectReadOnly {
			t.Errorf("command %q is read_only and cannot refuse --dry-run", name)
		}
	}
}

func TestGroupTreeIsPinned(t *testing.T) {
	hygiene.Isolate(t, hygiene.Preserve(hygiene.GoPath, hygiene.GoModCache, hygiene.GoCache))

	groups := newApp().Groups()
	if len(groups) != len(groupTree) {
		t.Errorf("app registers %d groups, pinned %d", len(groups), len(groupTree))
	}
	for name, g := range groups {
		want, ok := groupTree[name]
		if !ok {
			t.Errorf("group %q is registered but not pinned in groupTree", name)
			continue
		}
		if len(g.Groups) != 0 {
			t.Errorf("group %q has nested groups; groupTree pins a flat tree", name)
		}
		var got []string
		for cmd := range g.Commands {
			got = append(got, cmd)
		}
		sort.Strings(got)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("group %q members = %v, pinned %v", name, got, want)
		}
	}
	for name := range groupTree {
		if _, ok := groups[name]; !ok {
			t.Errorf("groupTree pins group %q but no such group is registered", name)
		}
	}
}

// TestAppDescriptionCommandCountIsTrue closes the class of defect that left the
// app description advertising "20 commands" long after there were 31: the
// number in the description is checked against the number of commands actually
// registered, so it cannot go stale silently.
func TestAppDescriptionCommandCountIsTrue(t *testing.T) {
	hygiene.Isolate(t, hygiene.Preserve(hygiene.GoPath, hygiene.GoModCache, hygiene.GoCache))

	app := newApp()
	m := regexp.MustCompile(`providing (\d+) commands`).FindStringSubmatch(app.Help)
	if m == nil {
		t.Fatalf("app description does not state a command count: %q", app.Help)
	}
	claimed, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parsing the claimed command count: %v", err)
	}
	if actual := len(collectCommands(app)); claimed != actual {
		t.Errorf("app description claims %d commands, %d are registered", claimed, actual)
	}
}

// TestNoReservedGlobalFlagNames guards the framework's reserved quartet at the
// app level. Command-level flags are covered implicitly: strictcli panics at
// registration for a reserved name anywhere, and newApp() registers everything.
func TestNoReservedGlobalFlagNames(t *testing.T) {
	hygiene.Isolate(t, hygiene.Preserve(hygiene.GoPath, hygiene.GoModCache, hygiene.GoCache))

	reserved := map[string]bool{"dry-run": true, "approve-consequential": true, "quiet": true, "verbose": true}
	for _, f := range newApp().GlobalFlags() {
		if reserved[f.Name] {
			t.Errorf("global flag %q is reserved by the framework", f.Name)
		}
	}
}
