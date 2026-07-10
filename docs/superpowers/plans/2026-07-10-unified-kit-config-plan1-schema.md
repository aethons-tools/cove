# Unified kit config — Plan 1: schema + secret buckets

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reshape `kit.Config` into the unified surface — `origin`→`source-control` (with `main-branch` + code-host secret nested), a `tracker` union, a `dispatch` block, `workers`/`collaborators` class trees with a `<common>` base — and relocate the code-host token so the credential air-gap is *structural* (by schema location, not name-matching). Runtime behavior of `work`/`dispatch`/`connect` is unchanged; the scheduler still reads its `--config` (Plan 2 rewires that).

**Architecture:** All change lands in `internal/kit` (the schema + validation + `<common>` resolvers), with mechanical reader updates in `internal/dispatchrun` and `cmd/at-cove`, and the reference kit + `at-cove-config.md` migrated in step. The one behavioral change is where the git token is declared (`source-control.github.secrets`) and how `dispatchrun` receives it (a distinct spec, not fished out of the flat secrets list by name).

**Tech Stack:** Go 1.22 — **no new dependencies** (`gopkg.in/yaml.v3` only). TDD; every commit builds + `go test ./...` green; `gofmt` clean.

**Design:** [`docs/superpowers/specs/2026-07-10-unified-kit-config-design.md`](../specs/2026-07-10-unified-kit-config-design.md) — §3 schema, §4 `<common>` rule, §5 secret buckets/air-gap, §6 validation.

## Global Constraints

- **No new `go.mod`/`go.sum` deps.** Verify unchanged after every task.
- **`<common>` is the only reserved `<…>` key** in `workers`/`collaborators`; any other `<…>`-wrapped key is a hard error; real class names may not contain `<`/`>`.
- **Merge rule** (both trees): effective class = merge(`<common>`, own) — scalars override (own > `<common>`), `secrets` union (own key wins), `prompt` own-only and required for workers.
- **Well-known secret names, validated** (unknown keys rejected): `source-control.github.secrets` → `AT_TASK_GIT_TOKEN`; `tracker.linear.secrets` → `AT_DISPATCH_TRACKER_TOKEN` + `AT_DISPATCH_WEBHOOK_SECRET`. Root `secrets` + `collaborators.*.secrets` keep arbitrary names.
- **Structural air-gap:** the code-host token lives only in `source-control` and is handed only to the `at-task` steps; the scheduler's tracker secrets never enter a VM. The rename must not move any secret into the agent env.
- **Scheduler untouched this plan.** `internal/dispatch/config` and `at-cove dispatch --config` stay as-is; the kit's `tracker`/`dispatch` are parsed + validated but not yet consumed at runtime (Plan 2).
- **Every kit-schema change updates the reference kit + `at-cove-config.md` in the same task** so both stay valid/accurate.

---

## Task 1: `origin` → `source-control`, nest `main-branch`

Pure rename + field relocation, behavior-preserving.

**Files:** `internal/kit/config.go`, `internal/dispatchrun/dispatchrun.go`, `internal/dispatchrun/dispatchrun_test.go`, `internal/kit/config_test.go`, `internal/kit/refkit_test.go`, `kits/reference-worker/config.yml`.

**Interfaces produced:** `kit.SourceControl{ GitHub *kit.GitHubSource }`; `kit.GitHubSource{ Project, MainBranch string }`; `Config.SourceControl *SourceControl`. `Config.Origin` and `Config.MainBranch` are removed.

- [ ] **Step 1: Update the failing tests first**

In `internal/kit/config_test.go`, change the origin/main-branch assertions to the new shape (find the block around the current lines 229–246):

```go
	if cfg.SourceControl == nil || cfg.SourceControl.GitHub == nil || cfg.SourceControl.GitHub.Project != "acme/myrepo" {
		t.Fatalf("source-control not parsed: %+v", cfg.SourceControl)
	}
	if cfg.SourceControl.GitHub.MainBranch != "main" { // default
		t.Fatalf("main-branch default = %q; want main", cfg.SourceControl.GitHub.MainBranch)
	}
```

and the override test (around current line 243–246):

```go
	if cfg.SourceControl.GitHub.MainBranch != "develop" {
		t.Fatalf("main-branch = %q; want develop", cfg.SourceControl.GitHub.MainBranch)
	}
```

Also update those two tests' inline YAML: `origin:` → `source-control:` and move `main-branch:` to sit under `github:` (indented beside `project:`). In `internal/kit/refkit_test.go`, change the origin assertion:

```go
	if cfg.SourceControl == nil || cfg.SourceControl.GitHub == nil || strings.TrimSpace(cfg.SourceControl.GitHub.Project) == "" {
		t.Errorf("expected a non-empty source-control.github.project; source-control=%+v", cfg.SourceControl)
	}
```

