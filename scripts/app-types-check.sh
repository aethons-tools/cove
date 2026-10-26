#!/usr/bin/env bash
# app-types-check.sh — fail when web/app/src/api/types.ts is stale (CI gate).
set -euo pipefail
cd "$(dirname "$0")/.."
go generate ./internal/jam/userui/
if ! git diff --exit-code -- web/app/src/api/types.ts; then
  echo "web/app/src/api/types.ts is stale: run 'go generate ./internal/jam/userui/' and commit it" >&2
  exit 1
fi
