---
summary: The at-cove kit config.yml schema — every field an operator sets to define a sandbox and its scheduler (name, source-control, tracker, dispatch, model-provider, secrets, workers, collaborators, docker, image), with validation rules, the secret-bucket boundaries, and a full annotated example.
read_when: You are authoring or editing a kit's .at-cove/config.yml — setting the target repo (source-control), wiring the issue tracker or scheduler policy, switching the agent to Claude on Vertex, enabling docker-in-sandbox, adding a secret, a worker or collaborator class, an allowed domain, or a PATH entry.
owns: "the config.yml schema: name, source-control, tracker, dispatch, model-provider, workers, collaborators, secrets, docker, image (+ validation)"
prereqs: ../OVERVIEW.md — what at-cove is and the kit/build model; at-cove-secrets.md — secret demand + supply
tier: leaf
updated: 2026-08-08
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
*tagged union — `github` or `gitlab`, mutually exclusive*

The remote the kit targets — the **single source of truth** for the repo identity *and* the
code-host kind (which selects the clone URL, the PR/MR API, and the matching secret
supply). **Required for `at-cove work`**; interactive `chat` works without it. `at-cove
work` fills the target repo into the worker's task from `source-control`, so nothing else
names a repo. GitHub opens a **pull request**; GitLab opens a **merge request** — both
provider arms share the same `project`/`main-branch`/`secrets.AT_TASK_GIT_TOKEN` shape
below.

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

#### source-control.gitlab.host
*string, defaults to `gitlab.com`*

