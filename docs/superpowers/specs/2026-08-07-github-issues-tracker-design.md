# GitHub Issues as a dispatch tracker

**Status:** design approved (brainstorm), pending spec review
**Date:** 2026-08-07
**Related:** [per-class egress](2026-07-19-per-class-egress-design.md) · [gitlab source-control](2026-07-21-gitlab-source-control-design.md) · dispatch scheduler (`internal/dispatch/scheduler`)

## Goal

Add **GitHub Issues** as a second dispatch tracker alongside Linear, at **full parity**
with the Linear tracker: the scheduler polls a GitHub repo for READY issues,
dispatches autonomous workers, transitions each issue through the lifecycle, posts
result comments, and the stale-claim reaper works. A GitHub-based team gets the same
autonomous dispatch loop a Linear team gets today.

Non-goal: replacing Linear, cross-repo issue references (v1 is same-repo), or a
GitHub Projects-based board.

## Why it's small

The scheduler is already provider-neutral. `internal/dispatch/scheduler` depends only
on the **`Tracker` interface** (5 methods) and neutral types (`Issue`, `Comment`,
`Role`, `InProgressIssue`); Linear is one implementation. The kit config is already a
union (`Tracker{ Linear *LinearTracker }`) with an `Active()` written to enforce
"exactly one provider." Adding GitHub Issues is therefore **additive**: a new config
variant, a new package implementing the interface, a provider switch at the composition
root, plus hoisting three Linear-specific reads that currently leak past the interface.

The `Tracker` contract a GitHub implementation satisfies (unchanged):

```go
ListReady(ctx)            ([]Issue, error)            // READY + dispatchable-now (blockers Done)
ListInProgress(ctx)       ([]InProgressIssue, error)  // + when each entered IN PROGRESS
Comments(ctx, id)         ([]Comment, error)
Transition(ctx, id, Role)                             // Ready/InProgress/InReview/NeedsInput/Blocked/Done
PostComment(ctx, id, body)
```

## Design decisions (resolved in brainstorming)

| Decision | Choice |
|---|---|
| Scope | Full parity — all 5 `Tracker` methods, real transitions, working reaper |
| Lifecycle states | **Status labels** (`status:ready`, …); **Done = issue closed** |
| Blocker gating | **Body convention** — parse `Depends on #N` / `Blocked by #N`; same-repo v1 |
| Repo scoping | **Inherit** from `source-control.github.project`, optional `tracker.github.repo` override |
| Credential | Scheduler's own `AT_DISPATCH_TRACKER_TOKEN` (issues:write); **never** the git token |
| PR auto-close | Worker PR carries `Closes #N`; merge auto-closes issue = Done (see §E) |

## A. Config & seam changes

Add a `github` variant to the `Tracker` union — no scheduler changes.

```yaml
tracker:
  github:
    # repo: acme/issues-only      # optional; defaults to source-control.github.project
    poll-interval: 60s
    class-label-prefix: "class:"  # default
    states:                       # role -> label; Done = issue closed
      ready:        status:ready
      in-progress:  status:in-progress
      in-review:    status:in-review
      needs-input:  status:needs-input
      blocked:      status:blocked
    secrets:
      AT_DISPATCH_TRACKER_TOKEN: {}   # GitHub token w/ issues:write; NEVER the git token
```

Changes:

- **`GitHubTracker` struct** (`internal/kit/config.go`): `Repo` (optional), `PollInterval`,
  `ClassLabelPrefix` (default `class:`), `States` (reuse the `StateMap` type, role→label),
  `Secrets`. **`states.done` is not required and is ignored if set** — Done is represented by the
  issue being *closed*, not by a label — so a GitHub `states` block declares only the five
  non-terminal roles (`ready`, `in-progress`, `in-review`, `needs-input`, `blocked`).
- **`Tracker.Active()`** extended to return `"github"` and to enforce **exactly one** of
  `{linear, github}` (currently only rejects zero).
- **`ParseConfig` validation** for `tracker.github`: repo resolvable (override set, or
  `source-control.github` present); each of the five non-terminal `states` labels non-empty
  (`done` optional/ignored); `class-label-prefix` non-empty if set; `secrets` demands exactly
  `AT_DISPATCH_TRACKER_TOKEN` (demand-only, no `command`, matching the Linear/source-control
  secret rules).
- **Hoist the three Linear leaks** so the engine/CLI stay provider-neutral:
  - `scheduler/engine.go:229` `cfg.Tracker.Linear.PollInterval` → `cfg.Tracker.PollInterval()`.
  - `cmd/at-cove/main.go` two log/dry-run reads of `Tracker.Linear.PollInterval` → same accessor.
  - `cmd/at-cove/main.go` `cfg.Tracker.Linear == nil` guards → `cfg.Tracker.Active()`.
- **Composition root** (`cmd/at-cove/main.go` ~1609): `switch cfg.Tracker.Active()` →
  `linear.New(...)` | `githubissues.New(...)`, both returning a `scheduler.Tracker`.

## B. `internal/dispatch/githubissues` — the client

Implements `scheduler.Tracker`. Constructor `New(cfg kit.Config, token string, httpc *http.Client)`
takes an injectable `*http.Client` so tests are hermetic (mirrors `internal/dispatch/linear`).
Resolves the repo from `tracker.github.repo` or `source-control.github.project`. Builds the
role→label map and the `class:` prefix from config. May reuse the auth/HTTP `do()` pattern from
`internal/dispatch/github` (the code-host client), but stays a separate package — tracker and
code-host are distinct concerns.

