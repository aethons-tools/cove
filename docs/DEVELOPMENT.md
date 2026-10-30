---
summary: Operational notes for building and testing this repo inside the egress-locked dev sandbox.
read_when: You are building or testing this repo inside the egress-locked dev sandbox and `go` or `just` won't fetch or build.
owns: dev-sandbox toolchain settings (GOPROXY/GOSUMDB/GOPATH) and build/test workarounds
prereqs: OVERVIEW.md
tier: leaf
updated: 2026-10-30
---

# Development notes

Operational notes for building and testing this repo,
including the workarounds needed inside the egress-locked dev sandbox this repo is developed in.
See [`OVERVIEW.md`](OVERVIEW.md) for the architecture and the `just` task list.

## The dev sandbox is egress-locked

This repo is developed inside a sandbox with no open internet:

- An HTTP(S) proxy at `127.0.0.1:3128` (already exported as `HTTP_PROXY`/`HTTPS_PROXY`).
- An allow-list permitting only a fixed set of domains —
  `.anthropic.com`, `.claude.com`, `claude.ai`, `.github.com`, `.githubusercontent.com`, `gopkg.in`, `pypi.org`, `.pythonhosted.org`.

Anything off the list is dropped,
which is what makes the Go toolchain settings below necessary.

## Go toolchain settings

Two host constraints shape how `go` is run here:

- **`proxy.golang.org` / `sum.golang.org` are not reachable** (only `github.com` and `gopkg.in` are allow-listed).
  Set `GOPROXY=direct` and `GOSUMDB=off` so `go get gopkg.in/yaml.v3@v3.0.1` resolves directly through the allow-listed hosts —
  no module proxy, no checksum DB.
  This is why the one dependency can be fetched without a vendor tree or an allow-list change.
- **The default `GOPATH` (`~/go` → `/home/agent/go`) works.** `jam-studio` provides
  a writable home with `~/go` pre-created, and bakes `GOROOT`/`GOPATH`/`GOPROXY`/
  `GOSUMDB`/`GOFLAGS` as image `ENV` surfaced into the session via `JAM_SSHENV`
  (see [`OVERVIEW.md`](#the-image-tree)). (Older sandboxes redirected
  `GOPATH` to `/home/agent/workspace/.gopath` because `~` was not writable; that
  override is now unnecessary — harmless if a stale `settings.json` still sets it.)

The `cove-ic` studio kit (kept in Jam, not in this repo) differs: it sets `GOPROXY=https://proxy.golang.org,direct`. A studio's
`github.com` git traffic goes through the jam git proxy, which serves only the
studio's own repo, so `direct` alone fails every dependency with a 403. It also
sets the agent's git commit identity (`GIT_AUTHOR_*`/`GIT_COMMITTER_*`, carried
into sessions via `JAM_SSHENV`), so a studio can commit without `git config`.

These are already exported in this environment (via `JAM_SSHENV`), and `go` is on
`PATH`. If you need to set them inline (e.g. a non-session shell that didn't read
`/etc/environment`), prefix the command:

```bash
GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod go test ./...
```

Two consequences worth knowing when adding a dependency:

- **`go get` works for modules hosted on an allow-listed host.** `github.com/jackc/pgx/v5`
  (the Postgres driver) resolves through `github.com` under `GOPROXY=direct`, so it
  needed **no** allow-list change.
- **`go mod tidy` can fail here even when your dependency is fine.** Tidy walks the
  *whole* module graph including transitive **test** dependencies, some of which
  (e.g. `gonum.org/v1/gonum`, pulled by a gRPC test package) live on hosts that are
  not allow-listed and fail with `Forbidden`. That error is about an unrelated
  transitive test-dep, not your change; `go get <mod>` + `go build ./...` already
  write correct `go.mod`/`go.sum` entries. Don't chase a mirror.

## Regenerating gRPC code

