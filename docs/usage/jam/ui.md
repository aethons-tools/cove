---
summary: The Jam admin UI — a server-rendered web view of the agents and their studios, the projects, users and specs, and the durable squawk Log, served by `at-jam serve`; reachable on loopback always, and off-loopback via browser OIDC login. Covers the rail of scopes (Jam and each project), their tabs, the list pages, search, the Intercom log, the session timeline and the participant /me/ surface; what the UI can change is in ui-editing.md.
read_when: You want to watch a running Jam in a browser — the agents and their studios, the squawk Log, a session timeline, the projects/users/specs — find your way around the UI (nav, sub-tabs, search), use the participant /me/ page, or configure browser login for it. To change something from the UI, read ui-editing.md instead.
owns: the `/ui/agents/{id}/session` timeline page; the `/ui/` observability surface (the rail, Jam's tabs and the Specs sub-tabs, what each list shows, search, how to reach it, its loopback + browser-OIDC-login exposure); and the participant `/me/` surface (its OIDC-always/no-loopback gate, reuse of the operator browser client, the operator/participant boundary, and the `POST /me/send` participant send path)
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
tier: leaf
updated: 2026-10-07
---

# The Jam admin UI (`/ui/`)

`at-jam serve` serves a web UI on the same **admin listener** as the JSON
admin API. Point a browser at the admin URL and open `/ui/` (`/` redirects
there):

```
http://127.0.0.1:8081/ui/
```

A permanent **rail** on the left lists **Jam** (or the serve config's
[`display-name`](serve.md#the-serve-config)), then every project, then **+ New project**; the selection decides the **tabs** over the content. Jam's tabs
are **Dashboard · Agents · Users · Specs · Intercom**; a project's are
**Overview · Members · Agents · Roles · Intercom · Escalation**
([ui-projects.md](ui-projects.md)). A detail page sits under its tab (a role
page: its project's Roles; a kit: Jam's Specs) with a breadcrumb below it.
**Specs** (`/ui/specs`, opening on Kits) groups **Kits · Destinations ·
Model-specs** under a sub-tab strip. On a narrow screen the rail is a drawer
(☰ in the title bar). Rail entries, tabs and rows carry **attention badges**
([ui-attention.md](ui-attention.md)). Jam's pages:

- **Dashboard** (`/ui/`) — summary tiles: live / raising / lost-or-terminating /
  idled agents, each opening the Agents list filtered to that phase, and counts
  of projects, agents, users and specs (kits + destinations + model-specs) —
  agents, users and specs open their tab, projects are listed in the rail; Jam's **Needs attention** card; then the Jam-wide **Session context** card
  ([ui-pages.md](ui-pages.md#session-context-cards)), above the studio table.
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
  `/ui/search?q=…`, which updates as you type. Session events aren't searched.
- **Agents** (`/ui/agents`) — each enrolled identity and each studio, one row
  per id, with its **kind** (`standing`, `personal`, `ticket` — a session with
  a unit, `manual`, or `enrolled` — no studio), project/role, phase, activity,
  connector and image status ([coves.md](coves.md#the-studio-verbs)); it
  **auto-refreshes every 3 seconds**. Phase filters
  (`?phase=live|raising|idled|attention`) match the dashboard tiles. Enroll,
  raise and teardown: [ui-editing.md](ui-editing.md). Each id opens the agent's
  page ([ui-pages.md](ui-pages.md#agent-pages)); `/ui/coves…` and `/ui/actors`
  redirect here.
- **Intercom** (`/ui/intercom`) — a read-only, filterable, newest-first table of
  the channel log, with the frozen legacy log on a Legacy tab. See
  [Intercom](#intercom) below.
- **Users / Kits / Destinations / Model-specs** — the control-plane objects as
  tables, all editable from here; roles live in their project — see
  [ui-editing.md](ui-editing.md).

Every table has a fixed order — agents and studios by id; roles by project, then
name; kits and destinations by name; squawks newest-first — so rows don't shuffle
across a poll or an edit, and the JSON admin API and CLI lists match it.

**One look for `/ui` and `/me`.** Both UIs take their colors (light and dark,
following the OS setting) and typography from one stylesheet, `jam.css`, in
`internal/jam/uiassets`, which also holds the single `htmx` copy. Each UI serves
them under its own prefix (`/ui/static/`, `/me/static/`), so neither gate
reaches the other. Change a color there and both UIs follow; each UI keeps its
own component styles in its layout.

## Reaching the UI

The UI has its own gate, separate from the JSON admin API's authenticator (see
the fail-closed rule in [serve.md](serve.md#exposing-the-admin-api-fail-closed)):

- **Loopback** (local host, or an SSH tunnel to the admin port) — reachable with
  no login: the local operator is trusted. This holds whether or not
  `operator-auth.oidc` is configured. The request's `Host` must be a loopback
  literal (`127.0.0.1`/`::1`/`localhost`) or a host listed in `ui-hosts` — if you
  reach the UI over a custom name that DNS-binds to loopback (e.g.
  `jam.local.example`), add it to `ui-hosts` (see [serve.md](serve.md)) or the
  UI refuses it as a possible DNS-rebinding attempt.
  A loopback viewer is the anonymous operator `local`, unless they have signed
  in via `/ui/auth/login`: a valid session is used even on loopback, so the UI
  knows *who* you are (the [role Request](ui-editing.md#runtime-studios) action needs this).
  A missing or expired session falls back to `local` without a login redirect.
  For UI development, [`dev-identity`](serve.md) makes loopback requests act as
  a chosen user on `/ui` and `/me` with no login at all.
- **Off-loopback, with a `browser-client-id`** set in `operator-auth.oidc` — the
  browser is redirected through an OIDC **Authorization Code + PKCE** login
  (`/ui/auth/login` → your IdP → `/ui/auth/callback`); on success a session cookie
  (the API access token; HttpOnly + Secure + SameSite=Lax) lets you browse until
  it expires, then you re-login. `require-scope` is enforced on every request,
  exactly as for the admin API. See [operators.md](operators.md).
- **Off-loopback, without a `browser-client-id`** — the UI is refused. The
  programmatic admin API is still reachable with a bearer token.

Register `https://<your-jam-host>/ui/auth/callback` in your IdP's Allowed
Callback URLs. Browser login needs TLS (the session cookie is `Secure`).

The UI never renders a token hash, launch secret, or credential value; the one
exception is the identity token shown once at enroll time
([ui-editing.md](ui-editing.md#roster-and-roles)) — and the
Intercom view, which shows comms bodies (agent/human squawks), not secrets. The
login routes themselves never expose mutation.

## The participant intercom (`/me/`)

`/me/` is a **separate, participant-facing** surface on the same admin listener,
distinct from the operator `/ui/`. It is where a **project member** — not the
operator — reads and replies to their intercom channels: the two-pane inbox UI
(see [intercom-ui.md](intercom-ui.md)) and the `/me/send` path are mounted here.
It has its own gate, and a participant session
**never** carries operator scope and cannot reach the `/admin/*` or `/ui/*`
routes (the participant cookie is path-scoped to `/me`, and `/admin`/`/ui` are
gated independently).

Auth differs from the operator UI in two deliberate ways:

- **Always requires OIDC login — no loopback bypass.** Unlike `/ui/` (where a
  loopback request is trusted as the local operator), `/me/` must know *which*
  human you are, and loopback cannot say — so even a local request logs in via
  `/me/auth/login`. The operator god-view stays the loopback affordance.
- **Reuses the operator's `browser-client-id`.** There is no separate IdP client
  to configure; operator vs participant is decided by mapping the login's OIDC
  subject to a user, not by the client. `/me/` is mounted only when
  browser login (`operator-auth.oidc.browser-client-id`) is configured.

The session (cookie `jam_participant`, Path `/me`) is the ID token, verified
against the browser client id; its `(issuer, subject)` is matched to a user's
OIDC binding (bind one with `at-jam user oidc <user> <issuer>:<subject>`; see
[comms-addressing.md](comms-addressing.md)). Users are Jam-wide, so the
participant's view spans every project they are a member of. An unbound
subject — or a user who is a member of no project — is refused with **403** (fail closed), not
redirected back to login (which would loop); the operator adds the binding to
let them in.

### Sending (`POST /me/send`)

`POST /me/send` is the participant's send — the human analog of the agent
`send` tool. It takes a JSON body `{"to": "<recipient>", "body": "<text>"}`, plus
an optional `"content_type"` (`text/markdown` by default, or `text/plain`; see
[content type](intercom.md#content-type-markdown-or-plain-text)), and returns
**204** on success. It writes to the **same** durable squawk Log the
agent `send` tool and the relay ingress write (never a parallel path); the UI is
an in-process Log writer, not an egress engine.

- **Identity is the resolved session**, never the body: the sender is the
  gate-injected participant's user id.
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

The Intercom page (`/ui/intercom`) is a read-only view of Jam's
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

`/ui/agents/{id}/session` (linked from studio tables and the agent's page) shows a managed
studio's agent session: a stream selector (current and past streams), header
totals (turns = results answered, episodes, tool calls, tokens in/out, cost = last total per episode), and a flat event list, each
event tagged with its turn (`tN`) (text, thinking, tool use/results expandable, results, gap and truncation
markers), with a raw-JSON toggle. `system`/`thinking_tokens` events are hidden
behind **show progress events**. It updates live over SSE from `/ui/agents/{id}/session/events` (backfill, then
live; reconnects resume via `Last-Event-ID`). Storage, retention,
and sensitivity: [session-events.md](session-events.md).

## Editing

The UI's writes — enrolling and granting, roles, raising and tearing down
studios, **Request**, and the kit/destination/model-spec registry — with their
gate, CSRF and audit rules, are in [ui-editing.md](ui-editing.md).
