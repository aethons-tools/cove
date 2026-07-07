# at-work — the git/PR worker — Design

**Date:** 2026-07-07
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binary `at-work`)
**Tracks:** [AET-26](https://linear.app/aethons-tools/issue/AET-26) (child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic)
**Related:** [orchestration design](../../orchestration/INDEX.md); the [scheduler MVP](2026-07-06-at-dispatch-scheduler-mvp-design.md) that ultimately consumes a worker's result.

## 1. Purpose

Design **`at-work`**: the tool that turns a unit of work into a pull request. It owns
all **git and code-host** interaction — clone, branch, commit, push, open PR — around
an agent run that at-cove performs. `at-work` itself **never runs the agent** and holds
no agent/VM/kit knowledge; it is two focused git/PR steps with a dead-simple file
handoff in between.

`at-work` splits into `prepare` and `complete`; at-cove sequences three steps in one
working directory:

```
at-work prepare <in>   →   (at-cove runs the agent)   →   at-work complete <in> <out>
```

This keeps agent execution where the knowledge already lives (at-cove runs agents),
and makes the **credential air-gap** a matter of *which steps get the token*: at-cove
injects `AT_WORK_GIT_TOKEN` for `prepare` and `complete` (which clone/push/PR) and
**not** for the agent step (which only edits/tests files). The untrusted-brief-ingesting
agent never holds the token.

## 2. Governing decisions (from brainstorming)

- **Scope: the `at-work` binary only.** It runs standalone and is fully hermetically
  testable. The `at-cove run`/VM sequencing (prepare → agent → complete, with the
  token on the outer two) is deferred to AET-21; the scoped-token minter to AET-24
  (the MVP uses a plain token). MVP class: **`implement`**. Code host: **GitHub only**.
- **`prepare`/`complete` split.** `at-work` does *not* exec the agent, construct its
  env, or pass anything through to it — that whole concern moves to at-cove.
- **Handoff is a file convention in the cwd** (no env passthrough, no flags): `prepare`
  writes the brief to `.at-work/brief.md`; the agent writes its outcome to
  `.at-work/outcome.json`; `complete` reads it.
- **`at-work` owns clone → branch → commit → push → PR.** Setup is idempotent and
  resume-aware (a `work-branch` may already exist from a prior `NEEDS_INPUT` round).
- **`at-work` has its own contract** (`input.json`/`output.json`), decoupled from the
  scheduler's `DISPATCH_*`/`result.json`; a future wrapper bridges them.

## 3. The contract

```
at-work prepare  <input.json>          # git setup + write .at-work/brief.md
at-work complete <input.json> <output.json>   # read .at-work/outcome.json → commit/push/PR → write output.json
at-work version
```

Both `prepare` and `complete` operate on the **current directory** (override with
`--dir`), which is the shared working dir for all three steps.

**`input.json`** — everything the worker needs (both subcommands read it):

```json
{
  "issue": { "key": "AET-33", "title": "Add thing X", "work-class": "implement", "brief": "…full markdown brief…" },
  "repo":  { "name": "aethons-tools/loom", "source-branch": "main", "work-branch": "implement/AET-33" }
}
```

**`.at-work/outcome.json`** — the agent's self-report (the agent writes it):

```json
{ "status": "OK", "pr-message": "…proposed PR body…" }
{ "status": "NEEDS_INPUT", "needs-input": { "doing":"…","blocker":"…","need":"…","tried":"…" } }
{ "status": "ERROR", "message": "…" }
```

**`output.json`** — written by `complete`; a top-level `status` (authoritative), an
optional `message`, and two detail blocks (`agent` = the outcome above; `work` = what
`at-work` did):

```json
// OK — agent finished; at-work pushed + opened the PR
{ "status": "OK", "message": "opened PR #42",
  "agent": { "status": "OK", "pr-message": "…" },
  "work":  { "branch": "implement/AET-33", "pr-url": "https://github.com/aethons-tools/loom/pull/42" } }

// NEEDS_INPUT — agent stopped; at-work pushed the WIP branch, no PR
{ "status": "NEEDS_INPUT",
  "agent": { "status": "NEEDS_INPUT", "needs-input": { "doing":"…","blocker":"…","need":"…","tried":"…" } },
  "work":  { "branch": "implement/AET-33", "safe-state": "implement/AET-33 @ <sha>" } }

// ERROR — agent reported failure, produced no outcome, or a mechanical failure occurred
{ "status": "ERROR", "message": "no agent outcome",
  "agent": { "status": "ERROR", "message": "…" },
  "work":  { "branch": "implement/AET-33" } }
```

`complete` **always** writes a valid `output.json` with a top-level `status`, even if
`prepare` or the agent never produced anything. **Status derivation:** `OK` iff
`.at-work/outcome.json` says `OK` **and** a PR was opened (`work.pr-url` set);
`NEEDS_INPUT` iff the outcome says so; otherwise `ERROR` (missing/invalid outcome, or
any mechanical failure), with a synthesized `message`.

**Environment:**

```shell
AT_WORK_GIT_TOKEN=…   # code-host read/write/PR token; at-cove injects it for the
                      # prepare and complete steps ONLY — never for the agent step.
```

## 4. The handoff (why `at-work` needs no agent knowledge)

- `prepare` writes `issue.brief` to `.at-work/brief.md` in the working dir.
- at-cove runs the kit's agent in that same dir, **without the token**. The agent
  reads `.at-work/brief.md`, edits and tests files, and writes an outcome block to
  `.at-work/outcome.json`. `at-work` never names, execs, or configures the agent.
- `complete` reads `.at-work/outcome.json`. A missing/invalid outcome → `ERROR`.

## 5. Flow

The setup is **idempotent and resume-aware**: `work-branch` may already exist because
the agent is responding to a previous `NEEDS_INPUT` round (re-dispatched after the
human answered). `at-work` refuses to run on a dirty checkout.

**`at-work prepare <in>`:**
1. Parse `input.json`. Guardrail: `work-branch` must be non-empty and ≠ `source-branch`.
2. **Ensure the repo, clean.** If the dir has no checkout → clone `repo.name` (token
   via a temp `GIT_ASKPASS`, never on argv). If it already has one → verify it is
   `repo.name` and the **working tree + index are clean** (else fail — never run on
   dirty state).
3. **Sync the base:** check out `source-branch` and fast-forward it from origin.
4. **Prepare the work branch:** if `work-branch` exists on origin → check it out and
   fast-forward it (**resume** the WIP), requiring a clean tree/index; else create it
   off the synced `source-branch`.
5. Write `issue.brief` to `.at-work/brief.md`. Exit 0 (ready) or non-zero (setup
   failed — at-cove skips the agent but still runs `complete`).

**`at-work complete <in> <out>`** (always writes `output.json`):
1. Parse `input.json`; read `.at-work/outcome.json` (absent/invalid → `ERROR`).
2. By the outcome's status:
   - `OK` → commit any changes; require `work-branch` to differ from `source-branch`
     (else `ERROR` — nothing to PR); push `work-branch`; **open a PR** (base
     `source-branch`, head `work-branch`, title `"<key>: <title>"`, body =
     `pr-message`) — returning an existing PR's URL if one already exists for that
     head; write `OK` with `work.pr-url`.
   - `NEEDS_INPUT` → commit any WIP; push `work-branch` (safe state); write
     `NEEDS_INPUT` with `work.safe-state = <work-branch> @ <sha>`. **No PR.**
   - `ERROR` (or missing outcome) → write `ERROR`. No git/PR attempted.
3. Any unexpected mechanical failure still writes a valid `ERROR` `output.json`.

## 6. Architecture

Plan/execution split (as in the scheduler), now with **no `Agent` interface** — just
git and the code host, driven by an orchestrator per subcommand:

```
cmd/at-work/                 the binary (prepare / complete / version)
internal/dispatch/worker/    prepare + complete orchestration, Input/Output/Outcome types, Git + CodeHost interfaces, fakes
internal/dispatch/github/    real CodeHost: open a PR via the GitHub API (live calls behind the `integration` tag)
```

```go
type Git interface { // names indicative; finalized in the plan
    EnsureClean(ctx context.Context, repo, dir string) error          // clone if absent; else verify identity + clean tree/index
    Sync(ctx context.Context, dir, branch string) error               // checkout + fast-forward (--ff-only) from origin
    RemoteHasBranch(ctx context.Context, dir, branch string) (bool, error)
    NewBranch(ctx context.Context, dir, branch, from string) error
    HasChanges(ctx context.Context, dir string) (bool, error)         // uncommitted changes present
    DiffersFrom(ctx context.Context, dir, base string) (bool, error)  // current branch has commits beyond base
    Commit(ctx context.Context, dir, msg string) (sha string, err error)
    Push(ctx context.Context, dir, branch string) error
}
type CodeHost interface {
    // OpenPR creates the PR, or returns the URL of an existing one for the same head.
    OpenPR(ctx context.Context, repo, base, head, title, body string) (prURL string, err error)
}
```

- **Real `Git`** — shells `git`; clone/fetch/push authenticate via a temp
  `GIT_ASKPASS` that echoes the token (never on argv, never logged); syncs are
  `--ff-only`.
- **Real `CodeHost`** — a small GitHub client over `net/http` (`POST
  /repos/{owner}/{repo}/pulls`, with a lookup to return an existing PR for the head);
  **no `gh` dependency**.

`at-work` defines its **own** `Input`/`Output`/`Outcome` types (kebab-case JSON as in
§3); it does **not** reuse the scheduler's `config.Result` — the two are decoupled.

## 7. Credentials & guardrails

- **One credential:** `AT_WORK_GIT_TOKEN`, used by `prepare` (clone/fetch) and
  `complete` (push/PR). at-cove injects it for those two steps and **not** for the
  agent step — that is the air-gap.
- **Never on argv/logs:** the token flows through a temp `GIT_ASKPASS` for git and an
  `Authorization` header for the API; never an argument, never logged.
- **Branch-first:** `at-work` only ever pushes `work-branch`; `prepare` refuses if it
  is empty or equals `source-branch`, and refuses a dirty checkout.
- **Always a result:** `complete` always writes a valid `output.json`.

## 8. Testing

- **`prepare` (hermetic):** against a **local bare repo** in `t.TempDir()` (clone via
  a `file://` path) — asserts fresh clone, base sync, fresh-branch vs. **resume**
  (a pre-existing remote branch is fast-forwarded), the **dirty-checkout refusal**,
  the **default-branch refusal**, and that `.at-work/brief.md` is written.
- **`complete` (hermetic):** given a prepared local checkout + a chosen
  `.at-work/outcome.json` + a fake `CodeHost` — asserts the `output.json` for
  **OK / NEEDS_INPUT / ERROR / missing-outcome / no-changes** paths and the
  git effects (branch pushed; PR opened only on `OK`).
- **GitHub `CodeHost`:** recorded-JSON unit tests via a fake `http.RoundTripper`; one
  `integration`-tagged live test (token + scratch repo via env, skipped by default).

## 9. Non-goals (deferred)

The `at-cove run`/VM sequencing (prepare → agent-without-token → complete) + the
dispatch wrapper that bridges `output.json` → the scheduler's `result.json` (AET-21);
the scoped-token minter (AET-24); `plan`/`review` class behaviors; multi-code-host
(GitHub only); and any Linear/tracker interaction (the worker never touches the
tracker).

## 10. Open questions

- **Whether `at-work` enforces its own test gate** in `complete` (e.g. refuse `OK`
  unless a kit-declared verification command passes) or trusts the agent's `OK`. The
  MVP trusts the agent.
- **Commit granularity** — the MVP has `complete` make one commit of the agent's
  changes; letting the agent structure multiple commits is a later refinement.
