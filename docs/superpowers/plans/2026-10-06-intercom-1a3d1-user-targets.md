# intercom 1a-3d-1: user targets, policy by id

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Test-first throughout.

**Goal (option B, decided 2026-10-06):** stored config and policy name users by id. The squawk log stays name-keyed (`human:<name>` rows) until slice 2's single cutover.

## Design
- **The roster view carries `Human.UserID`.** Authorization and resolution can then match a person by name or by id without new store plumbing.
- **Target syntax:**
  - `user:<name|usr_id>` is the target kind for people.
  - `human:<name>` is still accepted as an alias, for one release.
  - Addressing globs `human:<pat>` are read as `user:<pat>`.
  - A person matches a glob through either form: `user:<name>` or `user:<usr_id>`.
- **Resolution:** `DecideSend` resolves a user target to the member in the authorizing grant's project. The log still records `human:<current name>`, which is option B. `list_targets` and the session context offer `user:<name>`.
- **Personal-session owner by id:**
  - `Instance.OwnerID` and `RaiseSpec.OwnerID` are set at request.
  - The grant override becomes `user:<usr_id>`, and the default `to` becomes `user:<usr_id>`.
  - List and release compare `OwnerID`.
  - The nag and wake-on keep/release paths use `OwnerName` (the current name).
  - The allocator counts per-owner caps by `OwnerID`.
  - `Instance.Owner` stays as a label.
  - Renaming a user who owns a session is now allowed. Removing one is still refused.
- **Escalation tiers:** targets accept `user:<name|id>` (and `human:<name>`). The engine resolves the handle through the roster view, i.e. through accounts.
- **Migration step 3** (`roster_schema` 3), run at Postgres load and on import:
  - Backfill `Instance.OwnerID` from the legacy alias for (project, Owner), else from the name.
  - Rewrite stored addressing: role scopes, grant overrides and escalation tiers. Exact `human:<name>` becomes `user:<usr_id>` when the name resolves to a user in that project (alias, then name), else `user:<name>`. A `human:<glob>` becomes `user:<glob>`.

## Tasks
1. `Human.UserID` in the view; target matching and resolution (decide.go) with tests: name, id, alias and glob forms; ids never widen authz.
2. Owner by id: instance and raise-spec fields, personal sessions, override, default `to`, nag, wake-on, allocator, the rename guard. Tests.
3. Escalation engine: user targets. Tests.
4. Migration step 3. Planner tests, conformance on import, and a Postgres integration test.
5. Surfaces:
   - admin UI (`targetKnown`, suggestions);
   - cove-master tool descriptions and the session-context target list;
   - docs: comms-addressing (target space), escalation, personal-sessions, intercom.
