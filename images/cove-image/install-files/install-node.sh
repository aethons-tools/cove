#!/usr/bin/env bash
set -euxo pipefail

# Required env args
# NODE_MAJOR_VERSION — nodesource repo channel (e.g. 22)
# NODE_VERSION       — exact apt version to pin (e.g. 22.23.1-1nodesource1)

# ---------------------------------------------------------------------------
# Node.js ${NODE_MAJOR_VERSION}.x + npm, pinned to ${NODE_VERSION}.
# Installs to /usr/bin, already on the Dockerfile PATH.
# ---------------------------------------------------------------------------
curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR_VERSION}.x" | bash -
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "nodejs=${NODE_VERSION}"
rm -rf /var/lib/apt/lists/*

echo "Node version: $(node --version)"
echo "NPM version: $(npm --version)"
