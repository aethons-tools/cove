# at-work Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `at-work` — a two-step (`prepare`/`complete`) git/PR worker that sets up a resumable branch, then (after an agent run at-cove performs) commits, pushes, opens a PR, and writes a structured result.

**Architecture:** A hermetic orchestrator (`internal/dispatch/worker`) drives two thin real adapters — `Git` (shell git) and `CodeHost` (a GitHub PR client, `internal/dispatch/github`) — both faked in tests. `at-work` never runs the agent; the handoff is a cwd file convention (`.at-work/brief.md` in, `.at-work/outcome.json` out).

**Tech Stack:** Go 1.22, stdlib + `gopkg.in/yaml.v3` (already present) — **no new dependencies** (GitHub over `net/http`, git via `os/exec`).

## Global Constraints

- Packages `internal/dispatch/{worker,github}` + `cmd/at-work`; module `github.com/aethons-tools/cove`.
- **No new third-party dependencies**; `go.mod` must still list only `gopkg.in/yaml.v3`.
- **at-cove-agnostic:** these packages import nothing from `internal/backend|connect|assemble|kit`. `at-work` never execs or configures the agent.
- **`at-work` defines its own `Input`/`Output`/`Outcome` types** (kebab-case JSON); it does **not** reuse the scheduler's `config.Result`.
- **Handoff files (cwd):** `.at-work/brief.md` (written by `prepare`), `.at-work/outcome.json` (written by the agent, read by `complete`).
- **Statuses are `OK` / `NEEDS_INPUT` / `ERROR`** (uppercase).
- **Credential:** `AT_WORK_GIT_TOKEN` used only for clone/fetch/push/PR; **never on argv or in logs** (temp `GIT_ASKPASS` for git; `Authorization` header for the API).
- **Branch-first:** `prepare` refuses if `work-branch` is empty or equals `source-branch`, and refuses a dirty checkout. Syncs are `--ff-only`.
- **`complete` always writes a valid `output.json`** with a top-level `status`.
- **TDD, hermetic tests** — the git adapter is tested against a **local bare repo** in `t.TempDir()` (no network); the GitHub client via a fake `http.RoundTripper`; the `integration` build tag gates one live PR test.
- Spec: [`docs/superpowers/specs/2026-07-07-at-work-worker-design.md`](../specs/2026-07-07-at-work-worker-design.md).

---

## File Structure

- `internal/dispatch/worker/types.go` — `Input`/`Outcome`/`Output` types, status consts, `ReadInput`/`ReadOutcome`/`WriteBrief`/`WriteOutput`, path consts.
- `internal/dispatch/worker/git.go` — `Git` interface + `ShellGit` (real git).
- `internal/dispatch/worker/prepare.go` — `Prepare`.
- `internal/dispatch/worker/complete.go` — `CodeHost` interface + `Complete`.
- `internal/dispatch/worker/*_test.go` — hermetic tests + fakes.
- `internal/dispatch/github/github.go` (+ tests, integration test) — GitHub `CodeHost`.
- `cmd/at-work/main.go` (+ test) — the binary.
- `docs/OVERVIEW.md` — architecture map.

---

## Task 1: Types + file I/O

**Files:**
- Create: `internal/dispatch/worker/types.go`
- Test: `internal/dispatch/worker/types_test.go`

**Interfaces:**
- Produces: `Input`,`IssueInput`,`RepoInput`,`Outcome`,`NeedsInput`,`Output`,`Work`; consts `StatusOK/StatusNeedsInput/StatusError`; `ReadInput(path)`,`ReadOutcome(dir)`,`WriteBrief(dir,brief)`,`WriteOutput(path,Output)`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/worker/types_test.go`:

```go
package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "in.json")
	os.WriteFile(p, []byte(`{"issue":{"key":"AET-1","title":"T","work-class":"implement","brief":"do it"},"repo":{"name":"o/r","source-branch":"main","work-branch":"implement/AET-1"}}`), 0o600)
	in, err := ReadInput(p)
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}
	if in.Issue.Key != "AET-1" || in.Issue.Brief != "do it" || in.Repo.Name != "o/r" || in.Repo.WorkBranch != "implement/AET-1" {
		t.Fatalf("parsed wrong: %+v", in)
	}
}

func TestReadOutcome(t *testing.T) {
	dir := t.TempDir()
	// absent → ok=false, no error
	if _, ok, err := ReadOutcome(dir); ok || err != nil {
		t.Fatalf("absent outcome: ok=%v err=%v; want false,nil", ok, err)
	}
	// present
	os.MkdirAll(filepath.Join(dir, workSubdir), 0o755)
	os.WriteFile(filepath.Join(dir, workSubdir, "outcome.json"), []byte(`{"status":"OK","pr-message":"body"}`), 0o600)
	oc, ok, err := ReadOutcome(dir)
	if err != nil || !ok || oc.Status != "OK" || oc.PRMessage != "body" {
		t.Fatalf("present outcome: %+v ok=%v err=%v", oc, ok, err)
	}
}

