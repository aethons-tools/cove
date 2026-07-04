---
summary: Product-agnostic design for a Linear-driven agent workflow that runs work from idea → spec → plan → implementation → PR → review → closeout. Every issue flows one uniform lifecycle (BLOCKED → READY → IN PROGRESS → IN REVIEW → DONE, plus NEEDS INPUT); the "stage" lives in the issue's identity and its handler class, not the state. A dedicated non-LLM scheduler (webhook-driven, poll-backstopped) claims ready work and dispatches it to one-shot LLM worker agents on hardened infrastructure; humans are surfaced their issues to drive in chat sessions.
read_when: You are building, operating, or extending Linear-based orchestration of software work — the uniform lifecycle, the fan-out into per-stage subissues, assignment by handler class, the dedicated scheduler and webhook receiver, the worker fleet, the stop-and-write-needs-back protocol, or dependency-gated readiness.
owns: the uniform issue lifecycle, the idea→issues→subissues fan-out model, assignment by handler class, the stage-agnostic orchestrator principle, the webhook-driven dedicated-scheduler dispatch architecture, the worker execution model, the stop-and-write-needs-back protocol, and dependency-gated readiness
prereqs: none — the companion at-cove-dispatch-interface.md covers the dispatch substrate this doc references
tier: leaf
updated: 2026-07-04
---

# Linear-Driven Agent Workflow

## Purpose

This document designs how work flows through an issue tracker (Linear) from an idea to a merged, closed-out change,
executed by a mix of **autonomous** (headless, one-shot) and **interactive** (human-in-chat) agents.
The goal is to maximize async progress:
autonomous agents run with no human present and are meant to one-shot their task,
stopping and writing their needs back to the tracker only when they genuinely cannot proceed;
interactive agents are chained to a live human chat for the work that is inherently collaborative.

The dispatch substrate — how the scheduler actually launches workers on hardened infrastructure — is specified in the companion [at-cove dispatch interface](at-cove-dispatch-interface.md).

## Scope decisions captured

Binding on everything below:

- **One uniform lifecycle for every issue.**
  Every issue — regardless of what kind of work it is — flows `BLOCKED → READY → IN PROGRESS → IN REVIEW → DONE`, with `NEEDS INPUT` as a side state.
  The lifecycle *state* does not encode the *stage*.
  This is very nearly a stock tracker workflow; only `NEEDS INPUT` is custom.
- **Work fans out as a dependency graph, not a fixed pipeline.**
  An idea is a task.
  Its spec'ing is *generative*: it defines the set of issues the idea needs.
  Each issue spawns per-stage subissues wired with `blockedBy` dependencies.
  There is no separate "workstream" layer for scheduling — the dependency graph is the schedule.
- **The stage lives in the issue's identity and its handler class**, not in the state.
- **Every issue/subissue is assigned to a handler class** (a role: a class of bot or human).
  The class — not the state — determines whether the work is autonomous or interactive.
- **Agents run only on our own hardened infrastructure** — concretely, [at-cove](../OVERVIEW.md) sandboxes: egress-locked, secrets injected in memory only, hardening layered on last.
  Compute never leaves our machines.
- **Dispatch is a dedicated, non-LLM scheduler service**, webhook-driven for liveness with a low-frequency poll as backstop.
  Only the *workers* are LLM agents.

## Security posture — "runs on our infra," and the one accepted inbound

The hard constraint is that **agents execute only on our hardened infrastructure**.
This is compatible with the tracker's own agent features, because the tracker **never runs your agent code** — it only sends events and receives results.
So the constraint was never really about where agents run;
the only thing ever at stake is the **inbound ingress** (tracker → us).

This design accepts **exactly one inbound component**: a thin, hardened, single-purpose **webhook receiver** at the edge that verifies the signature, enqueues, and does nothing else — no LLM, minimal surface.
Everything else — all tracker reads, writes-back, and claiming — is **outbound from the scheduler**, which is the **single writer**.
Full tracker-native agent identity (assignable app-user agents, live agent sessions) is **not** adopted;
it remains an optional future upgrade for in-tracker human chat, not a default.

