---
summary: Personal sessions — a person's own long-lived session of a role, started from the Inbox's New session (any project member) or by an operator, admitted against the role's pool and per-owner caps, conversed with in its session channel (the Inbox, and Discord when set up), and released by that owner (or reclaimed by the idle ladder); the idle ladder; the `session request|list|release` verbs and their admin routes.
read_when: You want Jam to raise a session of a role for you personally (from the Inbox or the CLI), or you are setting a role's personal caps, or a `session` command answered 400/403/409 and you need to know why, or you want to know how to talk to your session, or your session is nagging you (or was reclaimed) and you want to know why, how to tune it, or how to answer a nag with `keep`/`release`.
owns: the personal-session story — New session in the Inbox, owner resolution (project member ↔ login), admission (pool + per-owner caps, the ledger requirement), the conversation loop, the idle ladder (nags, replying `keep`/`release` to a nag, optional reclaim, `idle-after`/`nag-every`/`reclaim-after` semantics), the `session request|list|release` verbs, the `/admin/sessions/personal` routes, and owner-only release
prereqs: comms-addressing.md for a Project's members and a user's logins; discord.md for Discord delivery profiles, the user-id binding, and reply attribution; intercom.md for the intercom a session talks over; roster.md for roles and the `--max-personal*` caps; coves.md for what a raised studio does; serve.md for `store-postgres` and the allocation ledger
tier: leaf
updated: 2026-11-01
---

# Personal sessions

A **personal session** is a long-lived managed studio that a person asks Jam
for directly — with **New session** in the Inbox, or as an operator from the CLI
or admin UI. Jam admits it against the role's personal caps, raises it **owned by
that person**, and only the owner can list or release it. The owner talks to it in
its session channel (the [Inbox](intercom-ui.md), and Discord when set up), and it
stays until the owner releases it. It needs `store-postgres`.

## The conversation

1. The session works its first prompt, delivered as-is. Its
   [session context](session-context.md) tells the agent it is your personal session and how to reach you.
