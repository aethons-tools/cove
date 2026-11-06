#!/usr/bin/env bash
# legs.test.sh — hermetic test for scripts/release/legs.sh.
# Run: bash scripts/release/legs.test.sh
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/legs.sh"
fail=0
check() { # name BASE STUDIO GO expected(base,studio,binaries)
  local got
  got="$(BASE=$2 STUDIO=$3 GO=$4 bash "$SCRIPT" | sed 's/^[a-z_]*=//' | paste -sd, -)"
  if [ "$got" = "$5" ]; then printf 'ok   - %s\n' "$1"; else
    fail=1
    printf 'FAIL - %s\n  expected: %s\n  actual:   %s\n' "$1" "$5" "$got"
  fi
}
check "base change => all three" true false false true,true,true
check "studio only => studio only" false true false false,true,false
check "go only => binaries only" false false true false,false,true
check "none => all false" false false false false,false,false
check "unset/empty => all false" '' '' '' false,false,false
[ "$fail" -eq 0 ]
