# AET-21 — at-cove dispatch + wiring the loop — Design

**Date:** 2026-07-08
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-dispatch`)
**Tracks:** [AET-21](https://linear.app/aethons-tools/issue/AET-21) (child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic)
**Builds on:** [config](2026-07-06-at-dispatch-config-design.md), [scheduler](2026-07-06-at-dispatch-scheduler-mvp-design.md), and [at-work](2026-07-07-at-work-worker-design.md) — this wires them together.

## 1. Purpose

Make the whole chain run end-to-end: the **scheduler** claims a ready issue,
**dispatches a unit of work** into a fresh hardened at-cove VM, and **brokers the
result** back to Linear. This is the integration piece that turns the three merged
components (config, scheduler, at-work) into a running system.

It has three parts:
1. **`at-cove dispatch`** — a new, generic at-cove subcommand: run a unit of work in a
   fresh hardened VM (files in → kit's entrypoint → file out), then destroy it.
2. **A `dispatch:` entrypoint in kit config**, plus a **reference worker kit** whose
   `run-worker.sh` runs `at-work prepare → agent (token-stripped) → at-work complete`.
3. **Rewiring the scheduler + config** to speak at-work's `input.json`/`output.json`
   directly — collapsing the `DISPATCH_*`/`result.json` indirection.

## 2. Governing decisions (from brainstorming)

- **Synchronous, one-shot.** No `run --detach`, run-ids, `status/result/logs/ls/rm`, or
  a multi-instance registry (the original AET-21 delta) — the scheduler runs each
  dispatch **blocking** in a bounded goroutine. Those are dropped, not deferred.
- **`at-cove dispatch` is purpose-named and generic.** It means "perform this unit of
  work"; *how* the work is done (which entrypoint, whether at-work is used, how
  `work-class` is interpreted) lives entirely in the **kit config + its entrypoint**,
  not in flags. at-cove never parses `input.json`/`output.json` — it plumbs the files
  and runs the kit's command.
- **The prepare→agent→complete sequence + the token air-gap live in a kit script**
  (`run-worker.sh`), not in at-cove.
- **No adapter.** The scheduler writes `input.json` and reads `output.json`
  **directly**. This couples the scheduler to at-work's `Input`/`Output` types — an
  accepted trade-off, since this chain is the only use case now.
- **A class maps to a kit**, not an arbitrary command. The `DISPATCH_*` env builder,
  per-command `secrets:`, and `config.Result`/`ReadResult` from the config layer are
  **removed** (superseded by at-work's contract; the kit owns worker secrets).

## 3. Part 1 — `at-cove dispatch`

```
at-cove dispatch <kit-dir> --in <input.json> --out <output.json> [--timeout <dur>]
```

Blocking, one-shot. Steps (reusing at-cove's existing machinery):
1. Load the kit config; it **must** declare `dispatch.command` (else error). Ensure the
   image is built (`docker build` the assembled context — cached across dispatches).
2. **Scavenge crash orphans:** force-remove any container labeled `at-cove.dispatch`
   whose age exceeds a **grace window** (> the max dispatch timeout). This self-heals
   containers leaked by a previously-crashed dispatch; the age gate guarantees a
   concurrent in-flight sibling is never reaped.
3. Run a **fresh, ephemeral** container from the image — a unique name, the label
   `at-cove.dispatch`, `--rm`, and **no persistent/named volume** (so a force-remove
   reclaims everything), **not** recorded in `.state/state.json` (dispatch owns its
   lifecycle), hardening applied.
4. Resolve the kit's declared secrets on the host (memory-only, exactly like `connect`);
   **seed the agent's credentials** into the container's filesystem (reuse `connect`'s
   credential seed) so the agent authenticates non-interactively.
5. **Write `--in` into the VM** at `/in/input.json` over ssh stdin (values never on
   argv/disk beyond the VM), then run the kit's `dispatch.command` via `connect`'s
   `Transport` (secrets sourced into the env, then `exec` the command), bounded by
   `--timeout`.
6. On exit, **extract `/out/output.json`** via `ssh cat` → the `--out` local path.
7. **Destroy** the container (force-remove by name) — on every path, including failure
   and timeout (deferred cleanup).
8. Exit `0` iff the command succeeded **and** the output file was produced; else nonzero
   with a diagnostic on stderr.

File I/O is **SSH-based** (write-via-stdin, read-via-`cat`) so it stays
backend-agnostic — no `docker cp`. `/in/input.json` and `/out/output.json` are the
**convention** the kit's entrypoint relies on. at-cove treats both files as opaque.

**Crash scavenging.** The container is labeled + volume-less, so it is reclaimable by
label alone. Three layers reap it: (a) the deferred force-remove on normal exit/timeout;
(b) `--rm` if the container stops on its own; (c) the age-based scavenge that every
subsequent `at-cove dispatch` runs at startup (step 2). The same scavenge logic is also
exposed as **`at-cove dispatch --reap`** (remove all labeled orphans and exit) for an
operator or a cron to call directly. Because dispatch carries no state volume, no volume
ever leaks.

**Kit config gains a `dispatch:` block:**
```yaml
dispatch:
  command: ["run-worker.sh"]   # runs in the VM; reads /in/input.json, writes /out/output.json
