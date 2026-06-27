# cove — user-level secrets file (`~/.config/at-cove/secrets.yml`) — Design

**Date:** 2026-06-27
**Status:** Approved (pre-implementation)
**Repo:** `aethons-tools/cove` (binary `at-cove`)
**Relates to:** `2026-06-26-cove-sandboxes-design.md` (§3.5 secrets, §3.7 the deferred `.local` layer)

## 1. Purpose

Let a user supply the values (or resolver commands) for a kit's secrets from a single user-owned file,
`~/.config/at-cove/secrets.yml`,
instead of requiring every secret's `command` to live in the kit's committed `config.yml`.

This separates two concerns that are currently fused:

- **Demand** — *which* secrets a sandbox needs.
  Owned by the kit (`config.yml` → `state.json`).
- **Supply** — *how* each secret's value is produced.
  May now come from the user's own `secrets.yml`.

It is the global-scope analog of the deferred `.local` mechanism (§3.7 of the sandboxes design):
a place for resolver commands and values that the user controls on their own machine,
never committed to a repo.

**The kit schema does not change.**
No new keys are added to `config.yml`.
The only relaxation is that a secret's `command` becomes optional (see §3).

## 2. The model: demand vs. supply

- The kit's secret list is the **authoritative demand**:
  the set of secret names the sandbox will have injected at `connect`.
- `secrets.yml` is the **supply**:
  a map from secret name to a value or a command.
- **Supply is consulted only for demanded names.**
  A name present in `secrets.yml` but not demanded by the kit is inert —
  it is never read or injected.
  Dropping an unfamiliar kit into a directory cannot cause it to pull arbitrary entries from your `secrets.yml`;
  it can only request the names it explicitly declares.

### 2.1 Resolution precedence (per demanded secret)

1. If the secret has a **command** (from `config.yml`, snapshotted into state), run it.
   The kit-provided command always wins.
2. Otherwise consult `secrets.yml` by the secret's name:
   - a **string** value → inject it literally (no command is run);
   - an **array of strings** → run it as the resolver command (argv), exactly like a `config.yml` command.
3. Otherwise the secret is **unresolved**:
   emit a warning and leave it unset (§6).

## 3. Kit schema relaxation

`config.yml`'s secret `command` becomes **optional**;
`name` remains required.
The YAML shape is unchanged — no keys added or renamed.

```yaml
secrets:
  - name: GITHUB_TOKEN            # demand only; supplied by secrets.yml
  - name: ANTHROPIC_API_KEY
    description: subscription key  # description still allowed
  - name: SOME_TOKEN
    command: ["op", "read", "op://Personal/x/token"]  # kit-supplied (still wins)
```

A secret declared with just a `name` is a demand for that secret to be supplied elsewhere.
`internal/kit.ParseConfig` drops its current "command is required" check (and the corresponding rejection test);
a new test asserts a name-only secret parses.

This aligns with the sandboxes design's stated direction (§3.5):
a committed `config.yml` should be able to carry `name`/`description` without a `command`.

## 4. The `secrets.yml` schema

`~/.config/at-cove/secrets.yml` (under `configDir()`, honoring `XDG_CONFIG_HOME`) is a flat map of secret name → value-or-command:

```yaml
# string  -> literal value, injected as-is
GITHUB_TOKEN: ghp_xxxxxxxxxxxxxxxxxxxx

# array of strings -> resolver command (argv), run on the host at connect
ANTHROPIC_API_KEY: ["pass", "show", "anthropic/api-key"]

OP_TOKEN: ["op", "read", "op://Personal/x/token"]
```

Rules:

- A **scalar** value is taken as its string form
  (a value that looks numeric, e.g. a PIN, is treated as a string — no quoting required).
- An **array** value must be a sequence of strings;
  it is the resolver argv.
- Any other shape (a mapping, a nested structure, a non-string array element) is a **malformed entry** and is an error (§6),
  named by its key.
- The file is optional.
  A **missing file is equivalent to an empty map** and is not an error.

## 5. Architecture

A new leaf-ish package owns the file and the precedence;
`internal/secret` gains a literal-value path;
`main.go` wires them at `connect`.
No backend, transport, or SSH changes.

```
internal/usersecret/      NEW: parse secrets.yml; hold the Store; own precedence
internal/secret/          extend Spec with a literal value; Resolve honors it
main.go (doConnect)       load store, plan demand×supply, warn, pass to connect
```

Dependency direction:
`usersecret` imports `secret`;
`secret` stays a leaf (imports only `runner`);
`main` imports both.
No cycles.

### 5.1 `internal/usersecret`

