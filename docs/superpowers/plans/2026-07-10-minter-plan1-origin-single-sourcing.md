# Single-repo kit (`origin`) + repo single-sourcing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the *target repo* one home — the kit's new `origin` — and have `at-cove dispatch` fill it into the injected task, so the `at-dispatch` scheduler and its config stop naming a repo.

**Architecture:** Three green tasks, additive → fill → subtractive. (1) The kit gains `origin` (a github-only tagged union) + `main-branch` (default `main`), additively. (2) `at-cove dispatch` parses the injected task, **fills `repo` from the kit's `origin`/`main-branch`**, and re-injects it — so `at-work`'s `task.json` contract is unchanged but the source moves to the kit. (3) The scheduler's `RepoConfig` retires; it builds a repo-less task (only `work-branch`) and at-cove fills the rest.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**.

**Scope note:** Plan 1 of 2 for [AET-24](https://linear.app/aethons-tools/issue/AET-24), on branch `feat/minter-passthrough`. Design: [`2026-07-10-minter-run-param-passthrough-design.md`](../specs/2026-07-10-minter-run-param-passthrough-design.md) §2–4. Plan 2 adds the `COVE_RUN_*` passthrough + the minter.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies**.
- **`at-work`'s `task.json` contract is unchanged** — it still reads a complete `repo{host,name,source-branch,work-branch}`. at-cove is now the one that *fills* it (from `origin`), and the scheduler stops filling it.
- **The repo has exactly one source: the kit's `origin`.** After this plan nothing else names a repo (no `at-dispatch` `repo.slug`, no scheduler-stamped `task.repo.name`).
- **`origin` is optional in the schema, required for `dispatch`** — interactive-only kits (no `origin`) still parse; `at-cove dispatch` errors if the kit has none.
- **Every commit builds + `go test ./...` green.** After each task: `go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.
- Hermetic tests (`runner.Fake`); the `integration` e2e must still compile.

---

## Task 1: kit `origin` + `main-branch` (additive)

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `internal/kit/refkit_test.go`, `kits/reference-worker/config.yml`

**Interfaces:** Produces `kit.Origin{GitHub *GitHubOrigin}`, `kit.GitHubOrigin{Project string}`, `(*Origin).Active() (string, error)`, `Config.Origin *Origin`, `Config.MainBranch string` (defaulted to `main`).

- [ ] **Step 1: Failing tests**

In `internal/kit/config_test.go` add:
```go
func TestParseConfigOrigin(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\norigin:\n  github:\n    project: acme/myrepo\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Origin == nil || cfg.Origin.GitHub == nil || cfg.Origin.GitHub.Project != "acme/myrepo" {
		t.Fatalf("origin not parsed: %+v", cfg.Origin)
	}
	if cfg.MainBranch != "main" { // default
		t.Fatalf("main-branch default = %q; want main", cfg.MainBranch)
	}
}

func TestParseConfigRejectsBadOriginProject(t *testing.T) {
	if _, err := ParseConfig([]byte("name: k\norigin:\n  github:\n    project: nope\n")); err == nil {
		t.Fatal("origin.github.project must be owner/name")
	}
}

func TestParseConfigMainBranchOverride(t *testing.T) {
	cfg, _ := ParseConfig([]byte("name: k\nmain-branch: develop\n"))
	if cfg.MainBranch != "develop" {
		t.Fatalf("main-branch = %q; want develop", cfg.MainBranch)
	}
}
```
Run `go test ./internal/kit/ -run 'Origin|MainBranch'` → FAIL (undefined fields).

- [ ] **Step 2: Add the types + fields + validation**

In `internal/kit/config.go` (needs `errors` imported):
```go
// Origin names the code host + repo the kit targets — a tagged union (exactly one host;
// github only today). It is the single source of the repo identity and the host kind.
type Origin struct {
	GitHub *GitHubOrigin `yaml:"github,omitempty"`
}

type GitHubOrigin struct {
	Project string `yaml:"project"` // "owner/name"
}

// Active returns the set host, or an error if not exactly one.
func (o *Origin) Active() (string, error) {
	n, name := 0, ""
	if o.GitHub != nil {
		n, name = n+1, "github"
	}
	if n != 1 {
		return "", errors.New("must set exactly one host (github)")
	}
	return name, nil
}
```
Add to `Config`: `Origin *Origin \`yaml:"origin,omitempty"\`` and `MainBranch string \`yaml:"main-branch,omitempty"\``. In `ParseConfig`, after the existing checks:
```go
if cfg.Origin != nil {
	if _, err := cfg.Origin.Active(); err != nil {
		return Config{}, fmt.Errorf("config.yml: origin: %w", err)
	}
	if gh := cfg.Origin.GitHub; gh != nil {
		if parts := strings.Split(gh.Project, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return Config{}, fmt.Errorf("config.yml: origin.github.project must be \"owner/name\", got %q", gh.Project)
		}
	}
}
if cfg.MainBranch == "" {
	cfg.MainBranch = "main"
}
```

- [ ] **Step 3: Reference kit + its test**

In `kits/reference-worker/config.yml` add (near the top, after `name:`):
```yaml
origin:
  github:
    project: your-org/your-repo   # ← set to the target repo
main-branch: main
```
In `internal/kit/refkit_test.go` add an assertion that `cfg.Origin.GitHub.Project` is non-empty.

- [ ] **Step 4: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean.
```bash
git commit -am "feat(kit): add origin (github union) + main-branch"
```

---

## Task 2: `at-cove dispatch` fills the repo from `origin`

**Files:** `internal/dispatchrun/dispatchrun.go`, `internal/dispatchrun/dispatchrun_test.go`, `internal/dispatchrun/e2e_integration_test.go`, `kits/reference-worker/testdata/task.json`

**Interfaces:** Consumes `kit.Config.Origin`/`MainBranch` and `worker.Task`/`worker.TaskRepo`. `Dispatch` now parses the injected task, fills `repo`, and re-injects it.

- [ ] **Step 1: Rewrite the dispatchrun tests (fail first)**

In `internal/dispatchrun/dispatchrun_test.go`, give each `Dispatch` test's `Cfg` an `Origin` and assert the injected task carries the filled repo. The fake records `writeVM` calls (stdin = the injected task bytes). Update `TestDispatchRunsBracket`:
```go
	Cfg: kit.Config{
		Name:       "w",
		Origin:     &kit.Origin{GitHub: &kit.GitHubOrigin{Project: "acme/myrepo"}},
		MainBranch: "main",
		Workers:    map[string]kit.Worker{"implement": {Prompt: "do it"}},
	},
```
and after the run, assert the task injected at `taskVMPath` has the repo filled:
```go
	var injected string
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cat > "+taskVMPath) {
			injected = c.Stdin
		}
	}
	if !strings.Contains(injected, `"name": "acme/myrepo"`) ||
		!strings.Contains(injected, `"source-branch": "main"`) {
		t.Fatalf("injected task missing filled repo:\n%s", injected)
	}
```
Add a `TestDispatchSourceBranchOverride` (input task with `repo.source-branch: "release"` → injected keeps `release`, not `main`) and a `TestDispatchRequiresOrigin` (Cfg with no `Origin` → `Dispatch` errors). Give `Origin` to the air-gap test's `Cfg` too. `TestDispatchUndeclaredClassErrors` needs no origin (it errors on the class before the origin check). Run → FAIL.

- [ ] **Step 2: Fill the repo in `dispatchrun.go`**

Replace `taskClass` + the class lookup + the raw-`input` injection. In `Dispatch`, after reading `input`:
```go
	var task worker.Task
	if err := json.Unmarshal(input, &task); err != nil {
		return fmt.Errorf("parse task: %w", err)
	}
	if task.Worker.Class == "" {
		return fmt.Errorf("task declares no worker.class")
	}
	w, ok := o.Cfg.Workers[task.Worker.Class]
	if !ok {
		return fmt.Errorf("kit %q declares no worker class %q", o.Cfg.Name, task.Worker.Class)
	}
	// Fill the repo from the kit's origin — the single source of truth.
	if o.Cfg.Origin == nil || o.Cfg.Origin.GitHub == nil {
		return fmt.Errorf("kit %q declares no origin (required for dispatch)", o.Cfg.Name)
	}
	task.Repo.Name = o.Cfg.Origin.GitHub.Project
	task.Repo.Host = "https://github.com"
	if task.Repo.SourceBranch == "" {
		task.Repo.SourceBranch = o.Cfg.MainBranch // defaulted to "main" at parse
	}
	filled, err := json.MarshalIndent(&task, "", "  ")
	if err != nil {
		return err
	}
```
Then inject `filled` instead of `input`: `writeVM(o.R, tgt, filled, taskVMPath)`. Keep using `w.Prompt` for the agent step. Delete the now-unused `taskClass`. Fix imports: add `encoding/json` and `github.com/aethons-tools/cove/internal/dispatch/worker`; remove `gopkg.in/yaml.v3` if `taskClass` was its only user (grep the file).

- [ ] **Step 3: Update the e2e + testdata (maintainer-run)**

The repo now comes from the kit's `origin`, not the task — so `at-cove dispatch` ignores the task's `repo`. In `kits/reference-worker/testdata/task.json`, drop the `repo` block (leave `issue`/`worker`/`task`); it's a template. In `internal/dispatchrun/e2e_integration_test.go`, drop `repo` from the inline task JSON and add a comment that the target repo comes from the reference kit's `origin` (the maintainer sets `origin.github.project` to their scratch repo before `just e2e`). Keep the `status.ok.pr-url` assertion. Confirm it compiles: `go build -tags integration ./internal/dispatchrun/`.

- [ ] **Step 4: Run + commit**

`go test ./internal/dispatchrun/ ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean.
```bash
git commit -am "feat(dispatch): fill task.repo from the kit's origin (single-sourced)"
```

---

## Task 3: retire the scheduler's repo

**Files:** `internal/dispatch/config/config.go`, `internal/dispatch/config/config_test.go`, `internal/dispatch/scheduler/engine.go`, `internal/dispatch/scheduler/engine_test.go`, `internal/dispatch/scheduler/brief.go`, `internal/dispatch/scheduler/brief_test.go`, `cmd/at-dispatch/main.go`, `cmd/at-dispatch/main_test.go`

- [ ] **Step 1: Failing test — scheduler builds a repo-less task**

In `internal/dispatch/scheduler/engine_test.go`: remove `Repo: config.RepoConfig{...}` from `testConfig()`, and in `TestHandleOKOpensReviewAndBuildsInput` change the `GotInput` assertions to expect the task **without** repo name/source-branch — keep `"work-branch": "implement/AET-9"`, `"key": "AET-9"`, `"class": "implement"`; drop the `"source-branch": "main"` assertion. Run `go test ./internal/dispatch/scheduler/` → FAIL to compile (`config.RepoConfig` still referenced in engine.go).

- [ ] **Step 2: Remove `RepoConfig` from the scheduler config**

`internal/dispatch/config/config.go`: delete the `RepoConfig` type, the `Repo RepoConfig` field, and its validation (the `repo.slug` owner/name check + the `source-branch` non-empty check). Grep the file for any other `Repo` use.

- [ ] **Step 3: Repo-less task + brief in the engine**

`internal/dispatch/scheduler/engine.go`:
- Line ~60: `brief := assembleBrief(iss, e.cfg.Repo.Slug, comments)` → `brief := assembleBrief(iss, comments)`.
- Lines ~73–76: `Repo: worker.TaskRepo{Name: e.cfg.Repo.Slug, SourceBranch: e.cfg.Repo.SourceBranch, WorkBranch: iss.Class + "/" + iss.Identifier}` → `Repo: worker.TaskRepo{WorkBranch: iss.Class + "/" + iss.Identifier}`.

`internal/dispatch/scheduler/brief.go`: `func assembleBrief(iss Issue, repo string, comments []Comment) string` → `func assembleBrief(iss Issue, comments []Comment) string`; drop the `"  **Repo:** " + repo` fragment from the header line.

`internal/dispatch/scheduler/brief_test.go`: update both `assembleBrief(...)` calls to drop the repo arg; delete the assertion that expects the repo slug in the output.

- [ ] **Step 4: `cmd/at-dispatch`**

`cmd/at-dispatch/main.go` line ~66: the config-OK log printed `cfg.Repo.Slug` — drop that field (e.g. `"at-dispatch: config OK — %d class(es): %s\n", len(classes), …`). `cmd/at-dispatch/main_test.go`: `goodConfig` drop the `repo:` block; `TestServeRejectsBadConfig` used `repo:\n  slug: not-a-slug` as the invalid config — swap it for a still-invalid config that survives the removal (e.g. an empty config missing `tracker`, or a class with `mode: autonomous` and no `kit` — pick one the validator still rejects, and confirm it does).

- [ ] **Step 5: Scheduler-config fixtures**

`internal/dispatch/config/config_test.go`: `validYAML` — drop the `repo:` block; delete the `cfg.Repo.Slug` assertion (~L46) and the `cfg.Repo.SourceBranch` assertion (~L146); delete the "bad repo slug" (~L85) and "missing source-branch" (~L102) validation cases (there is no repo to validate). `TestLoadConfigResolvesKitAndDefaults` — drop the repo lines from its embedded YAML.

- [ ] **Step 6: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.
Confirm: `grep -rn "RepoConfig\|cfg.Repo\|Repo.Slug\|Repo.SourceBranch\|repo:\|repo.slug" cmd/at-dispatch/ internal/dispatch/ --include=*.go --include=*.yml` — nothing (the `task.repo`/`worker.TaskRepo` at-work contract is separate and stays).
```bash
git commit -am "refactor(at-dispatch): retire RepoConfig; repo comes from the kit origin"
```

---

## Final verification

- [ ] `go test ./...` passes; `go build ./...`, `go vet ./...` clean; `gofmt -l cmd/ internal/` empty; `go.mod` unchanged; `just build` → three binaries.
- [ ] `go build -tags integration ./internal/dispatchrun/` — e2e compiles.
- [ ] The kit config surface is `{name, origin?, main-branch?, secrets, image, workers}`; `origin` is optional but **required for dispatch** (`TestDispatchRequiresOrigin`).
- [ ] Repo single-sourced: `grep -rn "Repo.Slug\|RepoConfig\|cfg.Repo\b" cmd/ internal/ --include=*.go` returns nothing; `at-cove dispatch` fills `task.repo` from `origin`; the scheduler names no repo.
- [ ] at-work unchanged: `internal/dispatch/worker/` (prepare/complete + their tests) untouched; `implementTask()` still builds a complete `TaskRepo`.

## Notes

- **Reconciliations** (re-grep; lines drift): the exact `Dispatch` body + whether `yaml` stays imported in `dispatchrun.go` after `taskClass` is removed; the exact invalid-config to substitute in `TestServeRejectsBadConfig`; any other `assembleBrief` caller.
- **Why at-cove fills the task (not the scheduler):** the repo lives in the kit; at-cove is the one component that holds the kit *and* injects the task, so it is the natural filler. This keeps `at-work`'s file contract stable and the scheduler repo-agnostic.
- **Plan 2 builds on this:** `dispatchrun` now fully parses `worker.Task`, so deriving `COVE_RUN_REPO`/`ISSUE`/`CLASS` for the minter is a small addition on top.
