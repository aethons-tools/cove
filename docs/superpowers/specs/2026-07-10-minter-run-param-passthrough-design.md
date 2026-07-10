# Per-task token minter + run-parameter passthrough — Design

**Date:** 2026-07-10
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-work`, `at-dispatch`)
**Tracks:** [AET-24](https://linear.app/aethons-tools/issue/AET-24) (the minter), folding in the enabling **run-parameter passthrough** from [AET-22](https://linear.app/aethons-tools/issue/AET-22). Child of the [AET-19](https://linear.app/aethons-tools/issue/AET-19) dispatcher epic.
**Builds on:** [AET-29](https://linear.app/aethons-tools/issue/AET-29) (at-cove drives the worker bracket; `AT_WORK_GIT_TOKEN` is a kit-declared resolver secret).

## 1. Purpose

Harden the code-host credential from a **standing, broadly-scoped token** into a **short-lived, single-repo, push+PR-scoped token minted per dispatch**, so a compromised or misbehaving worker can do far less. This realizes the "three separated authorities" the credential docs describe as the intended design.

The MVP (shipped in AET-29) injects a plain `AT_WORK_GIT_TOKEN` — a kit resolver `command` (e.g. `gh auth token`) whose stdout is injected into the VM in memory. That command has **no knowledge of the run**, so it can only return a broad standing token. This design adds the missing input — the **run's parameters** — to the resolver's environment, turning any resolver `command` into a **per-run minter** with no new at-cove code-host logic.

**at-cove stays code-host-agnostic** (decided): at-cove builds only the generic passthrough; the minter is a resolver **command** (a reference script in the kit). The actual GitHub App is operator-provided, exactly as `gh auth token` is today.

## 2. Governing decisions

- **Passthrough, not a built-in minter.** at-cove exposes `COVE_RUN_*` to each resolver command's environment during **dispatch** secret resolution. The minter is a kit-declared `command`.
- **Run parameters exposed:** `COVE_RUN_REPO` (task `repo.name`), `COVE_RUN_ISSUE` (`issue.key`), `COVE_RUN_CLASS` (`worker.class`), `COVE_RUN_TIMEOUT` (the dispatch `--timeout`). Derived by at-cove from the injected task + the run — **untrusted issue *text* never reaches them** (only structured fields).
- **Scope is fixed in the minter, not the run.** The reference minter hard-codes `permissions={contents:write, pull_requests:write}` and scopes to the single `COVE_RUN_REPO`. Run params tune only TTL/labels/target-repo — they can never *widen* scope, so untrusted task content can't escalate.
- **Reference minter = GitHub App installation token.** A template shell script mints an installation access token scoped to `COVE_RUN_REPO`. GitHub fixes these at **1-hour TTL** (not settable), so `COVE_RUN_TIMEOUT` is informational for this minter and **dispatch `--timeout` must stay < 1h**; the "TTL ≥ run timeout, capped at host max" rule only bites for token types with a settable TTL (documented, not enforced by at-cove).
- **Three authorities, logically separated** (matches the existing resolver model): the **scheduler** (`at-dispatch`) holds only the tracker token; the **minter** (runs inside `at-cove dispatch` on the host) reads the App **minting key** from a host path the scheduler's *code* never touches; the **worker** VM receives only the minted, scoped token — never the minting key. The token is injected into the VM in memory and never returned to the scheduler.
- **Passthrough is dispatch-only.** Interactive `at-cove connect` has no run, so it passes no `COVE_RUN_*` (the resolver just sees the normal host env). `secret.Resolve` gains a generic extra-env parameter; `connect` passes nil, `dispatch` passes the `COVE_RUN_*` map.

## 3. Mechanism

**`internal/runner`** — add `OutputEnv(extraEnv []string, name string, args ...string) (string, error)` (Output + extra `KEY=VALUE` child env), mirroring the existing `RunEnv`. Add it to `Fake` (record the env alongside the call).

**`internal/secret`** — `Resolve(r, extraEnv map[string]string, specs)` runs each non-literal spec's command via `r.OutputEnv(flatten(extraEnv), …)`. Literal specs are unchanged. Empty/nil `extraEnv` = today's behavior (plain host env). Fail-closed unchanged.

**`internal/dispatchrun`** — `Dispatch` already reads `worker.class` from the injected task; extend the read to also capture `repo.name` and `issue.key`, build:
```
COVE_RUN_REPO=<repo.name>  COVE_RUN_ISSUE=<issue.key>
COVE_RUN_CLASS=<worker.class>  COVE_RUN_TIMEOUT=<o.Timeout>
```
and pass it to `secret.Resolve`. Nothing else about the bracket changes; the resolved (possibly-minted) token still flows into the `prepare`/`complete` env only and is withheld from the agent (the AET-29 air-gap is unchanged).

**`cmd/at-cove` (connect path)** — pass `nil` extra-env to `secret.Resolve` (mechanical signature update).

**`kits/reference-worker`** — the `AT_WORK_GIT_TOKEN` secret's `command` becomes a reference minter:
```yaml
secrets:
  AT_WORK_GIT_TOKEN:
    description: per-task GitHub App installation token (push + PR on COVE_RUN_REPO)
    command: ["mint-github-token.sh"]
