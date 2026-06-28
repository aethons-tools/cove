# Loop Mode — Phase B: `setup` Workspace Seeding

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a kit-level `setup` command that populates an isolated workspace (typically `git clone`),
run by `connect` on first use of an empty workspace with secrets injected,
and suppressed for a `--ws` shared workspace.

**Architecture:** `setup` is a new optional `config.yml` field,
snapshotted into the kit's state at create time
(so `connect` reads it from state, never config.yml — the existing invariant).
A new exported `connect.RunSetup` injects secrets via tmpfs
(the same secret-safe path the `StdinScript` transport uses)
and runs the command in the workspace over a non-interactive ssh,
but only when the workspace is empty.
`Connect` calls it after auth and before launch;
`main` passes the state's setup command, blanked for shared workspaces.
The per-loop `setup` override and loop wiring come in Phase C.

**Tech Stack:** Go 1.22, standard library only; shell runs inside the sandbox VM over ssh.

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- `connect`/`destroy`/`status` operate on the **state file, not `config.yml`** — so `setup` must be snapshotted into state at create time, like secrets.
- **Secrets never hit disk or argv.** `RunSetup` injects values via a `/dev/shm` script sourced in the remote shell (the same mechanism as `StdinScript`), never as ssh arguments.
- `setup` runs only on an **empty** isolated workspace (first use); a non-empty workspace is left untouched.
- `setup` is **suppressed for a `--ws` shared workspace** (the host directory already holds the code).
- Tests are hermetic (`runner.Fake`); follow the existing `internal/connect` test style.
- `KnownFields(true)` stays on the config decoder; `setup` is optional (absent ⇒ empty ⇒ no-op).

---

### Task 1: `setup` config field, state field, and create snapshot

**Files:**
- Modify: `internal/kit/config.go` (add `Setup` to `Config`)
- Modify: `internal/state/state.go` (add `Setup` to `State`)
- Modify: `main.go` (snapshot `cfg.Setup` in `saveState`)
- Test: `internal/kit/config_test.go`, `main_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces (relied on by Task 3): `kit.Config.Setup string`, `state.State.Setup string`; created instances carry the setup command in their state file.

- [ ] **Step 1: Write the failing tests**

Append to `internal/kit/config_test.go`:

```go
func TestParseConfigSetup(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nbackend: colima\nsetup: \"git clone https://x .\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Setup != "git clone https://x ." {
		t.Fatalf("Setup = %q", cfg.Setup)
	}
}