```go
// Entry is one supply: exactly one of Value or Command is set.
type Entry struct {
    Value   string   // literal value (the YAML scalar form)
    Command []string // resolver argv (the YAML string sequence)
}

type Store map[string]Entry

// Load reads secrets.yml. A missing file yields an empty Store and no error.
// A present-but-malformed file (bad YAML, or a value that is neither a scalar
// nor a string sequence) is an error naming the offending key.
func Load(path string) (Store, error)

// Plan resolves each demanded secret to a runnable/literal Spec, applying the
// precedence in §2.1. It returns the resolvable specs (in demand order) and the
// names of any demanded secrets it could not resolve. Store entries whose names
// are not demanded are ignored.
func (s Store) Plan(demanded []secret.Spec) (resolvable []secret.Spec, unresolved []string)
```

`Plan` is pure (no I/O) and is the primary unit under test for precedence.

Parsing detail:
decode into `map[string]yaml.Node` and inspect each value node's `Kind` —
`ScalarNode` → `Entry{Value: node.Value}`;
`SequenceNode` → decode to `[]string` for `Entry{Command}`;
anything else → error.

### 5.2 `internal/secret`

```go
type Spec struct {
    Name    string
    Command []string // run to produce the value...
    Value   string   // ...unless Literal, in which case Value is injected as-is
    Literal bool
}
```

`Resolve` injects `Value` directly when `Literal` is set;
otherwise it runs `Command` as today
(trimming the trailing newline, failing closed on a nonzero exit).
The empty-command guard remains as defensive depth,
though `Plan` never emits a non-literal spec with an empty command.

### 5.3 `main.go` (`doConnect`)

1. Build `demanded []secret.Spec` from `state.Secrets` (`Name` + `Command`).
2. `store, err := usersecret.Load(filepath.Join(configDir(), "secrets.yml"))` —
   **abort on error** (a malformed file fails the connect, including under `--dry-run`).
3. `specs, unresolved := store.Plan(demanded)`.
4. For each name in `unresolved`, print to **stderr**:
   `at-cove: warning: secret "NAME" is demanded by the kit but has no command and no entry in <path>; it will not be set`.
5. `--dry-run`: print `would resolve <len(specs)> secrets ...` (the resolvable count) and return —
   after the warnings above.
6. Otherwise pass `specs` as `connect.Options.Secrets` and proceed unchanged.

`doConnect` gains a `stderr io.Writer` parameter
(its `run()` call site already has `stderr`).

## 6. Error handling

- **Malformed `secrets.yml`** (bad YAML, or a value that is neither a scalar nor a string sequence) →
  abort with a clear error naming the key;
  non-zero exit, even under `--dry-run`.
- **Missing `secrets.yml`** → empty store; no error.
- **Unresolved demanded secret** (no command, no supply) →
  non-fatal stderr warning;
  `connect` proceeds with that variable unset.
- **Resolver command failure** (from either source) →
  fail closed, aborting `connect` before SSH, exactly as today.

## 7. Security

- `secrets.yml` lives in the user's own `~/.config` and is never sourced from a repo,
  so its **commands are user-authored and trusted** —
  the safe analog of the deferred `.local` resolver-command rule, at global scope.
- **Literal string values are plaintext on disk.**
  This is a deliberate convenience that departs from the otherwise-strict "resolver-produced values never persist" posture.
  Recommendation: keep the file `chmod 600`.
  Enforcing or warning on loose permissions is a possible future enhancement, deferred (YAGNI).
- Because only **demanded** names are read,
  a kit's `config.yml` chooses which of your stored secrets may be injected into its sandbox.
  The existing assumption — only `connect` to kits you trust — carries over unchanged.
  The blast radius remains bounded by the sandbox egress lock.

## 8. Testing (TDD, all hermetic)

- **kit:** a name-only secret parses;
  remove the "command is required" rejection case.
- **usersecret.Load:** string → value;
  array → command;
  mixed file;
  missing file → empty store;
  malformed (scalar-keyed mapping value, non-string sequence element, invalid YAML) → error naming the key.
- **Store.Plan:** kit command wins over supply;
  string supply → literal;
  array supply → command;
  missing supply → unresolved;
  entries not demanded are ignored;
  output preserves demand order.
- **secret.Resolve:** a literal spec injects its value and runs no command;
  existing command / fail-closed tests stay green.
- **main `doConnect` (`--dry-run`):** an unresolved demanded secret emits the stderr warning
  and the resolvable count reflects only resolved secrets;
  a malformed `secrets.yml` aborts.

No transport/integration changes:
downstream of the resolved env map,
a literal value and a command-derived value are identical,
so the existing `SendEnv` / stdin-tmpfs transports and their integration tests cover injection unchanged.

## 9. Out of scope (YAGNI)

- Permission checks / warnings on a loosely-permissioned `secrets.yml`.
- Per-kit `secrets.yml` (this is user-global only);
  the `.local` layer remains the deferred per-kit mechanism.
- Any change to `create` (it stays secret-free) or to the backends/transports.
- Migrating existing committed `config.yml` commands out of the kit
  (allowed to remain; they simply win over supply).

## 10. Documentation impact

- `docs/OVERVIEW.md` — extend the secrets section:
  demand vs. supply, the `secrets.yml` format, precedence, the unresolved warning, and the plaintext-on-disk note.
