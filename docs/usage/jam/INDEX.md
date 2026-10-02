---
summary: Section index for operating `at-jam` — the durable central service that brokers a studio's credentials/egress, holds the actor roster (RBAC), and serves the kit registry.
read_when: You are running or administering a Jam service — standing it up, signing an operator in, deciding who can reach what, or registering kits — and need the map of its operator docs.
owns: the map of the at-jam operator/usage docs and how they relate
prereqs: ../../OVERVIEW.md for what at-cove/Jam is; ../at-cove-config.md#jam for the studio side of the connection
tier: section
updated: 2026-10-02
---

# `at-jam` — operating the central service

`at-jam` is a **durable, always-on host service** that a fleet of hardened
studios points at. It does three things for the studios it serves:

- **Brokers credentials + egress** — a studio reaches Anthropic and git through
  Jam with only a scoped *identity token*; Jam holds the real credentials
  and injects them. The high-value secrets live in Jam, never in the studio.
- **Holds the roster (RBAC)** — a top-level **Actor** (one identity + token) is
  granted **Role**s within **Project**s; a Role owns the security scope the broker
  enforces.
- **Serves the kit registry** — named, versioned kit definitions a Role can bind.

This is the *how to run and administer it* layer. For the studio side — making a
sandbox use a Jam — see [`../at-cove-config.md#jam`](../at-cove-config.md).
For *why* it's built this way (threat model, the broker's boundary relocation, the
five pillars), see the design history:
[`../../superpowers/specs/2026-09-10-harbor-design.md`](../../superpowers/specs/2026-09-10-harbor-design.md).

## Which doc for which task

