# harbor comms C2b v1 — escalation categories + `escalate(category)` — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A blocked cove declares its block category via a brokered `escalate(category)` MCP tool; harbor routes the escalation to that category's tier chain, falling back to C2 v1's default chain.

**Architecture:** Category-keyed policies are additive — `Project.Escalation` stays the default chain, `Project.EscalationByCategory` adds overrides. `Instance.EscalationCategory` (set by a brokered `POST /escalate` → `Supervisor.SetEscalationCategory`, persists — `Report` doesn't clear it) selects the chain. The escalation engine derives the chain per tick (`chainFor`, default fallback, bounds-guarded); everything else about escalation is unchanged.

**Tech Stack:** Go; the C1 `/messages` broker + `cove-master mcp` tool pattern (mirrored for `/escalate`); hermetic tests with fakes + injected clock.

## Global Constraints

- **Additive over C2 v1:** `Project.Escalation` remains the default chain; `EscalationByCategory` and `Instance.EscalationCategory` are new `omitempty` fields. A pre-C2b store loads with a nil map / empty category → every cove uses the default chain, exactly as C2 v1. No migration.
- **Category persists:** `Instance.EscalationCategory` is written ONLY by `Supervisor.SetEscalationCategory`. `Report` must NOT touch it (it still resets the tier state `EscalationTier`/`TierPingedAt` on entering Waiting, but the category survives). This sidesteps the `escalate()`→`Report(Waiting)` ordering problem.
- **Unknown category is safe by construction:** `chainFor` falls back to the default chain for an unset/unknown/empty-configured category. A cove-supplied category never selects a recipient the operator didn't configure — it only selects among operator-defined tier chains. It does not touch the comms access-graph or the broker credential path.
- **`escalate` only categorizes** — escalation still auto-opens on Waiting (C2 v1); the tool does not trigger an escalation.
- **`/escalate` stays harbor-core-clean** like `/messages` (Bearer→actor→supervisor; no kit/grpc/dispatch imports). `internal/escalate` still imports only `harbor` + stdlib.
- **Self-scoped by construction:** `/escalate` derives the target instance from the authenticated actor; there is no actor/target parameter.
- **Secrets/bodies never logged;** the category string is non-secret and may be logged. `cove-master`'s identity token stays env-only, never logged/argv.
- **The C2 v1 invariant is preserved:** `TierPingedAt.IsZero()` ⇔ escalation open; wake-on still owns reply-detection/waking/teardown; escalation only pings.
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Prefix go commands with `GOPROXY=off`.

---

### Task 1: Category policy + runtime type + store + supervisor (harbor core)

**Files:**
- Modify: `internal/harbor/identity.go` (`Project.EscalationByCategory`)
- Modify: `internal/harbor/instance.go` (`Instance.EscalationCategory`)
- Modify: `internal/harbor/filestore.go` (`SetEscalationPolicy` signature + `GetProject` map deep-copy + `Store` interface)
- Modify: `internal/harbor/supervisor.go` (`SetEscalationCategory`)
- Modify: `internal/harbor/admin.go` (ONE line: the PUT route passes `""` to keep the build green — behavior unchanged)
- Test: `internal/harbor/filestore_test.go`, `internal/harbor/supervisor_test.go`

**Interfaces:**
- Produces: `Project.EscalationByCategory map[string][]EscalationTier`; `Instance.EscalationCategory string`; `Store.SetEscalationPolicy(project, category string, tiers []EscalationTier) error`; `Supervisor.SetEscalationCategory(actorID, category string) error`.

- [ ] **Step 1: Write failing tests**

`internal/harbor/filestore_test.go`:

```go
func TestEscalationByCategoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	fs, _ := NewFileStore(path)
	def := []EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}}
	infra := []EscalationTier{{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}
	if err := fs.SetEscalationPolicy("acme", "", def); err != nil {
		t.Fatal(err)
	}
	if err := fs.SetEscalationPolicy("acme", "infra", infra); err != nil {
		t.Fatal(err)
	}
	fs2, _ := NewFileStore(path) // reload
	p, ok := fs2.GetProject("acme")
	if !ok || len(p.Escalation) != 1 || p.Escalation[0].Targets[0] != "human:oncall" {
		t.Fatalf("default chain not persisted: %+v", p.Escalation)
	}
	if got := p.EscalationByCategory["infra"]; len(got) != 1 || got[0].Targets[0] != "human:sre" || got[0].Timeout != 10*time.Minute {
		t.Fatalf("infra chain not persisted: %+v", p.EscalationByCategory)
	}
}

func TestGetProjectDeepCopiesCategoryMap(t *testing.T) {
	fs, _ := NewFileStore(filepath.Join(t.TempDir(), "s.json"))
	_ = fs.SetEscalationPolicy("p", "infra", []EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}})
	p, _ := fs.GetProject("p")
	p.EscalationByCategory["infra"][0].Targets[0] = "mutated" // must not corrupt the store
	p.EscalationByCategory["added"] = nil                     // must not appear in the store
	p2, _ := fs.GetProject("p")
	if p2.EscalationByCategory["infra"][0].Targets[0] != "human:a" {
		t.Fatalf("category chain aliased: %v", p2.EscalationByCategory["infra"][0].Targets)
	}
	if _, ok := p2.EscalationByCategory["added"]; ok {
		t.Fatal("category map aliased (new key leaked into store)")
	}
}

func TestMigrationLeavesNilCategoryMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.json")
	// a pre-C2b store: projects with an escalation default but no category key
	if err := os.WriteFile(path, []byte(`{"projects":{"acme":{"name":"acme","escalation":[{"targets":["human:a"],"timeout":60000000000}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := fs.GetProject("acme")
	if p.EscalationByCategory != nil {
		t.Fatalf("expected nil category map on migration, got %+v", p.EscalationByCategory)
	}
	if len(p.Escalation) != 1 {
		t.Fatalf("default chain lost on migration: %+v", p.Escalation)
	}
}
```

`internal/harbor/supervisor_test.go`:

```go
func TestSetEscalationCategoryPersistsAcrossWaitingEntry(t *testing.T) {
	sup, st := newTestSupervisor(t) // file's existing helper
	st.putInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityRunning})
	if err := sup.SetEscalationCategory("cove-1", "infra"); err != nil {
		t.Fatal(err)
	}
	// entering Waiting resets tier state but must NOT clear the category
	if err := sup.Report(context.Background(), "cove-1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetInstance("cove-1")
	if got.EscalationCategory != "infra" {
		t.Fatalf("category must persist across Waiting-entry, got %q", got.EscalationCategory)
	}
	if !got.TierPingedAt.IsZero() {
		t.Fatal("tier state should still reset on entering Waiting")
	}
}
```

> Adapt `newTestSupervisor`/`st.putInstance`/`ActivityRunning` to the file's real names.

- [ ] **Step 2: Run tests, verify they fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'EscalationByCategory|GetProjectDeepCopiesCategory|MigrationLeavesNilCategory|SetEscalationCategoryPersists' -v` → FAIL.

- [ ] **Step 3: Add fields**

`identity.go`, in `Project` (after `Escalation`):
```go
	EscalationByCategory map[string][]EscalationTier `json:"escalation_by_category,omitempty"` // category → chain; overrides Escalation (the default)
```
`instance.go`, in `Instance` (after `TierPingedAt`):
```go
	EscalationCategory string `json:"escalation_category,omitempty"` // cove-declared block category; "" = default chain
```

- [ ] **Step 4: Store signature + map deep-copy (filestore.go)**

Change `SetEscalationPolicy` to take a `category`:
```go
func (fs *FileStore) SetEscalationPolicy(project, category string, tiers []EscalationTier) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p := fs.projects[project]
	p.Name = project
	if category == "" {
		p.Escalation = tiers
	} else {
		if p.EscalationByCategory == nil {
			p.EscalationByCategory = map[string][]EscalationTier{}
		}
		p.EscalationByCategory[category] = tiers
	}
	fs.projects[project] = p
	return fs.save()
}
```
Update the `Store` interface line to `SetEscalationPolicy(project, category string, tiers []EscalationTier) error`.

In `GetProject`, after the existing `Escalation` deep-copy, add the category-map deep-copy:
```go
	if p.EscalationByCategory != nil {
		m := make(map[string][]EscalationTier, len(p.EscalationByCategory))
		for cat, tiers := range p.EscalationByCategory {
			cp := append([]EscalationTier(nil), tiers...)
			for i := range cp {
				cp[i].Targets = append([]string(nil), cp[i].Targets...)
			}
			m[cat] = cp
		}
		p.EscalationByCategory = m
	}
```

- [ ] **Step 5: Supervisor method (supervisor.go)**

```go
// SetEscalationCategory stamps the cove-declared block category on its instance.
// Persists until re-declared or teardown (Report does not clear it); the
// escalation engine reads it to pick the tier chain, falling back to the default
// when the category isn't configured. No-op semantics if the actor is gone.
func (s *Supervisor) SetEscalationCategory(actorID, category string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.EscalationCategory = category
	return s.store.PutInstance(inst)
}
```

(Confirm `Report`'s `enteringWaiting` block does NOT set `EscalationCategory` — it should be untouched. Do not add anything there.)

- [ ] **Step 6: Keep the build green (admin.go)**

The store signature changed, so the C2 v1 admin PUT route no longer compiles. Update ONLY that call to pass `""` (behavior unchanged — Task 5 will add the category param):
```go
		if err := store.SetEscalationPolicy(r.PathValue("project"), "", b.Tiers); err != nil {
```
Then find every OTHER caller of `store.SetEscalationPolicy` (mostly tests) with `GOPROXY=off go test ./...` and update them to pass a category argument (`""` for the default). (adminclient/CLI call the HTTP API, not the store, so they are unaffected here.)

- [ ] **Step 7: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'EscalationByCategory|GetProjectDeepCopies|MigrationLeavesNil|SetEscalationCategory' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → all pass.

- [ ] **Step 8: gofmt + commit**

```bash
gofmt -w internal/harbor/*.go
git add internal/harbor/identity.go internal/harbor/instance.go internal/harbor/filestore.go internal/harbor/supervisor.go internal/harbor/admin.go internal/harbor/filestore_test.go internal/harbor/supervisor_test.go
git commit -m "harbor: category-keyed escalation policy + Instance.EscalationCategory (COV-167)" # + trailers
```

---

### Task 2: Engine category routing (`chainFor`)

**Files:**
- Modify: `internal/escalate/escalate.go`
- Test: `internal/escalate/escalate_test.go`

**Interfaces:**
- Consumes: `Project.EscalationByCategory`, `Instance.EscalationCategory` (Task 1).

- [ ] **Step 1: Write failing tests**

Add to `internal/escalate/escalate_test.go` (reuse the file's `fakeReg`/`fakeProjects`/`fakeState`/`fakePinger` + injected clock):

```go
func TestRoutesByCategory(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting, EscalationCategory: "infra"}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation:           []harbor.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]harbor.EscalationTier{"infra": {{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}},
		Roster:               harbor.Roster{Humans: []harbor.Human{{Name: "oncall", Handle: "oncall.h"}, {Name: "sre", Handle: "sre.h"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if !strings.Contains(pg.lastBody, "@sre.h") || strings.Contains(pg.lastBody, "@oncall.h") {
		t.Fatalf("infra category must ping @sre.h (not the default @oncall.h); body=%q", pg.lastBody)
	}
}

func TestUnknownCategoryFallsBackToDefault(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting, EscalationCategory: "nonexistent"}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation:           []harbor.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]harbor.EscalationTier{"infra": {{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}},
		Roster:               harbor.Roster{Humans: []harbor.Human{{Name: "oncall", Handle: "oncall.h"}, {Name: "sre", Handle: "sre.h"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if !strings.Contains(pg.lastBody, "@oncall.h") {
		t.Fatalf("unknown category must fall back to default chain (@oncall.h); body=%q", pg.lastBody)
	}
}

func TestEmptyCategoryUsesDefault(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting}}} // no category
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation:           []harbor.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]harbor.EscalationTier{"infra": {{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}},
		Roster:               harbor.Roster{Humans: []harbor.Human{{Name: "oncall", Handle: "oncall.h"}, {Name: "sre", Handle: "sre.h"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if !strings.Contains(pg.lastBody, "@oncall.h") {
		t.Fatalf("empty category must use default chain; body=%q", pg.lastBody)
	}
}

func TestCategoryAdvanceUsesCategoryChainTimeout(t *testing.T) {
	clock := time.Unix(2000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting,
		EscalationCategory: "infra", EscalationTier: 0, TierPingedAt: clock.Add(-11 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation:           []harbor.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]harbor.EscalationTier{"infra": {
			{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute},
			{Targets: []string{"human:lead"}, Timeout: time.Hour}}},
		Roster: harbor.Roster{Humans: []harbor.Human{{Name: "sre", Handle: "s"}, {Name: "lead", Handle: "l"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.lastTier != 1 || !strings.Contains(pg.lastBody, "@l") {
		t.Fatalf("infra tier-0 (10m) elapsed → advance to infra tier-1 (@l); tier=%d body=%q", st.lastTier, pg.lastBody)
	}
}
```

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/escalate/ -run 'Category' -v` → FAIL.

- [ ] **Step 3: Implement `chainFor` + rewire `tick`/`pingTier`/`resolveHandles`**

Add:
```go
// chainFor picks the tier chain for a cove's declared category, falling back to
// the Project's default chain for an unset/unknown/empty-configured category.
func chainFor(proj harbor.Project, category string) []harbor.EscalationTier {
	if c, ok := proj.EscalationByCategory[category]; ok && len(c) > 0 {
		return c
	}
	return proj.Escalation
}
```

Rewrite the body of `tick`'s per-instance loop (after resolving `proj`):
```go
		chain := chainFor(proj, inst.EscalationCategory)
		if len(chain) == 0 {
			continue // neither a category chain nor a default configured
		}
		if inst.TierPingedAt.IsZero() {
			e.pingTier(ctx, inst, proj, chain, 0)
			continue
		}
		cur := inst.EscalationTier
		if cur+1 < len(chain) && e.now().Sub(inst.TierPingedAt) > chain[cur].Timeout {
			e.pingTier(ctx, inst, proj, chain, cur+1)
		}
```
(The old `len(proj.Escalation) == 0` guard is replaced by `len(chain) == 0` after `chainFor`.)

Change `pingTier` and `resolveHandles` to take the resolved chain instead of indexing `proj.Escalation`:
```go
func (e *Engine) pingTier(ctx context.Context, inst harbor.Instance, proj harbor.Project, chain []harbor.EscalationTier, tier int) {
	handles := e.resolveHandles(proj.Roster, chain, tier)
	// … unchanged body: if len(handles)>0 { IssueByIdentifier(inst.Unit); PostComment(...) }; SetEscalation(inst.ActorID, tier, e.now()) …
}

func (e *Engine) resolveHandles(roster harbor.Roster, chain []harbor.EscalationTier, tier int) []string {
	var handles []string
	for _, target := range chain[tier].Targets {
		// … unchanged: strings.Cut on ":", require "human", match roster.Humans by name → "@"+Handle, warn otherwise …
	}
	return handles
}
```

> Keep the C2 v1 body of `pingTier`/`resolveHandles` intact except for taking `chain`/`roster` params instead of reaching into `proj.Escalation`. The ping body string (`"… (escalation tier N)"`), the transient-error-doesn't-advance and empty-tier-advances semantics, and the warns are all unchanged.

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/escalate/ -v` → PASS (new category tests + all existing v1 tests).
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w internal/escalate/escalate.go internal/escalate/escalate_test.go
git add internal/escalate/
git commit -m "harbor: escalation engine routes by declared category (chainFor) (COV-167)" # + trailers
```

---

### Task 3: `POST /escalate` endpoint + `EscalateHandler` + mux

**Files:**
- Create: `internal/harbor/escalate_handler.go`
- Modify: `cmd/at-harbor/mux.go` (`messagesMux` routes `/escalate`)
- Modify: `cmd/at-harbor/main.go` (build + mount the handler)
- Test: `internal/harbor/escalate_handler_test.go`

**Interfaces:**
- Consumes: `Actor`, `Instance`, `HashToken` (existing); `Supervisor.SetEscalationCategory` (Task 1).
- Produces: `harbor.NewEscalateHandler(store escalateStore, setter categorySetter, log) *EscalateHandler` (an `http.Handler`); mounted at `POST /escalate`.

- [ ] **Step 1: Write failing tests**

`internal/harbor/escalate_handler_test.go` — mirror `messages_test.go`'s auth/token helpers (read it first for the token→hash helper + a fake store):

```go
func TestEscalateStampsCategory(t *testing.T) {
	st := newFakeEscStore() // Lookup + GetInstance
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	setter := &fakeCategorySetter{}
	h := NewEscalateHandler(st, setter, testLogger())

	rec := postEscalate(t, h, "tok-for-h", `{"category":"infra"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	if setter.actorID != "cove-1" || setter.category != "infra" {
		t.Fatalf("SetEscalationCategory(%q,%q)", setter.actorID, setter.category)
	}
}

func TestEscalateMissingTokenIs401(t *testing.T) { /* no Bearer → 401; setter not called */ }
func TestEscalateUnknownTokenIs401(t *testing.T) { /* token not in store → 401 */ }
func TestEscalateNoInstanceIs403(t *testing.T)   { /* actor exists, no Instance → 403 */ }
func TestEscalateOversizeBodyIs413(t *testing.T) { /* body > cap → 413; setter not called */ }
func TestEscalateGetIs405(t *testing.T)          { /* GET → 405 */ }
func TestEscalateEmptyCategoryAllowed(t *testing.T) { /* {"category":""} → 204, setter called with "" */ }
```

Fill the sketched bodies with real assertions mirroring `messages_test.go`. Provide `newFakeEscStore`, `fakeCategorySetter{actorID, category string; called bool}`, and `postEscalate` (POST with `Authorization: Bearer <tok>`), reusing the message tests' token→hash mapping.

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'Escalate' -v` → FAIL (no handler).

- [ ] **Step 3: Implement `escalate_handler.go`**

```go
package harbor

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// maxEscalateBodyBytes caps the /escalate POST body (category is short).
const maxEscalateBodyBytes = 1024

type escalateStore interface {
	Lookup(tokenHash string) (Actor, bool)
	GetInstance(actorID string) (Instance, bool)
}

type categorySetter interface {
	SetEscalationCategory(actorID, category string) error
}

// EscalateHandler is harbor's brokered escalation-category endpoint: an
// authenticated cove declares its block category, which harbor stamps on the
// caller's OWN instance (self-scoped by construction — no target parameter).
// The escalation engine reads it to pick the tier chain. Implements http.Handler.
type EscalateHandler struct {
	store  escalateStore
	setter categorySetter
	log    *slog.Logger
}

func NewEscalateHandler(store escalateStore, setter categorySetter, log *slog.Logger) *EscalateHandler {
	return &EscalateHandler{store: store, setter: setter, log: log}
}

func (h *EscalateHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		http.Error(w, "missing identity", http.StatusUnauthorized)
		return
	}
	actor, ok := h.store.Lookup(HashToken(tok))
	if !ok {
		http.Error(w, "unknown identity", http.StatusUnauthorized)
		return
	}
	if _, ok := h.store.GetInstance(actor.ID); !ok {
		http.Error(w, "no instance", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxEscalateBodyBytes)
	var req struct {
		Category string `json:"category"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := h.setter.SetEscalationCategory(actor.ID, req.Category); err != nil {
		h.log.Error("escalate: set category failed", "actor", actor.ID, "error", err.Error())
		http.Error(w, "set category failed", http.StatusBadGateway)
		return
	}
	h.log.Info("escalate", "actor", actor.ID, "category", req.Category)
	w.WriteHeader(http.StatusNoContent)
}
```

> Match the exact auth/token/lookup shape used by `messages.go`'s `ServeHTTP` (it uses the same `store.Lookup(HashToken(tok))` primitive). If `messages_test.go`'s fake store already provides `Lookup`/`GetInstance`, reuse it in the test rather than defining a second fake.

- [ ] **Step 4: Route `/escalate` in the mux + wire the handler**

`cmd/at-harbor/mux.go` — extend `messagesMux` to take and route to the escalate handler:
```go
func messagesMux(msgH, escH, broker http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/messages", "/messages/targets":
			msgH.ServeHTTP(w, r)
		case "/escalate":
			escH.ServeHTTP(w, r)
		default:
			broker.ServeHTTP(w, r)
		}
	})
}
```

`cmd/at-harbor/main.go` — where `msgH`/`messagesMux` are built (near line 1042), build the escalate handler and pass it:
```go
		escH := harbor.NewEscalateHandler(st, sup, log)
		httpHandler = messagesMux(msgH, escH, broker)
```
(`st` satisfies `escalateStore` via `Lookup`/`GetInstance`; `sup` satisfies `categorySetter` via `SetEscalationCategory`.) Update `mux_test.go` if it asserts the `messagesMux` signature/routing — add an `/escalate`→escH case.

- [ ] **Step 5: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'Escalate' ./cmd/at-harbor/ -run 'Mux|Messages' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 6: gofmt + commit**

```bash
gofmt -w internal/harbor/escalate_handler.go internal/harbor/escalate_handler_test.go cmd/at-harbor/mux.go cmd/at-harbor/main.go cmd/at-harbor/mux_test.go
git add internal/harbor/escalate_handler.go internal/harbor/escalate_handler_test.go cmd/at-harbor/mux.go cmd/at-harbor/main.go cmd/at-harbor/mux_test.go
git commit -m "harbor: brokered POST /escalate endpoint stamps cove block category (COV-167)" # + trailers
```

---

### Task 4: cove-master MCP `escalate` tool

**Files:**
- Modify: `cmd/cove-master/mcp.go`
- Test: `cmd/cove-master/mcp_test.go`

**Interfaces:**
- Consumes: harbor `POST /escalate` (`{category}`) (Task 3).
- Produces: MCP `escalate` tool.

- [ ] **Step 1: Write failing test**

```go
func TestMCPEscalateForwardsCategory(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_HARBOR_RUNTIME_ADDR":
			return srv.URL
		case "AT_HARBOR_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.escalate(context.Background(), "infra"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/escalate" || !strings.Contains(gotBody, `"category":"infra"`) {
		t.Fatalf("path=%q body=%q", gotPath, gotBody)
	}
}
```

- [ ] **Step 2: Run test, verify fail**

Run: `GOPROXY=off go test ./cmd/cove-master/ -run 'MCPEscalate' -v` → FAIL.

- [ ] **Step 3: Implement — client method + tool**

Add the client method (reuse `do(ctx, method, pathSuffix, body)`):
```go
func (c *messagingClient) escalate(ctx context.Context, category string) error {
	payload, err := json.Marshal(struct {
		Category string `json:"category"`
	}{Category: category})
	if err != nil {
		return fmt.Errorf("encoding escalate payload: %w", err)
	}
	_, err = c.do(ctx, http.MethodPost, "/escalate", payload)
	return err
}
```
Register the tool in `newMessagingServer` (add an `escalateIn` input type):
```go
type escalateIn struct {
	Category string `json:"category" jsonschema:"the block category to route escalation by, e.g. infra / ticket-blocked / code-architecture (free-form; unknown falls back to the default tier chain)"`
}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "escalate",
		Description: "Declare the category of your current block so harbor routes the escalation to the right on-call tier. Call this before you finish a turn needing input; it categorizes, it does not itself page anyone.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in escalateIn) (*mcp.CallToolResult, any, error) {
		if cfgErr != nil {
			return nil, nil, cfgErr
		}
		if err := client.escalate(ctx, in.Category); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	})
```

- [ ] **Step 4: Run tests, verify pass**

Run: `GOPROXY=off go test ./cmd/cove-master/ -v` → PASS (new + existing).
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass. Remove any stray binary.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -w cmd/cove-master/mcp.go cmd/cove-master/mcp_test.go
git add cmd/cove-master/mcp.go cmd/cove-master/mcp_test.go
git commit -m "harbor: cove-master MCP escalate(category) tool (COV-167)" # + trailers
```

---

### Task 5: Operator surface — category-aware admin + adminclient + CLI

**Files:**
- Modify: `internal/harbor/admin.go` (`EscalationBody.Category`, `EscalationView`, GET/PUT)
- Modify: `internal/harbor/adminclient/adminclient.go` (`SetEscalationPolicy` signature + `GetEscalationPolicy` → view)
- Modify: `cmd/at-harbor/main.go` (`cmdProjectEscalation` `--category`)
- Test: `internal/harbor/admin_test.go`, `internal/harbor/adminclient/adminclient_test.go`, `cmd/at-harbor/main_test.go`

**Interfaces:**
- Consumes: store `SetEscalationPolicy(project, category, tiers)` + `GetProject` (Task 1).
- Produces: `EscalationBody{Category, Tiers}`; `EscalationView{Default, ByCategory}`; adminclient `SetEscalationPolicy(project, category, tiers)` + `GetEscalationPolicy(project) (harbor.EscalationView, error)`; CLI `--category`.

- [ ] **Step 1: Write failing tests**

Mirror the C2 v1 escalation tests (`TestAdminEscalationRoutes`, `TestClientEscalationPolicy`, `TestProjectEscalationCommands`). Add real assertions for:
- `TestAdminEscalationCategoryRoutes`: PUT `{category:"infra", tiers:[…]}` then GET → `EscalationView` with the infra chain under `ByCategory["infra"]` and the default under `Default` (PUT with no category sets Default).
- `TestClientEscalationCategory`: adminclient `SetEscalationPolicy(p, "infra", tiers)` + `GetEscalationPolicy(p)` returns a view with both.
- `TestProjectEscalationCategoryCommands`: `project escalation set p --category infra --tier 'human:sre@10m'` then `escalation list p` shows a default (if set) and an `infra` section; `escalation clear p --category infra` removes just infra.

Update the EXISTING C2 v1 escalation tests to the new signatures (adminclient `SetEscalationPolicy` gains a `category` arg → pass `""`; `GetEscalationPolicy` now returns a view → read `.Default`).

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/ ./cmd/at-harbor/ -run 'Escalation' -v` → FAIL.

- [ ] **Step 3: admin.go — body/view + routes**

```go
// EscalationBody is the PUT /admin/projects/{project}/escalation body: the tiers
// for one category ("" targets the default/uncategorized chain).
type EscalationBody struct {
	Category string           `json:"category,omitempty"`
	Tiers    []EscalationTier `json:"tiers"`
}

// EscalationView is the GET /admin/projects/{project}/escalation response.
type EscalationView struct {
	Default    []EscalationTier            `json:"default"`
	ByCategory map[string][]EscalationTier `json:"by_category,omitempty"`
}
```
GET returns `EscalationView{Default: p.Escalation, ByCategory: p.EscalationByCategory}`. PUT passes `b.Category`:
```go
		if err := store.SetEscalationPolicy(r.PathValue("project"), b.Category, b.Tiers); err != nil { … }
		log.Info("admin escalation policy", "operator", OperatorID(r), "project", r.PathValue("project"), "category", b.Category, "tiers", len(b.Tiers))
```

- [ ] **Step 4: adminclient — signature + view**

```go
func (c *Client) SetEscalationPolicy(project, category string, tiers []harbor.EscalationTier) error {
	return c.do("PUT", "/admin/projects/"+url.PathEscape(project)+"/escalation", harbor.EscalationBody{Category: category, Tiers: tiers}, nil)
}
func (c *Client) GetEscalationPolicy(project string) (harbor.EscalationView, error) {
	var v harbor.EscalationView
	err := c.do("GET", "/admin/projects/"+url.PathEscape(project)+"/escalation", nil, &v)
	return v, err
}
```

- [ ] **Step 5: CLI — `--category` on set/list/clear (main.go)**

In `cmdProjectEscalation`: add `category := fs.String("category", "", "escalation category (default chain when empty); set/clear")`.
- `set`: `c.SetEscalationPolicy(pos[0], *category, tiers)`.
- `clear`: `c.SetEscalationPolicy(pos[0], *category, nil)`.
- `list`: `v, err := c.GetEscalationPolicy(pos[0])`; print the `Default` chain (labeled `default`) then each `ByCategory` entry (labeled by category), reusing the existing per-tier print format.

- [ ] **Step 6: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ ./internal/harbor/adminclient/ ./cmd/at-harbor/ -run 'Escalation|Project' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → pass.

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/harbor/admin.go internal/harbor/adminclient/adminclient.go cmd/at-harbor/main.go internal/harbor/admin_test.go internal/harbor/adminclient/adminclient_test.go cmd/at-harbor/main_test.go
git add internal/harbor/admin.go internal/harbor/adminclient/ cmd/at-harbor/main.go internal/harbor/admin_test.go cmd/at-harbor/main_test.go
git commit -m "harbor: category-aware escalation admin/adminclient/CLI (COV-167)" # + trailers
```

---

### Task 6: Docs

**Files:**
- Modify: `docs/usage/harbor/escalation.md`, `docs/usage/harbor/messaging.md`, `docs/usage/harbor/INDEX.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Update `escalation.md`**

Add a **categories** section: the default chain + `EscalationByCategory` overrides (free-form categories; unknown/unset → default); the **`escalate(category)`** tool (a brokered cove-side tool that stamps the cove's declared block category — self-scoped, **persists until re-declared**, and **categorizes but does not itself trigger** an escalation); the `--category` operator commands (`project escalation set/list/clear --category infra`). Note harbor auto-detection (egress/401→infra) remains deferred. Bump `updated`.

- [ ] **Step 2: Update `messaging.md` + `INDEX.md`**

- `messaging.md`: add `escalate` to the cove's brokered-tool list (`read`/`send`/`list_targets`/`escalate`), one line, linking to escalation.md for the semantics. Bump `updated`.
- `INDEX.md`: refresh the escalation.md row one-liner if needed; bump `updated`.

- [ ] **Step 3: Verify docs health**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs/usage` → 0 new errors (delta vs baseline). Confirm links resolve.

- [ ] **Step 4: Commit**

```bash
git add docs/usage/harbor/
git commit -m "docs: harbor comms C2b v1 — escalation categories + escalate() tool (COV-167)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 policy data → Task 1; §2 runtime state → Task 1; §3 setter (endpoint + tool) → Tasks 3 & 4; §4 engine routing → Task 2; §5 operator surface → Task 5; §6 docs → Task 6. All covered.
- **Type consistency:** `SetEscalationPolicy(project, category, tiers)` (store Task 1 + adminclient Task 5) and `SetEscalationCategory(actorID, category)` (supervisor Task 1) are deliberately distinct; `EscalationBody{Category,Tiers}` / `EscalationView{Default,ByCategory}` (Task 5) match `Project.Escalation`/`EscalationByCategory` (Task 1); `chainFor` (Task 2) consumes `EscalationByCategory` + `EscalationCategory`.
- **Green between tasks:** the store signature change (Task 1) is contained by the one-line admin PUT `""` update + test updates in the same task; adminclient/CLI (which hit the HTTP API, not the store) are untouched until Task 5.
- **Category persistence** (Report doesn't clear it) is pinned by `TestSetEscalationCategoryPersistsAcrossWaitingEntry`; the unknown/empty→default fallback by `TestUnknownCategoryFallsBackToDefault`/`TestEmptyCategoryUsesDefault`; self-scoping + fail-closed of `/escalate` by the handler tests.
- **Boundaries:** `/escalate` handler is package `harbor` (Bearer→actor→supervisor, no kit/grpc); `internal/escalate` unchanged imports; `cove-master` token stays env-only.
- **Placeholder scan:** the only open items are "adapt to the file's existing fake/helper names" (messages/supervisor/admin/CLI harnesses) and the sketched admin/CLI test bodies in Tasks 3 & 5 (pointed at concrete C1/C2v1 tests to mirror) — real, discoverable, not TBDs.
