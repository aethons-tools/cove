---
summary: The roster/RBAC operator guide — projects, roles, grants, and enrollment; a role's file in the config repo, the `role list`/`grant`/`ungrant`/`actors`/`enroll`/`revoke` verbs and how a Role's scope authorizes a studio at the broker.
read_when: You are deciding who can reach what on a Jam — adding or changing a role (its scope, kit, allocation, raw egress), granting roles to actors, enrolling a studio, viewing the roster, or revoking an identity.
owns: the operator-facing RBAC story — Project/Role/Actor/Grant in practice, the role file's fields (`scope`, `kit`, `model-spec`, the `allocation` policy and personal idle ladder, `turn-end`, `collaborate`), the grant/ungrant/actors/enroll/revoke verbs, a role's egress policy (`scope.egress`, `egress show` and its route), the enrollment snippet, and the in-progress identity registry (project ids, users, memberships, connections, accounts)
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; serve.md for destinations (what a role's scope points at); kits.md for binding a kit to a role
tier: leaf
updated: 2026-11-01
---

# Roles, grants & enrollment (RBAC)

Jam authorizes every brokered request against a **role-based** model:

- **Actor** — one enrolled identity (an `id` + a minted token). A studio or a
  standing teammate is an Actor. Jam stores only the token *hash*.
- **Role** — a named, reusable security class within a **Project** (which must
  exist first, except `default` — see [projects.md](projects.md)).
  A Role owns the **scope**: which `destinations` it may reach, the
  **credential** the broker injects for each (`credentials`; a destination
  without one uses its own default `cred-name` — see
  [serve.md](serve.md#destinations)), which comms `addressing` targets it may
  message (globs, e.g. `user:*`; comms plane — see
  [comms-addressing.md](comms-addressing.md)), a default token `ttl`, and
  optionally a bound **kit** (see [kits.md](kits.md)).
- **Grant** — assigns a Role (within a Project) to an Actor. An Actor may hold
  several grants (e.g. a human or a standing manager across projects).

At request time the broker resolves an Actor's scope **additively across its
grants**: a request for a destination is allowed iff *some* grant lists it, and
the injected credential is that grant's mapping. Two grants that allow the same
destination but map it to **different** credentials deny the request rather than
pick one. Jam does no per-repo policy: what a credential can reach is the
credential's own scope (e.g. a fine-grained PAT per project). Everything is
**fail-closed**: an unknown actor, a grant whose role was deleted, a credential
conflict, or a request outside scope is denied. A verb's missing `--project`
defaults to the `default` project.

All verbs below take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md).

## Roles

A role is a file in the config repo, `projects/<project>/roles/<name>.yml`
([config-git.md](config-git.md#layout)), and changes only by a commit: by
hand, or on a branch in the admin console ([config-branches.md](config-branches.md);
the role page: [ui-projects.md](ui-projects.md#role-pages)). Editing a role
re-scopes every actor granted it on the next request.

```yaml
# projects/acme/roles/pair.yml
kind: role
version: 1
scope:
  destinations: [anthropic, git]
  credentials: {git: git-pat-acme}   # optional per-destination credential
  addressing: ["user:*", "channel:eng-help"]
  ttl: 24h
kit: review-kit                      # optional; unset = the default kit
model-spec: fast                     # optional; unset = claude-default
allocation: {max-ephemeral: 4, max-personal: 3, max-personal-per-owner: 1}
```

`jam role list [--project acme]` prints the roles; `role inspect` shows what a raise would get ([inspect.md](inspect.md)).

- `scope.destinations` names destinations (each uses its default `cred-name`);
  `scope.credentials` maps a destination to another credential
  (`{git: git-pat-acme}`). `role list` and `actors` print them as
  `name` or `name=credential`. On the admin API a role's map is `credentials`
  on role list, on each roster grant, and on a grant/enroll `overrides`
  (which **replaces** the role's map). A mapping that names a destination the
  scope doesn't allow, or a credential the serve config doesn't declare, is refused.
- `scope.addressing` (comms target globs, e.g. `user:*`, `channel:eng-help`)
  scopes which comms targets the role's actors may `send(to=…)`. This is a
  separate plane from `destinations`; see
  [comms-addressing.md](comms-addressing.md) for the target space and the
  Project's roster of humans/channels the globs resolve against.
- `scope.ttl` is the default identity lifetime applied at enrollment (unset =
  no expiry). **A role with no `ttl` mints non-expiring tokens** — set one for
  ephemeral studios.
- `kit` binds a kit by name ([kits.md](kits.md#binding-a-kit-to-a-role)); `model-spec` binds a [model-spec](model-specs.md#binding-a-role) by name (must exist; unset = `claude-default`); `role list` shows `model-spec=…`.
- `allocation.max-ephemeral` is the role's **allocation policy**: the cap on its
  concurrent ephemeral sessions — Requisitioner-raised and manual
  (`studio raise`, the admin UI) alike. Jam's Allocator reads it
  live on each grant, so an applied change counts on the next grant. Unset = the Requisitioner's `max-concurrent`
  ([requisitioner.md](requisitioner.md#config-runtimerequisitioner)).
- `allocation.max-personal` caps the role's concurrent **personal sessions** across all
  owners (the pool); `max-personal-per-owner` caps one owner's share (unset =
  the pool cap only). An unset `max-personal` means the role
  admits no personal sessions. Both are read live, like `max-ephemeral`.
  What a personal session is: [personal-sessions.md](personal-sessions.md).
- `allocation.idle-after`, `nag-every`, `reclaim-after` (durations, e.g. `4h`)
  set the role's personal-session **idle ladder**: nag the owner once a session
  has waited on them `idle-after` (unset = 4h), then every `nag-every`
  (unset = 24h), and reclaim it after `reclaim-after` (unset = never).
  Negative values are refused. API: `idle_after_seconds`/`nag_every_seconds`/`reclaim_after_seconds`. What the ladder
  does: [personal-sessions.md](personal-sessions.md#the-idle-ladder). (Not to be confused with `turn-end: {idle-timeout, on-idle: wake|teardown, time-zone}`, API `idle_timeout_seconds`/`on_idle`/`time_zone`: the role's [turn-end policy](turn-end.md#idle-timeout).)
- A role's **collaboration policy** says which rights the project's members get
  on its studios: `collaborate: {preview: members|none, ssh: members|none}`
  (defaults `members` and `none`); other values are refused. API: `collaborate_preview`/`collaborate_ssh`. Per-session share grants are
  removed when the session ends. How access is decided:
  [collaboration.md](collaboration.md).
- A role's **standing sessions** (named, always-running teammates) are
  `allocation.standing` (`[{name, prompt}]`); `jam standing list|reset|upgrade`
  operates them. See [standing-sessions.md](standing-sessions.md).
- A role's **egress policy** is `scope.egress`: see [Role egress](#role-egress).
- A role's session context is its `context:` ([session-context-authoring.md](session-context-authoring.md)).

### Role egress

A role can set the **raw egress** of every studio Jam raises for it. That is
the domains the studio's squid allows beyond the sealed base and the kit's
always-on infra list (model provider, self-hosted GitLab, Jam host).

```yaml
# projects/acme/roles/fenced.yml
scope:
  egress: [registry.npmjs.org, .pypi.org]   # [] = an empty policy; no egress: = the kit's list
```

```
jam egress show --project acme --role fenced   # "kit default", "none", or the list
```

- **No policy (the default) = the kit's list.** The studio gets the kit's
  `egress:` list, so existing roles are unchanged. A **set but
  empty** policy (`egress: []`) means nothing beyond the base and infra lists.
- **The kit is the ceiling.** The role's list *replaces* the kit's list, but every
  domain must be covered by the kit's
  `egress:` list ([kits.md](kits.md#the-egress-ceiling-cov-208)). A leading-dot
  entry (`.x.com`) covers `x.com` and its subdomains; an exact entry covers only
  itself. Jam checks only syntax and normalizes (lowercase, dedupe, sort, drop
  entries a wildcard in the list already covers); **the box enforces the ceiling**.
  A role asking for more fails its raise with an error naming the domain.
- **Reaches running studios within one reconcile pass.** Each studio records the
  policy it runs under; Jam's [reconcile pass](coves.md#egress-drift) re-applies
  the role's current one when they differ (removing `egress:` restores the kit's list). A
  paused studio gets it when it resumes, before its agent is woken.
- **Fails closed.** A re-apply that fails is retried on the next pass; the third
  consecutive failure — or a failed re-apply on resume — tears the studio down, so a
  studio never keeps running under a policy other than its role's. A standing session
  is then raised again under the new policy; an ephemeral or personal one ends.
  A policy outside the kit's ceiling fails every time, so it reaches that teardown
  (the log names the domain).
- Domains follow the rule `^\.?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`
  after lowercasing: at least two labels, no scheme, port, path, `*` or whitespace.
- Grant overrides don't touch egress.
- `role list` shows the state as `egress=kit`, `egress=none` or
  `egress=<a.com,.b.org>`.

How it is applied in the box: [the egress model](../../OVERVIEW.md#egress-four-additive-allow-lists-and-a-ceiling);
when in the raise: [coves.md](coves.md#raising-a-real-managed-studio); on a running
studio: [coves.md](coves.md#egress-drift).

Admin API: `GET /admin/roles/{project}/{role}/egress` answers **200**
`{"managed":bool,"domains":[…]}` (`managed:false` = kit default), **404** for an
unknown role.

## Grants

```
jam grant   --id spider-18 --project beta --role reviewer
jam ungrant --id spider-18 --project beta --role reviewer
```

Grants extend an existing Actor; the Actor's token stays stable as grants come and
go — which is how a standing teammate or a human accretes access over time.

## Enrollment

`enroll` creates an Actor with its first grant and mints its token once. Scope
comes from the **role** (enrollment is role-required — there are no inline
destination/ttl flags):

```
jam enroll --id spider-18 --project acme --role guest        # prints the connector snippet
jam enroll --id spider-18 --role guest --json                # prints {"id","token","connector"} (for tooling)
jam revoke --id spider-18                                    # removes the whole Actor
```

The snippet is the identity's [connector](connector.md) (also in `--json` as
`connector`); a conflict among the role's destinations fails the enrollment (409).

- The role must already exist (else `enroll` fails closed).
- Without `--json`, `enroll` prints a shell **connector snippet** the Guest studio
  sources. The token is exported once as `JAM_IDENTITY_TOKEN`; everything else references
  it ([connector.md](connector.md)), and it never lands in gitconfig on disk.
- `--base-url` (or the app profile's `base-url`) sets the broker base in the
  printed snippet; `--json` needs no base URL.

## The actors

```
jam actors
```

Lists every Actor with its grants and each grant's **effective** destinations
(role scope, after any per-grant overrides). Never prints a token or hash.

Design rationale (the one fault line, the additive/per-grant model) lives in
[`../../superpowers/specs/2026-09-12-harbor-actor-roster.md`](../../superpowers/specs/2026-09-12-harbor-actor-roster.md). The roster is moving to a Jam-wide identity registry (surrogate ids for projects, users, connections and accounts, plus project memberships; removing a user is a tombstone); a project's people are its members — users with a membership (see [comms-addressing.md](comms-addressing.md#project-members-and-rooms)) — see [`../../superpowers/specs/2026-10-06-intercom-slice1-identity-registry-design.md`](../../superpowers/specs/2026-10-06-intercom-slice1-identity-registry-design.md).