2. **The studio speaks first.** When it has results or needs input, it `send`s with
   no `to`, which goes to its own **session channel** (its home channel). You are
   called into that channel when the session starts, so it reaches your Discord
   inbox channel and your [`/app/user`](intercom-ui.md) inbox. Its nags and notices go
   there too, and still arrive after it has ended. The channel is private: other
   people can't join it on their own, and a reply by someone else (say, in a
   shared Discord inbox) doesn't let them in — but you can **call them in**
   (`/app/user` **Call in…**), and you may **leave** it ([membership](intercom.md#channel-membership)). It may message **only** you: Jam enrolls it with an addressing
   override of exactly `user:<owner's usr_id>`.
3. Once its agent is idle — its turn over and no background task still running —
   the studio waits for you (it is [resident](coves.md#jam-studio-agent-the-in-cove-client);
   there is no time limit). While it waits past the `warm-timeout` it is paused
   (`idled`, ≈0 CPU). If it ended its turn with a background task running (say, a
   dev server), it stays `running` and keeps the agent live instead, for up to 30m.
4. **Reply to its Discord message** (Discord's reply-to-message feature) to
   continue. Jam routes the reply to the studio and wakes it. A waiting studio
   (unpaused first if needed) resumes with `claude --continue`; a still-`running`
   one gets your reply written straight into its live agent. Either way it `read`s
   your reply and carries on. Repeat from 2.

**v1 limit:** you can only *reply* to the studio's messages; a new, non-reply message
in your inbox channel does not reach it. The session is never torn down for
waiting; it [escalates](escalation.md) only when it calls `escalate` and the
project has a policy (you are already in its channel); it ends when you release it, or
when the [idle ladder](#the-idle-ladder) reclaims it (only if its role sets
`reclaim-after`).

## The idle ladder

A session is **idle** while it waits on you, measured from when it last started
waiting (each reply you send runs another turn and restarts the clock). Jam's
wake-on engine walks it up a ladder set by the role:

1. **Pause.** Past the `warm-timeout` it is paused (step 3 above).
2. **Nag.** Once it has waited **`idle-after`** (default **4h**), Jam messages you
   *as the session*, in your Discord inbox: "Your personal session *id* (*role*) has
   been idle for *N*. Reply to this message to pick it back up, or release
   it with: `jam session release <id>`". It repeats every **`nag-every`**
   (default **24h**). **Replying to a nag is replying to the session** — it wakes and
   carries on, and the ladder starts over.
   **Or answer the nag with a command:** reply to the nag with exactly
   **`keep`** or **`release`** (case and a trailing `.`/`!` don't matter).
   `release` tears the session down like `session release` and confirms
   ("Released your personal session …"). `keep` restarts the idle clock without
   waking the agent (a paused session stays paused), and confirms ("Keeping your
   personal session … Next reminder in *idle-after*."). Rules:
   - **Owner only.** The reply counts only if Jam attributes it to you, never
     by Discord display name ([the rules](discord.md#who-a-discord-reply-is-from)).
     **Bind your Discord user id (recommended):** add it as your account on
     the `discord` connection (`jam account add --connection discord --uid <your-user-id> --user <you>`). Then only your own
     Discord account counts as you, and `keep`/`release` works from **any**
     inbox, shared ones included.
     **Unbound**, only an inbox channel that is **exactly yours** proves it's
     you. A shared inbox (or one that is also a room's channel) gets no
     `keep`/`release` hint in the nag, and its replies are ordinary replies.
     Jam can't see Discord permissions, so unbound this relies on your setup:
     **only you (and Jam's bot) may post in your inbox channel.** Anyone who can
     post there can release your session (and could already steer it by
     replying).
   - **Only a reply to a nag.** A `keep` replying to the agent's own message is
     your answer to the agent; it wakes the session.
   - **Anything else wakes.** Someone else's `keep`/`release`, or a word in a
     sentence, is an ordinary reply. If several replies are pending, a `release`
     wins, then any other text wakes, and only `keep`s alone keep.
3. **Reclaim (optional).** If the role sets **`reclaim-after`** (default: never) and
   the session has waited that long, Jam tells you ("Reclaimed your personal
   session …") and tears it down exactly as a release would, freeing your slot. The
   notice is delivered even though the session is gone.

A nag that fails to send is retried on the next tick and never ends the session;
a reclaim waits until its notice has been sent. The three settings are in
the role's file in the config repo:

```yaml
# projects/acme/roles/pair.yml
allocation: {max-personal: 3, idle-after: 4h, nag-every: 24h, reclaim-after: 72h}
```

Unset means the default; negative values are refused
([roster.md](roster.md#roles)). Nags need the Discord relay to deliver them; without it there are no nags, but a configured reclaim still
happens.

## Starting one from the Inbox

**New session**, beside New message in the [Inbox](intercom-ui.md) rail, offers
every role with `max-personal` above 0 in each of your projects (a full one is
shown greyed, with why). Pick one, optionally write a first message, and
**Start**: Jam starts the session owned by you (the same admission as below; no
login link needed, you are signed in), opens its session channel with you in it,
posts your first message there, and takes you to that conversation. The button
is hidden when Jam runs no supervisor. Discord is optional: to also talk to it
over Discord, set the project's chat service to `discord` and give yourself a
Discord inbox ([discord.md](discord.md)).

## Who owns it: link your login

Jam finds the owner by matching the caller's **admin login** (the operator
identity: your OIDC `sub`, which `jam whoami` shows, or `local` on a
loopback-only Jam) against the **user** holding that login, who must be a
member of the target project. Both are in the config repo:

`logins: ['auth0|abc123']` in `users/alice.yml`, and `members: [{user: alice}]`
in `projects/acme/project.yml`.

A login belongs to one user Jam-wide. Logins are described with the
rest of a project's members in [comms-addressing.md](comms-addressing.md#project-members-and-rooms).
If no member of the project is linked to your login, every `session` call answers
**403**.

## Admission: two caps, one ledger

A role admits personal sessions only when it sets a pool cap, in its file in
the config repo:

```yaml
allocation: {max-personal: 3, max-personal-per-owner: 1}   # projects/acme/roles/pair.yml
```

- `max-personal` is the pool: the cap on concurrent personal sessions of the
  role across all owners. Unset (the default) means the role admits no personal
  sessions.
- `max-personal-per-owner` caps one owner's share. Unset means only the pool
  cap applies.

The flags themselves are described in [roster.md](roster.md#roles). Any member
of the project may start any role (New session), as may any linked operator,
bounded by these caps.

Jam's Allocator checks both caps inside the allocation ledger's single atomic
grant (see [serve.md](serve.md#postgres-store-store-postgres)), so concurrent requests
cannot overshoot either cap. Personal sessions **require the ledger**, which
`serve` always has (without it a request answers **409**; tests only). Jam always runs the Allocator, so personal sessions work
without a Requisitioner.

## The verbs

All take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md). `--project` defaults to `default`.

```
jam session request --project acme --role pair --prompt-file task.md   # prints the session id
#   [--manager NAME ...] [--placement-timeout 30s]: see coves.md#managers-and-placement
jam session list    [--project acme]
jam session release ses_01j9q3x8f2k7m4n6p0r2s5t8v1
```

- **request** grants a slot, then raises the studio with you as its owner, and
  prints only the session id (a new `ses_…` id for each request). It is **named**
  `pair-01`, or the next `pair-NN` not taken by a live non-standing session (Jam-wide, like a `studio raise` label). The admin UI's role
  **Request** action does the same from the browser (see
  [ui-editing.md](ui-editing.md#runtime-studios)). The prompt file is
  read on the host and sent in the request body. It never goes on argv. Unlike
  `studio raise`, no identity token or launch secret is returned.
- **list** shows only **your** personal sessions in the project: id, role,
  phase, activity, and when each was raised.
- **release** tears the session down. Only its owner may release it (or reply
  `release` to a [nag](#the-idle-ladder); the idle ladder's optional reclaim is
  the one other way it ends). The
  teardown records the reservation release, which frees your slot.

## The admin routes

| Route | Result |
|---|---|
| `POST /admin/sessions/personal` `{project, role, prompt}` | **201** `{id, name, owner, project, role, phase}`. **403** if your login is not linked. **400** for an unknown role. **409** at capacity, or with no ledger. **502** if the grant errors or the raise fails. A failed raise releases the grant, so no slot leaks. |
| `GET /admin/sessions/personal?project=P` | **200** with your own personal sessions in P. **403** if your login is not linked. |
| `DELETE /admin/sessions/personal/{id}` | **204** after the teardown. **404** if the id does not exist or is not a personal session. **403** unless you are its owner. |

A personal session is also an ordinary managed studio. It appears in
`studio list`, and its Instance records `owner` and `session_kind: personal`. The
reconcile sweep never releases personal reservations; only the owner's release
(the verb, or a `release` reply to a nag), or an idle-ladder reclaim, does.
