# Unified kit config — one `config.yml` for connect / work / dispatch — Design

**Date:** 2026-07-10
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-task`)
**Tracks:** follow-on to [COV-5](https://linear.app/aethons-tools/issue/COV-5) (at-cove config v2) and [COV-9](https://linear.app/aethons-tools/issue/COV-9) (the CLI rename). Prepares the ground for [COV-6](https://linear.app/aethons-tools/issue/COV-6)/[COV-7](https://linear.app/aethons-tools/issue/COV-7)/[COV-8](https://linear.app/aethons-tools/issue/COV-8).
**Builds on:** [COV-9](https://linear.app/aethons-tools/issue/COV-9) folded the scheduler into `at-cove dispatch`; [COV-11](https://linear.app/aethons-tools/issue/COV-11) added the per-run minter + `COVE_RUN_*` passthrough.

## 1. Purpose

Today there are **two** config surfaces: the kit's `.at-cove/config.yml` (used by `build`/`create`/`connect`/`work`) and a **separate** scheduler config passed to `at-cove dispatch --config` (`internal/dispatch/config`). Since the scheduler was folded into `at-cove` and the kit is already **one-per-repo**, keeping two files is redundant and inconsistent.

This design **merges the scheduler config into the kit `config.yml`**, so every `at-cove` subcommand reads one file the same way, and **homogenizes** the surface: the external systems the kit talks to (`source-control`, `tracker`) are parallel tagged unions that own their own well-known secrets; the handler classes split into two mode-specific trees (`workers`, `collaborators`); and the scheduler policy knobs group under `dispatch`. A key property falls out for free: the credential air-gap becomes **structural** — a secret's *location in the schema* determines who can ever see it, replacing today's name-based special-casing.

## 2. Governing decisions

- **One kit = one file for every command.** `at-cove dispatch` loads the kit like `work`/`connect` do. The `--config <path>` flag is replaced by the standard **optional positional kit-dir** (`at-cove dispatch [kit-dir]`, default `.`), matching `build`/`create`/`connect`/`work`.
- **`origin` → `source-control`**, a tagged union (one host; `github` today). `main-branch` nests **under the host member** (beside `project`). It carries the code-host's own well-known secret.
- **`tracker`** is a new **top-level** tagged union (one provider; `linear` today), a peer of `source-control` — both name an external system the kit talks to. The old `provider: linear` scalar disappears (the union key *is* the provider). It owns the tracker/state wiring and its well-known secrets.
- **`dispatch`** groups the scheduler *policy* knobs (`concurrency`, `reaper-timeout`, `dispatch-overhead`). `poll-interval`, `states`, and `class-label-prefix` stay under `tracker.linear` (they are tracker-specific).
- **Handler classes split by mode into two trees** (they share little config):
  - **`workers`** — **autonomous** classes (dispatchable). Each carries `prompt` (required) + scheduling attrs (`timeout`, `concurrency`). Their **VM-injected secrets come from the root `secrets` map only** — no per-worker secrets.
  - **`collaborators`** — **interactive / chat** classes (human-driven). Each carries its own `secrets`. **Parsed + validated now, not wired into runtime yet** (a later session adds interactive dispatch and binds `connect` to a chat class).
  - The tree *is* the mode; there is no `mode:` field.
  - Each tree has one reserved base key **`<common>`**; a class's effective config = merge(`<common>`, own).
- **Four secret buckets, scoped by location (structural air-gap):**
  1. **root `secrets`** — VM-injected into worker sandboxes (autonomous agents) and `connect`. Arbitrary names. (Unchanged role; today's top-level `secrets`.)
  2. **`source-control.<host>.secrets`** — the code-host credential (`AT_TASK_GIT_TOKEN`). Host-resolved, **minted fresh per git step**, handed only to `at-task prepare`/`complete` — never in the agent's VM env.
  3. **`tracker.<provider>.secrets`** — the scheduler's credentials (`AT_DISPATCH_TRACKER_TOKEN`, `AT_DISPATCH_WEBHOOK_SECRET`). Host-resolved inside `at-cove dispatch`, **never enters any VM**.
  4. **`collaborators.<class>.secrets`** — per interactive-class secrets (validated now, used later).
- **`connect` is unchanged** — it keeps resolving the root `secrets`. No connect work in this change.

## 3. The unified `config.yml`

```yaml
name: cove

source-control:                   # union (one host); required for work + dispatch
  github:
    project: aethons-tools/cove
    main-branch: main             # optional, default "main"
    secrets:                      # host-side; minted per git step; at-task only
      AT_TASK_GIT_TOKEN: { command: ["mint-github-token.sh"] }

tracker:                          # union (one provider); required for dispatch
  linear:
    team: COV
    poll-interval: 60s
    class-label-prefix: "class:"  # optional, default "class:"
    states:
      ready: Todo
      in-progress: In Progress
      in-review: In Review
      done: Done
      needs-input: Needs Input
      blocked: Backlog
    secrets:                      # host-side; scheduler only; never in a VM
      AT_DISPATCH_TRACKER_TOKEN:  { command: ["gh", "auth", "token"] }
      AT_DISPATCH_WEBHOOK_SECRET: { command: ["op", "read", "op://…"] }

