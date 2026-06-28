# Loop Mode — Phase C-1: `loops:` Config Schema

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Parse and validate a `loops:` map in `config.yml` —
each named loop carrying `interval`, `check`, `prompt`, optional `setup`, and optional `fresh-workspace` —
so later Phase C sub-plans (lifecycle, scheduler) can consume it.

**Architecture:** Add a `Loop` struct and a `Loops map[string]Loop` field to `kit.Config`,
with validation in `ParseConfig`:
each loop's `interval` must be a positive Go duration, and `check`/`prompt` are required.
A `ParsedInterval()` helper returns the validated duration for consumers.
Loop-name charset validation is deliberately left to instance-construction time
(`state.ValidLoopName`, landed in Phase A), keeping `kit` decoupled from `state`.

**Tech Stack:** Go 1.22, standard library + `gopkg.in/yaml.v3` (already a dependency).

## Global Constraints

- Go version floor `go 1.22`; no new dependencies (`gopkg.in/yaml.v3` already present).
- `KnownFields(true)` stays on the decoder — unknown keys anywhere (including inside a loop entry) are a hard error.
- `loops:` is optional; absent ⇒ empty map ⇒ no loops.
- Per loop: `interval` is **required** and must be a **positive** Go duration string (e.g. `5m`); `check` and `prompt` are **required**; `setup` and `fresh-workspace` are optional.
- Loop-**name** charset validation is NOT done here — it happens at use via `state.ValidLoopName` (Phase A). This keeps `kit` free of a `state` dependency.
- Hermetic tests; follow the existing `internal/kit/config_test.go` style.

---

### Task 1: `Loop` struct, `Config.Loops`, and validation

**Files:**
- Modify: `internal/kit/config.go`
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (relied on by later Phase C sub-plans):
  - `type Loop struct { Interval, Check, Prompt, Setup string; FreshWorkspace bool }` with yaml tags `interval`/`check`/`prompt`/`setup`/`fresh-workspace`.
  - `func (l Loop) ParsedInterval() time.Duration`
  - `kit.Config.Loops map[string]Loop` (yaml `loops`)

- [ ] **Step 1: Write the failing tests**

Append to `internal/kit/config_test.go` (and add `"time"` to its imports):

```go
func TestParseConfigLoops(t *testing.T) {
	data := []byte(`
name: x
backend: colima
loops:
  default:
    interval: 5m
    check: "test -e q"
    prompt: "do it"
  fresh:
    interval: 30s
    check: "c"
    prompt: "p"
    setup: "git clone https://x ."
    fresh-workspace: true
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Loops) != 2 {
		t.Fatalf("loops = %+v", cfg.Loops)
	}
	d := cfg.Loops["default"]
	if d.ParsedInterval() != 5*time.Minute {
		t.Fatalf("default interval = %v, want 5m", d.ParsedInterval())
	}
	if d.Check != "test -e q" || d.Prompt != "do it" {
		t.Fatalf("default loop = %+v", d)
	}
	f := cfg.Loops["fresh"]
	if !f.FreshWorkspace || f.Setup != "git clone https://x ." || f.ParsedInterval() != 30*time.Second {
		t.Fatalf("fresh loop = %+v", f)
	}
}

