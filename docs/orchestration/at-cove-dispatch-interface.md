---
summary: Product-agnostic contract by which a dedicated scheduler dispatches one-shot worker agents through at-cove — the command surface (image pinning, non-interactive run, lifecycle verbs), the run-parameter passthrough that unlocks per-task scoped secrets, the parametrized code-host-token minter, the three-authority credential split, the worker /out/result.json handoff schema, and the delta at-cove must add.
read_when: You are implementing or reviewing how the scheduler launches worker agents on hardened at-cove/Colima containers — the at-cove commands it calls, how per-task credentials are minted and injected, what a worker returns, or which at-cove capabilities and egress domains must be added.
owns: the at-cove dispatch command surface, the run-parameter passthrough, the per-task secret-minter design, the three-authority credential model, the worker result-handoff schema, per-class isolation, and the at-cove capability/egress delta
prereqs: linear-agent-workflow.md
tier: leaf
updated: 2026-07-04
---

# at-cove Dispatch Interface

## Purpose

This is the concrete, buildable contract that realizes the workflow's **worker fleet + dedicated scheduler**.
It defines how the scheduler dispatches **one-shot worker agents** through at-cove onto hardened containers,
how each worker gets a **short-lived, narrowly-scoped credential** and **no standing authority**,
and what a worker hands back.
It turns "workers run on hardened infra, credential-free" from a principle into a command surface.