**Credential model.** The workflow's one rule here: **workers hold no tracker credentials and do no tracker I/O** — they are pure compute returning a structured result, and the scheduler (the single writer) brokers every tracker write, so the untrusted brief reaches only the least-privileged component. The full three-authority split (scheduler / minter / worker), the per-task token minting, and the worker handoff schema are owned by the [at-cove dispatch interface](at-cove-dispatch-interface.md#three-separated-authorities).

## The uniform lifecycle

Every issue and subissue uses the same states.

| State | Type | Meaning |
|-------|------|---------|
| **BLOCKED** | backlog/unstarted | has unfinished `blockedBy` dependencies; not yet dispatchable |
| **READY** | unstarted | all blockers are `DONE`; eligible for its handler class to pick up |
| **IN PROGRESS** | started | a handler (bot worker or human session) is actively working it |
| **IN REVIEW** | started | work produced; awaiting the review handler for this issue |
| **DONE** | completed | accepted; closing it unblocks its dependents |
| **NEEDS INPUT** | unstarted | a handler hit a wall and wrote back; waiting on a human |
| *(Canceled / Duplicate)* | canceled | terminal |

```
              (blockers cleared, automatically)
  BLOCKED ─────────────────────────────────► READY
                                               │  handler class picks up
                                               ▼
                                          IN PROGRESS ───────► IN REVIEW ───────► DONE
                                               │                                    │
                                               │ hits a wall                        │ closing unblocks
                                               ▼                                    ▼  dependents → READY
                                          NEEDS INPUT ──(human answers, moves back to READY)──► re-dispatch
```

The **state transition is the handshake; comments are the payload.**
A human moving an issue out of `NEEDS INPUT` back to `READY` *is* the "your question is answered" signal —
the handler does not parse comment authorship to decide whether it may resume.

`BLOCKED → READY` is **automatic**: the scheduler flips it when the last blocker reaches `DONE`.
No human hand-manages the queue.

## Work structure — idea → issues → subissues

Three tiers, all scheduled by dependencies, no separate scheduling layer:

```
Idea (task)
  └─ spec'ing (generative) defines ─►  Issue A          Issue B          Issue C ...
                                          └─ subissues:     └─ subissues:
                                             spec ─► plan ─► implement ─► review
                                             (blockedBy edges chain the stages)
```

- **Idea = a task.** Its own spec'ing is the collaborative decomposition that emits the set of issues.
- **Issue = a unit of work** the idea needs. Issues carry `blockedBy` edges among themselves — including edges onto any **foundational tasks** (e.g. shared interface contracts) that must complete first.
- **Subissue = a stage of one issue** (spec, plan, implement, review), chained by dependencies so `implement` is `blockedBy` `plan` is `blockedBy` `spec`.
- **The template is collapsible.** Trivial work skips stages — a one-line fix does not get forced through spec + plan + review subissues. Match the ceremony to the risk.
- **Foundational contracts are ordinary blocking tasks.** They are the highest-fan-out nodes; nothing they gate goes `READY` until they are `DONE`.

## Handler classes and assignment

Every issue/subissue is assigned to a **handler class** — a role, not an individual.
The class determines the execution mode; the state does not.

Representative classes:

| Class | Mode | Typical stage |
|-------|------|---------------|
| `spec` (human) | interactive | spec subissues, idea decomposition |
| `plan` (bot) | autonomous | plan subissues |
| `implement` (bot) | autonomous | implement subissues (opens the PR) |
| `review` (human, or a *different* bot) | interactive / autonomous | review subissues |

Rules:

- **The review class must differ from the implement class** — separation of duties; a bot never approves its own PR.
- **Representation:** a **label** (`class:plan-bot`, `class:review-human`, …), read outbound by the scheduler. No tracker bot user is required.
  If nicer assignment UX is later wanted, classes can map to tracker app-user agents that the scheduler still reads outbound — adopting that is independent of turning on live agent sessions.

## The orchestrator is stage-agnostic

Because mode lives in the handler class and the lifecycle is uniform, the scheduler contains **no stage logic**. Its entire decision is:

> For each `READY` issue: read its handler class.
> An **autonomous** class → enqueue a dispatch job for a worker.
> An **interactive** class → assign the human and notify; a person will drive it from a chat session.

All stage-specific behavior lives in the **handler**, selected by class. Adding a new stage or class never touches the scheduler.

