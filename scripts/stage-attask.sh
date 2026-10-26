#!/usr/bin/env bash
# stage-attask.sh — build the linux at-task and cove-master binaries at-cove
# embeds, into internal/attask/bin/ and internal/covemasterbin/bin/, BEFORE
# at-cove is built so their `//go:embed bin` picks them up. Both arches always
# (at-cove may build a sandbox for either VM arch). Shared by scripts/build.sh
# and the goreleaser before-hook so the staging logic has a single home. See
# internal/attask + COV-36, internal/covemasterbin + COV-158.
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

covemaster_dir="internal/covemasterbin/bin"
mkdir -p "$covemaster_dir"
for a in amd64 arm64; do
  echo "  staging cove-master linux/${a} (${VERSION})"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${covemaster_dir}/cove-master-linux-${a}" ./cmd/cove-master
done
