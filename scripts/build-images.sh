#!/usr/bin/env bash
set -euo pipefail

# build-images.sh — build the cove image tree locally (cove-base-image, then
# cove-image FROM it). A convenience for local iteration; CI builds these
# multi-arch in .github/workflows/release.yml. The base is pure tools (no
# cove-master, no Go) — Jam's launcher installs cove-master in the hardening
# layer when it builds a studio image.

cd "$(dirname "$0")/.."  # repo root

echo "Building cove-base-image"
docker build --progress plain \
  --tag cove-base-image:latest \
  images/cove-base-image

echo "Building cove-image"
docker build --progress plain \
  --build-arg BASE_TAG=latest \
  --tag cove-image:latest \
  images/cove-image
