# at-work — the air-gapped worker — Design

**Date:** 2026-07-07
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binary `at-work`)
**Tracks:** [AET-26](https://linear.app/aethons-tools/issue/AET-26) (child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic)
**Related:** [orchestration design](../../orchestration/INDEX.md); the [scheduler MVP](2026-07-06-at-dispatch-scheduler-mvp-design.md) that ultimately consumes a worker's result.

## 1. Purpose

Design **`at-work`**: the worker that turns a unit of work into a pull request.
Given a structured brief, it clones the repo, runs a coding **agent** over a fresh
branch, then commits, pushes, and opens a PR — handing back a structured result.

The defining property is an **air-gap**: the agent (which ingests the untrusted
brief) only edits and tests files; it never holds the code-host credential and never
pushes. `at-work` owns all git and the token. `at-work` is also **agent- and
VM-agnostic** — it hardcodes no agent, model, image, or egress; the **kit** supplies
those, and a handler **class → kit** mapping (elsewhere) selects them.

## 2. Governing decisions (from brainstorming)

- **Scope: the `at-work` binary only.** It runs standalone (and is fully
  hermetically testable). Hosting it in an at-cove VM (the `at-cove run` delta + a
  dispatch wrapper) is deferred to AET-21; the scoped-token minter is deferred to
  AET-24 (the MVP uses a plain token). MVP class: **`implement`**. Code host:
  **GitHub only**.
- **`at-work` owns clone → branch → commit → push → PR.** Autonomous agents launch
  into an empty VM, so `at-work` sets up the whole checkout from its input.
- **The agent is air-gapped:** it edits/tests files in the checkout only; `at-work`
  runs it with the code-host token **stripped** from its environment.
- **`at-work` is agent-agnostic:** it execs a kit-declared agent command; it never
  names `claude` or a model.
- **`at-work` has its own contract** (`input.json`/`output.json`), decoupled from the
  scheduler's `DISPATCH_*`/`result.json` contract. A future wrapper bridges the two.

## 3. The contract

```
at-work run <input.json> <output.json>
at-work version
```

**`input.json`** — everything the worker needs, structured:

```json
{
  "issue": { "key": "AET-33", "title": "Add thing X", "work-class": "implement", "brief": "…full markdown brief…" },
  "repo":  { "name": "aethons-tools/loom", "source-branch": "main", "work-branch": "implement/AET-33" }
}
```

**`output.json`** — a top-level `status` (authoritative; the consumer reads this),
an optional `message`, and two detail blocks: `agent` (the agent's self-report) and
`work` (what `at-work` mechanically did):

```json
// OK — agent finished; at-work pushed + opened the PR
{ "status": "OK", "message": "opened PR #42",
  "agent": { "status": "OK", "pr-message": "…proposed PR body…" },
  "work":  { "branch": "implement/AET-33", "pr-url": "https://github.com/aethons-tools/loom/pull/42" } }

// NEEDS_INPUT — agent stopped; at-work pushed the WIP branch, no PR
{ "status": "NEEDS_INPUT",
  "agent": { "status": "NEEDS_INPUT", "needs-input": { "doing":"…","blocker":"…","need":"…","tried":"…" } },
  "work":  { "branch": "implement/AET-33", "safe-state": "implement/AET-33 @ <sha>" } }

// ERROR — agent reported failure, or at-work hit a mechanical failure (clone/push/PR/agent-crash)
{ "status": "ERROR", "message": "clone failed: …",
  "agent": { "status": "ERROR", "message": "…" },
  "work":  { "branch": "implement/AET-33" } }
```

`at-work` **always** writes a valid `output.json` with a top-level `status`, so its
caller can always act. **Status derivation:** `OK` iff `agent.status == OK` **and** a
PR was opened (`work.pr-url` set); `NEEDS_INPUT` iff the agent reported it; otherwise
`ERROR`. A missing/invalid agent output, or any mechanical failure, is `ERROR` with a
synthesized `message`.

**Environment:**

```shell
# set by the caller (at-cove via kit config / image):
AT_WORK_GIT_TOKEN=…                     # code-host read/write/PR token (per-task secret, memory-only)
AT_WORK_AGENT_COMMAND="run-claude.sh"   # the kit's agent; baked into the image at build time
AT_WORK_TIMEOUT=30m                     # optional wall-clock cap for the agent (default 30m; the outer at-cove run also caps)

# set BY at-work FOR the agent (agent inherits these; the token is NOT among them):
AT_WORK_BRIEF=<path>                    # at-work writes issue.brief here for the agent to read
AT_WORK_AGENT_OUTPUT=<path>             # the agent writes its "agent" block here
```

## 4. The at-work ↔ agent seam

This is what keeps `at-work` agent-agnostic:

- `at-work` execs `sh -c "$AT_WORK_AGENT_COMMAND"` with **cwd = the checkout**, the
  token **stripped**, and `AT_WORK_BRIEF`/`AT_WORK_AGENT_OUTPUT` set. It errors early
  (`ERROR`) if `AT_WORK_AGENT_COMMAND` is unset.
- The **agent** reads the brief, edits and tests files in place, and writes an
  **agent block** to `$AT_WORK_AGENT_OUTPUT`:
  `{ "status": "OK", "pr-message": "…" }` on success, or
  `{ "status": "NEEDS_INPUT", "needs-input": {doing,blocker,need,tried} }` when it
  must stop, or `{ "status": "ERROR", "message": "…" }`.
- The agent does **no git** and holds **no token**. If it writes no valid block
  (crash), `at-work` treats the run as `ERROR`.

## 5. Flow (`implement`)

1. **Parse** `input.json` (required fields; else `ERROR`).
2. **Clone** `repo.name` at `source-branch` into a work dir (token via a temp
   `GIT_ASKPASS`, never on argv); **create** `work-branch` off it. Refuse if
   `work-branch` is empty or equals `source-branch` (branch-first guardrail — never
   push the base).
3. **Write** `issue.brief` to the `AT_WORK_BRIEF` file.
4. **Run the agent** (air-gapped, bounded by `AT_WORK_TIMEOUT`); read its block.
5. **Finish** by the agent's status:
   - `OK` → require changes (else `ERROR`); commit; push `work-branch`; **open a PR**
     (base `source-branch`, head `work-branch`, title `"<key>: <title>"`, body =
     `pr-message`); write `OK` with `work.pr-url`.
   - `NEEDS_INPUT` → commit any WIP; push `work-branch` (preserve safe state); write
     `NEEDS_INPUT` with `work.safe-state = <work-branch> @ <sha>`. **No PR.**
   - `ERROR` → write `ERROR`.
6. **Safety-net:** any unexpected failure still writes a valid `ERROR` `output.json`.

## 6. Architecture

Plan/execution split (as in the scheduler): an orchestrator driven by three
interfaces, all faked in hermetic tests; real adapters are thin.

```
cmd/at-work/                 the binary (run / version)
internal/dispatch/worker/    orchestrator (Run), Input/Output types, Agent/Git/CodeHost interfaces, fakes
internal/dispatch/github/    real CodeHost: open a PR via the GitHub API (live calls behind the `integration` tag)
```

```go
type Agent interface {
    // Run execs the kit's agent over dir with the token stripped, giving it the
    // brief and an output path; returns the agent's parsed block (or an error).
    Run(ctx context.Context, dir, briefPath, outputPath string) (AgentBlock, error)
}
type Git interface {
    Clone(ctx context.Context, repo, sourceBranch, dir string) error
    NewBranch(ctx context.Context, dir, branch string) error
    HasChanges(ctx context.Context, dir string) (bool, error)
    Commit(ctx context.Context, dir, msg string) (sha string, err error)
    Push(ctx context.Context, dir, branch string) error
}
type CodeHost interface {
    OpenPR(ctx context.Context, repo, base, head, title, body string) (prURL string, err error)
}
```

- **Real `Agent`** — `sh -c "$AT_WORK_AGENT_COMMAND"`, env = parent minus
  `AT_WORK_GIT_TOKEN` plus the agent vars; reads `AT_WORK_AGENT_OUTPUT`.
- **Real `Git`** — shells `git`; clone/push authenticate via a temp `GIT_ASKPASS`
  that echoes the token (never on argv, never logged).
- **Real `CodeHost`** — a small GitHub client over `net/http` (`POST
  /repos/{owner}/{repo}/pulls`); **no `gh` dependency**.

`at-work` defines its **own** `Input`/`Output` types (kebab-case JSON as in §3); it
does **not** reuse the scheduler's `config.Result` — the two contracts are decoupled.

## 7. Credentials & guardrails

- **One credential:** `AT_WORK_GIT_TOKEN`, used by `at-work` for clone, push, and
  PR-open; **stripped** from the agent's environment.
- **Never on argv/logs:** the token flows through a temp `GIT_ASKPASS` for git and an
  `Authorization` header for the API; it is never an argument or logged.
- **Branch-first:** `at-work` only ever pushes `work-branch`; it refuses if
  `work-branch` is empty or equals `source-branch`.
- **Always a result:** every path writes a valid `output.json`.

## 8. Testing

- **Orchestrator (hermetic, the bulk):** a fake `Agent` (writes a chosen
  `AgentBlock`; asserts the token is absent from its env), a fake `Git` (or a real
  local repo), and a fake `CodeHost` (returns a canned URL) drive `Run` tests
  asserting the `output.json` for **OK / NEEDS_INPUT / ERROR / no-changes /
  agent-crash / default-branch-refused** paths.
- **Real `Git`:** tested against a **local bare repo** in `t.TempDir()` (clone via a
  `file://` path, branch, commit, push back) — no network.
- **GitHub `CodeHost`:** recorded-JSON unit tests via a fake `http.RoundTripper`;
  one `integration`-tagged live test (token + scratch repo via env, skipped by
  default).

## 9. Non-goals (deferred)

The `at-cove run`/VM integration + the dispatch wrapper that bridges `output.json` →
the scheduler's `result.json` (AET-21); the scoped-token minter (AET-24); `plan` and
`review` class behaviors (which may not open a PR); multi-code-host (GitHub-only);
and any Linear/tracker interaction (the worker never touches the tracker).

## 10. Open questions

- **Whether `at-work` also enforces a test gate** (e.g. refuse `OK` unless a
  configured test command passes) or leaves testing entirely to the agent. The MVP
  trusts the agent's `OK`; a kit-declared verification command could be added later.
- **Commit granularity** — the MVP has `at-work` make one commit of the agent's
  changes; letting the agent structure multiple commits is a later refinement.