```
Add `image-files/.../mint-github-token.sh` (a **template**): read `COVE_RUN_REPO`; build an App JWT (RS256 via `openssl`) from an operator-provisioned App id + private-key path; `POST /app/installations/<id>/access_tokens` with `{"repositories":["<repo>"],"permissions":{"contents":"write","pull_requests":"write"}}`; print `.token`. Fail-closed (nonzero exit if any step fails → `secret.Resolve` aborts before the VM is built). The App id / installation id / key path are operator-filled template markers. `install.sh` chmods it; `RUNBOOK.md` documents provisioning the GitHub App.

> Note: the minter runs on the **host** (in the `at-cove dispatch` process the scheduler spawns). The App key sits on that host; only the minter reads it. Physical isolation (a separate broker service) is a future option — logical separation matches today's model and is the MVP.

## 4. Component changes (summary)

- `internal/runner`: `+OutputEnv` on the interface, `OS`, and `Fake`.
- `internal/secret`: `Resolve` gains an `extraEnv map[string]string` param; uses `OutputEnv`.
- `internal/dispatchrun`: derive + pass `COVE_RUN_*`; read `repo.name`/`issue.key` alongside `worker.class`.
- `cmd/at-cove`: connect passes `nil` (mechanical).
- `kits/reference-worker`: minter script + config `command` + `install.sh` + `RUNBOOK.md`.
- Docs: `orchestration/at-cove-dispatch-interface.md` — move the minter + `COVE_RUN_*` passthrough from **deferred** to **shipped**; document the passthrough vars, the fixed-in-the-minter scope, the 1-hour-TTL constraint, and the authority separation. `OVERVIEW.md` secret section — a pointer if needed.
- **Not this issue:** AET-22's egress additions (tracker API host, `--egress-profile`) and multi-code-host remain in AET-22 (`api.github.com` is already allow-listed in the reference kit for the worker).

## 5. Testing

Hermetic (`runner.Fake`), no real GitHub:
- `runner.Fake.OutputEnv` records the extra env; `secret.Resolve` passes `extraEnv` into the command's environment (assert the fake saw `COVE_RUN_*`); literal + fail-closed paths unchanged.
- `dispatchrun`: the `COVE_RUN_*` env is derived correctly from a task (`repo.name`/`issue.key`/`worker.class`) + timeout and reaches `secret.Resolve`; **the minted token still lands only in the prepare/complete env and is withheld from the agent** (the AET-29 air-gap test still holds); the token value never on argv.
- Reference minter script: `sh -n` parses; a unit-ish check that it reads `COVE_RUN_REPO` and fails closed when it's unset (can be a shell test in the kit, or covered by inspection).
- The real GitHub App round-trip (actual scoped token) is the maintainer-run **`integration`/e2e** step (needs a provisioned App).

## 6. Risks / non-goals

- **1-hour GitHub cap:** dispatch runs must finish within the token's 1h life; longer runs need a token type with a settable/longer TTL (out of scope). Documented, not enforced.
- **Logical (not physical) authority separation:** the minting key is on the scheduler's host, read only by the minter command. A separate broker service is deferred.
- **Non-goals:** multi-code-host minters; per-run `--egress-profile`; the tracker-host egress addition (all AET-22 / later); changing the AET-29 air-gap or the bracket.

## 7. Decomposition (plans)

Modest — likely **two hermetic plans**:
1. **The passthrough** — `runner.OutputEnv`, `secret.Resolve(extraEnv, …)`, `dispatchrun` `COVE_RUN_*` derivation, connect signature update; all consumers + tests. (This is AET-22's enabling core; leaves the reference kit on its current `gh auth token` resolver.)
2. **The reference minter + docs** — the `mint-github-token.sh` template, the reference kit `command` + `install.sh` + `RUNBOOK.md`, and the dispatch-interface doc update (minter shipped). e2e stays maintainer-run.
