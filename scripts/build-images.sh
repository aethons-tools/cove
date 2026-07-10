#!/usr/bin/env bash
set -euo pipefail

# build-images.sh — build OCI images used by at-cove.

cd "$(dirname "$0")/.."  # repo root

# Build the at-task executable
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
# -s -w: strip symbol/debug tables (smaller binary);
# -X main.version: stamp the version reported by `at-* version`.
LDFLAGS="-s -w -X main.version=${VERSION}"
TARGETS=(amd64 arm64)
echo "Building at-task ${VERSION}"
built=()
for arch in "${TARGETS[@]}"; do
  dir="images/.build-install-files/${arch}"
  mkdir -p "$dir"
  echo "  building ${arch} -> ${dir}/at-task"
  # -trimpath: reproducible paths
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$dir/at-task" "./cmd/at-task"
  built+=("$dir/at-task")
done
echo
echo "Done. Built this run:"
ls -lh "${built[@]}"

docker build --progress plain --target base --tag cove-base images
