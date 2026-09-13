---
summary: The managed-cove supervisor operator guide — harbor's runtime registry of raised coves (Phase/Activity, leases), the `at-harbor cove raise|list|status|teardown` verbs, the `runtime:` serve-config block, and the Attach stream + its in-cove `cove-master` client.
read_when: You are raising or tearing down a managed cove through harbor, inspecting the runtime registry, tuning the supervisor's lease/reconcile timing, or configuring/running the in-cove `cove-master` client.
owns: the operator-facing managed-cove runtime story — the Instance registry (Phase vs Activity, leases), the `cove` verbs, the `runtime:` serve-config block, the Attach stream, and the `cove-master` client that dials it
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a cove is raised for
tier: leaf
updated: 2026-09-13
---

# Managed coves (the supervisor)

Harbor keeps a **runtime registry** of the coves it manages — each a raised
Worker or standing Manager — and drives their lifecycle: raise → record → track
status → tear down, self-healing across harbor restarts. This is the spine the
resident dispatcher and standing teammates build on.

> **This slice is the spine.** `at-harbor serve` wires a **placeholder launcher**:
> `cove raise` records a live registry entry but does **not** start a real cove
> yet. Raising coves on a real backend is a later slice.

## The model

A managed cove has a durable **identity** (a roster [Actor](roster.md) — token,
grants) and a runtime **Instance** keyed by that actor's id. The Instance carries
two status fields with different owners:

- **Phase** (harbor owns it): `raising → live → terminating → gone`, or
  `→ lost → terminating` when the reconciler finds it dead.
- **Activity** (the cove reports it, only while `live`): `running | waiting |
  blocked | done`. Reporting `done` tells harbor to tear the cove down.

Each Instance is **leased** to the harbor process supervising it. A lease has a
TTL; the owner renews it, and if it expires another process may take over
(reconnect/failover). On restart, harbor re-adopts live Instances from the store
instead of abandoning them — so in-progress work survives a restart.

## The `cove` verbs

```
at-harbor cove raise    --id spider-42 --role guest [--project acme] [--unit AET-9]
at-harbor cove list     # id  role  unit  phase  activity  lease-holder
at-harbor cove status   --id spider-42 --activity waiting
at-harbor cove teardown --id spider-42
```

- `cove raise` enrolls the identity (the role must exist — fail-closed) and
  records a `live` Instance. The role supplies scope, exactly as with
  [enroll](roster.md).
- `cove status` reports the cove's activity; `--activity done` triggers teardown.
- `cove teardown` tears the cove down and revokes its identity (idempotent).

All `cove` verbs take the admin-client flags (`--app`/`--admin-url`/`--token`);
see [operators.md](operators.md).

## Tuning the supervisor (`runtime:`)

Optional serve-config block (see [serve.md](serve.md) for the whole config):

```yaml
runtime:
  lease-ttl: 60s            # how long a lease is valid without renewal
  reconcile-interval: 30s   # reconcile + renew cadence (must be < lease-ttl)
```

Defaults are `60s` / `30s`. `reconcile-interval` must be strictly less than
`lease-ttl` so a live owner always renews before its own lease expires.

Design rationale (the identity/runtime split, the lease/steal model, the
reconciler) lives in
[`../../superpowers/specs/2026-09-12-harbor-cove-supervisor.md`](../../superpowers/specs/2026-09-12-harbor-cove-supervisor.md).

## The Attach stream

A managed cove holds one bidirectional gRPC stream to harbor — its **Attach**
stream. It authenticates the stream with two credentials: its actor **identity
token** (the same token the broker checks) **and** its **per-instance launch
secret**, minted at `raise` time. Over the stream the cove sends Activity
reports up and heartbeats to renew its lease; harbor pushes lifecycle
**control** down — teardown or wake. Harbor binds the Attach gRPC server at
`runtime.listen` (see [serve.md](serve.md)).

> **This slice added the harbor-side server; the in-cove client below dials it.**
> The stream still isn't multiplexed onto the broker's `:443` — that's a later
> slice, and so is a real supervised workload (see below).

## cove-master (the in-cove client)

`cove-master` is the cove's side of the Attach stream: the `internal/covemaster`
client library plus the `cove-master` binary (`cmd/cove-master`). It dials
harbor's runtime listener, authenticates the stream, reports Activity up, and
reacts to control (teardown, wake) pushed down — reconnecting with backoff
across transient drops. The client imports only the generated `attachpb` types
and grpc, never `internal/harbor`, so it stays a lean, server-free dependency
for whatever process embeds it.

It reads its configuration from the environment (no SSH, no host
orchestration):

```
AT_HARBOR_RUNTIME_ADDR    harbor's runtime (Attach) listener, host:port
AT_HARBOR_IDENTITY_TOKEN  the cove's identity token
AT_HARBOR_LAUNCH_SECRET   the per-instance launch secret, minted at raise time
AT_COVE_WORKDIR           the agent's cwd + where .at-task/worker-result.json is read (default /home/agent/workspace)
AT_COVE_AGENT_PROMPT_FILE path to the file holding the agent's prompt (required)
```

cove-master runs the agent as a **headless one-shot** (`internal/agentrun`):
it spawns `claude -p --dangerously-skip-permissions "<prompt>"` in `AT_COVE_WORKDIR`,
reports `running`, and when the agent exits reads `.at-task/worker-result.json`
(the same contract as the dispatch worker):

- `ok` → the client reports `done` and the supervisor tears the cove down.
- `needs-input` → a brief `waiting` is reported, then `done` (the dispatcher
  decides whether to re-dispatch; lingering-and-waking is a later slice).
- `error` / no result → `done` with the failure logged.

A harbor **teardown** cancels the run, which sends the agent `SIGTERM` and then
`SIGKILL` after a grace period. `wake` is a no-op for a one-shot agent.

> **Still deferred:** a persistent agent that lingers `waiting` and is woken with
> *new* input (needs the comms-hub input channel), `blocked`/escalation, and
> cove-master becoming the image entrypoint under its own non-root account
> (collapsing the SSH/systemd boot).

Design rationale (the package boundary, the Workload seam, the reconnect model,
and the agent wrapper's lifecycle mapping) lives in
[`../../superpowers/specs/2026-09-13-cove-master-client.md`](../../superpowers/specs/2026-09-13-cove-master-client.md)
and [`../../superpowers/specs/2026-09-13-cove-agent-wrapper.md`](../../superpowers/specs/2026-09-13-cove-agent-wrapper.md).
