# intercom 1a-3a: registry-backed roster — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the identity registry the source of truth for the people on project rosters. Per-project `Human` records are migrated once into Jam-wide **users**, project **memberships** and service **accounts** (spec §6). The existing human API (`AddHuman`, `RemoveHuman`, `GetRoster().Humans`, `GetProject().Roster.Humans`) is then served as a **view** over the registry, so no consumer changes in this PR.

**Architecture:**

- **View** (`roster_view.go`, deleted in 1a-3e). The `Human` for (project P, user U) is built from:
  - `U.Name`;
  - `U.Logins[0]` (as `Login`);
  - `U.OIDC` (as `Identity`);
  - the handle of U's live account on the `linear` connection (as `Handle`);
  - P's membership delivery addresses, with the uid of U's `discord` account filled into the discord profile.

  `AddHuman` becomes a registry write: upsert the user by name, set their logins/OIDC, set the membership and its delivery, and point the user's `linear`/`discord` accounts at the given handle/uid. `RemoveHuman` ends the membership; the user stays.
- **Implicit connections:** connections named `linear` and `discord` (of those kinds) are created on first need. 1a-4 binds them to serve config.
- **Migration** (`humans_migration.go`):
  - A pure planner turns every project doc's stored `Roster.Humans`, plus the current registry, into a `humanPlan`.
  - Stores apply the plan atomically: memState apply; Postgres runs one transaction under the migration advisory lock and records `jam_settings.roster_schema = 1`.
  - Postgres runs it at load when the marker is absent. Losing a race to another process means a reload.
  - Both stores run it inside `ImportConfig`, so a v1 snapshot's humans migrate on restore.
- **Stored project docs never hold humans afterwards.** `Roster.Humans` is filled only on the way out (`GetProject`, `GetRoster`, `ExportConfig`).

**Spec:** slice 1 spec §3, §5 (legacy aliases), §6 (migration), §7 (implicit connections, back-compat).

## Series 1a-3 (humans → users), each its own PR

1. **1a-3a (this plan):** registry-backed roster. Storage cuts over; the API and wire are unchanged.
2. **1a-3b:** admin plane. `/admin/users`, `/admin/projects/{p}/members`, `/admin/accounts`; CLI `user`, `project member`, `account`; UI pages. `/humans` routes and `add-human` are removed, and `/admin/roster` becomes `/admin/actors`.
3. **1a-3c:** identity at the edges. browserauth/participants by user and membership, personal-session ownership by user id, nag/owner matching.
4. **1a-3d:** the intercom `user:`/`account:` kinds.
   - Ids in new squawks; `human:` accepted as an input alias.
   - Addressing-glob and escalation-tier rewrite; escalation handles via accounts.
   - Ingress attribution by account (Linear `AuthorID`).
   - Unread cursors, and the me UI's dual read through legacy aliases.
5. **1a-3e:** delete the view: `Human`, `Roster.Humans`, `AddHuman`/`RemoveHuman`, `HumanBy*`, `DiscordAuthor`.

## Migration rules (spec §6, made precise)

- **Order:** projects are processed by name, and humans in roster order. (The spec says "project creation order"; projects carry no creation time in the cache, so name order is the deterministic stand-in.)
- **Grouping:**
  1. Union humans that share a **strong identity**: a non-empty login, an OIDC `(issuer, subject)`, or a discord delivery `UserID`.
  2. A human with no strong identity joins a group by **name**, picked in this order:
     - the one strong group containing a human with that name, if there is exactly one;
     - otherwise, if two or more strong groups share the name, a weak-only group for that name;
     - otherwise, the weak group for that name.
  3. An **existing live registry user** seeds the grouping. A group whose strong identity matches an existing user's login or OIDC, or whose name matches an existing user and carries no conflicting strong identity, becomes that user.
