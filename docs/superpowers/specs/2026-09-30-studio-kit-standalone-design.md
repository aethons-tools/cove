# jam: the StudioKit — a small, standalone managed-kit concept

**Status:** design approved (brainstorm complete), pre-plan
**Scope:** replace the derived managed kit with a **StudioKit** — a small type authored *directly*, declaring only what a brokered ("studio") cove needs: a base image, egress demands, secret demands, an orienting prompt, and build args. A studio kit no longer piggybacks on an `at-cove install` manifest, so **`runtime.launcher.install-manifest` is removed**.
**Builds on:** the Launcher abstraction — [`2026-09-29-cove-launcher-abstraction-design.md`](2026-09-29-cove-launcher-abstraction-design.md) (kit-reference + lazy prepare, `PrepareKit`/`Raise`, `backend.KitImageBuilder`) and its role→kit slice — [`2026-09-30-cove-launcher-role-kit-resolution-design.md`](2026-09-30-cove-launcher-role-kit-resolution-design.md). Reuses `internal/baseimage` (3-way base + provenance gate), `internal/assemble` (`AssembleContext`, data-only), `internal/basedigest` (blessed default), and the kit registry (`internal/jam` Store).
**Does not change:** `at-cove` and its `internal/*` callers (strictly additive — `at-cove install` and its `install.Manifest` stay, they just stop feeding the launcher); the broker/roster/`Decide` model; the hardening layer's sealed egress *mechanism*; the single gated build site (COV-38).
**Deferred:** the remote-build **second substrate** (still YAGNI per Phase 1); the **Dockerfile+context** base case is in the schema now but its build is a named later slice. Colima-only.

## Why

Today a "managed kit" is **not** a small thing: `jam.EnsureManagedKit(manifest.RunConfig)` takes the *entire* interactive `kit.Config` from an `at-cove install` manifest (`runtime.launcher.install-manifest`) and derives a managed variant by stripping the three Anthropic egress roots (COV-208). The FROM-base comes from the same manifest's `BaseRef`. So a brokered cove's kit carries the full interactive surface — workers, tracker, dispatch, collaborators, teammates, source-control, model-provider — none of which a broker-driven studio session needs, and it can only exist as a *derivative* of an interactive install on the same host.

A studio cove needs far less. Hardening the concept means giving it its **own small type**, authored directly, so the managed path stops depending on an interactive install and the launcher builds from a self-contained definition.

## What a StudioKit is

A new `internal/studio` package with a `StudioKit` type — five author-facing fields, split by *when* each matters:

| Field | Kind | Consumed at | Notes |
|---|---|---|---|
| `base` | build-affecting | **Prepare** | 3-way (ref / Dockerfile+context / omitted→blessed default), gated |
| `egress` | build-affecting (ceiling) | **Prepare** (Raise narrows) | authored allow-list; Anthropic excluded from the ceiling |
| `build-args` | build-affecting | **Prepare** | `map[string]string` → `--build-arg`; never secrets |
| `secrets` | declarative demand | **Raise** | `map[name]{description}`; resolved + injected at raise, never in the build |
| `prompt` | raise-time | **Raise** | the Kit-info orientation layer; never baked into the image |

The **build-affecting** fields (`base`, `egress` ceiling, `build-args`) determine the image; the **raise-time** fields (`secrets` injection, `prompt`) do not. This split drives the storage/versioning model (§ Storage) and the prompt model (§ Session prompt).

`StudioKit` is a distinct type — it deliberately does **not** reuse `kit.Config`. The derive-from-a-full-kit machinery (`ManagedKit`, `EnsureManagedKit`, `EnsureManagedKitFor`, and `role.Kit` → `managed-<name>`) is **retired** (§ Retiring the derived kit).

### Field: base image

Reuses the existing three-way selector and provenance gate (`internal/baseimage` `Spec{DockerfileDir, Base, DefaultRef}`; a kit-chosen base must descend from a blessed `cove-base-image`). For a studio kit the **gate is ON with no `--allow-unverified` escape hatch** — a brokered cove is the locked-down path and must run a blessed base; an unblessed base **fails the prepare loudly** (surfaced in the serve log).