func TestWriteBriefAndOutput(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBrief(dir, "the brief"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, workSubdir, "brief.md"))
	if string(got) != "the brief" {
		t.Fatalf("brief = %q", got)
	}
	out := filepath.Join(t.TempDir(), "out.json")
	if err := WriteOutput(out, Output{Status: StatusOK, Work: Work{PRURL: "u"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(out); len(b) == 0 {
		t.Fatal("output not written")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/worker/`
Expected: FAIL to build — undefined types/functions.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/worker/types.go`:

```go
// Package worker implements at-work: the git/PR steps (prepare, complete) that wrap
// an agent run at-cove performs. It never runs the agent; the handoff is a cwd file
// convention (.at-work/brief.md in, .at-work/outcome.json out).
package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const workSubdir = ".at-work"

// Input is the worker's task description (both subcommands read it).
type Input struct {
	Issue IssueInput `json:"issue"`
	Repo  RepoInput  `json:"repo"`
}
type IssueInput struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	WorkClass string `json:"work-class"`
	Brief     string `json:"brief"`
}
type RepoInput struct {
	Name         string `json:"name"`
	SourceBranch string `json:"source-branch"`
	WorkBranch   string `json:"work-branch"`
}

// Outcome is the agent's self-report (.at-work/outcome.json) and the "agent" block.
type Outcome struct {
	Status     string      `json:"status"`
	PRMessage  string      `json:"pr-message,omitempty"`
	NeedsInput *NeedsInput `json:"needs-input,omitempty"`
	Message    string      `json:"message,omitempty"`
}
type NeedsInput struct {
	Doing   string `json:"doing"`
	Blocker string `json:"blocker"`
	Need    string `json:"need"`
	Tried   string `json:"tried"`
}

// Output is what complete writes. Status is authoritative.
type Output struct {
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	Agent   *Outcome `json:"agent,omitempty"`
	Work    Work     `json:"work"`
}
type Work struct {
	Branch    string `json:"branch,omitempty"`
	PRURL     string `json:"pr-url,omitempty"`
	SafeState string `json:"safe-state,omitempty"`
	Error     string `json:"error,omitempty"`
}

const (
	StatusOK         = "OK"
	StatusNeedsInput = "NEEDS_INPUT"
	StatusError      = "ERROR"
)

func briefPath(dir string) string   { return filepath.Join(dir, workSubdir, "brief.md") }
func outcomePath(dir string) string { return filepath.Join(dir, workSubdir, "outcome.json") }

// ReadInput reads and decodes input.json.
func ReadInput(path string) (Input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Input{}, fmt.Errorf("read input %s: %w", path, err)
	}
	var in Input
	if err := json.Unmarshal(data, &in); err != nil {
		return Input{}, fmt.Errorf("parse input %s: %w", path, err)
	}
	return in, nil
}

// ReadOutcome reads .at-work/outcome.json from dir. ok is false if the file is absent.
func ReadOutcome(dir string) (Outcome, bool, error) {
	data, err := os.ReadFile(outcomePath(dir))
	if os.IsNotExist(err) {
		return Outcome{}, false, nil
	}
	if err != nil {
		return Outcome{}, false, err
	}
	var oc Outcome
	if err := json.Unmarshal(data, &oc); err != nil {
		return Outcome{}, false, fmt.Errorf("parse outcome: %w", err)
	}
	return oc, true, nil
}

// WriteBrief writes the brief to .at-work/brief.md, creating the dir.
func WriteBrief(dir, brief string) error {
	if err := os.MkdirAll(filepath.Join(dir, workSubdir), 0o755); err != nil {
		return err
	}
	return os.WriteFile(briefPath(dir), []byte(brief), 0o600)
}

// WriteOutput marshals out to path (pretty-printed).
func WriteOutput(path string, out Output) error {
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/worker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/types.go internal/dispatch/worker/types_test.go
git commit -m "feat(worker): input/outcome/output types + file I/O"
```

---

## Task 2: Git interface + ShellGit setup ops

**Files:**
- Create: `internal/dispatch/worker/git.go`
- Test: `internal/dispatch/worker/git_test.go`

**Interfaces:**
- Produces: `Git` interface (full, methods filled across Tasks 2–3); `NewShellGit(token) (*ShellGit, error)`; `ShellGit.{EnsureClean,Sync,RemoteHasBranch,NewBranch}`. Later tasks call the `Git` interface.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/worker/git_test.go`:

```go
package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// run execs a command in dir (or cwd if ""), failing the test on error.
func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return string(out)
}

// newRemote makes a bare repo seeded with one commit on `main`, returns its path.
func newRemote(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	bare := filepath.Join(base, "remote.git")
	run(t, "", "git", "init", "--bare", "-b", "main", bare)
	seed := filepath.Join(base, "seed")
	run(t, "", "git", "clone", bare, seed)
	os.WriteFile(filepath.Join(seed, "README.md"), []byte("hi\n"), 0o644)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-m", "init")
	run(t, seed, "git", "push", "origin", "main")
	return bare
}

func TestEnsureCleanAndSyncAndNewBranch(t *testing.T) {
	remote := newRemote(t)
	g, err := NewShellGit("")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "wt")
	ctx := context.Background()

	if err := g.EnsureClean(ctx, remote, dir); err != nil {
		t.Fatalf("EnsureClean(clone): %v", err)
	}
	if err := g.Sync(ctx, dir, "main"); err != nil {
		t.Fatalf("Sync main: %v", err)
	}
	has, err := g.RemoteHasBranch(ctx, dir, "implement/AET-1")
	if err != nil || has {
		t.Fatalf("RemoteHasBranch new = %v,%v; want false,nil", has, err)
	}
	if err := g.NewBranch(ctx, dir, "implement/AET-1", "main"); err != nil {
		t.Fatalf("NewBranch: %v", err)
	}
	// EnsureClean on the existing clean checkout is fine
	if err := g.EnsureClean(ctx, remote, dir); err != nil {
		t.Fatalf("EnsureClean(existing clean): %v", err)
	}
	// a dirty checkout is refused
	os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644)
	if err := g.EnsureClean(ctx, remote, dir); err == nil {
		t.Fatal("EnsureClean should refuse a dirty checkout")
	}
}

func TestSyncResumesExistingRemoteBranch(t *testing.T) {
	remote := newRemote(t)
	// push a work branch to the remote (a prior NEEDS_INPUT round)
	seed := filepath.Join(t.TempDir(), "s")
	run(t, "", "git", "clone", remote, seed)
	run(t, seed, "git", "checkout", "-b", "implement/AET-1")
	os.WriteFile(filepath.Join(seed, "wip.txt"), []byte("wip"), 0o644)
	run(t, seed, "git", "add", "-A")
	run(t, seed, "git", "commit", "-m", "wip")
	run(t, seed, "git", "push", "origin", "implement/AET-1")

	g, _ := NewShellGit("")
	dir := filepath.Join(t.TempDir(), "wt")
	ctx := context.Background()
	if err := g.EnsureClean(ctx, remote, dir); err != nil {
		t.Fatal(err)
	}
	has, err := g.RemoteHasBranch(ctx, dir, "implement/AET-1")
	if err != nil || !has {
		t.Fatalf("RemoteHasBranch = %v,%v; want true", has, err)
	}
	if err := g.Sync(ctx, dir, "implement/AET-1"); err != nil {
		t.Fatalf("Sync(resume): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wip.txt")); err != nil {
		t.Fatalf("resume did not restore WIP: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run 'TestEnsureClean|TestSyncResumes'`
Expected: FAIL to build — `undefined: NewShellGit`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/worker/git.go`:

```go
package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git is the git surface at-work needs. See ShellGit for the real implementation.
type Git interface {
	EnsureClean(ctx context.Context, remote, dir string) error // clone if absent; else verify clean
	Sync(ctx context.Context, dir, branch string) error        // checkout + fast-forward from origin
	RemoteHasBranch(ctx context.Context, dir, branch string) (bool, error)
	NewBranch(ctx context.Context, dir, branch, from string) error
	HasChanges(ctx context.Context, dir string) (bool, error)
	DiffersFrom(ctx context.Context, dir, base string) (bool, error)
	Commit(ctx context.Context, dir, msg string) (sha string, err error)
	Push(ctx context.Context, dir, branch string) error
	Head(ctx context.Context, dir string) (sha string, err error)
}

// ShellGit runs the git CLI. Auth for https remotes flows through a temp GIT_ASKPASS
// script (token never on argv). A bot identity is set so commits work without global
// git config.
type ShellGit struct {
	token   string
	askpass string // path to the askpass script; "" when no token
}

func NewShellGit(token string) (*ShellGit, error) {
	g := &ShellGit{token: token}
	if token != "" {
		f, err := os.CreateTemp("", "at-work-askpass-*.sh")
		if err != nil {
			return nil, err
		}
		if _, err := f.WriteString("#!/bin/sh\nprintf '%s\\n' \"$AT_WORK_ASKPASS_TOKEN\"\n"); err != nil {
			return nil, err
		}
		f.Close()
		if err := os.Chmod(f.Name(), 0o700); err != nil {
			return nil, err
		}
		g.askpass = f.Name()
	}
	return g, nil
}

func (g *ShellGit) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=at-work", "GIT_AUTHOR_EMAIL=at-work@aethons.tools",
		"GIT_COMMITTER_NAME=at-work", "GIT_COMMITTER_EMAIL=at-work@aethons.tools",
	)
	if g.askpass != "" {
		cmd.Env = append(cmd.Env, "GIT_ASKPASS="+g.askpass, "AT_WORK_ASKPASS_TOKEN="+g.token)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (g *ShellGit) EnsureClean(ctx context.Context, remote, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		_, err := g.git(ctx, "", "clone", remote, dir)
		return err
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

func (g *ShellGit) Sync(ctx context.Context, dir, branch string) error {
	if _, err := g.git(ctx, dir, "fetch", "origin", branch); err != nil {
		return err
	}
	_, err := g.git(ctx, dir, "checkout", "-B", branch, "origin/"+branch)
	return err
}

func (g *ShellGit) RemoteHasBranch(ctx context.Context, dir, branch string) (bool, error) {
	out, err := g.git(ctx, dir, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (g *ShellGit) NewBranch(ctx context.Context, dir, branch, from string) error {
	_, err := g.git(ctx, dir, "checkout", "-b", branch, from)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/worker/`
Expected: PASS (types tests + the two git tests). Requires `git` on PATH.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/git.go internal/dispatch/worker/git_test.go
git commit -m "feat(worker): Git interface + ShellGit setup ops (clone/sync/branch)"
```

---

## Task 3: ShellGit change ops

**Files:**
- Modify: `internal/dispatch/worker/git.go` (add `HasChanges`,`DiffersFrom`,`Commit`,`Push`,`Head`)
- Test: `internal/dispatch/worker/git_test.go` (add change-op tests)

**Interfaces:**
- Consumes: Task 2 `ShellGit`.git helper.
- Produces: `ShellGit.{HasChanges,DiffersFrom,Commit,Push,Head}` — completing the `Git` interface.

- [ ] **Step 1: Write the failing test**

Append to `internal/dispatch/worker/git_test.go`:

```go
func TestChangeOps(t *testing.T) {
	remote := newRemote(t)
	g, _ := NewShellGit("")
	dir := filepath.Join(t.TempDir(), "wt")
	ctx := context.Background()
	if err := g.EnsureClean(ctx, remote, dir); err != nil {
		t.Fatal(err)
	}
	if err := g.Sync(ctx, dir, "main"); err != nil {
		t.Fatal(err)
	}
	if err := g.NewBranch(ctx, dir, "implement/AET-1", "main"); err != nil {
		t.Fatal(err)
	}

	if has, _ := g.HasChanges(ctx, dir); has {
		t.Fatal("HasChanges = true on a clean tree")
	}
	os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644)
	if has, _ := g.HasChanges(ctx, dir); !has {
		t.Fatal("HasChanges = false after editing")
	}
	sha, err := g.Commit(ctx, dir, "AET-1: add new")
	if err != nil || sha == "" {
		t.Fatalf("Commit: sha=%q err=%v", sha, err)
	}
	if head, _ := g.Head(ctx, dir); head != sha {
		t.Fatalf("Head=%q; want committed sha %q", head, sha)
	}
	differs, err := g.DiffersFrom(ctx, dir, "main")
	if err != nil || !differs {
		t.Fatalf("DiffersFrom(main) = %v,%v; want true", differs, err)
	}
	if err := g.Push(ctx, dir, "implement/AET-1"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if has, _ := g.RemoteHasBranch(ctx, dir, "implement/AET-1"); !has {
		t.Fatal("branch not on origin after push")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run TestChangeOps`
Expected: FAIL to build — `undefined: (*ShellGit).HasChanges` etc.

- [ ] **Step 3: Write the implementation**

Append to `internal/dispatch/worker/git.go`:

```go
func (g *ShellGit) HasChanges(ctx context.Context, dir string) (bool, error) {
	out, err := g.git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

func (g *ShellGit) DiffersFrom(ctx context.Context, dir, base string) (bool, error) {
	out, err := g.git(ctx, dir, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "0", nil
}

func (g *ShellGit) Commit(ctx context.Context, dir, msg string) (string, error) {
	if _, err := g.git(ctx, dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := g.git(ctx, dir, "commit", "-m", msg); err != nil {
		return "", err
	}
	return g.Head(ctx, dir)
}

func (g *ShellGit) Push(ctx context.Context, dir, branch string) error {
	_, err := g.git(ctx, dir, "push", "-u", "origin", branch)
	return err
}

func (g *ShellGit) Head(ctx context.Context, dir string) (string, error) {
	return g.git(ctx, dir, "rev-parse", "HEAD")
}

// ShellGit implements Git.
var _ Git = (*ShellGit)(nil)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/worker/`
Expected: PASS (all worker tests so far).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/git.go internal/dispatch/worker/git_test.go
git commit -m "feat(worker): ShellGit change ops (has-changes/differs/commit/push/head)"
```

---

## Task 4: `Prepare`

**Files:**
- Create: `internal/dispatch/worker/prepare.go`
- Test: `internal/dispatch/worker/prepare_test.go` (+ a shared fake Git)
- Create: `internal/dispatch/worker/fakes_test.go`

**Interfaces:**
- Consumes: `Git`, `Input`, `WriteBrief`.
- Produces: `func Prepare(ctx context.Context, dir string, in Input, git Git) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/worker/fakes_test.go`:

```go
package worker

import "context"

// fakeGit records calls and returns configured values.
type fakeGit struct {
	calls          []string
	remoteHas      bool
	changes        bool
	differs        bool
	sha            string
	ensureErr      error
	failOn         string // method name to return an error from
}

func (f *fakeGit) rec(m string) { f.calls = append(f.calls, m) }
func (f *fakeGit) err(m string) error {
	if f.failOn == m {
		return context.Canceled
	}
	return nil
}
func (f *fakeGit) EnsureClean(_ context.Context, _, _ string) error { f.rec("EnsureClean"); if f.ensureErr != nil { return f.ensureErr }; return f.err("EnsureClean") }
func (f *fakeGit) Sync(_ context.Context, _, b string) error        { f.rec("Sync:" + b); return f.err("Sync") }
func (f *fakeGit) RemoteHasBranch(_ context.Context, _, _ string) (bool, error) { f.rec("RemoteHasBranch"); return f.remoteHas, f.err("RemoteHasBranch") }
func (f *fakeGit) NewBranch(_ context.Context, _, b, _ string) error { f.rec("NewBranch:" + b); return f.err("NewBranch") }
func (f *fakeGit) HasChanges(_ context.Context, _ string) (bool, error) { f.rec("HasChanges"); return f.changes, f.err("HasChanges") }
func (f *fakeGit) DiffersFrom(_ context.Context, _, _ string) (bool, error) { f.rec("DiffersFrom"); return f.differs, f.err("DiffersFrom") }
func (f *fakeGit) Commit(_ context.Context, _, _ string) (string, error) { f.rec("Commit"); return f.sha, f.err("Commit") }
func (f *fakeGit) Push(_ context.Context, _, b string) error { f.rec("Push:" + b); return f.err("Push") }
func (f *fakeGit) Head(_ context.Context, _ string) (string, error) { f.rec("Head"); return f.sha, f.err("Head") }
```

Create `internal/dispatch/worker/prepare_test.go`:

```go
package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func implementInput() Input {
	return Input{
		Issue: IssueInput{Key: "AET-1", Title: "T", WorkClass: "implement", Brief: "the brief"},
		Repo:  RepoInput{Name: "o/r", SourceBranch: "main", WorkBranch: "implement/AET-1"},
	}
}

func TestPrepareFreshBranch(t *testing.T) {
	dir := t.TempDir()
	g := &fakeGit{remoteHas: false}
	if err := Prepare(context.Background(), dir, implementInput(), g); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	joined := strings.Join(g.calls, ",")
	if !strings.Contains(joined, "EnsureClean") || !strings.Contains(joined, "Sync:main") ||
		!strings.Contains(joined, "NewBranch:implement/AET-1") || strings.Contains(joined, "Sync:implement/AET-1") {
		t.Fatalf("fresh-branch call sequence wrong: %v", g.calls)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, workSubdir, "brief.md")); string(b) != "the brief" {
		t.Fatalf("brief not written: %q", b)
	}
}

func TestPrepareResumesExistingBranch(t *testing.T) {
	g := &fakeGit{remoteHas: true}
	if err := Prepare(context.Background(), t.TempDir(), implementInput(), g); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(g.calls, ",")
	if !strings.Contains(joined, "Sync:implement/AET-1") || strings.Contains(joined, "NewBranch") {
		t.Fatalf("resume should Sync the work branch, not NewBranch: %v", g.calls)
	}
}

func TestPrepareRefusesBadWorkBranch(t *testing.T) {
	in := implementInput()
	in.Repo.WorkBranch = "main" // equals source-branch
	if err := Prepare(context.Background(), t.TempDir(), in, &fakeGit{}); err == nil {
		t.Fatal("Prepare should refuse work-branch == source-branch")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run TestPrepare`
Expected: FAIL to build — `undefined: Prepare`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/worker/prepare.go`:

```go
package worker

import (
	"context"
	"fmt"
)

// Prepare sets up (or resumes) the work branch in dir and writes the brief. It is
// idempotent: a clean existing checkout and an existing remote work-branch are reused.
func Prepare(ctx context.Context, dir string, in Input, git Git) error {
	sb, wb := in.Repo.SourceBranch, in.Repo.WorkBranch
	if wb == "" || wb == sb {
		return fmt.Errorf("work-branch must be non-empty and differ from source-branch %q", sb)
	}
	remote := "https://github.com/" + in.Repo.Name + ".git"
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
		if err := git.Sync(ctx, dir, wb); err != nil { // resume prior WIP
			return fmt.Errorf("resume %s: %w", wb, err)
		}
	} else {
		if err := git.NewBranch(ctx, dir, wb, sb); err != nil {
			return fmt.Errorf("create %s: %w", wb, err)
		}
	}
	return WriteBrief(dir, in.Issue.Brief)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/worker/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/prepare.go internal/dispatch/worker/prepare_test.go internal/dispatch/worker/fakes_test.go
git commit -m "feat(worker): Prepare (idempotent, resume-aware branch setup)"
```

---

## Task 5: `Complete`

**Files:**
- Create: `internal/dispatch/worker/complete.go`
- Test: `internal/dispatch/worker/complete_test.go`

**Interfaces:**
- Consumes: `Git` (Task 2–3), `Input`/`Outcome`/`Output`, `ReadOutcome`.
- Produces: `CodeHost` interface; `func Complete(ctx context.Context, dir string, in Input, git Git, ch CodeHost) Output` (always returns a valid Output).

- [ ] **Step 1: Write the failing test**

Add a fake CodeHost to `internal/dispatch/worker/fakes_test.go`:

```go
type fakeCodeHost struct {
	url    string
	err    error
	opened bool
}

func (f *fakeCodeHost) OpenPR(_ context.Context, _, _, _, _, _ string) (string, error) {
	f.opened = true
	return f.url, f.err
}
```

Create `internal/dispatch/worker/complete_test.go`:

```go
package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeOutcome(t *testing.T, dir, body string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, workSubdir), 0o755)
	if err := os.WriteFile(filepath.Join(dir, workSubdir, "outcome.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteOKOpensPR(t *testing.T) {
	dir := t.TempDir()
	writeOutcome(t, dir, `{"status":"OK","pr-message":"body"}`)
	g := &fakeGit{changes: true, differs: true, sha: "abc"}
	ch := &fakeCodeHost{url: "https://x/pull/1"}
	out := Complete(context.Background(), dir, implementInput(), g, ch)
	if out.Status != StatusOK || out.Work.PRURL != "https://x/pull/1" {
		t.Fatalf("out = %+v; want OK + pr-url", out)
	}
	if !ch.opened {
		t.Fatal("PR was not opened")
	}
}

func TestCompleteOKNoChangesIsError(t *testing.T) {
	dir := t.TempDir()
	writeOutcome(t, dir, `{"status":"OK"}`)
	g := &fakeGit{changes: false, differs: false} // nothing to PR
	ch := &fakeCodeHost{}
	out := Complete(context.Background(), dir, implementInput(), g, ch)
	if out.Status != StatusError {
		t.Fatalf("out = %+v; want ERROR (no changes)", out)
	}
	if ch.opened {
		t.Fatal("must not open a PR with no changes")
	}
}

func TestCompleteNeedsInputPushesNoPR(t *testing.T) {
	dir := t.TempDir()
	writeOutcome(t, dir, `{"status":"NEEDS_INPUT","needs-input":{"blocker":"b","need":"n"}}`)
	g := &fakeGit{changes: true, sha: "def"}
	ch := &fakeCodeHost{}
	out := Complete(context.Background(), dir, implementInput(), g, ch)
	if out.Status != StatusNeedsInput || out.Work.SafeState == "" {
		t.Fatalf("out = %+v; want NEEDS_INPUT + safe-state", out)
	}
	if ch.opened {
		t.Fatal("must not open a PR on needs_input")
	}
}

func TestCompleteMissingOutcomeIsError(t *testing.T) {
	out := Complete(context.Background(), t.TempDir(), implementInput(), &fakeGit{}, &fakeCodeHost{})
	if out.Status != StatusError {
		t.Fatalf("out = %+v; want ERROR for missing outcome", out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/worker/ -run TestComplete`
Expected: FAIL to build — `undefined: Complete`, `CodeHost`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/worker/complete.go`:

```go
package worker

import "context"

// CodeHost opens (or finds) a pull request.
type CodeHost interface {
	OpenPR(ctx context.Context, repo, base, head, title, body string) (prURL string, err error)
}

// Complete reads the agent's outcome and finishes the work: commit/push and, on OK,
// open a PR. It always returns a valid Output (never a Go error).
func Complete(ctx context.Context, dir string, in Input, git Git, ch CodeHost) Output {
	out := Output{Work: Work{Branch: in.Repo.WorkBranch}}
	oc, ok, err := ReadOutcome(dir)
	if err != nil {
		return errOut(out, "unreadable agent outcome: "+err.Error())
	}
	if !ok {
		return errOut(out, "no agent outcome")
	}
	out.Agent = &oc

	switch oc.Status {
	case StatusOK:
		return completeOK(ctx, dir, in, git, ch, out, oc)
	case StatusNeedsInput:
		return completeNeedsInput(ctx, dir, in, git, out)
	default: // ERROR or unknown
		msg := oc.Message
		if msg == "" {
			msg = "agent reported status " + oc.Status
		}
		return errOut(out, msg)
	}
}

func completeOK(ctx context.Context, dir string, in Input, git Git, ch CodeHost, out Output, oc Outcome) Output {
	if has, err := git.HasChanges(ctx, dir); err != nil {
		return errOut(out, "status: "+err.Error())
	} else if has {
		if _, err := git.Commit(ctx, dir, in.Issue.Key+": "+in.Issue.Title); err != nil {
			return errOut(out, "commit: "+err.Error())
		}
	}
	differs, err := git.DiffersFrom(ctx, dir, in.Repo.SourceBranch)
	if err != nil {
		return errOut(out, "diff: "+err.Error())
	}
	if !differs {
		return errOut(out, "agent reported OK but produced no changes")
	}
	if err := git.Push(ctx, dir, in.Repo.WorkBranch); err != nil {
		return errOut(out, "push: "+err.Error())
	}
	prURL, err := ch.OpenPR(ctx, in.Repo.Name, in.Repo.SourceBranch, in.Repo.WorkBranch,
		in.Issue.Key+": "+in.Issue.Title, oc.PRMessage)
	if err != nil {
		return errOut(out, "open PR: "+err.Error())
	}
	out.Status = StatusOK
	out.Message = "opened PR"
	out.Work.PRURL = prURL
	return out
}

func completeNeedsInput(ctx context.Context, dir string, in Input, git Git, out Output) Output {
	if has, err := git.HasChanges(ctx, dir); err == nil && has {
		_, _ = git.Commit(ctx, dir, "WIP "+in.Issue.Key)
	}
	_ = git.Push(ctx, dir, in.Repo.WorkBranch) // best-effort; safe state lives on origin
	head, _ := git.Head(ctx, dir)
	out.Status = StatusNeedsInput
	out.Work.SafeState = in.Repo.WorkBranch + " @ " + head
	return out
}

func errOut(out Output, msg string) Output {
	out.Status = StatusError
	out.Message = msg
	out.Work.Error = msg
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/worker/`
Expected: PASS (all worker tests).

- [ ] **Step 5: Commit**

```bash
git add internal/dispatch/worker/complete.go internal/dispatch/worker/complete_test.go internal/dispatch/worker/fakes_test.go
git commit -m "feat(worker): Complete (broker outcome → commit/push/PR → output.json)"
```

---

## Task 6: GitHub `CodeHost`

**Files:**
- Create: `internal/dispatch/github/github.go`
- Test: `internal/dispatch/github/github_test.go`
- Create: `internal/dispatch/github/github_integration_test.go`

**Interfaces:**
- Produces: `type Client`, `New(token string, httpc *http.Client) *Client`, `(*Client).OpenPR(ctx, repo, base, head, title, body) (string, error)` — satisfies `worker.CodeHost`.

- [ ] **Step 1: Write the failing test**

Create `internal/dispatch/github/github_test.go`:

```go
package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestOpenPRCreates(t *testing.T) {
	var body map[string]any
	var auth string
	c := New("tok", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		return resp(201, `{"html_url":"https://github.com/o/r/pull/7"}`), nil
	})})
	url, err := c.OpenPR(context.Background(), "o/r", "main", "implement/AET-1", "AET-1: T", "the body")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://github.com/o/r/pull/7" {
		t.Fatalf("url = %q", url)
	}
	if auth != "Bearer tok" {
		t.Fatalf("auth = %q; want Bearer tok", auth)
	}
	if body["head"] != "implement/AET-1" || body["base"] != "main" || body["title"] != "AET-1: T" {
		t.Fatalf("request body wrong: %v", body)
	}
}

func TestOpenPRReturnsExistingOn422(t *testing.T) {
	calls := 0
	c := New("tok", &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 { // create → already exists
			return resp(422, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists"}]}`), nil
		}
		// lookup existing open PR for the head
		return resp(200, `[{"html_url":"https://github.com/o/r/pull/3"}]`), nil
	})})
	url, err := c.OpenPR(context.Background(), "o/r", "main", "implement/AET-1", "t", "b")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	if url != "https://github.com/o/r/pull/3" {
		t.Fatalf("url = %q; want the existing PR", url)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatch/github/`
Expected: FAIL to build — `undefined: New`.

- [ ] **Step 3: Write the implementation**

Create `internal/dispatch/github/github.go`:

```go
// Package github is at-work's real CodeHost: a tiny GitHub PR client over net/http.
// Live calls are exercised by the integration-tagged test.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const api = "https://api.github.com"

// Client opens pull requests on GitHub. It satisfies worker.CodeHost.
type Client struct {
	http  *http.Client
	token string
}

func New(token string, httpc *http.Client) *Client {
	if httpc == nil {
		httpc = http.DefaultClient
	}
	return &Client{http: httpc, token: token}
}

// OpenPR creates a PR, or returns the URL of an existing open PR for the same head.
func (c *Client) OpenPR(ctx context.Context, repo, base, head, title, body string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"title": title, "head": head, "base": base, "body": body})
	code, raw, err := c.do(ctx, http.MethodPost, api+"/repos/"+repo+"/pulls", payload)
	if err != nil {
		return "", err
	}
	if code == http.StatusCreated {
		var out struct {
			HTMLURL string `json:"html_url"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", err
		}
		return out.HTMLURL, nil
	}
	if code == http.StatusUnprocessableEntity && strings.Contains(string(raw), "already exists") {
		return c.existing(ctx, repo, base, head)
	}
	return "", fmt.Errorf("github: create PR: http %d: %s", code, strings.TrimSpace(string(raw)))
}

func (c *Client) existing(ctx context.Context, repo, base, head string) (string, error) {
	owner := strings.SplitN(repo, "/", 2)[0]
	q := url.Values{"head": {owner + ":" + head}, "base": {base}, "state": {"open"}}
	code, raw, err := c.do(ctx, http.MethodGet, api+"/repos/"+repo+"/pulls?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("github: list PRs: http %d", code)
	}
	var prs []struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(raw, &prs); err != nil {
		return "", err
	}
	if len(prs) == 0 {
		return "", fmt.Errorf("github: PR reported existing but none found for %s", head)
	}
	return prs[0].HTMLURL, nil
}

func (c *Client) do(ctx context.Context, method, u string, payload []byte) (int, []byte, error) {
	var bodyReader *bytes.Reader
	if payload != nil {
		bodyReader = bytes.NewReader(payload)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw := new(bytes.Buffer)
	_, _ = raw.ReadFrom(resp.Body)
	return resp.StatusCode, raw.Bytes(), nil
}
```

- [ ] **Step 4: Add the integration smoke test**

Create `internal/dispatch/github/github_integration_test.go`:

```go
//go:build integration

package github

import (
	"context"
	"net/http"
	"os"
	"testing"
)

// TestLive opens a real PR. Run with:
//   GITHUB_TOKEN=… GH_REPO=owner/repo GH_BASE=main GH_HEAD=<existing-branch> \
//   go test -tags integration ./internal/dispatch/github/ -run TestLive -v
func TestLive(t *testing.T) {
	token, repo := os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_REPO")
	base, head := os.Getenv("GH_BASE"), os.Getenv("GH_HEAD")
	if token == "" || repo == "" || base == "" || head == "" {
		t.Skip("set GITHUB_TOKEN, GH_REPO, GH_BASE, GH_HEAD to run the live PR test")
	}
	url, err := New(token, http.DefaultClient).OpenPR(context.Background(), repo, base, head, "at-work smoke test", "opened by at-work TestLive")
	if err != nil {
		t.Fatalf("OpenPR: %v", err)
	}
	t.Logf("opened/found PR: %s", url)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/dispatch/github/`
Expected: PASS (create + already-exists paths; integration test excluded).

- [ ] **Step 6: Commit**

```bash
git add internal/dispatch/github/
git commit -m "feat(dispatch/github): GitHub PR client (create-or-get) + integration test"
```

---

## Task 7: `cmd/at-work` wiring

**Files:**
- Create: `cmd/at-work/main.go`
- Test: `cmd/at-work/main_test.go`
- Modify: `scripts/build.sh` (add `at-work` to the binary list)

**Interfaces:**
- Consumes: `worker.{ReadInput,Prepare,Complete,WriteOutput,NewShellGit}`, `github.New`.
- Produces: `at-work prepare|complete|version`.

- [ ] **Step 1: Write the failing test**

Create `cmd/at-work/main_test.go`:

```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "1.2.3"
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(out.String()) != "1.2.3" {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2", code)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

func TestPrepareRequiresInputPath(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"prepare"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2 (missing input path)", code)
	}
}

func TestCompleteRequiresTwoPaths(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"complete", "only-one.json"}, &out, &errOut); code != 2 {
		t.Fatalf("exit = %d; want 2 (missing output path)", code)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/at-work/`
Expected: FAIL to build — no `cmd/at-work`.

- [ ] **Step 3: Write the implementation**

Create `cmd/at-work/main.go`:

```go
// Command at-work is the git/PR worker: `prepare` sets up a branch and drops the
// brief; `complete` reads the agent's outcome and opens the PR. See docs/orchestration/.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/aethons-tools/cove/internal/dispatch/github"
	"github.com/aethons-tools/cove/internal/dispatch/worker"
)

var version = "dev"

const usage = `at-work — the at-dispatch git/PR worker

Usage:
  at-work prepare  <input.json>                 set up the branch + write .at-work/brief.md
  at-work complete <input.json> <output.json>   read .at-work/outcome.json → commit/push/PR → output.json
  at-work version

Both steps run in the current directory. Env: AT_WORK_GIT_TOKEN (code-host token).
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "prepare":
		return doPrepare(args[1:], stderr)
	case "complete":
		return doComplete(args[1:], stderr)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "at-work: unknown command %q\n\n", args[0])
		fmt.Fprint(stderr, usage)
		return 2
	}
}

func gitClient(stderr io.Writer) (*worker.ShellGit, bool) {
	g, err := worker.NewShellGit(os.Getenv("AT_WORK_GIT_TOKEN"))
	if err != nil {
		fmt.Fprintf(stderr, "at-work: %v\n", err)
		return nil, false
	}
	return g, true
}

func doPrepare(args []string, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "at-work prepare: expected <input.json>")
		return 2
	}
	in, err := worker.ReadInput(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "at-work: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	if err := worker.Prepare(context.Background(), ".", in, g); err != nil {
		fmt.Fprintf(stderr, "at-work prepare: %v\n", err)
		return 1
	}
	return 0
}

func doComplete(args []string, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "at-work complete: expected <input.json> <output.json>")
		return 2
	}
	in, err := worker.ReadInput(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "at-work: %v\n", err)
		return 1
	}
	g, ok := gitClient(stderr)
	if !ok {
		return 1
	}
	ch := github.New(os.Getenv("AT_WORK_GIT_TOKEN"), nil)
	out := worker.Complete(context.Background(), ".", in, g, ch)
	if err := worker.WriteOutput(args[1], out); err != nil {
		fmt.Fprintf(stderr, "at-work: write output: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 4: Add `at-work` to the build**

In `scripts/build.sh`, add `at-work` to the `BINARIES` list:

```bash
BINARIES=(at-cove at-dispatch at-work)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./cmd/at-work/ ./internal/dispatch/...`
Expected: PASS.

Run: `just build`
Expected: builds `at-cove`, `at-dispatch`, and `at-work`.

Run: `go vet ./... && gofmt -l cmd/ internal/dispatch/`
Expected: no vet errors; gofmt prints nothing.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-work/ scripts/build.sh
git commit -m "feat(at-work): prepare/complete CLI + build"
```

---

## Task 8: Docs — record `at-work` in the architecture map

**Files:**
- Modify: `docs/OVERVIEW.md`

- [ ] **Step 1: Update the architecture map**

In `docs/OVERVIEW.md`, add these rows after the existing `internal/dispatch/…` rows:

```
cmd/at-work/                  at-work entry: prepare / complete (git/PR worker)
internal/dispatch/worker/     at-work orchestration: Prepare + Complete, Git/CodeHost interfaces
internal/dispatch/github/     at-work's real CodeHost: GitHub PR client (live calls behind the integration tag)
```

- [ ] **Step 2: Verify**

Run: `grep -n "at-work entry" docs/OVERVIEW.md`
Expected: the new row is present.

Run: `go test ./... && go vet ./... && gofmt -l cmd/ internal/dispatch/`
Expected: all pass; gofmt clean.

- [ ] **Step 3: Commit**

```bash
git add docs/OVERVIEW.md
git commit -m "docs: record at-work in the architecture map"
```

---

## Final verification

- [ ] `go test ./...` — all packages pass (`worker`, `github` hermetic, `at-work`, plus at-cove/at-dispatch).
- [ ] `just build` — all three binaries build.
- [ ] `go.mod` still requires only `gopkg.in/yaml.v3` (no new deps).
- [ ] `go vet ./...` clean; `gofmt -l cmd/ internal/dispatch/` prints nothing.
- [ ] `git` is required on PATH for the `worker` tests (they drive a local bare repo — no network).
- [ ] Optional live check: `GITHUB_TOKEN=… GH_REPO=… GH_BASE=… GH_HEAD=… go test -tags integration ./internal/dispatch/github/ -run TestLive -v`.

## Notes

- **GitHub-only, token via `AT_WORK_GIT_TOKEN`.** The scoped-token minter (AET-24) and the at-cove/VM sequencing that injects the token on `prepare`/`complete` but not the agent step (AET-21) are out of scope here.
- **`at-work` never runs the agent.** The handoff is purely the `.at-work/brief.md` / `.at-work/outcome.json` files in the working dir.
