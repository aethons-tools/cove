# Session context — slice 4 (live refresh) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A running session picks up context edits (role/project/Jam text, grants, destinations, egress, kit prompt/notes) without a re-raise: cove-master re-fetches the bundle from Jam at every episode spawn and every wake, swaps the files atomically, and tells the agent what changed.

**Architecture:** Jam factors the raise-time compile into `Supervisor.compileContext` and serves `GET /context` (identity bearer, beside `/connector`) via `Supervisor.ContextFor(actorID)`, rebuilding the spec from the registered Instance and the role's current kit/egress. In the cove, a `contextRefresher` (shaped like `connectorRefresher`) fetches, compares `Bundle.Fingerprint`, writes the new tree into a versioned dir and atomically swaps a symlink, and returns the changed layer names. The workload calls it before each spawn (the system prompt is then current) and before writing each wake into a live episode (files current; the system prompt catches up next episode), adding a one-line notice to the text it sends. Resume prompts become per session kind (standing ≠ personal), with the kind passed from the launcher.

**Tech Stack:** Go 1.27 stdlib; `just test`.

**Spec:** [`docs/superpowers/specs/2026-10-02-session-context-layers-design.md`](../specs/2026-10-02-session-context-layers-design.md) — slice 4. Adjusted for #315's episode model ([`2026-10-02-agentrun-input-streaming-design.md`](../specs/2026-10-02-agentrun-input-streaming-design.md)): a live claude's system prompt is fixed for its episode.

## Global Constraints

- `GET /context`: GET only (405 otherwise); identity bearer (or gh `token` scheme) exactly as `/connector`; unknown/expired identity 401; identity with no registered instance 404; JSON `sessionctx.Bundle`. Logs the actor id only.
- Fetch failure, 404 (older Jam), or a malformed body → keep the last bundle (warn, keys/fingerprints only) — never fail a spawn or a wake.
- Swap is atomic: files live under `/agent-data/context.d/<fp12>/`; `/agent-data/context` is a symlink renamed into place; superseded version dirs are removed after the swap. A pre-slice-4 plain directory at `/agent-data/context` is removed once, before the first symlink.
- Notice text (verbatim):
  - new episode: `Session context changed (<layers>) since your last turn — your system prompt is current; re-open any leaf you rely on.`
  - live episode: `Session context changed (<layers>) — re-read /agent-data/context/CORE.md now; your system prompt catches up at your next episode.`
  - `<layers>` = changed layer names in delivery order, comma-separated (removed layers included).
- Resume prompts: ephemeral unchanged; personal unchanged ("Your owner may have replied…"); **standing**: `A message may have arrived — use the intercom \`read\` tool to fetch new messages, then continue. Pass \`to\` when you \`send\`.`
- No secrets in bundles (unchanged guarantee); the endpoint returns only what `Compile` produces.
- TDD, hermetic, docs in the same PR; `GOPROXY=https://proxy.golang.org,direct` here.

## Review Focus

1. **Jam returns 404 for `/context` (older Jam) or is unreachable** — expected: the session runs on its raise-time bundle, no notice, no error. Pinned in Task 4.
2. **A wake arrives mid-turn and the context changed** — expected: the coalesced wake carries the notice once, not per coalesced wake. Pinned in Task 4.
3. **The symlink swap while a reader holds the old path** — expected: old version dir removed only after the new symlink is in place; the next read sees a complete tree. Pinned in Task 3.
4. **A context change that only touches a leaf body** — expected: the layer is reported changed (layer fingerprints include leaves). Pinned in Task 1.
5. **An identity with no instance (a hand-enrolled actor)** — expected: 404, not 500. Pinned in Task 2.

---

## File structure

| File | Responsibility |
|------|----------------|
| `internal/jam/sessionctx/sessionctx.go` | `ChangedLayers(old, new Bundle) []string` |
| `internal/jam/supervisor.go` | `compileContext`, `ContextFor` |
| `internal/jam/context_handler.go` (create), `_test.go` | `NewContextHandler` |
| `cmd/at-jam/mux.go` | mount `/context` |
| `internal/agentrun/context.go` | versioned dirs + symlink swap |
| `internal/agentrun/contextrefresh.go` (create), `_test.go` | `ContextSource`, `HTTPContextSource`, `contextRefresher` |
| `internal/agentrun/workload.go` | refresh per spawn/wake, notices, per-kind resume text, `Config.SessionKind`, `Config.ContextSource` |
| `cmd/cove-master/main.go` | wire source + kind |
| `internal/connect/covemaster.go`, `internal/jam/launcher/launcher.go` | export `AT_COVE_SESSION_KIND` |
| Docs: `session-context.md`, `coves.md`, `standing-sessions.md`, `intercom.md` | |

