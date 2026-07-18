# Discrete `at-cove install` — compile the kit once, run it many times — Design

**Date:** 2026-07-18
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove`
**Epic:** [COV-38](https://linear.app/aethons-tools/issue/COV-38)
**Builds on:** [COV-34](https://linear.app/aethons-tools/issue/COV-34)/[COV-41](https://linear.app/aethons-tools/issue/COV-41) (the base resolution + provenance gate, which this relocates) and the `assemble` / `backend` / `state` / `runner` split.

## 1. Purpose

Today `at-cove build` only *assembles* the build context (`kitDir/.build`); `create`/`recreate`/`work`/`dispatch` each run `docker build` **inline** on every invocation (relying on the layer cache), each re-resolving and re-gating the base. There is no discrete "build the hardened image once and reuse it" step, and COV-34's provenance gate + `--allow-unverified-base` are scattered across every build-triggering command as an interim (the flag's own doc comment already promises this consolidation).

This design introduces **`at-cove install`**: the single operation that *compiles* a kit into a runnable artifact — validate `config.yml`, resolve + **gate** the base, assemble the context, `docker build` + tag — and freezes the result into a **`install.json`** manifest. Every other command reads `install.json`, never `config.yml`, verifies the install is still current, and **consumes the pre-built image**. The gate and `--allow-unverified-base` live only on `install`, the one place a base is actually built.

**Requirements this must meet:**
- One explicit, cacheable, gated build step; run commands consume its output instead of rebuilding.
- The provenance gate + `--allow-unverified-base` apply *only* where the image is built (`install`).
- Run commands operate on a fully-resolved manifest, so they cannot diverge in how they interpret the kit.
- Preserve the pure plan/execute split (the `runner` seam) — the new logic is testable without Docker or a VM.
- Do not weaken the hardening/egress threat model.

## 2. Principles

- **`config.yml` is source; `install.json` is the compiled artifact.** `install` is the *only* reader of `config.yml` at the kit level; all run commands read `install.json`.
- **One build path.** `docker build` happens only in `install` (via the backend). `build` is retired.
- **Strict currency, ready for auto.** A run command with a missing or stale `install.json` fails with "run `at-cove install`". The currency check is a pure function; a future `install --auto` / opt-in "build if stale" is a thin addition that calls the same check — deliberately not built now (YAGNI).
- **Gate at the boundary.** The base is resolved + verified exactly once, at `install`. Consumers trust the frozen result.

## 3. The lifecycle

```
config.yml  ──`at-cove install`──►  install.json  ──►  create / recreate / chat / work / dispatch
(source)      compile:                (resolved            verify currency, then consume the
              - validate config        manifest +          pre-built image (never read config.yml)
              - resolve + GATE base     built image)
              - assemble .build
              - docker build + tag
              - write install.json
