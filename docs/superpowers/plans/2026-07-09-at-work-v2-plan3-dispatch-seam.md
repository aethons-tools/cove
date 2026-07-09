# at-work contract v2 — Plan 3: dispatch seam + reference kit Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the real `at-cove dispatch` loop v2-shaped end-to-end: `at-work complete` always writes a `task-result` (even when it can't read its inputs); `at-work prepare` sets up the repo **in place** alongside the pre-injected `.at-work/`; the dispatch seam injects/extracts kit-configured `.at-work/` files; and the reference kit points the worker at `.at-work/task.json` and emits the tagged-union `worker-result.json`.

**Architecture:** Three seams change together. (1) **at-work robustness** — `complete` guarantees a `task-result` file always exists for the orchestrator to read, defaulting to JSON when the task file is unreadable; and `EnsureClean` inits the repo **in place** (`git init` + `git remote add`, not `git clone`) so the task file injected into `.at-work/` before prepare doesn't break the clone, excluding `.at-work/` from git so its files never get committed. (2) **dispatch seam** — the kit's `dispatch:` block gains VM-side `input`/`output` paths; `internal/dispatchrun` uses them instead of the hardcoded `/in/input.json` / `/out/output.json`. (3) **reference kit** — `run-worker.sh` drops path args and `cd`s to the workdir; `run-agent.sh` points the worker at `.at-work/task.json` and writes the tagged-union `worker-result.json`.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**. POSIX `sh` for the kit scripts.

