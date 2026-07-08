# Reference Worker Kit + End-to-End Implementation Plan (Plan C of AET-21)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the reference worker kit (`run-worker.sh` + the `claude` agent harness), the `dispatchrun` sshd cold-start retry, and a worker-side end-to-end validation (gated integration test + runbook) — closing the AET-21 loop.

**Architecture:** A new `kits/reference-worker/` kit whose `dispatch.command` (`run-worker.sh`) runs `at-work prepare → agent(token-stripped) → at-work complete`; the agent (`run-agent.sh`) drives headless `claude -p` to do the work and write `.at-work/outcome.json`. `internal/dispatchrun` gains a bounded `waitForSSH` retry before it seeds credentials. Validation is a `//go:build integration`, env-gated test the maintainer runs on real infra (this sandbox has no docker/claude/GitHub), plus a runbook.

**Tech Stack:** Go 1.22 (stdlib only — **no new deps**); POSIX shell (kit scripts); `just` (recipe); Colima/Docker + `claude` + `gh` (maintainer's real infra, for the runbook run).

**Scope note:** Plan C of AET-21, building on Plan A (`at-cove dispatch`) and Plan B (scheduler rewire), both merged. Worker-side validation only (dispatch → kit → PR); the Linear→scheduler→dispatch seam stays unit-tested (Plan B).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new third-party Go dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- The reference kit is a **template** — the base image, the `claude` install, the pinned `at-work` ref, the target repo, and the full `allowed-domains` are the operator's to complete; the kit demonstrates the wiring and is validated by the runbook run, not hermetically.
- **Credential air-gap:** `run-worker.sh` runs the agent with `env -u AT_WORK_GIT_TOKEN` — the agent never sees the code-host token; only `at-work prepare`/`complete` do.
- **`image-files/` mirror container paths** (`image-files/usr/local/bin/X` → `/usr/local/bin/X`); setup-scripts are kit-relative under `image-files/`.
- **The agent writes `.at-work/outcome.json`** itself (OK/NEEDS_INPUT/ERROR); at-work's missing/invalid→ERROR is the safety net.
- **Hermetic tests only in this sandbox:** the `waitForSSH` retry and the kit-config parse are unit tests; the end-to-end test is `//go:build integration` and only compiles here (`go vet -tags integration`), never runs.
- Spec: [`docs/superpowers/specs/2026-07-08-at-cove-dispatch-design.md`](../specs/2026-07-08-at-cove-dispatch-design.md) §4.

---

## File Structure

- `internal/dispatchrun/dispatchrun.go` — add `waitForSSH`, wire into `Dispatch`.
- `internal/dispatchrun/dispatchrun_test.go` — `waitForSSH` unit tests (local flaky runner).
- `kits/reference-worker/config.yml` — the kit config.
- `kits/reference-worker/image-files/usr/local/bin/run-worker.sh` — the `dispatch.command`.
- `kits/reference-worker/image-files/usr/local/bin/run-agent.sh` — the agent harness.
- `kits/reference-worker/image-files/.install-files/install.sh` — the build-time toolchain install.
- `kits/reference-worker/testdata/input.json` — a sample dispatch input.
- `kits/reference-worker/RUNBOOK.md` — how to run the loop live.
- `internal/dispatchrun/e2e_integration_test.go` — the gated end-to-end test.
- `internal/kit/refkit_test.go` — hermetic parse test for the reference kit config.
- `justfile` — the `e2e` recipe.
- `docs/OVERVIEW.md` — pointer to the reference kit.

---

## Task 1: `dispatchrun` sshd cold-start retry

`Dispatch` dials sshd immediately after `RunEphemeral`, so the first ssh (the credential seed) can race container startup. Add a bounded `waitForSSH` probe before seeding.

**Files:**
- Modify: `internal/dispatchrun/dispatchrun.go`
- Test: `internal/dispatchrun/dispatchrun_test.go`

**Interfaces:**
- Produces: `waitForSSH(r runner.Runner, tgt sshargs.Target, attempts int, delay time.Duration, sleep func(time.Duration)) error`; consts `sshReadyAttempts`, `sshReadyDelay`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/dispatchrun/dispatchrun_test.go` (a local runner that fails its first N `Run` calls, embedding the stock fake for the other methods):

```go
// flakyRunner fails its first failFirst Run calls, then succeeds — for waitForSSH.
type flakyRunner struct {
	*runner.Fake
	failFirst int
	runs      int
}

func (f *flakyRunner) Run(name string, args ...string) error {
	f.runs++
	if f.runs <= f.failFirst {
		return errors.New("connection refused")
	}
	return nil
}

func TestWaitForSSHRetriesThenSucceeds(t *testing.T) {
	f := &flakyRunner{Fake: &runner.Fake{}, failFirst: 2}
	err := waitForSSH(f, sshargs.Target{Host: "h", Port: 22}, 5, time.Millisecond, func(time.Duration) {})
	if err != nil {
		t.Fatalf("waitForSSH: %v", err)
	}
	if f.runs != 3 {
		t.Fatalf("probed %d times; want 3 (2 fail + 1 success)", f.runs)
	}
}

func TestWaitForSSHExhausts(t *testing.T) {
	f := &flakyRunner{Fake: &runner.Fake{}, failFirst: 100}
	err := waitForSSH(f, sshargs.Target{Host: "h", Port: 22}, 3, time.Millisecond, func(time.Duration) {})
	if err == nil {
		t.Fatal("expected an error when sshd never comes up")
	}
	if f.runs != 3 {
		t.Fatalf("probed %d times; want exactly 3 attempts", f.runs)
	}
}
```

Add `"errors"` to the test imports if absent.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dispatchrun/ -run TestWaitForSSH`
Expected: FAIL to build — `undefined: waitForSSH`.

- [ ] **Step 3: Implement `waitForSSH` + wire it in**

In `internal/dispatchrun/dispatchrun.go`, add the consts (near `Label`) and the function:

```go
const (
	sshReadyAttempts = 10
	sshReadyDelay    = 2 * time.Second
)

// waitForSSH probes the VM's sshd with a trivial command until it succeeds or
// attempts are exhausted, sleeping delay between tries. The container's sshd may
// not be accepting connections the instant RunEphemeral returns. sleep is injected
// so tests run without real delay.
func waitForSSH(r runner.Runner, tgt sshargs.Target, attempts int, delay time.Duration, sleep func(time.Duration)) error {
	probe := append([]string{"-o", "ConnectTimeout=5"}, sshargs.Base(tgt)...)
	probe = append(probe, "true")
	var err error
	for i := 0; i < attempts; i++ {
		if err = r.Run("ssh", probe...); err == nil {
			return nil
		}
		if i < attempts-1 {
			sleep(delay)
		}
	}
	return fmt.Errorf("sshd not ready after %d attempts: %w", attempts, err)
}
```

In `Dispatch`, after the `sshargs.Target` (`tgt`) is built and **before** `seedFile`, insert:

```go
	if err := waitForSSH(o.R, tgt, sshReadyAttempts, sshReadyDelay, time.Sleep); err != nil {
		return err
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatchrun/`
Expected: PASS. The existing `TestDispatchHappyPath`/`TestDispatchRemovesContainerOnFailure` still pass — `waitForSSH` calls `o.R.Run("ssh", …)`, which the stock `runner.Fake` returns `nil` for (probe succeeds on the first try). If either test asserts an exact call count, adjust it to account for the one added `ssh … true` probe (they assert via substring `Contains`, which is unaffected — confirm).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatchrun/
git commit -m "feat(dispatchrun): retry sshd on container cold start before seeding"
```

---

## Task 2: the reference worker kit

The kit dir + scripts + a hermetic parse test. The scripts are complete but carry maintainer-fill markers for base-image specifics (validated by the runbook, not here).

**Files:**
- Create: `kits/reference-worker/config.yml`
- Create: `kits/reference-worker/image-files/usr/local/bin/run-worker.sh`
- Create: `kits/reference-worker/image-files/usr/local/bin/run-agent.sh`
- Create: `kits/reference-worker/image-files/.install-files/install.sh`
- Test: `internal/kit/refkit_test.go`

**Interfaces:**
- Consumes: `kit.ParseConfig([]byte) (kit.Config, error)`, `kit.Config.Dispatch.Command`, `.Secrets`, `.Image.Env`.

- [ ] **Step 1: Write the failing parse test**

Create `internal/kit/refkit_test.go`:

```go
package kit

import (
	"os"
	"testing"
)

// The reference worker kit must be a valid, dispatch-ready kit.
func TestReferenceWorkerKitConfig(t *testing.T) {
	data, err := os.ReadFile("../../kits/reference-worker/config.yml")
	if err != nil {
		t.Fatalf("read reference kit config: %v", err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if len(cfg.Dispatch.Command) != 1 || cfg.Dispatch.Command[0] != "run-worker.sh" {
		t.Errorf("dispatch.command = %v; want [run-worker.sh]", cfg.Dispatch.Command)
	}
	if cfg.Image.Env["AT_WORK_AGENT_COMMAND"] != "run-agent.sh" {
		t.Errorf("AT_WORK_AGENT_COMMAND = %q; want run-agent.sh", cfg.Image.Env["AT_WORK_AGENT_COMMAND"])
	}
	var found bool
	for _, s := range cfg.Secrets {
		if s.Name == "AT_WORK_GIT_TOKEN" && len(s.Command) > 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an AT_WORK_GIT_TOKEN secret with a resolver command; secrets=%v", cfg.Secrets)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/kit/ -run TestReferenceWorkerKitConfig`
Expected: FAIL — `read reference kit config: … no such file`.

- [ ] **Step 3: Create the kit config**

`kits/reference-worker/config.yml`:

```yaml
# Reference worker kit for `at-cove dispatch`. This is a TEMPLATE: complete the
# base image, the claude install, the pinned at-work ref, the target project's
# toolchain, and the full allowed-domains for your project. See RUNBOOK.md.
name: reference-worker
backend: colima

secrets:
  # Code-host token used ONLY by at-work prepare/complete (clone/push/PR). Resolved
  # on the host at dispatch, injected in memory. The agent step never sees it.
  - name: AT_WORK_GIT_TOKEN
    description: GitHub token for clone/push/PR
    command: ["gh", "auth", "token"]

image:
  setup-script:
    - .install-files/install.sh
  env:
    AT_WORK_AGENT_COMMAND: run-agent.sh
  allowed-domains:
    - api.anthropic.com   # the agent (claude)
    - api.github.com      # at-work PR API
    - github.com          # at-work clone/push
    # + your target project's dependency registries (e.g. proxy.golang.org, registry.npmjs.org)

dispatch:
  command: ["run-worker.sh"]
```

- [ ] **Step 4: Create the worker + agent scripts**

`kits/reference-worker/image-files/usr/local/bin/run-worker.sh`:

```sh
#!/bin/sh
# The kit's dispatch.command. at-cove runs this in the container with the kit's
# secrets in the environment and /in/input.json present. It sequences the git/PR
# worker around the agent, stripping the token for the agent step (the air-gap).
set -e

at-work prepare  /in/input.json

# The untrusted-brief-ingesting agent runs WITHOUT the code-host token.
env -u AT_WORK_GIT_TOKEN  run-agent.sh

at-work complete /in/input.json /out/output.json
```

`kits/reference-worker/image-files/usr/local/bin/run-agent.sh`:

```sh
#!/bin/sh
# The agent harness. at-work prepare has written the task to .at-work/brief.md and
# cloned the repo into the cwd. Drive headless claude to do the work and write its
# self-report to .at-work/outcome.json. at-work complete reads that file; a missing
# or invalid outcome is treated as ERROR, so this script never has to synthesize one.
set -e

brief=$(cat .at-work/brief.md)

claude -p --dangerously-skip-permissions "$(cat <<PROMPT
$brief

---
Do the work described above in this repository: make the changes and run the
project's tests. When you are finished, write your outcome to .at-work/outcome.json
as ONE of these JSON objects (and nothing else in that file):

  {"status":"OK","pr-message":"<a concise PR description of what you did>"}
  {"status":"NEEDS_INPUT","needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}
  {"status":"ERROR","message":"<what went wrong>"}

Use OK only if the change is complete and tests pass. Use NEEDS_INPUT if you are
blocked on a decision only a human can make. Do not push or open a PR yourself —
that is handled after you exit.
PROMPT
)"
```

`kits/reference-worker/image-files/.install-files/install.sh`:

```sh
#!/bin/sh
# Build-time toolchain install (runs as root; build-time egress is open). TEMPLATE:
# adjust for your base image and target project.
set -e

# 1) at-work — the git/PR worker. Pin a ref for reproducibility.
#    Requires Go on the build image (install it here if the base image lacks it).
go install github.com/aethons-tools/cove/cmd/at-work@main   # <-- pin to a tag/SHA

# 2) The agent CLI (claude). Install per your base image, e.g.:
#    npm install -g @anthropic-ai/claude-code
#    (left to the operator; the base image may already provide it.)

# 3) git and the target project's build toolchain — add here.

# The worker/agent scripts arrive via image-files at /usr/local/bin (already on PATH).
chmod 0755 /usr/local/bin/run-worker.sh /usr/local/bin/run-agent.sh
```

- [ ] **Step 5: Run the parse test + shell syntax check**

Run: `go test ./internal/kit/ -run TestReferenceWorkerKitConfig`
Expected: PASS.

Run: `for f in kits/reference-worker/image-files/usr/local/bin/run-worker.sh kits/reference-worker/image-files/usr/local/bin/run-agent.sh kits/reference-worker/image-files/.install-files/install.sh; do sh -n "$f" && echo "ok $f"; done`
Expected: `ok` for each (POSIX syntax valid).

- [ ] **Step 6: Commit**

```bash
git add kits/reference-worker/ internal/kit/refkit_test.go
git commit -m "feat(kits): reference worker kit (run-worker.sh + claude agent harness)"
```

---

## Task 3: end-to-end scaffold + runbook

The gated test + a sample input + the runbook + a `just` recipe. The test only compiles in this sandbox.

**Files:**
- Create: `internal/dispatchrun/e2e_integration_test.go`
- Create: `kits/reference-worker/testdata/input.json`
- Create: `kits/reference-worker/RUNBOOK.md`
- Modify: `justfile`

- [ ] **Step 1: Create the sample input**

`kits/reference-worker/testdata/input.json`:

```json
{
  "issue": {
    "key": "DEMO-1",
    "title": "Add a greeting helper",
    "work-class": "implement",
    "brief": "Add a function `Greet(name string) string` returning \"Hello, <name>!\" with a unit test. Keep it minimal."
  },
  "repo": {
    "name": "<your-org>/<scratch-repo>",
    "source-branch": "main",
    "work-branch": "implement/DEMO-1"
  }
}
```

- [ ] **Step 2: Create the gated end-to-end test**

`internal/dispatchrun/e2e_integration_test.go`:

```go
//go:build integration

package dispatchrun_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestE2EReferenceWorker runs the whole worker path against real infra: it shells
// `at-cove dispatch` on the reference kit and asserts a real PR was opened.
//
// Prerequisites (skipped without them): colima running, `gh auth` logged in, a
// seeded claude login (via a prior `at-cove connect` login), and a scratch repo.
// Set E2E_REPO=<org>/<repo> to enable. See kits/reference-worker/RUNBOOK.md.
func TestE2EReferenceWorker(t *testing.T) {
	repo := os.Getenv("E2E_REPO")
	if repo == "" {
		t.Skip("set E2E_REPO=<org>/<scratch-repo> to run the end-to-end dispatch")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "input.json")
	out := filepath.Join(dir, "output.json")

	input := `{"issue":{"key":"DEMO-1","title":"Add a greeting helper","work-class":"implement",` +
		`"brief":"Add Greet(name string) string returning \"Hello, <name>!\" with a test."},` +
		`"repo":{"name":"` + repo + `","source-branch":"main","work-branch":"implement/DEMO-1"}}`
	if err := os.WriteFile(in, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("at-cove", "dispatch", "kits/reference-worker", "--in", in, "--out", out, "--timeout", "20m")
	cmd.Dir = repoRoot(t)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("at-cove dispatch: %v", err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output.json: %v", err)
	}
	var res struct {
		Status string `json:"status"`
		Work   struct {
			PRURL string `json:"pr-url"`
		} `json:"work"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("parse output.json: %v\n%s", err, data)
	}
	if res.Status != "OK" {
		t.Fatalf("status = %q; want OK\n%s", res.Status, data)
	}
	if res.Work.PRURL == "" {
		t.Fatalf("no PR url in output\n%s", data)
	}
	t.Logf("opened PR: %s", res.Work.PRURL)
}

// repoRoot returns the module root (two levels up from this package).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}
```

- [ ] **Step 3: Create the runbook**

`kits/reference-worker/RUNBOOK.md`:

```markdown
# Reference worker — end-to-end runbook

Runs the whole worker path on real infra: `at-cove dispatch` → a fresh hardened
container → `claude` implements the task → `at-work` opens a PR. Cannot run in the
egress-locked dev sandbox (no docker/claude/GitHub); run it on a machine with:

## Prerequisites
- **Colima** running (`colima start`) — the `at-cove` backend.
- **`gh auth login`** — the `AT_WORK_GIT_TOKEN` resolver is `gh auth token`.
- **A seeded `claude` login** — run `at-cove connect` once and log in; `at-cove`
  saves the credentials and `dispatch` seeds them into the worker container.
- **A scratch GitHub repo** you can push branches / open PRs on, with a `main` branch.
- The kit **completed** for your target: base image, `claude` install, pinned
  `at-work` ref, target toolchain, and the full `allowed-domains` in `config.yml`.

## Run
```
E2E_REPO=<org>/<scratch-repo> go test -tags integration ./internal/dispatchrun/ -run TestE2EReferenceWorker -v
```
or, hand-run:
```
at-cove dispatch kits/reference-worker \
  --in kits/reference-worker/testdata/input.json --out /tmp/output.json --timeout 20m
```
(edit `testdata/input.json`'s `repo.name` to your scratch repo first).

## Expected
- A new PR on the scratch repo implementing the brief.
- `output.json` with `"status":"OK"` and `work.pr-url` set.
- A `NEEDS_INPUT` or `ERROR` status means the agent stopped or failed — read
  `output.json`'s `agent`/`work` blocks.
```

- [ ] **Step 4: Add the `just` recipe**

Read the existing `justfile` to match its style, then add:

```make
# End-to-end dispatch of the reference worker kit (needs real infra; see kits/reference-worker/RUNBOOK.md).
e2e:
    E2E_REPO=${E2E_REPO:?set E2E_REPO=<org>/<scratch-repo>} go test -tags integration ./internal/dispatchrun/ -run TestE2EReferenceWorker -v
```

- [ ] **Step 5: Verify it compiles (does not run here)**

Run: `go vet -tags integration ./internal/dispatchrun/`
Expected: no errors (the gated test compiles).

Run: `go test ./...`
Expected: all pass (the integration test is excluded by default).

- [ ] **Step 6: Commit**

```bash
git add internal/dispatchrun/e2e_integration_test.go kits/reference-worker/testdata/ kits/reference-worker/RUNBOOK.md justfile
git commit -m "test(dispatchrun): worker-side end-to-end scaffold + runbook"
```

---

## Task 4: docs

**Files:**
- Modify: `docs/OVERVIEW.md`

- [ ] **Step 1: Point to the reference kit**

In `docs/OVERVIEW.md`, near the `at-cove dispatch` entry (or the architecture/kit section), add a one-line pointer: the reference worker kit lives at `kits/reference-worker/` and its `RUNBOOK.md` shows the end-to-end run. Keep it terse; match the surrounding style.

- [ ] **Step 2: Verify**

Run: `grep -n "reference-worker" docs/OVERVIEW.md`
Expected: the pointer is present.

Run: `go test ./... && gofmt -l cmd/ internal/`
Expected: pass; clean.

- [ ] **Step 3: Commit**

```bash
git add docs/OVERVIEW.md
git commit -m "docs: point to the reference worker kit + runbook"
```

---

## Final verification

- [ ] `go test ./...` — all pass.
- [ ] `go vet -tags integration ./internal/dispatchrun/` — the gated E2E test compiles.
- [ ] `just build` — all binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `gofmt -l cmd/ internal/` prints nothing; `sh -n` clean on the three kit scripts.
- [ ] The live end-to-end run is a maintainer step (RUNBOOK.md) — not run in this sandbox.

## Notes

- **Two implementer reconciliations** (both read-and-match): (1) confirm the existing `TestDispatchHappyPath`/`TestDispatchRemovesContainerOnFailure` still pass after `waitForSSH` is wired in (they should — the stock fake's `Run` returns nil; adjust only if one asserts an exact call count); (2) match the `justfile`'s recipe style (tabs vs. indentation, variable syntax).
- **The kit is a template.** `install.sh`/`config.yml` carry maintainer-fill markers (base image, `claude` install, pinned `at-work` ref, target toolchain, full egress list). The hermetic parse test guards the dispatch contract (`dispatch.command`, the token secret, `AT_WORK_AGENT_COMMAND`); the scripts' real validation is the runbook run.
- **No agent output-format dependency in code:** the agent writes `.at-work/outcome.json`; at-work's `complete` already defends against a missing/invalid file (→ ERROR), so a non-compliant agent degrades gracefully rather than breaking the pipeline.
