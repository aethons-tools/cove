#!/usr/bin/env bash
# manual.sh — build the customer manual (manual/, Released version only,
# internal content removed) that jam embeds, into internal/jam/uiassets/manual/
# (git-ignored). Needs Node and npm with access to the npm registry. Without
# it jam still builds: /app/manual/ answers "not built". See docs/web-app.md
# and docs/manual.md.
#
#   scripts/manual.sh                  # build (npm ci + antora)
#   APP_OPTIONAL=1 scripts/manual.sh   # skip, with a warning, when npm is missing
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/manual"

if ! command -v npm >/dev/null 2>&1; then
  if [ "${APP_OPTIONAL:-}" = 1 ]; then
    echo "manual: npm not found; skipping the customer manual (jam builds without it, and /app/manual/ answers \"not built\")" >&2
    exit 0
  fi
  echo "manual: npm is required (install Node 22): the manual is served at /app/manual/; APP_OPTIONAL=1 skips it" >&2
  exit 1
fi
out="$root/internal/jam/uiassets/manual"
# Build into a scratch directory and swap it in only on success, so a failed
# build never leaves the embedded directory empty (go:embed needs .keep).
rm -rf "$out.tmp" "$out.old"
"$root/scripts/npm-ci.sh"
npx antora antora-playbook.jam.yml
cp "$out/.keep" "$out.tmp/.keep"
mv "$out" "$out.old"
mv "$out.tmp" "$out"
rm -rf "$out.old"
echo "manual: customer manual built into internal/jam/uiassets/manual/"
