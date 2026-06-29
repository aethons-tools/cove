#!/usr/bin/env bash
# Run kit setup-scripts (config.yml image.setup-script) in order, as root, each
# in its own directory so a script can reference sibling files. The manifest
# lists in-image absolute script paths, one per line, written by cove's assemble
# step. Build-time network is open, so scripts may curl/apt-get.
set -euo pipefail

manifest="${COVE_SETUP_MANIFEST:-/.cove/setup-manifest}"
[ -f "$manifest" ] || exit 0

while IFS= read -r script; do
	[ -n "$script" ] || continue
	( cd "$(dirname "$script")" && bash "$script" )
done < "$manifest"
