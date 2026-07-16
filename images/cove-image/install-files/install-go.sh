#!/usr/bin/env bash
set -euxo pipefail

# Required env args
# TARGETARCH
# GO_VERSION

# Go
curl -L "https://go.dev/dl/go${GO_VERSION}.linux-${TARGETARCH}.tar.gz" -o go.tar.gz
tar -C /usr/local -xzf go.tar.gz
rm go.tar.gz

echo "Go version: $(/usr/local/go/bin/go version)"

