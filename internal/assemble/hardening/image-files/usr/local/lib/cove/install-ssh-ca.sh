#!/usr/bin/env bash
# Install Jam's SSH user-CA public key as /etc/ssh/jam_user_ca.pub, the file
# sshd's TrustedUserCAKeys (sshd_config.d/cove.conf) points at: certs that CA
# issues for principal "agent" may then log in as agent.
#
# Reads exactly one authorized_keys line from STDIN: `ssh-ed25519 <base64>
# [comment]`, no options and no second line (a second line would add a second
# trusted CA). Validates before writing: on bad input it exits 2 and changes
# nothing. On success the file is replaced atomically (0644); sshd reads it per
# connection, so no reload is needed.
#
# SEALED + ROOT-ONLY: delivered by the hardening layer and invoked solely by the
# host via `docker exec -u root` (Jam's launcher, at studio raise). /etc/ssh is
# root-owned, so the non-root agent workload cannot plant its own CA. Absent
# file (a plain at-cove sandbox, or a studio Jam gave no CA) = no cert logins;
# plain key auth is unaffected.
set -euo pipefail
export LC_ALL=C

if [ "$(id -u)" -ne 0 ]; then
	echo "install-ssh-ca: must run as root (delivered via host docker exec)" >&2
	exit 1
fi

ca_file="${COVE_SSH_USER_CA_FILE:-/etc/ssh/jam_user_ca.pub}"

key="$(head -c 4097)"
if [ "${#key}" -gt 4096 ]; then
	echo "install-ssh-ca: key line too long" >&2
	exit 2
fi
if ! [[ "$key" =~ ^ssh-ed25519\ [A-Za-z0-9+/]+={0,2}(\ [[:print:]]*)?$ ]]; then
	echo "install-ssh-ca: stdin must be one ssh-ed25519 public key line" >&2
	exit 2
fi

tmp="$(mktemp "${ca_file}.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
printf '%s\n' "$key" > "$tmp"
chmod 0644 "$tmp"
mv -f "$tmp" "$ca_file"
trap - EXIT
