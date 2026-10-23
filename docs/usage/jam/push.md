---
summary: Push notifications — Jam buzzes a member's phone or desktop through standard Web Push when they get a message that's for them, even with the app closed. Enabling it (`at-jam push keygen`, the VAPID credential, `runtime.push`, outbound hosts), turning it on per device from the principal menu, the iOS Home Screen requirement, what notifies, and the privacy/preview choice.
read_when: You want Jam to send push notifications — setting up the VAPID key and `runtime.push`, opening the Jam host's egress to the push services — or a member asks why the Notifications item is missing, reads "Blocked in browser settings" or "Add Jam to your Home Screen", or wants to know what buzzes them and what shows on the lock screen.
owns: the Web Push setup steps (keygen → credential → config → egress hosts), the per-device Notifications toggle and its states, the iOS Home Screen rule, the notify rule as members see it, the preview/privacy behaviour, and the `/app/user/push/*` routes. Does NOT own the `runtime.push` key reference — see serve.md; nor installing the app — see ui.md.
prereqs: ui.md (the Jam app, its login and installing it); credentials.md (supplying a credential); serve.md#the-serve-config (the `runtime.push` keys)
tier: leaf
updated: 2026-10-23
---

# Push notifications

Jam uses standard **Web Push**: a member's browser subscribes with its own
vendor's push service (Google, Mozilla, Apple, Microsoft) under Jam's **VAPID**
key, and Jam posts each notification, end-to-end encrypted, to that
subscription. No vendor account is needed. Notifications are **per device**: a
member turns them on in each browser or installed app they want buzzed.

## Enabling push on a Jam

1. **Generate a key pair** — `at-jam push keygen` prints a fresh VAPID pair:
   `private: …` and `public: …`. Only the private half is kept; Jam derives the
   public one from it.
2. **Store the private key as a credential** — add it to the credentials file
   under a name of your choosing, e.g. `jam-vapid` (see
   [credentials.md](credentials.md)), and demand it under `credentials:` in the
   serve config. Like every Jam secret it is resolved on the host, held in
   memory and never written to disk, argv or logs.
3. **Turn it on** with a `runtime.push` block (keys:
   [serve.md](serve.md#the-serve-config)):

   ```yaml
   runtime:
     push:
       vapid-private-key-cred: jam-vapid
       subject: mailto:ops@example.com
       preview: true
   ```

4. **Open egress** — the Jam host needs outbound HTTPS to the push services:
   `fcm.googleapis.com`, `updates.push.services.mozilla.com`,
   `web.push.apple.com` and `*.notify.windows.com`.
5. **Serve over HTTPS** — browsers offer push only to a secure origin (or
   `localhost`), the same rule as [installing the app](ui.md#installing-the-app).

`at-jam serve` **refuses to start** when the block is set but the credential
can't be resolved or isn't a valid P-256 private key; the error names the
credential, never its value. Without the block push is off: no menu item, and
the `/app/user/push/*` routes answer 404.

Changing the key invalidates every existing subscription; a device that had
Notifications on re-subscribes under the new key the next time the app is
opened on it.

## Turning it on for a device

A **member** (not an admin-only login) sees **Notifications** in the principal
menu at the right of the [title bar](ui.md#the-title-bar-and-look). It reflects
this browser only:

| The item reads | Meaning |
|---|---|
| **Notifications: Off** | Click to turn on: the browser asks for permission, subscribes, and Jam stores the subscription. |
| **Notifications: On** | Click to turn off: the browser unsubscribes and Jam deletes the subscription. |
| **Notifications: Blocked in browser settings** | Permission was denied; allow notifications for the Jam site in the browser's settings, then reload. |
| **Add Jam to your Home Screen to get notifications** | iOS/iPadOS in a Safari tab — see below. |
| *(absent)* | Push isn't configured on this Jam, you're not a member, or this browser has no Web Push. |

**iOS and iPadOS deliver push only to the Home Screen app.** Add Jam to the
Home Screen ([installing the app](ui.md#installing-the-app)), open it from
there, and turn Notifications on inside it.

A device belongs to whoever last signed in on it: opening the app re-registers
an existing subscription under the current user, so one browser never buzzes
two people. A member keeps at most 20 subscribed devices; turning on a 21st
drops their oldest. A subscription the push service reports gone is deleted,
and removing a user deletes all of theirs.

## What notifies

Only **people** are notified — never sessions or accounts — and only for a
message they can see in a channel they belong to, not their own, and not in
the conversation they currently have open in the app. Within that, a member is
buzzed for:

- any message in a **chat** (direct or group) or a **session** conversation;
- in any channel, an **ask** they may answer, a **call-in** inviting them, or
  an **escalation** reaching them.

An ask being amended, withdrawn or answered never notifies. A message not yet
delivered after 10 minutes (the Jam or a push service was down) is dropped
rather than buzzing late. Tapping a notification opens the conversation.

## Privacy and previews

- Payloads are **end-to-end encrypted** to the browser; the push service sees
  only their size and timing.
- With `preview: true` (the default) a notification shows the sender, the
  conversation and the start of the message — **on the lock screen**, too.
  `preview: false` replaces the text with "New message".
- Jam picks a recipient's subscriptions itself; a member can only add or delete
  their own.

## Routes

All under the `/app/user` gate (members only; writes must be same-origin), and
404 when push is off:

| Route | |
|---|---|
| `GET /app/user/push/key` | `{"key": "<VAPID public key, base64url>"}` |
| `POST /app/user/push/subscriptions` | Body: the browser's `PushSubscription.toJSON()`. The endpoint must be an `https:` URL of at most 2048 bytes and the keys the right sizes (`p256dh` 65 bytes, `auth` 16), else 400. Stores it for the caller (an endpoint another user had moves to the caller); 204. |
| `DELETE /app/user/push/subscriptions` | Body `{"endpoint": "…"}`; deletes it only if it is the caller's; 204 either way. |

The design history is in
[the push notifications spec](../../superpowers/specs/2026-10-23-jam-push-notifications-design.md).
