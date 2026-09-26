# Session kinds, Slice 1: generalize the reservation + per-role policy

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Lay the shared foundation for standing and personal sessions without changing behavior: reservations carry a **session kind** (+ standing name / personal owner), the ledger caps **per kind**, and the ephemeral cap moves to a **per-(project, role) policy authored on the roster Role** (`max-ephemeral`), with today's `runtime.dispatcher.max-concurrent` as the fallback. Ephemeral behaves exactly as it does now.

**Architecture:** `allocator` gains `SessionKind`, a `Request` type, and a `Policy`/`PolicySource` pair that replaces `Budget`/`StaticBudget`. `allocpg` gains three columns (`session_kind` defaulting to `ephemeral`, `session_name`, `session_owner`); `Grant` counts outstanding reservations **of the requested kind only**, and a release inherits the kind of the reservation's latest grant. The roster `Role` gains `Allocation.MaxEphemeral`, administered through `role add --max-ephemeral`. A `RosterPolicy` in `cmd/at-harbor` reads the Role live on each grant and falls back to the dispatcher's `max-concurrent`.

**Tech Stack:** Go 1.26, `pgx/v5`, `slog`, `just`. Spec: [`../specs/2026-09-26-session-kinds-standing-personal.md`](../specs/2026-09-26-session-kinds-standing-personal.md). Builds on orchestration Slices 1–5 (merged).

## Global Constraints

- **Behavior-preserving.** Only ephemeral reservations exist after this slice. Existing ledger rows become `ephemeral` via the column default, so counts are unchanged. A deployment with no role policy keeps using `max-concurrent` exactly as today.
- **Name the session kind distinctly.** `Event.Kind` stays the *event type*. The new field is `Event.SessionKind` (type `SessionKind`), with constants `SessionEphemeral`, `SessionStanding`, `SessionPersonal`. Never overload `Kind`.
- **Unsupported kinds fail closed.** `Allocator.Grant` admits only `SessionEphemeral` in this slice; `standing`/`personal` return `ErrUnsupportedKind` (their slices add them). `allocpg` itself is kind-agnostic.
- **Roster is the source of truth; live read, no materialized event.** The design called for materializing the budget onto the ledger. Because `allocpg.Grant` takes the cap as a parameter of its single atomic insert, reading the Role's policy from the memory-cached store on each grant gives the same accepted eventual consistency (capacity tolerates it). Do **not** build `RoleBudgetObserved`.
- **Roster wins over the fallback.** If the Role sets `max-ephemeral > 0`, use it; otherwise use the dispatcher's `max-concurrent` for the dispatcher's `(project, role)`; otherwise no policy ⇒ fail closed (as today).
- **Sweep only ephemeral.** The reconcile sweep must release only `ephemeral` reservations. Standing (resurrected, never swept) and personal (owner-released, idle ladder) own their lifecycles in later slices. No behavior change now, since only ephemeral exists.
- **Docs in the same change** (repo rule): `roster.md` (`--max-ephemeral`), `dispatcher.md` (`max-concurrent` is now the fallback), `serve.md` (the allocation store records the session kind).
- **TDD**; hermetic units + pg-gated `//go:build integration` tests. End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Session kinds, `Request`, and `Policy` in `allocator` (+ dispatcher call site)

**Files:** Modify `internal/allocator/allocator.go`, `internal/allocator/allocator_test.go`, `internal/dispatcher/dispatcher.go`, `internal/dispatcher/dispatcher_test.go`, `cmd/at-harbor/main.go` (compile-only adjustment; real wiring is Task 4).

**Interfaces (produced):**

