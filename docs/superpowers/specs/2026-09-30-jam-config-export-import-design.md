---
kind: design-spec
subject: at-jam — export/import the control-plane config aggregates of the jam store (actors, roles, kits, destinations, projects) as a single backup file, excluding studio (instances) and intercom (unread/squawk) state, for exact restore onto a fresh server
status: draft
date: 2026-09-30
read-when: implementing, slicing, or reviewing the `at-jam export` / `at-jam import` commands, the `GET`/`POST /admin/config` bulk endpoints, or the `Store.ExportConfig`/`ImportConfig` methods
---

# at-jam config export / import

## One paragraph

Add `at-jam export` and `at-jam import` so an operator can back up the jam
control-plane **config** to a single file and restore it exactly onto a fresh or
rebuilt server. "Config" is the five aggregate tables the jam store owns —
`actors` (with their token hashes and grants), `roles` (with egress and standing
sessions), `kits` (all versions plus the pin), `destinations`, and `projects`
(roster, escalation policy, chat service). It is **not** the runtime/studio state
(`instances`), the intercom UI's unread cursors (`intercom_unread_cursors`), or
the separate intercom-squawk (`internal/intercom/intercompg`) and allocator
(`internal/allocator/allocpg`) stores. Because a faithful backup must carry the
stored **token hashes** (so existing actor tokens keep working after restore) and
only the server can read/write those, export and import are **server-side bulk
endpoints**; the CLI streams the file.

## Goals

- **Exact backup & restore.** After `export` then `import` onto a fresh server,
  every actor's *existing* token still authenticates (the token hash round-trips),
  every role/kit/destination/project is byte-identical, and kit version history +
  the pin are preserved.
- **Config only.** Studio state (`instances`), intercom unread cursors, intercom
  squawks, and allocator events are never included.
- **One file, operator-chosen format.** A single self-contained file; JSON by
  default, YAML on request.
- **Safe restore.** Import is fail-closed: it refuses unless the target's config
  is empty, so it can never silently clobber or half-merge an existing server.

## Non-goals

- No merge/upsert or partial import. (`refuse-unless-empty` only; re-syncing a
  populated server is out of scope.)
- No export/import of runtime state, squawks, unread cursors, or allocator data.
- No re-minting of tokens. Import restores the stored hashes as-is; it does not
  issue new tokens.
- No live migration between running servers; this is file-mediated.

## Background: what the jam store holds

The jam Postgres store (and its file-store equivalent) has exactly seven tables
(`internal/jam/migrations/0001_init.sql`, `0002_intercom_unread_cursors.sql`):

| Table | Aggregate | Classification |
|---|---|---|
| `actors` | `Actor{ID, TokenHash, Grants, Expiry}` | **config** |
| `roles` | `Role` (Scope incl. Egress; Allocation incl. Standing) | **config** |
| `kits` | `Kit{Name, Current, Versions}` | **config** |
| `destinations` | `Destination` (references a credential *name*, never a value) | **config** |
| `projects` | `Project{Roster, Escalation, EscalationByCategory, ChatService}` | **config** |
| `instances` | `Instance` (raised coves — studio state) | excluded (state) |
| `intercom_unread_cursors` | per-(participant,channel) unread Seq | excluded (state) |

`schema_migrations` is infrastructure and excluded. Egress and standing sessions
have **no** separate tables — they live inside the role doc
(`Role.Scope.Egress`, `Role.Allocation.Standing`); roster, escalation, and chat
service live inside the project doc. So serializing the five config aggregates as
their full docs captures all nested config with no special-casing.

The intercom squawk log (`internal/intercom/intercompg`) and allocator events
(`internal/allocator/allocpg`) are entirely separate stores and are out of scope.

### Why server-side, and the token hash

`Enroll` (`internal/jam/enroll.go`) mints a random token, returns it once, and
stores only `TokenHash = sha256(token)` (`HashToken`, `internal/jam/identity.go`).
The raw token is never persisted. A backup that keeps working after restore must
therefore carry the **hash** (not the token): after import, an actor presenting
its unchanged token still hashes to a stored `token_hash` and resolves. The admin
API cannot do this — its only way to create an actor is `Enroll`, which mints a
*new* token. Reading and writing `token_hash` is possible only inside the server,
so export/import are server endpoints, not client-side fan-out over the existing
API.

## Architecture

### Persistence model (existing)

Both `FileStore` and `PostgresStore` embed a shared `*memState`
(`internal/jam/memstate.go`) holding the five config maps plus `instances` and
`unread`. `FileStore` mutates memState then saves the whole file;
`PostgresStore` mutates memState then write-throughs each doc row (it already has
full-doc writers such as `putActorDoc` and `putProject`, and loads all rows into
memState at startup).

### The snapshot type

A new versioned envelope in package `jam`, reusing the existing aggregate structs
(which already carry every nested config field), JSON-tagged for the wire and file:

```go
type ConfigSnapshot struct {
    Version      int                        `json:"version"`      // = ConfigSnapshotVersion (1)
    ExportedAt   time.Time                  `json:"exported_at"`
    Actors       []Actor                    `json:"actors"`
    Roles        map[string]map[string]Role `json:"roles"`        // project → name → Role
    Kits         []Kit                      `json:"kits"`         // full: Current + all Versions
    Destinations []Destination              `json:"destinations"`
    Projects     []Project                  `json:"projects"`
}

const ConfigSnapshotVersion = 1
```

`Version` lets a future schema change be detected; import rejects an unknown
version rather than silently mis-loading.

### Store interface: two new methods

