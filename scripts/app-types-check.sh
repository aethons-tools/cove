#!/usr/bin/env bash
# app-types-check.sh — fail when the Jam app's generated API types
# (web/app/src/api/types.ts, web/app/src/admin/api/types.ts) are stale (CI gate).
set -euo pipefail
cd "$(dirname "$0")/.."
files=(web/app/src/api/types.ts web/app/src/admin/api/types.ts)
go tool tygo generate
if [ -n "$(git status --porcelain -- "${files[@]}")" ]; then
  git diff -- "${files[@]}"
  echo "the app's API types are stale: run 'go generate ./internal/jam/userui/' and commit them" >&2
  exit 1
fi
