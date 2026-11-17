#!/usr/bin/env bash
# manual-browser.sh — print the headless Chrome the books' diagrams render with
# (manual/extensions/diagrams.js runs mmdc with $JAM_BROWSER): $JAM_BROWSER
# when set, else the kit's /opt/chrome-headless-shell, else a pinned
# chrome-headless-shell (Chrome for Testing, linux64 only; sha256-verified)
# fetched once into manual/build/chrome/ (git-ignored). Every build that
# renders the books runs it: scripts/manual.sh, the justfile's manual
# recipes, the gate and the team site workflow. See docs/modules/ROOT/pages/manual-diagrams.adoc.
#
#   export JAM_BROWSER="$(scripts/manual-browser.sh)"
set -euo pipefail
# The build puppeteer (manual/package.json) pins; bump the two together.
version=157.0.8103.50
sha256=7bf0d41e424efd1d6281c4162993ab9dc5de92fac213c211614905a71c057d0f
url="https://storage.googleapis.com/chrome-for-testing-public/$version/linux64/chrome-headless-shell-linux64.zip"
kit=/opt/chrome-headless-shell/chrome-headless-shell

if [ -n "${JAM_BROWSER:-}" ]; then
  echo "$JAM_BROWSER"
  exit 0
fi
if [ -x "$kit" ]; then
  echo "$kit"
  exit 0
fi
if [ "$(uname -sm)" != "Linux x86_64" ]; then
  echo "manual-browser: no headless Chrome for the books' diagrams: set JAM_BROWSER to a Chrome (the pinned download is linux64 only)" >&2
  exit 1
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$root/manual/build/chrome/$version"
bin="$dir/chrome-headless-shell-linux64/chrome-headless-shell"
if [ ! -x "$bin" ]; then
  zip="$dir.zip"
  rm -rf "$dir" "$zip"
  mkdir -p "$(dirname "$dir")"
  curl --fail --silent --show-error --location --retry 3 --output "$zip" "$url"
  if ! echo "$sha256  $zip" | sha256sum --check --status; then
    echo "manual-browser: $url does not match the pinned sha256 $sha256 (got $(sha256sum "$zip" | cut -d' ' -f1)); not using it" >&2
    rm -f "$zip"
    exit 1
  fi
  # Unzipped beside the binary's final place and moved in only whole, so an
  # interrupted unzip is never taken for an install.
  rm -rf "$dir.tmp"
  unzip -q "$zip" -d "$dir.tmp"
  rm -f "$zip"
  mv "$dir.tmp" "$dir"
  echo "manual-browser: chrome-headless-shell $version verified into manual/build/chrome/" >&2
fi
echo "$bin"
