#!/usr/bin/env bash
set -euxo pipefail

# Required env args
# NODE_MAJOR_VERSION

# ---------------------------------------------------------------------------
# Node.js ${NODE_MAJOR_VERSION}.x + npm.
# Installs to /usr/bin, already on the Dockerfile PATH.
# ---------------------------------------------------------------------------
curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR_VERSION}.x" | bash -
DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nodejs
rm -rf /var/lib/apt/lists/*

echo "Node version: $(node --version)"
echo "NPM version: $(npm --version)"
