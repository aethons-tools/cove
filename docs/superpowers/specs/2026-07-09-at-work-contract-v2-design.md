# at-work contract v2 — the redesigned `.at-work/` contract — Design

**Date:** 2026-07-09
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-work`, `at-cove`, `at-dispatch`)
**Tracks:** child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic
**Supersedes/revises:** the shipped at-work contract ([AET-26](https://linear.app/aethons-tools/issue/AET-26)) and its dispatch/scheduler wiring ([AET-21](https://linear.app/aethons-tools/issue/AET-21)).

## 1. Purpose

Re-implement at-work's input/output contract to match the reconciled usage docs, which
are now the **canonical contract**:
- [`docs/usage/at-work.md`](../../usage/at-work.md) — the CLI, the `.at-work/` handoff, and the JSON-or-YAML / unknown-field rules.
- [`docs/usage/at-work-inputs.md`](../../usage/at-work-inputs.md) — the `task.json` and `worker-result.json` schemas.
- [`docs/usage/at-work-output.md`](../../usage/at-work-output.md) — the `task-result.json` schema.

The shipped worker uses a **flat** JSON contract (`input.json`/`outcome.json`/`output.json`,
string statuses, an at-work-written `brief.md`, path-argument CLI). The redesign replaces it
with a **nested `.at-work/` contract** (`task.json`/`worker-result.json`/`task-result.json`),
**tagged-union statuses**, **JSON-or-YAML**, a **no-path-args CLI**, and the **`task.json`-direct**
worker handoff (no `brief.md`). It ripples into `at-cove dispatch`, the reference kit, and the
scheduler. This spec references the usage docs for exact schemas rather than restating them.

## 2. Governing decisions

- **Files:** `.at-work/{task,worker-result,task-result}.{json,yml}`. `brief.md` is removed —
  the worker reads its instructions straight from `task.task.brief` in `task.json`.
- **Class axes:** `worker.class` = the scheduler's handler class (`spec`/`plan`/`implement`/
  `review`); `task.class` is optional and **omitted for the MVP** (a later enhancement can
  fill it from the issue's type/labels).
- **PR ownership:** the **worker** owns the PR — title/body come from
  `worker-result.status.ok.pull-request`; at-work no longer constructs `"<key>: <title>"`.
  An `ok` with no `pull-request` → at-work pushes the branch and opens **no** PR
  (`task-result` `ok` without `pr-url`). No at-work fallback title.
- **Retrace SHA:** `task-result.status.needs-input` carries `commit` — the pushed WIP SHA,
  computed by at-work (the worker doesn't know it). Stable even if the branch later moves.
- **Dispatch seam:** `at-cove dispatch --in <local task file> --out <local task-result file>`.
  The scheduler only constructs/reads **local** files and calls dispatch; the **kit's
  `dispatch:` config** coordinates how those files land in / come out of the VM's `.at-work/`.
  at-cove stays VM-layout-generic; the scheduler stays VM-agnostic.
- **Statuses are tagged unions** (`status: { ok | needs-input | error }`, exactly one).
  **Strict** parsing (unknown field → error) for `task.json` and `task-result.json`;
  **lenient** for `worker-result.json` (accepts any fields the worker emits). Files may be
  **JSON or YAML** (error if both extensions exist); at-work **writes `task-result` in the
  `task` file's extension**.

## 3. Component changes

**`internal/dispatch/worker` (types + I/O) — the core rewrite.**
- `Input`: flat `Issue{Key,Title,WorkClass,Brief}`/`Repo{…}` → nested `issue{key,title}`,
  `repo{host?,name,source-branch,work-branch}`, `worker{class}`, `task{class?,brief}`.
- `WorkerResult`/`TaskResult`: string-status `Outcome`/`Output` → **tagged unions** (see
  §4). `TaskResult` echoes the raw `worker-result` (pass-through, unconstrained).
- I/O: `encoding/json` (lenient) → **`gopkg.in/yaml.v3`** (already the only dependency) —
  detect `.json`/`.yml` under `.at-work/` (error if both), parse strict (`KnownFields`) for
  task/task-result and lenient for worker-result, and write `task-result` mirroring the task
  file's extension. New path helpers for the three filenames; **`WriteBrief` deleted**.

**`cmd/at-work`.** `prepare <input.json>` / `complete <in> <out>` → **no path args**; both
operate on the fixed `.at-work/` files in the cwd. The cli-registry closures call
`doPrepare(args)`/`doComplete(args)` with zero positional args.

**`worker/prepare.go` + `complete.go`.** `prepare` drops the `WriteBrief` call (repo setup
only). `complete` switches on the tagged-union status; PR title/body come from the worker's
`pull-request` (no construction; PR optional); on `needs-input` it records the `commit` SHA;
`task-result` echoes the worker-result.

**Kit `dispatch:` config + `internal/dispatchrun`.** The kit's `dispatch:` block gains the
VM-side input/output paths (where at-cove places the injected task file and finds the result,
e.g. `.at-work/task.json` / `.at-work/task-result.json`); `dispatchrun` uses them instead of
the hardcoded `/in/input.json` / `/out/output.json`. `at-cove dispatch` takes **local**
`--in`/`--out`.

**Reference kit (`kits/reference-worker`).** `run-worker.sh`: `at-work prepare` / `complete`
lose their path args. `run-agent.sh`: point the worker at `.at-work/task.json` (brief =
`task.brief`) and emit `.at-work/worker-result.json` in the **new tagged-union shape**.

**`internal/dispatch/scheduler`.** `handle` builds the nested `Input` (`worker.class` = the
handler class, `task.brief` = the assembled brief). `broker` + comment builders read the
tagged-union `TaskResult` (`status.ok.pr-url`) and the echoed `worker-result.status.needs-input`
(doing/blocker/need/tried) plus the `commit` SHA. The scheduler writes a local `task.json`
and reads a local `task-result.json` (no VM paths).

## 4. Tagged-union representation (Go)

Each union is a struct with a pointer per variant plus an "exactly-one" validator:

```go
type WorkerStatus struct {
    OK         *WorkerOK    `json:"ok,omitempty" yaml:"ok,omitempty"`
    NeedsInput *NeedsInput  `json:"needs-input,omitempty" yaml:"needs-input,omitempty"`
    Error      *StatusError `json:"error,omitempty" yaml:"error,omitempty"`
}
// Active() returns the single set variant (or an error if zero/multiple are set).
```

`TaskResult.status` uses the same pattern; `TaskResult.worker-result` is a raw pass-through
(`json.RawMessage` / `yaml.Node`) so lenient worker content survives the echo unchanged and
gets re-serialized in the task file's format.

## 5. Testing

Hermetic as today: `prepare` against a local bare repo; `complete` with a prepared checkout +
a chosen `worker-result` + a fake `CodeHost`. New coverage: **JSON and YAML** round-trips for
each file; **strict-reject** on an unknown field in `task.json`/`task-result.json` and
**lenient-accept** on extra fields in `worker-result.json`; tagged-union marshal/unmarshal and
the exactly-one validator; the extension-mirror on write. The reference-kit end-to-end stays a
maintainer-run `integration` step.

## 6. Non-goals (deferred)

`task.class` from issue metadata; `repo.host` beyond an informational field (implementation
stays **GitHub-only**); multi-code-host; the scoped-token minter (AET-24); any change to the
credential air-gap (`AT_WORK_GIT_TOKEN`, `prepare`/`complete` only).

## 7. Decomposition (plans)

Sizable — sequence into four hermetic plans:
1. **worker types + I/O** — nested `Input`, tagged-union `WorkerResult`/`TaskResult`, YAML-or-JSON,
   strict/lenient, `.at-work/` filenames, `WriteBrief` removed; `cmd/at-work` no-args.
2. **`prepare`/`complete` semantics** — no `brief.md`; PR-from-worker; `commit` SHA; status derivation.
3. **dispatch seam + reference kit** — kit `dispatch:` VM paths, `dispatchrun`, `run-worker.sh`/`run-agent.sh`.
4. **scheduler** — nested `Input` construction + tagged-union broker (echoed worker-result, `commit` in the needs-input comment).
