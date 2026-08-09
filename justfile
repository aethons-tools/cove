# Aethon's Tools — cove. Thin task runner over the scripts; run `just` to list.
# Build/lint logic lives in scripts/ so CI never needs `just` installed.
set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# list available recipes
default:
    @just --list

# build for the current host into dist/<os>-<arch>/{at-cove,at-dispatch} (host-sensitive)
build:
    ./scripts/build.sh

# cross-compile every supported target into dist/<os>-<arch>/{at-cove,at-dispatch}
build-all:
    ./scripts/build.sh all

# build for the host, then run that binary, forwarding ARGS.
# e.g. `just run status`  or  `just run -- --dry-run create`
run *ARGS: build
    "dist/$(go env GOOS)-$(go env GOARCH)/at-cove" {{ARGS}}

# build, then run the at-dispatch binary, forwarding ARGS.
# e.g. `just run-dispatch version`
run-dispatch *ARGS: build
    "dist/$(go env GOOS)-$(go env GOARCH)/at-dispatch" {{ARGS}}

# install the host binaries (at-cove, at-dispatch) onto your PATH.
# Default dir: $(go env GOBIN) or $(go env GOPATH)/bin (~/go/bin) — no sudo.
# Override with BINDIR, e.g. `BINDIR=/usr/local/bin just install` (may need sudo)
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    bin="${BINDIR:-$(go env GOBIN)}"
    [ -n "$bin" ] || bin="$(go env GOPATH)/bin"
    plat="$(go env GOOS)-$(go env GOARCH)"
    mkdir -p "$bin"
    for b in at-cove at-task at-mint; do
      install -m 0755 "dist/$plat/$b" "$bin/$b"
      echo "installed dist/$plat/$b -> $bin/$b"
    done
    if command -v at-cove >/dev/null 2>&1; then
      echo "on PATH: $(command -v at-cove) ($(at-cove version))"
    else
      echo "warning: $bin is not on your PATH — add it or use BINDIR=/usr/local/bin"
    fi

# regenerate internal/basedigest/blessed-digests.txt from the registry (COV-44 §4).
# no-op without GITHUB_TOKEN; needs a packages:read token to snapshot the base list.
gen-blessed:
    go run ./cmd/gen-blessed

# adopt a published base image: resolve <tag> to its @sha256 index digest and pin
# it in .at-cove/config.yml (image.base). Add --breaking to also raise the blessed
# watermark, and --pr to branch+commit+open a PR. Needs GITHUB_TOKEN (read:packages).
# e.g. `just adopt-base 527-0808 --breaking --pr`
adopt-base *ARGS:
    go run ./cmd/adopt-base {{ARGS}}

# hermetic unit tests (no docker/network/ssh)
test:
    go test ./...

# hermetic test for the root install.sh installer (stubs gh/uname; no network)
test-install:
    bash install.test.sh

# real-ssh integration tests (needs ssh/sshd/ssh-keygen)
integration:
    go test -tags integration ./internal/connect/ -v

# End-to-end dispatch of the reference worker kit (needs real infra; see kits/reference-worker/RUNBOOK.md).
e2e:
    E2E_REPO=${E2E_REPO:?set E2E_REPO=<org>/<scratch-repo>} go test -tags integration ./internal/dispatchrun/ -run TestE2EReferenceWorker -v

# docker-in-sandbox e2e: install+boot a docker:true kit under Sysbox and assert the
# booted sandbox (needs a colima VM with Sysbox; see internal/dockere2e/RUNBOOK.md).
integration-docker:
    COVE_DOCKER_E2E=1 go test -tags integration ./internal/dockere2e/ -run TestDockerInSandboxE2E -v -timeout 20m

# go vet + gofmt check + shell/Dockerfile lint (linters skipped if absent; STRICT=1 to require them)
lint:
    ./scripts/lint.sh

# install dev/test tooling (podman, shellcheck, hadolint, jq, just)
setup:
    ./scripts/setup-test-tools.sh
