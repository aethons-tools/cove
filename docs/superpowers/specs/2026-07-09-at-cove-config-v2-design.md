# at-cove config v2 — workers map + host-orchestrated bracket — Design

**Date:** 2026-07-09
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binary `at-cove`)
**Tracks:** [AET-29](https://linear.app/aethons-tools/issue/AET-29), child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic
**Follows:** [AET-28](https://linear.app/aethons-tools/issue/AET-28) (at-work contract v2) — this reconciles `at-cove`'s kit config + dispatch to the redesigned surface.

## 1. Purpose

Bring the `at-cove` product into line with the redesigned **kit config surface**, now the
canonical contract in the usage docs:
- [`docs/usage/at-cove-config.md`](../../usage/at-cove-config.md) — the `config.yml` schema.
- [`docs/usage/at-cove-secrets.md`](../../usage/at-cove-secrets.md) — secret declaration + resolution.

The redesign shrinks the kit to identity + secrets + **worker classes** + image, and moves the
dispatch worker bracket (prepare → agent → complete) out of a kit-authored `run-worker.sh` and
into **at-cove itself**, driven step-by-step from the host so the code-host token is air-gapped
from the agent step. This spec references the usage docs for the exact schema rather than
restating it.

## 2. Governing decisions

- **`secrets`: list → map** keyed by the env var name; each value is `{ description?, command? }`.
- **`image.setup-script` → `setup-scripts`** (plural — a list of independent script *files*).
  `secrets.<name>.command` stays **singular** (argv of one command, run without a shell on the
  host). The naming now encodes the difference: one tokenized command vs. many script files.
- **`backend` removed** from the surface — default to **colima** (the only registered backend).
  Multi-backend stays an internal capability; no config knob today.
- **`dispatch` → `workers`** — a map `class → { prompt }`. The kit declares only *which worker
  classes exist and each one's role prompt*. `dispatch.command`/`input`/`output` are gone.
- **`setup` and `loops` removed** — `at-work prepare` seeds the dispatch workspace, so `setup`
  is redundant there; `loops` is a weaker form of the scheduler. (See §6 for the `connect` risk.)
- **at-cove owns the worker bracket** (§3). The class `prompt` is role/behavior; at-cove appends
  the standard result protocol. The token air-gap is enforced by at-cove, per step.

## 3. The host-orchestrated worker bracket

`at-cove dispatch <kit> --in <task.json> --out <task-result.json>` drives:

```
0. RunEphemeral (launch VM)              6. ssh: <env WITHOUT token>  claude -p "<prompt+protocol>"
1. Dial + waitForSSH                      8. ssh: <env WITH token>     at-work complete
2. seed OAuth credentials                 9. cat …/.at-work/task-result.json → local --out
3. writeVM task.json → …/.at-work/        10. RemoveContainer
4. ssh: <env WITH token>  at-work prepare
```

- **Class selection:** at-cove reads `worker.class` from the injected `task.json` and resolves
  `workers[class].prompt`; an undeclared class is a dispatch error.
- **Token air-gap (stronger than v1):** the secret literally named `AT_WORK_GIT_TOKEN` is included
  in the env for `prepare`/`complete` and **withheld** from the agent step. Because each `ssh`
  command is its own shell, the token is only ever *transmitted* to the VM for the two git steps —
  it is never resident during the agent step. Every *other* kit secret is present throughout.
- **Secret transport invariant preserved, per step:** values flow via a tmpfs env file written
  over ssh **stdin** (mode 600), sourced then removed; never on argv or in logs. at-cove uses a
  token-bearing env for steps 4/8 and a token-less env for step 6.
- **Prompt:** at-cove sends `workers[class].prompt` (role/behavior) plus a standard appended
  protocol — "read `.at-work/task.json`; do the work; write `.at-work/worker-result.json` as
  exactly one of `ok`/`needs-input`/`error`" (the shapes from
  [at-work-inputs.md](../../usage/at-work-inputs.md)). The protocol boilerplate lives in at-cove,
  not the kit.
- **Fixed VM workspace:** `/home/agent/work` with `.at-work/` — at-cove-owned constants; no kit
  `input`/`output` config. `at-work prepare` inits the repo in place there (per AET-28).

The `--timeout`/`--grace`/`--reap` flags and the scavenge/teardown behavior are unchanged; only
the middle "run one command" step becomes the three-step bracket.

## 4. Component changes

- **`internal/kit/config.go`** — `Secrets` `[]Secret` → `map[string]SecretConfig{Description,Command}`;
  `ImageConfig.SetupScript` → `SetupScripts` (yaml `setup-scripts`); **remove** `Backend`, `Setup`,
  `Loops`, `DispatchConfig`; **add** `Workers map[string]Worker` (`Worker{Prompt string}`). Rewrite
  `ParseConfig` validation (name required; no backend; map-key/worker rules; setup-scripts rename).
- **`internal/assemble`** — `setup-script` → `setup-scripts` in the manifest writer + error strings + tests.
- **`internal/dispatchrun`** — replace the single `dispatch.command` run with the three-step bracket:
  read the class from `--in`, resolve the prompt, seed task.json to the fixed path, run
  prepare/agent/complete over ssh with per-step env (token withheld from the agent), extract the
  result. Keep secret resolution, waitForSSH, scavenge, teardown.
- **`cmd/at-cove`** — delete the `loop` command; default the backend to colima (drop the
  `backend:` lookup); build `[]secret.Spec` from the secrets **map**; rewire `dispatch` (workers,
  no dispatch config).
- **`internal/loop`** — deleted.
- **`internal/connect`** — drop the `setup` seed step (see §6).
- **`internal/state`** — the persisted secret list still works from the map (name = key).
- **`kits/reference-worker`** — `config.yml` becomes `name` + `secrets` (map) + `workers` + `image`;
  delete `run-worker.sh`, `run-agent.sh`, the `dispatch:` block, and `AT_WORK_AGENT_COMMAND`.
- **Docs** — `OVERVIEW.md` (command surface: drop `loop`; drop the dispatch file contract; add
  `workers`); `orchestration/at-cove-dispatch-interface.md` (host-orchestrated bracket).
- **Scheduler — unchanged.** It already writes `worker.class` into `task.json` and calls
  `at-cove dispatch --in/--out`; it reads `task-result.json`.

## 5. Testing

Hermetic as today (`runner.Fake`, no VM/network). New/updated coverage:
- **config**: secrets-map parse, `setup-scripts` rename, `workers` parse, rejection of removed
  fields (`backend`/`setup`/`loops`/`dispatch` now unknown → error), validation rules.
- **dispatchrun**: the three ssh steps run in order; the agent step's env **omits**
  `AT_WORK_GIT_TOKEN` while prepare/complete include it; the token never appears on any recorded
  argv; class lookup + prompt assembly; missing/undeclared class → error; result extraction.
- **reference kit**: the parse test tracks the new shape; the `integration`-gated e2e drives the
  host-orchestrated bracket end-to-end (maintainer-run).

## 6. Risks / non-goals

- **`connect` isolated-workspace seeding.** `setup` today seeds an **isolated** workspace for
  interactive `at-cove connect`. Removing it means isolated interactive sessions are no longer
  auto-seeded (Shared/`--workspace` bind-mount still works; the agent can clone manually). This is
  a deliberate removal per AET-29, called out here so the reviewer confirms it's acceptable.
- **Non-goals:** additional worker *models* beyond the standard at-work bracket (the `workers`
  shape leaves room, but only one model ships today); any backend other than colima; the
  scoped-token minter; changes to the credential air-gap for interactive `connect`.

## 7. Decomposition (plans)

Sizable — sequence into three hermetic plans, each green:
1. **Config removals + renames** — `secrets` map, `setup-scripts`, remove `backend`/`setup`/`loops`
   (delete `internal/loop`, the `loop` command, the `connect` setup seed); update consumers +
   validation + tests. Keeps `DispatchConfig` so `dispatch` stays green.
2. **workers + host-orchestrated bracket** — add `Workers`, remove `DispatchConfig`, rewrite
   `dispatchrun` to the three-step bracket (per-step env air-gap, class lookup, embedded protocol
   prompt, fixed paths), rewire `cmd/at-cove dispatch`.
3. **reference kit + docs + e2e** — reference `config.yml` → workers, delete the worker scripts,
   update `OVERVIEW.md` + `orchestration/at-cove-dispatch-interface.md`, update the e2e.
