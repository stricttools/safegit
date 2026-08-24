# experiments/

Scratch space for probing git behavior: throwaway repositories, captures,
one-off outputs. Everything here except this file is gitignored and
disposable, and the repository's own integrity guards — which deliberately
scan gitignored files everywhere else — skip this directory, so a scratch
git repository here cannot turn a guard red.

Rules:

- Raw artifacts stay here, uncommitted, and may be deleted at any time.
- Anything reusable is promoted OUT of here: a generator script goes to
  `scripts/`, a finding becomes a test in the suite. The test suite, not
  saved artifacts, is where findings become permanent.
- This directory predates the fleet-wide scaffold-owned experiments
  convention; when the scaffold ships its version, this directory is
  reconciled to it.
