---
summary: The substrate by which a dedicated scheduler dispatches one-shot worker agents through at-cove — the shipped `at-cove dispatch` command (synchronous, one-shot, ephemeral hardened container from a kit), at-cove's host-orchestrated worker bracket (`at-work prepare` → agent → `at-work complete`) and the per-step credential air-gap it enforces, the `.at-work/task.json` → `task-result.json` worker contract at at-cove-owned VM paths, the three-authority credential model, and per-class isolation. The parametrized per-task token minter is the intended credential design and is deferred.
read_when: You are implementing or reviewing how the scheduler launches workers on hardened at-cove/Colima containers — the `at-cove dispatch` command it calls, how the input/output handoff works, how at-cove drives the prepare/agent/complete bracket and air-gaps the worker from the code-host token, or the credential model (shipped token vs. the deferred minter).
owns: the at-cove dispatch substrate contract, at-cove's host-orchestrated worker bracket + credential air-gap, the worker input/output handoff pointer, the three-authority credential model, and per-class isolation
prereqs: linear-agent-workflow.md
tier: leaf
updated: 2026-07-10
---

# at-cove Dispatch Interface

The concrete substrate that realizes the workflow's **worker fleet + dedicated scheduler**: how the scheduler dispatches **one-shot worker agents** through at-cove onto hardened containers, how each worker is **air-gapped from standing credentials**, and what it hands back. The command surface below (`at-cove dispatch`) and the host-orchestrated bracket it drives are **shipped**; the parametrized per-task minter is the intended credential design and is **deferred** (the MVP injects a plain repo-scoped token). For the workflow this substrate serves, see [linear-agent-workflow.md](linear-agent-workflow.md); for the operator config that keys into it, see [scheduler-config.md](scheduler-config.md).