---

### Task 1: `sessionctx.ChangedLayers`

**Interfaces:** Produces `func ChangedLayers(old, cur Bundle) []string` — layer names whose fingerprint differs or that appear in only one bundle, in delivery order (`boilerplate, kit, studio, project, role, jam`); nil when `old.Fingerprint == cur.Fingerprint`.

- [ ] **Step 1: Failing test** (`sessionctx_test.go`)

```go
func TestChangedLayers(t *testing.T) {
	base := Inputs{Session: SessionFacts{Kind: KindStanding, Name: "n", Project: "p", Role: "r"}, Role: Layer{Core: "R"}}
	a := Compile(base)
	if got := ChangedLayers(a, a); got != nil {
		t.Fatalf("same bundle: %v", got)
	}
	leaf := base
	leaf.Role = Layer{Core: "R", Leaves: []Leaf{{Name: "x.md", ReadWhen: "w", Body: "v1"}}}
	b := Compile(leaf)
	leaf.Role.Leaves[0].Body = "v2"
	c := Compile(leaf)
	if got := ChangedLayers(b, c); len(got) != 1 || got[0] != LayerRole {
		t.Fatalf("leaf-body change: %v", got)
	}
	more := base
	more.Jam = Layer{Core: "J"}
	more.Role = Layer{}
	if got := ChangedLayers(a, Compile(more)); strings.Join(got, ",") != "role,jam" {
		t.Fatalf("removed role + added jam, in delivery order: %v", got)
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/jam/sessionctx/ -run ChangedLayers` — FAIL (undefined).
- [ ] **Step 3: Implement** in `sessionctx.go`:

```go
// layerOrder is the delivery order, for reporting changes.
var layerOrder = []string{LayerBoilerplate, LayerKit, LayerStudio, LayerProject, LayerRole, LayerJam}

// ChangedLayers names the layers that differ between two bundles (changed,
// added or removed), in delivery order; nil when the bundles are identical.
func ChangedLayers(old, cur Bundle) []string {
	if old.Fingerprint == cur.Fingerprint {
		return nil
	}
	var out []string
	for _, n := range layerOrder {
		if old.Layers[n] != cur.Layers[n] {
			out = append(out, n)
		}
	}
	return out
}
```

(The per-layer hash in `Compile` already covers the core and every leaf path+body, so a leaf-body edit changes it.)

- [ ] **Step 4: Run** — PASS. **Step 5: Commit** `feat(sessionctx): ChangedLayers`

---

### Task 2: Jam — `compileContext`, `ContextFor`, `GET /context`

**Files:** Modify `internal/jam/supervisor.go`; create `internal/jam/context_handler.go`, `internal/jam/context_handler_test.go`; modify `cmd/at-jam/mux.go`; test `internal/jam/supervisor_test.go`.

**Interfaces:**
- Produces: `func (s *Supervisor) compileContext(spec RaiseSpec, actor Actor) sessionctx.Bundle` (the slice-1..3 block, moved verbatim; `Raise` calls it); `func (s *Supervisor) ContextFor(actorID string) (sessionctx.Bundle, error)` — `ErrNoInstance` when the actor has no registered instance; `func NewContextHandler(store Store, sup *Supervisor, now func() time.Time, log *slog.Logger) http.Handler`.

- [ ] **Step 1: Failing tests**

`supervisor_test.go`:

```go
// ContextFor recompiles a running session's bundle from current config.
func TestContextForTracksEdits(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Name: "bot", SessionKind: SessionKindStanding, Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	raised := *fl.gotSpec.Context
	got, err := sup.ContextFor("w1")
	if err != nil || got.Fingerprint != raised.Fingerprint {
		t.Fatalf("unchanged config must give the raise bundle: %v (%s vs %s)", err, got.Fingerprint, raised.Fingerprint)
	}
	if err := SetRoleContext(store, "default", "guest", sessionctx.Layer{Core: "NEW RULE"}); err != nil {
		t.Fatal(err)
	}
	got, _ = sup.ContextFor("w1")
	if !strings.Contains(got.Core, "NEW RULE") || !strings.Contains(got.Core, `standing session "bot"`) {
		t.Fatalf("edit not reflected, or session facts lost:\n%s", got.Core)
	}
	if _, err := sup.ContextFor("nobody"); !errors.Is(err, ErrNoInstance) {
		t.Fatalf("unknown actor = %v, want ErrNoInstance", err)
	}
}
```

