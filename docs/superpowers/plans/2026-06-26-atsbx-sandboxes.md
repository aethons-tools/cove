# atsbx Multi-Backend Sandboxes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:**
Evolve `atsbx` from an `sbx` wrapper
into a tool that runs hardened Claude Code sandboxes
from a discoverable `.atsbx/` kit directory across pluggable VM backends (Colima first),
with secrets resolved at `connect` time and injected over SSH.

**Architecture:**
A `Backend` interface abstracts provisioning and reaching a VM's `sshd`;
everything downstream of an SSH endpoint
(host-key TOFU, secret injection, launching `claude`)
is backend-agnostic.
`create` assembles a build context in `<kit>/.build/`
by stacking embedded overlays
(overridable defaults → kit `image-files/` → non-overridable hardening, last writer wins)
plus a managed public key;
the Colima backend turns that context into a Docker image and container.
`connect` resolves secrets via per-secret host commands and injects them memory-only over SSH.
Pure logic (config parsing, argv builders, assembly)
is separated from execution (a `Runner` that shells out)
so it is unit-testable without Docker/Colima.

**Tech Stack:**
Go standard library + `gopkg.in/yaml.v3`
(the single third-party dependency, for `config.yml`).
SSH via the system `ssh`/`ssh-keygen`;
containers via the system `docker` (provided by Colima).
No container/SSH Go libraries.

**Reference spec:** `docs/superpowers/specs/2026-06-26-atsbx-sandboxes-design.md`.

## Global Constraints

Every task's requirements implicitly include this section.