func TestParseConfigLoopValidation(t *testing.T) {
	bad := map[string]string{
		"bad interval":  "name: x\nbackend: colima\nloops:\n  a:\n    interval: nope\n    check: c\n    prompt: p\n",
		"zero interval": "name: x\nbackend: colima\nloops:\n  a:\n    interval: 0s\n    check: c\n    prompt: p\n",
		"no interval":   "name: x\nbackend: colima\nloops:\n  a:\n    check: c\n    prompt: p\n",
		"no check":      "name: x\nbackend: colima\nloops:\n  a:\n    interval: 1m\n    prompt: p\n",
		"no prompt":     "name: x\nbackend: colima\nloops:\n  a:\n    interval: 1m\n    check: c\n",
		"unknown field": "name: x\nbackend: colima\nloops:\n  a:\n    interval: 1m\n    check: c\n    prompt: p\n    bogus: 1\n",
	}
	for label, data := range bad {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestParseConfigNoLoopsOK(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: x\nbackend: colima\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Loops) != 0 {
		t.Fatalf("loops should be empty, got %+v", cfg.Loops)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/kit/ -run 'TestParseConfigLoops|TestParseConfigLoopValidation|TestParseConfigNoLoopsOK'`
Expected: FAIL — build error, `cfg.Loops` / `Loop` / `ParsedInterval` undefined.

- [ ] **Step 3: Add the `Loop` type and `ParsedInterval` helper**

In `internal/kit/config.go`, add `"time"` to the import block, then add the `Loop` type (after the `Secret` type):

```go
// Loop declares a scheduled, unattended agent run for `at-cove loop`. Interval
// is a Go duration string (e.g. "5m"). Check exits 0 to trigger the agent run;
// Prompt is passed to `claude -p`. Setup, when set, overrides the kit-level
// setup for this loop's workspace; FreshWorkspace re-seeds the workspace before
// each trigger.
type Loop struct {
	Interval       string `yaml:"interval"`
	Check          string `yaml:"check"`
	Prompt         string `yaml:"prompt"`
	Setup          string `yaml:"setup"`
	FreshWorkspace bool   `yaml:"fresh-workspace"`
}

// ParsedInterval returns the loop's interval as a time.Duration. It assumes the
// config passed ParseConfig (which rejects unparseable or non-positive
// intervals), so a parse error is reported as a zero duration.
func (l Loop) ParsedInterval() time.Duration {
	d, _ := time.ParseDuration(l.Interval)
	return d
}
```

- [ ] **Step 4: Add `Loops` to `Config`**

In `internal/kit/config.go`, add the field to `Config`:

```go
// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name    string          `yaml:"name"`
	Backend string          `yaml:"backend"`
	Setup   string          `yaml:"setup"` // optional: command run once to populate an isolated workspace
	Secrets []Secret        `yaml:"secrets"`
	Loops   map[string]Loop `yaml:"loops"`
}
```

- [ ] **Step 5: Validate loops in `ParseConfig`**

In `internal/kit/config.go`, in `ParseConfig`, after the existing `for i, s := range cfg.Secrets` validation loop and before `return cfg, nil`, add:

```go
	for name, lp := range cfg.Loops {
		d, err := time.ParseDuration(lp.Interval)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("config.yml: loops[%q]: interval must be a positive Go duration (e.g. 5m), got %q", name, lp.Interval)
		}
		if lp.Check == "" {
			return Config{}, fmt.Errorf("config.yml: loops[%q]: check is required", name)
		}
		if lp.Prompt == "" {
			return Config{}, fmt.Errorf("config.yml: loops[%q]: prompt is required", name)
		}
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/kit/`
Expected: PASS — the three new tests plus the existing config tests.

- [ ] **Step 7: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/kit/ && /usr/local/go/bin/gofmt -l internal/kit/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): loops config schema with per-loop validation"
```

---

## Self-Review

**Spec coverage (Phase C-1 slice):**
- `loops:` map of named loops with `interval`/`check`/`prompt`/`setup`/`fresh-workspace` → Task 1 (`Loop`, `Config.Loops`).
- `interval` required and config-sourced → Task 1 validation + `ParsedInterval`.
- Unknown keys rejected (incl. inside a loop entry) → `KnownFields(true)` (existing) + `TestParseConfigLoopValidation` "unknown field" case.
- Optional `setup`/`fresh-workspace` → struct fields with no required validation; covered by `TestParseConfigLoops`.

Deferred to later Phase C sub-plans: loop instance lifecycle + naming + name-aware `destroy`/`status` (C-2); headless agent run + check runner + setup sentinel/reset/fail-fast carry-ins (C-3); the drain-then-poll scheduler + `loop` command wiring + keep-awake + `ANTHROPIC_API_KEY` startup check (C-4). Loop-name charset validation is enforced at use via `state.ValidLoopName` (Phase A).

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `Loop` field names/tags and `ParsedInterval()` are defined once and used consistently in the tests; `Config.Loops` is `map[string]Loop` throughout. The validation references `lp.Interval`/`lp.Check`/`lp.Prompt`, matching the struct.
