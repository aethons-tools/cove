---
summary: The kit registry operator guide — storing named, versioned kit configs in harbor with `kit push|list|show|versions|pin|rm`, and binding one to a role with `role add --kit`.
read_when: You are registering a kit in harbor, pushing a new version, rolling a kit's current version back, or binding a kit to a role.
owns: the operator-facing kit-registry story — the name/version/current model and the `kit` verbs + `role --kit` binding
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; roster.md for the role a kit binds to; ../at-cove-config.md for the kit config.yml schema being stored
tier: leaf
updated: 2026-09-12
---

# The kit registry

Harbor stores **kit definitions** — the `config.yml` that defines a cove — so a
kit can live in harbor's data store and be referenced by a role, rather than only
as a repo-committed `.at-cove/`. This is the harbor-side registry; a managed cove
*resolving* its kit from harbor is a later slice. Today you register kits and bind
them to roles.

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
at-harbor kit push --name web --config ./.at-cove/config.yml   # → "pushed web v3"
at-harbor kit push --name web --config -                       # read config from stdin
at-harbor kit list                                             # name  current=vN  versions=K
at-harbor kit show web                                         # current version's config
at-harbor kit show web --version 1                             # a specific version's config
at-harbor kit versions web                                     # v1, v2, v3, …
at-harbor kit pin web 1                                        # roll current back to v1
at-harbor kit rm web                                           # remove the kit (all versions)
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
at-harbor role add --project acme --name builder --destinations anthropic,git --kit web
```

`--kit` names a registered kit (it must already exist — `role add` fails closed
otherwise). The role then resolves to that kit's *current* version. See
[roster.md](roster.md) for the rest of the role surface.

## Upgrades & rollback

- **Upgrade** a role's kit for everyone: `kit push --name web --config <new>` —
  *current* advances, and every role bound to `web` picks up the new version.
- **Roll back**: `kit pin web <older-version>` — *current* moves back; bindings are
  unchanged (they still reference `web`).

Design rationale (the versioning model, deferred image/payload-tree kits) lives in
[`../../superpowers/specs/2026-09-12-harbor-kit-registry.md`](../../superpowers/specs/2026-09-12-harbor-kit-registry.md).
