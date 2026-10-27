#!/usr/bin/env bash
# jam-image.sh — build the Jam container image from already-built release
# binaries (no compiling). Usage:
#   scripts/jam-image.sh [--dist DIR] [--platforms LIST] [--tag T]... [--push]
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"

dist=dist
platforms=""
tags=()
push=0

while [ $# -gt 0 ]; do
  case "$1" in
    --dist) dist="${2:?--dist needs a value}"; shift 2 ;;
    --platforms) platforms="${2:?--platforms needs a value}"; shift 2 ;;
    --tag) tags+=("${2:?--tag needs a value}"); shift 2 ;;
    --push) push=1; shift ;;
    *) echo "jam-image: unknown argument: $1" >&2; exit 2 ;;
  esac
done

[ -n "$platforms" ] || platforms="linux/$(go env GOARCH)"
[ "${#tags[@]}" -gt 0 ] || tags=(cove-jam:dev)

IFS=, read -r -a plist <<<"$platforms"
if [ "$push" -eq 0 ] && [ "${#plist[@]}" -gt 1 ]; then
  echo "jam-image: --load supports one platform; use --push" >&2
  exit 1
fi

ctx="$(mktemp -d)"
trap 'rm -rf "$ctx"' EXIT
cp "$REPO/images/jam/Dockerfile" "$ctx/Dockerfile"

# looked_in <binary> <arch>: the candidate paths, for error messages.
looked_in() { echo "$dist/linux-$2/$1, $dist/${1}_linux_${2}*/$1"; }

# find_bin <binary> <arch>: print the first existing path, else fail.
find_bin() {
  local bin="$1" arch="$2" p
  p="$dist/linux-$arch/$bin"
  [ -f "$p" ] && { printf '%s\n' "$p"; return 0; }
  for p in "$dist"/"${bin}"_linux_"${arch}"*/"$bin"; do
    [ -f "$p" ] && { printf '%s\n' "$p"; return 0; }
  done
  return 1
}

for plat in "${plist[@]}"; do
  case "$plat" in
    linux/amd64|linux/arm64) arch="${plat#linux/}" ;;
    *) echo "jam-image: unsupported platform $plat (only linux/amd64, linux/arm64)" >&2; exit 1 ;;
  esac
  mkdir -p "$ctx/bin/linux-$arch"
  for bin in at-jam at-mint; do
    if ! src="$(find_bin "$bin" "$arch")"; then
      echo "jam-image: no $bin for $plat (looked in $(looked_in "$bin" "$arch"))" >&2
      exit 1
    fi
    cp "$src" "$ctx/bin/linux-$arch/$bin"
  done
done

# Multi-platform --push needs a docker-container buildx builder (CI runs
# setup-buildx); the default docker driver only builds the host platform.
args=(buildx build --platform "$platforms")
for t in "${tags[@]}"; do args+=(-t "$t"); done
if [ "$push" -eq 1 ]; then args+=(--push); else args+=(--load); fi
docker "${args[@]}" "$ctx"
