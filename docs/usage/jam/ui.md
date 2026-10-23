---
summary: The Jam app — the one browser UI `at-jam serve` serves under `/app` (the admin console at `/app/admin/`, the user Inbox at `/app/user/`) behind one OIDC login; loopback is always an admin. Covers the routes and legacy redirects, the login and Principal (user / admin / both), the admin console's rail, tabs, list pages, search, Intercom log and session timeline, and the `POST /app/user/send` path; what the console can change is in ui-editing.md.
read_when: You want to use or expose Jam in a browser — watch the agents and their studios, the squawk Log, a session timeline, the projects/users/specs — find your way around the console (nav, sub-tabs, search), understand who may reach /app/user vs /app/admin and why you got a 403, configure browser login (redirect URI, scopes, cookies), or follow an old /ui or /me link. To change something from the UI, read ui-editing.md instead.
owns: the `/app` route table and the legacy `/ui`·`/me` 308 redirects; the `/app` gate (one browser OIDC login, its two session cookies, the Principal — user, admin, both — loopback trust, the 403 page); the `/app/admin/` observability surface (the rail, Jam's tabs and the Specs sub-tabs, what each list shows, search, the `/app/admin/agents/{id}/session` timeline); and the `/app/user/` area's identity rules and the `POST /app/user/send` participant send path
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
tier: leaf
updated: 2026-10-23
---

# The Jam app (`/app/`)

`at-jam serve` serves one browser app on the same **admin listener** as the
JSON admin API. Point a browser at the admin URL; `/` and `/app/` land on the
Inbox for a roster member, else on the admin console:

```
http://127.0.0.1:8081/app/
```

| Path | Holds |
|---|---|
| `/app/user/*` | the Inbox ([intercom-ui.md](intercom-ui.md)) and [`POST /app/user/send`](#sending-post-appusersend) |
| `/app/admin/*` | the admin console, below |
| `/app/auth/{login,callback,logout}` | the one browser login (ungated) |
| `/app/static/*` | `jam.css`, htmx (ungated) |

`/me/studios/*` ([collaboration.md](collaboration.md)) and `/admin/*` are APIs
and stay where they are. **Old URLs:** a `GET`/`HEAD` on a former `/ui/*` or
`/me/*` page (login and static included) answers `308` to its `/app/...`
equivalent with the query kept — `/ui/agents?project=p` →
`/app/admin/agents?project=p`, `/me/?c=chn_x` → `/app/user/?c=chn_x`. Any other
method on an old path is 404, never replayed. The redirects last one release.

## The admin console (`/app/admin/`)

A permanent **rail** lists **Jam**, then every project, then **+ New project**
([`display-name`](serve.md#the-serve-config) renames Jam there and in its scope
title; the title bar reads `<name> Jam`); the selection decides the **tabs** over
the content. Jam's tabs are **Dashboard · Agents · Users · Specs · Config · Intercom · Health**; a project's are
**Overview · Members · Agents · Intercom · Escalation**
([ui-projects.md](ui-projects.md)). A detail page sits under its tab (a role
page: its project's Agents; a kit: Jam's Specs) with a breadcrumb below it.
**Specs** (`/app/admin/specs`, opening on Kits) groups **Kits · Destinations ·
Model-specs** under a sub-tab strip. On a narrow screen the rail is a drawer
(☰ in the title bar). Rail entries, tabs and rows carry **attention badges**
([ui-attention.md](ui-attention.md)). Jam's pages:

- **Dashboard** (`/app/admin/`) — summary tiles: running / waiting / setting-up /
  lost-or-terminating / idled agents, each opening the Agents list filtered to it, and counts
  of projects, agents, users and specs (kits + destinations + model-specs) —
  agents, users and specs open their tab, projects are listed in the rail; Jam's **Needs attention** card; then the Jam-wide **Agent context** card
  ([ui-pages.md](ui-pages.md#agent-context-cards)), above the running agents.
- **Search** — the box in the top bar (press `/` from anywhere) searches every
  page's objects at once: agents (id, unit, owner, standing name,
  project/role, grants — one hit per id), roles (project/name, kit,
  destinations), projects, kits (name, current prompt and egress), destinations
  (name, route, upstream, env keys), model-specs (name, type, model, principal),
  users (name, logins, OIDC subject, account handles and ids) and rooms, and
  squawk bodies (newest 10; the rest via Intercom's `q=`).
  Matching is case-insensitive substring, at least 2 characters; results are
  grouped and link to each object's page. **Enter** jumps to the page when one
  object's name is the whole query (e.g. an agent id or `acme/dev`), else opens
  `/app/admin/search?q=…`, which updates as you type. Session events aren't searched.
- **Agents** (`/app/admin/agents`) — each enrolled identity and each studio, one row
  per id, with its **kind** (`standing`, `personal`, `ticket` — a session with
  a unit, `manual`, or `enrolled` — no studio), project/role, [status](ui-pages.md#agent-status),
  connector and image status ([coves.md](coves.md#the-studio-verbs)); it
  **auto-refreshes every 3 seconds**. Status filters
  (`?status=running|waiting|setting-up|idled|attention`; old `?phase=` links still work) match the dashboard tiles. Enroll,
  raise and teardown: [ui-editing.md](ui-editing.md). Each id opens the agent's
  page, its **Access** card included ([ui-pages.md](ui-pages.md#agent-pages));
  `/app/admin/coves…` and `/app/admin/actors` redirect here.
- **Intercom** (`/app/admin/intercom`) — a read-only, filterable, newest-first table of
  the channel log, with the frozen legacy log on a Legacy tab. See [Intercom](#intercom).
- **Health** (`/app/admin/health`) — operator-attention conditions (open with fix; resolved in 7 days); open critical/warning also badge the rail — see [monitoring.md](monitoring.md).
- **Config** (`/app/admin/config`) — the tracked config repo, its managed objects and their drift, Diff/Apply: [ui-config.md](ui-config.md).
- **Users / Kits / Destinations / Model-specs** — control-plane tables, editable
  here ([ui-editing.md](ui-editing.md)); roles live in their project.

Every table has a fixed order — agents by id; roles by project, then
name; kits and destinations by name; squawks newest-first — so rows don't shuffle
across a poll or an edit, and the JSON admin API and CLI lists match it.

**One look.** The console and the Inbox take their colors (light and dark,
following the OS setting) and typography from one stylesheet, `jam.css`, in
`internal/jam/uiassets`, which also holds the single `htmx` copy, served at
`/app/static/`. Each area keeps its own component styles in its layout.

## Signing in: one login, one Principal

Every `/app` page except login and static sits behind one gate, separate from
the JSON admin API's authenticator (see the fail-closed rule in
[serve.md](serve.md#exposing-the-admin-api-fail-closed)). It resolves each
request to a **Principal** — a **user**, an **admin**, or both:

- **User** — the login's ID token `(issuer, subject)` is bound to a Jam user
  (`at-jam user oidc <user> <issuer>:<subject>`; see
  [comms-addressing.md](comms-addressing.md)) who is a member of at least one
  project. Users are Jam-wide, so the Inbox spans all their projects.
- **Admin** — the login's access token verifies with `require-scope`, exactly
  as for the admin API ([operators.md](operators.md)), or the request is
  loopback. Jam's own config never grants admin.
  <a id="empty-require-scope"></a>**With `operator-auth.oidc.require-scope`
  empty, any access token for the audience verifies, so every signed-in user —
  Inbox members included — is an admin.** Set `require-scope` to keep the
  console to operators; `at-jam serve` warns at startup when browser login is
  configured without it.

`/app/user/*` needs a user, `/app/admin/*` an admin. How you reach it:

- **Loopback** (local host, or an SSH tunnel to the admin port) — always an
  admin, with or without `operator-auth.oidc`. With no session you are the
  anonymous operator `local` (and the Inbox asks you to sign in); signed in, you
  keep your session's identity and gain admin, so the console knows *who* you
  are (the [role Request](ui-editing.md#runtime-studios) action needs this). A
  missing or expired session falls back to `local` — no login redirect. The
  request's `Host` must be a loopback literal (`127.0.0.1`/`::1`/`localhost`)
  or listed in `ui-hosts` (see [serve.md](serve.md)), else it is refused as a
  possible DNS-rebinding attempt. For UI development,
  [`dev-identity`](serve.md) makes loopback requests act as a chosen user.
- **Off-loopback, with a `browser-client-id`** in `operator-auth.oidc` — no
  session redirects to an OIDC **Authorization Code + PKCE** login
  (`/app/auth/login` → your IdP → `/app/auth/callback`) asking for `openid`
  plus `require-scope`. The session is two cookies — `jam_app_id` (ID token)
  and `jam_app_at` (access token), `Path=/app`, HttpOnly, Secure under TLS,
  SameSite=Lax — re-verified on every request; they never reach `/admin`.
- **Off-loopback, without a `browser-client-id`** — refused. The admin API
  is still reachable with a bearer token.

A valid login that is **neither** a user nor an admin gets **403** on every
page, `/app/` included — never a login redirect, which would loop. So does a
missing requirement (a user-only principal on `/app/admin/`, an admin-only one
on `/app/user/`). The 403 page says which, and offers **Sign in again**
(`/app/auth/logout`, then login): the two tokens can expire separately — a
member admin whose access token lapsed is briefly a user only.

Browser login needs TLS. Upgrading from the old `/ui` and `/me` logins: see
[Migrating](#migrating-from-ui-and-me).

The UI never renders a token hash, launch secret, or credential value, except
the identity token shown once at enroll ([ui-editing.md](ui-editing.md#roster-and-roles)).

### Migrating from /ui and /me

- **Before deploying**, register `https://<jam-host>/app/auth/callback` at the
  IdP. Keep the old `/ui/auth/callback` and `/me/auth/callback` until the
  rollout settles; the previews callback is unchanged.
- The login now requests `openid` plus `require-scope`. Check that your IdP
  issues tokens to users not entitled to that scope; one that rejects the
  authorize request locks out Inbox-only users. See also
  [empty `require-scope`](#signing-in-one-login-one-principal) (above): it makes
  everyone an admin.
- Everyone signs in once more (the old `jam_session` and `jam_participant`
  cookies are ignored). Reload open tabs: an old tab posts to `/me/send`, which
  now answers 404.
- Old `/ui/*` and `/me/*` bookmarks get a `308` to the new path for one release
  (cached for an hour). `/admin/*` and `/me/studios/*` are unchanged.

### Sending (`POST /app/user/send`)

`POST /app/user/send` is the participant's send — the human analog of the agent
`send` tool. Like every write under `/app/user/`, it must come from the page's
own origin (or a `ui-origins` entry). It takes a JSON body `{"to": "<recipient>", "body": "<text>"}`, plus
an optional `"content_type"` (`text/markdown` by default, or `text/plain`; see
[content type](intercom.md#content-type-markdown-or-plain-text)), and returns
**204** on success. It writes to the **same** durable squawk Log the
agent `send` tool and the relay ingress write (never a parallel path); the UI is
an in-process Log writer, not an egress engine.

- **Identity is the resolved session**, never the body: the sender is the
  principal's user id.
- **`to` is a channel or a new conversation** — an open conversation's channel id
  (`chn_…`), or `user:<id|name>` / `session:<id>` to start (or reuse) a chat with
  a member of one of the person's projects or a live session in one. The
  [intercom](intercom.md#enabling-it) decides whether they may post there (a
  chat they're in; a ticket or room of a project they belong to — they join it)
  and records who hears it; a waiting session among them **wakes** exactly as on
  a relayed reply.
- **Errors mirror the agent send** (`/squawks`): a channel they may not post in,
  or that doesn't exist, → **403** (alike); a person or session that doesn't
  resolve → **404**; an append failure → **502**; an empty `to`/`body` → **400**.

## Intercom

The Intercom page (`/app/admin/intercom`) is a read-only view of Jam's
[channel log](intercom.md#enabling-it) (always available; it lives in Postgres — see
[serve.md](serve.md#postgres-store-store-postgres)). It shows a filterable, newest-first table of squawks,
each with its sender and channel (`<label> · <kind>`, linking to that channel's
squawks): filter by project, participant (an id — a user, session, account or
channel), a body substring, and a date window (the `since`/`until` bounds are interpreted as UTC
day boundaries; a malformed date is ignored, with a notice, rather than
silently applied). Filters live in the URL, so a filtered view is shareable via
link.

The page is a manual-refresh snapshot, not a live tail — reload to see new
squawks. The **Legacy** tab (`?log=legacy`) shows the log from before the
channel log, frozen, as it always did: filter by `kind:ref` participants, each
recipient badged internal or external.

Unlike the roster/kit/destination pages, Intercom has no mutation — the UI only
reads the Log (still a full snapshot per load — pagination is a later phase).

## Session timeline

`/app/admin/agents/{id}/session` (linked from running-agents tables and the agent's page) shows a managed
studio's agent session: a stream selector (current and past streams), header
totals (turns = results answered, episodes, tool calls, tokens in/out, cost = last total per episode), and a flat event list, each
event tagged with its turn (`tN`) (text, thinking, tool use/results expandable, results, gap and truncation
markers), with a raw-JSON toggle. `system`/`thinking_tokens` events are hidden
behind **show progress events**. It updates live over SSE from `/app/admin/agents/{id}/session/events` (backfill, then
live; reconnects resume via `Last-Event-ID`). Storage, retention,
and sensitivity: [session-events.md](session-events.md).

## Editing

The UI's writes — enrolling and granting, roles, raising and tearing down
studios, **Request**, and the kit/destination/model-spec registry — with their
gate, CSRF and audit rules, are in [ui-editing.md](ui-editing.md).
