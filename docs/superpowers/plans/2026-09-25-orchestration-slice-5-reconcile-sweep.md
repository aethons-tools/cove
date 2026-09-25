# Orchestration Slice 5: the reconcile sweep (close the leaked-slot gap)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Close the crash-between-grant-and-raise leak that Slice 4 left open: a periodic **reconcile sweep** releases `Granted` reservations that have **no live instance** and whose grant is older than a grace window. This makes the authoritative ledger self-healing — a leaked slot is reclaimed instead of permanently reducing capacity.

**Architecture:** The Allocator gains `Sweep(ctx)` + a resident `SweepLoop`. `allocpg` gains `OutstandingReservations(ctx, olderThan)` — the net-outstanding reservations (`granted − released > 0` per reservation) whose last grant predates a cutoff. The registry seam (`Counter`) gains `IsLive(actorID)`. For each outstanding reservation with no live instance, `Sweep` appends a `ReservationReleased` (reusing the Slice-3 path). Postgres-only (no ledger ⇒ nothing to sweep).

**Tech Stack:** Go 1.26, `pgx/v5`, `slog`, `just`. Design of record: [`../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md). Builds on Slice 4 (merged: ledger-authoritative OCC admission; the crash-leak gap is noted in the dispatcher code).

## Global Constraints

- **Net-count outstanding, not "no release."** `reservationID` (= `cove-<ticket>`) recurs when a ticket is re-dispatched, so a reservation is outstanding iff `count(granted) > count(released)` for it — a "has no released row" test would miss a re-grant after a swept release. Use `GROUP BY reservation_id HAVING granted > released`.
- **Grace window prevents racing legitimate raises.** Only sweep reservations whose **latest grant** is older than a cutoff (default ~5m, comfortably beyond a Colima raise). A just-granted, mid-raise reservation (instance not yet in the registry) is younger than the cutoff and is skipped.
- **Liveness by exact actor.** `reservationID == actorID`; a reservation is live iff `Store.GetInstance(actorID)` exists and is not `PhaseGone`. Sweep only when NOT live.
- **Postgres-only, idempotent, best-effort.** No ledger ⇒ `Sweep` is a no-op. A sweep-release failure is logged, not fatal; the next tick retries. Releasing an already-live or already-released reservation must be harmless (the net-count query excludes them next pass).
- **Out of scope (noted, not built):** *ticket recovery* — a swept reservation's tracker ticket may be stuck IN PROGRESS; the sweep frees the *slot*, not the ticket (existing/other concern). *Health signals* — over-subscription needs launcher capacity caps (not modeled yet), and drift-vs-registry is now apples-to-oranges (registry count is global, ledger is per-(project,role)); both deferred.
- **TDD**, hermetic units + a pg-gated `//go:build integration` test. End each commit with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: `OutstandingReservations` on `allocpg`

**Files:** Modify `internal/allocator/allocpg/allocpg.go`; add a case to `allocpg_integration_test.go`.

**Interfaces:** Produces `allocator.Reservation struct{ Project, Role, ReservationID string }` (define in `allocator`) and `func (*Store) OutstandingReservations(ctx context.Context, olderThan time.Time) ([]allocator.Reservation, error)`.

- [ ] **Step 1: Failing integration test** — grant 3, release 1, set an old `at` on the survivors (or use `olderThan = now+1h` so all count), assert the outstanding set. Also assert a re-granted-after-release reservation reappears as outstanding (net-count check).

```go
func TestAllocpg_OutstandingReservations_NetCountAndAge(t *testing.T) {
	st := newTestStore(t); ctx := context.Background()
	g := func(id string) { if _, err := st.Grant(ctx, "acme", "worker", id, 100); err != nil { t.Fatal(err) } }
	rel := func(id string) { if err := st.Record(ctx, allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased, ReservationID: id}); err != nil { t.Fatal(err) } }
	g("a"); g("b"); g("c"); rel("b")          // a,c outstanding; b released
	rel("c"); g("c")                           // c released then re-granted ⇒ outstanding again (net 1)
	got, err := st.OutstandingReservations(ctx, time.Now().Add(time.Hour)) // cutoff in the future ⇒ all ages qualify
	if err != nil { t.Fatal(err) }
	ids := idset(got) // helper: map[reservationID]bool
	if !ids["a"] || !ids["c"] || ids["b"] || len(got) != 2 {
		t.Fatalf("outstanding = %v, want {a,c}", got)
	}
}
```

- [ ] **Step 2:** Run `-tags integration` → FAIL. **Step 3: Implement** (`time` import):

```go
// OutstandingReservations returns reservations still holding a slot — net
// granted−released > 0 per reservation — whose most recent grant is older than
// olderThan (the grace window, so in-flight raises are not swept). Reservation IDs
// recur across dispatch cycles, so this is a net count, not a "no release" test.
func (s *Store) OutstandingReservations(ctx context.Context, olderThan time.Time) ([]allocator.Reservation, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT category, stream_id, reservation_id
		 FROM alloc_events
		 GROUP BY category, stream_id, reservation_id
		 HAVING COUNT(*) FILTER (WHERE kind = $1) > COUNT(*) FILTER (WHERE kind = $2)
		    AND MAX(at) FILTER (WHERE kind = $1) < $3`,
		string(allocator.KindReservationGranted), string(allocator.KindReservationReleased), olderThan)
	if err != nil {
		return nil, fmt.Errorf("allocpg: outstanding reservations: %w", err)
	}
	defer rows.Close()
	var out []allocator.Reservation
	for rows.Next() {
		var category, streamID, resID string
		if err := rows.Scan(&category, &streamID, &resID); err != nil {
			return nil, err
		}
		role := strings.TrimPrefix(streamID, category+"/")
		out = append(out, allocator.Reservation{Project: category, Role: role, ReservationID: resID})
	}
	return out, rows.Err()
}
```

- [ ] **Step 4:** Run → PASS (pg; else defer to CI + compile check). **Step 5: Commit** — `allocpg: OutstandingReservations (net-count, grace-windowed) (slice 5)`

---

## Task 2: `IsLive` on the registry seam

**Files:** Modify `internal/harbor/instance.go`, `internal/harbor/instance_test.go`; modify `internal/allocator/allocator.go` (extend `Counter`).

**Interfaces:** `Counter` gains `IsLive(actorID string) bool`; `harbor.InstanceCounter.IsLive` = `GetInstance(actorID)` exists and `Phase != PhaseGone`.

- [ ] **Step 1: Failing test** (`instance_test.go`): a live instance → `IsLive` true; a `PhaseGone` one and a missing one → false.
- [ ] **Step 2:** Run → FAIL. **Step 3: Implement:**

```go
// IsLive reports whether a specific actor currently holds a live instance — used
// by the Allocator's reconcile sweep to distinguish a real session from a leaked
// (dangling) reservation.
func (c InstanceCounter) IsLive(actorID string) bool {
	i, ok := c.Store.GetInstance(actorID)
	return ok && i.Phase != PhaseGone
}
```

Add `IsLive(actorID string) bool` to `allocator.Counter`. (Existing `fakeCounter` in `allocator_test.go` gains an `IsLive` — see Task 3.)

- [ ] **Step 4:** Run → PASS. **Step 5: Commit** — `harbor: InstanceCounter.IsLive for the reconcile sweep (slice 5)`

---

## Task 3: `Allocator.Sweep` + `SweepLoop`

**Files:** Modify `internal/allocator/allocator.go`, `internal/allocator/allocator_test.go`.

**Interfaces:**
- `Ledger` gains `OutstandingReservations(ctx, olderThan time.Time) ([]Reservation, error)`.
- Allocator gains `now func() time.Time` (injected; default `time.Now`) — add a constructor param or keep `New` and default it (choose the lower-churn option; if adding a param, update call sites).
- `func (a *Allocator) Sweep(ctx context.Context, grace time.Duration) (int, error)` — returns count swept.
- `func (a *Allocator) SweepLoop(ctx context.Context, interval, grace time.Duration)` — ticker loop (immediate first pass optional), like `Dispatcher.Run`.

- [ ] **Step 1: Failing tests** — a `fakeLedger` returning a fixed outstanding list + recording `Record` calls; a `fakeCounter` whose `IsLive` is table-driven. Assert: only NOT-live reservations get a `ReservationReleased`; live ones are skipped; nil ledger ⇒ `Sweep` returns 0 and records nothing.

```go
func TestSweep_ReleasesOnlyDanglingReservations(t *testing.T) {
	fl := &fakeLedger{outstanding: []Reservation{
		{Project: "acme", Role: "worker", ReservationID: "live-1"},
		{Project: "acme", Role: "worker", ReservationID: "dangling-1"},
	}}
	fc := fakeCounter{live: map[string]bool{"live-1": true}} // dangling-1 not live
	a := New(fc, StaticBudget{}, fl)
	n, err := a.Sweep(context.Background(), 5*time.Minute)
	if err != nil || n != 1 { t.Fatalf("swept %d,%v want 1", n, err) }
	if len(fl.records) != 1 || fl.records[0].ReservationID != "dangling-1" || fl.records[0].Kind != KindReservationReleased {
		t.Fatalf("unexpected sweep releases: %+v", fl.records)
	}
}

