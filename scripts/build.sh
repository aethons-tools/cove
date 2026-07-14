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
