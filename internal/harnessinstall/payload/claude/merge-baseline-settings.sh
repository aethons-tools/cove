#!/usr/bin/env bash
# merge-baseline-settings.sh — merge the harness layer's BASELINE Claude Code
# preferences UNDER the first-boot seed user settings ($SEED/settings.json) at
# BUILD time (the harness layer, after the plugin seed).
#
# Usage: merge-baseline-settings.sh BASELINE_JSON
# BASELINE_JSON is rendered by internal/harnessinstall from
# modelspec.DefaultClaudeSettings — the preferences the sealed managed settings
# used to force (COV-245). Every key already in the seed settings (the base
# image's own settings.json, the plugin seed's enablement) wins; a missing or
# empty file becomes the baseline. So every session — interactive, spec-less,
# any model-spec, any kit base — starts from these preferences as its
# lowest-precedence user settings, and a model-spec's claude.settings overrides
# them per run (--settings).
#
# Env overrides (defaults are the real build paths; tests inject temps):
#   COVE_PLUGIN_SEED    first-boot seed dir copied to /agent-data at boot
#   COVE_PLUGIN_RUN_AS  owner of the seed settings file (empty = leave as is)
set -euo pipefail

baseline="${1:?usage: merge-baseline-settings.sh BASELINE_JSON}"
SEED="${COVE_PLUGIN_SEED:-/home/agent/.init-agent-data}"
RUN_AS="${COVE_PLUGIN_RUN_AS-agent}"

settings="$SEED/settings.json"
mkdir -p "$SEED"
[ -s "$settings" ] || echo '{}' >"$settings"
# jq `*` is a deep merge with the right side winning: the existing seed
# settings override the baseline.
jq -s '.[0] * .[1]' "$baseline" "$settings" >"$settings.tmp"
mv "$settings.tmp" "$settings"
if [ -n "$RUN_AS" ]; then
	chown "$RUN_AS":"$RUN_AS" "$settings"
fi
