# Orchestration Slice 3: release wiring (ledger becomes accurate)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the allocation ledger *accurate* by recording `ReservationReleased` when a Session ends, so the shadow ledger's outstanding count (`Granted − Released`) tracks reality. The cap **stays on the registry `LiveCount`** — the ledger is still **not authoritative**. Behavior-preserving.

**Why this is its own slice (the Slice-3/4 split):** the roadmap's "release + cutover" is two steps. This slice (3) wires *release* so the ledger becomes trustworthy and verifiable against the registry; the **cutover** — flipping admission to an OCC ledger fold and retiring the registry count — is **Slice 4**, and it's the crux correctness change (new OCC admission flow + raise-failure compensation), so it deserves to land on an already-validated ledger. Dual-write → verify → cutover, matching the house pattern.

**Architecture:** The Supervisor's `Teardown` is the single choke point for a Session ending (Report-done, Reconcile-lost, and explicit teardown all route through it). It gains an optional `Releaser` (set via a setter, like `SetControlSink`/`SetTailReader`) and calls `RecordRelease` best-effort after `RemoveInstance`. The Allocator gains `RecordRelease` (symmetric to `RecordGrant`); `allocpg.Record` already accepts any event kind, so recording needs no store change — only a new `Outstanding` reader for verification and the Slice-4 cutover.

**Tech Stack:** Go 1.26, `pgx/v5`, `slog`, `just`. Design of record: [`../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md`](../specs/2026-09-15-orchestration-roles-requisitioner-allocator-supervisor.md). Builds on Slice 2 (merged: `internal/allocator/allocpg` with the OCC append; the Allocator records `ReservationGranted` per raise).

## Global Constraints

- **Behavior-preserving, still shadow.** The cap is unchanged (registry `LiveCount` vs `StaticBudget`). Recording a release is best-effort audit; a failure is logged, never fatal to teardown. The ledger does **not** drive the cap in this slice.
- **Postgres-gated.** With the file store there is no pool ⇒ no Recorder/Releaser ⇒ nothing recorded (identical to Slice 1). This flows from the same nil-check as Slice 2.
- **Grant and release MUST share a stream key** `(project, role)`. Trap: the dispatcher records grants under `dc.Project` (optional, may be `""`), but the Supervisor stores `inst.Project = orDefaultProject(spec.Project)` = `harbor.DefaultProject` (`"default"`) when empty. If left unnormalized, an empty project puts grants on stream `"/role"` and releases on `"default/role"` — they never reconcile. **Fix (Task 4): normalize the project once** (`harbor.DefaultProject` when empty) and use it for the budget key, the dispatcher `Config.Project`, so grant and release land on the same stream as `inst.Project`.
- **This directional coupling is by-design.** The Supervisor → Allocator "release" call is the *actual-state-out* half of the seam from the design diagram ("reports releases/liveness back up") — not a violation of "no directing" (which forbids the Allocator commanding the Supervisor). Realized as a callback now; an execution-event-stream subscription is a later refinement for multi-instance.
- **TDD**, hermetic units + a pg-gated `//go:build integration` reader test. End each commit with:
  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: `RecordRelease` on the Allocator

**Files:** Modify `internal/allocator/allocator.go`, `internal/allocator/allocator_test.go`.

**Interfaces:** Produces `const KindReservationReleased Kind = "reservation_released"`; `func (*Allocator) RecordRelease(ctx, project, role, reservationID string) error` (best-effort, nil-Recorder no-op — symmetric to `RecordGrant`).

- [ ] **Step 1: Failing tests** (mirror the `RecordGrant` tests)

```go
func TestRecordRelease_AppendsEvent(t *testing.T) {
	rec := &fakeRecorder{}
	a := New(fakeCounter{}, StaticBudget{{Project: "acme", Role: "worker"}: 3}, rec)
	if err := a.RecordRelease(context.Background(), "acme", "worker", "cove-AET-1"); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 1 || rec.events[0].Kind != KindReservationReleased || rec.events[0].ReservationID != "cove-AET-1" {
		t.Fatalf("unexpected: %+v", rec.events)
	}
}

func TestRecordRelease_NilRecorder_NoOp(t *testing.T) {
	a := New(fakeCounter{}, StaticBudget{}, nil)
	if err := a.RecordRelease(context.Background(), "acme", "worker", "x"); err != nil {
		t.Fatalf("nil recorder should no-op, got %v", err)
	}
}
```

- [ ] **Step 2:** Run → FAIL (`undefined: KindReservationReleased` / `RecordRelease`).
- [ ] **Step 3: Implement** — add the const and the method:

```go
const KindReservationReleased Kind = "reservation_released"

// RecordRelease durably records that a session's reservation was released (its
// Studio was torn down) for (project, role) — a best-effort shadow write (the cap
// is still the registry count in this slice). A nil Recorder is a no-op.
func (a *Allocator) RecordRelease(ctx context.Context, project, role, reservationID string) error {
	if a.recorder == nil {
		return nil
	}
	return a.recorder.Record(ctx, Event{
		Category:      project,
		Project:       project,
		Role:          role,
		Kind:          KindReservationReleased,
		ReservationID: reservationID,
	})
}
```

- [ ] **Step 4:** Run → PASS. **Step 5: Commit** — `allocator: RecordRelease (slice 3)`

---

## Task 2: `Outstanding` reader on `allocpg`

**Files:** Modify `internal/allocator/allocpg/allocpg.go`; add a case to `internal/allocator/allocpg/allocpg_integration_test.go`.

**Interfaces:** Produces `func (*Store) Outstanding(ctx context.Context, project, role string) (int, error)` — `count(granted) − count(released)` for the `(project, role)` stream. (Consumed by Slice 4's cutover; added now to verify the ledger tracks reality.)

- [ ] **Step 1: Failing integration test** (append; reuses `newTestStore`)

```go
func TestAllocpg_OutstandingCountsGrantsMinusReleases(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	grant := allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationGranted}
	rel := allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased}
	for i := 0; i < 3; i++ {
		if err := st.Record(ctx, grant); err != nil { t.Fatal(err) }
	}
	if err := st.Record(ctx, rel); err != nil { t.Fatal(err) }
	got, err := st.Outstanding(ctx, "acme", "worker")
	if err != nil { t.Fatal(err) }
	if got != 2 { // 3 granted − 1 released
		t.Fatalf("Outstanding = %d, want 2", got)
	}
}
```

- [ ] **Step 2:** Run under `-tags integration` (with dev Postgres) → FAIL (`undefined: Outstanding`).
- [ ] **Step 3: Implement** in `allocpg.go`:

```go
// Outstanding returns the live reservation count for a (project, role) stream —
// granted minus released. This is the ledger fold the cap will use once the
// cutover (Slice 4) makes the store authoritative.
func (s *Store) Outstanding(ctx context.Context, project, role string) (int, error) {
	streamID := project + "/" + role
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT
		   COUNT(*) FILTER (WHERE kind = $2) - COUNT(*) FILTER (WHERE kind = $3)
		 FROM alloc_events WHERE stream_id = $1`,
		streamID, string(allocator.KindReservationGranted), string(allocator.KindReservationReleased),
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("allocpg: outstanding: %w", err)
	}
	return n, nil
}
```

- [ ] **Step 4:** Run → PASS (with pg; deferred to CI if no local daemon). `go build -tags integration ./internal/allocator/allocpg/` compiles regardless. **Step 5: Commit** — `allocpg: Outstanding reader (granted − released) (slice 3)`

---

## Task 3: Supervisor releases on teardown

**Files:** Modify `internal/harbor/supervisor.go`, `internal/harbor/supervisor_test.go`.

**Interfaces:** Produces `type Releaser interface { RecordRelease(ctx context.Context, project, role, reservationID string) error }`; `func (*Supervisor) SetReleaser(r Releaser)`; a `released Releaser` field. Consumes `Instance.Project`/`Role`/`ActorID`.

- [ ] **Step 1: Failing test** — a fake releaser records calls; assert that a `Teardown` of a live instance invokes `RecordRelease(project, role, actorID)`.

```go
type fakeReleaser struct{ calls []string }
func (f *fakeReleaser) RecordRelease(_ context.Context, project, role, id string) error {
	f.calls = append(f.calls, project+"/"+role+"/"+id)
	return nil
}

