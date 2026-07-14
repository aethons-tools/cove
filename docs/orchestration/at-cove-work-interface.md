---
summary: The substrate by which a dedicated scheduler dispatches one-shot worker agents through at-cove — the shipped `at-cove work` command (synchronous, one-shot, ephemeral hardened container from a kit), at-cove's host-orchestrated worker bracket (`at-task prepare` → agent → `at-task complete`) and the per-step credential air-gap it enforces, the `.at-task/task.json` → `task-result.json` worker contract at at-cove-owned VM paths, the three-authority credential model, per-class isolation, and the `COVE_RUN_*` passthrough that turns a secret resolver into a per-run token minter.
read_when: You are implementing or reviewing how the scheduler launches workers on hardened at-cove/Colima containers — the `at-cove work` command it calls, how the input/output handoff works, how at-cove drives the prepare/agent/complete bracket and air-gaps the worker from the code-host token, or the credential model (the `COVE_RUN_*`-driven per-run minter).
owns: the at-cove work substrate contract, at-cove's host-orchestrated worker bracket + credential air-gap, the worker input/output handoff pointer, the three-authority credential model, and per-class isolation
prereqs: linear-agent-workflow.md
tier: leaf
updated: 2026-07-14
---

# at-cove Work Interface

The concrete substrate that realizes the workflow's **worker fleet + dedicated scheduler**: how the scheduler dispatches **one-shot worker agents** through at-cove onto hardened containers, how each worker is **air-gapped from standing credentials**, and what it hands back. The command surface below (`at-cove work`), the host-orchestrated bracket it drives, and the **`COVE_RUN_*`-driven per-run token minter** are all **shipped**. For the workflow this substrate serves, see [linear-agent-workflow.md](linear-agent-workflow.md); for the operator config that keys into it, see [at-cove-config.md](../usage/at-cove-config.md).

