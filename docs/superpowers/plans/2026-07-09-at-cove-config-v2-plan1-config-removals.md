# at-cove config v2 — Plan 1: config removals + renames Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reshape the `at-cove` kit `config.yml` schema to match the canonical usage docs — `secrets` list→map, `image.setup-script`→`setup-scripts`, and removal of `backend`, `setup`, and `loops` — updating every consumer and fixture so the build stays green.

**Architecture:** Five independent, mechanical sub-changes to `internal/kit/config.go`'s `Config` and its consumers, each its own task and commit. `DispatchConfig` is **left intact** so `at-cove dispatch` keeps working; Plan 2 replaces it with `workers`. Because `ParseConfig` uses `KnownFields(true)`, every YAML fixture that names a removed/renamed field must change in the same task or it fails to parse.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**.

**Scope note:** Plan 1 of 3 for [AET-29](https://linear.app/aethons-tools/issue/AET-29). Canonical target: [`docs/usage/at-cove-config.md`](../../usage/at-cove-config.md), [`docs/usage/at-cove-secrets.md`](../../usage/at-cove-secrets.md).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- **`DispatchConfig` and the `dispatch:` block stay** this plan — do not touch dispatch/`workers` (Plan 2). The reference kit keeps its `dispatch:` block + `run-worker.sh` for now.
- **Every commit builds and passes `go test ./...`.** After each task: `go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.
- **Secrets remain memory-only** — the map→`[]secret.Spec` conversion must not put values on argv/disk (it only carries names + resolver commands, as today).
- Task order matters: **1 setup-scripts → 2 secrets → 3 backend → 4 loops → 5 setup** (loops references `setup`, so remove loops first).

## Reference: target `Config` after this plan

```go
// SecretConfig configures a declared secret (keyed by its env var name in the map).
type SecretConfig struct {
	Description string   `yaml:"description"`
	Command     []string `yaml:"command"`
}

type ImageConfig struct {
	SetupScripts   []string          `yaml:"setup-scripts"`
	Paths          []string          `yaml:"paths"`
	Env            map[string]string `yaml:"env"`
	AllowedDomains []string          `yaml:"allowed-domains"`
}

type DispatchConfig struct { /* UNCHANGED — Plan 2 removes */ }

type Config struct {
	Name     string                  `yaml:"name"`
	Secrets  map[string]SecretConfig `yaml:"secrets"`
	Image    ImageConfig             `yaml:"image"`
	Dispatch DispatchConfig          `yaml:"dispatch"`
}
```

(`Backend`, `Setup`, `Loops`, the `Loop` type + `ParsedInterval`, and the `time` import are all gone.)

---

## Task 1: rename `image.setup-script` → `setup-scripts`

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `internal/assemble/assemble.go`, `internal/assemble/assemble_test.go`, `kits/reference-worker/config.yml`, `.at-cove/config.yml`

- [ ] **Step 1: Update the config_test image fixture + error assertion to fail**

In `internal/kit/config_test.go` (`TestParseConfigImage`, ~L158) change the YAML `setup-script:` → `setup-scripts:`, and any assertion referencing `Image.SetupScript` → `Image.SetupScripts`. Run `go test ./internal/kit/ -run Image` → FAIL (unknown field `setup-scripts` until the struct changes).

- [ ] **Step 2: Rename the field + validation in `config.go`**

`internal/kit/config.go`: `SetupScript []string \`yaml:"setup-script"\`` → `SetupScripts []string \`yaml:"setup-scripts"\``. In `ParseConfig`, the loop `for i, s := range cfg.Image.SetupScript` → `cfg.Image.SetupScripts`, and the error string `image.setup-script[%d]` → `image.setup-scripts[%d]`.

- [ ] **Step 3: Update `assemble.go`**

`internal/assemble/assemble.go:56` `img.SetupScript` → `img.SetupScripts`. In `writeSetupManifest` the error strings `image.setup-script %q` (L164, L167) → `image.setup-scripts %q`.

- [ ] **Step 4: Update assemble tests + kit YAMLs**

`internal/assemble/assemble_test.go` L107 & L120: `kit.ImageConfig{SetupScript: …}` → `SetupScripts`. In `kits/reference-worker/config.yml` and `.at-cove/config.yml`: `setup-script:` → `setup-scripts:`.

- [ ] **Step 5: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` — all clean.
```bash
git add internal/kit/config.go internal/kit/config_test.go internal/assemble/assemble.go internal/assemble/assemble_test.go kits/reference-worker/config.yml .at-cove/config.yml
git commit -m "refactor(kit): rename image.setup-script -> setup-scripts"
```

---

## Task 2: `secrets` list → map

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `cmd/at-cove/main.go`, `cmd/at-cove/main_test.go`, `kits/reference-worker/config.yml`, `.at-cove/config.yml`

**Interfaces:** Produces `kit.SecretConfig{Description, Command}` and `Config.Secrets map[string]SecretConfig` (the env var name is the map key). `state.Secret` (name+command list) is **unchanged** — `buildState` now uses the map key as the name.

- [ ] **Step 1: Convert the config_test secret fixtures to the map form (fail first)**

In `internal/kit/config_test.go`: `TestParseConfigValid` (~L10) and `TestParseConfigAllowsCommandlessSecret` (~L55) — change the `secrets:` list to a map:
```yaml
secrets:
  GITHUB_TOKEN:
    command: ["op", "read", "x"]
  ANTHROPIC_API_KEY:
    description: Anthropic key
    command: ["pass", "show", "y"]
```
and update assertions from `cfg.Secrets[0].Name`-style to map lookups (`cfg.Secrets["GITHUB_TOKEN"].Command`). Run `go test ./internal/kit/` → FAIL.

- [ ] **Step 2: Change the type + field + validation in `config.go`**

Replace the `Secret` struct with:
```go
// SecretConfig configures how a declared secret (keyed by its env var name in the
// secrets map) resolves. Command, when present, is the host argv whose stdout is the
// value; when omitted the value is supplied from ~/.config/at-cove/secrets.yml.
type SecretConfig struct {
	Description string   `yaml:"description"`
	Command     []string `yaml:"command"`
}
```
`Config.Secrets []Secret` → `Secrets map[string]SecretConfig \`yaml:"secrets"\``. Replace the `ParseConfig` secrets loop with a non-empty-key check:
```go
for name := range cfg.Secrets {
	if strings.TrimSpace(name) == "" {
		return Config{}, fmt.Errorf("config.yml: secrets: a secret name (map key) must not be empty")
	}
}
```

- [ ] **Step 3: Update the `cmd/at-cove` consumers**

- `buildState` (~L323): `for _, s := range cfg.Secrets { st.Secrets = append(st.Secrets, state.Secret{Name: s.Name, Command: s.Command}) }` → `for name, s := range cfg.Secrets { st.Secrets = append(st.Secrets, state.Secret{Name: name, Command: s.Command}) }`.
- `declaresSecret` helper (~L341): the `for _, s := range cfg.Secrets { if s.Name == name {…} }` loop → `_, ok := cfg.Secrets[name]; return ok`.
- `doDispatch` (~L891): `specs := make([]secret.Spec, len(cfg.Secrets)); for i, s := range cfg.Secrets { specs[i] = secret.Spec{Name: s.Name, Command: s.Command} }` → build by append: `specs := make([]secret.Spec, 0, len(cfg.Secrets)); for name, s := range cfg.Secrets { specs = append(specs, secret.Spec{Name: name, Command: s.Command}) }`.

(`doConnect`/`doLoop` read `st.Secrets` — `state.Secret` list — and are unaffected.)

- [ ] **Step 4: Update the cmd + kit YAML fixtures**

- `cmd/at-cove/main_test.go` `writeLoopKit` (~L655): the `secrets:\n  - name: ANTHROPIC_API_KEY\n  - name: GITHUB_TOKEN\n` string → map form `secrets:\n  ANTHROPIC_API_KEY: {}\n  GITHUB_TOKEN: {}\n`.
- `kits/reference-worker/config.yml` + `.at-cove/config.yml`: convert the `secrets:` list to the map form (e.g. `AT_WORK_GIT_TOKEN:` with its `description`/`command` nested).

- [ ] **Step 5: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean.
```bash
git commit -am "refactor(kit): secrets list -> map keyed by env var name"
```

---

## Task 3: remove `backend` (default colima)

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `cmd/at-cove/main.go`, `cmd/at-cove/main_test.go`, `kits/reference-worker/config.yml`, `.at-cove/config.yml`

**Interfaces:** the backend is no longer a config field; `cmd/at-cove` selects it via a new `const defaultBackend = "colima"`.

- [ ] **Step 1: Drop `backend:` from a config fixture to fail first**

In `internal/kit/config_test.go` remove `backend: colima` from a fixture (e.g. `TestParseConfigValid`) and drop any `cfg.Backend` assertion. Run `go test ./internal/kit/` → FAIL (unknown field `backend`) — confirming the field still exists.

- [ ] **Step 2: Remove the field + required check in `config.go`**

Delete `Backend string \`yaml:"backend"\`` from `Config` and the `if cfg.Backend == "" { … "backend is required" }` block in `ParseConfig`.

- [ ] **Step 3: Default the backend in `cmd/at-cove`**

Add `const defaultBackend = "colima"` near the top of `cmd/at-cove/main.go`. Replace each `getBackend(cfg.Backend, r)` call (`doCreate` ~L290, `doDispatch` ~L855) with `getBackend(defaultBackend, r)`. Fix the dry-run/error strings that print `cfg.Backend`:
- `doCreate` L287: drop the `(backend %s)` fragment (or hard-code `colima`).
- `doDispatch` L862 error: `backend %q does not support dispatch` → use `defaultBackend`.
`buildState` L312 uses `inst.Backend` (from the instance, not config) — leave it.

- [ ] **Step 4: Fix the fixtures + the unsupported-backend test**

- Remove `backend: colima` (or `backend: bogus`) from every config YAML fixture and helper: `cmd/at-cove/main_test.go` `writeKit` (~L24), `writeLoopKit` (~L655), and the inline YAMLs at L708/L843/L871; `internal/kit/config_test.go` fixtures; `kits/reference-worker/config.yml`; `.at-cove/config.yml`.
- `TestDispatchUnsupportedBackendErrors` (~L843) tested `backend: bogus` → "does not support dispatch". With the backend fixed to colima, that premise is gone. **Remove that test** (colima always supports dispatch); note the removal in the commit body. (If the reviewer wants coverage of the "backend lacks DispatchOps" branch, it needs a second registered backend — out of scope here.)

- [ ] **Step 5: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean.
```bash
git commit -am "refactor(kit): remove backend field; default to colima"
```

---

## Task 4: remove `loops` (delete the loop feature)

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `cmd/at-cove/main.go`, `cmd/at-cove/main_test.go`, delete `internal/loop/`, `internal/connect/seed.go` (+ its test), and any now-dead loop helpers in `internal/connect`.

- [ ] **Step 1: Confirm the loop footprint**

Run: `grep -rn "internal/loop\|cfg.Loops\|kit.Loop\|loopContainer\|SeedLoopWorkspace\|createLoopInstance\|\.Loops\b\|ParsedInterval\|RunCheck\|RunAgent" cmd/ internal/ --include=*.go`
This is your deletion checklist. Note which `internal/connect` functions (`SeedLoopWorkspace`, and `RunCheck`/`RunAgent` if present) are called **only** from the loop path — those get deleted too.

- [ ] **Step 2: Remove the loop config + type**

`internal/kit/config.go`: delete the `Loop` struct (L28–34), `ParsedInterval` (L39–41), `Config.Loops` (L88), and the `ParseConfig` loops-validation loop (L113–123). Remove the now-unused `"time"` import.

- [ ] **Step 3: Remove the loop command + orchestration in `cmd/at-cove`**

Delete: the `{Name: "loop", …}` cli.Command (L92–132), `doLoop` (L561–730), `createLoopInstance` (L351–395), `loopContainer` (L334–339), and the `"github.com/aethons-tools/cove/internal/loop"` import (L27). Remove any helpers left unused (e.g. a loop-only `--loop` flag path). Check for dangling refs after: `go build ./cmd/at-cove/`.

- [ ] **Step 4: Delete the loop package + dead connect seeding**

`git rm -r internal/loop`. Delete `internal/connect/seed.go` (`SeedLoopWorkspace`) and its test; if `RunCheck`/`RunAgent` (in `internal/connect`) are now unused (grep confirms zero callers), delete them and their tests too. Do **not** touch `RunSetup`/`connect.Connect` (that's Task 5).

- [ ] **Step 5: Remove loop fixtures/tests**

`internal/kit/config_test.go`: delete `TestParseConfigLoops` (~L96). `cmd/at-cove/main_test.go`: delete `writeLoopKit` (~L655), `TestCreateLoopInstanceRequiresAPIKey` (~L708), and any other loop-only tests (e.g. that exercised `doLoop`/the `loop` command). Grep the test files for `loop` to catch them all.

- [ ] **Step 6: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean. `go.mod` unchanged.
```bash
git commit -am "refactor: remove the loops feature (superseded by the scheduler)"
```

---

## Task 5: remove `setup`

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `cmd/at-cove/main.go`, `cmd/at-cove/main_test.go`, `internal/connect/connect.go`, `internal/connect/setup.go` (+ its test), `internal/state` (only if it persists `Setup`).

- [ ] **Step 1: Confirm the setup footprint**

Run: `grep -rn "cfg.Setup\|\.Setup\b\|RunSetup\|Options{.*Setup\|setupCmd" cmd/ internal/ --include=*.go`. After Task 4, the only callers should be `buildState`/`doConnect` (cmd) and `RunSetup`/`Options.Setup`/`connect.Connect` (connect). (`state.State.Setup` — check if the state struct still carries a `Setup` field; if so it becomes unused and should be removed with its `state_test.go` references.)

- [ ] **Step 2: Remove `Config.Setup`**

`internal/kit/config.go`: delete `Setup string \`yaml:"setup"\`` (L86).

- [ ] **Step 3: Remove the connect seed step**

`internal/connect/connect.go`: delete `Options.Setup` (L~49) and the `RunSetup(r, tgt, env, o.Setup)` call (L101). Delete `internal/connect/setup.go` (`RunSetup`) and its test.

- [ ] **Step 4: Remove the cmd consumers**

`cmd/at-cove/main.go`: in `doConnect`, delete the `setupCmd := st.Setup … ` block (L473) and the `Setup: setupCmd` option (L484). In `buildState`/`saveState` (L316, L331) drop the `Setup`/`setup` argument and the `state.State.Setup` field write. Update `buildState`'s signature to drop the `setup` param. If `state.State` had a `Setup` field, remove it (and its `state_test.go` uses).

- [ ] **Step 5: Remove setup fixtures/tests**

`internal/kit/config_test.go`: delete `TestParseConfigSetup` (~L76). `cmd/at-cove/main_test.go`: the `saveState` test at L499 (`kit.Config{… Setup: "git clone …"}`) — drop the `Setup` field (and any assertion that the seed ran).

- [ ] **Step 6: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean.
```bash
git commit -am "refactor: remove the setup workspace-seed field"
```

---

## Final verification

- [ ] `go test ./...` passes; `go build ./...`, `go vet ./...` clean; `gofmt -l cmd/ internal/` empty; `go.mod` unchanged.
- [ ] `grep -rn "Backend\b" internal/kit/config.go` — no `backend` field; `grep -rn "cfg.Setup\|cfg.Loops\|SetupScript\b\|internal/loop" cmd/ internal/ --include=*.go` — nothing (only `SetupScripts` remains).
- [ ] `ParseConfig` rejects a stray `backend:`/`setup:`/`loops:`/`setup-script:` as an unknown field (add/confirm a small `TestParseConfigRejectsRemovedFields` if not covered).
- [ ] `just build` — three binaries build.
- [ ] `DispatchConfig` + the reference kit's `dispatch:` block + `run-worker.sh` are **untouched** (Plan 2/3 handle them).
- [ ] `at-cove --help` no longer lists `loop`; `at-cove create`/`dispatch` default to colima.

## Notes

- **Reconciliations** (read-and-match): exact line numbers drift as tasks land — re-grep before each task. The Explore map that seeded this plan lists current sites; treat them as approximate. Confirm whether `state.State` carries `Setup`/`Backend` fields before editing `state_test.go`.
- **Why keep `DispatchConfig`:** removing it forces the `dispatch`→`workers` rewrite (Plan 2). Keeping it here makes every Plan 1 commit green and small.
- **Removed features are intentional** (AET-29): `loops` (superseded by the scheduler) and `setup` (including its interactive-`connect` isolated-workspace seed — confirmed acceptable). Shared/`--workspace` bind-mount is unaffected.
