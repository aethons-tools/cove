---
kind: design-spec
subject: at-jam — a central, protected credentials-supply file, so the serve config references credentials by name and secret strategies live out of source control
status: draft
date: 2026-09-30
read-when: implementing, slicing, or reviewing the move of at-jam's credential resolution strategies out of the serve config into a protected supply file (the at-cove demand/supply model ported to the service)
---

# at-jam credentials-supply file

## One paragraph

Today `at-jam serve` carries each downstream credential's **resolution strategy inline** in
its serve config: `credentials.<name>` holds a `{command: […]}` or `{value: "…"}`, and the
Discord bot-token is an inline `{command/value}` under `runtime.discord`. The serve config
(e.g. `dev/jam.dev.yml`) therefore physically contains secret material — or the argv that
mints it — right next to source control, the exact leak surface at-cove already solved for
kits. This design **ports at-cove's demand/supply split to the service**: the serve config
only *names* the credentials it needs (a demand list), and a separate **protected file**
(`~/.config/at-jam/credentials.yml`, never committed) supplies each name's strategy —
`value`, `command`, `global`, or `mint`. The broker downstream is unchanged: it still
receives a `map[string]secret.Spec` and resolves lazily, in memory.

## Drivers

1. **Close the leak** — no secret value, and no minting argv, ever sits in the serve config
   (and thus never near a committed file). This is the whole point.
2. **One model across the product** — the same demand/supply split, `Source` vocabulary
   (`value`/`command`/`global`/`mint`), and `minters:` library that at-cove kits already use
   (`~/.config/at-cove/secrets.yml`), so operators learn it once.
3. **No behavior change downstream** — the credential broker, lazy in-memory resolution, the
   subscription pool, and the Postgres-password path keep working exactly as they do now.

## Non-goals