- [ ] **Step 2: Run the tests to confirm they fail to compile**

Run: `go test ./internal/kit/`
Expected: build failure (`cfg.SourceControl` undefined).

- [ ] **Step 3: Reshape the struct in `internal/kit/config.go`**

Replace the `Origin`/`GitHubOrigin` types and the two `Config` fields:

```go
// SourceControl names the code host + repo the kit targets — a tagged union
// (exactly one host; github only today). It is the single source of the repo
// identity and the host kind.
type SourceControl struct {
	GitHub *GitHubSource `yaml:"github,omitempty"`
}

type GitHubSource struct {
	Project    string `yaml:"project"`               // "owner/name"
	MainBranch string `yaml:"main-branch,omitempty"` // base branch; default "main"
}

// Active returns the set host, or an error if not exactly one.
func (s *SourceControl) Active() (string, error) {
	n, name := 0, ""
	if s.GitHub != nil {
		n, name = n+1, "github"
	}
	if n != 1 {
		return "", errors.New("must set exactly one host (github)")
	}
	return name, nil
}
```

In `Config`, replace the `Origin`/`MainBranch` fields with:

```go
	SourceControl *SourceControl `yaml:"source-control,omitempty"`
```

- [ ] **Step 4: Update validation + default in `ParseConfig`**

Replace the old `if cfg.Origin != nil { … }` block and the `if cfg.MainBranch == "" { cfg.MainBranch = "main" }` block with:

```go
	if cfg.SourceControl != nil {
		if _, err := cfg.SourceControl.Active(); err != nil {
			return Config{}, fmt.Errorf("config.yml: source-control: %w", err)
		}
		if gh := cfg.SourceControl.GitHub; gh != nil {
			if parts := strings.Split(gh.Project, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
				return Config{}, fmt.Errorf("config.yml: source-control.github.project must be \"owner/name\", got %q", gh.Project)
			}
			if gh.MainBranch == "" {
				gh.MainBranch = "main"
			}
		}
	}
```

- [ ] **Step 5: Update the readers in `internal/dispatchrun/dispatchrun.go`**

- `o.Cfg.Origin == nil || o.Cfg.Origin.GitHub == nil` → `o.Cfg.SourceControl == nil || o.Cfg.SourceControl.GitHub == nil` (and the error text "declares no origin" → "declares no source-control").
- `task.Repo.Name = o.Cfg.Origin.GitHub.Project` → `o.Cfg.SourceControl.GitHub.Project`.
- `task.Repo.SourceBranch = o.Cfg.MainBranch` → `o.Cfg.SourceControl.GitHub.MainBranch`.
- `"COVE_RUN_REPO": o.Cfg.Origin.GitHub.Project` → `o.Cfg.SourceControl.GitHub.Project`.

In `internal/dispatchrun/dispatchrun_test.go`, replace every `Origin: &kit.Origin{GitHub: &kit.GitHubOrigin{Project: "acme/myrepo"}}` + separate `MainBranch: "main"` pair with:

```go
			SourceControl: &kit.SourceControl{GitHub: &kit.GitHubSource{Project: "acme/myrepo", MainBranch: "main"}},
```

(there are ~5 such `kit.Config{…}` literals — update all).

- [ ] **Step 6: Migrate the reference kit**

In `kits/reference-worker/config.yml`, change:

```yaml
source-control:
  github:
    project: your-org/your-repo   # ← set to the target repo
    main-branch: main
```

(was `origin:` with a top-level `main-branch:`).

- [ ] **Step 7: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
git diff --stat go.mod go.sum   # expect: empty
```
Expected: green; deps unchanged.
```bash
git commit -am "refactor(kit): origin -> source-control; nest main-branch under github member"
```

---

## Task 2: `workers` — `<common>` base + timeout/concurrency + resolver

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `internal/dispatchrun/dispatchrun.go`, `internal/dispatchrun/dispatchrun_test.go`, `kits/reference-worker/config.yml`.

**Interfaces produced:** `kit.Worker{ Prompt, Timeout string; Concurrency int }`; `(Config) ResolvedWorker(class string) (Worker, error)` — merges `<common>` into a named worker (own > `<common>`; `prompt` own-only); errors if `class` is empty, `<common>`, or absent. `commonKey = "<common>"`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/kit/config_test.go`:

```go
func TestResolvedWorkerMergesCommon(t *testing.T) {
	src := `
name: k
workers:
  <common>:
    timeout: 30m
    concurrency: 2
  implement:
    prompt: "do the thing"
    timeout: 40m
  audit:
    prompt: "check the thing"
    concurrency: 1
