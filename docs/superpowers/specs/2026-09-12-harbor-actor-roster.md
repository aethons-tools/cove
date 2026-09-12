---
what: >
  Design for COV-143 slice 1 — harbor's actor roster + role model as traditional
  RBAC. A top-level Actor (one durable identity + token) is granted Roles within
  Projects; a Role owns the security scope; the broker resolves each request's
  scope additively across the Actor's grants (per-grant existential, fail-closed).
read-when: >
  Implementing or reviewing COV-143 (the roster/role backbone), or any later
  harbor pillar (kit registry, comms hub, resident dispatcher) that builds on the
  Actor / Role / Grant model.
status: approved
---

# Harbor actor roster + role model (COV-143, slice 1)

## Context

The credential/egress **broker** pillar shipped end-to-end (#151–#159). Today the
broker's security scope is **flat and per-identity**: `harbor.Identity` carries
`Destinations`, `Repos`, and `Expiry` inline, set loosely at enrollment. `Project`
and `Role` exist only as free-text strings on the identity; nothing ties them
together or validates them, and `Decide` scopes a request straight off the
identity's inline fields.

COV-143 is the second of harbor's five pillars and the **backbone** the comms hub
(COV-145) and resident dispatcher (COV-146) both hang off. It gives harbor a
durable **roster** under the design's config object model
(`docs/superpowers/specs/2026-09-10-harbor-design.md`, §Actors and §Role).

An early draft made **Project a tenant** — an Actor lived inside exactly one
Project with exactly one Role. That is too restrictive: an **External** (a human
peer) is inherently cross-project (one person, many repos), and a **Manager**
(standing role-bound cove) may steward several projects. So this slice adopts a
**traditional RBAC** shape instead:

```
Actor (top-level identity)  ──granted──▶  Role (per-project)   within   Project
   ID, TokenHash, Expiry                    Name                         (namespace)
   Grants: [ Grant ]                        Scope{Destinations,Repos,TTL}

Grant = { Project, Role, Overrides? }   ← the many-to-many join (embedded in the Actor)
```

A **Worker** is just an Actor with one grant; a **Manager** or **human** accretes
grants over time while its identity **and token stay stable** — which also feeds
harbor's durability driver. This spec is **slice 1**: the role-scoped broker spine
plus the grant join. It defers the explicit Project object (declared Roster +
escalation policy), the Role→Kit binding (COV-144), grant-level expiry, and any UI.

## Goal

An Actor/Role/Grant model in harbor's store, with an admin API/CLI to manage Roles,
grant/revoke Roles to Actors, and view the roster. Enrollment creates an Actor with
its first grant; the broker scopes each request **additively across the Actor's
grants** (per-grant existential), failing closed when no grant authorizes it.

## Object model

Three persisted concepts plus an embedded join. A **Project** in this slice is just
a *namespace* for Roles — a name, no standalone record yet. The explicit Project
object (declared Roster + escalation policy) is a later slice.

### Role (per-project)

Owns the reusable security scope — the evolved dispatch "class":

```go
// Scope is a Role's security envelope: which destinations an actor granted this
// role may reach, which repos (for repo-scoped destinations), and the default
// token lifetime applied at enrollment.
type Scope struct {
	Destinations []string      `json:"destinations"` // destination names, e.g. ["anthropic","git"]
	Repos        []string      `json:"repos"`        // owner/repo globs, e.g. ["aethons-tools/*"]
	TTL          time.Duration `json:"ttl"`          // 0 = no expiry; applied at enroll
}

// Role is a named, reusable security class within a project.
type Role struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
}
```

Roles are defined **per project**: `guest` in `acme` and `guest` in `beta` are
distinct records with their own repo globs. (A global role *library* is a later
refinement; per-project roles keep slice 1 simple and explicit.)

### Actor (top-level) + Grant (the join)

`Actor` evolves today's `Identity` (renamed). It is a **top-level identity**: the
auth fields (`ID`, `TokenHash`, `Expiry`) plus a list of **Grants**. It no longer
carries a single project/role or inline scope.

```go
// Override lets one grant narrow/replace fields of its Role's scope. A nil field
// inherits the Role; a set field REPLACES the Role's field (no merge).
type Override struct {
	Destinations []string `json:"destinations,omitempty"`
	Repos        []string `json:"repos,omitempty"`
}

// Grant assigns a Role (within a Project) to an Actor, optionally narrowed.
type Grant struct {
	Project   string    `json:"project"`
	Role      string    `json:"role"`
	Overrides *Override `json:"overrides,omitempty"`
}

// Actor is one enrolled top-level identity. The raw token is never stored — only
// its hash — so a leaked store yields no usable credentials. Grants are the
// Actor's role assignments across projects (RBAC).
type Actor struct {
	ID        string    `json:"id"`
	TokenHash string    `json:"token_hash"`
	Grants    []Grant   `json:"grants"`
	Expiry    time.Time `json:"expiry"` // zero = no expiry
}
```

`MintToken`/`HashToken` are unchanged. The rename `Identity → Actor` is contained:
`grep` confirms `harbor.Identity` has no references outside `internal/harbor`.

**Expiry is per-actor** (the token's life), set once at enroll as
`now + firstRole.Scope.TTL` (zero TTL ⇒ no expiry). Adding grants later does **not**
change it; grant-level/temporary-assignment expiry is deferred.

## Scope resolution: additive, per-grant existential (fail-closed)

A broker request carries **no project/role selector** — it is just "clone `acme/x`"
or "call Anthropic." So the broker cannot pick a single grant; permissions are
**additive across grants**, the RBAC answer. The check is **per-grant existential**
to avoid cross-grant repo-scope bleed:

> A request for `(destination, ownerRepo)` is allowed iff **some single grant**
> authorizes the whole pair — its resolved Role has the destination **and** (if the
> destination is repo-scoped) that same grant's repos cover the repo.

Unioning fields globally would be wrong: a narrow `git→[acme/*]` grant plus an
unrelated grant carrying `[beta/*]` must **not** let git reach `beta/*`. The
existential check keeps each grant's (destination, repos) coupling intact.

`Decide` stays a **pure** function (no store access) so it remains testable without
a VM or a live store. Grants are resolved to scopes *before* `Decide`:

```go
// EffectiveScope layers a grant's overrides over its role's scope. Each field is
// the override when set, else the role's. (Override replaces, never merges.)
func EffectiveScope(g Grant, r Role) Scope

// Decide answers, for an actor and the list of effective scopes its grants
// resolve to, whether (dest, ownerRepo) is permitted. Fails closed: expired, or no
// grant authorizes the pair ⇒ error + zero Decision.
func Decide(a Actor, scopes []Scope, dest Destination, ownerRepo string, now time.Time) (Decision, error)
```

The proxy (`proxy.go`/`policy.go`) resolution order per request:

1. Lookup Actor by token hash (`store.Lookup`). Unknown ⇒ deny (unchanged).
2. For each grant, resolve `store.GetRole(grant.Project, grant.Role)`. A grant whose
   role is **missing is skipped** (it contributes no scope — a deleted role silently
   stops authorizing, fail-closed). Build `scopes := []Scope{ EffectiveScope(g, r) }`.
3. `Decide(actor, scopes, dest, ownerRepo, now)` — expiry check, then the per-grant
   existential test above. If `scopes` is empty (no grant resolved), deny.

Editing a Role's scope re-scopes every actor granted it, on the next request (live).

## Store + migration

### v3 on-disk shape

```go
type storeFile struct {
	Roles        map[string]map[string]Role `json:"roles"`        // project → roleName → Role
	Actors       map[string]Actor           `json:"actors"`       // keyed by TokenHash
	Destinations map[string]Destination     `json:"destinations"` // keyed by Name (unchanged)
}
```

### Store interface

Destination methods are unchanged. Identity methods are renamed to actor methods,
and role + grant methods are added:

```go
type Store interface {
	// Actors (was: Add/Lookup/Remove/ListIdentities)
	AddActor(a Actor) error                         // error if the id already exists
	Lookup(tokenHash string) (Actor, bool)
	RemoveActor(id string) error                    // revoke the whole identity
	ListActors() []Actor

	// Grants (mutate an existing actor's role assignments)
	AddGrant(actorID string, g Grant) error         // upsert by (project,role); error if actor absent
	RemoveGrant(actorID, project, role string) error

	// Roles / projects
	PutRole(project string, r Role) error           // upsert; auto-creates the project namespace
	GetRole(project, name string) (Role, bool)
	RemoveRole(project, name string) error
	ListRoles(project string) []Role
	ListProjects() []string                          // project names that own roles or grants

	// Destinations (unchanged)
	AddDestination(d Destination) error
	RemoveDestination(name string) error
	ListDestinations() []Destination
	Match(reqPath string) (Destination, bool)
}
```

`DefaultProject = "default"` is the harbor-side default used when enrollment or a
grant names no project (the enrollment-naming decision for COV-143).

### Migration (v1 → v2 → v3), behavior-preserving

A v3 load detects older shapes and migrates in memory (then persists v3 on first
write). The migration **preserves every actor's effective scope exactly**:

- v1 (bare `map[tokenHash]Identity`) → v2 is the existing path; chain into v3.
- For each legacy `Identity`:
  - `project := orElse(id.Project, DefaultProject)`, `role := orElse(id.Role, "default")`.
  - Emit `Actor{ID, TokenHash, Expiry, Grants: [ {Project: project, Role: role} ]}`
    (exactly one grant).
  - If no `Role(project, role)` exists yet, synthesize it from this identity's scope:
    `Role{Name: role, Scope: {Destinations: id.Destinations, Repos: id.Repos, TTL: 0}}`.
  - If the Role already exists and this identity's `(Destinations, Repos)` differ from
    it, record the delta as the grant's `Overrides` so the effective scope is
    identical to the legacy value.

## Admin API

| Method & path | Body / result |
|---|---|
| `GET /admin/projects` | `["default","acme"]` |
| `GET /admin/roles?project=acme` | `[RoleSummary…]` (project, name, destinations, repos, ttl-seconds) |
| `POST /admin/roles` | `{project?, name, destinations, repos, ttl_seconds}` → 201; `project` defaults to `default`; `name` required |
| `DELETE /admin/roles/{project}/{name}` | 204; 404 if absent |
| `GET /admin/roster` | `[ActorSummary…]` — id, expiry, and per-grant `{project, role, effective destinations/repos}` |
| `POST /admin/enrollments` | `{id, project?, role, overrides?}` → `{id, token}`; creates the Actor with its first grant; **no top-level dests/repos/ttl** |
| `POST /admin/actors/{id}/grants` | `{project?, role, overrides?}` → 201; adds a grant to an existing Actor |
| `DELETE /admin/actors/{id}/grants/{project}/{role}` | 204; 404 if absent |
| `DELETE /admin/enrollments/{id}` | 204 (revoke the whole Actor; route name unchanged) |

Enrollment (`POST /admin/enrollments`) — "create an Actor with its first grant":

- Body: `{ID, Project (optional → DefaultProject), Role (required), Overrides (optional)}`.
- `role` missing/empty ⇒ 400; role not found under the project ⇒ **400 (fail
  closed)**; `id` already exists ⇒ 409. The scope fields
  (`destinations`/`repos`/`ttl_seconds`) are **removed from `EnrollBody`**; since
  `decode` is non-strict (no `DisallowUnknownFields`), a stale caller that still
  sends them is *silently ignored* (scope always comes from the role) rather than
  erroring — a gentle, non-breaking server behavior.
- `Enroll` signature becomes:
  `Enroll(store Store, id, project, role string, overrides *Override, now time.Time) (string, error)`.
  It resolves the Role (error if absent), mints the token, computes
  `Expiry = now + Role.Scope.TTL` (zero TTL ⇒ no expiry), and stores the Actor with
  one grant.
- The `GET /admin/enrollments` listing is **replaced** by `GET /admin/roster` (its
  only consumer is the `at-harbor` CLI, updated in the same slice — no external
  consumers, so no alias is kept).

`operatorID(r)` is logged on every mutation (role put/rm, enroll, grant add/rm,
revoke), as today.

## CLI (`cmd/at-harbor`)

- `at-harbor role add --project P --name R --destinations a,b --repos o/r,… --ttl 24h`
- `at-harbor role list [--project P]`
- `at-harbor role rm --project P --name R`
- `at-harbor roster` — the roster table (actor → its grants + effective scope);
  replaces the enrollment listing.
- `at-harbor enroll --id ID [--project P] --role R` — creates an Actor with its
  first grant; **drops `--destinations`, `--repos`, `--ttl`** (scope comes from the
  role); `--role` defaults to `guest`.
- `at-harbor grant --id ID [--project P] --role R` — adds a grant to an existing
  Actor.
- `at-harbor ungrant --id ID [--project P] --role R` — removes a grant.
- `at-harbor revoke --id ID` — removes the whole Actor (unchanged).
- `adminclient` gains `PutRole`/`ListRoles`/`RemoveRole`/`ListProjects`/`Roster`/
  `AddGrant`/`RemoveGrant`, and the trimmed `Enroll` params (scope fields removed).

## at-cove side (the breaking change)

`harborPlan`'s auto-enroll argv (COV-141) currently is:

```go
args := []string{"enroll", "--json", "--id", coveID, "--role", "guest",
	"--destinations", "anthropic,git", "--ttl", "24h"}
if src.Project != "" { args = append(args, "--repos", src.Project) }
```

It becomes scope-free:

```go
args := []string{"enroll", "--json", "--id", coveID, "--role", "guest"}
```

This keeps at-cove **go-oidc-free** (an argv-only change; no new imports — verified
by `go list -deps ./cmd/at-cove | grep -i oidc` staying empty). The operator must
pre-create the `guest` Role in the `default` project with the appropriate scope
(e.g. `--destinations anthropic,git --repos 'aethons-tools/*' --ttl 24h`).

**Deliberate trade, documented:** per-cove repo narrowing (today's `--repos
<src.Project>`) goes away this slice — the `guest` Role's repo glob governs all
coves enrolling into it. Narrowing via a per-grant override on enroll is a noted
follow-up.

## Invariants preserved

- **Fail closed:** unknown actor, no resolvable grant, or a grant whose role was
  deleted ⇒ deny; zero behavioral softening.
- **No cross-grant bleed:** the per-grant existential check keeps each grant's
  (destination, repos) coupling; permissions are additive across grants but never
  recombine one grant's destination with another's repos.
- **Credentials stay config-sourced** — Roles carry *names* (destination names,
  repo globs), never secret values; the store still holds only token *hashes*.
- **No secrets on disk / argv / logs** — unchanged; role/roster/grant data is
  non-secret.
- **go-oidc boundary intact** — at-cove/connect/dispatchrun still never import
  `internal/harbor`; the at-cove change is argv text only.
- **Hermetic tests** — `Decide`/`EffectiveScope` stay pure; store tests use temp
  files; at-cove tests drive `runner.Fake`.

## Testing

Hermetic unit tests (no Docker/network/VM):

- **store/migration:** v1→v3 and v2→v3 preserve every actor's effective scope
  (including the override-delta case for a shared `(project,role)` with differing
  scope); role upsert/get/list/remove; grant add (upsert by project+role)/remove;
  `AddActor` rejects a duplicate id; `ListProjects` union of role- and
  grant-owning namespaces; `RemoveActor`/`Lookup`/`ListActors`.
- **resolution:** `EffectiveScope` (role-only; override replaces destinations;
  override replaces repos; nil override inherits). `Decide`:
  - additive — a (dest, repo) allowed by a *second* grant passes;
  - **no bleed** — `git→[acme/*]` grant + a `[beta/*]`-only grant denies `git beta/x`;
  - expiry denies; empty scopes (no grant) denies; missing-role grant skipped.
- **admin API:** role add/list/delete; roster reflects per-grant effective scope +
  overrides; `enroll` requires a role, rejects an unknown role (fail closed),
  rejects a duplicate id (409), no longer accepts inline scope; grant add/remove;
  `operator=` logged on mutations.
- **adminclient:** round-trips for role CRUD, grant add/remove, roster, trimmed
  enroll.
- **at-cove:** `harborPlan` auto-enroll argv carries `--role` and **no** scope flags.

## Docs

- `docs/usage/at-cove-config.md` (harbor section): the `guest` Role is now a
  prerequisite an operator creates; note the per-cove repo-narrowing caveat.
- harbor admin usage doc: `role add/list/rm`, `grant`/`ungrant`, `roster`; the new
  enroll contract; the RBAC model (Actor granted Roles across Projects; additive
  scope).
- `docs/OVERVIEW.md` / relevant INDEX rows if the command surface table is touched.

## Deferred (follow-ups)

- Explicit **Project** object: declared Roster (humans, Manager roles, channels) +
  escalation policy.
- **Role → Kit** binding (COV-144, kit registry).
- **Global role library** (roles reused across projects) and **grant-level
  expiry** (temporary assignments).
- Per-cove repo narrowing on enroll (per-grant override at enroll time).
- Web UI / observability over the roster (design stage #5).
