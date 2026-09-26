# Orchestration Slice 1: the Allocator admission seam

> **For agentic workers:** REQUIRED SUB-SKILL: use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Introduce the **Allocator** as harbor's capacity authority by moving the resident dispatcher's concurrency cap into a new `internal/allocator` package, expressed as a per-*(project, role)* budget — **behavior-preserving** for today's single-instance one-shot flow.

**Architecture:** The dispatcher (the "Requisitioner") stops counting instances and comparing to `max-concurrent` inline; instead it asks an `Allocator.Admit(project, role)`. Slice 1 is a pure in-memory admission check backed by the existing instance registry — it establishes the Requisitioner→Allocator seam so later slices can grow the Allocator (event-sourced reservation ledger, OCC append, roster-observed budgets, kinds) behind a stable interface. No event store, no reservation events, no Supervisor changes in this slice.

**Tech Stack:** Go 1.26, `slog`, `just`. Design of record: [`../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md).

## Global Constraints

- **Behavior-preserving.** The effective cap is unchanged: the budget for the dispatcher's single configured `(project, role)` is its `max-concurrent`, and the live count is the **global** non-`PhaseGone` instance count — exactly what `dispatcher.countLive()` computes today. (Per-*(project, role)* counting is a deliberate *later* behavior change, not this slice.)
- **Fail-closed.** No budget configured for a `(project, role)` ⇒ `Admit` returns false, matching harbor's fail-closed posture.
- **Scope discipline.** Do **not** touch the Supervisor, the launcher, the event log (`msglog`), or the `runtime.dispatcher` serve-config schema. `max-concurrent` stays in `dispatcherConfig` and its `validateDispatcher` check (`MaxConcurrent > 0`) is unchanged — it is now *consumed* to seed the budget.
- **Naming:** new code uses the golden-age names (Allocator; the dispatcher package stays `internal/dispatcher` for now — the Requisitioner rename is a later cosmetic pass, "migrate as we go").
- **TDD**, hermetic tests (`just test`), frequent commits. End each commit message with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: The `internal/allocator` package

**Files:**
- Create: `internal/allocator/allocator.go`
- Test: `internal/allocator/allocator_test.go`

**Interfaces:**
- Produces: `type Key struct{ Project, Role string }`; `type Counter interface { LiveCount(project, role string) int }`; `type Budget interface { For(project, role string) (int, bool) }`; `type StaticBudget map[Key]int`; `type Allocator struct{...}`; `func New(Counter, Budget) *Allocator`; `func (*Allocator) Admit(project, role string) bool`.

- [ ] **Step 1: Write the failing tests**

```go
package allocator

import "testing"

type fakeCounter struct{ n int }

func (f fakeCounter) LiveCount(project, role string) int { return f.n }

func TestAdmit_BelowBudget_Grants(t *testing.T) {
	a := New(fakeCounter{n: 2}, StaticBudget{{Project: "acme", Role: "worker"}: 3})
	if !a.Admit("acme", "worker") {
		t.Fatal("expected admit when live (2) < budget (3)")
	}
}

func TestAdmit_AtBudget_Denies(t *testing.T) {
	a := New(fakeCounter{n: 3}, StaticBudget{{Project: "acme", Role: "worker"}: 3})
	if a.Admit("acme", "worker") {
		t.Fatal("expected deny when live (3) >= budget (3)")
	}
}

func TestAdmit_NoBudget_FailsClosed(t *testing.T) {
	a := New(fakeCounter{n: 0}, StaticBudget{})
	if a.Admit("acme", "worker") {
		t.Fatal("expected deny when no budget configured (fail closed)")
	}
}
```

- [ ] **Step 2: Run the tests, verify they fail**

Run: `go test ./internal/allocator/`
Expected: FAIL — `undefined: New`, `undefined: StaticBudget`, etc.

- [ ] **Step 3: Implement the package**

```go
// Package allocator is harbor's capacity authority (the "Allocator" role from the
// orchestration design): it rations session existence per (project, role) against
// a budget. Slice 1 is an in-memory admission check that moves the concurrency cap
// out of the dispatcher; the event-sourced reservation ledger arrives in a later
// slice, behind this same interface.
package allocator

// Key identifies a role within a project — the allocation aggregate's key.
type Key struct{ Project, Role string }

// Counter reports how many sessions currently exist (are live) for a
// (project, role). Slice 1's implementation counts globally, preserving the
// dispatcher's prior max-concurrent semantics.
type Counter interface {
	LiveCount(project, role string) int
}

// Budget returns the per-(project, role) capacity; ok=false means no budget is
// configured for that pair, and admission fails closed.
type Budget interface {
	For(project, role string) (limit int, ok bool)
}

// StaticBudget is a fixed budget table. Slice 1 seeds it from the dispatcher's
// max-concurrent; a later slice replaces it with a roster-observed budget.
type StaticBudget map[Key]int

func (b StaticBudget) For(project, role string) (int, bool) {
	limit, ok := b[Key{Project: project, Role: role}]
	return limit, ok
}

// Allocator decides admission: may another session exist for (project, role)?
type Allocator struct {
	counter Counter
	budget  Budget
}

func New(counter Counter, budget Budget) *Allocator {
	return &Allocator{counter: counter, budget: budget}
}

// Admit reports whether a new session may be created for (project, role): the live
// count is strictly below the configured budget. Fail-closed when no budget.
func (a *Allocator) Admit(project, role string) bool {
	limit, ok := a.budget.For(project, role)
	if !ok {
		return false
	}
	return a.counter.LiveCount(project, role) < limit
}
```

- [ ] **Step 4: Run the tests, verify they pass**

Run: `go test ./internal/allocator/`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/allocator/
git commit -m "allocator: in-memory admission check (Allocator seam, slice 1)"
```

---

## Task 2: A registry-backed `Counter` in harbor

**Files:**
- Modify: `internal/harbor/instance.go` (add `InstanceCounter`)
- Test: `internal/harbor/instance_test.go` (add `TestInstanceCounter_LiveCount`; create the file if absent)

**Interfaces:**
- Consumes: `Store.ListInstances() []Instance`, `Phase`, `PhaseGone` (existing).
- Produces: `type InstanceCounter struct{ Store Store }` with `func (InstanceCounter) LiveCount(project, role string) int` — structurally satisfies `allocator.Counter` (no import of `allocator`, so no cycle).

- [ ] **Step 1: Write the failing test**

```go
func TestInstanceCounter_LiveCount(t *testing.T) {
	dir := t.TempDir()
	st, err := NewFileStore(dir + "/store.json")
	if err != nil {
		t.Fatal(err)
	}
	// two live, one gone → count is 2 (global, ignoring project/role, as today)
	must := func(err error) { if err != nil { t.Fatal(err) } }
	must(st.PutInstance(Instance{ActorID: "a", Phase: PhaseLive}))
	must(st.PutInstance(Instance{ActorID: "b", Phase: PhaseRaising}))
	must(st.PutInstance(Instance{ActorID: "c", Phase: PhaseGone}))

	if got := (InstanceCounter{Store: st}).LiveCount("acme", "worker"); got != 2 {
		t.Fatalf("LiveCount = %d, want 2", got)
	}
}
```

- [ ] **Step 2: Run it, verify it fails**

Run: `go test ./internal/harbor/ -run TestInstanceCounter_LiveCount`
Expected: FAIL — `undefined: InstanceCounter`.

- [ ] **Step 3: Implement (append to `internal/harbor/instance.go`)**

```go
// InstanceCounter counts live instances in a Store — the slice-1 capacity signal
// consumed by the Allocator. It counts globally (all non-Gone instances),
// preserving the dispatcher's prior max-concurrent semantics; per-(project, role)
// counting is a deliberate later change. Its method set structurally satisfies
// allocator.Counter without importing that package.
type InstanceCounter struct{ Store Store }

func (c InstanceCounter) LiveCount(project, role string) int {
	n := 0
	for _, i := range c.Store.ListInstances() {
		if i.Phase != PhaseGone {
			n++
		}
	}
	return n
}
```

- [ ] **Step 4: Run it, verify it passes**

Run: `go test ./internal/harbor/ -run TestInstanceCounter_LiveCount`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/harbor/instance.go internal/harbor/instance_test.go
git commit -m "harbor: InstanceCounter for the Allocator (global live count, slice 1)"
```

---

## Task 3: Rewire the dispatcher to admit via the Allocator

Lands the dispatcher change **and** the `main.go` wiring together, so the tree compiles at the task boundary and behavior is preserved.

**Files:**
- Modify: `internal/dispatcher/dispatcher.go` (add `Admitter`, drop `countLive`/`MaxConcurrent`, call `Admit`)
- Modify: `internal/dispatcher/dispatcher_test.go` (fake admitter; drop cap-via-registry expectations)
- Modify: `cmd/at-harbor/main.go` (construct + inject the Allocator)

**Interfaces:**
- Consumes: `allocator.Allocator.Admit`, `harbor.InstanceCounter`, `allocator.StaticBudget`, `allocator.Key`.
- Produces: `type Admitter interface { Admit(project, role string) bool }`; changed `func New(t Tracker, r Raiser, reg Registry, adm Admitter, cfg Config, log *slog.Logger) *Dispatcher`; `Config` no longer has `MaxConcurrent`.

- [ ] **Step 1: Write/adjust the failing test**

In `internal/dispatcher/dispatcher_test.go`, add a fake admitter and a test that the dispatcher raises while admitted and defers when not. Replace any existing test that drove the cap through the registry's `MaxConcurrent`.

```go
type fakeAdmitter struct{ allow int; calls int }

func (f *fakeAdmitter) Admit(project, role string) bool {
	f.calls++
	return f.calls <= f.allow
}

func TestTick_RaisesWhileAdmitted_DefersWhenNot(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "1", Identifier: "AET-1"}, {ID: "2", Identifier: "AET-2"}, {ID: "3", Identifier: "AET-3"}}}
	rz := &fakeRaiser{}
	reg := &fakeRegistry{} // no live instances → no dedup skips
	adm := &fakeAdmitter{allow: 2}
	d := New(tr, rz, reg, adm, Config{Role: "worker", Project: "acme", PollInterval: time.Minute}, slog.Default())

	d.tick(context.Background())

	if len(rz.raised) != 2 {
		t.Fatalf("raised %d, want 2 (admitter allowed 2 then denied)", len(rz.raised))
	}
}
```

(Reuse the file's existing `fakeTracker`/`fakeRaiser`/`fakeRegistry` doubles; add fields if needed. Match the real `scheduler.Issue` / `harbor.RaiseSpec` shapes.)

- [ ] **Step 2: Run it, verify it fails**

Run: `go test ./internal/dispatcher/ -run TestTick_RaisesWhileAdmitted_DefersWhenNot`
Expected: FAIL — `New` arity mismatch / `undefined: Admitter`.

- [ ] **Step 3: Implement the dispatcher change**

In `internal/dispatcher/dispatcher.go`:

```go
// Admitter decides whether another session may be raised for (project, role).
// Satisfied by *allocator.Allocator.
type Admitter interface {
	Admit(project, role string) bool
}
```

- Add `admitter Admitter` to the `Dispatcher` struct; add the `adm Admitter` parameter to `New` (after `reg Registry`) and assign it.
- Remove the `MaxConcurrent` field from `Config`.
- Delete the `countLive` method.
- In `tick`, replace the `live := d.countLive()` / `if live >= d.cfg.MaxConcurrent { break }` / `live++` logic with a per-issue admission check:

```go
for _, iss := range issues {
	actorID := "cove-" + iss.Identifier
	if _, ok := d.registry.GetInstance(actorID); ok {
		continue // already raised (dedup)
	}
	if !d.admitter.Admit(d.cfg.Project, d.cfg.Role) {
		d.log.Info("dispatcher: at capacity, deferring", "project", d.cfg.Project, "role", d.cfg.Role)
		break // backpressure — wait for a slot next tick
	}
	if err := d.tracker.Transition(ctx, iss.ID, scheduler.RoleInProgress); err != nil {
		d.log.Error("dispatcher: claim failed", "issue", iss.Identifier, "err", err.Error())
		continue
	}
	prompt, err := d.buildPrompt(ctx, iss)
	if err != nil {
		d.log.Error("dispatcher: prompt build failed", "issue", iss.Identifier, "err", err.Error())
		continue
	}
	if _, _, _, err := d.raiser.Raise(ctx, harbor.RaiseSpec{
		ActorID: actorID, Role: d.cfg.Role, Project: d.cfg.Project, Unit: iss.Identifier, Prompt: prompt,
	}); err != nil {
		d.log.Error("dispatcher: raise failed", "issue", iss.Identifier, "err", err.Error())
		// preserve today's behavior: on raise failure, surface as before (NEEDS INPUT)
		_ = d.tracker.Transition(ctx, iss.ID, scheduler.RoleNeedsInput)
	}
}
```

(Keep the exact claim/raise/error handling the file already has — only the cap check changes from a pre-loop count to a per-iteration `Admit`. Verify the `RoleNeedsInput` / error paths match the current code before editing.)

- [ ] **Step 4: Wire it in `cmd/at-harbor/main.go`**

At the dispatcher construction site (currently `disp := dispatcher.New(tracker, sup, st, dispatcher.Config{...})`), build the Allocator from the dispatcher config and inject it:

```go
poll, _ := time.ParseDuration(dc.PollInterval)
budget := allocator.StaticBudget{{Project: dc.Project, Role: dc.Role}: dc.MaxConcurrent}
alloc := allocator.New(harbor.InstanceCounter{Store: st}, budget)
disp := dispatcher.New(tracker, sup, st, alloc, dispatcher.Config{
	Role:         dc.Role,
	Project:      dc.Project,
	PollInterval: poll,
}, log)
go disp.Run(context.Background())
```

Add the `github.com/aethons-tools/cove/internal/allocator` import.

- [ ] **Step 5: Run the suite, verify green**

Run: `go test ./internal/dispatcher/ ./internal/allocator/ ./internal/harbor/`
Then: `go build ./...`
Expected: all PASS; build clean.

- [ ] **Step 6: Commit**

```bash
git add internal/dispatcher/ cmd/at-harbor/main.go
git commit -m "dispatcher: admit via the Allocator instead of an inline cap (slice 1)"
```

---

## Task 4: Verification gate

- [ ] **Step 1:** `just test` → green (hermetic).
- [ ] **Step 2:** `just build` → compiles (or `GOPROXY=direct GOSUMDB=off GOFLAGS=-mod=mod go build ./...` if `just build`'s `gen-blessed` step hits blocked egress — that failure is environmental, unrelated to this slice).
- [ ] **Step 3:** `just lint` → green.
- [ ] **Step 4: Behavior-preservation check.** Confirm by inspection that with a single configured `(project, role)` and `max-concurrent = N`, the dispatcher raises up to `N` live instances and defers beyond — identical to pre-slice behavior. The only moved logic is *where* the cap is decided (Allocator), not the cap or the count source.

---

## Self-review

- **Spec coverage:** implements the *admission-seam* portion of the design's "plausible first slice" (the cap moves into the Allocator, per-*(project, role)*-shaped). Deliberately defers the event-sourced reservation stream, OCC append, and the Supervisor reconcile-from-ledger to Slice 2+ — see roadmap. This deviation from the doc's bundled "first slice" is intentional: it is smaller, carries zero behavior risk, and the single-instance present needs no durable OCC yet.
- **Placeholder scan:** none — every step has concrete code/commands.
- **Type consistency:** `Key`, `Counter`, `Budget`, `StaticBudget`, `Allocator.Admit`, `Admitter`, `InstanceCounter.LiveCount` are used identically across Tasks 1–3; `InstanceCounter` satisfies `Counter` and `*Allocator` satisfies `Admitter` structurally (no import cycle).
- **Ambiguity:** the live count is explicitly **global** in Slice 1 (behavior-preserving); per-*(project, role)* counting is called out as a later, deliberate change.

## Roadmap (later slices — not this plan)

1. **Slice 2 — event-sourced allocation store.** A per-*(project, role)* stream with an **OCC append** (`UNIQUE(stream_id, stream_revision)` + expected-version) — a primitive `msglog` lacks today — mirroring `msglogpg`'s embedded-migrations/advisory-lock idiom with its own lock constant and `*_schema_migrations` table. Reservations become events (`ReservationRequested/Granted/Denied/Released`); the `StaticBudget` count becomes the ledger fold + snapshot.
2. **Slice 3 — Supervisor reconcile-from-ledger.** The Supervisor consumes granted reservations and reconciles them into raises (instead of the dispatcher calling `Raise` directly), and the Allocator observes execution terminals (`StudioGone`/`StudioLost`) to emit `ReservationReleased` — closing the Allocator↔Supervisor half of the seam.
3. **Slice 4 — roster-observed budget + per-*(project, role)* counting** (the deliberate behavior change), replacing `StaticBudget`/`max-concurrent`.
4. **Slice 5+ — standing kind; egress-policy promotion (security slice, incl. the deferred egress-delivery-at-raise hardening change); and the cosmetic Requisitioner/Studio/Session renames.**