`context_handler_test.go` (model the request/auth on `connector_handler_test.go`):

```go
func TestContextHandler(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	_, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"})
	if err != nil {
		t.Fatal(err)
	}
	h := NewContextHandler(store, sup, time.Now, slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/context", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := get(tok)
	var b sessionctx.Bundle
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &b) != nil || b.Fingerprint != fl.gotSpec.Context.Fingerprint {
		t.Fatalf("GET /context = %d %s", rec.Code, rec.Body)
	}
	if rec := get(""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no identity = %d", rec.Code)
	}
	if rec := get("bogus"); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown identity = %d", rec.Code)
	}
	orphan, err := Enroll(store, "hand", "default", "guest", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec := get(orphan); rec.Code != http.StatusNotFound {
		t.Errorf("identity without an instance = %d, want 404", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/context", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d", rec.Code)
	}
}
```

(`Raise` returns `(Instance, token, launchSecret, error)`; `Enroll(store, id, project, role, overrides, now)` returns the token.)

- [ ] **Step 2: Run** `go test ./internal/jam/ -run 'ContextFor|ContextHandler'` — FAIL.
- [ ] **Step 3: Implement**
  - Move the compile block from `Raise` into `compileContext(spec RaiseSpec, actor Actor) sessionctx.Bundle` (it reads `role` itself via `s.store.GetRole(spec.Project, spec.Role)`; warnings logged with `"id", spec.ActorID`). `Raise` calls `b := s.compileContext(spec, actor); spec.Context = &b`.
  - `var ErrNoInstance = errors.New("no registered instance for this identity")`.
  - `ContextFor`:

```go
// ContextFor recompiles the session context of a running instance from the
// current config — its role's kit and egress as a raise would resolve them now.
func (s *Supervisor) ContextFor(actorID string) (sessionctx.Bundle, error) {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return sessionctx.Bundle{}, ErrNoInstance
	}
	var actor Actor
	found := false
	for _, a := range s.store.ListActors() {
		if a.ID == actorID {
			actor, found = a, true
			break
		}
	}
	if !found {
		return sessionctx.Bundle{}, ErrNoInstance
	}
	spec := RaiseSpec{ActorID: actorID, Project: inst.Project, Role: inst.Role, Unit: inst.Unit, Owner: inst.Owner, Name: inst.Name, SessionKind: inst.SessionKind}
	if role, ok := s.store.GetRole(inst.Project, inst.Role); ok {
		if role.Scope.Egress != nil {
			spec.Egress = &EgressPolicy{Domains: slices.Clone(role.Scope.Egress.Domains)}
		}
		if ref, have, err := s.kitRefFor(role); err == nil && have {
			spec.Kit = ref
		}
	}
	return s.compileContext(spec, actor), nil
}
```

  - `context_handler.go`: copy `NewConnectorHandler`'s method/auth/expiry shape; on success `b, err := sup.ContextFor(a.ID)`; `errors.Is(err, ErrNoInstance)` → 404; other error → 500; else `writeJSON(w, 200, b)`.
  - `cmd/at-jam/mux.go` `coveHTTPHandler`: build `ctxH := jam.NewContextHandler(st, sup, time.Now, log)` and route `r.URL.Path == "/context"` to it in `withConnector` (rename the wrapper `withCoveEndpoints`). If `sup` is nil (a broker-only serve), answer 404.

- [ ] **Step 4: Run** `go test ./internal/jam/... ./cmd/at-jam/` — PASS. **Step 5: Commit** `feat(jam): GET /context — a running session's current context bundle`

---

### Task 3: Atomic versioned swap

**Files:** Modify `internal/agentrun/context.go`; test `internal/agentrun/context_test.go`.

**Interfaces:** `writeContext(dir string, b sessionctx.Bundle) error` keeps its signature; `dir` becomes a symlink to `<parent>/context.d/<fp12>` (fp12 = first 12 hex of `b.Fingerprint`, or `"nofp"` when empty). `clearContext(dir)` removes the symlink/dir, `dir+".new"` and `<parent>/context.d`.

- [ ] **Step 1: Failing tests** (append):

