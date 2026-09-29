# jam: the cove Launcher abstraction — a substrate-pluggable, kit-owning launcher

**Status:** design approved (brainstorm complete), pre-plan
**Scope:** factor the managed-cove concern — **kit → build → raise → init → teardown** — behind a `Launcher` abstraction (an internal library), so the managed path can evolve without disturbing `at-cove`, substrates (Colima now; Firecracker/Fly later) diverge behind one seam, and a **managed kit flavor** (locked egress) drops out. Colima is the only implementation built now.
**Builds on:** `internal/backend` (the runtime substrate seam — Colima impl), `internal/jam/launcher` (today's runtime-only managed launcher driven by the `at-jam` supervisor), `internal/assemble` + `internal/install` (kit → image build), `internal/connect` (`LaunchCoveMaster` cove init), and the kit registry (`at-jam kit`, `internal/kit`).
**Does not change:** `at-cove` and its use of `internal/*` (strictly additive — `at-cove` stays load-bearing and cannot regress); the broker/roster/`Decide` model; the hardening layer's sealed egress *mechanism*.

## Why

The managed-cove path (jam-launched personal/standing/requisitioner/pool coves) needs to evolve — its own kit, locked egress, and eventually other substrates — but **`at-cove` is critical to several projects right now** and must not be destabilized. We considered forking a second binary (`at-session`), but the variance we actually want is narrower than a CLI: it's the **launcher's** job — *what kit, how it's built, how the cove is initialized*. Keeping that as an **internal library behind a good abstraction** (not a second CLI to keep in sync) is both lower-risk today and the right long-road shape, because **substrates diverge exactly at the launcher** (Colima → docker/local; Firecracker → microVM/rootfs; Fly → machine image, built remotely).

## The abstraction

A **`Launcher`** owns a cove's whole lifecycle from a **kit**, over one substrate:

```
kit  ──▶  Launcher ──▶ { build (maybe remote) → raise → init → probe/pause/teardown }
```

Two principles, both from the brainstorm:

1. **The caller communicates a *kit*, not a pre-built image.** Today the flow is inverted: `at-cove install` builds an image locally and writes a manifest (`Image`+digest), and the launcher merely *runs* that pre-built image (`RunEphemeral(image, digest, …)`). That only works because build == local docker. **Build is the launcher's responsibility and may run in a remote environment** (Fly builds on Fly; a Firecracker launcher assembles a rootfs on its own infra; Colima builds locally via docker). So the seam is **kit-in**: the caller hands the launcher a kit (by value — config + build context — or by reference — a registry name/version the launcher resolves), and the launcher handles building/preparing it wherever that substrate builds.

2. **The substrate is the launcher's business.** `internal/backend` already abstracts the *runtime* ops (RunEphemeral/Dial/GetStatus/Pause/Unpause/RoleEgress) with Colima as the one impl — new substrates are new `backend` impls. The Launcher extends that seam to also own **build** and **init**, which today live *outside* it (`assemble`+`docker build`; `connect.LaunchCoveMaster`). After this, "add a substrate" = implement the Launcher (build + raise + init) for it; nothing above the seam changes.

### Shape (illustrative, not final — see YAGNI below)

```go
// A Kit is what the caller communicates: either an inline definition (config +
// build context / payload) or a reference the launcher resolves (registry name).
type Kit struct { … }   // by-value or by-reference; the launcher handles it

// Launcher owns a managed cove's lifecycle on one substrate. Build may be local
// (Colima/docker) or remote (Fly/Firecracker) — the caller doesn't know or care.
type Launcher interface {
    // Prepare turns a kit into a raiseable image for this substrate, building it
    // wherever this substrate builds (local or remote). Returns an opaque handle.
    Prepare(ctx, kit Kit) (Prepared, error)
    // Raise stands up + initializes a cove from a Prepared kit (connector env,
    // cove-master, egress) — the managed init recipe.
    Raise(ctx, spec RaiseSpec, creds LaunchCreds) (Instance, error)
    Teardown/Probe/Pause/Unpause(…)   // as today's jam.Launcher
}
```

`Prepare` and `Raise` may collapse for a substrate that builds-and-runs in one step; the point is that **the kit is the input and build-location is hidden**. Init (the connector env + `cove-master`) is largely substrate-agnostic once the substrate yields an SSH `Endpoint` (via `Dial`), so it stays shared.

## Relationship to what exists

- **`internal/backend`** stays the low-level substrate ops; the Colima `Launcher` uses it for raise/dial/status/pause. Unchanged in spirit.
- **`internal/jam/launcher`** (today's `jam.Launcher`: Raise/Teardown/Probe/Pause/Unpause from a pre-built manifest) is the seed of this abstraction — it grows to own **build (from a kit)** and keeps its runtime half. The `at-jam` supervisor keeps driving it (consolidation, not a new consumer).
- **`internal/assemble` + `internal/install`** (kit → build context → manifest) become the Colima launcher's **build** implementation, invoked *through* the Launcher rather than by a separate `at-cove install` step for managed coves.
- **`internal/connect.LaunchCoveMaster`** is the Colima launcher's **init** step (the managed flavor: `ANTHROPIC_AUTH_TOKEN`, `cove-master`, resident).
- **Kit registry** (`internal/kit`, `at-jam kit`) is the natural home for kit-by-reference: a role names a kit; the launcher resolves and prepares it. (Wiring the launcher to resolve a role's registered kit is the deferred slice `kits.md` already names — this abstraction is what makes it land cleanly.)

## The managed kit flavor (COV-208 rides here)

The managed cove's kit **omits `.anthropic.com`/`.claude.com`/`claude.ai`** from egress — a brokered cove reaches Anthropic only through the jam broker. With build+kit owned by the Launcher, this is just the managed kit's config, not sealed-base surgery and not a second binary. Combined with the already-shipped `ANTHROPIC_AUTH_TOKEN` init (#256), a managed cove holds no real credential and can't reach Anthropic directly. **This is the COV-208 deliverable, achieved as a property of the managed launcher's kit.**

## YAGNI / the honest long road

There is **exactly one substrate today (Colima).** The cleanest abstraction is discovered with the *second* implementation, not designed speculatively against imagined Firecracker/Fly shapes. So:

- Design the interface **from Colima's reality**, kept minimal and honest; the *remote build* requirement (this brainstorm's correction) is a first-class constraint on the interface (kit-in, build hidden) but we implement only the **local** build path now.
- Multi-substrate is the **direction the seam is shaped toward**, proven already by `internal/backend`'s pluggability — not code we write now.
- The abstraction earns its keep **immediately** by: consolidating kit+build+init for managed coves, shipping COV-208, and giving the managed path a clean internal library to evolve in without touching `at-cove`.

## Additive & reversible

`at-cove` and every existing `internal/*` caller are untouched — the Launcher is a new *composition* of existing packages for the managed flavor. Because the guts stay shared, "recombining" or refactoring later is cheap. No `at-session` binary is introduced; the veer-off surface is this library + the managed kit config.

## Testing

Hermetic, per the repo's plan/execute split: the `Launcher` interface is exercised against a **fake substrate** (a `backend`/`runner.Fake`-backed Colima launcher and a fake `Prepare`), so build/raise/init are testable without Docker or a VM. The managed-kit golden test asserts the locked egress list (no Anthropic hosts). Real-substrate paths stay behind the `integration` tag.

## Phasing

1. **Phase 1 (this epic):** define the `Launcher` interface (kit-in; owns build+raise+init) + a **Colima** implementation that consolidates today's `assemble`/`install` build and `connect.LaunchCoveMaster` init; introduce the **managed kit** (locked egress) → **ships COV-208**; the `at-jam` supervisor drives it. Local build only.
2. **Later:** kit-by-reference from the registry (role → kit resolution — the deferred `kits.md` slice); a **remote-build** launcher (Fly/Firecracker) as the second substrate, which is when the build/init seam gets refined against real divergence.

## Ticket reshaping

This supersedes the "`at-cove` → `at-session` binary split" framing. **COV-208** (egress lock) becomes the managed-kit egress config on this Launcher path. A new epic tracks the Launcher abstraction; COV-208 reparents under it; the `at-session` idea is recorded as *not pursued* (superseded by the internal library).

---

*Grounded in the current seam (`internal/backend` = runtime substrate abstraction, Colima the one impl) and the brainstorm decisions: launcher-as-abstraction (not a CLI clone), internal library (not a binary), kit communicated to the launcher which owns build (incl. remote), Colima-first with substrate-divergence as the shaped-for direction.*
