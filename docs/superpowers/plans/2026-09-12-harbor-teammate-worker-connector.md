# Harbor Connector for Teammate + Worker Sessions (COV-142) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Route a dispatched worker's and a teammate's Anthropic + git through harbor (connector injected, OAuth superseded), reusing the COV-138 snippet contract.

**Architecture:** `dispatchrun` and `connect.LaunchTeammate` gain `HarborHost`/`HarborToken`; when set they inject `snippet.Env` + run `snippet.GitConfig` and skip OAuth. `doWork` auto-enrolls (mint/revoke via `harborPlan`, coveID = worker container); `doTeammate` requires a pre-supplied `identity`.

**Tech Stack:** Go (stdlib) + `internal/harbor/snippet` (stdlib). No new dependency; at-cove/dispatchrun stay go-oidc-free.

## Global Constraints

- at-cove + `internal/dispatchrun` + `internal/connect` stay **go-oidc-free** (import only `snippet`, never `internal/harbor`/`adminclient`).
- Token env-only (existing tmpfs env-scripts); git config token-free; never argv/logs.
- Tests hermetic (`runner.Fake`); real round-trips behind `//go:build integration`.
- Before each commit: `go build ./...`, `go vet ./...`, `gofmt -l internal/ cmd/` clean (leave `internal/switchboard/discord_integration_test.go`).
- Kits without `harbor:` unchanged (worker seeds OAuth creds; teammate runs `ensureAuthenticated`).

---

### Task 1: `dispatchrun` — inject the harbor connector into the agent step

**Files:** Modify `internal/dispatchrun/dispatchrun.go`, `internal/dispatchrun/dispatchrun_test.go`.

**Interfaces:** `Options.HarborHost, Options.HarborToken string` (empty ⇒ off).

- [ ] **Step 1: Read** dispatchrun's Options, the creds-seed (`seedFile(o.CredentialsFile, credsVMPath)` ~line 212), the agent-step env assembly + `runStep`, and how git steps get their env — to find where the agent step's env map is built and where to run the git config.
- [ ] **Step 2: Write the failing test.** With `Options{HarborHost:"h.test", HarborToken:"TKN", …}` (fake runner), assert: the agent step's sourced env contains `ANTHROPIC_BASE_URL=https://h.test/anthropic`, `ANTHROPIC_API_KEY`/`AT_HARBOR_IDENTITY_TOKEN`; a `git config` (from `snippet.GitConfig`) runs in the VM; the OAuth creds-file seed is **not** performed; and the token isn't on argv. Unset harbor → creds seeded, no harbor env (existing behavior).
- [ ] **Step 3: Run to verify it fails.**
- [ ] **Step 4: Implement.** Add the two fields. Where the agent step's env is built, when `o.HarborHost != ""`: merge `snippet.Env("https://"+o.HarborHost, o.HarborToken)` into that env; run `snippet.GitConfig` in the VM (a token-free `runStep`/`writeVM`+ssh `sh`, before the agent step); and gate the `seedFile(o.CredentialsFile,…)` OAuth seed on `o.HarborHost == ""`. Import `internal/harbor/snippet`.
- [ ] **Step 5: Run to verify + vet + gofmt.**
- [ ] **Step 6: Commit** (`feat(cove): dispatchrun injects the harbor connector, superseding OAuth`).

---

### Task 2: `doWork` — auto-enroll the worker + defer revoke

**Files:** Modify `cmd/at-cove/main.go` (`doWork`/the work command), `cmd/at-cove/main_test.go`.

**Interfaces:** Consumes `harborPlan` (COV-141) + the worker container name as coveID.

- [ ] **Step 1: Read** `doWork`: where it builds `dispatchrun.Options`, where the worker container name is known, and where `cfg`/`store`/`expand`/`kitPath`/`secretsPath` are in scope.
- [ ] **Step 2: Write the failing test.** A harbor kit (no `identity`) → `doWork` resolves harbor and sets `Options.HarborHost/HarborToken`; assert (via the seam the work tests already use) the worker enroll happens with the worker container name as `--id` and a revoke is deferred. If `doWork` isn't unit-testable at that seam, test the smallest extracted helper that builds the harbor part of the options.
- [ ] **Step 3: Run to verify it fails.**
- [ ] **Step 4: Implement.** In `doWork`, when `cfg.Harbor != nil`: `auth, revoke, err := harborPlan(cfg, store, expand, cfg.Name, <worker-container-name>, kitPath, secretsPath, r)`; set `opts.HarborHost = cfg.Harbor.Host`, `opts.HarborToken = auth.Token`; `if revoke != nil { defer revoke() }` around `dispatchrun.Run`.
- [ ] **Step 5: Run to verify + build + `go list -deps ./cmd/at-cove | grep -i oidc` empty.**
- [ ] **Step 6: Commit** (`feat(cove): doWork auto-enrolls a harbor worker + revokes per unit`).