func TestSweep_NilLedger_NoOp(t *testing.T) {
	a := New(fakeCounter{}, StaticBudget{}, nil)
	if n, err := a.Sweep(context.Background(), time.Minute); err != nil || n != 0 {
		t.Fatalf("nil ledger sweep = %d,%v", n, err)
	}
}
```

(Extend `fakeCounter` with a `live map[string]bool` + `IsLive`; extend `fakeLedger` with `outstanding []Reservation` + `OutstandingReservations`.)

- [ ] **Step 2:** Run → FAIL. **Step 3: Implement:**

```go
// Sweep reclaims leaked slots: it releases outstanding reservations (older than
// grace) whose actor has no live instance — the crash-between-grant-and-raise gap
// Slice 4 left open. No ledger ⇒ nothing to sweep. Best-effort per reservation.
func (a *Allocator) Sweep(ctx context.Context, grace time.Duration) (int, error) {
	if a.ledger == nil {
		return 0, nil
	}
	outstanding, err := a.ledger.OutstandingReservations(ctx, a.now().Add(-grace))
	if err != nil {
		return 0, err
	}
	swept := 0
	for _, r := range outstanding {
		if a.counter.IsLive(r.ReservationID) {
			continue // a real session — leave it
		}
		if err := a.RecordRelease(ctx, r.Project, r.Role, r.ReservationID); err != nil {
			a.log.Warn("allocator: sweep release failed", "reservation", r.ReservationID, "err", err.Error())
			continue
		}
		swept++
	}
	return swept, nil
}

