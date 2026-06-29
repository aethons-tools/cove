# Part 2 — Work-Queue Conventions (TODO)

**Status:** not started.
Part 1 (the generic `at-cove loop` engine) is complete and merged (loop mode phases A, B, C-1..C-7).
Part 2 layers a work-queue convention on top —
mostly prompts/rules and repo conventions,
likely little or no new `at-cove` code (the engine is deliberately generic).

**Reference:** `docs/superpowers/specs/2026-06-28-loop-mode-design.md` (see the "Part 2 sanity check" section).

## What the engine already gives us

Per-loop `config.yml` hooks, all generic:
`interval`, `check` (exit 0 = trigger), `prompt`, `setup`, `fresh-workspace`.
Part 2 is a *specific* check/prompt/setup plus the task-file conventions —
no engine change should be required.

Starting point (the spec's worked example):

```yaml
loops:
  queue:
    interval: 2m
    setup: "git clone https://github.com/aethons-tools/cove ."
    check: "test -n \"$(ls .at-cove/queue/*.task 2>/dev/null)\""
    prompt: "Pick the oldest file in .at-cove/queue, do what it says, then remove it."
    fresh-workspace: true
```

## To design (its own brainstorm → spec → plan → build cycle)

- **Task file format:** location (`.at-cove/queue/`?), ordering (name vs mtime),
  content schema (free-form instructions vs structured: title/body/target branch/done-criteria),
  how a task names its target repo/branch.
- **Task lifecycle:** consume/complete (remove the file? move to `done/`? commit the removal?),
  failure handling (retry / move to `failed/`),
  and **poison-task protection** — a failing agent re-triggers the *same* task,
  so the task MUST be consumed or marked even on failure
  (the engine's `maxDrain` cap is only a backstop, not a fix).
- **Git workflow & idempotency:** each task is likely "make a change + open a PR";
  `fresh-workspace: true` gives a clean base per task.
  Define how the agent commits/pushes/PRs and how the queue mutation is recorded.
- **Queue location vs `fresh-workspace` (the key gotcha):**
  `fresh-workspace` clears the workspace and re-runs `setup` (re-clone) before each trigger,
  so an uncommitted queue file in the workspace would be wiped.
  The queue must therefore live somewhere that survives the reset —
  committed in the repo, on a separate control branch/repo,
  or outside the workspace (e.g. on the persistent `/agent-data` volume) with the check/prompt pointing there.
  RESOLVE this first; it shapes everything else.
- **Multi-repo scaling** ("and others, soon"):
  per-repo loop entries vs one dispatcher loop that routes tasks across repos;
  one kit per repo vs one kit with many loops.
- **Auth/secrets:** `ANTHROPIC_API_KEY` (API billing) per the engine;
  `GITHUB_TOKEN` scope sufficient for clone + push + PR.
- **Prompts/rules:** the agent's operating instructions (CLAUDE.md / a skill) for queue processing —
  how it picks, executes, verifies, consumes, and reports a task.

## Open questions for brainstorming

1. Does the queue live in the work repo (committed), on a separate control location,
   or on `/agent-data`? (Driven by the `fresh-workspace` interaction above.)
2. One loop draining a shared queue vs per-task isolation; concurrency across repos.
3. Failure/poison-task policy — guarantee the task is consumed/marked even when the agent run fails.

## Deferred Part-1 Minors that may bite Part 2 (from the SDD ledger)

- `fresh-workspace` re-seeds on every idle poll — a 2m queue loop re-clones each time even with no work.
  Revisit if it becomes costly.
- `doLoop` does not warn on unresolved non-critical secrets (unlike `connect`).

## Next action

Run the brainstorming skill → Part 2 spec → plan → build, as its own cycle.