`
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	impl, err := cfg.ResolvedWorker("implement")
	if err != nil {
		t.Fatalf("ResolvedWorker(implement): %v", err)
	}
	if impl.Prompt != "do the thing" || impl.Timeout != "40m" || impl.Concurrency != 2 {
		t.Fatalf("implement merge = %+v; want prompt/40m(own)/2(common)", impl)
	}
	aud, err := cfg.ResolvedWorker("audit")
	if err != nil {
		t.Fatalf("ResolvedWorker(audit): %v", err)
	}
	if aud.Timeout != "30m" || aud.Concurrency != 1 {
		t.Fatalf("audit merge = %+v; want 30m(common)/1(own)", aud)
	}
	if _, err := cfg.ResolvedWorker("<common>"); err == nil {
		t.Fatal("ResolvedWorker(<common>) should error (not a real class)")
	}
	if _, err := cfg.ResolvedWorker("nope"); err == nil {
		t.Fatal("ResolvedWorker(nope) should error (absent)")
	}
}

func TestParseConfigRejectsUnknownAngleKey(t *testing.T) {
	src := "name: k\nworkers:\n  <bogus>:\n    timeout: 30m\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection of reserved-looking key <bogus>")
	}
}

func TestParseConfigWorkerRequiresPrompt(t *testing.T) {
	src := "name: k\nworkers:\n  implement:\n    timeout: 30m\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected a real worker to require a prompt")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/kit/ -run 'ResolvedWorker|AngleKey|RequiresPrompt'`
Expected: build failure (`ResolvedWorker` undefined) then FAIL.

- [ ] **Step 3: Extend the `Worker` type + add the resolver**

In `internal/kit/config.go`, replace the `Worker` type:

```go
const commonKey = "<common>"

// Worker declares an autonomous handler class: the role prompt at-cove sends the
// agent (own-only, required) plus scheduling attrs that may be inherited from the
// workers <common> base.
type Worker struct {
	Prompt      string `yaml:"prompt,omitempty"`
	Timeout     string `yaml:"timeout,omitempty"`     // Go duration
	Concurrency int    `yaml:"concurrency,omitempty"`
}

// ResolvedWorker returns the named worker with the workers <common> base merged
// in (own overrides <common>; prompt is own-only). It errors if class is empty,
// the reserved <common> key, or absent.
func (c Config) ResolvedWorker(class string) (Worker, error) {
	if class == "" || class == commonKey {
		return Worker{}, fmt.Errorf("kit %q: %q is not a dispatchable worker class", c.Name, class)
	}
	own, ok := c.Workers[class]
	if !ok {
		return Worker{}, fmt.Errorf("kit %q declares no worker class %q", c.Name, class)
	}
	base := c.Workers[commonKey] // zero value if absent
	if own.Timeout == "" {
		own.Timeout = base.Timeout
	}
	if own.Concurrency == 0 {
		own.Concurrency = base.Concurrency
	}
	return own, nil
}
```

- [ ] **Step 4: Update `workers` validation in `ParseConfig`**

Replace the existing `for class, w := range cfg.Workers { … }` loop:

```go
	for class, w := range cfg.Workers {
		if strings.TrimSpace(class) == "" {
			return Config{}, fmt.Errorf("config.yml: workers: a class name (map key) must not be empty")
		}
		if isReservedAngleKey(class) {
			return Config{}, fmt.Errorf("config.yml: workers: %q is not a valid key (only %q is reserved)", class, commonKey)
		}
		if class == commonKey {
			// base: prompt not allowed; scalars validated below
			if strings.TrimSpace(w.Prompt) != "" {
				return Config{}, fmt.Errorf("config.yml: workers[%q]: the base must not set a prompt", commonKey)
			}
		} else if strings.TrimSpace(w.Prompt) == "" {
			return Config{}, fmt.Errorf("config.yml: workers[%q]: prompt is required", class)
		}
		if w.Timeout != "" {
			if err := checkKitDuration(fmt.Sprintf("workers[%q].timeout", class), w.Timeout); err != nil {
				return Config{}, err
			}
		}
		if w.Concurrency < 0 {
			return Config{}, fmt.Errorf("config.yml: workers[%q].concurrency must be >= 0", class)
		}
	}
```

Add the two helpers (near the bottom of the file):

```go
// isReservedAngleKey reports whether key is <…>-wrapped but not the one allowed
// reserved base key <common>.
func isReservedAngleKey(key string) bool {
	if !strings.HasPrefix(key, "<") || !strings.HasSuffix(key, ">") {
		return false
	}
	return key != commonKey
}

func checkKitDuration(field, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("config.yml: %s must be a positive Go duration (e.g. 30m), got %q", field, v)
	}
	return nil
}
```

Add `"time"` to the imports.

- [ ] **Step 5: Point `dispatchrun` at the resolver**

In `internal/dispatchrun/dispatchrun.go`, replace:

```go
	w, ok := o.Cfg.Workers[task.Worker.Class]
	if !ok {
		return fmt.Errorf("kit %q declares no worker class %q", o.Cfg.Name, task.Worker.Class)
	}
```

with:

```go
	w, err := o.Cfg.ResolvedWorker(task.Worker.Class)
	if err != nil {
		return err
	}
```

(The later `agentPrompt(w.Prompt)` use is unchanged; `err` is already declared later in the function via `:=` — if the compiler reports "no new variables on left side", change this to a fresh `var` or reuse; verify with `go build`.)

- [ ] **Step 6: Migrate the reference kit workers**

In `kits/reference-worker/config.yml`, add a `<common>` base above `implement`:

```yaml
workers:
  <common>:
    timeout: 30m
    concurrency: 1
  implement:
    prompt: |
      You are an implementer. Make the change described in the task and run the
      project's tests. Keep the change minimal and focused.
```

- [ ] **Step 7: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
git diff --stat go.mod go.sum
```
Expected: green; deps unchanged.
```bash
git commit -am "feat(kit): workers gain <common> base + timeout/concurrency + ResolvedWorker"
```

---

## Task 3: `tracker` union, `dispatch` block, `collaborators` tree (schema only)

Add the three new trees to `kit.Config` — parsed + validated, **not consumed at runtime this plan**.

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `kits/reference-worker/config.yml`.

**Interfaces produced:**
- `kit.Tracker{ Linear *LinearTracker }` with `Active()`; `kit.LinearTracker{ Team, PollInterval, ClassLabelPrefix string; States StateMap; Secrets map[string]SecretConfig }`; `kit.StateMap{ Ready, InProgress, InReview, Done, NeedsInput, Blocked string }`.
- `kit.Dispatch{ Concurrency int; ReaperTimeout, DispatchOverhead string }`.
- `kit.Collaborator{ Secrets map[string]SecretConfig }`; `(Config) ResolvedCollaborator(class string) (Collaborator, error)`.
- `Config` gains `Tracker *Tracker`, `Dispatch *Dispatch`, `Collaborators map[string]Collaborator`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/kit/config_test.go`:

```go
const trackerKit = `
name: k
tracker:
  linear:
    team: COV
    poll-interval: 60s
    states:
      ready: Todo
      in-progress: In Progress
      in-review: In Review
      done: Done
      needs-input: Needs Input
      blocked: Backlog
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  { command: ["gh", "auth", "token"] }
      AT_DISPATCH_WEBHOOK_SECRET: { command: ["true"] }
dispatch:
  concurrency: 1
  reaper-timeout: 45m
collaborators:
  <common>:
    secrets:
      COMMON_TOKEN: { command: ["true"] }
  triager:
    secrets:
      LINEAR_TOKEN: { command: ["true"] }
`

func TestParseConfigTrackerDispatchCollaborators(t *testing.T) {
	cfg, err := ParseConfig([]byte(trackerKit))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Tracker == nil || cfg.Tracker.Linear == nil || cfg.Tracker.Linear.Team != "COV" {
		t.Fatalf("tracker not parsed: %+v", cfg.Tracker)
	}
	if cfg.Tracker.Linear.ClassLabelPrefix != "class:" { // default
		t.Fatalf("class-label-prefix default = %q; want class:", cfg.Tracker.Linear.ClassLabelPrefix)
	}
	if cfg.Dispatch == nil || cfg.Dispatch.DispatchOverhead != "15m" { // default
		t.Fatalf("dispatch-overhead default = %+v; want 15m", cfg.Dispatch)
	}
	col, err := cfg.ResolvedCollaborator("triager")
	if err != nil {
		t.Fatalf("ResolvedCollaborator(triager): %v", err)
	}
	if _, ok := col.Secrets["COMMON_TOKEN"]; !ok { // from <common>
		t.Fatalf("collaborator secrets missing COMMON_TOKEN: %+v", col.Secrets)
	}
	if _, ok := col.Secrets["LINEAR_TOKEN"]; !ok { // own
		t.Fatalf("collaborator secrets missing LINEAR_TOKEN: %+v", col.Secrets)
	}
}

func TestParseConfigRejectsUnknownTrackerSecret(t *testing.T) {
	src := strings.Replace(trackerKit, "AT_DISPATCH_TRACKER_TOKEN", "BOGUS_TOKEN", 1)
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection of an unknown tracker secret name")
	}
}

func TestParseConfigRejectsMissingTrackerState(t *testing.T) {
	src := strings.Replace(trackerKit, "      blocked: Backlog\n", "", 1)
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection when a tracker state is missing")
	}
}
```

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/kit/ -run 'TrackerDispatch|UnknownTrackerSecret|MissingTrackerState'`
Expected: build failure then FAIL.

- [ ] **Step 3: Add the types**

In `internal/kit/config.go`:

```go
// Tracker names the issue tracker the kit's scheduler drives — a tagged union
// (exactly one provider; linear only today). Consumed by `at-cove dispatch`.
type Tracker struct {
	Linear *LinearTracker `yaml:"linear,omitempty"`
}