**Scope note:** Plan 3 of [AET-28](https://linear.app/aethons-tools/issue/AET-28), building on the merged Plans 1–2. The scheduler still uses the old flat types — Plan 4 migrates it and deletes them. After Plan 3 the loop is v2 end-to-end *except* the scheduler, which still writes the old `input.json` shape; the reference kit's e2e is maintainer-run and validates the full v2 path once Plan 4 lands. Canonical contract: [`docs/usage/at-work.md`](../../usage/at-work.md), [`docs/usage/at-work-inputs.md`](../../usage/at-work-inputs.md), [`docs/usage/at-work-output.md`](../../usage/at-work-output.md).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- **Do NOT touch the old flat types** (`Input`/`Outcome`/`Output`/`ReadInput`/`ReadOutcome`/`WriteOutput`, `Status*`) — the scheduler still imports them; Plan 4 deletes them.
- **Secrets air-gap unchanged** — `AT_WORK_GIT_TOKEN` reaches only `prepare`/`complete` (clone/push/PR), never the agent step, never on argv/logs. `run-worker.sh` strips it for the agent (`env -u`). Don't regress this.
- **Hardening layer wins** — nothing here touches `internal/assemble/hardening/`. The reference kit is an overridable/user kit under `kits/`.
- **`complete` exit codes:** 2 = misuse (extra args); 1 = the task-result **write** itself failed (nothing to deliver); 0 = a task-result was written, *regardless of its status* (ok/needs-input/error). This is what lets the orchestrator always read a result.
- **Hermetic tests by default** — `dispatchrun` drives `runner.Fake`; at-work uses `fakeGit` except the one real-`git` test in Task 2, which **skips if `git` is not on `PATH`** and never hits the network.
- **Template-vs-repo line:** files under `kits/reference-worker/` are *payload* (a kit copied into dispatch VMs), not this repo's config. Edit them as the worker template; keep repo docs (`docs/`) in sync separately.

---

## Task 1: `at-work complete` always writes a `task-result` (default JSON)

Closes the deferred Minor from Plan 2: `complete` currently exits 1 without writing anything when it can't read `.at-work/task.json`. It must instead write a JSON `task-result` with an `error` status so the orchestrator always gets a result.

**Files:**
- Modify: `internal/dispatch/worker/format.go` (`encodeFile` — create the dir)
- Modify: `internal/dispatch/worker/resultv2.go` (add `ErrorResult`)
- Modify: `internal/dispatch/worker/complete.go` (refactor `taskErr` onto `ErrorResult`)
- Modify: `cmd/at-work/main.go` (`doComplete`)
- Modify: `internal/dispatch/worker/resultv2_test.go` (or the file where result tests live) and `cmd/at-work/main_test.go`
- Modify: `docs/usage/at-work.md`

**Interfaces:**
- Produces: `worker.ErrorResult(message, detail string) TaskResult` — an `error`-status `TaskResult` with no `worker-result` echo. `encodeFile` now creates the parent dir.

- [ ] **Step 1: Failing test — `encodeFile`/`WriteTaskResult` creates `.at-work/`**

In the worker result test file (e.g. `resultv2_test.go`), add:

```go
func TestWriteTaskResultCreatesDir(t *testing.T) {
	dir := t.TempDir() // no .at-work/ yet
	tr := ErrorResult("boom", "detail")
	if err := WriteTaskResult(dir, ".json", tr); err != nil {
		t.Fatalf("WriteTaskResult: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".at-work", "task-result.json")); err != nil {
		t.Fatalf("task-result not written: %v", err)
	}
}

func TestErrorResult(t *testing.T) {
	tr := ErrorResult("msg", "det")
	v, err := tr.Status.ActiveTask()
	if err != nil || v != "error" {
		t.Fatalf("variant = %q err=%v; want error", v, err)
	}
	if tr.Status.Error.Message != "msg" || tr.Status.Error.Detail != "det" {
		t.Fatalf("error = %+v", tr.Status.Error)
	}
	if tr.WorkerResult != nil {
		t.Fatalf("ErrorResult must not echo a worker-result: %v", tr.WorkerResult)
	}
}
```

Ensure the test file imports `os`, `path/filepath`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run 'WriteTaskResultCreatesDir|ErrorResult'`
Expected: FAIL — `ErrorResult` undefined; `WriteTaskResult` fails because `.at-work/` doesn't exist.

- [ ] **Step 3: `encodeFile` creates the dir**

In `internal/dispatch/worker/format.go`, in `encodeFile`, before `os.WriteFile(path, out, 0o600)`:

```go
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
```

(`format.go` already imports `os` and `path/filepath`.)

- [ ] **Step 4: Add `ErrorResult` and refactor `taskErr`**

In `internal/dispatch/worker/resultv2.go`, add:

```go
// ErrorResult builds an ERROR TaskResult with no worker-result echo — for failures
// at-work hits before or around the worker (e.g. it cannot read the task file).
func ErrorResult(message, detail string) TaskResult {
	e := &TaskError{Message: message}
	if detail != "" {
		e.Detail = detail
	}
	return TaskResult{Status: TaskStatus{Error: e}}
}
```

In `internal/dispatch/worker/complete.go`, replace the body of `taskErr` so it reuses `ErrorResult` (behavior identical — an error result that also carries the raw echo):

```go
func taskErr(raw any, msg, detail string) TaskResult {
	tr := ErrorResult(msg, detail)
	tr.WorkerResult = raw
	return tr
}
```

- [ ] **Step 5: Run to verify green**

Run: `go test ./internal/dispatch/worker/`
Expected: PASS (new tests + all existing).

- [ ] **Step 6: Failing test — `complete` writes a result when the task is unreadable**

In `cmd/at-work/main_test.go`, add (adapt to the file's harness; this uses `os.Chdir` since `doComplete` operates on cwd — Go 1.22 has no `t.Chdir`):

```go
func TestCompleteWritesResultWhenTaskUnreadable(t *testing.T) {
	dir := t.TempDir() // no .at-work/task.json
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)

	var stdout, stderr bytes.Buffer
	code := run([]string{"complete"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (a task-result was written)\nstderr: %s", code, stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, ".at-work", "task-result.json"))
	if err != nil {
		t.Fatalf("task-result.json not written: %v", err)
	}
	if !strings.Contains(string(data), `"error"`) {
		t.Fatalf("expected an error status:\n%s", data)
	}
}
```

Ensure imports: `bytes`, `os`, `path/filepath`, `strings`.

- [ ] **Step 7: Run to verify it fails**

Run: `go test ./cmd/at-work/ -run TestCompleteWritesResultWhenTaskUnreadable`
Expected: FAIL — current `doComplete` returns 1 and writes nothing when `ReadTask` fails.

- [ ] **Step 8: Rewrite `doComplete`**

In `cmd/at-work/main.go`, replace `doComplete` with (and add a small `writeResult` helper):

```go
func doComplete(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "at-work complete: takes no arguments (reads .at-work/, writes .at-work/task-result)")
		return 2
	}
	// Resolve the task-result extension up front, defaulting to JSON, so we can
	// ALWAYS write a task-result — even when the task file is missing/unreadable.
	ext, err := worker.TaskExt(".")
	if err != nil {
		ext = ".json"
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		return writeResult(stderr, ext, worker.ErrorResult("at-work could not read the task", err.Error()))
	}
	g, err := worker.NewShellGit(os.Getenv("AT_WORK_GIT_TOKEN"))
	if err != nil {
		return writeResult(stderr, ext, worker.ErrorResult("at-work could not initialize git", err.Error()))
	}
	ch := github.New(os.Getenv("AT_WORK_GIT_TOKEN"), nil)
	tr := worker.Complete(context.Background(), ".", task, g, ch)
	return writeResult(stderr, ext, tr)
}