func TestParseConfigSetupOptional(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nbackend: colima\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Setup != "" {
		t.Fatalf("Setup should default empty, got %q", cfg.Setup)
	}
}
```

Append to `main_test.go`:

```go
func TestSaveStateSnapshotsSetup(t *testing.T) {
	dir := t.TempDir()
	cfg := kit.Config{Name: "box", Backend: "colima", Setup: "git clone https://x ."}
	inst := backend.Instance{Backend: "colima", Container: "box", Image: "img",
		Workspace: backend.WorkspaceMount{Mode: backend.Isolated}}
	if err := saveState(dir, cfg, inst); err != nil {
		t.Fatal(err)
	}
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Setup != "git clone https://x ." {
		t.Fatalf("state Setup = %q", st.Setup)
	}
}
```

Ensure `main_test.go` imports `"github.com/aethons-tools/cove/internal/kit"` and `"github.com/aethons-tools/cove/internal/backend"` (add any missing).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/kit/ ./ -run 'TestParseConfigSetup|TestSaveStateSnapshotsSetup'`
Expected: FAIL — `cfg.Setup`/`st.Setup` undefined (fields don't exist yet).

- [ ] **Step 3: Add `Setup` to the kit config**

In `internal/kit/config.go`, add the field to `Config`:

```go
// Config is the parsed contents of a kit's config.yml.
type Config struct {
	Name    string   `yaml:"name"`
	Backend string   `yaml:"backend"`
	Setup   string   `yaml:"setup"` // optional: command run once to populate an isolated workspace
	Secrets []Secret `yaml:"secrets"`
}
```

- [ ] **Step 4: Add `Setup` to the state**

In `internal/state/state.go`, add the field to `State` (after `WorkspaceHostPath`):

```go
	Setup             string   `json:"setup,omitempty"` // command snapshotted from config.yml to seed an isolated workspace
```

- [ ] **Step 5: Snapshot `cfg.Setup` in `saveState`**

In `main.go`, in `saveState`, set the field when building `st` (add `Setup: cfg.Setup` to the `state.State{...}` literal):

```go
	st := state.State{
		Name:          cfg.Name,
		Backend:       inst.Backend,
		Container:     inst.Container,
		Image:         inst.Image,
		WorkspaceMode: "isolated",
		Setup:         cfg.Setup,
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/kit/ ./internal/state/ ./`
Expected: PASS (new tests plus existing).

- [ ] **Step 7: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/gofmt -l internal/kit/ internal/state/ main.go`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/kit/config.go internal/state/state.go main.go internal/kit/config_test.go main_test.go
git commit -m "feat(kit): config setup command snapshotted into state"
```

---

### Task 2: `RunSetup` — secret-injecting workspace seeder

**Files:**
- Modify: `internal/connect/transport.go` (extract `envScript` helper, reuse in `StdinScript`)
- Create: `internal/connect/setup.go`
- Test: `internal/connect/setup_test.go`

**Interfaces:**
- Consumes: `shellQuote`, `workspaceDir` (existing, `transport.go`); `sshargs.Base`; `runner.Runner`.
- Produces (relied on by Task 3 and Phase C):
  - `func envScript(env map[string]string) string`
  - `func RunSetup(r runner.Runner, tgt sshargs.Target, env map[string]string, setupCmd string) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/connect/setup_test.go`:

```go
package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunSetupRunsWhenWorkspaceEmpty(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-empty\n"}}}
	if err := RunSetup(f, rawTarget(), map[string]string{"GITHUB_TOKEN": "tok"}, "git clone https://x ."); err != nil {
		t.Fatal(err)
	}
	// Calls: [0] emptiness probe (Output), [1] write env to tmpfs, [2] run setup.
	if len(f.Calls) != 3 {
		t.Fatalf("want 3 ssh calls, got %d: %+v", len(f.Calls), f.Calls)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if a == "tok" {
				t.Fatalf("secret value leaked onto argv: %v", c.Args)
			}
		}
	}
	last := f.Calls[2].Args[len(f.Calls[2].Args)-1]
	if !strings.Contains(last, "cd /home/agent/workspace && git clone https://x .") {
		t.Fatalf("setup not run in workspace: %q", last)
	}
}

func TestRunSetupSkipsWhenWorkspaceNonEmpty(t *testing.T) {
	f := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "cove-nonempty\n"}}}
	if err := RunSetup(f, rawTarget(), nil, "git clone x ."); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 1 { // only the probe
		t.Fatalf("non-empty workspace must skip setup; calls=%+v", f.Calls)
	}
}

func TestRunSetupEmptyCommandIsNoop(t *testing.T) {
	f := &runner.Fake{}
	if err := RunSetup(f, rawTarget(), nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls) != 0 {
		t.Fatalf("empty setup must do nothing; calls=%+v", f.Calls)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/connect/ -run TestRunSetup`
Expected: FAIL — build error, `undefined: RunSetup`.

- [ ] **Step 3: Extract `envScript` in `transport.go`**

In `internal/connect/transport.go`, add this helper (next to `shellQuote`):

```go
// envScript renders env as a sourceable shell script: one `export K=V` line per
// name, sorted for determinism, values single-quoted so no value ever reaches
// argv. Shared by the StdinScript transport and RunSetup.
func envScript(env map[string]string) string {
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		fmt.Fprintf(&b, "export %s=%s\n", k, shellQuote(env[k]))
	}
	return b.String()
}
```

Then in `StdinScript.Launch`, replace the inline names/script construction:

```go
	names := make([]string, 0, len(env))
	for k := range env {
		names = append(names, k)
	}
	sort.Strings(names)
	var script strings.Builder
	for _, k := range names {
		fmt.Fprintf(&script, "export %s=%s\n", k, shellQuote(env[k]))
	}
```

with:

```go
	script := envScript(env)
```

and update the write call to use `script` (a string) directly:

```go
	writeArgs := append(sshargs.Base(t), "umask 077; cat > "+file)
	if err := s.R.RunStdin(strings.NewReader(script), "ssh", writeArgs...); err != nil {
		return err
	}
```

(The `fmt`, `sort`, and `strings` imports remain in use via `envScript` and the rest of the file.)

- [ ] **Step 4: Implement `RunSetup`**

Create `internal/connect/setup.go`:

```go
package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// setupEmptyProbe reports whether the workspace is empty. It echoes a marker on
// stdout either way, so a non-zero ssh exit means the connection failed, not
// "non-empty". ls -A omits . and .. so a pristine volume reads as empty.
const setupEmptyProbe = `[ -z "$(ls -A ` + workspaceDir + ` 2>/dev/null)" ] && echo cove-empty || echo cove-nonempty`

const setupEmptyMark = "cove-empty"

// RunSetup populates an isolated workspace by running setupCmd in it, once,
// when the workspace is empty. It is a no-op when setupCmd is empty or the
// workspace already has contents (so reconnects don't re-clone). Secrets are
// injected via a tmpfs script sourced in the remote shell — values never reach
// argv — so a private `git clone` authenticates through the in-VM credential
// helper. A non-interactive ssh runs the command; its stdout/stderr stream live.
func RunSetup(r runner.Runner, tgt sshargs.Target, env map[string]string, setupCmd string) error {
	if setupCmd == "" {
		return nil
	}
	out, err := r.Output("ssh", append(sshargs.Base(tgt), setupEmptyProbe)...)
	if err != nil {
		return fmt.Errorf("checking workspace before setup: %w", err)
	}
	if !strings.Contains(out, setupEmptyMark) {
		return nil // already populated; leave it alone
	}

	file := fmt.Sprintf("/dev/shm/cove-setup-%s-%d", tgt.Host, tgt.Port)
	writeArgs := append(sshargs.Base(tgt), "umask 077; cat > "+file)
	if err := r.RunStdin(strings.NewReader(envScript(env)), "ssh", writeArgs...); err != nil {
		return fmt.Errorf("writing setup env: %w", err)
	}
	remote := "set -a; . " + file + "; rm -f " + file + "; cd " + workspaceDir + " && " + setupCmd
	if err := r.RunStdin(nil, "ssh", append(sshargs.Base(tgt), remote)...); err != nil {
		return fmt.Errorf("running setup (%s): %w", setupCmd, err)
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/connect/`
Expected: PASS — the three new `RunSetup` tests plus all existing transport/connect tests (the `envScript` extraction is behavior-preserving; existing tests do not assert stdin script content).

- [ ] **Step 6: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/connect/ && /usr/local/go/bin/gofmt -l internal/connect/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/connect/transport.go internal/connect/setup.go internal/connect/setup_test.go
git commit -m "feat(connect): RunSetup seeds an empty workspace with secrets injected"
```

---

### Task 3: Wire `setup` into `Connect` and `main` (with `--ws` suppression)

**Files:**
- Modify: `internal/connect/connect.go` (add `Options.Setup`; call `RunSetup` after auth)
- Modify: `main.go` (pass `st.Setup`, blanked for shared workspaces)
- Test: `internal/connect/connect_test.go`

**Interfaces:**
- Consumes: `RunSetup` (Task 2), `state.State.Setup` (Task 1).
- Produces: `connect.Options.Setup string`; `connect` seeds an empty isolated workspace on first use.

- [ ] **Step 1: Write the failing tests**

Append to `internal/connect/connect_test.go`:

```go
func TestConnectRunsSetupWhenConfigured(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	// Outputs consumed in order: secret, authProbe, setup emptiness probe.
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}, {Stdout: "cove-empty\n"}}}
	o := opts(t.TempDir())
	o.Setup = "git clone https://x ."
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, o); err != nil {
		t.Fatal(err)
	}
	if !calledWith(r.Calls, "git clone https://x .") {
		t.Fatal("setup must run when configured and the workspace is empty")
	}
	if !tr.launched {
		t.Fatal("must still launch after setup")
	}
}

