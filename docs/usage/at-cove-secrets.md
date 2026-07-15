---
summary: The demand/supply secret model — kits declare secrets by name only (config.yml `secrets`), the machine supplies values out of source control (~/.config/at-cove/secrets.yml + secrets.local.yml), the four supply sources, precedence, the anti-mining invariant, and the trust boundary.
read_when: You are adding a secret to a kit, supplying a value from your machine, wiring a shared/global supply, or reasoning about the trust boundary of at-cove secrets.
owns: the demand/supply secret model — config.yml `secrets` (demand) and ~/.config/at-cove/secrets.yml + secrets.local.yml (supply)
prereqs: at-cove-config.md — the config.yml schema this is part of; ../OVERVIEW.md — the chat/injection data flow
tier: leaf
updated: 2026-07-15
---

# at-cove secrets

A **secret** is an environment variable a sandbox needs (e.g. `AT_TASK_GIT_TOKEN`,
`ANTHROPIC_AUTH_TOKEN`). The model is split cleanly in two:

- **The kit only *demands*.** `config.yml` names the secrets it needs and why. It
  never carries a command, a value, or any machine-specific identifier.
- **The machine only *supplies*.** Two host files under `~/.config/at-cove/`
  (never committed) say how each named demand, for a specific kit, is produced.

