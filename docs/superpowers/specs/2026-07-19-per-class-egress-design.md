# Per-class allowed-domains — session-scoped egress — Design

**Date:** 2026-07-19
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove`
**Epic:** [COV-39](https://linear.app/aethons-tools/issue/COV-39)
**Builds on:** [COV-38](https://linear.app/aethons-tools/issue/COV-38) (the `install` model + `install.json` manifest this delivers from) and [COV-34](https://linear.app/aethons-tools/issue/COV-34) (the sealed squid egress layer this extends). Mirrors the secrets structure in `internal/kit` (`ResolvedWorker`/`ResolvedCollaborator`).

## 1. Purpose

Today a kit's egress allow-list is a single flat, kit-global `image.allowed-domains` list. `internal/assemble` bakes it into the image (`/etc/squid/allowed_domains.kit.txt`), the sealed `squid.conf` allows it *additively* on top of the sealed base list, and squid reads it **once at container boot**. Every session — every dispatched worker, every collaborator chat — shares that one baked list. There is no way to give an autonomous `deploy` worker a wider egress than a `docs` worker, or to scope a collaborator's reach to what its role needs.

This design gives egress the **same root + `<common>` + per-class shape as secrets**, and makes the effective allow-list **session-scoped**: at session start at-cove resolves the class's domains and applies them to squid inside the running container, without rebuilding the image. The sealed base still wins, class lists remain purely additive, and the image stays a single warm artifact (COV-38).

**Requirements:**
- `allowed-domains` gains a root list, a `<common>` list, and per-worker / per-collaborator lists, merged per class (root ∪ `<common>` ∪ class).
- Each session's egress is scoped to its handler class, from **one** installed image.
- Never weaken the egress threat model: the sealed squid still wins; class lists can only **widen** within what the proxy permits, never bypass it.
- The agent workload cannot alter its own allow-list.
- The per-class list derives from the **currency-pinned `install.json`**, not a live `config.yml` — "install froze it" (COV-38) still holds.
- Keep `allowed-domains` in `config.yml` (a hardening concern), never the kit Dockerfile.

## 2. Principles

- **Session-scoped, one image.** The class already drives only post-boot delivery (secrets, role prompt). Egress joins them as another per-session-delivered thing. The warm image + currency model is untouched.
- **Additive, sealed-wins.** squid remains default-deny with allow-only lists; adding a per-session list can only widen, and the sealed base is unconditional.
- **Privileged delivery, unprivileged workload.** Only at-cove (host, `docker exec` as root) writes the session list and reloads squid. The agent (SSH as the non-root `agent` user) cannot.
- **Frozen source.** The per-class list is read from `install.json`'s resolved config, so it reflects what was installed, not a later edit.

## 3. Config schema

Root egress stays `image.allowed-domains` (a `[]string`, in `config.yml`). Add an `allowed-domains: []string` to `Worker` and `Collaborator` (and their `<common>` bases):

```yaml
image:
  allowed-domains: [pypi.org]          # root — every session
workers:
  <common>:
    allowed-domains: [github.com]      # every worker class
  deploy:
    allowed-domains: [registry.example.com]
collaborators:
  <common>:
    allowed-domains: [docs.internal]
  planner:
    allowed-domains: [linear.app]