func TestTeardown_RecordsRelease(t *testing.T) {
	// build a Supervisor over a FileStore + a fake/placeholder launcher (mirror the
	// file's existing supervisor test setup), PutInstance a live cove, SetReleaser,
	// Teardown, then assert the releaser saw "<project>/<role>/<actorID>".
	...
	fr := &fakeReleaser{}
	sup.SetReleaser(fr)
	if err := sup.Teardown(context.Background(), "cove-AET-1"); err != nil { t.Fatal(err) }
	if len(fr.calls) != 1 || fr.calls[0] != "acme/worker/cove-AET-1" {
		t.Fatalf("release calls = %v", fr.calls)
	}
}
```

(Use the existing supervisor_test.go harness/doubles — match how it builds a `Supervisor` and a launcher today.)

- [ ] **Step 2:** Run → FAIL (`undefined: SetReleaser`).
- [ ] **Step 3: Implement** — add the `Releaser` interface, the `released` field, the setter (next to `SetTailReader`), and the call in `Teardown` after `RemoveInstance` succeeds:

```go
	if err := s.store.RemoveInstance(actorID); err != nil {
		return err
	}
	if s.released != nil {
		if err := s.released.RecordRelease(ctx, inst.Project, inst.Role, actorID); err != nil && s.log != nil {
			s.log.Warn("teardown: record release failed (shadow, non-fatal)", "id", actorID, "err", err.Error())
		}
	}
```

(`inst` is the instance loaded at the top of `Teardown`; it carries `Project`/`Role`.)

- [ ] **Step 4:** Run → PASS; `go build ./...`. **Step 5: Commit** — `harbor: supervisor records a release on teardown (slice 3)`

---

## Task 4: Wire the releaser + normalize the project

**Files:** Modify `cmd/at-harbor/main.go`.

- [ ] **Step 1: Normalize the project once** at the dispatcher wiring site so grant and release share a stream key:

```go
project := dc.Project
if project == "" {
	project = harbor.DefaultProject // match inst.Project (orDefaultProject) so grant/release share a stream
}
budget := allocator.StaticBudget{{Project: project, Role: dc.Role}: dc.MaxConcurrent}
```

Use `project` (not `dc.Project`) for the budget key **and** the dispatcher `Config.Project` (so `RecordGrant` uses the normalized value).

- [ ] **Step 2: Wire the Allocator as the Supervisor's releaser** (the Allocator already satisfies `harbor.Releaser` via `RecordRelease`). After the Allocator is built (it holds the pg recorder when `pgPool != nil`), set it on the already-constructed supervisor:

```go
alloc := allocator.New(harbor.InstanceCounter{Store: st}, budget, rec)
sup.SetReleaser(alloc) // actual-state-out: teardown records ReservationReleased (shadow)
disp := dispatcher.New(tracker, sup, st, alloc, dispatcher.Config{
	Role: dc.Role, Project: project, PollInterval: poll,
}, log)
```

(When `rec` is nil — file store — `RecordRelease` is a no-op, so `SetReleaser(alloc)` is harmless.)

- [ ] **Step 3:** `go build ./...` → compiles. **Step 4: Commit** — `at-harbor: record releases on teardown + normalize project (slice 3)`

---

## Task 5: Verification gate

- [ ] `just test` → green (hermetic: allocator + dispatcher + harbor supervisor).
- [ ] `just integration-harbor` with dev Postgres if the daemon starts (`Outstanding` test); otherwise note deferral to CI (the `store-integration` job already covers `./internal/allocator/...` from Slice 2).
- [ ] `go build ./...` and `go build -tags integration ./internal/allocator/allocpg/` → compile.
- [ ] `just lint` → green.
- [ ] **Behavior check:** cap still registry-based; with Postgres, teardown now appends a `reservation_released` row and `Outstanding(project, role)` equals the live count; with the file store nothing is recorded (= Slice 1).

## Self-review

- **Spec coverage:** implements the *release* half of the design's "release + cutover"; the ledger becomes accurate (grants + releases) but stays non-authoritative — cutover is Slice 4.
- **Placeholder scan:** the Task-3 test body is sketched (`...`) around the file's existing supervisor harness — the implementer fills it from the real doubles; every other step is concrete.
- **Type consistency:** `KindReservationReleased`, `RecordRelease`, `allocpg.Outstanding`, `harbor.Releaser`/`SetReleaser` are used identically across tasks; `*allocator.Allocator` satisfies `harbor.Releaser`; no new import cycle (harbor defines `Releaser` structurally — it does not import `allocator`).
- **Correctness trap handled:** the project normalization (Task 4) is the one non-obvious bug guard — grant and release must share `(project, role)`.

## Next (Slice 4 — the cutover)

Make the ledger authoritative: replace the registry-based `Admit` with an **OCC admission** — fold `{budget, Outstanding, head revision}`, grant iff `Outstanding < budget` by appending `ReservationGranted` at `expected_version = head` (retry on conflict), and handle **raise-failure compensation** (append `ReservationReleased`/withdraw so a failed raise frees its slot). Then retire the `InstanceCounter` cap path and add the over-subscription/drift health signal.
