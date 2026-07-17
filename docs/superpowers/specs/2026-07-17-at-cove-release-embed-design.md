# at-cove release pipeline + at-task embed — Design

**Date:** 2026-07-17
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-task`, `at-mint`)
**Tracks:** [COV-36](https://linear.app/aethons-tools/issue/COV-36).
**Builds on:** the published image tree from [COV-33](https://linear.app/aethons-tools/issue/COV-33) (its `cove-base-image` digest seeds the blessed list); the existing `go:embed` pattern in `internal/assemble/embed.go` (hardening + overridable trees).
**Unblocks:** [COV-34](https://linear.app/aethons-tools/issue/COV-34) — supplies both the embedded at-task it injects and the blessed digest its provenance gate needs.

## 1. Purpose

at-cove has to be installable to run sandboxes, so it needs a release pipeline. And at-task — the dispatched-worker harness — should reach the hardening layer by being **embedded in at-cove** rather than baked into the image tree: that guarantees version lockstep (the at-cove running `work` embeds the exact at-task it expects, injected into the hardening layer it builds), makes at-task a hardening concern consumers never maintain, and collapses the image bootstrap (at-task leaves the image tree entirely). The same embed mechanism carries the blessed `cove-base-image` digest that [COV-34](https://linear.app/aethons-tools/issue/COV-34)'s provenance gate checks against.

## 2. Governing decisions

- **Distribution: internal-first, public-ready.** goreleaser publishes to **private GitHub Releases** on a git tag; consumers with repo access download with a token / `gh release download`. The config is structured so adding a public surface later (a public releases repo and/or a Homebrew tap) is a goreleaser-config addition, not a rework.
- **Host binaries shipped: `at-cove` + `at-mint`,** darwin/linux × amd64/arm64, lockstep-versioned (one repo, one tag). `at-task` is **not** shipped standalone — it is embedded in at-cove.
  - *Public phase (later):* bundle at-mint **with** at-cove — one release archive, one Homebrew formula installing both binaries (`bin.install "at-cove", "at-mint"`), so `brew install at-cove` yields both. No separate formula / dependency edge.
- **at-task is embedded via `go:embed`.** The release builds the two **linux** at-task binaries (amd64 + arm64), stages them where `go:embed` picks them up, then builds at-cove. at-cove selects the right one by target VM arch when assembling the hardening build context.
- **The blessed digest is committed in-repo, embedded from there.** A tracked file holds the blessed `cove-base-image` digest(s), `go:embed`'d into at-cove. It is a **list** — the rolling set is free (resolves COV-34's cardinality open decision). Builds are reproducible from source and need no registry access.
- **Trigger: a git tag** cuts a release; `main.version` is stamped from the tag for all binaries.

## 3. The at-task embed

- New staging dir under `internal/assemble/` (e.g. `attask/`) holding `at-task-linux-amd64` / `at-task-linux-arm64`, gitignored (build artifacts), with an `embed.go` (`//go:embed all:attask`).
- **Build order (the one real ordering constraint):** compile the two linux at-task binaries → stage → build at-cove. The release workflow enforces this; `build.sh` gains the same pre-step for local builds.
- at-cove exposes the embedded at-task bytes for the target VM arch; COV-34 writes them into the hardening build context and the hardening `Dockerfile` COPYs them. (The *injection* is COV-34; COV-36 only makes at-cove *carry* the binaries.)

## 4. The blessed digest

- A tracked file (e.g. `internal/assemble/blessed-digests.txt`, one `sha256:…` per line) `go:embed`'d into at-cove; parsed into the set the provenance gate consumes.
- **Seeded** from COV-33's current publish (`sha256:de2c3c0165a994d7ba7c3896be4af15f284f3c5c2d2c60d920338dc4c9302caf`).
- **Kept current:** when `build-images.yml` publishes a new `cove-base-image` on main, it appends the new digest to the file via an automated bot PR (so the file, and thus at-cove, always trusts the latest — and, as a list, recent prior — publishes). *Open detail:* exact bot mechanism and whether/when old digests are pruned (§7).

## 5. Release pipeline

- **`.goreleaser.yaml`** — `builds` for at-cove + at-mint (darwin/linux × amd64/arm64, `CGO_ENABLED=0`, `-trimpath`, `-s -w -X main.version={{.Version}}`); `archives` per binary (or a combined archive for the brew phase); `release` → GitHub Releases (private); checksums. A `brews` block is added in the public phase.
- **`.github/workflows/release.yml`** — on tag push (`v*`): compile linux at-task → stage for embed → run goreleaser (which builds the host binaries embedding at-task + the committed blessed digest, and publishes the private release). `contents: write` for the release; `GITHUB_TOKEN`.
- **`scripts/build.sh`** — gains the at-task-linux pre-build + stage so local `just build` embeds at-task too.

## 6. Removing at-task from the image tree — deferred to COV-34

The epic listed "remove at-task from the image tree" under COV-36, but it must land **together with** COV-34 rewiring the hardening layer to inject the embedded at-task — otherwise there is a window where a sandbox has no at-task (hardening `FROM` a base that no longer carries it, and not yet injecting it). So the removal (`build-images.yml` drops the at-task compile/stage; `cove-base-image/Dockerfile` drops the `COPY at-task`; `cove-base-image` becomes pure tools) moves to **COV-34**, landing with the injection. COV-36 stops at making at-cove carry at-task.

## 7. Open decisions to resolve in implementation

1. **Blessed-digest currency mechanism** — how `build-images.yml` appends a new digest to the committed file (bot PR), and the pruning policy for old digests (§4).
2. **Staging layout** — exact embed dir/filenames and how `build.sh` + the release workflow share the pre-build step.
3. **at-mint today** — confirm at-mint is a plain host binary the release ships (no behavior change); only its *packaging* changes.

## 8. Testing

- **Hermetic (unit):** at-cove exposes the correct embedded at-task bytes per target arch (fixture binaries); the blessed-digest file parses into the expected set; version stamping. Drive `internal/runner.Fake` — no real release.
- **The release workflow** is exercised by cutting a pre-release tag on a branch and inspecting the draft/private release artifacts (integration-style, watched in Actions — can't run goreleaser in the egress-locked dev sandbox).
- **Embed round-trip:** a test that the embedded at-task actually runs (`at-task version`) for the host-matching arch, where feasible.

## 9. Scope and sequencing

Depends on COV-33 (published base — done). **Must land before COV-34**, which consumes the embedded at-task + blessed digest. Decomposes into: the embed staging + `go:embed` + build.sh pre-step; the committed blessed-digest file + embed + the publish-time bump; the goreleaser config + release workflow. The public/brew surface is an explicit later increment, not this one.

Non-goals: public distribution (deferred); removing at-task from the image tree (moves to COV-34); folding at-mint into at-cove (bundle-in-one-formula suffices).
