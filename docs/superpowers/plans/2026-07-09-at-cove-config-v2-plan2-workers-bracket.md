# at-cove config v2 — Plan 2: workers + host-orchestrated bracket Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the kit's `dispatch:` block with a `workers` map (`class → { prompt }`) and rewrite `internal/dispatchrun` so **at-cove itself** drives the `prepare → agent → complete` bracket step-by-step over ssh, with the code-host token air-gapped from the agent step.

**Architecture:** `at-cove dispatch` no longer runs a kit-authored `run-worker.sh`. `dispatchrun` reads `worker.class` from the injected task, looks up `workers[class].prompt`, and runs three ssh steps: `at-work prepare` (env **with** `AT_WORK_GIT_TOKEN`), `claude -p "<prompt + result protocol>"` (env **without** the token), `at-work complete` (env **with** the token). Because each ssh command is its own shell and each step writes/​removes its own tmpfs env file, the token is only ever transmitted to the VM for the two git steps — never present during the agent step. Three green tasks: add `workers` (additive), rewrite the bracket, then remove `DispatchConfig`.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**.

**Scope note:** Plan 2 of 3 for [AET-29](https://linear.app/aethons-tools/issue/AET-29), on branch `feat/at-cove-config-v2` (builds on Plan 1). Canonical target: [`docs/usage/at-cove-config.md`](../../usage/at-cove-config.md) (the `workers` section) and the spec [`2026-07-09-at-cove-config-v2-design.md`](../specs/2026-07-09-at-cove-config-v2-design.md) §3.

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies**.
- **Token air-gap (SECURITY, the core requirement):** the secret literally named `AT_WORK_GIT_TOKEN` is included in the env for `prepare`/`complete` and **withheld** from the agent step. Every *other* kit secret is present throughout. Values flow via a tmpfs env file over ssh **stdin** (never argv/logs), per step.
- **Bracket semantics match the old `run-worker.sh`:** a failed `prepare` skips the agent but `complete` still runs; the agent's nonzero exit is tolerated so `complete` always runs (`at-work complete` always writes a `task-result`, per AET-28).
- **VM workspace is at-cove-owned:** fixed `/home/agent/work` with `.at-work/`; no kit `input`/`output` config.
- **Every commit builds + `go test ./...` green.** After each task: `go build ./... && go vet ./... && go test ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.
- Hermetic tests only — `runner.Fake`, no VM/network.

---

## Task 1: add the `workers` config (additive)

**Files:** `internal/kit/config.go`, `internal/kit/config_test.go`, `internal/kit/refkit_test.go`, `kits/reference-worker/config.yml`

**Interfaces:** Produces `kit.Worker{Prompt string}` and `Config.Workers map[string]Worker` (key = handler class). `DispatchConfig` + `Config.Dispatch` are KEPT (Task 3 removes them).

- [ ] **Step 1: Failing test for parsing `workers`**

In `internal/kit/config_test.go` add:
```go
func TestParseConfigWorkers(t *testing.T) {
	cfg, err := ParseConfig([]byte("name: k\nworkers:\n  implement:\n    prompt: do the thing\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Workers["implement"].Prompt != "do the thing" {
		t.Fatalf("workers not parsed: %+v", cfg.Workers)
	}
}

func TestParseConfigRejectsWorkerWithoutPrompt(t *testing.T) {
	_, err := ParseConfig([]byte("name: k\nworkers:\n  implement: {}\n"))
	if err == nil {
		t.Fatal("a worker class with no prompt must be rejected")
	}
}
```
Run `go test ./internal/kit/ -run Workers` → FAIL (`cfg.Workers` undefined).

- [ ] **Step 2: Add the type, field, and validation**

In `internal/kit/config.go`:
```go
// Worker declares a dispatch worker class: the prompt at-cove sends the agent when
// at-cove dispatch runs this class. at-cove wraps it in the standard at-work bracket.
type Worker struct {
	Prompt string `yaml:"prompt"`
}
```
Add to `Config`: `Workers map[string]Worker \`yaml:"workers"\``. In `ParseConfig`, add:
```go
for class, w := range cfg.Workers {
	if strings.TrimSpace(class) == "" {
		return Config{}, fmt.Errorf("config.yml: workers: a class name (map key) must not be empty")
	}
	if strings.TrimSpace(w.Prompt) == "" {
		return Config{}, fmt.Errorf("config.yml: workers[%q]: prompt is required", class)
	}
}
```

- [ ] **Step 3: Add a `workers` block to the reference kit (keep `dispatch:` for now)**

In `kits/reference-worker/config.yml`, add (leave the existing `dispatch:` block — Task 3 removes it):
```yaml
workers:
  implement:
    prompt: |
      You are an implementer. Make the change described in the task and run the
      project's tests. Keep the change minimal and focused.
```
If `internal/kit/refkit_test.go` asserts the parsed config, add an assertion that `cfg.Workers["implement"].Prompt` is non-empty (optional but nice).

- [ ] **Step 4: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean.
```bash
git commit -am "feat(kit): add workers map (class -> prompt)"
```

---

## Task 2: rewrite `dispatchrun` as the host-orchestrated bracket

**Files:** `internal/dispatchrun/dispatchrun.go`, `internal/dispatchrun/dispatchrun_test.go`

**Interfaces:** Consumes `kit.Config.Workers`. `Dispatch(Options)` now resolves the class from the task and runs the three-step bracket. `Options` loses its reliance on `Cfg.Dispatch` (the struct still exists until Task 3).

- [ ] **Step 1: Rewrite the hermetic tests (fail first)**

Rewrite the three `Dispatch` tests in `internal/dispatchrun/dispatchrun_test.go`. Key points — the fake `runner.Fake` records `Calls` (each `{Name, Args, Stdin}`) and serves `Outputs` in call order. The test package is `dispatchrun`, so it can reference the unexported `envVMPath`, `resultVMPath`, `taskClass`, etc.

`TestDispatchRunsBracketWithClassPrompt`:
```go
func TestDispatchRunsBracket(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	r := &runner.Fake{}
	setOutputForCat(r, `{"status":{"ok":{}}}`) // the final `cat …/task-result.json`
	ops := &fakeOps{}

	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:      kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		BuildDir: dir, Name: "disp-1",
		InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	calls := allCalls(r)
	// the three bracket steps ran, in order, cd'd to the workdir
	for _, want := range []string{"at-work prepare", "claude -p", "at-work complete"} {
		if !strings.Contains(calls, want) {
			t.Fatalf("missing bracket step %q:\n%s", want, calls)
		}
	}
	if strings.Index(calls, "at-work prepare") > strings.Index(calls, "claude -p") ||
		strings.Index(calls, "claude -p") > strings.Index(calls, "at-work complete") {
		t.Fatalf("bracket steps out of order:\n%s", calls)
	}
	if !strings.Contains(calls, "cat "+resultVMPath) {
		t.Fatalf("did not extract the result:\n%s", calls)
	}
	if b := readFile(t, out); !strings.Contains(b, `"ok"`) {
		t.Fatalf("result not written: %q", b)
	}
}
```

`TestDispatchAirGapsTokenFromAgent` (THE security test):
```go
func TestDispatchAirGapsTokenFromAgent(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"implement"}}`)
	out := dir + "/task-result.json"
	const tok = "ghp-secret-token-value"
	// secret.Resolve calls r.Output per resolver (in name order: AT_WORK_GIT_TOKEN, OTHER),
	// then the final `cat` for the result.
	r := &runner.Fake{Outputs: []runner.FakeResult{
		{Stdout: tok + "\n"},          // AT_WORK_GIT_TOKEN resolver
		{Stdout: "other-value\n"},     // OTHER resolver
		{Stdout: `{"status":{"ok":{}}}`}, // cat result
	}}
	ops := &fakeOps{}
	err := Dispatch(Options{
		Ops: ops, R: r,
		Cfg:      kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		Secrets: []secret.Spec{
			{Name: "AT_WORK_GIT_TOKEN", Command: []string{"gh", "auth", "token"}},
			{Name: "OTHER", Command: []string{"echo", "x"}},
		},
		BuildDir: dir, Name: "disp-ag", InputPath: in, OutputPath: out,
		IdentityFile: "id", KnownHostsDir: t.TempDir(),
		Timeout: 30 * time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// the env-file writes, in call order, are prepare / agent / complete.
	var envWrites []string
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), "cat > "+envVMPath) {
			envWrites = append(envWrites, c.Stdin)
		}
	}
	if len(envWrites) != 3 {
		t.Fatalf("want 3 env-file writes (prepare/agent/complete); got %d", len(envWrites))
	}
	if !strings.Contains(envWrites[0], tok) {
		t.Fatal("prepare env must carry AT_WORK_GIT_TOKEN")
	}
	if strings.Contains(envWrites[1], tok) {
		t.Fatal("AIR-GAP BREACH: the agent step's env carried AT_WORK_GIT_TOKEN")
	}
	if !strings.Contains(envWrites[1], "other-value") {
		t.Fatal("agent step should still carry other secrets")
	}
	if !strings.Contains(envWrites[2], tok) {
		t.Fatal("complete env must carry AT_WORK_GIT_TOKEN")
	}
	// and the token must never appear on any argv
	for _, c := range r.Calls {
		if strings.Contains(strings.Join(c.Args, " "), tok) {
			t.Fatalf("token leaked onto argv: %v", c.Args)
		}
	}
}
```

`TestDispatchUndeclaredClassErrors`:
```go
func TestDispatchUndeclaredClassErrors(t *testing.T) {
	dir := t.TempDir()
	in := writeFile(t, dir, "task.json", `{"worker":{"class":"nope"}}`)
	err := Dispatch(Options{
		Ops: &fakeOps{}, R: &runner.Fake{},
		Cfg:      kit.Config{Name: "w", Workers: map[string]kit.Worker{"implement": {Prompt: "do it"}}},
		BuildDir: dir, Name: "x", InputPath: in, OutputPath: dir + "/o.json",
		IdentityFile: "id", KnownHostsDir: t.TempDir(), Timeout: time.Minute, GraceWindow: time.Hour, Now: time.Now(),
	})
	if err == nil {
		t.Fatal("expected an error for an undeclared worker class")
	}
}
```

Keep the `waitForSSH` tests. Update `TestDispatchRemovesContainerOnFailure` to the new Cfg (`Workers`, a task.json input); it should still fail (no `cat` output → extraction fails) and assert `ops.removed`. Update the helper comments that mention `/out/output.json`. Run `go test ./internal/dispatchrun/` → FAIL (Dispatch still uses `Cfg.Dispatch`).

- [ ] **Step 2: Rewrite `dispatchrun.go`**

Update the package doc (it now reads `worker.class` from the input). Add `gopkg.in/yaml.v3` to imports. Replace the constants block and `Dispatch`, and replace `runWork` with `runStep` + helpers:

```go
const (
	workDir      = "/home/agent/work"
	taskVMPath   = workDir + "/.at-work/task.json"
	resultVMPath = workDir + "/.at-work/task-result.json"
	promptVMPath = "/dev/shm/at-cove-prompt"
	gitTokenEnv  = "AT_WORK_GIT_TOKEN" // withheld from the agent step (the air-gap)
)

