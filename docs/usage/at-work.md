---
summary: at-work usage — the prepare/complete/version CLI, the AT_WORK_GIT_TOKEN credential, and the cwd file handoff under .at-work/ (JSON or YAML): task.json in; worker-result.json and task-result.json out. The per-file JSON Schemas live in the linked contract docs.
read_when: You are invoking at-work directly, or you need the file-handoff flow and the JSON/YAML file-format rules before reading a contract schema.
owns: the at-work CLI usage, the .at-work/ file handoff, and the JSON/YAML file-format + unknown-field rules (the per-file JSON Schemas are owned by at-work-inputs.md and at-work-output.md)
prereqs: none — for how at-work is dispatched inside a sandbox see ../orchestration/at-cove-dispatch-interface.md
tier: leaf
updated: 2026-07-09
---

# `at-work` usage

`at-work` turns one unit of work into a pull request. 
It owns all git and code-host interaction (clone, branch, commit, push, open PR)
**around** a **worker** run that something else drives — at-work never runs the worker itself.
The *worker* is whatever performs the work: today an LLM agent, but equally a deterministic
process or a human. The handoff is a file convention in the **current directory**, which
should be the root of the code repo to work on.

For one "unit of work", the calling orchestrator:

1. Writes the work spec to `.at-work/task.json`
2. Executes `at-work prepare`, which puts the code repo in the correct state
   (cloned, checked out to the work branch)
3. Executes the **worker** (an agent, a script, or a human), pointed at `.at-work/task.json`
   (its instructions are the `task.brief` field; the rest is context); the worker writes its
   result to `.at-work/worker-result.json`
4. Executes `at-work complete`, which:
   * Reads `.at-work/task.json` and `.at-work/worker-result.json`
   * Pushes the branch
   * If the result indicates success, opens a PR/MR; writes `.at-work/task-result.json` with PR/MR info
   * If the result indicates input is needed, writes `.at-work/task-result.json` with context information
   * Otherwise, writes `.at-work/task-result.json` with information about the error that occurred

For the design rationale and how it is dispatched in a hardened
sandbox, see the [dispatch interface](../orchestration/at-cove-dispatch-interface.md).

## Commands

`at-work` runs in the **current working directory** — the root of the target repo — and
communicates entirely through files under `.at-work/`; it takes **no path arguments**. One
environment variable is required by `prepare` and `complete`:

- `AT_WORK_GIT_TOKEN` — a code-host API token for the target repo, scoped for read, push,
  and PR/MR creation. Never passed on argv or logged.

### `at-work prepare`

Put the repo in the right state for the worker.

Reads `.at-work/task.json`. Clones the repo if the directory has no checkout, syncs
`repo.source-branch`, then creates `repo.work-branch` — or fast-forwards it, if it already
exists from a prior round. Refuses a dirty checkout, or a `work-branch` equal to
`source-branch`. (`prepare` does no content extraction — the worker reads the brief straight
from `.at-work/task.json`.)

*Exit:* `0` ready · `1` a setup or IO failure · `2` bad usage.

### `at-work complete`

Broker the worker's result into a branch push and, on success, a pull/merge request.

Reads `.at-work/task.json` and `.at-work/worker-result.json`, pushes `work-branch`, and acts
on the worker's `status`: `ok` → open the PR/MR (returning an existing one if already open);
`needs-input` → leave the pushed WIP branch, no PR; `error` → no PR. It **always** writes a
`task-result`, deriving the top-level `status`: `ok` when the worker said `ok` (carrying
`pr-url` if a PR was opened); `needs-input` when the worker said so; otherwise `error` —
including when `worker-result.json` is missing or invalid, or at-work itself fails. This holds
even when `.at-work/task.json` itself is missing or unreadable: `complete` can no longer tell
which extension `task-result` should mirror, so it writes `.at-work/task-result.json` (JSON is
the default) with an `error` status describing the read failure — the orchestrator always gets
a structured result, never nothing.

*Exit:* `0` — even for a `needs-input`/`error` result; the status lives in the file · `1`
only if the `task-result` write itself fails (there is then no result to deliver) · `2` bad
usage (extra arguments).

### `at-work version`

Print `at-work <version>` and exit.

## File format

The `.at-work/` contract files may be **JSON or YAML** (`task.json` or `task.yml`, and so
on). at-work parses either — YAML 1.2 is a superset of JSON, so JSON content is valid YAML —
and **errors if both** the `.json` and `.yml` form of the same file exist. The per-file JSON
Schemas (in the linked contract docs) describe the **shape**; they apply to either
serialization. YAML is the readable choice for `task.brief`, which becomes a block scalar
instead of an escaped one-liner:

```yaml
task:
  brief: |
    Read `docs/specs/add-x.md` and implement.
    Keep the change minimal; add a test.
```

- The **caller** picks the format of `task.{json,yml}`; the **worker** picks the format of
  `worker-result.{json,yml}` (JSON is often the robust choice for a machine to emit).
- **at-work writes `task-result` in the `task` file's extension** — a `task.yml` yields a
  `task-result.yml`, rendering even a JSON `worker-result` echo as readable YAML, which makes
  a `needs-input` handoff pleasant to troubleshoot.

**Unknown fields:** at-work **rejects** unknown fields in `task.json` and `task-result.json`
(closed schemas); `worker-result.json` is the deliberate exception — it **accepts any fields**
the worker emits, and at-work reads only the recognized ones.

**YAML caution:** quote any value that could read as a bool/number/date (a branch named
`no`, `1.0`, or `2026-07-09`) so it stays a string. This doc names the files `.json` for
brevity; everything applies equally to `.yml`.

## Contract files

Three `.at-work/` files, each with its JSON Schema and examples:

| File | Written by | Read by | Purpose |
|------|-----------|---------|---------|
| `task.json` | orchestrator | `prepare`, worker, `complete` | the work spec — issue, repo, worker/task classes, brief |
| `worker-result.json` | worker | `complete` | the worker's self-report — `ok` / `needs-input` / `error` |
| `task-result.json` | `complete` | orchestrator | at-work's authoritative outcome + the echoed `worker-result` |

- [**at-work inputs**](at-work-inputs.md) — the `task.json` and `worker-result.json` schemas + examples.
- [**at-work output**](at-work-output.md) — the `task-result.json` schema + examples.
