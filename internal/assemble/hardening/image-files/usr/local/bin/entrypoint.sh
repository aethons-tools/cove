#!/usr/bin/env bash
set -euxo pipefail
#
#AGENT_HOME=/home/agent
#
## The volumes might be root-owned on first mount; hand them to the agent.
#WORKSPACE="$AGENT_HOME/workspace"
#mkdir -p "$WORKSPACE"
#chown agent:agent "$WORKSPACE"
#
#
## Persist Claude Code's state on the VOLUME (not the ephemeral rootfs) by linking the agent's config paths into /agent-state.
## This should be a volume mount in situations where the rootfs is ephemeral (fly machines).
## This keeps session history, todos, settings, and any OAuth credentials across stop/start. The native binary itself
## stays baked into the image and is rebuilt on deploy, so it is NOT persisted here.
##STATE=/agent-state
##mkdir -p "$STATE"
##chown agent:agent "$STATE"
##install -d -o agent -g agent "$STATE/.claude"
#
#

mkdir -p /agent-data
cp -a /home/agent/.init-agent-data/. /agent-data
rm -rf /home/agent/.init-agent-data
chown -R agent:agent /agent-data


##ln -sfn "$STATE/.claude"       /home/agent/.claude
##ln -sfn "$STATE/.claude.json"  /home/agent/.claude.json
#
#chown -h agent:agent "$STATE"
#chown -R agent:agent /home/agent/.claude /home/agent/.claude.json
#
### If an explicit agent state directory is found, it is a volume mount;
### relocate the home directory agent state there.
##if [ -e "$STATE" ]; then
##
##  # First-boot seed: if the agent has no state yet, seed it from whatever the
##  # image baked into the home dir (settings.json, CLAUDE.md, skills, commands).
##  # On later boots the volume already has state, so we DON'T touch it.
##  if [ ! -e "$STATE/.claude" ]; then
##    if [ -d /home/agent/.claude ] && [ ! -L /home/agent/.claude ]; then
##    else
##      install -d "$STATE/.claude"
##    fi
##  fi
##  if [ ! -e "$STATE/.claude.json" ]; then
##    if [ -f /home/agent/.claude.json ] && [ ! -L /home/agent/.claude.json ]; then
##      echo "copying .claude.json"
##      cp -a /home/agent/.claude.json "$STATE/.claude.json"
##    else
##      install -m 600 /dev/null "$STATE/.claude.json"
##    fi
##  fi
##  chown -R agent:agent "$STATE"
##
##  # Replace the home paths with symlinks to the volume.
##
##else
##
##fi

# Raise the egress allow-list BEFORE anything can touch the network.
nft -f /etc/nftables.conf

# Start the filtering proxy. Squid drops to the 'proxy' user, which is the only
# uid nftables permits to make outbound connections.
mkdir -p /var/log/squid && chown proxy:proxy /var/log/squid
squid -f /etc/squid/squid.conf

## Hand off to sshd in the foreground as the machine's main process.
## exec /usr/sbin/sshd -D -e
#
## Start claude in remote control mode
##exec su - agent -c "claude --remote-control --dangerously-skip-permissions"


exec sudo -u agent \
  CLAUDE_CONFIG_DIR=/agent-data \
  GOSUMDB=off \
  -i bash -c "cd /home/agent/workspace && bash"
