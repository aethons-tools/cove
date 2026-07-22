#!/usr/bin/env bash
# install.sh — one-command installer for at-cove (and its sibling at-mint).
#
# Advertised:
#   Private phase (works today, uses your gh auth):
#     gh api -H "Accept: application/vnd.github.raw" \
#       /repos/aethons-tools/cove/contents/install.sh | bash
#   Public phase (once the repo is public — no change to this script):
#     curl -fsSL https://raw.githubusercontent.com/aethons-tools/cove/main/install.sh | bash
#
# It pulls a prebuilt release archive (cove_<version>_<os>_<arch>.tar.gz) that the
# CI pipeline already cuts on every push to main, verifies its SHA-256 against the
# release's checksums.txt, and drops `at-cove` + `at-mint` onto your PATH. `at-task`
# is embedded inside `at-cove`, so it is not installed separately.
#
# Env knobs:
#   COVE_VERSION=<N>-<MMDD>   pin a release (default: latest)
#   BINDIR=<dir>             install dir (wins over everything)
#   COVE_SYSTEM=1            install to /usr/local/bin (sudo if needed)
#   COVE_REPO=owner/name     override the source repo (default aethons-tools/cove)
#
# Download engine: prefer `gh` when installed + authenticated (handles the private
# repo); otherwise fall back to anonymous `curl` (which only succeeds once the repo
# is public). That single fork is what lets this script auto-upgrade to the plain
# `curl | bash` experience the day the repo goes public.
set -euo pipefail

REPO="${COVE_REPO:-aethons-tools/cove}"

err() { printf 'install: %s\n' "$*" >&2; }
info() { printf 'install: %s\n' "$*"; }

# normalize_arch <uname -m> -> goreleaser arch, or fail on unsupported.
normalize_arch() {
  case "$1" in
    x86_64 | amd64) echo amd64 ;;
    aarch64 | arm64) echo arm64 ;;
    *)
      err "unsupported architecture: $1 (need x86_64/amd64 or aarch64/arm64)"
      return 1
      ;;
  esac
}

# normalize_os <uname -s> -> goreleaser os, or fail on unsupported.
normalize_os() {
  case "$1" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    *)
      err "unsupported OS: $1 (need Linux or Darwin)"
      return 1
      ;;
  esac
}

# resolve_bindir -> the install directory, honouring BINDIR > COVE_SYSTEM > default.
resolve_bindir() {
  if [ -n "${BINDIR:-}" ]; then
    echo "$BINDIR"
  elif [ "${COVE_SYSTEM:-}" = 1 ]; then
    echo "${COVE_SYSTEM_BINDIR:-/usr/local/bin}"
  else
    echo "$HOME/.local/bin"
  fi
}

# sha256_of <file> -> hex digest (portable across coreutils / BSD).
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    err "need sha256sum or shasum to verify the download"
    return 1
  fi
}

# verify_checksum <archive> <checksums-file> (run in the dir holding them).
verify_checksum() {
  local archive="$1" sums="$2" expected actual
  expected="$(awk -v f="$archive" '$2 == f {print $1}' "$sums")"
  if [ -z "$expected" ]; then
    err "no checksum entry for $archive"
    return 1
  fi
  actual="$(sha256_of "$archive")"
  if [ "$expected" != "$actual" ]; then
    err "checksum mismatch for $archive"
    err "  expected $expected"
    err "  actual   $actual"
    return 1
  fi
}

# use_gh -> true when gh is present and authenticated (the private-repo path).
use_gh() {
  command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1
}

# resolve_version -> the release tag to install (COVE_VERSION, else latest).
resolve_version() {
  if [ -n "${COVE_VERSION:-}" ]; then
    echo "$COVE_VERSION"
    return
  fi
  if use_gh; then
    gh release view --repo "$REPO" --json tagName --jq .tagName
  elif command -v curl >/dev/null 2>&1; then
    curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
      grep -m1 '"tag_name"' | sed -E 's/.*"tag_name":[[:space:]]*"([^"]+)".*/\1/'
  else
    err "need gh (authenticated) or curl to resolve the latest version"
    return 1
  fi
}

# download_release <version> <archive> <destdir> — fetch archive + checksums.txt.
download_release() {
  local version="$1" archive="$2" dest="$3"
  if use_gh; then
    gh release download "$version" --repo "$REPO" \
      --pattern "$archive" --pattern checksums.txt --dir "$dest" --clobber
  elif command -v curl >/dev/null 2>&1; then
    local base="https://github.com/$REPO/releases/download/$version"
    curl -fsSL -o "$dest/$archive" "$base/$archive"
    curl -fsSL -o "$dest/checksums.txt" "$base/checksums.txt"
  else
    err "need gh (authenticated) or curl to download the release"
    err "the repo is private today — install gh and run: gh auth login"
    return 1
  fi
}

# place <src> <destdir> — install one 0755 binary, using sudo only if required.
place() {
  local src="$1" destdir="$2"
  if [ -w "$destdir" ] || { [ ! -e "$destdir" ] && mkdir -p "$destdir" 2>/dev/null; }; then
    install -m 0755 "$src" "$destdir/"
  else
    info "elevating with sudo to write $destdir"
    sudo mkdir -p "$destdir"
    sudo install -m 0755 "$src" "$destdir/"
  fi
}

# path_check <destdir> — warn if the install dir is not on PATH.
path_check() {
  case ":$PATH:" in
    *":$1:"*) : ;;
    *)
      err "$1 is not on your PATH — add it, e.g.:"
      err "  export PATH=\"$1:\$PATH\""
      ;;
  esac
}

main() {
  # `tmp` is intentionally global: the EXIT trap that cleans it up runs after
  # main returns, in global scope, where a `local` would be gone (set -u).
  local os arch version archive bindir
  os="$(normalize_os "$(uname -s)")"
  arch="$(normalize_arch "$(uname -m)")"

  version="$(resolve_version)"
  if [ -z "$version" ]; then
    err "could not determine a release version to install"
    exit 1
  fi
  archive="cove_${version}_${os}_${arch}.tar.gz"
  bindir="$(resolve_bindir)"

  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT

  info "downloading $archive ($version)"
  download_release "$version" "$archive" "$tmp"

  if ! (cd "$tmp" && verify_checksum "$archive" checksums.txt); then
    err "refusing to install — checksum verification failed"
    exit 1
  fi

  mkdir -p "$tmp/extract"
  tar -C "$tmp/extract" -xzf "$tmp/$archive"

  place "$tmp/extract/at-cove" "$bindir"
  place "$tmp/extract/at-mint" "$bindir"
  info "installed at-cove + at-mint -> $bindir"

  path_check "$bindir"
  if command -v "$bindir/at-cove" >/dev/null 2>&1; then
    info "$("$bindir/at-cove" version 2>/dev/null || echo "at-cove $version")"
  fi
}

# Run main unless sourced in lib mode (the test suite sources for unit tests).
if [ "${COVE_INSTALL_LIB:-0}" != 1 ]; then
  main "$@"
fi
