#!/usr/bin/env bash
# seed-agent-data.sh SEED DEST — seed the persistent state volume from the image.
#
# Sealed (hardening) mechanism, image-provided content: the entrypoint runs this
# as root on every boot with SEED=/home/agent/.init-agent-data and
# DEST=/agent-data (CLAUDE_CONFIG_DIR). It knows no file names — what to seed,
# and what to refresh, is whatever the image (the kit base, cove-base-image by
# default) put under SEED.
#
#   First boot   — copy all of SEED into DEST once, guarded by DEST/.seeded, so a
#                  restart or a recreate against an existing volume never
#                  clobbers saved state (e.g. the OAuth login in
#                  .credentials.json). A missing SEED seeds nothing.
#   Every boot   — re-copy the entries SEED/.refresh lists, image authoritative
#                  (an entry is removed first, so a directory's deleted or
#                  renamed files don't linger and a symlink planted in DEST is
#                  replaced, never written through). No manifest = refresh
#                  nothing.
#
# .refresh format: one top-level entry name per line; blank lines and #-comments
# are ignored. An entry must be a single path segment of [A-Za-z0-9._-] other
# than . or .. — anything else (absolute, nested, traversal, globs) is skipped
# with a warning, so the manifest can never steer this root-run copy outside
# DEST. Paths are passed as arguments, never read from the environment.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: seed-agent-data.sh SEED DEST" >&2
  exit 2
fi
seed=$1
dest=$2

mkdir -p "$dest"
if [ ! -e "$dest/.seeded" ]; then
  if [ -d "$seed" ]; then
    cp -a "$seed/." "$dest/"
  fi
  touch "$dest/.seeded"
fi

manifest="$seed/.refresh"
[ -f "$manifest" ] || exit 0

while IFS= read -r line || [ -n "$line" ]; do
  entry="${line%%#*}"
  entry="${entry#"${entry%%[![:space:]]*}"}"
  entry="${entry%"${entry##*[![:space:]]}"}"
  [ -n "$entry" ] || continue
  case "$entry" in
    . | .. | *[!A-Za-z0-9._-]*)
      echo "seed-agent-data: skipping invalid .refresh entry: $entry" >&2
      continue
      ;;
  esac
  [ -e "$seed/$entry" ] || [ -L "$seed/$entry" ] || continue
  rm -rf "${dest:?}/$entry"
  cp -a "$seed/$entry" "$dest/$entry"
done <"$manifest"
