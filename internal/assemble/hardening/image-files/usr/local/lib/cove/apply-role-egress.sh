#!/usr/bin/env bash
# Apply a Jam role's egress policy to the running squid (at raise, and on
# a running cove when the role's policy changes).
#
# Reads the role's domains (one host per line) from STDIN. Each must be a valid
# hostname and must be covered by the kit's baked egress ceiling
# (/etc/squid/egress_ceiling.txt, the kit's image.allowed-domains). Everything is
# validated BEFORE anything is written: on a bad line it exits 2, on a domain
# outside the ceiling it exits 3, and in both cases nothing changes.
#
# On success it overwrites the active policy list /etc/squid/allowed_domains.kit.txt
# with the role's list, clears the per-session delta allowed_domains.session.txt
# to its header, and runs `squid -k reconfigure`. On a bad reconfigure it fails
# loudly (exit 1).
#
# SEALED + ROOT-ONLY: delivered by the hardening layer and invoked solely by the
# host via `docker exec -u root` (Jam's launcher: at raise before the agent
# starts, and again on a running cove when its role's policy changes). The
# lists and the ceiling are root-owned and `squid -k reconfigure` is privileged,
# so the non-root `agent` workload cannot run this to widen its own egress. The
# ceiling bounds it: a role can narrow or re-pick within the kit's list, never
# exceed it. The sealed base and infra lists are untouched and always on.
#
# Empty stdin is a set-but-empty policy: nothing beyond the sealed base + infra.
#
# `--kit-default` (the only accepted argument) instead restores the kit default:
# the active list becomes the ceiling's own entries (the ceiling IS the kit's
# image.allowed-domains), the session delta is cleared, and squid reconfigures.
# It takes no domains: any stdin is refused (exit 2). Any other argument exits 2.
# Jam uses it when a role's policy is cleared on a running cove.
set -euo pipefail
export LC_ALL=C

if [ "$(id -u)" -ne 0 ]; then
	echo "apply-role-egress: must run as root (delivered via host docker exec)" >&2
	exit 1
fi

kit_file="${COVE_KIT_DOMAINS_FILE:-/etc/squid/allowed_domains.kit.txt}"
session_file="${COVE_SESSION_DOMAINS_FILE:-/etc/squid/allowed_domains.session.txt}"
ceiling_file="${COVE_EGRESS_CEILING_FILE:-/etc/squid/egress_ceiling.txt}"

# At least two labels; no scheme, port, path, glob or whitespace; an optional
# leading dot means "and subdomains". Jam enforces the same rule.
domain_re='^\.?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$'

# 0. Mode: a role list on stdin (no arguments), or --kit-default (no stdin).
kit_default=0
if [ "$#" -gt 1 ]; then
	echo "apply-role-egress: too many arguments" >&2
	exit 2
fi
if [ "$#" -eq 1 ]; then
	if [ "$1" != "--kit-default" ]; then
		echo "apply-role-egress: unknown argument: $1" >&2
		exit 2
	fi
	kit_default=1
fi

# 1. Read and validate the requested list (--kit-default: stdin must be empty).
requested=()
if [ "$kit_default" -eq 1 ]; then
	if [ -n "$(cat)" ]; then
		echo "apply-role-egress: --kit-default takes no domains on stdin" >&2
		exit 2
	fi
fi
while [ "$kit_default" -eq 0 ] && { IFS= read -r line || [ -n "$line" ]; }; do
	[ -z "$line" ] && continue
	d="${line,,}"
	if ! [[ "$d" =~ $domain_re ]]; then
		echo "apply-role-egress: invalid domain: $line" >&2
		exit 2
	fi
	requested+=("$d")
done

# 2. Load the ceiling (fail closed if it is missing) and check every domain.
if [ ! -r "$ceiling_file" ]; then
	echo "apply-role-egress: egress ceiling $ceiling_file is missing" >&2
	exit 1
fi
ceiling=()
while IFS= read -r line || [ -n "$line" ]; do
	line="${line#"${line%%[![:space:]]*}"}"
	line="${line%"${line##*[![:space:]]}"}"
	case "$line" in '' | '#'*) continue ;; esac
	ceiling+=("${line,,}")
done <"$ceiling_file"

# --kit-default: the ceiling's entries are the list (trivially within it).
if [ "$kit_default" -eq 1 ]; then
	requested=("${ceiling[@]+"${ceiling[@]}"}")
fi

# covered R: true when some ceiling entry C covers R — R == C, or C is a
# leading-dot wildcard and R is its apex or ends with it. An exact entry never
# covers a wildcard.
covered() {
	local r="$1" c
	for c in "${ceiling[@]+"${ceiling[@]}"}"; do
		if [ "$r" = "$c" ]; then
			return 0
		fi
		if [ "${c:0:1}" = "." ] && { [ "$r" = "${c:1}" ] || [[ "$r" == *"$c" ]]; }; then
			return 0
		fi
	done
	return 1
}

for d in "${requested[@]+"${requested[@]}"}"; do
	if ! covered "$d"; then
		echo "apply-role-egress: $d is outside the kit's egress ceiling" >&2
		exit 3
	fi
done

# 3. Write atomically: build each file beside its target (world-readable, as the
# baked lists are), then move it into place, so squid never sees a half-written
# file.
kit_tmp="$(mktemp "${kit_file}.XXXXXX")"
session_tmp="$(mktemp "${session_file}.XXXXXX")"
trap 'rm -f "$kit_tmp" "$session_tmp"' EXIT

{
	if [ "$kit_default" -eq 1 ]; then
		printf '# Active egress policy list: the kit default (image.allowed-domains),\n'
		printf '# restored by harbor when a role egress policy was cleared.\n'
	else
		printf '# Active egress policy list: a harbor role policy applied by harbor.\n'
	fi
	printf '# Within egress_ceiling.txt; additive to the sealed base + infra lists;\n'
	printf '# leading dot = subdomains.\n'
	for d in "${requested[@]+"${requested[@]}"}"; do
		printf '%s\n' "$d"
	done
} >"$kit_tmp"
{
	printf '# Per-session egress domains applied at session start (COV-39).\n'
	printf '# Cleared when harbor applies a role egress policy.\n'
} >"$session_tmp"
chmod 0644 "$kit_tmp" "$session_tmp"

mv "$kit_tmp" "$kit_file"
mv "$session_tmp" "$session_file"
trap - EXIT

# 4. Re-read the ACL files in place. Fail loudly if squid rejects the new config.
if ! squid -k reconfigure; then
	echo "apply-role-egress: squid -k reconfigure failed" >&2
	exit 1
fi
