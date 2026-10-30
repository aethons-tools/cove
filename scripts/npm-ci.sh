#!/usr/bin/env bash
# npm-ci.sh — `npm ci` in the current directory, skipped when node_modules was
# already installed from this exact package-lock.json by this Node (a stamp in
# node_modules records both). One CI job installs web/app once, though the app
# bundle, the app checks and the VS Code webview build each need it; a local
# rebuild skips the reinstall too. FORCE_NPM_CI=1 always reinstalls.
set -euo pipefail

stamp=node_modules/.jam-npm-ci
want="$(node --version) $(sha256sum package-lock.json | cut -d' ' -f1)"
if [ "${FORCE_NPM_CI:-0}" != 1 ] && [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$want" ]; then
  exit 0
fi
npm ci --no-audit --no-fund --loglevel=error
echo "$want" > "$stamp"
