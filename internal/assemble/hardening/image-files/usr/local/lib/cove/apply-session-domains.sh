#!/usr/bin/env bash
# Apply a session's per-class egress delta to the running squid (COV-39 §5).
#
# Reads the resolved domains (one host per line) from STDIN, overwrites the
# per-session allow-list /etc/squid/allowed_domains.session.txt (header preserved,
# one host per line), then runs `squid -k reconfigure` so squid re-reads its ACL
# files without dropping the process. On a bad reconfigure it fails loudly
# (non-zero exit) rather than leaving squid in a half-applied state.
#
# SEALED + ROOT-ONLY: delivered by the hardening layer and invoked solely by the
# host via `docker exec` as root (S3+). The session file is root-owned and
# `squid -k reconfigure` is privileged, so the non-root `agent` workload cannot
# run this to widen its own egress. Lists stay allow-only/additive: this can only
# widen within what the sealed base + nftables lock permit, never bypass them.
#
# Empty stdin (a no-class session) writes a header-only file, reverting egress to
# the baked (sealed + kit) lists.
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
	echo "apply-session-domains: must run as root (delivered via host docker exec)" >&2
	exit 1
fi

session_file="${COVE_SESSION_DOMAINS_FILE:-/etc/squid/allowed_domains.session.txt}"

# Rewrite atomically: build the new list beside the target, then move it into
# place, so a concurrent squid read never sees a half-written file.
tmp="$(mktemp "${session_file}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT

{
	printf '# Per-session egress domains applied at session start (COV-39).\n'
	printf '# Additive to the sealed base + kit lists; leading dot = subdomains.\n'
	# One host per line, dropping blank lines so the file stays clean.
	while IFS= read -r line || [ -n "$line" ]; do
		[ -n "$line" ] && printf '%s\n' "$line"
	done
} >"$tmp"

mv "$tmp" "$session_file"
trap - EXIT

# Re-read the ACL files in place. Fail loudly if squid rejects the new config.
if ! squid -k reconfigure; then
	echo "apply-session-domains: squid -k reconfigure failed" >&2
	exit 1
fi
