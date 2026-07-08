# Scheduler + Config Rewire Implementation Plan (Plan B of AET-21)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewire the at-dispatch scheduler + config to dispatch each ready issue through `at-cove dispatch` speaking at-work's `input.json`/`output.json` directly — removing the `DISPATCH_*` env / `config.Result` indirection.

**Architecture:** A class maps to a **kit** (not a command). `handle` builds a `worker.Input`, writes `input.json`, runs `at-cove dispatch <kit> --in --out --timeout` via the existing `Executor`, reads `worker.Output`, and brokers `OK`/`NEEDS_INPUT`/`ERROR` to the tracker. The rewire is sequenced so **every commit compiles and tests green**: add the new config fields non-breaking → rewire the scheduler → delete the superseded code.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**. Hermetic tests drive a fake `Executor` and `Tracker`.

**Scope note:** This is **Plan B** of AET-21, building on Plan A (`at-cove dispatch`, merged). Plan C (reference worker kit + `run-worker.sh` + the end-to-end `integration` run) follows.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new third-party dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- The scheduler writes at-work's `input.json` and reads its `output.json` **directly** — it **imports `internal/dispatch/worker`** for `Input`/`Output` (the accepted coupling).
- A class maps to a **kit**; a **relative `kit` resolves against the config file's directory** (absolute used as-is) — resolved in `LoadConfig`.
- **Two timeouts:** `cl.Timeout` bounds the agent work (passed as dispatch `--timeout`); the `Executor` **process** ctx is bounded by `cl.Timeout + dispatch-overhead` (default `15m`).
- **Delete** the superseded `env.go` (`Task`/`BuildEnv`/`ResolveSecrets`/`DISPATCH_*`/`reservedEnvNames`) and `result.go` (`Result`/`Artifacts`/`NeedsInput`/`Usage`/`ReadResult`/`Status*`); drop the top-level `secrets:` field. `tracker.token`/`webhook-secret` stay.
- `scheduler.New` **drops its `resolve` param** (it fed only `ResolveSecrets(cfg.Secrets)`); the tracker token is resolved in `cmd/at-dispatch`.
- The `Executor` interface is unchanged (`Run(ctx, argv, env)`); `handle` passes `nil` env — `at-cove dispatch` inherits the process env, and the kit owns worker secrets.
- The scheduler dispatches **autonomous** classes only; interactive stays a validated no-kit config concept.
- **TDD, hermetic** — the fake `Executor` simulates `at-cove dispatch` (reads the `--in` file, writes a chosen `--out` file). Every commit: `go test ./...` green.
- Spec: [`docs/superpowers/specs/2026-07-08-at-cove-dispatch-design.md`](../specs/2026-07-08-at-cove-dispatch-design.md) §5.

---

## File Structure

- `internal/dispatch/config/config.go` — `Class{Kit}`, `RepoConfig{SourceBranch}`, `Config{DispatchOverhead}`, `LoadConfig` kit-path resolution, `applyDefaults`, `Validate`.
- `internal/dispatch/config/env.go`, `result.go` — **deleted** in T3.
- `internal/dispatch/scheduler/engine.go` — `handle`, `broker`, comment builders, `readOutput`/`errorOutput`, `New`.
- `internal/dispatch/scheduler/fakes_test.go` — the fake `Executor` simulates dispatch.
- `cmd/at-dispatch/main.go` — the `scheduler.New` call (drop `resolve` arg) in T3.
- `docs/orchestration/` — the class/config schema doc.

---

## Task 1: config — new fields (non-breaking) + kit-path resolution

Adds `kit`, `source-branch`, `dispatch-overhead` **without removing** `command` yet, so the scheduler still compiles. Kit paths resolve in `LoadConfig`.

**Files:**
- Modify: `internal/dispatch/config/config.go`
- Test: `internal/dispatch/config/config_test.go`

**Interfaces:**
- Produces: `Config.DispatchOverhead string`; `RepoConfig.SourceBranch string`; `Class.Kit string`; `LoadConfig` resolves relative `Class.Kit` to absolute.

- [ ] **Step 1: Write the failing test**

Add to `internal/dispatch/config/config_test.go`:

```go
func TestLoadConfigResolvesKitAndDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "at-dispatch.yml")
	yaml := `
tracker:
  provider: linear
  team: T
  token: {command: ["echo","t"]}
  webhook-secret: {command: ["echo","w"]}
  poll-interval: 30s
  states: {ready: R, in-progress: IP, in-review: IR, done: D, needs-input: NI, blocked: B}
