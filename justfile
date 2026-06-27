# Aethon's Tools — cove. Thin task runner over the scripts; run `just` to list.
# Build/lint logic lives in scripts/ so CI never needs `just` installed.
set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# list available recipes
default:
    @just --list

# build for the current host into dist/<os>-<arch>/at-cove (host-sensitive)
build:
    ./scripts/build.sh

# cross-compile every supported target into dist/<os>-<arch>/at-cove
build-all:
    ./scripts/build.sh all

# hermetic unit tests (no docker/network/ssh)
test:
    go test ./...

# real-ssh integration tests (needs ssh/sshd/ssh-keygen)
integration:
    go test -tags integration ./internal/connect/ -v

# go vet + gofmt check + shell/Dockerfile lint (linters skipped if absent)
lint:
    go vet ./...
    @test -z "$(gofmt -l .)" || { echo "gofmt -w needed for:"; gofmt -l .; exit 1; }
    @if command -v shellcheck >/dev/null; then shellcheck scripts/*.sh internal/assemble/hardening/image-files/usr/local/bin/*.sh; else echo "shellcheck not installed (skipping)"; fi
    @if command -v hadolint >/dev/null; then hadolint internal/assemble/hardening/Dockerfile; else echo "hadolint not installed (skipping)"; fi

# install dev/test tooling (podman, shellcheck, hadolint, jq, just)
setup:
    ./scripts/setup-test-tools.sh
