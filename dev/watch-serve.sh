#!/usr/bin/env bash
# watch-serve.sh — the entrypoint air runs for `just dev-watch` (see .air.toml).
# Runs the air-built .air/at-jam serve against the dev config. The broker binds
# :443, so it runs under sudo by default; JAM_WATCH_SUDO= (empty) runs it as you.
# exec, so air's SIGINT on restart reaches sudo, which relays it to Jam.
set -euo pipefail
cd "$(dirname "$0")/.."
sudo_cmd=${JAM_WATCH_SUDO-sudo}
exec $sudo_cmd ./.air/at-jam serve --config "${JAM_WATCH_CONFIG:-dev/jam.dev.yml}"