func TestConnectNoSetupWhenUnset(t *testing.T) {
	b := &fakeBackend{state: backend.StateRunning}
	tr := &fakeTransport{}
	r := &runner.Fake{Outputs: []runner.FakeResult{{Stdout: "tok\n"}, {Stdout: "cove-authed\n"}}}
	if err := Connect(b, r, tr, &fakeInhibitor{r: &rec{}}, opts(t.TempDir())); err != nil {
		t.Fatal(err)
	}
	if calledWith(r.Calls, "ls -A") {
		t.Fatal("must not probe the workspace when no setup is configured")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/connect/ -run 'TestConnectRunsSetup|TestConnectNoSetup'`
Expected: FAIL — `o.Setup` undefined (`Options` has no `Setup` field yet).

- [ ] **Step 3: Add `Setup` to `Options` and call `RunSetup` in `Connect`**

In `internal/connect/connect.go`, add the field to `Options` (after `Stderr`):

```go
	Setup         string    // command to seed an empty isolated workspace; "" => no setup (also blanked for --ws)
```

Then in `Connect`, insert the setup call after the auth block and before the keep-awake/launch block:

```go
	if !o.SkipAuth {
		if err := ensureAuthenticated(r, tgt); err != nil {
			return err
		}
	}

	if err := RunSetup(r, tgt, env, o.Setup); err != nil {
		return err
	}

	// Keep the host awake for the session only: idle work happens between here
```

- [ ] **Step 4: Pass `st.Setup` from `main`, suppressed for shared workspaces**

In `main.go`, in `doConnect`, just before the `connect.Connect(...)` call (after `cmd` is set), compute the setup command:

```go
	setupCmd := st.Setup
	if st.WorkspaceMode == "shared" {
		setupCmd = "" // the host bind-mount already holds the code
	}
```

and add `Setup: setupCmd` to the `connect.Options{...}` literal:

```go
	return connect.Connect(b, r, connect.StdinScript{R: r, Cmd: cmd, Resume: resume}, awake.New(), connect.Options{
		Container:     st.Container,
		Secrets:       specs,
		IdentityFile:  priv,
		KnownHostsDir: filepath.Join(configDir(), "known_hosts.d"),
		SkipAuth:      noAuth,
		Stderr:        stderr,
		Setup:         setupCmd,
	})
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/connect/ ./`
Expected: PASS — the two new connect tests, the existing connect tests (which use `opts()` with `Setup` unset, so `RunSetup` returns before probing and consumes no extra `Output`), and the main package tests.

- [ ] **Step 6: Run the full suite, build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go test ./... && /usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./... && /usr/local/go/bin/gofmt -l internal/connect/ main.go`
Expected: PASS across all packages; no vet output; `gofmt -l` prints nothing.

```bash
git add internal/connect/connect.go main.go internal/connect/connect_test.go
git commit -m "feat(connect): run setup on first use of an empty workspace; suppress for --ws"
```

---

## Self-Review

**Spec coverage (Phase B slice):**
- Generic `setup` command (kit-level) → Task 1 (`Config.Setup`), snapshotted into state for the connect-operates-on-state invariant.
- Runs in the workspace over non-interactive ssh with secrets injected → Task 2 (`RunSetup`, tmpfs env script).
- Runs once on a fresh (empty) workspace; reconnects don't re-run → Task 2 (`setupEmptyProbe`/`setupEmptyMark`) + `TestRunSetupSkipsWhenWorkspaceNonEmpty`.
- `connect` first-use integration → Task 3 (`Connect` calls `RunSetup` after auth).
- Suppressed for `--ws` shared workspace → Task 3 (`main` blanks `setupCmd` when `WorkspaceMode == "shared"`).
- Private clone authenticates via the in-VM credential helper → secrets (incl. `GITHUB_TOKEN`) injected by `RunSetup`.

Deferred to Phase C: the per-loop `setup` override and re-running setup after a `fresh-workspace` reset (the `loops:` config and the loop scheduler/lifecycle).

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `RunSetup(r runner.Runner, tgt sshargs.Target, env map[string]string, setupCmd string) error`, `envScript(env map[string]string) string`, `Config.Setup`, `State.Setup`, and `Options.Setup` are defined and consumed with identical names/types across the three tasks. The `Fake.Outputs` ordering in tests matches the `Output` call order in `Connect` (secret → authProbe → setup probe).
