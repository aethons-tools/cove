# intercom slice 1: identity registry — design

**Status:** draft for review (2026-10-06).
**Parent:** [`2026-10-06-intercom-identity-and-channels-design.md`](2026-10-06-intercom-identity-and-channels-design.md) — slice 1 of 4. Read the parent for vocabulary and the decisions record.
**Delivers:** surrogate ids for projects, users, accounts, connections and sessions; Jam-wide users with project memberships (the `human` → `user` rename); tombstones and rename; ids in every stored reference outside the channel model. Channels (`Roster.Channels`) keep their names until slice 2.

Slice 1 ships as two plans, in order:

- **1a — registry:** `internal/ident`, projects / users / memberships / connections / accounts, rename and tombstones, admin API + CLI + UIs, config export/import v2, escalation tiers, the log's user/account refs.
- **1b — sessions:** session ids distinct from studios, standing / ticket / personal / manual session starts, container + volume naming, session and allocator events, relay state.

**Re-sequenced (2026-10-06, plan 1a-2):** project *references* (roles, grants and instances keyed by project id), project tombstones and project rename move from 1a to **1b**. A project name is baked into standing actor ids, allocator stream ids, session events and relay cursors, which 1b reworks anyway, and renaming a project before then would tear down its standing sessions. 1a gives projects ids and memberships only, and its store APIs stay keyed by project name.

## 1. Ids — `internal/ident` (new, stdlib-only)

```go
func New(k Kind) ID                // "<prefix>_<26 Crockford base32>": 48-bit ms time + 80-bit crypto/rand
func Parse(s string) (ID, error)   // validates prefix + body
func (id ID) Kind() Kind
```

| Kind | Prefix |
|---|---|
| project | `prj` |
| user | `usr` |
| connection | `con` |
| account | `acc` |
| session | `ses` |
| channel | `chn` (reserved; minted from slice 2) |

Ids are opaque: never parsed for meaning beyond `Kind()`, never derived from a
name, never reused. **Legacy session ids** (live actor ids at migration time, e.g.
`standing-acme-impl-spider`) are grandfathered as session ids — see §4.

## 2. Schema (`internal/jam/migrations/0006_identity_registry.sql` + a Go data step)

DDL is SQL; minting ids and reshaping jsonb docs is a Go step run once inside the
same advisory-locked migration transaction (the `MigrateModelSpecs` pattern),
recorded in `schema_migrations`.

```sql
participants(id text PK, kind text NOT NULL)            -- every id-bearing entity

projects(id PK→participants, name, status, created_at, removed_at, doc jsonb)
  UNIQUE (name) WHERE status='live'
users(id PK→participants, name, status, created_at, removed_at)
  UNIQUE (name) WHERE status='live'
user_logins(login PK, user_id→users)                    -- admin OperatorID; Jam-wide unique
user_oidc(issuer, subject, user_id→users, PK(issuer, subject))   -- NEW uniqueness (today: none)
memberships(project_id→projects, user_id→users, status, delivery jsonb, PK(project_id, user_id))
connections(id PK→participants, kind, name, status, doc jsonb)   -- doc: credential name, kind settings
  UNIQUE (name) WHERE status='live'
accounts(id PK→participants, connection_id→connections, service_uid NULL, handle NULL,
         label, user_id NULL→users, status)
  UNIQUE (connection_id, service_uid) WHERE service_uid IS NOT NULL
  UNIQUE (connection_id, handle)      WHERE handle IS NOT NULL AND status='live'
legacy_human_aliases(project_name, human_name, user_id) -- frozen at migration; read-only
roles: project → project_id (FK projects.id); PK (project_id, name)
```

`projects.doc` keeps context, resources, escalation and chat service; its
`Roster.Humans` is removed (→ users/memberships/accounts) and `Roster.Channels`
stays until slice 2.

**Lifecycle.** `status ∈ {live, removed}`; removal sets `removed_at` and keeps
the row (a tombstone). A removed entity resolves for labels ("alice (removed)")
but never for `Lookup`, addressing or login. A new entity with a removed one's
name is a different id. **Rename** updates `name` only; nothing references names,
so there are no fixups.

