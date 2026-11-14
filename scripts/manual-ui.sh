#!/usr/bin/env bash
# manual-ui.sh — fetch the team site's UI bundle (spring-io/antora-ui-spring,
# MPL-2.0) to manual/build/ui-spring.zip (git-ignored) and verify its sha256,
# so Antora (whose own ui.bundle.url fetch can't check a checksum) only ever
# reads a verified bundle. The team playbooks point at that path; the customer
# manual (antora-playbook.jam.yml) uses manual/ui and never needs this.
# Bumping the version: docs/modules/ROOT/pages/manual-books.adoc.
#
#   scripts/manual-ui.sh    # no-op when the verified bundle is already there
set -euo pipefail
version=v0.4.26
sha256=4f26ec21a4eeb3c1946afdb5066e0ee8a12767cdbec27d214c9bc8a7130b74d6
url="https://github.com/spring-io/antora-ui-spring/releases/download/$version/ui-bundle.zip"

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/manual/build/ui-spring.zip"

verified() { echo "$sha256  $1" | sha256sum --check --status; }

if [ -f "$out" ] && verified "$out"; then
  exit 0
fi
mkdir -p "$(dirname "$out")"
tmp="$out.tmp"
rm -f "$tmp"
curl --fail --silent --show-error --location --retry 3 --output "$tmp" "$url"
if ! verified "$tmp"; then
  echo "manual-ui: $url does not match the pinned sha256 $sha256 (got $(sha256sum "$tmp" | cut -d' ' -f1)); not using it" >&2
  rm -f "$tmp"
  exit 1
fi
mv "$tmp" "$out"
echo "manual-ui: antora-ui-spring $version verified into manual/build/ui-spring.zip"
