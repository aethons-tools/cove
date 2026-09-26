---
summary: Personal sessions — a human operator's own long-lived session of a role, admitted against the role's pool and per-owner caps, owned by the roster human linked to their login, conversed with over Discord, and released only by that owner; the `session request|list|release` verbs and their admin routes.
read_when: You (a human operator) want harbor to raise a session of a role for you personally, or you are setting a role's personal caps, or a `session` command answered 400/403/409 and you need to know why, or you want to know how to talk to your session.
owns: the personal-session story — owner resolution (roster Human ↔ login), the Discord delivery requirement, admission (pool + per-owner caps, the ledger requirement), the conversation loop, the `session request|list|release` verbs, the `/admin/sessions/personal` routes, and owner-only release
prereqs: comms-addressing.md for the Project roster, a Human's `--login`, and Discord delivery profiles; intercom.md for the intercom a session talks over; roster.md for roles and the `--max-personal*` caps; coves.md for what a raised cove does; serve.md for `store-postgres` and the allocation ledger
tier: leaf
updated: 2026-09-26
---

# Personal sessions

A **personal session** is a long-lived managed cove that a human operator asks
harbor for directly: harbor admits it against the role's personal caps, raises it
**owned by that human**, and only the owner can list or release it. The owner talks
to it over **Discord**, and it stays until the owner releases it. It needs
`store-postgres`, a Discord chat service on the project, and a Discord delivery
profile for the owner.

## The conversation

1. The session works its first prompt. Harbor prefixes your prompt with a short
   preamble telling the agent it is your personal session and how to reach you.
2. **The cove speaks first.** When it has results or needs input, it `send`s with
   no `to`, which goes to its owner — you — as a message in your Discord inbox
   channel. It may message **only** you: harbor enrolls it with an addressing
   override of exactly `human:<owner>`.
3. After **every** turn the cove waits for you (it is [resident](coves.md#cove-master-the-in-cove-client);
   there is no time limit). While it waits past the `warm-timeout` it is paused
   (`idled`, ≈0 CPU).
4. **Reply to its Discord message** (Discord's reply-to-message feature) to
   continue. Harbor routes the reply to the cove, wakes it (unpausing it first if
   needed), and it resumes with `claude --continue`, `read`s your reply, and
   carries on. Repeat from 2.

**v1 limit:** you can only *reply* to the cove's messages; a new, non-reply message
in your inbox channel does not reach it. The session is never torn down for waiting
and never escalated (it has no ticket); it ends only when you release it.

## Discord is required

A ticketless cove's messages can only be delivered over Discord, so a request is
refused with **400** (before any slot is granted) unless both hold:

- the project's chat service is `discord`:
  `at-harbor project chat-service set --project acme --service discord`;
- you (the owner) have a Discord delivery profile — your inbox channel:
  `at-harbor project roster add-human acme --name alice --handle alice.h --login '…' --delivery discord:<inbox-channel>`.

Harbor must also run the Discord relay (`runtime.discord` plus an `intercom-log`,
see [serve.md](serve.md#the-serve-config)); it no longer needs a dispatcher, and it
polls every project whose chat service is `discord`
([intercom.md](intercom.md#enabling-it)).

## Who owns it: link your login

Harbor finds the owner by matching the caller's **admin login** (the operator
identity: your OIDC `sub`, which `at-harbor whoami` shows, or `local` on a
loopback-only harbor) against the **roster human** in the target project whose
`Login` is set to it:

```
at-harbor project roster add-human acme --name alice --handle alice.h --login 'auth0|abc123'
```

A login links at most one human per project. `--login` is described with the
rest of the roster in [comms-addressing.md](comms-addressing.md#the-project-roster).
If no human in the project is linked to your login, every `session` call answers
**403**.

## Admission: two caps, one ledger

A role admits personal sessions only when it sets a pool cap:

```
at-harbor role add --project acme --name pair --destinations anthropic,git \
  --max-personal 3 --max-personal-per-owner 1
```

- `--max-personal` is the pool: the cap on concurrent personal sessions of the
  role across all owners. `0` (the default) means the role admits no personal
  sessions.
- `--max-personal-per-owner` caps one owner's share. `0` means only the pool
  cap applies.

The flags themselves are described in [roster.md](roster.md#roles). In this
release any linked operator may request any role, bounded by these caps.

Harbor's Allocator checks both caps inside the allocation ledger's single atomic
grant (see [serve.md](serve.md#postgres-store-backend-store-postgres)), so concurrent requests
cannot overshoot either cap. Personal sessions **require the ledger**. On the
file store there is no fallback, and a request answers **409** naming
`store-postgres`. Harbor always runs the Allocator, so personal sessions work
without a dispatcher.

## The verbs

All take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md). `--project` defaults to `default`.

```
at-harbor session request --project acme --role pair --prompt-file task.md   # prints the session id
at-harbor session list    [--project acme]
at-harbor session release personal-alice-1a2b3c4d
```

- **request** grants a slot, then raises the cove with you as its owner, and
  prints only the session id (`personal-<owner>-<8 hex>`). The prompt file is
  read on the host and sent in the request body. It never goes on argv. Unlike
  `cove raise`, no identity token or launch secret is returned.
- **list** shows only **your** personal sessions in the project: id, role,
  phase, activity, and when each was raised.
- **release** tears the session down — the only way a personal session ends. Only its owner may release it. The
  teardown records the reservation release, which frees your slot.

## The admin routes

| Route | Result |
|---|---|
| `POST /admin/sessions/personal` `{project, role, prompt}` | **201** `{id, owner, project, role, phase}`. **403** if your login is not linked. **400** for an unknown role, a project whose chat service isn't `discord`, or an owner with no Discord delivery profile. **409** at capacity, or with no ledger. **502** if the grant errors or the raise fails. A failed raise releases the grant, so no slot leaks. |
| `GET /admin/sessions/personal?project=P` | **200** with your own personal sessions in P. **403** if your login is not linked. |
| `DELETE /admin/sessions/personal/{id}` | **204** after the teardown. **404** if the id does not exist or is not a personal session. **403** unless you are its owner. |

A personal session is also an ordinary managed cove. It appears in
`cove list`, and its Instance records `owner` and `session_kind: personal`. The
reconcile sweep never releases personal reservations; only the owner's release
does.
