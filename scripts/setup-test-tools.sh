#!/usr/bin/env bash
#
# setup-test-tools.sh — install the tooling atsbx end-to-end testing needs.
#
# Installs:
#   - podman + podman-docker (a `docker` shim) so the Colima backend's
#     `docker {build,run,port,inspect,rm}` calls work without Docker Desktop;
#     plus the rootless plumbing (uidmap, fuse-overlayfs, slirp4netns).
#   - shellcheck — lints entrypoint.sh and the remote shell atsbx emits.
#   - hadolint   — lints the embedded Dockerfile (from GitHub releases).
#   - jq         — validate managed-settings.json / config.yml by hand.
#
# Target: Debian/Ubuntu (the image base is ubuntu:24.04). Arch-aware (arm64/amd64).
# Idempotent and safe to re-run. Uses sudo for system installs.
#
# After this, run the full local test loop:
#   go test ./...                              # hermetic unit tests
#   go test -tags integration ./internal/...  # real ssh/sshd integration
#   shellcheck internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh
#   hadolint   internal/assemble/hardening/Dockerfile
#
# Rootless-podman caveat: the sandbox image locks egress with nftables+squid
# inside the container's netns (needs --cap-add=NET_ADMIN, which atsbx already
# passes). Rootless networking (slirp4netns/pasta) differs from Docker's bridge;
# if the in-container egress lockdown misbehaves, retry with `sudo podman` (the
# `docker` shim respects sudo) or run the VM with rootful podman.

set -euo pipefail

HADOLINT_VERSION="v2.12.0"

log() { printf '\n\033[1;34m==>\033[0m %s\n' "$*"; }

if ! command -v apt-get >/dev/null 2>&1; then
  echo "This script targets Debian/Ubuntu (apt-get not found)." >&2
  echo "Install equivalents of: podman podman-docker shellcheck jq hadolint." >&2
  exit 1
fi

# Map uname -m to the labels apt and hadolint use.
case "$(uname -m)" in
  aarch64 | arm64) APT_ARCH="arm64";  HADO_ARCH="arm64"  ;;
  x86_64  | amd64) APT_ARCH="amd64";  HADO_ARCH="x86_64" ;;
  *) echo "Unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac
log "Detected arch: $(uname -m) (apt=$APT_ARCH, hadolint=$HADO_ARCH)"

log "Installing apt packages (podman + rootless plumbing, shellcheck, jq)"
sudo apt-get update -y
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
  podman podman-docker uidmap fuse-overlayfs slirp4netns \
  shellcheck jq ca-certificates curl

# Silence podman-docker's "Emulate Docker CLI using podman" banner on every call.
if [ ! -e /etc/containers/nodocker ]; then
  sudo mkdir -p /etc/containers
  sudo touch /etc/containers/nodocker
fi

# Rootless podman needs subuid/subgid ranges for the current user. Ubuntu sets
# these when a user is created, but add them if missing.
USER_NAME="${SUDO_USER:-$USER}"
if ! grep -q "^${USER_NAME}:" /etc/subuid 2>/dev/null; then
  log "Adding subuid/subgid ranges for ${USER_NAME} (rootless podman)"
  sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "${USER_NAME}"
  command -v podman >/dev/null && podman system migrate || true
fi

log "Installing hadolint ${HADOLINT_VERSION} (${HADO_ARCH})"
HADO_URL="https://github.com/hadolint/hadolint/releases/download/${HADOLINT_VERSION}/hadolint-Linux-${HADO_ARCH}"
TMP_HADO="$(mktemp)"
curl -fsSL "$HADO_URL" -o "$TMP_HADO"
sudo install -m 0755 "$TMP_HADO" /usr/local/bin/hadolint
rm -f "$TMP_HADO"

log "Versions:"
podman --version
docker --version || echo "  (docker shim: install ok; first run prints podman version)"
shellcheck --version | sed -n '1,2p'
hadolint --version
jq --version

log "Done. Smoke-test podman with:  docker run --rm hello-world  (needs egress)"
