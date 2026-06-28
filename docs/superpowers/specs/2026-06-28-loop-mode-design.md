# `loop` mode — scheduled, unattended agent runs

## Goal

Add an `at-cove loop` command that runs a project-defined check on a schedule
and, when the check signals work is available,
invokes Claude Code headlessly in a dedicated sandbox with a project-defined prompt.
The engine is generic:
it knows nothing about *what* the work is —
only how to check for it, run the agent, and repeat.

This is **Part 1** of a two-part effort.
Part 2 (a work-queue convention built from task files in the repo)
is deliberately out of scope here;
it will be specced separately, against the running engine.
A worked example near the end of this spec proves the generic hooks
can express the intended Part 2 use without engine changes.

## Scope

In scope:
- A `loop` command with named, independently-scheduled loops.
- Named sandbox **instances** (the interactive instance plus one per loop).
- A generic `setup` command for seeding an isolated workspace.
- Headless authentication via `ANTHROPIC_API_KEY`.

Out of scope (Part 2 or later):
- The work-queue file format, task lifecycle, and prompt conventions.
- Generalizing `create`/`connect`/`recreate` to arbitrary named instances
  (only `loop`, `destroy`, and `status` become name-aware here).
- A declarative `repo:` clone field (Appendix B of the sandboxes design) —
  the generic `setup` command covers seeding instead.

## Instance model

Today a kit has a single instance recorded in `.at-cove/.state/state.json`.
This introduces **named instances**:

- The interactive instance is unchanged (`state.json`).
- Each loop run owns its own instance with its own state file at
  `.at-cove/.state/loop-<name>.json`.
- Container and volume names gain a `-loop-<name>` suffix
  (`<base>-loop-<name>`, `<base>-loop-<name>-state`, `<base>-loop-<name>-workspace`)
  so instances never collide.
- Loop instances are **always isolated**:
  their own `/agent-data` and workspace volumes.
  `--ws` (shared host bind-mount) is rejected for loops —
  unattended automation must not mutate a developer's working tree.
- All instances are built from the **same kit image**;
  the image is built once and reused across instances.

## Command surface

- `at-cove loop [<name>] [kit-dir] [--once] [--keep] [--interval <dur>]`
  Runs the named loop (or `default` when the name is omitted).
  The first positional is the loop name;
  the kit directory is discovered from the cwd as usual,
  or given as a second positional.
- `at-cove destroy --loop <name> [kit-dir]`
  Destroys a `--keep` loop instance.
- `at-cove status [--loop <name>] [kit-dir]`
  Reports a loop instance's state;
  without `--loop`, reports the interactive instance as today.

`create`, `connect`, and `recreate` are **not** generalized to named instances
in this part — they continue to operate on the interactive instance.

## Lifecycle

`at-cove loop foo`:

1. Resolve the `foo` loop config; fail fast if it is undefined.
2. Verify `ANTHROPIC_API_KEY` is a declared, resolvable secret;
   fail fast if not (an instance that cannot run is never created).
3. Ensure the kit image is built (build once; reuse if present).
4. Create the `loop-foo` instance — its own container, volumes, and state file.
5. Run the `setup` command once to populate the workspace.
6. Enter the scheduler (continuous), or run a single drain for `--once`.
7. On exit (SIGINT/SIGTERM, or after a `--once` drain), **destroy** the instance —
   container, volumes, and state file — **unless `--keep`** was given.
   With `--keep`, a later `loop foo --keep` reuses the existing instance
   (skipping create and the initial `setup`) instead of recreating it.

A `--keep` instance left running can be inspected with `status --loop foo`
and torn down with `destroy --loop foo`.

## Locking

A running loop holds the **shared lock on its own state file** for its whole duration.
`destroy`/`recreate` targeting that instance take the exclusive lock and refuse
while the loop runs (reusing the existing shared/exclusive lock machinery,
keyed per state file).
Because each instance locks independently,
a loop runs concurrently with the interactive `connect` and with other loops
without contention.

## Configuration

A `loops:` map is added to `config.yml`:

```yaml
setup: "git clone https://github.com/aethons-tools/cove ."   # optional kit-level default

loops:
  default:
    interval: 5m            # required; --interval overrides at the command line
    check: "test -e .at-cove/queue/next"          # exit 0 => trigger
    prompt: "Work the next task in .at-cove/queue."
    setup: "git clone https://github.com/aethons-tools/cove ."   # optional; overrides kit-level
    fresh-workspace: false  # optional (default false); reset + re-run setup before each trigger
```

- `interval` is **required** per loop and is the source of truth;
  `--interval <dur>` overrides it for a single run.
- `check`, `prompt` are required per loop.
- `setup` is optional and may be set kit-level (a default for every loop
  and for the interactive instance) and/or per-loop (which overrides the default).
- `fresh-workspace` defaults to `false`.
- Decoding keeps `KnownFields(true)`:
  unknown keys are a hard error, as elsewhere in the kit schema.

## Setup / workspace seeding

The isolated workspace volume starts empty.
The `setup` command populates it (typically a `git clone`),
running in the workspace over a non-interactive ssh,
with the loop's secrets injected (so a private clone authenticates via
the existing in-VM git credential helper and `GITHUB_TOKEN`).

Cadence:
- Run **once** when a fresh instance is created (step 5 above).
- Re-run after a workspace reset when `fresh-workspace` is true (see Tick).
- **Suppressed** when the workspace is a `--ws` shared bind-mount —
  the host directory already holds the code.
  (Loops never use `--ws`, but `connect` may, hence the rule.)