// writeResult writes tr to .at-work/task-result<ext>. Exit 1 ONLY if the write itself
// fails (there is then no result to deliver); otherwise 0, whatever tr's status is.
func writeResult(stderr io.Writer, ext string, tr worker.TaskResult) int {
	if err := worker.WriteTaskResult(".", ext, tr); err != nil {
		fmt.Fprintf(stderr, "at-work complete: write task-result: %v\n", err)
		return 1
	}
	return 0
}
```

`doComplete` no longer uses the shared `gitClient` helper (it must turn a git-init failure into a written result); leave `gitClient` in place for `doPrepare`.

- [ ] **Step 9: Run + docs + commit**

Run: `go test ./cmd/at-work/ ./internal/dispatch/worker/ && go build ./... && go vet ./... && gofmt -l cmd/ internal/`
Expected: PASS; clean.

Update `docs/usage/at-work.md` (docs-author): in the `complete` command description, state that **`complete` always writes `.at-work/task-result` — when the task file is unreadable it writes an `error`-status result as JSON; exit 1 only if that write fails, exit 2 on extra arguments.** Bump the doc's `updated` frontmatter.

```bash
git add internal/dispatch/worker/format.go internal/dispatch/worker/resultv2.go internal/dispatch/worker/complete.go internal/dispatch/worker/resultv2_test.go cmd/at-work/main.go cmd/at-work/main_test.go docs/usage/at-work.md
git commit -m "feat(at-work): complete always writes a task-result (JSON default on unreadable task)"
```

---

## Task 2: `EnsureClean` inits the repo in place (tolerate pre-injected `.at-work/`)

In v2 the orchestrator injects `.at-work/task.json` **before** `prepare` runs (prepare reads it to learn the repo). `git clone <remote> .` refuses a non-empty dir, so `EnsureClean` must init in place instead, and must exclude `.at-work/` from git so the handoff files never enter `git status`/commits/PRs.

**Files:**
- Modify: `internal/dispatch/worker/git.go` (`EnsureClean` + a helper)
- Create/Modify: `internal/dispatch/worker/git_test.go` (a real-`git` test, skip if `git` absent)

**Interfaces:**
- `EnsureClean(ctx, remote, dir string) error` signature unchanged; behavior changes (clone → init-in-place + exclude).

- [ ] **Step 1: Failing test — init in place over a pre-existing `.at-work/`**

Create/append `internal/dispatch/worker/git_test.go`:

```go
package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCleanInitsInPlaceOverExisting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	// Simulate the orchestrator having injected the task file before prepare runs.
	if err := os.MkdirAll(filepath.Join(dir, ".at-work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".at-work", "task.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	g, err := NewShellGit("") // no token; no network is touched (no fetch here)
	if err != nil {
		t.Fatal(err)
	}
	const remote = "https://example.invalid/o/r.git"
	if err := g.EnsureClean(context.Background(), remote, dir); err != nil {
		t.Fatalf("EnsureClean over a pre-existing .at-work/: %v", err)
	}
	// A repo was initialized in place.
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf(".git not created: %v", err)
	}
	// origin points at the remote.
	if url, _ := g.git(context.Background(), dir, "remote", "get-url", "origin"); url != remote {
		t.Fatalf("origin = %q; want %q", url, remote)
	}
	// .at-work/ is excluded, so it never shows as an untracked change.
	status, err := g.git(context.Background(), dir, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status, ".at-work") {
		t.Fatalf(".at-work/ must be excluded from git status; got:\n%s", status)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run TestEnsureCleanInitsInPlace`
Expected: FAIL — current `EnsureClean` runs `git clone <remote> .` into a non-empty dir → error.

- [ ] **Step 3: Rewrite `EnsureClean` + add `excludeAtWork`**

In `internal/dispatch/worker/git.go`, replace `EnsureClean`:

```go
func (g *ShellGit) EnsureClean(ctx context.Context, remote, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		// Init in place: the orchestrator has already injected .at-work/ here, so a
		// `git clone` (which refuses a non-empty dir) won't work. Sync() then fetches
		// and checks out the base branch.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if _, err := g.git(ctx, dir, "init", "-q"); err != nil {
			return err
		}
		if _, err := g.git(ctx, dir, "remote", "add", "origin", remote); err != nil {
			return err
		}
		return excludeAtWork(dir)
	}
	status, err := g.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("refusing to run: %s has uncommitted changes", dir)
	}
	return nil
}

