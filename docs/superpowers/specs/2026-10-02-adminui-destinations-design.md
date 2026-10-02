# Admin UI destinations page — design

Date: 2026-10-02 · Scope: `internal/jam/adminui` (`/ui/destinations`) + `internal/jam` destination writes.

## Problem

Destinations grew `env` (client-env templates), `git` and `oauth_beta` (#288), plus legacy
defaults implied by the `/anthropic/` and `/git/` routes. The UI's add form predated all of it,
skipped `ValidateEnv`, and — because `AddDestination` upserts — re-adding a name to "edit" it
silently dropped env/git/oauth-beta.

## Design

- **Writes:** `RoleError`/`RoleStatus` generalize to `WriteError`/`WriteStatus`.
  `jam.ValidateDestination` (required fields, configured default credential, `ValidateEnv`) is
  shared by the JSON API and the UI. `CreateDestination` (409 on existing) and
  `UpdateDestination` (404 when missing) under one lock. The JSON `POST` stays an upsert.
- **List:** name (link), route, upstream, `identity-in → apply`, default credential, connector
  chips (effective env keys, git, oauth-beta), roles-using count. The form creates only and
  redirects to the new page.
- **Detail `/ui/destinations/{name}`:** broker facts; studio connector (effective `ClientEnv`,
  legacy defaults labeled as implied, git explicit/implied/off); used-by roles with the
  credential each injects; connector conflicts **flagged, not blocked** (decided in review) —
  per role scope, another destination setting an env key differently or also routing git.
  One pre-filled edit form for every field but the name (env as `KEY=TEMPLATE` lines).
- **Docs:** new `docs/usage/jam/ui-pages.md` leaf holds role + destination pages.