func (t *Tracker) Active() (string, error) {
	if t.Linear != nil {
		return "linear", nil
	}
	return "", errors.New("must set exactly one provider (linear)")
}

// LinearTracker wires the scheduler to one Linear team.
type LinearTracker struct {
	Team             string                  `yaml:"team"`
	PollInterval     string                  `yaml:"poll-interval"`
	ClassLabelPrefix string                  `yaml:"class-label-prefix"`
	States           StateMap                `yaml:"states"`
	Secrets          map[string]SecretConfig `yaml:"secrets"`
}

// StateMap binds the design's lifecycle roles to a team's real state names.
type StateMap struct {
	Ready      string `yaml:"ready"`
	InProgress string `yaml:"in-progress"`
	InReview   string `yaml:"in-review"`
	Done       string `yaml:"done"`
	NeedsInput string `yaml:"needs-input"`
	Blocked    string `yaml:"blocked"`
}

// Dispatch holds the scheduler policy knobs (consumed by `at-cove dispatch`).
type Dispatch struct {
	Concurrency      int    `yaml:"concurrency"`
	ReaperTimeout    string `yaml:"reaper-timeout"`
	DispatchOverhead string `yaml:"dispatch-overhead"`
}

// Collaborator declares an interactive (chat) handler class. Its secrets may be
// inherited from the collaborators <common> base. Parsed + validated; wired later.
type Collaborator struct {
	Secrets map[string]SecretConfig `yaml:"secrets"`
}

// ResolvedCollaborator returns the named collaborator with the collaborators
// <common> secrets merged in (own key wins). Errors like ResolvedWorker.
func (c Config) ResolvedCollaborator(class string) (Collaborator, error) {
	if class == "" || class == commonKey {
		return Collaborator{}, fmt.Errorf("kit %q: %q is not a collaborator class", c.Name, class)
	}
	own, ok := c.Collaborators[class]
	if !ok {
		return Collaborator{}, fmt.Errorf("kit %q declares no collaborator class %q", c.Name, class)
	}
	merged := map[string]SecretConfig{}
	for k, v := range c.Collaborators[commonKey].Secrets {
		merged[k] = v
	}
	for k, v := range own.Secrets {
		merged[k] = v
	}
	own.Secrets = merged
	return own, nil
}
```

Add these fields to `Config`:

```go
	Tracker       *Tracker                `yaml:"tracker,omitempty"`
	Dispatch      *Dispatch               `yaml:"dispatch,omitempty"`
	Collaborators map[string]Collaborator `yaml:"collaborators,omitempty"`
```

- [ ] **Step 4: Add validation + defaults in `ParseConfig`**

Add near the end of `ParseConfig`, before `return cfg, nil`:

```go
	if cfg.Tracker != nil {
		if _, err := cfg.Tracker.Active(); err != nil {
			return Config{}, fmt.Errorf("config.yml: tracker: %w", err)
		}
		if lt := cfg.Tracker.Linear; lt != nil {
			if strings.TrimSpace(lt.Team) == "" {
				return Config{}, fmt.Errorf("config.yml: tracker.linear.team is required")
			}
			if err := checkKitDuration("tracker.linear.poll-interval", lt.PollInterval); err != nil {
				return Config{}, err
			}
			if lt.ClassLabelPrefix == "" {
				lt.ClassLabelPrefix = "class:"
			}
			states := map[string]string{
				"ready": lt.States.Ready, "in-progress": lt.States.InProgress,
				"in-review": lt.States.InReview, "done": lt.States.Done,
				"needs-input": lt.States.NeedsInput, "blocked": lt.States.Blocked,
			}
			for name, v := range states {
				if strings.TrimSpace(v) == "" {
					return Config{}, fmt.Errorf("config.yml: tracker.linear.states.%s is required", name)
				}
			}
			if err := checkWellKnownSecrets("tracker.linear.secrets", lt.Secrets,
				"AT_DISPATCH_TRACKER_TOKEN", "AT_DISPATCH_WEBHOOK_SECRET"); err != nil {
				return Config{}, err
			}
		}
	}
	if cfg.Dispatch != nil {
		if cfg.Dispatch.Concurrency < 1 {
			return Config{}, fmt.Errorf("config.yml: dispatch.concurrency must be >= 1, got %d", cfg.Dispatch.Concurrency)
		}
		if err := checkKitDuration("dispatch.reaper-timeout", cfg.Dispatch.ReaperTimeout); err != nil {
			return Config{}, err
		}
		if cfg.Dispatch.DispatchOverhead == "" {
			cfg.Dispatch.DispatchOverhead = "15m"
		}
		if err := checkKitDuration("dispatch.dispatch-overhead", cfg.Dispatch.DispatchOverhead); err != nil {
			return Config{}, err
		}
	}
	if err := validateClassTree("collaborators", collaboratorKeys(cfg.Collaborators)); err != nil {
		return Config{}, err
	}
