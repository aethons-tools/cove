#!/usr/bin/env bash
# Fold the in-image session-env drop-in directory into /etc/environment so
# pam_env exposes it to every SSH session — interactive or not, login or not
# (COV-34). Fragments are concatenated in lexical order: the base ships
# /etc/cove/env.d/00-base.env (PATH, CLAUDE_CONFIG_DIR, the egress proxy vars) and
# a kit's Dockerfile may drop higher-numbered fragments. Blank lines and #-comments
# are ignored. pam_env is last-wins, so a later fragment can shadow an earlier key
# (the base keys sit in 00- for exactly this ordering). Egress stays enforced by
# nftables regardless of the proxy vars here.
set -euo pipefail

env_file="${COVE_ENV_FILE:-/etc/environment}"
dir="${COVE_ENV_D:-/etc/cove/env.d}"

[ -d "$dir" ] || exit 0

for f in "$dir"/*.env; do
	[ -e "$f" ] || continue # no matches → the glob stays literal; skip it
	while IFS= read -r line || [ -n "$line" ]; do
		case "$line" in
		'' | \#*) continue ;;
		esac
		printf '%s\n' "$line" >>"$env_file"
	done <"$f"
done
