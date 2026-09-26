---
summary: Personal sessions — a human operator's own session of a role, admitted against the role's pool and per-owner caps, owned by the roster human linked to their login, and released only by that owner; the `session request|list|release` verbs and their admin routes.
read_when: You (a human operator) want harbor to raise a session of a role for you personally, or you are setting a role's personal caps, or a `session` command answered 403/409 and you need to know why.
owns: the personal-session story — owner resolution (roster Human ↔ login), admission (pool + per-owner caps, the ledger requirement), the `session request|list|release` verbs, the `/admin/sessions/personal` routes, and owner-only release
prereqs: comms-addressing.md for the Project roster and a Human's `--login`; roster.md for roles and the `--max-personal*` caps; coves.md for what a raised cove does; serve.md for `store-postgres` and the allocation ledger
tier: leaf
updated: 2026-09-26
---

# Personal sessions

A **personal session** is a managed cove that a human operator asks harbor for
directly: harbor admits it against the role's personal caps, raises it **owned by
that human**, and only the owner can list or release it. It needs
`store-postgres`.

> **This release:** a personal session runs its prompt once, like any managed
> cove ([coves.md](coves.md)), and ends when the agent finishes. The long-lived
> conversation loop comes in a later slice.

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
- **release** tears the session down. Only its owner may release it. The
  teardown records the reservation release, which frees your slot.

## The admin routes

| Route | Result |
|---|---|
| `POST /admin/sessions/personal` `{project, role, prompt}` | **201** `{id, owner, project, role, phase}`. **403** if your login is not linked. **400** for an unknown role. **409** at capacity, or with no ledger. **502** if the grant errors or the raise fails. A failed raise releases the grant, so no slot leaks. |
| `GET /admin/sessions/personal?project=P` | **200** with your own personal sessions in P. **403** if your login is not linked. |
| `DELETE /admin/sessions/personal/{id}` | **204** after the teardown. **404** if the id does not exist or is not a personal session. **403** unless you are its owner. |

A personal session is also an ordinary managed cove. It appears in
`cove list`, and its Instance records `owner` and `session_kind: personal`. The
reconcile sweep never releases personal reservations; only the owner's release
does.
