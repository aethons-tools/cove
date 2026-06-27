#!/usr/bin/env bash
set -euxo pipefail

# Install Go

# When running on Colima
curl -L https://go.dev/dl/go1.26.4.linux-arm64.tar.gz -o go.tar.gz

# TODO for firecracker and fly
#curl -L https://go.dev/dl/go1.26.4.linux-amd64.tar.gz -o go.tar.gz

tar -C /usr/local -xzf go.tar.gz
rm go.tar.gz
echo 'export GOROOT=/usr/local/go' >> /etc/bash.bashrc
echo 'export GOPATH=/home/agent/go' >> /etc/bash.bashrc
echo 'export PATH=$PATH:$GOROOT/bin:$GOPATH/bin' >> /etc/bash.bashrc
/usr/local/go/bin/go version