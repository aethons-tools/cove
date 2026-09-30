---
summary: The kit registry operator guide — storing named, versioned kit configs in Jam with `kit push|list|show|versions|pin|rm`, and binding one to a role with `role add --kit`.
read_when: You are registering a kit in Jam, pushing a new version, rolling a kit's current version back, or binding a kit to a role.
owns: the operator-facing kit-registry story — the name/version/current model and the `kit` verbs + `role --kit` binding
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a kit binds to; ../at-cove-config.md for the kit config.yml schema being stored
tier: leaf
updated: 2026-09-30
---

# The kit registry

Jam stores **kit definitions** — the `config.yml` that defines a studio — so a
kit can live in Jam's data store and be referenced by a role, rather than only
as a repo-committed `.at-cove/`. This is the Jam-side registry. Today you register
kits and bind them to roles; Jam also auto-registers the [managed
kit](#the-managed-kit) and *resolves* it from here when raising a brokered studio.
Resolving an *arbitrary* role's kit by reference is still a later slice.

## The model

A registered **kit** is a name holding **immutable, monotonically-numbered
versions** of a config, behind a mutable **current** pointer:

- **push** a config → it becomes the next version (`v1`, `v2`, …) and *current*
  advances to it.
- A **role references a kit by name** (not `name@version`); the name resolves to
  *current*. Upgrades don't churn role bindings.
- **pin** *current* to an older version to **roll back** (or forward); versions
  themselves are never mutated or deleted.

The stored value is the kit's `config.yml` text (see
[`../at-cove-config.md`](../at-cove-config.md) for that schema). A registered kit
must pin its `image.base` by digest; kits whose image is a local `image/Dockerfile`
build context aren't registry-eligible yet.

## The `kit` verbs

```
at-jam kit push --name web --config ./.at-cove/config.yml   # → "pushed web v3"
at-jam kit push --name web --config -                       # read config from stdin
at-jam kit list                                             # name  current=vN  versions=K
at-jam kit show web                                         # current version's config
at-jam kit show web --version 1                             # a specific version's config
at-jam kit versions web                                     # v1, v2, v3, …
at-jam kit pin web 1                                        # roll current back to v1
at-jam kit rm web                                           # remove the kit (all versions)
```

- `kit push` **validates** the config with the same parser `at-cove` uses before
  sending it — a malformed kit is rejected client-side, not stored.
- `kit rm` is **fail-closed**: a kit that any role references can't be removed
  (the command reports the referencing role); `ungrant`/rebind the role first, or
  point the role at another kit.

All verbs take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md).

## Binding a kit to a role

```
at-jam role add --project acme --name builder --destinations anthropic,git --kit web
```

`--kit` names a registered kit (it must already exist — `role add` fails closed
otherwise). The role then resolves to that kit's *current* version. See
[roster.md](roster.md) for the rest of the role surface.

## The managed kit

When a [`runtime.launcher`](serve.md#the-launcher-runtimelauncher) is configured,
Jam auto-registers a kit named **`managed`** at startup: the launcher's base kit
with the Anthropic egress roots (`anthropic.com`, `claude.com`, `claude.ai`)
stripped, so a brokered studio reaches Anthropic only through the Jam broker (the
COV-208 egress lock — see [pool.md](pool.md)). The push is **idempotent**: an
unchanged config keeps its version across restarts, and a change bumps a new one
(so a drifted kit re-builds instead of running a stale image). It uses the same
name/version/current model as any registered kit, so `kit show managed` /
`kit versions managed` inspect it.

The supervisor carries only the kit's light **reference** (`<id>@v<n>`, a
`KitRef`) on each raise; the launcher resolves that against its own prepared-image
inventory and, on a miss, Jam resolves the full definition from *this* registry
and hands it to the launcher's `PrepareKit` to build before retrying the raise.
That light-reference / lazy-prepare handshake — and why the build context travels
as data with no source directory — is documented on the raise side in
[coves.md](coves.md#the-managed-kit-and-its-kit-prepare-protocol).

## Upgrades & rollback

- **Upgrade** a role's kit for everyone: `kit push --name web --config <new>` —
  *current* advances, and every role bound to `web` picks up the new version.
- **Roll back**: `kit pin web <older-version>` — *current* moves back; bindings are
  unchanged (they still reference `web`).

Design rationale (the versioning model, deferred image/payload-tree kits) lives in
[`../../superpowers/specs/2026-09-12-harbor-kit-registry.md`](../../superpowers/specs/2026-09-12-harbor-kit-registry.md).
