#!/usr/bin/env bash
# Post-release hook. Runs after a successful rlsbl release.
# Installs the freshly built safegit binary to $GOBIN (or $GOPATH/bin).

set -euo pipefail

echo "Installing safegit v$RLSBL_VERSION..."
go install .
echo "Installed: $(which safegit)"

# Push to assembly for unified documentation site
if command -v selfdoc &>/dev/null && [ -f selfdoc.json ]; then
  if python3 -c "import json; c=json.load(open('selfdoc.json')); exit(0 if c.get('assembly') or (c.get('topology') or {}).get('assembly') else 1)" 2>/dev/null; then
    echo "Pushing to documentation assembly..."
    selfdoc assembly push || echo "Warning: assembly push failed (non-fatal)"
  fi
fi
