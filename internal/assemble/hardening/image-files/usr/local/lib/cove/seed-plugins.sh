#!/usr/bin/env bash
# seed-plugins.sh — pre-install the Claude Code plugin marketplace and the
# plugins enabled in managed-settings.json at BUILD time, then fold them into
# the first-boot seed (.init-agent-data/ -> /agent-data).
#
# Why at build time: managed-settings.json enables
# superpowers@claude-plugins-official, but Claude Code's boot-time auto-installer
# would otherwise clone the marketplace and each plugin at RUNTIME. In the
# egress-locked sandbox that clone must traverse the proxy, and two installer
# invocations racing into the same directory leave it half-written
# ("could not lock config file .git/config: No such file or directory"), so the
# plugin never installs. Provisioning here — on the build host's open network,
# before the runtime egress lock, mirroring the build-time Claude Code install —
# means the sandbox never clones plugins at runtime and the race cannot occur.
#
# Env overrides (defaults are the real build/runtime paths; tests inject temps):
#   COVE_PLUGIN_BUILD_CFG    throwaway CLAUDE_CONFIG_DIR used for the install
#   COVE_PLUGIN_SEED         first-boot seed dir copied to /agent-data at boot
#   COVE_PLUGIN_RUNTIME_CFG  runtime CLAUDE_CONFIG_DIR the recorded paths resolve to
#   COVE_PLUGIN_RUN_AS       user to run `claude` as via `su -` (empty = inline)
set -euo pipefail

BUILD_CFG="${COVE_PLUGIN_BUILD_CFG:-/tmp/cove-plugin-seed}"
SEED="${COVE_PLUGIN_SEED:-/home/agent/.init-agent-data}"
RUNTIME_CFG="${COVE_PLUGIN_RUNTIME_CFG:-/agent-data}"
RUN_AS="${COVE_PLUGIN_RUN_AS-agent}"

# The marketplace and the plugins to provision. Keep in sync with the
# enabledPlugins block of etc/claude-code/managed-settings.json.
MARKETPLACE="anthropics/claude-plugins-official"
PLUGINS=("superpowers@claude-plugins-official")

rm -rf "$BUILD_CFG"
mkdir -p "$BUILD_CFG"

# Assemble the install as a single command string so it can run under `su -`.
# Values are fixed identifiers with no shell metacharacters, so single-quoting
# is sufficient.
steps="export CLAUDE_CONFIG_DIR='$BUILD_CFG'; claude plugin marketplace add '$MARKETPLACE';"
for p in "${PLUGINS[@]}"; do
	steps="$steps claude plugin install '$p';"
done
steps="$steps claude plugin list"

if [ -n "$RUN_AS" ]; then
	chown -R "$RUN_AS":"$RUN_AS" "$BUILD_CFG"
	su - "$RUN_AS" -c "set -e; $steps"
else
	bash -c "set -e; $steps"
fi

# The CLI records absolute paths in these registries (installLocation /
# installPath). Rewrite the throwaway build dir to the runtime CLAUDE_CONFIG_DIR
# so they resolve once the seed is copied into /agent-data on first boot.
for f in known_marketplaces.json installed_plugins.json; do
	if [ -f "$BUILD_CFG/plugins/$f" ]; then
		sed -i "s#$BUILD_CFG#$RUNTIME_CFG#g" "$BUILD_CFG/plugins/$f"
	fi
done

# Fold the plugin state (marketplace clone, plugin cache, registries) into the
# first-boot seed. entrypoint.sh copies .init-agent-data/. -> /agent-data on a
# fresh state volume, so /agent-data/plugins lands with runtime-correct paths.
mkdir -p "$SEED/plugins"
cp -a "$BUILD_CFG/plugins/." "$SEED/plugins/"
if [ -n "$RUN_AS" ]; then
	chown -R "$RUN_AS":"$RUN_AS" "$SEED/plugins"
fi

rm -rf "$BUILD_CFG"
