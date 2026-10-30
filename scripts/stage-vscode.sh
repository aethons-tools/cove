#!/usr/bin/env bash
# stage-vscode.sh — build the Jam VS Code extension's .vsix (scripts/vscode.sh)
# into internal/jam/vscodeext/vsix/jam.vsix (git-ignored), BEFORE jam is built
# so its `//go:embed all:vsix` picks it up: Jam serves the extension it was
# released with (`jam vscode install`, the extension's update check). The
# goreleaser before-hook runs it; release.yml attaches the same file to the
# release. Needs Node and npm with access to the npm registry.
#
# VERSION (env, optional, <N>-<MMDD>) stamps the extension 0.<N>.0, as
# scripts/vscode.sh does; without it the extension is 0.0.0, which never
# offers itself as an update.
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

VSIX_OUT="internal/jam/vscodeext/vsix/jam.vsix" ./scripts/vscode.sh