| Doc | Read when |
|-----|-----------|
| [dispatch-runbook.md](dispatch-runbook.md) | You are standing up (or reproducing) a real Linear→studio dispatch loop end to end and want the ordered steps + the field gotchas (egress, cert-name, flat-vs-grouped labels), not the per-field reference. |
| [credentials.md](credentials.md) | You are supplying the real secrets a Jam brokers/uses — writing ~/.config/at-jam/credentials.yml, choosing value/command/global/mint per credential, or wiring credentials-file — and want the file format and the demand/supply split. |
| [serve.md](serve.md) | Standing up the service: `at-jam serve`, the serve-config YAML (listen, TLS, `store-postgres`, `state-dir`, removed storage keys, credentials, the subscription account pool), the broker + destinations, and the off-loopback exposure rule. |
| [connector.md](connector.md) | Adding a destination a studio needs client-side setup for (env vars like GH_HOST, git routing), wondering why a studio has some ANTHROPIC_*/GH_* variable, or wiring `gh` through Jam. |
| [pool.md](pool.md) | Running coves on a subscription-OAuth account pool: identity→account binding + bearer injection, the `at-jam pool` verb, broker-owned token refresh, and the egress/rollout it needs. |
| [operators.md](operators.md) | Signing an operator in: `operator-auth.oidc`, `login`/`logout`/`whoami`, the `--token`/env fallback, and `settings.yml` app profiles (`--app`). |
| [projects.md](projects.md) | You are starting a new project on a Jam, a role/grant/roster/escalation write failed with "project not found", you want to delete a project, or you upgraded a Jam whose projects used to exist only as names. |
| [roster.md](roster.md) | Deciding who can reach what: `role`/`grant`/`ungrant`/`roster` and `enroll`/`revoke` — the Actor→Role RBAC model in practice — and a role's raw egress (`egress set`/`show`/`clear`). |
| [kits.md](kits.md) | Authoring or versioning a studio kit (base, egress, build-args, secrets, prompt; the Anthropic-excluding egress ceiling): `kit push\|list\|show\|versions\|pin\|rm`, and binding one to a role with `role add --kit` (unset → `default`). |
| [backup.md](backup.md) | Backing up or restoring a Jam's config (actors, roles, kits, destinations, projects) with `at-jam export`/`import` — the file's scope, the refuse-unless-empty restore, and the token-hash sensitivity note. |
| [coves.md](coves.md) | You are raising/tearing down a managed studio, inspecting the runtime registry, tuning the supervisor's lease/reconcile timing, or running the cove-side Attach client (cove-master). |
| [personal-sessions.md](personal-sessions.md) | You (a human operator) want your own session of a role: linking your login to the roster, the role's personal caps, `session request\|list\|release`, talking to it over Discord until you release it, its idle ladder (nags, replying `keep`/`release` to one, optional reclaim), and why it needs `store-postgres` and a Discord inbox. |
| [standing-sessions.md](standing-sessions.md) | You want a role to have a permanent, named agent running (a standing teammate) or want to remove one: `standing add\|list\|rm`, how Jam keeps one studio per name alive (restart, backoff), dismissal, admission, and how it messages people. |
| [requisitioner.md](requisitioner.md) | You are enabling Jam's always-on intake — polling a tracker (Linear) and raising a managed studio per ready ticket — or tuning its concurrency cap / poll interval. |
| [ui.md](ui.md) | You want to watch a running Jam in a browser — the live studios and the roster/roles/kits/destinations — or do the roster day-job (enroll/revoke, roles, grants), edit kits/destinations, or raise/tear down a managed studio, from the browser instead of the CLI. |
| [ui-pages.md](ui-pages.md) | You are viewing or editing one project, role, destination or kit in the admin UI — a project's roster/escalation, its scope, egress, allocation, standing sessions, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
| [session-events.md](session-events.md) | You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream, its storage/retention config, redaction, and the export API. |
| [intercom.md](intercom.md) | You want a raised studio's agent to read/send comments on its own ticket (the brokered intercom MCP), or you're wiring the `/squawks` endpoint + its `cove-master mcp` delivery, wake-on (`runtime.wake`), or running the intercom without a Requisitioner. |
| [intercom-ui.md](intercom-ui.md) | You want a roster human to read/reply to their studios and channels in a browser — the two-pane `/me` inbox (attention-grouped rail, conversation pane, New Message, unread) — or you're operating/extending it (routes, the 3s poll, mark-read, its wiring to `/me/send`). |
| [comms-addressing.md](comms-addressing.md) | You want a studio's agent to send to a named human or channel instead of only its own ticket — the target space, the Project roster, the comms access-graph (`Scope.Addressing`), and `send(to=…)`/`list_targets`. |
| [discord.md](discord.md) | You are giving a human a Discord inbox or binding them to their Discord user id (`--delivery discord:<channel>[:<user-id>]`), setting a project's chat service, or a Discord reply was attributed (roster human vs display name) differently than you expected. |
| [renamed-from-harbor.md](renamed-from-harbor.md) | You have a kit, config, script or env var that still says "harbor", or saw a "deprecated name" warning, and need the new name and how long the old one keeps working. |
| [studio-kit-migration.md](studio-kit-migration.md) | Your `jam.yml` still sets `runtime.launcher.install-manifest`, or a pre-existing full-kit registry row now fails `kit show`/resolution, and you need what to change. |
| [escalation.md](escalation.md) | You want a Waiting studio to actively nudge people instead of passively waiting — configuring a Project's ordered, category-keyed human escalation tiers + per-tier timeouts, the cove-declared `escalate(category)` tool, and operating the resident escalation engine. |

## The shape of a working Jam

1. **Run it** — write a serve config and start `at-jam serve` ([serve.md](serve.md)).
2. **Gate the admin API** (beyond loopback) and sign in ([operators.md](operators.md)).
3. **Create the project** ([projects.md](projects.md); skip it to use `default`),
   **declare destinations + roles**, then **enroll** studios or grant roles to
   standing actors ([roster.md](roster.md)).
4. **Register kits** a role can fulfil ([kits.md](kits.md)).
5. **Raise managed studios** against a role and track them through the runtime
   registry ([coves.md](coves.md)).

Every admin verb (`destination`, `project`, `role`, `grant`, `ungrant`, `roster`, `enroll`,
`revoke`, `kit`, `studio`, `session`, `standing`) is a thin client of the running Jam's admin API: it takes
`--app`/`--admin-url` to pick the target and `--token` (or a cached login) to
authenticate. That client story lives in [operators.md](operators.md); the
per-verb detail lives in the three admin docs above.
