#!/usr/bin/env bash
# cove git credential helper.
#
# Supplies a GitHub token for HTTPS git from the environment, memory-only — the
# token is never written to disk (no `store`). Declare it as a secret named
# GITHUB_TOKEN in the kit's config.yml; cove injects it into the connect session
# env, and git (run inside that session) invokes this helper for github.com.
#
# Only answers `get`; `store`/`erase` are no-ops, so nothing is persisted. If
# GITHUB_TOKEN is unset, it emits nothing and git fails closed (no prompt under
# the non-interactive sandbox) rather than using stale credentials.
set -euo pipefail

[ "${1:-}" = "get" ] || exit 0
[ -n "${GITHUB_TOKEN:-}" ] || exit 0

printf 'username=x-access-token\npassword=%s\n' "$GITHUB_TOKEN"