## Dispatch architecture

A dedicated, non-LLM scheduler service; a thin webhook receiver; a durable queue; a fleet of LLM worker agents on hardened at-cove containers. The tracker is the single source of truth for state.

```
   Tracker  (source of truth: uniform state + blockedBy graph + artifacts + comments)
     ▲  │        ▲                                            outbound writes
     │  │ events │ (webhooks)                                 (claims, results)
     │  ▼        │
     │  ┌─────────────────┐        ┌──────────────────────────┐
     │  │ Webhook receiver │ ─────► │  Dedicated scheduler      │
     │  │ (thin, hardened, │ enqueue│  (non-LLM service)        │
     │  │  edge; verify →  │  wake  │  • BLOCKED→READY (auto)   │
     │  │  enqueue → done) │        │  • claim READY→IN PROGRESS │
     │  └─────────────────┘        │    (single writer)        │
     │        ▲                    │  • enqueue autonomous jobs │
     └────────┘ backstop poll ─────│  • assign+notify humans    │
       (low-frequency reconcile)   │  • reap stale IN PROGRESS  │
                                   └────────────┬──────────────┘
                                                │ dispatch jobs
                                                ▼
                                           ┌─────────┐
                                           │  Queue  │  durable, on our infra; single delivery
                                           └────┬────┘
                                                ▼
                                 ┌────────────────────────────────┐
                                 │  Worker fleet (at-cove containers) │
                                 │  • LLM agent, one-shot            │
                                 │  • fresh checkout, scoped token    │
                                 │  • no tracker I/O (broker)        │
                                 │  • → /out/result.json:             │
                                 │      ok{prUrl} | needs_input{…}     │
                                 └────────────────────────────────┘
```

**Components:**

- **Webhook receiver** — the one inbound component. Verifies the signature, enqueues an event, wakes the scheduler. No business logic, no LLM.
- **Dedicated scheduler (non-LLM)** — talks the tracker API directly. It is the **only writer of claims**, which makes claiming race-free (below). Responsibilities: auto `BLOCKED → READY`; claim `READY → IN PROGRESS` and enqueue for autonomous classes; assign + notify for interactive classes; reap stale claims. Runs cheaply 24/7.
- **Dispatch queue** — durable, on our infra. Its **single-delivery** guarantee means exactly one worker gets each job — no distributed lock, no compare-and-swap against the tracker.
- **Worker fleet** — hardened at-cove containers running the LLM agents. Each pulls a job, works from a **self-contained brief** the scheduler assembled (issue description + linked spec/plan + comment thread), runs the class-specific handler in a fresh checkout, one-shot. A worker holds **no tracker credentials** and does no tracker I/O; it returns a structured `/out/result.json` and the scheduler brokers every tracker write. See the [at-cove dispatch interface](at-cove-dispatch-interface.md).

**Trigger model:** **webhooks for liveness, poll as backstop.** The receiver wakes the scheduler on events for near-instant dispatch; a low-frequency reconcile poll catches anything missed (dropped webhooks, restarts) so the system never wedges waiting on a lost event.

**Claiming is race-free** because the single scheduler is the only claim-writer and the queue delivers each job once. The tracker stays the source of truth for *state*; the queue is only the *dispatch channel*.

## Execution modes

**Autonomous (bot classes) — Plan, Implement, and other headless stages.**
A worker pulls the job, runs one-shot with the right handler (planning for plan; implement-and-test for implement, ending by opening the PR — branch-first, never the default branch, one PR per issue).
On success it writes its artifacts (plan doc path; branch and PR URL) to `/out/result.json`; the scheduler reads the result and brokers the tracker update — post artifacts, and move `IN PROGRESS → IN REVIEW` (or `→ DONE` for stages with no review).
On a wall it runs the stop-and-write-needs-back protocol.

**Interactive (human classes) — Spec, and human Review.**
The scheduler does not run anything; it assigns the issue to the right human and notifies via the tracker.
The human opens a chat session on our infra pointed at the issue, pulls context, runs the appropriate handler (decomposition/spec for spec; review for review), collaborates live, commits the artifact, posts a summary, and transitions the state.

## Stop-and-write-needs-back protocol

