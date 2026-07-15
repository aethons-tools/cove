# Worker/collaborator secret segregation + lazy worker-bearer resolution — Design

**Date:** 2026-07-15
**Status:** Proposed (pre-implementation)
**Repo:** `aethons-tools/cove` (binaries `at-cove`, `at-task`)
**Tracks:** [COV-24](https://linear.app/aethons-tools/issue/COV-24) (must segregate secrets between workers and collaborators). Folds in the deferred "#2" from the agent-token-TTL debugging (lazy-resolve the worker bearer just before the agent step).
**Builds on:** the unified kit config ([COV-13](https://linear.app/aethons-tools/issue/COV-13)) and the location-scoped secret model it established; the work-path fail-closed gate + the `at-mint` TTL guard shipped in `a5a6292` (this session).

## 1. Purpose

Worker secrets can only be declared in the kit's **root `secrets`** bucket, which is injected into **both** worker (`at-cove work`) and collaborator (`at-cove chat`) launches. So a worker's Anthropic bearer (`ANTHROPIC_AUTH_TOKEN`) declared at root is also injected into chat sessions — where, because the env credential outranks the subscription OAuth login (see the auth precedence in the Anthropic auth docs), its mere presence **disables the chat session's connectors**. Workers have no bucket of their own; collaborators do (`collaborators.<class>.secrets`). This design closes that asymmetry.

Fixing it means restructuring the work path's worker-credential resolution — the same code that "#2" (mint the bearer lazily, right before the agent step) touches — so the two are done as one coherent change. The result: the worker bearer lives in a worker-only bucket, is resolved just before the agent runs, never reaches chat or the git steps, and the already-shipped TTL guard lands at exactly the right moment.

## 2. Governing decisions

- **Add a `workers` secret bucket, symmetric with `collaborators`.** `Worker` gains a `Secrets` map; the worker tree's existing reserved `<common>` key merges into each class (`effective = merge(workers.<common>.secrets, workers.<class>.secrets)`), exactly as collaborators already do (`internal/kit/config.go:180-186`).
- **A secret's *location* determines who sees it** (extends the config's structural air-gap model):
  - `root.secrets` → injected into **both** chat and work — genuinely shared secrets only.
  - `collaborators.<class>.secrets` → **chat only**.
  - `workers.<class>.secrets` → **work only**. The agent bearer lives here.
- **The worker bucket is resolved lazily, agent-step-only (folds in #2).** It is not resolved up front and not handed to the `prepare`/`complete` git steps — only to the `claude` agent step, resolved immediately before it runs.
- **A root-declared agent bearer is a hard config error.** Both `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_API_KEY` are rejected in `root.secrets` (both outrank the chat OAuth login and break connectors), with a migration note pointing to `workers:`.
- **No new secret-knowledge in the resolution flow.** `secret.Resolve` stays generic; `at-mint` keeps sole ownership of the TTL policy. The only "special" name-knowledge is in config *validation* (the root-bearer rejection) and the pre-existing fail-closed gate — not in the resolve/inject path.

## 3. Schema change

`internal/kit/config.go`:

```yaml
workers:
  <common>:                 # optional base, merged into every worker class
    secrets:
      ANTHROPIC_AUTH_TOKEN: { mint: anthropic-prod }
  implementor:
    prompt: "…"
    timeout: 90m
    # inherits ANTHROPIC_AUTH_TOKEN from <common>; may add/override its own
```

- `Worker.Secrets map[string]SecretConfig` (new), `yaml:"secrets,omitempty"`.
- `ResolvedWorker(class)` merges `<common>.secrets` under the class's own (own wins), mirroring `ResolvedCollaborator`.
- **Validation:** reject the well-known git/tracker names (`AT_TASK_GIT_TOKEN`, `AT_DISPATCH_*`) from a worker bucket (they belong to `source-control`/`tracker`), via the existing `rejectReservedSecretNames` used for root and collaborators.

### Bucket visibility (the contract)

| Bucket | chat (`at-cove chat`) | work (`at-cove work`/`dispatch`) | resolved |
| --- | --- | --- | --- |
| `root.secrets` | ✅ | ✅ | up front |
| `collaborators.<class>.secrets` | ✅ | — | up front (chat) |
| `workers.<class>.secrets` | — | ✅ (agent step only) | **lazily, before the agent step** |
| `source-control.<host>.secrets` (git token) | — | ✅ (git steps only, minted fresh per step) | per git step |

## 4. Resolution & injection

**chat** (`doChat`/`connect`, `cmd/at-cove/main.go`): demanded = `root ∪ collaborators.<class>`. Unchanged except that the worker bucket is never part of it — so a chat session on a worker kit no longer receives the bearer, and its connectors stay live.

**work** (`doWork` → `dispatchrun.Dispatch`):

- **root (shared) secrets** — resolved up front (as today, `dispatchrun.go:116`), available to every step including the git steps.
- **git token** — minted fresh per git step (existing `mint()` closure), for `prepare`/`complete` only.
- **worker-class bucket** — carried into `Dispatch` as a distinct spec set and **resolved just before the agent step** (immediately before `dispatchrun.go:187`). Its env is merged only into the `claude` step — never into `prepare`/`complete`.

Concretely, `dispatchrun.Options` grows a `WorkerSecrets []secret.Spec` (the worker-class bucket) beside the existing `Secrets` (now: root shared only) and `GitToken`. The git-step env becomes `root + fresh-git-token` (no worker bucket); the agent env becomes `root + resolve(WorkerSecrets)`, resolved at the call site.

Because `at-mint` runs at that late point, the build/boot/prepare overhead is already spent, so its `expires_in ≥ COVE_RUN_TIMEOUT` check (shipped in `a5a6292`) is now *exactly* the right requirement — no overhead margin, no new env var, no orchestrator-side TTL logic. **#1 is unchanged; it simply executes at the right moment.**

## 5. Migration — hard error on a root-declared bearer

Config validation rejects `ANTHROPIC_AUTH_TOKEN` **or** `ANTHROPIC_API_KEY` appearing in `root.secrets`:

```
config.yml: secrets: ANTHROPIC_AUTH_TOKEN must be declared under
workers.<class>.secrets (or workers.<common>.secrets), not at the root.
A root agent bearer is injected into `chat` sessions too, where it outranks the
subscription login and disables their connectors. Move it under `workers:`.
```

This is deliberately breaking: existing kits that declare the bearer at root fail to load with the note above until moved. Implemented in the root-`secrets` validation block (`config.go:251-256`) alongside `rejectReservedSecretNames`.

## 6. Fail-closed gate

The work-path fail-closed gate (shipped in `a5a6292`, `cmd/at-cove/main.go`) currently checks `cfg.Secrets[ANTHROPIC_AUTH_TOKEN]` at root. It **moves to the dispatched class's worker bucket**: the "bearer not declared for this worker class" check stays a pre-VM abort (from the plan/declaration, in `doWork`); the "declared but the minted token's TTL is too short" failure now surfaces at lazy resolution (post-build) inside `at-mint` — the accepted tradeoff of choosing #2, and only reachable by a genuinely too-short token (a misconfiguration), since lazy minting makes `TTL ≥ runtime` the exact requirement.

## 7. Testing

Hermetic (drive `runner.Fake` / the config loader; no VM):

- **Segregation:** chat's demanded secret set includes `collaborators.<class>` but **excludes** `workers.<class>`; work's includes `workers.<class>`.
- **Lazy + agent-only:** the worker bucket is resolved once, immediately before the agent step; the `prepare`/`complete` step envs contain the git token but **not** the worker bearer; the agent step env does.
- **`<common>` merge:** a `workers.<common>.secrets` entry is present on a class that doesn't redeclare it; a class override wins.
- **Migration:** `secrets: { ANTHROPIC_AUTH_TOKEN: … }` and `secrets: { ANTHROPIC_API_KEY: … }` at root each fail `kit.Load` with the migration message; the same names under `workers:` load fine.
- **Reserved names:** a git/tracker well-known name under `workers:` is rejected.
- **Fail-closed:** an implementor class with no bearer aborts pre-VM; the `at-mint` TTL guard (existing tests) still fires on a too-short token at resolution.

## 8. Docs

- `docs/usage/at-cove-config.md` — add `workers.<class>.secrets` (+ `<common>`), and the bucket-visibility table from §3.
- `docs/usage/at-cove-secrets.md` — the segregation model (location determines visibility), the chat-connector rationale, and the root-bearer migration note.
- `docs/OVERVIEW.md` — the secret-bucket summary (workers now has its own bucket) if it enumerates them.

## 9. Scope

**In:** the schema addition, resolution/injection split (incl. #2 lazy worker-bucket resolution), the root-bearer hard error, the fail-closed gate move, tests, docs.

**Out:** #1 (the `at-mint` TTL guard) — already shipped, unchanged. Token refresh / long-run credential renewal (the air-gapped refresh problem) remains future work; this design only ensures the bearer is minted as late as possible and its TTL is validated against the run. Per-worker-class *distinct* credentials are supported by the schema but not a goal here (most kits will use `workers.<common>`).
