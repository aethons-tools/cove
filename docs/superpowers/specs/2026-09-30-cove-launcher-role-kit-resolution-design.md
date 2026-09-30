# jam: role→kit resolution (Launcher abstraction, Phase 2a)

**Status:** design approved (brainstorm complete), pre-plan
**Scope:** let a Jam **role name its own kit**, so a managed cove raises from that kit (Anthropic-stripped for COV-208), instead of every managed cove using the single default `managed` kit. Completes the registry's `role add --kit` binding, which exists today but is inert for managed raises.
**Builds on:** the Phase-1 Launcher abstraction — [`2026-09-29-cove-launcher-abstraction-design.md`](2026-09-29-cove-launcher-abstraction-design.md) (kit-reference + lazy prepare, the managed kit, `EnsureManagedKit`/`ResolveKitDefinition`, `backend.KitImageBuilder`), all shipped + live-verified (COV-211 T1–T6, COV-217).
**Does not change:** `at-cove`; the broker/roster/`Decide` model; the hardening layer's sealed egress mechanism; the single at-cove gated build site (COV-38).
**Deferred (unchanged):** the remote-build **second substrate** — still YAGNI per the Phase-1 spec (discovered with the second implementation, not designed speculatively). This spec is Colima-only.

## Why

Phase 1 gives every managed cove **one** kit: the `managed` kit, derived from the launcher's interactive base with Anthropic egress stripped. `Role` already carries a `Kit string` field and `at-jam role add --kit <name>` sets it, but nothing resolves it on the managed-raise path — the binding is inert. A role that needs a **different base image or toolset** than the default (e.g. a project whose coves need a different language runtime) has no way to get it. Per-role *egress* differences are already handled by role egress (narrowing within the kit's baked ceiling); this is specifically about a different **kit**.

## Behavior

- Role with **`Kit == ""`** → the default `managed` kit. Today's behavior, unchanged.
- Role with **`Kit == "web"`** → the cove runs a **managed (Anthropic-stripped) variant** of the registered kit `web`.
- Role names a kit **absent from the registry** → the raise **fails closed** (identity rolled back), like any other raise failure. No silent fallback to the default (that would hide a misconfiguration).

COV-208 holds for **every** managed cove by construction: the strip is applied at resolution to whatever kit the role names, so an operator can push ordinary kits (with or without Anthropic egress) and the managed path always removes it.

## Resolution (supervisor — stays backend-free)

At raise, given the role:

1. `role.Kit == ""` → use the default managed `KitRef` (as today, from `EnsureManagedKit(base)` at wiring).
2. `role.Kit == "web"` → **resolve + register a managed derivative**:
   - read `web`'s **current** config text from the kit registry (`KitConfig("web", 0)`); absent → fail closed.
   - unmarshal → `kit.Config`; apply `ManagedKit()` (strip Anthropic egress).
   - **idempotently register** the derivative under id **`managed-web`** via the same machinery as `EnsureManagedKit` (monotonic registry version; content-hash `Digest`): unchanged config reuses the version, a change bumps it.
   - stamp the derivative's `KitRef` on `spec.Kit`.
3. `ErrKitNotReady` on the raise → the supervisor resolves the full definition from the registry (`ResolveKitDefinition`) and calls `PrepareKit`, then retries — the existing Phase-1 flow, now keyed on whichever ref step 1/2 produced.

The supervisor still touches only the registry (config text) + refs; base resolution and build stay on the launcher side.

### Derivative naming (tag-safe)

A `KitRef.ID` becomes part of the docker tag `cove-kit:<id>-v<n>`, whose tag component admits only `[A-Za-z0-9_.-]` — so `managed/web` is invalid. The derivative id is:

- **`managed`** — the default (derivative of the launcher's base kit), as today.
- **`managed-<kitName>`** — derivative of registered kit `<kitName>`.

The **`managed-` prefix is reserved** for these derivatives. Kit names are already simple identifiers; the resolver rejects (fail closed) a `role.Kit` whose name isn't tag-safe rather than producing an unbuildable tag.

Registering derivatives in the registry (rather than deriving a throwaway ref) keeps every managed cove's kit **versioned and resolvable** — `kit show managed-web` / `kit versions managed-web` inspect it, and the Phase-1 resolve-on-miss path works unchanged.

## Base (launcher — it owns the backend + the gate)

Each kit declares its own `image.base`. The launcher picks the FROM-base per kit when building:

- **Default `managed`**: base = the install manifest's `BaseRef` (Phase-1 / COV-217, unchanged) — reuses the base the interactive install already resolved and gated, so it matches the interactive image and needs no re-gate (which would fail for a base the operator installed with `--allow-unverified-base`).
- **`managed-<kitName>`**: the launcher resolves + **gates** the derivative config's `image.base` via the backend, **gate ON — no `--allow-unverified` escape hatch**. A brokered cove is the locked-down path and must run a **blessed** base; a role-named kit with an unblessed base **fails the raise loudly** (surfaced in the serve log), rather than silently downgrading the gate.

Concretely: `backend.KitImageBuilder` gains a base-resolution step the launcher can call for a named kit (`ResolveKitBase(declaredBase string) (resolvedBase string, err error)`, gate on), feeding the resolved value into the existing `BuildKitImage(buildDir, tag, base, noCache)`. The default kit keeps passing `cfg.BaseImage` (the manifest `BaseRef`). The launcher chooses by the derivative id: the default `managed` id uses `cfg.BaseImage`; any other uses the resolved config base.

## Egress / role egress

Each managed variant's **baked ceiling omits Anthropic** (COV-208), exactly as the default. Role egress still narrows the active list within that ceiling at raise — mechanics unchanged. A role can therefore both name a kit *and* carry an egress policy; the policy is applied in-box after the kit's image is raised, as today.

## Testing

Hermetic, per the plan/execute split:
- **Supervisor** (`runner`-free, fake launcher): a role with `Kit: "web"` resolves + registers `managed-web` and raises from that ref; an unchanged config reuses the version; a missing kit fails closed; `Kit: ""` still uses the default ref. Drives the file store's real kit registry.
- **Launcher/backend** (`runner.Fake` + fake ops): the default kit builds FROM `cfg.BaseImage`; a named kit resolves its declared base via the backend (gate on) and builds FROM the resolved value; an unblessed named base surfaces an error.
- **Live**: one raise with a role bound to a second kit — `dev/verify-managed-kit.sh` shows `cove-kit:managed-<name>-v<n>` with an Anthropic-free ceiling.

Real-substrate paths stay behind the `integration` tag.

## Slicing (Phase 2a — attended, TDD)

1. **Supervisor role→kit resolution** — `resolveRoleKit(role) (KitRef, error)`: `""`→default ref; named→strip + idempotent-register `managed-<name>` + ref; missing/untag-safe→fail closed. Wire into `Raise`. Hermetic.
2. **Launcher per-kit base** — backend `ResolveKitBase` (gate on); the launcher resolves a named kit's declared base and builds FROM it, default keeps `cfg.BaseImage`. Hermetic + one live raise.

## Additive & reversible

`at-cove` untouched. A role with no `Kit` is byte-for-byte the Phase-1 behavior. The derivative registry entries are ordinary versioned kits; the base gate reuses the existing backend provenance path. Nothing here presupposes the remote substrate, which stays deferred.