`connect` integration:
`connect` runs `setup` on first use of an isolated, empty workspace,
guarded by a marker file so it does not re-run on every connect,
and suppressed for `--ws`.
This gives the interactive instance the same one-command seeding loops get.

## Headless authentication

Loop agent runs are unattended and cannot perform the interactive OAuth login.
They authenticate Claude Code via `ANTHROPIC_API_KEY`,
declared as a secret in `config.yml` and injected into the agent run env
like any other secret (memory-only, never on disk or argv).
This is API billing rather than subscription/OAuth — an explicit tradeoff for
unattended operation.
If `ANTHROPIC_API_KEY` cannot be resolved, the loop aborts at startup (lifecycle step 2).

## Scheduling — drain then poll

The scheduler separates *doing available work* from *waiting for work*:

```
drain():
  loop:
    if fresh-workspace: reset workspace, re-run setup
    run check in the workspace
    if check exited 0 (trigger):
      run the agent (claude -p <prompt>) to completion
      continue            # immediately re-check — no sleep while work remains
    else:
      return              # nothing to do

continuous:
  loop until SIGINT/SIGTERM:
    drain()
    sleep interval        # idle poll only when there was nothing to do
```

- **Continuous (default):** drain all available work back-to-back,
  then sleep `interval` and drain again — *work until idle, then poll*.
- **`--once`:** a single `drain()` then exit.
  Performs all currently-available work with no interval waiting;
  the external scheduler (cron/launchd) supplies the polling cadence.
  Its exit code is non-zero if any agent run in the drain failed.

The clock/sleep is injected so tests exercise the scheduling logic
without real waiting.

## Tick mechanics

A single check-and-maybe-run step (inside `drain`):

1. If `fresh-workspace`: clear the workspace contents in the VM and re-run `setup`.
2. Run `check` in the workspace over non-interactive ssh.
   Exit 0 → trigger; any non-zero → no trigger (a normal idle result, not an error).
3. If triggered: run `claude -p "<prompt>"` in the workspace,
   secrets (including `ANTHROPIC_API_KEY`) injected via the existing transport,
   output streamed to the loop's stdout/log; wait for completion.
4. Log the outcome: timestamp, loop name, triggered?, agent exit status.

Each agent run is an independent headless invocation
(`claude -p` is one-shot; there is no session resume between triggers).

## Keep-awake

The loop holds the `internal/awake` assertion for its whole duration,
so the host Mac does not idle-sleep between ticks and the schedule keeps firing.
Released on loop exit (alongside instance teardown).

## Error handling

- `check` non-zero — not an error; ends the current drain and polls.
- Agent run failure (`claude -p` exits non-zero) — logged as a warning;
  the loop continues to the next tick (resilient to a single bad run).
  In `--once`, it sets a non-zero final exit code.
- `setup` or instance creation failure at startup — abort the loop;
  tear down any partially-created instance.
- Transient infra failure mid-loop (ssh/dial) — logged;
  the loop continues to the next tick rather than dying.
- Missing `ANTHROPIC_API_KEY` — abort before creating an instance.

## Testing

Hermetic, following the repo's `runner.Fake` + pure-plan/execution split:

- Drain logic: triggers the agent when `check` exits 0;
  re-checks immediately after a trigger (no sleep);
  stops draining and polls when `check` is non-zero.
- `--once` runs exactly one drain and exits;
  its exit code reflects an agent run failure.
- `fresh-workspace` resets and re-runs `setup` before each trigger.
- `--interval` overrides the configured interval; the injected clock confirms timing.
- Lifecycle: create → run → destroy; `--keep` skips destroy and reuses on reconnect.
- Per-instance locking: a running loop blocks `destroy`/`recreate` of its own
  instance but not of others.
- Missing `ANTHROPIC_API_KEY` aborts before any instance is created.
- Command-string builders (check/setup/agent remote commands) unit-tested directly.

Real-ssh behavior stays behind the existing `integration` build tag.

## Part 2 sanity check (forward-looking, not built here)

The intended work-queue is expressible as pure loop config, with no engine change:

```yaml
loops:
  queue:
    interval: 2m
    setup: "git clone https://github.com/aethons-tools/cove ."
    check: "test -n \"$(ls .at-cove/queue/*.task 2>/dev/null)\""   # exit 0 when a task waits
    prompt: "Pick the oldest file in .at-cove/queue, do what it says, then remove it."
    fresh-workspace: true
```

Drain semantics process the whole backlog each time the loop wakes,
then poll every two minutes for new tasks —
exactly the Part 2 behavior, driven entirely by `check`, `prompt`, and `setup`.
This confirms the generic hooks are sufficient before we build the engine.

## Implementation plan phases (preview)

The plan will be split into independently-shippable phases:

- **Phase A — named-instance groundwork:**
  state keyed per instance, `-loop-<name>` container/volume naming,
  per-instance locking, name-aware `destroy`/`status`.
- **Phase B — `setup` workspace seeding:**
  kit-level and per-loop `setup`, the in-VM run mechanism,
  `connect` first-use integration, `--ws` suppression.
- **Phase C — the loop scheduler:**
  `loops:` config, drain-then-poll, tick (check + headless agent run),
  `--once`/`--keep`/`--interval`, keep-awake, `ANTHROPIC_API_KEY` auth.
