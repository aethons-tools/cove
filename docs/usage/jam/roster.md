---
summary: The roster/RBAC operator guide — projects, roles, grants, and enrollment; the `role`/`grant`/`ungrant`/`roster`/`enroll`/`revoke` verbs and how a Role's scope authorizes a studio at the broker.
read_when: You are deciding who can reach what on a Jam — defining roles, setting a role's raw egress, granting roles to actors, enrolling a studio, viewing the roster, or revoking an identity.
owns: the operator-facing RBAC story — Project/Role/Actor/Grant in practice, the role/grant/ungrant/roster/enroll/revoke verbs (incl. a role's `--max-ephemeral`/`--max-personal`/`--max-personal-per-owner` allocation policy and its `--idle-after`/`--nag-every`/`--reclaim-after` personal idle settings), a role's egress policy (`egress set|show|clear` and its routes), and the enrollment snippet
prereqs: INDEX.md for the service overview; operators.md for the admin-client flags; serve.md for destinations (what a role's scope points at); kits.md for binding a kit to a role
tier: leaf
updated: 2026-10-02
---

# Roles, grants & enrollment (RBAC)

Jam authorizes every brokered request against a **role-based** model:

- **Actor** — one enrolled identity (an `id` + a minted token). A studio or a
  standing teammate is an Actor. Jam stores only the token *hash*.
- **Role** — a named, reusable security class within a **Project** (a namespace).
  A Role owns the **scope**: which `destinations` it may reach, the
  **credential** the broker injects for each (`credentials`; a destination
  without one uses its own default `cred-name` — see
  [serve.md](serve.md#destinations)), which comms `addressing` targets it may
  message (globs, e.g. `human:*`; comms plane — see
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
conflict, or a request outside scope is denied. A missing `--project`
defaults to the `default` project.

All verbs below take the admin-client flags (`--app`/`--admin-url`/`--token`); see
[operators.md](operators.md).

## Roles

```
at-jam role add --project acme --name guest \
  --destinations anthropic,git=git-pat-acme --ttl 24h
at-jam role add --project acme --name reviewer --destinations anthropic --kit review-kit
at-jam role add --project acme --name worker --destinations anthropic,git --max-ephemeral 4
at-jam role add --project acme --name pair --destinations anthropic,git \
  --max-personal 3 --max-personal-per-owner 1
at-jam role list [--project acme]
at-jam role rm   [--project acme] guest
```

- `--destinations` is comma-separated; each entry is a destination name,
  optionally `name=credential` to pick the credential the broker injects for it
  (a bare name uses the destination's default `cred-name`). `role list` and
  `roster` print the same syntax.
- A role's per-destination credentials travel on the admin API as
  `credentials` (`{"git": "git-pat-acme"}`) on role put/list, on each roster
  grant, and on a grant/enroll `overrides` (which **replaces** the role's map).
  Writes are rejected (400) when a mapping names a destination the scope
  doesn't allow, or a credential the serve config doesn't declare.
- `--addressing` (comma-separated comms target globs, e.g. `human:*,channel:eng-help`)
  scopes which comms targets the role's actors may `send(to=…)`. This is a
  separate plane from `destinations`; see
  [comms-addressing.md](comms-addressing.md) for the target space and the
  Project's roster of humans/channels the globs resolve against.
- `--ttl` is the default identity lifetime applied at enrollment (`0` = no
  expiry). **A role with no `--ttl` mints non-expiring tokens** — set one for
  ephemeral studios.
- `--kit` binds a registered kit by name (optional; [kits.md](kits.md)).
- `--max-ephemeral N` is the role's **allocation policy**: the cap on its
  concurrent ephemeral (Requisitioner-raised) sessions. Jam's Allocator reads it
  live from the roster on each grant, so an edit applies on the next grant with no
  restart. `0` (the default) = unset — the Requisitioner's `max-concurrent` applies
  as the fallback ([requisitioner.md](requisitioner.md#config-runtimerequisitioner)).
  `role list` shows it as `max-ephemeral=N`.
- `--max-personal N` caps the role's concurrent **personal sessions** across all
  owners (the pool). `--max-personal-per-owner M` caps one owner's share (`0` =
  the pool cap only). `0` for `--max-personal` (the default) means the role
  admits no personal sessions. Both are read live, like `--max-ephemeral`.
  `role list` shows them as `max-personal=N` and `max-personal-per-owner=M`. What
  a personal session is: [personal-sessions.md](personal-sessions.md).
- `--idle-after D`, `--nag-every D`, `--reclaim-after D` (durations, e.g. `4h`)
  set the role's personal-session **idle ladder**: nag the owner once a session
  has waited on them `idle-after` (`0` = default 4h), then every `nag-every`
  (`0` = default 24h), and reclaim it after `reclaim-after` (`0` = never).
  Negative values are refused. The admin API carries them as
  `idle_after_seconds`/`nag_every_seconds`/`reclaim_after_seconds`; `role list`
  shows them as `idle-after=…`, `nag-every=…`, `reclaim-after=…`. What the ladder
  does: [personal-sessions.md](personal-sessions.md#the-idle-ladder).
- A role's **standing sessions** (named, always-running teammates) are declared
  on the role too (`allocation.standing`), but with their own verb,
  `at-jam standing add|list|rm`, not with `role add` flags. Re-running
  `role add` keeps them. See [standing-sessions.md](standing-sessions.md).
- A role's **egress policy** is managed with its own verb,
  `at-jam egress set|show|clear`, not `role add` flags. Re-running `role add`
  (or saving the role in the [admin UI](ui.md)) keeps it. See
  [Role egress](#role-egress).
- Editing a role re-scopes every actor granted it on the next request (live).

### Role egress

A role can set the **raw egress** of every studio Jam raises for it. That is
the domains the studio's squid allows beyond the sealed base and the kit's
always-on infra list (model provider, self-hosted GitLab, Jam host).

```
at-jam egress set   --project acme --role fenced registry.npmjs.org,.pypi.org
at-jam egress set   --project acme --role fenced --none   # an empty policy
at-jam egress show  --project acme --role fenced          # "kit default", "none", or the list
at-jam egress clear --project acme --role fenced          # back to the kit default
```

- **No policy (the default) = the kit's list.** The studio gets the kit's
  `image.allowed-domains` as before, so existing roles are unchanged. A **set but
  empty** policy (`--none`) means nothing beyond the base and infra lists.
- **The kit is the ceiling.** The role's list *replaces* the kit's list, but every
  domain must be covered by the kit's
  [`image.allowed-domains`](../at-cove-config.md#imageallowed-domains). A leading-dot
  entry (`.x.com`) covers `x.com` and its subdomains; an exact entry covers only
  itself. Jam checks only syntax and normalizes (lowercase, dedupe, sort, drop
  entries a wildcard in the list already covers); **the box enforces the ceiling**.
  A role asking for more fails its raise with an error naming the domain.
- **Reaches running studios within one reconcile pass.** Each studio records the
  policy it runs under; Jam's [reconcile pass](coves.md#egress-drift) re-applies
  the role's current one when they differ (`clear` restores the kit's list). A
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

Admin API (the verbs wrap these; each write keeps every other role field):

| Route | Result |
|---|---|
| `PUT /admin/roles/{project}/{role}/egress` body `{"domains":[…]}` | **204**; stores the normalized list. **400** names a bad domain; **404** unknown role. An empty list is valid. |
| `GET /admin/roles/{project}/{role}/egress` | **200** `{"managed":bool,"domains":[…]}`; `managed:false` = kit default. **404** unknown role. |
| `DELETE /admin/roles/{project}/{role}/egress` | **204**; reverts to the kit default. **404** unknown role. |

## Grants

```
at-jam grant   --id spider-18 --project beta --role reviewer
at-jam ungrant --id spider-18 --project beta --role reviewer
```

Grants extend an existing Actor; the Actor's token stays stable as grants come and
go — which is how a standing teammate or a human accretes access over time.

## Enrollment

`enroll` creates an Actor with its first grant and mints its token once. Scope
comes from the **role** (enrollment is role-required — there are no inline
destination/ttl flags):

```
at-jam enroll --id spider-18 --project acme --role guest        # prints the connector snippet
at-jam enroll --id spider-18 --role guest --json                # prints {"id","token"} (for tooling)
at-jam revoke --id spider-18                                    # removes the whole Actor
```

The printed snippet is the identity's **connector** — the env and git routing
its role's destinations declare ([connector.md](connector.md)); `--json` adds it
as `connector`. A conflict among those destinations fails the enrollment (409).

- The role must already exist (else `enroll` fails closed).
- Without `--json`, `enroll` prints a shell **connector snippet** the Guest studio
  sources. The token is exported once as `AT_JAM_IDENTITY_TOKEN` (its deprecated
  name is exported from it, for older images — see
  [renamed-from-harbor.md](renamed-from-harbor.md)), and both
  connectors reference it: `ANTHROPIC_BASE_URL=<base>/anthropic` with the token as
  the key, and git `insteadOf github.com → <base>/git/` with a credential helper
  that reads the env var at run time. The token never lands in gitconfig on disk.
- `--base-url` (or the app profile's `base-url`) sets the broker base in the
  printed snippet; `--json` needs no base URL.
- Hardened studios usually **auto-enroll** themselves at session start rather than
  using a hand-run snippet — see [`../at-cove-config.md#jam`](../at-cove-config.md).

## The roster

```
at-jam roster
```

Lists every Actor with its grants and each grant's **effective** destinations
(role scope, after any per-grant overrides). Never prints a token or hash.

Design rationale (the one fault line, the additive/per-grant model) lives in
[`../../superpowers/specs/2026-09-12-harbor-actor-roster.md`](../../superpowers/specs/2026-09-12-harbor-actor-roster.md).