```go
// SessionKind is what kind of session a reservation holds (distinct from Kind,
// the allocation *event* type).
type SessionKind string

const (
	SessionEphemeral SessionKind = "ephemeral" // dispatcher, one unit of work
	SessionStanding  SessionKind = "standing"  // operator-declared, named, until dismissed
	SessionPersonal  SessionKind = "personal"  // a human, ad hoc, until the owner releases it
)

// ErrUnsupportedKind: the Allocator does not (yet) admit this session kind.
var ErrUnsupportedKind = errors.New("allocator: unsupported session kind")

// Request asks for one reservation. Name is set for standing, Owner for personal.
type Request struct {
	Project, Role string
	ReservationID string
	Kind          SessionKind
	Name          string
	Owner         string
}

// Policy is a (project, role)'s allocation policy. Later slices add MaxPersonal,
// the standing name set, idle settings, and requester grants.
type Policy struct {
	MaxEphemeral int
}

// PolicySource returns the policy for a (project, role); ok=false ⇒ fail closed.
type PolicySource interface {
	Policy(project, role string) (Policy, bool)
}

// StaticPolicy is a fixed table (tests, and the fallback seed).
type StaticPolicy map[Key]Policy
```

- `Event` gains `SessionKind SessionKind`, `Name`, `Owner string`.
- `Reservation` gains `SessionKind SessionKind`, `Name`, `Owner string`.
- `Ledger.Grant` becomes `Grant(ctx context.Context, req Request, cap int) (bool, error)`.
- `New(counter Counter, policy PolicySource, ledger Ledger) *Allocator` (replaces `budget Budget`); delete `Budget`/`StaticBudget`.
- `Allocator.Grant(ctx context.Context, req Request) (bool, error)`.

- [ ] **Step 1: Failing tests** — update the existing Grant/Sweep tests to the new shapes, then add:

```go
func TestGrant_Ephemeral_UsesMaxEphemeral(t *testing.T) {
	fl := &fakeLedger{grantResult: true}
	a := New(fakeCounter{}, StaticPolicy{{Project: "acme", Role: "worker"}: {MaxEphemeral: 3}}, fl)
	ok, err := a.Grant(context.Background(), Request{Project: "acme", Role: "worker", ReservationID: "cove-1", Kind: SessionEphemeral})
	if err != nil || !ok { t.Fatalf("got %v,%v", ok, err) }
	if fl.grantCap != 3 || fl.grantReq.Kind != SessionEphemeral { t.Fatalf("ledger saw cap=%d req=%+v", fl.grantCap, fl.grantReq) }
}

func TestGrant_UnsupportedKind_FailsClosed(t *testing.T) {
	a := New(fakeCounter{}, StaticPolicy{{Project: "acme", Role: "worker"}: {MaxEphemeral: 3}}, &fakeLedger{grantResult: true})
	for _, k := range []SessionKind{SessionStanding, SessionPersonal} {
		ok, err := a.Grant(context.Background(), Request{Project: "acme", Role: "worker", ReservationID: "x", Kind: k})
		if ok || !errors.Is(err, ErrUnsupportedKind) { t.Fatalf("kind %s: got %v,%v", k, ok, err) }
	}
}

func TestSweep_SkipsNonEphemeral(t *testing.T) {
	fl := &fakeLedger{outstanding: []Reservation{
		{Project: "acme", Role: "worker", ReservationID: "e", SessionKind: SessionEphemeral},
		{Project: "acme", Role: "worker", ReservationID: "s", SessionKind: SessionStanding},
	}}
	a := New(fakeCounter{live: map[string]bool{}}, StaticPolicy{}, fl) // nothing live
	n, err := a.Sweep(context.Background(), time.Minute)
	if err != nil || n != 1 || len(fl.records) != 1 || fl.records[0].ReservationID != "e" {
		t.Fatalf("swept %d (%v), records %+v — only the ephemeral should be swept", n, err, fl.records)
	}
}
```

Keep the registry-fallback test (nil ledger ⇒ `LiveCount < MaxEphemeral`) and the no-policy fail-closed test, ported to `StaticPolicy`/`Request`.

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement.**

```go
func (a *Allocator) Grant(ctx context.Context, req Request) (bool, error) {
	if req.Kind == "" {
		req.Kind = SessionEphemeral
	}
	if req.Kind != SessionEphemeral {
		return false, fmt.Errorf("%w: %s", ErrUnsupportedKind, req.Kind)
	}
	pol, ok := a.policy.Policy(req.Project, req.Role)
	if !ok || pol.MaxEphemeral <= 0 {
		return false, nil // no policy ⇒ fail closed
	}
	if a.ledger != nil {
		return a.ledger.Grant(ctx, req, pol.MaxEphemeral)
	}
	return a.counter.LiveCount(req.Project, req.Role) < pol.MaxEphemeral, nil
}
```

