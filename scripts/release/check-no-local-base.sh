#!/usr/bin/env bash
# check-no-local-base.sh — refuse to cut a release from a tree holding a
# `just local` studio base (internal/basedigest/blessed/local.txt, git-ignored):
# jam would embed it as its default, trusted base. The goreleaser before-hook
# runs this first. CI's checkout never has one; this catches a local release.
set -euo pipefail
cd "$(dirname "$0")/../.."
f=internal/basedigest/blessed/local.txt
if [ -e "$f" ]; then
  echo "release: $f exists (a \`just local\` build); remove it (just local-clean) before a release build" >&2
  exit 1
fi
