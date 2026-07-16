#!/usr/bin/env bash
# lint.sh — go vet + gofmt check + shell/Dockerfile lint.
#
# Extracted from the justfile so CI runs exactly these checks without needing
# `just` installed (see the justfile header); `just lint` delegates here. Keep
# the logic in this script, not in the recipe or the workflow, so the local loop
# and CI can never drift apart.
#
# The optional linters (shellcheck, hadolint) are not required: a fresh clone
# can lint without the full tool setup (scripts/setup-test-tools.sh installs
# them), so a missing one is skipped with a note. That default is a kindness
# locally and a trap in CI — a gate whose linters are all absent passes without
# linting anything. Set STRICT=1 to make a missing linter an error instead;
# CI does.
#
# (A comment whose first word is "shellcheck" parses as a shellcheck directive,
# not prose — hence the wording above.)
#
# Usage:
#   scripts/lint.sh            # skip optional linters that aren't installed
#   STRICT=1 scripts/lint.sh   # a missing optional linter is an error (CI)
set -euo pipefail

STRICT="${STRICT:-0}"

# have <tool> — true if the tool should run. Absent tools are skipped, or fail
# the run under STRICT=1.
have() {
  local tool="$1"
  if command -v "$tool" >/dev/null 2>&1; then
    return 0
  fi
  if [ "$STRICT" = "1" ]; then
    echo "lint: $tool is not installed, and STRICT=1 requires it" >&2
    echo "lint: install it with scripts/setup-test-tools.sh" >&2
    exit 1
  fi
  echo "lint: $tool not installed (skipping)"
  return 1
}

go vet ./...

# gofmt walks the filesystem it is given, so `gofmt -l .` also descends into
# dot-directories — including .gopath/, the local GOPATH this repo's sandbox
# sets (see docs/DEVELOPMENT.md), whose vendored third-party sources are not
# ours to format. Go's own package patterns skip dot-directories, so ask the
# toolchain for the module's package dirs and check exactly those.
pkg_dirs=()
while IFS= read -r dir; do
  pkg_dirs+=("$dir")
done < <(go list -f '{{.Dir}}' ./...)

unformatted="$(gofmt -l "${pkg_dirs[@]}")"
if [ -n "$unformatted" ]; then
  echo "gofmt -w needed for:" >&2
  echo "$unformatted" >&2
  exit 1
fi

if have shellcheck; then
  shellcheck scripts/*.sh internal/assemble/hardening/image-files/usr/local/bin/*.sh
fi

if have hadolint; then
  hadolint images/cove-base-image/Dockerfile images/cove-image/Dockerfile internal/assemble/hardening/Dockerfile
fi