Values are resolved on the host at `chat`/`work`/`dispatch` time, held in
memory, and injected over SSH into a tmpfs file — never written to the kit, to
disk, or onto any command line. See the
[injection data flow](../OVERVIEW.md#secret-injection-the-chat-data-flow).

## Declaring a demand — `config.yml` `secrets`

```yaml
secrets:
  ANTHROPIC_AUTH_TOKEN:
    description: short-lived Anthropic bearer for the worker agent
```

Each entry is **keyed by the environment variable name** (exported inside the
sandbox); its only field is:

| Field | Required | Meaning |
|-------|----------|---------|
| `description` | no | Human-readable note; not used at runtime. |

That's the whole schema — a demand is a name plus a description. There is no
`command:` field on a kit secret; a kit that includes one fails to parse. A
committed kit can never dictate *how* its secrets are produced, only *that* it
needs them — this is what keeps kits portable and safe to run from an
untrusted checkout (see [Security caveats](#security-caveats)).

## Supplying a value — the two host files

The machine supplies values in `~/.config/at-cove/secrets.yml` (primary) and
`~/.config/at-cove/secrets.local.yml` (escape hatch), both honoring
`XDG_CONFIG_HOME`. Neither is ever checked in. `secrets.yml` has three
top-level sections:

```yaml
# ~/.config/at-cove/secrets.yml
minters:                             # profiles — how an identity mints; inert until referenced
  gh-cove:
    github:
      app-id: "123"
      install-id: "789"
      app-key: /etc/cove/gh-A.pem

global:                               # named shared supplies; inert until delegated
  shared-tracker: { command: ["gh", "auth", "token"] }

kits:                                 # per-kit authorization: demand -> source
  reference-worker:
    AT_TASK_GIT_TOKEN:         { mint: gh-cove }
    ANTHROPIC_AUTH_TOKEN:      { command: ["your-anthropic-mint.sh"] }
    AT_DISPATCH_TRACKER_TOKEN: { global: shared-tracker }
```

```yaml
# ~/.config/at-cove/secrets.local.yml   (escape hatch; keyed by canonical kit path)
kits:
  /home/me/checkouts/cove:
    ANTHROPIC_AUTH_TOKEN: { value: "sk-ant-oat01-…test…" }   # temp override for testing
```

- **`kits:`** is where a demand actually gets a value: keyed by the kit's
  `name` (in `secrets.yml`) or by the kit's **canonical absolute path** (in
  `secrets.local.yml`). Only an entry written here, under that specific kit,
  supplies anything.
- **`global:`** is a library of named, reusable supplies (a `value` or
  `command`). A `global` entry never supplies a secret by itself — it is only
  reached when a `kits:` entry delegates to it with `{ global: <name> }`.
- **`minters:`** is a library of named minting profiles (structured
  credentials for `at-mint`, e.g. a GitHub App or an Anthropic federation
  identity). Like `global:`, a minter is inert until a `kits:` entry
  references it with `{ mint: <name> }`. Resolving a `{ mint: <name> }` demand
  mints the value by running `at-mint <provider>` built from the named
  `minters:` profile: non-secret fields (an App id, a tenant, an App key given
  as a filesystem path, …) become `at-mint` flags, and any field sourced from
  `command:`/`global:` (e.g. the Auth0 client secret) is resolved and passed to
  `at-mint` as env, in memory, never on argv. A bare
  `command: ["at-mint", "github", …]` (see [at-mint.md](at-mint.md)) remains a
  working manual alternative if you'd rather spell out the full invocation
  yourself; see also
  [the reference kit's RUNBOOK](../../kits/reference-worker/RUNBOOK.md) for a
  complete `minters:` example.

## The four supply sources

Every entry under `kits:` (and every `global:` entry) sets **exactly one** of:

| Source | Form | Resolves to |
|--------|------|--------------|
| `value:` | a literal string | that string, verbatim |
| `command:` | a host **argv array** | stdout of running it on the host, trailing newline trimmed |
| `global: <name>` | a name | delegates to `global[<name>]` in the store (itself a `value`/`command`) |
| `mint: <name>` | a name | mints via `at-mint`, built from `minters[<name>]` (see above) |

`command:` resolvers additionally see the run's parameters as
`COVE_RUN_{REPO,ISSUE,CLASS,TIMEOUT}` in their environment during `work`/
`dispatch` — see
[at-cove-work-interface.md](../orchestration/at-cove-work-interface.md#three-separated-authorities)
for how this turns a resolver into a per-run credential minter.

## Precedence and fail-closed resolution

For each secret **S** demanded by kit **K** at canonical path **P**:

1. **`secrets.local.yml` → `kits[P][S]`** — if present, use it (highest
   precedence; this is *why* the local file is keyed by path — its job is to
   disambiguate, per checkout, not to be portable).
2. **`secrets.yml` → `kits[K.name][S]`** — else if present, use it.
3. otherwise **unresolved** — what happens next depends on whether **S** is
   *required*:
   - a **required well-known secret** — the git token
     (`AT_TASK_GIT_TOKEN`) or the tracker token
     (`AT_DISPATCH_TRACKER_TOKEN`) — **fails closed**: the run aborts
     *before* any VM is built or any SSH happens, naming the secret and the
     kit.
   - on the `work`/`dispatch` path specifically, the agent's Anthropic
     bearer — under **either** well-known name, `ANTHROPIC_AUTH_TOKEN` **or**
     `ANTHROPIC_API_KEY` — is required the same way: the gate passes when at
     least one is declared *and* resolves, and **fails closed** (before
     building or launching a VM, naming the bearer names it looked for and the
     kit) only when neither does. A keyless worker is a guaranteed 401, so
     `at-cove work` refuses to build a container that is certain to fail
     authentication.
   - any other **general / agent demand** instead
     **warns to stderr and is left unset**; the run continues without it.
     (This is still `chat`'s behavior for `ANTHROPIC_AUTH_TOKEN` — the
     pre-flight fail-closed check above is `work`/`dispatch`-only.)

## The anti-mining invariant

**`global:` and `minters:` are never matched by demand name.** A demand named
`ANTHROPIC_AUTH_TOKEN` does *not* automatically pick up a `global` or `minters`
entry of the same name — it is reached *only* through an entry an operator
explicitly wrote under `kits: <that kit>:` (by name or by path). This means:

- A malicious or careless kit cannot "mine" a secret by declaring a demand
  that happens to collide with something in your `global:`/`minters:`
  libraries — those libraries are inert until you, the operator, wire a
  specific kit to them.
- Sharing one `global:`/`minters:` entry across many kits is opt-in and
  explicit (one `kits:` line per kit that should get it), never implicit.
- **Nothing machine-level ever lives in source control.** `secrets.yml` and
  `secrets.local.yml` are host-only files; a kit's committed `config.yml`
  carries no command, no profile name, no identifier that could steer
  resolution — only the demand's name and description.

## Security caveats

- **Host-execution vector.** A `command:` (in `secrets.yml`, `secrets.local.yml`,
  or a `minters:` profile field) is host-authored, not kit-authored — a kit can
  never smuggle in a resolver command of its own, since kit secrets carry no
  `command:` field at all. Still, only supply commands you trust running on
  your own machine.
- **`ANTHROPIC_AUTH_TOKEN` selects the worker's auth path, and fails closed
  twice over.** A dispatched **worker** authenticates to Anthropic with a
  short-lived bearer, `ANTHROPIC_AUTH_TOKEN`, declared as a root secret. Two
  independent layers refuse a keyless worker rather than let it fall back to
  a subscription: `at-cove work` aborts on the *host*, before any VM is
  built, if the bearer is unresolved (see "Precedence and fail-closed
  resolution" above); and, should a worker somehow still launch keyless, the
  work path does not seed OAuth `credentials.json` into the VM, so there is
  no subscription to fall back to *inside* it either. Because the env key
  outranks a subscription OAuth login, an interactive `chat` session on a kit
  that declares it will use the bearer too — so keep worker kits that
  declare it distinct from a kit you `chat` into on a personal subscription.
  See [Authentication](../OVERVIEW.md#authentication).
