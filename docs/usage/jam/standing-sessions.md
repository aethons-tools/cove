---
summary: Standing sessions — named, long-lived teammates an operator declares on a role (e.g. `reviewer/alice-bot`); Jam keeps exactly one studio running per name, raises it again (resuming its conversation and workspace) if it dies, and tears it down with its state when the name is removed. Covers declaring, the `standing add|list|rm|reset|upgrade` verbs and admin routes, keep-alive with backoff, reset, upgrading to the current image, dismissal, admission, and messaging.
read_when: You want a role to have a permanent, named agent running (a standing teammate), or you are removing, resetting or upgrading one (e.g. its image is stale after an at-jam, kit or model-spec change), or a standing session keeps restarting / isn't coming up and you want to know why, or you need to know how a standing session reaches people.
owns: the standing-session story — declarations on a role (`RoleAllocation.Standing`), the declaration ↔ session map (session ids), the `standing add|list|rm|reset|upgrade` verbs and `/admin/roles/{project}/{role}/standing` routes, the standing reconciler (keep-alive, restart under the same name, backoff, dismissal), reset, upgrade (the queue, prepare-first, idle wait, `--wait`), standing admission (declared-name cap, file-store behavior), and how a standing session messages people
prereqs: roster.md for roles; standing-state.md for what a restart keeps; coves.md for what a raised studio does and resident mode; comms-addressing.md for `send(to=…)` targets and a role's addressing; discord.md for the Discord reply loop; serve.md for `store-postgres` and the allocation ledger
tier: leaf
updated: 2026-10-07
---

# Standing sessions

