#!/usr/bin/env bash
# app.sh — build the Jam app bundle (web/app) that jam embeds, into
# internal/jam/uiassets/app/ (git-ignored). Needs Node and npm with access to
# the npm registry; the lockfile pins every package. Without the bundle jam
# still builds: /app/user/ answers "not built". See docs/web-app.md.
#
#   scripts/app.sh                  # build (npm ci + vite build + size budget)
#   APP_OPTIONAL=1 scripts/app.sh   # skip, with a warning, when npm is missing
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/web/app"

if ! command -v npm >/dev/null 2>&1; then
  if [ "${APP_OPTIONAL:-}" = 1 ]; then
    echo "app: npm not found; skipping the Jam app bundle (jam builds without it, and its user area /app/user/ answers \"not built\")" >&2
    exit 0
  fi
  echo "app: npm is required (install Node 22): the bundle is jam's user area (/app/user/); APP_OPTIONAL=1 skips it" >&2
  exit 1
fi
out="$root/internal/jam/uiassets/app"
# Build into a scratch directory and swap it in only on success, so a failed
# build never leaves the embedded directory empty (go:embed needs .keep).
rm -rf "$out.tmp" "$out.old"
npm ci --no-audit --no-fund --loglevel=error
APP_OUT_DIR="$out.tmp" npx vite build --logLevel warn
node scripts/size-budget.mjs "$out.tmp"
# Excalidraw's fonts, served from the bundle (no CDN: the app's CSP allows
# font-src 'self' only), without the 13 MB CJK Xiaolai set.
fonts=node_modules/@excalidraw/excalidraw/dist/prod/fonts
mkdir -p "$out.tmp/assets/excalidraw/fonts"
for f in "$fonts"/*; do
  [ "$(basename "$f")" = Xiaolai ] || cp -R "$f" "$out.tmp/assets/excalidraw/fonts/"
done
cp "$out/.keep" "$out.tmp/.keep"
mv "$out" "$out.old"
mv "$out.tmp" "$out"
rm -rf "$out.old"
echo "app: bundle built into internal/jam/uiassets/app/"
