# at-dispatch — configuration — Design

**Date:** 2026-07-06
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binary `at-dispatch`)
**Tracks:** [AET-20](https://linear.app/aethons-tools/issue/AET-20) (child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic)
**Related:** [orchestration design](../../orchestration/INDEX.md) — the workflow and the at-cove dispatch interface this configures a consumer of.

## 1. Purpose

Define how the **`at-dispatch`** binary is configured — the config file format, its
schema, secret resolution, validation/loading, and the **dispatch-command contract**
the config plugs into. This is the schema every later piece of the dispatcher
(scheduler, webhook receiver, reaper) keys off, so it is designed first.

`at-dispatch` today is a skeleton (`serve` is a stub, see `cmd/at-dispatch/`). This
spec is the design for its configuration layer only.

## 2. Governing decisions

Settled during brainstorming:

- **at-dispatch is at-cove-agnostic.** It knows nothing about images, digests, or
  `at-cove run` flags. Each **handler class** maps to a **dispatch command** (an
  argv); running that command *is* how a task is dispatched. The command is the only
  seam — it is what shells out to `at-cove run` (or any wrapper). This mirrors
  at-cove's own philosophy: a secret is a command whose stdout is the value; here a
  dispatch is a command that runs the task.
- **Single repo per instance.** One `at-dispatch` process serves one repo and one
  tracker team; the schema is flat. Scale out by running N instances. A future
  multi-repo `repos:` list is deferred and the flat form is forward-compatible with
  it.
- **Mirror at-cove's config conventions:** YAML, **strict decoding** (unknown keys
  rejected), fail-closed, and **secrets as resolver commands** (argv whose stdout is
  the value, resolved on the host, held in memory only — never on disk or argv).
  Reuse `internal/secret`.
- **Two standing secrets only.** at-dispatch holds the **tracker API token** and the
  **webhook signing secret**. Everything else — notably code-host token minting —
  lives behind the dispatch command (the at-cove side), keeping the three authorities
  separated.
- **Config path is explicit:** `at-dispatch serve --config <path>`. No env-var
  fallback, no cwd walk-up discovery (it is a daemon, not repo-local).

**Non-goals (out of scope for this spec):** the scheduler loop, the webhook server,
the queue, the Linear API client, the reaper *behavior* (AET-25/23/27 consume this
config but are separate); multi-repo; and the `dispatch/*.sh` command scripts
themselves (operator/at-cove-kit concern).

## 3. Config file and loading

