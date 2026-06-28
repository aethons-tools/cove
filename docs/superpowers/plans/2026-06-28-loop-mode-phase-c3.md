# Loop Mode — Phase C-3: Headless Agent Run

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `connect.RunAgent` —
run Claude Code headlessly (`claude -p <prompt>`) in the workspace
with secrets (incl. `ANTHROPIC_API_KEY`) injected and no interactive login —
sharing a secret-safe, fail-fast injected-run helper with `RunSetup`.

**Architecture:** Extract the env-inject-then-run half of `RunSetup` into a `runInjected` helper
that writes the secret env to a `/dev/shm` script (values never on argv),
sources it, and runs a remote command with **empty stdin**
so a credential or other prompt fails fast instead of hanging (the Phase B review carry-in).
`RunSetup` is refactored onto it (behavior-preserving),
and `RunAgent` is a thin caller that runs `claude -p <prompt>`.
The setup completion-sentinel + `fresh-workspace` reset carry-in is deferred to C-4,
where the loop lifecycle decides interactive-vs-loop seeding.

**Tech Stack:** Go 1.22, standard library only; shell runs inside the sandbox VM over ssh.

## Global Constraints

- Go version floor `go 1.22`; standard library only — no new dependencies.
- **Secrets never on disk/argv**: env injected via a `/dev/shm` script sourced in the remote shell (the established `StdinScript`/`RunSetup` mechanism).
- The injected run uses **empty stdin** (`strings.NewReader("")`), not `os.Stdin`, so an unattended run fails fast instead of hanging on a prompt.
- `RunAgent` attempts **no interactive login**; it relies on `ANTHROPIC_API_KEY` being present in the injected env.
- `RunSetup`'s observable behavior (its 3 ssh calls and their argv) is **unchanged** by the refactor — existing `setup_test.go` tests must pass untouched.
- Hermetic tests (`runner.Fake`); follow the existing `internal/connect` test style.

---

### Task 1: `runInjected` helper, `RunSetup` refactor, and `RunAgent`

**Files:**
- Modify: `internal/connect/setup.go` (extract `runInjected`; refactor `RunSetup`)
- Create: `internal/connect/agent.go` (`RunAgent`)
- Test: `internal/connect/agent_test.go`

**Interfaces:**
- Consumes: `envScript`, `shellQuote`, `workspaceDir` (existing, `transport.go`); `sshargs.Base`; `runner.Runner`.
- Produces (relied on by C-4):
  - `func RunAgent(r runner.Runner, tgt sshargs.Target, env map[string]string, prompt string) error`
  - (internal) `func runInjected(r runner.Runner, tgt sshargs.Target, env map[string]string, label, remoteTail string) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/connect/agent_test.go`:

```go
package connect

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/runner"
)

func TestRunAgentInjectsAndRunsHeadless(t *testing.T) {
	f := &runner.Fake{}
	if err := RunAgent(f, rawTarget(), map[string]string{"ANTHROPIC_API_KEY": "sk-secret"}, "do the task"); err != nil {
		t.Fatal(err)
	}
	// Two ssh calls: write env to tmpfs, then run the agent.
	if len(f.Calls) != 2 {
		t.Fatalf("want 2 ssh calls, got %d: %+v", len(f.Calls), f.Calls)
	}
	for _, c := range f.Calls {
		for _, a := range c.Args {
			if strings.Contains(a, "sk-secret") {
				t.Fatalf("secret value leaked onto argv: %v", c.Args)
			}
		}
	}
	last := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	if !strings.Contains(last, "cd /home/agent/workspace && exec claude -p 'do the task'") {
		t.Fatalf("headless agent command wrong: %q", last)
	}
	// No interactive auth probe is run.
	if strings.Contains(last, "claude auth") {
		t.Fatalf("agent run must not touch auth: %q", last)
	}
}

func TestRunAgentShellQuotesPrompt(t *testing.T) {
	f := &runner.Fake{}
	if err := RunAgent(f, rawTarget(), nil, "a'b"); err != nil {
		t.Fatal(err)
	}
	last := f.Calls[1].Args[len(f.Calls[1].Args)-1]
	// shellQuote renders the embedded ' as '\'', so a'b becomes 'a'\''b' — one
	// argument to the remote shell.
	if !strings.Contains(last, `claude -p 'a'\''b'`) {
		t.Fatalf("prompt not shell-quoted as a single argument: %q", last)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `/usr/local/go/bin/go test ./internal/connect/ -run TestRunAgent`
Expected: FAIL — build error, `undefined: RunAgent`.

- [ ] **Step 3: Extract `runInjected` in `setup.go`**