repo:
  slug: owner/name
  source-branch: main
concurrency: 2
reaper-timeout: 1h
classes:
  implement:
    mode: autonomous
    kit: ./kits/worker
    command: ["placeholder"]
    timeout: 30m
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Repo.SourceBranch != "main" {
		t.Errorf("SourceBranch = %q", cfg.Repo.SourceBranch)
	}
	if cfg.DispatchOverhead != "15m" {
		t.Errorf("DispatchOverhead default = %q; want 15m", cfg.DispatchOverhead)
	}
	want := filepath.Join(dir, "kits/worker")
	if got := cfg.Classes["implement"].Kit; got != want {
		t.Errorf("Kit = %q; want absolute %q", got, want)
	}
}
```

(The `command: ["placeholder"]` line keeps the config valid under the *current* autonomous validation, which still requires a command until Task 3.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/config/ -run TestLoadConfigResolvesKitAndDefaults`
Expected: FAIL — `SourceBranch`/`DispatchOverhead`/`Kit` fields undefined (build error).

- [ ] **Step 3: Add the fields + defaults + kit resolution**

In `internal/dispatch/config/config.go`:

Add to `Config`:
```go
	Concurrency      int    `yaml:"concurrency"`
	ReaperTimeout    string `yaml:"reaper-timeout"`
	DispatchOverhead string `yaml:"dispatch-overhead"` // build+boot+teardown margin added to a class's work timeout
```

Add to `RepoConfig`:
```go
type RepoConfig struct {
	Slug         string `yaml:"slug"`
	SourceBranch string `yaml:"source-branch"` // base branch work is built on
}
```

Add `Kit` to `Class` (keep `Command` for now):
```go
type Class struct {
	Mode        string   `yaml:"mode"`    // "autonomous" | "interactive"
	Kit         string   `yaml:"kit"`     // path to the class's .at-cove kit (autonomous); relative resolves against the config dir
	Command     []string `yaml:"command"` // DEPRECATED (removed in the rewire); kept transiently so the scheduler still builds
	Timeout     string   `yaml:"timeout"` // Go duration; autonomous
	Concurrency int      `yaml:"concurrency"`
}
```

In `applyDefaults`, add:
```go
	if c.DispatchOverhead == "" {
		c.DispatchOverhead = "15m"
	}
```

Resolve kit paths in `LoadConfig` (it has the file path); replace the body:
```go
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return Config{}, err
	}
	// Resolve each class's kit relative to the config file's directory.
	base := filepath.Dir(path)
	for name, cl := range cfg.Classes {
		if cl.Kit != "" && !filepath.IsAbs(cl.Kit) {
			cl.Kit = filepath.Join(base, cl.Kit)
			cfg.Classes[name] = cl
		}
	}
	return cfg, nil
}
```

Add `"path/filepath"` to the imports if not present.

In `Validate`, add (after the `repo.slug` check) the `source-branch` + `dispatch-overhead` checks:
```go
	if strings.TrimSpace(c.Repo.SourceBranch) == "" {
		return fmt.Errorf("config: repo.source-branch is required")
	}
	if err := checkDuration("dispatch-overhead", c.DispatchOverhead); err != nil {
		return err
	}
```

