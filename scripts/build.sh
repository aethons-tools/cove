#!/usr/bin/env bash
# build.sh — cross-compile at-cove for every supported host target.
#
# at-cove is pure Go (no cgo; it shells out to ssh/docker), so Go's built-in
# cross-compilation just works — no toolchain or framework needed. One binary
# per target, each in its own directory:
#
#   dist/<os>-<arch>/at-cove
#
# Targets (GOOS/GOARCH; darwin == macOS):
#   darwin/amd64  darwin/arm64  linux/amd64  linux/arm64
#
# Usage:
#   scripts/build.sh            # build all targets into ./dist
#   OUT=/tmp/out scripts/build.sh   # override the output root
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

OUT="${OUT:-dist}"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
# -trimpath: reproducible paths; -s -w: strip symbol/debug tables (smaller binary).
LDFLAGS="-s -w"

TARGETS=(
  darwin/amd64
  darwin/arm64
  linux/amd64
  linux/arm64
)

echo "Building at-cove ${VERSION}"
for t in "${TARGETS[@]}"; do
  os="${t%/*}"
  arch="${t#*/}"
  dir="${OUT}/${os}-${arch}"
  mkdir -p "$dir"
  echo "  building ${t} -> ${dir}/at-cove"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$dir/at-cove" .
done

echo
echo "Done. Artifacts under ${OUT}/:"
find "$OUT" -type f -name at-cove -exec ls -lh {} +
