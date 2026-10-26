---
summary: Operator guide to remote Studio Managers — enabling them, the `at-jam manager add|list|rotate|revoke` verbs, how a manager connects to Jam, and what happens on disconnect, reconnect and revoke.
read_when: You register, rotate or revoke a Studio Manager, or debug a manager connection (refused stream, version mismatch, a manager shown disconnected, strays, a revoke refused).
owns: the operator-facing Studio Manager story — `runtime.studio-managers`, the `manager` verbs and admin API, the Connect stream's auth and lifecycle, and disconnect/reconnect/revoke behaviour
prereqs: coves.md#managers-and-placement for how a raise picks a manager; serve.md for the serve config
tier: leaf
updated: 2026-10-26
---

# Studio Managers

A **Studio Manager** is a remote process that hosts studios on another machine and dials *into* Jam; Jam registers it under a name next to the in-process `local` manager.
The manager binary itself arrives in a later plan (plan 3); this doc covers Jam's side.
How a raise chooses among managers is in [Managers and placement](coves.md#managers-and-placement).

## Enable it

Set `runtime.studio-managers: true` in the [serve config](serve.md).
It works with or without a `runtime.launcher` block; with neither, Jam keeps the placeholder launcher.
The admin UI dashboard then shows a read-only Managers panel (name, connected, version, studios/capacity, last seen, strays) and the studios table gains a Manager column.

**Warning:** turning `runtime.studio-managers` off while remote studios exist makes Jam treat them as dead, because their manager is no longer registered.
Jam then probes their studios as dead: each is torn down (a no-op, since no manager is registered to call), its identity revoked and its record dropped; the containers stay on the host.

## The verbs

All are clients of the admin API (see [operators.md](operators.md) for the client flags).

| Verb | Does |
|------|------|
| `at-jam manager add <name>` | Registers a manager and prints `manager <name> token (shown once): <token>`. |
| `at-jam manager rotate <name>` | Mints a new token, same output line; the old token stops working. |
| `at-jam manager list` | One line per manager: `<name>\tconnected=<bool>\tversion=<v>\tstudios=<n>\tcapacity=<c>\tlast-seen=<RFC3339\|->\tstrays=<n>`. |
| `at-jam manager revoke <name> [--force]` | Removes the manager; see Revoke. |

Admin API: `GET`/`POST /admin/managers`, `POST /admin/managers/{name}/rotate`, `DELETE /admin/managers/{name}?force=true`.
Names match `^[a-z0-9][a-z0-9-]{0,62}$`; `local` is reserved.
Jam stores only a hash of the token, so a lost token means `rotate`.
Managers are not part of config export/import ([backup.md](backup.md)); a restored Jam re-registers them with `manager add`.

## How a manager connects

The manager calls the gRPC method `cove.manager.v1.StudioManager/Connect` on the cove-facing TLS port (:443) with metadata `authorization: Bearer <token>`.

- **Security:** the same RPC is also served on the plaintext dev listener `runtime.listen`, which is not loopback-enforced.
  Managers must use the TLS :443 endpoint, because the dev listener carries the bearer token in clear.
- The `Hello` name must match the token's name, otherwise the stream is refused (`PermissionDenied`).
- The `Hello` version must equal Jam's exactly, otherwise it is refused with `version mismatch: jam <J>, manager <M>`.
- One stream per name: a new connection replaces the old one.
- Heartbeats every 10s; a manager silent for 30s is dead and shown disconnected.

## Disconnect

While a manager is disconnected, Jam does not guess:

- Probe answers Unknown, so its studios are not reaped.
- Teardown, Pause, Unpause and egress changes fail with "manager unavailable" until it is back; the reconciler re-drives teardown every tick.
- A raise interrupted by a dropped stream fails and is not retried on another manager.
  Any studio it left is reported as a stray on reconnect.

## Reconnect

On reconnect the manager reports its inventory and Jam reconciles:

- Jam's studios missing from the inventory become Lost, then are torn down.
- Studios the manager reports that Jam does not know are **strays**: logged, shown in `list` and the UI, and left running.
- Pinned [standing sessions](standing-sessions.md) whose state is missing there are warned about; they start fresh and keep their pin.

## Revoke

`revoke` refuses while the manager hosts studios: `manager <name> hosts <n> studios; revoke with force to forget them`.
`--force` forgets them: they are marked Lost at once, and the next reconcile pass revokes their identities and removes them from Jam's records.
Their containers are left on the host for the operator to remove.

Manager metrics are in [monitoring.md](monitoring.md#metrics).