- Module path: `github.com/aethons-tools/at-sbx`; binary is `atsbx` (build with `go build -o atsbx .`).
- Go version floor: `go 1.22`.
- **Exactly one third-party dependency:** `gopkg.in/yaml.v3` (for `config.yml`). Everything else is standard library or shells out via `Runner`.
- **`agent-infrastructure/` is READ-ONLY. Do not modify any file under it.** Where its image files are needed (`Dockerfile`, `image-files/`, `nftables.conf`, `squid.conf`, `allowed_domains.txt`, `entrypoint.sh`), **copy them into the `sbx` repo** under `internal/assemble/{overridable,hardening}/` and modify the copies there.
- Agent launch command is **hardcoded** to `claude`.
- **All tests MUST pass without Docker, Colima, `sbx`, network, or a live VM** — drive execution through `runner.Fake`.
- **Fail closed on secrets:** any resolver command failing aborts `connect` before any SSH; never partially inject.
- Non-overridable hardening files are copied **verbatim** (no substitution); there is **no `envsubst`**.
- `--dry-run` is global (may appear before or after the subcommand); under it, **no** runner calls execute.
- On non-zero child exit, **propagate the same exit code**; stream child stdio live for interactive paths.
- Commit after every task. Run `gofmt -w` on changed files before each commit.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/runner/runner.go` | `Runner` interface + `OS`/`Fake`. Extended with `Output` (capture stdout) and `RunEnv` (stream stdio with extra env). |
| `internal/kit/config.go` | `Config`/`Secret` types; `ParseConfig` (unmarshal + validate). |
| `internal/kit/discover.go` | `Discover` (cwd walk-up to `.atsbx/`); `Load` (read+parse `config.yml`). |
| `internal/sshargs/sshargs.go` | Pure argv builders for the `ssh` client (`Target`, `Base`, `InteractiveSendEnv`). |
| `internal/secret/secret.go` | `Spec` type; `Resolve` (run each command via `Runner`, capture, trim, fail closed). |
| `internal/backend/backend.go` | `Backend` interface, `State`/`WorkspaceMode`/`WorkspaceMount`/`Endpoint`/`CreateContext`, factory registry. |
| `internal/backend/colima/colima.go` | Colima backend: `docker build`/`run`/`port`/`inspect`/`rm`. |
| `internal/assemble/assemble.go` | Layered `.build` assembly from `embed.FS` + managed-key injection. |
| `internal/assemble/overridable/…` | Embedded overridable defaults (copied from `agent-infrastructure`). |
| `internal/assemble/hardening/…` | Embedded non-overridable hardening + `Dockerfile` (copied from `agent-infrastructure`, adapted). |
| `internal/keys/keys.go` | Managed keypair: `Ensure` (generate on first use via `ssh-keygen`, return public key). |
| `internal/connect/transport.go` | `Transport` interface + `SendEnv` transport. |
| `internal/connect/connect.go` | `Connect` orchestration: resolve → status → dial → TOFU known_hosts → launch. |
| `main.go` | Subcommand dispatch (`build`/`create`/`connect`/`destroy`/`status`), flags, kit discovery, dry-run. |
| `internal/sbx/*`, `internal/kit/{build,create,template}.go` | **Removed** in the final task. |

**Dependency direction:** `kit`, `sshargs`, `secret`, `keys` are leaves (plus `runner`); `assemble` uses `embed.FS`+`runner`; `backend` is a leaf; `backend/colima` uses `backend`+`runner`; `connect` uses `secret`,`sshargs`,`backend`,`runner`,`keys`; `main` wires all and imports `colima` for its registration side effect. No cycles.

---

## Task 1: Extend the Runner seam

**Files:**
- Modify: `internal/runner/runner.go`
- Test: `internal/runner/runner_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `runner.Runner` interface adds `Output(name string, args ...string) (string, error)` and `RunEnv(extraEnv []string, name string, args ...string) error`.
  - `runner.Fake` adds `Outputs []FakeResult` (FIFO results for `Output`) and records `Call.Env`.
  - `type FakeResult struct { Stdout string; Err error }`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/runner/runner_test.go`:

```go
func TestOSOutputCaptures(t *testing.T) {
	out, err := OS{}.Output("sh", "-c", "printf hello")
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello" {
		t.Fatalf("Output = %q, want %q", out, "hello")
	}
}

func TestFakeOutputReturnsQueuedResults(t *testing.T) {
	f := &Fake{Outputs: []FakeResult{{Stdout: "one"}, {Stdout: "two"}}}
	a, _ := f.Output("docker", "port", "x")
	b, _ := f.Output("docker", "inspect", "x")
	if a != "one" || b != "two" {
		t.Fatalf("got %q,%q want one,two", a, b)
	}
	if len(f.Calls) != 2 || f.Calls[0].Name != "docker" {
		t.Fatalf("calls not recorded: %+v", f.Calls)
	}
}

func TestFakeRunEnvRecordsEnv(t *testing.T) {
	f := &Fake{}
	if err := f.RunEnv([]string{"K=V"}, "ssh", "host"); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || len(f.Calls[0].Env) != 1 || f.Calls[0].Env[0] != "K=V" {
		t.Fatalf("env not recorded: %+v", f.Calls)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runner/ -run 'Output|RunEnv' -v`
Expected: FAIL (compile error — `Output`/`RunEnv`/`FakeResult`/`Call.Env` undefined).

- [ ] **Step 3: Implement the extensions**

In `internal/runner/runner.go`, update the interface and types:

```go
// Runner executes external commands.
type Runner interface {
	Run(name string, args ...string) error
	// RunEnv is Run with extra "KEY=VALUE" entries appended to the child env.
	RunEnv(extraEnv []string, name string, args ...string) error
	// Output runs the command and returns its captured stdout.
	Output(name string, args ...string) (string, error)
}
```

Add the `OS` implementations:

```go
func (OS) RunEnv(extraEnv []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return err
}

func (OS) Output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), &ExitError{Code: ee.ExitCode(), Err: ee}
	}
	return string(out), err
}
```

Extend the fake (add `Env` to `Call`, add `FakeResult` and fields, implement methods):

```go
type Call struct {
	Name string
	Args []string
	Env  []string
}

// FakeResult is one queued result for Fake.Output.
type FakeResult struct {
	Stdout string
	Err    error
}

type Fake struct {
	Calls   []Call
	Err     error
	Outputs []FakeResult // consumed in order by Output
	out     int
}

func (f *Fake) Run(name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	return f.Err
}

func (f *Fake) RunEnv(extraEnv []string, name string, args ...string) error {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...), Env: append([]string(nil), extraEnv...)})
	return f.Err
}

func (f *Fake) Output(name string, args ...string) (string, error) {
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	if f.out < len(f.Outputs) {
		r := f.Outputs[f.out]
		f.out++
		return r.Stdout, r.Err
	}
	return "", f.Err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/runner/ -v`
Expected: PASS (including the pre-existing tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/runner/runner.go internal/runner/runner_test.go
git add internal/runner/
git commit -m "feat(runner): add Output and RunEnv to the execution seam"
```

---

## Task 2: Config types + parsing

**Files:**
- Create: `internal/kit/config.go`
- Test: `internal/kit/config_test.go`
- Modify: `go.mod`, `go.sum` (add `gopkg.in/yaml.v3`)

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Secret struct { Name string; Description string; Command []string }`
  - `type Config struct { Name string; Backend string; Secrets []Secret }`
  - `func ParseConfig(data []byte) (Config, error)`

- [ ] **Step 1: Add the YAML dependency**

Run:
```bash
go get gopkg.in/yaml.v3@v3.0.1
```
Expected: `go.mod` now requires `gopkg.in/yaml.v3 v3.0.1`.

- [ ] **Step 2: Write the failing test**

Create `internal/kit/config_test.go`:

```go
package kit

import "testing"

func TestParseConfigValid(t *testing.T) {
	data := []byte(`
name: claude-on-myrepo
backend: colima
secrets:
  - name: GITHUB_TOKEN
    command: ["op", "read", "x"]
  - name: ANTHROPIC_API_KEY
    description: Anthropic key
    command: ["pass", "show", "y"]
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "claude-on-myrepo" || cfg.Backend != "colima" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if len(cfg.Secrets) != 2 || cfg.Secrets[0].Name != "GITHUB_TOKEN" {
		t.Fatalf("secrets = %+v", cfg.Secrets)
	}
	if cfg.Secrets[1].Description != "Anthropic key" {
		t.Fatalf("description not parsed: %+v", cfg.Secrets[1])
	}
}

func TestParseConfigRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"no name":          "backend: colima\n",
		"no backend":       "name: x\n",
		"secret no name":   "name: x\nbackend: colima\nsecrets:\n  - command: [\"a\"]\n",
		"secret no command": "name: x\nbackend: colima\nsecrets:\n  - name: T\n",
	}
	for label, data := range cases {
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Errorf("%s: expected error, got nil", label)
		}
	}
}

func TestParseConfigRejectsUnknownField(t *testing.T) {
	if _, err := ParseConfig([]byte("name: x\nbackend: colima\nbogus: 1\n")); err == nil {
		t.Error("expected error on unknown field")
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/kit/ -run ParseConfig -v`
Expected: FAIL (`ParseConfig` undefined).

- [ ] **Step 4: Implement `config.go`**

Create `internal/kit/config.go`:

```go
package kit

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Secret declares an environment variable the sandbox needs and the host
// command that produces its value. Command is trusted today (pre-.local).
type Secret struct {
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Command     []string `yaml:"command"`
}

// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name    string   `yaml:"name"`
	Backend string   `yaml:"backend"`
	Secrets []Secret `yaml:"secrets"`
}

// ParseConfig unmarshals and validates config.yml bytes. Unknown fields are
// rejected to catch typos early.
func ParseConfig(data []byte) (Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config.yml: %w", err)
	}
	if cfg.Name == "" {
		return Config{}, fmt.Errorf("config.yml: name is required")
	}
	if cfg.Backend == "" {
		return Config{}, fmt.Errorf("config.yml: backend is required")
	}
	for i, s := range cfg.Secrets {
		if s.Name == "" {
			return Config{}, fmt.Errorf("config.yml: secrets[%d]: name is required", i)
		}
		if len(s.Command) == 0 {
			return Config{}, fmt.Errorf("config.yml: secret %q: command is required", s.Name)
		}
	}
	return cfg, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/kit/ -run ParseConfig -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/kit/config.go internal/kit/config_test.go
git add internal/kit/config.go internal/kit/config_test.go go.mod go.sum
git commit -m "feat(kit): config.yml types, parsing, and validation"
```

---

## Task 3: Kit discovery + load

**Files:**
- Create: `internal/kit/discover.go`
- Test: `internal/kit/discover_test.go`

**Interfaces:**
- Consumes: `ParseConfig` (Task 2).
- Produces:
  - `func Discover(start string) (kitDir string, err error)` — walk up from `start` to the nearest `.atsbx/` directory; returns its path.
  - `func Load(kitDir string) (Config, error)` — read `<kitDir>/config.yml` and `ParseConfig` it.

- [ ] **Step 1: Write the failing test**

Create `internal/kit/discover_test.go`:

```go
package kit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverWalksUp(t *testing.T) {
	root := t.TempDir()
	atsbx := filepath.Join(root, ".atsbx")
	if err := os.MkdirAll(atsbx, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != atsbx {
		t.Fatalf("Discover = %q, want %q", got, atsbx)
	}
}

func TestDiscoverNotFound(t *testing.T) {
	if _, err := Discover(t.TempDir()); err == nil {
		t.Error("expected error when no .atsbx exists")
	}
}

func TestLoadReadsConfig(t *testing.T) {
	kitDir := t.TempDir()
	yml := "name: x\nbackend: colima\n"
	if err := os.WriteFile(filepath.Join(kitDir, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(kitDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "x" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/kit/ -run 'Discover|Load' -v`
Expected: FAIL (`Discover`/`Load` undefined).

- [ ] **Step 3: Implement `discover.go`**

Create `internal/kit/discover.go`:

```go
package kit

import (
	"fmt"
	"os"
	"path/filepath"
)

// Discover walks up from start to the nearest directory containing a .atsbx/
// child, returning the path to that .atsbx directory.
func Discover(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		cand := filepath.Join(dir, ".atsbx")
		if info, err := os.Stat(cand); err == nil && info.IsDir() {
			return cand, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no .atsbx/ found in %s or any parent", start)
		}
		dir = parent
	}
}

// Load reads and parses <kitDir>/config.yml.
func Load(kitDir string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(kitDir, "config.yml"))
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(data)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/kit/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/kit/discover.go internal/kit/discover_test.go
git add internal/kit/discover.go internal/kit/discover_test.go
git commit -m "feat(kit): .atsbx discovery (cwd walk-up) and config load"
```

---

## Task 4: SSH argv builders

**Files:**
- Create: `internal/sshargs/sshargs.go`
- Test: `internal/sshargs/sshargs_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Target struct { Host, User string; Port int; IdentityFile, KnownHostsFile string }`
  - `func Base(t Target) []string` — common `ssh` options + `user@host`.
  - `func InteractiveSendEnv(t Target, envNames []string, remoteCmd string) []string` — `-tt`, one `-o SendEnv=NAME` per name, base options, `user@host`, then `remoteCmd`.

- [ ] **Step 1: Write the failing test**

Create `internal/sshargs/sshargs_test.go`:

```go
package sshargs

import (
	"reflect"
	"testing"
)

func target() Target {
	return Target{Host: "127.0.0.1", User: "agent", Port: 49153,
		IdentityFile: "/k/id", KnownHostsFile: "/k/kh"}
}

func TestBase(t *testing.T) {
	got := Base(target())
	want := []string{
		"-i", "/k/id",
		"-p", "49153",
		"-o", "UserKnownHostsFile=/k/kh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"agent@127.0.0.1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Base = %v\nwant %v", got, want)
	}
}

func TestInteractiveSendEnv(t *testing.T) {
	got := InteractiveSendEnv(target(), []string{"GITHUB_TOKEN", "X"}, "exec claude")
	want := []string{
		"-tt",
		"-o", "SendEnv=GITHUB_TOKEN",
		"-o", "SendEnv=X",
		"-i", "/k/id",
		"-p", "49153",
		"-o", "UserKnownHostsFile=/k/kh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"agent@127.0.0.1",
		"exec claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InteractiveSendEnv = %v\nwant %v", got, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/sshargs/ -v`
Expected: FAIL (package/functions undefined).

- [ ] **Step 3: Implement `sshargs.go`**

Create `internal/sshargs/sshargs.go`:

```go
// Package sshargs builds argv (after the "ssh" binary name) for the ssh
// client. Pure: no I/O.
package sshargs

import "strconv"

// Target identifies a VM's sshd and the local credentials to reach it.
type Target struct {
	Host           string
	User           string
	Port           int
	IdentityFile   string
	KnownHostsFile string
}

// Base returns the common ssh options ending in user@host.
func Base(t Target) []string {
	return []string{
		"-i", t.IdentityFile,
		"-p", strconv.Itoa(t.Port),
		"-o", "UserKnownHostsFile=" + t.KnownHostsFile,
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		t.User + "@" + t.Host,
	}
}

// InteractiveSendEnv builds an interactive (pty) ssh argv that forwards the
// named environment variables and runs remoteCmd.
func InteractiveSendEnv(t Target, envNames []string, remoteCmd string) []string {
	args := []string{"-tt"}
	for _, n := range envNames {
		args = append(args, "-o", "SendEnv="+n)
	}
	args = append(args, Base(t)...)
	if remoteCmd != "" {
		args = append(args, remoteCmd)
	}
	return args
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/sshargs/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/sshargs/sshargs.go internal/sshargs/sshargs_test.go
git add internal/sshargs/
git commit -m "feat(sshargs): pure ssh-client argv builders"
```

---

## Task 5: Secret resolver

**Files:**
- Create: `internal/secret/secret.go`
- Test: `internal/secret/secret_test.go`

**Interfaces:**
- Consumes: `runner.Runner` (Task 1).
- Produces:
  - `type Spec struct { Name string; Command []string }`
  - `func Resolve(r runner.Runner, specs []Spec) (map[string]string, error)` — runs each command via `r.Output`, trims one trailing newline, fails closed naming the secret.

- [ ] **Step 1: Write the failing test**

Create `internal/secret/secret_test.go`:

```go
package secret

import (
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func TestResolveTrimsAndMaps(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "key"}}}
	env, err := Resolve(f, []Spec{
		{Name: "GITHUB_TOKEN", Command: []string{"op", "read", "x"}},
		{Name: "ANTHROPIC_API_KEY", Command: []string{"pass", "y"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if env["GITHUB_TOKEN"] != "tok" || env["ANTHROPIC_API_KEY"] != "key" {
		t.Fatalf("env = %v", env)
	}
	if f.Calls[0].Name != "op" || f.Calls[0].Args[0] != "read" {
		t.Fatalf("call0 = %+v", f.Calls[0])
	}
}

func TestResolveFailsClosed(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 1}}}}
	_, err := Resolve(f, []Spec{{Name: "GITHUB_TOKEN", Command: []string{"op", "read", "x"}}})
	if err == nil {
		t.Fatal("expected error when a resolver command fails")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/secret/ -v`
Expected: FAIL (package/functions undefined).

- [ ] **Step 3: Implement `secret.go`**

Create `internal/secret/secret.go`:

```go
// Package secret resolves declared secrets by running a host command per
// secret and capturing its stdout. Fails closed.
package secret

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

// Spec is a secret name and the argv that produces its value.
type Spec struct {
	Name    string
	Command []string
}

// Resolve runs each spec's command and returns name->value. Any failure aborts
// with an error naming the secret; no partial map is returned.
func Resolve(r runner.Runner, specs []Spec) (map[string]string, error) {
	env := make(map[string]string, len(specs))
	for _, s := range specs {
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

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/secret/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/secret/secret.go internal/secret/secret_test.go
git add internal/secret/
git commit -m "feat(secret): just-in-time resolver, fail-closed"
```

---

## Task 6: Backend interface + registry

**Files:**
- Create: `internal/backend/backend.go`
- Test: `internal/backend/backend_test.go`

**Interfaces:**
- Consumes: `runner.Runner` (Task 1).
- Produces:
  - `type State int` with `StateAbsent`, `StateStopped`, `StateRunning`.
  - `type WorkspaceMode int` with `Isolated`, `Shared`.
  - `type WorkspaceMount struct { Mode WorkspaceMode; HostPath string }`
  - `type Endpoint struct { Host string; Port int; User string }`
  - `type CreateContext struct { Name string; BuildDir string; Workspace WorkspaceMount }`
  - `type Backend interface { Create(CreateContext) error; Dial(name string) (Endpoint, func(), error); Destroy(name string) error; GetStatus(name string) (State, error) }`
  - `type Factory func(r runner.Runner) Backend`
  - `func Register(name string, f Factory)`; `func Get(name string) (Factory, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/backend/backend_test.go`:

```go
package backend

import (
	"strings"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

type stub struct{}

func (stub) Create(CreateContext) error                 { return nil }
func (stub) Dial(string) (Endpoint, func(), error)      { return Endpoint{}, func() {}, nil }
func (stub) Destroy(string) error                       { return nil }
func (stub) GetStatus(string) (State, error)            { return StateAbsent, nil }

func TestRegistryGetKnown(t *testing.T) {
	Register("stub", func(runner.Runner) Backend { return stub{} })
	f, err := Get("stub")
	if err != nil {
		t.Fatal(err)
	}
	if f(&runner.Fake{}) == nil {
		t.Fatal("factory produced nil backend")
	}
}

func TestRegistryGetUnknownListsSupported(t *testing.T) {
	Register("stub", func(runner.Runner) Backend { return stub{} })
	_, err := Get("nope")
	if err == nil || !strings.Contains(err.Error(), "stub") {
		t.Fatalf("error should list supported backends, got %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backend/ -v`
Expected: FAIL (types/functions undefined).

- [ ] **Step 3: Implement `backend.go`**

Create `internal/backend/backend.go`:

```go
// Package backend defines the VM-backend abstraction: provision a VM from an
// assembled build context, reach its sshd, query state, destroy it.
package backend

import (
	"fmt"
	"sort"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

type State int

const (
	StateAbsent State = iota
	StateStopped
	StateRunning
)

type WorkspaceMode int

const (
	Isolated WorkspaceMode = iota
	Shared
)

// WorkspaceMount expresses how the workspace is realized. HostPath is set iff
// Mode == Shared.
type WorkspaceMount struct {
	Mode     WorkspaceMode
	HostPath string
}

// Endpoint is a reachable sshd address.
type Endpoint struct {
	Host string
	Port int
	User string
}

// CreateContext is everything a backend needs to provision a VM.
type CreateContext struct {
	Name      string
	BuildDir  string
	Workspace WorkspaceMount
}

// Backend provisions and manages VMs of one technology.
type Backend interface {
	Create(ctx CreateContext) error
	Dial(name string) (Endpoint, func(), error)
	Destroy(name string) error
	GetStatus(name string) (State, error)
}

// Factory constructs a Backend bound to a Runner.
type Factory func(r runner.Runner) Backend

var registry = map[string]Factory{}

// Register adds a backend factory under name (called from backend init()s).
func Register(name string, f Factory) { registry[name] = f }

// Get returns the factory for name, or an error listing supported backends.
func Get(name string) (Factory, error) {
	if f, ok := registry[name]; ok {
		return f, nil
	}
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("unknown backend %q; supported: %s", name, strings.Join(names, ", "))
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backend/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/backend/backend.go internal/backend/backend_test.go
git add internal/backend/backend.go internal/backend/backend_test.go
git commit -m "feat(backend): Backend interface and factory registry"
```

---

## Task 7: Copy + embed the image assets

This task copies the hardened image files from the **read-only** `agent-infrastructure` repo into `sbx`, split into the overridable and non-overridable trees, and wires `embed.FS`. No behavior yet — the next task consumes these.

**Files (create under `sbx`; sources are in `../agent-infrastructure`, do not modify there):**
- Create: `internal/assemble/hardening/Dockerfile`
- Create: `internal/assemble/hardening/image-files/etc/nftables.conf`
- Create: `internal/assemble/hardening/image-files/etc/squid/squid.conf`
- Create: `internal/assemble/hardening/image-files/etc/squid/allowed_domains.txt`
- Create: `internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh`
- Create: `internal/assemble/hardening/image-files/etc/ssh/sshd_config.d/atsbx.conf`
- Create: `internal/assemble/overridable/image-files/home/agent/.init-agent-data/CLAUDE.md` (and `settings.json`, `.claude.json`, `skills/…` as present)
- Create: `internal/assemble/embed.go`
- Test: `internal/assemble/embed_test.go`

- [ ] **Step 1: Copy the hardening files (verbatim, then adapt the Dockerfile COPY path)**

```bash
cd /home/agent/workspace/sbx
SRC=../agent-infrastructure/claude-code-oci
mkdir -p internal/assemble/hardening/image-files/etc/squid \
         internal/assemble/hardening/image-files/usr/local/bin \
         internal/assemble/hardening/image-files/etc/ssh/sshd_config.d
cp "$SRC/context/Dockerfile"                       internal/assemble/hardening/Dockerfile
cp "$SRC/image-files/etc/nftables.conf"            internal/assemble/hardening/image-files/etc/nftables.conf
cp "$SRC/image-files/etc/squid/squid.conf"         internal/assemble/hardening/image-files/etc/squid/squid.conf
cp "$SRC/image-files/etc/squid/allowed_domains.txt" internal/assemble/hardening/image-files/etc/squid/allowed_domains.txt
cp "$SRC/image-files/usr/local/bin/entrypoint.sh"  internal/assemble/hardening/image-files/usr/local/bin/entrypoint.sh
```

Edit `internal/assemble/hardening/Dockerfile`: change the build-context COPY line
`COPY .build/image-files/. /.` to `COPY image-files/. /.` (the assembled context
puts the overlay at `<buildDir>/image-files`).

- [ ] **Step 2: Add the sshd AcceptEnv drop-in (enables the SendEnv transport)**

Create `internal/assemble/hardening/image-files/etc/ssh/sshd_config.d/atsbx.conf`:

```
# Accept any client-forwarded env var (the in-VM agent is not a threat in this
# model; values still never touch disk). Required by the SendEnv transport.
AcceptEnv *
```

- [ ] **Step 3: Copy the overridable defaults**

```bash
cd /home/agent/workspace/sbx
SRC=../agent-infrastructure/claude-code-oci/image-files/home/agent/.init-agent-data
DST=internal/assemble/overridable/image-files/home/agent/.init-agent-data
mkdir -p "$DST"
cp -a "$SRC/." "$DST/"
```

- [ ] **Step 4: Write the failing embed test**

Create `internal/assemble/embed_test.go`:

```go
package assemble

import (
	"io/fs"
	"testing"
)

func TestEmbedsContainKeyFiles(t *testing.T) {
	for _, p := range []string{
		"hardening/Dockerfile",
		"hardening/image-files/etc/nftables.conf",
		"hardening/image-files/etc/squid/squid.conf",
		"hardening/image-files/etc/ssh/sshd_config.d/atsbx.conf",
	} {
		if _, err := fs.Stat(hardeningFS, p); err != nil {
			t.Errorf("hardeningFS missing %s: %v", p, err)
		}
	}
	if _, err := fs.Stat(overridableFS, "overridable/image-files/home/agent/.init-agent-data/CLAUDE.md"); err != nil {
		t.Errorf("overridableFS missing CLAUDE.md: %v", err)
	}
}
```

- [ ] **Step 5: Run the test to verify it fails**

Run: `go test ./internal/assemble/ -v`
Expected: FAIL (`hardeningFS`/`overridableFS` undefined).

- [ ] **Step 6: Wire `embed.FS`**

Create `internal/assemble/embed.go`:

```go
package assemble

import "embed"

// hardeningFS holds the non-overridable layer (Dockerfile + image-files).
// "all:" includes dotfiles.
//
//go:embed all:hardening
var hardeningFS embed.FS

// overridableFS holds the overridable defaults layer.
//
//go:embed all:overridable
var overridableFS embed.FS
```

- [ ] **Step 7: Run the test to verify it passes**

Run: `go test ./internal/assemble/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/assemble/embed.go internal/assemble/embed_test.go
git add internal/assemble/
git commit -m "feat(assemble): vendor + embed hardened image layers from agent-infrastructure"
```

---

## Task 8: Managed keypair

**Files:**
- Create: `internal/keys/keys.go`
- Test: `internal/keys/keys_test.go`

**Interfaces:**
- Consumes: `runner.Runner` (Task 1).
- Produces:
  - `func Ensure(r runner.Runner, dir string) (privPath string, pub []byte, err error)` — if `<dir>/id_ed25519` is absent, generate it with `ssh-keygen`; return the private key path and the public key bytes.

- [ ] **Step 1: Write the failing test**

Create `internal/keys/keys_test.go`:

```go
package keys

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func TestEnsureUsesExistingKey(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_ed25519")
	os.WriteFile(priv, []byte("PRIV"), 0o600)
	os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA test\n"), 0o644)

	f := &runner.Fake{}
	gotPriv, pub, err := Ensure(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	if gotPriv != priv {
		t.Fatalf("priv = %q", gotPriv)
	}
	if string(pub) != "ssh-ed25519 AAAA test\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("should not call ssh-keygen when key exists: %+v", f.Calls)
	}
}

func TestEnsureGeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id_ed25519")
	// Fake ssh-keygen by pre-creating the .pub the way keygen would, but assert
	// the call is made. Resolve uses Output? No — Run. So simulate via Run.
	f := &runner.Fake{}
	// Pre-place the .pub so Ensure's post-generate read succeeds.
	os.WriteFile(priv+".pub", []byte("ssh-ed25519 AAAA gen\n"), 0o644)

	_, pub, err := Ensure(f, dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub) != "ssh-ed25519 AAAA gen\n" {
		t.Fatalf("pub = %q", pub)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh-keygen" {
		t.Fatalf("expected ssh-keygen call, got %+v", f.Calls)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/keys/ -v`
Expected: FAIL (`Ensure` undefined).

- [ ] **Step 3: Implement `keys.go`**

Create `internal/keys/keys.go`:

```go
// Package keys manages atsbx's dedicated SSH keypair.
package keys

import (
	"os"
	"path/filepath"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

// Ensure returns the path to <dir>/id_ed25519 and its public key bytes,
// generating the keypair with ssh-keygen on first use.
func Ensure(r runner.Runner, dir string) (string, []byte, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	priv := filepath.Join(dir, "id_ed25519")
	if _, err := os.Stat(priv); os.IsNotExist(err) {
		if err := r.Run("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "atsbx", "-f", priv); err != nil {
			return "", nil, err
		}
	} else if err != nil {
		return "", nil, err
	}
	pub, err := os.ReadFile(priv + ".pub")
	if err != nil {
		return "", nil, err
	}
	return priv, pub, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/keys/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/keys/keys.go internal/keys/keys_test.go
git add internal/keys/
git commit -m "feat(keys): managed ed25519 keypair, generated on first use"
```

---

## Task 9: Build-context assembly

**Files:**
- Create: `internal/assemble/assemble.go`
- Test: `internal/assemble/assemble_test.go`

**Interfaces:**
- Consumes: `overridableFS`, `hardeningFS` (Task 7).
- Produces:
  - `func Assemble(kitDir, buildDir string, pub []byte) error` — wipe `buildDir`; copy overridable `image-files` → kit `image-files/` → hardening (`Dockerfile` + `image-files`), last writer wins; then write `pub` to `<buildDir>/image-files/home/agent/.ssh/authorized_keys` (mode 0600).

- [ ] **Step 1: Write the failing test**

Create `internal/assemble/assemble_test.go`:

```go
package assemble

import (
	"os"
	"path/filepath"
	"testing"
)

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAssembleLayersAndKey(t *testing.T) {
	kitDir := t.TempDir()
	buildDir := filepath.Join(t.TempDir(), ".build")

	// Local override: a benign file, plus an attempt to shadow a hardening path.
	mustWrite(t, filepath.Join(kitDir, "image-files/home/agent/note.txt"), "local")
	mustWrite(t, filepath.Join(kitDir, "image-files/etc/nftables.conf"), "PWNED")

	if err := Assemble(kitDir, buildDir, []byte("ssh-ed25519 AAAA k\n")); err != nil {
		t.Fatal(err)
	}

	// Dockerfile present (from hardening).
	if _, err := os.Stat(filepath.Join(buildDir, "Dockerfile")); err != nil {
		t.Fatalf("Dockerfile missing: %v", err)
	}
	// Local non-conflicting file survives.
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/note.txt")); got != "local" {
		t.Fatalf("note = %q", got)
	}
	// Hardening wins over the local shadow attempt.
	if got := read(t, filepath.Join(buildDir, "image-files/etc/nftables.conf")); got == "PWNED" {
		t.Fatal("local file overrode hardening — security boundary breached")
	}
	// Managed key injected.
	if got := read(t, filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")); got != "ssh-ed25519 AAAA k\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/assemble/ -run Assemble -v`
Expected: FAIL (`Assemble` undefined).

- [ ] **Step 3: Implement `assemble.go`**

Create `internal/assemble/assemble.go`:

```go
package assemble

import (
	"io/fs"
	"os"
	"path/filepath"
)

// Assemble builds the context in buildDir from the layered overlays (last
// writer wins) and injects the managed public key.
func Assemble(kitDir, buildDir string, pub []byte) error {
	if err := os.RemoveAll(buildDir); err != nil {
		return err
	}
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}

	// Layer 1: overridable defaults (strip the "overridable/" prefix).
	if err := copyEmbed(overridableFS, "overridable", buildDir); err != nil {
		return err
	}
	// Layer 2: kit's local image-files (if present).
	localIF := filepath.Join(kitDir, "image-files")
	if _, err := os.Stat(localIF); err == nil {
		if err := copyTree(localIF, filepath.Join(buildDir, "image-files")); err != nil {
			return err
		}
	}
	// Layer 3 (deferred): .local/image-files — intentionally not applied yet.
	// Layer 4: non-overridable hardening (Dockerfile + image-files), wins.
	if err := copyEmbed(hardeningFS, "hardening", buildDir); err != nil {
		return err
	}

	// Managed key injection.
	ak := filepath.Join(buildDir, "image-files/home/agent/.ssh/authorized_keys")
	if err := os.MkdirAll(filepath.Dir(ak), 0o700); err != nil {
		return err
	}
	return os.WriteFile(ak, pub, 0o600)
}

// copyEmbed copies efs under root into dst, stripping the root prefix.
func copyEmbed(efs fs.FS, root, dst string) error {
	return fs.WalkDir(efs, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(efs, p)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if filepath.Ext(p) == ".sh" || filepath.Base(p) == "entrypoint.sh" {
			mode = 0o755
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, mode)
	})
}

// copyTree copies a real directory tree from src to dst, preserving modes.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/assemble/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/assemble/assemble.go internal/assemble/assemble_test.go
git add internal/assemble/assemble.go internal/assemble/assemble_test.go
git commit -m "feat(assemble): layered build-context assembly + key injection"
```

---

## Task 10: Colima backend

**Files:**
- Create: `internal/backend/colima/colima.go`
- Test: `internal/backend/colima/colima_test.go`

**Interfaces:**
- Consumes: `backend` (Task 6), `runner.Runner` (Task 1).
- Produces:
  - `func New(r runner.Runner) backend.Backend`
  - Registers itself as `"colima"` via `init()`.
  - `Create`/`Dial`/`Destroy`/`GetStatus` driving `docker`.

- [ ] **Step 1: Write the failing test**

Create `internal/backend/colima/colima_test.go`:

```go
package colima

import (
	"testing"

	"github.com/aethons-tools/at-sbx/internal/backend"
	"github.com/aethons-tools/at-sbx/internal/runner"
)

func TestCreateIsolated(t *testing.T) {
	f := &runner.Fake{}
	b := New(f)
	err := b.Create(backend.CreateContext{
		Name: "box", BuildDir: "/b",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Calls[0].Name != "docker" || f.Calls[0].Args[0] != "build" {
		t.Fatalf("build call = %+v", f.Calls[0])
	}
	run := f.Calls[1].Args
	if !contains(run, "-v") || !contains(run, "box-workspace:/home/agent/workspace") {
		t.Fatalf("isolated workspace volume missing: %v", run)
	}
	if !contains(run, "box-state:/agent-data") {
		t.Fatalf("state volume missing: %v", run)
	}
}

func TestCreateShared(t *testing.T) {
	f := &runner.Fake{}
	New(f).Create(backend.CreateContext{
		Name: "box", BuildDir: "/b",
		Workspace: backend.WorkspaceMount{Mode: backend.Shared, HostPath: "/host/repo"},
	})
	if !contains(f.Calls[1].Args, "/host/repo:/home/agent/workspace") {
		t.Fatalf("shared bind missing: %v", f.Calls[1].Args)
	}
}

func TestDialParsesDockerPort(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "127.0.0.1:49153\n"}}}
	ep, cleanup, err := New(f).Dial("box")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if ep.Host != "127.0.0.1" || ep.Port != 49153 || ep.User != "agent" {
		t.Fatalf("ep = %+v", ep)
	}
}

func TestGetStatus(t *testing.T) {
	cases := []struct {
		out   string
		err   error
		want  backend.State
	}{
		{out: "true\n", want: backend.StateRunning},
		{out: "false\n", want: backend.StateStopped},
		{out: "", err: &runner.ExitError{Code: 1}, want: backend.StateAbsent},
	}
	for _, c := range cases {
		f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: c.out, Err: c.err}}}
		got, _ := New(f).GetStatus("box")
		if got != c.want {
			t.Fatalf("status(%q,%v) = %v want %v", c.out, c.err, got, c.want)
		}
	}
}

func TestRegistered(t *testing.T) {
	if _, err := backend.Get("colima"); err != nil {
		t.Fatalf("colima not registered: %v", err)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backend/colima/ -v`
Expected: FAIL (`New` undefined).

- [ ] **Step 3: Implement `colima.go`**

Create `internal/backend/colima/colima.go`:

```go
// Package colima implements the Backend interface over local Docker (Colima).
package colima

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/backend"
	"github.com/aethons-tools/at-sbx/internal/runner"
)

func init() {
	backend.Register("colima", func(r runner.Runner) backend.Backend { return New(r) })
}

type Colima struct{ r runner.Runner }

func New(r runner.Runner) backend.Backend { return &Colima{r: r} }

func image(name string) string { return "atsbx/" + name }

func (c *Colima) Create(ctx backend.CreateContext) error {
	if err := c.r.Run("docker", "build", "-t", image(ctx.Name), ctx.BuildDir); err != nil {
		return err
	}
	ws := ctx.Name + "-workspace:/home/agent/workspace"
	if ctx.Workspace.Mode == backend.Shared {
		ws = ctx.Workspace.HostPath + ":/home/agent/workspace"
	}
	return c.r.Run("docker", "run", "-d",
		"--name", ctx.Name,
		"--init",
		"--cap-add=NET_ADMIN",
		"--dns", "1.1.1.1",
		"-p", "127.0.0.1::2222",
		"-v", ctx.Name+"-state:/agent-data",
		"-v", ws,
		image(ctx.Name),
	)
}

func (c *Colima) Dial(name string) (backend.Endpoint, func(), error) {
	out, err := c.r.Output("docker", "port", name, "2222")
	if err != nil {
		return backend.Endpoint{}, func() {}, err
	}
	hostport := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]) // "127.0.0.1:49153"
	i := strings.LastIndex(hostport, ":")
	if i < 0 {
		return backend.Endpoint{}, func() {}, fmt.Errorf("colima: cannot parse docker port output %q", out)
	}
	port, err := strconv.Atoi(hostport[i+1:])
	if err != nil {
		return backend.Endpoint{}, func() {}, fmt.Errorf("colima: bad port in %q: %w", hostport, err)
	}
	return backend.Endpoint{Host: hostport[:i], Port: port, User: "agent"}, func() {}, nil
}

func (c *Colima) Destroy(name string) error {
	return c.r.Run("docker", "rm", "-f", name)
}

func (c *Colima) GetStatus(name string) (backend.State, error) {
	out, err := c.r.Output("docker", "inspect", "-f", "{{.State.Running}}", name)
	if err != nil {
		return backend.StateAbsent, nil // no such container
	}
	if strings.TrimSpace(out) == "true" {
		return backend.StateRunning, nil
	}
	return backend.StateStopped, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/backend/colima/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/backend/colima/colima.go internal/backend/colima/colima_test.go
git add internal/backend/colima/
git commit -m "feat(colima): Docker-backed Backend implementation"
```

---

## Task 11: Transport (SendEnv) + interface

**Files:**
- Create: `internal/connect/transport.go`
- Test: `internal/connect/transport_test.go`

**Interfaces:**
- Consumes: `backend.Endpoint` (Task 6), `sshargs` (Task 4), `runner.Runner` (Task 1).
- Produces:
  - `type Transport interface { Launch(t sshargs.Target, env map[string]string) error }`
  - `type SendEnv struct { R runner.Runner }` implementing `Transport` — runs `ssh` with `-o SendEnv=NAME` per var and the values placed in the child env, launching `exec claude`.

- [ ] **Step 1: Write the failing test**

Create `internal/connect/transport_test.go`:

```go
package connect

import (
	"sort"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

func TestSendEnvLaunch(t *testing.T) {
	f := &runner.Fake{}
	tr := SendEnv{R: f}
	tgt := sshargs.Target{Host: "h", User: "agent", Port: 22, IdentityFile: "/id", KnownHostsFile: "/kh"}
	err := tr.Launch(tgt, map[string]string{"GITHUB_TOKEN": "tok", "X": "y"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 || f.Calls[0].Name != "ssh" {
		t.Fatalf("expected one ssh call, got %+v", f.Calls)
	}
	// Values travel in the child env, never on argv.
	env := append([]string(nil), f.Calls[0].Env...)
	sort.Strings(env)
	if len(env) != 2 || env[0] != "GITHUB_TOKEN=tok" || env[1] != "X=y" {
		t.Fatalf("env = %v", env)
	}
	for _, a := range f.Calls[0].Args {
		if a == "tok" || a == "y" {
			t.Fatalf("secret value leaked onto argv: %v", f.Calls[0].Args)
		}
	}
	// SendEnv flags present for both names; remote command launches claude.
	joined := f.Calls[0].Args
	if !hasPair(joined, "SendEnv=GITHUB_TOKEN") || !hasPair(joined, "SendEnv=X") {
		t.Fatalf("SendEnv flags missing: %v", joined)
	}
	if joined[len(joined)-1] != "exec claude" {
		t.Fatalf("remote cmd = %q", joined[len(joined)-1])
	}
}

func hasPair(args []string, v string) bool {
	for _, a := range args {
		if a == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/connect/ -run SendEnv -v`
Expected: FAIL (`SendEnv`/`Transport` undefined).

- [ ] **Step 3: Implement `transport.go`**

Create `internal/connect/transport.go`:

```go
package connect

import (
	"sort"

	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

// Transport injects env and launches claude interactively over SSH.
type Transport interface {
	Launch(t sshargs.Target, env map[string]string) error
}

// SendEnv forwards secrets via ssh SendEnv: values live only in the ssh child's
// environment (never on argv, never on disk). The VM's sshd AcceptEnv allowlist
// (shipped in the hardening layer) accepts them.
type SendEnv struct{ R runner.Runner }

func (s SendEnv) Launch(t sshargs.Target, env map[string]string) error {
	names := make([]string, 0, len(env))
	childEnv := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names) // deterministic argv/env
	for _, k := range names {
		childEnv = append(childEnv, k+"="+env[k])
	}
	args := sshargs.InteractiveSendEnv(t, names, "exec claude")
	return s.R.RunEnv(childEnv, "ssh", args...)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/connect/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/connect/transport.go internal/connect/transport_test.go
git add internal/connect/transport.go internal/connect/transport_test.go
git commit -m "feat(connect): Transport interface + SendEnv (no argv/disk leakage)"
```

---

## Task 12: Connect orchestration

**Files:**
- Create: `internal/connect/connect.go`
- Test: `internal/connect/connect_test.go`

**Interfaces:**
- Consumes: `secret` (Task 5), `backend` (Task 6), `sshargs` (Task 4), `Transport` (Task 11).
- Produces:
  - `type Options struct { Name string; Secrets []secret.Spec; IdentityFile, KnownHostsDir string }`
  - `func Connect(b backend.Backend, r runner.Runner, t Transport, o Options) error` — order: resolve secrets → `GetStatus` must be Running → `Dial` (defer cleanup) → ensure per-sandbox known_hosts path → `Launch`.

- [ ] **Step 1: Write the failing test**

Create `internal/connect/connect_test.go`:

```go
package connect

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/backend"
	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/secret"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

type fakeBackend struct {
	state        backend.State
	dialErr      error
	cleaned      bool
	statusCalled bool
	dialCalled   bool
}

func (b *fakeBackend) Create(backend.CreateContext) error { return nil }
func (b *fakeBackend) Destroy(string) error               { return nil }
func (b *fakeBackend) GetStatus(string) (backend.State, error) {
	b.statusCalled = true
	return b.state, nil
}
func (b *fakeBackend) Dial(string) (backend.Endpoint, func(), error) {
	b.dialCalled = true
	return backend.Endpoint{Host: "h", Port: 22, User: "agent"}, func() { b.cleaned = true }, b.dialErr
}

type fakeTransport struct {
	launched bool
	gotEnv   map[string]string
}

func (t *fakeTransport) Launch(_ sshargs.Target, env map[string]string) error {
	t.launched = true
	t.gotEnv = env
	return nil
}

func opts(dir string) Options {
	return Options{
		Name:          "box",
		Secrets:       []secret.Spec{{Name: "GITHUB_TOKEN", Command: []string{"op", "x"}}},
		IdentityFile:  "/id",
		KnownHostsDir: filepath.Join(dir, "known_hosts.d"),
	}
}

func TestConnectHappyPath(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}}}
	if err := Connect(b, r, tr, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if !tr.launched || tr.gotEnv["GITHUB_TOKEN"] != "tok" {
		t.Fatalf("transport not launched with env: %+v", tr)
	}
	if !b.cleaned {
		t.Fatal("Dial cleanup not invoked")
	}
}

func TestConnectSecretFailureAbortsBeforeDial(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Err: &runner.ExitError{Code: 1}}}}
	err := Connect(b, r, tr, opts(t.TempDir()))
	if err == nil {
		t.Fatal("expected error")
	}
	if b.dialCalled || tr.launched {
		t.Fatal("must not Dial or Launch after secret failure")
	}
}

func TestConnectRequiresRunning(t *testing.T) {
	b := &fakeBackend{state: backend.StateStopped}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok"}}}
	err := Connect(b, r, &fakeTransport{}, opts(t.TempDir()))
	if err == nil || b.dialCalled {
		t.Fatalf("stopped VM should error before Dial; err=%v dial=%v", err, b.dialCalled)
	}
}

func TestConnectCreatesKnownHostsDir(t *testing.T) {
	dir := t.TempDir()
	b := &fakeBackend{state: backend.StateRunning}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok"}}}
	if err := Connect(b, r, &fakeTransport{}, opts(dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "known_hosts.d")); err != nil {
		t.Fatalf("known_hosts dir not created: %v", err)
	}
}
```

> Note: `Connect` creates the `KnownHostsDir` (so the per-sandbox known_hosts
> file has a home) but does not pre-create the file itself — `ssh`'s
> `accept-new` writes it on first connect. The test asserts the directory entry
> the implementation guarantees; see `connect.go` below.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/connect/ -run Connect -v`
Expected: FAIL (`Connect`/`Options` undefined).

- [ ] **Step 3: Implement `connect.go`**

Create `internal/connect/connect.go`:

```go
package connect

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aethons-tools/at-sbx/internal/backend"
	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/secret"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

// Options configures a connect.
type Options struct {
	Name          string
	Secrets       []secret.Spec
	IdentityFile  string
	KnownHostsDir string // per-sandbox known_hosts files live here
}

// Connect resolves secrets, verifies the VM is running, dials it, and launches
// claude with the secrets injected. Secret resolution happens before any SSH so
// a failure aborts cleanly (fail closed).
func Connect(b backend.Backend, r runner.Runner, t Transport, o Options) error {
	env, err := secret.Resolve(r, o.Secrets)
	if err != nil {
		return err
	}

	state, err := b.GetStatus(o.Name)
	if err != nil {
		return err
	}
	if state != backend.StateRunning {
		return fmt.Errorf("sandbox %q is not running; run `atsbx create` or start the VM first", o.Name)
	}

	ep, cleanup, err := b.Dial(o.Name)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.MkdirAll(o.KnownHostsDir, 0o700); err != nil {
		return err
	}
	knownHosts := filepath.Join(o.KnownHostsDir, o.Name)

	tgt := sshargs.Target{
		Host:           ep.Host,
		User:           ep.User,
		Port:           ep.Port,
		IdentityFile:   o.IdentityFile,
		KnownHostsFile: knownHosts,
	}
	return t.Launch(tgt, env)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/connect/ -v`
Expected: PASS (all transport + connect tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/connect/connect.go internal/connect/connect_test.go
git add internal/connect/connect.go internal/connect/connect_test.go
git commit -m "feat(connect): orchestrate resolve -> status -> dial -> TOFU -> launch"
```

---

## Task 13: Wire up `main.go`

Replace the `sbx`-wrapper dispatcher with the new subcommands. Keep the existing testable `run()` shape (inject `Runner`, `lookup`, writers).

**Files:**
- Modify: `main.go` (full rewrite of dispatch)
- Test: `main_test.go` (replace `sbx`-era assertions)

**Interfaces:**
- Consumes: `kit`, `assemble`, `keys`, `backend`, `backend/colima` (side-effect import), `connect`, `secret`, `runner`.
- Produces: CLI `atsbx build|create|connect|destroy|status [kit-dir] [--workspace/--ws PATH] [--dry-run]`.

- [ ] **Step 1: Write the failing tests**

Replace the body of `main_test.go` with:

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/at-sbx/internal/runner"
)

func writeKit(t *testing.T, dir string) {
	t.Helper()
	atsbx := filepath.Join(dir, ".atsbx")
	if err := os.MkdirAll(atsbx, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "name: box\nbackend: colima\n"
	if err := os.WriteFile(filepath.Join(atsbx, "config.yml"), []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusDispatchesToBackend(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "true\n"}}}
	var out, errOut bytes.Buffer
	code := run([]string{"status", filepath.Join(dir, ".atsbx")}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "running") {
		t.Fatalf("status output = %q", out.String())
	}
}

func TestUnknownBackendErrors(t *testing.T) {
	dir := t.TempDir()
	atsbx := filepath.Join(dir, ".atsbx")
	os.MkdirAll(atsbx, 0o755)
	os.WriteFile(filepath.Join(atsbx, "config.yml"), []byte("name: box\nbackend: bogus\n"), 0o644)
	var out, errOut bytes.Buffer
	code := run([]string{"status", atsbx}, &runner.Fake{}, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "bogus") {
		t.Fatalf("expected unknown-backend error, code=%d stderr=%q", code, errOut.String())
	}
}

func TestDryRunCreatePrintsNoExec(t *testing.T) {
	dir := t.TempDir()
	writeKit(t, dir)
	f := &runner.Fake{}
	var out, errOut bytes.Buffer
	code := run([]string{"--dry-run", "create", filepath.Join(dir, ".atsbx")}, f, os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, errOut.String())
	}
	if len(f.Calls) != 0 {
		t.Fatalf("dry-run executed commands: %+v", f.Calls)
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("dry-run should describe planned actions: %q", out.String())
	}
}

func dummyLookPath(string) (string, error) { return "/usr/bin/x", nil }
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test . -v`
Expected: FAIL (old `run` signature / behavior differs).

- [ ] **Step 3: Rewrite `main.go`**

Replace `main.go` with:

```go
// Command atsbx runs hardened Claude Code sandboxes from a .atsbx kit
// directory across pluggable VM backends.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/aethons-tools/at-sbx/internal/assemble"
	"github.com/aethons-tools/at-sbx/internal/backend"
	_ "github.com/aethons-tools/at-sbx/internal/backend/colima" // register colima
	"github.com/aethons-tools/at-sbx/internal/connect"
	"github.com/aethons-tools/at-sbx/internal/keys"
	"github.com/aethons-tools/at-sbx/internal/kit"
	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/secret"
)

const usage = `atsbx — run hardened Claude Code sandboxes

Usage:
  atsbx build   [kit-dir]
  atsbx create  [kit-dir] [--workspace|--ws <path>]
  atsbx connect [kit-dir]
  atsbx destroy [kit-dir]
  atsbx status  [kit-dir]

If kit-dir is omitted, atsbx walks up from the cwd to the nearest .atsbx/.

Global flags:
  --dry-run   print planned actions without executing
`

func main() {
	code := run(os.Args[1:], runner.OS{}, os.LookupEnv, exec.LookPath, os.Stdout, os.Stderr)
	os.Exit(code)
}

func run(argv []string, r runner.Runner, lookup func(string) (string, bool), lookPath func(string) (string, error), stdout, stderr io.Writer) int {
	dryRun := false
	var args []string
	wsPath := ""
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--dry-run" || a == "-dry-run":
			dryRun = true
		case a == "--workspace" || a == "--ws":
			if i+1 >= len(argv) {
				fmt.Fprintln(stderr, "atsbx: --workspace requires a path")
				return 2
			}
			i++
			wsPath = argv[i]
		default:
			args = append(args, a)
		}
	}
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, rest := args[0], args[1:]

	// Resolve the kit directory (explicit arg or discovery).
	start := "."
	if len(rest) == 1 {
		start = rest[0]
	} else if len(rest) > 1 {
		fmt.Fprintf(stderr, "atsbx: %s takes at most one kit-dir\n", cmd)
		return 2
	}
	kitDir, err := resolveKit(start)
	if err != nil {
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	cfg, err := kit.Load(kitDir)
	if err != nil {
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	factory, err := backend.Get(cfg.Backend)
	if err != nil {
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	b := factory(r)

	switch cmd {
	case "build":
		err = doBuild(kitDir, r, dryRun, stdout)
	case "create":
		err = doCreate(kitDir, cfg, b, r, wsPath, dryRun, stdout)
	case "connect":
		err = doConnect(cfg, b, r, dryRun, stdout)
	case "destroy":
		err = doSimple(b.Destroy, cfg.Name, "destroy", dryRun, stdout)
	case "status":
		err = doStatus(b, cfg.Name, dryRun, stdout)
	default:
		fmt.Fprintf(stderr, "atsbx: unknown command %q\n\n%s", cmd, usage)
		return 2
	}

	if err != nil {
		var xe *runner.ExitError
		if errors.As(err, &xe) {
			return xe.ExitCode()
		}
		fmt.Fprintln(stderr, "atsbx:", err)
		return 1
	}
	return 0
}

func resolveKit(start string) (string, error) {
	// An explicit path that already ends in .atsbx (or contains config.yml) is used directly.
	if filepath.Base(start) == ".atsbx" {
		return start, nil
	}
	if _, err := os.Stat(filepath.Join(start, "config.yml")); err == nil {
		return start, nil
	}
	return kit.Discover(start)
}

func configDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "atsbx")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "atsbx")
}

func doBuild(kitDir string, r runner.Runner, dryRun bool, stdout io.Writer) error {
	buildDir := filepath.Join(kitDir, ".build")
	if dryRun {
		fmt.Fprintf(stdout, "would assemble %s and inject managed key\n", buildDir)
		return nil
	}
	_, pub, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	return assemble.Assemble(kitDir, buildDir, pub)
}

func doCreate(kitDir string, cfg kit.Config, b backend.Backend, r runner.Runner, wsPath string, dryRun bool, stdout io.Writer) error {
	buildDir := filepath.Join(kitDir, ".build")
	ws := backend.WorkspaceMount{Mode: backend.Isolated}
	if wsPath != "" {
		abs, err := filepath.Abs(wsPath)
		if err != nil {
			return err
		}
		ws = backend.WorkspaceMount{Mode: backend.Shared, HostPath: abs}
	}
	if dryRun {
		fmt.Fprintf(stdout, "would assemble %s then backend.Create(%s)\n", buildDir, cfg.Name)
		return nil
	}
	if err := doBuild(kitDir, r, false, stdout); err != nil {
		return err
	}
	return b.Create(backend.CreateContext{Name: cfg.Name, BuildDir: buildDir, Workspace: ws})
}

func doConnect(cfg kit.Config, b backend.Backend, r runner.Runner, dryRun bool, stdout io.Writer) error {
	if dryRun {
		fmt.Fprintf(stdout, "would resolve %d secrets and connect to %s\n", len(cfg.Secrets), cfg.Name)
		return nil
	}
	priv, _, err := keys.Ensure(r, configDir())
	if err != nil {
		return err
	}
	specs := make([]secret.Spec, len(cfg.Secrets))
	for i, s := range cfg.Secrets {
		specs[i] = secret.Spec{Name: s.Name, Command: s.Command}
	}
	return connect.Connect(b, r, connect.SendEnv{R: r}, connect.Options{
		Name:          cfg.Name,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
	})
}

func doSimple(fn func(string) error, name, verb string, dryRun bool, stdout io.Writer) error {
	if dryRun {
		fmt.Fprintf(stdout, "would %s %s\n", verb, name)
		return nil
	}
	return fn(name)
}

func doStatus(b backend.Backend, name string, dryRun bool, stdout io.Writer) error {
	if dryRun {
		fmt.Fprintf(stdout, "would query status of %s\n", name)
		return nil
	}
	st, err := b.GetStatus(name)
	if err != nil {
		return err
	}
	labels := map[backend.State]string{
		backend.StateAbsent:  "absent",
		backend.StateStopped: "stopped",
		backend.StateRunning: "running",
	}
	fmt.Fprintln(stdout, labels[st])
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test . -v`
Expected: PASS.

- [ ] **Step 5: Run the whole suite and build the binary**

Run:
```bash
go build -o atsbx . && go test ./...
```
Expected: build succeeds; all packages PASS (note: `internal/sbx` and old `internal/kit` files removed in Task 14 may still exist here — if they fail to compile because they reference removed APIs, proceed to Task 14, then re-run).

- [ ] **Step 6: Commit**

```bash
gofmt -w main.go main_test.go
git add main.go main_test.go
git commit -m "feat(cli): kit-directory subcommands (build/create/connect/destroy/status)"
```

---

## Task 14: Retire the sbx-wrapper code

**Files:**
- Delete: `internal/sbx/sbx.go`, `internal/sbx/sbx_test.go`
- Delete: `internal/kit/build.go`, `internal/kit/build_test.go`, `internal/kit/create.go`, `internal/kit/create_test.go`, `internal/kit/template.go`, `internal/kit/template_test.go`

**Interfaces:** none produced; this removes dead code now that `main.go` no longer calls it.

- [ ] **Step 1: Confirm nothing live imports the old code**

Run:
```bash
grep -rn "internal/sbx" --include='*.go' . | grep -v _test.go
grep -rn "kit.Build\|kit.Create\|kit.Substitute\|kit.Stage" --include='*.go' .
```
Expected: no matches outside the files being deleted.

- [ ] **Step 2: Delete the files**

```bash
git rm internal/sbx/sbx.go internal/sbx/sbx_test.go \
       internal/kit/build.go internal/kit/build_test.go \
       internal/kit/create.go internal/kit/create_test.go \
       internal/kit/template.go internal/kit/template_test.go
```

- [ ] **Step 3: Build and test the whole module**

Run:
```bash
go build -o atsbx . && go test ./...
```
Expected: build succeeds; **all** packages PASS.

- [ ] **Step 4: Commit**

```bash
git commit -m "chore: retire sbx wrapper and envsubst templating (superseded)"
```

---

## Task 15 (optional, time-permitting): stdin/tmpfs transport

Implements the spec's *primary* transport. Only attempt if Task 13's `SendEnv`
path works end-to-end; otherwise `SendEnv` is the shipping transport per the spec.

**Files:**
- Modify: `internal/connect/transport.go` (add `StdinScript` transport)
- Test: `internal/connect/transport_test.go` (add a case)

**Interfaces:**
- Produces: `type StdinScript struct { R runner.Runner }` implementing `Transport`.

- [ ] **Step 1: Write the failing test**

Add to `internal/connect/transport_test.go`:

```go
func TestStdinScriptNoValueOnArgv(t *testing.T) {
	f := &runner.Fake{}
	tr := StdinScript{R: f}
	tgt := sshargs.Target{Host: "h", User: "agent", Port: 22, IdentityFile: "/id", KnownHostsFile: "/kh"}
	if err := tr.Launch(tgt, map[string]string{"GITHUB_TOKEN": "tok"}); err != nil {
		t.Fatal(err)
	}
	// Two ssh calls: write-to-tmpfs, then interactive source+launch.
	if len(f.Calls) != 2 {
		t.Fatalf("expected 2 ssh calls, got %d", len(f.Calls))
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret value leaked onto argv: %v", c.Args)
			}
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/connect/ -run StdinScript -v`
Expected: FAIL (`StdinScript` undefined).

- [ ] **Step 3: Implement `StdinScript`**

Add to `internal/connect/transport.go`. The first `ssh` writes the export
script to `/dev/shm/atsbx-env-<name>` via stdin (so values never hit argv); the
second interactive `ssh -tt` sources then removes it and `exec claude`. This
needs a `Runner` method that streams a provided stdin reader; add
`RunStdin(stdin io.Reader, name string, args ...string) error` to the `Runner`
interface (OS: set `cmd.Stdin = stdin`; Fake: record the call). Build the
write-side argv with `sshargs.Base(t)` plus the remote command
`cat > /dev/shm/atsbx-env-<name>` and the interactive side with
`sshargs.Base(t)` + `-tt` + `set -a; . <file>; rm -f <file>; exec claude`.

```go
import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/aethons-tools/at-sbx/internal/runner"
	"github.com/aethons-tools/at-sbx/internal/sshargs"
)

type StdinScript struct{ R runner.Runner }

func (s StdinScript) Launch(t sshargs.Target, env map[string]string) error {
	file := "/dev/shm/atsbx-env-" + t.Host // unique enough per session host:port
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	var script strings.Builder
	for _, k := range names {
		fmt.Fprintf(&script, "export %s=%s\n", k, shellQuote(env[k]))
	}

	// 1) write the script into tmpfs (no value on argv)
	writeArgs := append(sshargs.Base(t), "umask 077; cat > "+file)
	if err := s.R.RunStdin(strings.NewReader(script.String()), "ssh", writeArgs...); err != nil {
		return err
	}
	// 2) interactive: source, remove, launch
	remote := "set -a; . " + file + "; rm -f " + file + "; exec claude"
	runArgs := append([]string{"-tt"}, append(sshargs.Base(t), remote)...)
	return s.R.RunStdin(nil, "ssh", runArgs...)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
```

Also add `RunStdin` to `internal/runner/runner.go` (interface + `OS` + `Fake`)
with a small test in `internal/runner/runner_test.go` mirroring `TestFakeRecordsCalls`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/connect/ ./internal/runner/ -v`
Expected: PASS.

- [ ] **Step 5: Wire it (optional) and commit**

Optionally switch `doConnect` in `main.go` to `connect.StdinScript{R: r}`. Then:

```bash
gofmt -w internal/connect/transport.go internal/connect/transport_test.go internal/runner/runner.go internal/runner/runner_test.go
git add -A
git commit -m "feat(connect): stdin/tmpfs transport (spec primary)"
```

---

## Final verification

- [ ] Run the full suite: `go test ./...` → all PASS.
- [ ] Build: `go build -o atsbx .` → succeeds.
- [ ] Dry-run smoke (no Docker needed), from a repo containing `.atsbx/config.yml`:
  `./atsbx --dry-run create` and `./atsbx --dry-run connect` → prints planned actions, exit 0.
- [ ] Confirm `agent-infrastructure/` is unchanged: `git -C ../agent-infrastructure status --porcelain` (run from `sbx`) → no output attributable to this work.