- One YAML file, passed as `at-dispatch serve --config <path>` (required).
- Loaded into a `dispatch.Config`, **strict-decoded** (`yaml.v3` `KnownFields(true)`,
  as at-cove's kit loader does — recursively rejects typos/unknown keys).
- `Validate()` runs after decode (§6).
- **Standing secrets** (`tracker.token`, `tracker.webhook-secret`) are resolved
  **once at startup**; any resolver failure aborts before serving (fail-closed).
  Periodic re-resolution/refresh of a rotating token is a runtime concern, deferred
  to the scheduler (AET-25).
- **Per-command secrets** (`secrets:`, §4) are resolved **per dispatch** (fresh for
  each task) so a rotated value is never stale, matching at-cove's per-connect
  resolution.

## 4. Schema

Anchored by a complete example, then field notes.

```yaml
tracker:
  provider: linear                 # only "linear" supported today
  team: AET
  token:          { command: ["op","read","op://work/linear-token"] }
  webhook-secret: { command: ["op","read","op://work/linear-webhook"] }
  poll-interval: 60s               # backstop reconcile cadence (Go duration)
  states:                          # map lifecycle ROLES → this team's real state names
    ready:        Todo
    in-progress:  In Progress
    in-review:    In Review
    done:         Done
    needs-input:  Needs Input
    blocked:      Backlog
  class-label-prefix: "class:"     # an issue's class is read from a label, e.g. class:implement

repo:
  slug: aethons-tools/cove

secrets:                           # optional; each injected as env into every dispatch command
  - name: SOME_TOKEN
    command: ["op","read","op://work/x"]

classes:
  implement: { mode: autonomous, command: ["./dispatch/implement.sh"], timeout: 30m, concurrency: 2 }
  plan:      { mode: autonomous, command: ["./dispatch/plan.sh"],      timeout: 15m }
  spec:      { mode: interactive }     # no command → assign + notify a human
  review:    { mode: interactive }

concurrency: 4                     # global cap on in-flight autonomous dispatches
reaper-timeout: 45m                # IN PROGRESS with no progress this long → NEEDS INPUT
```

**Field notes:**

- `tracker.provider` — enum; `linear` is the only supported value now.
- `tracker.token` / `tracker.webhook-secret` — a **secret ref**: `{ command: [argv] }`.
  These are at-dispatch's two standing authorities.
- `tracker.states` — binds the design's lifecycle **roles** to whatever this team
  actually named its workflow columns. All six roles are required (no guessing);
  this is what lets the design run on a stock Linear team whose states are
  `Todo`/`Backlog`/… rather than custom `READY`/`BLOCKED` names.
- `tracker.class-label-prefix` — how a class is read off an issue: the label whose
  name starts with this prefix, suffix = the class key. Default `class:`.
- `repo.slug` — `owner/name`; passed to commands as context; the single repo this
  instance serves.
- `secrets:` — optional operator-declared secrets, each `{ name, command }`, injected
  as env vars into *every* dispatch command (§5). Names must not collide with the
  reserved `DISPATCH_*` names.
- `classes:` — keyed by class name (must equal the label suffix). Each class:
  - `mode` — `autonomous` | `interactive`.
  - `command` — argv; **required iff `autonomous`**, **forbidden iff `interactive`**.
  - `timeout` — Go duration; the per-task wall-clock cap (autonomous only). Passed to
    the command as `DISPATCH_TIMEOUT`.
  - `concurrency` — optional per-class in-flight cap (autonomous only).
- `concurrency` — global cap on simultaneous autonomous dispatches (≥ 1).
- `reaper-timeout` — Go duration; consumed by the reaper (AET-27).

## 5. The dispatch-command contract

The contract every `classes.<name>.command` plugs into. at-dispatch owns it.

**Per ready autonomous task, at-dispatch:**
1. Assembles a **markdown brief** (issue title/description + linked spec/plan + comment
   thread) and writes it to a temp file.
2. Resolves the per-command `secrets:` freshly.
3. Runs the class's `command` argv with this environment:

| Env var | Value |
|---|---|
| `DISPATCH_ISSUE` | issue identifier, e.g. `AET-42` |
| `DISPATCH_CLASS` | class name, e.g. `implement` |
| `DISPATCH_REPO` | `repo.slug`, e.g. `aethons-tools/cove` |
| `DISPATCH_TIMEOUT` | the class `timeout`, e.g. `30m` |
| `DISPATCH_BRIEF` | absolute path to the markdown brief file |
| `DISPATCH_RESULT` | absolute path where the command must write `result.json` |
| *(each `secrets[].name`)* | its resolved value |

4. Enforces the class `timeout` as a hard wall-clock cap on the command (the kill
   mechanism itself lives in the scheduler, AET-25; this spec fixes the value and its
   `DISPATCH_TIMEOUT` passthrough so the command can align at-cove's own
   `--timeout`/token TTL). On exit, reads and validates `result.json` at
   `DISPATCH_RESULT`: **a present, valid file is authoritative**; an **absent or
   unparseable** result is treated as `status: error` regardless of exit code, with
   the exit code recorded for diagnostics.

**`result.json`** (the command must produce; same shape as the worker result in the
[at-cove dispatch interface](../../orchestration/at-cove-dispatch-interface.md#worker-contract) —
kept identical, cross-linked at implementation time so there is one source of truth):

```json
{
  "status": "ok | needs_input | error",
  "artifacts": { "branch": "…", "prUrl": "…", "docPath": "…" },
  "needsInput": { "doing": "…", "blocker": "…", "need": "…", "tried": "…", "safeState": "…" },
  "summary": "one-paragraph human-readable outcome",
  "usage": { "tokens": 0, "wallMs": 0 }
}
```

`needsInput` is present iff `status == needs_input`. at-dispatch does no tracker I/O
in *this* layer — reading the result is the config layer's job; brokering the tracker
write is the scheduler's (AET-25).

**Interactive classes** have no command: at-dispatch assigns + notifies a human
(runtime behavior, AET-25). Their config carries only `mode: interactive`.

## 6. Validation rules

`Validate()` rejects, with a clear message, any of:

- `tracker.provider` != `linear`; missing `tracker.team`, `token`, or `webhook-secret`.
- Any of the six `states` roles missing or empty.
- `poll-interval`, `reaper-timeout`, or any class `timeout` not parseable by
  `time.ParseDuration`.
- `repo.slug` missing or not `owner/name`.
- `classes` empty; any class key empty; `mode` not in {`autonomous`,`interactive`}.
- `autonomous` class with empty `command`; `interactive` class with a `command` set.
- Negative `concurrency` (global or per-class); global `concurrency` < 1.
- Duplicate or empty `secrets[].name`; a `secrets[].name` that collides with a
  reserved `DISPATCH_*` name; empty `secrets[].command`.
- `class-label-prefix` empty (after defaulting to `class:`).

Validation is pure (no I/O) and runs before any secret resolution.

## 7. Go design

New package `internal/dispatch/config` (or `config.go` under `internal/dispatch`):

```go
type Config struct {
    Tracker       TrackerConfig    `yaml:"tracker"`
    Repo          RepoConfig       `yaml:"repo"`
    Secrets       []Secret         `yaml:"secrets"`
    Classes       map[string]Class `yaml:"classes"`
    Concurrency   int              `yaml:"concurrency"`
    ReaperTimeout string           `yaml:"reaper-timeout"`
}
type TrackerConfig struct {
    Provider, Team, PollInterval, ClassLabelPrefix string
    Token, WebhookSecret SecretRef
    States StateMap
}
type StateMap struct { Ready, InProgress, InReview, Done, NeedsInput, Blocked string } // yaml: ready, in-progress, …
type RepoConfig  struct { Slug string }
type SecretRef   struct { Command []string }
type Secret      struct { Name string; Command []string }
type Class       struct { Mode string; Command []string; Timeout string; Concurrency int }
```
(Names indicative; finalized in the plan.) Public surface:

- `Load(path string) (*Config, error)` — read + strict-decode + `Validate()`.
- `(*Config) Validate() error` — §6, pure.
- A helper that builds the `DISPATCH_*` env slice for a task (pure, unit-testable),
  given the task fields + brief/result paths + resolved secrets.

Secret resolution reuses `internal/secret`; the resolver is injected (a func) so tests
never spawn processes.

## 8. Testing

Hermetic, table-driven:

- **Valid fixtures** decode to the expected `Config` (round-trip field checks).
- **One invalid fixture per rule in §6** → the specific validation error.
- **Strict decoding**: an unknown key anywhere is rejected.
- **Env builder**: given a task, produces exactly the `DISPATCH_*` map (+ injected
  secret names), with a fake resolver — no real commands run.
- **`result.json` parsing**: valid, `needs_input`, malformed, and missing-file
  (exit-code fallback) cases.

## 9. Open questions

- **Standing-secret refresh.** Whether the long-running daemon periodically
  re-resolves the tracker token (rotation). Deferred to the scheduler runtime
  (AET-25); the config layer resolves once at startup.
- **Literal-value secrets file.** at-cove has a user `secrets.yml` supplying literal
  values; at-dispatch uses resolver commands only for now (a command like
  `["cat","/run/secrets/x"]` covers the same need). Add a file layer later only if
  wanted.
