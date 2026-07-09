# at-work contract v2 — Plan 4: scheduler migration + delete old types Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate the `at-dispatch` scheduler to construct the v2 `Task` and broker the tagged-union `TaskResult` (with the echoed `worker-result`), then delete the now-orphaned old flat types — making the whole scheduler-driven loop v2 and completing [AET-28](https://linear.app/aethons-tools/issue/AET-28).

**Architecture:** Two tasks, sequenced additive-then-subtractive so every commit is green. (1) **Migrate** — `engine.go` builds a `worker.Task` (nested `issue`/`repo`/`worker`/`task`), writes a local `task.json`, and `at-cove dispatch --in task.json --out task-result.json` streams it through the VM's `.at-work/` (Plan 3's seam); the broker switches on `TaskResult.Status.ActiveTask()` and the comment builders read the tagged-union result plus the echoed `worker-result` (doing/blocker/need/tried) and the retrace `commit` SHA. A tiny `worker.WorkerResultFrom` helper re-decodes the echo. The old flat types stay defined but unused. (2) **Delete** — remove the orphaned `Input`/`Outcome`/`Output`/`Read*`/`WriteOutput`/`Status*` from `worker/types.go` (keeping only `workSubdir`), delete their tests, and refresh a now-stale comment.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**.

**Scope note:** Plan 4 (final) of AET-28, building on the merged Plans 1–3. After this, the scheduler-driven loop is fully v2 and the old contract is gone. Canonical contract: [`docs/usage/at-work.md`](../../usage/at-work.md), [`docs/usage/at-work-inputs.md`](../../usage/at-work-inputs.md), [`docs/usage/at-work-output.md`](../../usage/at-work-output.md).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- **Consume the shipped v2 types** (in `internal/dispatch/worker/`): `Task{Issue TaskIssue{Key,Title}, Repo TaskRepo{Host?,Name,SourceBranch,WorkBranch}, Worker TaskWorker{Class}, Task TaskSpec{Class?,Brief}}`; `TaskResult{Status TaskStatus, WorkerResult any}`; `TaskStatus{OK *TaskOK, NeedsInput *TaskNeedsInput, Error *TaskError}` + method `ActiveTask() (string, error)`; `TaskOK{Message,PRURL}`; `TaskNeedsInput{Message,Commit}`; `TaskError{Message,Detail}`; `WorkerResult{Status WorkerStatus}`, `WorkerStatus{OK *WorkerOK, NeedsInput *WorkerNeedsInput, Error *WorkerError}`, `WorkerOK{PullRequest *PullRequest}`, `PullRequest{Title,Message}`, `WorkerNeedsInput{Doing,Blocker,Need,Tried}`; `ErrorResult(message, detail string) TaskResult`.
- **The scheduler is the LAST user of the old flat types** — after Task 1 nothing references `worker.Input`/`Output`/`Outcome`/`ReadInput`/`ReadOutcome`/`WriteOutput`/`Status*` (verified: only `engine.go`). Task 2 deletes them.
- **Preserve scheduler behavior exactly** except the wire contract: the same role transitions (`ok`+no-runErr → InReview; `needs-input` → NeedsInput; everything else → NeedsInput), the same timeout split (process = work+overhead, `--timeout` = work), the same fail-safe (missing/invalid result → error → NeedsInput), the same single-writer broker. `scheduler.New`'s signature is unchanged.
- **Hermetic tests** — the fake `Executor` simulates `at-cove dispatch` (reads `--in`, writes `--out`); no VM/network. Keep it that way.

---

## Task 1: migrate the scheduler to `Task` / `TaskResult`

**Files:**
- Modify: `internal/dispatch/worker/resultv2.go` (add `WorkerResultFrom`)
- Modify: `internal/dispatch/scheduler/engine.go`
- Modify: `internal/dispatch/scheduler/engine_test.go`
- Modify: `docs/orchestration/scheduler-config.md`

**Interfaces:**
- Produces: `worker.WorkerResultFrom(raw any) (WorkerResult, bool)` — re-decodes an echoed `TaskResult.WorkerResult` (a `map[string]any`) back into a typed `WorkerResult`; `ok` false if `raw` is nil or undecodable.

