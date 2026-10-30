#!/usr/bin/env bash
# local.sh — a local build whose pieces point at each other (`just local`):
#
#   1. jam-studio-base:dev-<id>, then jam-studio:dev-<id> FROM it, built in the
#      Docker your Studio Manager builds studios in;
#   2. internal/basedigest/blessed/local.txt naming that base (git-ignored), so
#      the jam and jam-studio-manager built next use it as their default studio
#      base and trust it (and anything built FROM it) in the provenance gate;
#   3. dist/<host>/{jam,jam-studio-manager} and dist/linux-<arch>/ with that
#      embedded, the VS Code extension included;
#   4. jam:dev, the Jam container image, from those binaries, in the same Docker.
#
# <id> is the image ID's first 12 hex digits, so a tag never moves to other
# content. The published bases stay trusted too: kits on the ghcr images keep
# working. `just local-clean` removes local.txt, so the next build is a normal
# one; until then every `just build` embeds the local base too (and says so).
#
# DOCKER_CONTEXT picks the Docker (default: colima when that context exists,
# else the current one). LOCAL_SKIP_IMAGES=1 reuses the images local.txt names.
set -euo pipefail

cd "$(dirname "$0")/.."  # repo root

local_file=internal/basedigest/blessed/local.txt

if [ -z "${DOCKER_CONTEXT:-}" ]; then
  if docker context ls -q 2>/dev/null | grep -qx colima; then
    DOCKER_CONTEXT=colima
  else
    DOCKER_CONTEXT="$(docker context show)"
  fi
fi
export DOCKER_CONTEXT
arch="$(docker version -f '{{.Server.Arch}}')"
case "$arch" in
  amd64|arm64) ;;
  *) echo "local: Docker ($DOCKER_CONTEXT) is $arch; only amd64 and arm64 are supported" >&2; exit 1 ;;
esac
echo "local: Docker context $DOCKER_CONTEXT (linux/$arch)"

# build_image <name> <dir> [build args...]: builds <dir> as <name>:dev-<id> and
# prints that ref.
build_image() {
  local name="$1" dir="$2" id ref
  shift 2
  docker build --progress plain "$@" --tag "$name:dev-build" "$dir" >&2
  id="$(docker image inspect -f '{{.Id}}' "$name:dev-build")"
  id="${id#sha256:}"
  ref="$name:dev-${id:0:12}"
  docker tag "$name:dev-build" "$ref"
  docker rmi "$name:dev-build" >/dev/null
  printf '%s\n' "$ref"
}

if [ "${LOCAL_SKIP_IMAGES:-0}" = 1 ]; then
  [ -s "$local_file" ] || { echo "local: LOCAL_SKIP_IMAGES=1 but there is no $local_file; run just local once without it" >&2; exit 1; }
  base="$(grep -v '^#' "$local_file" | head -1)"
  docker image inspect "$base" >/dev/null || { echo "local: $base is not in Docker ($DOCKER_CONTEXT)" >&2; exit 1; }
  studio="$(docker image ls --format '{{.Repository}}:{{.Tag}}' jam-studio | grep ':dev-' | head -1 || true)"
else
  echo "local: building jam-studio-base"
  base="$(build_image jam-studio-base images/jam-studio-base)"
  echo "local: building jam-studio FROM $base"
  studio="$(build_image jam-studio images/jam-studio --build-arg "BASE_TAG=${base#jam-studio-base:}")"
  printf '# written by scripts/local.sh (just local); just local-clean removes it\n%s\n' "$base" > "$local_file"
fi

echo "local: building the VS Code extension jam serves"
VSCODE_SKIP_CHECK=1 ./scripts/stage-vscode.sh

host="$(go env GOOS)/$(go env GOARCH)"
targets=("$host")
[ "$host" = "linux/$arch" ] || targets+=("linux/$arch")
./scripts/build.sh "${targets[@]}"

echo "local: building jam:dev"
./scripts/jam-image.sh --platforms "linux/$arch" --tag jam:dev

cat <<EOF

local: done. These point at each other:
  studio base   $base  (jam's default; trusted by the gate)
  studio image  ${studio:-?}  (FROM the base)
  binaries      dist/${host/\//-}/{jam,jam-studio-manager}$( [ "$host" = "linux/$arch" ] || echo ", dist/linux-$arch/" )
  Jam image     jam:dev  (just dev-jam-up)
  all in Docker context $DOCKER_CONTEXT

Run this build's jam-studio-manager too: it builds studios FROM the local base.
For a kit on the local jam-studio, set in its kit.yml:  base: {image: ${studio:-jam-studio:dev-<id>}}
Back to a normal build: just local-clean
EOF
