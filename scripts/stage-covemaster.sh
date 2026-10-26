#!/usr/bin/env bash
# stage-covemaster.sh — build the linux cove-master binaries at-jam embeds into
# internal/studioagentbin/bin/, BEFORE at-jam is built so its `//go:embed bin`
# picks them up. Both arches always (a studio may run on either VM arch).
# Shared by scripts/build.sh and the goreleaser before-hook so the staging logic
# has a single home. See internal/studioagentbin + COV-158.
#
# VERSION (env, optional) stamps the embedded binaries so they match the
# at-jam built alongside them; defaults to `git describe`.
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"

covemaster_dir="internal/studioagentbin/bin"
mkdir -p "$covemaster_dir"
for a in amd64 arm64; do
  echo "  staging cove-master linux/${a} (${VERSION})"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${covemaster_dir}/cove-master-linux-${a}" ./cmd/cove-master
done
