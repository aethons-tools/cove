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
- The gate runs on the **plain runner** and installs the linters per-run — it is
  deliberately *not* run inside `cove-image` (COV-35). A container gate would
  inherit the image's sandbox-tuned `GOPROXY=direct`/`GOSUMDB=off` (see above) and
  silently stop verifying module checksums, and would add a digest-pin to chase.
  Instead the drift the container would have solved is handled directly: the
  `hadolint` version is pinned in **three** places — this gate, `cove-image`'s
  Dockerfile, and [`scripts/setup-test-tools.sh`](../scripts/setup-test-tools.sh)
  (local dev) — and one Renovate `customManager` ([`renovate.json`](../renovate.json))
  watches all three, so a bump lands in a single PR and image/CI/local never
  diverge. (`shellcheck` comes from the distro `apt` in the gate + setup script;
  it is not version-pinned there.)
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
- `scripts/lint.sh` runs `shellcheck` on `scripts/*.sh` and the sealed hardening
  helpers under `internal/assemble/hardening/image-files/usr/local/{bin,lib/cove}/`
  — so a sealed helper delivered into a sandbox (e.g. `apply-session-domains.sh`)
  is gated, not just the host scripts.

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

**Build & publish** — the images are built and published by the monolithic
pipeline (below); consumers **pin by `@sha256` digest**, never a moving tag
(COV-34/COV-35). Both arches build **natively** (amd64 on `ubuntu-latest`, arm64
on `ubuntu-24.04-arm`) and smoke-test that every tool resolves **before** any
publish. Published (private) to `ghcr.io/aethons-tools/cove-base-image` and
`…/cove-image`.

## CI — the release pipeline

[`.github/workflows/release.yml`](../.github/workflows/release.yml) is **one
monolithic continuous-delivery pipeline** (COV-44, design:
[monolithic-release-pipeline](superpowers/specs/2026-07-17-monolithic-release-pipeline-design.md)).
It runs on every push to `main` and on every PR, and **decides from the repo diff
which artifacts to (re)build** — there is no version-tag trigger and no manual
"cut a release" step.

- **Change-detection** (path-based) selects the legs: `images/cove-base-image/**`
  → rebuild the base **and** cove-image (downstream) **and** re-cut at-cove (its
  new digest becomes the default base + blessed-list head);
  `images/cove-image/**` → rebuild just cove-image (FROM the current published
  base); `cmd/**` · `internal/**` · `go.*` · `.goreleaser.yaml` → re-cut at-cove
  (blessed list recomputed from the registry head). Docs-only → nothing publishes.
- **DAG order (no cycle):** base → publish → digest **D** → [blessed-list
  snapshot, [COV-47](../internal/blessgen)] → at-cove (embeds at-task + list);
  cove-image = `FROM cove-base-image:ci`. The base publishes **before** the
  at-cove leg so `gen-blessed` sees **D** as the registry head.
- **PRs build + smoke the touched legs but never publish** (spec §5); only a push
  to `main` pushes to GHCR / cuts the release. Images publish as multi-arch
  manifests tagged `<N>-<MMDD>` (immutable) + `latest`; at-cove is built by
  `goreleaser --snapshot` (archives + checksums, stamped `<N>-<MMDD>`) and the
  release cut with `gh` (private). See [Versioning](#versioning).
- **at-task is embedded**, not shipped standalone — re-cutting at-cove re-cuts the
  embedded at-task ([`.goreleaser.yaml`](../.goreleaser.yaml) before-hook).

### Versioning

`<N>-<MMDD>` — `<N>` = `git rev-list --count HEAD` is the version (globally
monotonic, reproducible offline); `<MM><DD>` is an advisory month-day; `-` (not
`.`) so it is never mistaken for SemVer. Every published artifact
(`cove-base-image`, `cove-image`, `at-cove`) is tagged `<N>-<MMDD>` + a moving
`latest`. Because only what changed is rebuilt, artifacts' `<N>` values may
differ — each reflects when it was last built. at-cove references the base by
**digest**, never version, so divergent tags don't affect the gate.

[`scripts/build.sh`](../scripts/build.sh) builds the host binaries locally (no
publish) for iteration; `scripts/build-images.sh` does the same for the image
tree. A dev `at-cove` run straight from `dist/<os>-<arch>/at-cove` invokes its
**sibling** `dist/<os>-<arch>/at-mint` (version-matched, built by the same
`build.sh`) rather than a PATH `at-mint`, falling back to the bare name on PATH
when no sibling is present.

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
