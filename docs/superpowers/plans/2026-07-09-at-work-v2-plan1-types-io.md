# at-work contract v2 — Plan 1: types + I/O (additive) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the new `.at-work/` data layer — the nested `Task`, and the tagged-union `WorkerResult`/`TaskResult` types, with JSON-or-YAML I/O (strict for task/task-result, lenient for worker-result) — **alongside** the shipped flat types, touching no existing code.

**Architecture:** A small format layer (`gopkg.in/yaml.v3`, already the only dependency) locates a contract file as `.json` or `.yml` under `.at-work/` (errors if both), decodes strict or lenient, and encodes mirroring the extension. The three contract types carry both `json:` and `yaml:` tags. Statuses are Go tagged unions (pointer-per-variant + an exactly-one validator). This plan is **purely additive** — the migration of `prepare`/`complete`/`cmd` and deletion of the old types is Plan 2.

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies**.

**Scope note:** Plan 1 of [AET-28](https://linear.app/aethons-tools/issue/AET-28). Canonical contract: [`docs/usage/at-work-inputs.md`](../../usage/at-work-inputs.md), [`docs/usage/at-work-output.md`](../../usage/at-work-output.md), and the file-format rules in [`docs/usage/at-work.md`](../../usage/at-work.md).

## Global Constraints

- Module `github.com/aethons-tools/cove`; **no new third-party dependencies** (`go.mod` stays only `gopkg.in/yaml.v3`).
- **Additive only** — do not modify or delete existing `worker` types/functions (`Input`/`Outcome`/`Output`/`ReadInput`/`ReadOutcome`/`WriteOutput`/`WriteBrief`) or `prepare`/`complete`/`cmd`. New names don't collide (`Task`, `WorkerResult`, `TaskResult`). Every commit: `go test ./...` green.
- **Files** live under `.at-work/`; each contract file is `<name>.json` **or** `<name>.yml` — **error if both** exist. Names: `task`, `worker-result`, `task-result`.
- **Parsing:** strict (unknown field → error) for `task` and `task-result`; **lenient** (accept extra fields) for `worker-result`. Reading uses `yaml.v3` for both `.json` and `.yml` (JSON is valid YAML).
- **Writing:** mirror the extension — `.json` via `encoding/json` (2-space indent), `.yml` via `yaml.v3`.
- **Statuses are tagged unions** — exactly one of `ok`/`needs-input`/`error`; a validator enforces it.
- **kebab-case keys verbatim from the usage-doc schemas** — `source-branch`, `work-branch`, `pull-request`, `needs-input`, `pr-url`, etc.; both the `json:` and `yaml:` tag carry the same value.
- **TDD, hermetic** — table tests with temp dirs; JSON and YAML round-trips; strict-reject and lenient-accept; both-extensions-error.

---

## File Structure

- `internal/dispatch/worker/format.go` (+ test) — file resolution + strict/lenient decode + extension-mirroring encode.
- `internal/dispatch/worker/taskv2.go` (+ test) — the `Task` type + `ReadTask`.
- `internal/dispatch/worker/resultv2.go` (+ test) — `WorkerResult`, `TaskResult`, the tagged-union types + validator + `ReadWorkerResult` + `WriteTaskResult`.

(New files keep the additions clearly separate from the shipped `types.go`; Plan 2 folds them in and deletes the old.)

---

## Task 1: the format layer

**Files:**
- Create: `internal/dispatch/worker/format.go`
- Test: `internal/dispatch/worker/format_test.go`

**Interfaces:**
- Produces: `resolveContract(dir, name string) (path, ext string, err error)` (ext is `".json"`/`".yml"`; err if both exist; returns `os.ErrNotExist` if neither); `decodeFile(path string, strict bool, v any) error`; `encodeFile(path string, v any) error`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dispatch/worker/format_test.go`:

```go
package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type fmtSample struct {
	Name  string `json:"name" yaml:"name"`
	Count int    `json:"count" yaml:"count"`
}

func writeAtWork(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".at-work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".at-work", file), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveContract(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := resolveContract(dir, "task"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent: want ErrNotExist, got %v", err)
	}
	writeAtWork(t, dir, "task.json", "{}")
	p, ext, err := resolveContract(dir, "task")
	if err != nil || ext != ".json" || p != filepath.Join(dir, ".at-work", "task.json") {
		t.Fatalf("json: %q %q %v", p, ext, err)
	}
	writeAtWork(t, dir, "task.yml", "{}")
	if _, _, err := resolveContract(dir, "task"); err == nil {
		t.Fatal("both .json and .yml present: want an error")
	}
}