```go
func TestWriteContextSwapsSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "context")
	// A pre-slice-4 plain directory is replaced.
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "stale.md"), []byte("x"), 0o644)
	b1 := sessionctx.Bundle{Core: "ONE", Files: map[string]string{"INDEX.md": "I"}, Fingerprint: "aaaaaaaaaaaa1111"}
	if err := writeContext(dir, b1); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(dir); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("context must be a symlink: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stale.md")); !os.IsNotExist(err) {
		t.Fatal("the old plain directory's files must be gone")
	}
	held, _ := filepath.EvalSymlinks(dir) // a reader holding version 1
	b2 := sessionctx.Bundle{Core: "TWO", Files: map[string]string{"INDEX.md": "I2"}, Fingerprint: "bbbbbbbbbbbb2222"}
	if err := writeContext(dir, b2); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); string(got) != "TWO" {
		t.Fatalf("CORE.md = %q", got)
	}
	if _, err := os.Stat(held); !os.IsNotExist(err) {
		t.Fatal("the superseded version dir must be removed after the swap")
	}
	entries, _ := os.ReadDir(filepath.Join(root, "context.d"))
	if len(entries) != 1 {
		t.Fatalf("exactly one version dir must remain, got %d", len(entries))
	}
	clearContext(dir)
	for _, p := range []string{dir, filepath.Join(root, "context.d")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s survives clearContext", p)
		}
	}
}
```

Keep `TestWriteContextReplacesWholesale` and `TestWriteContextRefusesNonLocalPaths` passing.

- [ ] **Step 2: Run** `go test ./internal/agentrun/ -run WriteContext` — FAIL.
- [ ] **Step 3: Implement** `context.go`:

```go
// writeContext writes b into a fresh version dir <parent>/context.d/<fp12> and
// atomically points dir (a symlink) at it, then removes superseded versions, so
// a reader always sees one complete tree. Every path must be local.
func writeContext(dir string, b sessionctx.Bundle) error {
	files := map[string]string{"CORE.md": b.Core}
	for p, body := range b.Files {
		if !filepath.IsLocal(p) {
			return fmt.Errorf("session context: refusing non-local path %q", p)
		}
		files[p] = body
	}
	parent := filepath.Dir(dir)
	versions := filepath.Join(parent, "context.d")
	name := short(b.Fingerprint)
	if name == "" {
		name = "nofp"
	}
	target := filepath.Join(versions, name)
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	for p, body := range files {
		full := filepath.Join(target, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			return err
		}
	}
	// A pre-symlink plain directory (older cove-master) is removed once.
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink == 0 {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	tmp := dir + ".new"
	_ = os.RemoveAll(tmp)
	if err := os.Symlink(filepath.Join("context.d", name), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	entries, _ := os.ReadDir(versions)
	for _, e := range entries {
		if e.Name() != name {
			_ = os.RemoveAll(filepath.Join(versions, e.Name()))
		}
	}
	return nil
}

// clearContext removes any bundle left under dir (best effort), so a session
// running without context never reads a previous raise's: SANDBOX.md treats an
// existing CORE.md as "this is a Jam session".
func clearContext(dir string) {
	_ = os.RemoveAll(dir + ".new")
	_ = os.RemoveAll(dir)
	_ = os.RemoveAll(filepath.Join(filepath.Dir(dir), "context.d"))
}
```

(The symlink is relative, so it resolves inside `/agent-data` whatever the mount.)

- [ ] **Step 4: Run** `go test ./internal/agentrun/` — PASS. **Step 5: Commit** `feat(agentrun): swap the session context atomically via a versioned symlink`

---

### Task 4: Refresh per spawn and per wake, notices, per-kind resume prompts

**Files:** Create `internal/agentrun/contextrefresh.go`, `contextrefresh_test.go`. Modify `internal/agentrun/workload.go`, `cmd/cove-master/main.go`, `internal/connect/covemaster.go`, `internal/jam/launcher/launcher.go`. Tests: `workload_test.go`, `cmd/cove-master/main_test.go`, `internal/connect/covemaster_test.go`.

**Interfaces:**
- Produces:
  - `type ContextSource interface { Fetch(ctx context.Context) (sessionctx.Bundle, error) }`
  - `func HTTPContextSource(baseURL, token string) ContextSource` — GET `<base>/context` with `Authorization: Bearer <token>` through the default proxy-aware client (10 s timeout); non-200 → error.
  - `type contextRefresher struct` with `func (r *contextRefresher) refresh(ctx context.Context) (changed []string)` — fetch; on error keep `last`, warn, return nil; if `ChangedLayers(last, cur)` non-empty: `writeContext`; on write error keep `last` (warn) and return nil; else `last = cur` and return the changed names.
  - `Config.ContextSource ContextSource` (nil = no refresh); `Config.SessionKind string` (`ephemeral|personal|standing`, "" = ephemeral).
  - `const standingResumePrompt = "A message may have arrived — use the intercom `read` tool to fetch new messages, then continue. Pass `to` when you `send`."`
  - Env `AT_COVE_SESSION_KIND` (launcher → cove-master).

