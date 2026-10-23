# Jam app: push notifications — design

**Status:** approved in brainstorming (2026-10-23).
**Scope:** Web Push notifications for the Jam app (`/app`, installable since #455):
people opt in per device and get a notification for new messages in their
conversations, the way Slack does.
**Does not change:** the Linear and Discord relays, the intercom read model,
the `/app` gate, offline behaviour (still none).
**Builds on:** [`2026-10-22-jam-app-pwa-design.md`](2026-10-22-jam-app-pwa-design.md) §7
(sketch), the relay engine (`internal/relay`), the channel log.

## 1. Intent

Keep a person's attention on active sessions: when an agent they're working with
posts (e.g. "review of PR #812 done"), they should know at once, on whatever
device they have, rather than finding it later. Follow Slack: noisy is fine,
nothing gets missed.

## 2. What notifies

A new squawk notifies a **user** (never a session or an unlinked account) when
all hold:

1. they are in the squawk's audience — a member of its channel who can see it,
   and not its author;
2. the squawk is
   - in a **chat** or a **session channel**, or
   - an **ask** whose `Of` names them (any channel kind), or
   - a **call-in** notice inviting them (any channel kind), or
   - an **escalation** notice targeting them (any channel kind);
3. it is not an ingested echo the person authored elsewhere (`m.From` is not them);
4. they are **not focused on that channel** right now (§6).

Rooms and ticket channels do not notify (except 2's asks / call-ins /
escalations). Ask amend/withdraw/answer events do not notify. Session turn-end
without a message does not notify — agents report completion by message.

## 3. Routing (Web Push)

Standard Web Push: the browser subscribes with its vendor's push service (Google
FCM, Mozilla autopush, Apple, Microsoft WNS) using Jam's **VAPID** public key and
gets an endpoint URL plus encryption keys; Jam stores them; to notify, Jam
encrypts the payload (RFC 8291, only the browser can read it), signs a VAPID JWT
with its private key, and POSTs to the endpoint; the service delivers over the
device's existing connection. No vendor account. The Jam host needs outbound
HTTPS to `fcm.googleapis.com`, `*.push.services.mozilla.com`, `*.push.apple.com`
and `*.notify.windows.com`. Library:
`github.com/SherClockHolmes/webpush-go` (MIT). iOS delivers only to the Home
Screen–installed app.

## 4. Configuration

```yaml
runtime:
  push:
    vapid-private-key-cred: jam-vapid   # names a `credentials:` entry (base64url P-256 private key)
    subject: mailto:ops@example.com     # VAPID "sub"; push services require one
    preview: true                       # default true; false ⇒ body "New message"
```

The private key is resolved through Jam's credential resolver (never on disk, in
argv or in logs); the public key is derived from it. `at-jam push keygen` prints a
fresh pair (private for the credential store, public for reference). Omitted
block ⇒ push off: no menu item, no relay, `/app/user/push/*` answers 404.
Validation: `vapid-private-key-cred` required and declared under `credentials:`;
`subject` required, `mailto:` or `https:`.

## 5. Subscriptions

Postgres table (`internal/jam/migrations/0017_push_subscriptions.sql`) with a
`MemStore` twin and a `storetest` conformance suite:

| Column | |
|---|---|
| `endpoint` | text, primary key |
| `user_id` | text, references `users(id)` |
| `p256dh`, `auth` | text — the browser's keys |
| `user_agent` | text — device label |
| `created_at`, `last_ok_at` | timestamptz |

Upsert by endpoint (a re-subscribe on the same device moves it to the current
user). Rows are deleted on unsubscribe, on a 404/410 from the push service, and
when the user is removed (`RemoveUser`).

**API** (under `/app/user/`, so member-only, gated, same-origin for writes):

| Route | |
|---|---|
| `GET /app/user/push/key` | `{"key": "<VAPID public key, base64url>"}` |
| `POST /app/user/push/subscriptions` | body = `PushSubscription.toJSON()`; 204 |
| `DELETE /app/user/push/subscriptions` | body `{"endpoint"}`; deletes only the caller's row; 204 |
| `POST /app/user/push/focus` | body `{"channel": "<id>" or null}`; 204 (§6) |

Endpoints must be `https:` URLs (≤ 2 KB) on the default port whose host is a
Web Push service (`fcm.googleapis.com`, `*.push.services.mozilla.com`,
`*.push.apple.com`, `*.notify.windows.com`; no IP literals — `jam.ValidPushEndpoint`,
re-checked at send); keys must decode to the right sizes; anything else is 400. Max 20 subscriptions per user (oldest evicted).

**UI.** The principal menu gains **Notifications: Off / On** for this device
(members only, push configured). On: `Notification.requestPermission()` →
`registration.pushManager.subscribe({userVisibleOnly: true, applicationServerKey})`
→ POST. Off: `subscription.unsubscribe()` → DELETE. Permission denied shows
"Blocked in browser settings". On iOS outside standalone mode the item reads
"Add Jam to your Home Screen to get notifications".

