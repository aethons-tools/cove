---
summary: Section index for a reusable, product-agnostic agent-orchestration design — a Linear-driven workflow that runs work from idea to closeout using autonomous one-shot agents and human-chained agents, dispatched onto hardened at-cove containers with brokered least-privilege credentials.
read_when: You are seeding or evaluating the at-cove agent-orchestration design and need the map of its three documents and how they relate.
owns: the map of the agent-orchestration design cluster and the per-project inputs the design deliberately leaves open
prereqs: none — but this design layers on at-cove itself; see ../OVERVIEW.md for what at-cove is
tier: section
updated: 2026-07-08
---

# at-cove Agent Orchestration — Design

This directory holds the **product-agnostic** design for running software work through an issue tracker with a mix of autonomous and human-in-the-loop agents, all executing on hardened at-cove infrastructure.

It was extracted from a specific project's design and **genericized**: anything particular to the originating product (its languages, its build toolchain, its specific "interface contracts," its repo name and egress domains) has been removed or marked as *per-project*. What remains is the reusable machine.

The **workflow** (idea → issues → lifecycle → closeout) remains a forward-looking, product-agnostic design. The **dispatch substrate** it rides on is now largely **shipped** in this repo — `at-cove dispatch`, the `at-work` worker, and the `at-dispatch` scheduler — with the per-task token minter still deferred; the dispatch-interface and config docs below describe that shipped substrate. For what at-cove itself does, start at [`../OVERVIEW.md`](../OVERVIEW.md).

## The three documents

| Doc | What it owns | Read when |
|-----|--------------|-----------|
| [linear-agent-workflow.md](linear-agent-workflow.md) | The workflow: the uniform issue lifecycle, the idea → issues → subissues fan-out, assignment by handler class, the dedicated-scheduler dispatch model, the stop-and-write-needs-back protocol, dependency-gated readiness. | You need the *what and why* — how work flows and how agents are scheduled. |
| [at-cove-dispatch-interface.md](at-cove-dispatch-interface.md) | The shipped substrate: the `at-cove dispatch` command, at-cove's host-orchestrated worker bracket + credential air-gap, at-work's `.at-work/` task.json → task-result.json worker contract, the three-authority credential model, and per-class isolation (the per-task minter is deferred). | You need the *how* — the concrete contract by which the scheduler launches workers on at-cove. |
| [scheduler-config.md](scheduler-config.md) | The at-dispatch configuration schema: tracker wiring, repo metadata, handler-class-to-kit binding, concurrency/timeout settings, secret resolution, and loading/validation. | You are setting up an at-dispatch instance, adding a class, configuring state mappings, or adjusting timeouts. |

Read the workflow first; it references the dispatch interface for mechanics. The config document is a reference for operators.

## The idea in one paragraph

An issue tracker (Linear) is the single source of truth. Every issue flows one uniform lifecycle. A **dedicated, non-LLM scheduler** watches the tracker (webhook-driven, poll-backstopped), and for each ready issue reads its **handler class**: an autonomous class is dispatched as a **one-shot LLM worker** in a fresh hardened at-cove container; a human class is surfaced to a person who drives it in a chat session. Workers are **pure compute** — they hold no tracker credentials and only a short-lived, narrowly-scoped token minted per task; they return a structured result and the scheduler brokers every tracker write. When an autonomous worker cannot proceed, it **stops and writes its need back**, and a human unblocks it by moving the issue — the state transition is the handshake.

## What you must supply per project

These are intentionally *not* in the design:

- The **image** contents (build toolchain) and its `allowed-domains` egress list.
- The **repo** the workers operate on.
- Any **foundational tasks** (e.g. shared interface contracts) that should block others, wired as `blockedBy` relations.
- The **handler classes** you need beyond the canonical `spec` / `plan` / `implement` / `review`.
