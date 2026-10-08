# jam: role-mapped destination credentials (drop repo scoping)

**Status:** design approved (2026-10-02, intercom with the operator), pre-plan
**Foundation:** the Jam credential broker — `internal/jam/{policy,decide,proxy,identity}.go`; roles/grants per [`docs/usage/jam/roster.md`](../../usage/jam/roster.md), destinations per [`docs/usage/jam/serve.md`](../../usage/jam/serve.md).

## Problem

A destination carries exactly one credential (`cred_name`), shared by every
project and role on the Jam. The only per-role narrowing is `Scope.Repos`
(owner/repo globs) on `repo_scoped` destinations. That causes trip-ups:

- public third-party repos (e.g. `chromedp/chromedp`) 403 through `/git/`
  even though any PAT can read them;
- API-style upstreams (`api.github.com` for `gh`) can't be repo-scoped at all —
  REST paths are `/repos/<o>/<r>/…` and GraphQL has no repo in the path;
- reach is really decided by the token's own scope, which the broker duplicates
  less precisely.

## Design

**The role maps each destination it allows to the credential the broker
injects.** Repo scoping is removed; a credential's reach is the credential's
own scope (e.g. a fine-grained PAT per project).

- `Scope` gains `Credentials map[string]string` (`json:"credentials,omitempty"`):
  destination name → credential name. Additive: a destination listed in
  `Scope.Destinations` with no entry (or an empty one) falls back to the
  destination's own `CredName` — so existing roles keep working unchanged.
- `Override` gains `Credentials map[string]string`; non-nil **replaces** the
  role's map (same semantics as every other override field).
- `Decide` picks the credential from the scope that authorized the request.
  When two of an actor's grants both authorize the destination but resolve to
  **different** credentials, the request is **denied** (fail closed — never a
  silent pick).
- Validation at write time (role put, grant add, enroll): every `Credentials`
  key must be one of the effective scope's destinations, and every non-empty
  value must name a configured credential (`credExists`). A typo fails at the
  admin API, not at request time.
- Removed: `Scope.Repos`, `Override.Repos`, `Destination.RepoScoped`,
  `RepoFromPath`, `repoAllowed`, and their API/CLI/UI fields (`repos`,
  `--repos`, `repo_scoped`, `--repo-scoped`). Stored JSON with those keys still
  loads (unknown keys are ignored).
- Surface syntax (CLI `--destinations`, UI destinations field):
  `git=git-pat-cove,anthropic` — `name=cred` maps, a bare `name` uses the
  destination default. The admin API carries the map as `credentials`.
- `gh` support: the broker's bearer identity also accepts
  `Authorization: token <x>` (what `gh` sends to a GHES host), so a studio can
  run `gh` with `GH_HOST=<jam host>` against two plain destinations
  (`/api/v3/` and `/api/` → `https://api.github.com`).

## Upgrade note (security)

Removing repo scoping **widens** any role that relied on `repos` globs to
narrow a broad PAT. Before upgrading, the operator must make each git
credential's own scope the boundary — a fine-grained PAT per project, mapped
per role.

## Non-goals

Per-request repo policy of any kind; credential *values* in roles (roles hold
names only; values stay in the host-side credentials file).
