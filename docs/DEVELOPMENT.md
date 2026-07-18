# Development notes

Operational notes for building and testing `at-cove`,
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
- **The default `GOPATH` (`~/go` → `/home/agent/go`) works.** `cove-image` provides
  a writable home with `~/go` pre-created, and bakes `GOROOT`/`GOPATH`/`GOPROXY`/
  `GOSUMDB`/`GOFLAGS` as image `ENV` surfaced into the session via `COVE_SSHENV`
  (see [`OVERVIEW.md`](OVERVIEW.md#the-image-tree)). (Older sandboxes redirected
  `GOPATH` to `/home/agent/workspace/.gopath` because `~` was not writable; that
  override is now unnecessary — harmless if a stale `settings.json` still sets it.)

These are already exported in this environment (via `COVE_SSHENV`), and `go` is on
`PATH`. If you need to set them inline (e.g. a non-session shell that didn't read
`/etc/environment`), prefix the command:

```bash
GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod go test ./...
```

## Tests

- `just test` (`go test ./...`) is **hermetic** —
  every test drives `internal/runner.Fake`,
  so no Docker, network, or live VM is required.
  Keep new tests this way.
- `just integration` (`go test -tags integration ./internal/connect/ -v`) runs the **real-ssh** suite:
  it boots an unprivileged throwaway `sshd` on loopback with a fake `claude`
  and exercises the transports and TOFU end-to-end with the real `ssh` client.
  Needs `ssh`/`sshd`/`ssh-keygen` but **not** Docker.
- `go test -tags integration ./internal/baseimage/` proves the provenance gate against
  **real docker**: it builds a base, a descendant, and an unrelated image and asserts the
  `diff_id`-prefix `DescendsFrom` check matches OCI reality. Needs Docker + network (pulls alpine).
- `just setup` installs the optional dev tooling (podman + a `docker` shim, shellcheck, hadolint, jq).
- The remaining untested gap is a full `create`→container→`connect` against a real image,
  which needs a container runtime;
  podman covers the SSH/lifecycle mechanics,
  but the in-container `nftables`/Squid egress lockdown under rootless is the part to watch.

## CI — the merge gate

[`.github/workflows/gate.yml`](../.github/workflows/gate.yml) runs on every PR
to `main`, and on `main` itself. It runs the two hermetic checks —
`go test ./...` and [`scripts/lint.sh`](../scripts/lint.sh) — the same ones
`just test` and `just lint` run, through the same script, so CI and the local
loop cannot drift.

- The job is named **`gate`**, and that name is what branch protection lists as
  the required check. Renaming the job silently un-gates `main`: protection goes
  on waiting for a check that never reports. Rename only alongside the setting.
- CI runs lint as `STRICT=1 scripts/lint.sh`. Outside CI a missing `shellcheck`
  or `hadolint` is skipped with a note, so a fresh clone can lint before
  `just setup`; in CI that default would let the gate pass having linted
  nothing, so `STRICT=1` turns a missing linter into a failure.
- The workflow needs no `just` — the logic lives in `scripts/`, per the
  justfile's header.
- **Not** gated: `just integration` (real-ssh) and `just e2e` (live infra).
- CI leaves `GOPROXY`/`GOSUMDB` at their defaults. The `direct`/`off` settings
  above are a workaround for *this sandbox's* egress lock; a runner has open
  egress and should verify module checksums against `go.sum`.
- `scripts/lint.sh` runs `hadolint` on all three Dockerfiles —
  `images/cove-base-image/Dockerfile`, `images/cove-image/Dockerfile`, and
  `internal/assemble/hardening/Dockerfile`. `.hadolint.yaml` records the rules
  ignored for them, each with its reason. Everything else fails the gate.

## The image tree

The utility set lives in one published, pinned, multi-arch image tree — the
single source of truth shared by CI job containers and the agent sandboxes
at-cove hardens, so their tooling can't drift. See
[the design](superpowers/specs/2026-07-16-shared-base-image-design.md) for the
full rationale.

- **`images/cove-base-image/Dockerfile`** — the universal lean floor: OS +
  git/gh/sshd + the egress stack (nftables/squid/podman) + core utils. **Pure
  tools**: no language toolchains, chrome, or java — and **no `at-task`**. at-cove
  injects the version-locked `at-task` into its sealed hardening layer (COV-42),
  so the base has no Go build and rebuilds only when `images/` changes (COV-44).
- **`images/cove-image/Dockerfile`** — `FROM cove-base-image` + the full
  build/test/run toolchain (go, just, shellcheck, hadolint, node, chrome, java).
  Base for both CI and the sandboxes.

**Reproducible by pinning.** Every input is pinned: the `FROM ubuntu:24.04`
manifest digest, each apt package `pkg=version`, and the toolchain args
(`GO_VERSION`, `JDK_RELEASE`, `NODE_VERSION`, `HADOLINT_VERSION`). Versions are
identical across amd64/arm64, so one pin serves both. A pin that ages out of the
archive fails the build loudly. [`renovate.json`](../renovate.json) keeps them
current — Renovate opens a bump PR per newer version instead of a silent float.
(The one input not yet pinned is chromium, which rides whatever `playwright`
resolves.)

**Build & publish** — [`.github/workflows/build-images.yml`](../.github/workflows/build-images.yml):

- Both arches build **natively** (amd64 on `ubuntu-latest`, arm64 on
  `ubuntu-24.04-arm`) and smoke-test that every tool resolves, **before** any
  publish.
- **Pull requests build + smoke only — they never publish.** Only a push to
  `main` publishes: each arch pushes its smoke-tested images to GHCR under an
  intermediate `sha-<sha>-<arch>` tag, then a `publish` job merges them into
  one multi-arch manifest per image, tagged `<date>-<sha>` (immutable) and
  `latest` (moving). The run summary prints the manifest-list digest.
- Published (private) to `ghcr.io/aethons-tools/cove-base-image` and
  `…/cove-image`. Consumers **pin by `@sha256` digest**, never a moving tag
  (wired up in COV-34/COV-35).
- [`scripts/build-images.sh`](../scripts/build-images.sh) builds the tree
  locally (host arch, no publish) for iteration; it is slated for retirement in
  COV-34.

## Verified `claude` CLI facts

Checked against the `claude` CLI present in this sandbox (v2.1.x):

- `claude auth login --claudeai` is the real subscription-OAuth login command (`--claudeai` is the default).
- `claude auth status` exits `0` when logged in and non-zero otherwise —
  this is what `connect` probes to decide whether a first-session login is needed.

## Git / pushing

`origin` is `https://github.com/aethons-tools/cove.git`.
SSH git is impossible here — the egress lock drops port 22 —
so the hardening layer rewrites GitHub remotes to HTTPS
and a credential helper supplies the token from `GITHUB_TOKEN` in the session env
(see [`OVERVIEW.md`](OVERVIEW.md#authentication) for the mechanism).
A session whose kit does not declare that secret has no token,
and git fails closed rather than prompting;
this repo's kit declares it under `collaborators.<common>.secrets`
(see [at-cove-config.md](usage/at-cove-config.md#collaborators)).

`main` is protected: changes land through a PR whose **`gate`** check passes.
Direct pushes to `main` are rejected, including for admins.
Inside a *sandbox* the story is different —
the hardening rewrites GitHub remotes to HTTPS and feeds a `GITHUB_TOKEN` via a credential helper (see [`OVERVIEW.md`](OVERVIEW.md#authentication)).