A **standing session** is a named, long-lived agent that an operator declares on
a role, such as `reviewer/alice-bot`. The declaration is the desired state.
Jam keeps **exactly one studio running per declared name**. If the studio dies, Jam
raises it again under the same name, and it **resumes**: its conversation and
workspace persist across restarts ([State across restarts](#state-across-restarts)).
When the name is removed, Jam tears the studio down and deletes that state. Its admission is capped by the allocation ledger.

## Declaring one

```
at-jam standing add  --project acme --role reviewer --name alice-bot --prompt-file alice.md
at-jam standing list --project acme --role reviewer
at-jam standing rm    --project acme --role reviewer alice-bot
at-jam standing reset --project acme --role reviewer alice-bot
at-jam standing upgrade --project acme --role reviewer alice-bot
```

Like every admin verb they take `--app`/`--admin-url`/`--token`
([operators.md](operators.md)); without `--project` they act on the `default` project.

- **add** declares the name with its prompt. The prompt file is read on the host
  and sent in the request body. It never goes on argv. Both `--name` and
  `--prompt-file` are required.
- **list** prints each declared name, its session id (`-` until first raised), and that
  studio's `phase` and [`image`](coves.md#the-studio-verbs) status (`-` when none runs,
  or — with a warning on stderr — when Jam can't list studios).
- **rm** removes the name. Jam then tears its studio down and deletes its state
  (see [Dismissal](#dismissal)).
- **reset** keeps the name but deletes its studio and state, so Jam raises it
  fresh (see [Reset](#reset)).
- **upgrade** queues a restart on the current image, keeping its state
  (`--force`, `--wait`; see [Upgrading a standing session](#upgrading-a-standing-session)).

The declarations live on the role (`allocation.standing`, a list of
`{name, prompt}`). Each write reads the role, changes only that list, and writes it
back, so the role's scope, kit and other allocation settings are kept. Re-running
`role add` for the role keeps its standing declarations
([roster.md](roster.md#roles)).

Each declaration **is a session** (Jam's standing-session map): its first raise
starts one with a minted `ses_…` id (`standing list` shows it); every restart or
upgrade sets that same session up in a new studio — same id, conversation and
workspace — and a [reset](#reset) ends it. Sessions from before session ids keep
their `standing-<project>-<role>-<name>` id. A name is refused with **400** if
it is already declared on the role or contains `/`.

## The admin routes

| Route | Result |
|---|---|
| `POST /admin/roles/{project}/{role}/standing` `{name, prompt}` | **201**. **400** if the name or prompt is missing, the name is already declared or contains `/`. **404** for an unknown role. |
| `GET /admin/roles/{project}/{role}/standing` | **200** with the declared list, prompts included, each with its pending `upgrade` state (omitted when none). **404** for an unknown role. |
| `DELETE /admin/roles/{project}/{role}/standing/{name}` | **204**. **404** if the role or the name doesn't exist. |
| `POST /admin/roles/{project}/{role}/standing/{name}/upgrade[?force=true]` | **202** `{"pending":true,"state":…}` queued; **200** already current or not started yet. **409** a reset is pending or the session id is held by another studio; **404**; **503** without the standing reconciler. See [Upgrading](#upgrading-a-standing-session). |
| `POST /admin/roles/{project}/{role}/standing/{name}/reset` | **200** `{"pending":false}`: studio torn down and state deleted, declaration kept. **202** `{"pending":true,"reason":…}`: still in progress (see [Reset](#reset)). **404** if the role or the name doesn't exist. **409** if the session id is held by a studio that isn't this session. **503** without the standing reconciler. |

The role page in the admin UI has **Upgrade** and **Reset** buttons (with a
confirm) beside **Dismiss** on each standing session ([ui-pages.md](ui-pages.md)).

## What "kept alive" means

Jam's **standing reconciler** runs whenever `at-jam serve` runs. It needs no
Requisitioner and no config. Every 30s it walks every role's declarations. For each
name:

- **Its studio is live**: nothing to do.
- **It has no studio**: Jam asks the Allocator for a standing slot (see
  [Admission](#admission)), then raises the studio as the name's session, with
  the declared prompt as-is. Who it is (the standing session `<name>`) and how to
  message people come from the Boilerplate of its [session context](session-context.md).
- **Its studio died**: the supervisor detects it and tears it down (see
  [coves.md](coves.md#the-model)). That frees its reservation, and the next pass
  raises the name again. The new studio **resumes** the old one's conversation
  and workspace ([State across restarts](#state-across-restarts)).

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

## State across restarts

A standing studio keeps its conversation and workspace in named Docker volumes
that outlive its container and re-attach when the name is raised again; a
restarted session resumes with `claude --continue`. No teardown deletes them —
only [Reset](#reset) and [Dismissal](#dismissal) do. The volumes, their labels
and the resume rules are in [standing-state.md](standing-state.md).

## Reset

`standing reset` (or the UI's **Reset**) tears the name's studio down, **deletes
its volumes and ends its session**, keeping the declaration; the reconciler then
starts a **new session** (a new id, so a new inbox too) with the declared prompt. It runs inside the reconciler (one pass
at a time) and clears the name's raise backoff. Use it when a session's
conversation or workspace has gone bad. A name that is down has its volumes
deleted all the same. If the teardown or the deletion fails (a volume still in
use), the reset is **pending**: the name is held back from raising — it would
re-attach the old state — and every pass retries until it completes. The pending
mark is in memory: a Jam restart forgets it, and the name is raised on whatever
state remains.

## Upgrading a standing session

A standing studio keeps the image it was raised on. When `standing list` or the
role page shows [`image=stale`](coves.md#the-studio-verbs) — after a new at-jam
build, a build-changing kit edit, or a [model-spec](model-specs.md) change —
queue an upgrade with `standing upgrade` (or the role page's **Upgrade** button,
highlighted when stale). The request only records intent and answers at once:
**202** `{"pending":true,"state":"queued"}`, or **200**
`{"pending":false,"reason":"already current"}` when the studio's image is `ok`
(unless `--force`; `unknown` is queued). The reconciler's passes then:

1. **Prepare** (`preparing`): it builds the image a raise would run now, off its
   lock, and waits until that image is ready. Nothing is torn down meanwhile,
   and a slow or failed build doesn't count as a raise failure (no backoff).
2. **Wait for idle** (`waiting-for-idle: <state>`): right before the teardown
   it re-checks that the session is between episodes — `idled`, or live and
   `waiting`, `blocked` or `done`. `running`, `raising`, an unreported activity,
   and [`holding`](turn-end.md#holding) (background tasks a teardown would kill)
   are busy: the upgrade waits, it is never refused. `--force` skips the wait.
3. **Restart**: a plain teardown — **its volumes are kept** — then a raise of the
   same session, which **resumes its conversation and workspace** like any
   restart (its Docker cache starts clean: it is the studio's). A failed teardown stays pending (`teardown-failed: <reason>`) and
   is retried every pass; a failed raise backs the name off as usual. A name
   with no studio is just raised, its backoff cleared.

`standing list` shows a pending upgrade as `upgrade=<state>` (and the role page
row as an **upgrade:** pill); `standing upgrade --wait` polls it until done
(`--wait-timeout`, default 15m) and exits 0 once the studio runs an `ok` (or `unknown`) image.
A pending reset refuses an upgrade (**409**; the reset raises it fresh anyway).
Pending upgrades are **in memory**: re-run `standing upgrade` after a Jam
restart. Jam never upgrades on its own — a restart interrupts a teammate.

## Dismissal

Once a name is no longer declared, or its role is removed, the next pass tears its
studio down and **deletes its volumes** once no container uses them (retrying
every pass until it can), so declaring the name again starts fresh. This also
covers a name dismissed while its studio was down. `standing rm` only changes
the declaration, so the studio goes away within about one pass (30s). The
teardown releases the reservation like any other.

To stop a standing session for good, remove its name. Tearing its studio down by
hand (`studio teardown`) only restarts it: the next pass raises it again,
resuming its state.

## Admission

A standing grant must name a declared session. Otherwise it is denied.

- The grant goes through the allocation ledger and is
  capped at **the number of names declared on the role**
  ([serve.md](serve.md#postgres-store-store-postgres)). Standing
  reservations are counted separately from ephemeral and personal ones, so a dead
  name's slot can't be taken by another kind. A standing reservation leaked by a
  crash between the grant and the raise is reclaimed by the reconcile sweep.

## Messaging

A standing session's home is its own **session channel** ([intercom.md](intercom.md)):
a `send` with no `to` reaches whoever is in it. Any project member may join it
(from [`/me`](intercom-ui.md), or by posting); a session whose addressing allows
`session:<name>` may post there. A `to` posts elsewhere, within its role's
`--addressing`. Its [session context](session-context.md) says so, and a reply
wakes it with its own resume text ("… `send` without `to` posts to your own
channel; pass `to` to answer anywhere else."); it `read`s the reply. See
[comms-addressing.md](comms-addressing.md) for the targets and
[discord.md](discord.md#egress-the-reply-loop) for the reply loop.

A standing studio also appears in `studio list`. Its Instance records `name` and
`session_kind: standing`.
