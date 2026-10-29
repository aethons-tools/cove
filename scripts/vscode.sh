#!/usr/bin/env bash
# vscode.sh — check, bundle and package the Jam VS Code extension (web/vscode)
# into a .vsix, with web/app's webview bundle. Needs Node and npm with access
# to the npm registry.
#
#   scripts/vscode.sh                          # -> web/vscode/dist/jam.vsix
#   VERSION=123-1029 scripts/vscode.sh         # stamps version 0.123.0
#   VSIX_OUT=dist/jam-vscode_1.vsix scripts/vscode.sh
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"

# vsce needs SemVer: <N>-<MMDD> becomes 0.<N>.0.
ver=0.0.0
if [ -n "${VERSION:-}" ]; then ver="0.${VERSION%%-*}.0"; fi

out="${VSIX_OUT:-web/vscode/dist/jam.vsix}"
case "$out" in /*) ;; *) out="$PWD/$out" ;; esac
mkdir -p "$(dirname "$out")"

cd "$root/web/vscode"
npm ci --no-audit --no-fund --loglevel=error
npm run check
# The Conversation pane's webview bundle (web/app/vite.vscode.config.ts) into
# dist/webview, which the .vsix ships beside the extension.
(cd ../app && npm ci --no-audit --no-fund --loglevel=error && npx vite build -c vite.vscode.config.ts --logLevel warn)
for f in dist/webview/pane.js dist/webview/pane.css; do
  if [ ! -f "$f" ]; then echo "vscode: the webview build left no web/vscode/$f" >&2; exit 1; fi
done
node esbuild.mjs
npx vsce package --no-dependencies --skip-license --no-update-package-json "$ver" -o "$out"
echo "vscode: built $out ($ver)"
