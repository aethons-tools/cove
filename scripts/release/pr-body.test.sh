#!/usr/bin/env bash
# pr-body.test.sh — hermetic tests for scripts/release/pr-body.sh and
# release-exists.sh (the release PR's scripts), in a temp git repo.
# Run: bash scripts/release/pr-body.test.sh
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/pr-body.sh"
EXISTS="$HERE/release-exists.sh"
SITE=https://aethons-tools-jam.pages.dev

pass=0
fail=0
ok() { pass=$((pass + 1)); printf 'ok   - %s\n' "$1"; }
bad() {
  fail=$((fail + 1))
  printf 'FAIL - %s\n' "$1"
  shift || true
  for line in "$@"; do printf '  %s\n' "$line"; done
}
assert_eq() {
  if [ "$2" = "$3" ]; then ok "$1"; else bad "$1" "expected: $2" "actual:   $3"; fi
}
assert_contains() { # name haystack needle
  case "$2" in *"$3"*) ok "$1" ;; *) bad "$1" "missing: $3" "in: $2" ;; esac
}
assert_lacks() { # name haystack needle
  case "$2" in *"$3"*) bad "$1" "unexpected: $3" "in: $2" ;; *) ok "$1" ;; esac
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"
git init -q -b main .
git config user.email t@example.com
git config user.name t
mkdir -p manual/modules/projects/pages manual/modules/ROOT/pages internal
echo a >manual/modules/projects/pages/agent-context.adoc
echo a >manual/modules/ROOT/pages/old.adoc
echo a >internal/foo.go
git add -A && git commit -q -m "init"
# Before the release branch exists, the release PR workflow does nothing.
assert_eq "no origin/release => exists=false" "exists=false" "$(bash "$EXISTS")"
# The remote-tracking refs the workflow uses.
git update-ref refs/remotes/origin/release HEAD
assert_eq "origin/release => exists=true" "exists=true" "$(bash "$EXISTS")"
LEGS='{"base":"false","studio_image":"false","binaries":"true"}'

# main == release: nothing to release.
out="$(bash "$SCRIPT" origin/release HEAD "$LEGS")"
assert_eq "no commits => empty output" "" "$out"

# Go-only change: no manual edits.
echo b >>internal/foo.go
git commit -q -am "fix: go only (#11)"
out="$(bash "$SCRIPT" origin/release HEAD "$LEGS")"
assert_contains "no manual changes => users section says so" "$out" "No user-visible changes."
assert_contains "merged section lists PR" "$out" "#11"

# Manual edit, new ROOT page, deleted ROOT page.
echo b >>manual/modules/projects/pages/agent-context.adoc
echo n >manual/modules/ROOT/pages/new.adoc
git rm -q manual/modules/ROOT/pages/old.adoc
git add -A && git commit -q -m "feat: x (#12)"
# A nested page path, and a merge commit on main (--first-parent keeps the PR line).
mkdir -p manual/modules/projects/pages/sub
echo n >manual/modules/projects/pages/sub/p.adoc
git add -A && git commit -q -m "docs: nested page (#13)"
git checkout -q -b side
echo s >side.txt && git add -A && git commit -q -m "side-branch commit"
git checkout -q main
git merge -q --no-ff side -m "Merge side (#14)"
out="$(bash "$SCRIPT" origin/release HEAD "$LEGS")"
assert_contains "nested page link" "$out" "$SITE/jam/next/projects/sub/p.html"
assert_contains "merge commit listed" "$out" "Merge side (#14)"
assert_lacks "merged side commit hidden by first-parent" "$out" "side-branch commit"
assert_contains "users heading" "$out" "## What changes for users"
assert_contains "module page link" "$out" "$SITE/jam/next/projects/agent-context.html"
assert_contains "ROOT page link" "$out" "$SITE/jam/next/new.html"
assert_contains "deleted page named" "$out" "- removed: ROOT/old"
assert_lacks "deleted page not linked" "$out" "$SITE/jam/next/old.html"
assert_lacks "users section not empty" "$out" "No user-visible changes."
assert_contains "rebuilds heading + binaries" "$(printf '%s\n' "$out" | sed -n '/## What it rebuilds/,/## Merged/p')" "binaries"
assert_lacks "unselected leg absent" "$(printf '%s\n' "$out" | sed -n '/## What it rebuilds/,/## Merged/p')" "studio_image"
assert_contains "merged heading" "$out" "## Merged"
assert_contains "merged lists #12" "$out" "#12"

out="$(bash "$SCRIPT" origin/release HEAD '{"base":"false","studio_image":"false","binaries":"false"}')"
assert_contains "no legs => nothing rebuilt" "$out" "Nothing is rebuilt"

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[ "$fail" -eq 0 ]