// excludeAtWork adds the .at-work/ handoff dir to the repo's local excludes so its
// files never appear in git status or get committed into the work branch.
func excludeAtWork(dir string) error {
	p := filepath.Join(dir, ".git", "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString("\n/" + workSubdir + "/\n")
	return err
}
```

(`git.go` already imports `os`, `path/filepath`, `fmt`, `strings`. `workSubdir` is the package const `.at-work`.)

- [ ] **Step 4: Run + full suite + commit**

Run: `go test ./internal/dispatch/worker/ && go test ./... && go vet ./... && gofmt -l internal/`
Expected: PASS (the new test runs where `git` exists, skips otherwise); `go.mod` unchanged.

Note: `prepare_test.go` uses `fakeGit`, so its `EnsureClean` is unaffected — no changes there.

```bash
git add internal/dispatch/worker/git.go internal/dispatch/worker/git_test.go
git commit -m "fix(at-work): EnsureClean inits repo in place + excludes .at-work/ (v2 handoff)"
```

---

## Task 3: dispatch seam — kit `dispatch.input`/`dispatch.output` + `dispatchrun`

Replace the hardcoded VM paths `/in/input.json` / `/out/output.json` with kit-declared paths, so at-cove stays VM-layout-generic and the kit coordinates where the `.at-work/` files land.

**Files:**
- Modify: `internal/kit/config.go` (`DispatchConfig`)
- Modify: `internal/dispatchrun/dispatchrun.go`
- Modify: `internal/dispatchrun/dispatchrun_test.go`
- Modify: `docs/OVERVIEW.md` and `docs/orchestration/at-cove-dispatch-interface.md`

**Interfaces:**
- Produces: `kit.DispatchConfig{Command []string; Input string; Output string}` (yaml `command`/`input`/`output`). `dispatchrun.Dispatch` now requires `Cfg.Dispatch.Input` and `Cfg.Dispatch.Output` to be non-empty.

- [ ] **Step 1: Add `Input`/`Output` to `DispatchConfig`**

In `internal/kit/config.go`, replace the `DispatchConfig` block:

```go
// DispatchConfig declares how `at-cove dispatch` performs a unit of work: the command
// run inside the VM, and the VM-side paths where at-cove injects the task file and reads
// the result. Input/Output are absolute VM paths under the worker's .at-work/ dir; the
// dispatch command must run with a cwd such that at-work reads/writes the same files
// (see kits/reference-worker for the reference wiring).
type DispatchConfig struct {
	Command []string `yaml:"command"`
	Input   string   `yaml:"input"`  // VM path at-cove writes the task file to, e.g. /home/agent/work/.at-work/task.json
	Output  string   `yaml:"output"` // VM path at-cove reads the task-result from, e.g. /home/agent/work/.at-work/task-result.json
}
```

`ParseConfig` uses `KnownFields(true)`; adding the fields keeps existing configs parseable and lets the reference kit declare them. No parse-time validation is added (dispatch is optional for non-dispatch kits) — `Dispatch` validates at run time (Step 3).

- [ ] **Step 2: Failing test — the hermetic dispatch tests set input/output**

In `internal/dispatchrun/dispatchrun_test.go`, add `Input`/`Output` to every `kit.DispatchConfig` literal, e.g. in `TestDispatchHappyPath`:

```go
		Cfg: kit.Config{Name: "w", Dispatch: kit.DispatchConfig{
			Command: []string{"run-worker.sh"},
			Input:   "/home/agent/work/.at-work/task.json",
			Output:  "/home/agent/work/.at-work/task-result.json",
		}},
```

Do the same in `TestDispatchRemovesContainerOnFailure` and `TestDispatchSecretNeverOnArgv`. Update the stale `/out/output.json` mentions in comments to the configured output path. Add a positive assertion in `TestDispatchHappyPath` that the extraction cats the configured output:

```go
	if !strings.Contains(allCalls(r), "cat /home/agent/work/.at-work/task-result.json") {
		t.Fatalf("did not extract from the configured output path:\n%s", allCalls(r))
	}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./internal/dispatchrun/ -run TestDispatchHappyPath`
Expected: FAIL — `Dispatch` still cats the hardcoded `/out/output.json`; the new assertion fails (and once Step 4 adds validation, the not-yet-updated tests would fail on the missing input/output — hence updating all three in Step 2).

- [ ] **Step 4: Rewire `dispatchrun.go` to the configured paths**

In `internal/dispatchrun/dispatchrun.go`:

Remove the `inputVMPath` and `outputVMPath` consts (keep `credsVMPath`, `envVMPath`):

```go
const (
	credsVMPath = "/agent-data/.credentials.json"
	envVMPath   = "/dev/shm/at-cove-dispatch-env"
)
```

In `Dispatch`, extend the up-front validation:

```go
	if len(o.Cfg.Dispatch.Command) == 0 {
		return fmt.Errorf("kit %q declares no dispatch.command", o.Cfg.Name)
	}
	if o.Cfg.Dispatch.Input == "" || o.Cfg.Dispatch.Output == "" {
		return fmt.Errorf("kit %q must declare dispatch.input and dispatch.output", o.Cfg.Name)
	}
```

Replace the inject/run/extract block:

```go
	if err := writeVM(o.R, tgt, input, o.Cfg.Dispatch.Input); err != nil {
		return fmt.Errorf("inject input: %w", err)
	}
	outDir := filepath.Dir(o.Cfg.Dispatch.Output)
	if err := runWork(o.R, tgt, env, o.Cfg.Dispatch.Command, outDir, o.Timeout); err != nil {
		return fmt.Errorf("dispatch command: %w", err)
	}
	out, err := o.R.Output("ssh", append(sshargs.Base(tgt), "cat "+o.Cfg.Dispatch.Output)...)
	if err != nil {
		return fmt.Errorf("extract output at %s: %w", o.Cfg.Dispatch.Output, err)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("dispatch produced no output at %s", o.Cfg.Dispatch.Output)
	}
	return os.WriteFile(o.OutputPath, []byte(out), 0o600)
```

Change `runWork` to take the output dir and `mkdir -p` it (instead of the hardcoded `/out`):

```go
func runWork(r runner.Runner, tgt sshargs.Target, env map[string]string, cmd []string, outDir string, timeout time.Duration) error {
	if err := writeVM(r, tgt, []byte(envScript(env)), envVMPath); err != nil {
		return err
	}
	secs := int(timeout.Seconds())
	if secs <= 0 {
		secs = 1800
	}
	remote := fmt.Sprintf("set -a; . %s; rm -f %s; mkdir -p %s; timeout %d %s",
		envVMPath, envVMPath, shellQuote(outDir), secs, shellJoin(cmd))
	return r.RunStdin(nil, "ssh", append(sshargs.Base(tgt), remote)...)
}
```

Update the package doc comment (line 1–3) reference to `/in`/`/out` if it names them (it says "it never parses the in/out files" — fine to leave; update only if it names the hardcoded paths).

- [ ] **Step 5: Run + verify + commit**

Run: `go test ./internal/dispatchrun/ ./internal/kit/ && go test ./... && go build ./... && go vet ./... && gofmt -l internal/`
Expected: PASS. If a `cmd/at-cove` dispatch test constructs a `kit.Config` that reaches `Dispatch` without input/output, update that fixture to include them (reconcile against the actual test).

Update docs (docs-author, same change):
- `docs/OVERVIEW.md` — the `at-cove dispatch` command row (~line 132): note it injects the task file at the kit's `dispatch.input` and extracts `dispatch.output`. If OVERVIEW documents the kit config schema (the `dispatch:` block), add the `input`/`output` fields there.
- `docs/orchestration/at-cove-dispatch-interface.md` — update the seam description from the old `input.json`/`output.json` to `.at-work/task.json` → `.at-work/task-result.json` driven by `dispatch.input`/`dispatch.output`, and reference the canonical [`docs/usage/at-work*.md`](../usage/at-work.md) for the file schemas rather than restating them. Bump `updated`.

```bash
git add internal/kit/config.go internal/dispatchrun/dispatchrun.go internal/dispatchrun/dispatchrun_test.go docs/OVERVIEW.md docs/orchestration/at-cove-dispatch-interface.md
git commit -m "feat(dispatch): kit-configured VM input/output paths for the .at-work/ seam"
```

---

## Task 4: reference kit — v2 scripts, config, testdata, e2e

Point the reference worker at `.at-work/task.json` and the tagged-union `worker-result.json`, and wire the kit's `dispatch.input`/`output`.

**Files:**
- Modify: `kits/reference-worker/config.yml`
- Modify: `kits/reference-worker/image-files/usr/local/bin/run-worker.sh`
- Modify: `kits/reference-worker/image-files/usr/local/bin/run-agent.sh`
- Rename/replace: `kits/reference-worker/testdata/input.json` → `kits/reference-worker/testdata/task.json`
- Modify: `internal/dispatchrun/e2e_integration_test.go`
- Modify: `kits/reference-worker/RUNBOOK.md`
- Modify: any hermetic test that parses the reference `config.yml` (reconcile)

- [ ] **Step 1: `config.yml` — declare the workdir input/output**

In `kits/reference-worker/config.yml`, replace the `dispatch:` block:

```yaml
dispatch:
  command: ["run-worker.sh"]
  # at-cove injects the task file here (creating .at-work/) before run-worker.sh runs,
  # and reads the result here after. run-worker.sh cd's to /home/agent/work so at-work's
  # cwd-relative .at-work/ resolves to these same files.
  input: /home/agent/work/.at-work/task.json
  output: /home/agent/work/.at-work/task-result.json
```

- [ ] **Step 2: `run-worker.sh` — no path args, cd to the workdir**

Replace `kits/reference-worker/image-files/usr/local/bin/run-worker.sh`:

```sh
#!/bin/sh
# The kit's dispatch.command. at-cove runs this in the container with the kit's secrets
# in the environment and .at-work/task.json already injected under /home/agent/work. It
# sequences the git/PR worker around the agent, stripping the token for the agent step
# (the air-gap).
#
# `at-work complete` ALWAYS runs — even if prepare or the agent fails — and always writes
# .at-work/task-result.json (a missing/unreadable task, or a missing/invalid worker-result,
# becomes a structured error). A failed prepare skips the agent but still completes; a
# nonzero agent exit is tolerated so completion is never skipped.
set -e

cd /home/agent/work

# Only run the agent if prepare succeeded (a clean, ready checkout). The agent runs
# WITHOUT the code-host token (the air-gap); its failure is tolerated so `at-work
# complete` below always runs.
if at-work prepare; then
	env -u AT_WORK_GIT_TOKEN sh -c "$AT_WORK_AGENT_COMMAND" || true
fi

at-work complete
```

- [ ] **Step 3: `run-agent.sh` — point the worker at `.at-work/task.json`, emit `worker-result.json`**

Replace `kits/reference-worker/image-files/usr/local/bin/run-agent.sh`:

```sh
#!/bin/sh
# The agent harness. at-work prepare has checked the repo out into the cwd (/home/agent/work)
# and the task spec is at .at-work/task.json. Drive headless claude to do the work and write
# its self-report to .at-work/worker-result.json. at-work complete reads that file; a missing
# or invalid worker-result becomes a structured error, so this script never synthesizes one.
set -e

claude -p --dangerously-skip-permissions "$(cat <<'PROMPT'
Your task is specified in .at-work/task.json in the current directory. Read that file:
the "task" -> "brief" field contains your instructions, and "repo" describes the
checked-out repository (already cloned into the cwd on the correct work branch).

Do the work described in this repository: make the changes and run the project's tests.
When you are finished, write your result to .at-work/worker-result.json as EXACTLY ONE
of these JSON objects (and nothing else in that file):

  {"status":{"ok":{"pull-request":{"title":"<PR title>","message":"<PR description>"}}}}
  {"status":{"needs-input":{"doing":"…","blocker":"…","need":"…","tried":"…"}}}
  {"status":{"error":{"message":"<what went wrong>"}}}

Use ok only if the change is complete and the tests pass (omit "pull-request" to push the
branch without opening a PR). Use needs-input if you are blocked on a decision only a human
can make. Do NOT push or open a PR yourself — that is handled after you exit.
PROMPT
)"
```

The heredoc is single-quoted (`<<'PROMPT'`) so the JSON braces aren't expanded by the shell. No `jq`/`brief.md` — the worker reads `task.json` directly.

- [ ] **Step 4: testdata → v2 `task.json`**

Replace `kits/reference-worker/testdata/input.json` with `kits/reference-worker/testdata/task.json` (delete the old file):

```json
{
  "issue": {
    "key": "DEMO-1",
    "title": "Add a greeting helper"
  },
  "repo": {
    "name": "<your-org>/<scratch-repo>",
    "source-branch": "main",
    "work-branch": "implement/DEMO-1"
  },
  "worker": {
    "class": "implement"
  },
  "task": {
    "brief": "Add a function `Greet(name string) string` returning \"Hello, <name>!\" with a unit test. Keep it minimal."
  }
}
```

- [ ] **Step 5: e2e test — v2 input + tagged-union assertion**

In `internal/dispatchrun/e2e_integration_test.go`, update the input construction and the result assertion:

```go
	in := filepath.Join(dir, "task.json")
	out := filepath.Join(dir, "task-result.json")

	input := `{"issue":{"key":"DEMO-1","title":"Add a greeting helper"},` +
		`"repo":{"name":"` + repo + `","source-branch":"main","work-branch":"implement/DEMO-1"},` +
		`"worker":{"class":"implement"},` +
		`"task":{"brief":"Add Greet(name string) string returning \"Hello, <name>!\" with a test."}}`
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
		t.Fatalf("read task-result.json: %v", err)
	}
	var res struct {
		Status struct {
			OK *struct {
				PRURL string `json:"pr-url"`
			} `json:"ok"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("parse task-result.json: %v\n%s", err, data)
	}
	if res.Status.OK == nil {
		t.Fatalf("status is not ok\n%s", data)
	}
	if res.Status.OK.PRURL == "" {
		t.Fatalf("no pr-url in task-result\n%s", data)
	}
	t.Logf("opened PR: %s", res.Status.OK.PRURL)
```

- [ ] **Step 6: RUNBOOK + reconcile hermetic kit-parse test**

Update `kits/reference-worker/RUNBOOK.md`: replace references to `input.json`/`output.json`/`brief.md`/`outcome.json` with `task.json`/`task-result.json`/`worker-result.json`; note the workdir (`/home/agent/work`) and the `dispatch.input`/`output` fields; update the `just e2e` example to use `testdata/task.json` if it names the file.

If a hermetic test parses the reference `config.yml` (e.g. a kit-config parse test added in the reference-kit plan), run it — adding `input`/`output` to `DispatchConfig` (Task 3) keeps it parseable; if the test asserts the exact `Dispatch` struct, update the expectation to include the new fields.

- [ ] **Step 7: Verify + commit**

Run:
```
go test ./... && go build ./... && go vet ./...
go build -tags integration ./internal/dispatchrun/    # e2e compiles
sh -n kits/reference-worker/image-files/usr/local/bin/run-worker.sh
sh -n kits/reference-worker/image-files/usr/local/bin/run-agent.sh
just --list >/dev/null
```
Expected: default suite PASS; e2e compiles under the `integration` tag (not run here — maintainer runs `just e2e`); scripts parse; `go.mod` unchanged.

```bash
git add kits/reference-worker/ internal/dispatchrun/e2e_integration_test.go
git commit -m "feat(reference-worker): v2 kit — task.json handoff, tagged-union worker-result"
```

---

## Final verification

- [ ] `go test ./...` — all pass (scheduler still compiles on the old flat types).
- [ ] `just build` — three binaries build.
- [ ] `go build -tags integration ./internal/dispatchrun/` — the e2e test compiles.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/` prints nothing.
- [ ] `grep -rn 'input.json\|output.json\|/in/\|/out/\|brief.md\|outcome.json' internal/dispatchrun/ kits/reference-worker/` — only the local host-side `--in`/`--out` filenames in the e2e test remain; no `/in//out/`, `brief.md`, or `outcome.json` in the seam or kit.
- [ ] Secrets air-gap intact: `run-worker.sh` still strips `AT_WORK_GIT_TOKEN` for the agent (`env -u`), and it never appears on argv/logs (dispatchrun's stdin-only env script is unchanged).
- [ ] Docs synced: `docs/usage/at-work.md` (complete's always-write behavior), `docs/OVERVIEW.md` + `docs/orchestration/at-cove-dispatch-interface.md` (the input/output seam). Run the docs-audit skill on the touched docs.

## Notes

- **Reconciliations** (read-and-match): the `cmd/at-work/main_test.go` harness (how `run(...)` is invoked, whether a helper already chdirs); the exact `resultv2_test.go` filename/imports; any `cmd/at-cove` dispatch test that builds a `kit.Config` reaching `Dispatch`; any hermetic parse test over the reference `config.yml`.
- **Workdir convention:** `/home/agent/work` is the reference kit's choice, kept consistent between `dispatch.input`/`output` and `run-worker.sh`'s `cd`. It is kit payload, not a repo-wide constant — another kit may choose differently as long as the three agree.
- **Still on the old types (Plan 4):** the scheduler writes the old `input.json` shape and reads the old `output.json` shape, so the *scheduler-driven* loop is not yet v2. The reference kit + `at-cove dispatch` path *is* v2 after Plan 3; the maintainer's `just e2e` exercises it directly. Plan 4 migrates the scheduler and deletes the old flat types, completing AET-28.
- **Why init-in-place (not clone-into-subdir):** chosen so at-work keeps its single `dir="."` model (no `Prepare`/`Complete` signature churn) while tolerating the pre-injected `.at-work/`; `.git/info/exclude` keeps the handoff files out of `git status`/commits/PRs.