// Dispatch runs one unit of work: resolve the class → build → ephemeral run → inject the
// task → prepare/agent/complete bracket (token withheld from the agent) → extract → destroy.
func Dispatch(o Options) error {
	input, err := os.ReadFile(o.InputPath)
	if err != nil {
		return err
	}
	class, err := taskClass(input)
	if err != nil {
		return err
	}
	w, ok := o.Cfg.Workers[class]
	if !ok {
		return fmt.Errorf("kit %q declares no worker class %q", o.Cfg.Name, class)
	}

	_, _ = o.Ops.ScavengeLabeled(Label, o.GraceWindow, o.Now)

	env, err := secret.Resolve(o.R, o.Secrets) // fail closed before creating anything
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
	defer o.Ops.RemoveContainer(o.Name)

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
	if err := waitForSSH(o.R, tgt, sshReadyAttempts, sshReadyDelay, time.Sleep); err != nil {
		return err
	}
	if err := seedFile(o.R, tgt, o.CredentialsFile, credsVMPath); err != nil {
		return fmt.Errorf("seed agent credentials: %w", err)
	}
	if err := writeVM(o.R, tgt, input, taskVMPath); err != nil {
		return fmt.Errorf("inject task: %w", err)
	}

	// The bracket. prepare gates the agent; complete always runs (at-work complete
	// always writes a task-result). The agent runs WITHOUT the code-host token.
	if err := runStep(o.R, tgt, env, "at-work prepare", o.Timeout); err == nil {
		if err := writeVM(o.R, tgt, []byte(agentPrompt(w.Prompt)), promptVMPath); err != nil {
			return err
		}
		agentCmd := fmt.Sprintf("claude -p --dangerously-skip-permissions \"$(cat %s)\"", shellQuote(promptVMPath))
		_ = runStep(o.R, tgt, withoutToken(env), agentCmd, o.Timeout) // tolerate agent failure
	}
	if err := runStep(o.R, tgt, env, "at-work complete", o.Timeout); err != nil {
		return fmt.Errorf("at-work complete: %w", err)
	}

	out, err := o.R.Output("ssh", append(sshargs.Base(tgt), "cat "+resultVMPath)...)
	if err != nil {
		return fmt.Errorf("extract result at %s: %w", resultVMPath, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("dispatch produced no result at %s", resultVMPath)
	}
	return os.WriteFile(o.OutputPath, []byte(out), 0o600)
}

// runStep sources the given env from a tmpfs file (never on argv), removes it, cds to the
// workdir, and runs command under a timeout.
func runStep(r runner.Runner, tgt sshargs.Target, env map[string]string, command string, timeout time.Duration) error {
	if err := writeVM(r, tgt, []byte(envScript(env)), envVMPath); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 1800
	}
	remote := fmt.Sprintf("set -a; . %s; rm -f %s; cd %s; timeout %d %s",
		envVMPath, envVMPath, shellQuote(workDir), secs, command)
	return r.RunStdin(nil, "ssh", append(sshargs.Base(tgt), remote)...)
}

