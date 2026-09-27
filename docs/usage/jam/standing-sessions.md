---
summary: Standing sessions — named, long-lived teammates an operator declares on a role (e.g. `reviewer/alice-bot`); Jam keeps exactly one studio running per name, raises it again if it dies, and tears it down when the name is removed. Covers declaring, the `standing add|list|rm` verbs and admin routes, keep-alive with backoff, dismissal, admission, and messaging.
read_when: You want a role to have a permanent, named agent running (a standing teammate), or you are removing one, or a standing session keeps restarting / isn't coming up and you want to know why, or you need to know how a standing session reaches people.
owns: the standing-session story — declarations on a role (`RoleAllocation.Standing`), the per-name actor id, the `standing add|list|rm` verbs and `/admin/roles/{project}/{role}/standing` routes, the standing reconciler (keep-alive, restart under the same name, backoff, dismissal), standing admission (declared-name cap, file-store behavior), and how a standing session messages people
prereqs: roster.md for roles; coves.md for what a raised studio does and resident mode; comms-addressing.md for `send(to=…)` targets and a role's addressing; discord.md for the Discord reply loop; serve.md for `store-postgres` and the allocation ledger
tier: leaf
updated: 2026-09-27
---

# Standing sessions

A **standing session** is a named, long-lived agent that an operator declares on
a role, such as `reviewer/alice-bot`. The declaration is the desired state.
Jam keeps **exactly one studio running per declared name**. If the studio dies, Jam
raises it again under the same name. When the name is removed, Jam tears the
studio down. It works on the file store and on `store-postgres`.

## Declaring one

```
at-jam standing add  --project acme --role reviewer --name alice-bot --prompt-file alice.md
at-jam standing list --project acme --role reviewer
at-jam standing rm   --project acme --role reviewer alice-bot
```

Like every admin verb they take `--app`/`--admin-url`/`--token`
([operators.md](operators.md)); without `--project` they act on the `default` project.

- **add** declares the name with its prompt. The prompt file is read on the host
  and sent in the request body. It never goes on argv. Both `--name` and
  `--prompt-file` are required.
- **list** prints each declared name and the actor id its studio runs under.
- **rm** removes the name. Jam then tears its studio down (see
  [Dismissal](#dismissal)).

The declarations live on the role (`allocation.standing`, a list of
`{name, prompt}`). Each write reads the role, changes only that list, and writes it
back, so the role's scope, kit and other allocation settings are kept. Re-running
`role add` for the role keeps its standing declarations
([roster.md](roster.md#roles)).

Each name runs under the actor id
`standing-<project>-<role>-<name>`, with characters outside `[A-Za-z0-9._-]` turned
into `-` (as for personal session ids). A name is refused with **400** if it is
already declared on the role, or if its actor id is already used by another
declaration on any role or project (for example, `a b` and `a/b` both become
`a-b`).

## The admin routes

| Route | Result |
|---|---|
| `POST /admin/roles/{project}/{role}/standing` `{name, prompt}` | **201**. **400** if the name or prompt is missing, the name is already declared, or its actor id is already in use. **404** for an unknown role. |
| `GET /admin/roles/{project}/{role}/standing` | **200** with the declared list, prompts included. **404** for an unknown role. |
| `DELETE /admin/roles/{project}/{role}/standing/{name}` | **204**. **404** if the role or the name doesn't exist. |

## What "kept alive" means

Jam's **standing reconciler** runs whenever `at-jam serve` runs. It needs no
Requisitioner and no config. Every 30s it walks every role's declarations. For each
name:

- **Its studio is live**: nothing to do.
- **It has no studio**: Jam asks the Allocator for a standing slot (see
  [Admission](#admission)), then raises the studio under the name's actor id, with
  the declared prompt behind a short preamble. The preamble tells the agent it is
  the standing session `<name>` and how to message people.
- **Its studio died**: the supervisor detects it and tears it down (see
  [coves.md](coves.md#the-model)). That frees its reservation, and the next pass
  raises the name again. This is a **fresh session** under the same name. The
  agent's context from before the death is not carried over.

**Backoff.** If a raise fails, Jam releases the slot and waits before trying
that name again: 30s, then doubling (1m, 2m, …) up to 30m. The backoff is per name,
so one failing name doesn't delay another. It resets once the name has a live
studio, and it is kept in memory, so a Jam restart retries at once. A denied or
failed *grant* is logged and retried on the next pass, without backoff.
A role whose [egress policy](roster.md#role-egress) asks for a domain outside the
kit's ceiling fails every raise this way, so its standing sessions back off (each
failure logged, naming the domain) until the policy or the kit is fixed.

If Jam stops partway through raising a standing session — after creating its
identity but before recording the studio — the identity is left behind with no studio.
The reconciler removes it on its next pass (standing ids are Jam's own) and
raises the session normally, rather than failing "already exists" from then on.

A standing studio is **resident**, like a personal session: it waits after every
turn instead of ending, and it is never torn down for `wait-max` (see
[coves.md](coves.md#cove-master-the-in-cove-client) and
[intercom.md](intercom.md#waiting-for-a-reply-wake-on)). Past the `warm-timeout`
it is still paused and woken on a reply. It gets **no idle nags** and is never
reclaimed, because it has no owner. That ladder is for
[personal sessions](personal-sessions.md#the-idle-ladder) only.

## Dismissal

Once a name is no longer declared, or its role is removed, the next pass tears its
studio down. `standing rm` only changes the declaration, so the studio goes away within
about one pass (30s). The teardown releases the reservation like any other.

To stop a standing session for good, remove its name. Tearing its studio down by
hand (`studio teardown`) only restarts it: the next pass raises it again.

## Admission

A standing grant must name a declared session. Otherwise it is denied.

- **With `store-postgres`**, the grant goes through the allocation ledger and is
  capped at **the number of names declared on the role**
  ([serve.md](serve.md#postgres-store-backend-store-postgres)). Standing
  reservations are counted separately from ephemeral and personal ones, so a dead
  name's slot can't be taken by another kind. A standing reservation leaked by a
  crash between the grant and the raise is reclaimed by the reconcile sweep.
- **On the file store** there is no ledger. A declared name is admitted by its
  declaration alone. The one-actor-id-per-name rule is what keeps it to one studio.

## Messaging

A standing session has no ticket and no owner, so it has **no default
recipient**. Its `send` must name a `to` (a `human:` or `channel:` target from the
project roster), or it answers `400 no default recipient: pass "to"`
([intercom.md](intercom.md)). What it may address is limited by its role's
`--addressing`, like any studio. Its preamble tells it this. A reply to one of its
messages wakes it, and it `read`s the reply. See
[comms-addressing.md](comms-addressing.md) for the targets and
[discord.md](discord.md#egress-the-reply-loop) for the reply loop.

A standing studio also appears in `studio list`. Its Instance records `name` and
`session_kind: standing`.