In this repo the scheduler is **`at-cove dispatch`** and the worker is **`at-task`**, both driving the **`at-cove work`** command; see [OVERVIEW's architecture](../OVERVIEW.md#architecture).

## What at-cove is (grounding)

at-cove assembles a layered build context, `docker build`s it, and runs it on a backend (Colima today), then reaches it over SSH — see [`../OVERVIEW.md`](../OVERVIEW.md). Two phases matter here:

- **Build time:** egress is open; a setup script bakes the **project's toolchain** into the image.
- **Run time:** egress is locked by squid + nftables to the kit's `allowed-domains`; **secrets are acquired by running a configured command whose stdout is injected in memory only** — never baked into the image ([secret-injection flow](../OVERVIEW.md#secret-injection-the-chat-data-flow)).

A **worker is therefore a container from the kit's image**, and **dispatch is a hardened, non-interactive run** at-cove mediates so the egress lock, secret injection, and baseline all apply.

## Governing principle: gate the image, automate the run

- **The image is the trust root** — what it contains and what it may reach (`allowed-domains`). Keep image builds human-approved.
- **Instantiating a run from an approved image is the hot path** — fully programmatic.
- **Hard rule:** the scheduler dispatches **only through at-cove**, never raw `docker`/`colima` — bypassing at-cove bypasses the egress lock and secret broker.

## Command surface — `at-cove work`

Dispatch is **synchronous and one-shot** (shipped). The scheduler runs one blocking `at-cove work` per issue in a bounded goroutine; there is **no** run-id registry, detach, or lifecycle-verb set (`status`/`result`/`logs`/`ls`/`kill`) — an earlier design considered them and they were dropped as unnecessary under the synchronous model.

```
at-cove work [--kit-dir <dir>] --in <task.json> --out <task-result.json> [--timeout <dur>] [--grace <dur>] [--reap]
```

One invocation, start to finish:
1. **Scavenge** crash orphans — remove any container labeled `at-cove.work` older than `--grace` (self-healing after a crashed prior run).
2. **Build** the kit's image (docker-cached) and **run a fresh ephemeral container** — labeled, `--rm`, **no persistent volume**, not recorded in `.state` (dispatch owns its lifecycle).
3. **Fill + inject the task** — at-cove parses `--in`, **fills the target repo** into it from the kit's [`source-control`](../usage/at-cove-config.md) (repo + `main-branch`; the scheduler names no repo — the kit is the single source), and writes the completed task over SSH stdin to the at-cove-owned VM path `/home/agent/work/.at-task/task.json`.
4. **Drive the worker bracket itself, step-by-step over ssh**: `at-task prepare` (env **with** `AT_TASK_GIT_TOKEN`) → `claude -p "<class prompt + result protocol>"` (env **without** the token) → `at-task complete` (env **with** the token), each bounded by `--timeout`. at-cove reads `worker.class` from the task to resolve the kit's `workers[class].prompt` — so the task is not opaque (at-cove reads the class and fills the repo), but its brief and other contents are.
5. **Extract** the at-cove-owned VM path `/home/agent/work/.at-task/task-result.json` to `--out`; **destroy** the container on every exit path.

`--reap` runs only the scavenge and exits. File I/O is SSH-based (backend-agnostic). The kit's `workers` schema is owned by [at-cove-config.md](../usage/at-cove-config.md); the `.at-task/` file shapes are owned by the [at-task usage doc](../usage/at-task.md). Full design: [`../superpowers/specs/2026-07-09-at-cove-config-v2-design.md`](../superpowers/specs/2026-07-09-at-cove-config-v2-design.md) §3.

## at-cove owns the bracket, and the credential air-gap

Earlier designs had a kit-authored entrypoint script sequence the git/PR worker around the agent, with the air-gap enforced by an `env -u` inside that script. That script is gone: **at-cove itself drives the bracket**, issuing `at-task prepare`, `claude -p`, and `at-task complete` as three separate ssh invocations, each with its own env.

The token named `AT_TASK_GIT_TOKEN` is included in the env for the `prepare` and `complete` steps and withheld from the agent step. Each ssh step writes its own tmpfs env file over stdin, sources it, and removes it before running its command — so the token is **only ever transmitted to the VM for the two git steps**, never resident during the agent step. This is a stronger air-gap than the old kit-script `env -u`, which ran in the same container session as a script that *did* hold the token.

The untrusted-brief-ingesting **agent never holds the code-host token**; only `at-task`'s clone/push/PR steps do. The kit declares only `workers[class].prompt` — the role/behavior for that class ([at-cove-config.md](../usage/at-cove-config.md)) — and at-cove appends the standard result protocol (the instruction to read `.at-task/task.json` and write `.at-task/worker-result.json`) before sending it to `claude -p`. `at-task` owns all git and code-host interaction; see its design at [`../superpowers/specs/2026-07-07-at-work-worker-design.md`](../superpowers/specs/2026-07-07-at-work-worker-design.md).

## Worker contract

The worker's contract is **at-task's**, not a bespoke `result.json`. The scheduler builds the task file (naming **no repo**); at-cove fills the target repo from the kit's `source-control`, injects the task at `.at-task/task.json`, and extracts the result from `.at-task/task-result.json`, both fixed, at-cove-owned VM paths (no kit-declared input/output). The file shapes, the JSON Schemas, and the `.at-task/` file-handoff convention (`task.json` → `worker-result.json` → `task-result.json`) are owned by the [at-task usage doc](../usage/at-task.md) and its linked [inputs](../usage/at-task-inputs.md)/[output](../usage/at-task-output.md) contract docs — not restated here.

The worker does **no tracker I/O** — it writes its result, pushes any branch/PR, and exits; the scheduler reads `task-result.json` and performs **all** tracker writes (the single-writer property). The scheduler's mapping of the result → tracker transitions is owned by [at-cove-config.md](../usage/at-cove-config.md).

## Three separated authorities

No component holds two authorities; untrusted input reaches only the least-privileged one.

| Component | Holds | Never |
|-----------|-------|-------|
| **Scheduler** (`at-cove dispatch`) | the **tracker** API token (its own standing secret) | touches the code host |
| **Worker** (`at-task`) | a **code-host token** (clone/push/PR), used only by `prepare`/`complete` | tracker creds; the agent step never sees it |
| **Agent** | nothing — edits and tests files only | any credential (at-cove withholds the token from its ssh step) |

**Credential status.** `AT_TASK_GIT_TOKEN` is an ordinary kit-declared secret (a `command` whose stdout is injected in memory) — but at-cove exposes the run's parameters to that command's environment as `COVE_RUN_{REPO,ISSUE,CLASS,TIMEOUT}` during dispatch, which turns the resolver into a **per-run minter**: it mints a short-lived, repo-scoped token fresh for this run rather than returning a standing credential. Resolver mechanics (the `command` array, `secrets.yml`, precedence, fail-closed behavior) are owned by [at-cove-secrets.md](../usage/at-cove-secrets.md); this doc covers only what dispatch adds.

Two properties matter for the threat model:
- **Scope is fixed in the minter, not from run params.** The minter is told *which* repo to scope the token to (the kit's `source-control.github.project`, passed as `at-mint github --repo`), but the *permissions* it requests (e.g. `contents`+`pull_requests`) are hard-coded in `at-mint` itself — untrusted issue text flowing through the task can never widen a scope.
- **Minted fresh before each git step, not once per run.** at-cove re-invokes the resolver separately for `at-task prepare` and `at-task complete` (not once and cached), so each git step gets its own token and the code host's fixed token TTL (e.g. GitHub's ~1-hour App-installation-token lifetime) never bounds how long a dispatch run may take.

The reference kit's minter is [`at-mint github`](../usage/at-mint.md): a GitHub App installation-token resolver scoped by `--repo` (at-cove supplies the kit's repo) while the operator supplies `--app-id`/`--install-id` and the App private key via `--app-key-file` or `AT_MINT_GITHUB_APP_KEY`, and fails closed on any missing input or API error. See its [RUNBOOK](../../kits/reference-worker/RUNBOOK.md) for App provisioning.

