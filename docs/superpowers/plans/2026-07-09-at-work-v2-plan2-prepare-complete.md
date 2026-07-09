# at-work contract v2 — Plan 2: prepare/complete + cmd cutover Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cut `at-work`'s `prepare`/`complete` and its CLI over to the v2 types (`Task`/`WorkerResult`/`TaskResult`, added in Plan 1): no `brief.md`, the worker owns the PR, `needs-input` carries the retrace `commit` SHA, and the commands take **no path arguments** (fixed `.at-work/` files, JSON or YAML).

**Architecture:** `Prepare(…, task Task, …)` does repo setup only (drops `WriteBrief`). `Complete(…, task Task, …) TaskResult` reads the worker's tagged-union `WorkerResult`, pushes the branch, opens a PR **only when the worker proposed one** (`status.ok.pull-request`), and always returns a `TaskResult` (echoing the raw worker-result, `commit` SHA on needs-input). `cmd/at-work` reads the fixed `.at-work/` files and writes `task-result` mirroring the task extension. The **shipped flat types (`Input`/`Outcome`/`Output`) stay** — the scheduler still uses them; Plan 4 deletes them after the scheduler migrates.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**.

**Scope note:** Plan 2 of [AET-28](https://linear.app/aethons-tools/issue/AET-28), building on Plan 1 (the v2 data layer, merged). Canonical contract: [`docs/usage/at-work-inputs.md`](../../usage/at-work-inputs.md), [`docs/usage/at-work-output.md`](../../usage/at-work-output.md), [`docs/usage/at-work.md`](../../usage/at-work.md).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- **Consume Plan 1's v2 layer:** `Task`/`ReadTask`, `WorkerResult`/`ReadWorkerResult` (typed + `raw any` echo), `WorkerStatus.Active()`, `TaskResult`/`TaskStatus`/`TaskOK`/`TaskNeedsInput`/`TaskError`/`ActiveTask`/`WriteTaskResult`, `PullRequest`, `WorkerOK`. (Note the Plan-1 renames: the worker needs-input/error Go types are `WorkerNeedsInput`/`WorkerError`.)
- **Do NOT touch the shipped flat types** (`Input`/`IssueInput`/`RepoInput`/`Outcome`/`NeedsInput`/`Output`/`Work`/`ReadInput`/`ReadOutcome`/`WriteOutput`/`Status*`) — the scheduler imports them. They stay until Plan 4. (`WriteBrief`/`briefPath` are the exception — removed in Task 3, since `brief.md` is gone.)
- **`prepare`/`complete` take no path arguments** — they operate on the current dir's `.at-work/` files. Extra args → exit 2.
- **PR ownership:** the worker's `status.ok.pull-request{title,message}` is the PR title/body. If the worker's `ok` has no `pull-request`, at-work pushes the branch and opens **no PR** (`task-result.ok` without `pr-url`). No at-work-constructed PR title.
- **`Complete` never returns a Go error** — always a `TaskResult` (like the shipped `Complete→Output`). The raw worker-result is echoed into `TaskResult.WorkerResult` (unless the worker-result was missing/unreadable).
- **Hermetic tests** — `prepare` against a local bare repo; `complete` with a prepared checkout + a written `.at-work/worker-result.json` + a fake `CodeHost`. Every commit: `go test ./...` green (the scheduler still compiles against the untouched flat types).

## Known temporary inconsistency (expected)

After Plan 2, the **hermetic suite stays green**, but the automated dispatch loop is temporarily inconsistent: `cmd/at-work` now reads `.at-work/task.{json,yml}` (no args), while the reference kit's `run-worker.sh` still calls `at-work prepare /in/input.json` and the scheduler still writes `input.json`. Plan 3 fixes the kit + dispatch seam; Plan 4 fixes the scheduler. The end-to-end reference run is maintainer-validated only after Plan 4 — this is by design for the sequence.

---

## Task 1: `Prepare` (Task) + `cmd` prepare (no-args)

**Files:**
- Modify: `internal/dispatch/worker/prepare.go`
- Modify: `internal/dispatch/worker/prepare_test.go`
- Modify: `cmd/at-work/main.go` (`doPrepare`)
- Modify: `cmd/at-work/main_test.go`

**Interfaces:**
- Produces: `Prepare(ctx context.Context, dir string, task Task, git Git) error` (signature change: `Input`→`Task`, no brief write).

- [ ] **Step 1: Rewrite the Prepare tests for `Task` + no-brief**

In `internal/dispatch/worker/prepare_test.go`, change the calls from `Prepare(ctx, dir, Input{…}, git)` to build a `Task{Issue: TaskIssue{Key,Title}, Repo: TaskRepo{Name,SourceBranch,WorkBranch}, Worker: TaskWorker{Class}, Task: TaskSpec{Brief}}`. Remove any assertion that `.at-work/brief.md` was written; **add** an assertion that it is **absent** after `Prepare`:

```go
if _, err := os.Stat(filepath.Join(dir, ".at-work", "brief.md")); !os.IsNotExist(err) {
	t.Fatalf("prepare must not write brief.md; stat err=%v", err)
}
```

Keep the existing dirty-refusal / resume / fresh-branch / default-branch-guard assertions (they're unchanged behavior). If a test builds the remote from `Input.Repo.Name`, it now uses `Task.Repo.Name`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run Prepare`
Expected: FAIL to build — `Prepare` still takes `Input`.

- [ ] **Step 3: Rewrite `prepare.go`**

```go
package worker

import (
	"context"
	"fmt"
	"strings"
)

// Prepare sets up (or resumes) the work branch in dir. It does no content extraction —
// the worker reads its brief straight from task.json. Idempotent: a clean existing
// checkout and an existing remote work-branch are reused.
func Prepare(ctx context.Context, dir string, task Task, git Git) error {
	sb, wb := task.Repo.SourceBranch, task.Repo.WorkBranch
	if wb == "" || wb == sb {
		return fmt.Errorf("work-branch must be non-empty and differ from source-branch %q", sb)
	}
	host := task.Repo.Host
	if host == "" {
		host = "https://github.com"
	}
	remote := strings.TrimSuffix(host, "/") + "/" + task.Repo.Name + ".git"
	if err := git.EnsureClean(ctx, remote, dir); err != nil {
		return err
	}
	if err := git.Sync(ctx, dir, sb); err != nil {
		return fmt.Errorf("sync base %s: %w", sb, err)
	}
	has, err := git.RemoteHasBranch(ctx, dir, wb)
	if err != nil {
		return err
	}
	if has {
		if err := git.Sync(ctx, dir, wb); err != nil {
			return fmt.Errorf("resume %s: %w", wb, err)
		}
		return nil
	}
	if err := git.NewBranch(ctx, dir, wb, sb); err != nil {
		return fmt.Errorf("create %s: %w", wb, err)
	}
	return nil
}
```

- [ ] **Step 4: Rewire `cmd/at-work` `doPrepare` to no-args**

Read `cmd/at-work/main.go`. Change `doPrepare` to take **no positional args** — it reads `.at-work/task.{json,yml}` via `worker.ReadTask(".")`:

```go
func doPrepare(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "at-work prepare: takes no arguments (reads .at-work/task.json)")
		return 2
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		fmt.Fprintf(stderr, "at-work prepare: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	if err := worker.Prepare(context.Background(), ".", task, g); err != nil {
		fmt.Fprintf(stderr, "at-work prepare: %v\n", err)
		return 1
	}
	return 0
}
```

Keep the `gitClient` helper and the `cli` closure that calls `doPrepare(args, errw)` unchanged (the closure still passes `args`; `doPrepare` now rejects non-empty).

- [ ] **Step 5: Update `cmd/at-work/main_test.go` (prepare)**

The old prepare test asserted `at-work prepare <input.json>` arg handling (exit 2 when the arg is missing). Now: `at-work prepare` with **no** args is valid; `at-work prepare x` → exit 2. Update/replace the prepare arg-validation test accordingly (a hermetic test can only check the exit-2-on-extra-arg path without a real repo; the happy path needs git and is covered by `prepare_test.go`). Match the file's existing `run(...)`/fake harness.

- [ ] **Step 6: Run + commit**

Run: `go test ./internal/dispatch/worker/ ./cmd/at-work/ && go build ./... && go vet ./... && gofmt -l cmd/ internal/`
Expected: PASS; clean; `go.mod` unchanged (the scheduler still compiles against the untouched flat types).

```bash
git add internal/dispatch/worker/prepare.go internal/dispatch/worker/prepare_test.go cmd/at-work/main.go cmd/at-work/main_test.go
git commit -m "feat(worker): Prepare(Task) + at-work prepare no-args (no brief.md)"
```

---

## Task 2: `Complete` (Task→TaskResult) + `cmd` complete (no-args)

**Files:**
- Modify: `internal/dispatch/worker/complete.go`
- Modify: `internal/dispatch/worker/complete_test.go`
- Modify: `internal/dispatch/worker/resultv2.go` (add `TaskExt`)
- Modify: `cmd/at-work/main.go` (`doComplete`)
- Modify: `cmd/at-work/main_test.go`

**Interfaces:**
- Consumes: `ReadWorkerResult`, `WorkerStatus.Active()`, `PullRequest`, the `TaskResult`/`TaskStatus`/`TaskOK`/`TaskNeedsInput`/`TaskError` types, `WriteTaskResult`, the `Git`/`CodeHost` interfaces (unchanged).
- Produces: `Complete(ctx, dir string, task Task, git Git, ch CodeHost) TaskResult`; `TaskExt(dir string) (string, error)`.

- [ ] **Step 1: Rewrite the Complete tests for the tagged union**

Rewrite `internal/dispatch/worker/complete_test.go` to write `.at-work/worker-result.json` (the v2 tagged union) and assert the returned `TaskResult`. Core cases (adapt to the file's existing prepared-checkout + `fakeGit`/`fakeCodeHost` harness — the fakes implement `Git`/`CodeHost` unchanged):

```go
// ok WITH a proposed PR → PR opened, task-result ok carries pr-url; worker-result echoed
func TestCompleteOKOpensPR(t *testing.T) {
	dir := preparedRepo(t) // existing helper: a checkout on the work-branch with a change
	writeAtWork(t, dir, "worker-result.json",
		`{"status":{"ok":{"pull-request":{"title":"AET-1: X","message":"body"}}},"extra":"kept"}`)
	ch := &fakeCodeHost{url: "https://x/pull/1"}
	tr := Complete(context.Background(), dir, sampleTask(), &fakeGit{changes: true, differs: true, sha: "abc"}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "ok" || tr.Status.OK.PRURL != "https://x/pull/1" {
		t.Fatalf("ok: %+v", tr.Status)
	}
	if ch.title != "AET-1: X" { // PR title comes from the worker, not at-work
		t.Fatalf("PR title = %q; want the worker's", ch.title)
	}
	if m, _ := tr.WorkerResult.(map[string]any); m["extra"] != "kept" {
		t.Fatalf("worker-result echo dropped unknown field: %v", tr.WorkerResult)
	}
}

// ok WITHOUT a proposed PR → branch pushed, no PR, ok without pr-url
func TestCompleteOKNoPR(t *testing.T) {
	dir := preparedRepo(t)
	writeAtWork(t, dir, "worker-result.json", `{"status":{"ok":{}}}`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, sampleTask(), &fakeGit{changes: true, differs: true, sha: "abc"}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "ok" || tr.Status.OK.PRURL != "" {
		t.Fatalf("ok-no-pr: %+v", tr.Status)
	}
	if ch.opened {
		t.Fatal("no PR should be opened when the worker proposes none")
	}
}

// needs-input → WIP pushed, commit SHA recorded, no PR
func TestCompleteNeedsInput(t *testing.T) {
	dir := preparedRepo(t)
	writeAtWork(t, dir, "worker-result.json",
		`{"status":{"needs-input":{"doing":"d","blocker":"b","need":"n","tried":"t"}}}`)
	tr := Complete(context.Background(), dir, sampleTask(), &fakeGit{changes: true, sha: "deadbeef"}, &fakeCodeHost{})
	if v, _ := tr.Status.ActiveTask(); v != "needs-input" || tr.Status.NeedsInput.Commit != "deadbeef" {
		t.Fatalf("needs-input: %+v", tr.Status)
	}
}

// worker error → task-result error; no git/PR
func TestCompleteWorkerError(t *testing.T) {
	dir := preparedRepo(t)
	writeAtWork(t, dir, "worker-result.json", `{"status":{"error":{"message":"cannot"}}}`)
	ch := &fakeCodeHost{}
	tr := Complete(context.Background(), dir, sampleTask(), &fakeGit{}, ch)
	if v, _ := tr.Status.ActiveTask(); v != "error" || ch.opened {
		t.Fatalf("worker-error: %+v opened=%v", tr.Status, ch.opened)
	}
}

// missing worker-result → error, no echo
func TestCompleteMissingWorkerResult(t *testing.T) {
	dir := preparedRepo(t)
	tr := Complete(context.Background(), dir, sampleTask(), &fakeGit{}, &fakeCodeHost{})
	if v, _ := tr.Status.ActiveTask(); v != "error" || tr.WorkerResult != nil {
		t.Fatalf("missing: %+v echo=%v", tr.Status, tr.WorkerResult)
	}
}
```

Reconcile `preparedRepo`/`sampleTask`/`fakeGit`/`fakeCodeHost` with the file's existing helpers (the shipped `complete_test.go` already has a prepared-checkout helper + the fakes; `sampleTask()` builds a `Task` with the same repo/issue the helper set up). `fakeCodeHost` records `opened`/`title`/`url`.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run Complete`
Expected: FAIL to build — `Complete` still takes `Input`/returns `Output`.

- [ ] **Step 3: Rewrite `complete.go`**

```go
package worker

import "context"

// CodeHost opens (or finds) a pull request.
type CodeHost interface {
	OpenPR(ctx context.Context, repo, base, head, title, body string) (prURL string, err error)
}

// Complete reads the worker's result and finishes the work: commit/push and, when the
// worker proposed a PR, open it. It always returns a valid TaskResult (never a Go error),
// echoing the raw worker-result.
func Complete(ctx context.Context, dir string, task Task, git Git, ch CodeHost) TaskResult {
	wr, raw, ok, err := ReadWorkerResult(dir)
	if err != nil {
		return taskErr(nil, "unreadable worker result", err.Error())
	}
	if !ok {
		return taskErr(nil, "no worker result", "")
	}
	variant, verr := wr.Status.Active()
	if verr != nil {
		return taskErr(raw, "invalid worker result", verr.Error())
	}
	switch variant {
	case "ok":
		return completeOK(ctx, dir, task, git, ch, wr, raw)
	case "needs-input":
		return completeNeedsInput(ctx, dir, task, git, raw)
	default: // error
		msg := ""
		if wr.Status.Error != nil {
			msg = wr.Status.Error.Message
		}
		return taskErr(raw, "worker could not execute task", msg)
	}
}

func completeOK(ctx context.Context, dir string, task Task, git Git, ch CodeHost, wr WorkerResult, raw any) TaskResult {
	if has, err := git.HasChanges(ctx, dir); err != nil {
		return taskErr(raw, "check for changes", err.Error())
	} else if has {
		if _, err := git.Commit(ctx, dir, task.Issue.Key+": "+task.Issue.Title); err != nil {
			return taskErr(raw, "commit", err.Error())
		}
	}
	if err := git.Push(ctx, dir, task.Repo.WorkBranch); err != nil {
		return taskErr(raw, "push", err.Error())
	}
	okStatus := &TaskOK{Message: "pushed " + task.Repo.WorkBranch}
	if pr := wr.Status.OK.PullRequest; pr != nil {
		differs, err := git.DiffersFrom(ctx, dir, task.Repo.SourceBranch)
		if err != nil {
			return taskErr(raw, "diff", err.Error())
		}
		if differs {
			url, err := ch.OpenPR(ctx, task.Repo.Name, task.Repo.SourceBranch, task.Repo.WorkBranch, pr.Title, pr.Message)
			if err != nil {
				return taskErr(raw, "open PR", err.Error())
			}
			okStatus.PRURL = url
			okStatus.Message = "opened PR"
		} else {
			okStatus.Message = "no changes to open a PR"
		}
	}
	return TaskResult{Status: TaskStatus{OK: okStatus}, WorkerResult: raw}
}

func completeNeedsInput(ctx context.Context, dir string, task Task, git Git, raw any) TaskResult {
	if has, err := git.HasChanges(ctx, dir); err == nil && has {
		_, _ = git.Commit(ctx, dir, "WIP "+task.Issue.Key)
	}
	_ = git.Push(ctx, dir, task.Repo.WorkBranch) // best-effort; the WIP lives on origin
	head, _ := git.Head(ctx, dir)
	return TaskResult{
		Status:       TaskStatus{NeedsInput: &TaskNeedsInput{Message: "worker needs input; WIP pushed to " + task.Repo.WorkBranch, Commit: head}},
		WorkerResult: raw,
	}
}

// taskErr builds an ERROR TaskResult. detail is the underlying diagnostic (omitted if "").
func taskErr(raw any, msg, detail string) TaskResult {
	e := &TaskError{Message: msg}
	if detail != "" {
		e.Detail = detail
	}
	return TaskResult{Status: TaskStatus{Error: e}, WorkerResult: raw}
}
```

- [ ] **Step 4: Add `TaskExt` (so the CLI can mirror the task extension)**

Append to `internal/dispatch/worker/resultv2.go`:

```go
// TaskExt returns the extension (".json" or ".yml") of the task file in dir's .at-work,
// so complete can write task-result in the same format.
func TaskExt(dir string) (string, error) {
	_, ext, err := resolveContract(dir, "task")
	return ext, err
}
```

- [ ] **Step 5: Rewire `cmd/at-work` `doComplete` to no-args + WriteTaskResult**

```go
func doComplete(args []string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "at-work complete: takes no arguments (reads .at-work/, writes .at-work/task-result)")
		return 2
	}
	task, err := worker.ReadTask(".")
	if err != nil {
		fmt.Fprintf(stderr, "at-work complete: %v\n", err)
		return 1
	}
	ext, err := worker.TaskExt(".")
	if err != nil {
		fmt.Fprintf(stderr, "at-work complete: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	ch := github.New(os.Getenv("AT_WORK_GIT_TOKEN"), nil)
	tr := worker.Complete(context.Background(), ".", task, g, ch)
	if err := worker.WriteTaskResult(".", ext, tr); err != nil {
		fmt.Fprintf(stderr, "at-work: write task-result: %v\n", err)
		return 1
	}
	return 0
}
```

- [ ] **Step 6: Update `cmd/at-work/main_test.go` (complete)** — the complete arg-validation test: `at-work complete` (no args) is valid-shaped; `at-work complete x` → exit 2. Match the file's harness.

- [ ] **Step 7: Run + commit**

Run: `go test ./... && go build ./... && go vet ./... && gofmt -l cmd/ internal/ && just build`
Expected: all pass; three binaries; `go.mod` unchanged.

```bash
git add internal/dispatch/worker/complete.go internal/dispatch/worker/complete_test.go internal/dispatch/worker/resultv2.go cmd/at-work/main.go cmd/at-work/main_test.go
git commit -m "feat(worker): Complete(Task)->TaskResult (worker-owned PR, commit SHA) + no-args CLI"
```

---

## Task 3: remove `WriteBrief` (brief.md is gone)

`WriteBrief`/`briefPath` are now unused (Prepare no longer writes a brief) and contradict the v2 design. Remove them and their test. (The other shipped flat types/functions stay for the scheduler — Plan 4 deletes them.)

**Files:**
- Modify: `internal/dispatch/worker/types.go` (remove `WriteBrief`, `briefPath`)
- Modify: `internal/dispatch/worker/types_test.go` (remove the `WriteBrief` test)

- [ ] **Step 1: Confirm `WriteBrief` is unused**

Run: `grep -rn 'WriteBrief\|briefPath' internal/ cmd/`
Expected: references only in `types.go` (the decls) and `types_test.go` (a test). If anything else references them, STOP and report.

- [ ] **Step 2: Remove them**

Delete the `WriteBrief` function and the `briefPath` helper from `types.go`, and delete the `WriteBrief` test (e.g. `TestWriteBrief…`) from `types_test.go`. Leave `workSubdir`, `outcomePath`, and all the flat types/`ReadInput`/`ReadOutcome`/`WriteOutput`/`Status*` intact (the scheduler + the shipped `types_test` still use them).

- [ ] **Step 3: Run + commit**

Run: `go test ./... && go vet ./... && gofmt -l internal/`
Expected: all pass; clean; `go.mod` unchanged.

```bash
git add internal/dispatch/worker/types.go internal/dispatch/worker/types_test.go
git commit -m "refactor(worker): remove WriteBrief/briefPath (brief.md removed in v2)"
```

---

## Final verification

- [ ] `go test ./...` — all pass (incl. the untouched scheduler, which still uses the flat `Input`/`Output`).
- [ ] `just build` — three binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/` prints nothing.
- [ ] `grep -rn 'brief.md\|WriteBrief\|Issue.Brief' internal/dispatch/worker/ cmd/at-work/` — no live references (the brief flows only via `task.json`).
- [ ] The old flat types remain (scheduler): `grep -n 'type Output\|type Input\|StatusOK' internal/dispatch/worker/types.go` still present.

## Notes

- **Reconciliations** (read-and-match against the current files): the `cmd/at-work/main.go` `doPrepare`/`doComplete` signatures + the `cli` closure that calls them; the `complete_test.go` prepared-checkout helper + `fakeGit`/`fakeCodeHost` field names (add `title` capture to `fakeCodeHost` if absent, to assert the PR title comes from the worker); the `prepare_test.go` remote-URL assertion.
- **Commit message vs PR title:** at-work's mechanical commit message stays `"<key>: <title>"` (deterministic); the **PR** title/body come from the worker's `pull-request` (decision 2 was about PR-title ownership, not the commit message).
- **The scheduler is intentionally left on the flat types** — after this plan the automated loop is inconsistent (see "Known temporary inconsistency"); Plan 3 (kit/dispatch) and Plan 4 (scheduler + delete the old types) close it. Hermetic tests stay green throughout.