dispatch:                         # scheduler policy (dispatch only)
  concurrency: 1
  reaper-timeout: 45m
  dispatch-overhead: 15m          # optional, default "15m"

secrets:                          # root: VM-injected into workers + connect
  ANTHROPIC_API_KEY: { command: ["op", "read", "op://…"] }

workers:                          # autonomous classes — dispatchable
  <common>:
    timeout: 30m
    concurrency: 2
  implementor:
    prompt: "You are an implementer. …"
    timeout: 40m                  # overrides <common>
  auditor:
    prompt: "You are an auditor. …"
    concurrency: 1                # overrides <common>; timeout inherits 30m

collaborators:                    # interactive/chat classes — validated now, wired later
  <common>:
    secrets:
      COMMON_TOKEN: { command: ["…"] }
  triager:
    secrets:
      LINEAR_TOKEN: { command: ["…"] }

image:
  setup-scripts: [ .install-files/install.sh ]
  allowed-domains: [ api.anthropic.com, api.github.com, github.com ]
```

## 4. `<common>` resolution

For a class in either tree, the **effective** config is `merge(<common>, own)`:
- **scalars** (`timeout`, `concurrency`): own overrides `<common>`.
- **`secrets`**: **union** of `<common>` and own; on a key collision, own wins.
- **`prompt`** (workers): own-only, **required** per worker; `<common>` may not set it.
- There is **no cross-tree base** — a secret both trees need is declared in each tree's `<common>` (the trees share little, by design).
- **`<common>` is the only reserved `<…>` key.** Any other `<…>`-wrapped map key is a hard error. Real class names may not contain `<`/`>`.

## 5. Secret handling & the structural air-gap

The change turns today's name-based special-casing into a location-based guarantee. In `internal/dispatchrun` today, the git token is fished out of the flat `secrets` map by matching the name `AT_TASK_GIT_TOKEN`. After this change:

- The **root `secrets`** resolve once and are injected into the worker VM (the agent step) — exactly as the non-token secrets do today. The git token is simply **not among them** anymore (it moved to `source-control`).
- The **`source-control.<host>` secret** (`AT_TASK_GIT_TOKEN`) is resolved **per git step** with the `COVE_RUN_*` env (the minter), and merged only into the `at-task prepare` / `at-task complete` step env — never the agent step. (COV-11's per-step-mint behavior, now keyed off the secret's *location* instead of its name.)
- The **`tracker.<provider>` secrets** are resolved only inside `at-cove dispatch` (the scheduler) and never leave the host.

So an autonomous worker structurally cannot receive the scheduler's tracker credentials or the raw long-lived git token — not by discipline, but because those secrets live in trees the worker's env is never built from.

**Well-known names (validated).** Inside `source-control.<host>.secrets` and `tracker.<provider>.secrets`, the keys are fixed and checked (typo protection); unknown keys are rejected:
- `source-control.github.secrets`: **`AT_TASK_GIT_TOKEN`** (required when dispatching/working the repo).
- `tracker.linear.secrets`: **`AT_DISPATCH_TRACKER_TOKEN`** and **`AT_DISPATCH_WEBHOOK_SECRET`** (both required).

Root `secrets` and `collaborators.*.secrets` keep **arbitrary** names (the standard resolver schema: `{ description?, command? }`, `command` omitted ⇒ resolved from the user's `~/.config/at-cove/secrets.yml`).

## 6. Command-scoped validation

Same pattern `origin` already uses (present-but-unused for some commands, required for others):

| Command | Requires |
|---|---|
| `build` / `create` | `name` (+ `image`) |
| `connect` | `name`; resolves root `secrets` |
| `work` | `name`, `source-control` (+ the named worker, root `secrets`) |
| `dispatch` | `name`, `source-control`, `tracker`, `dispatch`, ≥1 `workers` class |

- Exactly one union member under `source-control` and (if present) `tracker` (`Active()` validators, like the status unions).
- `github.project` is `owner/name`; `main-branch` defaults to `main`.
- `workers`: every real class needs a non-empty `prompt`; `timeout`/`concurrency` positive when set (own or via `<common>`). `dispatch`-required durations are positive Go durations.
- `collaborators`: structurally validated (secret shapes, `<common>`), **no runtime use yet**.
- `class-label-prefix` defaults to `class:`; `dispatch-overhead` defaults to `15m`.

## 7. Component changes (summary)

- **`internal/kit/config.go`** — the merge target. `Origin` → `SourceControl` (rename; `main-branch` + `secrets` move under the host member). Add `Tracker` union (`linear` member: `team`, `poll-interval`, `class-label-prefix`, `states`, `secrets`), `Dispatch` block (`concurrency`, `reaper-timeout`, `dispatch-overhead`), `Workers` (with `<common>` + per-class `prompt`/`timeout`/`concurrency`), `Collaborators` (with `<common>` + per-class `secrets`). Root `secrets` stays. New validation (well-known secret names, `<common>`, command-scoped requireds). A `(Config).Worker(class) (ResolvedWorker, error)` helper applies `<common>`.
- **`internal/dispatch/config`** — **retires.** Its `TrackerConfig`/`StateMap`/`Class` schema is subsumed by `kit.Config`. The scheduler and `linear.New` read `kit.Config` (or a small view derived from it) instead of a standalone config.
- **`internal/dispatch/scheduler`** — reads the kit's `tracker`/`dispatch`/`workers` (resolved via `<common>`); maps a class label to a `workers` key; writes that class into the `task.json` it injects and computes the per-class timeout (`workers[class].timeout` + `dispatch-overhead`); still shells `at-cove work <kit> --in <task> --out <result> --timeout <d>` (per COV-9). `collaborators` are not dispatched.
- **`internal/dispatchrun`** — the worker class continues to arrive **in the injected task** (`task.Worker.Class`), which resolves `Workers[class]` (now `<common>`-merged) for the prompt. Resolve the **root** `secrets` as the agent bucket; resolve the **`source-control`** secret per git step (minter, `COVE_RUN_*`) into the `at-task` steps only. The air-gap split now keys off schema location, not the token name.
- **`cmd/at-cove/main.go`** — `doDispatch` takes the positional kit-dir (drop `--config`), `kit.Load`s it, and requires `tracker`+`dispatch`+workers. `doWork` is unchanged in how it selects the worker (class from the injected task) and keeps its `--timeout` flag (the scheduler computes and passes it); root `secrets` injection is handled in `dispatchrun`.
- **`kits/reference-worker`** — migrate to the unified shape (`source-control` with `AT_TASK_GIT_TOKEN`, `tracker.linear` with `AT_DISPATCH_*`, `dispatch`, `workers`, a sample `collaborators`), plus a sample scheduler wiring so `at-cove dispatch ./kits/reference-worker` is self-contained.
- **Docs** — `docs/usage/at-cove-config.md` becomes the single canonical schema (absorbing `docs/orchestration/scheduler-config.md`, which retires or becomes a pointer); `at-cove-work-interface.md` (secret buckets/air-gap), `OVERVIEW.md` (one config surface), and the orchestration `INDEX.md` updated.

## 8. Testing

Hermetic (`runner.Fake`), no real GitHub/Linear:
- `kit`: parse + validate the full surface — the two unions (`Active()`, one member, `owner/name`, `main-branch` default), well-known secret names (missing/unknown rejected), `<common>` merge (scalar override, secret union, own-wins), `<…>` reserved-key rejection, command-scoped requireds (dispatch needs tracker+dispatch+workers; work needs source-control).
- `dispatchrun`: root secrets → agent env; `source-control` secret → per-step mint into `at-task` steps only, **withheld from the agent** (the COV-11 air-gap test, now asserting by location); tracker secrets never reach any VM; no secret value on argv.
- `scheduler`: reads kit `tracker`/`workers`; class-label → worker mapping; `<common>` applied to per-class `timeout`/`concurrency`; `collaborators` ignored at dispatch.
- `cmd/at-cove`: `at-cove dispatch [kit-dir]` positional (config-missing/bad-config/token-fail paths ported from COV-9); `work` resolves the class from the injected task and applies `<common>`.

## 9. Risks / non-goals

- **`collaborators` is schema-only now.** It is parsed and validated but nothing dispatches or connects to it yet; wiring interactive dispatch and binding `connect` to a chat class is a **later** session.
- **`connect` unchanged** — still resolves root `secrets`; no chat-class binding in this change.
- **Migration is breaking** for any existing kit/scheduler config; the reference kit and docs are migrated in-change, and there is no compatibility shim (pre-release surface).
- **Non-goals:** multi-repo kits (still one-per-repo); the webhook receiver (COV-7); egress additions / `--egress-profile` (COV-6); changing the minter or the bracket/air-gap *mechanism* (COV-11) — only *where the secret is declared* moves.

## 10. Decomposition (plans)

Sequential, each green:
1. **Schema + validation** — reshape `kit.Config` (source-control rename + nesting, tracker union, dispatch block, workers/collaborators trees, `<common>` resolver, root secrets kept), with the full validation matrix. Pure parse/validate + `Worker(class)` helper; no runtime rewiring yet. Migrate the reference kit + `at-cove-config.md`.
2. **Secret buckets + air-gap by location** — `dispatchrun` resolves root secrets as the agent bucket and the `source-control` secret per git step (minter) into `at-task` steps only; wire well-known names. Update the air-gap tests to assert by location.
3. **Scheduler reads the kit + CLI** — retire `internal/dispatch/config`; scheduler + `linear.New` consume `kit.Config`; `at-cove dispatch [kit-dir]` positional (drop `--config`); `doWork --class`. Absorb `scheduler-config.md` into `at-cove-config.md`; update OVERVIEW/INDEX/at-cove-work-interface.
