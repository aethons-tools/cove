---
summary: Personal sessions — a human operator's own long-lived session of a role, admitted against the role's pool and per-owner caps, owned by the roster human linked to their login, conversed with over Discord, and released by that owner (or reclaimed by the idle ladder); the idle ladder; the `session request|list|release` verbs and their admin routes.
read_when: You (a human operator) want Jam to raise a session of a role for you personally, or you are setting a role's personal caps, or a `session` command answered 400/403/409 and you need to know why, or you want to know how to talk to your session, or your session is nagging you (or was reclaimed) and you want to know why, how to tune it, or how to answer a nag with `keep`/`release`.
owns: the personal-session story — owner resolution (roster Human ↔ login), the Discord delivery requirement, admission (pool + per-owner caps, the ledger requirement), the conversation loop, the idle ladder (nags, replying `keep`/`release` to a nag, optional reclaim, `--idle-after`/`--nag-every`/`--reclaim-after` semantics), the `session request|list|release` verbs, the `/admin/sessions/personal` routes, and owner-only release
prereqs: comms-addressing.md for the Project roster and a Human's `--login`; discord.md for Discord delivery profiles, the user-id binding, and reply attribution; intercom.md for the intercom a session talks over; roster.md for roles and the `--max-personal*` caps; coves.md for what a raised studio does; serve.md for `store-postgres` and the allocation ledger
tier: leaf
updated: 2026-10-06
---

# Personal sessions

A **personal session** is a long-lived managed studio that a human operator asks
Jam for directly: Jam admits it against the role's personal caps, raises it
**owned by that human**, and only the owner can list or release it. The owner talks
to it over **Discord**, and it stays until the owner releases it. It needs
`store-postgres`, a Discord chat service on the project, and a Discord delivery
profile for the owner.

## The conversation

1. The session works its first prompt, delivered as-is. Its
   [session context](session-context.md) tells the agent it is your personal session and how to reach you.
2. **The studio speaks first.** When it has results or needs input, it `send`s with
   no `to`, which goes to its owner — you — as a message in your Discord inbox
   channel. It may message **only** you: Jam enrolls it with an addressing
   override of exactly `user:<owner's usr_id>`.
3. Once its agent is idle — its turn over and no background task still running —
   the studio waits for you (it is [resident](coves.md#cove-master-the-in-cove-client);
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
`wait-max` and never escalated (it has no ticket); it ends when you release it, or
when the [idle ladder](#the-idle-ladder) reclaims it (only if its role sets
`--reclaim-after`).

## The idle ladder

A session is **idle** while it waits on you, measured from when it last started
waiting (each reply you send runs another turn and restarts the clock). Jam's
wake-on engine walks it up a ladder set by the role:

1. **Pause.** Past the `warm-timeout` it is paused (step 3 above).
2. **Nag.** Once it has waited **`idle-after`** (default **4h**), Jam messages you
   *as the session*, in your Discord inbox: "Your personal session *id* (*role*) has
   been waiting on you for *N*. Reply to this message to pick it back up, or release
   it with: `at-jam session release <id>`". It repeats every **`nag-every`**
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
     **Bind your Discord user id (recommended):**
     `--delivery discord:<inbox-channel>:<your-user-id>`. Then only your own
     Discord account counts as you, and `keep`/`release` works from **any**
     inbox, shared ones included.
     **Unbound**, only an inbox channel that is **exactly yours** proves it's
     you. A shared inbox (or one that is also a roster channel) gets no
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
a reclaim waits until its notice has been sent. The three settings are role flags:

```
at-jam role add --project acme --name pair --max-personal 3 \
  --idle-after 4h --nag-every 24h --reclaim-after 72h
```

`0` (unset) means the default; negative values are refused
([roster.md](roster.md#roles)). Nags need the Discord relay to deliver them; without it there are no nags, but a configured reclaim still
happens.

## Discord is required

A ticketless studio's messages can only be delivered over Discord, so a request is
refused with **400** (before any slot is granted) unless both hold:

- the project's chat service is `discord`:
  `at-jam project chat-service set --project acme --service discord`;
- you (the owner) have a Discord inbox in the project, ideally with your Discord
  account bound ([discord.md](discord.md)):
  `at-jam project member add acme alice --delivery discord:<inbox-channel>` and
  `at-jam account add --connection discord --uid <your-user-id> --user alice`.

Jam must also run the Discord relay (`runtime.discord`,
see [serve.md](serve.md#the-serve-config)); it no longer needs a Requisitioner, and it
polls every project whose chat service is `discord`
([intercom.md](intercom.md#enabling-it)).

## Who owns it: link your login

Jam finds the owner by matching the caller's **admin login** (the operator
identity: your OIDC `sub`, which `at-jam whoami` shows, or `local` on a
loopback-only Jam) against the **user** holding that login, who must be a
member of the target project:

```
at-jam user login alice 'auth0|abc123'
at-jam project member add acme alice
```

A login belongs to one user Jam-wide. `--login` is described with the
rest of the roster in [comms-addressing.md](comms-addressing.md#the-project-roster).
If no human in the project is linked to your login, every `session` call answers
**403**.

## Admission: two caps, one ledger

A role admits personal sessions only when it sets a pool cap:

```
at-jam role add --project acme --name pair --destinations anthropic,git \
  --max-personal 3 --max-personal-per-owner 1
```

- `--max-personal` is the pool: the cap on concurrent personal sessions of the
  role across all owners. `0` (the default) means the role admits no personal
  sessions.
- `--max-personal-per-owner` caps one owner's share. `0` means only the pool
  cap applies.

The flags themselves are described in [roster.md](roster.md#roles). In this
release any linked operator may request any role, bounded by these caps.

Jam's Allocator checks both caps inside the allocation ledger's single atomic
grant (see [serve.md](serve.md#postgres-store-store-postgres)), so concurrent requests
cannot overshoot either cap. Personal sessions **require the ledger**, which
`serve` always has (without it a request answers **409**; tests only). Jam always runs the Allocator, so personal sessions work
without a Requisitioner.

## The verbs

All take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md). `--project` defaults to `default`.

```
at-jam session request --project acme --role pair --prompt-file task.md   # prints the session id
at-jam session list    [--project acme]
at-jam session release personal-alice-1a2b3c4d
```

- **request** grants a slot, then raises the studio with you as its owner, and
  prints only the session id (`personal-<owner>-<8 hex>`). The admin UI's role
  **Request** action does the same from the browser (see
  [ui.md](ui.md#runtime-studios)). The prompt file is
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
| `POST /admin/sessions/personal` `{project, role, prompt}` | **201** `{id, owner, project, role, phase}`. **403** if your login is not linked. **400** for an unknown role, a project whose chat service isn't `discord`, or an owner with no Discord delivery profile. **409** at capacity, or with no ledger. **502** if the grant errors or the raise fails. A failed raise releases the grant, so no slot leaks. |
| `GET /admin/sessions/personal?project=P` | **200** with your own personal sessions in P. **403** if your login is not linked. |
| `DELETE /admin/sessions/personal/{id}` | **204** after the teardown. **404** if the id does not exist or is not a personal session. **403** unless you are its owner. |

A personal session is also an ordinary managed studio. It appears in
`studio list`, and its Instance records `owner` and `session_kind: personal`. The
reconcile sweep never releases personal reservations; only the owner's release
(the verb, or a `release` reply to a nag), or an idle-ladder reclaim, does.
