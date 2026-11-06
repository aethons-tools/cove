#!/usr/bin/env bash
# legs.test.sh — hermetic test for scripts/release/legs.sh.
# Run: bash scripts/release/legs.test.sh
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/legs.sh"
fail=0
check() { # name BASE STUDIO GO RELEASE expected(base,studio,binaries)
  local got
  got="$(BASE=$2 STUDIO=$3 GO=$4 RELEASE=$5 bash "$SCRIPT" | sed 's/^[a-z_]*=//' | paste -sd, -)"
  if [ "$got" = "$6" ]; then printf 'ok   - %s\n' "$1"; else
    fail=1
    printf 'FAIL - %s\n  expected: %s\n  actual:   %s\n' "$1" "$6" "$got"
  fi
}
check "base change => all three" true false false false true,true,true
check "studio only => studio only" false true false false false,true,false
check "go only => binaries only" false false true false false,false,true
check "none => all false" false false false false false,false,false
check "unset/empty => all false" '' '' '' '' false,false,false
# A release always cuts one: the binaries leg is the GitHub release.
check "release, none => binaries" false false false true false,false,true
check "release, studio only => studio + binaries" false true false true false,true,true
check "release, base change => all three" true false false true true,true,true
[ "$fail" -eq 0 ]
