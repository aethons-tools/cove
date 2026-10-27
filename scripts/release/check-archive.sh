#!/usr/bin/env bash
# check-archive.sh — assert every release archive goreleaser built
# (dist/jam_*_<os>_<arch>.tar.gz) holds exactly `jam`, mode 0755.
# Run after `goreleaser release --snapshot` (release.yml, PR and main).
# Usage: scripts/release/check-archive.sh [distdir]   (default: dist)
set -euo pipefail

dist="${1:-dist}"
shopt -s nullglob
archives=("$dist"/jam_*.tar.gz)
if [ "${#archives[@]}" -eq 0 ]; then
  echo "check-archive: no $dist/jam_*.tar.gz (run goreleaser release --snapshot first)" >&2
  exit 1
fi

fail=0
for a in "${archives[@]}"; do
  names="$(tar -tzf "$a" | sort | tr '\n' ' ')"
  if [ "$names" != "jam " ]; then
    echo "check-archive: $a holds [${names% }], want [jam]" >&2
    fail=1
    continue
  fi
  mode="$(tar -tzvf "$a" jam | awk '{print $1}')"
  if [ "$mode" != "-rwxr-xr-x" ]; then
    echo "check-archive: $a jam mode $mode, want -rwxr-xr-x" >&2
    fail=1
    continue
  fi
  echo "check-archive: $a ok (jam)"
done
exit "$fail"
