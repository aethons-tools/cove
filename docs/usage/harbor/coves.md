---
summary: The managed-cove supervisor operator guide — harbor's runtime registry of raised coves (Phase/Activity, leases) and the `at-harbor cove raise|list|status|teardown` verbs, plus the `runtime:` serve-config block.
read_when: You are raising or tearing down a managed cove through harbor, inspecting the runtime registry, or tuning the supervisor's lease/reconcile timing.
owns: the operator-facing managed-cove runtime story — the Instance registry (Phase vs Activity, leases), the `cove` verbs, and the `runtime:` serve-config block
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
