#!/usr/bin/env bash
set -euo pipefail
# This hook runs BEFORE built-in pre-release checks (tests, lint).
# Use it for setup tasks: starting services, setting env vars, etc.
# Built-in checks run after this hook. Custom validation goes in pre-release.sh.

# testdata/exit-sites.txt is a mechanically generated census of every
# process-exit site in the production sources, and its rows carry FILE LINE
# NUMBERS. Any commit that adds or moves a line in a file holding an exit site
# invalidates them, so regenerating it on every change would produce a stream of
# commits that say nothing. It is therefore regenerated HERE and nowhere else:
# between releases its line numbers are known-stale and are not to be trusted,
# and every release ships one census that matches the code it shipped.
#
# A regeneration that changes the file blocks the release. The census is
# committed state, and a release must never carry an uncommitted change to it.
scripts/exit-inventory > testdata/exit-sites.txt
if ! git diff --quiet -- testdata/exit-sites.txt; then
  echo "error: testdata/exit-sites.txt was stale and has been regenerated." >&2
  echo "       Commit it, then run the release again:" >&2
  echo "         safegit commit -m 'testdata: regenerate the exit-site census' -- testdata/exit-sites.txt" >&2
  exit 1
fi
