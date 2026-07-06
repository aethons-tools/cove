# at-dispatch Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement `at-dispatch`'s configuration layer — the YAML schema, strict loader, validation, the `DISPATCH_*` command contract, and the `result.json` parser — plus a `serve --config` that loads and validates a config.

**Architecture:** A new `internal/dispatch/config` package holds the config types, a bytes-based `ParseConfig` + path-based `LoadConfig` (strict `KnownFields` decode, matching `internal/kit`), a pure `Validate`, the `DISPATCH_*` env builder + secret resolver, and the `result.json` reader. `cmd/at-dispatch`'s `serve` gains `--config`, loads+validates, and reports (the scheduler itself stays out of scope). at-dispatch stays at-cove-agnostic.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already a dep — **no new dependencies**), `encoding/json` for results.

## Global Constraints

- **Package `internal/dispatch/config`**; single Go module `github.com/aethons-tools/cove`.
- **No new third-party dependencies** — stdlib + `gopkg.in/yaml.v3` only; `go.mod` must still list only `gopkg.in/yaml.v3`.
- **at-cove-agnostic:** this package imports nothing from `internal/backend`, `internal/connect`, `internal/assemble`, etc. — no at-cove knowledge.
- **Strict YAML**, fail-closed: `yaml.NewDecoder(...).KnownFields(true)` (recursively rejects unknown keys), matching `internal/kit/config.go`. Config errors are prefixed `config: `.
- **`Config` is a value type** (not a pointer), matching `internal/kit`. `ParseConfig(data []byte) (Config, error)`, `LoadConfig(path string) (Config, error)`.
- **Secrets never persisted:** the config holds resolver argv only; values are produced in memory via an injected resolver func — never written to disk or argv by this package.
- **Exact `DISPATCH_*` env names:** `DISPATCH_ISSUE`, `DISPATCH_CLASS`, `DISPATCH_REPO`, `DISPATCH_TIMEOUT`, `DISPATCH_BRIEF`, `DISPATCH_RESULT`.
- **Exact `result.json` shape:** `status` (`ok|needs_input|error`), `artifacts{branch,prUrl,docPath}`, `needsInput{doing,blocker,need,tried,safeState}`, `summary`, `usage{tokens,wallMs}`.
- **TDD, hermetic tests** — table-driven, bytes/in-memory; the secret resolver is an injected func so no process is ever spawned in tests.
- Spec: [`docs/superpowers/specs/2026-07-06-at-dispatch-config-design.md`](../specs/2026-07-06-at-dispatch-config-design.md).

---

## File Structure

- `internal/dispatch/config/config.go` — types, constants, `ParseConfig`, `LoadConfig`, defaults, `Validate`.
- `internal/dispatch/config/config_test.go` — parse/defaults/strict + validation tests.
- `internal/dispatch/config/env.go` — `Task`, `ResolveSecrets`, `BuildEnv`.
- `internal/dispatch/config/env_test.go`.
- `internal/dispatch/config/result.go` — `Result` types, `ReadResult`.
- `internal/dispatch/config/result_test.go`.
- `cmd/at-dispatch/main.go` — `serve --config` wiring (rewritten).
- `cmd/at-dispatch/main_test.go` — updated serve tests.
- Remove: `internal/dispatch/dispatch.go`, `internal/dispatch/dispatch_test.go` (the `Serve` stub is superseded by config-loading serve); keep `internal/dispatch/doc.go`.

Note on naming vs. spec: the spec's `Load`/`Validate`/`*Config` are realized as `LoadConfig`/`ParseConfig`/`Validate` returning a **value** `Config`, to match the `internal/kit` house style.

---

## Task 1: Config types + strict ParseConfig/LoadConfig + defaults

Define the schema types and the strict, bytes-based parser (no validation yet — Task 2). Establish the `DISPATCH_*` name constants that Validate (Task 2) and BuildEnv (Task 3) share.

**Files:**
- Create: `internal/dispatch/config/config.go`
- Test: `internal/dispatch/config/config_test.go`