- [ ] **Step 1: Add `WorkerResultFrom` to the worker package**

In `internal/dispatch/worker/resultv2.go`, add `encoding/json` to the imports and append:

```go
// WorkerResultFrom decodes an echoed raw worker-result (a TaskResult.WorkerResult,
// which arrives as a map[string]any after JSON/YAML decoding) back into a typed
// WorkerResult. ok is false if raw is nil or not decodable.
func WorkerResultFrom(raw any) (WorkerResult, bool) {
	if raw == nil {
		return WorkerResult{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return WorkerResult{}, false
	}
	var wr WorkerResult
	if json.Unmarshal(b, &wr) != nil {
		return WorkerResult{}, false
	}
	return wr, true
}
```

- [ ] **Step 2: Unit-test `WorkerResultFrom`**

In `internal/dispatch/worker/resultv2_test.go`, add:

```go
func TestWorkerResultFrom(t *testing.T) {
	// nil → not ok
	if _, ok := WorkerResultFrom(nil); ok {
		t.Fatal("nil raw should be not-ok")
	}
	// a decoded needs-input echo (as it arrives from json.Unmarshal into `any`)
	raw := map[string]any{"status": map[string]any{"needs-input": map[string]any{
		"doing": "d", "blocker": "b", "need": "n", "tried": "tr",
	}}}
	wr, ok := WorkerResultFrom(raw)
	if !ok || wr.Status.NeedsInput == nil || wr.Status.NeedsInput.Blocker != "b" {
		t.Fatalf("decode: ok=%v wr=%+v", ok, wr.Status)
	}
}
```

Run: `go test ./internal/dispatch/worker/ -run TestWorkerResultFrom` → PASS.

- [ ] **Step 3: Rewrite the scheduler tests for the v2 wire shapes (fail first)**

In `internal/dispatch/scheduler/engine_test.go`, replace the v1 `OutJSON` literals and the assertions with v2 tagged-union `task-result` JSON:

`TestHandleOKOpensReviewAndBuildsInput`:
```go
	ex := &fakeExecutor{OutJSON: `{"status":{"ok":{"pr-url":"https://x/pull/1","message":"opened PR"}},` +
		`"worker-result":{"status":{"ok":{"pull-request":{"title":"T","message":"did the thing"}}}}}`}
	eng := newTestEngine(t, tr, ex)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "Add X", Class: "implement"})

	// task.json the scheduler built (v2 nested shape)
	if !strings.Contains(ex.GotInput, `"work-branch": "implement/AET-9"`) ||
		!strings.Contains(ex.GotInput, `"source-branch": "main"`) ||
		!strings.Contains(ex.GotInput, `"key": "AET-9"`) ||
		!strings.Contains(ex.GotInput, `"class": "implement"`) {
		t.Fatalf("task.json wrong:\n%s", ex.GotInput)
	}
	joined := strings.Join(ex.GotArgv, " ")
	if !strings.Contains(joined, "at-cove dispatch") || !strings.Contains(joined, "--timeout 30m") {
		t.Fatalf("argv wrong: %v", ex.GotArgv)
	}
	if tr.lastRole != RoleInReview {
		t.Errorf("role = %v; want IN REVIEW", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "https://x/pull/1") || !strings.Contains(tr.lastComment, "did the thing") {
		t.Errorf("comment missing PR/message: %q", tr.lastComment)
	}
```

`TestHandleNeedsInput`:
```go
	ex := &fakeExecutor{OutJSON: `{"status":{"needs-input":{"message":"WIP pushed","commit":"abc123"}},` +
		`"worker-result":{"status":{"needs-input":{"doing":"d","blocker":"b","need":"n","tried":"tr"}}}}`}
	eng := newTestEngine(t, tr, ex)
	eng.handle(context.Background(), Issue{ID: "id1", Identifier: "AET-9", Title: "X", Class: "implement"})
	if tr.lastRole != RoleNeedsInput {
		t.Errorf("role = %v; want NEEDS INPUT", tr.lastRole)
	}
	if !strings.Contains(tr.lastComment, "**Blocker:** b") || !strings.Contains(tr.lastComment, "abc123") {
		t.Errorf("needs-input comment wrong: %q", tr.lastComment)
	}
```