**Removal rules (fail-closed, mirroring today's `ErrProjectInUse`):**

- project — refused while a live role, grant, membership or session references it;
- user — removes their memberships and unlinks their accounts; refused while they
  started a live personal session;
- connection — refused while an account, project chat service or serve-config
  block references it.

## 3. Domain model (`internal/jam`)

```go
type User struct { ID ident.ID; Name string; Status Status; Logins []string; OIDC []OIDCIdentity }
type Membership struct { ProjectID, UserID ident.ID; Delivery []DeliveryProfile } // Address only
type Connection struct { ID ident.ID; Kind, Name, CredName string; Status Status }
type Account struct {
    ID, ConnectionID ident.ID
    ServiceUID, Handle, Label string
    UserID ident.ID // "" = unlinked
}
type Project struct { ID ident.ID; Name string; …; Roster struct{ Channels []Channel } }
```

`Human`, `Human.Handle`, `DeliveryProfile.UserID` and `Roster.Humans` are deleted.
`HumanByLogin`, `ParticipantByIdentity`, `HumanByIdentity`, `HumanByDiscordUser`,
`DiscordInboxOwner` and `DiscordAuthor` are replaced by `Directory` methods:

```go
type Directory interface {
    Resolve(id ident.ID) (Entry, bool)                        // {ID, Kind, Label, Status}; removed included
    Lookup(kind ident.Kind, name string) (ident.ID, bool)     // live only
    UserByLogin(login string) (User, bool)
    UserByOIDC(issuer, subject string) (User, bool)
    AccountByUID(conn ident.ID, uid string) (Account, bool)
    AccountByHandle(conn ident.ID, handle string) (Account, bool)
    IsMember(project, user ident.ID) bool
}
```

`Store` gains CRUD + `Rename*` for projects, users, memberships, connections and
accounts, keyed by id; every name→id resolution happens at the edges via
`Directory.Lookup`. `PostgresStore` keeps the write-through `memState` cache; the
uniqueness the code checks today under `rosterMu` (`roster_edit.go`) moves into
the partial unique indexes, with `memState` enforcing the same rules so `MemStore`
and Postgres stay conformant.

## 4. Sessions (plan 1b)

A **session** is the logical identity; a **studio** is one setup of it
(container, token, allocator reservation) — see the overview's Vocabulary. Restarts
and standing upgrades set the same session up in a new studio; dismissal, `end`,
ticket completion and **standing reset** end it. Code keeps the `Raise`/`cove`
names in this slice except where a touched identifier would otherwise mislead.

| Kind | Session minted when | Ended by | Found by |
|---|---|---|---|
| standing | declared (`AddStanding`) or after a reset | reset, removal of the declaration | `standing_sessions(project_id, role, name) → session_id` |
| ticket | the Requisitioner dispatches a ticket | the session ends (studio torn down) | live instance with `Unit = <identifier>` (dedup no longer recomputes `cove-<ISSUE>`) |
| personal | a personal-session request | release / end | the request's returned id |
| manual (`studio raise`, admin UI) | the command | the session ends | the returned id; the operator's `--id` becomes `Instance.Name` (a label) |

- `Actor.ID` and `Instance.ActorID` hold the session id; `Instance.Project` →
  `ProjectID`, `Instance.Owner` → `StartedBy` (a `usr_` id; comms meaning unchanged
  until slice 3).
- **The agent-data and workspace volumes belong to the session; the container
  and the `-docker` volume (in-studio Docker storage, a cache) belong to the
  studio** — so a new studio (restart, upgrade) starts with a clean Docker store,
  a change from today where it survives upgrades. Names derive from the session
  id (a session has at most one studio at a time)
  (`naming.CoveContainer(sessionID)`), labelled `harbor.cove.state=<sessionID>`.
  The standing reconciler's liveness, reset, upgrade, backoff, 409-holder check and
  `sweep` purge set key on `standing_sessions`, not on `StandingActorID`, which is
  deleted.
- **Grandfathering:** every session live at migration keeps its current id as its
  session id (registered in `participants`), so its volumes, inbox rows, Discord
  receipts and nag ids keep working. `standing_sessions` is seeded with those ids.
- `session_events` and `alloc_events`: add `project_id` / `owner_id` columns,
  backfilled by exact name→id mapping; the old text columns stay as historical
  labels. `alloc_events.stream_id` is rewritten `<project name>/<role>` →
  `<project id>/<role>` in place (exact mapping, so live reservation counts
  carry over), and `MaxPersonalPerOwner` counts by `owner_id`.
- Personal-session grants: `Override.Addressing: ["human:"+owner]` becomes
  `["user:<usr_id>"]`. **Addressing globs may name an entity by id as well as by
  name**; generated policy always uses ids so a rename can't break it.
- Human-facing text (Discord `name: ` body prefix, escalation comments) renders
  `Directory.Resolve(id).Label`, never a raw id.

## 5. The log during slice 1

The squawk schema is unchanged (slice 2 replaces it). From the 1a cutover:

- **New squawks write ids:** `user:<usr_id>`, `account:<acc_id>` (new kind,
  Reach External) and `actor:<session id>`. Channel targets stay `channel:<name>`.
- **Existing rows are not rewritten** (decision 4). Readers that key on a user —
  the me UI inbox and unread state — read **both** `user:<usr_id>` and their
  `human:<name>` rows via `legacy_human_aliases` (frozen at migration, so later
  renames don't affect it). Session inboxes need no dual read (grandfathered ids).
- **Ingress attribution:** an author maps to an account — Discord by
  `(connection, AuthorID)`; Linear by `(connection, AuthorID)` once the Linear
  relay populates `AuthorID` (comment user id; added in 1a), falling back to
  `(connection, handle)` and backfilling `service_uid`. Unknown authors create an
  unlinked account. `From` is the linked user if any, else the account.
  Bots are still never users.
- Escalation tiers become `user:<usr_id>`; the engine resolves each user's
  account on the project's tracker connection for the `@handle`.
- Unread cursors: `participant` `human:<name>` → `user:<usr_id>` and `dm:` ids
  embedding `human:<name>` are rewritten (exact per-project mapping; a name that
  maps to two users duplicates the row per user).

## 6. Users: migration from per-project humans

Run once by the Go data step:

1. **Group** every project's humans into users by strong identity: the same
   login, an overlapping OIDC `(issuer, subject)`, or the same Discord user id.
   Humans with no strong identity group by **name** — unless that would merge two
   groups that already conflict (different strong identities).
2. **Name** each user from its humans' name. A collision between different users
   keeps the name for the first (by project creation order) and renames the rest
   `<name>-<project>`; every collision is reported in the migration log
   (structured, ids and names only).
3. **Memberships:** one per (project, human) occurrence; `Delivery[].Address`
   moves onto it.
4. **Accounts:** `Handle` → a `linear` account (`handle` set, `service_uid` learned
   at first ingress); `Delivery[].UserID` → a `discord` account
   (`service_uid` set). Both linked to the user.
5. `legacy_human_aliases` gets a row per (project, human name) → user.

## 7. Connections

- Admin-managed, like projects: `at-jam connection add --kind linear --name
  linear-acme --cred linear-bot`. The credential is named, never stored.
- Serve config references connections by name:
  `runtime.requisitioner.connection: linear-acme` (replacing `tracker-token-cred`),
  `runtime.discord.connection: discord-main` (replacing `bot-token-cred`).
  `Project.ChatService` holds a connection id (set by name).
- **Back-compat:** at first start after migration, an existing
  `tracker-token-cred` / `bot-token-cred` creates connections named `linear` /
  `discord` if absent, and the old keys keep working (with a deprecation warning)
  until removed in a later release.
- Relay state files: cursor keys `service/<project name>` → `<connection id>/<project id>`,
  markers keyed by connection id; a one-time rewrite at startup, keeping a `.bak`.

## 8. The `human` → `user` rename

- **Agent-facing (`send`, `list_targets`, session context):** emit `user:`;
  accept `human:` as an input alias for one release.
- **Admin plane (API, CLI, UIs) ships with the server**, so it renames outright:
  `/admin/users`, `/admin/projects/{p}/members`, `/admin/connections`,
  `/admin/accounts`; CLI `at-jam user {add,rename,rm,list,login,oidc}`,
  `at-jam project member {add,rm,list}`, `at-jam project rename`,
  `at-jam connection …`, `at-jam account {list,link,unlink}`. Path params accept
  a name or an id. `GET /admin/roster` (which lists *actors*, not humans) becomes
  `/admin/actors`.
- **Addressing globs / tier specs:** existing `human:*` patterns are rewritten to
  `user:*` by the migration; `human:` is accepted on input for one release.
- **`serve dev-identity`:** `{project, human}` → `{user}`.
- `logging`'s "human-readable" sense of the word is out of scope.

## 9. Config export/import v2

`ConfigSnapshotVersion = 2`: adds users, memberships, connections and accounts,
and carries **ids** everywhere (projects, grants, roles, tiers), so a restore keeps
the log's references valid. Tombstoned entities are exported (they back history).
Import of a **v1** snapshot runs the same §6 migration over it. The empty-target
rule is unchanged.

## 10. Testing

- `internal/ident`: format, kind round-trip, uniqueness, sortability.
- `storetest.RunConformance` (mem + pg): CRUD, rename, tombstone resolution,
  live-name uniqueness and re-use after removal, login/OIDC/account uniqueness,
  removal refusals, `Directory` resolution.
- Migration: table-driven fixtures of v1 project docs → expected users,
  memberships, accounts, aliases and collision reports; the same fixtures through
  the v1 import path; a pg integration test of `0006` against a populated database.
- Sessions (1b): standing reconciler tests that a restart/upgrade keeps the
  session id and volumes and a reset mints a new one; dispatcher dedup by unit;
  grandfathered ids still route inbox, receipts and nags.
- Relay: attribution by account (Discord uid, Linear uid, handle fallback, unknown
  author → unlinked account); state-file rewrite.
- All hermetic (`runner.Fake`, `MemStore`); pg behind the existing integration tag.

## 11. Docs updated with the change

`comms-addressing.md` (target space, roster → users/members), `discord.md`
(accounts, attribution), `roster.md`, `personal-sessions.md`,
`standing-sessions.md`, `requisitioner.md` + `serve.md` (connections),
`intercom.md` (ids on the wire), `escalation.md`, `operators.md` (CLI),
config export/import docs.

## Decided in review (2026-10-06)

All four were accepted with the spec; the `-docker` volume is studio-side.


1. **Session vs studio** (§4) — upgrade/restart sets the same session up in a new studio; reset ends it.
2. **Merge heuristic** (§6) — group by strong identity, then by name; rename
   collisions `<name>-<project>`.
3. **Connections admin-managed** (§7) rather than declared in serve YAML.
4. **No admin-plane aliases** (§8) — CLI and server ship together.