- [ ] **Step 1: Failing tests**

`contextrefresh_test.go`:

```go
package agentrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

type seqContext struct {
	bundles []sessionctx.Bundle
	errs    []error
	i       int
}

func (s *seqContext) Fetch(context.Context) (sessionctx.Bundle, error) {
	i := min(s.i, len(s.bundles)-1)
	s.i++
	if i < len(s.errs) && s.errs[i] != nil {
		return sessionctx.Bundle{}, s.errs[i]
	}
	return s.bundles[i], nil
}

func compileRole(core string) sessionctx.Bundle {
	return sessionctx.Compile(sessionctx.Inputs{Session: sessionctx.SessionFacts{Kind: "standing", Name: "n", Project: "p", Role: "r"}, Role: sessionctx.Layer{Core: core}})
}

func TestContextRefresher(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "context")
	first := compileRole("ONE")
	if err := writeContext(dir, first); err != nil {
		t.Fatal(err)
	}
	src := &seqContext{bundles: []sessionctx.Bundle{first, compileRole("TWO"), compileRole("TWO"), compileRole("THREE")}, errs: []error{nil, nil, nil, errors.New("jam down")}}
	r := newContextRefresher(src, first, dir, nil)
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("unchanged: %v", got)
	}
	if got := r.refresh(context.Background()); strings.Join(got, ",") != "role" {
		t.Fatalf("changed: %v", got)
	}
	if core, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatalf("files not rewritten: %s", core)
	}
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("same again: %v", got)
	}
	if got := r.refresh(context.Background()); got != nil {
		t.Fatalf("fetch error must keep the last bundle silently: %v", got)
	}
	if core, _ := os.ReadFile(filepath.Join(dir, "CORE.md")); !strings.Contains(string(core), "TWO") {
		t.Fatal("a failed fetch must not touch the files")
	}
}
```

`workload_test.go` — using the slice-1 `TestRunSpawnArgsWithContext` setup and the #315 scripted-episode helpers (find the resident wake test: `grep -n "func TestResident" internal/agentrun/*_test.go`):

```go
// Before each spawn the context is refreshed; the episode's first message
// carries the new-episode notice when it changed.
func TestRunRefreshesContextPerEpisode(t *testing.T) {
	// Arrange: a resident workload with Context = compileRole("ONE"),
	// ContextSource returning ONE then TWO, and a scripted spawner whose first
	// episode ends idle (stdin closed) so a Wake starts a second episode.
	// Assert:
	//  - episode 1's first stdin message is the prompt, with no notice;
	//  - episode 2's first stdin message is the standing resume prompt followed
	//    by "Session context changed (role) since your last turn — your system
	//    prompt is current; re-open any leaf you rely on.";
	//  - CORE.md (via the symlink) contains "TWO" when episode 2 is spawned.
}

// A wake written into a live episode carries the live notice once.
func TestRunWakeIntoLiveEpisodeCarriesNotice(t *testing.T) {
	// Arrange: a live episode (the #315 between-turns state) and a
	// ContextSource that changes on the wake; send two Wakes mid-turn so they
	// coalesce.
	// Assert: exactly one write after the prompt; it contains the resume text
	// and "Session context changed (role) — re-read /agent-data/context/CORE.md
	// now; your system prompt catches up at your next episode." once.
}

func TestResumeTextPerKind(t *testing.T) {
	for kind, want := range map[string]string{"standing": standingResumePrompt, "personal": residentResumePrompt, "": resumePrompt, "ephemeral": resumePrompt} {
		w := New(Config{SessionKind: kind, Resident: kind == "standing" || kind == "personal"}, nil)
		if got := w.resumeText(); got != want {
			t.Errorf("%q: resume = %q", kind, got)
		}
	}
}
```

Write both episode tests concretely with the scripted spawner and stdin capture the #315 tests use (the comments state the arrangement and the exact assertions); they are the two tests that pin Review Focus 2 and the notice texts.

`cmd/cove-master/main_test.go`: with `AT_JAM_BASE_URL`, an identity token and a context file set, `buildAgentConfig` yields a non-nil `ContextSource`; with `AT_COVE_SESSION_KIND=standing`, `cfg.SessionKind == "standing"`.

