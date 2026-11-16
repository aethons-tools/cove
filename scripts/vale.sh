#!/usr/bin/env bash
# vale.sh — lint the books' pages with Vale (.vale.ini at the repo root: the
# Vale, Google and Jam styles). The pages are the AsciiDoc files under
# manual/modules/ and docs/modules/ (so not docs/superpowers/ or the
# extensions' test fixtures). Errors fail the run; warnings and suggestions are
# only reported. Uses the pinned Vale (sha256-verified, fetched once into
# .vale/, git-ignored) unless the vale on PATH is that version, as in the
# studio kit, and fetches the pinned Google package (sha256-verified) into
# styles/Google/ (git-ignored). Vale reads AsciiDoc through asciidoctor: the
# one on PATH, else the pinned Asciidoctor.js installed into .vale/.
# See docs/modules/ROOT/pages/manual-vale.adoc.
#
#   scripts/vale.sh                    # every page
#   scripts/vale.sh docs manual/x.adoc # the pages among these paths
#   scripts/vale.sh --changed REF      # the pages changed since REF (the gate)
#   scripts/vale.sh --list ...         # print the pages instead of linting
#   scripts/vale.sh --which            # print the vale binary to run (and install asciidoctor)
set -euo pipefail
vale_version=3.27.0
# vale_sha256 <platform>: the pinned Vale archive's sha256 for a platform.
vale_sha256() {
  case "$1" in
    Linux_64-bit) echo c80d62f6bce87b81843ac5b512a26e63213927b957adba0a64fc594570da2000 ;;
    Linux_arm64) echo dcaee812fd892c5750702d16ed348a1f7113be57c73cf36fe9e016c789cae6e2 ;;
    macOS_64-bit) echo 0288ab603ee6d67240ac5b97e67b0915efcfc28bb33f212a885110a2a5a6e2bc ;;
    macOS_arm64) echo 91bcd6352a832e5995a61dde73ee6367c5006669c6efa4091e0404ec8d7dd7e3 ;;
  esac
}
google_version=v0.7.2
google_sha256=c1f61ae1c4cee3f2c2ffb2912fbbdff48a02c0e47e114dfd6435b15438171d97
asciidoctor_cli=4.0.0
asciidoctor_core=3.0.4

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$root/.vale"

# sha256 <file>: its sha256 (shasum on macOS, which has no sha256sum).
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}
verified() { [ "$(sha256 "$2")" = "$1" ]; }

# fetch <url> <sha256> <out>: download to <out>, or fail if it doesn't verify.
fetch() {
  rm -f "$3.tmp"
  curl --fail --silent --show-error --location --retry 3 --output "$3.tmp" "$1"
  if ! verified "$2" "$3.tmp"; then
    echo "vale: $1 does not match the pinned sha256 $2 (got $(sha256 "$3.tmp")); not using it" >&2
    rm -f "$3.tmp"
    exit 1
  fi
  mv "$3.tmp" "$3"
}

# vale_bin: print the pinned vale, fetching it on first use.
vale_bin() {
  if command -v vale >/dev/null 2>&1 && [ "$(vale --version)" = "vale version $vale_version" ]; then
    command -v vale
    return
  fi
  local os arch platform sha bin="$dir/vale-$vale_version/vale"
  case "$(uname -s)" in Linux) os=Linux ;; Darwin) os=macOS ;; *) os="$(uname -s)" ;; esac
  case "$(uname -m)" in x86_64 | amd64) arch=64-bit ;; aarch64 | arm64) arch=arm64 ;; *) arch="$(uname -m)" ;; esac
  platform="${os}_$arch"
  sha="$(vale_sha256 "$platform")"
  if [ -z "$sha" ]; then
    echo "vale: no pinned Vale for $platform: install vale $vale_version on PATH" >&2
    exit 1
  fi
  if [ ! -x "$bin" ]; then
    mkdir -p "$dir"
    local tgz="$dir/vale_${vale_version}_$platform.tar.gz"
    fetch "https://github.com/errata-ai/vale/releases/download/v$vale_version/vale_${vale_version}_$platform.tar.gz" \
      "$sha" "$tgz"
    rm -rf "$(dirname "$bin")" && mkdir -p "$(dirname "$bin")"
    tar -xzf "$tgz" -C "$(dirname "$bin")" vale
    rm -f "$tgz"
    echo "vale: Vale $vale_version verified into .vale/" >&2
  fi
  echo "$bin"
}

# google: put the pinned Google package in styles/Google/.
google() {
  local zip="$dir/Google-$google_version.zip"
  if [ ! -f "$zip" ] || ! verified "$google_sha256" "$zip"; then
    mkdir -p "$dir"
    fetch "https://github.com/errata-ai/Google/releases/download/$google_version/Google.zip" "$google_sha256" "$zip"
  fi
  if [ "$(cat "$root/styles/Google/.version" 2>/dev/null)" != "$google_version" ]; then
    rm -rf "$root/styles/Google"
    unzip -q "$zip" -d "$root/styles"
    echo "$google_version" >"$root/styles/Google/.version"
  fi
}

# need_asciidoctor: make sure an asciidoctor is on PATH for Vale.
need_asciidoctor() {
  command -v asciidoctor >/dev/null 2>&1 && return
  local prefix="$dir/asciidoctor-$asciidoctor_cli-$asciidoctor_core"
  if [ ! -x "$prefix/node_modules/.bin/asciidoctor" ]; then
    npm install --silent --no-save --no-package-lock --prefix "$prefix" \
      "@asciidoctor/cli@$asciidoctor_cli" "@asciidoctor/core@$asciidoctor_core" >&2
  fi
  PATH="$prefix/node_modules/.bin:$PATH"
  # The CLI pins yargs 17.3.1, which a newer Node loads as an ES module and fails.
  if ! command asciidoctor --version >/dev/null 2>&1; then
    echo "vale: the pinned Asciidoctor.js CLI doesn't run on Node $(node --version); use Node 22 (the studio kit's) or put an asciidoctor on PATH" >&2
    exit 1
  fi
}

list=0 changed=""
paths=()
while [ $# -gt 0 ]; do
  case "$1" in
    --which) bin="$(vale_bin)"; need_asciidoctor; echo "$bin"; exit 0 ;;
    --list) list=1 ;;
    --changed) changed="${2:?--changed needs a git ref}"; shift ;;
    *) paths+=("$1") ;;
  esac
  shift
done

# The pages, relative to the repository's top (the one in the current
# directory, so the test can run this on a temp repo).
top="$(git rev-parse --show-toplevel)"
cd "$top"
if [ -n "$changed" ]; then
  files="$(git diff --name-only --diff-filter=d "$changed" -- "${paths[@]}")"
else
  files="$(git ls-files --cached --others --exclude-standard -- "${paths[@]}")"
fi
pages=()
while IFS= read -r f; do
  [ -f "$f" ] && pages+=("$f")
done < <(printf '%s\n' "$files" | grep -E '^(manual|docs)/modules/.*\.adoc$' | sort -u || true)

if [ "$list" = 1 ]; then
  [ "${#pages[@]}" -eq 0 ] || printf '%s\n' "${pages[@]}"
  exit 0
fi
if [ "${#pages[@]}" -eq 0 ]; then
  echo "vale: no pages to check"
  exit 0
fi
bin="$(vale_bin)"
google
need_asciidoctor
echo "vale: checking ${#pages[@]} page(s)"
"$bin" --config "$root/.vale.ini" "${pages[@]}"
