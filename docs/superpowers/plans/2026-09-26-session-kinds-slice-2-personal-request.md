# Session kinds, Slice 2: personal sessions — request and ownership

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A human can ask harbor for a **personal session** of a role, harbor admits it against a per-role pool cap and a per-owner cap, raises it owned by that human, and only the owner can release it. In this slice the session runs its initial prompt like a normal one-shot cove (it ends when the agent finishes); Slice 3 makes it a long-lived conversation.

**Decisions this plan implements** (agreed in review):
- **Owner identity:** roster Humans are linked to their admin login. `Human.Login` holds the operator identity (`OperatorID(r)`: the OIDC `sub`, or `"local"` on loopback). The owner of a personal session is the roster Human in the target project whose `Login` matches the caller. No linked Human ⇒ 403.
- **Caps only for v1:** any admin operator with a linked Human may request any role, bounded by `max-personal` (pool, per `(project, role)`) and `max-personal-per-owner` (per owner, per `(project, role)`). The "which roles may I request" grant is deferred.
- **Personal sessions need the ledger.** They require Postgres (`store-postgres`); without the allocation ledger the request fails with a clear error. (Ephemeral keeps its file-store fallback.)

**Architecture:** `Human` gains `Login`. `RoleAllocation`/`allocator.Policy` gain `MaxPersonal` and `MaxPersonalPerOwner`. `Ledger.Grant` takes a `Caps` value (kind cap + optional owner cap), enforced in the same single atomic insert. `RaiseSpec`/`Instance` gain `Owner` and `SessionKind`. The Allocator is built whenever harbor serves, not only with a dispatcher. New admin endpoints and a `session` CLI verb request, list and release personal sessions.

**Tech Stack:** Go 1.26, `pgx/v5`, `slog`, `just`. Spec: [`../specs/2026-09-26-session-kinds-standing-personal.md`](../specs/2026-09-26-session-kinds-standing-personal.md). Builds on session-kinds Slice 1 (merged: `SessionKind`, `Request`, per-kind caps, roster `max-ephemeral`).

## Global Constraints

- **Ephemeral behavior unchanged.** The dispatcher path, its caps, and its file-store fallback behave exactly as today.
- **Caps are atomic.** Pool cap and owner cap are both checked inside `allocpg.Grant`'s single conditional insert; OCC still serializes on `UNIQUE(stream_id, stream_revision)`. Releases already inherit kind and owner from the grant (Slice 1), so both counts net correctly.
- **Grant, then raise, then compensate.** Same pattern as the dispatcher: `Grant` reserves; if the raise fails, `RecordRelease` the reservation.
- **Owner-only release.** `DELETE` checks the caller's linked Human against `Instance.Owner`. Release goes through `Supervisor.Teardown`, which already records the release.
- **Leave `cove raise` alone.** The existing ungoverned `cove raise` / `POST /admin/coves` path is unchanged (it bypasses the Allocator today; out of scope).
- **Docs in the same change:** a new leaf `docs/usage/harbor/personal-sessions.md` (+ `docs/usage/harbor/INDEX.md` row), `--login` in the roster docs (`comms-addressing.md` owns the project roster), `--max-personal*` in `roster.md`, and fix `coves.md`'s stale text (it says wake is a no-op and lingering is deferred; the code already wakes with `claude --continue`).
- **TDD**; hermetic units + pg-gated `//go:build integration` tests. Stage files by path (an untracked `.switchboard/` exists). End each commit with:
  ```
  Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

---

## Task 1: Link roster Humans to logins

**Files:** `internal/harbor/identity.go` (`Human`), a lookup helper (e.g. `internal/harbor/roster.go` or next to `Human`), `internal/harbor/storetest/conformance.go` fixtures if needed, `cmd/at-harbor/main.go` (`project roster add-human --login`, `roster list`), tests.

```go
type Human struct {
	Name     string            `json:"name"`
	Handle   string            `json:"handle"`
	Login    string            `json:"login,omitempty"`    // admin operator identity (OIDC sub, or "local" on loopback)
	Delivery []DeliveryProfile `json:"delivery,omitempty"`
}

