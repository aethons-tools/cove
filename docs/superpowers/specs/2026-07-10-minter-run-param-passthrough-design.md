# Single-repo kit (`origin`) + per-task token minter — Design

**Date:** 2026-07-10
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-work`, `at-dispatch`)
**Tracks:** [AET-24](https://linear.app/aethons-tools/issue/AET-24) (the minter), founded on a kit-model cleanup and folding in the enabling run-parameter passthrough from [AET-22](https://linear.app/aethons-tools/issue/AET-22). Child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic.
**Builds on:** [AET-29](https://linear.app/aethons-tools/issue/AET-29) (at-cove drives the worker bracket; `AT_WORK_GIT_TOKEN` is a kit resolver secret).

## 1. Purpose

Two coupled changes. **First, clean up the model:** give the *repo being acted on* exactly one home. Today it is over-specified — the `at-dispatch` config names a repo (`repo.slug`), the scheduler stamps it into every `task.json`, and "kit" straddles two roles (a repo's interactive sandbox vs. a reusable dispatch worker). We collapse this: **one kit per repo**, the kit declares its `origin`, and everything downstream (the injected task, `at-work`'s clone, the minter's scope) reads that single source. **Second, harden the credential:** with the repo unambiguous, at-cove exposes the run's parameters to secret resolver commands (`COVE_RUN_*`), turning the kit's `AT_WORK_GIT_TOKEN` resolver into a **per-task minter** that produces a short-lived, single-repo, push+PR-scoped token — with no code-host logic in at-cove itself.

## 2. Governing decisions

- **One kit = one repo.** The interactive-sandbox kit and the dispatch-worker kit are the same thing — a repo's `.at-cove/` kit. There is no separate repo-agnostic worker kit.
- **`origin` is a tagged union** naming the remote and its host: `origin: { github: { project: "owner/name" } }` — exactly one host (github only today; the shape leaves room for others). It is the single source of the **repo identity** *and* the **code-host kind** (which selects the clone URL, the PR API, and the matching minter). Optional in the schema, **required for `dispatch`** (like `workers`); interactive `connect` works without it.
- **`main-branch`** (optional, default `main`) is the repo's base branch.
- **`task.json` `source-branch` is an optional override.** The scheduler normally sends none → at-cove uses `main-branch`; a caller may set it to stack work on a non-main base.
- **at-cove fills the repo into the task.** The scheduler sends `{issue, class, brief, work-branch, source-branch?}` and names **no repo**. `at-cove dispatch` merges the kit's `origin` + `main-branch` into the task before injecting it, so **`at-work`'s `task.json` contract is unchanged** (it still reads a complete `repo` block) — only the *source* moves to the kit.
- **The scheduler + `at-dispatch` config become repo-agnostic.** `RepoConfig{slug,source-branch}` retires; the brief drops its repo line. The dispatcher's purview is "which tracker issues," not "which repo."
- **Passthrough, not a built-in minter** (unchanged from the prior design): at-cove exposes `COVE_RUN_*` to each resolver command during **dispatch** secret resolution; the minter is a kit `command`. Scope is fixed **in** the minter; run params tune only labels/target — untrusted issue text can never widen scope.
- **The token is minted fresh before *each* `at-work` git step** (`prepare`, then again `complete`), not once per run. Because the long agent step between them holds no token (the air-gap), each minted token need only outlive a single git operation (seconds–minutes) — so the code host's fixed token TTL (GitHub: 1h) never bounds the total run length. This retires the "dispatch `--timeout` < 1h" constraint entirely.

## 3. The kit

```yaml
# <repo>/.at-cove/config.yml — one kit per repo, for both `connect` and `dispatch`
name: myrepo-sandbox

origin:                        # tagged union; required for dispatch
  github:
    project: aethons-tools/cove   # owner/name
main-branch: main              # optional, default "main"

secrets:
  AT_WORK_GIT_TOKEN:
    description: per-task token — push + PR on origin.github.project
    command: ["mint-github-token.sh"]

workers:
  implement: { prompt: "You are an implementer. …" }

image:
  setup-scripts: [ .install-files/install.sh ]
  allowed-domains: [ api.anthropic.com, api.github.com, github.com ]
```

`kit.Config` gains `Origin *Origin` (union: `Origin{GitHub *GitHubOrigin{Project string}}`, an `Active()` validator like the status unions) and `MainBranch string` (default `main`). Validation: at most one origin host; `github.project` must be `owner/name`.

## 4. Repo single-sourcing — the data flow

```
kit: origin.github.project = acme/myrepo, main-branch = main
        │
at-cove dispatch <kit> --in <task(issue,class,brief,work-branch,source-branch?)>:
        │  merge → task.repo = { host: https://github.com (from github kind),
        │                        name: acme/myrepo,
        │                        source-branch: <task.source-branch ?? main-branch>,
        │                        work-branch: <from task> }
        ▼
   inject complete task.json  →  at-work prepare clones acme/myrepo
        │
   COVE_RUN_REPO = acme/myrepo  →  minter scopes the token to it
