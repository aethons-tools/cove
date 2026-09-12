---
summary: The roster/RBAC operator guide — projects, roles, grants, and enrollment; the `role`/`grant`/`ungrant`/`roster`/`enroll`/`revoke` verbs and how a Role's scope authorizes a cove at the broker.
read_when: You are deciding who can reach what on a harbor — defining roles, granting them to actors, enrolling a cove, viewing the roster, or revoking an identity.
owns: the operator-facing RBAC story — Project/Role/Actor/Grant in practice, the role/grant/ungrant/roster/enroll/revoke verbs, and the enrollment snippet
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; serve.md for destinations (what a role's scope points at); kits.md for binding a kit to a role
tier: leaf
updated: 2026-09-12
---

# Roles, grants & enrollment (RBAC)

Harbor authorizes every brokered request against a **role-based** model:

- **Actor** — one enrolled identity (an `id` + a minted token). A cove or a
  standing teammate is an Actor. Harbor stores only the token *hash*.
- **Role** — a named, reusable security class within a **Project** (a namespace).
  A Role owns the **scope**: which `destinations` it may reach, which `repos`
  (globs, for repo-scoped destinations), a default token `ttl`, and optionally a
  bound **kit** (see [kits.md](kits.md)).
- **Grant** — assigns a Role (within a Project) to an Actor. An Actor may hold
  several grants (e.g. a human or a standing manager across projects).

At request time the broker resolves an Actor's scope **additively across its
grants, per-grant**: a request for `(destination, repo)` is allowed iff *some one
grant* authorizes that whole pair — one grant's destination never recombines with
another grant's repos. Everything is **fail-closed**: an unknown actor, a grant
whose role was deleted, or a request outside scope is denied. A missing `--project`
defaults to the `default` project.

All verbs below take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md).

## Roles

```
at-harbor role add --project acme --name guest \
  --destinations anthropic,git --repos 'aethons-tools/*' --ttl 24h
at-harbor role add --project acme --name reviewer --destinations anthropic --kit review-kit
at-harbor role list [--project acme]
at-harbor role rm   [--project acme] guest
```

- `--destinations` / `--repos` are comma-separated; `--repos` are `owner/repo`
  globs matched only for repo-scoped destinations.
- `--ttl` is the default identity lifetime applied at enrollment (`0` = no
  expiry). **A role with no `--ttl` mints non-expiring tokens** — set one for
  ephemeral coves.
- `--kit` binds a registered kit by name (optional; [kits.md](kits.md)).
- Editing a role re-scopes every actor granted it on the next request (live).

## Grants

```
at-harbor grant   --id spider-18 --project beta --role reviewer
at-harbor ungrant --id spider-18 --project beta --role reviewer
```

Grants extend an existing Actor; the Actor's token stays stable as grants come and
go — which is how a standing teammate or a human accretes access over time.

## Enrollment

`enroll` creates an Actor with its first grant and mints its token once. Scope
comes from the **role** (enrollment is role-required — there are no inline
destination/repo/ttl flags):

```
at-harbor enroll --id spider-18 --project acme --role guest        # prints the connector snippet
at-harbor enroll --id spider-18 --role guest --json                # prints {"id","token"} (for tooling)
at-harbor revoke --id spider-18                                    # removes the whole Actor
```

- The role must already exist (else `enroll` fails closed).
- Without `--json`, `enroll` prints a shell **connector snippet** the Guest cove
  sources. The token is exported once as `AT_HARBOR_IDENTITY_TOKEN`, and both
  connectors reference it: `ANTHROPIC_BASE_URL=<base>/anthropic` with the token as
  the key, and git `insteadOf github.com → <base>/git/` with a credential helper
  that reads the env var at run time. The token never lands in gitconfig on disk.
- `--base-url` (or the app profile's `base-url`) sets the broker base in the
  printed snippet; `--json` needs no base URL.
- Hardened coves usually **auto-enroll** themselves at session start rather than
  using a hand-run snippet — see [`../at-cove-config.md#harbor`](../at-cove-config.md).

## The roster

```
at-harbor roster
```

Lists every Actor with its grants and each grant's **effective** destinations/repos
(role scope, after any per-grant overrides). Never prints a token or hash.

Design rationale (the one fault line, the additive/per-grant model) lives in
[`../../superpowers/specs/2026-09-12-harbor-actor-roster.md`](../../superpowers/specs/2026-09-12-harbor-actor-roster.md).