This keeps the **three-authority separation** exact: the scheduler holds only the tracker token and never runs the minter; the minter (a host-side resolver `command`, not scheduler code) is the only thing that ever reads the GitHub App private key, and only on the at-cove host; the worker VM receives only the scoped, short-lived token the minter produced, over the same per-step air-gap described above.

## Isolation by handler class

- **Build-heavy classes (e.g. `implement`)** — **container-per-task** (shipped): each dispatch is a fresh, ephemeral, volume-less container, torn down after and crash-scavenged. Clean state per run, no build-daemon/cache contention.
- **Light classes (e.g. `plan`)** — could later pack into a shared warm container with a worktree per task and no code-host token; a v-next optimization, not required. A worktree isolates checkouts but not the shared mutable state (daemon home, caches) a multi-tenant container contends on — which is why build-heavy tasks get their own container.

## Status — shipped vs. deferred

- **Shipped:** `at-cove work` (synchronous one-shot, ephemeral container, crash-scavenge); the reference worker kit and the host-orchestrated bracket at-cove drives (`at-task prepare` → agent → `at-task complete`) with the per-step credential air-gap; at-task's `.at-task/task.json` → `task-result.json` contract; the scheduler wiring ([at-cove-config.md](../usage/at-cove-config.md)); container-per-task isolation; the `COVE_RUN_*` run-parameter passthrough and the resulting per-run token minter (`at-mint github`), minted fresh before each git step.
- **Deferred:** per-run `--egress-profile`; multi-code-host (GitHub-only today).
- **Dropped:** run-ids, `run --detach`, and the `status`/`result`/`logs`/`kill`/`ls` lifecycle-verb registry — superfluous under synchronous dispatch.
