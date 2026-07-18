#!/usr/bin/env bash
# build.sh — build at-cove and at-task, host-sensitive by default.
#
# The binaries are pure Go (no cgo; at-cove shells out to ssh/docker), so
# Go's built-in cross-compilation just works — no toolchain or framework
# needed. Every binary lands in a directory keyed by its OS/arch:
#
#   dist/<os>-<arch>/<binary>   # e.g. dist/linux-amd64/{at-cove,at-task}
#
# The DEFAULT build targets the current host (go env GOOS/GOARCH). That keeps a
# mac and a linux box (e.g. the cove VM) that share the same output folder from
# stepping on each other: the mac writes dist/darwin-arm64/, the linux box writes
# dist/linux-amd64/, and neither overwrites the other's binary.
#
# Usage:
#   scripts/build.sh                 # current host only (host-sensitive)
#   scripts/build.sh all             # cross-compile every supported target
#   scripts/build.sh darwin/arm64 linux/amd64   # specific targets
#   OUT=/path scripts/build.sh       # override the output root (default: dist)
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

OUT="${OUT:-dist}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
# -trimpath: reproducible paths; -s -w: strip symbol/debug tables (smaller
# binary); -X main.version: stamp the version reported by `at-cove version`.
LDFLAGS="-s -w -X main.version=${VERSION}"

ALL_TARGETS=(darwin/amd64 darwin/arm64 linux/amd64 linux/arm64)
BINARIES=(at-cove at-task at-mint)

# Stage the linux at-task binaries at-cove embeds, before at-cove is built —
# shared with the goreleaser before-hook, see scripts/stage-attask.sh.
echo "Staging embedded at-task (linux amd64+arm64)"
VERSION="$VERSION" ./scripts/stage-attask.sh

# Snapshot the blessed cove-base-image digests from the registry into the
# embedded (gitignored) blessed/generated.txt, before at-cove is built (COV-44
# spec §4). With no GITHUB_TOKEN this is a no-op: the committed low-watermark is
# embedded alone, so local/offline builds never need registry access. CI runs it
# with a packages:read token to pick up every current base with no commit-back loop.
echo "Snapshotting blessed cove-base-image digests (no-op offline)"
go run ./cmd/gen-blessed

# Choose targets from the args (default: just the current host).
case "${1:-}" in
  "" | host) TARGETS=("$(go env GOOS)/$(go env GOARCH)") ;;
  all)       TARGETS=("${ALL_TARGETS[@]}") ;;
  *)         TARGETS=("$@") ;;
esac

echo "Building cove ${VERSION} (${BINARIES[*]})"
built=()
for t in "${TARGETS[@]}"; do
  os="${t%/*}"
  arch="${t#*/}"
  dir="${OUT}/${os}-${arch}"
  mkdir -p "$dir"
  for bin in "${BINARIES[@]}"; do
    echo "  building ${t} -> ${dir}/${bin}"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$dir/$bin" "./cmd/$bin"
    built+=("$dir/$bin")
  done
done

echo
echo "Done. Built this run:"
ls -lh "${built[@]}"
