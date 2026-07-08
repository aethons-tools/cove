---
summary: The substrate by which a dedicated scheduler dispatches one-shot worker agents through at-cove — the shipped `at-cove dispatch` command (synchronous, one-shot, ephemeral hardened container from a kit), the kit-declared entrypoint and the credential air-gap, at-work's input.json/output.json worker contract, the three-authority credential model, and per-class isolation. The parametrized per-task token minter is the intended credential design and is deferred.
read_when: You are implementing or reviewing how the scheduler launches workers on hardened at-cove/Colima containers — the `at-cove dispatch` command it calls, how the input/output handoff works, how the worker is air-gapped from the code-host token, or the credential model (shipped token vs. the deferred minter).
owns: the at-cove dispatch substrate contract, the kit-declared dispatch entrypoint + credential air-gap, the worker input/output handoff pointer, the three-authority credential model, and per-class isolation
prereqs: linear-agent-workflow.md
tier: leaf
updated: 2026-07-08
---

# at-cove Dispatch Interface

The concrete substrate that realizes the workflow's **worker fleet + dedicated scheduler**: how the scheduler dispatches **one-shot worker agents** through at-cove onto hardened containers, how each worker is **air-gapped from standing credentials**, and what it hands back. The command surface below (`at-cove dispatch`) is **shipped**; the parametrized per-task minter is the intended credential design and is **deferred** (the MVP injects a plain repo-scoped token). For the workflow this substrate serves, see [linear-agent-workflow.md](linear-agent-workflow.md); for the operator config that keys into it, see [scheduler-config.md](scheduler-config.md).

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
at-cove dispatch <kit-dir> --in <input.json> --out <output.json> [--timeout <dur>] [--grace <dur>] [--reap]
```

One invocation, start to finish:
1. **Scavenge** crash orphans — remove any container labeled `at-cove.dispatch` older than `--grace` (self-healing after a crashed prior run).
2. **Build** the kit's image (docker-cached) and **run a fresh ephemeral container** — labeled, `--rm`, **no persistent volume**, not recorded in `.state` (dispatch owns its lifecycle).
3. **Inject** the kit's declared secrets (in memory) and **write `--in` to `/in/input.json`** over SSH stdin.
4. **Run the kit-declared entrypoint** (`dispatch.command`), the secrets sourced from a tmpfs env script, bounded by `--timeout`.
5. **Extract `/out/output.json`** to `--out`; **destroy** the container on every exit path.

at-cove treats the two files as **opaque** — it never parses them and knows nothing about the worker. `--reap` runs only the scavenge and exits. File I/O is SSH-based (backend-agnostic). Full design: [`../superpowers/specs/2026-07-08-at-cove-dispatch-design.md`](../superpowers/specs/2026-07-08-at-cove-dispatch-design.md).

## The kit-declared entrypoint and the credential air-gap

The kit's `config.yml` declares `dispatch.command` — the entrypoint at-cove runs inside the container. *What* it does (whether it uses at-work, how it reads the work-class) is the kit's concern; at-cove stays generic.

The reference worker entrypoint (`run-worker.sh`) sequences the git/PR worker around the agent, and is where the **credential air-gap** lives:

```sh
at-work prepare  /in/input.json                          # clone/branch — has the token
env -u AT_WORK_GIT_TOKEN  "$AT_WORK_AGENT_COMMAND"       # the agent — token stripped
at-work complete /in/input.json /out/output.json         # commit/push/PR — has the token
```

The untrusted-brief-ingesting **agent never holds the code-host token**; only `at-work`'s clone/push/PR steps do. `at-work` owns all git and code-host interaction; see its design at [`../superpowers/specs/2026-07-07-at-work-worker-design.md`](../superpowers/specs/2026-07-07-at-work-worker-design.md).

## Worker contract

The worker's contract is **at-work's**, not a bespoke `result.json`. The scheduler builds `input.json`; the worker writes `output.json`:

- **`input.json`** — `issue{key, title, work-class, brief}` + `repo{name, source-branch, work-branch}`. The brief is the self-contained assembly of the issue, linked spec/plan, and comment thread.
- **`output.json`** — a top-level `status` (`OK` | `NEEDS_INPUT` | `ERROR`) plus an `agent` block (the agent's self-report: `pr-message` or `needs-input{doing,blocker,need,tried}`) and a `work` block (what at-work did: `branch`, `pr-url`, `safe-state`, `error`).

The worker does **no tracker I/O** — it writes `output.json`, pushes any branch/PR, and exits; the scheduler reads it and performs **all** tracker writes (the single-writer property). The scheduler's mapping of `output.json` → tracker transitions is owned by [scheduler-config.md](scheduler-config.md).

## Three separated authorities

No component holds two authorities; untrusted input reaches only the least-privileged one.

| Component | Holds | Never |
|-----------|-------|-------|
| **Scheduler** (`at-dispatch`) | the **tracker** API token (its own standing secret) | touches the code host |
| **Worker** (`at-work`) | a **code-host token** (clone/push/PR), used only by `prepare`/`complete` | tracker creds; the agent step never sees it |
| **Agent** | nothing — edits and tests files only | any credential (the token is stripped for its step) |

**Credential status.** The MVP injects a **plain repo-scoped `AT_WORK_GIT_TOKEN`** — an ordinary kit-declared secret (a `command` whose stdout is injected in memory). The intended hardening is a **parametrized per-task minter**: at-cove exposes the run's parameters to the secret command's environment (e.g. `COVE_RUN_*`) so the secret command becomes a per-run minter producing a short-lived, repo-scoped, expiring token — scope fixed **in** the minter (untrusted issue text cannot widen it), TTL/labels tuned by run params. This passthrough + minter is **deferred**; nothing else about secret handling changes when it lands.

## Isolation by handler class

- **Build-heavy classes (e.g. `implement`)** — **container-per-task** (shipped): each dispatch is a fresh, ephemeral, volume-less container, torn down after and crash-scavenged. Clean state per run, no build-daemon/cache contention.
- **Light classes (e.g. `plan`)** — could later pack into a shared warm container with a worktree per task and no code-host token; a v-next optimization, not required. A worktree isolates checkouts but not the shared mutable state (daemon home, caches) a multi-tenant container contends on — which is why build-heavy tasks get their own container.

## Status — shipped vs. deferred

- **Shipped:** `at-cove dispatch` (synchronous one-shot, ephemeral container, crash-scavenge); the kit `dispatch.command` entrypoint; at-work's `input.json`/`output.json` contract; the scheduler wiring ([scheduler-config.md](scheduler-config.md)); container-per-task isolation.
- **Deferred:** the reference worker kit + `run-worker.sh` and the end-to-end integration run; the parametrized token minter and the `COVE_RUN_*` run-parameter passthrough; per-run `--egress-profile`; multi-code-host (GitHub-only today).
- **Dropped:** run-ids, `run --detach`, and the `status`/`result`/`logs`/`kill`/`ls` lifecycle-verb registry — superfluous under synchronous dispatch.
