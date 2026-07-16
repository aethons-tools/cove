# Shared base image (`cove-base-image` / `cove-image`) — Design

**Date:** 2026-07-16
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-task`)
**Builds on:** the current two-layer image model — `images/Dockerfile` `base` stage (built to `cove-base` by `scripts/build-images.sh`) + `internal/assemble/hardening/Dockerfile` (`FROM cove-base:latest`); and the merge gate landed this session (`.github/workflows/gate.yml`, `scripts/lint.sh`).

## 1. Purpose

Today's tool set is defined in three drifting places: `images/Dockerfile`'s `base` stage (the sandbox tool floor), the `agent` stage (Claude + startup, **never built** — dead code that duplicates the hardening layer), and CI (which `apt-get install`s `shellcheck`/`hadolint` on every run because they aren't in the base). `cove-base` is built locally, published nowhere, and must pre-exist on every build host as an undocumented prerequisite. Nothing guarantees the agent's environment and CI's environment agree.

This design makes **one published, pinned, multi-arch image tree the single source of truth** for the utility set, consumed identically by CI job containers and by the agent sandboxes at-cove hardens. Equivalence stops being something to maintain and becomes structural: CI and agents build from the same image, so "it passed CI" and "it works in the sandbox" cannot diverge on tooling.

## 2. Governing decisions

- **Host the image definitions in *this* repo** (monorepo), not in separate image repos. This is the pivotal choice. It lets CI compile `at-task` from source and bake it into `cove-base-image`, so the hardening layer stops shipping `at-task` at all. A separate image repo could not do this without inverting the dependency (the base would depend on cove). The cost is a CI **bootstrap** cycle borne only by this repo (§5).
- **Two images, layered:**
  - **`cove-base-image`** — the universal lean floor any org repo can build on. OS + `git`, `gh`, `sshd`, the egress stack (`nftables`, `squid`, `podman` + rootless plumbing), core utilities, and **`at-task`** (the generic dispatched-worker harness, built from this repo). Deliberately **excludes** `chrome` and `java`.
  - **`cove-image`** — `FROM cove-base-image` + the full build/test/run toolchain: `go`, `just`, `shellcheck`, `hadolint`, `node`, `chrome`, `java`, and anything else CI or an agent needs. It is the base for **both** cove's CI job containers **and** the agent sandboxes, so an agent can build/test/run anything CI does.
- **Publish to GHCR**, private: `ghcr.io/aethons-tools/cove-base-image` and `ghcr.io/aethons-tools/cove-image`. Auth via `GITHUB_TOKEN`; no new registry account or secret.
- **Reproducible by construction.** Every input is pinned: `FROM …@sha256:<digest>`, apt packages as `pkg=version`, tool versions as build args. Each build publishes an **immutable** tag (date + short SHA) and consumers pin by **`@sha256` digest**; `latest` may also move for convenience but is never what a consumer pins.
- **Multi-arch, native per arch.** `amd64` on `ubuntu-latest`, `arm64` on `ubuntu-24.04-arm`, merged into one manifest. Both are required: CI runs amd64, the sandbox VMs are arm64.
- **Currency via Renovate, not a schedule.** Under full pinning a scheduled rebuild reproduces the same image, so it buys nothing. Renovate watches the `FROM` digests, the apt `pkg=version` pins (via a custom manager — Dependabot does not track these), and the tool-version args, and opens pin-bump PRs. Merging a bump is the push that rebuilds and republishes.

## 3. Image hierarchy and consumers

```
cove-base-image        ← universal lean floor for ALL org repos
  • OS + git, gh, sshd, egress stack (nftables/squid/podman + rootless), core utils
  • at-task            (built from THIS repo's source, baked in)
  • NO chrome, NO java
        │
        └── cove-image ← FROM cove-base-image + full toolchain
              • go, just, shellcheck, hadolint, node, chrome, java, …
              • base for BOTH cove CI job containers AND agent sandboxes
```

| Consumer | Uses | How |
|---|---|---|
| Other org repos | `cove-base-image` | directly as their job container, or as the base for their own `<repo>-image` |
| cove CI (the gate) | `cove-image` | `container:` pinned by digest (after the bootstrap builds it) |
| Agents (worker/collaborator) | `cove-image` | at-cove hardens it: `FROM cove-image` + Claude + plugins + sealed hardening + kit; `at-task` already present |

**`at-task`'s new home.** It moves *down* into `cove-base-image` (was: baked into the local `base` stage and, at runtime, into the hardening layer). It belongs there because it is the generic worker bracket every dispatched sandbox runs, for any target repo — not a cove-specific tool. The hardening layer no longer adds it.

## 4. What changes in the existing layers

- **`images/Dockerfile`** is split into the two image definitions and its **dead `agent` stage is deleted** (lines ~44–128 today; never built, fully duplicated by the hardening layer).
- **`internal/assemble/hardening/Dockerfile`** rebases from `cove-base:latest` onto the pinned `cove-image` digest and drops whatever `cove-image` now provides (`at-task`; any tool it duplicated). It keeps only what is genuinely the security/startup boundary: Claude install, plugin seed, sealed egress/sshd config, entrypoint, env, kit customization.
- **`.github/workflows/gate.yml`** runs the gate inside `container: cove-image@<digest>`, dropping the linter-install step and the `STRICT=1` dance (the linters are in the image). `scripts/lint.sh` keeps its skip-if-absent behavior for local clones but no longer needs `STRICT` in CI.
- **`scripts/build-images.sh`** stops being the source of truth for the base and becomes (or is replaced by) the local-build convenience wrapper over the new definitions.
- **Docs:** `cove-base`/`cove-image` and the bootstrap are currently undocumented; `docs/OVERVIEW.md` (build/image model) and `docs/DEVELOPMENT.md` (CI) gain the image tree, the registry/pinning contract, and the bootstrap sequence.

## 5. The bootstrap (cove's self-hosting cycle)

cove cannot run its CI *inside* the image its CI is building. Its pipeline breaks the cycle in order:

```
1. stock golang image (e.g. golang:<pinned>)   → compile the Go code (at-task, cove binaries)
2. build cove-base-image                        → bake in the just-built at-task
3. build cove-image                             → FROM cove-base-image + toolchain
4. run the gate INSIDE cove-image               → go test ./… + scripts/lint.sh
```

A **stock** golang image is used for step 1 deliberately: it needs nothing pre-existing, so the cycle is broken with zero prerequisites. Other org repos never do steps 1–3 — they consume `cove-base-image` directly. That asymmetry is the accepted inconvenience of hosting the definitions here.

**Open sequencing question for the plan:** whether publishing (push to GHCR) happens on every `main` build, or the gate builds-and-tests the images without publishing on PRs and only publishes on merge to `main`. Default assumption: PRs build + smoke-test the images but do **not** publish; `main` publishes. Resolve in the implementation plan.

## 6. Testing / verification

- **Image smoke test** (in the build pipeline, before publish): the built image runs and the expected tools resolve — e.g. `go version`, `just --version`, `shellcheck --version`, `hadolint --version`, `at-task version`, `node --version`, and (for `cove-image`) `chrome`/`java` presence. A bad pin or a missing tool fails here, not in a downstream consumer.
- **Reproducibility check:** with all inputs pinned, two builds of the same commit produce the same package set; the smoke test asserts the pinned versions are the ones installed.
- **Consumer equivalence (the point):** once `cove-image` backs both the gate and the sandbox, `just test` / `scripts/lint.sh` behave identically in CI and in an agent session by construction — no cross-environment drift to test for.
- Existing `just test` stays hermetic and unaffected.

## 7. Scope and sequencing

One coherent architecture, implemented as sequential PRs (each its own board issue via board-plan):

- **A — Image tree + bootstrap CI (this repo).** Split `images/Dockerfile` into `cove-base-image` + `cove-image`, pin everything, wire the multi-arch native build + manifest + smoke test + GHCR publish, add Renovate. Deletes the dead `agent` stage. Produces the published, pinned digests every downstream step binds to.
- **B — at-cove consumes `cove-image`.** Rebase the hardening Dockerfile onto the pinned `cove-image` digest; drop `at-task` and any duplicated tool install from hardening; retire `scripts/build-images.sh` as the source of truth; document the model. Blocked by A's published interface (image names + digest scheme).
- **C — Gate runs in `cove-image`.** Switch `gate.yml` to `container:` the pinned digest; drop the linter-install/`STRICT` dance; simplify `scripts/lint.sh`'s CI path. Blocked by A. May fold into A's bootstrap work since both live in `gate.yml`.

Non-goals: changing the egress/sshd hardening threat model; changing what packages the sandbox ships beyond the `chrome`/`java` placement above and the `hadolint` addition; a public build repo.
