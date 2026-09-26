# Orchestration Slice 4: the ledger cutover (authoritative OCC admission)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. This is the crux slice — read the whole plan before starting.

**Goal:** Make the allocation ledger **authoritative** for the cap. Replace the registry-based `Admit` with an **atomic OCC admission** — a single conditional append that grants iff `Outstanding < budget`, gated by `UNIQUE(stream_id, stream_revision)` — done **before** the raise, with **compensation** (release) on any post-grant failure. The registry count stays only as the **file-store fallback** (no Postgres ⇒ no ledger).

**Two intended changes (not accidental):**
1. **Cap semantics:** with Postgres the cap becomes **per-*(project, role)* `Outstanding`** (granted − released), not the old **global** non-`PhaseGone` instance count. This is the deliberate per-*(project, role)* counting the design flagged. (File-store mode keeps the global registry count — no ledger there.)
2. **Order:** grant now happens **before** raise (it reserves the slot), so admission is atomic and cannot overshoot under concurrency. A raise/claim/prompt failure **compensates** by releasing the reserved slot.

**Architecture:** `allocpg` gains an atomic `Grant(project, role, resID, budget) (bool, error)` — a conditional `INSERT … SELECT … WHERE outstanding < budget` at `stream_revision = head+1`, retried on `23505`. The Allocator's `Recorder` seam widens to a `Ledger` (Record + Grant); `Allocator.Grant` uses the ledger when present, else the Slice-1 registry check. The dispatcher inverts to grant→claim→raise with compensation.

