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

# hermetic unit tests (no docker/network/ssh)
test:
    go test ./...

# real-ssh integration tests (needs ssh/sshd/ssh-keygen)
integration:
    go test -tags integration ./internal/connect/ -v

# End-to-end dispatch of the reference worker kit (needs real infra; see kits/reference-worker/RUNBOOK.md).
e2e:
    E2E_REPO=${E2E_REPO:?set E2E_REPO=<org>/<scratch-repo>} go test -tags integration ./internal/dispatchrun/ -run TestE2EReferenceWorker -v

# go vet + gofmt check + shell/Dockerfile lint (linters skipped if absent)
lint:
    go vet ./...
    @test -z "$(gofmt -l .)" || { echo "gofmt -w needed for:"; gofmt -l .; exit 1; }
    @if command -v shellcheck >/dev/null; then shellcheck scripts/*.sh internal/assemble/hardening/image-files/usr/local/bin/*.sh; else echo "shellcheck not installed (skipping)"; fi
    @if command -v hadolint >/dev/null; then hadolint internal/assemble/hardening/Dockerfile; else echo "hadolint not installed (skipping)"; fi

# install dev/test tooling (podman, shellcheck, hadolint, jq, just)
setup:
    ./scripts/setup-test-tools.sh