```

Add helpers:

```go
// checkWellKnownSecrets requires exactly the allowed secret keys (each with a
// non-empty resolver) and rejects any other key.
func checkWellKnownSecrets(field string, got map[string]SecretConfig, allowed ...string) error {
	want := map[string]bool{}
	for _, a := range allowed {
		want[a] = true
		if s, ok := got[a]; !ok || len(s.Command) == 0 {
			return fmt.Errorf("config.yml: %s.%s is required (with a resolver command)", field, a)
		}
	}
	for k := range got {
		if !want[k] {
			return fmt.Errorf("config.yml: %s: unknown secret %q (allowed: %v)", field, k, allowed)
		}
	}
	return nil
}

// validateClassTree rejects reserved-looking keys other than <common> and empty
// keys in a class map.
func validateClassTree(field string, keys []string) error {
	for _, k := range keys {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("config.yml: %s: a class name (map key) must not be empty", field)
		}
		if isReservedAngleKey(k) {
			return fmt.Errorf("config.yml: %s: %q is not a valid key (only %q is reserved)", field, k, commonKey)
		}
	}
	return nil
}

func collaboratorKeys(m map[string]Collaborator) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
```

- [ ] **Step 5: Migrate the reference kit**

In `kits/reference-worker/config.yml`, add a `tracker`, `dispatch`, and sample `collaborators` (keep the placeholder resolver commands `["true"]` where a real one is project-specific, matching the template style). Add after `source-control:`:

```yaml
tracker:
  linear:
    team: your-team-key        # ← set to your Linear team
    poll-interval: 60s
    states:
      ready: Todo
      in-progress: In Progress
      in-review: In Review
      done: Done
      needs-input: Needs Input
      blocked: Backlog
    secrets:
      AT_DISPATCH_TRACKER_TOKEN:  { command: ["gh", "auth", "token"] }
      AT_DISPATCH_WEBHOOK_SECRET: { command: ["true"] }

dispatch:
  concurrency: 1
  reaper-timeout: 45m

collaborators:
  triager:
    secrets:
      LINEAR_TOKEN: { command: ["true"] }
```

- [ ] **Step 6: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
git diff --stat go.mod go.sum
```
Expected: green; deps unchanged.
```bash
git commit -am "feat(kit): add tracker union + dispatch block + collaborators tree (schema)"
```

---

## Task 4: relocate the code-host secret → structural air-gap

Move `AT_TASK_GIT_TOKEN` from the flat root `secrets` into `source-control.github.secrets`, and change `dispatchrun` to receive it as a distinct spec (drop the name-based split).

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `internal/kit/refkit_test.go`, `internal/dispatchrun/dispatchrun.go`, `internal/dispatchrun/dispatchrun_test.go`, `cmd/at-cove/main.go`, `kits/reference-worker/config.yml`.

**Interfaces produced:** `GitHubSource.Secrets map[string]SecretConfig`; `(Config) GitTokenSpec() (secret.Spec, bool)` — the `AT_TASK_GIT_TOKEN` resolver from `source-control.github.secrets`. `dispatchrun.Options` gains `GitToken secret.Spec` (the code-host token; root `Secrets` no longer contains it).

- [ ] **Step 1: Write the failing tests**

In `internal/kit/config_test.go`, add:

```go
func TestGitTokenSpecFromSourceControl(t *testing.T) {
	src := `
name: k
source-control:
  github:
    project: acme/myrepo
    secrets:
      AT_TASK_GIT_TOKEN: { command: ["mint.sh"] }
`
	cfg, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	spec, ok := cfg.GitTokenSpec()
	if !ok || spec.Name != "AT_TASK_GIT_TOKEN" || spec.Command[0] != "mint.sh" {
		t.Fatalf("GitTokenSpec = %+v, ok=%v", spec, ok)
	}
}

func TestParseConfigRejectsUnknownSourceControlSecret(t *testing.T) {
	src := "name: k\nsource-control:\n  github:\n    project: a/b\n    secrets:\n      BOGUS: { command: [\"x\"] }\n"
	if _, err := ParseConfig([]byte(src)); err == nil {
		t.Fatal("expected rejection of an unknown source-control secret name")
	}
}
```

Update `internal/kit/refkit_test.go`: the git-token assertion now reads from source-control:

