---
summary: The at-cove kit config.yml schema — every field an operator sets to define a sandbox and its scheduler (name, source-control, tracker, dispatch, secrets, workers, collaborators, image), with validation rules, the secret-bucket boundaries, and a full annotated example.
read_when: You are authoring or editing a kit's .at-cove/config.yml — setting the target repo (source-control), wiring the issue tracker or scheduler policy, adding a secret, a worker or collaborator class, an allowed domain, or a PATH entry.
owns: the config.yml schema: name, source-control, tracker, dispatch, workers, collaborators, secrets, image (+ validation)
prereqs: ../OVERVIEW.md — what at-cove is and the kit/build model; at-cove-secrets.md — secret declaration + resolution
tier: leaf
updated: 2026-07-13
---

# at-cove `config.yml`

`config.yml` is a kit's **spec** — identity and wiring only, for both the sandbox
(`connect`/`work`) and the scheduler (`dispatch`). It carries **no secret values**, no
hardening knobs, and no workspace mode (those are chosen at `create` time so a committed
spec stays portable). It lives at the kit root — by convention `<repo>/.at-cove/config.yml`
(see the [kit layout](../OVERVIEW.md#the-kit-at-cove)).

Parsing is **strict**: an unknown or misspelled field is a hard error (`config.yml: field
… not found`), so typos surface immediately rather than being silently ignored.

`at-cove dispatch [kit-dir]` reads this same file directly — the scheduler now consumes
the kit like every other command; there is no separate scheduler config file.

## Fields

A `*` marks a field required for `config.yml` to parse. Some fields are optional in the
*schema* but required for a particular command to run — those are called out explicitly.

### name*
*string*

The base sandbox/VM name. Also keys the per-sandbox `known_hosts` and the state/workspace
volumes. Keep it stable — changing it points commands at a different instance.

### source-control
*tagged union — one host (`github` only today)*

The remote the kit targets — the **single source of truth** for the repo identity *and* the
code-host kind (which selects the clone URL, the PR API, and the matching secret minter).
**Required for `at-cove work`**; interactive `connect` works without it. `at-cove work`
fills the target repo into the worker's task from `source-control`, so nothing else names a repo.

#### source-control.github.project*
*string — `owner/name`*

The GitHub repository the workers act on.

#### source-control.github.main-branch
*string, defaults to `main`*

The repo's base branch. A dispatched task may override it per-run (its `source-branch`);
absent an override, at-cove uses `source-control.github.main-branch`.

#### source-control.github.secrets
*map of secret env name → config, optional in the schema*

Host-side secrets minted for git operations, in the same declaration shape as the root
`secrets` (see [at-cove-secrets.md](at-cove-secrets.md)) — but a **distinct bucket**: see
[Secret buckets](#secret-buckets) below. The only allowed name is the well-known
`AT_TASK_GIT_TOKEN`; if the map is non-empty it must contain exactly that key. Its
`command` is **optional** — omit it to supply the value from the user's
`~/.config/at-cove/secrets.yml` instead (matched by name; see
[at-cove-secrets.md](at-cove-secrets.md)). **Schema-optional, but `at-cove work` refuses
to run without the key declared** (`kit "…" declares no source-control.github.secrets
AT_TASK_GIT_TOKEN`) — the value is resolved fresh per git step and read only by
`at-task prepare`/`complete`, never by the agent.

```yaml
source-control:
  github:
    project: acme/myrepo
    main-branch: main
    secrets:
      AT_TASK_GIT_TOKEN:
        description: per-task GitHub App installation token — push + PR on the repo
        command: ["mint-github-token.sh"]
```

### tracker
*tagged union — one provider (`linear` only today)*

Names the issue tracker the kit's scheduler drives. Parsed, validated, and read
directly by `at-cove dispatch [kit-dir]` — the scheduler consumes the kit's
`tracker`/`dispatch`/`workers` fields instead of a separate config file.

#### tracker.linear.team*
*string*

The Linear team key the scheduler polls.

#### tracker.linear.poll-interval*
*string (Go duration)*

How often the scheduler polls Linear for state changes, as a backstop to
webhook-driven updates. Keep it low-frequency (e.g. `60s`) — webhooks handle the
common case, and this interval only needs to catch what they miss.

#### tracker.linear.class-label-prefix
*string, defaults to `class:`*

The label prefix that maps a Linear issue to a worker class (e.g. `class:implement`).

#### tracker.linear.states*
*map of the scheduler's six lifecycle roles → that team's real state names*

`ready`, `in-progress`, `in-review`, `done`, `needs-input`, `blocked` — all six required.
This binds the design's uniform lifecycle roles (see
[linear-agent-workflow.md](../orchestration/linear-agent-workflow.md)) to whatever state
names a given Linear team actually uses, so the scheduler's logic never hard-codes
team-specific state strings — e.g. `ready: Todo` means issues in the team's "Todo" state
are treated as `ready`.

#### tracker.linear.secrets
*map of secret env name → config*

Host-side, scheduler-only — never injected into a sandbox VM (see
[Secret buckets](#secret-buckets)). Accepts exactly `AT_DISPATCH_TRACKER_TOKEN` and
`AT_DISPATCH_WEBHOOK_SECRET`; both keys must be declared, but each `command` is
**optional** — omit it to supply the value from the user's
`~/.config/at-cove/secrets.yml` instead (matched by name; see
[at-cove-secrets.md](at-cove-secrets.md)). Any other key is rejected.

```yaml
tracker:
  linear:
    team: COV
    poll-interval: 60s
    states:
      ready: Todo
      in-progress: In Progress
      in-review: In Review
      done: Done
      needs-input: Needs Input
      blocked: Backlog
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  { command: ["gh", "auth", "token"] }
      AT_DISPATCH_WEBHOOK_SECRET: { command: ["true"] }
```

### dispatch
Scheduler policy knobs, read by `at-cove dispatch [kit-dir]`.

#### dispatch.concurrency*
*int >= 1*

Global cap on concurrent autonomous dispatches across all worker classes. A class's own
`workers.*.concurrency` (if set) further restricts that class's slice of this budget;
tune both to your resource budget and SLA.

#### dispatch.reaper-timeout*
*string (Go duration)*

Bounds how long a stalled run is left running before being reaped. If an issue sits
`in-progress` with no progress past this timeout, the scheduler moves it to
`needs-input` so a human can look — this guards against a hung or crashed worker
leaving an issue silently stuck.

#### dispatch.dispatch-overhead
*string (Go duration), defaults to `15m`*

Spare time budgeted around a worker's own `timeout` (its class's `workers.*.timeout`)
before the scheduler treats the run as stalled. Tune it to your image's build/boot
time — a slower-building image needs more overhead before the scheduler calls a run
stale.

```yaml
dispatch:
  concurrency: 1
  reaper-timeout: 45m
```

### secrets
*map of secret env name → config*

The **agent bucket** — environment variables injected into the sandbox VM, in memory only,
for both interactive `connect` and `at-cove work`. Declared **by name**; values are resolved
at `connect`/`dispatch` time and never stored in the kit. Full schema — declaration,
host-resolver `command`, the user-supplied `~/.config/at-cove/secrets.yml`, precedence,
and the fail-closed / host-execution rules — is in **[at-cove-secrets.md](at-cove-secrets.md)**.

#### secrets.*name*.description
*string*

Describes the content and use of the secret.

#### secrets.*name*.command
*list of command line tokens*

The command to execute to resolve the secret's value (its stdout).
If omitted, the value is supplied from the user's `~/.config/at-cove/secrets.yml`
(see [at-cove-secrets.md](at-cove-secrets.md)).

```yaml
secrets:
  GITHUB_TOKEN:
    description: private-repo git over HTTPS (interactive sessions)
    command: ["gh", "auth", "token"]
```

### workers
*map of classname → config*

Defines the autonomous worker classes that `at-cove work` can launch. The reserved key
`<common>` is a base merged into every real class (own value wins on a scalar); it must
not set a `prompt`.

#### workers.*class*.prompt*
*string, required for every class except `<common>`, own-only (not inherited)*

The prompt to send to the worker.

#### workers.*class*.timeout
*string (Go duration, e.g. `30m`), optional, inherited from `<common>` if unset*

Per-run timeout for the worker's agent step.

#### workers.*class*.concurrency
*int >= 0, optional, inherited from `<common>` if unset*

Max concurrent runs of this class.

```yaml
workers:
  <common>:
    timeout: 30m
    concurrency: 1
  triage:
    prompt: Determine what needs to be done and write TODOs.
```

`at-cove` resolves a class's effective config (the `<common>` merge, own overrides base) via
`kit.Config.ResolvedWorker`.

### collaborators
*map of classname → config*

Declares interactive (chat) handler classes, mirroring `workers`' `<common>`-base
shape: the reserved key `<common>` holds `secrets` merged into every real class
(own key wins). **Validated now, wired later** — parsed and validated like every
other field, but no command consumes it yet.

#### collaborators.*class*.secrets
*map of secret env name → config, optional, inherited from `<common>` (own key wins)*

Same declaration shape as the root `secrets`, but a distinct bucket (see
[Secret buckets](#secret-buckets)).

```yaml
collaborators:
  <common>:
    secrets:
      COMMON_TOKEN: { command: ["true"] }
  triager:
    secrets:
      LINEAR_TOKEN: { command: ["true"] }
```

`at-cove` resolves a class's effective secrets via `kit.Config.ResolvedCollaborator`.

### image
**Additive** build-time customizations of the sandbox image. Every field layers **onto**
the hardened baseline and can never override it — cove translates each to the correct
sealed mechanism.

#### image.setup-scripts
*list of strings*
Kit-relative scripts run **as root at build**, in place (e.g. install a toolchain). Each must be non-empty.

#### image.paths
*list of strings*

Appended to `PATH` in `/etc/environment`. Each must be non-empty and single-line.

#### image.env
*map string → string*

`KEY=VALUE` written to `/etc/environment`. Keys must be non-empty and free of `=`/newline; values single-line.
**Cannot set base-owned keys** — `PATH`, `CLAUDE_CONFIG_DIR`, `http_proxy`/`https_proxy`
(and their upper-case / `no_proxy` variants) are owned by the sealed hardening layer;
setting them is a hard error (it would breach the additive guarantee or weaken the egress
gate). Use `paths:` to extend `PATH`.

#### image.allowed-domains
*list of strings*

Added to the squid egress allow-list. Each must be non-empty.

```yaml
image:
  setup-scripts:
    - .install-files/install-go.sh
  paths:
    - /usr/local/go/bin
  env:
    GOFLAGS: "-mod=mod"
  allowed-domains:
    - proxy.golang.org
    - sum.golang.org
```

## Secret buckets

A secret's declaration lives in one of **four schema locations**, and that location — not
a naming convention — *is* its trust boundary: which process resolves it and which process
(if any) ever sees the value inside a VM. This is a **structural** air-gap: a secret can't
leak across a boundary by name collision, because each consumer reads its own bucket, not a
flat merged list.

| Bucket | Resolved by | Reaches a sandbox VM? | Used by |
|---|---|---|---|
| `secrets` (root) | host, at `connect`/`dispatch` time | yes — injected in memory, both `connect` and `at-cove work` | the agent process |
| `source-control.github.secrets` | host, minted fresh per git step | no — held on the host | `at-task prepare`/`complete` only |
| `tracker.linear.secrets` | host, scheduler-only | never | `at-cove dispatch` (a later plan) |
| `collaborators.*.secrets` | host (validated now) | not yet — wired in a later plan | interactive/chat classes, once wired |

The resolver mechanics (`command`/`description`, the user's `~/.config/at-cove/secrets.yml`,
precedence, fail-closed behavior) are the same across all four buckets and documented once,
in [at-cove-secrets.md](at-cove-secrets.md) — this table only draws the boundaries between them.

## Full example

```yaml
name: claude-on-myrepo

source-control:
  github:
    project: acme/myrepo
    main-branch: main
    secrets:
      # Used only by at-task prepare/complete (clone/push/PR); minted per run,
      # resolved on the host, injected in memory. The agent never sees it.
      AT_TASK_GIT_TOKEN:
        description: per-task GitHub App installation token — push + PR on the repo
        command: ["mint-github-token.sh"]

tracker:
  linear:
    team: COV
    poll-interval: 60s
    states:
      ready: Todo
      in-progress: In Progress
      in-review: In Review
      done: Done
      needs-input: Needs Input
      blocked: Backlog
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  { command: ["gh", "auth", "token"] }
      AT_DISPATCH_WEBHOOK_SECRET: { command: ["true"] }

dispatch:
  concurrency: 1
  reaper-timeout: 45m

secrets:
  # The agent bucket — injected into the sandbox VM (connect and at-cove work).
  GITHUB_TOKEN:
    description: private-repo git over HTTPS (interactive sessions)
    command: ["gh", "auth", "token"]

workers:
  <common>:
    timeout: 30m
    concurrency: 1
  implement:
    prompt: |
      You are an implementer. Make the change described in the task and run the
      project's tests. Keep the change minimal and focused.

collaborators:
  triager:
    secrets:
      LINEAR_TOKEN: { command: ["true"] }

image:
  setup-scripts:
    - .install-files/install.sh
  allowed-domains:
    - api.anthropic.com   # the agent (claude)
    - api.github.com      # at-task PR API
    - github.com          # at-task clone/push
```

This mirrors [`kits/reference-worker/config.yml`](../../kits/reference-worker/config.yml),
the template kit for `at-cove dispatch`.

## Validation summary

`config.yml` is rejected (with a `config.yml: …` error) if any of:
- an unknown field is present, or `name` is missing;
- `source-control` sets more than one host, or `source-control.github.project` is not `owner/name`;
- `source-control.github.secrets` is non-empty but doesn't declare exactly
  `AT_TASK_GIT_TOKEN` (its `command` is optional; else supplied from
  `~/.config/at-cove/secrets.yml`);
- an `image.setup-scripts[i]` / `image.paths[i]` / `image.allowed-domains[i]` is empty (or a
  path contains a newline);
- an `image.env` key is empty, contains `=`/newline, or is a **base-owned** key; or a value
  contains a newline;
- a `workers` key looks `<reserved>` but isn't `<common>`; `<common>` sets a `prompt`; a real
  class omits `prompt`; a `timeout` isn't a positive Go duration; or a `concurrency` is negative;
- `tracker` sets more than one provider; `tracker.linear.team` is missing, `poll-interval`
  isn't a positive Go duration, a `states` entry is missing, or `secrets` doesn't declare
  exactly `AT_DISPATCH_TRACKER_TOKEN` / `AT_DISPATCH_WEBHOOK_SECRET` (each `command` is
  optional; else supplied from `~/.config/at-cove/secrets.yml`);
- `dispatch.concurrency` is < 1, or `reaper-timeout` / `dispatch-overhead` isn't a positive
  Go duration;
- a `collaborators` key looks `<reserved>` but isn't `<common>`.

Other fields (e.g. `secrets.*.command`) are structurally validated only — the decoder rejects
wrong shapes/types.