```

The repo is declared once (the kit's `origin`) and read by every consumer through at-cove. `at-work`, the minter, and interactive `connect` never learn a repo independently.

## 5. The minter + `COVE_RUN_*` passthrough

**`internal/runner`** — add `OutputEnv(extraEnv []string, name, args...) (string, error)` (Output + extra child env), mirroring `RunEnv`; add to `Fake` (record the env).

**`internal/secret`** — `Resolve(r, extraEnv map[string]string, specs)`; non-literal specs run via `OutputEnv`. `connect` passes `nil`; `dispatch` passes the run map.

**`internal/dispatchrun`** — build `COVE_RUN_REPO` (= `origin.github.project`), `COVE_RUN_ISSUE` (`issue.key`), `COVE_RUN_CLASS` (`worker.class`), `COVE_RUN_TIMEOUT` (`--timeout`). **Resolve the base (non-`AT_WORK_GIT_TOKEN`) secrets once up front** (fail-closed, before `BuildImage`); then **mint the token fresh immediately before `prepare` and again immediately before `complete`** (re-run the `AT_WORK_GIT_TOKEN` resolver with `COVE_RUN_*`), merging it into that step's env. The agent step runs with the base secrets only (no token) — the AET-29 air-gap holds, now with a per-step-fresh token.

**Reference minter** — `kits/reference-worker` becomes a **per-repo kit template** (`origin` placeholder, `main-branch`, `workers`, the minter secret). Add `mint-github-token.sh` (template): read `COVE_RUN_REPO`; build an App JWT (RS256 via `openssl`) from an operator-provisioned App id + key path; `POST /app/installations/<id>/access_tokens` with `{"repositories":["<repo-name>"],"permissions":{"contents":"write","pull_requests":"write"}}`; print `.token`; fail-closed. Because at-cove mints once per git step, the code host's fixed 1-hour TTL only needs to cover a single `prepare` or `complete` — no run-length constraint. `install.sh` chmods it; `RUNBOOK.md` documents App provisioning.

**Authority separation** (logical, matching today's resolver model): scheduler → tracker token only; the minter runs inside `at-cove dispatch` and reads the App **minting key** from a host path the scheduler *code* never touches; the worker VM receives only the minted token, in memory, never the key.

## 6. Component changes (summary)

- `internal/kit/config.go` — add `Origin` union + `MainBranch`; validation.
- `internal/dispatchrun` — read `origin`/`main-branch`/issue/class from kit+task; **merge repo into the injected task**; build + pass `COVE_RUN_*`.
- `internal/runner` — `+OutputEnv` (interface, `OS`, `Fake`).
- `internal/secret` — `Resolve` gains `extraEnv`.
- `internal/dispatch/config` + `internal/dispatch/scheduler` — drop `RepoConfig`; the task carries no repo; `assembleBrief` drops the repo arg.
- `cmd/at-cove` — connect passes `nil` extra-env; dispatch validates `origin` present.
- `kits/reference-worker` — per-repo template: `origin`/`main-branch` + minter script + `install.sh` + `RUNBOOK.md`.
- Docs — `at-cove-config.md` (`origin`/`main-branch`), `at-cove-dispatch-interface.md` (repo single-sourced; minter shipped; `COVE_RUN_*`), `scheduler-config.md` (repo removed), `OVERVIEW.md`, at-work usage (a note that at-cove fills `repo` from the kit — contract unchanged).

## 7. Testing

Hermetic (`runner.Fake`), no real GitHub:
- `kit`: `origin` union parse + validation (one host, `owner/name`); `main-branch` default.
- `dispatchrun`: repo merged into the injected task from `origin`+`main-branch` (+ `source-branch` override honored); `COVE_RUN_*` derived and passed to the resolver; **the `AT_WORK_GIT_TOKEN` resolver runs once before `prepare` and once before `complete` (two fresh mints), and the token lands only in those two envs — withheld from the agent** (AET-29 air-gap test still holds); base secrets resolved once; no secret value on argv.
- `runner`/`secret`: `OutputEnv` passes env; `Resolve(extraEnv,…)`; literal + fail-closed unchanged.
- `scheduler`: task built without repo; broker/comment paths unaffected.
- Reference minter: `sh -n`; reads `COVE_RUN_REPO`, fails closed when unset.
- Real GitHub App round-trip: maintainer-run `integration`/e2e (provisioned App; e2e sets the kit's `origin` to the scratch repo).

## 8. Risks / non-goals

- **Fail-closed timing:** base secrets are validated before the VM is built, but the two token mints happen just before their git steps — so a *minter* misconfiguration surfaces after the image build (one wasted build, torn down on error), not before. Acceptable; an optional up-front smoke-mint could restore strict fail-before-build.
- **Logical (not physical) authority separation** — the minting key is on the scheduler's host, read only by the minter command; a separate broker is deferred.
- **Non-goals:** multi-code-host origins/minters (union leaves room); per-run `--egress-profile` and the tracker-host egress (AET-22); changing the AET-29 bracket/air-gap; a repo-agnostic (multi-repo) worker kit (explicitly rejected — one kit per repo).

## 9. Decomposition (plans)

Two hermetic plans, each green:
1. **Single-repo kit + `origin` + repo single-sourcing** — `kit.Config` `origin`/`main-branch`; `at-cove dispatch` merges the repo into the task; retire `at-dispatch` `RepoConfig` + the scheduler's repo handling; reference kit gains `origin`/`main-branch`; docs. (No minter yet — the resolver stays `gh auth token`.)
2. **`COVE_RUN_*` passthrough + reference minter** — `runner.OutputEnv`, `secret.Resolve(extraEnv,…)`, `dispatchrun` `COVE_RUN_*` derivation; the `mint-github-token.sh` template + reference-kit wiring; docs (minter shipped). e2e stays maintainer-run.
