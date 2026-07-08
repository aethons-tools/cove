# at-cove dispatch Implementation Plan (Plan A of AET-21)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `at-cove dispatch <kit> --in input.json --out output.json` — a generic, synchronous one-shot: create a fresh ephemeral hardened VM from the kit, inject the kit's secrets + the input file, run the kit-declared dispatch entrypoint, extract the output file, and tear the container down (with crash-scavenging).

**Architecture:** Extends the `Backend` with a small `DispatchOps` surface (ephemeral labeled/volume-less run, image build, container remove, age-based scavenge — colima impl). A new `internal/dispatchrun` orchestrates scavenge→build→run→dial→seed→inject→exec→extract→destroy, **reusing** `secret.Resolve`, `sshargs`, and the `RunStdin`/`Output` ssh patterns from `connect`. `cmd/at-cove` gains a `dispatch` subcommand. at-cove never parses the in/out files.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**. Docker (via colima) and ssh are shelled through the existing `runner.Runner`.

**Scope note:** This is **Plan A** of AET-21. Plan B (rewire scheduler+config to speak at-work `input.json`/`output.json`) and Plan C (reference worker kit + `run-worker.sh` + integration) follow as separate plans/branches.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new third-party dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- **Synchronous, one-shot** — no run-ids, no `--detach`, no lifecycle-verb registry.
- **at-cove never parses `input.json`/`output.json`** — they are opaque files plumbed in/out.
- **Ephemeral container:** unique name, label `at-cove.dispatch`, `--rm`, **no named/persistent volume**, **not** recorded in `.state/state.json`. Force-removed on every exit path.
- **Crash scavenging:** each dispatch removes labeled containers older than a **grace window** (> the max dispatch timeout) at startup; also `at-cove dispatch --reap`.
- **File I/O is SSH-based** (write via `RunStdin` stdin, read via `Output` `cat`) — backend-agnostic, no `docker cp`. VM paths: `/in/input.json`, `/out/output.json`.
- **Secrets** come from the kit `config.yml` (resolved on the host via `secret.Resolve`, injected memory-only, exactly like `connect`); **never on argv or in logs**.
- **Reuse, don't duplicate,** at-cove's machinery (`internal/kit`, `internal/assemble`, `internal/backend`, `internal/secret`, `internal/sshargs`, the `connect` credential-seed pattern).
- **TDD, hermetic tests** — orchestration + backend ops are driven by `runner.Fake`; a real VM run is `integration`-tagged.
- Spec: [`docs/superpowers/specs/2026-07-08-at-cove-dispatch-design.md`](../specs/2026-07-08-at-cove-dispatch-design.md).

---

## File Structure

- `internal/kit/config.go` — add the `dispatch:` block to `Config`.
- `internal/backend/backend.go` — add the `DispatchOps` interface.
- `internal/backend/colima/dispatch.go` (+ test) — colima's `DispatchOps` impl.
- `internal/dispatchrun/dispatchrun.go` (+ test) — the orchestration.
- `cmd/at-cove/main.go` — the `dispatch` subcommand.
- `docs/OVERVIEW.md` — command surface + architecture.

---

## Task 1: kit `dispatch:` config field

**Files:**
- Modify: `internal/kit/config.go`
- Test: `internal/kit/config_test.go`

**Interfaces:**
- Produces: `kit.DispatchConfig{Command []string}`; `kit.Config.Dispatch`.

- [ ] **Step 1: Write the failing test**

Append to `internal/kit/config_test.go`:

```go
func TestParseConfigDispatch(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: w\nbackend: colima\ndispatch:\n  command: [\"run-worker.sh\"]\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Dispatch.Command) != 1 || cfg.Dispatch.Command[0] != "run-worker.sh" {
		t.Fatalf("Dispatch.Command = %v; want [run-worker.sh]", cfg.Dispatch.Command)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/kit/ -run TestParseConfigDispatch`
Expected: FAIL to build — `cfg.Dispatch` undefined (strict `KnownFields` would also reject the unknown `dispatch:` key).

- [ ] **Step 3: Write the implementation**