(Leave the class autonomous/interactive branches and the `Secrets` loop unchanged in this task.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/config/`
Expected: PASS. Fix any existing config-test fixtures that now need `repo.source-branch` (add `source-branch: main` to their repo block) — the required-field addition will surface them.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/config/
git commit -m "feat(config): add kit/source-branch/dispatch-overhead (non-breaking)"
```

---

## Task 2: scheduler — rewire `handle` + `broker` to `at-cove dispatch`

Rewrites `handle` to build `worker.Input`, run `at-cove dispatch`, and read `worker.Output`; rewrites `broker` + comment builders for `worker.Output`. The config's `Command`/`ResolveSecrets`/`ReadResult`/`Result` still exist (deleted in T3) but are no longer used here. `scheduler.New`'s `resolve` param stays (unused) until T3.

**Files:**
- Modify: `internal/dispatch/scheduler/engine.go`
- Modify: `internal/dispatch/scheduler/fakes_test.go`, `internal/dispatch/scheduler/engine_test.go`

**Interfaces:**
- Consumes: `worker.Input`/`IssueInput`/`RepoInput`/`Output`/`Outcome`/`NeedsInput`/`Work`, `worker.StatusOK`/`StatusNeedsInput`/`StatusError` (from `internal/dispatch/worker/types.go`, committed); `config.Class.Kit`, `config.RepoConfig.SourceBranch`, `config.Config.DispatchOverhead`.
- Produces: rewired `handle`/`broker`; `okComment`/`needsInputComment`/`errorComment` now take `worker.Output`; `readOutput(path) worker.Output`; `errorOutput(err) worker.Output`.

- [ ] **Step 1: Rewrite the fake Executor to simulate dispatch**

In `internal/dispatch/scheduler/fakes_test.go`, make the fake `Executor` (read the existing one first and adapt it) parse `--in`/`--out` from argv and write a canned output. Target shape:

```go
// fakeExecutor simulates `at-cove dispatch`: it reads the --in input.json and
// writes OutJSON to the --out path. RunErr (if set) is returned after writing.
type fakeExecutor struct {
	OutJSON  string // what to write to the --out path ("" => write nothing)
	RunErr   error
	GotInput string // captured contents of the --in file
	GotArgv  []string
}

func (f *fakeExecutor) Run(_ context.Context, argv []string, _ []string) error {
	f.GotArgv = argv
	var inPath, outPath string
	for i := 0; i < len(argv)-1; i++ {
		switch argv[i] {
		case "--in":
			inPath = argv[i+1]
		case "--out":
			outPath = argv[i+1]
		}
	}
	if b, err := os.ReadFile(inPath); err == nil {
		f.GotInput = string(b)
	}
	if f.OutJSON != "" && outPath != "" {
		_ = os.WriteFile(outPath, []byte(f.OutJSON), 0o600)
	}
	return f.RunErr
}
```

Reconcile with the existing fake's name/fields used by `engine_test.go` (it may currently be a struct with a recorded argv/env + a returned error — replace its `Run` with the simulating version and keep the field the tests reference, renaming test call sites as needed).

- [ ] **Step 2: Write the failing tests**

In `internal/dispatch/scheduler/engine_test.go`, replace the broker/handle tests with ones driving the new path. Core cases (adapt to the file's existing test harness — how it constructs an `Engine` + fake `Tracker`):

```go
func TestHandleOKOpensReviewAndBuildsInput(t *testing.T) {
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":"OK","work":{"pr-url":"https://x/pull/1","branch":"implement/AET-9"},"agent":{"status":"OK","pr-message":"did the thing"}}`}
	eng := newTestEngine(t, tr, ex) // builds config with an autonomous "implement" class (kit set, timeout 30m), repo source-branch main, dispatch-overhead 15m
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "Add X", Class: "implement"})

	// input.json the scheduler built
	if !strings.Contains(ex.GotInput, `"work-branch": "implement/AET-9"`) ||
		!strings.Contains(ex.GotInput, `"source-branch": "main"`) ||
		!strings.Contains(ex.GotInput, `"key": "AET-9"`) {
		t.Fatalf("input.json wrong:\n%s", ex.GotInput)
	}
	// argv carried the kit + --timeout
	joined := strings.Join(ex.GotArgv, " ")
	if !strings.Contains(joined, "at-cove dispatch") || !strings.Contains(joined, "--timeout 30m") {
		t.Fatalf("argv wrong: %v", ex.GotArgv)
	}
	// brokered: IN REVIEW + a comment with the PR
	if tr.lastRole != RoleInReview {
		t.Errorf("role = %v; want IN REVIEW", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "https://x/pull/1") {
		t.Errorf("comment missing PR: %q", tr.lastComment)
	}
}

func TestHandleNeedsInput(t *testing.T) {
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: `{"status":"NEEDS_INPUT","work":{"safe-state":"implement/AET-9 @ abc"},"agent":{"status":"NEEDS_INPUT","needs-input":{"doing":"d","blocker":"b","need":"n","tried":"tr"}}}`}
	eng := newTestEngine(t, tr, ex)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})
	if tr.lastRole != RoleNeedsInput {
		t.Errorf("role = %v; want NEEDS INPUT", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "**Blocker:** b") || !strings.Contains(tr.lastComment, "implement/AET-9 @ abc") {
		t.Errorf("needs-input comment wrong: %q", tr.lastComment)
	}
}