- **No `.local` escape-hatch file** for v1 (at-cove's `secrets.local.yml`). One file. YAGNI —
  add later if a per-host override need appears.
- **No change to the pool** (`pool:` still resolves from the account pool by identity, not
  from `credentials:`) beyond its `cred-name` continuing to reference a demanded credential.
- **No change to the broker / destination model.** Destinations still name a `cred-name`; the
  only difference is where that credential's *strategy* comes from.

## Background: the two models today

**at-cove kit (the good model).** A kit's `config.yml` *demands* secrets by name only — a
`command:` there is a hard parse error. The machine *supplies* them out of source control in
`~/.config/at-cove/secrets.yml` (+ `secrets.local.yml`), whose `minters:`/`global:`/`kits:`
sections resolve each demand through one of four sources. This is implemented in
`internal/usersecret` (`Source`, `Minter`, `Store.Plan(kit, path, demanded, expand) →
[]secret.Spec`). See `docs/usage/at-cove-secrets.md`.

**at-jam serve-config (the weakness).** `cmd/at-jam/config.go` parses
`Credentials map[string]credSpec`, where `credSpec` is `{command, value}` — the strategy is
inline. `serveConfig.credSpecs()` turns that into `map[string]secret.Spec`, and
`cmd/at-jam/main.go` feeds it to `jam.NewSecretResolver(runner.OS{}, specs)`. The
Postgres-password (`store-postgres.password-cred`) already references a name in that map; the
Discord bot-token (`runtime.discord.bot-token`) is inline.

## The protected file — `~/.config/at-jam/credentials.yml`

Reuses `usersecret`'s vocabulary, **flattened** (no `kits:` — Jam is a single service):

```yaml
# ~/.config/at-jam/credentials.yml   (mode 0600, honors XDG_CONFIG_HOME, never committed)
minters:                       # inert library; reached only via a mint: reference
  anthropic-fed:
    anthropic:
      audience: "https://jam.example.com/anthropic"
      # …the rest of an at-mint anthropic profile
global:                        # optional inert library; reached only via a global: reference
  gh-token: { command: ["gh", "auth", "token"] }
credentials:                   # name -> source: exactly one of value | command | global | mint
  anthropic-key: { mint: anthropic-fed }
  git-pat:       { global: gh-token }
  jam-db:        { value: "dev-only-password" }
  discord-bot:   { command: ["cat", "/run/secrets/discord"] }
```

- Each `credentials:` entry sets **exactly one** source, validated on load (the existing
  `Source.Kind()` check).
- `minters:` and `global:` are **inert libraries** — reached only through an explicit
  `mint:`/`global:` reference. This is the anti-mining invariant, ported: an entry supplies
  nothing until something demands it.
- Missing file ⇒ an empty store ⇒ every demand fails closed at startup (see resolution).

## Serve-config changes

```yaml
credentials-file: /home/jam/.config/at-jam/credentials.yml   # NEW, optional (see default)
credentials:                    # name-only DEMANDS — an inline strategy is a parse error
  anthropic-key:
  git-pat:
  jam-db:
  discord-bot:
store-postgres:
  password-cred: jam-db         # unchanged; resolves against the file-supplied specs
runtime:
  discord:
    bot-token-cred: discord-bot # NEW name; replaces inline `bot-token: {command/value}`
```

1. **`credentials-file: <path>`** — new optional field. When omitted, defaults to
   `${XDG_CONFIG_HOME:-~/.config}/at-jam/credentials.yml`.
2. **`credentials:` is now a name-only demand list.** A `command:` or `value:` under an entry
   is a **hard parse error** naming the offending credential and pointing at the credentials
   file. Each entry's value must be empty/null.
3. **`runtime.discord.bot-token` → `runtime.discord.bot-token-cred: <name>`**, referencing a
   demanded credential — the same pattern as `store-postgres.password-cred`. An inline
   `bot-token:` is a parse error with a migration message.
4. **`store-postgres.password-cred`** — unchanged shape; now resolves against the
   file-supplied specs like any other reference.

## Resolution flow (`at-jam serve` startup)

The broker and everything downstream of `map[string]secret.Spec` are untouched.

1. Parse the serve config. The **demanded set** is the key set of `credentials:`.
2. Determine the credentials-file path (the `credentials-file:` field, else the XDG default)
   and load it via `usersecret.LoadFlat(path)`. A missing file yields an empty `Store`.
3. `store.PlanFlat(demanded, mintExpander) → ([]secret.Spec, unresolved []string, err)`,
   where `mintExpander` is the same `internal/mint` expander at-cove uses (a `mint:` source
   becomes an `at-mint` invocation `secret.Spec`). `value`/`command`/`global` map to specs as
   in `usersecret.resolve` today.
4. **Fail closed.** Any demanded name in `unresolved` ⇒ `serve` aborts at startup, naming the
   credential and the file. (This is stricter than today, where a missing inline value simply
   resolved to empty.)
5. Build the `map[string]secret.Spec` and pass it to
   `jam.NewSecretResolver(runner.OS{}, specs)` exactly as `main.go` does now. The
   Postgres-password path (`secret.Resolve(specs[pc.PasswordCred])`), the Discord bot-token,
   the destinations, and the pool `cred-name` all key into that map by name, unchanged.

## Validation & invariants

- **Reference integrity.** Every credential reference — `password-cred`, `bot-token-cred`, a
  destination's `cred-name` (validated at add time), and the pool `cred-name` — must name a
  **demanded** credential. This extends the existing `credConfigured` check to draw the
  allowed set from the demand list.
- **Anti-mining, ported.** File `credentials:`/`global:`/`minters:` entries are inert until
  named as a demand; an undemanded file entry supplies nothing.
- **Fail-closed on missing supply.** A demanded credential with no matching file entry aborts
  `serve` before the broker starts.
- **Secrets never logged.** Preserved by construction — resolved values and `secret.Spec`s
  are never written to the log sink at any level; only names appear in errors.
- **Inline strategy is unrepresentable in the serve config.** The parse step rejects it, so
  the leak surface is closed structurally, not by convention.

## Package changes (Approach A — extend `usersecret`)

`internal/usersecret` already owns the generic `value`/`command`/`global`/`mint` vocabulary,
`Minter`, `Source` validation, and mint expansion. We give it a **flat front** for a
service-style consumer, keeping that logic in exactly one place:

- Add `Credentials map[string]Source` to the on-disk `file` shape and to `Store`.
- Add `PlanFlat(demanded []string, expand MintExpander) ([]secret.Spec, []string, error)`,
  reusing the existing unexported `resolve`. Same precedence-free, validate-then-resolve shape
  as `Plan`, minus the kit/path lookup.
- Add `LoadFlat(path string) (Store, error)` — a single-file loader (no `.local`), validating
  every source and that every `global:`/`mint:` reference resolves.
- Update the package doc comment to state it serves **both** the kit-partitioned supply
  (at-cove, `kits:`) and the flat service supply (at-jam, `credentials:`).

`cmd/at-jam/config.go`:

- Replace `Credentials map[string]credSpec` with a name-only demand parse; a present
  `command:`/`value:` is a parse error.
- Add `CredentialsFile string` (`yaml:"credentials-file"`) with the XDG default resolved when
  empty.
- Add `runtime.discord.bot-token-cred`; make inline `bot-token` a parse error.
- Remove the inline strategy fields from the credential path; `credSpecs()` is replaced by the
  `usersecret.PlanFlat` call in the `serve` wiring.

`cmd/at-jam/main.go`: in the `serve` path, load the credentials file, `PlanFlat` the demand
set, fail closed on unresolved, and hand the resulting specs to `jam.NewSecretResolver` (and
the Postgres-password `secret.Resolve`) exactly where `credSpecs()` is used today.

## Testing (hermetic, TDD — write the failing test first)

- **`usersecret.PlanFlat`** — table tests over `value`/`command`/`global`/`mint`, an
  unresolved demand, a `mint:` with its expander, and validation errors (undefined
  `global:`/`mint:`, malformed `Source`). Driven by `runner.Fake`; no VM/network.
- **`LoadFlat`** — missing file ⇒ empty store; malformed YAML ⇒ error; a `global:`/`mint:`
  reference to an undefined library entry ⇒ error.
- **serve-config parsing** — inline `command:`/`value:` under `credentials:` ⇒ error; inline
  `runtime.discord.bot-token` ⇒ error; name-only `credentials:` + `bot-token-cred` parse; the
  `credentials-file` default path resolves under a temp `XDG_CONFIG_HOME`.
- **serve wiring** — a demanded-but-unsupplied credential ⇒ fail closed before the broker
  starts; a `password-cred`/`bot-token-cred`/destination `cred-name` naming an undemanded
  credential ⇒ error; given equivalent inputs, the broker receives an **identical**
  `map[string]secret.Spec` (parity with the pre-change path).

## Docs

- **New leaf** `docs/usage/jam/credentials.md` — owns the credentials-file format and the
  demand/supply model for the service; cross-linked to `docs/usage/at-cove-secrets.md` as its
  sibling (the shared `Source`/`minters:` vocabulary lives there; this doc does not re-explain
  it, it points).
- **`docs/usage/jam/serve.md`** — rewrite the credentials section: `credentials-file`,
  name-only `credentials:` demands, `bot-token-cred`, and move the "never inline" rule from
  just the DB password to **all** credentials. Link to `credentials.md` for the file.
- **`docs/usage/jam/INDEX.md`** — add the `credentials.md` row.
- **`docs/usage/jam/renamed-from-harbor.md`** — if the inline→`bot-token-cred` change warrants
  a deprecation note, record it there.

## Migration

- A serve config with an inline `credentials:` strategy, or an inline `runtime.discord.bot-token`,
  fails to parse with a message naming the field and pointing at the credentials file.
- Migrate the operator's `dev/jam.dev.yml` and the sample serve config: strategies move to a
  new `~/.config/at-jam/credentials.yml`; the serve config keeps only names + `bot-token-cred`.
  Low blast radius — the dev serve config is locally staged and never committed.
