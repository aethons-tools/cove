#!/usr/bin/env bash
# Fold kit-declared env (image.env) and PATH additions (image.paths) into
# /etc/environment so pam_env exposes them to every SSH session — interactive or
# not, login or not. env vars are appended as their own KEY=VALUE lines; path
# entries are appended to the SINGLE existing PATH= line (never a second PATH=
# line, since pam_env is last-wins). The base PATH/env written by the Dockerfile
# is preserved — this is strictly additive.
set -euo pipefail

cove_dir="${COVE_DIR:-/.cove}"
env_file="${COVE_ENV_FILE:-/etc/environment}"

if [ -s "$cove_dir/env" ]; then
	while IFS= read -r line; do
		[ -n "$line" ] || continue
		printf '%s\n' "$line" >> "$env_file"
	done < "$cove_dir/env"
fi

if [ -s "$cove_dir/paths" ]; then
	add=""
	while IFS= read -r p; do
		[ -n "$p" ] || continue
		add="${add}:${p}"
	done < "$cove_dir/paths"
	if grep -q '^PATH=' "$env_file"; then
		sed -i "\\#^PATH=#s#\$#${add}#" "$env_file"
	else
		printf 'PATH=%s\n' "${add#:}" >> "$env_file"
	fi
fi