func TestDecodeStrictAndLenient(t *testing.T) {
	dir := t.TempDir()
	// JSON with an unknown field
	writeAtWork(t, dir, "j.json", `{"name":"a","count":1,"extra":true}`)
	var s fmtSample
	if err := decodeFile(filepath.Join(dir, ".at-work", "j.json"), true, &s); err == nil {
		t.Fatal("strict JSON: unknown field must error")
	}
	if err := decodeFile(filepath.Join(dir, ".at-work", "j.json"), false, &s); err != nil || s.Name != "a" || s.Count != 1 {
		t.Fatalf("lenient JSON: %+v %v", s, err)
	}
	// YAML with an unknown field
	writeAtWork(t, dir, "y.yml", "name: b\ncount: 2\nextra: true\n")
	var s2 fmtSample
	if err := decodeFile(filepath.Join(dir, ".at-work", "y.yml"), true, &s2); err == nil {
		t.Fatal("strict YAML: unknown field must error")
	}
	if err := decodeFile(filepath.Join(dir, ".at-work", "y.yml"), false, &s2); err != nil || s2.Name != "b" {
		t.Fatalf("lenient YAML: %+v %v", s2, err)
	}
}

func TestEncodeFileMirrorsExtension(t *testing.T) {
	dir := t.TempDir()
	jp := filepath.Join(dir, "out.json")
	if err := encodeFile(jp, fmtSample{Name: "x", Count: 3}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(jp); string(b) == "" || b[0] != '{' {
		t.Fatalf("json output not JSON: %q", b)
	}
	yp := filepath.Join(dir, "out.yml")
	if err := encodeFile(yp, fmtSample{Name: "x", Count: 3}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(yp); string(b) == "" || b[0] == '{' {
		t.Fatalf("yaml output looks like JSON: %q", b)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/dispatch/worker/ -run 'TestResolveContract|TestDecode|TestEncodeFile'`
Expected: FAIL to build — `resolveContract`/`decodeFile`/`encodeFile` undefined.

- [ ] **Step 3: Implement `format.go`**

Create `internal/dispatch/worker/format.go`:

```go
package worker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// resolveContract finds .at-work/<name>.json or .at-work/<name>.yml under dir.
// It returns os.ErrNotExist if neither exists, and an error if BOTH exist.
func resolveContract(dir, name string) (path, ext string, err error) {
	base := filepath.Join(dir, workSubdir, name)
	jsonPath, ymlPath := base+".json", base+".yml"
	_, jErr := os.Stat(jsonPath)
	_, yErr := os.Stat(ymlPath)
	switch {
	case jErr == nil && yErr == nil:
		return "", "", fmt.Errorf("%s: both %s.json and %s.yml exist; keep one", workSubdir, name, name)
	case jErr == nil:
		return jsonPath, ".json", nil
	case yErr == nil:
		return ymlPath, ".yml", nil
	default:
		return "", "", os.ErrNotExist
	}
}

// decodeFile reads path (JSON or YAML — JSON is valid YAML) into v. When strict,
// an unknown field is an error.
func decodeFile(path string, strict bool, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(strict)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	return nil
}

// encodeFile writes v to path, mirroring the extension: JSON (2-space) for .json,
// YAML for anything else (.yml/.yaml).
func encodeFile(path string, v any) error {
	var out []byte
	var err error
	if filepath.Ext(path) == ".json" {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err = enc.Encode(v); err != nil {
			return err
		}
		out = buf.Bytes()
	} else if out, err = yaml.Marshal(v); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/worker/ && go vet ./internal/dispatch/worker/ && gofmt -l internal/dispatch/worker/`
Expected: PASS; clean. (`workSubdir` is the existing `".at-work"` const in `types.go`.)

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/format.go internal/dispatch/worker/format_test.go
git commit -m "feat(worker): .at-work/ JSON-or-YAML format layer (resolve/decode/encode)"
```

---

## Task 2: the `Task` type + `ReadTask`

**Files:**
- Create: `internal/dispatch/worker/taskv2.go`
- Test: `internal/dispatch/worker/taskv2_test.go`

**Interfaces:**
- Consumes: `resolveContract`, `decodeFile` (Task 1).
- Produces: `Task`, `TaskIssue`, `TaskRepo`, `TaskWorker`, `TaskSpec`; `ReadTask(dir string) (Task, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/worker/taskv2_test.go`:

```go
package worker

import "testing"

func TestReadTaskJSONAndYAML(t *testing.T) {
	jsonBody := `{"issue":{"key":"AET-33","title":"Add X"},
	  "repo":{"name":"o/r","source-branch":"main","work-branch":"implement/AET-33"},
	  "worker":{"class":"coder"},"task":{"class":"feature","brief":"do it"}}`
	yamlBody := "issue:\n  key: AET-33\n  title: Add X\n" +
		"repo:\n  name: o/r\n  source-branch: main\n  work-branch: implement/AET-33\n" +
		"worker:\n  class: coder\n" +
		"task:\n  class: feature\n  brief: do it\n"
	for _, tc := range []struct{ file, body string }{{"task.json", jsonBody}, {"task.yml", yamlBody}} {
		dir := t.TempDir()
		writeAtWork(t, dir, tc.file, tc.body)
		got, err := ReadTask(dir)
		if err != nil {
			t.Fatalf("%s: %v", tc.file, err)
		}
		if got.Issue.Key != "AET-33" || got.Repo.WorkBranch != "implement/AET-33" ||
			got.Worker.Class != "coder" || got.Task.Brief != "do it" {
			t.Fatalf("%s: %+v", tc.file, got)
		}
	}
}

func TestReadTaskRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	writeAtWork(t, dir, "task.json", `{"issue":{"key":"K","title":"T"},"repo":{"name":"o/r","source-branch":"main","work-branch":"w"},"worker":{"class":"c"},"task":{"brief":"b"},"bogus":1}`)
	if _, err := ReadTask(dir); err == nil {
		t.Fatal("unknown top-level field must error (strict)")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run TestReadTask`
Expected: FAIL — `Task`/`ReadTask` undefined.

- [ ] **Step 3: Implement `taskv2.go`**

```go
package worker

// Task is the parsed .at-work/task.json (or .yml) — the work specification.
// See docs/usage/at-work-inputs.md for the authoritative schema.
type Task struct {
	Issue  TaskIssue  `json:"issue" yaml:"issue"`
	Repo   TaskRepo   `json:"repo" yaml:"repo"`
	Worker TaskWorker `json:"worker" yaml:"worker"`
	Task   TaskSpec   `json:"task" yaml:"task"`
}

type TaskIssue struct {
	Key   string `json:"key" yaml:"key"`
	Title string `json:"title" yaml:"title"`
}

type TaskRepo struct {
	Host         string `json:"host,omitempty" yaml:"host,omitempty"`
	Name         string `json:"name" yaml:"name"`
	SourceBranch string `json:"source-branch" yaml:"source-branch"`
	WorkBranch   string `json:"work-branch" yaml:"work-branch"`
}

type TaskWorker struct {
	Class string `json:"class" yaml:"class"`
}

type TaskSpec struct {
	Class string `json:"class,omitempty" yaml:"class,omitempty"`
	Brief string `json:"brief" yaml:"brief"`
}

// ReadTask reads .at-work/task.{json,yml} (strict — unknown fields error).
func ReadTask(dir string) (Task, error) {
	path, _, err := resolveContract(dir, "task")
	if err != nil {
		return Task{}, err
	}
	var t Task
	if err := decodeFile(path, true, &t); err != nil {
		return Task{}, err
	}
	return t, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/dispatch/worker/ && go vet ./... && gofmt -l internal/dispatch/worker/`
Expected: PASS; clean; `go.mod` unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/taskv2.go internal/dispatch/worker/taskv2_test.go
git commit -m "feat(worker): Task (task.json) type + ReadTask (strict, JSON or YAML)"
```

---

## Task 3: `WorkerResult` + the tagged union + `ReadWorkerResult`

**Files:**
- Create: `internal/dispatch/worker/resultv2.go`
- Test: `internal/dispatch/worker/resultv2_test.go`

**Interfaces:**
- Consumes: `resolveContract`, `decodeFile`.
- Produces: `WorkerResult`, `WorkerStatus`, `WorkerOK`, `PullRequest`, `NeedsInput`, `StatusError`; `(WorkerStatus).Active() (variant string, err error)`; `ReadWorkerResult(dir string) (wr WorkerResult, raw any, ok bool, err error)`.

- [ ] **Step 1: Write the failing tests**

Create `internal/dispatch/worker/resultv2_test.go`:

```go
package worker

import "testing"

func TestWorkerStatusActive(t *testing.T) {
	if _, err := (WorkerStatus{}).Active(); err == nil {
		t.Fatal("zero variants: want error")
	}
	pr := &PullRequest{Title: "t", Message: "m"}
	v, err := (WorkerStatus{OK: &WorkerOK{PullRequest: pr}}).Active()
	if err != nil || v != "ok" {
		t.Fatalf("ok: %q %v", v, err)
	}
	if _, err := (WorkerStatus{OK: &WorkerOK{}, Error: &StatusError{Message: "x"}}).Active(); err == nil {
		t.Fatal("two variants: want error")
	}
}

func TestReadWorkerResultLenientAndRaw(t *testing.T) {
	dir := t.TempDir()
	// extra top-level field + extra field inside ok — both must be accepted, and
	// survive in `raw`.
	writeAtWork(t, dir, "worker-result.json",
		`{"status":{"ok":{"pull-request":{"title":"T","message":"M"}}},"note":"kept"}`)
	wr, raw, ok, err := ReadWorkerResult(dir)
	if err != nil || !ok {
		t.Fatalf("read: ok=%v err=%v", ok, err)
	}
	if v, _ := wr.Status.Active(); v != "ok" || wr.Status.OK.PullRequest.Title != "T" {
		t.Fatalf("typed: %+v", wr)
	}
	m, _ := raw.(map[string]any)
	if m["note"] != "kept" {
		t.Fatalf("raw echo dropped unknown field: %v", raw)
	}
}

func TestReadWorkerResultAbsent(t *testing.T) {
	if _, _, ok, err := ReadWorkerResult(t.TempDir()); ok || err != nil {
		t.Fatalf("absent: ok=%v err=%v (want false,nil)", ok, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run 'TestWorkerStatus|TestReadWorkerResult'`
Expected: FAIL — types/functions undefined.

- [ ] **Step 3: Implement `resultv2.go`**

```go
package worker

import (
	"errors"
	"os"
)

// WorkerResult is the parsed .at-work/worker-result.json (or .yml) — the worker's
// self-report. LENIENT: unknown fields are accepted (and preserved in the echo).
// See docs/usage/at-work-inputs.md.
type WorkerResult struct {
	Status WorkerStatus `json:"status" yaml:"status"`
}

// WorkerStatus is a tagged union — exactly one of ok / needs-input / error.
type WorkerStatus struct {
	OK         *WorkerOK    `json:"ok,omitempty" yaml:"ok,omitempty"`
	NeedsInput *NeedsInput  `json:"needs-input,omitempty" yaml:"needs-input,omitempty"`
	Error      *StatusError `json:"error,omitempty" yaml:"error,omitempty"`
}

// Active returns the name of the single set variant, or an error if not exactly one.
func (s WorkerStatus) Active() (string, error) {
	n := 0
	name := ""
	if s.OK != nil {
		n++
		name = "ok"
	}
	if s.NeedsInput != nil {
		n++
		name = "needs-input"
	}
	if s.Error != nil {
		n++
		name = "error"
	}
	if n != 1 {
		return "", errors.New("status must set exactly one of ok / needs-input / error")
	}
	return name, nil
}

type WorkerOK struct {
	PullRequest *PullRequest `json:"pull-request,omitempty" yaml:"pull-request,omitempty"`
}

type PullRequest struct {
	Title   string `json:"title" yaml:"title"`
	Message string `json:"message" yaml:"message"`
}

type NeedsInput struct {
	Doing   string `json:"doing" yaml:"doing"`
	Blocker string `json:"blocker" yaml:"blocker"`
	Need    string `json:"need" yaml:"need"`
	Tried   string `json:"tried" yaml:"tried"`
}

type StatusError struct {
	Message string `json:"message" yaml:"message"`
}

// ReadWorkerResult reads .at-work/worker-result.{json,yml} leniently. It returns the
// typed result (recognized fields) AND raw (the whole document, for the task-result
// echo, so unknown worker fields survive). ok is false if the file is absent.
func ReadWorkerResult(dir string) (wr WorkerResult, raw any, ok bool, err error) {
	path, _, err := resolveContract(dir, "worker-result")
	if errors.Is(err, os.ErrNotExist) {
		return WorkerResult{}, nil, false, nil
	}
	if err != nil {
		return WorkerResult{}, nil, false, err
	}
	if err := decodeFile(path, false, &wr); err != nil {
		return WorkerResult{}, nil, false, err
	}
	if err := decodeFile(path, false, &raw); err != nil {
		return WorkerResult{}, nil, false, err
	}
	return wr, raw, true, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/dispatch/worker/ && go vet ./... && gofmt -l internal/dispatch/worker/`
Expected: PASS; clean. (Confirm `yaml.v3` decodes an `interface{}` target into `map[string]any` — it does in v3; if the raw-echo assertion needs a different type assertion, adjust the test to the fake's actual map type.)

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/resultv2.go internal/dispatch/worker/resultv2_test.go
git commit -m "feat(worker): WorkerResult tagged union + lenient ReadWorkerResult (typed + raw echo)"
```

---

## Task 4: `TaskResult` + `WriteTaskResult`

**Files:**
- Modify: `internal/dispatch/worker/resultv2.go` (append)
- Test: `internal/dispatch/worker/resultv2_test.go` (append)

**Interfaces:**
- Consumes: `encodeFile`, the tagged-union types (Task 3).
- Produces: `TaskResult`, `TaskStatus`, `TaskOK`, `TaskNeedsInput`, `TaskError`; `WriteTaskResult(dir, ext string, tr TaskResult) error`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/dispatch/worker/resultv2_test.go`:

```go
func TestWriteTaskResultMirrorsExtension(t *testing.T) {
	tr := TaskResult{
		Status:       TaskStatus{OK: &TaskOK{Message: "opened", PRURL: "https://x/pull/1"}},
		WorkerResult: map[string]any{"status": map[string]any{"ok": map[string]any{}}, "note": "kept"},
	}
	for _, ext := range []string{".json", ".yml"} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".at-work"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WriteTaskResult(dir, ext, tr); err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		p := filepath.Join(dir, ".at-work", "task-result"+ext)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s not written: %v", ext, err)
		}
		if ext == ".json" && b[0] != '{' {
			t.Fatalf("json output not JSON: %q", b)
		}
		if ext == ".yml" && b[0] == '{' {
			t.Fatalf("yaml output looks like JSON: %q", b)
		}
		// round-trips back (leniently) with the echo intact
		var back TaskResult
		if err := decodeFile(p, false, &back); err != nil {
			t.Fatalf("%s reparse: %v", ext, err)
		}
		if v, _ := back.Status.ActiveTask(); v != "ok" || back.Status.OK.PRURL == "" {
			t.Fatalf("%s round-trip status: %+v", ext, back.Status)
		}
	}
}
```

(Uses `os`/`path/filepath`, already imported by `format_test.go` in the same package.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run TestWriteTaskResult`
Expected: FAIL — `TaskResult`/`WriteTaskResult`/`ActiveTask` undefined.

- [ ] **Step 3: Implement — append to `resultv2.go`**

```go
// TaskResult is what `at-work complete` writes to .at-work/task-result.{json,yml} —
// the authoritative outcome. STRICT on parse (see the schema in
// docs/usage/at-work-output.md). WorkerResult is the raw worker-result, echoed.
type TaskResult struct {
	Status       TaskStatus `json:"status" yaml:"status"`
	WorkerResult any        `json:"worker-result,omitempty" yaml:"worker-result,omitempty"`
}

// TaskStatus is a tagged union — exactly one of ok / needs-input / error.
type TaskStatus struct {
	OK         *TaskOK         `json:"ok,omitempty" yaml:"ok,omitempty"`
	NeedsInput *TaskNeedsInput `json:"needs-input,omitempty" yaml:"needs-input,omitempty"`
	Error      *TaskError      `json:"error,omitempty" yaml:"error,omitempty"`
}

// ActiveTask returns the name of the single set variant, or an error otherwise.
func (s TaskStatus) ActiveTask() (string, error) {
	n := 0
	name := ""
	if s.OK != nil {
		n++
		name = "ok"
	}
	if s.NeedsInput != nil {
		n++
		name = "needs-input"
	}
	if s.Error != nil {
		n++
		name = "error"
	}
	if n != 1 {
		return "", errors.New("status must set exactly one of ok / needs-input / error")
	}
	return name, nil
}

type TaskOK struct {
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
	PRURL   string `json:"pr-url,omitempty" yaml:"pr-url,omitempty"`
}

type TaskNeedsInput struct {
	Message string `json:"message" yaml:"message"`
	Commit  string `json:"commit" yaml:"commit"`
}

type TaskError struct {
	Message string `json:"message" yaml:"message"`
	Detail  string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

// WriteTaskResult writes tr to .at-work/task-result<ext> (ext is ".json" or ".yml",
// mirroring the task file). The .at-work dir must already exist.
func WriteTaskResult(dir, ext string, tr TaskResult) error {
	return encodeFile(filepath.Join(dir, workSubdir, "task-result"+ext), tr)
}
```

Add `"path/filepath"` to `resultv2.go`'s imports.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./... && go vet ./... && gofmt -l internal/ && just build`
Expected: all pass; clean; three binaries build; `go.mod` unchanged.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/resultv2.go internal/dispatch/worker/resultv2_test.go
git commit -m "feat(worker): TaskResult tagged union + WriteTaskResult (mirror extension)"
```

---

## Final verification

- [ ] `go test ./...` — all pass (incl. the untouched shipped `worker` tests).
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3`.
- [ ] `go vet ./...` clean; `gofmt -l internal/` prints nothing; `just build` builds all three binaries.
- [ ] Grep confirms **nothing shipped changed**: `git diff --stat <base>..HEAD` touches only the four new `*v2*.go`/`format*.go` files — no edits to `types.go`/`prepare.go`/`complete.go`/`cmd/`.

## Notes

- **Additive by design** — the old `Input`/`Outcome`/`Output` and `prepare`/`complete`/`cmd` are untouched, so every commit is green. Plan 2 migrates them to `Task`/`WorkerResult`/`TaskResult`, wires `cmd` to no-args, and deletes the old types + `WriteBrief`.
- **Dual tags** (`json:` + `yaml:`) are required: reads go through `yaml.v3` (handles both formats), but writes branch — JSON via `encoding/json`, YAML via `yaml.v3` — so both tag sets must be present. The values are identical kebab-case, copied from the usage-doc schemas.
- **One reconciliation** the implementer should confirm: `yaml.v3` decoding an `interface{}` target yields `map[string]any` in v3 (used by the raw-echo test); if the local version differs, adjust the test's type assertion, not the production code.