- **`base: <ref>`** → gated, built FROM that ref.
- **Dockerfile + context** → accepted by the schema **now**, but `PrepareKit` returns `ErrDockerfileContextUnsupported` until the deferred build slice lands (see Deferred). Defining it now hardens the concept without speculative build plumbing; carrying a build context by value to a (possibly remote) launcher is exactly the kind of divergence the Phase-1 spec says to discover with the second substrate.
- **omitted** → `basedigest.DefaultRef()` (the blessed `cove-base-image`).

### Field: egress — ceiling-only, COV-208 kept, transparent

The studio build bakes an egress **ceiling that structurally excludes** the Anthropic roots (`anthropic.com`, `claude.com`, `claude.ai`) — a brokered cove reaches Anthropic only through the jam broker. The authored `egress` is the **policy list within that ceiling** (the existing role-egress "must fit inside the ceiling" mechanism applies at raise).

There is **no silent strip and no author-time rejection**: a studio kit may list whatever it wants, but the ceiling caps it, and an Anthropic entry simply can't take effect. To keep that non-surprising, **the ceiling and its exclusions are surfaced obviously**:

- `studio show` / `kit show` print the effective ceiling *and* the excluded roots.
- `PrepareKit` logs once at build: `studio kit <id> prepared … ceiling excludes: anthropic.com, claude.com, claude.ai`.

`ManagedKit()`'s Anthropic-strip derivation is removed; the exclusion moves into the studio ceiling assembler (a fixed, always-applied property of the studio build, not a transformation of an author's list).

### Field: secrets — demands only

Reuse the existing demand model unchanged: `map[name]SecretConfig{Description}` — an env-var name plus a human description of purpose. **The kit never carries a resolver command or value** (that stays a machine-side concern). Values are resolved at Raise by the broker/host credential resolver — the same path that supplies a studio's identity token today — and injected into the session env via `cove-master`. The demand only declares *what the session needs*.

### Field: build-args

`build-args: map[string]string`, rendered as `docker build --build-arg k=v` and **included in the image content key** (§ Storage). Rejected at **register time** if a build-arg name collides with a declared secret demand or a reserved secret name — upholding *secrets never hit disk or argv*. Secrets reach the session only at Raise, never the build.

## Session prompt composition (Raise-time layers)

The session prompt is **assembled at Raise**, not baked at Prepare, from ordered layers each drawn from its own source:

```
Jam boilerplate   (at-jam built-in: "you are operating in a sandbox…")
   ↓  Kit info     (StudioKit.prompt)
   ↓  Project info (Project config)
   ↓  Role info    (Role config)
   ↓  Launch prompt (per-raise spec.Prompt / Requisitioner brief)
```

A small `studio/prompt` composer, called by the supervisor/launcher in **`Raise`** (never `PrepareKit`), concatenates the present layers in this fixed order and produces the single prompt handed to `connect.LaunchCoveMaster`. Only the **Kit-info** layer lives in the StudioKit; the other layers come from their existing sources.

Because the prompt is a raise-time input, it is **outside the image content key** — editing a kit's `prompt` does not rebuild the image (§ Storage).

**Scope for this spec:** define the composer and the layer ordering, and wire the **Kit-info** and **Launch** layers end-to-end. Project-info and Role-info plug into the same ordered composer; if `Project`/`Role` lack a prompt field today, adding those fields is a thin follow-up, not a blocker (the composer simply omits an absent layer).

## Storage, versioning & the image content key

A StudioKit is stored in the **existing kit registry** as JSON, **type-discriminated** from full `kit.Config` entries so the `studio`/`kit` verbs list, show, and version them. The registry keeps its monotonic-version-behind-a-`current`-pointer model.

**Two identities, decoupled:**

- The registry **`(id, version)`** keys the *definition* (the whole StudioKit, prompt included).
- The built **image is tagged by a content digest over the build-affecting fields only** — `base` + `egress` ceiling + `build-args` — e.g. `cove-kit:<buildDigest>`. The launcher's prepared-kit inventory (`HasKitImage`) keys on this build-digest.

So editing a kit's `prompt` (or a purely raise-time change) **bumps the registry version but reuses the cached image** — no rebuild. A change to any build-affecting field yields a new build-digest and a fresh image. This makes "the prompt is outside the image" concrete and avoids image churn on prompt edits.

## Retiring the derived kit and install-manifest

A brokered cove's kit is **always** a directly-authored StudioKit. Consequently:

- **`ManagedKit` / `EnsureManagedKit` / `EnsureManagedKitFor` / `ManagedVariantID`** (the strip-and-derive path) are **removed**.
- **`role.Kit == "web"`** resolves **directly** to StudioKit `web` (fail closed if absent or not tag-safe). No `managed-<name>` derivative, no strip step.
- **`runtime.launcher.install-manifest` is removed** — a **breaking `jam.yml` change, no shim**. The launcher no longer reads an `install.Manifest`. Documented in the migration/rename doc. (`at-cove install` and `install.Manifest` themselves are untouched; they simply stop feeding the launcher.)
- The launcher's static **`Image` / `ImageDigest`** legacy raise is retired; the SSH identity public key still comes from `IdentityFile.pub`.

### The built-in blessed default StudioKit

For `role.Kit == ""` there is a **default StudioKit defined in code** (blessed base, Anthropic-free minimal egress, generic orient prompt). It is **idempotently seeded into the registry at serve start** — so it is inspectable and versioned like any other kit (satisfying the transparency rule), not an invisible in-code special case. `role.Kit == ""` → this default; any role may override by naming its own StudioKit.

## Launcher / supervisor wiring

- **`KitDefinition` carries a `StudioKit`** (not `kit.Config`). `ResolveKitDefinition` parses the registry's studio-kit JSON.
- **`PrepareKit`** assembles a **data-only** build context from the StudioKit — base (gated), build-args, and the studio ceiling via `AssembleContext` — and builds `cove-kit:<buildDigest>`; idempotent + per-ref locked as today. A Dockerfile-context kit errors `ErrDockerfileContextUnsupported` (deferred).
- **`Raise`** composes the prompt (§ Session prompt), resolves + injects the declared secret demands, applies role egress within the studio ceiling, then `connect.LaunchCoveMaster` — unchanged below the SSH `Endpoint`. On `ErrKitNotReady`, the supervisor resolves the definition and calls `PrepareKit` then retries (the existing Phase-1 flow), now keyed on the studio kit's ref.

The supervisor stays backend-free: it touches only the registry (studio-kit JSON) and refs; base resolution/gating and build stay on the launcher.

## Testing

Hermetic, per the plan/execute split (`runner.Fake` + fake backend):

- **Studio ceiling** excludes the three Anthropic roots — golden test over the baked ceiling file.
- **Build-digest stability**: a prompt-only edit reuses the image (same build-digest, no rebuild); a base/egress/build-arg change yields a new digest.
- **Default kit** is seeded into the registry at start and is inspectable via `studio`/`kit show`.
- **role→studio-kit** resolves a named kit directly and **fails closed** on absent/not-tag-safe.
- **Register validation**: a build-arg colliding with a secret demand / reserved name is rejected; a Dockerfile-context kit registers but `PrepareKit` returns `ErrDockerfileContextUnsupported`.
- **Transparency**: `studio show` / `kit show` render the effective ceiling + exclusions; the prepare log line names the excluded roots.

Real-substrate paths stay behind the `integration` tag.

## Phasing

This lands as the next slice under the Launcher epic (COV-211, ~Phase 2b). Proposed order (firmed in the plan):

1. **`StudioKit` type + validation** (`internal/studio`): the five fields, register-time checks (build-arg/secret collision, tag-safe name), JSON codec, registry type-discrimination.
2. **Build-digest + Prepare from a StudioKit**: `PrepareKit` consumes a StudioKit; image tagged by build-digest; studio ceiling assembler (Anthropic-excluded); Dockerfile-context → `ErrDockerfileContextUnsupported`.
3. **Prompt composer + Raise wiring**: ordered layers; Kit-info + Launch wired; secret demands resolved/injected at Raise.
4. **Default StudioKit + role→kit + retire derivation/install-manifest**: seed the built-in default; `role.Kit` resolves directly; remove `ManagedKit*` and `runtime.launcher.install-manifest`; update `jam.yml` docs (breaking, no shim).
5. **Transparency surfaces**: `studio show`/`kit show` ceiling + exclusions; prepare log line.

**Deferred sub-slice:** the Dockerfile+context base build (carrying a context by value to the launcher) — discovered properly alongside the remote substrate.

## Additive & reversible

`at-cove` and its `install.Manifest` are untouched; only the launcher's *consumption* of a manifest goes away. The StudioKit is a new, self-contained type; the registry and launcher seams already exist. The one intentional break is `runtime.launcher.install-manifest` (removed, no shim, per decision).