## 6. Focus suppression

While a conversation is open in a visible, focused window, the Inbox page posts
`/app/user/push/focus {channel}` on focus/visibility change and every 30 s; on
blur or hidden it posts `{channel: null}`. Jam holds an in-memory map
user → (channel, expires = now + 60 s); nothing is persisted. A user focused on a
channel gets no push for it on any device. The 60 s expiry bounds a crashed tab.

## 7. The push relay

A third `relay.Surface` (`service "push"`) on the existing engine, egress only:

- **Directory.** `Projects("push")` returns nil (no ingress). `Surfaces("push", m)`
  applies §2 and returns one `Delivery` per (recipient, subscription) with
  `Address = "push:" + sha256(endpoint)[:16]`, so the engine's pending window
  tracks each device separately. Recipients come from the channel's members
  (`store.ChannelMembers`, `ident.User` kinds, visibility as the live audience).
- **Seeded at the log tail** on first enable (like Discord), so no backlog replay.
- **Deliver** builds the payload (§8), encrypts and sends with TTL 86400 s and
  `Topic` = a hash of the channel id (so the push service collapses queued
  pushes per conversation). Outcomes:
  - 2xx → success; `last_ok_at` updated.
  - 404 / 410 → delete the subscription; **success** (never retried).
  - other 4xx → log, **success** (a permanent error must not stall the log —
    the engine's low-water mark only advances over delivered squawks).
  - 429 / 5xx / network → error (retried next tick).
- **Staleness.** A squawk older than 10 minutes at delivery time is marked
  delivered without sending (after an outage, don't buzz for old news; the
  badge still reflects it).
- **Health.** A `push:relay` condition is raised after 5 consecutive transient
  failures and cleared on the next success; `/app/admin/health` shows it.

## 8. Payload and service worker

Payload (JSON, ≤ 3 KB before encryption):

```json
{"title": "Code-review agent · PR #812 review", "body": "Review done: 3 comments…",
 "channel": "chn_…", "id": "<squawk id>", "unread": 7}
```

- **title** = sender label · conversation label (chat: the other members'
  labels; session channel: its label).
- **body** = markdown-stripped first 140 characters; an ask → "❓ " + question;
  a call-in → "📣 " + notice body; escalation → "⚠️ " + notice body. With
  `preview: false` the body is "New message".
- **unread** = the user's total unread across their channels (a new
  `jam.UnreadTotal(store, ic, log, user) int` over the same computation as the
  rail's per-channel counts).

`sw.js` gains:

- `push`: parse; if a notification with `tag = channel` is showing, replace it
  with body "N new messages · latest: <body>" (N counted in its `data`); show
  with `tag`, `renotify: true`, icon `icon-192`, badge `favicon-32`,
  `data: {channel, n}`; then `navigator.setAppBadge(unread)` where supported.
- `notificationclick`: close; focus an existing `/app/` client and navigate it
  to `/app/user/?c=<channel>`, else `clients.openWindow` that URL.

The Inbox page exposes its total unread (a `data-unread-total` attribute on the
rail fragment) and calls `navigator.setAppBadge(n)` / `clearAppBadge()` when it
changes, so reading in the app clears the badge.

## 9. Security and privacy

- Payloads are end-to-end encrypted; push services see size and timing only.
- Previews appear on lock screens (Slack's default); `preview: false` hides them.
- Subscription and focus routes inherit the `/app/user` gate and same-origin
  check; a user can only delete their own subscriptions.
- Sends go only to the recipient's own subscriptions, chosen server-side.
- The VAPID private key follows the secrets rule (never on disk, argv or logs).

## 10. Testing

- **Recipient selection** (table): each channel kind; ask `Of`; call-in and
  escalation targets; author excluded; sessions and accounts excluded; removed
  users; focus suppression and expiry; ask amend/answer excluded.
- **Deliver**: an `httptest` push endpoint that decrypts with a test browser key
  pair and checks payload, VAPID `Authorization`, TTL, Topic; 404/410 prune;
  other 4xx success; 429/5xx error; staleness skip.
- **Store**: conformance suite (Mem + Postgres in store-integration), user
  removal, upsert moving an endpoint between users.
- **API**: gate (non-member 403), same-origin, validation, own-row delete,
  push-off 404.
- **Config**: validation; keygen output round-trips.
- **Browser** (headless Chrome): opt-in calls subscribe and POSTs (subscription
  stubbed); `sw.js` contains the push/click handlers.
- **Manual**: desktop Chrome, Android, iOS Home Screen app end to end.

## 11. Delivery

1. **Subscriptions** — config + keygen, store + migration, API (key,
   subscribe, unsubscribe), menu toggle, docs. Nothing is sent yet.
2. **Sending** — relay surface + directory, payload, focus endpoint and page
   reporting, service worker handlers, app badge, health condition, docs.
