# Jam app: one UI under `/app`, installable as a PWA — design

**Status:** approved (2026-10-22); plan: `docs/superpowers/plans/2026-10-22-jam-app-slices-1-3.md`.
**Scope:** merge the participant intercom (`/me`, `internal/jam/meui`) and the
operator console (`/ui`, `internal/jam/adminui`) into one browser app under
`/app`, with one login, one title bar and blended tabs; then make that app an
installable Progressive Web App. Push notifications are sketched here (§7) and
get their own spec.
**Does not change:** the admin API (`/admin/*`), the bearer APIs that live under
`/me` (`/me/studios/*`), write endpoint semantics, the store, or
offline behaviour (none — out of scope).
**Builds on:** [`2026-09-28-jam-intercom-ui-design.md`](2026-09-28-jam-intercom-ui-design.md),
[`2026-10-07-admin-ui-ia-design.md`](2026-10-07-admin-ui-ia-design.md),
[`2026-10-07-admin-ui-rail-design.md`](2026-10-07-admin-ui-rail-design.md),
[`2026-09-13-harbor-ui-oidc-login.md`](2026-09-13-harbor-ui-oidc-login.md).

## 1. What and why

Jam has two browser UIs on one origin that know nothing of each other: `/me`
(participant intercom: inbox, asks, boards) and `/ui` (operator console). Each
has its own OIDC login, cookie, gate, layout and top bar, and neither links to
the other. A person who is both a project member and an operator signs in twice
and juggles two apps.

The goal is **one Jam app** a person installs once (home screen / dock, own
window), whose navigation shows exactly what that person may use, and which
later delivers **push notifications** for squawks and asks.

Sub-projects, in order — each gets its own plan:

1. **One Jam app** (§2–§5) — merge the UIs.
2. **Installable** (§6) — manifest, icons, minimal service worker.
3. **Push notifications** (§7) — own spec after 1 and 2 ship.

## 2. Routes

Every browser page moves under `/app`. The prefixes pick the authorization check;
users experience one app (§4).

| Path | Was | Holds |
|---|---|---|
| `/app/user/*` | `/me/*` (UI routes) | inbox, rail, stream, send, read, join/leave/call-in, asks, uploads, blobs, boards, presence, events |
| `/app/admin/*` | `/ui/*` | dashboard, projects, agents, users, specs, config, health and their htmx fragments |
| `/app/auth/{login,callback,logout}` | `/ui/auth/*`, `/me/auth/*` | the one OIDC login |
| `/app/static/*` | `/ui/static/`, `/me/static/` | `jam.css`, htmx, brand marks, icons |
| `/app/manifest.webmanifest`, `/app/sw.js` | — | PWA files (§6) |
| `/`, `/app/` | `/` → `/ui/` | redirect to the landing page: `/app/user/` for a roster member, else `/app/admin/` |

**Old URLs.** Every `GET` on a former `/ui/*` or `/me/*` UI route answers `308`
to its `/app/...` equivalent, query string preserved. The redirects live for one
release, then are deleted.

**What stays.** `/me/studios/*` (collaboration bearer API) and `/admin/*` are
APIs, not UI; they do not move. (`/me/send` is cookie-gated and used by the
composer, so it moves to `/app/user/send`.)

**Package rename.** `internal/jam/meui` becomes `internal/jam/userui` in the move,
matching `adminui`.

## 3. Login and authorization

**One login.** One OIDC client, one session in two cookies (`jam_app_id` for the
ID token, `jam_app_at` for the access token — two JWTs can exceed one cookie's
4 KB; `Path=/app`, HttpOnly, Secure under TLS, SameSite=Lax), one gate in front of `/app/` (except the
public PWA files, §6). The login requests `openid` plus the operator scope
configured as `operator-auth.oidc.require-scope`. The server keeps both the ID
token (roster identity) and the access token (operator scope).

**Principal.** The gate resolves each request to a `Principal` on the request
context, owned by a new package `internal/jam/appui`:

```
Principal{ User *jam.Participant  // roster member of ≥1 project, or nil
           Admin bool              // operator scope present, or loopback trust
           Local bool }            // admin by loopback trust, no login
```

- **User** — resolved as today's `ParticipantByIdentity` (fail closed: unbound
  identity or no project memberships → nil).
- **Admin** — the access token carries the configured operator scope (today's
  `OIDCAuthenticator` check). Admin status comes from the IdP only; Jam's own
  config cannot grant it.
