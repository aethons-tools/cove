# Worker Secret Segregation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give workers their own kit secret bucket (`workers.<class>.secrets`), keep it out of chat sessions, resolve it lazily agent-step-only, and hard-error a root-declared agent bearer — so the Anthropic bearer stops leaking into chat (breaking connectors) and the git steps.

**Architecture:** Mirror the existing `collaborators.<class>.secrets` pattern for `workers`. A secret's *location* determines visibility: root = both modes, `collaborators` = chat-only, `workers` = work-only. The work path resolves root up front (all steps) and the worker bucket lazily, injected only into the `claude` agent step.

**Tech Stack:** Go stdlib + `gopkg.in/yaml.v3` (existing). No new dependency.

**Design spec:** [`docs/superpowers/specs/2026-07-15-worker-secret-segregation-design.md`](../specs/2026-07-15-worker-secret-segregation-design.md).

## Global Constraints

- **No new dependency.** Go stdlib + the existing `gopkg.in/yaml.v3`.
- **`secret.Resolve` stays generic** — no `ANTHROPIC_*`/at-mint/TTL knowledge in the resolve/inject path. TTL policy stays in `at-mint` (already shipped, unchanged).
- **The worker bucket never reaches chat or the git steps** — only the `claude` agent step.
- **Reserved names** (`commonKey = "<common>"`, and the well-known subsystem names in `reservedSecretNames`) keep their meaning.
- **Hermetic tests, TDD** — failing test first; drive the config loader / `runner.Fake`; no VM/network. Run via `just test`, `just build`.
- **Docs updated in the same change** (Task 5). One commit per task.
- **Module:** `github.com/aethons-tools/cove`.

---

### Task 1: `Worker.Secrets` schema + `<common>` merge + worker-bucket validation

**Files:**
- Modify: `internal/kit/config.go` (the `Worker` struct ~L54, `ResolvedWorker` ~L63, and the workers validation loop)
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Produces: `Worker.Secrets map[string]SecretConfig` (`yaml:"secrets,omitempty"`); `ResolvedWorker(class)` now also merges `Workers[<common>].Secrets` under the class's own (own wins), exactly like `ResolvedCollaborator`.

- [ ] **Step 1: Write the failing test**

```go
func TestResolvedWorkerMergesCommonSecrets(t *testing.T) {
	cfg := Config{Name: "k", Workers: map[string]Worker{
		commonKey:     {Secrets: map[string]SecretConfig{"ANTHROPIC_AUTH_TOKEN": {}, "SHARED": {}}},
		"implementor": {Prompt: "impl", Secrets: map[string]SecretConfig{"SHARED": {Description: "own wins"}}},
	}}
	w, err := cfg.ResolvedWorker("implementor")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w.Secrets["ANTHROPIC_AUTH_TOKEN"]; !ok {
		t.Fatal("class must inherit <common> worker secret")
	}
	if w.Secrets["SHARED"].Description != "own wins" {
		t.Fatalf("own secret must override <common>; got %+v", w.Secrets["SHARED"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run TestResolvedWorkerMergesCommonSecrets -v`
Expected: FAIL — `Worker` has no `Secrets` field.

- [ ] **Step 3: Implement**

In `config.go`, add the field to `Worker`:

```go
type Worker struct {
	Prompt      string                  `yaml:"prompt,omitempty"`
	Timeout     string                  `yaml:"timeout,omitempty"`
	Concurrency int                     `yaml:"concurrency,omitempty"`
	Secrets     map[string]SecretConfig `yaml:"secrets,omitempty"`
}
```

Extend `ResolvedWorker` to merge `<common>` secrets (mirror `ResolvedCollaborator`) before returning:

```go
	base := c.Workers[commonKey] // zero value if absent
	if own.Timeout == "" {
		own.Timeout = base.Timeout
	}
	if own.Concurrency == 0 {
		own.Concurrency = base.Concurrency
	}
	merged := map[string]SecretConfig{}
	for k, v := range base.Secrets {
		merged[k] = v
	}
	for k, v := range own.Secrets {
		merged[k] = v
	}
	own.Secrets = merged
	return own, nil
```

In the workers validation loop (where each worker class is validated — near the `workers[%q]` prompt/timeout checks), reject reserved names in the bucket:

```go
	if err := rejectReservedSecretNames(fmt.Sprintf("workers[%q].secrets", class), w.Secrets); err != nil {
		return Config{}, err
	}
```

- [ ] **Step 4: Add a validation test + run**

```go
func TestWorkerBucketRejectsReservedName(t *testing.T) {
	yml := "name: k\nworkers:\n  implementor:\n    prompt: p\n    timeout: 30m\n    secrets:\n      AT_TASK_GIT_TOKEN: {}\n"
	if _, err := Parse([]byte(yml)); err == nil {
		t.Fatal("a reserved subsystem name under workers.secrets must be rejected")
	}
}
```

(Use whatever the package's parse-from-bytes entry point is — mirror an existing `config_test.go` case; if it's `Parse`/`load`, match it.)

Run: `go test ./internal/kit/ -run 'ResolvedWorker|WorkerBucket' -v` → PASS. Then `go test ./internal/kit/` (full package).

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): workers.<class>.secrets bucket with <common> merge"
```

---

### Task 2: Hard error on a root-declared agent bearer

**Files:**
- Modify: `internal/kit/config.go` (root `secrets` validation block, ~L251-256, beside `rejectReservedSecretNames("secrets", cfg.Secrets)`)
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Produces: `kit.Load`/`Parse` now returns an error when `secrets` (root) contains `ANTHROPIC_AUTH_TOKEN` or `ANTHROPIC_API_KEY`, with a migration message.

- [ ] **Step 1: Write the failing test**

```go
func TestRootBearerIsRejectedWithMigrationNote(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		yml := "name: k\nsecrets:\n  " + name + ": {}\n"
		_, err := Parse([]byte(yml))
		if err == nil {
			t.Fatalf("%s at root must be rejected", name)
		}
		if !strings.Contains(err.Error(), "workers") {
			t.Fatalf("%s error must point to workers.<class>.secrets; got %v", name, err)
		}
	}
	// The same name under workers is fine.
	ok := "name: k\nworkers:\n  implementor:\n    prompt: p\n    timeout: 30m\n    secrets:\n      ANTHROPIC_AUTH_TOKEN: {}\n"
	if _, err := Parse([]byte(ok)); err != nil {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN under workers must load; got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/kit/ -run TestRootBearerIsRejectedWithMigrationNote -v`
Expected: FAIL — root bearer currently loads.

- [ ] **Step 3: Implement**

In `config.go`, add a package-level set and check it in the root-`secrets` validation (right after the existing `rejectReservedSecretNames("secrets", cfg.Secrets)`):

```go
// rootRejectedBearers are agent-auth credentials that must NOT live at the kit
// root: root secrets are injected into `chat` too, where an Anthropic bearer
// outranks the subscription login and disables the session's connectors. They
// belong under workers.<class>.secrets.
var rootRejectedBearers = map[string]bool{
	"ANTHROPIC_AUTH_TOKEN": true,
	"ANTHROPIC_API_KEY":    true,
}

func rejectRootBearers(got map[string]SecretConfig) error {
	for k := range got {
		if rootRejectedBearers[k] {
			return fmt.Errorf("config.yml: secrets: %q must be declared under workers.<class>.secrets (or workers.<common>.secrets), not at the root — a root agent bearer is injected into `chat` sessions too, where it outranks the subscription login and disables their connectors; move it under `workers:`", k)
		}
	}
	return nil
}
```

Call it in the root-secrets validation:

```go
	if err := rejectReservedSecretNames("secrets", cfg.Secrets); err != nil {
		return Config{}, err
	}
	if err := rejectRootBearers(cfg.Secrets); err != nil {
		return Config{}, err
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/kit/ -run TestRootBearer -v` → PASS. Then `go test ./internal/kit/`.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): reject a root-declared ANTHROPIC bearer with a migration note"
```

---

### Task 3: `dispatchrun` — lazy, agent-step-only worker-secret injection

**Files:**
- Modify: `internal/dispatchrun/dispatchrun.go` (`Options`, the agent-step section ~L185-190)
- Test: `internal/dispatchrun/dispatchrun_test.go`

**Interfaces:**
- Consumes: `secret.Spec`, `secret.Resolve` (existing).
- Produces: `Options.WorkerSecrets []secret.Spec` — the worker-class bucket. `Options.Secrets` now carries **root (shared)** secrets only. The agent step runs on `root + resolve(WorkerSecrets)`; the git steps (`mint()`) stay `root + git-token` (worker bucket absent).

- [ ] **Step 1: Write the failing test**

Model on the package's existing dispatch test (it drives `runner.Fake` and inspects the env written to the VM via `writeVM`/`envScript`). Assert: (a) a `WorkerSecrets` entry appears in the **agent** step's sourced env, and (b) it does **not** appear in the `prepare`/`complete` step envs.

```go
func TestWorkerSecretsInjectedOnlyAtAgentStep(t *testing.T) {
	// fake runner captures each `ssh … set -a; . env; …` remote command; the env
	// file content is written just before via writeVM. Capture per-step env.
	// (Adapt to the package's existing capture helper — see how the current
	// dispatch test asserts on prepare/agent/complete commands.)
	f := newDispatchFake(t) // existing helper or the pattern used by current tests
	err := Dispatch(Options{
		/* … existing minimal fields … */
		Secrets:       []secret.Spec{{Name: "SHARED", Value: "s"}},
		WorkerSecrets: []secret.Spec{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "tok"}},
		/* GitToken, Ops, R: f, InputPath (a task.json with class implementor), … */
	})
	// ... assert Dispatch reached the agent step ...
	if !f.agentEnvHas("ANTHROPIC_AUTH_TOKEN=tok") {
		t.Fatal("worker secret must be in the agent step env")
	}
	if f.prepareEnvHas("ANTHROPIC_AUTH_TOKEN") || f.completeEnvHas("ANTHROPIC_AUTH_TOKEN") {
		t.Fatal("worker secret must NOT reach the git steps")
	}
	if !f.agentEnvHas("SHARED=s") {
		t.Fatal("root shared secret must still reach the agent")
	}
	_ = err
}
```

(The exact capture mechanism must match the existing `dispatchrun_test.go`. If there's no per-step env capture helper yet, add a minimal one that records the `envScript` bytes written before each step — keep it in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/dispatchrun/ -run TestWorkerSecretsInjectedOnlyAtAgentStep -v`
Expected: FAIL — `Options` has no `WorkerSecrets`; nothing injects it.

- [ ] **Step 3: Implement**

Add the field to `Options` (beside `Secrets`/`GitToken`):

```go
	Secrets       []secret.Spec // root (shared) secrets — resolved up front, all steps
	WorkerSecrets []secret.Spec // worker-class bucket — resolved lazily, agent step only
	GitToken      secret.Spec
```

In `Dispatch`, at the agent step (replacing the current `_ = runStep(o.R, tgt, base, agentCmd, o.Timeout)`), resolve the worker bucket lazily and merge it over `base` for the agent env only:

```go
	// Resolve the worker-class bucket now — immediately before the agent runs —
	// so a freshly minted bearer's TTL only has to cover the agent's own run
	// (the build/prepare overhead is already spent). It is merged only into the
	// agent env; the git steps never carry it.
	agentEnv := base
	if len(o.WorkerSecrets) > 0 {
		ws, err := secret.Resolve(o.R, runEnv, o.WorkerSecrets)
		if err != nil {
			return egressOr(o.R, tgt, o.OutputPath, fmt.Errorf("resolve worker secrets: %w", err))
		}
		agentEnv = make(map[string]string, len(base)+len(ws))
		for k, v := range base {
			agentEnv[k] = v
		}
		for k, v := range ws {
			agentEnv[k] = v
		}
	}
	agentCmd := fmt.Sprintf("claude -p --dangerously-skip-permissions \"$(cat %s)\" 2>&1 | tee %s",
		shellQuote(promptVMPath), shellQuote(agentLogVMPath))
	_ = runStep(o.R, tgt, agentEnv, agentCmd, o.Timeout) // agent: root + worker bucket; no git token
```

Leave `base` (= `secret.Resolve(o.Secrets)` at the top) and the `mint()` closure unchanged — the git steps stay `base + git-token`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/dispatchrun/ -v` → PASS. Then `just build`.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatchrun/dispatchrun.go internal/dispatchrun/dispatchrun_test.go
git commit -m "feat(dispatch): resolve the worker secret bucket lazily, agent-step only"
```

---

### Task 4: `doWork` wiring + fail-closed gate on the worker bucket

**Files:**
- Modify: `cmd/at-cove/main.go` (`doWork`: read the task class, split root vs worker demanded, plan both, pass `WorkerSecrets`, move the gate)
- Test: `cmd/at-cove/main_test.go`

**Interfaces:**
- Consumes: `Config.ResolvedWorker(class)` (Task 1), `dispatchrun.Options.WorkerSecrets` (Task 3), `store.Plan`, `worker.Task` (the `--in` task shape).
- Produces: `doWork` resolves the dispatched class's worker bucket and passes it as `WorkerSecrets`; `Options.Secrets` now carries root only; the fail-closed gate checks the **worker class's** bearer.

- [ ] **Step 1: Write the failing test**

Extend the existing work tests (`cmd/at-cove/main_test.go`). The kit now declares the bearer under `workers.implementor.secrets`, supplied via the hermetic `secrets.yml`. Assert: a work run whose kit declares `ANTHROPIC_AUTH_TOKEN` under the *worker* bucket resolves and reaches dispatch (the existing `TestWorkFailsClosedWhenAgentBearerUnresolved` moves to the worker bucket); a kit whose worker class does NOT declare the bearer fails closed pre-VM (no ssh/docker run).

```go
func TestWorkFailsClosedWhenWorkerBearerUnresolved(t *testing.T) {
	// kit: workers.implementor.secrets: { ANTHROPIC_AUTH_TOKEN: {} }, no supply.
	kitDir := writeWorkerBearerKit(t) // adapt writeMinimalWorkerKit/workerBearerKitConfig from the existing test
	seedConfigDir(t)                  // hermetic; supply AT_TASK_GIT_TOKEN so the bearer is the sole unresolved
	var stderr bytes.Buffer
	f := &runner.Fake{}
	code := run([]string{"work", "--kit-dir", kitDir, "--in", writeTaskJSON(t), "--out", tmpOut(t)},
		f, os.LookupEnv, dummyLookPath, io.Discard, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit when the worker bearer is unresolved")
	}
	if !strings.Contains(stderr.String(), "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("error must name the bearer; got %q", stderr.String())
	}
	if f.Ran("ssh") || dockerArg0Index(f.Calls, "run") >= 0 {
		t.Fatal("must abort before any VM/SSH")
	}
}
```

(Reuse the exact helpers the existing `TestWorkFailsClosedWhenAgentBearerUnresolved` used — `seedConfigDir`, `writeTaskJSON`, `dockerArg0Index`, the secrets.yml supply pattern — and move the bearer from the kit's root `secrets` to `workers.implementor.secrets`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestWorkFailsClosedWhenWorkerBearerUnresolved -v`
Expected: FAIL — `doWork` reads root secrets, not the worker bucket, so the kit (bearer only under workers) is treated as "no bearer" incorrectly, or the config no longer parses with the bearer at root.

- [ ] **Step 3: Implement**

In `doWork`, after loading `cfg` and the secret `store`:

1. **Read the task's class** from `--in`:

```go
	taskBytes, err := os.ReadFile(*inPath)
	if err != nil { /* existing error handling */ }
	var task worker.Task
	if err := json.Unmarshal(taskBytes, &task); err != nil { /* error */ }
	rw, err := cfg.ResolvedWorker(task.Worker.Class)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
```

2. **Two demand sets** — root (shared) and the worker bucket:

```go
	rootDemanded := keysOf(cfg.Secrets)      // helper or inline loop
	workerDemanded := keysOf(rw.Secrets)
	rootSpecs, rootUnresolved, err := store.Plan(cfg.Name, kitPath, rootDemanded, expand)
	// … existing error/warn handling for rootUnresolved …
	workerSpecs, workerUnresolved, err := store.Plan(cfg.Name, kitPath, workerDemanded, expand)
	// … warn on workerUnresolved too …
```

3. **Gate on the worker bucket** (replace the current `cfg.Secrets[agentBearerSecret]` check):

```go
	const agentBearerSecret = "ANTHROPIC_AUTH_TOKEN"
	bearerUnresolved := false
	if _, declared := rw.Secrets[agentBearerSecret]; !declared {
		bearerUnresolved = true
	} else {
		for _, name := range workerUnresolved {
			if name == agentBearerSecret { bearerUnresolved = true; break }
		}
	}
	// … existing fail-closed UserError + return 1 …
```

4. **Pass both** to dispatch:

```go
	err = dispatchrun.Dispatch(dispatchrun.Options{
		// …
		Secrets:       rootSpecs,
		WorkerSecrets: workerSpecs,
		GitToken:      gitTok,
		// …
	})
```

Add `import "encoding/json"` and `".../internal/dispatch/worker"` to `main.go` if not present. Provide a small `keysOf(map[string]kit.SecretConfig) []string` helper (or inline the loops).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/at-cove/ -run TestWork -v` → PASS. Then `just test` (full suite) and `just build`.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-cove/main.go cmd/at-cove/main_test.go
git commit -m "feat(work): resolve the dispatched class's worker secret bucket; gate the bearer there"
```

---

### Task 5: Docs

**Files:**
- Modify: `docs/usage/at-cove-config.md` (add `workers.<class>.secrets` + `<common>`, and the bucket-visibility table)
- Modify: `docs/usage/at-cove-secrets.md` (the segregation model + the root-bearer migration note)
- Modify: `docs/OVERVIEW.md` (secret-bucket summary, if it enumerates them)

**Interfaces:** none (docs).

- [ ] **Step 1: Update `at-cove-config.md`**

Add a `workers.<class>.secrets` subsection mirroring the `collaborators.<class>.secrets` docs, and the bucket-visibility table:

```markdown
| Bucket | `chat` | `work`/`dispatch` |
|---|---|---|
| root `secrets` | ✅ | ✅ |
| `collaborators.<class>.secrets` | ✅ | — |
| `workers.<class>.secrets` | — | ✅ (agent step only) |
| `source-control.<host>.secrets` | — | ✅ (git steps) |
```

Note the `<common>` merge for the worker tree (as documented for collaborators).

- [ ] **Step 2: Update `at-cove-secrets.md`**

State that a secret's **location determines visibility** (root = both modes, `workers` = work-only, `collaborators` = chat-only), the chat-connector rationale (an Anthropic bearer outranks the subscription login), and the migration note: `ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY` must be declared under `workers:`, not root, and doing otherwise is a hard config error.

- [ ] **Step 3: Update `docs/OVERVIEW.md`** if it enumerates the secret buckets — add `workers.<class>.secrets` (work-only) alongside the existing list.

- [ ] **Step 4: Sanity-check links/tables render** (no broken references; `docs/usage/INDEX.md` already lists these docs, so no INDEX change).

- [ ] **Step 5: Commit**

```bash
git add docs/
git commit -m "docs: workers.<class>.secrets bucket, visibility model, and root-bearer migration"
```

---

## Final verification (after Task 5)

- [ ] `just test` — all hermetic tests green.
- [ ] `just build` — `at-cove`/`at-task`/`at-mint` build.
- [ ] `just lint` — `go vet` clean; changed files `gofmt`-clean (ignore pre-existing module-cache noise).
- [ ] Grep check: no code path outside config validation / the fail-closed gate references `ANTHROPIC_AUTH_TOKEN`/`ANTHROPIC_API_KEY` by name (the resolve/inject path stays generic).
- [ ] A kit declaring the bearer at root fails `kit.Load` with the migration note; the same under `workers:` loads and dispatches.

## Spec Coverage Note

Task 1 = spec §3 (schema + `<common>` merge + worker-bucket reserved-name validation). Task 2 = spec §5 (root-bearer hard error, both names). Task 3 = spec §4 (lazy, agent-step-only worker injection; git steps tightened). Task 4 = spec §4 wiring + §6 (fail-closed gate moves to the worker bucket). Task 5 = spec §8 (docs). Spec §2 governing decisions are realized across Tasks 1–4; #1 (the `at-mint` TTL guard, spec §9 "out") is untouched and simply runs at the lazier resolution point from Task 3.