See the [Linear agent-workflow](linear-agent-workflow.md) for the lifecycle, the fan-out model, and the scheduler's role;
this document owns only the dispatch substrate.
In this repo the scheduler is realized as the **`at-dispatch`** binary — a separate executable that consumes the `at-cove` CLI; see [OVERVIEW's architecture](../OVERVIEW.md#architecture) for the repo layout.

## What at-cove is (grounding)

at-cove today assembles a layered build context, `docker build`s it, and `docker run`s it on **Colima**, then SSHes in — see [`../OVERVIEW.md`](../OVERVIEW.md) for the full picture. Two phases matter here:

- **Build time:** egress is open; a setup script bakes the **project's build toolchain** into the image (`at-cove build` assembles the context; the backend builds the image inside `create`).
- **Run time:** egress is locked by squid + nftables to the kit's `allowed-domains`; **secrets are acquired at connect time by running a configured command whose stdout becomes the injected value, in memory only** — secret *material* is never baked into the image. This is at-cove's real [secret-injection flow](../OVERVIEW.md#secret-injection-the-connect-data-flow), and the minter below reuses it wholesale.

Therefore a **worker is a container from the pinned image**, and **dispatch is a hardened, non-interactive `run`** that at-cove mediates so the egress lock, secret injection, and baseline all apply.

**The gap to close.** at-cove today is built for **one interactive instance per kit**: `create` provisions a single VM, `connect` opens an interactive session, and `.state/state.json` records *the* running instance. Autonomous dispatch inverts every one of those assumptions — **many concurrent, detached, per-run-addressable containers from one pinned image, no human at the terminal**. That shift (per-run identity instead of per-kit state) is the real weight behind the delta below, not the individual verbs.

## Governing principle: gate the image, automate the run

- **The image is the trust root** — what it contains and what it may reach (`allowed-domains`).
  Keep image builds **human-approved and pinned by digest**.
  This is where the sandbox's human-gating belongs.
- **Instantiating runs from an approved image is the hot path** — make it fully programmatic.
- **Hard rule:** the scheduler dispatches **only through at-cove**, never raw `docker`/`colima`.
  Bypassing at-cove bypasses the egress lock and secret broker.

Humans gate the capability surface; the scheduler automates instantiation.

## Command surface

The core the scheduler needs.
"Status" is measured against at-cove's **real** [command surface](../OVERVIEW.md#command-surface) today (`build` / `create` / `connect` / `recreate` / `destroy` / `status` / `version`).

| Command | Purpose | Status vs. today |
|---------|---------|--------|
| `at-cove image build` → **digest** | Build the hardened image from the kit; emit an immutable, pinnable digest. Human-approved, infrequent. | partial: context assembly is `build`, image build is folded into `create`; split out + add digest emit/pin |
| `at-cove run --detach …` → **run-id** | Launch a fresh hardened container from a pinned image, **non-interactively**, and return a run-id immediately. | add: today's `create`+`connect` are one interactive instance per kit |
| `at-cove status <run-id> --json` | Lifecycle state + exit code; drives polling and the reaper. | extend: `status` today is per-kit (`running`/`stopped`/`absent`), not per-run + `--json` |
| `at-cove result <run-id>` | Return the worker's structured handoff (`/out/result.json`). | add |
| `at-cove logs <run-id> [--follow]` | Transcript / observability. | add |
| `at-cove kill <run-id>` / `rm <run-id>` | Terminate + reclaim. The reaper's tools. | add (cf. per-kit `destroy` today) |
| `at-cove ls --json` | Enumerate runs; reconcile after a scheduler restart; concurrency accounting. | add: needs multi-instance state beyond today's single `state.json` |

**`run` flags** (this is where the design lives):

- `--image <digest>` — pinned, reproducible, auditable.
- `--repo <ref>` — seed a **fresh checkout at a SHA** (clean one-shot state).
- `--env KEY=VAL` — e.g. `ISSUE=<key>`, the brief path.
- `--cmd '<argv>'` — the workload (a headless agent invocation over the brief).
- `--timeout <dur>` — hard wall-clock cap; feeds the reaper and the token TTL.
- `--cpu/--memory/--disk` — cgroup limits for build-heavy classes.
- `--egress-profile <name>` — optional per-run tightening (e.g. a read-only class needs no code-host/registry egress).
- `--label k=v,…` — tag by issue/class so a restarted scheduler can find in-flight work.

## The run-parameter passthrough — the key addition

Per-task scoped secrets reuse at-cove's **existing** [secret mechanism](../OVERVIEW.md#configyml) unchanged — a `config.yml` secret is a `command` (argv array) run on the host at connect, whose stdout is injected in memory only.
The one missing piece is **context**:

> **at-cove must expose the run's parameters to the secret command's environment** when it runs the command at connect —
> e.g. `COVE_RUN_ISSUE`, `COVE_RUN_REPO`, `COVE_RUN_TIMEOUT`, `COVE_RUN_CLASS`.

With that single passthrough, a secret command becomes a **per-run minter**.
Nothing else about secret handling changes.

## Secret brokering — the parametrized minter

The code-host token secret's command becomes a minter that reads the run params and produces a scoped, expiring token:

```
# secret command for the code-host token, invoked by at-cove at connect
mint-token.sh:
  # The minting key (e.g. a GitHub App private key) lives on the control side,
  # never in the worker image.
  installation_token \
     --repo <org>/<repo> \                          # scope FIXED here, server-side
     --permissions contents:write,pull_requests:write \
     --ttl "${COVE_RUN_TIMEOUT:-30m}"               # run params tune ttl/labels only
```

- **Scope is fixed in the minter**, not derived from run params — so untrusted issue text cannot widen the token.
  Run params only tune TTL and labels.
- **TTL** ≥ run timeout, capped at the code host's maximum for such tokens (e.g. 1 hour for GitHub App installation tokens).
- The worker receives an **ephemeral token that can push a branch and open a PR on one repo**, and nothing else.
- The worker owns **all** code-host interaction (push + PR-open) so the scheduler stays entirely out of the code-host trust domain.

## Three separated authorities

No component holds two authorities; untrusted input reaches only the least-privileged one.

| Component | Holds | Never |
|-----------|-------|-------|
| **Scheduler** | the **tracker** API token (its own standing secret) | touches the code host |
| **Minter** (secret command, control side) | the **code-host minting key** | orchestrates |
| **Worker** | a short-lived, repo-scoped **code-host token** only | holds tracker creds, the minting key, or any long-lived credential |

The worker ingests the untrusted brief (issue + comments) and holds only a short-lived, push-and-PR-scoped token — the smallest blast radius in the system, further capped by the egress allow-list to the build/registry/code-host hosts.

## Worker contract

**Input:** a self-contained brief at a known path (assembled by the scheduler from the issue, linked spec/plan, and comment thread), plus `--env` context and the fresh checkout.

**Output:** a single structured file the worker writes, surfaced by `at-cove result`:

```json
// /out/result.json
{
  "status": "ok" | "needs_input" | "error",
  "issue": "<issue-key>",
  "class": "implement",
  "artifacts": {
    "branch": "<issue-key>-...",
    "prUrl": "https://<code-host>/<org>/<repo>/pull/…",
    "docPath": "docs/…"            // for plan/spec classes
  },
  "needsInput": {                  // present iff status == needs_input
    "doing": "…",
    "blocker": "…",
    "need": "…",
    "tried": "…",
    "safeState": "branch <issue-key>-… @ <wip-sha>"
  },
  "summary": "one-paragraph human-readable outcome",
  "usage": { "tokens": 0, "wallMs": 0 }
}
```

The worker **does no tracker I/O** — it writes `result.json`, pushes any branch/PR, and exits; the scheduler reads the result and performs **all** tracker writes (the single-writer property).

## The dispatch loop

```
on claim(issue):                       # scheduler is the only tracker-state writer
   run = at-cove run --detach \
           --image <pinned-digest> --repo <sha> \
           --env ISSUE=<key> --timeout 30m \
           --label issue=<key>,class=implement \
           --cmd '<headless agent over @/brief.md>'
   # at-cove mints the scoped code-host token at connect via the parametrized secret command
   record(issue ↔ run.id)

on tick / webhook:
   for run in at-cove ls --json where status == exited:
      r = at-cove result <run.id>
      case r.status:
        ok          → broker tracker: post artifacts (prUrl/doc), IN PROGRESS → IN REVIEW/DONE
        needs_input → broker tracker: post the ❓NEEDS INPUT comment from r.needsInput,
                      move → NEEDS INPUT, assign a human
        error       → per policy: retry once, else NEEDS INPUT
      at-cove rm <run.id>

reaper:
   for run past --timeout or unknown-to-scheduler:
      at-cove kill/rm <run.id>; broker tracker: issue → NEEDS INPUT
```

The scheduler formats the `❓ NEEDS INPUT` comment from the worker's `needsInput` payload; the worker never writes it.

## Isolation by handler class

- **Build-heavy classes (e.g. `implement`)** — **container-per-task**, one build per container (avoids build-daemon/resource contention), full `--cpu/--memory`, the scoped code-host token, a build `--egress-profile`. Clean state per run.
- **Light classes (e.g. `plan`)** — may pack into a shared warm container with a worktree per task, **no code-host token**, a minimal `--egress-profile`. Tiny blast radius.

A worktree isolates each task's checkout, but not the shared mutable state a multi-tenant container contends on — build-daemon home, package/dependency caches, lockfiles — which is why build-heavy tasks get their own container (the v1 default; see Decisions).

## Delta — what at-cove must add

This is **net-new roadmap** for at-cove, distinct from the currently-deferred items in [OVERVIEW's status & roadmap](../OVERVIEW.md#status-and-roadmap) (the `.local/` layer, Firecracker/Fly backends, declarative cloning). Modest and specific; mostly surfacing the container lifecycle through the hardened wrapper, plus one genuinely new state model:

1. **Non-interactive `run --detach`** returning a run-id (no TTY, no human).
2. **Per-run, multi-instance state + lifecycle verbs:** `status` / `result` / `logs` / `rm` / `ls` (`--json` where noted), addressed by run-id — replacing today's single per-kit `.state/state.json` with a registry of concurrent runs.
3. **Image pinning by digest** for reproducible, auditable runs.
4. **Run-parameter passthrough** into the secret command's environment — the unlock for per-task scoped secrets.
5. **Egress additions** (human-gated kit change): the code-host API host for PR-open (e.g. `api.github.com`) and the tracker API host (e.g. `api.linear.app`) for the scheduler's environment, which cannot use an interactive human-authenticated connection as a 24/7 service.
6. **Per-run `--egress-profile`** (nice-to-have) so light classes can drop code-host/registry egress entirely.

## Decisions

The dispatch substrate's own open questions, resolved for v1.

- **Minting runs inside at-cove's connect**, via the parametrized secret command. The scheduler never sees the token or the minting key, which is what keeps the [three authorities](#three-separated-authorities) genuinely separated — the alternative (scheduler mints and passes the value to `run`) is simpler but collapses code-host + tracker authority into one component. This is also the option that reuses at-cove's real connect-time secret flow unchanged; it only requires the run-param passthrough.
- **Container-per-task for all classes in v1.** Uniform and simple, and it matches at-cove's current one-container model — no worktree-multiplexing to build first. Packing light classes into a shared warm container with a worktree per task is a later cost optimization, not a v1 requirement; the isolation section above notes the shared mutable state it would have to solve.
- **Interactive attach path: deferred.** The sync classes (spec/review) run as ordinary host-side chat sessions — today's interactive [`at-cove connect`](../OVERVIEW.md#command-surface) — so no `at-cove attach` / `run --interactive` is needed for v1. The autonomous path does not depend on it.