func TestHandleMissingOutputIsError(t *testing.T) {
	tr := newFakeTracker()
	ex := &fakeExecutor{OutJSON: "", RunErr: errors.New("boom")} // writes no output.json
	eng := newTestEngine(t, tr, ex)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})
	if tr.lastRole != RoleNeedsInput {
		t.Errorf("role = %v; want NEEDS INPUT (error path)", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "⚠️") {
		t.Errorf("expected error comment, got %q", tr.lastComment)
	}
}
```

If the file has no `newTestEngine`/`newFakeTracker` helpers, add them (a fake `Tracker` recording `lastRole`/`lastComment`, and an `Engine` built via `New(...)` with a small in-code `config.Config`). Read the existing `engine_test.go`/`fakes_test.go` and reuse whatever harness is already there.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/dispatch/scheduler/`
Expected: FAIL to build / assertions fail — `handle`/`broker` still use `config.Result`.

- [ ] **Step 4: Rewrite `handle`, `broker`, comment builders, add `readOutput`/`errorOutput`**

In `internal/dispatch/scheduler/engine.go`, add imports `"encoding/json"` and `"github.com/aethons-tools/cove/internal/dispatch/worker"`. Replace `handle` (lines ~48-90):

```go
// handle runs one issue synchronously: claim → brief → at-cove dispatch → broker.
func (e *Engine) handle(ctx context.Context, iss Issue) {
	if err := e.tracker.Transition(ctx, iss.ID, RoleInProgress); err != nil {
		e.log.Printf("claim %s: %v", iss.Identifier, err)
		return
	}
	cl := e.cfg.Classes[iss.Class]

	comments, err := e.tracker.Comments(ctx, iss.ID)
	if err != nil {
		e.log.Printf("comments %s: %v (continuing with none)", iss.Identifier, err)
	}
	brief := assembleBrief(iss, e.cfg.Repo.Slug, comments)

	dir, err := os.MkdirTemp("", "at-dispatch-")
	if err != nil {
		e.broker(ctx, iss, errorOutput(fmt.Errorf("tempdir: %w", err)), nil)
		return
	}
	defer os.RemoveAll(dir)
	inPath := filepath.Join(dir, "input.json")
	outPath := filepath.Join(dir, "output.json")

	in := worker.Input{
		Issue: worker.IssueInput{Key: iss.Identifier, Title: iss.Title, WorkClass: iss.Class, Brief: brief},
		Repo: worker.RepoInput{
			Name: e.cfg.Repo.Slug, SourceBranch: e.cfg.Repo.SourceBranch,
			WorkBranch: iss.Class + "/" + iss.Identifier,
		},
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		e.broker(ctx, iss, errorOutput(fmt.Errorf("marshal input: %w", err)), nil)
		return
	}
	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		e.broker(ctx, iss, errorOutput(fmt.Errorf("write input: %w", err)), nil)
		return
	}

	work, _ := time.ParseDuration(cl.Timeout)            // validated by config
	over, _ := time.ParseDuration(e.cfg.DispatchOverhead) // validated by config
	rctx, cancel := context.WithTimeout(ctx, work+over)
	defer cancel()
	argv := []string{"at-cove", "dispatch", cl.Kit, "--in", inPath, "--out", outPath, "--timeout", cl.Timeout}
	runErr := e.exec.Run(rctx, argv, nil)

	e.broker(ctx, iss, readOutput(outPath), runErr)
}
```

Replace `broker` + comment builders + add helpers:

```go
// broker performs the tracker writes for one dispatch outcome. Single writer.
func (e *Engine) broker(ctx context.Context, iss Issue, out worker.Output, runErr error) {
	switch {
	case runErr == nil && out.Status == worker.StatusOK:
		e.post(ctx, iss, okComment(out))
		e.transition(ctx, iss, RoleInReview)
	case out.Status == worker.StatusNeedsInput:
		e.post(ctx, iss, needsInputComment(out))
		e.transition(ctx, iss, RoleNeedsInput)
	default:
		e.post(ctx, iss, errorComment(out, runErr))
		e.transition(ctx, iss, RoleNeedsInput)
	}
}

func okComment(out worker.Output) string {
	var b strings.Builder
	b.WriteString("✅ Done.\n\n")
	if out.Work.PRURL != "" {
		b.WriteString("PR: " + out.Work.PRURL + "\n")
	}
	if out.Work.Branch != "" {
		b.WriteString("Branch: " + out.Work.Branch + "\n")
	}
	if out.Agent != nil && out.Agent.PRMessage != "" {
		b.WriteString("\n" + out.Agent.PRMessage + "\n")
	}
	return b.String()
}

func needsInputComment(out worker.Output) string {
	b := "❓ NEEDS INPUT\n\n"
	if out.Agent != nil && out.Agent.NeedsInput != nil {
		n := out.Agent.NeedsInput
		b += "**Doing:** " + n.Doing + "\n" +
			"**Blocker:** " + n.Blocker + "\n" +
			"**Need:** " + n.Need + "\n" +
			"**Tried:** " + n.Tried + "\n"
	}
	if out.Work.SafeState != "" {
		b += "**Safe state:** " + out.Work.SafeState + "\n"
	}
	return b
}

func errorComment(out worker.Output, runErr error) string {
	msg := out.Message
	if msg == "" && out.Work.Error != "" {
		msg = out.Work.Error
	}
	if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	if msg == "" {
		msg = "dispatch failed"
	}
	return "⚠️ ERROR\n\n" + msg + "\n"
}

// readOutput reads a worker.Output from path, synthesizing an ERROR output when
// the file is missing, unreadable, invalid, or has no status.
func readOutput(path string) worker.Output {
	data, err := os.ReadFile(path)
	if err != nil {
		return errorOutput(fmt.Errorf("no dispatch output: %w", err))
	}
	var out worker.Output
	if err := json.Unmarshal(data, &out); err != nil {
		return errorOutput(fmt.Errorf("invalid dispatch output: %w", err))
	}
	if out.Status == "" {
		return errorOutput(fmt.Errorf("dispatch output has no status"))
	}
	return out
}

func errorOutput(err error) worker.Output {
	return worker.Output{Status: worker.StatusError, Message: err.Error()}
}
```

