#!/usr/bin/env bash
# seed-plugins.sh — pre-install Claude Code plugin marketplaces and plugins at
# BUILD time (the harness layer, between the kit base and the sealed hardening
# layer), then fold them into the first-boot seed (.init-agent-data/ ->
# /agent-data).
#
# Usage: seed-plugins.sh -m NAME=GITHUB_SOURCE... -p PLUGIN_ID...
#   -m  a marketplace: its name and the `claude plugin marketplace add` (GitHub)
#       source, e.g. claude-plugins-official=anthropics/claude-plugins-official
#   -p  a plugin id, name@marketplace, e.g. superpowers@claude-plugins-official
# The marketplaces and plugins come from the model-spec's claude.plugins
# (internal/harnessinstall generates the invocation; ids are validated there to
# a shell-inert alphabet).
#
# It also ENABLES exactly these plugins (and declares their marketplaces) in the
# first-boot user settings ($SEED/settings.json, merged over the base image's),
# so interactive sessions — which get no per-run --settings — have them on. It
# never reads the managed settings (harness-layer sandbox policy, COV-245):
# plugin enablement follows the model-spec, and the headless agent's per-run
# settings carry it too.
#
# Why at build time: Claude Code's boot-time auto-installer would otherwise
# clone the marketplace and each enabled plugin at RUNTIME. In the egress-locked
# sandbox that clone must traverse the proxy, and two installer invocations
# racing into the same directory leave it half-written ("could not lock config
# file .git/config: No such file or directory"), so the plugin never installs.
# Provisioning here — on the build host's open network, before the runtime
# egress lock, right after the build-time Claude Code install — means the
# sandbox never clones plugins at runtime and the race cannot occur.
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

MARKETPLACES=()
PLUGINS=()
while [ "$#" -gt 0 ]; do
	case "$1" in
	-m) MARKETPLACES+=("$2"); shift 2 ;;
	-p) PLUGINS+=("$2"); shift 2 ;;
	*) echo "seed-plugins.sh: unknown argument $1" >&2; exit 2 ;;
	esac
done
if [ "${#PLUGINS[@]}" -eq 0 ]; then
	echo "seed-plugins.sh: no plugins to seed" >&2
	exit 2
fi

rm -rf "$BUILD_CFG"
mkdir -p "$BUILD_CFG"

# Assemble the install as a single command string so it can run under `su -`.
# Values are validated identifiers with no shell metacharacters, so
# single-quoting is sufficient.
steps="export CLAUDE_CONFIG_DIR='$BUILD_CFG';"
for m in "${MARKETPLACES[@]}"; do
	steps="$steps claude plugin marketplace add '${m#*=}';"
done
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

# Enable the seeded plugins in the first-boot user settings (see header).
settings="$SEED/settings.json"
[ -s "$settings" ] || echo '{}' >"$settings"
enabled='{}'
for p in "${PLUGINS[@]}"; do
	enabled="$(jq -c --arg p "$p" '. + {($p): true}' <<<"$enabled")"
done
markets='{}'
for m in "${MARKETPLACES[@]}"; do
	markets="$(jq -c --arg n "${m%%=*}" --arg r "${m#*=}" '. + {($n): {source: {source: "github", repo: $r}}}' <<<"$markets")"
done
jq --argjson e "$enabled" --argjson k "$markets" '.enabledPlugins = $e | .extraKnownMarketplaces = $k' \
	"$settings" >"$settings.tmp"
mv "$settings.tmp" "$settings"
if [ -n "$RUN_AS" ]; then
	chown "$RUN_AS":"$RUN_AS" "$settings"
fi
