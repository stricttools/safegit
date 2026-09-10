# Declared filesystem properties: what git cannot store, applied by safegit wherever it materializes files

## Context

Git's tree records exactly one filesystem property per path: the executable
bit. Every other per-path property a project relies on exists only on the
machine that set it and is silently lost by every materialization of the
tree — a fresh clone, a checkout or switch, a pull or merge that brings new
files in, a worktree add, a sequencer conclusion. The fleet relies on such
properties today: released changelog records and release archives are kept
read-only (mode 444) so nothing edits them by accident, and a smaller set
of files is kept at a restrictive mode (600). Measured across the fleet's
repositories, over a thousand committed files are read-only on the machine
that created them and nearly a hundred are mode 600 — and a clone of any of
those repositories materializes all of them at the umask default. The
protection is locally real and globally fictional, and any tool that assumes
a file is read-only on every checkout is wrong on every other checkout.

This todo is deliberately general: not one property (read-only) and not one
entry point (clone). It is the mechanism by which a repository DECLARES the
filesystem properties its files must carry, and by which safegit APPLIES
them at every point where it materializes or changes files.

## Problem

- A property that is not a tree fact cannot survive cloning. Today nothing
  declares these properties anywhere; they are set imperatively by the tool
  that created the file and never restored.
- The declaration must be something git itself carries into every clone,
  readable before any tool has been installed or configured there.
- Application must happen at every materialization point safegit controls,
  not only at clone: a pull that brings in a newly released record must
  leave it read-only; a switch to another branch must apply that branch's
  declarations; a repair command must be able to heal existing drift.

## Solution

### The declaration: a git attribute vocabulary (recommended)

Declare properties per path pattern in `.gitattributes` under a safegit-
owned attribute vocabulary. Attributes are a tree fact: present in every
clone, pattern-based with per-path overrides, parsed by git, and safegit
already reads attributes from a tree (`git check-attr --source <tree-ish>`
in `internal/git/conflict.go`, the conflict-marker precedent). The
vocabulary is open in design and closed in implementation — every property
is registered, and an unknown value is a hard error. Grounded properties:

- a mode property (read-only 444 and restrictive 600 are the two live
  fleet cases; octal or a symbolic spelling is a naming decision);
- immutability, consumed by the sibling design for a commit-time refusal
  (a change to an immutable path is refused without an explicit election) —
  the same declaration feeds both the refusal at commit time and the
  read-only bits at materialization time, one declaration with two
  consumers.

Pros: git-native and clone-safe; per-path granularity with overrides
(directories that mix immutable records with mutable siblings are
expressible, which a per-directory declaration cannot do); no new file
format and no filename decision; the same attribute serves the commit-time
refusal.
Cons: `.gitattributes` is a shared file that other tools also edit; the
vocabulary must be documented in safegit's own docs with a stability
contract; git attributes cannot express anything conditional.

### The application points

- **`safegit clone`** — a subset-law wrapper over `git clone`: clone, install
  safegit's hooks, then apply every declared property to the checked-out
  tree. The one entry point where nothing of safegit's exists yet, which is
  why a hook alone cannot cover it.
- **After safegit's own tree-writing operations** — `switch`, `pull`,
  `merge`, the cherry-pick and revert conclusions, `mv`, and any other path
  that writes files to the working tree: one shared apply step after the
  write, restricted to the paths the operation touched.
- **`doctor --action fix`** — heal existing drift: every declared path whose
  on-disk property disagrees with its declaration is reported by
  `--action diagnose` and corrected by `fix`.
- **Optionally, a `post-checkout` git hook** installed through safegit's hook
  store, so raw `git checkout` / `git switch` / `git worktree add` by a
  user not going through safegit also apply declarations. Per-clone, so it
  only exists after `safegit clone` or a hook install — which is exactly
  what makes the clone command necessary rather than sufficient.

### Alternatives considered

- **A per-directory manifest file declaring the properties.** Git-unaware
  (git does not read it, so no git operation can honor it), directory
  granularity only (the fleet's changelog directories mix read-only records
  with mutable siblings), and it requires a reserved filename and a format.
  Rejected for this purpose; a manifest can carry documentary metadata but
  is the wrong instrument for a property git must carry into every clone.
- **A post-checkout hook only.** Covers raw git operations but does not
  exist in a fresh clone until something installs it — the clone case is the
  one that needs it most.
- **Do nothing; rely on commit-time refusals alone.** Integrity is preserved
  (a change cannot be committed), but the accident-protection the read-only
  bit gives an editor or an agent is lost on every clone. The two are
  complements, not substitutes.

### Constraints from safegit's own architecture

- Every chmod goes through the effects handle, so a dry run of `clone`,
  `doctor --action fix`, or any tree-writing operation previews the
  properties it would apply, listing paths.
- The apply step must never touch a path outside the declared set and must
  never relax a stricter mode the user set by hand unless the declaration
  says so — declarations are applied, not enforced as a ceiling, unless a
  property explicitly declares otherwise.
- A declared property that cannot be applied (unsupported filesystem,
  permission denied) is a hard error naming the path, never a warning.
- `clone` forwards a default-deny subset of git's flags, like the other
  wrapped commands; every refused flag is named.
- Consumers that deliberately rewrite a read-only file (an unlock-rewrite-
  relock sequence around a released record) keep working: they change the
  mode, write, and restore it; the declaration and their relock agree.
- New refusals get registered exit codes and divergence entries per the
  registry's own rules; the payload of every applying command names the
  paths it applied and the properties it applied to them.

## Affected files

- `main.go` — the `clone` command; `doctor` gains the diagnose/fix arm.
- a new module for the attribute vocabulary and the shared apply step.
- `internal/git/` — the `check-attr` helper generalized for the vocabulary.
- the tree-writing operations' post-write sites (`switch`, `pull`, `merge`,
  the conclusions, `mv`).
- the hook store, if the `post-checkout` hook is in scope.
- `internal/exitcode/` registry, the generated exit table, and
  `docs/divergences.md` entries.
- `docs/commands-guide.md` and the attribute vocabulary's own page.
- tests under `internal/test/`: clone applies declarations; pull of a new
  declared file applies; switch applies the target branch's declarations;
  doctor detects and heals drift; dry runs preview without applying; an
  unknown property value is refused; the unlock-rewrite-relock consumer
  path round-trips.

## Open decisions (owner)

- The attribute name(s) and the property vocabulary's spellings.
- Whether the raw-git `post-checkout` hook is in scope for the first cut.
- Whether declared properties are applied silently or always reported in
  the human output (the machine payload names them regardless).
- Whether `doctor --action diagnose` treats drift as a warning or an error.
- Whether `.gitattributes` is the only declaration source, or safegit also
  honors a repository-level declaration file it owns.

## Effort

Medium: two to three days including the clone wrapper, the shared apply
step at the existing write sites, the doctor arm, registry and divergence
obligations, docs, and the test set above. The optional hook adds half a
day.
