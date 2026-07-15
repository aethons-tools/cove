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
`GITHUB_TOKEN`). The model is split cleanly in two:

- **The kit only *demands*.** `config.yml` names the secrets it needs and why. It
  never carries a command, a value, or any machine-specific identifier.
- **The machine only *supplies*.** Two host files under `~/.config/at-cove/`
  (never committed) say how each named demand, for a specific kit, is produced.

Values are resolved on the host at `chat`/`work`/`dispatch` time, held in
memory, and injected over SSH into a tmpfs file — never written to the kit, to
disk, or onto any command line. See the
[injection data flow](../OVERVIEW.md#secret-injection-the-chat-data-flow).

## A demand's bucket is its trust boundary

A kit declares secrets at one of **five schema locations** (the root `secrets`,
`workers.<class>.secrets`, `collaborators.<class>.secrets`,
`source-control.github.secrets`, `tracker.linear.secrets`), and *which* location
a demand is declared at — not its name — determines who resolves it and which
sandbox mode, if any, ever sees the value: the root bucket is injected into
**both** `chat` and `work`/`dispatch`; `workers.<class>.secrets` is **work-only**
(and, within a run, agent-step only — see
[Precedence and fail-closed resolution](#precedence-and-fail-closed-resolution)
below); `collaborators.<class>.secrets` is **chat-only**. The full matrix —
every bucket, its resolver, and its `chat`/`work` visibility — is the single
table in [at-cove-config.md § Secret buckets](at-cove-config.md#secret-buckets);
this doc covers the supply side, which is identical across all five.

## Declaring a demand — `config.yml` `secrets`

```yaml
secrets:
  GITHUB_TOKEN:
    description: private-repo git over HTTPS (interactive sessions)
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

An Anthropic agent bearer (`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`) is the
one demand that may not be declared in the root `secrets` bucket shown above —
see [Migrating the worker bearer off the root bucket](#migrating-the-worker-bearer-off-the-root-bucket).

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
     bearer — declared in the resolved worker class's `workers.<class>.secrets`
     (or `workers.<common>.secrets`), as either `ANTHROPIC_AUTH_TOKEN` or
     `ANTHROPIC_API_KEY` — is subject to a pre-flight gate keyed specifically
     on `ANTHROPIC_AUTH_TOKEN`: if that name is declared with no supply, or
     not declared at all, a keyless worker is a guaranteed 401, so
     `at-cove work` **fails closed** before building or launching a VM, naming
     the secret and the kit, instead of building a container that is certain
     to fail authentication. The gate checks only that literal name — a class
     that declares `ANTHROPIC_API_KEY` instead isn't recognized by it, and
     (since the gate treats a missing `ANTHROPIC_AUTH_TOKEN` as unresolved)
     still fails closed today rather than being treated as covered. See
     [Migrating the worker bearer off the root bucket](#migrating-the-worker-bearer-off-the-root-bucket)
     for why it lives in that bucket, not the root one.
   - any other **general / agent demand** instead
     **warns to stderr and is left unset**; the run continues without it. This
     includes the worker bearer on `chat`: since it can only be declared under
     `workers.*.secrets` — a bucket `chat` never resolves at all — a `chat`
     session simply never sees it, so there is no `chat`-side fail-closed case
     for it to hit.

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

## Migrating the worker bearer off the root bucket

A dispatched **worker** authenticates to Anthropic with a short-lived bearer —
either `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` — declared under that
worker class's `workers.<class>.secrets`, or the shared `workers.<common>.secrets`
base (see [at-cove-config.md § workers](at-cove-config.md#workers)). It must
**not** be declared in the root `secrets` bucket; `config.yml` rejects either
name there as a hard parse error.

**Why.** The root bucket is injected into *both* `chat` and
`at-cove work`/`dispatch` (see [Secret buckets](at-cove-config.md#secret-buckets)).
Claude Code picks its credential by env-driven precedence, and an injected
bearer always outranks a subscription OAuth login. So a bearer declared at
root would reach an interactive `chat` session too — silently switching that
session off the human's own subscription and onto the bearer, and **disabling
the session's claude.ai connectors** (GitHub/Linear), which only work under
subscription OAuth. Requiring the bearer in the worker bucket — which `chat`
never resolves at all (see [Secret buckets](at-cove-config.md#secret-buckets))
— removes this failure mode structurally instead of relying on kit authors to
remember to keep worker and chat kits distinct.

**Migrating an existing kit** — move the entry from the root `secrets:` map
into `workers.<common>.secrets` (shared by every class) or a specific
`workers.<class>.secrets` (only that class):

```yaml
# before (root — now a hard parse error)
secrets:
  ANTHROPIC_AUTH_TOKEN:
    description: short-lived Anthropic bearer for the worker agent

# after
workers:
  <common>:
    secrets:
      ANTHROPIC_AUTH_TOKEN:
        description: short-lived Anthropic bearer for the worker agent
```

The bucket is resolved **lazily** — immediately before the class's agent step,
after `at-task prepare` has already succeeded — so a freshly minted bearer's
TTL only has to cover the agent's own run; the value never reaches the git
steps (`at-task prepare`/`complete`). `at-cove work` still fails closed on the
host, before any VM is built, if the bearer ends up unresolved — see
[Precedence and fail-closed resolution](#precedence-and-fail-closed-resolution)
above. See also [Authentication](../OVERVIEW.md#authentication) for how this
fits the two-layer fail-closed design (host pre-flight + no OAuth fallback
inside the VM).

## Security caveats

- **Host-execution vector.** A `command:` (in `secrets.yml`, `secrets.local.yml`,
  or a `minters:` profile field) is host-authored, not kit-authored — a kit can
  never smuggle in a resolver command of its own, since kit secrets carry no
  `command:` field at all. Still, only supply commands you trust running on
  your own machine.
- **The worker bearer fails closed twice over.** Two independent layers
  refuse a keyless worker rather than let it fall back to a subscription:
  `at-cove work` aborts on the *host*, before any VM is built, if the bearer
  is unresolved (see "Precedence and fail-closed resolution" above); and,
  should a worker somehow still launch keyless, the work path does not seed
  OAuth `credentials.json` into the VM, so there is no subscription to fall
  back to *inside* it either. See
  [Migrating the worker bearer off the root bucket](#migrating-the-worker-bearer-off-the-root-bucket)
  for why the bearer must live in the worker bucket, not root.
