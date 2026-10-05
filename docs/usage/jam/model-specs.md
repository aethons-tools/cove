---
summary: Model-specs — named, harness-typed descriptions of how a cove runs its agent (harness family + CLI version, principal credential by name, model, permission policy, per-harness body); the YAML schema, its validation rules, and the `at-jam model-spec` verb.
read_when: You are authoring, listing, changing or deleting a model-spec with `at-jam model-spec`, a model-spec write was refused, or you need the model-spec schema or the `/admin/model-specs` API.
owns: the model-spec entity — its schema, validation rules, the `at-jam model-spec` verb and the `/admin/model-specs` admin API
prereqs: serve.md for serve-config `credentials:` and `pool:`; operators.md for `--app`/`--token`
tier: leaf
updated: 2026-10-05
---

# Model-specs

A **model-spec** is a named, stored description of *how a cove runs its agent*:
which harness (and CLI version), which principal it authenticates as, which model,
and which permission policy. Today a model-spec is **stored only** — nothing binds
it to a role or delivers it to a cove yet, so adding one changes no running cove.

## Schema

A common envelope plus exactly one per-harness body, keyed by `type`. `claude` is
the only harness family today.

```yaml
name: claude-default
type: claude                # harness family; implies the harness (required)
version: "2.x"              # required harness CLI version constraint
principal:
  credential: anthropic     # a serve-config credential NAME, or `pool` (required)
model:                      # optional; empty = harness default
  id: claude-opus-5-5
  effort: ""
policy:
  mode: bypassPermissions   # optional; empty = harness default
  allow: []                 # permission rules
  deny: []
note: ""                    # operator hint, ≤ 300 bytes
claude:                     # the body matching `type` (required)
  provider: anthropic       # anthropic | vertex | bedrock (required)
  provider-env: {}          # non-secret env, e.g. Vertex project/region
  settings: {}              # Claude settings.json fragment (preferences only)
  plugins: []
```

## Validation

Every write (`add`, `update`, and config [import](backup.md)) is checked; a refusal
is a 400 naming the field, and nothing is stored.

| Field | Rule |
|-------|------|
| `name` | Required (same rule as destinations). It is the key: `update` cannot rename. |
| `type` | Required; a known harness family (`claude`). |
| `version` | Required, non-blank. |
| `principal.credential` | Required. A [`credentials:`](serve.md#the-serve-config) name (or the pool's `cred-name`), or the keyword `pool` — accepted only when a [`pool:`](pool.md) is configured. |
| `policy.mode` | Empty, or one of Claude's modes: `default`, `acceptEdits`, `plan`, `bypassPermissions`, `dontAsk`. |
| `policy.allow` / `deny` | No empty rules. |
| `note` | ≤ 300 bytes. |
| body | The body matching `type` must be set (`claude:` for `type: claude`). |
| `claude.provider` | Required; `anthropic`, `vertex` or `bedrock`. |
| `claude.provider-env` | Keys are env-var names; not `AT_JAM_*`/`AT_HARBOR_*`; not a protected variable (the proxy vars, `PATH`, `CLAUDE_CONFIG_DIR`, `GOOGLE_APPLICATION_CREDENTIALS` — the same list a kit's `model-provider` env obeys); not a credential-carrying variable (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_BEARER_TOKEN_BEDROCK`). |
| `claude.settings` | A JSON object without the non-preference keys `env`, `permissions`, `apiKeyHelper`, `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`. |
| `claude.plugins` | No empty or duplicate entries. |

**No secrets in a model-spec.** Credentials appear by name only (`principal`);
the real values stay in the [credentials file](credentials.md). Refusals never
echo an env value, and audit logs carry only the operator, name and type.

## The `at-jam model-spec` verb

All subcommands go through the [admin API](serve.md#exposing-the-admin-api-fail-closed)
and take the usual `--admin-url`, `--app` and `--token` flags ([operators.md](operators.md)).

```
at-jam model-spec add <file.yaml>      # create; fails if the name exists
at-jam model-spec update <file.yaml>   # replace every field; fails if absent
at-jam model-spec show <name>          # print as YAML (a valid update input)
at-jam model-spec list                 # name, type, version, principal, provider
at-jam model-spec delete <name>
```

Spec files are decoded strictly: an unknown key (e.g. a typo like `notes:`) is an
error rather than silently dropped. A typical edit is
`show NAME > spec.yaml`, edit, `update spec.yaml`.

## Admin API

| Method + path | Effect |
|---------------|--------|
| `GET /admin/model-specs` | List (sorted by name). |
| `POST /admin/model-specs` | Create — 201; 409 if the name exists; 400 if invalid. |
| `GET /admin/model-specs/{name}` | Show — 404 if absent. |
| `PUT /admin/model-specs/{name}` | Replace — 204; 404 if absent; 400 if the body's name differs. |
| `DELETE /admin/model-specs/{name}` | Delete — 204; 404 if absent. |

Model-specs persist in the Postgres `model_specs` table (one jsonb doc per name)
and are included in [config backups](backup.md).
