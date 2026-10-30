---
summary: The Jam app — the one browser UI `jam serve` serves under `/app` (the admin console at `/app/admin/`, the user Inbox at `/app/user/`) behind one OIDC login; loopback is always an admin. Covers the routes and legacy redirects (old `/ui`·`/me` URLs and the old admin pages' paths), the title bar and shared look, installing the app, the login and Principal (user / admin / both), and the `POST /app/user/send` path; the console's pages are in ui-console.md and what it can change in ui-editing.md.
read_when: You want to expose Jam in a browser, understand who may reach /app/user vs /app/admin and why you got a 403, configure browser login (redirect URI, scopes, cookies), read the title bar, install the app, or follow an old /ui, /me or old admin-page link. For what the console pages show (agents, search, Channel log, session timeline) read ui-console.md; to change something from the UI, read ui-editing.md.
owns: the `/app` route table, the legacy `/ui`·`/me` 308 redirects and the old admin pages' 302 redirects; the `/app` gate (one browser OIDC login, its two session cookies, the Principal — user, admin, both — loopback trust, the 403 page); the `/app` title bar (blended tabs, principal menu, narrow-screen behaviour) and the shared `jam.css` look; installing the app (manifest, service worker, icons); and the `/app/user/` area's identity rules and the `POST /app/user/send` participant send path
prereqs: serve.md for the admin listener + the off-loopback fail-closed rule; roster.md for the RBAC model these edits act on; coves.md for the managed-cove lifecycle the runtime actions drive; comms-addressing.md for the squawk targets/wake-on model the send path writes into; INDEX.md for the service overview
tier: leaf
updated: 2026-10-30
---

# The Jam app (`/app/`)

`jam serve` serves one browser app on the same **admin listener** as the
JSON admin API. Point a browser at the admin URL; `/` and `/app/` land on the
Inbox for a roster member, else on the admin console:

```
http://127.0.0.1:8081/app/
```

| Path | Holds |
|---|---|
| `/app/user/*` | the Inbox — the Jam app ([intercom-ui.md](intercom-ui.md)) and its API — and [`POST /app/user/send`](#sending-post-appusersend) |
| `/app/admin/*` | the admin console, the Jam app's admin area ([ui-console.md](ui-console.md)); its JSON API is `/app/admin/api/` ([web-app.md](../../web-app.md#admin-area)) |
| `/app/auth/{login,callback,logout}` | the one browser login (ungated) |
| `/app/auth/app-token`, `/app/auth/app-token/exchange` | sign-in for a client such as VS Code ([app-tokens.md](app-tokens.md)) |
| `/app/static/*` | `jam.css` (for the 403 page), the brand mark and app icons (ungated) |
| `/app/manifest.webmanifest`, `/app/sw.js` | the [installable app](#installing-the-app)'s manifest and service worker (ungated) |

`/me/studios/*` ([collaboration.md](collaboration.md)) and `/admin/*` are APIs
and stay where they are. **Old URLs:** a `GET`/`HEAD` on a former `/ui/*` or
`/me/*` page (login and static included) answers `308` to its `/app/...`
equivalent with the query kept — `/ui/agents?status=running` →
`/app/admin/agents?status=running`, `/ui/users/u` → `/app/admin/users/u`
(which [goes on](#old-admin-pages) to `/app/admin/config/users/u`), `/me/?c=chn_x` → `/app/user/?c=chn_x` (which the app
turns into `/app/user/c/chn_x`). Any other method on an old path is 404, never
replayed. The redirects last one release. `/app/next/*`, the app's preview
address, redirects to `/app/user/*` the same way.

### Old admin pages

The console's server-rendered pages are gone; a `GET` or `HEAD`
on an old page path answers `302` to its new page, with the query kept
(except `project` on an agent path; a draft's `file` and `diff` become the
kit page's `dfile` and `ddiff`). The gate runs first, so a non-admin
gets its 403 and learns nothing. A write there gets a 405, never a page.

| Old path (under `/app/admin/`) | New path |
|---|---|
| `config` | `config/tracking` |
| `users[/…]`, `destinations[/…]`, `model-specs[/…]` | `config/` + the same |
| `projects/<p>[/members\|agents\|intercom\|escalation]` | `config/projects/<p>[/…]` |
| `projects/<p>/roles` | `config/projects/<p>/agents` |
| `projects/<p>/roles/<r>[/setup]`, `roles/<p>/<r>` | `config/projects/<p>/agents/<r>` |
| `kits/<k>[/draft]` | `config/kits/<k>` |
| `specs` | `config/kits` |
| `intercom` | `health/intercom` |
| `agents/<id>/setup` | `agents/<id>` |
| `coves[/<id>[/session]]`, `actors` | `agents[/<id>[/session]]` |
| `roles`, or any other old path | the Dashboard |
| `next/<path>` (the app's preview address) | `<path>` |

## The title bar and look

Every `/app` page opens with one **title bar**: the Jam's name (`<name> Jam`
with a [`display-name`](serve.md#the-serve-config), else `Jam`), the **tabs**
you may use, then a theme toggle (light, dark or the OS setting) and your
account menu (name and roles — member / admin — and **Sign out**; a member's
per-device **Notifications** and sounds in the Inbox when [push](push.md) is
on; a loopback admin with no login reads "Local admin" and has no sign-out).
The tabs are blended: **Inbox** for a member; **Dashboard · Config · Agents ·
Health** for an admin, each with its [attention badge](ui-attention.md); the
page's own tab is highlighted. In the console the bar also holds the
[search box](ui-console.md#search).

**One look.** Both areas are the Jam app (React and Mantine, built from
`web/app` — [web-app.md](../../web-app.md)), with a theme carrying the
accent and fonts of `jam.css` (`internal/jam/uiassets`), which now styles
only the server-rendered 403 page.

## Installing the app

The Jam app is an installable web app. Over **HTTPS** (or on `localhost`), use
the browser's own **Install** (Chrome, Edge), **Add to Home Screen** (iOS,
Android) or **Add to Dock** (Safari on macOS); it opens in its own window at
`/app/`, named after the Jam (`<name> Jam`, else `Jam`; the short name is
`Jam`). There is no in-app install prompt. Plain HTTP off localhost can't
install: the browser has no service worker there, and the page skips it quietly.

- **Nothing is cached** — the service worker is network-only, so the installed
  app is online-only and always current. It exists so the app is installable,
  and to show [push notifications](push.md) and the app badge (on iOS only the
  Home Screen app gets them).
- **Public files** — `/app/manifest.webmanifest`, `/app/sw.js` and the icons
  under `/app/static/icons/` are served without the gate (the browser fetches
  them without cookies); every other `/app/` path stays gated.
- **Icons** are PNGs rendered from the J mark and committed under
  `internal/jam/uiassets/static/icons/` (and the VS Code extension's
  `web/vscode/media/icon.png`); regenerate them with `just icons` after
  changing the mark (a test fails when they drift). The extension also ships the
  mark as `media/jam-color.svg` (board tabs, a copy of `brand/j-logo-plain.svg`)
  and `media/jam.svg` (the same shapes in `currentColor`, for the activity bar
  and panel, which tint it); update both by hand with the mark.

## Signing in: one login, one Principal

Every `/app` page sits behind one gate (the exceptions are listed under
[Installing the app](#installing-the-app) and in the route table), separate from
the JSON admin API's authenticator (see the fail-closed rule in
[serve.md](serve.md#exposing-the-admin-api-fail-closed)). It resolves each
request to a **Principal** — a **user**, an **admin**, or both:

- **User** — the login's ID token `(issuer, subject)` is bound to a Jam user
  (`jam user oidc <user> <issuer>:<subject>`; see
  [comms-addressing.md](comms-addressing.md)) who is a member of at least one
  project. Users are Jam-wide, so the Inbox spans all their projects.
- **Admin** — the login's access token verifies with `require-scope`, exactly
  as for the admin API ([operators.md](operators.md)), or the request is
  loopback. Jam's own config never grants admin.
  <a id="empty-require-scope"></a>**With `operator-auth.oidc.require-scope`
  empty, any access token for the audience verifies, so every signed-in user —
  Inbox members included — is an admin.** Set `require-scope` to keep the
  console to operators; `jam serve` warns at startup when browser login is
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

## Editing

The UI's writes — enrolling and granting, roles, raising and tearing down
studios, **Request**, and the kit/destination/model-spec registry — with their
gate, CSRF and audit rules, are in [ui-editing.md](ui-editing.md).
