---
summary: Model-specs — named, harness-typed descriptions of how a cove runs its agent (harness family + exact CLI version pin + runtime version-constraint, principal credential by name, model, permission policy, per-harness body incl. build-time plugins); the YAML schema and version-constraint syntax, validation, the version-split migration, what a spec builds into the image (harness layer), binding a role to one, the seeded claude-default, how a spec reaches the cove and what the claude harness applies, the `at-jam model-spec` verb and the admin API.
read_when: You are authoring, listing, changing or deleting a model-spec, binding a role to one, a model-spec write or delete was refused, a cove failed its claude version check, you are bumping the harness CLI version or plugins, or you need to know which spec fields a cove actually applies and when an edit takes effect.
owns: the model-spec entity — its schema, the version / version-constraint split and its migration, version-constraint syntax, validation rules, the spec's harness-layer build inputs, role binding and the claude-default seed, delivery to the cove and the claude harness's use of it, the `at-jam model-spec` verb and the `/admin/model-specs` admin API
prereqs: serve.md for serve-config `credentials:` and `pool:`; operators.md for `--app`/`--token`
tier: leaf
updated: 2026-10-05
---

# Model-specs

A **model-spec** is a named, stored description of *how a cove runs its agent*:
which harness (and CLI version), which principal it authenticates as, which model,
and which permission policy. Every **role** resolves to one (unbound = `claude-default`);
Jam delivers the resolved spec to each of the role's coves, which apply it at their
next episode — except its harness CLI `version` and `claude.plugins`, which are
[built into the image](model-spec-harness.md). The permission
policy's argv mapping is in [model-spec-policy.md](model-spec-policy.md). The same
schema and validator load a plain at-cove kit's
[`model-spec:` block](../at-cove-config.md#model-spec) (no `principal` there;
`version` optional).

## Schema

A common envelope plus exactly one per-harness body, keyed by `type`. `claude` is
the only harness family today.

```yaml
name: claude-default
type: claude                # harness family; implies the harness (required)
version: "2.1.287"          # required EXACT harness CLI release the image installs
version-constraint: ""      # optional runtime check (below); empty = exactly `version`
principal:
  credential: anthropic     # a serve-config credential NAME, or `pool` (required)
  headers: []               # broker header rules — model-spec-headers.md
model:                      # optional; empty = harness default
  id: claude-opus-5-5
  effort: ""
policy:
  mode: bypassPermissions   # optional; empty = bypassPermissions (legacy), NOT Claude's `default`
  allow: []                 # permission rules
  deny: []
note: ""                    # operator hint, ≤ 300 bytes
claude:                     # the body matching `type` (required)
  provider: anthropic       # anthropic | vertex | bedrock (required)
  provider-env: {}          # non-secret env, e.g. Vertex project/region
  settings: {}              # Claude settings.json fragment (preferences only)
  plugins: []               # name@marketplace ids, installed at image build
```

## Validation

Every write (`add`, `update`, and config [import](backup.md)) is checked; a refusal
is a 400 naming the field, and nothing is stored.