```go
	spec, ok := cfg.GitTokenSpec()
	if !ok || len(spec.Command) == 0 {
		t.Errorf("expected source-control AT_TASK_GIT_TOKEN with a resolver command; source-control=%+v", cfg.SourceControl)
	}
```

(remove the old `cfg.Secrets["AT_TASK_GIT_TOKEN"]` check; add `secret` import if the returned type needs it — `GitTokenSpec` returns `secret.Spec`).

In `internal/dispatchrun/dispatchrun_test.go`, the air-gap test (`TestDispatch…` around line 103–146) must now supply the git token via the new `GitToken` option and the kit's source-control, not via the root `Secrets` list. Change that test's `kit.Config{…}` to include `SourceControl.GitHub.Secrets` and set `Options.GitToken`; drop the `{Name: "AT_TASK_GIT_TOKEN", …}` entry from the root `Secrets` slice. Keep the assertion that the agent step's env does **not** carry `AT_TASK_GIT_TOKEN` (line ~146).

- [ ] **Step 2: Run to confirm failure**

Run: `go test ./internal/kit/ ./internal/dispatchrun/`
Expected: build failure (`GitTokenSpec`/`GitToken` undefined) then FAIL.

- [ ] **Step 3: Schema — add `source-control.github.secrets` + `GitTokenSpec`**

In `internal/kit/config.go`, add `Secrets` to `GitHubSource`:

```go
type GitHubSource struct {
	Project    string                  `yaml:"project"`
	MainBranch string                  `yaml:"main-branch,omitempty"`
	Secrets    map[string]SecretConfig `yaml:"secrets,omitempty"`
}
```

