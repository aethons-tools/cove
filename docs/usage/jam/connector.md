---
summary: The studio connector contract — what a studio sets (env vars, git routing) to use the destinations in its scope; destinations declare it with `--env`/`--git`, and Jam assembles each identity's connector. Includes the `gh` (GitHub API) destinations.
read_when: You are adding a destination a studio needs client-side setup for (env vars like GH_HOST, or git routing), wondering why a studio has (or lacks) some ANTHROPIC_*/GH_* variable, or wiring `gh` through Jam.
owns: destination `env`/`git`, the template placeholders, the legacy defaults, connector assembly + conflicts, and the GitHub-API (`gh`) destination recipe
prereqs: serve.md#destinations for destinations; roster.md for which destinations a role's scope allows
tier: leaf
updated: 2026-10-02
---

# The studio connector (destination env)

A studio reaches Jam's destinations by setting **client env** (and, for git, a
token-free git config). Each destination declares what it needs; Jam assembles
an identity's **connector** from the destinations its grants allow.

## Declaring it on a destination

```
at-jam destination add --name github-api --route /api/v3/ --upstream https://api.github.com \
  --identity-in bearer --cred-name gh-pat --apply bearer \
  --env GH_HOST={host} --env GH_ENTERPRISE_TOKEN={token}
```

- `--env KEY=TEMPLATE` (repeatable; `env:` in `destination import` YAML).
  Placeholders: `{url}` = broker base + this route (no trailing `/`), `{base}` =
  `https://<jam host>`, `{host}` = the Jam host, `{token}` = the studio's
  identity token. Keys must be env-var names; `AT_JAM_*`/`AT_HARBOR_*` are
  reserved. Validated at add time.
- `--git` routes the studio's `https://github.com/` through this destination's
  route (insteadOf + a credential helper that reads the token from env — no
  secret in gitconfig).
- **The token never sits in a template, the store, or a log.** Clients
  substitute it in memory, or render it as `$AT_JAM_IDENTITY_TOKEN` in a
  sourced snippet.

**Legacy defaults** (a destination with no `env`): the `/anthropic/` route sets
`ANTHROPIC_BASE_URL={url}` plus `ANTHROPIC_API_KEY={token}` (identity-in
`x-api-key`) or `ANTHROPIC_AUTH_TOKEN={token}` (identity-in `bearer` — the
[pool](pool.md) configuration); the `/git/` route implies `--git`. Existing
Jams therefore keep exactly their previous studio env. Giving the `/git/`
destination an `env` drops its implied git routing — add `--git` to keep it.

## Assembly and conflicts

An identity's connector is the union over every destination in its grants'
effective scopes ([roster.md](roster.md)). Two destinations setting one
variable to **different** values, or two different git routes, is an error —
fail closed, never a silent pick.

## Delivery

- **Jam-raised studios** get their connector at raise, and cove-master re-fetches
  it before every agent turn, so edits reach a running studio at its next turn
  ([coves.md](coves.md#cove-master-the-in-cove-client)).
- **Host-side clients** (at-cove connect, teammates, dispatch) call
  `GET /connector` on the broker listener with the identity as a bearer (or
  `token`) and receive `{"env": {…}, "git_route": "/git/"}` — templates still
  unexpanded, `{url}` already resolved to `{base}<route>`. Unknown/expired
  identity → 401; a conflict → 409. Against a Jam without the endpoint (404),
  clients fall back to the legacy Anthropic + git contract.

## Notes for sessions

A destination's optional `note` (≤ 300 bytes; `at-jam destination add --note`, or
the UI's edit form) is shown to every session granted it, in the Studio layer of
its [session context](session-context.md), next to the env keys it sets. Write how
to use the route — e.g. "`gh` goes via Jam: pass `-R $GH_HOST/<owner>/<repo>`".
It is not a secret store: never put a credential in it.

## GitHub API for `gh`

`gh` reaches the GitHub API through the broker by treating Jam as a GitHub
Enterprise host: it calls `/api/v3/…` (REST) and `/api/graphql`, sending
`Authorization: token <x>` — accepted as a bearer identity. Two destinations
cover it (the longer `/api/v3/` route wins for REST); the env on one of them
points `gh` at Jam:

```
at-jam destination add --name github-api --route /api/v3/ --upstream https://api.github.com \
  --identity-in bearer --cred-name gh-pat --apply bearer \
  --env GH_HOST={host} --env GH_ENTERPRISE_TOKEN={token}
at-jam destination add --name github-graphql --route /api/ --upstream https://api.github.com \
  --identity-in bearer --cred-name gh-pat --apply bearer
at-jam role add --project acme --name dev \
  --destinations anthropic,git=gh-pat-acme,github-api=gh-pat-acme,github-graphql=gh-pat-acme
```

The PAT needs Contents plus the API permissions `gh` uses (pull requests,
issues, metadata). Only the API is brokered this way — clone/push with plain
`git` (the `/git/` destination), not `gh repo clone`.