---

### Task 3: `LaunchTeammate` — inject the connector, skip OAuth

**Files:** Modify `internal/connect/teammate.go`, `internal/connect/teammate_test.go` (or the connect test file covering teammate).

**Interfaces:** `TeammateOptions.HarborHost, HarborToken string`.

- [ ] **Step 1: Read** `LaunchTeammate` (`internal/connect/teammate.go`): the `ensureAuthenticated` call, how the launch script is built (the `export DISCORD_BOT_TOKEN` region), and where it's written/launched.
- [ ] **Step 2: Write the failing test.** With `HarborHost`/`HarborToken` set, assert `ensureAuthenticated` (`claude auth`) is **not** run, the launch script carries `ANTHROPIC_BASE_URL=https://…/anthropic` + token exports, and a git config runs; unset → unchanged (auth runs, no harbor env).
- [ ] **Step 3: Run to verify it fails.**
- [ ] **Step 4: Implement.** Add the two fields. When `o.HarborHost != ""`: skip `ensureAuthenticated`; append `snippet.Env(...)` exports to the script (or merge into the env it sources); run `snippet.GitConfig` in the VM. Import `snippet`.
- [ ] **Step 5: Run to verify + vet + gofmt.**
- [ ] **Step 6: Commit** (`feat(cove): LaunchTeammate injects the harbor connector, superseding OAuth`).

---

### Task 4: `doTeammate` — resolve harbor (pre-supplied only)

**Files:** Modify `cmd/at-cove/main.go` (`doTeammate`), `cmd/at-cove/main_test.go`.

- [ ] **Step 1: Read** `doTeammate`: where it builds `TeammateOptions`, and the store/expand/secretsPath scope.
- [ ] **Step 2: Write the failing test.** A harbor teammate **without** `identity` → `doTeammate` errors; **with** `identity` (supplied) → sets `TeammateOptions.HarborHost/HarborToken`.
- [ ] **Step 3: Run to verify it fails.**
- [ ] **Step 4: Implement.** In `doTeammate`, when `cfg.Harbor != nil`: if `cfg.Harbor.Identity == ""`, return an error ("teammate harbor requires harbor.identity; auto-enroll is unsupported for a detached teammate"). Else `auth, _, err := harborPlan(cfg, store, expand, st.Name, st.Container, kitPath, secretsPath, r)` and set `opts.HarborHost = cfg.Harbor.Host`, `opts.HarborToken = auth.Token`.
- [ ] **Step 5: Run to verify + build.**
- [ ] **Step 6: Commit** (`feat(cove): doTeammate wires harbor (pre-supplied identity only)`).

---

### Task 5: Docs

**Files:** Modify `docs/usage/at-cove-config.md` (harbor section), `docs/OVERVIEW.md`.

- [ ] **Step 1:** In `at-cove-config.md` `### harbor`, update the "applies to the interactive/managed session this cut" line: the connector now also applies to **dispatch workers** (auto-enroll fits) and **teammates** (pre-supplied `identity` required — auto-enroll unsupported for a detached teammate).
- [ ] **Step 2:** `docs/OVERVIEW.md` — drop/adjust the "chat only" caveat near the cove→harbor sentence.
- [ ] **Step 3:** `just test && STRICT=1 ./scripts/lint.sh`; commit (`docs: harbor connector for teammate + worker (COV-142)`).

---

## Manual verification (definition of done)

1. A dispatched worker of a `harbor:` kit runs its agent step through harbor (auto-enrolled + revoked per unit; OAuth superseded).
2. A teammate of a `harbor:` kit with `identity` routes Anthropic + git through harbor (OAuth superseded); without `identity` → clear error.
3. Kits without `harbor:` — worker + teammate unchanged.

## Self-review

**Spec coverage:** worker injection → Task 1; worker auto-enroll + revoke → Task 2; teammate injection → Task 3; teammate pre-supplied + guard → Task 4; docs → Task 5. Deferred (teammate auto-enroll, configurable scope) absent.

**Placeholder scan:** Tasks begin with a Read of the bespoke path; new fields + branch logic specified; exact-at-implementation after the read.

**Type consistency:** `Options.HarborHost/Token` (Task 1) set by `doWork` (Task 2); `TeammateOptions.HarborHost/Token` (Task 3) set by `doTeammate` (Task 4); both consume `snippet.Env`/`snippet.GitConfig`; `harborPlan(..., coveID, ...)` signature matches COV-141.
