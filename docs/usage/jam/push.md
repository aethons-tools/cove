---
summary: Push notifications — Jam buzzes a member's phone or desktop through standard Web Push when they get a message that's for them, even with the app closed. Enabling it (`jam push keygen`, the VAPID credential, `runtime.push`, outbound hosts), turning it on per device from the principal menu, the iOS Home Screen requirement, what notifies (and why the open conversation doesn't), how notifications look and behave on the device (grouping, tap-through, app badge, sign-out), the privacy/preview choice, and troubleshooting.
read_when: You want Jam to send push notifications — setting up the VAPID key and `runtime.push`, opening the Jam host's egress to the push services — or a member asks why the Notifications item is missing, reads "Blocked in browser settings" or "Add Jam to your Home Screen", or wants to know what buzzes them, what shows on the lock screen, what the app badge counts, or what signing out does to notifications; or pushes aren't arriving.
owns: the Web Push setup steps (keygen → credential → config → egress hosts), the per-device Notifications toggle and its states, the iOS Home Screen rule, the notify rule as members see it, focus suppression as members see it, the on-device behaviour (one notification per conversation, tap-through, app badge, sounds, sign-out unsubscribing the device), the preview/privacy behaviour, and push troubleshooting. Does NOT own how the relay sends or the `/app/user/push/*` routes — see push-delivery.md; nor the `runtime.push` key reference — see serve.md; nor installing the app — see ui.md.
prereqs: ui.md (the Jam app, its login and installing it); credentials.md (supplying a credential); serve.md#the-serve-config (the `runtime.push` keys)
tier: leaf
updated: 2026-10-24
---

# Push notifications

Jam uses standard **Web Push**: a member's browser subscribes with its own
vendor's push service (Google, Mozilla, Apple, Microsoft) under Jam's **VAPID**
key, and Jam posts each notification, end-to-end encrypted, to that
subscription. No vendor account is needed. Notifications are **per device**: a
member turns them on in each browser or installed app they want buzzed.

## Enabling push on a Jam

1. **Generate a key pair** — `jam push keygen` prints a fresh VAPID pair:
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
   `fcm.googleapis.com`, `*.push.services.mozilla.com`, `*.push.apple.com`
   and `*.notify.windows.com`.
5. **Serve over HTTPS** — browsers offer push only to a secure origin (or
   `localhost`), the same rule as [installing the app](ui.md#installing-the-app).

`jam serve` **refuses to start** when the block is set but the credential
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
and removing a user deletes all of theirs. Signing out turns the device off
([on the device](#on-the-device)).

## What notifies

Only **people** are notified — never sessions or accounts — and only for a
message they can see in a channel they belong to, not their own, and not in
the conversation they currently have open in the app. Within that, a member is
buzzed for:

- any message in a **chat** (direct or group) or a **session** conversation;
- in any channel, an **ask** they may answer, a **call-in** inviting them, or
  an **escalation** reaching them.

An ask being amended, withdrawn or answered never notifies, and a push that
can't go out within 10 minutes is dropped rather than buzzing late
([how Jam sends](push-delivery.md#how-jam-sends)).

**The conversation you're reading doesn't buzz.** While a conversation is open
in a visible, focused Jam window, the page tells Jam so (on focus, on
visibility change and every 30 s), and Jam pushes nothing for it to *any* of
your devices. Switching tab or app, minimising, or opening another
conversation ends that at once; a tab that crashed or lost its connection
stops suppressing within 60 s. Nothing about focus is stored.

## On the device

- **One notification per conversation.** A newer message replaces the
  conversation's notification instead of stacking another: it reads
  "*N* new messages · latest: …" and buzzes again.
- **Tapping it** opens the conversation — in an open Inbox window if there
  is one (focused and moved to it), else in a new one; an admin window is
  never navigated away.
- **App badge.** Where the platform has one (the installed app on desktop
  Chrome/Edge, Android launchers that support it, iOS Home Screen), the icon
  shows your total unread: set by each push, and updated by the Inbox as you
  read, so it clears once everything is read.
- **Signing out** (principal menu → Sign out) first turns Notifications off
  for *this* device — unsubscribing it and deleting its subscription — so the
  next person on a shared device isn't buzzed with your messages. If that
  fails, sign-out goes ahead and the subscription moves to whoever signs in
  next (see above).

## Sounds

**Sounds…** in the principal menu (any member, push on or not) sets this
device's two sounds — **Messages** (a new message in a conversation you're
in) and **Needs you** (an ask you can answer, a call-in, an escalation) —
each **Off** or one of a few chimes, plus one volume and a ▶ preview. The
settings stay in this browser.

- **While a Jam window is open** (even a background tab), the Inbox plays the
  chime when something changes: Needs you when a conversation starts needing
  you (an ask, a call-in or escalation, or a session waiting on a person),
  else Messages when one has more unread. It stays quiet for the
  conversation you are looking at. Across your open Jam tabs it plays at
  most one chime a second (a Needs you chime still plays right after a
  Messages one; anything else in that second is dropped, not queued).
  Browsers play sound only after you've clicked or typed in the page once;
  until then the page stays quiet rather than chiming late.
- **Push notifications** can't carry a custom sound — the web gives a
  notification only the OS's sound or none. Jam makes a push **silent** while
  a Jam page that can chime (clicked at least once, not frozen) has checked
  in within the last 90 seconds, or when that kind's sound is Off; otherwise
  the OS plays its notification sound (on macOS, set per app under System
  Settings → Notifications). Each push says which kind it is (`needs` for an
  ask, call-in or escalation).

## Privacy and previews

- Payloads are **end-to-end encrypted** to the browser; the push service sees
  only their size and timing.
- With `preview: true` (the default) a notification shows the sender, the
  conversation and the start of the message — **on the lock screen**, too.
  `preview: false` replaces the text with "New message".
- Jam picks a recipient's subscriptions itself; a member can only add or delete
  their own.

## Troubleshooting

| Symptom | Fix |
|---|---|
| "Notifications: Blocked in browser settings" | The browser's permission for the Jam site is *Block*. Allow notifications for the site (the padlock/site-settings menu), then reload. Jam can't re-ask once blocked. |
| "Add Jam to your Home Screen …" on iPhone/iPad | iOS delivers push only to the Home Screen app: Share → Add to Home Screen, open Jam from the icon, turn Notifications on there. |
| No Notifications item | Push isn't configured, you're not a member, or the browser has no Web Push (a non-HTTPS origin counts). |
| On, but nothing arrives | You may have the conversation open and focused (by design). Otherwise check the OS's notification settings for the browser or app, then the [`push:relay`](monitoring.md#v1-conditions) condition: open means the Jam host can't reach the push services — open its egress to the hosts in [step 4](#enabling-push-on-a-jam). |
| A device stopped after a key change | Open the app on it once: it re-subscribes under the new key. |

How Jam sends each push, its retries and the `/app/user/push/*` routes:
[push-delivery.md](push-delivery.md).

The design history is in
[the push notifications spec](../../superpowers/specs/2026-10-23-jam-push-notifications-design.md).