In `internal/kit/config.go`, add the type and field (near `ImageConfig`):

```go
// DispatchConfig declares how `at-cove dispatch` performs a unit of work: the
// command run inside the VM, which reads /in/input.json and writes /out/output.json.
type DispatchConfig struct {
	Command []string `yaml:"command"`
}
```

Add the field to `Config`:

```go
	Image    ImageConfig     `yaml:"image"`
	Dispatch DispatchConfig  `yaml:"dispatch"`
```

(No `ParseConfig` validation change — only `at-cove dispatch` requires `Dispatch.Command`, and it checks that itself in Task 4.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/kit/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/kit/config.go internal/kit/config_test.go
git commit -m "feat(kit): dispatch entrypoint config field"
```

---

## Task 2: `DispatchOps` backend interface + colima impl

**Files:**
- Modify: `internal/backend/backend.go` (add `DispatchOps`)
- Create: `internal/backend/colima/dispatch.go`
- Test: `internal/backend/colima/dispatch_test.go`

**Interfaces:**
- Produces: `backend.DispatchOps` interface; colima's `BuildImage`, `RunEphemeral`, `RemoveContainer`, `ScavengeLabeled` (colima already has `Dial`). `var _ backend.DispatchOps = (*colima.Colima)(nil)`.

- [ ] **Step 1: Write the failing test**

Create `internal/backend/colima/dispatch_test.go`:

```go
package colima

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunEphemeralArgs(t *testing.T) {
	f := &runner.Fake{}
	c := New(f).(*Colima)
	inst, err := c.RunEphemeral("img:tag", "disp-1", "at-cove.dispatch")
	if err != nil {
		t.Fatalf("RunEphemeral: %v", err)
	}
	if inst.Container != "disp-1" || inst.Image != "img:tag" {
		t.Fatalf("instance = %+v", inst)
	}
	got := strings.Join(f.Calls[len(f.Calls)-1].Args, " ")
	for _, want := range []string{"run -d", "--name disp-1", "--rm", "--label at-cove.dispatch", "-p 127.0.0.1::2222", "img:tag"} {
		if !strings.Contains(got, want) {
			t.Errorf("run args missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "-v ") {
		t.Errorf("ephemeral run must not mount a volume:\n%s", got)
	}
}

func TestScavengeLabeledRemovesOldOnly(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	f := &runner.Fake{
		Outputs: map[string]runner.FakeResult{
			// ps -aq --filter label=... → two container ids
			"docker --context colima ps -aq --filter label=at-cove.dispatch": {Out: "old\nfresh\n"},
			// inspect Created for each
			"docker --context colima inspect -f {{.Created}} old":   {Out: now.Add(-2 * time.Hour).Format(time.RFC3339Nano)},
			"docker --context colima inspect -f {{.Created}} fresh": {Out: now.Add(-1 * time.Minute).Format(time.RFC3339Nano)},
		},
	}
	c := New(f).(*Colima)
	n, err := c.ScavengeLabeled("at-cove.dispatch", 30*time.Minute, now)
	if err != nil {
		t.Fatalf("ScavengeLabeled: %v", err)
	}
	if n != 1 {
		t.Fatalf("removed %d; want 1 (only the 2h-old one)", n)
	}
	joined := ""
	for _, c := range f.Calls {
		joined += strings.Join(c.Args, " ") + "\n"
	}
	if !strings.Contains(joined, "rm -f old") || strings.Contains(joined, "rm -f fresh") {
		t.Fatalf("should rm old, not fresh:\n%s", joined)
	}
}
```

Note: this test uses `runner.Fake`'s `Calls` recording and an `Outputs` map keyed by the joined command. **Confirm the `Fake`'s field names** (`Calls`, `Outputs`, `FakeResult{Out,Err}`) against `internal/runner/runner.go` and adjust the keys/fields to match the existing fake's shape before finalizing.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/backend/colima/ -run 'TestRunEphemeral|TestScavenge'`
Expected: FAIL to build — `undefined: (*Colima).RunEphemeral` / `ScavengeLabeled`.

- [ ] **Step 3: Write the implementation**

In `internal/backend/backend.go`, add the interface:

```go
import "time" // add to the import block

// DispatchOps is the ephemeral-container surface `at-cove dispatch` needs, beyond
// the persistent Create/Destroy lifecycle. A Backend may implement it.
type DispatchOps interface {
	BuildImage(buildDir, tag string) error
	RunEphemeral(image, name, label string) (Instance, error) // fresh labeled --rm no-volume container; sshd published
	Dial(container string) (Endpoint, func(), error)
	RemoveContainer(name string) error // docker rm -f; no image/volume removal
	// ScavengeLabeled force-removes labeled containers whose age (relative to now)
	// exceeds olderThan. Returns the count removed.
	ScavengeLabeled(label string, olderThan time.Duration, now time.Time) (int, error)
}
```

Create `internal/backend/colima/dispatch.go`:

```go
package colima

import (
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
)

// Compile-time proof colima satisfies the dispatch surface.
var _ backend.DispatchOps = (*Colima)(nil)

func (c *Colima) BuildImage(buildDir, tag string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", dargs("build", "-t", tag, buildDir)...)
}

// RunEphemeral starts a fresh, labeled, volume-less container with --rm and a
// published sshd, so a force-remove (or --rm on stop) reclaims everything.
func (c *Colima) RunEphemeral(image, name, label string) (backend.Instance, error) {
	if err := c.preflight(); err != nil {
		return backend.Instance{}, err
	}
	if err := c.r.Run("docker", dargs("run", "-d",
		"--name", name,
		"--rm",
		"--label", label,
		"--init",
		"--cap-add=NET_ADMIN",
		"--dns", "1.1.1.1",
		"-p", "127.0.0.1::2222",
		image,
	)...); err != nil {
		return backend.Instance{}, err
	}
	return backend.Instance{Backend: "colima", Container: name, Image: image}, nil
}

func (c *Colima) RemoveContainer(name string) error {
	if err := c.preflight(); err != nil {
		return err
	}
	return c.r.Run("docker", dargs("rm", "-f", name)...)
}

// ScavengeLabeled removes labeled containers older than olderThan. It never removes
// the image (shared across dispatches) or a volume (there are none).
func (c *Colima) ScavengeLabeled(label string, olderThan time.Duration, now time.Time) (int, error) {
	if err := c.preflight(); err != nil {
		return 0, err
	}
	out, err := c.r.Output("docker", dargs("ps", "-aq", "--filter", "label="+label)...)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, id := range strings.Fields(out) {
		created, err := c.r.Output("docker", dargs("inspect", "-f", "{{.Created}}", id)...)
		if err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(created))
		if err != nil {
			continue
		}
		if now.Sub(t) > olderThan {
			if err := c.r.Run("docker", dargs("rm", "-f", id)...); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}
```

**Verify against real docker:** `docker inspect -f '{{.Created}}'` emits RFC3339Nano (e.g. `2026-07-08T12:00:00.123456789Z`); `time.RFC3339Nano` parses it. The hermetic test feeds this exact format; the `integration` run confirms it live.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/backend/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/backend/backend.go internal/backend/colima/dispatch.go internal/backend/colima/dispatch_test.go
git commit -m "feat(backend): DispatchOps (ephemeral run + scavenge) on colima"
```

---

## Task 3: the dispatch orchestration

**Files:**
- Create: `internal/dispatchrun/dispatchrun.go`
- Test: `internal/dispatchrun/dispatchrun_test.go`

**Interfaces:**
- Consumes: `backend.DispatchOps`, `runner.Runner`, `kit.Config`, `secret.Spec`, `sshargs`.
- Produces: `dispatchrun.Options`, `dispatchrun.Dispatch(o Options) error`, `dispatchrun.Reap(ops backend.DispatchOps, grace time.Duration, now time.Time) error`, const `Label`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatchrun/dispatchrun_test.go`:

```go
package dispatchrun

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
)

// fakeOps records DispatchOps calls; Dial returns a fixed endpoint.
type fakeOps struct {
	scavenged bool
	built     bool
	ran       bool
	removed   bool
}

func (f *fakeOps) BuildImage(_, _ string) error { f.built = true; return nil }
func (f *fakeOps) RunEphemeral(_, name, _ string) (backend.Instance, error) {
	f.ran = true
	return backend.Instance{Container: name}, nil
}
func (f *fakeOps) Dial(string) (backend.Endpoint, func(), error) {
	return backend.Endpoint{Host: "127.0.0.1", Port: 2222, User: "agent"}, func() {}, nil
}
func (f *fakeOps) RemoveContainer(string) error { f.removed = true; return nil }
func (f *fakeOps) ScavengeLabeled(string, time.Duration, time.Time) (int, error) {
	f.scavenged = true
	return 0, nil
}

func TestDispatchHappyPath(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "input.json", `{"issue":{}}`)
	out := dir + "/output.json"
	// the ssh `cat /out/output.json` returns the worker's output
	r := &runner.Fake{Outputs: map[string]runner.FakeResult{}}
	setOutputForCat(r, `{"status":"OK"}`) // helper: make any `cat /out/output.json` ssh return this
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:       kit.Config{Name: "w", Dispatch: kit.DispatchConfig{Command: []string{"run-worker.sh"}}},
		BuildDir:  dir, Name: "disp-1",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !ops.scavenged || !ops.built || !ops.ran || !ops.removed {
		t.Fatalf("ops sequence incomplete: %+v", ops)
	}
	if b := readFile(t, out); !strings.Contains(b, `"status":"OK"`) {
		t.Fatalf("output not extracted: %q", b)
	}
	// the worker's dispatch command ran, timeout-wrapped, secrets sourced
	joined := allCalls(r)
	if !strings.Contains(joined, "run-worker.sh") || !strings.Contains(joined, "timeout ") {
		t.Fatalf("dispatch command not run with timeout:\n%s", joined)
	}
}

func TestDispatchRemovesContainerOnFailure(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "input.json", `{}`)
	r := &runner.Fake{} // no cat output → extraction fails
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:      kit.Config{Name: "w", Dispatch: kit.DispatchConfig{Command: []string{"x"}}},
		BuildDir: dir, Name: "disp-2", InputPath: in, OutputPath: dir + "/o.json",
		IdentityFile: "id", KnownHostsDir: t.TempDir(), Timeout: time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error when no output is produced")
	}
	if !ops.removed {
		t.Fatal("container must be removed even on failure")
	}
}
```

Note: `writeFile`/`readFile`/`allCalls`/`setOutputForCat` are small test helpers — implement them at the bottom of the test file against the **actual** `runner.Fake` shape (its `Calls`/`Outputs`/result fields), which you must read from `internal/runner/runner.go` first. `setOutputForCat` makes the ssh invocation whose remote arg contains `cat /out/output.json` return the given stdout; `allCalls` joins every recorded call's argv.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatchrun/`
Expected: FAIL to build — `undefined: Dispatch`, `Options`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatchrun/dispatchrun.go`:

```go
// Package dispatchrun orchestrates `at-cove dispatch`: a synchronous, one-shot run
// of a unit of work in a fresh ephemeral hardened VM. It reuses at-cove's secret,
// ssh, and backend machinery; it never parses the in/out files.
package dispatchrun

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/secret"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// Label tags every ephemeral dispatch container so scavenging can find orphans.
const Label = "at-cove.dispatch"

const (
	inputVMPath  = "/in/input.json"
	outputVMPath = "/out/output.json"
	credsVMPath  = "/agent-data/.credentials.json"
	envVMPath    = "/dev/shm/at-cove-dispatch-env"
)

type Options struct {
	Ops             backend.DispatchOps
	R               runner.Runner
	Cfg             kit.Config
	BuildDir        string
	Name            string // unique container name
	Secrets         []secret.Spec
	CredentialsFile string // host-saved agent login to seed; "" = none
	IdentityFile    string
	KnownHostsDir   string
	InputPath       string
	OutputPath      string
	Timeout         time.Duration
	GraceWindow     time.Duration
	Now             time.Time
}

// Reap removes labeled dispatch orphans older than grace (the `--reap` path).
func Reap(ops backend.DispatchOps, grace time.Duration, now time.Time) error {
	_, err := ops.ScavengeLabeled(Label, grace, now)
	return err
}

// Dispatch runs one unit of work: scavenge → build → ephemeral run → inject →
// exec the kit's dispatch command → extract output → destroy. Blocking.
func Dispatch(o Options) error {
	if len(o.Cfg.Dispatch.Command) == 0 {
		return fmt.Errorf("kit %q declares no dispatch.command", o.Cfg.Name)
	}
	// Scavenge crash orphans (best-effort; never blocks a live dispatch).
	_, _ = o.Ops.ScavengeLabeled(Label, o.GraceWindow, o.Now)

	// Resolve secrets before creating anything (fail closed).
	env, err := secret.Resolve(o.R, o.Secrets)
	if err != nil {
		return err
	}

	img := "at-cove-for-" + o.Cfg.Name
	if err := o.Ops.BuildImage(o.BuildDir, img); err != nil {
		return err
	}
	if _, err := o.Ops.RunEphemeral(img, o.Name, Label); err != nil {
		return err
	}
	defer o.Ops.RemoveContainer(o.Name) // teardown on every path

	ep, cleanup, err := o.Ops.Dial(o.Name)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := os.MkdirAll(o.KnownHostsDir, 0o700); err != nil {
		return err
	}
	tgt := sshargs.Target{
		Host: ep.Host, User: ep.User, Port: ep.Port,
		IdentityFile: o.IdentityFile, KnownHostsFile: filepath.Join(o.KnownHostsDir, o.Name),
	}

	if err := seedFile(o.R, tgt, o.CredentialsFile, credsVMPath); err != nil {
		return fmt.Errorf("seed agent credentials: %w", err)
	}
	input, err := os.ReadFile(o.InputPath)
	if err != nil {
		return err
	}
	if err := writeVM(o.R, tgt, input, inputVMPath); err != nil {
		return fmt.Errorf("inject input: %w", err)
	}
	if err := runWork(o.R, tgt, env, o.Cfg.Dispatch.Command, o.Timeout); err != nil {
		return fmt.Errorf("dispatch command: %w", err)
	}
	out, err := o.R.Output("ssh", append(sshargs.Base(tgt), "cat "+outputVMPath)...)
	if err != nil || strings.TrimSpace(out) == "" {
		return fmt.Errorf("dispatch produced no output at %s", outputVMPath)
	}
	return os.WriteFile(o.OutputPath, []byte(out), 0o600)
}

// seedFile copies a host file into the VM (mode 077, via stdin, never on argv).
// A "" local path or a missing file is a no-op.
func seedFile(r runner.Runner, tgt sshargs.Target, localPath, vmPath string) error {
	if localPath == "" {
		return nil
	}
	data, err := os.ReadFile(localPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return writeVM(r, tgt, data, vmPath)
}

// writeVM writes data to vmPath in the VM over ssh stdin (values never on argv).
func writeVM(r runner.Runner, tgt sshargs.Target, data []byte, vmPath string) error {
	remote := "umask 077; mkdir -p " + filepath.Dir(vmPath) + "; cat > " + vmPath
	return r.RunStdin(bytes.NewReader(data), "ssh", append(sshargs.Base(tgt), remote)...)
}

// runWork runs the kit's dispatch command with secrets sourced from a tmpfs env
// script (never on argv), bounded by timeout, /out ready for the output.
func runWork(r runner.Runner, tgt sshargs.Target, env map[string]string, cmd []string, timeout time.Duration) error {
	if err := writeVM(r, tgt, []byte(envScript(env)), envVMPath); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 1800
	}
	remote := fmt.Sprintf("set -a; . %s; rm -f %s; mkdir -p /out; timeout %d %s",
		envVMPath, envVMPath, secs, shellJoin(cmd))
	return r.RunStdin(nil, "ssh", append(sshargs.Base(tgt), remote)...)
}

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

func shellJoin(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatchrun/`
Expected: PASS (happy path extracts output + runs the timeout-wrapped command; failure path still removes the container).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatchrun/
git commit -m "feat(dispatchrun): at-cove dispatch orchestration (scavenge→run→inject→exec→extract→destroy)"
```

---

## Task 4: `cmd/at-cove dispatch` subcommand

**Files:**
- Modify: `cmd/at-cove/main.go`
- Test: `cmd/at-cove/main_test.go`

**Interfaces:**
- Consumes: `dispatchrun.Dispatch`/`Reap`, `kit.Load`, `assemble.Assemble`, `backend.Get` + `DispatchOps`, `secret.Spec`.
- Produces: `at-cove dispatch <kit-dir> --in <f> --out <f> [--timeout <dur>] [--reap]`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/at-cove/main_test.go` (follow the file's existing `run(...)` test style):

```go
func TestDispatchRequiresInAndOut(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"dispatch", "somekit"}, runner.NewFake(), os.LookupEnv, dummyLookPath, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit = %d; want 2 (missing --in/--out)", code)
	}
	if !strings.Contains(errOut.String(), "--in") {
		t.Fatalf("stderr = %q; want mention of --in/--out", errOut.String())
	}
}
```

Note: match the exact `run(...)` signature and the existing fake/`dummyLookPath` helpers used elsewhere in `main_test.go` (read the file first).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/at-cove/ -run TestDispatchRequires`
Expected: FAIL — `dispatch` is an unknown command (falls through to the default/usage → but assert the specific `--in` message once implemented; before implementation it returns the unknown-command path).

- [ ] **Step 3: Write the implementation**

In `cmd/at-cove/main.go`, add the `dispatch` case to the subcommand switch (alongside `case "build":` … `case "status":`):

```go
	case "dispatch":
		return doDispatch(args, r, stdout, stderr)
```

Add the handler (model its assemble/backend/secret wiring on `doCreate`/`doConnect`; read those to match this repo's exact helpers — `configDir()`, the managed key path, `assemble.Assemble`, `kit.Load`, `backend.Get`):

```go
func doDispatch(args []string, r runner.Runner, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inPath := fs.String("in", "", "path to the input.json to inject")
	outPath := fs.String("out", "", "path to write the extracted output.json")
	timeout := fs.Duration("timeout", 30*time.Minute, "hard wall-clock cap for the work")
	grace := fs.Duration("grace", 60*time.Minute, "age past which a labeled orphan is scavenged")
	reap := fs.Bool("reap", false, "scavenge dispatch orphans and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(stderr, "at-cove dispatch: expected <kit-dir>")
		return 2
	}
	kitDir := rest[0]

	cfg, err := kit.Load(kitDir)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	factory, err := backend.Get(cfg.Backend)
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	ops, ok := factory(r).(backend.DispatchOps)
	if !ok {
		fmt.Fprintf(stderr, "at-cove: backend %q does not support dispatch\n", cfg.Backend)
		return 1
	}

	if *reap {
		if err := dispatchrun.Reap(ops, *grace, time.Now()); err != nil {
			fmt.Fprintf(stderr, "at-cove: reap: %v\n", err)
			return 1
		}
		return 0
	}

	if *inPath == "" || *outPath == "" {
		fmt.Fprintln(stderr, "at-cove dispatch: --in and --out are required")
		return 2
	}
	if len(cfg.Dispatch.Command) == 0 {
		fmt.Fprintf(stderr, "at-cove: kit %q declares no dispatch.command\n", cfg.Name)
		return 1
	}

	// Assemble the build context (public key injected), as `create` does.
	buildDir := filepath.Join(kitDir, ".build")
	pub, err := keys.EnsureManaged() // match doCreate/doBuild's key helper; adjust to the real call
	if err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}
	if err := assemble.Assemble(kitDir, buildDir, pub, cfg.Image); err != nil {
		fmt.Fprintf(stderr, "at-cove: %v\n", err)
		return 1
	}

	specs := make([]secret.Spec, len(cfg.Secrets))
	for i, s := range cfg.Secrets {
		specs[i] = secret.Spec{Name: s.Name, Command: s.Command}
	}

	// Ephemeral, uniquely-named per dispatch. Derive from the input path so
	// concurrent dispatches don't collide (a hash/uuid is fine; keep it stable-free).
	name := "at-cove-dispatch-" + cfg.Name + "-" + filepath.Base(filepath.Dir(*inPath))

	err = dispatchrun.Dispatch(dispatchrun.Options{
		Ops: ops, R: r, Cfg: cfg, BuildDir: buildDir, Name: name,
		Secrets:         specs,
		CredentialsFile: filepath.Join(configDir(), "credentials.json"),
		IdentityFile:    filepath.Join(configDir(), "id_ed25519"), // match doConnect's priv key path
		KnownHostsDir:   filepath.Join(configDir(), "known_hosts.d"),
		InputPath:       *inPath, OutputPath: *outPath,
		Timeout: *timeout, GraceWindow: *grace, Now: time.Now(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "at-cove dispatch: %v\n", err)
		return 1
	}
	return 0
}
```

Add `dispatchrun` and `time`/`flag` to the imports if not present. **Reconcile every helper name** (`keys.EnsureManaged`, the private-key path, `configDir()`, `assemble.Assemble`, `kit.Load`) with what `doCreate`/`doConnect`/`doBuild` actually call — read those functions and mirror them exactly; the block above is the shape, not guaranteed-verbatim helper names.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/at-cove/ ./internal/...`
Expected: PASS (arg-validation test). The full dispatch happy path needs a real VM and is covered by the `integration` run (Plan C).

Run: `go build ./cmd/... && go vet ./... && gofmt -l cmd/ internal/`
Expected: builds; no vet errors; gofmt clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/at-cove/main.go cmd/at-cove/main_test.go
git commit -m "feat(at-cove): dispatch subcommand"
```

---

## Task 5: docs

**Files:**
- Modify: `docs/OVERVIEW.md`

- [ ] **Step 1: Update the command surface + architecture**

Add a row to the command-surface table in `docs/OVERVIEW.md` (after `version`):

```
| `at-cove dispatch <kit> --in <f> --out <f> [--timeout] [--reap]` | Run one unit of work in a fresh ephemeral hardened VM: inject the input, run the kit's `dispatch.command`, extract the output, destroy. Scavenges crashed dispatch orphans. |
```

Add architecture-map rows (after the existing at-cove rows):

```
internal/dispatchrun/           `at-cove dispatch` orchestration (scavenge → run → inject → exec → extract → destroy)
```

And note in the "How the build context is assembled" / backend area that colima also implements `backend.DispatchOps` (ephemeral labeled runs + scavenge) for `dispatch`.

- [ ] **Step 2: Verify**

Run: `grep -n "at-cove dispatch" docs/OVERVIEW.md`
Expected: the new rows are present.

Run: `go test ./... && go vet ./... && gofmt -l cmd/ internal/`
Expected: all pass; gofmt clean.

- [ ] **Step 3: Commit**

```bash
git add docs/OVERVIEW.md
git commit -m "docs: record at-cove dispatch"
```

---

## Final verification

- [ ] `go test ./...` — all packages pass.
- [ ] `just build` — all binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/` prints nothing.
- [ ] A real dispatch (ephemeral VM, a kit with a trivial `dispatch.command` that copies `/in/input.json` to `/out/output.json`) is deferred to Plan C's `integration` run.

## Notes

- **Two implementer reconciliations are required** and flagged inline: (1) the `runner.Fake` shape (`Calls`/`Outputs`/result fields) — read `internal/runner/runner.go` and adjust the test helpers; (2) the exact `cmd/at-cove` helpers (`keys.*`, private-key path, `configDir()`, `assemble.Assemble`) — mirror `doCreate`/`doConnect`. These are real names in this repo, not placeholders; the plan gives the shape and the reconciliation is a read-and-match, not a design decision.
- **`--rm` + label + no volume + deferred `RemoveContainer` + startup scavenge** together guarantee no container or volume leaks across crashes.
