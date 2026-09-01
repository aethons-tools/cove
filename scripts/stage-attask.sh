#!/usr/bin/env bash
# stage-attask.sh — build the linux at-task and at-switchboard binaries at-cove
# embeds, into internal/attask/bin/ and internal/atswitchboard/bin/, BEFORE
# at-cove is built so their `//go:embed bin` picks them up. Both arches always
# (at-cove may build a sandbox for either VM arch). Shared by scripts/build.sh
# and the goreleaser before-hook so the staging logic has a single home. See
# internal/attask + COV-36, internal/atswitchboard + COV-135.
#
# VERSION (env, optional) stamps the embedded binaries so they match the
# at-cove built alongside them; defaults to `git describe`.
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"

dir="internal/attask/bin"
mkdir -p "$dir"
for a in amd64 arm64; do
  echo "  staging at-task linux/${a} (${VERSION})"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${dir}/at-task-linux-${a}" ./cmd/at-task
done

switchboard_dir="internal/atswitchboard/bin"
mkdir -p "$switchboard_dir"
for a in amd64 arm64; do
  echo "  staging at-switchboard linux/${a} (${VERSION})"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${switchboard_dir}/at-switchboard-linux-${a}" ./cmd/at-switchboard
done
