#!/usr/bin/env bash
# whiteboard.sh — build the whiteboard editor bundle (Excalidraw) that at-jam
# embeds, into internal/jam/uiassets/whiteboard/ (git-ignored). Needs Node and
# npm with access to the npm registry; the lockfile pins every package.
#
# Without the bundle at-jam still builds and works: /app/user hides New
# whiteboard and board cards say the editor isn't built.
#
#   scripts/whiteboard.sh            # build (npm ci + esbuild)
#   WHITEBOARD_OPTIONAL=1 scripts/whiteboard.sh   # skip, with a warning, when npm is missing
set -euo pipefail
cd "$(dirname "$0")/../web/whiteboard"

if ! command -v npm >/dev/null 2>&1; then
  if [ "${WHITEBOARD_OPTIONAL:-}" = 1 ]; then
    echo "whiteboard: npm not found; skipping the editor bundle (at-jam builds without it)" >&2
    exit 0
  fi
  echo "whiteboard: npm is required (install Node 20+), or set WHITEBOARD_OPTIONAL=1" >&2
  exit 1
fi
npm ci --no-audit --no-fund --loglevel=error
node build.mjs
echo "whiteboard: bundle built into internal/jam/uiassets/whiteboard/"
