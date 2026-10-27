#!/usr/bin/env bash
# stage-studio-agent.sh — build the linux jam-studio-agent binaries jam embeds into
# internal/studioagentbin/bin/, BEFORE jam is built so its `//go:embed bin`
# picks them up. Both arches always (a studio may run on either VM arch).
# Shared by scripts/build.sh and the goreleaser before-hook so the staging logic
# has a single home. See internal/studioagentbin + COV-158.
#
# VERSION (env, optional) stamps the embedded binaries so they match the
# jam built alongside them; defaults to `git describe`.
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"

studioagent_dir="internal/studioagentbin/bin"
mkdir -p "$studioagent_dir"
for a in amd64 arm64; do
  echo "  staging jam-studio-agent linux/${a} (${VERSION})"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${studioagent_dir}/jam-studio-agent-linux-${a}" ./cmd/jam-studio-agent
done