Methods:

- **`ListReady`** — search `repo:O/N is:issue is:open label:<status:ready>`. For each hit: parse
  the class from its `class:*` label (→ `Issue.Class`), set `Identifier = "#N"` and `ID` to the
  issue number (used for REST calls). Then parse `Depends on #N` / `Blocked by #N` from the body and **drop any issue
  with an open referenced blocker** (blocker state resolved in one batched pass, like Linear's
  `doneBlockers`). Same-repo references only in v1; a cross-repo `owner/repo#N` marker is ignored
  with a logged note (documented limitation).
- **`ListInProgress`** — search `label:<status:in-progress> is:open`. For each, derive the
  *entered-in-progress* time from the most recent `labeled` **timeline event** for that label
  (one extra call per in-progress issue; N is small). Feeds the stale-claim reaper's time-in-state.
- **`Comments`** — list issue comments → `[]scheduler.Comment{Author, Body}`.
- **`Transition(id, role)`** — `RoleDone` → **close** the issue; every other role → **add** that
  role's label and **remove sibling `status:*` labels** (so an issue never carries two status
  labels), keeping it open. The engine is the single writer, so no locking is needed. Closing an
  already-closed issue and adding an already-present label are both no-ops (idempotent).
- **`PostComment`** — create an issue comment.

## C. Auth, limits, identity

- **Credential air-gap preserved.** The scheduler holds only `AT_DISPATCH_TRACKER_TOKEN`; the
  worker's git steps still use `source-control.github`'s minted token. Even though both are
  GitHub, they remain separate authorities under the three-authority model — the scheduler never
  sees the git token and the worker never sees the tracker token.
- **Rate limits.** GitHub's Search API is 30 req/min and core REST 5000/hr; a 60s poll issuing a
  handful of searches plus per-issue timeline/comment reads stays well within budget. Documented,
  with a note to raise `poll-interval` for very large boards.
- **Identity.** `Issue.Identifier` = `#N`; `Issue.ID` = the numeric issue number used for REST
  calls.

## D. PR auto-close via annotation

When the **GitHub Issues tracker** dispatches a unit, it writes a **close reference** into the
task contract — `task.json` gains an optional `issue.closes` (e.g. `"#42"`) — set **only** when
the active tracker is `github` **and** the issue lives in the PR's target repo. During the
`complete` step, `at-task` appends the GitHub keyword `Closes #42` to the PR body it passes to
`OpenPR` (`internal/dispatch/worker/complete.go:64`, body = `pr.Message`). Merging the PR then
auto-closes the issue, which the tracker reads as **Done**.

Rationale and constraints:

- **at-task stays tracker-agnostic.** It honors a field in the task contract; it gains no
  GitHub-tracker coupling. The tracker-awareness lives on the scheduler side, which already knows
  the active tracker and the repo.
- **Done via merge, not a second writer.** The lifecycle reaches Done through the natural
  code-host merge instead of an extra scheduler write.
- **Single-writer stays intact.** The scheduler remains the writer of
  Ready→In Progress→In Review and Needs Input/Blocked. `Transition(RoleDone)` is idempotent
  (closing an already-closed issue is a no-op), so a reviewer-driven Done and a merge-driven
  auto-close never conflict.
- **Emitted narrowly.** Only for same-repo GitHub-tracker dispatches. A Linear tracker (which has
  its own GitHub PR-linking) or a cross-repo issue emits no annotation.

## E. Testing

- **`githubissues` client** — hermetic tests via a fake `http.RoundTripper` / `httptest` server
  (mirrors `internal/dispatch/linear`'s tests): label-swap transitions (incl. sibling removal and
  close-on-Done), blocker parsing + gating, timeline-derived entered-in-progress timestamp,
  comment mapping, repo inheritance vs override.
- **Config** — parse/validate tests for `tracker.github`: union exclusivity (`linear` **and**
  `github` rejected), repo inheritance and override, empty-label rejection, secret demand.
- **Task contract** — `issue.closes` round-trips through `task.json`; `at-task` `complete`
  appends `Closes #N` to the PR body only when the field is present.
- **Scheduler engine** — existing tests use a fake `Tracker` and are unaffected; that they keep
  passing is the proof the seam holds.
- All tests hermetic (`runner.Fake` / injected `http.Client`); no live GitHub. `just test` +
  `just lint` green.

## Decomposition (for planning)

1. **Config variant + seam** — `GitHubTracker`, `Active()` exclusivity, validation, hoist the
   three Linear leaks behind provider-neutral accessors. *(Small; enabling — no behavior yet.)*
2. **`githubissues` read methods** — `New`, `ListReady` (+ blocker parsing/gating),
   `ListInProgress` (+ timeline timestamp), `Comments`. *(Medium.)*
3. **`githubissues` write methods + wiring** — `Transition` (label-swap/close), `PostComment`,
   and the composition-root `switch`. *(Small–medium.)*
4. **PR auto-close** — `issue.closes` in the task contract + `at-task` `complete` appends the
   keyword; scheduler sets it for same-repo GitHub dispatches. *(Small.)*
5. **Docs** — `docs/usage/at-cove-config.md` (tracker.github section + validation summary) and the
   orchestration tracker docs. *(Small; folded into each PR per the "docs in the same change"
   rule, with a final consolidation pass.)*

Dependency order: 1 → {2, 3} → 4; docs ride each. 3 depends on 1 (composition root) and pairs
naturally with 2.