```

- `install.json` is the **runtime contract**. `state.json` is unchanged — it still records the *live instance* (backend/container handles), written by `create` after the container starts.
- Any edit to `config.yml` (or the kit's `image/` tree, or an at-cove upgrade, or the configured base ref) changes the currency hash → the next run command reports the install stale → re-`install`.

## 4. `install.json` — the manifest

A new package `internal/install` owns the manifest (mirroring `internal/state`): schema, compile, currency, read/write. The file lives at `kitDir/.state/install.json` (gitignored, alongside `state.json` — it is machine-generated and host-specific).

Contents:
- `schemaVersion`, `name`, `installedAt`.
- `image` — the built, tagged image ref (the stable `at-cove-for-<kit>` identity, as today; `destroy` already `docker rmi`s it). Currency lives in the manifest, not the tag string.
- `baseRef` — the base as configured (or the blessed default); `baseDigest` — the digest it resolved to.
- `currencyHash` — sha256 over the **build-affecting inputs** (§5).
- **Resolved run-config** materialized from `config.yml`: workspace defaults, `collaborators`, `workers`, `tracker`, `source-control`, `dispatch`, secret *demands* (names + resolver argv only — never values), `allowed-domains`. Whatever a run command needs, resolved once.

`internal/install` exposes at least: `Compile(cfg kit.Config, resolved ResolvedBuild) Manifest`, `Load(kitDir) (Manifest, error)`, `Save(kitDir, Manifest) error`, `Exists(kitDir) bool`, and `(Manifest).Stale(current CurrencyInputs) bool`. `Compile` and the currency logic are pure — no I/O, no docker — so they unit-test with plain structs.

## 5. Currency

`currencyHash = sha256( kitSourceTree ‖ atCoveBuildIdentity ‖ baseRef )` where:
- **`kitSourceTree`** — a deterministic hash of the kit inputs that feed the build: `config.yml` + everything under the kit's `image/` (Dockerfile + image-files).
- **`atCoveBuildIdentity`** — a hash of at-cove's embedded contributions (the sealed hardening FS + the embedded `at-task` binaries), so an at-cove upgrade that changes the sealed layer invalidates every install.
- **`baseRef`** — the configured base string (or the blessed default ref).

A run command recomputes `currencyHash` from the **current** kit source + its own embedded identity + the configured base ref, and compares to `install.json`. This is:
- **Cheap + offline** — file hashing only; no docker, no registry.
- **Non-mutating** — it hashes *source*, never re-assembles `.build`, so concurrent `work` units under `dispatch` don't race on the build dir.

**Deliberate boundary:** a **moving base tag** whose digest drifts upstream is *not* detected — `install` froze `baseDigest`; re-install to pick up a newer base. Digest-pinned bases (the default and the recommended kit setting) are exact. This is stated, not hidden.

## 6. Command surface & backend seam

**Backend refactor** — split "build the image" from "run a container". Today `Backend.Create` does resolve-gate + `docker build` + `docker run`, and `DispatchOps.BuildImage` does resolve-gate + build. After:

- **`Backend.Install(InstallContext) (InstalledImage{Ref, BaseDigest}, error)`** — the only build path: `resolveBase` (the gate) + `docker build --build-arg BASE=… -t <tag> <buildDir>` + tag. Owns the gate + `AllowUnverified`.
- **`Backend.Create`** — consumes a pre-built image ref: `docker run` only. No build, no gate.
- **`work`/`dispatch`** — `RunEphemeral(<installed image>)`; `BuildImage` leaves the hot path entirely. Dispatch's units consume the one warm image (no per-unit build, no per-unit gate).

**Commands:**

| command | today | after |
|---|---|---|
| `build` | assemble `.build` only | **retired** (`install --dry-run` covers "assemble + inspect") |
| `install` | — | assemble + gate + `docker build` + tag + write `install.json`; **owns `--allow-unverified-base`** |
| `create` / `recreate` | assemble + build + run | verify currency → **run** the installed image; write `state.json` |
| `chat` / `work` / `dispatch` | read `config.yml` | read `install.json`; verify currency; consume |

`state.State.Image` continues to reference the running image, now sourced from `install.json`. `--allow-unverified-base` is removed from `create`/`recreate`/`work` and added to `install`.

## 7. Testing

- **Hermetic + pure:** `internal/install` (`Compile`, `CurrencyHash`, `Stale`, read/write) unit-tests over plain structs + a temp dir — no docker, no network.
- **Runner seam preserved:** `Backend.Install` / the run-only `Create` drive `runner.Fake`; assert the recorded `docker build` / `docker run` argv (as `colima_test.go` does today). The gate's own tests (`internal/baseimage`) stand.
- **Currency behavior:** unit tests for "unchanged kit → current", "edited config → stale", "at-cove identity bump → stale", "base ref change → stale".

## 8. Non-goals / deferred

- **`install --auto` / build-if-stale** on run commands — architected for, not built (§2).
- **Registry drift detection** for moving-tag bases (§5).
- Changing the gate's runtime logic (COV-41 stands) or the hardening/egress model.
- Multi-instance install (one compiled manifest per kit, as today's single `at-cove-for-<kit>` identity).

## 9. Decomposition (sub-issues under COV-38)

Strict order via `blockedBy`; all `class:implementor` (autonomous). Each lands as one PR against `main` passing `gate`, with docs updated in the same change.

- **S1 · `internal/install` package** — manifest schema + `Compile` + `CurrencyHash` + `Stale` + read/write. Pure, hermetic, TDD. No command wiring yet.
- **S2 · `at-cove install` + `Backend.Install`** — new command; the build+gate+tag backend op; relocate the gate + `--allow-unverified-base` onto `install`; write `install.json`; **retire `build`**. *Blocked by S1.*
- **S3 · migrate `create` / `recreate` / `chat`** — `Backend.Create` becomes run-only; strict currency check; read run-config from `install.json`. *Blocked by S2.*
- **S4 · migrate `work` / `dispatch`** — consume the installed image via `RunEphemeral`; drop inline `BuildImage`; strict currency. *Blocked by S3.*
- **S5 · docs sweep** — OVERVIEW command surface + DEVELOPMENT; confirm no doc describes `build` / inline build / the scattered flag. *Blocked by S4.*

S1→S2 are the foundation; S3 and S4 are logically independent migrations but chained here for a clean, reviewable sequence per the epic's ordering requirement.