The GitLab instance the project lives on — a bare hostname, no scheme or path
(self-hosted supported, e.g. `gitlab.example.com`). A self-hosted `host`
**auto-widens the kit-root egress allow-list** at `install` time — no manual
[`image.allowed-domains`](#imageallowed-domains) entry needed; see
[Egress](../OVERVIEW.md#egress-three-additive-allow-lists-session-scoped). `gitlab.com`
is already in the sealed base, so the common case needs no egress change at all.

The resolved `host` is also defaulted into the interactive in-VM agent session as
**`GITLAB_HOST`** (the variable the GitLab CLI `glab` reads to pick its target
instance), so an agent running `glab` against a self-hosted GitLab needs no manual
setup — the value is set even when it is `gitlab.com` (matching `glab`'s own
default). It is a plain non-secret env value; a `GITLAB_HOST` you set explicitly in
the kit's own session env always wins and is never overwritten. This defaults the
env only — `glab` itself is not installed by at-cove. A GitHub kit sets nothing.

#### source-control.gitlab.project*
*string — `group/.../name`, at least 2 `/`-separated segments*

The GitLab project the workers act on, including any nested groups — unlike GitHub's
exactly `owner/name`, a GitLab path may have more than one leading group segment.

#### source-control.gitlab.main-branch
*string, defaults to `main`*

The repo's base branch — the same role as
[`source-control.github.main-branch`](#source-controlgithubmain-branch).

#### source-control.gitlab.secrets
*map of secret env name → config, optional in the schema*

Same shape and bucket as
[`source-control.github.secrets`](#source-controlgithubsecrets) above — the only
allowed key is `AT_TASK_GIT_TOKEN`. For GitLab, the value is a **supplied** Personal
Access Token or Project Access Token with `api` + `write_repository` scope: unlike
GitHub's per-run minted App installation token, GitLab has no minting primitive yet
(**token minting is a tracked follow-up, COV-79**) — v1 is supplied-only, resolved the
same demand/supply way as any other kit secret. The token doubles as both the git
askpass credential and the GitLab Merge-Request-API bearer.

```yaml
source-control:
  gitlab:
    host: gitlab.com                 # optional; default gitlab.com; self-hosted supported
    project: group/subgroup/name     # >= 2 segments; nested groups allowed
    main-branch: main
    secrets:
      AT_TASK_GIT_TOKEN:
        description: GitLab PAT / Project Access Token — api + write_repository scope
```

`source-control.github` and `source-control.gitlab` are **mutually exclusive** — a kit
declares at most one.

### tracker
*tagged union — one provider (`linear` or `github`)*

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

#### tracker.github.repo
*string (`owner/name`), defaults to `source-control.github.project`*

The GitHub repo whose Issues the scheduler drives. Omit it to inherit
`source-control.github.project`; set it only to point the tracker at a different
repo than the one the kit targets for code. Required (as an override) when the kit
declares no `source-control.github`.

#### tracker.github.poll-interval*
*string (Go duration)*

How often the scheduler polls GitHub for issue state changes. As with Linear, keep
it low-frequency (e.g. `60s`).

#### tracker.github.class-label-prefix
*string, defaults to `class:`*

The label prefix that maps a GitHub issue to a worker class (e.g. `class:implement`).
Must be non-empty if provided.

#### tracker.github.states*
*map of the scheduler's five **non-terminal** lifecycle roles → that repo's real label names*

`ready`, `in-progress`, `in-review`, `needs-input`, `blocked` — all five required.
Unlike Linear, `done` is **not** a role and is ignored: on GitHub, *Done means the
issue is closed*, so there is no state label for it. These bind the design's
lifecycle roles to whatever labels the repo actually uses.

When the scheduler transitions an issue it swaps status labels so an issue never
carries two at once — the target role's label is added and the sibling `status:*`
labels are removed — and for *done* it **closes** the issue instead. Both are
idempotent (closing a closed issue, or re-adding a label already present, is a
no-op), so a redundant transition is harmless.

#### tracker.github.secrets
*map of secret env name → config*

Host-side, scheduler-only — never injected into a sandbox VM (see
[Secret buckets](#secret-buckets)). Demands exactly `AT_DISPATCH_TRACKER_TOKEN`,
demand-only (no `command`) — the value is supplied from the user's
`~/.config/at-cove/secrets.yml`/`secrets.local.yml` (see
[at-cove-secrets.md](at-cove-secrets.md)). Any other key (including Linear's
`AT_DISPATCH_WEBHOOK_SECRET`) is rejected.

```yaml
source-control:
  github:
    project: acme/myrepo   # tracker.github.repo inherits this
tracker:
  github:
    poll-interval: 60s
    states:
      ready: Todo
      in-progress: In Progress
      in-review: In Review
      needs-input: Needs Input
      blocked: Blocked
    secrets:
      AT_DISPATCH_TRACKER_TOKEN: { description: "GitHub API token for the scheduler" }
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

### model-provider
*tagged union — one provider (`vertex` only today), optional*

Switches the sandbox's agent from first-party Anthropic (the default, absent this
block) to a third-party-hosted Claude. **Kit-global** — one setting for the whole
kit, not yet per-collaborator/per-worker-class — and **`chat`-only**: `at-cove
work`/`dispatch` do not yet read this block (a documented follow-up; see the
[design spec](../superpowers/specs/2026-07-21-vertex-model-provider-design.md)).
Absent block → today's Anthropic OAuth/bearer behavior, unchanged.

#### model-provider.vertex.env*
*map of string → string*

Non-secret, kit-authored configuration for **Claude on Google Vertex AI**, passed
through as env for the `chat` session. Two keys are **required** (a missing one is
a hard config error):

| Key | Meaning |
|---|---|
| `ANTHROPIC_VERTEX_PROJECT_ID` | the GCP project Vertex bills/governs through |
| `CLOUD_ML_REGION` | a specific region, or the multi-region `us`/`eu`, or `global` |

at-cove itself **sets `CLAUDE_CODE_USE_VERTEX=1`** — implied by choosing `vertex`,
so the kit must not (and need not) set it. Any other key
(`ANTHROPIC_VERTEX_BASE_URL`, a `VERTEX_REGION_CLAUDE_*` override, `ANTHROPIC_MODEL`,
…) passes straight through to Claude Code with no schema change required.

**Hardening denylist (load-bearing).** Unlike a *secret* — which a kit only
*demands* by name, with the host as the supply-side gate — this `env` map is
**kit-authored with no host-side gate**, so a committed-but-untrusted kit could
otherwise inject security-relevant env directly. A **protected set** is therefore
rejected at config validation (a hard parse error) and independently re-checked
(defensive drop) at injection:

- the egress proxy vars — `http_proxy`/`https_proxy`/`no_proxy` and their uppercase
  forms — because the per-session env-file is *sourced* in the session shell, so an
  unchecked value would **shadow** the sealed `/etc/environment` proxy vars the
  hardening layer writes last, quietly defeating egress;
- `CLAUDE_CONFIG_DIR` (sealed-owned);
- `GOOGLE_APPLICATION_CREDENTIALS` (at-cove-owned — it points at the seeded GCP ADC
  file; see [Authentication](../OVERVIEW.md#authentication-claude-on-vertex));
- `PATH`.

This is the `env`-block analog of the egress rule "additive, sealed-wins": a kit
can *configure* the provider but can never shadow a sealed-owned or
security-relevant variable.

**Egress is auto-derived, not hand-listed.** When `model-provider.vertex` is
present, `install` widens the kit-root allow-list with the GCP hosts Vertex needs
— derived from the block, not hand-maintained — see
[Egress](../OVERVIEW.md#egress-three-additive-allow-lists-session-scoped).

```yaml
model-provider:
  vertex:
    env:
      ANTHROPIC_VERTEX_PROJECT_ID: my-gcp-project   # required
      CLOUD_ML_REGION: us                           # required
      # CLAUDE_CODE_USE_VERTEX=1 is set by at-cove — implied by the vertex block
```

The credential itself (a GCP ADC) is **not** part of this block — it is supplied
host-side and seeded as a file; see
[Authentication](../OVERVIEW.md#authentication-claude-on-vertex) and the
[`GOOGLE_APPLICATION_CREDENTIALS_JSON` demand](at-cove-secrets.md#the-vertex-credential-demand-google_application_credentials_json).

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
*int > 0, optional, inherited from `<common>` if unset*

Max concurrent runs of this class. **Unset** (the field omitted) inherits
`<common>`; an omitted value everywhere means no per-class cap (the class is
bounded only by `dispatch.concurrency`). An **explicit `0` is rejected** — it is
indistinguishable from unset only by intent, and would *remove* the per-class cap
rather than pause the class (the opposite of the likely goal); to pause a class,
manage it through tracker state, not `concurrency: 0` (COV-87).

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

#### collaborators.*class*.shadow-dirs
*list of strings, optional, own-only (not inherited); `<common>` must not set it;
requires `share-repo-dir: true`*

Workspace-relative directories to overmount with a persistent **per-sandbox**
volume, so transient or platform-specific content (`.venv`, `node_modules`,
`target`) doesn't collide across the [shared](#collaboratorsclassshare-repo-dir)
host↔VM bind. **Per-class only** — rejected on `<common>` — and meaningful only
when `share-repo-dir: true`; declaring it without a Shared workspace is a hard
config error. Each entry must be a clean relative path inside the workspace: an
absolute path or a `..`-escaping path is rejected, as is a duplicate entry or two
entries that collide once sanitized to the same volume name. The overmount
volumes are **per-sandbox and persistent** — they survive `recreate` and are
removed only by `destroy`, same lifecycle as the other instance volumes. A
forthcoming `at-cove doctor` (COV-131) will recommend a kit's `shadow-dirs` list
from the repo's own ignore rules.

```yaml
collaborators:
  human:
    share-repo-dir: true
    shadow-dirs: [.venv, node_modules]
```

#### collaborators.*class*.secrets
*map of secret env name → config, optional, inherited from `<common>` (own key wins)*

Same declaration shape as the root `secrets`, but a distinct bucket (see
[Secret buckets](#secret-buckets)). Typically just `GITHUB_TOKEN` — see above.
This is a **chat-only** bucket, so an Anthropic agent bearer
(`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`) is rejected here for the same reason it
is at the root — a bearer in a `chat` session outranks the subscription login and
disables its connectors; it belongs under `workers.*.secrets`.

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

### docker
*bool, defaults to `false`*

Opts the kit into **docker-in-sandbox** — a working Docker *inside* the sandbox for
testing workloads (`docker build` / `docker compose up` / **testcontainers**) —
via the **Sysbox** runtime, without weakening the sandbox's hardening (COV-117).
When `true`, the colima backend runs the sandbox container itself under
`--runtime=sysbox-runc`, sets `COVE_DOCKER=1`, and mounts a persistent
`/var/lib/docker` cache volume (`atcove-{kit}[-{class}]-docker`, removed on
`destroy`). It never adds `--privileged`, a host docker-socket mount, `--device`,
or `--security-opt` — Sysbox provides the isolation host-side, so a normal rootful
`dockerd` runs inside an **unprivileged** container. Default `false` leaves the run
argv **byte-for-byte unchanged** (no runtime, no env, no volume).

**Prerequisite (detect-only, at-cove does not install):** the colima VM's docker
daemon must register the `sysbox-runc` runtime. When `docker: true` and it is
absent, `at-cove` fails fast with an actionable message — install Sysbox CE in the
colima Lima VM and make it persist across `colima stop/start` (a colima provision
hook). See [docker-in-sandbox.md](docker-in-sandbox.md) for the copy-paste install
steps, registry allow-list recipes, and nested-container egress; the
[Sysbox docker-in-sandbox design](../superpowers/specs/2026-08-08-sysbox-docker-in-sandbox-design.md)
§C/§H covers the mechanism.

```yaml
docker: true   # this kit's sandboxes get a Sysbox-backed Docker for testing
```

### image
The image at-cove hardens, plus the kit's additive egress. Build-time customization
lives in the kit's **`image/Dockerfile`** (COV-34); `config.yml` carries `base`,
`allowed-domains`, and `dns`.

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

#### image.dns
*list of strings (IP addresses), optional*

Pins the sandbox container's DNS **resolver IPs** (`docker run --dns`, one per entry).
Each entry must be a valid IP address — a hostname is rejected, since `docker --dns`
takes IPs only. **Empty (the default) emits no `--dns` flag**, so the container inherits
Docker's default resolver — which on the colima backend chains to the Lima VM / host
resolver.

Leave it unset unless the sandbox must resolve a name the host's resolver can't reach.
The one case that needs it: a resolver that only answers on a **split-DNS VPN** *and*
whose queries Docker would otherwise send to a public resolver. Concretely, a
**self-hosted `source-control.gitlab.host`** reachable only through a corporate VPN
(e.g. GlobalProtect): allow-listing the host lets squid *try* it, but the container
still can't **resolve** it unless it asks the internal resolver. If a non-shared
(isolated-workspace) `chat` fails to clone with a 502/503 from within the proxy CONNECT
while the same clone works from your own shell, set `image.dns` to the internal resolver
IP (or leave it unset if the VM resolver already answers — the default inherits it):

```yaml
image:
  dns:
    - 10.0.0.53   # internal resolver that answers for the self-hosted GitLab host
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
| `source-control.{github,gitlab}.secrets` | host, resolved fresh per git step (minted for GitHub; supplied for GitLab — see [gitlab secrets](#source-controlgitlabsecrets)) | — | injected (git steps only) | `at-task prepare`/`complete` only |
| `tracker.{linear,github}.secrets` | host, scheduler-only | — | — (never reaches a VM) | `at-cove dispatch` (a later plan) |

Every bucket is **demand-only** in the kit — a name plus a `description`. The supply
mechanics (the two host files, the four sources, precedence, the anti-mining invariant,
fail-closed behavior) are the same across all five buckets and documented once, in
[at-cove-secrets.md](at-cove-secrets.md) — this table only draws the boundaries between them.
In particular, an Anthropic agent bearer (`ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY`) must
live in the `workers.*.secrets` row and is rejected in **every other** row — see
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
- `source-control` sets more than one provider (`github` **and** `gitlab`);
  `source-control.github.project` is not `owner/name`; or `source-control.gitlab.project`
  has fewer than 2 `/`-separated segments;
- `source-control.{github,gitlab}.secrets` is non-empty but doesn't declare exactly
  `AT_TASK_GIT_TOKEN` (demand-only — a `command` field is a parse error; the value is
  always supplied from `~/.config/at-cove/secrets.yml`/`secrets.local.yml`);
- an `image.setup-scripts[i]` / `image.paths[i]` / `image.allowed-domains[i]` is empty (or a
  path contains a newline);
- an `image.dns[i]` is empty or is not a valid IP address (`docker --dns` requires an IP,
  not a hostname);
- `docker` is set to a non-bool value (it is a plain boolean, default `false`);
- an `image.env` key is empty, contains `=`/newline, or is a **base-owned** key; or a value
  contains a newline;
- a `workers` key looks `<reserved>` but isn't `<common>`; `<common>` sets a `prompt`; a real
  class omits `prompt`; a `timeout` isn't a positive Go duration; a `concurrency` is negative
  or an explicit `0` (omit it to inherit `<common>`; pause a class via tracker state);
  or a `workers.*.allowed-domains[i]` / `collaborators.*.allowed-domains[i]` entry is empty;
- any **non-worker** bucket (`secrets` root, `collaborators.*.secrets`,
  `source-control.{github,gitlab}.secrets`, `tracker.linear.secrets`) declares
  `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY` — an Anthropic agent bearer is legitimate
  only under `workers.<class>.secrets` (or `workers.<common>.secrets`); anywhere else it
  is injected into a `chat`/session env where it outranks the subscription login and
  disables connectors; see
  [at-cove-secrets.md](at-cove-secrets.md#migrating-the-worker-bearer-off-the-root-bucket);
- a `secrets` name (map key) is empty at **any** of the five bucket locations;
- `tracker` sets zero or more than one provider (exactly one of `linear` / `github`);
- `tracker.linear.team` is missing, `poll-interval` isn't a positive Go duration, a `states`
  entry is missing, or `secrets` doesn't declare exactly `AT_DISPATCH_TRACKER_TOKEN` /
  `AT_DISPATCH_WEBHOOK_SECRET` (demand-only — each value is supplied from
  `~/.config/at-cove/secrets.yml`/`secrets.local.yml`);
- `tracker.github.repo` is neither set nor inherited from `source-control.github` (or is set
  but not `owner/name`), `poll-interval` isn't a positive Go duration, a `class-label-prefix`
  is provided but empty, one of the five non-terminal `states` (`ready`, `in-progress`,
  `in-review`, `needs-input`, `blocked` — `done` is ignored) is missing, or `secrets` doesn't
  declare exactly `AT_DISPATCH_TRACKER_TOKEN` (demand-only);
- `dispatch.concurrency` is < 1, or `reaper-timeout` / `dispatch-overhead` isn't a positive
  Go duration;
- `model-provider` sets more than one provider; `model-provider.vertex.env` is missing
  `ANTHROPIC_VERTEX_PROJECT_ID` or `CLOUD_ML_REGION`; or it sets a protected key
  (an egress proxy var, `CLAUDE_CONFIG_DIR`, `GOOGLE_APPLICATION_CREDENTIALS`, or
  `PATH`) — see [model-provider](#model-provider);
- a `collaborators` key looks `<reserved>` but isn't `<common>`; `<common>` sets a `prompt`,
  `default`, `share-repo-dir`, or `shadow-dirs`; or more than one class sets `default: true`;
- a `collaborators.*.shadow-dirs` entry is set without that class's `share-repo-dir: true`, is
  empty, absolute, or escapes the workspace via `..`; or two entries duplicate or collide once
  sanitized to the same volume name (see
  [collaborators.*class*.shadow-dirs](#collaboratorsclassshadow-dirs));
- any `secrets` entry (at any of the five bucket locations) sets a field other than
  `description` — most notably, a `command` under a kit secret is a hard parse error (see
  [at-cove-secrets.md](at-cove-secrets.md)).

Other fields are structurally validated only — the decoder rejects wrong shapes/types.
