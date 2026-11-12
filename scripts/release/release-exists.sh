#!/usr/bin/env bash
# release-exists.sh — prints exists=true when origin/release exists, else
# exists=false (for $GITHUB_OUTPUT). Before the first-time setup creates the
# release branch (docs/modules/ROOT/pages/DEVELOPMENT.adoc), the release PR workflow has nothing to do.
set -euo pipefail
if git rev-parse --verify -q refs/remotes/origin/release >/dev/null; then
  echo exists=true
else
  echo exists=false
fi
