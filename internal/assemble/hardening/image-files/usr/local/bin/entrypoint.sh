#!/usr/bin/env bash
set -euxo pipefail

# First-boot seed of the persistent state volume (/agent-data is CLAUDE_CONFIG_DIR).
# Guarded by a marker so a restart — or a recreate against an existing volume —
# never clobbers saved state, including the OAuth credentials that
# `claude auth login` writes to /agent-data/.credentials.json.
SEED=/home/agent/.init-agent-data
if [ ! -e /agent-data/.seeded ]; then
  mkdir -p /agent-data
  cp -a "$SEED/." /agent-data/
  touch /agent-data/.seeded
fi
# Every boot: re-mirror the image-owned reference set so a rebuilt image's updates
# reach existing sandboxes. These subtrees hold no user state; runtime-owned seed
# files (.claude.json, settings.json, plugins/, COLLABORATOR.md) are NOT touched.
for d in skills reference; do
  [ -d "$SEED/$d" ] && rm -rf "/agent-data/$d" && cp -a "$SEED/$d" "/agent-data/$d"
done
for f in CLAUDE.md PROGRESSIVE_DISCLOSURE.md SANDBOX.md; do
  cp -a "$SEED/$f" "/agent-data/$f"
done
chown -R agent:agent /agent-data

# A share-repo-dir class may overmount transient dirs (.venv, node_modules) with
# fresh per-sandbox volumes (COV-132). Each mounts empty and root:root, so chown
# the mountpoint to agent (non-recursive: first boot is empty; later boots' content
# is already agent-owned). COVE_SHADOW_DIRS is the space-joined list from the run.
# Defense-in-depth for this sealed file: skip anything that escapes the workspace
# (config already rejects '..'/absolute at parse time). Guard on existence so a dir
# that is somehow not mounted cannot abort boot under `set -e`.
for d in ${COVE_SHADOW_DIRS:-}; do
  case "$d" in /*|*/../*|../*|*/..|..) continue ;; esac
  p="/home/agent/workspace/$d"
  if [ -e "$p" ]; then chown agent:agent "$p"; fi
done

# docker:true sandboxes boot systemd as PID 1 (Sysbox gives the unprivileged
# container a real init environment), and systemd raises the egress lock via
# cove-egress.service (nftables drop) + the distro squid.service BEFORE it starts
# sshd or the inner rootful dockerd — see the sealed units under /etc/systemd/system.
# The backend runs these sandboxes WITHOUT --init, so this exec makes systemd PID 1
# (COV-118, COV-125). The seed/refresh above ran
# for both paths (it touches no network); everything below is the non-docker path,
# byte-for-byte unchanged.
if [ "${COVE_DOCKER:-}" = "1" ]; then
  exec /sbin/init
fi

# Raise the egress allow-list BEFORE anything can touch the network.
nft -f /etc/nftables.conf

# Start the filtering proxy. Squid drops to the 'proxy' user, which is the only
# uid nftables permits to make outbound connections.
mkdir -p /var/log/squid && chown proxy:proxy /var/log/squid
squid -f /etc/squid/squid.conf

# Ensure SSH host keys exist (idempotent; a freshly built image may not have
# them yet). cove pins them per-sandbox via known_hosts TOFU on first connect.
ssh-keygen -A

# Hand off to sshd in the foreground as the container's main process. cove
# reaches this sshd over the mapped port, injects secrets, and launches claude
# per `connect`. CLAUDE_CONFIG_DIR=/agent-data is supplied to every session via
# /etc/environment (pam_env), so claude finds its state on the volume.
exec /usr/sbin/sshd -D -e
