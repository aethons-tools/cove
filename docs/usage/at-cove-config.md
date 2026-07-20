---
summary: The at-cove kit config.yml schema — every field an operator sets to define a sandbox and its scheduler (name, source-control, tracker, dispatch, secrets, workers, collaborators, image), with validation rules, the secret-bucket boundaries, and a full annotated example.
read_when: You are authoring or editing a kit's .at-cove/config.yml — setting the target repo (source-control), wiring the issue tracker or scheduler policy, adding a secret, a worker or collaborator class, an allowed domain, or a PATH entry.
owns: "the config.yml schema: name, source-control, tracker, dispatch, workers, collaborators, secrets, image (+ validation)"
prereqs: ../OVERVIEW.md — what at-cove is and the kit/build model; at-cove-secrets.md — secret demand + supply
tier: leaf
updated: 2026-07-20
---

# at-cove `config.yml`

`config.yml` is a kit's **spec** — identity and wiring only, for both the sandbox
(`chat`/`work`) and the scheduler (`dispatch`). It carries **no secret values** and no
hardening knobs (those stay machine-side so a committed spec stays portable). The one
workspace-mode knob is a per-collaborator opt-in — [`share-repo-dir`](#collaboratorsclassshare-repo-dir).
It lives at the kit root — by convention `<repo>/.at-cove/config.yml`
(see the [kit layout](../OVERVIEW.md#the-kit-at-cove)).

Parsing is **strict**: an unknown or misspelled field is a hard error (`config.yml: field
… not found`), so typos surface immediately rather than being silently ignored.

`at-cove dispatch --project-dir <dir>` reads this same file directly — the scheduler now
consumes the kit like every other command; there is no separate scheduler config file.

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
**Required for `at-cove work`**; interactive `chat` works without it. `at-cove work`
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

Host-side secrets minted for git operations, in the same **demand-only** declaration
shape as the root `secrets` (see [at-cove-secrets.md](at-cove-secrets.md)) — but a
**distinct bucket**: see [Secret buckets](#secret-buckets) below. The only allowed
name is the well-known `AT_TASK_GIT_TOKEN`; if the map is non-empty it must contain
exactly that key. The value is always supplied machine-side, from the user's
`~/.config/at-cove/secrets.yml`/`secrets.local.yml` (matched by kit name/path; see
[at-cove-secrets.md](at-cove-secrets.md)) — a kit secret never carries a resolver
command. **Schema-optional, but `at-cove work` refuses to run without the key
declared** (`kit "…" declares no source-control.github.secrets AT_TASK_GIT_TOKEN`) —
the value is resolved fresh per git step and read only by `at-task prepare`/
`complete`, never by the agent.

```yaml
source-control:
  github:
    project: acme/myrepo
    main-branch: main
    secrets:
      AT_TASK_GIT_TOKEN:
        description: per-task GitHub App installation token — push + PR on the repo
```

### tracker
*tagged union — one provider (`linear` only today)*

Names the issue tracker the kit's scheduler drives. Parsed, validated, and read
directly by `at-cove dispatch --project-dir <dir>` — the scheduler consumes the kit's
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
`AT_DISPATCH_WEBHOOK_SECRET`; both keys must be declared, demand-only (no
`command`) — the value is always supplied from the user's
`~/.config/at-cove/secrets.yml`/`secrets.local.yml` (matched by kit name/path; see
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
      AT_DISPATCH_TRACKER_TOKEN:  { description: "Linear API token for the scheduler" }
      AT_DISPATCH_WEBHOOK_SECRET: { description: "Linear webhook signing secret" }
```

### dispatch
Scheduler policy knobs, read by `at-cove dispatch --project-dir <dir>`.

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
for both interactive `chat` and `at-cove work`. **Demand-only**: declared **by name**
with an optional description; a kit never carries a command or a value. Every value is
resolved machine-side at `chat`/`dispatch` time from the user's
`~/.config/at-cove/secrets.yml`/`secrets.local.yml`, and never stored in the kit. Full
schema — the two host supply files, the four supply sources (`value`/`command`/`global`/
`mint`), precedence, the anti-mining invariant, and the fail-closed / trust-boundary rules
— is in **[at-cove-secrets.md](at-cove-secrets.md)**.

#### secrets.*name*.description
*string*

Describes the content and use of the secret. The only field a kit secret carries.

```yaml
secrets:
  GITHUB_TOKEN:
    description: private-repo git over HTTPS (interactive sessions)
```

An Anthropic agent bearer (`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`) may
**not** be declared here — the root bucket reaches `chat` as well as `work`,
and there the bearer would outrank the subscription OAuth login and disable
the session's connectors. `config.yml` rejects it as a hard parse error;
declare it under `workers.*class*.secrets` (see [workers](#workers)) instead
— see
[at-cove-secrets.md](at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket).

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

#### workers.*class*.secrets
*map of secret env name → config, optional, inherited from `<common>` (own key wins)*

Same declaration shape as the root `secrets`, but a distinct bucket (see
[Secret buckets](#secret-buckets)): **work-only**, and resolved **lazily** —
immediately before the class's agent step, after `at-task prepare` has already
succeeded. It never reaches `chat`, and never reaches the git steps (`at-task
prepare`/`complete`) of a dispatched run — only the agent process on that one
run sees it. This is where a worker's Anthropic bearer belongs:
`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY` must be declared here (or under
`workers.<common>.secrets`), never at the kit root — see
[at-cove-secrets.md](at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket).

#### workers.*class*.allowed-domains
*list of strings, optional, unioned with `<common>` (a set, not overwritten)*

Egress domains scoped to this worker class, **added to** the root
[`image.allowed-domains`](#imageallowed-domains) for a run of this class. Same
per-entry rule as the root list (each non-empty). Unlike the `<common>`-merge of
scalars/secrets (where own overwrites base), domains are a **set union**: a
class's effective per-class list is `workers.<common>.allowed-domains ∪
workers.<class>.allowed-domains`, deduped and order-normalized. This gives an
autonomous class a wider (never narrower) egress than the kit default. It is the
config + resolver layer (`kit.Config.ResolvedWorkerDomains`) of the per-class
**session-scoped** egress model
([COV-39](../superpowers/specs/2026-07-19-per-class-egress-design.md)): at-cove
resolves this delta from the current `install.json` (never a live `config.yml`) and
applies it to the running container **before the agent step** via `ApplySessionEgress`
(a privileged `docker exec` of the sealed `apply-session-domains.sh` + `squid -k
reconfigure`), so squid reaches only `root ∪ <common> ∪ class` for that run — see the
[three additive allow-lists](../OVERVIEW.md#egress-three-additive-allow-lists-session-scoped)
and [the work interface](../orchestration/at-cove-work-interface.md).

```yaml
workers:
  <common>:
    timeout: 30m
    concurrency: 1
    allowed-domains: [github.com]          # every worker class
    secrets:
      ANTHROPIC_AUTH_TOKEN:
        description: short-lived Anthropic bearer for the worker agent
  triage:
    prompt: Determine what needs to be done and write TODOs.
  deploy:
    prompt: Ship it.
    allowed-domains: [registry.example.com]  # deploy also reaches the registry
```

`at-cove` resolves a class's effective config — `timeout`/`concurrency`/`secrets`
all `<common>`-merged, own overrides base — via `kit.Config.ResolvedWorker`, and
its per-class egress delta (`<common> ∪ class`) via
`kit.Config.ResolvedWorkerDomains` (root stays separate, delivered to every
session).

### collaborators
*map of classname → config*

Declares interactive (`chat`) handler classes, mirroring `workers`' `<common>`-base
shape: the reserved key `<common>` holds `secrets` merged into every real class
(own key wins). `at-cove chat [collaborator]` selects one of these classes and
launches a session with its role injected as context — see
[the collaborator session boundary](../OVERVIEW.md#the-chat-command-and-collaborator-sessions).

**Most** collaborator access rides the human's **claude.ai account connectors**
during an interactive session, not a minted token, so collaborators declare few
secrets. The exception is the **`gh` and `git` CLIs**: they are separate
processes that read a token from the session env, not connector tools, so no
connector can serve them — the hardening layer's credential helper feeds
`github.com` from `GITHUB_TOKEN` (see
[Authentication](../OVERVIEW.md#authentication)). A kit whose collaborators run
`gh` or push over HTTPS declares `GITHUB_TOKEN` under `<common>`, merging it
into every class; `secrets` (below) also covers the occasional extra scoped
token.

#### collaborators.*class*.prompt
*string, optional, own-only (not inherited); `<common>` must not set it*

The collaborator's **role**, injected as session context (written into a
`CLAUDE.md`-included file in the VM, not sent as a headless `-p` prompt — the
session is interactive). Use it to state the class's job and its plan-vs-implement
boundary, e.g. "groom the board and emit Linear issues; only fix in place during
review/troubleshooting." A collaborator with no `prompt` still selects and
launches — it just injects no role.

#### collaborators.*class*.default
*bool, optional, own-only (not inherited); `<common>` must not set it*

Marks this class as the one `at-cove chat` picks when invoked with no collaborator
positional and the kit defines more than one. **At most one** class may set
`default: true` — `config.yml` fails to parse if two or more do. See the
[selection rule](#chat-collaborator-selection) below.

#### collaborators.*class*.share-repo-dir
*bool, optional, own-only (not inherited); `<common>` must not set it*

Opts this class's VM into a **Shared** workspace: instead of an isolated volume,
`create <class>` bind-mounts the **kit's repo dir** — the directory that contains
`.at-cove/` — at `/home/agent/workspace`, so host and VM share the live `.git`.
Absent/false (the default), the workspace is **Isolated** and the
[clone-on-first-session](../OVERVIEW.md#workspace-and-state-volumes) populates it.
Only the kit repo dir is shareable — arbitrary host paths are not mountable (the
former `--workspace`/`--ws` flag is gone). `recreate` recovers the recorded mount
from the instance's state, never re-reading this field. See
[Workspace and state volumes](../OVERVIEW.md#workspace-and-state-volumes).

#### collaborators.*class*.secrets
*map of secret env name → config, optional, inherited from `<common>` (own key wins)*

Same declaration shape as the root `secrets`, but a distinct bucket (see
[Secret buckets](#secret-buckets)). Typically just `GITHUB_TOKEN` — see above.

#### collaborators.*class*.allowed-domains
*list of strings, optional, unioned with `<common>` (a set, not overwritten)*

Egress domains scoped to this collaborator class, mirroring
[`workers.*class*.allowed-domains`](#workersclassallowed-domains): a **set union**
with the collaborators `<common>` list (deduped, order-normalized), added to the
root `image.allowed-domains` for a `chat` session of this class. Resolved via
`kit.Config.ResolvedCollaboratorDomains` (from the current `install.json`, never a
live `config.yml`); `chat` applies this delta on session start and clears it on exit,
so an idle persistent container reverts to root-only (COV-39 §5 — see
[The `chat` command and collaborator sessions](../OVERVIEW.md#the-chat-command-and-collaborator-sessions)).
Part of the per-class egress model under
[COV-39](../superpowers/specs/2026-07-19-per-class-egress-design.md).

```yaml
collaborators:
  <common>:
    secrets:
      COMMON_TOKEN: { description: "shared token for every collaborator class" }
  triager:
    default: true
    prompt: |
      You are the board steward for this repo. Turn ideas into well-formed Linear
      issues and decompose them into dispatchable sub-issues. PLAN — do not
      implement: emit issues, let dispatched workers build them. The one
      exception: during review or troubleshooting you MAY make direct fixes.
  reviewer:
    prompt: |
      You review dispatched work and may fix issues in place via the GitHub
      connector.
```

`at-cove` resolves a class's effective secrets and prompt via
`kit.Config.ResolvedCollaborator`.

#### `chat` collaborator selection

`at-cove chat [collaborator]` takes an optional leading positional naming a
`collaborators` class (`<common>` is never selectable):

- given explicitly, it must match a defined class, else a usage error listing
  the declared classes;
- omitted, with exactly one class defined — that one is used;
- omitted, with several classes defined — the one marked `default: true`; if
  none is marked, a usage error listing them (`chat: multiple collaborators;
  specify one of: …`);
- omitted, with **no** classes defined — `chat` launches a plain session with no
  role injected (today's behavior, unchanged).

### image
The image at-cove hardens, plus the kit's additive egress. Build-time customization
lives in the kit's **`image/Dockerfile`** (COV-34); `config.yml` carries only `base`
and `allowed-domains`.

A kit's build-time files live in a sibling **`image/`** directory (`.at-cove/image/`),
which is the Docker **build context** for an `image/Dockerfile` — it is **not** overlaid
onto the sandbox. To customize the build (install a toolchain, seed files), write an
`image/Dockerfile`. To add session env for every SSH session, just set it with **`ENV`**
and name it in **`COVE_SSHENV`** (colon-separated): the sealed hardening layer copies
`PATH` (intrinsic) plus every `COVE_SSHENV`-named variable's live value into
`/etc/environment`. So one `ENV` satisfies both `docker run`/CI and SSH sessions — e.g.
`ENV FOO=bar` then `ENV COVE_SSHENV="${COVE_SSHENV}:FOO"`. (The egress proxy vars and
`CLAUDE_CONFIG_DIR` are sealed-owned and cannot be set this way.) at-task is injected by
the sealed layer — never install it.

#### image.base
*string*

Names the base image at-cove hardens (an image ref, e.g. `ghcr.io/acme/base@sha256:…`).
**Mutually exclusive** with an `image/Dockerfile`: if the kit both sets `image.base`
**and** ships an `image/Dockerfile`, `at-cove` rejects the kit (they are two ways to name
the same thing — pick one). Set neither and at-cove hardens the default
`cove-base-image`.

A kit-chosen base (this field or an `image/Dockerfile`) must pass a **provenance
gate**: it must descend from a blessed `cove-base-image` (proven by an OCI layer
`diff_id` prefix), so the sealed hardening layer can trust its prerequisites. A
base that descends from none is **rejected** — pass `--allow-unverified-base` (on
`at-cove install`, the single build+gate step) to downgrade the rejection to a
loud warning and proceed at your own risk. The default base skips the gate (it is
blessed by construction).

#### image.allowed-domains
*list of strings*

The **root** egress allow-list — added to the squid allow-list and **baked into
every session** (as `allowed_domains.kit.txt`), on top of the sealed base. Each
entry must be non-empty. It is the base term of the per-class union: a class's
effective egress is **`image.allowed-domains ∪ workers.<common> ∪ workers.<class>`**
(and likewise for `collaborators`), where only the `<common> ∪ class` delta is
delivered per session — see [`workers.*class*.allowed-domains`](#workersclassallowed-domains)
and the [three additive allow-lists](../OVERVIEW.md#egress-three-additive-allow-lists-session-scoped)
model. When a dispatched run is blocked by the allow-list, at-cove ends the issue in
**NEEDS INPUT** naming the blocked host(s) and pointing back to this key as the remedy
— see [at-cove-work-interface.md](../orchestration/at-cove-work-interface.md#egress-wall-denials-surface-as-needs-input).

```yaml
# base + build-time customization live in image/Dockerfile (FROM a blessed base,
# install your toolchain, ENV + COVE_SSHENV for session env). config.yml carries:
image:
  allowed-domains:
    - proxy.golang.org
    - sum.golang.org
```

## Secret buckets

A secret's declaration lives in one of **five schema locations**, and that location — not
a naming convention — *is* its trust boundary: which process resolves it and which process
(if any) ever sees the value inside a VM, in which command mode. This is a **structural**
air-gap: a secret can't leak across a boundary by name collision, because each consumer
reads its own bucket, not a flat merged list.

| Bucket | Resolved by | `chat` | `work`/`dispatch` | Used by |
|---|---|---|---|---|
| `secrets` (root) | host, at `chat`/`dispatch` time | injected | injected | the agent process |
| `collaborators.*.secrets` | host, at `chat` time, `<common>`-merged | injected | — | the collaborator session (usually just `GITHUB_TOKEN` for `gh`/`git`; most other access rides connectors) |
| `workers.*.secrets` | host, resolved lazily right before the agent step, `<common>`-merged | — | injected (agent step only) | the dispatched agent process |
| `source-control.github.secrets` | host, minted fresh per git step | — | injected (git steps only) | `at-task prepare`/`complete` only |
| `tracker.linear.secrets` | host, scheduler-only | — | — (never reaches a VM) | `at-cove dispatch` (a later plan) |

Every bucket is **demand-only** in the kit — a name plus a `description`. The supply
mechanics (the two host files, the four sources, precedence, the anti-mining invariant,
fail-closed behavior) are the same across all five buckets and documented once, in
[at-cove-secrets.md](at-cove-secrets.md) — this table only draws the boundaries between them.
In particular, an Anthropic agent bearer (`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`) must
live in the `workers.*.secrets` row, not the root row — see
[at-cove-secrets.md](at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket).

## Full example

```yaml
name: claude-on-myrepo

source-control:
  github:
    project: acme/myrepo
    main-branch: main
    secrets:
      # DEMAND only. Used only by at-task prepare/complete (clone/push/PR);
      # minted per run, resolved on the host, injected in memory. The agent
      # never sees it. The value is supplied machine-side (see at-cove-secrets.md).
      AT_TASK_GIT_TOKEN:
        description: per-task GitHub App installation token — push + PR on the repo

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
      AT_DISPATCH_TRACKER_TOKEN:  { description: "Linear API token for the scheduler" }
      AT_DISPATCH_WEBHOOK_SECRET: { description: "Linear webhook signing secret" }

dispatch:
  concurrency: 1
  reaper-timeout: 45m

secrets:
  # The agent bucket — injected into the sandbox VM (chat and at-cove work).
  # DEMAND only; the value is supplied machine-side (see at-cove-secrets.md).
  GITHUB_TOKEN:
    description: private-repo git over HTTPS (interactive sessions)

workers:
  <common>:
    timeout: 30m
    concurrency: 1
    secrets:
      # Work-only, agent-step only — never chat, never the git steps (see
      # Secret buckets above). Must live here, not the root `secrets` bucket
      # (see at-cove-secrets.md).
      ANTHROPIC_AUTH_TOKEN:
        description: short-lived Anthropic bearer for the worker agent
  implement:
    prompt: |
      You are an implementer. Make the change described in the task and run the
      project's tests. Keep the change minimal and focused.

collaborators:
  triager:
    default: true
    prompt: |
      You are the board steward for this repo. Turn ideas into well-formed Linear
      issues and decompose them into dispatchable sub-issues. PLAN — do not
      implement: emit issues, let dispatched workers build them. The one
      exception: during review or troubleshooting you MAY make direct fixes.

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
  `AT_TASK_GIT_TOKEN` (demand-only — a `command` field is a parse error; the value is
  always supplied from `~/.config/at-cove/secrets.yml`/`secrets.local.yml`);
- an `image.setup-scripts[i]` / `image.paths[i]` / `image.allowed-domains[i]` is empty (or a
  path contains a newline);
- an `image.env` key is empty, contains `=`/newline, or is a **base-owned** key; or a value
  contains a newline;
- a `workers` key looks `<reserved>` but isn't `<common>`; `<common>` sets a `prompt`; a real
  class omits `prompt`; a `timeout` isn't a positive Go duration; a `concurrency` is negative;
  or a `workers.*.allowed-domains[i]` / `collaborators.*.allowed-domains[i]` entry is empty;
- the root `secrets` declares `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` — an Anthropic
  agent bearer must be declared under `workers.<class>.secrets` (or
  `workers.<common>.secrets`) instead; see
  [at-cove-secrets.md](at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket);
- `tracker` sets more than one provider; `tracker.linear.team` is missing, `poll-interval`
  isn't a positive Go duration, a `states` entry is missing, or `secrets` doesn't declare
  exactly `AT_DISPATCH_TRACKER_TOKEN` / `AT_DISPATCH_WEBHOOK_SECRET` (demand-only — each
  value is supplied from `~/.config/at-cove/secrets.yml`/`secrets.local.yml`);
- `dispatch.concurrency` is < 1, or `reaper-timeout` / `dispatch-overhead` isn't a positive
  Go duration;
- a `collaborators` key looks `<reserved>` but isn't `<common>`; `<common>` sets a `prompt`
  or `default`; or more than one class sets `default: true`;
- any `secrets` entry (at any of the five bucket locations) sets a field other than
  `description` — most notably, a `command` under a kit secret is a hard parse error (see
  [at-cove-secrets.md](at-cove-secrets.md)).

Other fields are structurally validated only — the decoder rejects wrong shapes/types.
