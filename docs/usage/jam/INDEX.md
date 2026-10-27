---
summary: Section index for operating `at-jam` — the durable central service that brokers a studio's credentials/egress, holds the actor roster (RBAC), and serves the kit registry.
read_when: You are running or administering a Jam service — standing it up, signing an operator in, deciding who can reach what, or registering kits — and need the map of its operator docs.
owns: the map of the at-jam operator/usage docs and how they relate
prereqs: ../../OVERVIEW.md for the studio image and its security model
tier: section
updated: 2026-10-27
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

`at-jam` ships in one release archive with `at-mint`: install it with the
one-command installer and keep it current with `at-jam update` (see
[installing the binaries](../../OVERVIEW.md#installing-the-binaries)).

This is the *how to run and administer it* layer.
For *why* it's built this way (threat model, the broker's boundary relocation, the
five pillars), see the design history:
[`../../superpowers/specs/2026-09-10-harbor-design.md`](../../superpowers/specs/2026-09-10-harbor-design.md).

## Which doc for which task

| Doc | Read when |
|-----|-----------|
| [dispatch-runbook.md](dispatch-runbook.md) | You are standing up (or reproducing) a real Linear→studio dispatch loop end to end and want the ordered steps + the field gotchas (egress, cert-name, flat-vs-grouped labels), not the per-field reference. |
| [credentials.md](credentials.md) | You are supplying the real secrets a Jam brokers/uses — writing ~/.config/at-jam/credentials.yml, choosing value/command/global/mint per credential, or wiring credentials-file — and want the file format and the demand/supply split. |
| [serve.md](serve.md) | Standing up the service: `at-jam serve`, the serve-config YAML (listen, TLS, `store-postgres`, `state-dir`, removed storage keys, credentials, the subscription account pool), the broker + destinations, the launcher (incl. which colima instance studios run in), and the off-loopback exposure rule. |
| [header-specs.md](header-specs.md) | Adding a destination whose upstream wants the credential in a header the presets don't cover (or bare in Authorization, like Linear), choosing an `identity_in`/`apply` preset, or debugging a 401 / wrong upstream auth header. |
| [destination-header-logging.md](destination-header-logging.md) | A brokered request fails auth and you need to see which identity header the studio sent or which credential and username Jam forwarded (redacted), or you are turning a destination's `log_headers_in`/`log_headers_out` on or off. |
| [connector.md](connector.md) | Adding a destination a studio needs client-side setup for (env vars like GH_HOST, git routing), wondering why a studio has some ANTHROPIC_*/GH_* variable, or wiring `gh` through Jam. |
| [vertex.md](vertex.md) | You want a role's coves to run Claude on Vertex AI under Jam (the GCP credential brokered as `exchange: gcp`, a path-guarded destination per region), are adding a Vertex region/project, or a Vertex cove gets 403 "path not allowed" / 502 "credential unavailable". |
| [pool.md](pool.md) | Running coves on a subscription-OAuth account pool: identity→account binding + bearer injection, the broker's `oauth-2025-04-20` beta (replacing the removed `oauth_beta` / `--oauth-beta`), the `at-jam pool` verb, broker-owned token refresh, and the egress/rollout it needs. |
| [monitoring.md](monitoring.md) | You want to be alerted when Jam needs a human, are setting up `metrics:` or the deploy/monitoring bundle, or a condition on the Health tab / a Discord alert from Jam needs explaining. |
| [operators.md](operators.md) | Signing an operator in: `operator-auth.oidc`, `login`/`logout`/`whoami`, the `--token`/env fallback, and `settings.yml` app profiles (`--app`). |
| [projects.md](projects.md) | You are starting a new project on a Jam, a role/grant/roster/escalation write failed with "project not found", you want to delete a project, or you upgraded a Jam whose projects used to exist only as names. |
| [roster.md](roster.md) | Deciding who can reach what: `role`/`grant`/`ungrant`/`actors` and `enroll`/`revoke` — the Actor→Role RBAC model in practice — and a role's raw egress (`egress set`/`show`/`clear`). |
| [kits.md](kits.md) | Authoring or versioning a studio kit (base, egress, build-args, secrets, prompt, mcp-servers; the Anthropic-excluding egress ceiling): `kit push\|list\|show\|versions\|pin\|rm`, and binding one to a role with `role add --kit` (unset → `default`); the entry point to build on another kit. |
| [kits-directory.md](kits-directory.md) | You are laying out a kit directory (Dockerfile, scripts, note files), a push or import refused a file (symlink, limit, unsafe name, `base.context…` field), or want to know what a kit version stores. |
| [kits-drafts.md](kits-drafts.md) | You want to open, inspect, edit, discard, test-build, try in a studio or publish a kit's draft with `at-jam kit draft`, `studio raise --kit-draft`, over the admin API (`/admin/kits/{name}/draft`) or a draft call answered 409, 412, 413, 415, 428 or 503. |
| [kits-drafts-ui.md](kits-drafts-ui.md) | You are using or changing the kit page's Draft tab in the admin UI (`/app/admin/kits/<name>/draft`), or a Draft-tab form refused a save or a publish. |
| [kits-inheritance.md](kits-inheritance.md) | You want a kit to build on another kit (`base: { kit: <name>[@vN] }`), are choosing to follow or pin a parent, a push/raise failed with a missing-parent, cycle or too-deep error, `kit rm`/`kit pin` was refused because of child kits, or a child studio went `image=stale` after its parent changed. |
| [kits-git.md](kits-git.md) | You want a kit to come from a git repo (`kit import\|track\|sync\|untrack`), are writing a `<repo>[//dir]@<ref>` source, giving Jam a `kits.git` token for a private repo, wondering why a sync said `unchanged`, or an import/sync failed (auth, ref or kit.yml not found, escape refused, limit exceeded). |
| [kits-path-parents.md](kits-path-parents.md) | A git kit builds on another kit in the same repo (`base: { kit: ../web }`), you want to know which kits an import/track/sync of it writes and at which versions, or a set import was refused (not from this source, local push newer, leaves the checkout, no kit.yml, cycle, too deep, name used twice). |
| [model-specs.md](model-specs.md) | You are authoring, changing or deleting a model-spec (how a cove runs its agent: harness, exact version pin + runtime constraint, plugins built into the image, principal credential, model, permission policy), binding a role to one, a model-spec write/delete was refused, or a cove failed its claude version check. |
| [model-spec-harness.md](model-spec-harness.md) | You are changing a model-spec's version or plugins and want to know when coves get them, a plugin is missing or unexpectedly enabled in a cove, you need to know where a Claude setting (theme, remote control, permissions default) comes from, or Jam logged a model-spec migration warning after an upgrade. |
| [model-spec-policy.md](model-spec-policy.md) | You are setting a model-spec's policy.mode / allow / deny, or a cove's agent was denied (or allowed) a tool and you need the claude flags it ran with. |
| [model-spec-headers.md](model-spec-headers.md) | A model-spec's principal must send an extra upstream header (e.g. the `oauth-2025-04-20` beta for a subscription token outside the pool), a `principal.headers` write was refused, or a header rule isn't reaching the upstream. |
| [backup.md](backup.md) | Backing up or restoring a Jam's config (actors, roles, kits, destinations, model-specs, projects) with `at-jam export`/`import` — the file's scope, the refuse-unless-empty restore, and the token-hash sensitivity note. |
| [config-git.md](config-git.md) | You want a Jam's config (kits, destinations, model-specs, users, connections, projects, roles, context, serve settings) in a git repo — writing it out, diffing/applying a checkout, replicating a Jam — or a config apply refused, flagged drift or listed unmanaged objects, or want the repo to stop managing an object (`config release`). |
| [config-git-sources.md](config-git-sources.md) | You want a Jam to follow a config repo in git (`config track\|sync\|untrack`, `--git`), are referencing a kit by its own source (`kits/<name>.source.yml`), or a config sync failed to fetch or flagged a source kit as drifted. |
| [coves.md](coves.md) | You are raising/tearing down a managed studio, inspecting the runtime registry, tuning the supervisor's lease/reconcile timing, or running the cove-side Attach client (cove-master). |
| [studio-managers.md](studio-managers.md) | You register, rotate or revoke a remote Studio Manager (`at-jam manager add\|list\|rotate\|revoke`, `runtime.studio-managers`), or debug a manager connection, its strays, or a refused revoke. |
| [inspect.md](inspect.md) | You want to see everything a studio (or a raise of a role now) is set up with — context files, egress, destinations, env, model-spec, kit/image, seed, MCP, policy — or why a studio differs from its role: `role inspect`, `studio inspect --diff`, or an agent or role page's Setup tab. |
| [session-context-authoring.md](session-context-authoring.md) | You want sessions of a role, a project or the whole Jam to know something at raise — rules, goals, repos — and need `at-jam context`, the YAML format or the limits. |
| [session-context.md](session-context.md) | You are writing a kit prompt, debugging what a session was told at raise, or changing how session context is compiled or delivered. |
| [personal-sessions.md](personal-sessions.md) | You (a human operator) want your own session of a role: linking your login to the roster, the role's personal caps, `session request\|list\|release`, talking to it over Discord until you release it, its idle ladder (nags, replying `keep`/`release` to one, optional reclaim), and why it needs `store-postgres` and a Discord inbox. |
| [standing-sessions.md](standing-sessions.md) | You want a role to have a permanent, named agent running (a standing teammate) or want to remove, reset or upgrade one: `standing add\|list\|rm\|reset\|upgrade`, how Jam keeps one studio per name alive (restart with its conversation and workspace kept, backoff), upgrading a stale one, dismissal, admission, and how it messages people. |
| [standing-state.md](standing-state.md) | You need to know what survives a standing session's restart or upgrade, where its conversation and workspace live (labeled volumes), why it did or didn't resume, or which volumes Jam may delete. |
| [collaboration.md](collaboration.md) | You want to SSH or point an IDE at an agent's studio, share a studio with a teammate, set a role's `collaborate` policy or the `ssh:` serve config, or an `at-jam ssh` connection was refused or timed out. |
| [previews.md](previews.md) | You want to open or script against a studio preview, set up previews on a Jam (`previews:`, DNS, wildcard cert), run a web IDE in a studio, or a preview link 404s, 403s, 502s or 504s. |
| [requisitioner.md](requisitioner.md) | You are enabling Jam's always-on intake — polling a tracker (Linear or GitHub Issues) and raising a managed studio per ready ticket — or tuning its concurrency cap / poll interval. |
| [ui.md](ui.md) | You want to use or expose Jam in a browser (`/app`) — who reaches /app/user vs /app/admin or why you got a 403; configure browser login (redirect URI, scopes, cookies); read the title bar; install the app; or follow an old /ui or /me link. To change something, see ui-editing.md. |
| [ui-console.md](ui-console.md) | You want to find your way around the admin console — what each tab and list shows (Dashboard, Agents, Health, Users, Specs), the Projects rail, search, the Channel log, or an agent's session timeline. |
| [ui-editing.md](ui-editing.md) | You want to change something from the admin UI instead of the CLI — enroll/revoke agents, grants, roles, raise/tear down a studio, Request a personal session, edit kits/destinations/model-specs — or a UI write was refused. |
| [ui-projects.md](ui-projects.md) | You are viewing the project list or editing one project in the admin UI — its tabs (overview, members, agents, roles, rooms and messages, escalation, chat service, context) — or a role's page (scope, egress, allocation, standing sessions), or you followed an old /app/admin/roles link. |
| [ui-attention.md](ui-attention.md) | You see a red or amber badge in the admin UI's title bar, Projects rail, on a tab or on a row and want to know what it counts and where to fix it. |
| [ui-pages.md](ui-pages.md) | You are viewing or editing one user, agent, destination, model-spec or kit in the admin UI — a user's logins/OIDC/accounts, an agent's grants and its studio's runtime/session/squawks, client env/connector, kit versions/diffs/pinning, who uses it — or wondering why the list pages only create. |
| [ui-config.md](ui-config.md) | You are using the admin UI's Config tab (tracked config repo, managed objects and drift, Diff/Apply/Track/Untrack/Release), or see a `managed · config @…` badge or a drift prompt on an object page. |
| [session-events.md](session-events.md) | You want to watch, audit, or export what a managed studio's agent did — the captured Claude Code event stream, its storage/retention config, redaction, and the export API. |
| [intercom.md](intercom.md) | You want a raised studio's agent to read/send comments on its own ticket (the brokered intercom MCP), or you're wiring the `/squawks` endpoint + its `cove-master mcp` delivery, wake-on (`runtime.wake`), or running the intercom without a Requisitioner. |
| [turn-end.md](turn-end.md) | You want a ticket session to report its ticket's state or a session to end itself, to be woken at a time or on a schedule (optionally gated by a check), or to be woken (or torn down) after sitting idle; or you need to know why a studio shows `holding`, what a woken agent is told about why it woke, or how Jam decides a session whose turn ended may be woken, paused, or torn down. |
| [intercom-ui.md](intercom-ui.md) | You want a project member to read/reply to their studios and channels in a browser — the `/app/user` Jam app (attention-grouped rail, conversations, New message, asks, the composer, unread, live refresh, the session status strip) — or you're operating it (its URLs and old-link redirects). |
| [session-view.md](session-view.md) | You're using or extending a session's conversation in `/app/user` — its header and actions, the Studio (in-pane IDE)/Previews (View here)/Artifacts/Status/Thinking pane (what the agent thinks and does), its per-session layout memory, or who sees a session's cost. |
| [push.md](push.md) | You want Jam to send push notifications to members' phones and desktops — `at-jam push keygen`, the VAPID credential, `runtime.push`, the push-service egress hosts — or a member asks about the Notifications menu item (missing, "Blocked", the iOS Home Screen rule), what buzzes them, lock-screen previews, the app badge, or sign-out on a shared device. |
| [push-delivery.md](push-delivery.md) | You're debugging push delivery (`push:relay` open, `push: send refused`, a subscription vanished, a late or missing push) or extending the `/app/user/push/*` routes (key, subscriptions, focus) or the endpoint allowlist. |
| [comms-addressing.md](comms-addressing.md) | You want a studio's agent to send to a named human or channel instead of only its own ticket — the target space, a Project's members and rooms, the comms access-graph (`Scope.Addressing`), and `send(to=…)`/`list_targets`. |
| [discord.md](discord.md) | You are giving a human a Discord inbox or binding them to their Discord user id (`--delivery discord:<channel>`, `account add --connection discord --uid`), setting a project's chat service, or a Discord reply was attributed (member vs display name) differently than you expected. |
| [renamed-from-harbor.md](renamed-from-harbor.md) | You have a kit, config, script or env var that still says "harbor", or saw a "deprecated name" warning, and need the new name and how long the old one keeps working. |
| [studio-kit-migration.md](studio-kit-migration.md) | Your `jam.yml` still sets `runtime.launcher.install-manifest`, or a pre-existing full-kit registry row now fails `kit show`/resolution, and you need what to change. |
| [whiteboards.md](whiteboards.md) | You want people and agents to sketch together on a shared canvas in a conversation — board access, per-element merge, sync routes, the board_* agent tools, the server-side renderer, and the board editor in the Jam app. |
| [attachments.md](attachments.md) | You want people or agents to share files (screenshots, logs, PDFs) in a conversation, you're setting up the attachments bucket (Garage/S3/R2), or an upload or download is refused — upload → reference → access-checked download, `attach`/`fetch`. |
| [asks.md](asks.md) | You want an agent to ask people a question with answers to pick from (approve, choose A/B/C) and get the answer through `read` — the `ask`/`amend`/`withdraw`/`asks` tools, who may answer, and how open asks count as asking for a person. |
| [escalation.md](escalation.md) | You want a waiting session that asked for a person to actively reach people — configuring a Project's ordered, category-keyed escalation tiers (called into the session's channel) + per-tier timeouts, the `escalate(category)` tool, and operating the resident escalation engine. |
| [../docker-in-sandbox.md](../docker-in-sandbox.md#prerequisite-install-sysbox-in-the-colima-vm-one-time) | You are preparing a Jam host's colima VM for `docker: true` kits — `at-jam colima setup-docker` / `check-docker`. |

## The shape of a working Jam

1. **Run it** — write a serve config and start `at-jam serve` ([serve.md](serve.md)).
2. **Gate the admin API** (beyond loopback) and sign in ([operators.md](operators.md)).
3. **Create the project** ([projects.md](projects.md); skip it to use `default`),
   **declare destinations + roles**, then **enroll** studios or grant roles to
   standing actors ([roster.md](roster.md)).
4. **Register kits** a role can fulfil ([kits.md](kits.md)).
5. **Raise managed studios** against a role and track them through the runtime
   registry ([coves.md](coves.md)).

Every admin verb (`destination`, `model-spec`, `project`, `role`, `grant`, `ungrant`, `roster`, `enroll`,
`revoke`, `kit`, `studio`, `session`, `standing`) is a thin client of the running Jam's admin API: it takes
`--app`/`--admin-url` to pick the target and `--token` (or a cached login) to
authenticate. That client story lives in [operators.md](operators.md); the
per-verb detail lives in the three admin docs above.
