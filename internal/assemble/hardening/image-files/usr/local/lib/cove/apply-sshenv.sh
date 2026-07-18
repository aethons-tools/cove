#!/usr/bin/env bash
# Populate /etc/environment (read by pam_env for every SSH agent session — login
# or not, interactive or not) from the image's Docker ENV, so SSH sessions see the
# same environment that a bare `docker run` / CI container already gets from ENV.
# This removes the old env.d footgun: an image no longer maintains a session-env
# fragment separately from its ENV statements and risk them drifting — one ENV
# feeds both.
#
# What transfers:
#   - PATH is ALWAYS included (intrinsic), with /home/agent/.local/bin (where
#     `claude` installs) prepended to whatever the image's ENV PATH resolved to.
#   - Every variable NAMED in COVE_SSHENV (colon-separated), using its live value.
#
# What does NOT come from the image: the egress proxy vars and CLAUDE_CONFIG_DIR.
# The sealed hardening layer writes those itself, LAST (so they always win), and
# they are deliberately not settable via ENV — as ENV they would poison the image
# build (a proxy that isn't running yet breaks apt/curl; CLAUDE_CONFIG_DIR would
# misdirect the build-time `claude install`). Egress stays enforced by nftables
# regardless of these vars.
set -euo pipefail

env_file="${COVE_ENV_FILE:-/etc/environment}"
: >"$env_file" # the sealed layer owns this file — start clean and deterministic

# 1) Image-provided session env: PATH (intrinsic) + the COVE_SSHENV names.
printf 'PATH=%s\n' "/home/agent/.local/bin:${PATH}" >>"$env_file"
IFS=':' read -r -a names <<<"${COVE_SSHENV:-}"
for n in "${names[@]}"; do
	case "$n" in '' | PATH) continue ;; esac # empty entry / PATH already emitted
	[ -n "${!n+isset}" ] && printf '%s=%s\n' "$n" "${!n}" >>"$env_file"
done

# 2) Sealed runtime env — hardening-owned, written last so it wins on any key.
printf '%s\n' \
	'CLAUDE_CONFIG_DIR=/agent-data' \
	'http_proxy=http://127.0.0.1:3128' \
	'https_proxy=http://127.0.0.1:3128' \
	'HTTP_PROXY=http://127.0.0.1:3128' \
	'HTTPS_PROXY=http://127.0.0.1:3128' \
	'no_proxy=localhost,127.0.0.1,::1,.internal' \
	'NO_PROXY=localhost,127.0.0.1,::1,.internal' \
	>>"$env_file"