**Interfaces:**
- Consumes: `gopkg.in/yaml.v3`.
- Produces: types `Config`, `TrackerConfig`, `StateMap`, `RepoConfig`, `SecretRef`, `Secret`, `Class`; `func ParseConfig(data []byte) (Config, error)`; `func LoadConfig(path string) (Config, error)`; env-name consts `EnvIssue/EnvClass/EnvRepo/EnvTimeout/EnvBrief/EnvResult`; `var reservedEnvNames map[string]bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/config/config_test.go`:

```go
package config

import "testing"

// validYAML is a complete, valid config used across tests.
const validYAML = `
tracker:
  provider: linear
  team: AET
  token:          { command: ["op","read","op://work/linear-token"] }
  webhook-secret: { command: ["op","read","op://work/linear-webhook"] }
  poll-interval: 60s
  states:
    ready: Todo
    in-progress: In Progress
    in-review: In Review
    done: Done
    needs-input: Needs Input
    blocked: Backlog
repo:
  slug: aethons-tools/cove
secrets:
  - name: SOME_TOKEN
    command: ["op","read","op://work/x"]
classes:
  implement: { mode: autonomous, command: ["./dispatch/implement.sh"], timeout: 30m, concurrency: 2 }
  spec:      { mode: interactive }
concurrency: 4
reaper-timeout: 45m
`

func TestParseConfigValid(t *testing.T) {
	cfg, err := ParseConfig([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseConfig: unexpected error: %v", err)
	}
	if cfg.Tracker.Team != "AET" {
		t.Errorf("Team = %q; want AET", cfg.Tracker.Team)
	}
	if cfg.Tracker.States.Ready != "Todo" {
		t.Errorf("States.Ready = %q; want Todo", cfg.Tracker.States.Ready)
	}
	if cfg.Repo.Slug != "aethons-tools/cove" {
		t.Errorf("Repo.Slug = %q", cfg.Repo.Slug)
	}
	impl := cfg.Classes["implement"]
	if impl.Mode != "autonomous" || len(impl.Command) != 1 || impl.Timeout != "30m" || impl.Concurrency != 2 {
		t.Errorf("implement class parsed wrong: %+v", impl)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].Name != "SOME_TOKEN" {
		t.Errorf("secrets parsed wrong: %+v", cfg.Secrets)
	}
	if cfg.Concurrency != 4 {
		t.Errorf("Concurrency = %d; want 4", cfg.Concurrency)
	}
}

func TestParseConfigDefaultsClassLabelPrefix(t *testing.T) {
	cfg, err := ParseConfig([]byte(validYAML))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Tracker.ClassLabelPrefix != "class:" {
		t.Errorf("ClassLabelPrefix = %q; want default class:", cfg.Tracker.ClassLabelPrefix)
	}
}

func TestParseConfigRejectsUnknownKey(t *testing.T) {
	_, err := ParseConfig([]byte("repo:\n  slug: a/b\nbogus: 1\n"))
	if err == nil {
		t.Fatal("ParseConfig: expected error for unknown key, got nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/config/`
Expected: FAIL to build — `undefined: ParseConfig` (and the types).

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/config/config.go`:

```go
// Package config defines and loads the at-dispatch configuration: the tracker
// wiring, the repo, per-class dispatch commands, secrets, and the DISPATCH_*
// command contract. It is at-cove-agnostic — a class's command is the only seam.
package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the parsed contents of an at-dispatch config file.
type Config struct {
	Tracker       TrackerConfig    `yaml:"tracker"`
	Repo          RepoConfig       `yaml:"repo"`
	Secrets       []Secret         `yaml:"secrets"`
	Classes       map[string]Class `yaml:"classes"`
	Concurrency   int              `yaml:"concurrency"`
	ReaperTimeout string           `yaml:"reaper-timeout"`
}