| Field | Rule |
|-------|------|
| `name` | Required (same rule as destinations). It is the key: `update` cannot rename. |
| `type` | Required; a known harness family (`claude`). |
| `version` | Required; an exact `X.Y.Z` release (digits only — no `v`, suffix or range: a range belongs in `version-constraint`). |
| `version-constraint` | Optional; a valid [version constraint](#version-constraints) that **admits `version`** (else every cove would fail its check). |
| `principal.credential` | Required. A [`credentials:`](serve.md#the-serve-config) name (or the pool's `cred-name`), or the keyword `pool` — accepted only when a [`pool:`](pool.md) is configured. |
| `principal.headers` | Header rules — see [model-spec-headers.md](model-spec-headers.md#validation). |
| `policy.mode` | Empty (= `bypassPermissions`), or one of Claude's modes `default`, `acceptEdits`, `bypassPermissions`, `dontAsk`. `plan` is refused: a cove runs claude headless with nobody to approve a plan, so it could never leave plan mode. |
| `policy.allow` / `deny` | No empty rules, and no leading or trailing whitespace on a rule. |
| `note` | ≤ 300 bytes. |
| body | The body matching `type` must be set (`claude:` for `type: claude`). |
| `claude.provider` | Required; `anthropic`, `vertex` or `bedrock`. |
| `claude.provider-env` | Keys are env-var names; not `AT_JAM_*`/`AT_HARBOR_*`; not a protected variable (the proxy vars, `PATH`, `CLAUDE_CONFIG_DIR`, `GOOGLE_APPLICATION_CREDENTIALS`); not a credential-carrying variable (`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `CLAUDE_CODE_OAUTH_TOKEN`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_BEARER_TOKEN_BEDROCK`). |
| `claude.settings` | A JSON object without the non-preference keys: `env`, `permissions`; the credential helpers `apiKeyHelper`, `awsAuthRefresh`, `awsCredentialExport`, `otelHeadersHelper`; `hooks`, `disableAllHooks`, `statusLine` (they run commands, and hooks can override the policy); the MCP selectors `enableAllProjectMcpServers`, `enabledMcpjsonServers`, `disabledMcpjsonServers`, `allowedMcpServers`, `deniedMcpServers` (the kit owns MCP servers); `enabledPlugins`, `extraKnownMarketplaces` (`claude.plugins` owns them); and the managed sandbox-policy keys `autoUpdates`, `disableRemoteControl`, `remoteControlAtStartup`, `skipDangerousModePermissionPrompt`, `bypassPermissionsModeAccepted`, `disableAutoMode` ([why](model-spec-harness.md#managed-settings-vs-preferences-cov-245)). |
| `claude.plugins` | No empty or duplicate entries; each `name@marketplace`, both halves `[A-Za-z0-9._-]` (they reach a build `RUN` line), and the marketplace a known one (today only `claude-plugins-official` → `anthropics/claude-plugins-official`; adding one is a code change). |

**No secrets in a model-spec.** Credentials appear by name only (`principal`);
the real values stay in the [credentials file](credentials.md). Refusals never
echo an env value, and audit logs carry only the operator, name and type.

## Version constraints

`version` is what the image **installs**; the **runtime constraint** —
`version-constraint`, or exactly `version` when empty — is checked against
`claude --version` in the cove before each spec is applied
([below](#what-a-cove-applies)). Set a looser constraint (e.g. `2.x`) to let a
spec edit's new pin apply to running coves before their image is rebuilt:

| Constraint | Matches |
|------------|---------|
| `X.Y.Z` | exactly that release |
| `X.x` / `X.*` | any `X.*.*` |
| `X.Y.x` / `X.Y.*` | any `X.Y.*` |
| `>=X.Y.Z` (also `>=X`, `>=X.Y`) | that release or later |
| `*` / `x` | any (no check) |

A pre-release/build suffix on the installed version is ignored. Anything else
(`latest`, `~2.1`, a bare `2`) is refused at write.

## Binding a role

A role names its spec with `model_spec` (`at-jam role add --model-spec NAME`, the
role forms in the admin UI — [roster.md](roster.md#roles)). Empty means
`claude-default`. Every role write checks the name exists (400 otherwise), and a
spec a role resolves to — explicitly, or `claude-default` via an unbound role —
cannot be deleted (409 naming the role).

**`claude-default`** is seeded at `at-jam serve` startup when absent (an
operator's edits to it are kept): type `claude`, provider `anthropic`,
`policy.mode: bypassPermissions`, version `modelspec.DefaultClaudeVersion` with
no separate constraint, plugins `[superpowers@claude-plugins-official]`, the claude-default preferences as `claude.settings` (moved out of the managed
settings, [model-spec-harness.md](model-spec-harness.md#managed-settings-vs-preferences-cov-245)),
no model/effort — exactly how coves ran before model-specs.
`DefaultClaudeVersion` is one Renovate-bumped constant: a bump moves **new**
seeds, the harness of every full `config.yml` kit without a `model-spec:`, and raises that deliver no spec —
**not** a `claude-default` already stored. Bump that one yourself
(`at-jam model-spec show claude-default > s.yaml`, edit `version`,
`at-jam model-spec update s.yaml`, or the admin UI). Its principal is `pool` when a
[`pool:`](pool.md) is configured, else the `cred_name` of the destination named
`anthropic` (or else routed at `/anthropic/`). With neither, nothing is seeded
(a WARN, retried each startup) and unbound roles deliver no spec: their coves
keep the built-in defaults, with no version check.

What a spec's `version` and `plugins` build into the image, the image's managed
settings, and the one-time store migration, are in
[model-spec-harness.md](model-spec-harness.md).

## What a cove applies

The resolved spec rides in the role's [connector](connector.md) (`model_spec`,
`GET /connector`), which cove-master re-fetches before every episode
([coves.md](coves.md#cove-master-the-in-cove-client)) — so an edit or a re-binding
takes effect at each cove's **next episode**, and the cove's `connector` status
shows `stale` until then. The spec carries names only, never a secret value.
An actor whose roles resolve to different specs, or a binding to a missing spec,
is a connector conflict (409; a raise fails closed).

Before the first episode, and before any episode whose spec changed, the claude
harness **validates** it and fails the run loud on error: a non-`claude` type,
a `policy.mode` outside the list above (including `plan`), a missing `claude`
binary, or a version outside the constraint
(`model-spec "x" requires claude 3.x, but this image has claude 2.1.287 —
rebuild the image or change the spec's version / version-constraint`). Then each episode applies:

| Field | Applied as |
|-------|------------|
| `model.id` | `--model ID` |
| `model.effort` | `--effort LEVEL` (Claude Code's flag; `low`…`max`) |
| `claude.provider` | `vertex` → `CLAUDE_CODE_USE_VERTEX=1`; `bedrock` → `CLAUDE_CODE_USE_BEDROCK=1`; `anthropic` → nothing |
| `claude.provider-env` | set in the agent env — never over a key the connector sets (routing and identity stay Jam's) |
| `claude.settings` + `claude.plugins` | written to `/dev/shm/cove-agent-settings.json` — the settings, plus `enabledPlugins` for each plugin and `extraKnownMarketplaces` for their marketplaces — passed as `--settings` (only when either is non-empty) |
| `policy` | permission flags — see [model-spec-policy.md](model-spec-policy.md) |

`claude.plugins` are *installed* by the [harness layer](model-spec-harness.md),
not per episode. **Not applied yet:** `principal` on the cove side — the broker
resolves the credential and applies its [header rules](model-spec-headers.md). A Jam predating model-specs delivers none: no check,
built-in defaults.

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

## In the admin UI

The [admin UI](ui.md) lists model-specs at `/ui/model-specs` and creates, edits
and deletes them through the same validation as the verb — see
[ui-pages.md](ui-pages.md#model-spec-pages).

## Admin API

| Method + path | Effect |
|---------------|--------|
| `GET /admin/model-specs` | List (sorted by name). |
| `POST /admin/model-specs` | Create — 201; 409 if the name exists; 400 if invalid. |
| `GET /admin/model-specs/{name}` | Show — 404 if absent. |
| `PUT /admin/model-specs/{name}` | Replace — 204; 404 if absent; 400 if the body's name differs. |
| `DELETE /admin/model-specs/{name}` | Delete — 204; 404 if absent; 409 while a role resolves to it. |

Model-specs persist in the Postgres `model_specs` table (one jsonb doc per name)
and are included in [config backups](backup.md).