// withoutToken returns env minus the code-host token — the agent's air-gapped env.
func withoutToken(env map[string]string) map[string]string {
	out := make(map[string]string, len(env))
	for k, v := range env {
		if k == gitTokenEnv {
			continue
		}
		out[k] = v
	}
	return out
}

// taskClass extracts worker.class from the task (JSON or YAML) so at-cove can pick the prompt.
func taskClass(input []byte) (string, error) {
	var head struct {
		Worker struct {
			Class string `yaml:"class"`
		} `yaml:"worker"`
	}
	if err := yaml.Unmarshal(input, &head); err != nil {
		return "", fmt.Errorf("read task worker.class: %w", err)
	}
	if head.Worker.Class == "" {
		return "", fmt.Errorf("task declares no worker.class")
	}
	return head.Worker.Class, nil
}

// agentPrompt joins the class's role prompt with the standard worker-result protocol.
func agentPrompt(classPrompt string) string { return classPrompt + "\n\n" + resultProtocol }

const resultProtocol = `---
Your task is specified in .at-work/task.json in the current directory (the "task" -> "brief"
field is your instructions; "repo" describes the checked-out repository, already cloned into
the cwd on the work branch). Do the work: make the changes and run the project's tests.

When finished, write your result to .at-work/worker-result.json as EXACTLY ONE of:
  {"status":{"ok":{"pull-request":{"title":"<PR title>","message":"<PR description>"}}}}
  {"status":{"needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}}
  {"status":{"error":{"message":"<what went wrong>"}}}
Use ok only if the change is complete and tests pass (omit "pull-request" to push without a PR).
Do NOT push or open a PR yourself — that is handled after you exit.`
```

Delete the old `runWork`. Keep `envScript`, `shellJoin` (if still used — `shellJoin` is now unused; remove it if `go vet`/build flags it), `shellQuote`, `writeVM`, `seedFile`, `waitForSSH`, `envScript`. (`shellJoin` was only used by the old `runWork` — remove it and any now-unused import.)

- [ ] **Step 3: Run + commit**

`go test ./internal/dispatchrun/ && go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean. `DispatchConfig` is now unused by `dispatchrun` but still defined (Task 3).
```bash
git commit -am "feat(dispatch): host-orchestrated worker bracket (token air-gapped from the agent)"
```

