#!/bin/sh
# Build-time toolchain install (runs as root; build-time egress is open). TEMPLATE:
# adjust for your base image and target project.
set -e

# 1) at-work — the git/PR worker. Pin a ref for reproducibility.
#    Requires Go on the build image (install it here if the base image lacks it).
go install github.com/aethons-tools/cove/cmd/at-work@main   # <-- pin to a tag/SHA

# 2) The agent CLI (claude). Install per your base image, e.g.:
#    npm install -g @anthropic-ai/claude-code
#    (left to the operator; the base image may already provide it.)

# 3) git and the target project's build toolchain — add here.

# The worker/agent scripts arrive via image-files at /usr/local/bin (already on PATH).
chmod 0755 /usr/local/bin/run-worker.sh /usr/local/bin/run-agent.sh