In `internal/connect/setup.go`, add the helper (after the `setupEmptyMark` const, before `RunSetup`):

```go
// runInjected writes env to a tmpfs script over ssh (values arrive via stdin,
// never on argv), then runs remoteTail in a shell that sources and removes the
// script first. The run attaches EMPTY stdin, so a credential or other prompt
// fails fast instead of hanging an unattended caller. label distinguishes the
// tmpfs file and error messages across concurrent uses.
func runInjected(r runner.Runner, tgt sshargs.Target, env map[string]string, label, remoteTail string) error {
	file := fmt.Sprintf("/dev/shm/cove-%s-%s-%d", label, tgt.Host, tgt.Port)
	writeArgs := append(sshargs.Base(tgt), "umask 077; cat > "+file)
	if err := r.RunStdin(strings.NewReader(envScript(env)), "ssh", writeArgs...); err != nil {
		return fmt.Errorf("writing %s env: %w", label, err)
	}
	remote := "set -a; . " + file + "; rm -f " + file + "; " + remoteTail
	if err := r.RunStdin(strings.NewReader(""), "ssh", append(sshargs.Base(tgt), remote)...); err != nil {
		return fmt.Errorf("running %s: %w", label, err)
	}
	return nil
}
```

- [ ] **Step 4: Refactor `RunSetup` onto `runInjected`**

In `internal/connect/setup.go`, replace the body of `RunSetup` after the emptiness check with a `runInjected` call. The function becomes:

```go
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
	return runInjected(r, tgt, env, "setup", "cd "+workspaceDir+" && "+setupCmd)
}
```

The `/dev/shm` path is unchanged (`cove-setup-<host>-<port>` — `runInjected`'s `cove-<label>-<host>-<port>` with `label="setup"` renders identically), so the existing `setup_test.go` assertions still hold.

- [ ] **Step 5: Add `RunAgent` in `agent.go`**

Create `internal/connect/agent.go`:

```go
package connect

import (
	"github.com/aethons-tools/cove/internal/runner"
	"github.com/aethons-tools/cove/internal/sshargs"
)

// RunAgent runs Claude Code headlessly in the workspace — `claude -p <prompt>`
// — with secrets injected (including ANTHROPIC_API_KEY, which authenticates the
// unattended run; no interactive login is attempted). The prompt is shell-quoted
// so it reaches claude as a single argument. Output streams live; a non-zero
// claude exit is returned so the caller can record the run's outcome.
func RunAgent(r runner.Runner, tgt sshargs.Target, env map[string]string, prompt string) error {
	return runInjected(r, tgt, env, "agent", "cd "+workspaceDir+" && exec claude -p "+shellQuote(prompt))
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `/usr/local/go/bin/go test ./internal/connect/`
Expected: PASS — the two new `RunAgent` tests plus all existing connect tests (the `RunSetup` refactor is behavior-preserving; `setup_test.go` unchanged).

- [ ] **Step 7: Build, vet, gofmt, and commit**

Run: `/usr/local/go/bin/go build ./... && /usr/local/go/bin/go vet ./internal/connect/ && /usr/local/go/bin/gofmt -l internal/connect/`
Expected: success, no vet output, `gofmt -l` prints nothing.

```bash
git add internal/connect/setup.go internal/connect/agent.go internal/connect/agent_test.go
git commit -m "feat(connect): RunAgent headless claude -p; fail-fast injected-run helper"
```

---

## Self-Review

**Spec coverage (Phase C-3 slice):**
- Headless agent run `claude -p <prompt>` with secrets injected → Task 1 (`RunAgent`).
- `ANTHROPIC_API_KEY` authenticates unattended; no interactive login → `RunAgent` injects env, runs no auth probe; `TestRunAgentInjectsAndRunsHeadless` asserts no `claude auth`.
- Secrets never on argv → `runInjected` tmpfs path; test asserts no secret on argv.
- Fail-fast (Phase B carry-in) → `runInjected` uses empty stdin.
- Prompt safety → `shellQuote(prompt)`; `TestRunAgentShellQuotesPrompt`.

Deferred to C-4: loop config resolution, `createLoopInstance`, the drain-then-poll scheduler, the `loop` command (`--once`/`--keep`/`--interval`, keep-awake, auto-create/destroy), the setup completion-sentinel + `fresh-workspace` reset carry-in, and the C-2 carry-ins (interactive-destroy `rmi` guard, volume reclamation).

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `runInjected(r, tgt, env, label, remoteTail)` and `RunAgent(r, tgt, env, prompt)` signatures are defined once and consumed consistently; `RunSetup` keeps its signature. `envScript`, `shellQuote`, `workspaceDir` are existing `transport.go` symbols.
