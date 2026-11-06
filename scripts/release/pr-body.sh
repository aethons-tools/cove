#!/usr/bin/env bash
# pr-body.sh <base> <head> <legs-json> — the release PR description (markdown).
# Prints nothing when <head> has no commits beyond <base>. Sections: what changes
# for users (the manual-page diff, linked to the Next manual), what it rebuilds
# (the legs that are "true" in <legs-json>), and the merged PRs.
set -euo pipefail

[ $# -eq 3 ] || { echo "usage: pr-body.sh <base> <head> <legs-json>" >&2; exit 2; }
base=$1 head=$2 legs=$3
SITE=${MANUAL_SITE:-https://aethons-tools-jam.pages.dev}

[ "$(git rev-list --count "$base..$head")" -gt 0 ] || exit 0

echo "## What changes for users"
echo
users=0
while IFS=$'\t' read -r status path; do
  [ -n "$path" ] || continue
  case "$path" in
    manual/modules/*/pages/*.adoc) ;;
    *) continue ;;
  esac
  rest=${path#manual/modules/}
  module=${rest%%/*}
  page=${rest##*/pages/}
  page=${page%.adoc}
  if [ "$module" = ROOT ]; then url="$SITE/jam/next/$page.html"; else url="$SITE/jam/next/$module/$page.html"; fi
  name="$module/$page"
  users=1
  case "$status" in
    D) echo "- removed: $name" ;;
    A) echo "- new: [$name]($url)" ;;
    *) echo "- changed: [$name]($url)" ;;
  esac
done < <(git diff --name-status --no-renames "$base" "$head" -- manual)
if [ "$users" -eq 0 ]; then echo "No user-visible changes."; fi

echo
echo "## What it rebuilds"
echo
rebuilt=0
for leg in base studio_image binaries; do
  if printf '%s' "$legs" | grep -Eq "\"$leg\"[[:space:]]*:[[:space:]]*\"true\""; then
    echo "- $leg"
    rebuilt=1
  fi
done
if [ "$rebuilt" -eq 0 ]; then echo "Nothing is rebuilt."; fi

echo
echo "## Merged"
echo
git log --first-parent --format='- %s' "$base..$head"
