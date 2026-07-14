---
summary: Section index for the cove binaries' usage/reference docs — how to invoke each tool, its inputs and outputs, and the file/JSON contracts an operator or integrator needs.
read_when: You are running one of the cove binaries directly (not developing it) and need its command surface, flags, environment, and I/O contract.
owns: the map of the per-binary usage docs
prereqs: none — see ../OVERVIEW.md for what the project is
tier: section
updated: 2026-07-14
---

# Usage reference

Operator/integrator-facing usage for the cove binaries: the command surface, the
environment, and the concrete input/output contracts (with JSON Schemas). This is the
*how to run it* layer; for *why*, see [`../OVERVIEW.md`](../OVERVIEW.md) and the
[orchestration design](../orchestration/INDEX.md).

| Doc | What it covers | Read when |
|-----|----------------|-----------|
| [at-task.md](at-task.md) | The `at-task` git/PR worker — usage: the `prepare`/`complete`/`version` CLI, the `AT_TASK_GIT_TOKEN` credential, the `.at-task/` file handoff, and the JSON-or-YAML file-format rules. | You are invoking `at-task`, or need the handoff flow / file-format rules before reading a contract schema. |
| [at-task-inputs.md](at-task-inputs.md) | The JSON Schemas + examples for the two files `at-task` reads: `task.json` (the work spec) and `worker-result.json` (the worker's self-report). | You are building a `task.json`, or writing a worker that produces `worker-result.json`. |
| [at-task-output.md](at-task-output.md) | The JSON Schema + examples for `task-result.json` — `at-task`'s authoritative outcome (`ok`/`needs-input`/`error`) with the echoed `worker-result`. | You are consuming `at-task`'s result — brokering its status, or troubleshooting a run. |
| [at-cove-config.md](at-cove-config.md) | The `at-cove` kit `config.yml` schema — every field (name, source-control, tracker, dispatch, secrets, workers, collaborators, image), validation rules, and a full annotated example. | You are authoring or editing a kit's `.at-cove/config.yml`. |
| [at-cove-secrets.md](at-cove-secrets.md) | The demand/supply secret model — `config.yml` `secrets` are demand-only (name + description); the machine supplies values via `~/.config/at-cove/secrets.yml`/`secrets.local.yml` (`minters`/`global`/`kits`), the four supply sources, precedence, and the anti-mining trust boundary. | You are adding a secret to a kit, supplying a value from your machine, wiring a shared/global supply, or reasoning about secret trust. |
| [at-mint.md](at-mint.md) | The `at-mint` host-side token minter — the `github` (GitHub App installation token) and `anthropic` (Auth0→federation bearer) subcommands, their flags/env, and the flags=non-secret/env=secret/one-token-to-stdout/fail-closed contract. | You are configuring a machine-side secret supply that mints a token — a GitHub App installation token or an Anthropic WIF bearer. |
