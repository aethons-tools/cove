# Monolithic continuous-delivery release pipeline + registry-sourced blessing — Design

**Date:** 2026-07-17
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (artifacts: `cove-base-image`, `cove-image`, `at-cove`, `at-task`)
**Supersedes:** the split of [COV-33](https://linear.app/aethons-tools/issue/COV-33) (`build-images.yml`, publishes images on `main`) + [COV-36](https://linear.app/aethons-tools/issue/COV-36) (goreleaser, releases at-cove on tags), and **obviates** [COV-37](https://linear.app/aethons-tools/issue/COV-37) (auto-append digest — the feedback loop this design removes).
**Builds on:** [COV-41](https://linear.app/aethons-tools/issue/COV-41) (the provenance gate) — its runtime behavior is unchanged; only *how the blessed list is populated* changes.

## 1. Purpose

The provenance gate ([COV-34](https://linear.app/aethons-tools/issue/COV-34)) is worth keeping, but the way at-cove learns which `cove-base-image`s are blessed has a chicken-and-egg flaw. Today the blessed digests are a **committed file** (`internal/basedigest/blessed-digests.txt`) embedded into at-cove. A new `cove-base-image` digest only exists *after* it is published — from this same repo's `main` — so keeping the file current requires publishing, then a **commit back** to update the file, then rebuilding at-cove. [COV-37](https://linear.app/aethons-tools/issue/COV-37) would automate that as a bot PR: an external service pushing a commit to re-trigger the build. That feedback loop is not integral CI, and it leaves windows where `main` is not solidly buildable into a working sandbox.

This design makes CI integral: **one monolithic, continuous pipeline off `main`** that decides what to build from **repo state**, with the **container registry as the source of truth** for what is blessed. at-cove *snapshots* that truth into its binary at build time, so the gate still runs fully **offline** at `create` time — no registry access when a user runs a sandbox.

**Requirements this must meet:**
- `main` is always buildable into a working sandbox — no broken window.
- No commit-back / external-PR feedback loop. The digest coupling is resolved *within* one pipeline run.
- The provenance gate keeps working offline from what at-cove was built with.
- Continuous delivery: every push to `main` can release; no manual "cut a release" gate.

## 2. Principles

- **One pipeline, continuous off `main`.** Every push to `main` runs it. There is no version-tag trigger and no human toggle for which artifact to publish.
- **Repo state decides what builds.** Change-detection (paths in the push's diff) selects which legs of the build run. The pipeline never takes "publish only X" as an input — it *derives* that from what changed.
- **The registry is the source of truth for blessing.** The set of blessed `cove-base-image`s is *computed from the registry* at build time, not hand-maintained in the repo. The only thing the repo commits is a **low-watermark** (§4).
- **at-cove gates offline.** The computed set is embedded into at-cove at build time (`go:embed`), so `create`/`work` never need the registry to know what is trusted — only to *pull* the base, as they already do.

## 3. The build DAG and change-detection

The dependency graph has **no cycle**:

```
 at-task (Go) ─────────────┐
                           ▼
 cove-base-image ── publish ──► digest D ──► registry ──► blessed list (D newest)
       │                                                        │
       ▼                                                        ▼
 cove-image (FROM cove-base-image@D)                 at-cove (embeds at-task + list; DefaultRef = D)
                                                                │
                                                             publish
```

1. `at-task` (Go) and `cove-base-image` build independently. **`cove-base-image` sheds at-task** — it becomes pure tools (OS + git/gh/sshd + egress stack + core utils). at-task is a hardening concern injected by at-cove (COV-36/COV-42), so the base no longer compiles or bakes it; the base has no Go build dependency.
2. Publish `cove-base-image` → immutable digest **D**.
3. Compute the blessed list from the registry (§4), newest-first, **D** at the head.
4. Build `at-cove` embedding the `at-task` binaries + the generated list. `DefaultRef` = the head (**D** when the base was just rebuilt, else the current registry head).
5. Publish `at-cove` (+ `at-task` assets); build/publish `cove-image` = `FROM cove-base-image@D`.

**Change-detection** (path-based, from the push diff) prunes legs:
- `images/cove-base-image/**` or its pins changed → full chain (rebuild base → recompute list → rebuild at-cove; rebuild cove-image).
- Only `cmd/**` / `internal/**` (Go) changed → **no base republish**; recompute the list from the *current* registry head and re-cut at-cove + at-task.
- Only `images/cove-image/**` changed → rebuild just cove-image.
- Docs-only / unrelated → nothing published.

Each artifact carries its own last-built version (§5); they need not be rebuilt in lockstep. at-cove references the base by **digest**, never by version, so divergent version tags across artifacts are irrelevant to the gate.

## 4. Blessing: the low-watermark + the registry

The repo commits exactly **one** digest — the **low-watermark**: the oldest `cove-base-image` still considered blessed. It lives where `blessed-digests.txt` is today (`internal/basedigest/`), now holding a single meaningful entry.

**Building the blessed list (a CI step, before `go build`):**
1. List `cove-base-image` versions from the registry (GHCR packages API), ordered newest→oldest by publish time.
2. Walk from newest downward, collecting digests, **until and including** the low-watermark; stop there.
3. Write the collected digests (newest-first) into the workspace as the file `go:embed` reads. The head is `DefaultRef`.
4. If the watermark digest is **not found** in the registry listing → **fail the build loudly** (a pruned or wrong watermark must never silently shrink the trust set).

This gives:
- **Steady state (a base republish for pins etc.):** the new digest appears in the registry; the next at-cove build simply includes it — **no commit, no loop**. The watermark does not move.
- **A breaking base change:** a human bumps the watermark to the first post-break digest in one deliberate commit. Everything older than the watermark drops out of the blessed set — the mechanism for "these changes require a newer base." Rare and explicit; never per-publish churn.
- **Offline / local dev:** with no registry access the CI step does not run, so `go build` embeds the **committed watermark file as-is** — the blessed set degrades to just the watermark (and `DefaultRef` = the watermark). Safe and buildable offline; a dev hardening a newer base uses `--allow-unverified-base`.

The gate logic itself (`internal/baseimage`, the `diff_ids`-prefix `Verify`) is unchanged — it still checks descent from any entry in the embedded list. Only the list's *provenance* changes: registry-computed, not hand-committed.

## 5. Versioning

Continuous, monotonic, deliberately **not** SemVer:

```
<N>-<MM>-<DD>          e.g. 1873-07-17
```

- **`<N>` is the version** — `git rev-list --count HEAD` on `main`: globally monotonic, deterministic, reproducible even locally (no CI state needed).
- **`<MM>-<DD>`** (month-day) is **advisory** — a quick human reference. `<YYYY>` is omitted (trivially inferrable) to keep the string short.
- **`-` separators** (not `.`) so the string is never mistaken for SemVer.

Every published artifact (`cove-base-image`, `cove-image`, `at-cove`, `at-task`) is tagged with the `<N>-<MM>-<DD>` of the commit that built it, plus a moving `latest`. Because change-detection builds only what changed, artifacts' `<N>` values may differ — each reflects when that artifact was last built. This is intended.

## 6. What changes from today

- **`build-images.yml`** (publish images on `main`) and the **goreleaser** at-cove release (on tags) **merge into one workflow** triggered on push to `main`, with the change-detection + DAG of §3.
- **`cove-base-image` sheds at-task**: drop the at-task compile + `COPY at-task` from the base build and its smoke test; at-cove injects the version-locked at-task (COV-42).
- **`internal/basedigest/blessed-digests.txt`** becomes the **single-line low-watermark**; the embedded list is generated at build time. `Blessed()`/`DefaultRef()`/`BlessedRefs()` keep their signatures — they read the generated file.
- **[COV-37](https://linear.app/aethons-tools/issue/COV-37) is obviated** (no digest to append; the registry is the source). Close it as superseded.
- **Versioning** moves to `<N>-<MM><DD>` for all artifacts.

## 7. Relationship to COV-34 / the COV-42 one-off

COV-34's runtime design is untouched. [COV-42](https://linear.app/aethons-tools/issue/COV-42) (the hardening rewire + base furniture move) still lands via its **manual one-off bless** — publish the furniture-bearing `cove-base-image` once, hand-set the digest — to keep the COV-34 series moving. **This pipeline supersedes that mechanism afterward:** once it exists, the manual bless, the `blessed-digests.txt` handoff comment, and any per-publish digest maintenance go away, replaced by the low-watermark + registry snapshot.

## 8. Open / deferred details

- **Registry enumeration API + auth:** GHCR packages REST API with `GITHUB_TOKEN`; confirm ordering (publish time vs. the monotonic tag) and pagination.
- **Uniform re-tagging:** whether an unchanged artifact is re-tagged to the new `<N>` for a uniform "release N" view, or simply retains its prior tag (leaning: retain — build only what changed).
- **Reproducibility:** the embedded list is "as of build time," so rebuilding an old commit may bless newer bases too — harmless for an allowlist, accepted.
- **Pruning/retention:** if old `cove-base-image` tags are ever GC'd below the watermark, the build fails (§4) — decide a retention policy or a watermark-bump discipline.

## 9. Testing

- **Hermetic:** the list-generation step is a pure function over a *fixture* registry listing + a watermark (walk-to-watermark, newest-first, fail-on-missing-watermark) — unit-tested with no network. The offline fallback (watermark-only) is a unit test.
- **Gate unchanged:** `internal/baseimage` tests (COV-41) stand.
- **Pipeline:** validated by real runs on `main` (multi-arch build, GHCR publish, the change-detection matrix) — a CI concern, exercised in the loop like COV-33.

## 10. Non-goals

- Changing the provenance gate's runtime logic (COV-41 stands).
- SemVer or human-cut releases (deliberately continuous).
- Reproducible-to-the-byte blessed lists (registry-time snapshot accepted).