- **Naming:** a group's name is its first human's name.
  - An invalid entity name is sanitized: each forbidden character becomes `-`, cut to 64; if the result is empty or parses as an id, it becomes `user`.
  - A name already taken by another user or group becomes `<name>-<project>` (the group's first project), then `<name>-<project>-2`, and so on.
  - Every sanitize and rename is reported.
- **User fields:** the union of the group's logins and of its OIDC bindings.
- **Memberships:** one per (project, group), with the human's `Delivery[]` addresses (Service, Address), excluding empty addresses.
- **Accounts:**
  - `Handle` → an account on `linear` (`handle` set), linked to the user.
  - A discord `UserID` → an account on `discord` (`service_uid` set), linked to the user.
  - A handle already linked to a *different* user is left with its first user and reported.
- **Legacy aliases:** one `(project name, human name) → user id` per human. Frozen, read-only, and served by `LegacyHumanAlias(project, name)`.
- **Renames rewrite exact references within that project:**
  - `human:<old>` → `human:<new>` in the project's role `Scope.Addressing`, actor grant `Overrides.Addressing` for that project, and escalation tiers;
  - `Instance.Owner` `<old>` → `<new>` for that project's instances.

  Glob patterns are not rewritten; any glob that matched the old name is reported.
- **Report:** `HumanMigration{Users, Memberships, Accounts int; Notes []string}`, which is logged. Notes carry names and ids only.

## Global Constraints

- No consumer outside `internal/jam` store files changes behaviour. Every existing test passes, except where a test relied on two *different* same-named humans in two projects (they are now one user); such a test is updated, with the reason in the commit.
- Postgres and MemStore stay conformant. The planner is shared, pure and hermetically tested.
- Fail closed: if a Postgres load can't migrate, `NewPostgresStore` returns an error.
- Secrets never enter the registry: implicit connections carry no `CredName` (1a-4 sets it).

## Review Focus

1. **The view must round-trip every field `AddHuman` accepts** (Name, Handle, Login, Delivery with discord UserID, Identity). Pinned by the existing `roster_and_escalation` conformance test, which is unchanged.
2. **The migration is atomic and runs exactly once.** Postgres runs it under the advisory lock and re-checks the marker inside the transaction; losing the race reloads. Pinned by `TestPostgresHumansMigration` (integration) and planner tests.
3. **Stored docs never re-acquire humans.** `putProject` must persist docs with `Roster.Humans` empty, even though `GetProject` returns them. Pinned by `TestPostgresHumansMigration` (reload shows the same view, and docs have no humans).
4. **Grouping edge cases:** conflicting strong identities with one name; a weak human joining the unique strong group; logins shared across projects. Pinned by `TestPlanHumanMigration*`.
5. **The view's `Login` is single while a user may hold several logins.** `AddHuman` replaces the user's logins with `[h.Login]` (or none). `HumanByLogin` is reimplemented on `UserByLogin` + `IsMember`, so every login of a migrated multi-login user still works.

## Tasks

1. **Membership delivery + legacy aliases + marker (memState, MemStore).**
   - `Membership.Delivery []DeliveryProfile`; `PutMembership(m)`, an upsert that replaces delivery; `GetMembership(project, user)`.
   - `LegacyHumanAlias(project, name) (ident.ID, bool)`.
   - Conformance tests first.
2. **The planner** (`humans_migration.go`): `planHumanMigration(projects []Project, reg *memState) humanPlan`, with table-driven unit tests covering:
   - single human;
   - same login across projects → one user;
   - same name, no ids → one user;
   - same name, conflicting logins → two users, the second renamed `<name>-<project>`;
   - weak human + unique strong group → joined;
   - weak human + two strong groups → separate;
   - invalid-name sanitize;
   - handle conflict reported;
   - existing registry user reused.
3. **Apply + view in MemStore.**
   - `applyHumanPlan`; the view on `GetProject`/`GetRoster`/`ExportConfig`.
   - `AddHuman`/`RemoveHuman` through the registry.
   - `ImportConfig` migrates the snapshot's humans.
   - `HumanByLogin` via the registry.
   - `PutRosterHuman` maps registry errors to 400.
   - Implicit connections.
   - All existing tests pass.
4. **Postgres.**
   - Migration 0008 (`memberships.delivery`, `legacy_human_aliases`).
   - `applyHumanPlanTx`; marker load; migrate-at-load under the advisory lock; `ImportConfig` writes the plan in its transaction; `AddHuman`/`RemoveHuman` registry writes.
   - `TestPostgresHumansMigration`: seed legacy docs via the pool, open, assert view + registry + aliases + cleared docs, then reopen and assert idempotence.
5. **Docs.**
   - `roster.md`: humans are stored as users and memberships; user names are Jam-wide.
   - `projects.md`, `backup.md` (a v1 restore migrates humans), `comms-addressing.md` (names are Jam-wide).
   - Docs audit.

## Final verification

- [ ] `go build ./... && go vet ./... && go test ./...` and `scripts/lint.sh` pass.
- [ ] `store-integration` is green on the PR.