`internal/jam/attach/attachpb` (the Jam Attach stream's generated types
and gRPC stubs) is built from `internal/jam/attach/proto/attach.proto` by
`just buf-gen`. It installs `protoc-gen-go`, `protoc-gen-go-grpc`, and a pinned
`buf`, then runs `buf generate`.

- The real module proxy is required here — unlike the `GOPROXY=direct` used
  elsewhere in this sandbox (see above), `buf-gen` sets
  `GOPROXY=https://proxy.golang.org` (plus `GOTOOLCHAIN=local`, so a `go
  install` doesn't try to fetch a different toolchain version) because `buf`
  and the protoc-gen plugins are fetched as tagged modules, not resolved
  directly against `github.com`.
- That means `storage.googleapis.com` (the module proxy's blob store) must be
  egress-allow-listed alongside `proxy.golang.org` — both already are, in the
  `egress:` list of the `cove-ic` studio kit (kept in Jam).
- Generated `.pb.go` files **are committed**. CI never regenerates them — `just
  buf-gen` is a manual step a developer runs after editing the `.proto`, and
  the diff it produces is reviewed and committed like any other source change.

## Tests

- `just test` (`go test ./...`) is **hermetic** —
  every test drives `internal/runner.Fake`,
  so no Docker, network, or live VM is required.
  Keep new tests this way.
  Tests also never touch a real cove path: agents run this suite *inside* coves,
  so a test using a production path (e.g. the agentrun harness's `/dev/shm` files)
  clobbers the live agent. Give every harness test paths (`testClaude`);
  `internal/agentrun`'s `TestMain` fails the run if a real harness file changed.
- `just integration` (`go test -tags integration ./internal/sshargs/ -v`) runs the **real-ssh** suite:
  it boots an unprivileged throwaway `sshd` on loopback
  and exercises the ssh argv builders and TOFU end-to-end with the real `ssh` client.
  Needs `ssh`/`sshd`/`ssh-keygen` but **not** Docker.
- `go test -tags integration ./internal/baseimage/` proves the provenance gate against
  **real docker**: it builds a base, a descendant, and an unrelated image and asserts the
  `diff_id`-prefix `DescendsFrom` check matches OCI reality. Needs Docker + network (pulls alpine).
- `JAM_TEST_POSTGRES_DSN=… go test -tags integration ./internal/jam/... ./internal/intercom/...`
  runs the **Postgres store** conformance + fail-closed suite (`PostgresStore`)
  and the **Postgres squawk log** (`intercompg`) conformance suites — the channel
  log and the legacy log, each case in a fresh schema of its own, so the cutover
  migration runs over a populated legacy log — against a real Postgres; both
  **skip** when `JAM_TEST_POSTGRES_DSN` is unset. Jam is Postgres-only; hermetic
  tests (`go test ./...`) use the in-memory stores `jam.NewMemStore`, `intercom.NewMemLog`,
  and `sessionevents.NewMemStore` (test-only, never constructed by `serve`). Example DSN:
  `host=localhost port=5432 dbname=jam user=jam password=jam sslmode=disable`.
  The sandbox has no Postgres, so run this against your own instance; CI provides one
  (see [CI: the store integration job](#ci-the-store-integration-job)).
- `just test-browser` (`go test -tags browser ./internal/jam/userui/`) drives the
  Jam app at `/app/user` in **headless Chrome** via chromedp (it needs the bundle:
  `just app`). It needs
  `chrome-headless-shell` on `PATH` (or `JAM_BROWSER=<path>`), and each test
  **skips** without one. The `cove-ic` studio kit installs a pinned Chrome
  for Testing build; the same binary can screenshot a page
  (`chrome-headless-shell --screenshot=out.png <url>`) to eyeball a UI change.
- `just setup` installs the optional dev tooling (podman + a `docker` shim, shellcheck, hadolint, jq).
- The remaining untested gap is a full studio raise against a real image,
  which needs a container runtime;
  podman covers the SSH/lifecycle mechanics,
  but the in-container `nftables`/Squid egress lockdown under rootless is the part to watch.

## CI: the merge gate

[`.github/workflows/gate.yml`](../.github/workflows/gate.yml) runs on every PR
to `main`, and on `main` itself. Two legs run in parallel and the `gate` job joins them:

- **`go`:** the app bundle (so the tests serve the real app), `go test ./...`, the installer and image-script tests, and
  [`scripts/lint.sh`](../scripts/lint.sh). These are the same scripts that `just test` and `just lint` run, so CI and the local
  loop cannot drift.
- **`web`:** the Jam app's checks (`just app-check`) and the VS Code extension's (`scripts/vscode.sh`, which uploads the `jam-vscode` artifact).
  [`scripts/npm-ci.sh`](../scripts/npm-ci.sh) installs `web/app` once per job, though three steps use it: it skips `npm ci` when
  `node_modules` was installed from the same lockfile by the same Node.

- The joining job is named **`gate`**, and that name is what branch protection lists as
  the required check. It passes only when both legs passed. Renaming it silently un-gates `main`: protection goes
  on waiting for a check that never reports. Rename only alongside the setting.
- CI runs lint as `STRICT=1 scripts/lint.sh`. Outside CI a missing `shellcheck`
  or `hadolint` is skipped with a note, so a fresh clone can lint before
  `just setup`; in CI that default would let the gate pass having linted
  nothing, so `STRICT=1` turns a missing linter into a failure.
- The gate runs on the **plain runner** and installs the linters per-run — it is
  deliberately *not* run inside `jam-studio` (COV-35). A container gate would
  inherit the image's sandbox-tuned `GOPROXY=direct`/`GOSUMDB=off` (see above) and
  silently stop verifying module checksums, and would add a digest-pin to chase.
  Instead the drift the container would have solved is handled directly: the
  `hadolint` version is pinned in **three** places — this gate, `jam-studio`'s
  Dockerfile, and [`scripts/setup-test-tools.sh`](../scripts/setup-test-tools.sh)
  (local dev) — and one Renovate `customManager` ([`renovate.json`](../renovate.json))
  watches all three, so a bump lands in a single PR and image/CI/local never
  diverge. (`shellcheck` comes from the distro `apt` in the gate + setup script;
  it is not version-pinned there.)
- The workflow needs no `just` — the logic lives in `scripts/`, per the
  justfile's header.
- **Not** gated: `just integration` (real-ssh) and `just test-browser` (headless
  Chrome).
- CI leaves `GOPROXY`/`GOSUMDB` at their defaults. The `direct`/`off` settings
  above are a workaround for *this sandbox's* egress lock; a runner has open
  egress and should verify module checksums against `go.sum`.
- `scripts/lint.sh` runs `hadolint` on all three Dockerfiles —
  `images/jam-studio-base/Dockerfile`, `images/jam-studio/Dockerfile`, and
  `internal/assemble/hardening/Dockerfile`. `.hadolint.yaml` records the rules
  ignored for them, each with its reason. Everything else fails the gate.
- `scripts/lint.sh` runs `shellcheck` on `scripts/*.sh` and the sealed hardening
  helpers under `internal/assemble/hardening/image-files/usr/local/{bin,lib/jam}/`
  — so a sealed helper delivered into a sandbox (e.g. `apply-session-domains.sh`)
  is gated, not just the host scripts.

## CI: the store integration job

[`.github/workflows/store-integration.yml`](../.github/workflows/store-integration.yml)
runs `go test -tags integration ./internal/jam/... ./internal/intercom/...`
against a Postgres **service container**, with `JAM_TEST_POSTGRES_DSN`
pointing at it — the Postgres-backed `jam.Store` conformance suite and the
Postgres squawk log (`intercompg`) conformance suite, both behind the
`//go:build integration` tag. It is a **separate** workflow from `gate.yml` on
purpose: the required check is `gate`, and this job must not touch it. This job
only reports — promoting it to a required check is a branch-protection
setting, not a change here.

## The image tree

The utility set lives in one published, pinned, multi-arch image tree — the
single source of truth shared by CI job containers and the studios
a Studio Manager hardens, so their tooling can't drift. See
[the design](superpowers/specs/2026-07-16-shared-base-image-design.md) for the
full rationale.

- **`images/jam-studio-base/Dockerfile`** — the universal lean floor: OS +
  git/gh/sshd + the egress stack (nftables/squid) + the Docker engine & systemd
  (for the opt-in docker-in-sandbox capability, present-but-inert) + core utils.
  **Pure tools**: no language toolchains, chrome, or java — and **no
  `jam-studio-agent`**: the Studio Manager injects the version-locked `jam-studio-agent` into its
  sealed hardening layer, so the base has no Go build and rebuilds only when `images/` changes (COV-44).
- **`images/jam-studio/Dockerfile`** — `FROM jam-studio-base` + the full
  build/test/run toolchain (go, just, shellcheck, hadolint, node, chrome, java).
  Base for both CI and the sandboxes. It also clones the pinned
  [`samber/cc-skills-golang`](https://github.com/samber/cc-skills-golang) release
  and seeds its Go agent skills into `.init-agent-data/skills/` — so agents
  working on *this* repo get Go-specific skills. This lives in `jam-studio` (not
  `jam-studio-base`, which carries the generic board/docs skills and agent docs
  every kit inherits), so other kits are unaffected; the base seed's `.refresh`
  manifest lists `skills`, so the sealed entrypoint re-mirrors the seed's
  `skills/` into `/agent-data/skills` on every boot and a rebuilt image reaches
  existing sandboxes on restart. The MIT notice is kept at
  `/usr/share/doc/cc-skills-golang/LICENSE`.

**Reproducible by pinning.** Every input is pinned: the `FROM ubuntu:24.04`
manifest digest, each apt package `pkg=version`, the toolchain args
(`GO_VERSION`, `JDK_RELEASE`, `NODE_VERSION`, `HADOLINT_VERSION`), and the
`CC_SKILLS_GOLANG_VERSION` release tag. Versions are
identical across amd64/arm64, so one pin serves both. A pin that ages out of the
archive fails the build loudly. [`renovate.json`](../renovate.json) keeps them
current instead of letting them silently float — mostly a bump PR per newer
version, except the base `ubuntu:24.04` digest and its apt pins, which Renovate
**couples into one grouped PR** (a digest bump alone would fail the rebuild the
moment a pin ages out). (The one input not yet pinned is chromium, which rides
whatever `playwright` resolves.)

**Build & publish** — the images are built and published by the monolithic
pipeline (below); consumers **pin by `@sha256` digest**, never a moving tag
(COV-34/COV-35). Both arches build **natively** (amd64 on `ubuntu-latest`, arm64
on `ubuntu-24.04-arm`) and smoke-test that every tool resolves **before** any
publish. Published (private) to `ghcr.io/aethons-tools/jam-studio-base` and
`…/jam-studio`.

## CI: the release pipeline

[`.github/workflows/release.yml`](../.github/workflows/release.yml) is **one
monolithic continuous-delivery pipeline** (COV-44, design:
[monolithic-release-pipeline](superpowers/specs/2026-07-17-monolithic-release-pipeline-design.md)).
It runs on every push to `main` and on every PR, and **decides from the repo diff
which artifacts to (re)build** — there is no version-tag trigger and no manual
"cut a release" step.

- **Change-detection** (path-based) selects the legs: `images/jam-studio-base/**`
  → rebuild the base **and** jam-studio (downstream) **and** re-cut the host binaries (its
  new digest becomes the default base + blessed-list head);
  `images/jam-studio/**` → rebuild just jam-studio (FROM the current published
  base); `cmd/**` · `internal/**` · `go.*` · `.goreleaser.yaml` · `install.sh` → re-cut the host binaries
  (blessed list recomputed from the registry head). Docs-only → nothing publishes.
- **DAG order (no cycle):** base → publish → digest **D** → [blessed-list
  snapshot, [COV-47](../internal/blessgen)] → the host binaries (embed jam-studio-agent + list); jam-studio = `FROM jam-studio-base:ci`. The base
  publishes **before** the binaries leg so `gen-blessed` sees **D** as the
  registry head.
- **PRs build + smoke the touched legs but never publish** (spec §5); only a push
  to `main` pushes to GHCR / cuts the release. Images publish as multi-arch
  manifests tagged `<N>-<MMDD>` (immutable) + `latest`; the binaries are built by
  `goreleaser --snapshot` (`jam` and `jam-studio-manager` for darwin/linux ×
  amd64/arm64, one `jam_<ver>_<os>_<arch>.tar.gz` per target holding exactly
  those two — checked by
  `scripts/release/check-archive.sh` — plus checksums, stamped `<N>-<MMDD>`) and the
  release cut with `gh` (private). See [Versioning](#versioning).
- **jam-studio-agent is embedded**, not shipped standalone — re-cutting the binaries
  re-cuts it (the [`.goreleaser.yaml`](../.goreleaser.yaml) before-hook runs
  `scripts/stage-studio-agent.sh`).

### Versioning

`<N>-<MMDD>` — `<N>` = `git rev-list --count HEAD` is the version (globally
monotonic, reproducible offline); `<MM><DD>` is an advisory month-day; `-` (not
`.`) so it is never mistaken for SemVer. Every published artifact
(`jam-studio-base`, `jam-studio`, the binaries release) is tagged `<N>-<MMDD>` + a moving
`latest`. Because only what changed is rebuilt, artifacts' `<N>` values may
differ — each reflects when it was last built. Jam references the base by
**digest**, never version, so divergent tags don't affect the gate.

[`scripts/build.sh`](../scripts/build.sh) builds the host binaries locally (no
publish) for iteration; `scripts/build-images.sh` does the same for the image
tree. A `jam` that mints a token runs itself (`<its own path> mint …`, so the
minter is always version-matched), falling back to `jam` on PATH when it can't
resolve its own path.

### Adopting a new base

Publishing is automatic (above), and so is adopting a routine base: the release's
blessed-list snapshot makes the newest published base Jam's default. A **breaking**
base change (one that shifts the OCI layer-diff-id prefix, or one the sealed
hardening layer newly *depends on*) also needs the blessed watermark raised so older
bases stop being trusted. `just adopt-base` does that:

```
just adopt-base <tag>        # raise internal/basedigest/blessed/watermark.txt to <tag>'s base
just adopt-base <tag> --pr   # after editing, branch + commit + open a PR
```

It resolves `<tag>` (e.g. `527-0808`, or `latest`) to `jam-studio-base`'s
multi-arch **index** digest via the GitHub packages API. It needs `GITHUB_TOKEN`
with `read:packages` (like `gen-blessed`) and cannot run offline; preview the
resulting set with `just gen-blessed`. Only raise the watermark for a genuinely
breaking base — doing it on a routine bump wrongly evicts still-valid older bases.
Design: [adopt-base-recipe](superpowers/specs/2026-08-09-adopt-base-recipe-design.md).

## Git / pushing

`origin` is `https://github.com/aethons-tools/cove.git`.
SSH git is impossible here — the egress lock drops port 22 —
so the hardening layer rewrites GitHub remotes to HTTPS,
and in a Jam studio git authenticates through Jam's broker
([connector.md](usage/jam/connector.md)).

`main` is protected: changes land through a PR whose **`gate`** check passes.
Direct pushes to `main` are rejected, including for admins.
Inside a *sandbox* the story is different —
the hardening rewrites GitHub remotes to HTTPS and git authenticates through Jam's broker ([connector.md](usage/jam/connector.md)).