```
`kit.Config` gains a `Dispatch` field; `at-cove dispatch` (only) requires it non-empty.

## 4. Part 2 — the reference worker kit + `run-worker.sh`

A reference kit demonstrating an autonomous worker (the image/toolchain/egress remain
per-project, per the design's "you supply the image"):
- **Image** bakes `at-work` (built or copied in via the kit's `setup-script`), the agent
  (`claude`), `git`, and the project toolchain; `allowed-domains` covers Anthropic +
  the code host + dependency registries.
- **`config.yml`** declares `dispatch.command: ["run-worker.sh"]`, the `AT_WORK_GIT_TOKEN`
  secret (host resolver), and `image.env: AT_WORK_AGENT_COMMAND`.
- **`run-worker.sh`** (in `image-files/`):
  ```sh
  set -e
  at-work prepare  /in/input.json
  env -u AT_WORK_GIT_TOKEN  sh -c "$AT_WORK_AGENT_COMMAND"   # agent, token stripped
  at-work complete /in/input.json /out/output.json
  ```
  The token is present for `prepare`/`complete` (clone/push/PR) and **stripped** for the
  agent step — the air-gap, enforced here.

## 5. Part 3 — rewire the scheduler + config

**Config (`internal/dispatch/config`):**
- `Class` (autonomous): `{mode, kit, timeout, concurrency}` — **`command` removed,
  `kit` added** (path to the class's `.at-cove` kit).
- `RepoConfig`: add `source-branch` (the base branch to build work on).
- **Remove** `Task`, `BuildEnv`, `ResolveSecrets`, the `Env*` consts, the top-level
  `secrets:` list, and `Result`/`Artifacts`/`NeedsInput`/`Usage`/`ReadResult` — all
  superseded. `Validate` updates: autonomous ⇒ `kit` set (not `command`); interactive ⇒
  no `kit`; `repo.source-branch` required. The scheduler's **standing** secrets
  (`tracker.token`, `tracker.webhook-secret`) are unchanged.

**Scheduler (`internal/dispatch/scheduler`)** — `handle` is rewired:
1. Claim (`Transition → IN PROGRESS`).
2. Assemble the brief (unchanged) and build `worker.Input`:
   `Issue{Key: identifier, Title, WorkClass: class, Brief: brief}`,
   `Repo{Name: cfg.Repo.Slug, SourceBranch: cfg.Repo.SourceBranch, WorkBranch: "<class>/<identifier>"}`.
3. Write it to a temp `input.json`; run
   `at-cove dispatch <class.kit> --in input.json --out output.json` via the existing
   `Executor` (bounded by the class timeout).
4. Read `worker.Output` from `output.json` (missing/invalid → treat as `ERROR`).
5. **Broker to Linear:**
   - `OK` → `PostComment(agent.pr-message + work.pr-url)`, `Transition → IN REVIEW`.
   - `NEEDS_INPUT` → `PostComment` the ❓ block (`agent.needs-input` + `work.safe-state`),
     `Transition → NEEDS INPUT`.
   - `ERROR` (or missing output) → `PostComment` the diagnostic, `Transition → NEEDS INPUT`.

The `Executor` and `Tracker` seams are unchanged (the `Executor` now runs `at-cove
dispatch …` instead of the old class command). The scheduler **imports
`internal/dispatch/worker`** for the `Input`/`Output` types (the accepted coupling).

## 6. Architecture / touchpoints

```
cmd/at-cove/                    + `dispatch` subcommand
internal/dispatchrun/           the `at-cove dispatch` orchestration (create → seed → in → exec → out → destroy); Fake-runner tested
internal/backend/               ephemeral run (fresh container, not recorded in state) + destroy
internal/connect/               reused: Transport (secret inject + remote exec), credential seed, file-in via stdin
internal/kit/                   + Dispatch config field
internal/dispatch/config/       Class{kit}, Repo{source-branch}; remove DISPATCH_*/Result machinery
internal/dispatch/scheduler/    handle rewired to Input→at-cove dispatch→Output; broker maps worker.Output
kits/reference-worker/          reference kit + run-worker.sh (image is per-project)
```

## 7. Testing

- **`at-cove dispatch` orchestration** — plan/execution split like the rest of at-cove:
  the scavenge→create→seed→write-in→exec→read-out→destroy sequence is unit-tested against
  the `runner.Fake` (assert the exact backend/ssh argv sequence, incl. the startup
  scavenge removing labeled containers past the grace window, and that this run's
  container is force-removed on both success and failure). A real VM run is
  `integration`-tagged.
- **Scheduler `handle`** stays hermetic: the fake `Executor` simulates `at-cove dispatch`
  (reads the `input.json` it's handed, writes a chosen `output.json`); broker tests
  assert the Linear transitions + comments for `OK` / `NEEDS_INPUT` / `ERROR` /
  missing-output.
- **Config** — validation tests updated for the `kit`/`source-branch` schema; removed
  types' tests deleted.
- **End-to-end** — the reference kit + `run-worker.sh` are exercised by a manual/live
  integration run (real VM, real agent, real GitHub), which is also where the
  headless-agent-auth path is validated.

## 8. Non-goals (deferred/dropped)

- **Dropped:** `run --detach`, run-ids, `status/result/logs/kill/rm/ls`, the
  multi-instance registry (the synchronous model doesn't use them).
- **Deferred:** the scoped-token minter + step-wise minting (AET-24); the webhook
  receiver (AET-23); `plan`/`review` kits; multi-code-host; image **digest** pinning as a
  hard requirement (the MVP uses the kit's built image).

## 9. Open questions

- **Headless agent auth.** The agent (`claude`) needs valid non-interactive credentials
  in the worker VM. `dispatch` seeds the host-saved subscription login (like `connect`),
  but a 24/7 fleet needs those to stay refreshed — or an API-key-based agent config.
  Resolved at deploy; flagged here.
- **Image freshness.** `dispatch` rebuilds (docker-cached) each run; whether to require a
  pre-built pinned digest (`at-cove image build`) is a later hardening.
- **Scope size.** This spec spans at-cove + a kit + an at-dispatch revision; the plan
  will sequence it (dispatch command → kit field + reference kit → config revision →
  scheduler rewiring → integration) and is larger than the prior plans.
