---
summary: Section index for operating `at-harbor` — the durable central service that brokers a cove's credentials/egress, holds the actor roster (RBAC), and serves the kit registry.
read_when: You are running or administering a harbor service — standing it up, signing an operator in, deciding who can reach what, or registering kits — and need the map of its operator docs.
owns: the map of the at-harbor operator/usage docs and how they relate
prereqs: ../../OVERVIEW.md for what at-cove/harbor is; ../at-cove-config.md#harbor for the cove side of the connection
tier: section
updated: 2026-09-14
---

# `at-harbor` — operating the central service

`at-harbor` is a **durable, always-on host service** that a fleet of hardened
coves points at. It does three things for the coves it serves:

- **Brokers credentials + egress** — a cove reaches Anthropic and git through
  harbor with only a scoped *identity token*; harbor holds the real credentials
  and injects them. The high-value secrets live in harbor, never in the cove.
- **Holds the roster (RBAC)** — a top-level **Actor** (one identity + token) is
  granted **Role**s within **Project**s; a Role owns the security scope the broker
  enforces.
- **Serves the kit registry** — named, versioned kit definitions a Role can bind.

This is the *how to run and administer it* layer. For the cove side — making a
sandbox use a harbor — see [`../at-cove-config.md#harbor`](../at-cove-config.md).
For *why* it's built this way (threat model, the broker's boundary relocation, the
five pillars), see the design history:
[`../../superpowers/specs/2026-09-10-harbor-design.md`](../../superpowers/specs/2026-09-10-harbor-design.md).

## Which doc for which task

| Doc | Read when |
|-----|-----------|
| [serve.md](serve.md) | Standing up the service: `at-harbor serve`, the serve-config YAML (listen, TLS, store, credentials), the broker + destinations, and the off-loopback exposure rule. |
| [operators.md](operators.md) | Signing an operator in: `operator-auth.oidc`, `login`/`logout`/`whoami`, the `--token`/env fallback, and `settings.yml` app profiles (`--app`). |
| [roster.md](roster.md) | Deciding who can reach what: `role`/`grant`/`ungrant`/`roster` and `enroll`/`revoke` — the Actor→Role RBAC model in practice. |
| [kits.md](kits.md) | Registering or versioning kits: `kit push\|list\|show\|versions\|pin\|rm` and binding one to a role with `role add --kit`. |
| [coves.md](coves.md) | You are raising/tearing down a managed cove, inspecting the runtime registry, tuning the supervisor's lease/reconcile timing, or running the cove-side Attach client (cove-master). |
| [dispatcher.md](dispatcher.md) | You are enabling harbor's always-on intake — polling a tracker (Linear) and raising a managed cove per ready ticket — or tuning its concurrency cap / poll interval. |
| [ui.md](ui.md) | You want to watch a running harbor in a browser — the live coves and the roster/roles/kits/destinations — or do the roster day-job (enroll/revoke, roles, grants), or raise/tear down a managed cove, from the browser instead of the CLI. |
| [messaging.md](messaging.md) | You want a raised cove's agent to read/send comments on its own ticket (the brokered messaging MCP), or you're wiring the `/messages` endpoint + its `cove-master mcp` delivery. |
| [comms-addressing.md](comms-addressing.md) | You want a cove's agent to send to a named human or channel instead of only its own ticket — the target space, the Project roster, the comms access-graph (`Scope.Addressing`), and `send(to=…)`/`list_targets`. |

## The shape of a working harbor

1. **Run it** — write a serve config and start `at-harbor serve` ([serve.md](serve.md)).
2. **Gate the admin API** (beyond loopback) and sign in ([operators.md](operators.md)).
3. **Declare destinations + roles**, then **enroll** coves or grant roles to
   standing actors ([roster.md](roster.md)).
4. **Register kits** a role can fulfil ([kits.md](kits.md)).
5. **Raise managed coves** against a role and track them through the runtime
   registry ([coves.md](coves.md)).

Every admin verb (`destination`, `role`, `grant`, `ungrant`, `roster`, `enroll`,
`revoke`, `kit`, `cove`) is a thin client of the running harbor's admin API: it takes
`--app`/`--admin-url` to pick the target and `--token` (or a cached login) to
authenticate. That client story lives in [operators.md](operators.md); the
per-verb detail lives in the three admin docs above.
