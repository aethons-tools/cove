---
summary: at-dispatch scheduler configuration schema — tracker wiring, repo metadata, handler-class dispatch kits, concurrency/timeout settings, and how the scheduler reads and mutates the config at startup and runtime.
read_when: You are setting up a new at-dispatch instance, adding a handler class, changing tracker state mappings, adjusting timeouts/concurrency, or wiring a new kit to a class.
owns: the at-dispatch configuration file format, schema, secret resolution, loading/validation, tracker state role mapping, class-to-kit binding, and concurrency/timeout policy
prereqs: linear-agent-workflow.md (the scheduler's role in dispatch); at-cove-dispatch-interface.md (the kit and run-parameter model the config keys into)
tier: leaf
updated: 2026-07-08
---

# at-dispatch Scheduler Configuration

## Purpose

An `at-dispatch` instance is configured with a single YAML file passed at startup:
```bash
at-dispatch serve --config /path/to/at-dispatch.yml
```

This document defines the schema, the meaning of each field, how secrets are resolved, validation/loading, and the runtime implications.

## Schema by example

```yaml
# ──────────────────────────────────────────────────────────────────────────
# TRACKER WIRING
# ──────────────────────────────────────────────────────────────────────────
tracker:
  provider: linear                          # only "linear" supported today
  team: AET                                 # Linear team key this scheduler serves
  
  # Secrets: commands run on the host; stdout (trimmed) is the value, held in memory only
  token:          { command: ["op", "read", "op://work/linear-token"] }
  webhook-secret: { command: ["op", "read", "op://work/linear-webhook"] }
  
  # Reconciliation and event-handling cadence
  poll-interval: 60s                        # backstop poll period (Go duration; must be > 0)
  
  # Bind this team's real state names to the design's uniform lifecycle roles
  states:
    ready:        Todo
    in-progress:  In Progress
    in-review:    In Review
    done:         Done
    needs-input:  Needs Input
    blocked:      Backlog
  
  # Issues are assigned to classes via labels; this prefix + class name forms the label
  class-label-prefix: "class:"              # so an issue labeled "class:implement" maps to the "implement" class

# ──────────────────────────────────────────────────────────────────────────
# REPO METADATA
# ──────────────────────────────────────────────────────────────────────────
repo:
  slug: owner/repo                          # "owner/repo" format; the single repo this scheduler serves
  source-branch: main                       # base branch that work builds on (e.g. "main" or "develop")

# ──────────────────────────────────────────────────────────────────────────
# HANDLER CLASSES — each autonomous class maps to a kit
# ──────────────────────────────────────────────────────────────────────────
classes:
  # Autonomous class: runs a LLM agent in a sandboxed at-cove container
  implement:
    mode: autonomous                        # "autonomous" | "interactive"
    kit: ./kits/implement                   # path to the .at-cove kit; relative paths resolve against the config file's directory
    timeout: 30m                            # hard wall-clock cap for this class (Go duration; must be > 0)
    concurrency: 2                          # per-class limit on concurrent in-flight instances (optional; 0 means use global)
  
  plan:
    mode: autonomous
    kit: ./kits/plan
    timeout: 15m
    # concurrency: omitted; uses global setting
  
  # Interactive class: no kit; the scheduler assigns to a human
  spec:
    mode: interactive                       # humans drive this via a chat session
    # kit: not set for interactive classes
    # timeout: not needed; interactive work is human-paced
  
  review:
    mode: interactive

# ──────────────────────────────────────────────────────────────────────────
# CONCURRENCY & TIMEOUTS — global and per-class limits
# ──────────────────────────────────────────────────────────────────────────
concurrency: 4                              # global cap on in-flight autonomous dispatches across all classes

reaper-timeout: 45m                         # if an issue stays IN PROGRESS with no progress this long, move to NEEDS INPUT

dispatch-overhead: 15m                      # build + boot + teardown margin added to each class's timeout
                                            # (Go duration; default: 15m)
                                            # A class with timeout 30m will actually get 30m + 15m = 45m wall-clock

```

## Field details

### `tracker`

**`provider`** (`string`, required)
- The tracking system. Only `"linear"` is supported.

**`team`** (`string`, required)
- The Linear team key this scheduler instance serves. Each team has its own scheduler instance.

**`token`** (`{ command: [...] }`, required)
- A resolver command run on the host at startup; its stdout (trimmed) is the Linear API token.
- Failure to resolve aborts startup (fail-closed).
- Secrets are held in memory only; never written to files or argv.

**`webhook-secret`** (`{ command: [...] }`, required)
- A resolver command run at startup; its stdout is the webhook signature secret used to verify incoming events.
- Failure aborts startup (fail-closed).

**`poll-interval`** (`duration`, required)
- Reconcile cadence. The scheduler polls the tracker for state changes (as a backstop to webhooks) at this interval.
- Must be a positive Go duration (e.g., `60s`, `5m`).

**`states`** (`{ ready, in-progress, in-review, done, needs-input, blocked }`, required)
- Maps the design's uniform lifecycle roles to this team's real state names.
- All six roles must be supplied and non-empty.
- Example: `ready: "Todo"` means issues in the team's "Todo" state are treated as `ready` by the scheduler.

**`class-label-prefix`** (`string`, optional; default: `"class:"`)
- The prefix for class labels on issues.
- An issue labeled `"class:implement"` is assigned to the handler class `"implement"`.

### `repo`

**`slug`** (`string`, required; format: `"owner/repo"`)
- The single repository this scheduler serves.
- Must be exactly two slash-separated parts, both non-empty.

**`source-branch`** (`string`, required)
- The base branch for work (e.g., `"main"`, `"develop"`).
- Passed to the kit when the scheduler dispatches a worker, so the kit's checkout is against this branch.

### `classes`

A map of handler class names to their dispatch modes and kits.

**`mode`** (`string`, required per class)
- `"autonomous"` — the scheduler dispatches a one-shot LLM agent into a hardened at-cove container, pointing the kit at the issue's brief.
- `"interactive"` — no dispatch; the scheduler assigns the issue to a human, who drives it in a chat session.

**`kit`** (`string`, required for `autonomous`, forbidden for `interactive`)
- Path to the `.at-cove` kit directory for this class.
- Relative paths resolve against the config file's directory at load time.
- The kit is passed to `at-cove dispatch` by the scheduler and must contain a valid `.at-cove/config.yml`.
- The kit is the trust boundary: it defines the container image, egress allowlist, and receives the worker's task via injected `input.json`.

**`timeout`** (`duration`, required for `autonomous`, not used for `interactive`)
- The hard wall-clock cap for an instance of this class.
- Must be a positive Go duration (e.g., `30m`, `2h`).
- The actual timeout passed to `at-cove dispatch` is `timeout + dispatch-overhead` (the overhead is added by the scheduler).

**`concurrency`** (`int`, optional per class; default: 0, meaning use global `concurrency`)
- Per-class limit on concurrent in-flight instances.
- Set to 0 or omit to use the global `concurrency` setting.
- Must be ≥ 0.

### Top-level settings

**`concurrency`** (`int`, required; must be ≥ 1)
- Global cap on concurrent autonomous dispatches across all classes.
- Per-class limits (if set) further restrict their slice of this budget.

**`reaper-timeout`** (`duration`, required; must be > 0)
- The scheduler's stale-claim reaper moves any issue stuck in `IN PROGRESS` past this timeout to `NEEDS INPUT`.
- Must be a positive Go duration (e.g., `45m`, `1h`).

**`dispatch-overhead`** (`duration`, optional; default: `15m`; must be > 0 if set)
- Build + boot + teardown margin added to every class's `timeout`.
- A class with `timeout: 30m` will actually get `30m + dispatch-overhead` wall-clock before the scheduler considers the run stale.
- Must be a positive Go duration if explicitly set.

## Loading and resolution

1. **Parse** — the YAML is strictly decoded (`KnownFields(true)`); unknown keys are rejected.
2. **Resolve secrets** — `tracker.token` and `tracker.webhook-secret` commands are run on the host; failure aborts startup.
3. **Resolve kit paths** — relative paths in `classes[*].kit` are resolved against the config file's directory.
4. **Apply defaults** — `class-label-prefix` defaults to `"class:"`, `dispatch-overhead` defaults to `"15m"`.
5. **Validate** — all required fields are checked, durations are parsed and must be positive, modes are validated, rules (e.g., autonomous classes must have a kit) are enforced.

## Dispatch flow (from the scheduler's perspective)

For each `READY` issue with a class label:

1. Read the issue's class from the label (using `tracker.class-label-prefix`).
2. Look up the class in the config.
3. If `mode: interactive` — assign and notify the human; exit.
4. If `mode: autonomous`:
   - Compute the wall-clock timeout: `timeout + dispatch-overhead`.
   - Load the kit from the path in `classes[class].kit`.
   - Run `at-cove dispatch <kit> --in input.json --out output.json --timeout <timeout>`.
   - Read the worker's `output.json`.
   - Map the result's `status` (`OK` / `NEEDS_INPUT` / `ERROR`) to tracker state transitions.
   - Update the issue (post artifacts, move state, assign humans if needed).

The scheduler never holds a code-host token; secrets are confined to the kit (for secret injection) or the minter process (for per-task token generation via run-parameter passthrough).

## Per-project customization

- **Handler classes.** Add new classes as needed; each maps to a `mode` and (for autonomous) a kit.
- **Concurrency budget.** Tune `concurrency` and per-class limits based on your resource budget and SLA.
- **Timeouts.** Adjust `dispatch-overhead` based on your image's build/boot time; adjust per-class `timeout` based on expected work duration.
- **Egress policy.** The kit owns the egress allowlist via its `.at-cove/config.yml`; workers can only reach domains listed there.
- **Secrets.** Add kit-specific secrets in the kit's `.at-cove/config.yml` (e.g., a code-host token minter command using run-parameter passthrough); top-level secrets are not supported (the kit is the trust boundary).
