#!/usr/bin/env bash
set -euo pipefail

# build-images.sh — build the cove image tree locally (jam-studio-base, then
# jam-studio FROM it). A convenience for local iteration; CI builds these
# multi-arch in .github/workflows/release.yml. The base is pure tools (no
# cove-master, no Go) — Jam's launcher installs cove-master in the hardening
# layer when it builds a studio image.

cd "$(dirname "$0")/.."  # repo root

echo "Building jam-studio-base"
docker build --progress plain \
  --tag jam-studio-base:latest \
  images/jam-studio-base

echo "Building jam-studio"
docker build --progress plain \
  --build-arg BASE_TAG=latest \
  --tag jam-studio:latest \
  images/jam-studio