Remove the now-unused `resolve`/`secrets`/`BuildEnv`/`Task` code from `handle` (done by the replacement above). Leave `scheduler.New`'s signature untouched in this task (the `resolve` param/field is now unused but still compiles).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/scheduler/`
Expected: PASS. Then `go build ./... && go vet ./... && gofmt -l internal/ cmd/`.
Expected: builds; clean. (`config.Result`/`env.go` still exist but are now unused by the scheduler — that's fine until Task 3.)

- [ ] **Step 6: Commit**

```bash
git add internal/dispatch/scheduler/
git commit -m "feat(scheduler): dispatch via at-cove dispatch (worker Input/Output)"
```

---

## Task 3: delete superseded config code + finalize signatures

Removes the now-dead code and tightens validation. Nothing references it after Task 2.

**Files:**
- Delete: `internal/dispatch/config/env.go`, `internal/dispatch/config/result.go`
- Modify: `internal/dispatch/config/config.go` (remove `Command`, `Secrets`, `reservedEnvNames`; `Validate`), `internal/dispatch/scheduler/engine.go` (`New` drops `resolve`), `cmd/at-dispatch/main.go` (New call)
- Test: `internal/dispatch/config/config_test.go`, `internal/dispatch/config/env_test.go`, `internal/dispatch/config/result_test.go` (delete the last two if present), `internal/dispatch/scheduler/*_test.go`

**Interfaces:**
- Produces: `scheduler.New(cfg config.Config, t Tracker, e Executor, logger *log.Logger) *Engine` (no `resolve`).

- [ ] **Step 1: Delete the dead files + update tests to expect removal**

```bash
git rm internal/dispatch/config/env.go internal/dispatch/config/result.go
git rm -f internal/dispatch/config/env_test.go internal/dispatch/config/result_test.go 2>/dev/null || true
```
(If `env_test.go`/`result_test.go` don't exist, skip — the `2>/dev/null` guards it.)

- [ ] **Step 2: Update config — remove `Command`/`Secrets`/`reservedEnvNames`; tighten `Validate`**

In `internal/dispatch/config/config.go`:
- Remove the `Command []string` field from `Class`.
- Remove the top-level `Secrets []Secret` field from `Config` and the `Secret` type if unused elsewhere (grep first: `grep -rn "config.Secret\b" .`).
- Remove the `reservedEnvNames` var and the `Env*` consts if still declared here (they moved to `env.go`, now deleted — remove any remaining references).
- Replace the class-validation switch and delete the `Secrets` loop:

```go
	for name, cl := range c.Classes {
		if name == "" {
			return fmt.Errorf("config: a class name must not be empty")
		}
		switch cl.Mode {
		case "autonomous":
			if strings.TrimSpace(cl.Kit) == "" {
				return fmt.Errorf("config: classes[%q]: autonomous class requires a kit", name)
			}
			if err := checkDuration(fmt.Sprintf("classes[%q].timeout", name), cl.Timeout); err != nil {
				return err
			}
		case "interactive":
			if strings.TrimSpace(cl.Kit) != "" {
				return fmt.Errorf("config: classes[%q]: interactive class must not set a kit", name)
			}
		default:
			return fmt.Errorf("config: classes[%q].mode must be \"autonomous\" or \"interactive\", got %q", name, cl.Mode)
		}
		if cl.Concurrency < 0 {
			return fmt.Errorf("config: classes[%q].concurrency must be >= 0", name)
		}
	}
```

Delete the `seen`/`c.Secrets` validation loop entirely.

- [ ] **Step 3: `scheduler.New` drops `resolve`; `cmd/at-dispatch` updates the call**

In `internal/dispatch/scheduler/engine.go`, change `New` to drop the `resolve func(...)` param and remove the `resolve` field from the `Engine` struct (grep the struct + `New` body; it is now unused). Signature becomes:
```go
func New(cfg config.Config, t Tracker, e Executor, logger *log.Logger) *Engine {
```
Remove the `resolve: resolve,` assignment from the returned `&Engine{...}` and the `resolve` field from the struct definition.

In `cmd/at-dispatch/main.go`, update the call (keep the local `resolve` — it still resolves the tracker token):
```go
	engine := scheduler.New(cfg, tracker, dexec.New(), logger)
```

- [ ] **Step 4: Update config tests + run**

Update `internal/dispatch/config/config_test.go`: the `TestLoadConfigResolvesKitAndDefaults` fixture should now **drop** the `command: ["placeholder"]` line (autonomous now validates on `kit`). Add/keep a `Validate` test asserting: autonomous-without-kit → error; interactive-with-kit → error; missing `source-branch` → error. Remove any test asserting the deleted `Secrets`/`Result`/`BuildEnv` behavior.

Run: `go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/`
Expected: all pass; gofmt clean; `go.mod` unchanged.

- [ ] **Step 5: Commit**

```bash
git add -A internal/dispatch/config/ internal/dispatch/scheduler/ cmd/at-dispatch/
git commit -m "refactor(dispatch): remove DISPATCH_*/Result indirection; drop resolve param"
```

---

## Task 4: docs

**Files:**
- Modify: the at-dispatch config/orchestration doc (find it: `grep -rln "DISPATCH_\|result.json\|classes" docs/`)

- [ ] **Step 1: Update the config schema doc**

In the owning doc (likely under `docs/orchestration/`), update the class/config schema: a class maps to a **`kit`** (not `command`); add `repo.source-branch` and top-level `dispatch-overhead` (default 15m); state that the scheduler writes at-work's `input.json` and reads `output.json` directly (no more `DISPATCH_*` env or `result.json`). Remove references to the removed `DISPATCH_*` contract and per-command `secrets:`. Keep the doc terse and progressively-disclosed; update `docs/orchestration/INDEX.md` if a row's description changed.

- [ ] **Step 2: Verify**

Run: `grep -rn "DISPATCH_" docs/ | grep -v superpowers` — expected: no stale references in the orchestration docs (design/spec history under `superpowers/` may keep them).
Run: `go test ./... && gofmt -l cmd/ internal/`
Expected: pass; clean.

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "docs: scheduler dispatches via at-cove dispatch (kit + input/output.json)"
```

---

## Final verification

- [ ] `go test ./...` — all pass.
- [ ] `just build` — all binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/` prints nothing.
- [ ] `grep -rn "DISPATCH_\|config.Result\|ResolveSecrets\|BuildEnv" internal/ cmd/` — no live references remain (superseded code fully removed).

## Notes

- **Two implementer reconciliations** are flagged inline, both read-and-match against existing code: (1) the scheduler's existing fake `Executor`/`Tracker` harness in `fakes_test.go`/`engine_test.go` — adapt the provided simulating fake to the real field/helper names; (2) whether `env_test.go`/`result_test.go` exist to delete.
- **Green at every commit:** T1 adds fields (keeps `command`), T2 rewires the scheduler (dead config code still present but unused), T3 deletes the dead code + drops `resolve`. Do not reorder — deleting `env.go`/`result.go` before T2 rewires the scheduler would break the build.
- The `worker` package is unchanged — the scheduler marshals `worker.Input` directly and `readOutput` unmarshals `worker.Output`; no `WriteInput`/`ReadOutput` helpers are added there.
