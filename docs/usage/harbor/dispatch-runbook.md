---
summary: End-to-end runbook for standing up harbor's Linear-driven dispatch on a single Colima host — the ordered procedure (serve → destinations → role → dispatch-label gate) plus the field gotchas that bite a first real run (egress, cert-name, flat-vs-grouped labels).
read_when: You are bringing up a real harbor dispatch loop for the first time (or reproducing one) and want the ordered steps and the traps, not the per-field reference.
owns: the ordered end-to-end dispatch stand-up procedure and its field gotchas; it links to the reference docs it stitches together and never restates their schemas
prereqs: serve.md, roster.md, dispatcher.md, coves.md, ../at-cove-config.md#harbor — this runbook orders them, it does not replace them
tier: leaf
updated: 2026-09-15
---

# Runbook: harbor dispatch, end to end

The shortest correct path to a working **Linear ticket → raised cove** loop on a
single host with Colima, and the traps that actually bite. Each step links to the
doc that owns the detail; this runbook only owns the **order** and the
**gotchas**. Read [dispatcher.md](dispatcher.md) for the model.

## Prerequisites

- Colima running; a cove image built via `at-cove install` (its `install.json`).
- A Linear workspace + team; a Linear API token (harbor's own).
- Anthropic + git credentials harbor will broker.
- A DNS name for harbor with a TLS cert (see the cert gotcha below).

## The procedure

1. **Postgres (optional but recommended)** — `just dev-up` raises the dev
   Postgres; a `store-postgres` block makes both the control plane *and* the
   intercom log (the durable squawk Log) Postgres-backed. See [serve.md](serve.md#postgres-store-backend-store-postgres)
   and [`dev/`](../../../dev/README.md).
2. **`at-harbor serve`** — write the serve config: cove-facing `listen: :443` +
   `tls`, loopback `admin-listen` (no OIDC needed on loopback), `store-postgres`,
   `credentials` (anthropic + git + the DB password), and a `runtime.launcher`
   pointed at your `install.json`. Start it (`:443` needs privilege). Full schema:
   [serve.md](serve.md).
3. **Destinations** — `at-harbor destination add` for `anthropic` and `git` (the
   latter `--repo-scoped`). Each `--cred-name` must resolve to a `credentials:`
   entry, **validated at add time** — so add the credential to the serve config
   and restart before adding the destination. [serve.md](serve.md#destinations).
4. **Role** — `at-harbor role add --name worker --destinations anthropic,git
   --repos '<owner>/*' --ttl 24h`. A role with **no `--ttl` mints non-expiring
   tokens** — always set one for ephemeral coves. [roster.md](roster.md).
5. **Dispatcher** — add `runtime.dispatcher` (`role`, `max-concurrent`,
   `tracker-token`, `linear.team` + `states`). Restart serve. It polls the
   `ready` state and raises one cove per **dispatch-labeled** ticket, bounded by
   the cap. [dispatcher.md](dispatcher.md).
6. **Trigger** — label a ticket `dispatch:go`, move it to your `ready` state, and
   watch `at-harbor cove list`. The kit registry ([kits.md](kits.md)) is **not**
   required — the launcher raises from `install.json`, not a registered kit.

## Gotchas (each one cost a real debugging loop)

- **Dispatch is opt-in per ticket.** Only issues carrying a label matching
  `dispatch-label-prefix` (default `dispatch:`) are raised. Point `states.ready`
  at a *dedicated* column or a shared "Todo" will sweep the whole backlog into
  coves. See [dispatcher.md](dispatcher.md).
- **Use a FLAT `dispatch:*` label, not a Linear label _group_.** A grouped label's
  API `name` is just the child (e.g. `spider`), *without* the `dispatch:` prefix,
  so the gate never matches and nothing raises. Create a flat label named literally
  `dispatch:go` — same shape as `class:attended`.
- **Cove egress must allow the harbor host.** The raised cove reaches harbor
  through its own squid proxy; if `harbor-host` isn't on the cove's egress
  allow-list, every brokered call (and the Attach stream) dies and the cove does
  nothing. Widen egress in the kit/image.
- **The dialed name must match the TLS cert.** `runtime.launcher.harbor-host` /
  `runtime-addr` must be a name the broker cert's SAN covers — the cove validates
  harbor's cert against its system trust store (the launcher injects no CA). A
  cert for `local.aethons.tools` will not satisfy a cove told to dial
  `harbor.local.aethons.tools`. Match them, or reissue the cert (wildcard/SAN).
- **`store-postgres` password must resolve.** The DB password is a named
  `credentials:` entry; serve fails closed on connect. (A prior bug dropped the
  credential's name so the password resolved empty — fixed; if you see
  `password authentication failed` on a correct password, confirm you're on a
  build past that fix.)

## Known issues (open)

- **COV-188** — a raised cove's **intercom MCP tools don't register** (the
  `cove-master mcp` server), so the agent cannot `read`/`send` on its ticket. Any
  task that squawks on the ticket (and the whole comms/Discord path) is blocked
  until this lands.
- **COV-189** — an agent that **exits without a `worker-result.json`** leaves the
  Instance stuck `live/running` with no teardown.

For *why* harbor is built this way, follow the design-history pointer in
[INDEX.md](INDEX.md).
