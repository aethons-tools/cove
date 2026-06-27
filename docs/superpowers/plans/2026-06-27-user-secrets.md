# User-Level Secrets File Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
Let a user supply a kit's secret values/commands from `~/.config/at-cove/secrets.yml`,
consulted only for kit-demanded secrets that have no `command`.

**Architecture:**
The kit's secret list is the demand; a new `internal/usersecret` package parses the supply file and owns the precedence (kit command wins, else file value/command, else unresolved).
`internal/secret.Spec` gains a literal-value path so a value can be injected without running a command.
`main.go`'s `doConnect` wires them and warns (stderr) for unresolved secrets.

**Tech Stack:**
Go (stdlib + `gopkg.in/yaml.v3`). Hermetic tests via `internal/runner.Fake`.

## Global Constraints

- Go toolchain lives at `/usr/local/go/bin` — prefix shell commands with `export PATH=$PATH:/usr/local/go/bin` (the session already exports `GOPATH=/home/agent/workspace/.gopath`, `GOPROXY=direct`, `GOSUMDB=off`).
- Only dependency is `gopkg.in/yaml.v3` (already vendored in the module cache). Add no new dependencies.
- All tests are hermetic: no Docker, network, or live SSH. Drive process execution through `internal/runner.Fake`.
- After each task: `go test ./...`, `go vet ./...`, and `gofmt -l` on changed files must be clean.
- Commit after each task. The repo appends `Co-Authored-By:` and `Claude-Session:` trailers to commit messages by convention.
- Do not change `config.yml`'s YAML keys; the only kit change is relaxing `command` from required to optional.

---

### Task 1: Relax the kit schema — `command` becomes optional

**Files:**
- Modify: `internal/kit/config.go` (remove the "command is required" validation; update the `Secret` doc comment)
- Test: `internal/kit/config_test.go` (drop the "secret no command" rejection case; add a name-only parse test)

**Interfaces:**
- Consumes: nothing new.
- Produces: `kit.ParseConfig` now accepts a secret with a `name` and no `command`. `kit.Secret` is unchanged in shape (`Name`, `Description`, `Command []string`).

- [ ] **Step 1: Write the failing test**

Add to `internal/kit/config_test.go`:

```go
func TestParseConfigAllowsCommandlessSecret(t *testing.T) {
	data := []byte("name: x\nbackend: colima\nsecrets:\n  - name: GITHUB_TOKEN\n")
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("name-only secret should be valid: %v", err)
	}
	if len(cfg.Secrets) != 1 || cfg.Secrets[0].Name != "GITHUB_TOKEN" || len(cfg.Secrets[0].Command) != 0 {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/kit/ -run TestParseConfigAllowsCommandlessSecret -v`
Expected: FAIL — error contains `secret "GITHUB_TOKEN": command is required`.

- [ ] **Step 3: Remove the validation check**

In `internal/kit/config.go`, delete the command check inside the `for` loop so it reads:

```go
	for i, s := range cfg.Secrets {
		if s.Name == "" {
			return Config{}, fmt.Errorf("config.yml: secrets[%d]: name is required", i)
		}
	}
```

Update the `Secret` doc comment to:

```go
// Secret declares an environment variable the sandbox needs. Command is
// optional: when omitted, the secret is a demand to be supplied by the user's
// ~/.config/at-cove/secrets.yml at connect time (or it warns and is left unset).
// When present, Command is the host argv that produces the value (trusted today,
// pre-.local).
type Secret struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Command     []string `yaml:"command"`
}
```

- [ ] **Step 4: Drop the now-invalid rejection case**

In `internal/kit/config_test.go`, in `TestParseConfigRejectsMissingFields`, delete this map entry (a name-only secret is now valid):

```go
		"secret no command": "name: x\nbackend: colima\nsecrets:\n  - name: T\n",
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/kit/ -v`
Expected: PASS (incl. `TestParseConfigAllowsCommandlessSecret`; `TestParseConfigRejectsMissingFields` still passes for the remaining cases).

