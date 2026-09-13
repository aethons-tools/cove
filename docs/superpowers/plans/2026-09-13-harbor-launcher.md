# harbor real Colima Launcher + cove-master-in-image — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `placeholderLauncher` with a real Colima-backed `harbor.Launcher` and embed `cove-master` in the cove image, so `at-harbor cove raise --prompt-file` starts a cove that runs the agent and reports over the :443 Attach stream, then is torn down.

**Architecture:** Build-tree half embeds `cove-master` (mirrors at-switchboard). Harbor half: a supervisor seam that hands the launcher the minted creds; a `connect.LaunchCoveMaster` that injects connector+env+prompt over SSH and starts cove-master detached; an `internal/harbor/launcher` package implementing Raise/Teardown/Probe on Colima; CLI + serve wiring.

**Tech Stack:** Go 1.25, existing `internal/{backend,connect,keys,sshargs,naming,install,runner,assemble}` + `internal/harbor/snippet`.

## Global Constraints

- **Module commands offline:** run go commands with **`GOPROXY=off`** (live proxy blocked, deps cached). No new external deps in this slice.
- **Boundary gates (must stay true):**
  - `internal/harbor` **core** imports no backend/connect/grpc/kit (the real launcher lives in `internal/harbor/launcher`, wired from `cmd/at-harbor`). The seam types (`RaiseSpec`, `LaunchCreds`, `Launcher`) are plain.
  - `internal/connect` stays **go-oidc-free and grpc-free** (`LaunchCoveMaster` uses only snippet/backend/runner/sshargs). Verify `go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc'` stays empty.
  - `internal/covemaster` untouched.
- **Secrets never on argv/disk/logs:** the identity token, launch secret, and prompt flow into the cove via tmpfs over ssh stdin (`writeVM`), exactly like the teammate path. Never `docker -e`, never logged.
- **Mirror the embedding precisely** — `internal/covemasterbin` is a byte-for-byte structural copy of `internal/atswitchboard` (only the binary name `cove-master` and `cmd/cove-master` differ).
- **Honor COV-151:** Probe returns `LivenessUnknown` on a transient (non-nil error) probe, `LivenessDead` only on a confirmed absent/stopped state.
- TDD; hermetic tests (fakes, no Docker/network/VM); gofmt-clean (CI lint gate).
- **Commit trailers** on every commit:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv
  ```

---

### Task 1: Embed `cove-master` — `internal/covemasterbin` + staging

**Files:**
- Create: `internal/covemasterbin/covemasterbin.go`, `internal/covemasterbin/bin/README`, `internal/covemasterbin/bin/.gitignore`
- Test: `internal/covemasterbin/covemasterbin_test.go`
- Modify: `scripts/stage-attask.sh`

**Interfaces:**
- Produces: `covemasterbin.Binary(goarch string) ([]byte, error)`, `covemasterbin.BinFS() fs.FS`. Tasks 2 consume these.

- [ ] **Step 1: Write the failing test** — `internal/covemasterbin/covemasterbin_test.go`

```go
package covemasterbin

import (
	"strings"
	"testing"
)

func TestBinaryUnstagedErrorsActionably(t *testing.T) {
	// A fresh checkout stages only bin/README + bin/.gitignore, so the real
	// per-arch binaries are absent and Binary must error actionably (never panic).
	_, err := Binary("amd64")
	if err == nil {
		t.Skip("cove-master binary is staged in this build; nothing to assert")
	}
	if !strings.Contains(err.Error(), "cove-master") {
		t.Fatalf("error should name cove-master, got: %v", err)
	}
}

