---
summary: Section index for the cove binaries' usage/reference docs — how to invoke each tool, its inputs and outputs, and the file/JSON contracts an operator or integrator needs.
read_when: You are running one of the cove binaries directly (not developing it) and need its command surface, flags, environment, and I/O contract.
owns: the map of the per-binary usage docs
prereqs: none — see ../OVERVIEW.md for what the project is
tier: section
updated: 2026-07-08
---

# Usage reference

Operator/integrator-facing usage for the cove binaries: the command surface, the
environment, and the concrete input/output contracts (with JSON Schemas). This is the
*how to run it* layer; for *why*, see [`../OVERVIEW.md`](../OVERVIEW.md) and the
[orchestration design](../orchestration/INDEX.md).

| Doc | What it covers | Read when |
|-----|----------------|-----------|
| [at-work.md](at-work.md) | The `at-work` git/PR worker: the `prepare`/`complete`/`version` CLI, the `AT_WORK_GIT_TOKEN` credential, the `.at-work/` file handoff, and the JSON Schemas for `input.json`, the agent's `outcome.json`, and `output.json`. | You are invoking `at-work`, writing an agent that produces `.at-work/outcome.json`, or building/consuming its `input.json`/`output.json`. |