`internal/connect/covemaster_test.go`: `CoveMasterOptions{SessionKind: "standing"}` exports `AT_COVE_SESSION_KIND='standing'`; empty kind exports nothing.

- [ ] **Step 2: Run** `go test ./internal/agentrun/ ./cmd/cove-master/ ./internal/connect/` — FAIL.

- [ ] **Step 3: Implement**
  - `contextrefresh.go`: `ContextSource`, `HTTPContextSource` (mirror `httpSource` in `connector.go`: `http.Client{Timeout: 10 * time.Second}`, `GET base+"/context"`, bearer header, decode JSON), `contextRefresher{src ContextSource; last sessionctx.Bundle; dir string; log *slog.Logger}`, `newContextRefresher(src, initial, dir, log)`, `refresh` as specified (log `"agentrun: session context refreshed", "fingerprint", short(cur.Fingerprint), "layers", strings.Join(changed, ",")`; never log bodies), and:

```go
// contextNotice is the line added to what the agent is sent when its context
// changed; live = written into a running episode (system prompt still old).
func contextNotice(changed []string, live bool) string {
	if len(changed) == 0 {
		return ""
	}
	layers := strings.Join(changed, ", ")
	if live {
		return "\n\nSession context changed (" + layers + ") — re-read /agent-data/context/CORE.md now; your system prompt catches up at your next episode."
	}
	return "\n\nSession context changed (" + layers + ") since your last turn — your system prompt is current; re-open any leaf you rely on."
}
```

  - `workload.go`:
    - `Config.ContextSource`, `Config.SessionKind`; `Workload.ctx *contextRefresher` built in `Run` after the initial write succeeds (only when `cfg.ContextSource != nil && w.contextCore != ""`).
    - Before each `Spawn` (beside `conn.prepare`): `notice := ""; if w.ctx != nil { notice = contextNotice(w.ctx.refresh(ctx), false) }`; when `continued` (a resume episode) append `notice` to the first message; on the very first episode the bundle was just written at Run start, so skip the notice (call refresh only from episode 2 on).
    - Where a wake is written into a live episode (`write(resume)` at both the between-turns and the coalesced-mid-turn sites): compute `resume + contextNotice(w.ctx.refresh(ctx), true)` at write time — once per actual write, so coalesced wakes produce one notice.
    - `resumeText()`: `standing` → `standingResumePrompt`; else as today.
  - `cmd/cove-master/main.go` `buildAgentConfig`: `cfg.SessionKind = getenv("AT_COVE_SESSION_KIND")`; when `cfg.Context != nil` and `AT_JAM_BASE_URL` is set: `cfg.ContextSource = agentrun.HTTPContextSource(base, token)`.
  - `internal/connect/covemaster.go`: `CoveMasterOptions.SessionKind string`; export `AT_COVE_SESSION_KIND` when non-empty. `launcher.go`: pass `SessionKind: spec.SessionKind`.

- [ ] **Step 4: Run** `go test ./internal/agentrun/ ./cmd/cove-master/ ./internal/connect/ ./internal/jam/...` — PASS. **Step 5: Commit** `feat(agentrun): refresh session context per episode and per wake, with change notices`

---

### Task 5: Docs

- [ ] **Step 1: Edit** (docs-author):
  - `session-context.md` Delivery: add step "Refresh" — at every episode spawn and every wake cove-master re-fetches `GET /context`; files swap atomically (`context` → `context.d/<fp>`); the agent is told which layers changed (both notice texts); a new episode's system prompt is current, a live one's catches up next episode; fetch failure/older Jam keeps the last bundle. Replace the slice-2 caveat "a snapshot taken at raise … until per-turn refresh lands" with this. Kit/egress caveat: the bundle reflects the role's current kit and egress policy (the image is not rebuilt until re-raise).
  - `coves.md`: env table adds `AT_COVE_SESSION_KIND`; cove-master section: context refresh beside connector refresh (per episode and per wake).
  - `standing-sessions.md`: the resume text for standing sessions.
  - `intercom.md`: a wake may carry a "Session context changed" line.
  - Bump `updated:`.
- [ ] **Step 2: Verify** docs-audit (no new findings), `just test && just lint`.
- [ ] **Step 3: Commit** `docs(jam): live session-context refresh`

---

## Out of scope

Reporting the applied context fingerprint to Jam (like `ConnectorApplied`) and showing it in the UI; timed self-wake (spec follow-up).