The defining behavior of the autonomous mode. When a worker cannot proceed — an ambiguous requirement, a missing decision, an egress wall, a gate it cannot get green, or an out-of-scope discovery — it does **not** guess or thrash. It:

1. **Stops.**
2. Emits a **structured `needs_input` payload** in `/out/result.json` using a fixed template:
   - **Doing:** what it was working on.
   - **Blocker:** the specific thing preventing progress.
   - **Need:** exactly what it needs from a human, phrased so a one-line reply unblocks it.
   - **Tried:** what it already attempted.
   - **Safe state:** branch name, WIP commit pushed (or nothing changed).
3. Leaves the branch in the documented safe state so no work is lost, and **exits cleanly.**

The **scheduler** then performs the tracker writes: it posts the `❓ NEEDS INPUT` comment formatted from the payload, moves the issue to **NEEDS INPUT**, and **assigns a human**. It never dispatches a `NEEDS INPUT` issue.

**Resume:** the human answers in-thread and moves the issue back to `READY`. On the next dispatch the worker re-runs with the whole thread (answer included) as context, resuming from the WIP branch. This round-trip *is* the asynchronous "chat" for rare clarification: a durable, auditable Q&A anchored to the issue, not a live conversation.

## Guardrails

- **Branch-first, never the default branch, one PR per issue.**
- **Container-per-task isolation** for build-heavy classes, fresh checkout each run; isolation-by-class and credential scoping are specified in the [at-cove dispatch interface](at-cove-dispatch-interface.md).
- **Egress walls are a first-class `NEEDS INPUT` reason.** If a build needs a domain not on the allow-list, the worker reports it as a `needs_input` result ("add X to the egress kit") rather than improvising — the scheduler surfaces it, turning a sandbox limit into a clean handoff.
- **Stale-claim reaper:** the scheduler moves issues stuck in `IN PROGRESS` past a timeout with no progress (a crashed or hung worker) to `NEEDS INPUT`.
- **Bounded one-shot budget:** each autonomous job has a token/time ceiling; exceeding it routes to `NEEDS INPUT`, never an unbounded burn.

## How this leverages the tracker

- **`blockedBy` = the scheduler's read side.** The dependency graph is the schedule, running itself; finishing an issue auto-unblocks its dependents.
- **Human-class dispatch is free** — the tracker's own assignment + notifications surface the issue; no custom notifier.
- **Sub-issue rollup** gives the idea-level progress view for nothing.
- **Code-host integration** auto-links the PR to the issue on `IN REVIEW`.
- **App-user agents** remain available purely as a nicer class marker (read outbound), adoptable without ever enabling live agent sessions.

## Bootstrapping order

1. **Set up the tracker:** the uniform states (add only `NEEDS INPUT` to stock), the handler-class labels, the `❓ NEEDS INPUT` comment template, and the code-host link integration. Seed any **foundational contract tasks** and wire their dependency edges as `blockedBy` relations.
2. **Interactive path first (zero automation):** conventions + your chat handlers for the human `spec` and `review` classes.
3. **Autonomous MVP:** the webhook receiver + a minimal scheduler that claims one `READY` `implement` issue → enqueues → a worker runs it → PR or `NEEDS INPUT`.
4. **Harden:** add the `plan` class, the backstop poll, the stale-claim reaper, budgets, resume-from-WIP, and the optional plan-approval gate.

## Decisions

Resolved for v1; each was an open question in the extracted design.

- **Scheduler and webhook receiver are two processes.** The inbound surface stays minimal and independently hardenable, mirroring at-cove's own "exactly one accepted inbound" posture — the receiver verifies, enqueues, and does nothing else, while all outbound tracker I/O lives in the scheduler. The slight operational cost of a second process is worth the smaller attack surface.
- **Native app-user agents / live agent sessions: deferred, out of scope for v1.** Letting humans drive the interactive classes *inside the tracker UI* in real time requires the full native-agent path; the design works fully without it. Handler classes remain plain labels the scheduler reads outbound, adoptable as app-user markers later without ever enabling live sessions.
- **Plan-approval gate: off by default.** The default is maximum async — `plan → implement` proceeds automatically. Requiring a human to approve the plan first is an **opt-in per-issue/per-class policy** (e.g. a `gate:plan-approval` label the scheduler honors), not a global default.
