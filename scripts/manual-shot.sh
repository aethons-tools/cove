#!/usr/bin/env bash
# manual-shot.sh — screenshot one view of the Jam app on mock data into the
# manual (docs/modules/ROOT/pages/manual.adoc): serves web/app with Vite on its mock API
# (web/app/src/shots/mocks.ts) and shoots it with the kit's headless Chrome
# (JAM_BROWSER overrides). Paths are relative to the repo root.
#
#   scripts/manual-shot.sh <route> --out manual/modules/<area>/images/<name>.png \
#     [--selector <css>] [--pad 8] [--width 1280] [--height 800] [--scale 2]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/web/app"
"$root/scripts/npm-ci.sh"
SHOT_CWD="$root" node scripts/shot.mjs "$@"
