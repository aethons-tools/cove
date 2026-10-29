#!/usr/bin/env bash
# vscode.sh — check, bundle and package the Jam VS Code extension (web/vscode)
# into a .vsix. Needs Node and npm with access to the npm registry.
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
node esbuild.mjs
npx vsce package --no-dependencies --skip-license --no-update-package-json "$ver" -o "$out"
echo "vscode: built $out ($ver)"