```

**The effective egress for a class is `root ∪ <common> ∪ class`** (a de-duplicated set — domains are additive, unlike the map-overwrite secrets merge). The implementation splits it exactly the way secrets already split (where the root bucket is separate from `ResolvedWorker`'s `<common> ∪ own` merge):
- **Root** (`image.allowed-domains`) is delivered to *every* session by the baked kit file (§4) — it is not part of the per-class resolver, just as root secrets are not part of `ResolvedWorker`.
- New pure resolvers `ResolvedWorkerDomains(class)` / `ResolvedCollaboratorDomains(class)` return the **`<common> ∪ class`** portion (deduped, order-normalized) — the per-session delta that squid receives at session start.

So the union is realized as *baked root* + *session-delivered `<common> ∪ class`*, with no domain written twice. Validation mirrors the root list (non-empty entries; `<common>` reserved).

## 4. Enforcement — three additive allow-lists

squid gains a third allow-list file; all three are allow-only (a request passes if it matches **any**), so the model stays widen-only and default-deny:

| file | source | when | scope |
|---|---|---|---|
| `allowed_domains.txt` | sealed hardening | baked | base, unconditional |
| `allowed_domains.kit.txt` | `image.allowed-domains` (root) | baked at `install` | every session |
| `allowed_domains.session.txt` | `<common> ∪ class` **delta** | delivered per session | this session's class |

`squid.conf` adds `acl allowed_session_domains dstdomain "/etc/squid/allowed_domains.session.txt"` + `http_access allow allowed_session_domains`. The session file is **always present** (the hardening layer bakes an empty, header-only file) so the ACL never dangles; a no-class session (`create`) simply leaves it empty. Only the per-class *delta* goes in the session file — root is already covered by the baked kit file, so nothing is duplicated.

## 5. Delivery mechanism

A new backend operation applies a session's egress, behind the runner seam so it is hermetically testable:

```
ApplySessionEgress(container string, domains []string) error
```

It `docker exec`s (as root) a **sealed** helper `apply-session-domains.sh` in the hardening layer, passing the resolved domains on **stdin**; the helper overwrites `/etc/squid/allowed_domains.session.txt` and runs `squid -k reconfigure` (squid re-reads its ACL files without dropping the process). The domains are resolved **host-side** from `install.json`'s `RunConfig` via the §3 resolvers, and applied **before** the agent is handed the session.

- **Ephemeral** (`work` / `dispatch`): apply immediately after `RunEphemeral` boots the container, before the agent step runs. The container boots with the baked (sealed + root) lists; the exec adds the worker class's delta.
- **Persistent** (`chat`): apply on chat start; **clear the session file + reconfigure on chat exit**, so an idle persistent container reverts to root-only rather than retaining a collaborator's widened egress. One active class per persistent container at a time (a single interactive session) — a documented constraint, not enforced.

## 6. Threat model

- The agent runs as the non-root `agent` user and reaches the container only over SSH. `/etc/squid/allowed_domains.session.txt` and `squid -k reconfigure` are root-only; the helper is invoked solely via host `docker exec`. So the workload **cannot widen its own egress**.
- Lists are additive to the sealed base — a kit can only widen within what the proxy permits, and the proxy + nftables lock are unchanged.
- The per-class list comes from the currency-pinned `install.json`, so it cannot be changed without a re-`install` (which re-freezes it). A live `config.yml` edit does not silently change a running session's egress.

## 7. Testing

- **Hermetic + pure:** `ResolvedWorkerDomains`/`ResolvedCollaboratorDomains` (root ∪ `<common>` ∪ class, dedup, order) unit-tested over plain structs.
- **Runner seam:** `ApplySessionEgress` drives `runner.Fake`; assert the recorded `docker exec … apply-session-domains.sh` call and the stdin payload. Ephemeral + chat wiring assert egress is applied before agent handoff (and cleared on chat exit).
- **squid config:** an assemble/hardening test that `squid.conf` references the session file and that the baked empty file exists.
- **Real-run:** actual per-class egress (a domain allowed for one class, denied for another) is validated by a live dispatch/chat run — a CI/integration concern, like the existing egress lockdown.

## 8. Non-goals / deferred

- Concurrent multi-class sessions in one persistent container (one active class at a time).
- Per-class *narrowing* (removing sealed/root domains) — lists are widen-only by design.
- Per-class images or an install-time class dimension (explicitly rejected — breaks the one-warm-image + currency model).
- Changing the sealed base list, the proxy, or the nftables lock.

## 9. Decomposition (sub-issues under COV-39)

Strict order via `blockedBy`; all `class:implementor`. Each lands as one PR against `main` passing `gate`, docs updated in-change.

- **S1 · config schema + union-merge resolvers** — add `allowed-domains` to `Worker`/`Collaborator` (+ `<common>`); `ResolvedWorkerDomains`/`ResolvedCollaboratorDomains` (root ∪ `<common>` ∪ class, deduped); validation. Pure, hermetic, TDD. No wiring.
- **S2 · hardening: session allow-list** — add the third ACL to `squid.conf`; add the sealed `apply-session-domains.sh` (writes the session file from stdin + `squid -k reconfigure`); bake an empty `allowed_domains.session.txt`. *Blocked by S1.*
- **S3 · backend `ApplySessionEgress`** — the `docker exec` + reconfigure op behind the backend seam; `runner.Fake` tests. *Blocked by S2.*
- **S4 · wire worker/dispatch (ephemeral)** — resolve the worker-class domains from `install.json` and apply after `RunEphemeral`, before the agent step. *Blocked by S3.*
- **S5 · wire collaborator/chat (persistent)** — resolve collaborator domains and apply on chat start; clear + reconfigure on chat exit. *Blocked by S4.*
- **S6 · docs sweep** — OVERVIEW (egress model) + usage config docs describe root + `<common>` + per-class egress and the session-scoped enforcement. *Blocked by S5.*