In the `source-control` validation block (Task 1's `if gh := cfg.SourceControl.GitHub; gh != nil { … }`), after the project/main-branch checks add — guarded, so a source-control with **no** `secrets:` still parses (connect-only kits); only a declared block must hold exactly the well-known key:

```go
			if len(gh.Secrets) > 0 {
				if err := checkWellKnownSecrets("source-control.github.secrets", gh.Secrets, "AT_TASK_GIT_TOKEN"); err != nil {
					return Config{}, err
				}
			}
```

Add the accessor (needs the `secret` package):

```go
// GitTokenSpec returns the code-host token resolver from source-control, if set.
func (c Config) GitTokenSpec() (secret.Spec, bool) {
	if c.SourceControl == nil || c.SourceControl.GitHub == nil {
		return secret.Spec{}, false
	}
	s, ok := c.SourceControl.GitHub.Secrets["AT_TASK_GIT_TOKEN"]
	if !ok {
		return secret.Spec{}, false
	}
	return secret.Spec{Name: "AT_TASK_GIT_TOKEN", Command: s.Command}, true
}
```

Add the import `"github.com/aethons-tools/cove/internal/secret"`. (If this creates an import cycle — `secret` importing `kit` — instead define `GitTokenSpec` to return `(name string, command []string, ok bool)` and let the caller build the `secret.Spec`. Verify with `go build`; prefer the plain-types return if a cycle exists.)

> **Note:** `checkWellKnownSecrets` requires the token when `secrets:` is present under `source-control.github`. A kit with no `source-control.github.secrets` at all still parses (the block is skipped) — `work`/`dispatch`-time validation (Plan 2) enforces its presence for those commands. This keeps `connect`-only kits valid.

- [ ] **Step 4: `dispatchrun` — take the token as a distinct spec**

In `internal/dispatchrun/dispatchrun.go`:
- Add to `Options`: `GitToken secret.Spec // code-host token; withheld from the agent step`.
- Delete the `gitTokenEnv` constant and the name-based split loop. Replace the base/token split (current lines ~115–125) with:

```go
	base, err := secret.Resolve(o.R, runEnv, o.Secrets) // root secrets → agent bucket; fail closed
```

  and everywhere the code previously merged the freshly-minted `AT_TASK_GIT_TOKEN` for a git step, mint from `o.GitToken` instead (re-resolve `[]secret.Spec{o.GitToken}` with `runEnv` before `prepare` and before `complete`, exactly as today — only the *source* of the spec changes from "the split-out entry" to `o.GitToken`).

> Read the current mint closure to keep its per-step behavior identical; only the spec's origin changes. `o.Secrets` now carries **only** the root (agent) secrets.

- [ ] **Step 5: `cmd/at-cove` — build root secrets + git token separately**

In `doWork` (`cmd/at-cove/main.go`), where `specs` is built from `cfg.Secrets`, that stays the root/agent bucket. Add the git token from the kit and pass it through:

```go
	gitTok, ok := cfg.GitTokenSpec()
	if !ok {
		fmt.Fprintf(stderr, "at-cove: kit %q declares no source-control.github.secrets AT_TASK_GIT_TOKEN\n", cfg.Name)
		return 1
	}
```

and add `GitToken: gitTok,` to the `dispatchrun.Options{…}` literal. (`cfg.Secrets` — the root secrets — continues to populate `Secrets:`.)

- [ ] **Step 6: Migrate the reference kit — move the token**

In `kits/reference-worker/config.yml`, move `AT_TASK_GIT_TOKEN` out of the root `secrets:` block and under `source-control.github.secrets`; drop the now-empty root `secrets:` (or leave a commented example of a root/agent secret):

```yaml
source-control:
  github:
    project: your-org/your-repo
    main-branch: main
    secrets:
      # Code-host token used ONLY by at-task prepare/complete (clone/push/PR).
      # Resolved on the host, minted per git step, injected in memory. The agent never sees it.
      AT_TASK_GIT_TOKEN:
        description: per-task GitHub App installation token — push + PR on the repo, minted per run
        command: ["mint-github-token.sh"]
```

- [ ] **Step 7: Verify + commit**

```
go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/
go build -tags integration ./internal/dispatchrun/    # e2e compiles
git diff --stat go.mod go.sum
grep -rn "AT_TASK_GIT_TOKEN" kits/reference-worker/config.yml   # under source-control only
```
Expected: green; the air-gap test passes with the token sourced from `source-control`; deps unchanged.
```bash
git commit -am "feat(kit): code-host token moves to source-control.github.secrets (structural air-gap)"
```

---

## Task 5: rewrite `at-cove-config.md`

Bring the canonical config doc in line with the unified schema.

**Files:** `docs/usage/at-cove-config.md` (and its `INDEX.md` row if the `read_when`/`owns` change).

- [ ] **Step 1: Rewrite the schema doc**

Rewrite `docs/usage/at-cove-config.md` to document the unified surface, following the **docs-author** skill (frontmatter `summary`/`read_when`/`owns` updated; `owns` becomes "the config.yml schema: name, source-control, tracker, dispatch, workers, collaborators, secrets, image"; bump `updated: 2026-07-10`). Cover, in this order: `name`; `source-control` (union, `github.project`/`main-branch`/`secrets` with the well-known `AT_TASK_GIT_TOKEN`); `tracker` (union, `linear` team/poll-interval/class-label-prefix/states/secrets with the two `AT_DISPATCH_*`); `dispatch` (concurrency/reaper-timeout/dispatch-overhead + defaults); root `secrets` (VM-injected agent bucket); `workers` (autonomous; `<common>` base + per-class `prompt`/`timeout`/`concurrency`; the merge rule); `collaborators` (interactive; `<common>` + per-class `secrets`; note "validated now, wired later"); `image`. Include the **secret-bucket table** (root → VM; source-control → at-task per-step; tracker → scheduler-only; collaborators → later) and a **full annotated example** matching the reference kit. Reuse the exact prose for `image.*` and `secrets.*.command`/`description` from the current doc (unchanged mechanics) — link, don't restate, the secret-resolution rules in [at-cove-secrets.md](at-cove-secrets.md).

- [ ] **Step 2: Verify + commit**

```
python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage/
go build ./... && go test ./...
```
Expected: docs-audit 0 errors (pre-existing warnings ok); links resolve; build/tests still green.
```bash
git add -A
git commit -m "docs(config): rewrite at-cove-config.md for the unified kit schema"
```

---

## Final verification (Plan 1)

- [ ] `go build ./...`, `go vet ./...`, `go test ./...` green; `gofmt -l cmd/ internal/` empty; `go.mod`/`go.sum` unchanged; `go build -tags integration ./internal/dispatchrun/` compiles.
- [ ] `at-cove work`/`connect` behavior unchanged; the air-gap test passes with the token sourced from `source-control`.
- [ ] `grep -rn "\.Origin\b\|\.MainBranch\b\|kit.Origin\|GitHubOrigin\|gitTokenEnv" cmd/ internal/` — nothing (all renamed/removed).
- [ ] `source-control`, `tracker`, `dispatch`, `workers` (`<common>` + attrs), `collaborators` all parse + validate; the reference kit is a valid unified kit; well-known secret names enforced; `<…>` keys other than `<common>` rejected.
- [ ] Scheduler untouched (`internal/dispatch/config`, `at-cove dispatch --config` unchanged) — its kit-side `tracker`/`dispatch` are validated but not yet consumed.

## Notes

- **Two-plan split:** this plan is the whole kit *config surface* (schema + secret buckets, incl. the git-token relocation which must be atomic — declaration and reader move together). **Plan 2** retires `internal/dispatch/config`, points the scheduler + `linear.New` at `kit.Config`, and switches `at-cove dispatch` to the positional kit-dir (dropping `--config`), absorbing `scheduler-config.md` into `at-cove-config.md`.
- **`connect` untouched** — still resolves root `secrets`. Interactive/chat-class wiring is a later session (the spec's non-goal).
