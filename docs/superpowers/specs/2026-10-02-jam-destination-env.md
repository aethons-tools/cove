# jam: destination-declared client env (the connector contract)

**Status:** design approved (2026-10-02, intercom: "your picks"), pre-plan
**Foundation:** `internal/jam/snippet` (today's hard-coded connector contract), the broker's `Destination` (`internal/jam/policy.go`), role-mapped credentials (`2026-10-02-jam-role-destination-credentials.md`).

## Problem

What a studio must set to use a destination is hard-coded in
`internal/jam/snippet`: Anthropic (`ANTHROPIC_BASE_URL` + `ANTHROPIC_API_KEY`, or
`ANTHROPIC_AUTH_TOKEN` in pool mode) and git (insteadOf + credential helper).
Five client paths call it (cove-master launch, `at-jam enroll`, at-cove
connect, teammate, dispatch). A new destination — e.g. the `gh` pair needing
`GH_HOST` + `GH_ENTERPRISE_TOKEN` — has no way to reach a studio's env.

## Design

**A destination declares the client env it needs; Jam assembles each actor's
connector from the destinations in its scope.**

- `Destination` gains `Env map[string]string` (`env`) and `Git bool` (`git`).
  Env values are templates: `{url}` (broker base + this route, no trailing
  slash), `{base}` (`https://<jam host>`), `{host}` (jam host), `{token}` (the
  identity token). Keys must be env-var names; `AT_JAM_*`/`AT_HARBOR_*` are
  reserved. `Git` routes `https://github.com/` through this destination's
  route with the existing token-free credential helper.
- **Legacy defaults** (no store migration): a destination with no `env`
  gets, if its route is `/anthropic/`, `ANTHROPIC_BASE_URL={url}` plus
  `ANTHROPIC_API_KEY={token}` (identity-in `x-api-key`) or
  `ANTHROPIC_AUTH_TOKEN={token}` (identity-in `bearer` — the pool
  configuration); and a route of `/git/` implies `git`. Existing Jams keep
  exactly today's studio env.
- **Connector** (`snippet.Connector`, stdlib-only so at-cove can use it):
  `{Env, GitRoute}` with `{url}` already resolved to `{base}<route>`. The
  client expands `{base}`/`{host}`/`{token}` in memory; a rendered shell
  snippet exports the token once and references it as `$AT_JAM_IDENTITY_TOKEN`
  — the raw token is never in a template, the store, or a log.
- `jam.ConnectorFor(store, actor)`: union of the destinations in the actor's
  effective scopes. Two destinations setting one variable to different values,
  or two different git routes → error (fail closed).
- **Delivery.**
  - Jam-raised studios: the supervisor computes the connector at raise and the
    launcher hands it to cove-master (no fetch). A connector error rolls the
    raise back.
  - `at-jam enroll`: the admin API returns the connector with the token; the
    CLI renders it.
  - Host-side clients (at-cove connect, teammate, dispatch): `GET /connector`
    on the broker, authenticated by the identity (bearer/`token`). A 404 (an
    older Jam) falls back to today's built-in contract.
- Dispatch keeps its env-only stance (no git routing), as today.

## Non-goals

Admin-UI editing of destination env (CLI/API/import only); per-role env
overrides; non-env client config beyond git.