`TestHandleMissingOutputIsError` — unchanged assertions (still `⚠️`), but no `OutJSON` shape to update (it's `""`).

Update the remaining OK `OutJSON` literals (they only need to broker OK → InReview): in `TestHandleFailedClaimStops`, `TestTickReconcilesAndDispatches`, `TestTickRespectsGlobalConcurrency`, `TestRunStopsOnContextCancel`, replace `` `{"status":"OK","work":{}}` `` with `` `{"status":{"ok":{}}}` ``.

- [ ] **Step 4: Run to verify failure**

Run: `go test ./internal/dispatch/scheduler/`
Expected: FAIL — `engine.go` still builds `worker.Input`/reads `worker.Output`, so the v2 `OutJSON` unmarshals into the old struct as an empty status → error path, and the comment/role assertions fail.

- [ ] **Step 5: Migrate `engine.go`**

In `internal/dispatch/scheduler/engine.go`:

Rewrite the input construction in `handle` (the `inPath`/`outPath`/`in`/marshal block):

```go
	inPath := filepath.Join(dir, "task.json")
	outPath := filepath.Join(dir, "task-result.json")

	task := worker.Task{
		Issue: worker.TaskIssue{Key: iss.Identifier, Title: iss.Title},
		Repo: worker.TaskRepo{
			Name: e.cfg.Repo.Slug, SourceBranch: e.cfg.Repo.SourceBranch,
			WorkBranch: iss.Class + "/" + iss.Identifier,
		},
		Worker: worker.TaskWorker{Class: iss.Class},
		Task:   worker.TaskSpec{Brief: brief},
	}
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		e.broker(ctx, iss, errorResult(fmt.Errorf("marshal task: %w", err)), nil)
		return
	}
	if err := os.WriteFile(inPath, data, 0o600); err != nil {
		e.broker(ctx, iss, errorResult(fmt.Errorf("write task: %w", err)), nil)
		return
	}
```

Also update the earlier tempdir failure line in `handle` from `errorOutput(...)` to `errorResult(...)`.

Rewrite `broker`:

```go
// broker performs the tracker writes for one dispatch result. Single writer.
func (e *Engine) broker(ctx context.Context, iss Issue, tr worker.TaskResult, runErr error) {
	variant, _ := tr.Status.ActiveTask()
	switch {
	case runErr == nil && variant == "ok":
		e.post(ctx, iss, okComment(tr))
		e.transition(ctx, iss, RoleInReview)
	case variant == "needs-input":
		e.post(ctx, iss, needsInputComment(tr))
		e.transition(ctx, iss, RoleNeedsInput)
	default:
		e.post(ctx, iss, errorComment(tr, runErr))
		e.transition(ctx, iss, RoleNeedsInput)
	}
}
```

Rewrite the three comment builders:

```go
func okComment(tr worker.TaskResult) string {
	var b strings.Builder
	b.WriteString("✅ Done.\n\n")
	if tr.Status.OK != nil {
		if tr.Status.OK.PRURL != "" {
			b.WriteString("PR: " + tr.Status.OK.PRURL + "\n")
		}
		if tr.Status.OK.Message != "" {
			b.WriteString(tr.Status.OK.Message + "\n")
		}
	}
	if wr, ok := worker.WorkerResultFrom(tr.WorkerResult); ok &&
		wr.Status.OK != nil && wr.Status.OK.PullRequest != nil && wr.Status.OK.PullRequest.Message != "" {
		b.WriteString("\n" + wr.Status.OK.PullRequest.Message + "\n")
	}
	return b.String()
}

func needsInputComment(tr worker.TaskResult) string {
	b := "❓ NEEDS INPUT\n\n"
	if wr, ok := worker.WorkerResultFrom(tr.WorkerResult); ok && wr.Status.NeedsInput != nil {
		n := wr.Status.NeedsInput
		b += "**Doing:** " + n.Doing + "\n" +
			"**Blocker:** " + n.Blocker + "\n" +
			"**Need:** " + n.Need + "\n" +
			"**Tried:** " + n.Tried + "\n"
	}
	if tr.Status.NeedsInput != nil {
		if tr.Status.NeedsInput.Message != "" {
			b += "**Handoff:** " + tr.Status.NeedsInput.Message + "\n"
		}
		if tr.Status.NeedsInput.Commit != "" {
			b += "**Commit:** " + tr.Status.NeedsInput.Commit + "\n"
		}
	}
	return b
}

func errorComment(tr worker.TaskResult, runErr error) string {
	msg := ""
	if tr.Status.Error != nil {
		msg = tr.Status.Error.Message
		if tr.Status.Error.Detail != "" {
			msg += ": " + tr.Status.Error.Detail
		}
	}
	if msg == "" && runErr != nil {
		msg = runErr.Error()
	}
	if msg == "" {
		msg = "dispatch failed"
	}
	return "⚠️ ERROR\n\n" + msg + "\n"
}
```

Rewrite `readOutput` + replace `errorOutput` with `errorResult`:

```go
// readOutput reads a worker.TaskResult from path, synthesizing an ERROR result when
// the file is missing, unreadable, invalid, or has no valid status.
func readOutput(path string) worker.TaskResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return errorResult(fmt.Errorf("no dispatch output: %w", err))
	}
	var tr worker.TaskResult
	if err := json.Unmarshal(data, &tr); err != nil {
		return errorResult(fmt.Errorf("invalid dispatch output: %w", err))
	}
	if _, err := tr.Status.ActiveTask(); err != nil {
		return errorResult(fmt.Errorf("dispatch output: %w", err))
	}
	return tr
}

func errorResult(err error) worker.TaskResult {
	return worker.ErrorResult(err.Error(), "")
}
```

Delete the old `errorOutput` function. Keep everything else in `engine.go` (Run/tick/acquire/release/handle's claim+brief logic) unchanged.

- [ ] **Step 6: Run to verify green**

Run: `go test ./internal/dispatch/scheduler/ && go test ./... && go build ./... && go vet ./... && gofmt -l internal/`
Expected: PASS; `go.mod` unchanged. The old flat types are still defined (Task 2 deletes them) so the build stays green.

- [ ] **Step 7: Update the scheduler-flow doc**

In `docs/orchestration/scheduler-config.md`, update the stale v1 references (found at ~lines 150, 195–197):
- the injected file is now `task.json` (not `input.json`); the worker's result is `task-result.json` (not `output.json`);
- the `at-cove dispatch` example: `at-cove dispatch <kit> --in task.json --out task-result.json --timeout <timeout>`;
- the result mapping now reads the tagged-union `status` (`ok` / `needs-input` / `error`) — not the string `OK`/`NEEDS_INPUT`/`ERROR`;
- reference the canonical [`docs/usage/at-work-inputs.md`](../usage/at-work-inputs.md) / [`at-work-output.md`](../usage/at-work-output.md) for the file schemas rather than restating them; bump `updated`.

Run the docs-audit skill on the touched doc.

- [ ] **Step 8: Commit**

```bash
git add internal/dispatch/worker/resultv2.go internal/dispatch/worker/resultv2_test.go internal/dispatch/scheduler/engine.go internal/dispatch/scheduler/engine_test.go docs/orchestration/scheduler-config.md
git commit -m "feat(scheduler): construct v2 Task + broker tagged-union TaskResult"
```

---

## Task 2: delete the orphaned old flat types

With the scheduler migrated, nothing references the v1 flat contract. Remove it.

**Files:**
- Modify: `internal/dispatch/worker/types.go` (reduce to `workSubdir` + package doc)
- Delete: `internal/dispatch/worker/types_test.go`
- Modify: `internal/dispatch/worker/resultv2.go` (refresh the now-stale collision NOTE)

- [ ] **Step 1: Guard — confirm the old symbols are unused**

Run:
```
grep -rn 'worker\.\(Input\|IssueInput\|RepoInput\|Outcome\|NeedsInput\|Output\|Work\|Status\|ReadInput\|ReadOutcome\|WriteOutput\)\b' --include=*.go .
grep -rn '\b\(ReadInput\|ReadOutcome\|WriteOutput\|outcomePath\)\b' internal/dispatch/worker/ --include=*.go
```
Expected: the first prints nothing; the second prints only the declarations in `types.go` and their tests in `types_test.go`. If anything else references them, STOP and report.

- [ ] **Step 2: Reduce `types.go` to just `workSubdir`**

`workSubdir` is used by the v2 code (`format.go`'s `resolveContract`, `resultv2.go`'s `WriteTaskResult`), so it stays. Replace the entire contents of `internal/dispatch/worker/types.go` with:

```go
// Package worker implements at-work: the git/PR steps (prepare, complete) that wrap
// a worker run at-cove performs. It never runs the worker; the handoff is a cwd file
// convention under .at-work/ (task.json in, worker-result → task-result out).
package worker

// workSubdir is the per-work directory holding the .at-work/ handoff files.
const workSubdir = ".at-work"
```

(This removes `Input`/`IssueInput`/`RepoInput`/`Outcome`/`NeedsInput`/`Output`/`Work`, the `Status*` consts, `outcomePath`, `ReadInput`, `ReadOutcome`, `WriteOutput`, and the now-unused `encoding/json`/`fmt`/`os`/`path/filepath` imports.)

- [ ] **Step 3: Delete `types_test.go`**

`internal/dispatch/worker/types_test.go` tests only `ReadInput`/`ReadOutcome`/`WriteOutput` — all removed. Delete the file:

```bash
git rm internal/dispatch/worker/types_test.go
```

If, after Step 1's grep, the file turns out to contain a test for a still-present symbol, instead remove only the dead tests and keep the rest — but per the current tree it is entirely v1.

- [ ] **Step 4: Refresh the stale collision NOTE in `resultv2.go`**

The comment above `WorkerStatus` (roughly lines 16–22) explains that `WorkerNeedsInput`/`WorkerError` were named to avoid colliding with `types.go`'s `NeedsInput`/`StatusError` — which no longer exist. Replace that NOTE block with a plain description:

```go
// WorkerStatus is a tagged union — exactly one of ok / needs-input / error. The
// variant payload types are WorkerOK / WorkerNeedsInput / WorkerError; wire format is
// kebab-case json/yaml tags (see docs/usage/at-work-inputs.md).
```

- [ ] **Step 5: Run + commit**

Run: `go build ./... && go vet ./... && go test ./... && gofmt -l internal/`
Expected: PASS; `go.mod` unchanged; no dangling references.

```bash
git add internal/dispatch/worker/types.go internal/dispatch/worker/resultv2.go
git commit -m "refactor(worker): delete the v1 flat contract (Input/Outcome/Output) — v2 only"
```

---

## Final verification

- [ ] `go test ./...` — all pass (scheduler + worker on v2 only).
- [ ] `just build` — three binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/` prints nothing.
- [ ] `grep -rn 'worker\.\(Input\|Output\|Outcome\)\b\|StatusOK\|StatusNeedsInput\|StatusError\|ReadInput\|ReadOutcome\|WriteOutput' --include=*.go .` — prints nothing (the v1 contract is gone).
- [ ] `grep -rn 'workSubdir' internal/dispatch/worker/` — still defined (in `types.go`) and used by `format.go`/`resultv2.go`.
- [ ] The scheduler builds `task.json` and reads `task-result.json`; docs (`scheduler-config.md`) describe the tagged-union result. Run docs-audit on the touched docs.
- [ ] **AET-28 whole-loop check:** the scheduler → `at-cove dispatch` → reference kit → `at-work` path is v2 end-to-end; nothing writes or reads the old `input.json`/`outcome.json`/`output.json` string-status contract anywhere in `internal/` or `cmd/`.

## Notes

- **Reconciliations** (read-and-match): the exact `resultv2_test.go` imports (add `testing` only if not present); the exact set of `OutJSON` literals in `engine_test.go` (grep `OutJSON:` to catch all); whether `engine.go` still needs its `encoding/json` import (yes — `MarshalIndent` + `Unmarshal` remain).
- **Behavior preserved:** the broker's role mapping is identical to v1 — the only change is reading a tagged union instead of a status string, and sourcing doing/blocker/need/tried from the echoed `worker-result` and the retrace SHA from `task-result.needs-input.commit` (there is no more `safe-state` string; the SHA replaces it, per the AET-28 design decision).
- **Completes AET-28:** after Plan 4 the entire loop speaks v2 and the old flat contract is deleted. No follow-on plans for this issue.