In this repo the scheduler is the **`at-dispatch`** binary and the worker is **`at-work`**, both consuming the **`at-cove`** CLI; see [OVERVIEW's architecture](../OVERVIEW.md#architecture).

## What at-cove is (grounding)

at-cove assembles a layered build context, `docker build`s it, and runs it on a backend (Colima today), then reaches it over SSH — see [`../OVERVIEW.md`](../OVERVIEW.md). Two phases matter here:

- **Build time:** egress is open; a setup script bakes the **project's toolchain** into the image.
- **Run time:** egress is locked by squid + nftables to the kit's `allowed-domains`; **secrets are acquired by running a configured command whose stdout is injected in memory only** — never baked into the image ([secret-injection flow](../OVERVIEW.md#secret-injection-the-connect-data-flow)).

A **worker is therefore a container from the kit's image**, and **dispatch is a hardened, non-interactive run** at-cove mediates so the egress lock, secret injection, and baseline all apply.

## Governing principle: gate the image, automate the run

- **The image is the trust root** — what it contains and what it may reach (`allowed-domains`). Keep image builds human-approved.
- **Instantiating a run from an approved image is the hot path** — fully programmatic.
- **Hard rule:** the scheduler dispatches **only through at-cove**, never raw `docker`/`colima` — bypassing at-cove bypasses the egress lock and secret broker.

## Command surface — `at-cove dispatch`

Dispatch is **synchronous and one-shot** (shipped). The scheduler runs one blocking `at-cove dispatch` per issue in a bounded goroutine; there is **no** run-id registry, detach, or lifecycle-verb set (`status`/`result`/`logs`/`ls`/`kill`) — an earlier design considered them and they were dropped as unnecessary under the synchronous model.

```
at-cove dispatch <kit-dir> --in <task.json> --out <task-result.json> [--timeout <dur>] [--grace <dur>] [--reap]
```

One invocation, start to finish:
1. **Scavenge** crash orphans — remove any container labeled `at-cove.dispatch` older than `--grace` (self-healing after a crashed prior run).
2. **Build** the kit's image (docker-cached) and **run a fresh ephemeral container** — labeled, `--rm`, **no persistent volume**, not recorded in `.state` (dispatch owns its lifecycle).
3. **Inject the task** — write `--in` over SSH stdin to the at-cove-owned VM path `/home/agent/work/.at-work/task.json`.
4. **Drive the worker bracket itself, step-by-step over ssh**: `at-work prepare` (env **with** `AT_WORK_GIT_TOKEN`) → `claude -p "<class prompt + result protocol>"` (env **without** the token) → `at-work complete` (env **with** the token), each bounded by `--timeout`. at-cove reads `worker.class` from the injected task to resolve the kit's `workers[class].prompt` — the task is no longer fully opaque to at-cove, only its other contents are.
5. **Extract** the at-cove-owned VM path `/home/agent/work/.at-work/task-result.json` to `--out`; **destroy** the container on every exit path.

`--reap` runs only the scavenge and exits. File I/O is SSH-based (backend-agnostic). The kit's `workers` schema is owned by [at-cove-config.md](../usage/at-cove-config.md); the `.at-work/` file shapes are owned by the [at-work usage doc](../usage/at-work.md). Full design: [`../superpowers/specs/2026-07-09-at-cove-config-v2-design.md`](../superpowers/specs/2026-07-09-at-cove-config-v2-design.md) §3.

## at-cove owns the bracket, and the credential air-gap

Earlier designs had a kit-authored entrypoint script sequence the git/PR worker around the agent, with the air-gap enforced by an `env -u` inside that script. That script is gone: **at-cove itself drives the bracket**, issuing `at-work prepare`, `claude -p`, and `at-work complete` as three separate ssh invocations, each with its own env.

The token named `AT_WORK_GIT_TOKEN` is included in the env for the `prepare` and `complete` steps and withheld from the agent step. Each ssh step writes its own tmpfs env file over stdin, sources it, and removes it before running its command — so the token is **only ever transmitted to the VM for the two git steps**, never resident during the agent step. This is a stronger air-gap than the old kit-script `env -u`, which ran in the same container session as a script that *did* hold the token.

The untrusted-brief-ingesting **agent never holds the code-host token**; only `at-work`'s clone/push/PR steps do. The kit declares only `workers[class].prompt` — the role/behavior for that class ([at-cove-config.md](../usage/at-cove-config.md)) — and at-cove appends the standard result protocol (the instruction to read `.at-work/task.json` and write `.at-work/worker-result.json`) before sending it to `claude -p`. `at-work` owns all git and code-host interaction; see its design at [`../superpowers/specs/2026-07-07-at-work-worker-design.md`](../superpowers/specs/2026-07-07-at-work-worker-design.md).

## Worker contract

The worker's contract is **at-work's**, not a bespoke `result.json`. The scheduler builds the task file; at-cove injects it at `.at-work/task.json` and extracts the result from `.at-work/task-result.json`, both fixed, at-cove-owned VM paths (no kit-declared input/output). The file shapes, the JSON Schemas, and the `.at-work/` file-handoff convention (`task.json` → `worker-result.json` → `task-result.json`) are owned by the [at-work usage doc](../usage/at-work.md) and its linked [inputs](../usage/at-work-inputs.md)/[output](../usage/at-work-output.md) contract docs — not restated here.

The worker does **no tracker I/O** — it writes its result, pushes any branch/PR, and exits; the scheduler reads `task-result.json` and performs **all** tracker writes (the single-writer property). The scheduler's mapping of the result → tracker transitions is owned by [scheduler-config.md](scheduler-config.md).

## Three separated authorities

No component holds two authorities; untrusted input reaches only the least-privileged one.

| Component | Holds | Never |
|-----------|-------|-------|
| **Scheduler** (`at-dispatch`) | the **tracker** API token (its own standing secret) | touches the code host |
| **Worker** (`at-work`) | a **code-host token** (clone/push/PR), used only by `prepare`/`complete` | tracker creds; the agent step never sees it |
| **Agent** | nothing — edits and tests files only | any credential (at-cove withholds the token from its ssh step) |

**Credential status.** The MVP injects a **plain repo-scoped `AT_WORK_GIT_TOKEN`** — an ordinary kit-declared secret (a `command` whose stdout is injected in memory). The intended hardening is a **parametrized per-task minter**: at-cove exposes the run's parameters to the secret command's environment (e.g. `COVE_RUN_*`) so the secret command becomes a per-run minter producing a short-lived, repo-scoped, expiring token — scope fixed **in** the minter (untrusted issue text cannot widen it), TTL/labels tuned by run params. This passthrough + minter is **deferred**; nothing else about secret handling changes when it lands.

## Isolation by handler class

- **Build-heavy classes (e.g. `implement`)** — **container-per-task** (shipped): each dispatch is a fresh, ephemeral, volume-less container, torn down after and crash-scavenged. Clean state per run, no build-daemon/cache contention.
- **Light classes (e.g. `plan`)** — could later pack into a shared warm container with a worktree per task and no code-host token; a v-next optimization, not required. A worktree isolates checkouts but not the shared mutable state (daemon home, caches) a multi-tenant container contends on — which is why build-heavy tasks get their own container.

## Status — shipped vs. deferred

- **Shipped:** `at-cove dispatch` (synchronous one-shot, ephemeral container, crash-scavenge); the reference worker kit and the host-orchestrated bracket at-cove drives (`at-work prepare` → agent → `at-work complete`) with the per-step credential air-gap; at-work's `.at-work/task.json` → `task-result.json` contract; the scheduler wiring ([scheduler-config.md](scheduler-config.md)); container-per-task isolation.
- **Deferred:** the parametrized token minter and the `COVE_RUN_*` run-parameter passthrough; per-run `--egress-profile`; multi-code-host (GitHub-only today).
- **Dropped:** run-ids, `run --detach`, and the `status`/`result`/`logs`/`kill`/`ls` lifecycle-verb registry — superfluous under synchronous dispatch.