func TestBinFSNonNil(t *testing.T) {
	if BinFS() == nil {
		t.Fatal("BinFS() returned nil")
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

Run: `GOPROXY=off go test ./internal/covemasterbin/ -v`
Expected: FAIL — package/`Binary`/`BinFS` undefined.

- [ ] **Step 3: Create the embed package** — `internal/covemasterbin/covemasterbin.go`

```go
// Package covemasterbin embeds the linux cove-master binaries into at-cove, so
// the at-cove that builds a cove image installs the *exact* matching cove-master
// into the hardening layer — version lockstep, no coordination, no pin to drift
// (mirrors internal/atswitchboard, COV-135, and internal/attask, COV-36). Both
// linux arches are embedded regardless of at-cove's own host, since at-cove may
// build a sandbox for either VM arch.
package covemasterbin

import (
	"embed"
	"fmt"
	"io/fs"
)

// binFS holds the linux cove-master binaries. Only bin/README + bin/.gitignore
// are tracked; the binaries (bin/cove-master-linux-{amd64,arm64}) are gitignored
// build artifacts, staged by scripts/stage-attask.sh before at-cove is built so
// this embed picks them up. A fresh checkout embeds just the placeholder, so the
// package still compiles — Binary then errors actionably at runtime.
//
//go:embed bin
var binFS embed.FS

// Binary returns the embedded linux cove-master binary for goarch ("amd64" or
// "arm64"). It errors if the binary was not staged (a plain `go build` without
// the pre-step) rather than shipping a broken sandbox.
func Binary(goarch string) ([]byte, error) {
	return lookup(binFS, goarch)
}

// BinFS returns the embedded cove-master binaries as a read-only FS (rooted at
// "bin"). internal/install hashes it as part of at-cove's build identity for the
// install currency check (COV-38), so a cove-master rebuild invalidates installs.
func BinFS() fs.FS { return binFS }

func lookup(fsys fs.FS, goarch string) ([]byte, error) {
	name := "bin/cove-master-linux-" + goarch
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("embedded cove-master for linux/%s not staged — run scripts/stage-attask.sh (or use a release build): %w", goarch, err)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("embedded cove-master for linux/%s is empty", goarch)
	}
	return b, nil
}
```

- [ ] **Step 4: Create the placeholder tracked files**

`internal/covemasterbin/bin/README`:
```
cove-master binaries embedded into at-cove for version lockstep (COV-158).

This placeholder keeps `//go:embed bin` compiling in a fresh checkout. The
real per-arch binaries (cove-master-linux-amd64, cove-master-linux-arm64)
are staged here by scripts/stage-attask.sh / the release workflow before at-cove
is built, and are gitignored.
```

`internal/covemasterbin/bin/.gitignore`:
```
/cove-master-linux-*
```

- [ ] **Step 5: Add the staging loop** — `scripts/stage-attask.sh`

After the at-switchboard staging loop, append:
```bash
covemaster_dir="internal/covemasterbin/bin"
mkdir -p "$covemaster_dir"
for a in amd64 arm64; do
  echo "  staging cove-master linux/${a} (${VERSION})"
  CGO_ENABLED=0 GOOS=linux GOARCH="$a" \
    go build -trimpath -ldflags "$LDFLAGS" -o "${covemaster_dir}/cove-master-linux-${a}" ./cmd/cove-master
done
```
Also update the script's header comment to mention cove-master (internal/covemasterbin + COV-158).

- [ ] **Step 6: Run the test + build, verify pass**

Run:
```bash
GOPROXY=off go test ./internal/covemasterbin/ -v
GOPROXY=off go build ./...
```
Expected: tests PASS (unstaged-error test skips or passes depending on whether bin is staged; `TestBinFSNonNil` passes); build OK.

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/covemasterbin/
git add internal/covemasterbin/ scripts/stage-attask.sh
git commit -m "covemasterbin: embed cove-master linux binaries + staging (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 2: Stage into the image — `assemble` + Dockerfile + install currency

**Files:**
- Modify: `internal/assemble/assemble.go`, `internal/assemble/hardening/Dockerfile`, `internal/install/currency.go`
- Test: extend `internal/install/currency_test.go` (or add) to assert covemaster participates in the identity hash.

**Interfaces:**
- Consumes: `covemasterbin.Binary`, `covemasterbin.BinFS` (Task 1).

- [ ] **Step 1: Add `writeCoveMaster` + call it** — `internal/assemble/assemble.go`

Add the import `"github.com/aethons-tools/cove/internal/covemasterbin"`. After the `writeSwitchboard(buildDir)` call in `Assemble`, add:
```go
	if err := writeCoveMaster(buildDir); err != nil {
		return err
	}
```
Add the function (mirrors `writeSwitchboard`):
```go
// writeCoveMaster stages the embedded linux cove-master binaries into the build
// context (buildDir/covemaster/cove-master-linux-<arch>), so the sealed hardening
// layer can install the arch-matching one — mirrors writeSwitchboard (COV-158).
// When the embed was not staged (a plain `go build` without scripts/stage-attask.sh),
// a 0-byte placeholder is written instead and hardening's install guard skips it;
// a raised cove then has no cove-master and the launcher's start step fails clearly.
func writeCoveMaster(buildDir string) error {
	dir := filepath.Join(buildDir, "covemaster")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, arch := range []string{"amd64", "arm64"} {
		b, err := covemasterbin.Binary(arch)
		if err != nil {
			b = nil // not staged → placeholder; caught at launch, not build
		}
		if err := os.WriteFile(filepath.Join(dir, "cove-master-linux-"+arch), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 2: Add the Dockerfile install step** — `internal/assemble/hardening/Dockerfile`

After the at-switchboard block (the `rm -rf /tmp/switchboard` line), add (mirror it):
```dockerfile
# Install the embedded, version-locked cove-master the orchestrating at-cove
# staged into the build context (mirrors at-switchboard above, COV-158). Install
# only when the arch binary is non-empty; a plain `go build` stages a 0-byte
# placeholder, in which case cove-master is simply absent and a managed raise
# fails clearly at launch — there is no base-image fallback.
COPY covemaster/ /tmp/covemaster/
RUN arch="$(dpkg --print-architecture)" \
 && if [ -s "/tmp/covemaster/cove-master-linux-${arch}" ]; then \
      install -m 0755 "/tmp/covemaster/cove-master-linux-${arch}" /usr/local/bin/cove-master; \
    fi \
 && rm -rf /tmp/covemaster
```
(Match the exact `arch=`/`if [ -s ... ]` shape used by the at-switchboard block just above — copy its RUN structure verbatim, swapping the paths/name.)

- [ ] **Step 3: Add covemaster to the install identity hash** — `internal/install/currency.go`

Add the import `"github.com/aethons-tools/cove/internal/covemasterbin"`. In `AtCoveIdentity`, after the switchboard field block, add:
```go
	cm, err := HashTree(covemasterbin.BinFS())
	if err != nil {
		return "", err
	}
	writeField(h, []byte("covemaster"))
	writeField(h, []byte(cm))
```
Update the `AtCoveIdentity` doc comment to mention cove-master alongside at-task and at-switchboard.

- [ ] **Step 4: Write the failing test (identity includes covemaster)**

If `internal/install/currency_test.go` has an existing identity/stability test, extend it; otherwise add:
```go
func TestAtCoveIdentityStable(t *testing.T) {
	a, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	b, err := AtCoveIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("identity not stable: %s != %s", a, b)
	}
	if a == "" {
		t.Fatal("empty identity")
	}
}
```
(A deterministic content-change test isn't hermetically feasible without mutating the embed; the value of this task is that `AtCoveIdentity` now *reads* `covemasterbin.BinFS()` — verified by the code review + the build. Keep the stability assertion.)

- [ ] **Step 5: Run tests + build**

Run:
```bash
GOPROXY=off go test ./internal/install/ ./internal/assemble/ -v
GOPROXY=off go build ./...
```
Expected: PASS; build OK.

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/assemble/assemble.go internal/install/currency.go
git add internal/assemble/assemble.go internal/assemble/hardening/Dockerfile internal/install/currency.go internal/install/currency_test.go
git commit -m "assemble+install: stage cove-master into the image + identity hash (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 3: Supervisor seam — `RaiseSpec.Prompt` + `Launcher.Raise(…, creds)`

**Files:**
- Modify: `internal/harbor/supervisor.go`, `cmd/at-harbor/main.go` (placeholderLauncher), the fake launchers in `internal/harbor/supervisor_test.go` and `internal/harbor/admin_test.go` (whichever define a fake `Launcher`).
- Test: extend `internal/harbor/supervisor_test.go`.

**Interfaces:**
- Produces: `harbor.LaunchCreds{IdentityToken, LaunchSecret string}`; `RaiseSpec.Prompt string`; `Launcher.Raise(ctx, spec, creds LaunchCreds) (string, error)`. Task 5 (launcher) + Task 6 (admin) consume these.

- [ ] **Step 1: Write the failing test** — add to `internal/harbor/supervisor_test.go`

Find the existing fake launcher in the test file (it implements `Launcher`). Add a field to record creds, then a test:
```go
func TestRaisePassesCredsToLauncher(t *testing.T) {
	st := newTestStore(t) // use whatever the file's existing store constructor is
	fl := &fakeLauncher{}  // the file's existing fake; extend it with gotCreds/gotSpec
	sup := NewSupervisor(st, fl, NewHolderID(), time.Minute, 30*time.Second, time.Now, nil)
	if err := seedGuestRole(t, st); err != nil { // reuse the file's role-seeding helper
		t.Fatal(err)
	}
	_, tok, secret, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if fl.gotCreds.IdentityToken != tok || fl.gotCreds.LaunchSecret != secret {
		t.Fatalf("launcher creds = %+v, want token=%q secret=%q", fl.gotCreds, tok, secret)
	}
	if fl.gotSpec.Prompt != "do it" {
		t.Fatalf("launcher spec.Prompt = %q, want %q", fl.gotSpec.Prompt, "do it")
	}
}
```
Adapt the helper names to those already in the test file (match its existing Raise tests). Extend the fake launcher:
```go
type fakeLauncher struct {
	// ...existing fields...
	gotSpec  RaiseSpec
	gotCreds LaunchCreds
}
func (f *fakeLauncher) Raise(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (string, error) {
	f.gotSpec, f.gotCreds = spec, creds
	return "loc-" + spec.ActorID, nil
}
```

- [ ] **Step 2: Run the test, verify it fails to compile**

Run: `GOPROXY=off go test ./internal/harbor/ -run TestRaisePassesCreds`
Expected: FAIL — `LaunchCreds` undefined / `Raise` signature mismatch.

- [ ] **Step 3: Change the seam** — `internal/harbor/supervisor.go`

Add `Prompt` to `RaiseSpec`:
```go
type RaiseSpec struct {
	ActorID string
	Project string
	Role    string
	Unit    string
	Prompt  string // workload prompt for the raised cove's agent; consumed by the launcher, not persisted
}
```
Add the creds type and change the interface:
```go
// LaunchCreds carries the per-instance credentials the supervisor mints and the
// launcher must inject into the cove (identity token + launch secret). Passed to
// Raise so the launcher can bootstrap cove-master without the supervisor leaking
// them elsewhere.
type LaunchCreds struct {
	IdentityToken string
	LaunchSecret  string
}

type Launcher interface {
	Raise(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (location string, err error)
	Teardown(ctx context.Context, inst Instance) error
	Probe(ctx context.Context, inst Instance) (Liveness, error)
}
```
In `Supervisor.Raise`, change the call:
```go
	loc, err := s.launcher.Raise(ctx, spec, LaunchCreds{IdentityToken: tok, LaunchSecret: secret})
```

- [ ] **Step 4: Update `placeholderLauncher`** — `cmd/at-harbor/main.go`

```go
func (placeholderLauncher) Raise(_ context.Context, spec harbor.RaiseSpec, _ harbor.LaunchCreds) (string, error) {
	return "placeholder:" + spec.ActorID, nil
}
```
(Keep Teardown/Probe as-is.)

- [ ] **Step 5: Run tests + build**

Run:
```bash
GOPROXY=off go build ./...
GOPROXY=off go test ./internal/harbor/ ./cmd/at-harbor/ -v
```
Expected: PASS (the new creds test + all existing harbor/admin tests, with fakes updated).

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/harbor/supervisor.go cmd/at-harbor/main.go internal/harbor/supervisor_test.go
git add internal/harbor/supervisor.go internal/harbor/supervisor_test.go internal/harbor/admin_test.go cmd/at-harbor/main.go
git commit -m "harbor: thread LaunchCreds + RaiseSpec.Prompt through the Launcher seam (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 4: `connect.LaunchCoveMaster`

**Files:**
- Create: `internal/connect/covemaster.go`
- Test: `internal/connect/covemaster_test.go`

**Interfaces:**
- Consumes: `snippet.Render`, `writeVM`, `envScript`, `shellQuote`, `sshargs`, `runner.Runner` (all existing in package `connect`).
- Produces: `connect.CoveMasterOptions`, `connect.LaunchCoveMaster(r runner.Runner, o CoveMasterOptions) error`. Task 5 consumes it.

- [ ] **Step 1: Write the failing test** — `internal/connect/covemaster_test.go`

```go
package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

func TestLaunchCoveMasterInjectsAndLaunches(t *testing.T) {
	fake := &runner.Fake{}
	tgt := sshargs.Target{Host: "h", User: "agent", Port: 2222, IdentityFile: "k", KnownHostsFile: "kh"}
	err := LaunchCoveMaster(fake, CoveMasterOptions{
		Target:        tgt,
		HarborHost:    "harbor.example.com",
		RuntimeAddr:   "harbor.example.com:443",
		IdentityToken: "tok-123",
		LaunchSecret:  "sec-456",
		WorkDir:       "/home/agent/workspace",
		Prompt:        "do the task",
	})
	if err != nil {
		t.Fatal(err)
	}

	// runner.Fake records each invocation in f.Calls ([]runner.Call{Name, Args, Stdin}).
	// stdinTo returns the Stdin piped to the ssh call whose argv contains "cat > <path>".
	stdinTo := func(path string) string {
		for _, c := range fake.Calls {
			if c.Name == "ssh" && strings.Contains(strings.Join(c.Args, " "), "cat > "+path) {
				return c.Stdin
			}
		}
		t.Fatalf("no ssh stdin write to %s; calls=%+v", path, fake.Calls)
		return ""
	}

	// The prompt is written to a tmpfs file via ssh stdin (never argv).
	if got := stdinTo(coveMasterPromptVMPath); got != "do the task" {
		t.Fatalf("prompt stdin = %q, want %q", got, "do the task")
	}

	// The env script is written to a tmpfs file and contains the connector + cove-master vars.
	envWrite := stdinTo(coveMasterEnvVMPath)
	for _, want := range []string{
		"AT_HARBOR_RUNTIME_ADDR=", "harbor.example.com:443",
		"AT_HARBOR_LAUNCH_SECRET=", "sec-456",
		"AT_COVE_WORKDIR=", "/home/agent/workspace",
		"AT_COVE_AGENT_PROMPT_FILE=", coveMasterPromptVMPath,
		"AT_HARBOR_IDENTITY_TOKEN=tok-123",
		"ANTHROPIC_BASE_URL=https://harbor.example.com/anthropic",
		"git config --global",
	} {
		if !strings.Contains(envWrite, want) {
			t.Fatalf("env script missing %q; got:\n%s", want, envWrite)
		}
	}

	// The detached launch command starts cove-master, and no secret rides argv.
	var launched bool
	for _, c := range fake.Calls {
		argv := strings.Join(c.Args, " ")
		if c.Name == "ssh" && strings.Contains(argv, "setsid nohup cove-master") {
			launched = true
		}
		for _, sec := range []string{"tok-123", "sec-456", "do the task"} {
			if strings.Contains(argv, sec) {
				t.Fatalf("secret %q leaked onto argv: %s", sec, argv)
			}
		}
	}
	if !launched {
		t.Fatalf("no detached cove-master launch; calls=%+v", fake.Calls)
	}
}
```

`runner.Fake` (see `internal/runner/runner.go`) exposes `Calls []Call` where `Call{Name string; Args []string; Stdin string}` — `Stdin` holds the bytes piped via `RunStdin` (how `writeVM` stages tmpfs files). The test above uses that real API directly.

- [ ] **Step 2: Run the test, verify it fails**

Run: `GOPROXY=off go test ./internal/connect/ -run TestLaunchCoveMaster`
Expected: FAIL — `LaunchCoveMaster`/`CoveMasterOptions` undefined.

- [ ] **Step 3: Write the implementation** — `internal/connect/covemaster.go`

```go
package connect

import (
	"fmt"
	"strings"

	"github.com/aethons-tools/cove/internal/harbor/snippet"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// coveMasterPromptVMPath / coveMasterEnvVMPath are tmpfs files the prompt and env
// are staged into over ssh stdin (never argv, never persistent disk), sourced and
// (env) removed by the detached launch — mirrors the teammate env path.
const (
	coveMasterPromptVMPath = "/dev/shm/cove-agent-prompt"
	coveMasterEnvVMPath    = "/dev/shm/cove-master-env"
	coveMasterLogVMPath    = "/agent-data/cove-master.log"
)

// CoveMasterOptions carries what LaunchCoveMaster injects into a raised cove.
type CoveMasterOptions struct {
	Target        sshargs.Target
	HarborHost    string // harbor.host — connector base is https://<HarborHost>
	RuntimeAddr   string // AT_HARBOR_RUNTIME_ADDR (harbor.host:443)
	IdentityToken string // shared: AT_HARBOR_IDENTITY_TOKEN + the agent connector token
	LaunchSecret  string // AT_HARBOR_LAUNCH_SECRET
	WorkDir       string // AT_COVE_WORKDIR
	Prompt        string // written to tmpfs; AT_COVE_AGENT_PROMPT_FILE points at it
}

// LaunchCoveMaster stages the agent connector (Anthropic + git through harbor)
// plus the cove-master env and the prompt into tmpfs over ssh stdin, then starts
// cove-master detached over a non-tty ssh (fire-and-forget), so it survives the
// ssh channel closing. Secrets never touch argv or persistent disk.
func LaunchCoveMaster(r runner.Runner, o CoveMasterOptions) error {
	if err := writeVM(r, o.Target, o.Prompt, coveMasterPromptVMPath); err != nil {
		return fmt.Errorf("cove-master prompt: %w", err)
	}
	var script strings.Builder
	// Agent connector (Anthropic base URL + x-api-key token + git routing). The
	// identity token is exported here and shared with cove-master below.
	script.WriteString(snippet.Render("https://"+o.HarborHost, o.IdentityToken))
	// cove-master's own env (AT_HARBOR_IDENTITY_TOKEN already exported by Render).
	fmt.Fprintf(&script, "export AT_HARBOR_RUNTIME_ADDR=%s\n", shellQuote(o.RuntimeAddr))
	fmt.Fprintf(&script, "export AT_HARBOR_LAUNCH_SECRET=%s\n", shellQuote(o.LaunchSecret))
	fmt.Fprintf(&script, "export AT_COVE_WORKDIR=%s\n", shellQuote(o.WorkDir))
	fmt.Fprintf(&script, "export AT_COVE_AGENT_PROMPT_FILE=%s\n", shellQuote(coveMasterPromptVMPath))
	if err := writeVM(r, o.Target, script.String(), coveMasterEnvVMPath); err != nil {
		return fmt.Errorf("cove-master env: %w", err)
	}
	cmd := "set -a; . " + coveMasterEnvVMPath + "; set +a; rm -f " + coveMasterEnvVMPath + "; " +
		"setsid nohup cove-master </dev/null >>" + coveMasterLogVMPath + " 2>&1 &"
	if err := r.Run("ssh", append(sshargs.Base(o.Target), cmd)...); err != nil {
		return fmt.Errorf("cove-master launch: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the test, verify it passes**

Run: `GOPROXY=off go test ./internal/connect/ -run TestLaunchCoveMaster -v`
Expected: PASS.

- [ ] **Step 5: Boundary + gofmt + commit**

```bash
go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo "at-cove clean"
gofmt -w internal/connect/covemaster.go internal/connect/covemaster_test.go
git add internal/connect/covemaster.go internal/connect/covemaster_test.go
git commit -m "connect: LaunchCoveMaster — inject connector+env+prompt, start cove-master detached (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```
Expected: "at-cove clean".

---

### Task 5: `internal/harbor/launcher` — Raise/Teardown/Probe + `naming.CoveContainer`

**Files:**
- Modify: `internal/naming/naming.go` (+ its test)
- Create: `internal/harbor/launcher/launcher.go`, `internal/harbor/launcher/launcher_test.go`

**Interfaces:**
- Consumes: `harbor.{RaiseSpec,LaunchCreds,Instance,Launcher,Liveness,Liveness*}` (Task 3), `connect.LaunchCoveMaster` (Task 4), `backend.{DispatchOps,Backend,Endpoint,State,State*}`, `sshargs.Target`, `runner.Runner`.
- Produces: `naming.CoveContainer(actorID) string`; `launcher.New(Config) *Launcher` implementing `harbor.Launcher`.

- [ ] **Step 1: Add `naming.CoveContainer` (TDD)** — test first in `internal/naming/naming_test.go`:

```go
func TestCoveContainer(t *testing.T) {
	if got := CoveContainer("w1"); got != "atcove-cove-w1" {
		t.Fatalf("CoveContainer = %q, want atcove-cove-w1", got)
	}
	// Unsafe chars are sanitized to a docker-safe name.
	if got := CoveContainer("proj/worker@1"); strings.ContainsAny(got, "/@") {
		t.Fatalf("CoveContainer left unsafe chars: %q", got)
	}
}
```
Then implement in `naming.go`:
```go
// CoveContainer names a managed cove's container from its actor id: atcove-cove-<id>,
// with any docker-unsafe characters replaced by '-' so the name is always valid.
func CoveContainer(actorID string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			return r
		default:
			return '-'
		}
	}, actorID)
	return prefix + "-cove-" + safe
}
```
(Ensure `strings` is imported in naming.go.)

- [ ] **Step 2: Write the failing launcher test** — `internal/harbor/launcher/launcher_test.go`

```go
package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/runner"
)

type fakeOps struct {
	ran        bool
	runName    string
	runImage   string
	runAddHost []string
	removed    string
	status     backend.State
	statusErr  error
	runErr     error
}

func (f *fakeOps) RunEphemeral(image, digest, name, label string, dns, addHosts []string, docker bool) (backend.Instance, error) {
	f.ran, f.runName, f.runImage, f.runAddHost = true, name, image, addHosts
	if f.runErr != nil {
		return backend.Instance{}, f.runErr
	}
	return backend.Instance{Container: name, Image: image}, nil
}
func (f *fakeOps) Dial(container string) (backend.Endpoint, func(), error) {
	return backend.Endpoint{Host: "127.0.0.1", Port: 2222, User: "agent"}, func() {}, nil
}
func (f *fakeOps) RemoveContainer(name string) error { f.removed = name; return nil }
func (f *fakeOps) ScavengeLabeled(label string, olderThan time.Duration, now time.Time) (int, error) {
	return 0, nil
}
func (f *fakeOps) GetStatus(container string) (backend.State, error) { return f.status, f.statusErr }

func newLauncher(ops *fakeOps) *Launcher {
	return New(Config{
		Ops: ops, Runner: &runner.Fake{},
		Image: "atcove-worker", ImageDigest: "sha256:abc",
		HarborHost: "harbor.example.com", RuntimeAddr: "harbor.example.com:443",
		IdentityFile: "k", KnownHostsDir: "/kh",
		sleep: func(time.Duration) {}, // injected no-op wait-for-sshd
	})
}

func TestRaiseRunsDialsLaunches(t *testing.T) {
	ops := &fakeOps{}
	l := newLauncher(ops)
	loc, err := l.Raise(context.Background(), harbor.RaiseSpec{ActorID: "w1", Prompt: "go"}, harbor.LaunchCreds{IdentityToken: "t", LaunchSecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if !ops.ran || ops.runName != "atcove-cove-w1" || ops.runImage != "atcove-worker" {
		t.Fatalf("RunEphemeral not called correctly: %+v", ops)
	}
	if len(ops.runAddHost) != 1 || ops.runAddHost[0] != "harbor.example.com" {
		t.Fatalf("addHosts = %v, want [harbor.example.com]", ops.runAddHost)
	}
	if loc != "atcove-cove-w1" {
		t.Fatalf("location = %q, want atcove-cove-w1", loc)
	}
}

func TestRaiseRemovesContainerOnLaunchFailure(t *testing.T) {
	ops := &fakeOps{}
	l := New(Config{
		Ops: ops, Runner: runner.Failing(), // a Fake whose Run/RunStdin returns an error
		Image: "img", HarborHost: "h", RuntimeAddr: "h:443", IdentityFile: "k", KnownHostsDir: "/kh",
		sleep: func(time.Duration) {},
	})
	_, err := l.Raise(context.Background(), harbor.RaiseSpec{ActorID: "w1"}, harbor.LaunchCreds{})
	if err == nil {
		t.Fatal("want error on launch failure")
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("container not cleaned up on failure: removed=%q", ops.removed)
	}
}

func TestTeardownRemoves(t *testing.T) {
	ops := &fakeOps{}
	if err := newLauncher(ops).Teardown(context.Background(), harbor.Instance{Location: "atcove-cove-w1"}); err != nil {
		t.Fatal(err)
	}
	if ops.removed != "atcove-cove-w1" {
		t.Fatalf("removed = %q", ops.removed)
	}
}

func TestProbeMapsState(t *testing.T) {
	cases := []struct {
		st   backend.State
		err  error
		want harbor.Liveness
	}{
		{backend.StateRunning, nil, harbor.LivenessAlive},
		{backend.StateStopped, nil, harbor.LivenessDead},
		{backend.StateAbsent, nil, harbor.LivenessDead},
		{backend.StateAbsent, errors.New("docker hiccup"), harbor.LivenessUnknown},
	}
	for _, c := range cases {
		ops := &fakeOps{status: c.st, statusErr: c.err}
		got, _ := newLauncher(ops).Probe(context.Background(), harbor.Instance{Location: "x"})
		if got != c.want {
			t.Fatalf("state %v err %v → %v, want %v", c.st, c.err, got, c.want)
		}
	}
}
```

**Note:** adapt `runner.Fake`/`runner.Failing()` to the real API. If there is no built-in "failing runner," add a tiny local fake in the test file that returns an error from `Run`/`RunStdin`.

- [ ] **Step 3: Run the test, verify it fails**

Run: `GOPROXY=off go test ./internal/harbor/launcher/ -v`
Expected: FAIL — package/`New`/`Launcher` undefined.

- [ ] **Step 4: Write the implementation** — `internal/harbor/launcher/launcher.go`

```go
// Package launcher is the real harbor.Launcher: it raises/tears down/probes a
// managed cove on a Colima backend and bootstraps cove-master over SSH. It lives
// outside internal/harbor core (which stays backend/connect-free) and is wired
// from cmd/at-harbor.
package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/aethons-tools/cove/internal/backend"
	"github.com/aethons-tools/cove/internal/connect"
	"github.com/aethons-tools/cove/internal/harbor"
	"github.com/aethons-tools/cove/internal/naming"
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

const Label = "harbor.cove"

// Backend is the backend surface the launcher needs: the ephemeral run/dial/remove
// ops plus GetStatus for probing. The Colima backend value satisfies both.
type Backend interface {
	backend.DispatchOps // RunEphemeral, Dial, RemoveContainer, ScavengeLabeled
	GetStatus(container string) (backend.State, error)
}

// Config configures the Colima launcher.
type Config struct {
	Ops           Backend
	Runner        runner.Runner
	Image         string
	ImageDigest   string
	HarborHost    string
	RuntimeAddr   string
	IdentityFile  string
	KnownHostsDir string
	DNS           []string
	Docker        bool
	WorkDir       string // AT_COVE_WORKDIR; default /home/agent/workspace
	log           *slog.Logger
	sleep         func(time.Duration) // wait-for-sshd backoff; nil → time.Sleep
}

type Launcher struct{ cfg Config }

var _ harbor.Launcher = (*Launcher)(nil)

func New(cfg Config) *Launcher {
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "/home/agent/workspace"
	}
	if cfg.log == nil {
		cfg.log = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	return &Launcher{cfg: cfg}
}

func (l *Launcher) Raise(ctx context.Context, spec harbor.RaiseSpec, creds harbor.LaunchCreds) (string, error) {
	name := naming.CoveContainer(spec.ActorID)
	if _, err := l.cfg.Ops.RunEphemeral(l.cfg.Image, l.cfg.ImageDigest, name, Label, l.cfg.DNS, []string{l.cfg.HarborHost}, l.cfg.Docker); err != nil {
		return "", fmt.Errorf("raise %s: run: %w", name, err)
	}
	// From here, clean up the container on any failure so a failed raise leaks nothing.
	launch := func() error {
		ep, cleanup, err := l.cfg.Ops.Dial(name)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		defer cleanup()
		tgt := sshargs.Target{
			Host: ep.Host, User: ep.User, Port: ep.Port,
			IdentityFile:   l.cfg.IdentityFile,
			KnownHostsFile: filepath.Join(l.cfg.KnownHostsDir, name),
		}
		if err := l.waitForSSH(tgt); err != nil {
			return fmt.Errorf("wait for sshd: %w", err)
		}
		return connect.LaunchCoveMaster(l.cfg.Runner, connect.CoveMasterOptions{
			Target: tgt, HarborHost: l.cfg.HarborHost, RuntimeAddr: l.cfg.RuntimeAddr,
			IdentityToken: creds.IdentityToken, LaunchSecret: creds.LaunchSecret,
			WorkDir: l.cfg.WorkDir, Prompt: spec.Prompt,
		})
	}
	if err := launch(); err != nil {
		if rmErr := l.cfg.Ops.RemoveContainer(name); rmErr != nil {
			l.cfg.log.Warn("raise cleanup: remove container failed", "name", name, "error", rmErr)
		}
		return "", fmt.Errorf("raise %s: %w", name, err)
	}
	return name, nil
}

func (l *Launcher) Teardown(ctx context.Context, inst harbor.Instance) error {
	return l.cfg.Ops.RemoveContainer(inst.Location)
}

func (l *Launcher) Probe(ctx context.Context, inst harbor.Instance) (harbor.Liveness, error) {
	st, err := l.cfg.Ops.GetStatus(inst.Location)
	if err != nil {
		return harbor.LivenessUnknown, nil // transient — don't reap (COV-151)
	}
	switch st {
	case backend.StateRunning:
		return harbor.LivenessAlive, nil
	default:
		return harbor.LivenessDead, nil
	}
}

// waitForSSH polls sshd with a trivial command until it answers or attempts run out.
func (l *Launcher) waitForSSH(tgt sshargs.Target) error {
	const attempts = 30
	probe := append(sshargs.Base(tgt), "true")
	var err error
	for i := 0; i < attempts; i++ {
		if err = l.cfg.Runner.Run("ssh", probe...); err == nil {
			return nil
		}
		if i < attempts-1 {
			l.cfg.sleep(time.Second)
		}
	}
	return err
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
```

**Note to implementer:** `Config.Ops` is the composite `Backend` interface (DispatchOps + GetStatus); the test's `fakeOps` implements all of it, and the production Colima value satisfies it too (Task 7 asserts it). Probe behavior is fixed: error→Unknown, Running→Alive, else→Dead.

- [ ] **Step 5: Run tests + build + boundary**

Run:
```bash
GOPROXY=off go test ./internal/harbor/launcher/ ./internal/naming/ -v
GOPROXY=off go build ./...
go list -deps ./internal/harbor | grep -iE 'internal/backend|internal/connect|grpc' || echo "harbor core clean"
```
Expected: tests PASS; build OK; "harbor core clean" (the launcher subpackage importing backend/connect must NOT pull those into `internal/harbor` core — verify the grep excludes the `launcher` subpkg by checking `./internal/harbor` only, not `./internal/harbor/...`).

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/naming/naming.go internal/naming/naming_test.go internal/harbor/launcher/
git add internal/naming/naming.go internal/naming/naming_test.go internal/harbor/launcher/
git commit -m "harbor/launcher: real Colima Raise/Teardown/Probe + naming.CoveContainer (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 6: Admin API + CLI — `cove raise --prompt-file`

**Files:**
- Modify: `internal/harbor/admin.go` (CoveRaiseBody + the raise handler), `cmd/at-harbor/main.go` (the `cove raise` verb), and the relevant adminclient call if it constructs the body.
- Test: extend `internal/harbor/admin_test.go`.

- [ ] **Step 1: Write the failing test** — extend `internal/harbor/admin_test.go`

Add a test that POSTs `/admin/coves` with a `prompt` and asserts it reaches the fake launcher's `gotSpec.Prompt` (reuse the Task 3 fake launcher wiring in the admin test harness). Follow the file's existing cove-raise test shape.

- [ ] **Step 2: Run, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run Cove`
Expected: FAIL — `CoveRaiseBody` has no `Prompt`.

- [ ] **Step 3: Add `Prompt` to the body + handler** — `internal/harbor/admin.go`

Add `Prompt string \`json:"prompt,omitempty"\`` to `CoveRaiseBody`, and pass it into the `RaiseSpec` the handler builds:
```go
inst, tok, secret, err := sup.Raise(r.Context(), RaiseSpec{ActorID: b.ID, Project: b.Project, Role: b.Role, Unit: b.Unit, Prompt: b.Prompt})
```

- [ ] **Step 4: Add `--prompt-file` to the CLI** — `cmd/at-harbor/main.go`

In the `cove raise` verb: add a `--prompt-file` string flag; when set, read the file (`os.ReadFile`) host-side and set `Prompt` on the request body sent to the admin API. (If the adminclient has a typed method for raise, thread `Prompt` through it.) Read the file host-side so the prompt never rides argv.

- [ ] **Step 5: Run tests + build**

Run:
```bash
GOPROXY=off go build ./...
GOPROXY=off go test ./internal/harbor/ ./cmd/at-harbor/ -v
```
Expected: PASS.

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/harbor/admin.go cmd/at-harbor/main.go internal/harbor/admin_test.go
git add internal/harbor/admin.go cmd/at-harbor/main.go internal/harbor/admin_test.go
# include the adminclient file if modified
git commit -m "harbor: cove raise --prompt-file → RaiseSpec.Prompt (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 7: Serve wiring — `runtime.launcher` config builds the real launcher

**Files:**
- Modify: `cmd/at-harbor/config.go` (the `Runtime` block + a `Launcher` struct + validation), `cmd/at-harbor/main.go` (`cmdServe`).
- Test: extend `cmd/at-harbor/config_test.go` (or add) for parse + validation.

- [ ] **Step 1: Write the failing config test** — `cmd/at-harbor/config_test.go`

Add a test that parses a serve-config with a `runtime.launcher` block and asserts the fields land (install-manifest, runtime-addr, harbor-host, identity-file, dns, docker), and that a launcher block missing a required field (install-manifest / runtime-addr / harbor-host) errors from the validation helper.

- [ ] **Step 2: Run, verify fail**

Run: `GOPROXY=off go test ./cmd/at-harbor/ -run Launcher`
Expected: FAIL — the `Launcher` config field/validator doesn't exist.

- [ ] **Step 3: Extend the config** — `cmd/at-harbor/config.go`

Add to `serveConfig.Runtime` a `Launcher *launcherConfig` field with:
```go
type launcherConfig struct {
	InstallManifest string   `yaml:"install-manifest"`
	RuntimeAddr     string   `yaml:"runtime-addr"`
	HarborHost      string   `yaml:"harbor-host"`
	IdentityFile    string   `yaml:"identity-file"`
	KnownHostsDir   string   `yaml:"known-hosts-dir"`
	DNS             []string `yaml:"dns"`
	Docker          bool     `yaml:"docker"`
}
```
Add a `validateLauncher()` that, when `Runtime.Launcher != nil`, requires `InstallManifest`, `RuntimeAddr`, `HarborHost` (clear error each). Default `IdentityFile`/`KnownHostsDir` to the at-cove config dir + `known_hosts.d` when empty (document that harbor must share the install key dir).

- [ ] **Step 4: Wire it in `cmdServe`** — `cmd/at-harbor/main.go`

Where the supervisor is built (currently `harbor.NewSupervisor(st, placeholderLauncher{}, …)`), select the launcher:
```go
var lch harbor.Launcher = placeholderLauncher{}
if lc := cfg.Runtime.Launcher; lc != nil {
	var m install.Manifest
	b, err := os.ReadFile(lc.InstallManifest)
	if err != nil {
		fmt.Fprintln(stderr, "at-harbor: launcher install-manifest:", err)
		return 1
	}
	if err := json.Unmarshal(b, &m); err != nil {
		fmt.Fprintln(stderr, "at-harbor: launcher install-manifest:", err)
		return 1
	}
	be, ok := colima.New(runner.OS{}).(launcher.Backend) // colima.New returns backend.Backend; *Colima also satisfies DispatchOps+GetStatus
	if !ok {
		fmt.Fprintln(stderr, "at-harbor: colima backend does not satisfy launcher.Backend")
		return 1
	}
	lch = launcher.New(launcher.Config{
		Ops: be, Runner: runner.OS{},
		Image: m.Image, ImageDigest: m.ImageDigest,
		HarborHost: lc.HarborHost, RuntimeAddr: lc.RuntimeAddr,
		IdentityFile: lc.IdentityFile, KnownHostsDir: lc.KnownHostsDir,
		DNS: lc.DNS, Docker: lc.Docker,
	})
	log.Info("harbor launcher: colima", "image", m.Image, "runtime-addr", lc.RuntimeAddr)
}
sup := harbor.NewSupervisor(st, lch, harbor.NewHolderID(), ttl, reconcile, time.Now, log)
```
Add imports: `encoding/json`, `internal/install`, `internal/harbor/launcher`, `internal/backend/colima`, `internal/runner`. `colima.New(r runner.Runner) backend.Backend` (confirmed at `internal/backend/colima/colima.go:27`) returns a `*Colima`, which also implements `backend.DispatchOps` + `GetStatus` — hence the `.(launcher.Backend)` assertion above.

- [ ] **Step 5: Run tests + build + boundary**

Run:
```bash
GOPROXY=off go build ./...
GOPROXY=off go test ./cmd/at-harbor/ -v
go list -deps ./cmd/at-cove | grep -iE 'oidc|grpc' || echo "at-cove clean"
```
Expected: PASS; build OK; "at-cove clean".

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w cmd/at-harbor/config.go cmd/at-harbor/main.go cmd/at-harbor/config_test.go
git add cmd/at-harbor/config.go cmd/at-harbor/main.go cmd/at-harbor/config_test.go
git commit -m "at-harbor: build the real Colima launcher from runtime.launcher config (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

### Task 8: Docs

**Files:**
- Modify: `docs/usage/harbor/coves.md`, `docs/usage/harbor/serve.md`

- [ ] **Step 1: `coves.md`** — add a "Raising a real managed cove" note: `at-harbor cove raise --role <r> --prompt-file <f>` starts a Colima cove from the configured image, injects the connector + prompt, starts cove-master (which runs `claude -p` on the prompt with Anthropic+git via harbor), reports over the Attach stream, and is torn down when the agent finishes. Note the scope: this validates the lifecycle; the role must grant the anthropic + git destinations; commit/push/PR handling comes with the dispatcher.

- [ ] **Step 2: `serve.md`** — document the `runtime.launcher` block (install-manifest, runtime-addr, harbor-host, identity-file, known-hosts-dir, dns, docker) and the **deployment constraint**: harbor must be given the same SSH key `at-cove install` baked into the image's `authorized_keys` (`identity-file`), and reach the coves' Colima backend.

- [ ] **Step 3: Bump `updated:` frontmatter to 2026-09-13; docs-audit delta**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no *new* errors referencing `coves.md`/`serve.md` (delta vs the known baseline).

- [ ] **Step 4: Commit**

```bash
git add docs/usage/harbor/coves.md docs/usage/harbor/serve.md
git commit -m "docs: real managed-cove raise flow + runtime.launcher config (COV-158)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_018DRQWUqrvQMzYD8o9ZeLHv"
```

---

## Self-Review

- **Spec coverage:** embed (T1) + assemble/Dockerfile/currency (T2); seam RaiseSpec.Prompt+LaunchCreds (T3); connect.LaunchCoveMaster (T4); launcher pkg + naming (T5); admin+CLI --prompt-file (T6); serve wiring runtime.launcher (T7); docs (T8). Every spec section maps to a task.
- **Type consistency:** `Launcher.Raise(ctx, RaiseSpec, LaunchCreds) (string, error)` identical across T3/T5/placeholder; `CoveMasterOptions`/`LaunchCoveMaster` identical across T4/T5; `launcher.Config`/`New` T5/T7; `naming.CoveContainer` T5; `install.Manifest.Image/ImageDigest` (real fields) T7; `backend.DispatchOps.RunEphemeral(image,digest,name,label,dns,addHosts,docker)` matches the real signature.
- **Placeholder scan:** none — code steps carry full code; three "adapt to the real Fake/constructor API" notes point the implementer at the exact files to read (`internal/runner`, `internal/backend/colima`) rather than guessing names.
- **Ordering:** T3 (seam + placeholder update) precedes T5/T6 that depend on it; T4 precedes T5; T1 precedes T2; T5+install-read precede T7. Each task builds and tests on its own.