// HumanByLogin returns the roster Human in project whose Login matches login.
// Empty login never matches.
func HumanByLogin(store Store, project, login string) (Human, bool)
```

- [ ] **Step 1: Failing tests** — `HumanByLogin` finds a linked human, misses an unlinked login, never matches `""`; `project roster add-human ... --login auth0|abc` stores it; `roster list` shows `login=...`.
- [ ] **Step 2:** Run → FAIL. **Step 3: Implement.** The `POST /admin/projects/{project}/humans` handler decodes `Human` directly, so the field passes through. Reject a login already used by a *different* Human in the same project (400), so ownership is unambiguous.
- [ ] **Step 4:** Run → PASS. **Step 5: Commit** — `harbor: link roster humans to their admin login (session-kinds slice 2)`

---

## Task 2: Personal caps in the Allocator and ledger

**Files:** `internal/harbor/identity.go` (`RoleAllocation`), `internal/harbor/admin.go` + `adminclient` + `cmd/at-harbor/main.go` (`role add --max-personal`, `--max-personal-per-owner`, `role list`), `internal/allocator/allocator.go` (+tests), `internal/allocator/allocpg/allocpg.go` (+integration tests), `cmd/at-harbor/policy.go` (+test).

**Interfaces:**

```go
// harbor
type RoleAllocation struct {
	MaxEphemeral        int `json:"max_ephemeral,omitempty"`
	MaxPersonal         int `json:"max_personal,omitempty"`           // pool cap; 0 = no personal sessions of this role
	MaxPersonalPerOwner int `json:"max_personal_per_owner,omitempty"` // per owner; 0 = pool cap only
}

// allocator
type Policy struct {
	MaxEphemeral        int
	MaxPersonal         int
	MaxPersonalPerOwner int
}

// Caps are the limits a grant must satisfy atomically: Kind caps outstanding
// reservations of the request's kind; Owner (if > 0) caps those with the same owner.
type Caps struct {
	Kind  int
	Owner int
}

// ErrNeedsLedger: this session kind requires the allocation ledger (Postgres).
var ErrNeedsLedger = errors.New("allocator: personal sessions need the allocation ledger (store-postgres)")
```

`Ledger.Grant(ctx, req Request, caps Caps) (bool, error)` replaces the single `cap int`.

- [ ] **Step 1: Failing tests**
  - Allocator (hermetic, fake ledger): personal with `MaxPersonal: 2, MaxPersonalPerOwner: 1` passes `Caps{Kind: 2, Owner: 1}` to the ledger; personal with no owner → error; `MaxPersonal == 0` → denied (fail closed); nil ledger → `ErrNeedsLedger`; ephemeral unchanged (`Caps{Kind: MaxEphemeral}`); standing still `ErrUnsupportedKind`.
  - allocpg (integration): pool cap across owners (alice 1 + bob 1 with pool 2, a third denied); owner cap (alice 1 with owner cap 1, alice's second denied while bob's granted); release frees the owner's slot (release inherits owner from Slice 1).
  - Role admin/CLI round-trip for the two new fields; negative → 400. `rosterPolicy` returns them.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3: Implement.**
  - `Allocator.Grant`: ephemeral as today but with `Caps{Kind: pol.MaxEphemeral}`. Personal: require `req.Owner != ""`; require a ledger (`ErrNeedsLedger`); `pol.MaxPersonal <= 0` ⇒ `(false, nil)`; call `ledger.Grant(ctx, req, Caps{Kind: pol.MaxPersonal, Owner: pol.MaxPersonalPerOwner})`. Standing stays `ErrUnsupportedKind`.
  - `allocpg.Grant`: add the owner condition to the same `WHERE` when `caps.Owner > 0`:

    ```sql
    WHERE (SELECT COUNT(*) FILTER (WHERE kind = $4 AND session_kind = $9)
                - COUNT(*) FILTER (WHERE kind = $7 AND session_kind = $9)
           FROM alloc_events WHERE stream_id = $2) < $8
      AND ($12 = 0 OR
           (SELECT COUNT(*) FILTER (WHERE kind = $4 AND session_kind = $9 AND session_owner = $11)
                 - COUNT(*) FILTER (WHERE kind = $7 AND session_kind = $9 AND session_owner = $11)
            FROM alloc_events WHERE stream_id = $2) < $12)
    ```
  - Update the dispatcher's use and every fake to the `Caps` signature.
- [ ] **Step 4:** `go test ./internal/allocator/... ./internal/dispatcher/ ./internal/harbor/... ./cmd/at-harbor/`; `go build -tags integration ./internal/allocator/allocpg/` → PASS/compile.
- [ ] **Step 5: Commit** — `allocator: personal sessions with pool and per-owner caps (session-kinds slice 2)`

---

## Task 3: Owner and session kind on raised coves

**Files:** `internal/harbor/supervisor.go` (`RaiseSpec`), `internal/harbor/instance.go` (`Instance`), tests.

```go
type RaiseSpec struct {
	// ...existing...
	Owner       string // personal: the owning roster Human's name; "" otherwise
	SessionKind string // "ephemeral" | "standing" | "personal"; "" = ephemeral
}

