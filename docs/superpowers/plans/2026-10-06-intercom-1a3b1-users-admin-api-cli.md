# intercom 1a-3b-1: users, members and accounts — admin API + CLI

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans. Test-first throughout.

**Goal:** Give operators the registry directly: users, project members, accounts and (read-only) connections. This replaces the per-project `humans` admin routes and the `project roster add-human|rm-human` verbs, with no aliases (spec §8: the admin plane ships with the server). The admin UI moves in 1a-3b-2. It writes through the store, not the API, so it keeps working in between.

**Spec:** slice 1 spec §8. Series context: `2026-10-06-intercom-1a3a-registry-backed-roster.md`.

## API (`internal/jam/users_admin.go`)

A `{user}` path parameter takes a name or a `usr_` id; `{account}` is an `acc_` id; `{connection}` is a name or a `con_` id.

| Route | Body → result |
|---|---|
| `GET /admin/users` | → `[]UserView` (live users, by name) |
| `POST /admin/users` | `UserBody{name, logins, oidc}` → 201 `UserView` |
| `GET /admin/users/{user}` | → `UserView` (an id also finds a removed user) |
| `PUT /admin/users/{user}/name` | `RenameBody{name}` → 204 |
| `PUT /admin/users/{user}/logins` | `LoginsBody{logins}` → 204 (replaces the set) |
| `PUT /admin/users/{user}/oidc` | `OIDCBody{oidc}` → 204 (replaces the set) |
| `DELETE /admin/users/{user}` | → 204 (tombstone). 409 while the user owns a live personal session |
| `GET /admin/projects/{project}/members` | → `[]MemberView{user_id, user, delivery}` |
| `PUT /admin/projects/{project}/members/{user}` | `MemberBody{delivery}` → 204 (add, or replace delivery) |
| `DELETE /admin/projects/{project}/members/{user}` | → 204 |
| `GET /admin/connections` | → `[]Connection` |
| `GET /admin/accounts?connection=` | → `[]AccountView` (one connection, or all) |
| `POST /admin/accounts` | `AccountBody{connection, service_uid, handle, label, user}` → 201 `AccountView` (upsert, plus an optional link) |
| `PUT /admin/accounts/{account}/user` | `LinkBody{user}` → 204 |
| `DELETE /admin/accounts/{account}/user` | → 204 (unlink) |
| `GET /admin/actors` | was `GET /admin/roster` |

**Removed:** `POST /admin/projects/{p}/humans`, `DELETE /admin/projects/{p}/humans/{name}` and `GET /admin/roster`. `GET /admin/projects/{p}/roster` stays read-only (channels, plus the humans view) until slice 2.

**Status mapping** (`registryErrStatus`):

| Error | Status |
|---|---|
| any not-found (user, connection, account, membership, project) | 404 |
| `ErrNameTaken`, `ErrRemoved`, `ErrConnectionInUse`, `ErrProjectInUse` | 409 |
| `ErrInvalidName`, `ErrLoginTaken`, `ErrIdentityTaken`, `ErrAccountLinked` | 400 |
| everything else | 400 |

## CLI (`cmd/at-jam/users.go`)

```
at-jam user add <name> [--login L]... [--oidc issuer:subject]...
at-jam user list | show <user> | rename <user> <new> | rm <user>
at-jam user login <user> [L...]        # replaces the set; none clears
at-jam user oidc <user> [issuer:subject...]
at-jam project member add <project> <user> [--delivery service:address]...
at-jam project member list <project> | rm <project> <user>
at-jam account list [--connection c]
at-jam account add --connection c (--uid u | --handle h) [--label l] [--user u]
at-jam account link <account> <user> | unlink <account>
at-jam connection list
at-jam actors                           # was: at-jam roster
```

`project roster` keeps `add-channel|list|rm-channel`. `list` prints channels, plus members as `member\t<user>\t…`.

## Tasks

1. API + handler tests (`users_admin_test.go`): every route, name-or-id resolution, the status mapping, and the personal-session refusal.
2. adminclient methods + tests; `Roster` → `Actors`; drop `AddHuman`/`RemoveHuman`.
3. CLI verbs + tests; remove `add-human`/`rm-human`; `roster` → `actors`.
4. Docs:
   - `roster.md` (users/members/accounts, the CLI)
   - `comms-addressing.md` (roster section → users + members)
   - `discord.md` (binding a Discord id = `account add --connection discord --uid … --user …`)
   - `operators.md` (`actors`)
   - `personal-sessions.md` (the owner is a member user with a login)
   - `INDEX.md`, if a verb list lives there

   Then the docs audit.
