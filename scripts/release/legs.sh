#!/usr/bin/env bash
# legs.sh — fan the raw path filters into the DAG's legs (spec §3). Shared by
# release.yml (what to build) and release-pr.yml (what the PR says it rebuilds),
# so the two cannot drift. Reads BASE, STUDIO, GO (the paths-filter outputs,
# "true" or anything else) and RELEASE ("true" for a release cut), and prints
# base=…, studio_image=…, binaries=… lines for $GITHUB_OUTPUT.
#   base change     => base AND studio_image (downstream) AND binaries
#                      (new digest => new DefaultRef + blessed-list head).
#   jam-studio only => studio_image only (FROM the current published base).
#   go change       => binaries only (blessed list recomputed from the head).
#   a release       => binaries always: every approved release cuts a GitHub
#                      release, and jam update / install.sh need its archives.
set -euo pipefail

base=false studio=false binaries=false
if [ "${BASE:-}" = 'true' ]; then base=true; fi
if [ "${BASE:-}" = 'true' ] || [ "${STUDIO:-}" = 'true' ]; then studio=true; fi
if [ "${BASE:-}" = 'true' ] || [ "${GO:-}" = 'true' ] || [ "${RELEASE:-}" = 'true' ]; then binaries=true; fi
echo "base=$base"
echo "studio_image=$studio"
echo "binaries=$binaries"