In `Sweep`, skip any reservation whose `SessionKind` is non-empty and not `SessionEphemeral` (empty = legacy/ephemeral). `RecordRelease` is unchanged (the store derives the release's kind — Task 2).

Update the dispatcher: `Admitter.Grant(ctx, allocator.Request) (bool, error)`; `tick` builds `allocator.Request{Project: d.cfg.Project, Role: d.cfg.Role, ReservationID: actorID, Kind: allocator.SessionEphemeral}`. Update its fake. In `main.go`, replace `allocator.StaticBudget{...: dc.MaxConcurrent}` with `allocator.StaticPolicy{...: {MaxEphemeral: dc.MaxConcurrent}}` so the tree compiles (Task 4 swaps in `RosterPolicy`). `allocpg` will not compile against the new `Ledger` until Task 2 — land Tasks 1–2 in one working session and verify after Task 2.

- [ ] **Step 4:** Run `go test ./internal/allocator/ ./internal/dispatcher/` → PASS.
- [ ] **Step 5: Commit** — `allocator: session kinds, Request, and per-role Policy (session-kinds slice 1)`

---

## Task 2: Session columns + per-kind counting in `allocpg`

**Files:** Create `internal/allocator/allocpg/migrations/0002_session_kind.sql`; modify `internal/allocator/allocpg/allocpg.go`, `allocpg_integration_test.go`.

- [ ] **Step 1: Migration** `0002_session_kind.sql`:

```sql
ALTER TABLE alloc_events
    ADD COLUMN session_kind  TEXT NOT NULL DEFAULT 'ephemeral',
    ADD COLUMN session_name  TEXT NOT NULL DEFAULT '',
    ADD COLUMN session_owner TEXT NOT NULL DEFAULT '';
```

Existing rows become `ephemeral` — every current reservation is one, so counts are unchanged.

- [ ] **Step 2: Failing integration tests** (append; reuse `newTestStore`):

```go
// A cap applies to its own kind only: personal grants do not consume ephemeral capacity.
func TestAllocpg_Grant_CapsPerSessionKind(t *testing.T) {
	st := newTestStore(t); ctx := context.Background()
	req := func(id string, k allocator.SessionKind) allocator.Request {
		return allocator.Request{Project: "acme", Role: "worker", ReservationID: id, Kind: k, Owner: "brent"}
	}
	for _, id := range []string{"p1", "p2"} {
		if ok, err := st.Grant(ctx, req(id, allocator.SessionPersonal), 5); err != nil || !ok { t.Fatalf("personal %s: %v,%v", id, ok, err) }
	}
	if ok, err := st.Grant(ctx, req("e1", allocator.SessionEphemeral), 1); err != nil || !ok { t.Fatalf("e1 should grant (personals don't count): %v,%v", ok, err) }
	if ok, _ := st.Grant(ctx, req("e2", allocator.SessionEphemeral), 1); ok { t.Fatal("e2 should be denied: ephemeral cap 1 reached") }
}

// A release inherits the kind of the reservation's latest grant, so per-kind counts net correctly.
func TestAllocpg_Release_InheritsSessionKind(t *testing.T) {
	st := newTestStore(t); ctx := context.Background()
	r := allocator.Request{Project: "acme", Role: "worker", ReservationID: "p1", Kind: allocator.SessionPersonal, Owner: "brent"}
	if ok, err := st.Grant(ctx, r, 1); err != nil || !ok { t.Fatal(ok, err) }
	if err := st.Record(ctx, allocator.Event{Category: "acme", Project: "acme", Role: "worker", Kind: allocator.KindReservationReleased, ReservationID: "p1"}); err != nil { t.Fatal(err) }
	if ok, err := st.Grant(ctx, allocator.Request{Project: "acme", Role: "worker", ReservationID: "p2", Kind: allocator.SessionPersonal}, 1); err != nil || !ok {
		t.Fatalf("p2 should grant once p1's personal slot is released: %v,%v", ok, err)
	}
}

// OutstandingReservations reports kind/name/owner.
func TestAllocpg_OutstandingReservations_ReportsSessionFields(t *testing.T) { /* grant one personal with Owner, query with a future cutoff, assert SessionKind/Owner */ }
```

Update the existing Grant tests to the `Grant(ctx, allocator.Request, cap)` signature (ephemeral kind).

- [ ] **Step 3: Implement.**
  - `Grant(ctx, req allocator.Request, cap int)`: default an empty `req.Kind` to ephemeral; insert `session_kind`, `session_name`, `session_owner`; the budget subquery counts **only rows of the requested kind**:

    ```sql
    INSERT INTO alloc_events (category, stream_id, stream_revision, kind, reservation_id, data,
                              session_kind, session_name, session_owner)
    SELECT $1, $2, $3, $4, $5, $6, $9, $10, $11
    WHERE (SELECT COUNT(*) FILTER (WHERE kind = $4 AND session_kind = $9)
                - COUNT(*) FILTER (WHERE kind = $7 AND session_kind = $9)
           FROM alloc_events WHERE stream_id = $2) < $8
    ```

    OCC is unchanged: all kinds of a `(project, role)` share one stream, so every append still serializes on `UNIQUE(stream_id, stream_revision)`.
  - `Record`: for a **release** event, set `session_kind` (and name/owner) from the reservation's **latest grant** in the same stream (scalar subquery; `COALESCE` to `'ephemeral'` if none), so callers (supervisor teardown, sweep, compensation) need not know the kind. For other events, use `ev.SessionKind` (default ephemeral).
  - `OutstandingReservations`: also select the latest grant's `session_kind`, `session_name`, `session_owner` per reservation (e.g. `MAX(...) FILTER (WHERE kind = granted)` alongside the existing GROUP BY) and populate them on `allocator.Reservation`.
  - `Outstanding` stays the all-kinds total.

- [ ] **Step 4:** `go build ./...`; `go build -tags integration ./internal/allocator/allocpg/`; run the integration tests with dev Postgres if available, else defer to CI (`store-integration`). `go test ./internal/allocator/... ./internal/dispatcher/` → PASS.
- [ ] **Step 5: Commit** — `allocpg: session_kind columns + per-kind caps (session-kinds slice 1)`

---

## Task 3: `max-ephemeral` on the roster Role

**Files:** Modify `internal/harbor/identity.go` (Role), `internal/harbor/admin.go` (`RoleBody`, `RoleSummary`, `POST`/`GET /admin/roles`), `internal/harbor/adminclient/adminclient.go` (`PutRole`/`ListRoles` mapping), `cmd/at-harbor/main.go` (`cmdRole`), and tests in `internal/harbor/admin_test.go` (or the existing role-admin test file), `internal/harbor/adminclient/adminclient_test.go`, `cmd/at-harbor/main_test.go`.

**Interfaces:**

```go
// RoleAllocation is a role's allocation policy (authored here, read live by the
// Allocator). Later slices add MaxPersonal, Standing, idle settings, and grants.
type RoleAllocation struct {
	MaxEphemeral int `json:"max_ephemeral,omitempty"`
}

type Role struct {
	Name       string         `json:"name"`
	Scope      Scope          `json:"scope"`
	Kit        string         `json:"kit,omitempty"`
	Allocation RoleAllocation `json:"allocation"`
}
```

Roles persist as JSON docs (file store and the pg `roles.doc` column), so storage needs no migration; old docs decode with a zero `Allocation`.

- [ ] **Step 1: Failing tests** — admin round-trip: `POST /admin/roles` with `max_ephemeral: 4` then `GET /admin/roles` returns `max_ephemeral: 4`; a negative value → 400. CLI: `role add ... --max-ephemeral 4` sends it; `role list` prints `max-ephemeral=4`.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement** — add `MaxEphemeral int \`json:"max_ephemeral,omitempty"\`` to `RoleBody` and `RoleSummary`; map both ways in the handlers and the admin client; reject `< 0` with 400; add `--max-ephemeral` (int, default 0 = unset) to `cmdRole` and a `max-ephemeral=N` column to `role list`.
- [ ] **Step 4:** Run `go test ./internal/harbor/... ./cmd/at-harbor/` → PASS.
- [ ] **Step 5: Commit** — `harbor: max-ephemeral role allocation policy (session-kinds slice 1)`

---

## Task 4: `RosterPolicy` wiring + docs + verification

**Files:** Create `cmd/at-harbor/policy.go`, `cmd/at-harbor/policy_test.go`; modify `cmd/at-harbor/main.go`; docs `docs/usage/harbor/roster.md`, `docs/usage/harbor/dispatcher.md`, `docs/usage/harbor/serve.md`.

```go
// rosterPolicy is the Allocator's PolicySource: the roster Role is the source of
// truth (read live from the memory-cached store on each grant); the dispatcher's
// max-concurrent is the fallback for its own (project, role) when the Role sets
// no max-ephemeral. No role and no fallback ⇒ no policy ⇒ fail closed.
type rosterPolicy struct {
	store    harbor.Store
	fallback allocator.StaticPolicy
}

func (p rosterPolicy) Policy(project, role string) (allocator.Policy, bool) {
	if r, ok := p.store.GetRole(project, role); ok && r.Allocation.MaxEphemeral > 0 {
		return allocator.Policy{MaxEphemeral: r.Allocation.MaxEphemeral}, true
	}
	pol, ok := p.fallback[allocator.Key{Project: project, Role: role}]
	return pol, ok
}
```

- [ ] **Step 1: Failing tests** (`policy_test.go`, hermetic over a `FileStore` in a temp dir): roster value wins over the fallback; role without `max-ephemeral` uses the fallback; unknown role with no fallback → `ok=false`.
- [ ] **Step 2:** Run → FAIL. **Step 3: Implement** `policy.go`; in `main.go` build `rosterPolicy{store: st, fallback: allocator.StaticPolicy{{Project: project, Role: dc.Role}: {MaxEphemeral: dc.MaxConcurrent}}}` and pass it to `allocator.New`. If the dispatcher's Role also sets `max-ephemeral` and it differs from `max-concurrent`, log once at startup that the roster value wins.
- [ ] **Step 4: Docs.** `roster.md`: `role add --max-ephemeral N` — the role's ephemeral-session cap, read live by the Allocator. `dispatcher.md`: `max-concurrent` is now the fallback when the dispatcher's role sets no `max-ephemeral`. `serve.md`: the allocation store records each reservation's session kind; only ephemeral exists today.
- [ ] **Step 5: Commit** — `at-harbor: roster-sourced allocation policy (session-kinds slice 1)`
- [ ] **Step 6: Verification gate**
  - `just test` → green.
  - `go build ./...` and `go build -tags integration ./internal/allocator/allocpg/` → compile.
  - `just integration-harbor` with dev Postgres if the daemon starts; else CI's `store-integration` (already covers `./internal/allocator/...`).
  - `just lint` → green.
  - **Behavior check:** no role policy ⇒ same cap as today (`max-concurrent`); a role `max-ephemeral` overrides it; ledger rows are tagged `ephemeral`; the sweep behaves as before.

## Self-review

- **Spec coverage:** Slice 1 of the session-kinds spec — reservation generalization (`SessionKind`/name/owner), per-kind ledger caps, the per-role policy authored in roster with `max-ephemeral` replacing `StaticBudget`. Personal and standing admission are later slices; they fail closed here.
- **Refinement recorded:** live policy read instead of a materialized `RoleBudgetObserved` event (the spec's "where it lives" paragraph is updated to match).
- **Type consistency:** `SessionKind`, `Request`, `Policy`/`PolicySource`/`StaticPolicy`, `Ledger.Grant(ctx, Request, cap)`, `Reservation` session fields, `RoleAllocation.MaxEphemeral`, `rosterPolicy` are used consistently across tasks. `*allocpg.Store` still satisfies `allocator.Ledger`; `*allocator.Allocator` still satisfies `harbor.Releaser`.
- **Compile continuity:** Task 1 changes `Ledger.Grant`, so `allocpg` builds again only after Task 2 — do them back to back.

## Next

Slice 2 (personal: request + ownership) adds `MaxPersonal` and the requester grant to `RoleAllocation`, a `personal` admission path in the Allocator (both caps), and the operator request/release verbs.
