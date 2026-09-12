---
what: >
  Design for COV-144 slice 1 — harbor's kit registry (harbor-side). Harbor stores
  named, immutably-versioned kit configs; a Role references a kit by name (which
  resolves to the kit's current version); an admin API/CLI pushes/lists/pins/removes
  kits. The cove-side "resolve my kit from harbor" path is a later slice.
read-when: >
  Implementing or reviewing COV-144 (the kit registry), binding a Role to a kit,
  or any later slice that has a managed cove resolve its kit from harbor.
status: approved
---

# Harbor kit registry + Role→Kit binding (COV-144, slice 1)

## Context

Harbor's credential/egress **broker** (#151–#159) and **actor roster / RBAC**
(COV-143, #160) pillars are shipped. COV-143 left an explicit deferred follow-up:
"Role → Kit binding (COV-144, kit registry)". This is the third of harbor's five
pillars.

Today a "kit" is a repo-committed `.at-cove/` directory whose `config.yml` is the
source of truth (`internal/kit`); `at-cove install` assembles + builds a hardened
image and freezes the resolved config + image digest into `.state/install.json`
(`internal/install`). The harbor design (`docs/superpowers/specs/2026-09-10-harbor-design.md`)
moves the kit into harbor's data store: "**Kit** now lives in harbor's data store,
referenced by a Role — not committed in the repo."

This spec is **slice 1: the harbor-side registry + the Role→Kit binding.** It is
pure `internal/harbor` + `cmd/at-harbor`, mirroring the shipped roles/destinations
machinery. It deliberately defers the at-cove-side "a managed cove resolves its kit
from harbor" integration (which crosses the go-oidc boundary and plugs into the
assemble/install pipeline) to a later slice.

## Goal

Harbor stores named kit definitions with immutable, monotonically-numbered
versions behind a mutable "current" pointer; a **Role references a kit by name**
(stable across upgrades); an admin API/CLI pushes/lists/shows/pins/removes kits and
binds a kit to a Role. Hermetic tests; docs.

## Model

### Kit (registry entry)

A **Kit** is a named entry holding immutable versions of a kit config (the
`config.yml` text), with a mutable pointer to the current version:

```go
// Kit is one named registry entry: immutable, monotonically-numbered versions of
// a kit config, behind a mutable Current pointer. A Role references a Kit by name;
// the name resolves to Current. Upgrading = push a new version (Current advances);
// rolling back = pin Current to an older version.
type Kit struct {
	Name     string         `json:"name"`
	Current  int            `json:"current"`  // the version Role references resolve to
	Versions map[int]string `json:"versions"` // version number → config.yml text (immutable)
}
```

- **push(name, config):** store as version `max(existing)+1`; advance `Current` to
  it. Re-pushing identical config still mints a new version (monotonic, no dedup —
  readable history is the chosen tradeoff).
- **pin(name, version):** set `Current` to an existing version (rollback /
  roll-forward). Versions themselves are never mutated or deleted in slice 1.

The stored value is the **raw `config.yml` text** — the store treats it as an
opaque blob. Registered kits must pin `image.base` by digest; a kit whose image is
a local `image/Dockerfile` build context is out of scope this slice (the
payload-tree problem defers).

### Role → Kit binding

`Role` (COV-143: `Role{Name, Scope}`) gains an **optional** `Kit` field — the kit
*name* (not `name@version`):

```go
type Role struct {
	Name  string `json:"name"`
	Scope Scope  `json:"scope"`
	Kit   string `json:"kit,omitempty"` // optional kit name; "" = no kit
}
```

A Role references a kit **by name** so upgrades (pushing a new version) never churn
Role bindings. Resolution: `Role.Kit` (name) → that Kit's `Current` version → its
config. Only the admin/roster surface exercises resolution this slice; the cove
consumes it in a later slice.

**Validation (fail-closed):** when `PutRole` is called with a non-empty `Kit`, the
kit name must already exist in the registry, else the call is rejected — mirroring
role-required enrollment. An empty `Kit` is always allowed (today's behavior).

## Store + migration (v4)

`storeFile` gains a `Kits` collection; `Role` gains `Kit` (additive):

```go
type storeFile struct {
	Roles        map[string]map[string]Role `json:"roles"`        // project → name → Role (Role now has Kit)
	Actors       map[string]Actor           `json:"actors"`
	Destinations map[string]Destination     `json:"destinations"`
	Kits         map[string]Kit             `json:"kits"`          // new: keyed by Kit.Name
}
```

Migration v3→v4 is **purely additive**: a v3 file (no `kits` key) loads with an
empty kit registry, and existing roles deserialize with `Kit == ""`. No data
rewrite; `Kits` is initialized to an empty map when absent. (v1/v2→v3 migration is
unchanged and chains through.)

New `Store` methods (destination/actor/grant/role methods unchanged):

```go
PushKit(name, config string) (version int, err error) // new version, advance Current; err if name/config empty
GetKit(name string) (Kit, bool)
KitConfig(name string, version int) (string, bool)     // version 0 ⇒ Current
PinKit(name string, version int) error                 // set Current; err if name/version absent
ListKits() []Kit
RemoveKit(name string) error                            // err if absent
RoleReferencingKit(name string) (project, role string, ok bool) // for fail-closed rm
```

`PushKit` is the sole writer of version numbers (no caller supplies them).
`RoleReferencingKit` scans roles so `RemoveKit`/the admin layer can reject removing
a kit a Role still binds.

## Admin API

All routes sit behind the existing operator-auth gate (inside `NewAdminHandler`
before `authMiddleware`). `operator=<sub>` is logged on every mutation.

| Method & path | Body / result |
|---|---|
| `POST /admin/kits` | `{name, config}` → 201 `{name, version}`; creates version, advances current |
| `GET /admin/kits` | `[KitSummary…]` — name, current, version count |
| `GET /admin/kits/{name}` | `{name, current, version, config}` for Current (or `?version=N`); 404 if absent |
| `GET /admin/kits/{name}/versions` | `[int]` (ascending) |
| `POST /admin/kits/{name}/pin` | `{version}` → 204; 404 if name/version absent |
| `DELETE /admin/kits/{name}` | 204; **409 if a Role references it** (fail-closed), 404 if absent |
| `POST /admin/roles` (existing) | body gains optional `kit`; `RoleBody`/`RoleSummary` gain `Kit`; a non-empty kit that doesn't exist ⇒ 400 |

Wire types:

```go
type KitBody struct {
	Name   string `json:"name"`
	Config string `json:"config"`
}
type KitResult struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}
type KitSummary struct {
	Name     string `json:"name"`
	Current  int    `json:"current"`
	Versions int    `json:"versions"` // count
}
type KitConfigResult struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
	Config  string `json:"config"`
}
type PinBody struct {
	Version int `json:"version"`
}
```

The kit **config is validated by the `at-harbor kit push` CLI** (via
`kit.ParseConfig`) before it is sent — malformed kits fail fast client-side. The
admin handler checks non-empty `name`/`config`; the `internal/harbor` store stays
free of a dependency on `internal/kit` (it stores opaque text). (`cmd/at-harbor`
may import `internal/kit`, which is go-oidc-free.)

## CLI (`cmd/at-harbor`)

- `at-harbor kit push --name N --config <file>` — reads the file (or `-` for
  stdin), parses it with `kit.ParseConfig` (reject malformed), pushes; prints
  `pushed <N> v<version>`.
- `at-harbor kit list` — `name  current=vN  versions=K` per kit.
- `at-harbor kit show N [--version V]` — prints the config text (Current or `V`).
- `at-harbor kit versions N` — prints the version numbers.
- `at-harbor kit pin N V` — sets Current to V.
- `at-harbor kit rm N` — removes the kit (surfacing the 409 "referenced by role …"
  error clearly).
- `at-harbor role add … [--kit N]` — binds a kit to the role.
- `adminclient` gains `PushKit`/`ListKits`/`GetKit`(`+version`)/`KitVersions`/
  `PinKit`/`RemoveKit`, and `RoleBody`/`RoleSummary` carry `Kit`.

## Invariants preserved

- **Fail closed:** a Role may only bind an existing kit; a kit referenced by a Role
  can't be removed (409).
- **Secrets:** kit configs declare secret *names/descriptions*, never values
  (enforced by `kit.ParseConfig` + the existing kit model) — nothing secret is
  stored; the store still holds only config text + version ints + token hashes.
- **go-oidc boundary unaffected:** all changes are harbor-side (`internal/harbor`,
  `cmd/at-harbor`, `adminclient`); `cmd/at-cove`/`connect`/`dispatchrun` are
  untouched this slice.
- **Backward-compatible store:** v3 files load unchanged; `Kits` and `Role.Kit` are
  additive.
- **Hermetic tests:** store tests use temp files; admin tests drive `httptest`; the
  CLI `push` validation test uses an in-repo valid/invalid config fixture.

## Testing

Hermetic unit tests:

- **store/migration:** `PushKit` mints v1 then v2 and advances `Current`;
  `KitConfig(name, 0)` returns the Current config, `KitConfig(name, N)` a specific
  version; `PinKit` moves `Current` and rejects an absent version; `RemoveKit`
  rejects an absent name; `ListKits`; a v3 store file (no `kits` key) loads with an
  empty registry and roles with `Kit == ""` (migration additive).
- **Role binding:** `PutRole` with a non-existent `Kit` is rejected; with an
  existing kit it persists and round-trips; `RoleReferencingKit` finds the binding.
- **admin API:** kit push (201 + version), list, show (current + `?version=`),
  versions, pin (and pin-absent → 404), rm (204), rm-while-referenced → **409**;
  role POST with `kit` of a non-existent kit → 400, with an existing kit → 201 and
  the roster/role summary reflects `Kit`; `operator=` logged on mutations.
- **adminclient:** round-trips for push/list/show/versions/pin/rm and role-with-kit.
- **CLI:** `kit push` rejects a malformed config (parse failure, non-zero exit) and
  accepts a valid one; `--config -` reads stdin; `role add --kit` sends the kit.

## Docs

- `docs/usage/INDEX.md`: a row for this spec (map-style, points at the spec).
- harbor usage / OVERVIEW: add the `kit` verbs and the Role→Kit binding to the
  `at-harbor` command surface + the RBAC/roster description, if enumerated there.

## Deferred (later slices / follow-ups)

- **Cove resolves its kit from harbor** — the at-cove side: a managed cove fetches
  its kit config from harbor (via the go-oidc-free subprocess/stdlib-sibling
  boundary) and feeds the existing assemble/install pipeline instead of reading a
  local `.at-cove/`. This is the heavier half of the DoD.
- **Local `image/Dockerfile` / payload-tree kits** in the registry (blob/artifact
  storage, not a JSON record).
- **Built-image-digest ownership** by harbor (extending the COV-78 / blessed-digest
  machinery to a harbor-resolved ref).
- **Content-hash dedup** of identical pushes; version GC/retention.
- **Migrating existing `.at-cove/config.yml` kits** into the registry (a helper).
