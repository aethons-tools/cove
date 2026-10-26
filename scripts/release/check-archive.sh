#!/usr/bin/env bash
# check-archive.sh — assert every release archive goreleaser built
# (dist/cove_*_<os>_<arch>.tar.gz) holds exactly at-cove (the tombstone, mode
# 0755, byte-identical to scripts/release/at-cove), at-mint and at-jam. An older
# `at-jam update` places at-cove unconditionally, so an archive without the
# tombstone breaks every pre-slice-2 updater (install.test.sh's compat case).
# Run after `goreleaser release --snapshot` (release.yml, PR and main).
# Usage: scripts/release/check-archive.sh [distdir]   (default: dist)
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
dist="${1:-dist}"
tombstone="$root/scripts/release/at-cove"

shopt -s nullglob
archives=("$dist"/cove_*.tar.gz)
if [ "${#archives[@]}" -eq 0 ]; then
  echo "check-archive: no $dist/cove_*.tar.gz (run goreleaser release --snapshot first)" >&2
  exit 1
fi

fail=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
for a in "${archives[@]}"; do
  bad=0
  names="$(tar -tzf "$a" | sort | tr '\n' ' ')"
  if [ "$names" != "at-cove at-jam at-mint " ]; then
    echo "check-archive: $a holds [${names% }], want [at-cove at-jam at-mint]" >&2
    fail=1
    continue
  fi
  mode="$(tar -tzvf "$a" at-cove | awk '{print $1}')"
  if [ "$mode" != "-rwxr-xr-x" ]; then
    echo "check-archive: $a at-cove mode $mode, want -rwxr-xr-x" >&2
    bad=1
  fi
  rm -rf "${tmp:?}"/*
  tar -C "$tmp" -xzf "$a" at-cove
  if ! cmp -s "$tmp/at-cove" "$tombstone"; then
    echo "check-archive: $a at-cove is not the scripts/release/at-cove tombstone" >&2
    bad=1
  fi
  if [ "$bad" = 0 ]; then
    echo "check-archive: $a ok (at-cove tombstone + at-mint + at-jam)"
  else
    fail=1
  fi
done
exit "$fail"