- [ ] **Step 6: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): make secret command optional (demand without a value)"
```

---

### Task 2: Add a literal-value path to `secret.Spec`

**Files:**
- Modify: `internal/secret/secret.go` (add `Value`/`Literal` to `Spec`; honor them in `Resolve`)
- Test: `internal/secret/secret_test.go` (literal injects, runs no command)

**Interfaces:**
- Consumes: `runner.Runner` (unchanged).
- Produces: `secret.Spec{Name string; Command []string; Value string; Literal bool}`. `secret.Resolve(r, specs)` injects `Value` for literal specs and runs `Command` otherwise.

- [ ] **Step 1: Write the failing test**

Add to `internal/secret/secret_test.go`:

```go
func TestResolveLiteralValueRunsNoCommand(t *testing.T) {
	f := &runner.Fake{}
	env, err := Resolve(f, []Spec{{Name: "GITHUB_TOKEN", Value: "ghp_x", Literal: true}})
	if err != nil {
		t.Fatal(err)
	}
	if env["GITHUB_TOKEN"] != "ghp_x" {
		t.Fatalf("env = %v", env)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("a literal secret must not run a command; calls=%+v", f.Calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/secret/ -run TestResolveLiteralValueRunsNoCommand -v`
Expected: FAIL — compile error: `unknown field Value`/`Literal` in struct literal.

- [ ] **Step 3: Add the fields and the literal branch**

Replace the `Spec` type and `Resolve` body in `internal/secret/secret.go`:

```go
// Spec is a secret name and how to produce its value: either a literal Value
// (when Literal is set) or a host Command to run.
type Spec struct {
	Name    string
	Command []string
	Value   string
	Literal bool
}

// Resolve produces name->value for each spec. A literal spec contributes its
// Value directly (no command run); otherwise the spec's command is executed and
// its trimmed stdout used. Any command failure aborts with an error naming the
// secret; no partial map is returned.
func Resolve(r runner.Runner, specs []Spec) (map[string]string, error) {
	env := make(map[string]string, len(specs))
	for _, s := range specs {
		if s.Literal {
			env[s.Name] = s.Value
			continue
		}
		if len(s.Command) == 0 {
			return nil, fmt.Errorf("secret %q: empty command", s.Name)
		}
		out, err := r.Output(s.Command[0], s.Command[1:]...)
		if err != nil {
			return nil, fmt.Errorf("secret %q: resolver command failed: %w", s.Name, err)
		}
		env[s.Name] = strings.TrimSuffix(out, "\n")
	}
	return env, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/secret/ -v`
Expected: PASS (new test plus existing `TestResolveTrimsAndMaps`, `TestResolveFailsClosed`).

- [ ] **Step 5: Commit**

```bash
git add internal/secret/secret.go internal/secret/secret_test.go
git commit -m "feat(secret): support literal-value specs in Resolve"
```

---

### Task 3: New `internal/usersecret` package — `Load`

**Files:**
- Create: `internal/usersecret/usersecret.go` (`Entry`, `Store`, `Load`)
- Test: `internal/usersecret/usersecret_test.go`

**Interfaces:**
- Consumes: `gopkg.in/yaml.v3`.
- Produces: `usersecret.Entry{Value string; Command []string}`, `usersecret.Store map[string]Entry`, and `usersecret.Load(path string) (Store, error)`. Missing file → empty `Store`, nil error. Malformed → error naming the key.

- [ ] **Step 1: Write the failing tests**

Create `internal/usersecret/usersecret_test.go`:

```go
package usersecret

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secrets.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadStringIsValue(t *testing.T) {
	s, err := Load(write(t, "GITHUB_TOKEN: ghp_abc\n"))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := s["GITHUB_TOKEN"]
	if !ok || e.Value != "ghp_abc" || len(e.Command) != 0 {
		t.Fatalf("entry = %+v ok=%v", e, ok)
	}
}

func TestLoadNumericScalarIsStringValue(t *testing.T) {
	s, err := Load(write(t, "PIN: 1234\n"))
	if err != nil {
		t.Fatal(err)
	}
	if s["PIN"].Value != "1234" {
		t.Fatalf("entry = %+v", s["PIN"])
	}
}

func TestLoadArrayIsCommand(t *testing.T) {
	s, err := Load(write(t, `TOK: ["op", "read", "x"]`+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	e := s["TOK"]
	if e.Value != "" || len(e.Command) != 3 || e.Command[0] != "op" || e.Command[2] != "x" {
		t.Fatalf("entry = %+v", e)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if len(s) != 0 {
		t.Fatalf("store = %+v", s)
	}
}

func TestLoadInvalidYAMLErrors(t *testing.T) {
	if _, err := Load(write(t, "a: [unterminated\n")); err == nil {
		t.Fatal("expected error on invalid YAML")
	}
}

func TestLoadMappingValueErrors(t *testing.T) {
	_, err := Load(write(t, "GITHUB_TOKEN:\n  nested: bad\n"))
	if err == nil {
		t.Fatal("expected error on a mapping value")
	}
}

func TestLoadNonStringArrayElementErrors(t *testing.T) {
	_, err := Load(write(t, `TOK: ["op", 5]`+"\n"))
	if err == nil {
		t.Fatal("expected error on a non-string array element")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/usersecret/ -v`
Expected: FAIL — build error: no package / `Load` undefined.

- [ ] **Step 3: Implement `Load`**

Create `internal/usersecret/usersecret.go`:

```go
// Package usersecret loads the user-level secrets file
// (~/.config/at-cove/secrets.yml): a map of secret name to either a literal
// value (a YAML string) or a resolver command (a YAML string array). It is the
// "supply" side, consulted for kit-demanded secrets that have no command.
package usersecret

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Entry is one secret's supply: exactly one of Value or Command is set.
type Entry struct {
	Value   string   // literal value (the YAML scalar form)
	Command []string // resolver argv (the YAML string sequence)
}

// Store maps a secret name to its supply.
type Store map[string]Entry

// Load reads the secrets.yml at path. A missing file yields an empty Store and
// no error. A present-but-malformed file (invalid YAML, or a value that is
// neither a scalar nor a string sequence) is an error naming the offending key.
func Load(path string) (Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Store{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("secrets.yml: %w", err)
	}
	store := make(Store, len(doc))
	for name, node := range doc {
		switch node.Kind {
		case yaml.ScalarNode:
			store[name] = Entry{Value: node.Value}
		case yaml.SequenceNode:
			var cmd []string
			if err := node.Decode(&cmd); err != nil {
				return nil, fmt.Errorf("secrets.yml: secret %q: command must be a list of strings: %w", name, err)
			}
			store[name] = Entry{Command: cmd}
		default:
			return nil, fmt.Errorf("secrets.yml: secret %q: value must be a string or a list of strings", name)
		}
	}
	return store, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/usersecret/ -v`
Expected: PASS (all seven tests).

- [ ] **Step 5: Commit**

```bash
git add internal/usersecret/usersecret.go internal/usersecret/usersecret_test.go
git commit -m "feat(usersecret): parse ~/.config/at-cove/secrets.yml into a Store"
```

---

### Task 4: `Store.Plan` — precedence engine

**Files:**
- Create: `internal/usersecret/plan.go` (`Store.Plan`)
- Test: `internal/usersecret/plan_test.go`

**Interfaces:**
- Consumes: `usersecret.Store` (Task 3), `secret.Spec` (Task 2).
- Produces: `func (s Store) Plan(demanded []secret.Spec) (resolvable []secret.Spec, unresolved []string)`. Kit command wins; else store value→literal spec / store command→command spec; else the name is unresolved. Output is in demand order; undemanded store entries are ignored.

- [ ] **Step 1: Write the failing tests**

Create `internal/usersecret/plan_test.go`:

```go
package usersecret

import (
	"testing"

	"github.com/aethons-tools/cove/internal/secret"
)

func TestPlanKitCommandWins(t *testing.T) {
	store := Store{"T": {Value: "fromfile"}}
	got, unresolved := store.Plan([]secret.Spec{{Name: "T", Command: []string{"kit"}}})
	if len(unresolved) != 0 || len(got) != 1 {
		t.Fatalf("got=%+v unresolved=%v", got, unresolved)
	}
	if got[0].Literal || len(got[0].Command) != 1 || got[0].Command[0] != "kit" {
		t.Fatalf("kit command must win: %+v", got[0])
	}
}

func TestPlanStoreValueBecomesLiteral(t *testing.T) {
	store := Store{"T": {Value: "v"}}
	got, unresolved := store.Plan([]secret.Spec{{Name: "T"}})
	if len(unresolved) != 0 || len(got) != 1 {
		t.Fatalf("got=%+v unresolved=%v", got, unresolved)
	}
	if !got[0].Literal || got[0].Value != "v" || len(got[0].Command) != 0 {
		t.Fatalf("store value must become a literal spec: %+v", got[0])
	}
}

func TestPlanStoreCommand(t *testing.T) {
	store := Store{"T": {Command: []string{"op", "read"}}}
	got, _ := store.Plan([]secret.Spec{{Name: "T"}})
	if got[0].Literal || len(got[0].Command) != 2 {
		t.Fatalf("store command must become a command spec: %+v", got[0])
	}
}

func TestPlanUnresolved(t *testing.T) {
	got, unresolved := Store{}.Plan([]secret.Spec{{Name: "MISSING"}})
	if len(got) != 0 || len(unresolved) != 1 || unresolved[0] != "MISSING" {
		t.Fatalf("got=%+v unresolved=%v", got, unresolved)
	}
}

func TestPlanIgnoresUndemandedEntries(t *testing.T) {
	store := Store{"DEMANDED": {Value: "a"}, "EXTRA": {Value: "b"}}
	got, _ := store.Plan([]secret.Spec{{Name: "DEMANDED"}})
	if len(got) != 1 || got[0].Name != "DEMANDED" {
		t.Fatalf("undemanded entries must be ignored: %+v", got)
	}
}

func TestPlanPreservesDemandOrder(t *testing.T) {
	store := Store{"A": {Value: "1"}, "B": {Value: "2"}}
	got, _ := store.Plan([]secret.Spec{{Name: "B"}, {Name: "A"}})
	if len(got) != 2 || got[0].Name != "B" || got[1].Name != "A" {
		t.Fatalf("order not preserved: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/usersecret/ -run TestPlan -v`
Expected: FAIL — build error: `store.Plan` undefined.

- [ ] **Step 3: Implement `Plan`**

Create `internal/usersecret/plan.go`:

```go
package usersecret

import "github.com/aethons-tools/cove/internal/secret"

// Plan resolves each demanded secret to a runnable or literal Spec, applying the
// precedence: a kit-provided command wins; otherwise the store supplies a value
// (literal) or a command; otherwise the secret is unresolved. It returns the
// resolvable specs in demand order and the names of demanded secrets with no
// supply. Store entries whose names are not demanded are ignored.
func (s Store) Plan(demanded []secret.Spec) (resolvable []secret.Spec, unresolved []string) {
	for _, d := range demanded {
		if len(d.Command) > 0 {
			resolvable = append(resolvable, d)
			continue
		}
		e, ok := s[d.Name]
		if !ok {
			unresolved = append(unresolved, d.Name)
			continue
		}
		if len(e.Command) > 0 {
			resolvable = append(resolvable, secret.Spec{Name: d.Name, Command: e.Command})
		} else {
			resolvable = append(resolvable, secret.Spec{Name: d.Name, Value: e.Value, Literal: true})
		}
	}
	return resolvable, unresolved
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./internal/usersecret/ -v`
Expected: PASS (Load tests + all six Plan tests).

- [ ] **Step 5: Commit**

```bash
git add internal/usersecret/plan.go internal/usersecret/plan_test.go
git commit -m "feat(usersecret): Plan applies demand-vs-supply precedence"
```

---

### Task 5: Wire `secrets.yml` into `connect`

**Files:**
- Modify: `main.go` (`doConnect` signature gains `stderr`; load store, plan, warn, pass resolvable specs; its call site in `run`)
- Test: `main_test.go` (unresolved warning under dry-run; malformed file aborts)

**Interfaces:**
- Consumes: `usersecret.Load` + `Store.Plan` (Tasks 3–4), `secret.Spec` (Task 2), existing `configDir()`, `state.Load`, `connect.Connect`.
- Produces: `doConnect(kitDir string, r runner.Runner, dryRun, raw, noAuth bool, stdout, stderr io.Writer) error`.

- [ ] **Step 1: Keep the existing dry-run connect test hermetic**

`doConnect` will now read `configDir()/secrets.yml` on every path (incl. dry-run).
Guard the pre-existing `TestDryRunConnectRawNoAuth` against the real home dir by adding, right after its `kitDir := writeKit(t, dir)` line in `main_test.go`:

```go
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // hermetic: no real ~/.config/at-cove/secrets.yml
```

- [ ] **Step 2: Write the failing tests**

Add to `main_test.go`:

```go
func TestDryRunConnectWarnsUnresolvedSecret(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"}) // demanded, no command
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())                                   // empty config dir -> no secrets.yml
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "GITHUB_TOKEN") || !strings.Contains(errOut.String(), "will not be set") {
		t.Fatalf("expected unresolved warning on stderr; got %q", errOut.String())
	}
	if !strings.Contains(out.String(), "would resolve 0 secrets") {
		t.Fatalf("resolvable count should be 0; got %q", out.String())
	}
}

func TestConnectMalformedSecretsFileAborts(t *testing.T) {
	dir := t.TempDir()
	kitDir := writeKit(t, dir)
	writeState(t, kitDir, "colima", "box", state.Secret{Name: "GITHUB_TOKEN"})
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	coveCfg := filepath.Join(cfgHome, "at-cove")
	if err := os.MkdirAll(coveCfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coveCfg, "secrets.yml"), []byte("GITHUB_TOKEN:\n  nested: bad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "connect", kitDir}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 {
		t.Fatalf("malformed secrets.yml should abort; out=%q", out.String())
	}
	if !strings.Contains(errOut.String(), "GITHUB_TOKEN") {
		t.Fatalf("error should name the bad key; stderr=%q", errOut.String())
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `export PATH=$PATH:/usr/local/go/bin && go test . -run 'TestDryRunConnectWarnsUnresolvedSecret|TestConnectMalformedSecretsFileAborts' -v`
Expected: FAIL — `TestDryRunConnectWarnsUnresolvedSecret` fails on the missing warning (and reports `would resolve 1 secrets`); the malformed test fails because a malformed file is not yet read.

- [ ] **Step 4: Add the import**

In `main.go`, add to the import block:

```go
	"github.com/aethons-tools/cove/internal/usersecret"
```

- [ ] **Step 5: Update the `doConnect` call site in `run`**

In `main.go`, change the `connect` dispatch line to pass `stderr`:

```go
	case "connect":
		err = doConnect(kitDir, r, dryRun, raw, noAuth, stdout, stderr)
```

- [ ] **Step 6: Rewrite `doConnect`**

Replace the whole `doConnect` function in `main.go` with:

```go
// doConnect launches an interactive session in the sandbox, driven by the
// recorded state (not the kit). It resolves each demanded secret from its kit
// command or, failing that, the user's ~/.config/at-cove/secrets.yml; secrets
// with neither warn (non-fatal) and are left unset. It holds a SHARED lock on
// the state file for the whole session, so destroy can't tear the sandbox down
// underneath it. With raw it drops into bash instead of claude; with noAuth it
// skips `claude auth login`.
func doConnect(kitDir string, r runner.Runner, dryRun, raw, noAuth bool, stdout, stderr io.Writer) error {
	st, err := state.Load(kitDir)
	if err != nil {
		return err
	}

	// Demand (from state) resolved against supply (the user's secrets.yml).
	demanded := make([]secret.Spec, len(st.Secrets))
	for i, s := range st.Secrets {
		demanded[i] = secret.Spec{Name: s.Name, Command: s.Command}
	}
	secretsPath := filepath.Join(configDir(), "secrets.yml")
	store, err := usersecret.Load(secretsPath)
	if err != nil {
		return err
	}
	specs, unresolved := store.Plan(demanded)
	for _, name := range unresolved {
		fmt.Fprintf(stderr, "at-cove: warning: secret %q is demanded by the kit but has no command and no entry in %s; it will not be set\n", name, secretsPath)
	}

	launch := "claude"
	if raw {
		launch = "bash"
	}
	if dryRun {
		auth := "with auth"
		if noAuth {
			auth = "no auth"
		}
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s, launching %s (%s)\n",
			len(specs), st.Container, launch, auth)
		return nil
	}
	b, err := getBackend(st.Backend, r)
	if err != nil {
		return err
	}

	lock, err := state.AcquireShared(kitDir)
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			return fmt.Errorf("sandbox %q is being destroyed; try again shortly", st.Container)
		}
		return err
	}
	defer lock.Release()

	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	cmd := ""
	if raw {
		cmd = "bash"
	}
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd}, connect.Options{
		Container:     st.Container,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:      noAuth,
	})
}
```

Note: this removes the old in-body `specs := make(...)` loop (now built as `demanded` at the top). The `secret` import stays in use.

- [ ] **Step 7: Run the new tests to verify they pass**

Run: `export PATH=$PATH:/usr/local/go/bin && go test . -run 'TestDryRunConnectWarnsUnresolvedSecret|TestConnectMalformedSecretsFileAborts|TestDryRunConnectRawNoAuth' -v`
Expected: PASS (incl. the existing `TestDryRunConnectRawNoAuth`, whose commanded secret now reports `would resolve 1 secrets`).

- [ ] **Step 8: Run the full suite + vet**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./... && go vet ./...`
Expected: all packages PASS; vet clean.

- [ ] **Step 9: Commit**

```bash
git add main.go main_test.go
git commit -m "feat(connect): resolve demanded secrets from ~/.config/at-cove/secrets.yml"
```

---

### Task 6: Document the feature in `OVERVIEW.md`

**Files:**
- Modify: `docs/OVERVIEW.md` (extend the secrets docs with demand vs. supply)

**Interfaces:**
- Consumes: nothing (docs only).
- Produces: a new `### Supplying secret values` subsection.

- [ ] **Step 1: Insert the new subsection**

In `docs/OVERVIEW.md`, immediately after the security-caveat blockquote that ends with the line `> See the design spec.` (just before `## Command surface`), insert:

```markdown

### Supplying secret values — `~/.config/at-cove/secrets.yml`

A secret in `config.yml` may be declared with just a `name` (no `command`) — a
*demand* for that secret, to be supplied from the user-owned
`~/.config/at-cove/secrets.yml`. The kit's secret list is the authoritative
demand; the file is the supply, and is consulted **only** for demanded names
(entries it holds for other names are inert).

```yaml
# ~/.config/at-cove/secrets.yml
GITHUB_TOKEN: ghp_xxxxxxxxxxxxxxxxxxxx          # string  -> literal value
ANTHROPIC_API_KEY: ["pass", "show", "anthropic/api-key"]  # array -> resolver argv
```

Per demanded secret, precedence is: a `config.yml` `command` wins; otherwise a
string in `secrets.yml` is injected literally and an array is run as the
resolver command; otherwise the secret is **unresolved** and `connect` prints a
warning and leaves it unset. A missing file is fine (treated as empty); a
malformed file aborts `connect`.

> **Note:** literal values sit in plaintext on disk — keep the file `chmod 600`.
> Resolver *commands* (from either source) still produce values only in memory.

```

- [ ] **Step 2: Verify it renders and the anchors are intact**

Run: `grep -n "Supplying secret values\|## Command surface" docs/OVERVIEW.md`
Expected: the new subsection heading appears before `## Command surface`.

- [ ] **Step 3: Reformat prose with semantic line breaks**

Apply the `sembr-format` skill to the edited region (per the repo's Markdown convention), keeping code/YAML fences untouched.

- [ ] **Step 4: Commit**

```bash
git add docs/OVERVIEW.md
git commit -m "docs: document the user-level secrets.yml supply file"
```

---

### Task 7: Final verification

**Files:** none (verification only).

- [ ] **Step 1: Full hermetic suite**

Run: `export PATH=$PATH:/usr/local/go/bin && go test ./...`
Expected: all packages PASS (incl. `internal/usersecret`).

- [ ] **Step 2: Vet + gofmt**

Run: `export PATH=$PATH:/usr/local/go/bin && go vet ./... && gofmt -l main.go internal/`
Expected: vet clean; `gofmt -l` prints nothing (no files need formatting).

- [ ] **Step 3: Build**

Run: `export PATH=$PATH:/usr/local/go/bin && go build -o /tmp/at-cove . && echo OK`
Expected: `OK`.

- [ ] **Step 4: Dry-run smoke against the dogfood kit**

Run: `export PATH=$PATH:/usr/local/go/bin && go run . --dry-run connect .at-cove`
Expected: prints a `would resolve N secrets ...` line; if the dogfood kit demands a commandless secret with no `secrets.yml` entry, a `warning: secret "…" … will not be set` line appears on stderr. No commands execute.