// Instance gains:
Owner       string `json:"owner,omitempty"`
SessionKind string `json:"session_kind,omitempty"`
```

- [ ] **Step 1: Failing test** — `Supervisor.Raise` with `Owner: "alice", SessionKind: "personal"` stores both on the Instance.
- [ ] **Step 2–4:** Implement (copy the fields in `Raise`), run → PASS. (`SessionKind` is a plain string in `harbor` to avoid importing `allocator`.)
- [ ] **Step 5: Commit** — `harbor: record owner and session kind on raised coves (session-kinds slice 2)`

---

## Task 4: Build the Allocator whenever harbor serves

**Files:** `cmd/at-harbor/main.go`.

Move out of `if dc := cfg.Runtime.Dispatcher; dc != nil {`: the `rosterPolicy` construction (fallback seeded only when `dc != nil`, else empty), the `ledger` / `allocpg.New` block, `allocator.New`, `alloc.SetLogger`, `sup.SetReleaser(alloc)`, and the sweep loop (with its constants). The dispatcher block keeps only dispatcher-specific wiring and receives `alloc`. The `max-concurrent` override log stays with the dispatcher.

- [ ] **Step 1:** Add/adjust a serve-wiring test if one exists for the allocator; otherwise verify by build + the Task 5 handler tests (which need an Allocator without a dispatcher).
- [ ] **Step 2:** Implement; `go build ./...`; `go test ./cmd/at-harbor/` → PASS.
- [ ] **Step 3: Commit** — `at-harbor: build the allocator without a dispatcher (session-kinds slice 2)`

---

## Task 5: Request, list and release personal sessions

**Files:** `internal/harbor/admin.go` (or a new `internal/harbor/sessions.go` handler file wired into the admin mux) + tests, `internal/harbor/adminclient/adminclient.go` + tests, `cmd/at-harbor/main.go` (new `session` verb + brief in the command table) + tests. The handler needs the Supervisor and the Allocator, so pass the Allocator to the admin mux the way the Supervisor is passed today (via a small interface: `Grant`, `RecordRelease`).

**Endpoints:**
- `POST /admin/sessions/personal` body `{project, role, prompt}` →
  1. `human, ok := HumanByLogin(store, orDefaultProject(project), OperatorID(r))` — else **403** `"no roster human in <project> is linked to your login"`.
  2. id = `"personal-" + human.Name + "-" + <8 hex random>`.
  3. `alloc.Grant(ctx, allocator.Request{Project, Role, ReservationID: id, Kind: SessionPersonal, Owner: human.Name})` — error `ErrNeedsLedger` → **409** with its message; `(false, nil)` → **409** `"at capacity"`; other error → 502.
  4. `sup.Raise(ctx, RaiseSpec{ActorID: id, Project, Role, Prompt, Owner: human.Name, SessionKind: "personal"})` — on error: `alloc.RecordRelease(...)` (compensate), **502**.
  5. **201** `{id, owner, project, role, phase}`.
- `GET /admin/sessions/personal?project=P` → the caller's own personal sessions in P (Instances with `SessionKind == "personal"` and `Owner == human.Name`). 403 if not linked.
- `DELETE /admin/sessions/personal/{id}` → 404 if no such personal Instance; **403** unless the caller's linked Human (in the instance's project) is its `Owner`; then `sup.Teardown` (records the release) → **204**.

**CLI:**
```
at-harbor session request --project P --role R --prompt-file F   # prints the session id
at-harbor session list    [--project P]
at-harbor session release <id>
```
Standard admin-client flags (`--app`, `--admin-url`, `--token`). The prompt file is read on the host, never put on argv (same as `cove raise --prompt-file`).

- [ ] **Step 1: Failing tests** (handler tests over a FileStore-backed harbor with a fake Supervisor/launcher and a fake allocator):
  - request with a linked Human → 201, grant called with `Kind: personal, Owner: alice`, raise called with owner.
  - unlinked login → 403; grant denied → 409; raise fails → 502 **and** release recorded.
  - list shows only the caller's sessions.
  - release by the owner → 204 and teardown; by another linked human → 403; unknown id → 404.
  - client + CLI round-trips.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Run → PASS.
- [ ] **Step 5: Commit** — `harbor: request, list and release personal sessions (session-kinds slice 2)`

---

## Task 6: Docs + verification

- [ ] **Docs.**
  - New `docs/usage/harbor/personal-sessions.md` (leaf, with frontmatter): what a personal session is, linking your login (`project roster add-human --login`), the two caps, `session request|list|release`, that it needs `store-postgres`, and that in this release the session runs its prompt once (long-lived conversation comes next).
  - `docs/usage/harbor/INDEX.md`: add its row.
  - `comms-addressing.md`: the `--login` field on roster Humans.
  - `roster.md`: `role add --max-personal N --max-personal-per-owner M`.
  - `coves.md`: correct the stale "wake is a no-op / lingering deferred" text to match the code (`needs-input` waits for a wake and resumes with `claude --continue`, bounded by `MaxWait`).
- [ ] **Commit** — `docs: personal sessions (session-kinds slice 2)`
- [ ] **Verification gate**
  - `just test` → green.
  - `go build ./...` and `go build -tags integration ./internal/allocator/allocpg/` → compile.
  - `just integration-harbor` with dev Postgres if the daemon starts; else CI's `store-integration`.
  - `just lint` → green.
  - **Behavior check:** dispatcher/ephemeral unchanged; a linked human can request up to the caps, is refused beyond them, and only the owner can release; a session's slot frees on release.

## Self-review

- **Spec coverage:** personal request + ownership (slice 2 in the revised sequence below), with the three review decisions (login link, caps-only, ledger required).
- **Types:** `Human.Login`, `HumanByLogin`, `RoleAllocation.MaxPersonal{,PerOwner}`, `Policy` fields, `Caps`, `ErrNeedsLedger`, `RaiseSpec`/`Instance` `Owner` + `SessionKind` used consistently. `*allocpg.Store` satisfies the updated `Ledger`.
- **Out of scope, stated:** the resident conversation loop, owner-only addressing, ticketless send defaults, running wake/squawks/Discord without a dispatcher (Slice 3); the idle ladder (Slice 4); standing (Slice 5); the "which roles may I request" grant.

## Next: Slice 3 — the long-lived conversation

Resident cove workload (`AT_COVE_RESIDENT`: after every turn report Waiting and block until Wake, no `MaxWait`, a personal-session resume prompt); wake-on skips the wait-max teardown for personal Instances (keeps the pause); escalation skips ticketless coves; a ticketless cove's `send` without `to` defaults to `human:<owner>`; `Enroll` with an `Override{Addressing: ["human:<owner>"]}`; and squawks, wake-on and the Discord relay wired without a dispatcher, covering every project with a Discord chat service. Known v1 limit: the owner can only reply to the cove's messages, not start a thread.