---

## Task 3: cmd cleanup + remove `DispatchConfig`

**Files:** `cmd/at-cove/main.go`, `internal/kit/config.go`, `kits/reference-worker/config.yml`, and any remaining `kit.DispatchConfig` fixture

- [ ] **Step 1: Rewire `doDispatch`**

In `cmd/at-cove/main.go` `doDispatch`: replace the two `len(cfg.Dispatch.Command) == 0` guards (the dry-run one and the pre-assemble one) with a workers check:
```go
if len(cfg.Workers) == 0 {
	fmt.Fprintf(stderr, "at-cove: kit %q declares no workers\n", cfg.Name)
	return 1
}
```
Update the dry-run message to drop `cfg.Dispatch.Command` — e.g.:
```go
fmt.Fprintf(stdout, "would dispatch %s (kit-dir %s, image %s): scavenge orphans, build image, run an ephemeral labeled container, inject %s, run the at-work worker bracket (prepare → agent → complete), extract %s, then destroy the container\n",
	cfg.Name, kitDir, img, *inPath, *outPath)
```

- [ ] **Step 2: Remove `DispatchConfig`**

In `internal/kit/config.go` delete the `DispatchConfig` type and the `Dispatch DispatchConfig \`yaml:"dispatch"\`` field from `Config`. (`ParseConfig` is `KnownFields(true)`, so a stray `dispatch:` now errors as unknown — which is why Step 3 must clear the reference kit's block.)

- [ ] **Step 3: Remove the reference kit's `dispatch:` block**

In `kits/reference-worker/config.yml`, delete the `dispatch:` block (leave `workers:`). Leave `run-worker.sh`/`run-agent.sh`/`AT_WORK_AGENT_COMMAND` for now — they're unused but harmless; Plan 3 deletes them.

- [ ] **Step 4: Fix any remaining `DispatchConfig` fixtures**

Grep: `grep -rn "DispatchConfig\|\.Dispatch\b\|dispatch:" cmd/ internal/ kits/ .at-cove/ --include=*.go --include=*.yml`. Any leftover `kit.Config{… Dispatch: …}` literal or `dispatch:` YAML (in `config_test.go`, `refkit_test.go`, `main_test.go`) must be removed. (Task 2 already rewrote `dispatchrun_test.go`.)

- [ ] **Step 5: Run + commit**

`go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/` clean; `go.mod` unchanged.
```bash
git commit -am "refactor(kit): remove DispatchConfig; at-cove dispatch runs the workers bracket"
```

---

## Final verification

- [ ] `go test ./...` passes; `go build ./...`, `go vet ./...` clean; `gofmt -l cmd/ internal/` empty; `go.mod` unchanged.
- [ ] `grep -rn "DispatchConfig\|cfg.Dispatch\|dispatch.command\|dispatch.input" cmd/ internal/ --include=*.go` — nothing.
- [ ] `just build` — three binaries build.
- [ ] The config surface is now `{name, secrets, workers, image}` — matching [`docs/usage/at-cove-config.md`](../../usage/at-cove-config.md) field-for-field (the `dispatch`→`workers` seam from Plan 1 is closed).
- [ ] **Air-gap test passes:** the agent step's env file omits `AT_WORK_GIT_TOKEN` while prepare/complete include it; the token never on argv (`TestDispatchAirGapsTokenFromAgent`).
- [ ] The reference kit still parses (`refkit_test.go`) with `workers:` and no `dispatch:`.

## Notes

- **Reconciliations** (read-and-match; line numbers drift): the current `dispatchrun_test.go` helpers (`writeFile`/`readFile`/`setOutputForCat`/`allCalls`/`fakeOps`) — reuse them; `doDispatch`'s exact dry-run/guard text; whether `shellJoin` becomes unused after `runWork` is removed (remove it if so). `secret.Resolve`'s call order (it runs resolvers in the order of the `Secrets` slice) determines the `Outputs` queue order in the air-gap test.
- **Timeout:** `o.Timeout` bounds each step; `prepare`/`complete` finish well under it, the agent gets the full budget. Acceptable (the effective bound is the agent's).
- **Still deferred to Plan 3:** deleting the reference kit's `run-worker.sh`/`run-agent.sh`/`AT_WORK_AGENT_COMMAND`, the e2e update, and the `OVERVIEW.md`/`orchestration` doc updates (the command surface still describes `dispatch.command`). Also still open from Plan 1: the `--loop` flag + `state` loop helpers cleanup.
- **Why the class is read in `dispatchrun` (not `cmd`):** `dispatchrun` already holds `Cfg` and the input bytes and owns the bracket policy; reading `worker.class` there keeps `cmd/at-cove` thin and makes the resolution hermetically testable.