// SweepLoop runs Sweep every interval until ctx is cancelled.
func (a *Allocator) SweepLoop(ctx context.Context, interval, grace time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := a.Sweep(ctx, grace); err != nil {
				a.log.Warn("allocator: sweep failed", "err", err.Error())
			} else if n > 0 {
				a.log.Info("allocator: swept leaked reservations", "count", n)
			}
		}
	}
}
```

(Add a `log *slog.Logger` to `Allocator` if not present — default to a discard logger in `New`; and the injected `now`.)

- [ ] **Step 4:** Run → PASS. **Step 5: Commit** — `allocator: reconcile Sweep + SweepLoop for leaked slots (slice 5)`

---

## Task 4: Wire the sweep loop

**Files:** Modify `cmd/at-harbor/main.go`.

- [ ] **Step 1:** When the ledger is present (`pgPool != nil`), start the sweep loop after the Allocator is built:

```go
alloc := allocator.New(harbor.InstanceCounter{Store: st}, budget, ledger)
sup.SetReleaser(alloc)
if ledger != nil {
	go alloc.SweepLoop(context.Background(), sweepInterval, sweepGrace) // e.g. 1m / 5m
}
```

Pick sensible constants (interval ~1m, grace ~5m); a `runtime.dispatcher` sub-field could tune them later (not required now). If `New` gained a `now`/`log` param, pass `time.Now`/`log`.

- [ ] **Step 2:** `go build ./...` → compiles. **Step 3: Commit** — `at-harbor: run the allocator reconcile sweep (slice 5)`

- [ ] **Step 4: Verification gate**
  - `just test` → green.
  - `just integration-harbor` with dev pg if available; else CI (`store-integration`).
  - `go build ./...`, `go build -tags integration ./internal/allocator/allocpg/` → compile.
  - `just lint` → green.
  - Update `docs/usage/harbor/serve.md`: note the allocator's periodic reconcile sweep reclaims leaked slots (per the same-change docs rule).

## Self-review

- **Spec coverage:** closes the Slice-4 crash-leak gap with a grace-windowed, net-count reconcile sweep; the ledger is now self-healing.
- **Correctness traps handled:** net-count (reservation reuse), grace window (in-flight raises), exact-actor liveness, Postgres-only no-op.
- **Placeholder scan:** test bodies reuse the files' doubles (`fakeLedger`/`fakeCounter` extended); SQL/Go concrete.
- **Type consistency:** `Reservation`, `OutstandingReservations` (on `Ledger` + `*allocpg.Store`), `Counter.IsLive` (+ `InstanceCounter`), `Sweep`/`SweepLoop` are consistent.
- **Deferred, flagged:** ticket recovery; health signals (over-subscription needs launcher caps; drift is apples-to-oranges post-cutover).

## Next (later)

The deferred design items: **standing kind** (long-lived reservations); **egress-policy promotion** (the security slice — Role owns the egress allow-list, delivered at raise); **snapshots** for the ledger fold if a stream grows large; launcher/Supervisor **capacity caps** + the over-subscription health signal; and the cosmetic **Requisitioner / Studio / Session** renames in code (currently the design vocabulary; the code still says dispatcher/cove).
