---
summary: How a destination says where a studio's identity token arrives (`identity_in`) and how Jam sets the real credential upstream (`apply`) — the presets (bearer, basic-password, x-api-key, raw) and custom header specs.
read_when: You are adding a destination whose upstream wants the credential in a header the presets don't cover (or bare in Authorization, like Linear), choosing between presets, or debugging a 401 / wrong upstream auth header.
owns: the `identity_in`/`apply` presets, the custom header-spec schema (`identity_in_spec`/`apply_spec`), its validation and storage compatibility
prereqs: serve.md#destinations for the destination verb
tier: leaf
updated: 2026-10-05
---

# Destination header specs (`identity_in` / `apply`)

Every destination has two header specs (header auth only):

- **inbound** (`identity_in`) — where the studio presents its Jam identity
  token: `{header, prefixes, encoding}`. Jam reads the header, cuts the first
  matching prefix (none listed ⇒ the bare value), or with `encoding: basic`
  takes the basic-auth password. This header is always stripped before
  forwarding (and so is `Authorization`), so an identity token never reaches
  the upstream.
- **outbound** (`apply`) — how Jam sets the real credential:
  `{header, template, encoding, basic_user}`. `{cred}` in `template` is
  replaced by the credential; with `encoding: basic` the rendered value is the
  password and the header is `Basic base64(basic_user:value)`.

## Presets

`identity_in` / `apply` take a preset name (what `--identity-in`/`--apply`,
the UI select and every pre-existing destination use):

| Preset | Inbound | Outbound |
|---|---|---|
| `bearer` | `Authorization`, prefixes `Bearer `, `token ` (gh to a GHE host) | `Authorization: Bearer {cred}` |
| `basic-password` | `Authorization`, basic (password; 401 challenges with `WWW-Authenticate: Basic`) | basic, user `x-access-token` |
| `x-api-key` | `X-Api-Key`, bare | `X-Api-Key: {cred}` |
| `raw` | `Authorization`, bare | `Authorization: {cred}` (e.g. Linear GraphQL personal API keys) |

## Custom specs

Set the field to `custom` and give the spec in `identity_in_spec` /
`apply_spec` — via `at-jam destination import` YAML (or the admin API JSON):

```yaml
destinations:
  - name: gitlab
    route: /gitlab/
    upstream: https://gitlab.example.com
    identity_in: custom
    identity_in_spec: {header: Private-Token}
    cred_name: gitlab-pat
    apply: custom
    apply_spec: {header: Private-Token, template: "{cred}"}
```

Validated at write (admin API, UI, `import` of a backup): header names are
valid HTTP tokens; `template` contains `{cred}` exactly once and is
single-line; `encoding` is `raw` (default) or `basic`, and `basic` requires
header `Authorization` (outbound: plus a `basic_user` without `:`, inbound: no
prefixes); a spec is only accepted with `custom`, and `custom` requires one.
Errors never echo a template or prefix, and neither is logged.

The admin UI selects offer the presets; a destination already on `custom`
shows its spec read-only, and saving the form with `custom` kept retains it.
A custom `identity_in` gets no legacy client-env default — see
[connector.md](connector.md).

**Storage.** `identity_in`/`apply` stay the preset strings they always were;
the spec fields are optional and omitted when unset. Stored destinations
(memory/Postgres JSON docs, backups) load unchanged; no migration.
