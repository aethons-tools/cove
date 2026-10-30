---
summary: How Jam delivers Web Push — the egress-only push relay (recipients per squawk, one push per subscribed device, per-device outcomes and retries, the per-host breaker for a slow push service, pruning dead subscriptions, the 10-minute staleness cut-off, collapse per conversation, when `push:relay` is raised), the push endpoint allowlist, and the `/app/user/push/*` routes the app calls (key, subscriptions, focus).
read_when: You're operating or debugging push delivery — the `push:relay` condition is open, a `push: send refused` log line appeared, a subscription vanished, or a push arrived late or not at all — or you're extending the push routes or the endpoint allowlist.
owns: the push relay's sending behaviour (start mark, TTL/collapse, recipient re-check, per-device outcome table, per-host breaker, health), the endpoint allowlist, and the `/app/user/push/*` route table including `POST /app/user/push/focus`. Does NOT own setup, the notify rule or on-device behaviour — see push.md.
prereqs: push.md (what push is and how it's enabled)
tier: leaf
updated: 2026-10-29
---

# Push delivery

What happens between a message and a buzz. Setup and what members see:
[push.md](push.md).

## How Jam sends

A resident **push relay** (egress only, beside the Linear and Discord relays —
[intercom.md](intercom.md#enabling-it)) picks each new message's recipients
by the [notify rule](push.md#what-notifies) and sends one push per
subscribed device, retrying each device on its own:

- **First enable starts at the log's tail** — turning push on never buzzes
  for older messages. Its progress mark lives in `relay-markers.json` under
  [`state-dir`](serve.md#the-serve-config).
- Each push is **held 24 hours** by the push service for an offline device,
  and pushes for one conversation **collapse**: a device that was offline
  gets the latest, not a backlog.
- The recipient is re-checked when sending (and on every retry), so someone
  who left, opened the conversation, or read the message on another device
  in the meantime isn't buzzed.
- Outcomes per device:

  | Push service answers | Jam |
  |---|---|
  | 2xx | done; the device's last success is recorded |
  | 404 or 410 (subscription gone) | deletes the subscription |
  | 3xx (redirects are never followed) or another 4xx | logs `push: send refused` with the status and moves on — not retried |
  | 429, 5xx, or no answer within 5 s | retries — until the message is **10 minutes old**, then drops it |

  A stored endpoint off the [allowlist](#endpoint-allowlist), or whose keys
  can't encrypt, is deleted without sending. Logs name a device by a hash of
  its endpoint, never the endpoint itself.
- **One slow push service can't hold up the rest.** Devices are sent one at
  a time, so after a 429, 5xx or no answer from a push service host, Jam
  sends nothing to *any* endpoint on that host for **30 s** — those devices
  count as failed and are retried after — while devices on other push
  services go out as normal. The first answer from the host after that
  clears it.
- **The notify relay** is a second egress-only relay service, `notify`, over the same rule. It publishes to the recipient's open `/app/user/events` streams ([the signal stream](intercom-ui.md#unread-and-refresh)), drops after 10 minutes, skips users with no open stream, has no retries (nothing is queued) and no `runtime.push` gate. It keeps its marker in memory, seeded at the log tail on every start (no state dir, nothing written). It works without Web Push keys.
- **Health.** Sends that keep failing raise the `push:relay`
  [condition](monitoring.md#v1-conditions) — usually the Jam host's egress to
  the push services ([step 4](push.md#enabling-push-on-a-jam)).

## Routes

All under the `/app/user` gate (members only; writes must be same-origin), and
404 when neither Web Push nor the notify stream is on (`push/focus` is mounted with either; the key and subscription routes need Web Push):

| Route | |
|---|---|
| `GET /app/user/push/key` | `{"key": "<VAPID public key, base64url>"}` |
| `POST /app/user/push/subscriptions` | Body: the browser's `PushSubscription.toJSON()`. The endpoint must pass the [endpoint allowlist](#endpoint-allowlist) and the keys be the right sizes (`p256dh` 65 bytes, `auth` 16), else 400. Stores it for the caller (an endpoint another user had moves to the caller; an unchanged re-send is a no-op); 204. |
| `DELETE /app/user/push/subscriptions` | Body `{"endpoint": "…"}`; deletes it only if it is the caller's; 204 either way. |
| `POST /app/user/push/focus` | Body `{"channel": "<id>"}` or `{"channel": null}`: the conversation the caller has open and focused, held in memory briefly ([how focus suppresses](push.md#what-notifies)), or none. The channel must be one the caller can see, else 400; 204. |

The key and subscription routes refuse an [app token](app-tokens.md#bearer-scope) (403): a subscription belongs to a browser.

### Endpoint allowlist

Jam sends from the server to whatever endpoint a browser hands it, so it only
accepts the Web Push services' own: an `https:` URL of at most 2048 bytes, no
credentials, on the default port (none or `:443`), whose host is
`fcm.googleapis.com`, `updates.push.services.mozilla.com`, `web.push.apple.com`,
or ends in `.push.services.mozilla.com`, `.push.apple.com` or
`.notify.windows.com`. IP literals and every other host are refused, at
subscribe time and again before each send.