```go
// ExportConfig returns a deep copy of the five config aggregates. Runtime state
// (instances, unread cursors) is never included.
ExportConfig() ConfigSnapshot

// ImportConfig loads a snapshot into an EMPTY store. It fails closed: if any
// config aggregate already has an entry, it returns an error naming the
// non-empty aggregate(s) and writes nothing. On success every aggregate is
// restored exactly (token hashes and kit version maps included).
ImportConfig(s ConfigSnapshot) error
```

- **`ExportConfig`** is implemented once on `*memState`: under `RLock`, deep-copy
  the five config maps into the snapshot. Shared by both backends. `instances`
  and `unread` are never read.
- **`ImportConfig`** is per-backend because persistence differs, but shares a
  memState-level helper for the emptiness precondition and the map assignment:
  - Precondition: if `len(actors)|roles|kits|dests|projects) > 0` for any,
    return an error listing the non-empty aggregate names (counts only, no
    values). This is the source of the endpoint's 409.
  - `FileStore.ImportConfig`: assign the maps on memState, then `save()`.
  - `PostgresStore.ImportConfig`: within a single `pgx` transaction, insert every
    doc into its table; on commit, assign memState (or reload). Atomic — a failed
    import leaves the store untouched.
  - Kits are written as full docs (exact `Current` + `Versions` map), **not**
    through `PushKit` (which auto-increments and would not reproduce arbitrary
    version numbers).

### Admin endpoints

Behind the existing admin-token guard (`internal/jam/admin.go`), wire format
**always JSON**:

- `GET /admin/config` → `200` with `ConfigSnapshot` JSON (calls
  `store.ExportConfig()`).
- `POST /admin/config` → body is a `ConfigSnapshot` JSON.
  - `store.ImportConfig()` succeeds → `204 No Content`.
  - target not empty → `409 Conflict` with a message naming the non-empty
    aggregate(s).
  - malformed body / unknown `Version` → `400 Bad Request`.

### adminclient

```go
func (c *Client) ExportConfig() (jam.ConfigSnapshot, error) // GET /admin/config
func (c *Client) ImportConfig(s jam.ConfigSnapshot) error   // POST /admin/config; maps 409 to a clear "target not empty" error
```

### CLI commands

Both follow the existing command pattern (parse flags, resolve admin URL + token,
`adminclient.New`, dispatch), registered in `cmd/at-jam/main.go`:

- `at-jam export [--format json|yaml] [file|-]`
  - Calls `c.ExportConfig()`, gets a `ConfigSnapshot`.
  - `--format json` (default) → marshal JSON; `--format yaml` → marshal YAML.
  - Positional file path, or `-`/omitted → **stdout**.
- `at-jam import [--format json|yaml] <file|->`
  - Reads the file (or stdin for `-`).
  - Format: if `--format` is set, honor it; otherwise **sniff** (leading `{`
    after whitespace → JSON, else YAML).
  - Unmarshal to `ConfigSnapshot`, call `c.ImportConfig()`.
  - A 409/"not empty" error is reported clearly and exits non-zero.

The **wire is JSON**; YAML exists only at the file boundary (marshal/unmarshal in
the CLI). This matches the repo config RoE (human-facing YAML, stored/wire JSON)
and keeps the server free of a format flag.

## Security & observability

- The export file **intentionally contains token hashes** — auth material. It is
  a sensitive artifact; document it as such. (Hashes are one-way: the file yields
  no usable raw tokens, but a matching hash still authenticates, so the file must
  be protected like a credential store.)
- **No upstream secret values are ever in config**: destinations reference a
  credential *name*, resolved to a value only at request time. So the snapshot
  carries names, never secrets.
- **Snapshot contents are never logged** at any level, consistent with the
  logging RoE (`docs/usage/observability.md`). Diagnostics name aggregates and
  counts only.

## Testing (TDD, hermetic)

Hermetic by default (`FileStore` / `runner.Fake`); Postgres-specific behavior
behind the `integration` build tag.

- **Store (`internal/jam`)**
  - `ExportConfig` returns all five aggregates and omits `instances` + `unread`
    (populate a store with instances and unread cursors; assert they are absent
    from the snapshot).
  - Round-trip: export → fresh `FileStore` → `ImportConfig` → `ExportConfig`
    deep-equals the original (including kit `Versions`/`Current` and actor
    `TokenHash`).
  - Fidelity: after import, `Lookup(presentedToken)` resolves the actor whose
    hash was restored.
  - `ImportConfig` refuses when any single aggregate is non-empty (one test per
    aggregate), names it, and writes nothing.
- **Admin handler (`httptest`)**: `GET /admin/config` returns the snapshot;
  `POST` into empty → 204; `POST` into non-empty → 409; malformed/unknown version
  → 400; both require the admin token.
- **adminclient**: `ExportConfig`/`ImportConfig` marshal/parse correctly; 409 maps
  to a clear error.
- **CLI (`cmd/at-jam`, table-driven `main_test` style)**: export to file and to
  stdout; JSON default and `--format yaml`; import from file and stdin; format
  sniffing; export→import fresh-store fidelity end to end; non-empty import
  surfaces the 409 and exits non-zero.
- **Postgres (`integration`)**: `ImportConfig` is transactional — a mid-import
  failure leaves the store empty; a successful import is durable across reload.

## Docs

Updated in the same change (per repo policy):

- `docs/OVERVIEW.md` — add `export`/`import` to the `at-jam` command surface.
- The owning `docs/usage/` doc — document the two commands, the config-only
  scope, the refuse-unless-empty semantics, the format flag, and the
  file-is-sensitive warning. Create a leaf only if none owns the subject.
- `docs/INDEX.md` / the usage `INDEX.md` — keep in sync; every link resolves.

## Open questions

None outstanding. (Use case: backup & restore. Actors: include token hashes.
Architecture: server-side bulk endpoints. Import: refuse unless empty. Format:
specify in command, default JSON. Kits: all versions + pin.)