**Tech Stack:** Go 1.26, `pgx/v5`, `slog`, `just`. Design of record: [`../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md). Builds on Slices 1–3 (merged: allocator + allocpg with `Record`/`Outstanding`, supervisor releases on teardown).

## Global Constraints

- **Cap equivalence (Postgres path):** because Slice 3 validated that the ledger tracks reality, `Outstanding(project, role)` equals the live count for that pair — so for a single-*(project, role)* resident dispatcher the effective cap is unchanged; only its *source* and *scoping* (per-pair, not global) change. Call this out; it is intended.
- **File-store fallback preserved.** With no `pgPool` the Allocator has no ledger ⇒ `Grant` falls back to the registry `LiveCount < budget` (Slice-1 behavior, global) and `RecordRelease` is a no-op. Do **not** remove `InstanceCounter` — it is the no-Postgres cap.
- **Grant reserves before raise; every post-grant failure compensates.** After a successful `Grant`, a failed claim/prompt/raise MUST call `RecordRelease` for that `reservationID` so the slot is freed. (In file-store mode `Grant` reserved nothing and `RecordRelease` is a no-op, so compensation is harmless there.)
- **Known gap (documented, deferred to Slice 5):** a crash *between* a successful `Grant` and the raise/teardown leaves a dangling `Granted` (a leaked slot) with no compensation. Closing it needs a **reconcile sweep** (release `Granted` reservations with no live instance) — that is Slice 5, not here. Note it in code + the doc; it is an availability nuisance, not a correctness break (the cap stays ≤ budget).
- **Atomicity is real, not just single-instance.** The conditional-insert + `UNIQUE(stream_id, revision)` makes admission correct under concurrent writers (multi-instance-ready), even though today's single dispatcher serializes anyway.
- **TDD**, hermetic units + pg-gated `//go:build integration` tests. End each commit with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Atomic `Grant` on `allocpg`

**Files:** Modify `internal/allocator/allocpg/allocpg.go`; add cases to `internal/allocator/allocpg/allocpg_integration_test.go`.

**Interfaces:** Produces `func (*Store) Grant(ctx context.Context, project, role, reservationID string, budget int) (bool, error)` — appends a `ReservationGranted` at `head+1` **iff** `Outstanding < budget`; returns `(true, nil)` granted, `(false, nil)` over budget, retries on `23505`.

- [ ] **Step 1: Failing integration tests** (append; reuse `newTestStore`)

```go
func TestAllocpg_GrantHonorsBudget(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// budget 2: first two grant, third denied
	for i, want := range []bool{true, true, false} {
		got, err := st.Grant(ctx, "acme", "worker", "cove-"+strconv.Itoa(i), 2)
		if err != nil { t.Fatal(err) }
		if got != want { t.Fatalf("grant %d = %v, want %v", i, got, want) }
	}
	// release one → a slot frees → next grant succeeds
	if err := st.Record(ctx, allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased}); err != nil { t.Fatal(err) }
	got, err := st.Grant(ctx, "acme", "worker", "cove-after-release", 2)
	if err != nil { t.Fatal(err) }
	if !got { t.Fatal("expected grant after a release freed a slot") }
}
```

- [ ] **Step 2:** Run `-tags integration` (dev pg) → FAIL (`undefined: Grant`).
- [ ] **Step 3: Implement** in `allocpg.go`:

```go
// Grant atomically appends a ReservationGranted at head+1 iff the stream's
// Outstanding (granted − released) is below budget — the OCC admission gate. The
// conditional INSERT enforces the budget and UNIQUE(stream_id, stream_revision)
// enforces the version, so concurrent grants cannot overshoot: the loser of a
// revision race retries and re-evaluates the budget against the winner's grant.
// Returns (true,nil) granted, (false,nil) over budget, ErrConflictExhausted after
// maxAppendRetries lost races.
func (s *Store) Grant(ctx context.Context, project, role, reservationID string, budget int) (bool, error) {
	streamID := project + "/" + role
	data, err := json.Marshal(map[string]string{})
	if err != nil {
		return false, fmt.Errorf("allocpg: marshal: %w", err)
	}
	for attempt := 0; attempt < maxAppendRetries; attempt++ {
		var head int64
		if err := s.pool.QueryRow(ctx,
			`SELECT COALESCE(MAX(stream_revision), 0) FROM alloc_events WHERE stream_id = $1`,
			streamID).Scan(&head); err != nil {
			return false, fmt.Errorf("allocpg: grant head: %w", err)
		}
		tag, err := s.pool.Exec(ctx,
			`INSERT INTO alloc_events (category, stream_id, stream_revision, kind, reservation_id, data)
			 SELECT $1, $2, $3, $4, $5, $6
			 WHERE (SELECT COUNT(*) FILTER (WHERE kind = $4)
			               - COUNT(*) FILTER (WHERE kind = $7)
			        FROM alloc_events WHERE stream_id = $2) < $8`,
			project, streamID, head+1, string(allocator.KindReservationGranted), reservationID, data,
			string(allocator.KindReservationReleased), budget)
		if err != nil {
			if isUniqueViolation(err) {
				continue // lost the revision race — re-read head and re-evaluate budget
			}
			return false, fmt.Errorf("allocpg: grant: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return true, nil
		}
		return false, nil // 0 rows: at/over budget
	}
	return false, ErrConflictExhausted
}
```

- [ ] **Step 4:** Run → PASS (pg; else defer to CI + confirm `go build -tags integration ./internal/allocator/allocpg/`). **Step 5: Commit** — `allocpg: atomic OCC Grant honoring budget (slice 4)`

---

## Task 2: `Allocator.Grant` (ledger-authoritative, registry fallback)

**Files:** Modify `internal/allocator/allocator.go`, `internal/allocator/allocator_test.go`.

**Interfaces:**
- Replace the `Recorder` interface with **`Ledger`**: `interface { Record(ctx, Event) error; Grant(ctx, project, role, reservationID string, budget int) (bool, error) }` (satisfied by `*allocpg.Store`).
- `New(counter Counter, budget Budget, ledger Ledger) *Allocator` (rename the 3rd param; nil ⇒ no ledger).
- Add `func (a *Allocator) Grant(ctx, project, role, reservationID string) (bool, error)`.
- **Remove** `Admit` and `RecordGrant` (superseded by `Grant`). Keep `RecordRelease` (now via the ledger).

- [ ] **Step 1: Failing tests** — a `fakeLedger` recording grant/record calls; assert (a) ledger path: `Grant` calls `ledger.Grant(project, role, id, budget)` and returns its result; (b) fallback: nil ledger ⇒ `Grant` returns `counter.LiveCount < budget`; (c) `RecordRelease` routes to `ledger.Record` (or no-ops when nil).

```go
type fakeLedger struct {
	grantResult bool
	grantBudget int
	records     []Event
}
func (f *fakeLedger) Grant(_ context.Context, _, _, _ string, budget int) (bool, error) {
	f.grantBudget = budget
	return f.grantResult, nil
}
func (f *fakeLedger) Record(_ context.Context, ev Event) error { f.records = append(f.records, ev); return nil }

func TestGrant_LedgerPath(t *testing.T) {
	fl := &fakeLedger{grantResult: true}
	a := New(fakeCounter{n: 99}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, fl)
	got, err := a.Grant(context.Background(), "acme", "worker", "cove-1")
	if err != nil || !got { t.Fatalf("got %v,%v", got, err) }
	if fl.grantBudget != 3 { t.Fatalf("budget passed = %d, want 3", fl.grantBudget) }
}

func TestGrant_RegistryFallback_NilLedger(t *testing.T) {
	a := New(fakeCounter{n: 2}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	got, err := a.Grant(context.Background(), "acme", "worker", "cove-1")
	if err != nil || !got { t.Fatalf("expected grant (2<3), got %v,%v", got, err) }
	a2 := New(fakeCounter{n: 3}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, nil)
	if got, _ := a2.Grant(context.Background(), "acme", "worker", "c"); got { t.Fatal("expected deny (3>=3)") }
}
```

- [ ] **Step 2:** Run → FAIL. **Step 3: Implement:**

```go
// Grant admits (and reserves) a session for (project, role). With a ledger it is
// the authoritative OCC admission (append-granted-iff-under-budget); without one
// (file store) it falls back to the registry live count vs budget. Fail-closed
// when no budget is configured.
func (a *Allocator) Grant(ctx context.Context, project, role, reservationID string) (bool, error) {
	limit, ok := a.budget.For(project, role)
	if !ok {
		return false, nil
	}
	if a.ledger != nil {
		return a.ledger.Grant(ctx, project, role, reservationID, limit)
	}
	return a.counter.LiveCount(project, role) < limit, nil
}
```

Update `RecordRelease` to call `a.ledger.Record(...)` (no-op when `a.ledger == nil`); delete `Admit` and `RecordGrant`; rename the field/param `recorder`→`ledger` and interface `Recorder`→`Ledger` (add `Grant`). Update the Slice-1/2/3 tests that referenced `Admit`/`RecordGrant`/`New(..., recorder)`.

- [ ] **Step 4:** Run → PASS; `go build ./...` (dispatcher/main will break until Tasks 3–4 — that's expected within this slice; keep them in the same working session). **Step 5: Commit** — `allocator: ledger-authoritative Grant with registry fallback (slice 4)`

---

## Task 3: Dispatcher — grant-before-raise + compensation

**Files:** Modify `internal/dispatcher/dispatcher.go`, `internal/dispatcher/dispatcher_test.go`.

**Interfaces:** `Admitter` becomes:
```go
type Admitter interface {
	Grant(ctx context.Context, project, role, reservationID string) (bool, error)
	RecordRelease(ctx context.Context, project, role, reservationID string) error
}
```

- [ ] **Step 1: Failing tests** — update `fakeAdmitter` to `Grant` (returns a configured bool/err, records calls) + `RecordRelease` (records compensation). Cover: (a) grant→raise success (no release); (b) over-budget `Grant`→false ⇒ `break`, no claim/raise; (c) **raise failure ⇒ compensation** (`RecordRelease` called for the actor, ticket → needs-input).

```go
func TestTick_RaiseFailure_CompensatesRelease(t *testing.T) {
	tr := &fakeTracker{ready: []scheduler.Issue{{ID: "1", Identifier: "AET-1", DispatchLabeled: true}}}
	rz := &fakeRaiser{failRaise: true}
	adm := &fakeAdmitter{grant: true}
	d := New(tr, rz, &fakeRegistry{}, adm, Config{Role: "worker", Project: "acme", PollInterval: time.Minute}, slog.Default())
	d.tick(context.Background())
	if len(adm.released) != 1 || adm.released[0] != "cove-AET-1" {
		t.Fatalf("expected compensation release for cove-AET-1, got %v", adm.released)
	}
}
```

- [ ] **Step 2:** Run → FAIL. **Step 3: Implement the tick rewrite** — replace the `Admit`/`RecordGrant` logic:

```go
	granted, err := d.admitter.Grant(ctx, d.cfg.Project, d.cfg.Role, actorID)
	if err != nil {
		d.log.Warn("dispatcher: grant failed", "actor", actorID, "err", err.Error())
		break // store trouble — back off this tick
	}
	if !granted {
		d.log.Info("dispatcher: at capacity, deferring", "project", d.cfg.Project, "role", d.cfg.Role)
		break
	}
	// slot reserved — any failure from here must release it (compensation)
	if err := d.tracker.Transition(ctx, iss.ID, scheduler.RoleInProgress); err != nil {
		d.log.Warn("dispatcher: claim failed", "issue", iss.Identifier, "error", err.Error())
		d.release(ctx, actorID)
		continue
	}
	prompt, err := d.buildPrompt(ctx, iss)
	if err != nil {
		d.log.Warn("dispatcher: build prompt failed", "issue", iss.Identifier, "error", err.Error())
		d.needsInput(ctx, iss)
		d.release(ctx, actorID)
		continue
	}
	if _, _, _, err := d.raiser.Raise(ctx, harbor.RaiseSpec{
		ActorID: actorID, Role: d.cfg.Role, Project: d.cfg.Project, Unit: iss.Identifier, Prompt: prompt,
	}); err != nil {
		d.log.Warn("dispatcher: raise failed", "issue", iss.Identifier, "error", err.Error())
		d.needsInput(ctx, iss)
		d.release(ctx, actorID)
		continue
	}
	d.log.Info("dispatcher: raised cove", "issue", iss.Identifier, "actor", actorID)
```

Add the compensation helper:
```go
// release compensates a reserved-but-not-raised slot (best-effort; the teardown
// path releases normally-completed sessions).
func (d *Dispatcher) release(ctx context.Context, actorID string) {
	if err := d.admitter.RecordRelease(ctx, d.cfg.Project, d.cfg.Role, actorID); err != nil {
		d.log.Warn("dispatcher: compensating release failed", "actor", actorID, "err", err.Error())
	}
}
```

- [ ] **Step 4:** Run → PASS. **Step 5: Commit** — `dispatcher: grant-before-raise with compensation (slice 4)`

---

## Task 4: Wire the ledger + verification gate

**Files:** Modify `cmd/at-harbor/main.go`.

- [ ] **Step 1:** Pass the `allocpg` store as the Allocator's **ledger** (the store already exists when `pgPool != nil`); `InstanceCounter` stays the fallback counter, and the normalized `project` (Slice 3) stays. The `New(...)` third arg is now the ledger:

```go
var ledger allocator.Ledger // nil ⇒ registry-count fallback (file store)
if pgPool != nil {
	as, err := allocpg.New(context.Background(), pgPool, log)
	if err != nil { fmt.Fprintln(stderr, "at-harbor:", err); return 1 }
	ledger = as
}
alloc := allocator.New(harbor.InstanceCounter{Store: st}, budget, ledger)
sup.SetReleaser(alloc)
```

(Update the comment to describe the authoritative-ledger/registry-fallback split. `sup.SetReleaser(alloc)` unchanged; `RecordRelease` now routes through the ledger.)

- [ ] **Step 2:** `go build ./...` → compiles. **Step 3: Commit** — `at-harbor: make the allocation ledger authoritative (slice 4)`

- [ ] **Step 4: Verification gate**
  - `just test` → green (allocator, dispatcher, harbor).
  - `just integration-harbor` with dev pg if the daemon starts (Grant/budget/compensation); else note CI deferral (`store-integration` covers `./internal/allocator/...`).
  - `go build ./...`, `go build -tags integration ./internal/allocator/allocpg/` → compile.
  - `just lint` → green.
  - **Behavior check:** Postgres path — cap = per-*(project, role)* `Outstanding`; over budget ⇒ defer; raise failure ⇒ slot released (compensated). File path — unchanged global registry cap. Update `docs/usage/harbor/serve.md` to say the ledger is now authoritative with Postgres (registry count is the file-store fallback), per the repo's same-change docs rule.

## Self-review

- **Spec coverage:** implements the cutover — OCC admission (atomic conditional append) + compensation, ledger authoritative with Postgres, registry retained only as the file-store fallback.
- **Placeholder scan:** test bodies reuse the files' existing doubles; SQL/Go are concrete.
- **Type consistency:** `Ledger` (Record+Grant), `Allocator.Grant`, `allocpg.Grant`, the new `Admitter` (Grant+RecordRelease) are consistent; `*allocpg.Store` satisfies `Ledger`; `*allocator.Allocator` still satisfies `harbor.Releaser` (RecordRelease kept).
- **Deliberate changes flagged:** global→per-*(project, role)* cap (Postgres path); grant-before-raise; the crash-leak gap deferred to a Slice-5 reconcile sweep.

## Next (Slice 5+)

- **Reconcile sweep:** release `Granted` reservations with no live instance (closes the crash-between-grant-and-raise leak); add the over-subscription/drift health signal.
- Then the deferred design items: standing kind; egress-policy promotion (the security slice, incl. egress-delivery-at-raise); snapshots for the ledger fold if streams grow; and the cosmetic Requisitioner/Studio/Session renames in code.
