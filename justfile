# Aethon's Tools — cove. Thin task runner over the scripts; run `just` to list.
# Build/lint logic lives in scripts/ so CI never needs `just` installed.
set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# list available recipes
default:
    @just --list

# build for the current host into dist/<os>-<arch>/{jam,jam-studio-manager} (host-sensitive)
build:
    ./scripts/build.sh

# cross-compile every supported target into dist/<os>-<arch>/{jam,jam-studio-manager}
build-all:
    ./scripts/build.sh all

# Run the jam binary, forwarding ARGS (uses the last `just build` output —
# run `just build` first). e.g. `just jam destination list`  or  `just jam version`
jam *ARGS:
    "dist/$(go env GOOS)-$(go env GOARCH)/jam" {{ARGS}}

# install the host binaries (jam, jam-studio-manager) onto your PATH.
# Default dir: $(go env GOBIN) or $(go env GOPATH)/bin (~/go/bin) — no sudo.
# Override with BINDIR, e.g. `BINDIR=/usr/local/bin just install` (may need sudo)
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    bin="${BINDIR:-$(go env GOBIN)}"
    [ -n "$bin" ] || bin="$(go env GOPATH)/bin"
    plat="$(go env GOOS)-$(go env GOARCH)"
    mkdir -p "$bin"
    for b in jam jam-studio-manager; do
      install -m 0755 "dist/$plat/$b" "$bin/$b"
      echo "installed dist/$plat/$b -> $bin/$b"
    done
    if command -v jam >/dev/null 2>&1; then
      echo "on PATH: $(command -v jam) ($(jam version))"
    else
      echo "warning: $bin is not on your PATH — add it or use BINDIR=/usr/local/bin"
    fi

# regenerate internal/basedigest/blessed-digests.txt from the registry (COV-44 §4).
# no-op without GITHUB_TOKEN; needs a packages:read token to snapshot the base list.
gen-blessed:
    go run ./cmd/gen-blessed

# regenerate the Jam app's PNG icons from the J mark (internal/jam/uiassets/static/icons).
# Generated files ARE committed; a test fails when they drift from the SVG.
icons:
    go run ./cmd/gen-icons

# regenerate the jam Attach stream's gRPC code (internal/jam/attach/attachpb)
# from internal/jam/attach/proto/attach.proto. Installs protoc-gen-go,
# protoc-gen-go-grpc, and buf (pinned) into GOPATH/bin, then runs `buf generate`.
# Generated files ARE committed — CI never regenerates.
buf-gen:
    #!/usr/bin/env bash
    set -euo pipefail
    export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local
    go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
    go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
    go install github.com/bufbuild/buf/cmd/buf@v1.45.0
    (cd internal/jam/attach && PATH="$PATH:$(go env GOPATH)/bin" buf generate)
    (cd internal/jam/managers && PATH="$PATH:$(go env GOPATH)/bin" buf generate)

# raise the blessed watermark to a published, breaking base image: resolve <tag>
# to its jam-studio-base @sha256 index digest and rewrite
# internal/basedigest/blessed/watermark.txt. Add --pr to branch+commit+open a PR.
# Needs GITHUB_TOKEN (read:packages). e.g. `just adopt-base 527-0808 --pr`
adopt-base *ARGS:
    go run ./cmd/adopt-base {{ARGS}}

# generate a self-signed dev broker cert for jam.local.aethons.tools into dev/tls/ (gitignored)
dev-cert:
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p dev/tls
    openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
      -subj "/CN=jam.local.aethons.tools" \
      -addext "subjectAltName=DNS:jam.local.aethons.tools" \
      -keyout dev/tls/key.pem -out dev/tls/cert.pem
    # openssl writes key.pem 0600; the Jam container runs as uid 10001 and, on
    # Colima mounts, cannot read a 0600 host file. Dev-only self-signed key.
    chmod 644 dev/tls/key.pem
    echo "wrote dev/tls/cert.pem, dev/tls/key.pem (CN=jam.local.aethons.tools)"

# raise the local dev Postgres (host port 15432) and Garage blob store (13900) for Jam; see dev/README.md
dev-up:
    docker compose -f dev/docker-compose.yml up -d

# stop the local dev Postgres and Garage. Pass ARGS=-v to also wipe their data volumes.
dev-down *ARGS:
    docker compose -f dev/docker-compose.yml --profile jam down {{ARGS}}

# start Jam in its container (profile "jam") beside Postgres and Garage; see dev/docker-compose.yml
dev-jam-up:
    #!/usr/bin/env bash
    set -euo pipefail
    for f in dev/jam.container.yml dev/credentials.container.yml; do
      [ -f "$f" ] || { echo "missing $f: cp $f.example $f (docker would create a directory at the bind source)" >&2; exit 1; }
    done
    docker compose -f dev/docker-compose.yml --profile jam up -d

# follow the Jam container's logs
dev-jam-logs:
    docker compose -f dev/docker-compose.yml logs -f jam