// TrackerConfig wires at-dispatch to one tracker team.
type TrackerConfig struct {
	Provider         string    `yaml:"provider"`
	Team             string    `yaml:"team"`
	Token            SecretRef `yaml:"token"`
	WebhookSecret    SecretRef `yaml:"webhook-secret"`
	PollInterval     string    `yaml:"poll-interval"`
	States           StateMap  `yaml:"states"`
	ClassLabelPrefix string    `yaml:"class-label-prefix"`
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

// RepoConfig names the single repo this instance serves.
type RepoConfig struct {
	Slug string `yaml:"slug"`
}

// SecretRef is a resolver: Command's stdout is the value, produced in memory.
type SecretRef struct {
	Command []string `yaml:"command"`
}

// Secret is a named resolver injected as env into every dispatch command.
type Secret struct {
	Name    string   `yaml:"name"`
	Command []string `yaml:"command"`
}

// Class maps a handler class to how at-dispatch runs it.
type Class struct {
	Mode        string   `yaml:"mode"`    // "autonomous" | "interactive"
	Command     []string `yaml:"command"` // required iff autonomous
	Timeout     string   `yaml:"timeout"` // Go duration; autonomous
	Concurrency int      `yaml:"concurrency"`
}

const defaultClassLabelPrefix = "class:"

// Env var names at-dispatch sets for every dispatch command.
const (
	EnvIssue   = "DISPATCH_ISSUE"
	EnvClass   = "DISPATCH_CLASS"
	EnvRepo    = "DISPATCH_REPO"
	EnvTimeout = "DISPATCH_TIMEOUT"
	EnvBrief   = "DISPATCH_BRIEF"
	EnvResult  = "DISPATCH_RESULT"
)

// reservedEnvNames are the env names at-dispatch owns; a secret may not use one.
var reservedEnvNames = map[string]bool{
	EnvIssue: true, EnvClass: true, EnvRepo: true,
	EnvTimeout: true, EnvBrief: true, EnvResult: true,
}

// ParseConfig strict-decodes config bytes and applies defaults. Validation is
// applied here too once Validate exists (see Validate).
func ParseConfig(data []byte) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: %w", err)
	}
	cfg.applyDefaults()
	return cfg, nil
}

// LoadConfig reads a config file and parses it.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	return ParseConfig(data)
}

