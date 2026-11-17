#!/usr/bin/env bash
# vale.test.sh — test for the books' Vale setup: the Jam style's rules
# (styles/Jam, on the pages in testdata/vale/), the pages scripts/vale.sh
# picks and the alert levels that fail it (a temp git repo). It installs the pinned vale and asciidoctor first
# (scripts/vale.sh --which), so it runs on a fresh clone.
# Run: bash scripts/vale.test.sh
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Install the pinned Vale and asciidoctor first (a fresh clone has neither),
# then put that asciidoctor on PATH: vale.sh's own PATH stays in its process.
"$root/scripts/vale.sh" --which >/dev/null
PATH="$PATH:$(echo "$root"/.vale/asciidoctor-*/node_modules/.bin | tr ' ' :)"
fail=0
expect() { # name expected actual
  if [ "$2" = "$3" ]; then printf 'ok   - %s\n' "$1"; else
    fail=1
    printf 'FAIL - %s\n  expected: %s\n  actual:   %s\n' "$1" "$2" "$3"
  fi
}

# --- the Jam rules, alone, on the fixtures: one line per alert ---
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
cat >"$tmp/.vale.ini" <<INI
StylesPath = $root/styles
MinAlertLevel = suggestion
[*.adoc]
BasedOnStyles = Jam
INI
vale="$("$root/scripts/vale.sh" --which)"
alerts() { # page
  { "$vale" --config "$tmp/.vale.ini" --output line "$root/testdata/vale/$1" 2>&1 || true; } |
    sed -E 's/^[^:]*:([0-9]+):[0-9]+:([A-Za-z.]+):.*/\1 \2/' | sort -n | paste -sd, -
}
expect "normative page: vague words are errors, items without a keyword are suggestions" \
  "7 Jam.Normative,7 Jam.NormativeKeyword,10 Jam.NormativeKeyword,12 Jam.Normative,12 Jam.NormativeKeyword,15 Jam.Normative" \
  "$(alerts normative.adoc)"
expect "a page without :page-normative: is not checked" "" "$(alerts plain.adoc)"

# --- the pages scripts/vale.sh lints ---
repo="$tmp/repo"
mkdir -p "$repo"/{manual/modules/ROOT/pages,manual/extensions/test,docs/modules/ROOT/pages,docs/superpowers,web}
git -C "$repo" init -q
for f in manual/modules/ROOT/pages/a.adoc manual/extensions/test/fixture.adoc \
  docs/modules/ROOT/pages/b.adoc docs/superpowers/c.adoc docs/modules/ROOT/pages/d.md web/e.adoc; do
  echo "= Page" >"$repo/$f"
done
git -C "$repo" add -A
git -C "$repo" -c user.name=t -c user.email=t@t commit -qm one
expect "all pages: manual/ and docs/ AsciiDoc, not docs/superpowers/ or test fixtures" \
  "docs/modules/ROOT/pages/b.adoc,manual/modules/ROOT/pages/a.adoc" \
  "$(cd "$repo" && "$root/scripts/vale.sh" --list | paste -sd, -)"
echo "== more" >>"$repo/docs/modules/ROOT/pages/b.adoc"
echo "== more" >>"$repo/docs/superpowers/c.adoc"
echo "= New" >"$repo/manual/modules/ROOT/pages/new.adoc"
git -C "$repo" rm -q manual/modules/ROOT/pages/a.adoc
git -C "$repo" add -A
git -C "$repo" -c user.name=t -c user.email=t@t commit -qm two
expect "changed pages: added and modified, not deleted" \
  "docs/modules/ROOT/pages/b.adoc,manual/modules/ROOT/pages/new.adoc" \
  "$(cd "$repo" && "$root/scripts/vale.sh" --list --changed HEAD^1 | paste -sd, -)"
expect "given paths: only the pages among them" \
  "docs/modules/ROOT/pages/b.adoc" \
  "$(cd "$repo" && "$root/scripts/vale.sh" --list docs web/e.adoc | paste -sd, -)"

# --- what fails the run: errors and warnings, not suggestions ---
page="$repo/docs/modules/ROOT/pages"
printf '= Page\n\nWe check this.\n' >"$page/warning.adoc"     # Google.We, a warning
printf '= Page\n\nIt is checked.\n' >"$page/suggestion.adoc" # Google.Passive and more, suggestions
printf '= Page\n\nIt checks this!\n' >"$page/error.adoc"     # Google.Exclamation, an error
status() { # page
  (cd "$repo" && "$root/scripts/vale.sh" "docs/modules/ROOT/pages/$1" >/dev/null 2>&1) && echo pass || echo fail
}
expect "a warning fails the run" fail "$(status warning.adoc)"
expect "an error fails the run" fail "$(status error.adoc)"
expect "a suggestion only is reported" pass "$(status suggestion.adoc)"
[ "$fail" -eq 0 ]