# run a jam verb inside the Jam container (loopback-trusted admin), e.g. `just dev-jam destination list`
dev-jam *ARGS:
    docker compose -f dev/docker-compose.yml exec jam jam {{ARGS}}

# Run jam serve against the local dev config (uses the last `just build`
# output — run `just build` + `just dev-cert` + `just dev-up` first). macOS binds a
# wildcard :443 without root; on Linux, sudo the built binary directly for that.
dev-serve *ARGS:
    "dist/$(go env GOOS)-$(go env GOARCH)/jam" serve --config dev/jam.dev.yml {{ARGS}}

# Config in .air.toml; needs a plain-HTTP admin listener (no admin-tls). Runs
# Jam as you (macOS wildcard :443 bind); JAM_WATCH_SUDO=1 uses sudo. See dev/README.md.
# live-reload jam serve (air): rebuild+restart on change, browse http://localhost:8090
dev-watch:
    #!/usr/bin/env bash
    set -euo pipefail
    cfg="${JAM_WATCH_CONFIG:-dev/jam.dev.yml}"
    if grep -q '^admin-tls:' "$cfg"; then
        echo "dev-watch: $cfg sets admin-tls; air's browser-reload proxy can only front a plain-HTTP admin listener. Comment out admin-tls (loopback admin may be plain HTTP) — see dev/README.md." >&2
        exit 1
    fi
    if [ -n "${JAM_WATCH_SUDO:-}" ]; then sudo -v; fi
    go run github.com/air-verse/air@v1.67.4 -c .air.toml

# hermetic unit tests (no docker/network/ssh)
test:
    go test ./...

# build the Jam app bundle jam embeds at /app/user/ (needs Node/npm; see
# scripts/app.sh and docs/web-app.md). `just build` runs it when npm is available.
app:
    ./scripts/app.sh

# the Jam app's dev server (hot reload) proxying the API to a running
# `jam serve` (JAM_URL, default http://localhost:8081); open http://localhost:5173/app/user/
app-dev:
    cd web/app && npm ci --no-audit --no-fund --loglevel=error && npx vite

# the Jam app's checks: API types current, typecheck, lint, unit tests
app-check:
    ./scripts/app-types-check.sh
    cd web/app && npm ci --no-audit --no-fund --loglevel=error && npm run check

# headless-Chrome tests of the /app/user UI's JS (needs chrome-headless-shell on PATH or
# JAM_BROWSER; each test skips without one)
test-browser:
    go test -tags browser ./internal/jam/userui/ -run Browser -v

# hermetic test for the root install.sh installer (stubs gh/uname; no network)
test-install:
    bash install.test.sh

# hermetic test for scripts/jam-image.sh, images/jam/Dockerfile and the release.yml wiring (fake docker; no daemon)
test-jam-image:
    bash scripts/jam-image.test.sh

# build the Jam container image (jam:dev) from freshly built linux binaries
jam-image *ARGS:
    scripts/build.sh linux/$(go env GOARCH)
    scripts/jam-image.sh {{ARGS}}

# real-ssh integration tests of the ssh argv builders (needs ssh/sshd/ssh-keygen)
integration:
    go test -tags integration ./internal/sshargs/ -v

# jam integration: real-TLS broker round-trip, real-sshd studio SSH end to end
# (skips without sshd), kit git sources end to end (skips without git), + the
# jam packages' tagged tests
integration-jam:
    go test -tags integration ./cmd/jam/... ./internal/jam/... ./internal/allocator/...

# Discord live check: a real Discord REST round-trip through internal/discord
# (DISCORD_IT_TOKEN + DISCORD_IT_CHANNEL); skips if they are unset.
integration-discord:
    go test -tags integration ./internal/discord/ -run TestDiscordRoundTripLive -v -timeout 5m

# go vet + gofmt check + shell/Dockerfile lint (linters skipped if absent; STRICT=1 to require them)
lint:
    ./scripts/lint.sh

# install dev/test tooling (podman, shellcheck, hadolint, jq, just)
setup:
    ./scripts/setup-test-tools.sh

# operator-attention monitoring: local Prometheus + Alertmanager scraping <jam-host>
# (put scrape-token, jam-ca.pem and discord-webhook in deploy/monitoring/.local/ first)
monitoring-up jam_host:
    mkdir -p deploy/monitoring/.local
    for f in scrape-token jam-ca.pem discord-webhook; do test -s deploy/monitoring/.local/$f || { echo "missing deploy/monitoring/.local/$f (see docs/usage/jam/monitoring.md)"; exit 1; }; done
    chmod 644 deploy/monitoring/.local/scrape-token deploy/monitoring/.local/jam-ca.pem deploy/monitoring/.local/discord-webhook
    sed 's/@JAM_HOST@/{{jam_host}}/g' deploy/monitoring/prometheus.yml > deploy/monitoring/.local/prometheus.yml
    JAM_HOST={{jam_host}} docker compose -f deploy/monitoring/compose.yml up -d

monitoring-down:
    JAM_HOST=unused docker compose -f deploy/monitoring/compose.yml down
