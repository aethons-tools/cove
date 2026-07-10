# Unified kit config — Plan 2: scheduler reads the kit + CLI

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire the standalone `internal/dispatch/config` package — point the scheduler + `linear.New` at `kit.Config`, and switch `at-cove dispatch` from `--config <file>` to the standard positional `[kit-dir]`. After this, one `config.yml` drives every `at-cove` subcommand. Also fold in two defense-in-depth validation hardenings the Plan 1 review flagged, and collapse the scheduler config doc into `at-cove-config.md`.

**Architecture:** Plan 1 put the tracker/dispatch/workers schema into `kit.Config`. This plan makes the scheduler *consume* it: `scheduler.New`/`linear.New` take `kit.Config` (plus the single `kitDir`), the per-class kit path disappears (one kit per repo), the autonomous/interactive split comes from workers-vs-collaborators membership, and `doDispatch` loads the kit like every other command. The `internal/dispatch/config` package is deleted.

**Tech Stack:** Go 1.22 — **no new dependencies**. TDD; every commit builds + `go test ./...` green; `gofmt` clean.

**Design:** [`docs/superpowers/specs/2026-07-10-unified-kit-config-design.md`](../specs/2026-07-10-unified-kit-config-design.md) §2 (CLI), §7 (component changes), §10 (plan 3). Plan 1 (merged) reshaped `kit.Config`.

## Global Constraints

- **No new `go.mod`/`go.sum` deps.** Verify unchanged after every task.
- **`internal/dispatch/config` is deleted** — the scheduler, `linear`, and `cmd/at-cove` read `kit.Config` directly. No re-derived duplicate config type.
- **`at-cove dispatch [kit-dir]`** — optional positional, default `.`, resolved via the same `kitDirArg` the other commands use; **`--config` is removed**.
- **Command-scoped validation:** `dispatch` requires `source-control`, `tracker`, `dispatch`, and ≥1 `workers` class; error clearly if any is missing.
- **Field mapping (config → kit), authoritative:**

  | old (`config.Config`) | new (`kit.Config`) |
  |---|---|
  | `cfg.Concurrency` | `cfg.Dispatch.Concurrency` |
  | `cfg.DispatchOverhead` | `cfg.Dispatch.DispatchOverhead` |
  | `cfg.Classes[c].Timeout` | `cfg.ResolvedWorker(c).Timeout` |
  | `cfg.Classes[c].Concurrency` | `cfg.ResolvedWorker(c).Concurrency` |
  | `cfg.Classes[c].Kit` | the single `kitDir` (passed to `scheduler.New`) |
  | `cfg.Classes[c].Mode == "autonomous"` | `iss.Class ∈ cfg.Workers` (i.e. `ResolvedWorker(c)` returns no error) |
  | `cfg.Tracker.PollInterval` | `cfg.Tracker.Linear.PollInterval` |
  | `cfg.Tracker.Team` | `cfg.Tracker.Linear.Team` |
  | `cfg.Tracker.ClassLabelPrefix` | `cfg.Tracker.Linear.ClassLabelPrefix` |
  | `cfg.Tracker.States` (`config.StateMap`) | `cfg.Tracker.Linear.States` (`kit.StateMap`) |
  | `cfg.Tracker.Token.Command` | `cfg.Tracker.Linear.Secrets["AT_DISPATCH_TRACKER_TOKEN"].Command` |

- **Scheduler behavior otherwise unchanged** — poll → claim → `at-cove work` → broker, single-writer, per-class + global concurrency, reaper. Only the config *source* moves.

---

## Task 1: retire `internal/dispatch/config`; scheduler + linear + `doDispatch` read the kit

