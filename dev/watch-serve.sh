#!/usr/bin/env bash
# watch-serve.sh — the entrypoint air runs for `just dev-watch` (see .air.toml).
# Runs the air-built .air/at-jam serve against the dev config, as you: on macOS a
# wildcard bind of :443 (listen: ":443") needs no root. JAM_WATCH_SUDO=1 runs it
# under sudo instead (e.g. Linux, or a listen bound to a specific address).
# exec, so air's SIGINT on restart reaches Jam (or sudo, which relays it).
set -euo pipefail
cd "$(dirname "$0")/.."
cfg="${JAM_WATCH_CONFIG:-dev/jam.dev.yml}"
if [ -n "${JAM_WATCH_SUDO:-}" ]; then
    exec sudo ./.air/at-jam serve --config "$cfg"
fi
exec ./.air/at-jam serve --config "$cfg"
