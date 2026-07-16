#!/usr/bin/env bash
set -euo pipefail

# build-images.sh — build the cove image tree locally (cove-base-image, then
# cove-image FROM it). A convenience for local iteration; CI builds these
# multi-arch in .github/workflows/build-images.yml. Slated for retirement in
# COV-34, when at-cove consumes the published cove-image rather than a
# locally-built base.

cd "$(dirname "$0")/.."  # repo root

ARCH="$(go env GOARCH)"
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
# -s -w: strip symbol/debug tables; -X main.version: stamp `at-* version`;
# -trimpath: reproducible paths.
LDFLAGS="-s -w -X main.version=${VERSION}"

echo "Building at-task ${VERSION} (${ARCH}) -> images/cove-base-image/at-task"
CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" \
  go build -trimpath -ldflags "${LDFLAGS}" -o images/cove-base-image/at-task ./cmd/at-task

echo "Building cove-base-image"
# Also tag cove-base:latest: internal/assemble/hardening/Dockerfile still does
# `FROM cove-base:latest` (rebased onto cove-image in COV-34).
docker build --progress plain \
  --tag cove-base-image:latest --tag cove-base:latest \
  images/cove-base-image

echo "Building cove-image"
docker build --progress plain \
  --build-arg BASE_TAG=latest \
  --tag cove-image:latest \
  images/cove-image