This is one atomic change (the config *type* is swapped everywhere it flows). Work it as: update `linear` → `scheduler` → `doDispatch` → delete the package → fix tests, verifying `go build ./...` only at the end of the task (intermediate states won't compile).

**Files:** `internal/dispatch/linear/linear.go`, `internal/dispatch/scheduler/scheduler.go`, `internal/dispatch/scheduler/engine.go`, `cmd/at-cove/main.go`; delete `internal/dispatch/config/config.go` + `internal/dispatch/config/config_test.go`; tests `internal/dispatch/scheduler/engine_test.go`, `internal/dispatch/linear/linear_test.go`, `internal/dispatch/linear/linear_integration_test.go`, `cmd/at-cove/main_test.go`.

**Interfaces produced:**
- `linear.New(cfg kit.Config, token string, httpc *http.Client) (*Client, error)` — reads `cfg.Tracker.Linear.{Team,ClassLabelPrefix,States}`; `Client.states` becomes `kit.StateMap`.
- `scheduler.New(cfg kit.Config, kitDir string, t Tracker, e Executor, logger *log.Logger) *Engine` — `Engine` stores `cfg kit.Config` + `kitDir string`.

- [ ] **Step 1: `linear.go` — consume `kit.Config`**

Change the import `internal/dispatch/config` → `internal/kit`. Update `New` and the `Client.states` field type:

```go
func New(cfg kit.Config, token string, httpc *http.Client) (*Client, error) {
	if cfg.Tracker == nil || cfg.Tracker.Linear == nil {
		return nil, fmt.Errorf("linear: kit declares no tracker.linear")
	}
	lt := cfg.Tracker.Linear
	// … same client construction, but:
	//   team:   lt.Team
	//   prefix: lt.ClassLabelPrefix
	//   states: lt.States   // now kit.StateMap
}
```

Change the `states` struct field's type from `config.StateMap` to `kit.StateMap` (same field names — `Ready`/`InProgress`/…), and any other `config.` references in the file to their `kit.` equivalents. (Add `"fmt"` if not already imported.)

- [ ] **Step 2: `scheduler.go`/`engine.go` — consume `kit.Config` + `kitDir`**

Change the import `internal/dispatch/config` → `internal/kit`. In the `Engine` struct, `cfg config.Config` → `cfg kit.Config`, and add `kitDir string`. Rewrite `New`:

```go
func New(cfg kit.Config, kitDir string, t Tracker, e Executor, logger *log.Logger) *Engine {
	gcap := 1
	if cfg.Dispatch != nil && cfg.Dispatch.Concurrency > 0 {
		gcap = cfg.Dispatch.Concurrency
	}
	csem := map[string]chan struct{}{}
	for name := range cfg.Workers {
		rw, err := cfg.ResolvedWorker(name) // skips <common> (errors) and applies the base
		if err != nil {
			continue
		}
		if rw.Concurrency > 0 {
			csem[name] = make(chan struct{}, rw.Concurrency)
		}
	}
	return &Engine{cfg: cfg, kitDir: kitDir, tracker: t, exec: e, log: logger,
		gsem: make(chan struct{}, gcap), csem: csem}
}
```

In `handle`, replace the `cl := e.cfg.Classes[iss.Class]` lookup + its uses:

```go
	rw, err := e.cfg.ResolvedWorker(iss.Class)
	if err != nil {
		return // not a dispatchable worker class (defensive; the Run filter already gates this)
	}
	// … later:
	work, _ := time.ParseDuration(rw.Timeout)
	over := 15 * time.Minute
	if e.cfg.Dispatch != nil && e.cfg.Dispatch.DispatchOverhead != "" {
		over, _ = time.ParseDuration(e.cfg.Dispatch.DispatchOverhead)
	}
	rctx, cancel := context.WithTimeout(ctx, work+over)
	// …
	argv := []string{"at-cove", "work", e.kitDir, "--in", inPath, "--out", outPath, "--timeout", rw.Timeout}
```

In `Run`, the poll interval: `time.ParseDuration(e.cfg.Tracker.Linear.PollInterval)`. Replace the dispatch filter (`cl, ok := e.cfg.Classes[iss.Class]; if !ok || cl.Mode != "autonomous"`) with:

```go
		if _, err := e.cfg.ResolvedWorker(iss.Class); err != nil {
			continue // skip interactive (collaborator) / unknown / <common> classes
		}
```

Also change the temp-dir prefix `os.MkdirTemp("", "at-cove-dispatch-")` stays as-is (already correct from the CLI rename).

- [ ] **Step 3: `cmd/at-cove/main.go` — `doDispatch` loads the kit**

Rewrite `doDispatch` to take the positional kit-dir (drop `--config`) and read the tracker token from the kit. Keep its existing signature `doDispatch(args []string, stdout, stderr io.Writer) int` and the existing registration (`return doDispatch(args, out, errw)`); the token resolver uses `runner.OS{}` internally, as it does today. The new body:

```go
func doDispatch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pos, err := cli.ParseInterspersed(fs, args)
	if err != nil {
		return 2
	}
	kitDir, code := kitDirArg(pos, "dispatch", stderr)
	if code != 0 {
		return code
	}
	cfg, err := kit.Load(kitDir)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	// dispatch requires the full scheduler surface.
	if cfg.SourceControl == nil || cfg.Tracker == nil || cfg.Tracker.Linear == nil || cfg.Dispatch == nil || len(cfg.Workers) == 0 {
		fmt.Fprintln(stderr, "at-cove dispatch: kit must declare source-control, tracker.linear, dispatch, and at least one worker")
		return 1
	}

	classes := make([]string, 0, len(cfg.Workers))
	for name := range cfg.Workers {
		if _, err := cfg.ResolvedWorker(name); err == nil {
			classes = append(classes, name)
		}
	}
	sort.Strings(classes)
	fmt.Fprintf(stdout, "at-cove dispatch: kit OK — %d worker class(es): %s\n", len(classes), strings.Join(classes, ", "))

	tokSpec := cfg.Tracker.Linear.Secrets["AT_DISPATCH_TRACKER_TOKEN"]
	out, err := runner.OS{}.Output(tokSpec.Command[0], tokSpec.Command[1:]...)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: resolve tracker token: %v\n", err)
		return 1
	}
	token := strings.TrimSuffix(out, "\n")

	tracker, err := linear.New(cfg, token, nil)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: connect to Linear: %v\n", err)
		return 1
	}
	logger := log.New(stderr, "at-cove dispatch ", log.LstdFlags)
	engine := scheduler.New(cfg, kitDir, tracker, dexec.New(), logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Printf("scheduler started (poll %s); Ctrl-C to stop", cfg.Tracker.Linear.PollInterval)
	_ = engine.Run(ctx)
	logger.Printf("scheduler stopped")
	return 0
}
```

Leave the command registration as-is (`return doDispatch(args, out, errw)`). Drop the now-unused `internal/dispatch/config` import; keep `kit`, `linear`, `scheduler`, `dexec`, `sort`, `strings`, `log`, `context`, `os/signal`, `syscall`. (`kitDirArg` and `kit.Load` already exist and are used by other commands.)

> The well-known validation from Plan 1 guarantees `AT_DISPATCH_TRACKER_TOKEN` has a non-empty `Command` whenever `tracker.linear` is present, so `tokSpec.Command[0]` is safe after the presence check above.

- [ ] **Step 4: Delete the retired package**

```bash
git rm internal/dispatch/config/config.go internal/dispatch/config/config_test.go
```

- [ ] **Step 5: Convert the tests**

- `internal/dispatch/scheduler/engine_test.go`: `testConfig()` returns a `kit.Config` per the mapping — e.g.:

  ```go
  func testConfig() kit.Config {
  	return kit.Config{
  		Tracker:  &kit.Tracker{Linear: &kit.LinearTracker{PollInterval: "1m"}},
  		Dispatch: &kit.Dispatch{Concurrency: 4, DispatchOverhead: "15m"},
  		Workers:  map[string]kit.Worker{"implement": {Prompt: "impl", Timeout: "30m"}},
  	}
  }
  ```

  `newEngine` calls `New(cfg, "/kits/implement", tr, ex, …)`. The argv assertion now expects `at-cove work /kits/implement …` (the kitDir, not a per-class kit). The global-concurrency test sets `cfg.Dispatch.Concurrency = 1`. Any test that dispatched a class must have that class in `Workers`.
- `internal/dispatch/linear/linear_test.go` + `linear_integration_test.go`: `testCfg()` returns `kit.Config{Tracker: &kit.Tracker{Linear: &kit.LinearTracker{Team: …, ClassLabelPrefix: …, States: kit.StateMap{…}}}}`; `New(testCfg(), "tok", …)`.
- `cmd/at-cove/main_test.go`: the three `dispatch` tests (`TestDispatchTokenResolveFailure`, `TestDispatchRejectsBadConfig`, `TestDispatchRequiresConfig`) now drive a **kit dir**, not a `--config` file. Rewrite `writeDispatchConfig`/`dispatchGoodConfig` to write a **kit `config.yml`** into a temp dir (name + source-control + tracker.linear + dispatch + workers), and invoke `run([]string{"dispatch", <dir>}, …)`:
  - token-fail: set `AT_DISPATCH_TRACKER_TOKEN.command: ["false"]` → exit 1, stderr mentions "token".
  - bad-config: omit `tracker` → exit 1, stderr mentions the missing-surface message.
  - rename `TestDispatchRequiresConfig` → `TestDispatchRejectsIncompleteKit` (there is no `--config` to require now); assert a kit missing `tracker`/`dispatch`/`workers` exits 1. Keep passing `&runner.Fake{}` as `r` (the resolver uses `runner.OS{}` internally).

- [ ] **Step 6: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
go build -tags integration ./internal/dispatchrun/ ./internal/dispatch/...
grep -rn "dispatch/config" cmd/ internal/ --include=*.go      # expect: nothing
grep -rn "\-\-config" cmd/at-cove/ --include=*.go             # expect: nothing (dispatch flag gone)
git diff --stat go.mod go.sum
```
Expected: green; the package is gone; no `--config`; deps unchanged. `at-cove dispatch ./kits/reference-worker` now loads the kit.
```bash
git commit -am "feat(dispatch): scheduler reads the kit; at-cove dispatch [kit-dir]; retire internal/dispatch/config"
```

---

## Task 2: defense-in-depth secret validation (deferred Plan 1 minors)

Two hardenings the Plan 1 review flagged.

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/kit/config_test.go`:

```go
func TestParseConfigRejectsWellKnownNameInRootSecrets(t *testing.T) {
	for _, name := range []string{"AT_TASK_GIT_TOKEN", "AT_DISPATCH_TRACKER_TOKEN", "AT_DISPATCH_WEBHOOK_SECRET"} {
		src := "name: k\nsecrets:\n  " + name + ": { command: [\"x\"] }\n"
		if _, err := ParseConfig([]byte(src)); err == nil {
			t.Fatalf("root secrets must not accept the reserved name %q", name)
		}
	}
}

func TestParseConfigRejectsWellKnownNameInCollaboratorSecrets(t *testing.T) {
	src := "name: k\ncollaborators:\n  triager:\n    secrets:\n      AT_TASK_GIT_TOKEN: { command: [\"x\"] }\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("collaborator secrets must not accept a reserved subsystem name")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/kit/ -run WellKnownName`
Expected: FAIL (currently accepted).

- [ ] **Step 3: Reject the reserved names outside their trees**

Add a helper + call it for root `secrets` and every collaborator's `secrets` (including `<common>`). The reserved set = the subsystem well-known names, which must live only under `source-control`/`tracker`:

```go
var reservedSecretNames = map[string]bool{
	"AT_TASK_GIT_TOKEN":          true,
	"AT_DISPATCH_TRACKER_TOKEN":  true,
	"AT_DISPATCH_WEBHOOK_SECRET": true,
}

// rejectReservedSecretNames forbids the subsystem well-known secret names in a
// general (VM-injected / collaborator) secrets map, so a misconfigured entry
// can never shadow the host-side, air-gapped subsystem credentials.
func rejectReservedSecretNames(field string, got map[string]SecretConfig) error {
	for k := range got {
		if reservedSecretNames[k] {
			return fmt.Errorf("config.yml: %s: %q is a reserved subsystem secret and must be declared under source-control/tracker, not here", field, k)
		}
	}
	return nil
}
```

In `ParseConfig`: after the existing empty-name check on root `cfg.Secrets`, add `if err := rejectReservedSecretNames("secrets", cfg.Secrets); err != nil { return Config{}, err }`. In the `collaborators` validation, for each collaborator (including `<common>`) call `rejectReservedSecretNames(fmt.Sprintf("collaborators[%q].secrets", name), col.Secrets)`.

> This is the belt-and-suspenders for the structural air-gap: even a mistaken root/collaborator entry named `AT_TASK_GIT_TOKEN` can no longer flow into the agent bucket. (The second Plan 1 minor — whether `tracker` secrets should allow a `secrets.yml` fallback — is intentionally left as-is: the scheduler resolves them via a host `command`, matching how `doDispatch` runs `tokSpec.Command`. Revisit only if a standing-value fallback is ever needed.)

- [ ] **Step 4: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
git diff --stat go.mod go.sum
```
Expected: green; the two new tests pass; deps unchanged.
```bash
git commit -am "feat(kit): reject reserved subsystem secret names in root/collaborator secrets (air-gap defense-in-depth)"
```

---

## Task 3: collapse the scheduler config doc

`at-cove dispatch` now reads the kit, so the separate scheduler-config doc is redundant.

**Files:** `docs/orchestration/scheduler-config.md` (delete), `docs/orchestration/INDEX.md`, `docs/orchestration/at-cove-work-interface.md`, `docs/OVERVIEW.md`, `docs/usage/at-cove-config.md`.

- [ ] **Step 1: Fold the scheduler config into `at-cove-config.md`, delete the standalone doc**

Follow the **docs-author** skill. The `tracker`/`dispatch` sections in `at-cove-config.md` already own the scheduler wiring (from Plan 1) — move any operator guidance that lived only in `scheduler-config.md` (state-role mapping rationale, poll/concurrency/reaper guidance) into those sections, then:

```bash
git rm docs/orchestration/scheduler-config.md
```

Update inbound links (re-grep `grep -rn "scheduler-config" docs/ | grep -v superpowers`):
- `docs/orchestration/INDEX.md`: drop the `scheduler-config.md` row; adjust the two-doc intro; point operators configuring the scheduler at [`../usage/at-cove-config.md`](../usage/at-cove-config.md).
- `docs/orchestration/at-cove-work-interface.md`: the "operator config that keys into it, see scheduler-config.md" pointer → `at-cove-config.md`.
- `docs/OVERVIEW.md`: any `scheduler-config.md` reference / the command-surface line for `at-cove dispatch --config` → `at-cove dispatch [kit-dir]`.
- `docs/usage/at-cove-config.md`: note `at-cove dispatch [kit-dir]` reads this file (the scheduler now consumes the kit); bump `updated: 2026-07-10`.

- [ ] **Step 2: Verify + commit**

```
grep -rn "scheduler-config\|dispatch --config\|dispatch -\\-config" docs/ | grep -v docs/superpowers/     # expect: nothing
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/orchestration/
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage/
go build ./... && go test ./...
```
Expected: no stale `scheduler-config`/`dispatch --config` references; docs-audit 0 errors (pre-existing warnings ok); links resolve; build/tests green.
```bash
git add -A
git commit -m "docs: fold scheduler-config.md into at-cove-config.md; at-cove dispatch reads the kit"
```

---

## Final verification (Plan 2)

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; `gofmt -l cmd/ internal/` empty; `go.mod`/`go.sum` unchanged; integration builds compile.
- [ ] `internal/dispatch/config` is gone; `grep -rn "dispatch/config" cmd/ internal/` empty; scheduler/linear/`doDispatch` read `kit.Config`.
- [ ] `at-cove dispatch [kit-dir]` (no `--config`) loads the kit, resolves the tracker token from `tracker.linear.secrets`, and shells `at-cove work <kit-dir> …` per ready worker class; interactive/collaborator classes are skipped.
- [ ] Root/collaborator secrets reject the reserved subsystem names.
- [ ] No stale `scheduler-config.md` / `dispatch --config` references; docs-audit clean.

## Notes

- **Why one atomic Task 1:** the config *type* flows through `linear`, `scheduler`, and `doDispatch` together; swapping it in stages would leave the tree non-compiling. It's a mechanical, mapping-driven change (the table in Global Constraints), so a single reviewer gate fits.
- **Interactive classes** (`collaborators`) are recognized by the scheduler only as "not an autonomous worker → skip" this plan; actually dispatching them to a human is the deferred interactive-classes session.