func (c *Config) applyDefaults() {
	if c.Tracker.ClassLabelPrefix == "" {
		c.Tracker.ClassLabelPrefix = defaultClassLabelPrefix
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/dispatch/config/`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/config/config.go internal/dispatch/config/config_test.go
git commit -m "feat(dispatch/config): config types + strict parse/load"
```

---

## Task 2: Validate() + wire into ParseConfig

Add the pure validator (spec §6) and call it from `ParseConfig`, so a malformed config is rejected with a clear message.

**Files:**
- Modify: `internal/dispatch/config/config.go` (add `Validate`; call it in `ParseConfig`)
- Test: `internal/dispatch/config/config_test.go` (add validation cases)

**Interfaces:**
- Consumes: types + `reservedEnvNames` from Task 1; `time` (stdlib), `strings` (stdlib).
- Produces: `func (c Config) Validate() error`; `ParseConfig` now returns validated configs.

- [ ] **Step 1: Write the failing test**

First change the import at the top of `internal/dispatch/config/config_test.go` from
`import "testing"` to:

```go
import (
	"strings"
	"testing"
)
```

Then append this function to the end of the file:

```go
func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string // substring expected in the error
	}{
		{"bad provider", strings.Replace(validYAML, "provider: linear", "provider: jira", 1), "provider"},
		{"missing team", strings.Replace(validYAML, "team: AET\n", "", 1), "team"},
		{"missing state role", strings.Replace(validYAML, "blocked: Backlog\n", "", 1), "states.blocked"},
		{"bad poll duration", strings.Replace(validYAML, "poll-interval: 60s", "poll-interval: soon", 1), "poll-interval"},
		{"bad repo slug", strings.Replace(validYAML, "slug: aethons-tools/cove", "slug: cove", 1), "repo.slug"},
		{"autonomous without command", strings.Replace(validYAML,
			`implement: { mode: autonomous, command: ["./dispatch/implement.sh"], timeout: 30m, concurrency: 2 }`,
			`implement: { mode: autonomous, timeout: 30m }`, 1), "command"},
		{"interactive with command", strings.Replace(validYAML,
			`spec:      { mode: interactive }`,
			`spec:      { mode: interactive, command: ["x"] }`, 1), "command"},
		{"bad mode", strings.Replace(validYAML,
			`spec:      { mode: interactive }`, `spec:      { mode: sideways }`, 1), "mode"},
		{"reserved secret name", strings.Replace(validYAML, "name: SOME_TOKEN", "name: DISPATCH_ISSUE", 1), "reserved"},
		{"global concurrency zero", strings.Replace(validYAML, "concurrency: 4", "concurrency: 0", 1), "concurrency"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}
```

(Note: keep the single `import "testing"` — merge the added `strings` import into one import block.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/config/ -run TestValidateRejects`
Expected: FAIL — configs currently parse without validation, so `err` is nil.

- [ ] **Step 3: Write the implementation**

In `internal/dispatch/config/config.go`, add `time` and `strings` to the import block:

```go
import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)
```

Call `Validate` at the end of `ParseConfig` (after `applyDefaults`):

```go
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
```

Add the validator:

```go
// Validate checks the config for internal consistency. It is pure (no I/O).
func (c Config) Validate() error {
	if c.Tracker.Provider != "linear" {
		return fmt.Errorf("config: tracker.provider must be \"linear\", got %q", c.Tracker.Provider)
	}
	if c.Tracker.Team == "" {
		return fmt.Errorf("config: tracker.team is required")
	}
	if len(c.Tracker.Token.Command) == 0 {
		return fmt.Errorf("config: tracker.token.command is required")
	}
	if len(c.Tracker.WebhookSecret.Command) == 0 {
		return fmt.Errorf("config: tracker.webhook-secret.command is required")
	}
	roles := map[string]string{
		"states.ready": c.Tracker.States.Ready, "states.in-progress": c.Tracker.States.InProgress,
		"states.in-review": c.Tracker.States.InReview, "states.done": c.Tracker.States.Done,
		"states.needs-input": c.Tracker.States.NeedsInput, "states.blocked": c.Tracker.States.Blocked,
	}
	for name, v := range roles {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("config: tracker.%s is required", name)
		}
	}
	if err := checkDuration("tracker.poll-interval", c.Tracker.PollInterval); err != nil {
		return err
	}
	if err := checkDuration("reaper-timeout", c.ReaperTimeout); err != nil {
		return err
	}
	if c.Tracker.ClassLabelPrefix == "" {
		return fmt.Errorf("config: tracker.class-label-prefix must not be empty")
	}
	if !strings.Contains(c.Repo.Slug, "/") || strings.HasPrefix(c.Repo.Slug, "/") || strings.HasSuffix(c.Repo.Slug, "/") {
		return fmt.Errorf("config: repo.slug must be \"owner/name\", got %q", c.Repo.Slug)
	}
	if c.Concurrency < 1 {
		return fmt.Errorf("config: concurrency must be >= 1, got %d", c.Concurrency)
	}
	seen := map[string]bool{}
	for i, s := range c.Secrets {
		if s.Name == "" {
			return fmt.Errorf("config: secrets[%d].name is required", i)
		}
		if reservedEnvNames[s.Name] {
			return fmt.Errorf("config: secrets[%d].name %q is a reserved DISPATCH_* name", i, s.Name)
		}
		if seen[s.Name] {
			return fmt.Errorf("config: secrets[%d].name %q is duplicated", i, s.Name)
		}
		seen[s.Name] = true
		if len(s.Command) == 0 {
			return fmt.Errorf("config: secrets[%d] (%s).command is required", i, s.Name)
		}
	}
	if len(c.Classes) == 0 {
		return fmt.Errorf("config: at least one class is required")
	}
	for name, cl := range c.Classes {
		if name == "" {
			return fmt.Errorf("config: a class name must not be empty")
		}
		switch cl.Mode {
		case "autonomous":
			if len(cl.Command) == 0 {
				return fmt.Errorf("config: classes[%q]: autonomous class requires a command", name)
			}
			if err := checkDuration(fmt.Sprintf("classes[%q].timeout", name), cl.Timeout); err != nil {
				return err
			}
		case "interactive":
			if len(cl.Command) != 0 {
				return fmt.Errorf("config: classes[%q]: interactive class must not set a command", name)
			}
		default:
			return fmt.Errorf("config: classes[%q].mode must be \"autonomous\" or \"interactive\", got %q", name, cl.Mode)
		}
		if cl.Concurrency < 0 {
			return fmt.Errorf("config: classes[%q].concurrency must be >= 0", name)
		}
	}
	return nil
}

func checkDuration(field, v string) error {
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fmt.Errorf("config: %s must be a positive Go duration (e.g. 30m), got %q", field, v)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/config/`
Expected: PASS (Task 1 tests + all `TestValidateRejects` subtests). The valid `validYAML` still parses (its `implement` timeout `30m`, durations, slug, concurrency all pass).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/config/config.go internal/dispatch/config/config_test.go
git commit -m "feat(dispatch/config): validation rules"
```

---

## Task 3: DISPATCH_* env builder + secret resolution

The command input contract: a `Task`, an injected secret resolver, and the env slice.

**Files:**
- Create: `internal/dispatch/config/env.go`
- Test: `internal/dispatch/config/env_test.go`

**Interfaces:**
- Consumes: env-name consts + `Secret` from Task 1.
- Produces: `type Task struct{ Issue, Class, Repo, Timeout, BriefPath, ResultPath string }`; `func ResolveSecrets(secrets []Secret, resolve func([]string) (string, error)) (map[string]string, error)`; `func BuildEnv(t Task, secrets map[string]string) []string`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/config/env_test.go`:

```go
package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestBuildEnv(t *testing.T) {
	task := Task{
		Issue: "AET-42", Class: "implement", Repo: "aethons-tools/cove",
		Timeout: "30m", BriefPath: "/tmp/brief.md", ResultPath: "/tmp/result.json",
	}
	got := BuildEnv(task, map[string]string{"B_TOKEN": "b", "A_TOKEN": "a"})
	want := []string{
		"DISPATCH_ISSUE=AET-42",
		"DISPATCH_CLASS=implement",
		"DISPATCH_REPO=aethons-tools/cove",
		"DISPATCH_TIMEOUT=30m",
		"DISPATCH_BRIEF=/tmp/brief.md",
		"DISPATCH_RESULT=/tmp/result.json",
		"A_TOKEN=a", // secrets sorted for determinism
		"B_TOKEN=b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildEnv =\n%v\nwant\n%v", got, want)
	}
}

func TestResolveSecrets(t *testing.T) {
	secrets := []Secret{{Name: "TOK", Command: []string{"echo", "x"}}}
	resolve := func(cmd []string) (string, error) {
		if len(cmd) == 2 && cmd[0] == "echo" {
			return "resolved", nil
		}
		return "", errors.New("unexpected cmd")
	}
	got, err := ResolveSecrets(secrets, resolve)
	if err != nil {
		t.Fatalf("ResolveSecrets: %v", err)
	}
	if got["TOK"] != "resolved" {
		t.Fatalf("TOK = %q; want resolved", got["TOK"])
	}
}

func TestResolveSecretsPropagatesError(t *testing.T) {
	secrets := []Secret{{Name: "TOK", Command: []string{"false"}}}
	resolve := func([]string) (string, error) { return "", errors.New("boom") }
	_, err := ResolveSecrets(secrets, resolve)
	if err == nil {
		t.Fatal("expected an error from ResolveSecrets")
	}
	if !strings.Contains(err.Error(), "TOK") {
		t.Fatalf("error %q should name the secret TOK", err.Error())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/config/ -run 'TestBuildEnv|TestResolveSecrets'`
Expected: FAIL to build — `undefined: Task`, `BuildEnv`, `ResolveSecrets`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/config/env.go`:

```go
package config

import (
	"fmt"
	"sort"
)

// Task is the per-dispatch context at-dispatch hands a class's command.
type Task struct {
	Issue      string // e.g. "AET-42"
	Class      string // e.g. "implement"
	Repo       string // e.g. "aethons-tools/cove"
	Timeout    string // the class timeout, e.g. "30m"
	BriefPath  string // absolute path to the markdown brief
	ResultPath string // absolute path the command must write result.json to
}

// ResolveSecrets runs each secret's resolver via the injected resolve func and
// returns name→value. Values are held in memory only. The resolver is injected so
// tests never spawn processes.
func ResolveSecrets(secrets []Secret, resolve func([]string) (string, error)) (map[string]string, error) {
	out := make(map[string]string, len(secrets))
	for _, s := range secrets {
		v, err := resolve(s.Command)
		if err != nil {
			return nil, fmt.Errorf("resolve secret %s: %w", s.Name, err)
		}
		out[s.Name] = v
	}
	return out, nil
}

// BuildEnv returns the environment for a dispatch command: the fixed DISPATCH_*
// entries followed by resolved secrets (sorted by name for determinism).
func BuildEnv(t Task, secrets map[string]string) []string {
	env := []string{
		EnvIssue + "=" + t.Issue,
		EnvClass + "=" + t.Class,
		EnvRepo + "=" + t.Repo,
		EnvTimeout + "=" + t.Timeout,
		EnvBrief + "=" + t.BriefPath,
		EnvResult + "=" + t.ResultPath,
	}
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		env = append(env, n+"="+secrets[n])
	}
	return env
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/config/`
Expected: PASS (all prior + env tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/config/env.go internal/dispatch/config/env_test.go
git commit -m "feat(dispatch/config): DISPATCH_* env builder + secret resolution"
```

---

## Task 4: result.json types + ReadResult

The command output contract: parse the `result.json` the command writes, with absent/invalid → `error`.

**Files:**
- Create: `internal/dispatch/config/result.go`
- Test: `internal/dispatch/config/result_test.go`

**Interfaces:**
- Produces: types `Result`, `Artifacts`, `NeedsInput`, `Usage`; status consts `StatusOK/StatusNeedsInput/StatusError`; `func ReadResult(path string) Result`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/config/result_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadResultOK(t *testing.T) {
	p := writeTemp(t, `{"status":"ok","artifacts":{"prUrl":"https://x/pr/1"},"summary":"done"}`)
	r := ReadResult(p)
	if r.Status != StatusOK {
		t.Fatalf("Status = %q; want ok", r.Status)
	}
	if r.Artifacts.PRURL != "https://x/pr/1" {
		t.Fatalf("PRURL = %q", r.Artifacts.PRURL)
	}
}

func TestReadResultNeedsInput(t *testing.T) {
	p := writeTemp(t, `{"status":"needs_input","needsInput":{"blocker":"ambiguous","need":"pick A or B"}}`)
	r := ReadResult(p)
	if r.Status != StatusNeedsInput {
		t.Fatalf("Status = %q; want needs_input", r.Status)
	}
	if r.NeedsInput == nil || r.NeedsInput.Need != "pick A or B" {
		t.Fatalf("NeedsInput parsed wrong: %+v", r.NeedsInput)
	}
}

func TestReadResultMissingFileIsError(t *testing.T) {
	r := ReadResult(filepath.Join(t.TempDir(), "nope.json"))
	if r.Status != StatusError {
		t.Fatalf("Status = %q; want error for missing file", r.Status)
	}
}

func TestReadResultMalformedIsError(t *testing.T) {
	p := writeTemp(t, `{not json`)
	if r := ReadResult(p); r.Status != StatusError {
		t.Fatalf("Status = %q; want error for malformed json", r.Status)
	}
}

func TestReadResultUnknownStatusIsError(t *testing.T) {
	p := writeTemp(t, `{"status":"weird"}`)
	if r := ReadResult(p); r.Status != StatusError {
		t.Fatalf("Status = %q; want error for unknown status", r.Status)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/config/ -run TestReadResult`
Expected: FAIL to build — `undefined: ReadResult`, `Result`, etc.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/config/result.go`:

```go
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// Result is the structured handoff a dispatch command writes to DISPATCH_RESULT.
type Result struct {
	Status     string      `json:"status"`
	Artifacts  Artifacts   `json:"artifacts"`
	NeedsInput *NeedsInput `json:"needsInput,omitempty"`
	Summary    string      `json:"summary"`
	Usage      Usage       `json:"usage"`
}

type Artifacts struct {
	Branch  string `json:"branch"`
	PRURL   string `json:"prUrl"`
	DocPath string `json:"docPath"`
}

type NeedsInput struct {
	Doing     string `json:"doing"`
	Blocker   string `json:"blocker"`
	Need      string `json:"need"`
	Tried     string `json:"tried"`
	SafeState string `json:"safeState"`
}

type Usage struct {
	Tokens int `json:"tokens"`
	WallMs int `json:"wallMs"`
}

// Result status values.
const (
	StatusOK         = "ok"
	StatusNeedsInput = "needs_input"
	StatusError      = "error"
)

// ReadResult reads and validates the result file at path. A present, valid file
// with a known status is authoritative; an absent, unparseable, or unknown-status
// result is reported as StatusError (with a diagnostic Summary). ReadResult never
// returns an error — the coarse outcome is always a Result.
func ReadResult(path string) Result {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Status: StatusError, Summary: fmt.Sprintf("no result file at %s: %v", path, err)}
	}
	var r Result
	if err := json.Unmarshal(data, &r); err != nil {
		return Result{Status: StatusError, Summary: fmt.Sprintf("unparseable result at %s: %v", path, err)}
	}
	switch r.Status {
	case StatusOK, StatusNeedsInput, StatusError:
		return r
	default:
		return Result{Status: StatusError, Summary: fmt.Sprintf("invalid status %q in result at %s", r.Status, path)}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/config/`
Expected: PASS (all tests in the package).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/config/result.go internal/dispatch/config/result_test.go
git commit -m "feat(dispatch/config): result.json types + ReadResult"
```

---

## Task 5: Wire `serve --config` into the binary

Make `at-dispatch serve --config <path>` load + validate a config and report. This supersedes the `dispatch.Serve` stub, which is removed.

**Files:**
- Modify (rewrite): `cmd/at-dispatch/main.go`
- Modify: `cmd/at-dispatch/main_test.go` (serve tests)
- Remove: `internal/dispatch/dispatch.go`, `internal/dispatch/dispatch_test.go`

**Interfaces:**
- Consumes: `config.LoadConfig` (Task 1).
- Produces: `serve --config <path>` behavior (exit 0 on valid config, 1 on load/validate error, 2 on missing `--config`).

- [ ] **Step 1: Write the failing test**

Replace the `serve` tests in `cmd/at-dispatch/main_test.go`. Remove `TestServeReportsNotImplemented` and add:

```go
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "at-dispatch.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodConfig = `
tracker:
  provider: linear
  team: AET
  token:          { command: ["true"] }
  webhook-secret: { command: ["true"] }
  poll-interval: 60s
  states: { ready: Todo, in-progress: In Progress, in-review: In Review, done: Done, needs-input: Needs Input, blocked: Backlog }
repo:
  slug: aethons-tools/cove
classes:
  implement: { mode: autonomous, command: ["./x.sh"], timeout: 30m }
concurrency: 1
reaper-timeout: 45m
`

func TestServeLoadsValidConfig(t *testing.T) {
	p := writeConfig(t, goodConfig)
	var out, errOut bytes.Buffer
	code := run([]string{"serve", "--config", p}, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (stderr: %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "aethons-tools/cove") || !strings.Contains(out.String(), "implement") {
		t.Fatalf("stdout = %q; want repo + class summary", out.String())
	}
}

func TestServeRejectsBadConfig(t *testing.T) {
	p := writeConfig(t, "repo:\n  slug: not-a-slug\n")
	var out, errOut bytes.Buffer
	code := run([]string{"serve", "--config", p}, &out, &errOut)
	if code != 1 {
		t.Fatalf("exit = %d; want 1", code)
	}
	if !strings.Contains(errOut.String(), "config:") {
		t.Fatalf("stderr = %q; want a config error", errOut.String())
	}
}

func TestServeRequiresConfig(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"serve"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "--config") {
		t.Fatalf("stderr = %q; want mention of --config", errOut.String())
	}
}
```

Add the needed imports to `main_test.go`'s import block: `"os"`, `"path/filepath"` (keep existing `"bytes"`, `"strings"`, `"testing"`).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/at-dispatch/`
Expected: FAIL to build/compile — `serve --config` not handled; helpers undefined; old `TestServeReportsNotImplemented` removed.

- [ ] **Step 3: Rewrite `cmd/at-dispatch/main.go`**

Replace the entire file with:

```go
// Command at-dispatch is the Linear-driven dispatcher that schedules work onto
// at-cove sandboxes. Today it loads and validates its config; the scheduler is
// not implemented yet — see docs/orchestration/.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/aethons-tools/cove/internal/dispatch/config"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

const usage = `at-dispatch — Linear-driven dispatcher for at-cove sandboxes (skeleton)

Usage:
  at-dispatch version                 print the build version
  at-dispatch serve --config <path>   load + validate the config (scheduler not implemented yet)

See docs/orchestration/ for the design.
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "serve":
		return doServe(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "at-dispatch: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func doServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the at-dispatch config file (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *cfgPath == "" {
		fmt.Fprintln(stderr, "at-dispatch serve: --config <path> is required")
		return 2
	}
	cfg, err := config.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "at-dispatch serve: %v\n", err)
		return 1
	}
	classes := make([]string, 0, len(cfg.Classes))
	for name := range cfg.Classes {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	fmt.Fprintf(stdout, "at-dispatch: config OK for %s — %d class(es): %s\n",
		cfg.Repo.Slug, len(classes), strings.Join(classes, ", "))
	fmt.Fprintln(stdout, "scheduler not implemented yet — see docs/orchestration/")
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 4: Remove the superseded stub**

```bash
git rm internal/dispatch/dispatch.go internal/dispatch/dispatch_test.go
```

(`internal/dispatch/doc.go` stays — it documents the package as the future scheduler home.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/at-dispatch/ ./internal/dispatch/...`
Expected: PASS — the three new serve tests + `TestVersionPrintsStampedValue` / `TestUnknownCommandPrintsUsage` / `TestNoArgsPrintsUsage`; the `internal/dispatch` package still builds (doc-only) and `internal/dispatch/config` passes.

Run: `go build ./cmd/... && go vet ./... && gofmt -l cmd/ internal/dispatch/`
Expected: builds; no vet errors; `gofmt -l` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-dispatch/main.go cmd/at-dispatch/main_test.go internal/dispatch/
git commit -m "feat(at-dispatch): serve --config loads and validates config"
```

---

## Final verification

- [ ] `go test ./...` — all packages pass.
- [ ] `just build` — both binaries build.
- [ ] `just run-dispatch version` prints the version; write a valid config and confirm `at-dispatch serve --config <file>` prints the summary and exits 0, and an invalid one exits 1.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3` (no new deps).
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/dispatch/` prints nothing.

## Notes

- **No repo-doc change required.** The design spec is the doc of record; `serve` is not yet user-facing runtime (still a config-check). `internal/dispatch` already appears in the OVERVIEW architecture map; the `config` subpackage does not need its own row.
- **`result.json` single source.** The `Result` shape here matches the worker contract in the orchestration dispatch-interface doc; when the worker is built, keep them identical (cross-link rather than fork).