- **Loopback trust** — a loopback request with no session is `Admin, Local`
  (as `/ui` is today). A loopback request with a session also gains `Admin`.
- **Sign-in admission** — a login succeeds if the principal is a user, an admin,
  or both. Neither → a "not authorized" page (403).

**Authorization.** `/app/user/*` requires `User != nil`; `/app/admin/*` requires
`Admin`. A missing requirement on a page → 403 page; with no session at all
off-loopback → redirect to `/app/auth/login`. The existing same-origin guards
(`RequireSameOrigin`, `originGuard`) keep covering every write.

**Ops step.** The OIDC client's registered redirect URI becomes
`…/app/auth/callback`; the IdP must allow it before deploy.

## 4. Title bar and frame

One **title bar** on every `/app` page, rendered from a shared template owned by
`appui` and included by both `userui` and `adminui` layouts:

`[logo] {display name} Jam   Inbox · Dashboard · Projects · Agents · Users · Specs · Config · Health   …   [principal menu]`

- **Logo** — the plain J mark (§6.2), linking to `/app/`.
- **Name** — the configured Jam display name (today's console brand source).
- **Tabs — blended, filtered by principal:**

  | Tab | Shown to | Goes to |
  |---|---|---|
  | Inbox | user | `/app/user/` |
  | Dashboard | admin | `/app/admin/` |
  | Projects | admin | `/app/admin/projects` |
  | Agents | admin | `/app/admin/agents` |
  | Users | admin | `/app/admin/users` |
  | Specs | admin | `/app/admin/specs` (sub-tabs Kits · Destinations · Model-specs unchanged) |
  | Config | admin | `/app/admin/config` |
  | Health | admin | `/app/admin/health` |

  Tabs a principal may not use are not rendered. Each page declares its tab; the
  current one is highlighted. Attention badges move from the rail onto the
  admin tabs.
- **Principal menu** (far right) — avatar and name; opens to name, roles
  (member / admin) and **Sign out**. A `Local` admin shows "Local admin" with no
  sign-out.

**Below the title bar:**

- **Inbox** keeps its two-pane layout (conversation rail + conversation). The
  Rendered/Raw toggle and **New message** move into the conversation rail's
  header; the old intercom top bar is removed.
- **Projects** keeps a left rail listing projects with attention badges; the
  selected project's own tabs sit under the title bar as today.
- **Every other admin tab** is full-width with no rail. The console's search
  box and narrow-screen rail toggle sit in a slim bar under the title bar; the
  old console top bar is removed.
- **Intercom (admin)** — the Jam-wide read-only channel log leaves the top tabs
  and moves under **Health** as "Channel log".

Visual polish beyond this is deferred to a later UI pass.

## 5. Package layout

| Package | Owns |
|---|---|
| `internal/jam/appui` | the gate, `Principal`, `/app/auth/*` wiring, landing redirect, old-URL redirects, title bar template + data, PWA file handlers |
| `internal/jam/userui` | today's `meui`, routes rebased to `/app/user` |
| `internal/jam/adminui` | today's console, routes rebased to `/app/admin`, rail limited to Projects |
| `internal/jam/uiassets` | static assets, now also brand marks and generated icons |
| `internal/jam/browserauth` | OIDC client and session cookie, reduced to one mount |

`cmd/at-jam` mounts one `appui` handler where it mounts `/ui` and `/me` today.

## 6. Installable

### 6.1 Manifest and service worker

- **`/app/manifest.webmanifest`** — rendered per Jam: `name` "{display name} Jam",
  `short_name` "Jam", `scope` and `start_url` `/app/`, `display` `standalone`,
  `theme_color` `#2F67B2` (the logo blue, chosen 2026-10-23), `background_color` `#F5F6F8` (light) / `#0F1116`
  (dark, via a `media` theme-color meta), icons (§6.2). Linked from every page's
  `<head>`.
- **`/app/sw.js`** — served from `/app/` so its scope is the app. Minimal:
  `install`/`activate` only, no `fetch` handler (so every request goes to the
  network); caches nothing. It exists for
  installability and as the home for push (§7). A small registration snippet
  lives in the shared title-bar template.
- **iOS** — `apple-touch-icon`, `apple-mobile-web-app-capable`,
  `apple-mobile-web-app-title`.
- **Unauthenticated.** Browsers fetch the manifest, icons and service worker
  without credentials, so these files sit outside the gate. They are static,
  public, and versioned by content hash. Every other `/app/` path is gated.
- **HTTPS.** Install needs a secure context; Jam already requires TLS for any
  off-loopback admin listener, and loopback is a secure context.
- **Prompt.** No custom install prompt; browsers' own "Install app" / "Add to
  Home Screen" / "Add to Dock" is used. An "Install" item in the principal menu
  is a possible follow-up.

### 6.2 Brand marks and icons

Source marks (from branch `bam/logo`, text converted to outlines):

- `assets/j-logo.ai` — the Illustrator source; stays in `assets/`.
- `j-logo.svg` (J, dot and "Jam" wordmark) and `j-logo-plain.svg` (J and dot)
  — move to `internal/jam/uiassets/static/brand/` so `embed` can reach them;
  one copy only.
- The hand-made PNGs under `assets/` are deleted.

A pure-Go generator (`go generate` / `just icons`, rasterizing with
`github.com/srwiley/oksvg` + `rasterx`; no system tools) renders
`j-logo-plain.svg` into `uiassets/static/icons/`:

| File | Size | Background |
|---|---|---|
| `favicon-32.png` | 32 | transparent |
| `icon-192.png`, `icon-512.png` | 192, 512 | transparent |
| `icon-maskable-512.png` | 512 | white, mark inside the 80% safe zone |
| `apple-touch-icon.png` | 180 | white (iOS renders transparency black) |

Generated files are committed; a test regenerates and fails on drift. The title
bar uses `j-logo-plain.svg` directly.

## 7. Push notifications (sketch — own spec)

- **Opt-in** — "Enable notifications" in the principal menu requests permission;
  the service worker subscribes; subscriptions are stored per user (many
  devices), pruned on 404/410 from the push service.
- **Server** — VAPID key pair generated once and held with Jam's secrets; a Web
  Push library sends. The Jam host needs egress to the browser push services
  (FCM, Mozilla autopush, Apple).
- **Triggers** — a new message in a conversation the user is in, not sent by
  them; an ask addressed to them; being called into a channel.
- **Payload** — title, short preview, deep link `/app/user/?c=<channel>`;
  coalesced per channel; suppressed when a visible client is open.
- **Hook** — today's `Changes.Subscribe()` is a payload-less coalescing signal;
  push needs a per-message subscription (channel, sender, recipients) on the
  intercom log, designed in the push spec.

## 8. Testing

`httptest`, in the style of today's `adminui` / `meui` tests:

- **Gate matrix** — {member, admin, both, neither, loopback, loopback+member} ×
  {`/app/user/*`, `/app/admin/*`, `/`, `/app/`} → expected 200 / redirect / 403.
- **Title bar** — per principal: exactly the right tabs, current tab marked,
  principal menu contents (sign-out absent for `Local`).
- **Redirects** — every former `GET /ui/*` and `/me/*` UI route → 308 to its
  `/app` path, query preserved (`/me/send` → `/app/user/send`); `/me/studios/*`
  and `/admin/*` unchanged.
- **CSRF** — cross-origin writes refused on both areas.
- **PWA** — manifest is valid JSON with the right scope, start URL and icons;
  manifest, `sw.js` and icons served ungated with correct content types; every
  other `/app/` path gated; icons match the SVG.
- **Manual** — install from Chrome against a loopback dev Jam (standalone window,
  icon); smoke-test iOS Safari "Add to Home Screen".

## 9. Delivery

Each slice is one PR, shippable alone, docs updated in the same PR
(`docs/usage/jam/ui.md`, `intercom-ui.md`, operator-auth docs):

1. **One login** — `appui`: the gate, `Principal`, `/app/auth/*`, one session (two cookies),
   fully tested but not yet mounted (its `/app/`-scoped cookie can't cover
   `/ui` and `/me`, so mounting waits for the move).
2. **Move** — mount `appui`; `meui` → `userui`; routes to `/app/user` and
   `/app/admin`; old-URL redirects; landing redirect; retire the two old logins.
3. **Title bar + blended tabs** — logo files from `bam/logo` moved into
   `uiassets/static/brand/`; shared title bar and principal menu; admin IA
   change (rail only on Projects); Intercom → Health "Channel log"; old top bars
   removed.
4. **Installable** — icon generator, manifest, service worker, iOS meta.

Then push (§7) gets its own spec.
