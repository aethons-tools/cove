#!/usr/bin/env bash
set -euxo pipefail

# First-boot seed of the persistent state volume (/agent-data is CLAUDE_CONFIG_DIR).
# Guarded by a marker so a restart — or a recreate against an existing volume —
# never clobbers saved state, including the OAuth credentials that
# `claude auth login` writes to /agent-data/.credentials.json.
if [ ! -e /agent-data/.seeded ]; then
  mkdir -p /agent-data
  cp -a /home/agent/.init-agent-data/. /agent-data/
  touch /agent-data/.seeded
fi
chown -R agent:agent /agent-data

# Raise the egress allow-list BEFORE anything can touch the network.
nft -f /etc/nftables.conf

# Start the filtering proxy. Squid drops to the 'proxy' user, which is the only
# uid nftables permits to make outbound connections.
mkdir -p /var/log/squid && chown proxy:proxy /var/log/squid
squid -f /etc/squid/squid.conf

# Ensure SSH host keys exist (idempotent; a freshly built image may not have
# them yet). atsbx pins them per-sandbox via known_hosts TOFU on first connect.
ssh-keygen -A

# Hand off to sshd in the foreground as the container's main process. atsbx
# reaches this sshd over the mapped port, injects secrets, and launches claude
# per `connect`. CLAUDE_CONFIG_DIR=/agent-data is supplied to every session via
# /etc/environment (pam_env), so claude finds its state on the volume.
exec /usr/sbin/sshd -D -e
