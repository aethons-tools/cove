# Turn-end lifecycle — Slice 2: `end` + the idle timeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A session can end itself (`end(reason)` takes effect at turn end), and a role can give its sessions an idle timeout (wake the agent, or tear the session down) that the agent can override for the next turn end or for good.

**Architecture:** A new `Role.TurnEnd` policy (`IdleTimeout`, `OnIdle`) is carried through the admin API, client and CLI. The supervisor stamps `TurnEndedAt` and a concrete `IdleDeadline` when a session leaves `running` for `holding`/`waiting`, consuming a `next`-scoped override. Two new cove-facing endpoints (`POST /end`, `PUT /idle`) back two new MCP tools. The wake-on engine tears down a `waiting` session with `EndRequested` (and never wakes it again), fires the idle deadline per `OnIdle`, and keeps `wait-max` only as a backstop for non-resident sessions with no idle deadline armed.

**Tech Stack:** Go; the existing hermetic fakes (`internal/wakeon` fakes, `supTestKit`, `newTestAdmin`, `fakeEscStore`, `connectMCP`/`newMessagingClient`).

**Spec:** `docs/superpowers/specs/2026-10-05-turn-end-lifecycle-design.md` — §8 slice 2 (`end` + the role's idle settings), implementing the "At turn end" and idle-deadline parts of §3, the `end`/`idle_timeout` rows of §1, and the `EndRequested`/`IdleOverride`/`TurnEndedAt` fields of §2.

## Global Constraints

- **No changes** under `internal/dispatch/**`, `internal/dispatchrun`, `cmd/at-task`.
- Ticket-state transitions on `end`/idle teardown are **slice 5** — this slice only logs them. Alarms/gates/`time-zone` are slices 3–4: do not add them.
- Tool names (exact): `end`, `idle_timeout`. Endpoints: `POST /end` body `{"reason": string}`; `PUT /idle` body `{"duration": string, "scope": "next"|"always"}` where `duration` is a Go duration (`"30m"`) or `"off"`.
- `OnIdle` values (exact): `wake`, `teardown`; `""` means `wake`.
- Wake reason for an idle wake: `jam.WakeIdle` (renders "Idle timeout: no other wake arrived." — slice 1).
- Tests hermetic; TDD; build env `export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local`.
- Docs updated in the same change.

## Design rulings carried by this plan (deviations from the spec's wording)

1. **`Role.TurnEnd`, not `RoleAllocation`.** The admin UI's allocation form rebuilds `RoleAllocation` from its form fields on save (`internal/jam/adminui/role_edit.go:152-175`), so new fields there would be wiped by every UI save. A separate struct is untouched by that form. The UI shows it read-only this slice.
2. **Flat role fields, not a YAML `turn-end:` block:** roles are configured by flags/JSON (`--idle-after`, `idle_after_seconds`). So: `--idle-timeout`/`--on-idle`, `idle_timeout_seconds`/`on_idle`.
3. **`wait-max` is a backstop, not removed:** a non-resident `waiting` session is reaped past `wait-max` **only when no idle deadline is armed** (role has no `idle-timeout`, or the agent turned it `off`). Once a deadline is armed, `OnIdle` decides. Without this, `idle_timeout off` on a ticket studio would wait forever.
4. **`OnIdle` defaults to `wake`** (the non-destructive choice).
5. **Concrete deadline at turn end:** the supervisor computes `IdleDeadline` when the turn ends (role timeout, or the override), rather than wake-on recomputing it every tick — so a `next` override is consumed exactly once, and a later role edit affects the *next* turn end, not a wait in progress.

## Review Focus

1. **A squawk and the idle deadline are both due on the same tick** — the agent must get one squawk wake, not a squawk wake followed by "Idle timeout: no other wake arrived." on the next tick. (Task 5, `TestTick_SquawkWakeClearsIdleDeadline`.)
2. **`idle_timeout off` (or a role with no idle timeout) on a ticket studio** — `wait-max` still reaps it; with an armed deadline it does not. (Task 5, `TestTick_WaitMaxOnlyWithoutIdleDeadline`.)
3. **`end` called, then a squawk arrives before the turn ends** — the session is not woken (no new turn), it ends when it reports `waiting`. (Task 5, `TestTick_EndRequestedNeverWoken`.)
4. **Idle deadline passes while the session is paused (`idled`) and `OnIdle: wake`** — it is resumed and woken on a later tick, not skipped. (Task 5, `TestTick_IdleWakeResumesPausedSession`.)
5. **A `next` override followed by two turn ends** — applies to the first, the role default to the second. (Task 2, `TestReportTurnEndConsumesNextOverride`.)

---

## File Structure

| File | Change |
|------|--------|
| `internal/jam/turnend.go` (new) | `TurnEndPolicy`, `OnIdle*` consts, `IdleOverride`, `EndRequest`, `ValidateTurnEnd`. |
| `internal/jam/identity.go` | `Role.TurnEnd`. |
| `internal/jam/instance.go` | `TurnEndedAt`, `IdleDeadline`, `IdleOverride`, `EndRequested`. |
| `internal/jam/admin.go`, `internal/jam/adminclient/adminclient.go` | `idle_timeout_seconds`, `on_idle` on `RoleBody`/`RoleSummary`; mapping; `holding` in the status-report comment. |
| `internal/jam/roleedit.go` | none (`PutRoleKeeping` replaces the role from the body, which now carries `TurnEnd`). |
| `cmd/at-jam/main.go` | `role add --idle-timeout --on-idle`; `role list` prints them; `--activity` help lists `holding`. |
| `internal/jam/adminui/role_detail.go` | two read-only rows. |
| `internal/jam/supervisor.go` | turn-end stamping in `Report`; `SetEndRequested`, `SetIdleOverride`, `ClearIdleDeadline`. |
| `internal/jam/turnend_handler.go` (new) | `POST /end`, `PUT /idle`. |
| `cmd/at-jam/mux.go` | mount `/end`, `/idle`. |
| `cmd/cove-master/mcp.go` | `end`, `idle_timeout` tools. |
| `internal/wakeon/wakeon.go` | end teardown, idle deadline, wait-max backstop; `SetTurnEnd`. |
| `cmd/at-jam/nag.go`, `cmd/at-jam/main.go` | `NotifyEnded`; wire `SetTurnEnd`. |
| `internal/agentrun/workload.go` | slice-1 minors: report `Running` when the BackgroundWait timer ends a hold; tests. |
| Docs | `turn-end.md` (§ending, §idle timeout), `roster.md` (role flags), `intercom.md` (tool list links), `coves.md` (`wait-max` sentence). |

---

### Task 1: `Role.TurnEnd` policy and its plumbing

**Files:**
- Create: `internal/jam/turnend.go`, `internal/jam/turnend_test.go`
- Modify: `internal/jam/identity.go:98-111`, `internal/jam/admin.go` (RoleBody ~88-101, RoleSummary ~104-128, GET mapping ~470-484, POST validation+mapping ~495-525), `internal/jam/adminclient/adminclient.go:150-190,367`, `cmd/at-jam/main.go` (~430-495, 1063), `internal/jam/adminui/role_detail.go:82-90`
- Test: `internal/jam/admin_test.go`, `internal/jam/adminui/role_detail_test.go`

**Interfaces:**
- Produces:
  ```go
  const (OnIdleWake = "wake"; OnIdleTeardown = "teardown")
  type TurnEndPolicy struct {
      IdleTimeout time.Duration `json:"idle_timeout,omitempty"` // 0 = no idle timeout
      OnIdle      string        `json:"on_idle,omitempty"`      // wake | teardown; "" = wake
  }
  func (p TurnEndPolicy) Action() string        // OnIdle with the default applied
  func ValidateTurnEnd(p TurnEndPolicy) error   // IdleTimeout >= 0; OnIdle in {"", wake, teardown}
  // Role gains: TurnEnd TurnEndPolicy `json:"turn_end,omitzero"`
  // RoleBody/RoleSummary gain: IdleTimeoutSeconds int64 `json:"idle_timeout_seconds,omitempty"`; OnIdle string `json:"on_idle,omitempty"`
  ```

- [ ] **Step 1: Write the failing tests**

`internal/jam/turnend_test.go`:

```go
package jam

import (
	"testing"
	"time"
)

func TestTurnEndPolicyAction(t *testing.T) {
	if got := (TurnEndPolicy{}).Action(); got != OnIdleWake {
		t.Fatalf("default action = %q, want wake", got)
	}
	if got := (TurnEndPolicy{OnIdle: OnIdleTeardown}).Action(); got != OnIdleTeardown {
		t.Fatalf("action = %q", got)
	}
}

func TestValidateTurnEnd(t *testing.T) {
	for _, ok := range []TurnEndPolicy{{}, {IdleTimeout: time.Hour, OnIdle: OnIdleWake}, {OnIdle: OnIdleTeardown}} {
		if err := ValidateTurnEnd(ok); err != nil {
			t.Errorf("ValidateTurnEnd(%+v) = %v", ok, err)
		}
	}
	for _, bad := range []TurnEndPolicy{{IdleTimeout: -1}, {OnIdle: "nap"}} {
		if err := ValidateTurnEnd(bad); err == nil {
			t.Errorf("ValidateTurnEnd(%+v) accepted", bad)
		}
	}
}
```

`internal/jam/admin_test.go` (next to `TestAdminRoleIdleSettingsRoundTrip`):

```go
func TestAdminRoleTurnEndRoundTrip(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "worker", IdleTimeoutSeconds: 1800, OnIdle: "teardown"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST role = %d", rec.Code)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].IdleTimeoutSeconds != 1800 || roles[0].OnIdle != "teardown" {
		t.Fatalf("roles = %+v", roles)
	}
	if r, _ := store.GetRole("acme", "worker"); r.TurnEnd != (TurnEndPolicy{IdleTimeout: 30 * time.Minute, OnIdle: OnIdleTeardown}) {
		t.Fatalf("stored turn-end = %+v", r.TurnEnd)
	}
	for _, b := range []RoleBody{
		{Project: "acme", Name: "bad", IdleTimeoutSeconds: -1},
		{Project: "acme", Name: "bad", OnIdle: "nap"},
	} {
		if rec := doJSON(t, h, "POST", "/admin/roles", b); rec.Code != http.StatusBadRequest {
			t.Fatalf("bad turn-end %+v = %d, want 400", b, rec.Code)
		}
	}
}
```

`internal/jam/adminui/role_detail_test.go` — follow the existing role-detail test in that file (it renders a role page and asserts setting rows); add a role with `TurnEnd: jam.TurnEndPolicy{IdleTimeout: 30 * time.Minute, OnIdle: jam.OnIdleTeardown}` and assert the page contains `Idle timeout` and `30m` and `On idle` and `teardown`; for a role without it, assert `none` (idle timeout) and `wake` (on idle). Use the same `fmtDur` formatting the page uses for the other durations.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/jam/ ./internal/jam/adminui/ -run 'TurnEnd|RoleDetail'`
Expected: compile errors (`TurnEndPolicy`, `RoleBody.IdleTimeoutSeconds` undefined).

- [ ] **Step 3: Implement**

`internal/jam/turnend.go`:

```go
package jam

import (
	"fmt"
	"time"
)

// What a session does when its idle deadline passes (TurnEndPolicy.OnIdle).
const (
	OnIdleWake     = "wake"
	OnIdleTeardown = "teardown"
)

// TurnEndPolicy is a role's turn-end policy: how long a session may sit after
// a turn ends with nothing waking it, and what happens then. See
// docs/usage/jam/turn-end.md.
type TurnEndPolicy struct {
	IdleTimeout time.Duration `json:"idle_timeout,omitempty"` // 0 = no idle timeout
	OnIdle      string        `json:"on_idle,omitempty"`      // OnIdleWake | OnIdleTeardown; "" = wake
}

// Action is OnIdle with its default applied.
func (p TurnEndPolicy) Action() string {
	if p.OnIdle == "" {
		return OnIdleWake
	}
	return p.OnIdle
}

// ValidateTurnEnd rejects a negative timeout or an unknown on-idle action.
func ValidateTurnEnd(p TurnEndPolicy) error {
	if p.IdleTimeout < 0 {
		return fmt.Errorf("idle timeout must be >= 0")
	}
	switch p.OnIdle {
	case "", OnIdleWake, OnIdleTeardown:
		return nil
	}
	return fmt.Errorf("on-idle must be %q or %q", OnIdleWake, OnIdleTeardown)
}
```

`identity.go` `Role`: after `Allocation`:

```go
	// TurnEnd is the role's turn-end policy (idle timeout + on-idle action).
	// Separate from Allocation, whose UI form rebuilds it on save.
	TurnEnd TurnEndPolicy `json:"turn_end,omitzero"`
```

`admin.go`: add to both `RoleBody` and `RoleSummary`:

```go
	// IdleTimeoutSeconds / OnIdle are the role's turn-end policy
	// (Role.TurnEnd): 0 = no idle timeout; on_idle "" = wake.
	IdleTimeoutSeconds int64  `json:"idle_timeout_seconds,omitempty"`
	OnIdle             string `json:"on_idle,omitempty"`
```

GET mapping: `IdleTimeoutSeconds: int64(ro.TurnEnd.IdleTimeout / time.Second), OnIdle: ro.TurnEnd.OnIdle,`. POST: build `te := TurnEndPolicy{IdleTimeout: time.Duration(b.IdleTimeoutSeconds) * time.Second, OnIdle: b.OnIdle}`; `if err := ValidateTurnEnd(te); err != nil { http.Error(w, err.Error(), http.StatusBadRequest); return }` next to the idle-ladder check; set `TurnEnd: te` on the `Role` literal.

`adminclient.go`: `PutRole` sends `IdleTimeoutSeconds: int64(r.TurnEnd.IdleTimeout / time.Second), OnIdle: r.TurnEnd.OnIdle`; `ListRoles` sets `TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Duration(rs.IdleTimeoutSeconds) * time.Second, OnIdle: rs.OnIdle}`. Fix the `ReportCoveStatus` comment to `(running|holding|waiting|blocked|done)`.

`cmd/at-jam/main.go` `role add`:

```go
	idleTimeout := fs.Duration("idle-timeout", 0, "after a turn ends with nothing waking the session for this long, apply --on-idle (0 = no idle timeout)")
	onIdle := fs.String("on-idle", "", "what the idle timeout does: wake (default) | teardown")
```

Validate with `jam.ValidateTurnEnd` (exit 2, message prefixed `at-jam role add:`), set `TurnEnd: jam.TurnEndPolicy{IdleTimeout: *idleTimeout, OnIdle: *onIdle}` on the role. `role list`: append `\tidle-timeout=%s\ton-idle=%s` with `r.TurnEnd.IdleTimeout, r.TurnEnd.Action()`. `studio status --activity` help: `running|holding|waiting|blocked|done (status only)`.

`role_detail.go`: append to `d.Allocation`:

```go
		duration("Idle timeout", role.TurnEnd.IdleTimeout, "none"),
		setting{Name: "On idle", Value: role.TurnEnd.Action()},
```

(Match the actual `setting` struct's field names in `role_detail.go`; if `setting` has a helper for plain strings, use it.)

- [ ] **Step 4: Run to verify they pass**

Run: `go build ./... && go test ./internal/jam/... ./cmd/at-jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam cmd/at-jam
git commit -m "feat(jam): role turn-end policy (idle timeout, on-idle)"
```

---

### Task 2: Instance turn-end state and the supervisor

**Files:**
- Modify: `internal/jam/instance.go` (fields), `internal/jam/turnend.go` (types), `internal/jam/supervisor.go` (`Report` ~418-445; new setters after `SetEscalationCategory`)
- Test: `internal/jam/supervisor_test.go`

**Interfaces:**
- Consumes: `TurnEndPolicy` (Task 1).
- Produces:
  ```go
  const (IdleScopeNext = "next"; IdleScopeAlways = "always")
  type IdleOverride struct {
      Duration time.Duration `json:"duration"` // 0 = off
      Scope    string        `json:"scope"`    // next | always
  }
  type EndRequest struct {
      Reason string    `json:"reason"`
      At     time.Time `json:"at"`
  }
  // Instance gains:
  //   TurnEndedAt  time.Time     `json:"turn_ended_at,omitempty"`
  //   IdleDeadline time.Time     `json:"idle_deadline,omitempty"` // zero = none armed
  //   IdleOverride *IdleOverride `json:"idle_override,omitempty"`
  //   EndRequested *EndRequest   `json:"end_requested,omitempty"`
  func (s *Supervisor) SetEndRequested(actorID, reason string) error   // first request wins; idempotent
  func (s *Supervisor) SetIdleOverride(actorID string, o IdleOverride) error
  func (s *Supervisor) ClearIdleDeadline(actorID string) error
  ```

- [ ] **Step 1: Write the failing tests** (`internal/jam/supervisor_test.go`)

```go
// raiseWithRole raises w1 under a role carrying the given turn-end policy.
func raiseWithTurnEnd(t *testing.T, te TurnEndPolicy) (*Supervisor, Store, *time.Time) {
	t.Helper()
	sup, store, now := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutRole("default", Role{Name: "te", Scope: Scope{Destinations: []string{"anthropic"}}, TurnEnd: te}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "te"}); err != nil {
		t.Fatal(err)
	}
	return sup, store, now // *now is the supervisor's clock
}

func TestReportTurnEndArmsIdleDeadline(t *testing.T) {
	sup, store, now := raiseWithTurnEnd(t, TurnEndPolicy{IdleTimeout: 30 * time.Minute})
	*now = time.Unix(2000, 0)
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if !inst.TurnEndedAt.Equal(time.Unix(2000, 0)) || !inst.IdleDeadline.Equal(time.Unix(2000, 0).Add(30*time.Minute)) {
		t.Fatalf("turn ended %v, deadline %v", inst.TurnEndedAt, inst.IdleDeadline)
	}
	// holding → waiting is the same turn end: no re-stamp.
	sup2, store2, now2 := raiseWithTurnEnd(t, TurnEndPolicy{IdleTimeout: time.Minute})
	*now2 = time.Unix(3000, 0)
	_ = sup2.Report(context.Background(), "w1", ActivityHolding)
	*now2 = time.Unix(3500, 0)
	_ = sup2.Report(context.Background(), "w1", ActivityWaiting)
	inst2, _ := store2.GetInstance("w1")
	if !inst2.TurnEndedAt.Equal(time.Unix(3000, 0)) {
		t.Fatalf("holding→waiting re-stamped TurnEndedAt to %v", inst2.TurnEndedAt)
	}
}

func TestReportTurnEndNoTimeoutNoDeadline(t *testing.T) {
	sup, store, _ := raiseWithTurnEnd(t, TurnEndPolicy{})
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	if inst, _ := store.GetInstance("w1"); !inst.IdleDeadline.IsZero() {
		t.Fatalf("deadline armed without a timeout: %v", inst.IdleDeadline)
	}
}

func TestReportTurnEndConsumesNextOverride(t *testing.T) {
	sup, store, now := raiseWithTurnEnd(t, TurnEndPolicy{IdleTimeout: time.Hour})
	if err := sup.SetIdleOverride("w1", IdleOverride{Duration: 5 * time.Minute, Scope: IdleScopeNext}); err != nil {
		t.Fatal(err)
	}
	*now = time.Unix(2000, 0)
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	inst, _ := store.GetInstance("w1")
	if !inst.IdleDeadline.Equal(time.Unix(2000, 0).Add(5*time.Minute)) || inst.IdleOverride != nil {
		t.Fatalf("first turn end: deadline %v override %+v", inst.IdleDeadline, inst.IdleOverride)
	}
	_ = sup.Report(context.Background(), "w1", ActivityRunning)
	*now = time.Unix(4000, 0)
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	inst, _ = store.GetInstance("w1")
	if !inst.IdleDeadline.Equal(time.Unix(4000, 0).Add(time.Hour)) {
		t.Fatalf("second turn end: deadline %v, want role default", inst.IdleDeadline)
	}
}

func TestReportTurnEndAlwaysOverrideOffPersists(t *testing.T) {
	sup, store, _ := raiseWithTurnEnd(t, TurnEndPolicy{IdleTimeout: time.Hour})
	_ = sup.SetIdleOverride("w1", IdleOverride{Duration: 0, Scope: IdleScopeAlways})
	for i := 0; i < 2; i++ {
		_ = sup.Report(context.Background(), "w1", ActivityWaiting)
		inst, _ := store.GetInstance("w1")
		if !inst.IdleDeadline.IsZero() || inst.IdleOverride == nil {
			t.Fatalf("turn end %d: deadline %v override %+v", i, inst.IdleDeadline, inst.IdleOverride)
		}
		_ = sup.Report(context.Background(), "w1", ActivityRunning)
	}
}

func TestSetEndRequestedFirstWins(t *testing.T) {
	sup, store, _ := raiseWithTurnEnd(t, TurnEndPolicy{})
	_ = sup.SetEndRequested("w1", "done")
	_ = sup.SetEndRequested("w1", "again")
	if inst, _ := store.GetInstance("w1"); inst.EndRequested == nil || inst.EndRequested.Reason != "done" {
		t.Fatalf("end requested = %+v", inst.EndRequested)
	}
}

func TestClearIdleDeadline(t *testing.T) {
	sup, store, _ := raiseWithTurnEnd(t, TurnEndPolicy{IdleTimeout: time.Minute})
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	_ = sup.ClearIdleDeadline("w1")
	if inst, _ := store.GetInstance("w1"); !inst.IdleDeadline.IsZero() {
		t.Fatal("deadline not cleared")
	}
}
```

(`supTestKit` returns a pointer to the supervisor's clock; assigning `*now` moves it. The `TurnEndedAt` comparisons use `time.Unix(...)`; the kit's clock is UTC — compare with `.Equal`, as written.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/jam/ -run 'TurnEnd|EndRequested|ClearIdleDeadline'`
Expected: compile errors (`SetIdleOverride`, `IdleOverride` undefined).

- [ ] **Step 3: Implement**

Types go in `turnend.go` (from Interfaces). Instance fields in `instance.go` with one-line comments as in Interfaces.

`supervisor.go` `Report`, before `inst.Activity = a`:

```go
	// A turn ends when the cove leaves Running for Holding or Waiting (not
	// Holding → Waiting: same turn end). Arm the idle deadline from the
	// override (a next-scoped one is used up here) or the role's timeout.
	turnEnded := inst.Activity == ActivityRunning && (a == ActivityHolding || a == ActivityWaiting)
	if turnEnded {
		inst.TurnEndedAt = now
		inst.IdleDeadline = time.Time{}
		d := time.Duration(0)
		if role, ok := s.store.GetRole(inst.Project, inst.Role); ok {
			d = role.TurnEnd.IdleTimeout
		}
		if o := inst.IdleOverride; o != nil {
			d = o.Duration
			if o.Scope == IdleScopeNext {
				inst.IdleOverride = nil
			}
		}
		if d > 0 {
			inst.IdleDeadline = now.Add(d)
		}
	}
```

(Check `inst.Project` is the field the store keys roles by — `Instance` has `Project` and `Role`; an empty `Project` means `DefaultProject` elsewhere — use the same normalization `GetRole` callers in this file use.)

Setters (same shape as `SetEscalationCategory`):

```go
// SetEndRequested records that the cove asked to end (first request wins).
// Wake-on tears it down once it is Waiting and never wakes it again.
func (s *Supervisor) SetEndRequested(actorID, reason string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	if inst.EndRequested != nil {
		return nil
	}
	inst.EndRequested = &EndRequest{Reason: reason, At: s.now()}
	return s.store.PutInstance(inst)
}

// SetIdleOverride replaces the cove's idle-timeout override; it applies from
// the next turn end (a next-scoped one only to that turn end).
func (s *Supervisor) SetIdleOverride(actorID string, o IdleOverride) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.IdleOverride = &o
	return s.store.PutInstance(inst)
}

// ClearIdleDeadline disarms the cove's idle deadline (it fired, or another
// wake answered this turn end).
func (s *Supervisor) ClearIdleDeadline(actorID string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	inst.IdleDeadline = time.Time{}
	return s.store.PutInstance(inst)
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/jam/...`
Expected: PASS (including the Postgres conformance suite's JSON round-trip, if it runs hermetically; the store-integration suite runs in CI).

- [ ] **Step 5: Commit**

```bash
git add internal/jam
git commit -m "feat(jam): arm the idle deadline at turn end; end and idle-override state"
```

---

### Task 3: `POST /end` and `PUT /idle`

**Files:**
- Create: `internal/jam/turnend_handler.go`, `internal/jam/turnend_handler_test.go`
- Modify: `cmd/at-jam/mux.go:34-50,88-93`
- Test: `cmd/at-jam/mux_test.go` (if routing is tested there; else add one routing assertion)

**Interfaces:**
- Consumes: `SetEndRequested`, `SetIdleOverride`, `IdleOverride`, `IdleScope*` (Task 2).
- Produces:
  ```go
  type turnEndSetter interface {
      SetEndRequested(actorID, reason string) error
      SetIdleOverride(actorID string, o IdleOverride) error
  }
  func NewTurnEndHandler(store escalateStore, setter turnEndSetter, log *slog.Logger) *TurnEndHandler // serves POST /end and PUT /idle
  ```

Validation: `/end` — `reason` required, ≤ 1000 bytes (body cap 2 KiB) → else 400. `/idle` — `scope` must be `next` or `always`; `duration` `"off"` → 0, else `time.ParseDuration` and `> 0` and `≤ 30 days` → else 400. Both: 401 missing/unknown token, 403 no instance, 405 wrong method, 413 oversize, 502 setter error, 204 success.

- [ ] **Step 1: Write the failing tests** (`internal/jam/turnend_handler_test.go`, reusing `fakeEscStore`, `testLogger` from `escalate_handler_test.go`)

```go
package jam

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeTurnEndSetter struct {
	endActor, endReason string
	idleActor           string
	idle                IdleOverride
	err                 error
}

func (f *fakeTurnEndSetter) SetEndRequested(a, r string) error { f.endActor, f.endReason = a, r; return f.err }
func (f *fakeTurnEndSetter) SetIdleOverride(a string, o IdleOverride) error {
	f.idleActor, f.idle = a, o
	return f.err
}

func turnEndFixture() (*TurnEndHandler, *fakeTurnEndSetter) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1"}
	set := &fakeTurnEndSetter{}
	return NewTurnEndHandler(st, set, testLogger()), set
}

func doTurnEnd(h http.Handler, method, path, tok, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEndRecordsReason(t *testing.T) {
	h, set := turnEndFixture()
	if rec := doTurnEnd(h, "POST", "/end", "tok", `{"reason":"merged"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if set.endActor != "cove-1" || set.endReason != "merged" {
		t.Fatalf("got %q %q", set.endActor, set.endReason)
	}
}

func TestEndRejects(t *testing.T) {
	h, _ := turnEndFixture()
	for _, c := range []struct {
		method, tok, body string
		want              int
	}{
		{"POST", "", `{"reason":"x"}`, 401},
		{"POST", "nope", `{"reason":"x"}`, 401},
		{"POST", "tok", `{"reason":""}`, 400},
		{"POST", "tok", `{"reason":"` + strings.Repeat("x", 1001) + `"}`, 400},
		{"GET", "tok", ``, 405},
	} {
		if rec := doTurnEnd(h, c.method, "/end", c.tok, c.body); rec.Code != c.want {
			t.Errorf("%+v → %d, want %d", c, rec.Code, c.want)
		}
	}
}

func TestIdleSetsOverride(t *testing.T) {
	h, set := turnEndFixture()
	if rec := doTurnEnd(h, "PUT", "/idle", "tok", `{"duration":"45m","scope":"next"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if set.idle != (IdleOverride{Duration: 45 * time.Minute, Scope: IdleScopeNext}) {
		t.Fatalf("override = %+v", set.idle)
	}
	if rec := doTurnEnd(h, "PUT", "/idle", "tok", `{"duration":"off","scope":"always"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("off: status %d", rec.Code)
	}
	if set.idle != (IdleOverride{Duration: 0, Scope: IdleScopeAlways}) {
		t.Fatalf("override = %+v", set.idle)
	}
}

func TestIdleRejects(t *testing.T) {
	h, _ := turnEndFixture()
	for _, body := range []string{
		`{"duration":"45m","scope":"sometimes"}`,
		`{"duration":"soon","scope":"next"}`,
		`{"duration":"-5m","scope":"next"}`,
		`{"duration":"0s","scope":"next"}`,
		`{"duration":"800h","scope":"next"}`,
	} {
		if rec := doTurnEnd(h, "PUT", "/idle", "tok", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", body, rec.Code)
		}
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/jam/ -run 'TestEnd|TestIdle'`
Expected: compile errors (`NewTurnEndHandler` undefined).

- [ ] **Step 3: Implement `internal/jam/turnend_handler.go`**

Follow `EscalateHandler` exactly for auth, instance check, `MaxBytesReader` (2048), decode and the 413 path; factor the shared "authenticate → actor" prefix into a small helper *inside this file* (do not refactor `escalate_handler.go`). Dispatch on `r.URL.Path`: `/end` requires POST, `/idle` requires PUT (405 with `Allow` otherwise). Log `turn-end: end requested` / `turn-end: idle override` with the actor id (never the token).

```go
const (
	maxTurnEndBodyBytes = 2048
	maxEndReason        = 1000
	maxIdleOverride     = 30 * 24 * time.Hour
)

// parseIdle turns the /idle body into an override: "off" → 0, else a positive
// Go duration up to maxIdleOverride; scope next|always.
func parseIdle(duration, scope string) (IdleOverride, error) {
	if scope != IdleScopeNext && scope != IdleScopeAlways {
		return IdleOverride{}, fmt.Errorf("scope must be %q or %q", IdleScopeNext, IdleScopeAlways)
	}
	if duration == "off" {
		return IdleOverride{Scope: scope}, nil
	}
	d, err := time.ParseDuration(duration)
	if err != nil || d <= 0 || d > maxIdleOverride {
		return IdleOverride{}, fmt.Errorf(`duration must be "off" or a positive duration up to 720h`)
	}
	return IdleOverride{Duration: d, Scope: scope}, nil
}
```

Error bodies are the validation message (the agent reads them).

`cmd/at-jam/mux.go`: `squawksMux` gains a `turnEndH http.Handler` parameter and `case "/end", "/idle": turnEndH.ServeHTTP(w, r)`; `coveHTTPHandler` builds `jam.NewTurnEndHandler(st, sup, log)` and logs `Jam turn-end: mounted`. Update `squawksMux`'s doc comment and every caller/test of `squawksMux`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/jam/ ./cmd/at-jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam/turnend_handler*.go cmd/at-jam/mux*.go
git commit -m "feat(jam): POST /end and PUT /idle cove endpoints"
```

---

### Task 4: `end` and `idle_timeout` MCP tools

**Files:**
- Modify: `cmd/cove-master/mcp.go` (input types near `escalateIn` ~90; client methods near `escalate` ~273; tool registration near the `escalate` tool ~357)
- Test: `cmd/cove-master/mcp_test.go`

**Interfaces:**
- Consumes: the endpoints from Task 3.
- Produces: tools `end` (`{reason}`) and `idle_timeout` (`{duration, scope}`); client methods `end(ctx, reason string) error`, `idleTimeout(ctx, duration, scope string) error`.

- [ ] **Step 1: Write the failing tests** (after `TestMCPEscalateForwardsCategory`)

```go
func turnEndClient(t *testing.T, gotMethod, gotPath, gotBody *string) *messagingClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotMethod, *gotPath = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		*gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	c, err := newMessagingClient(func(k string) string {
		switch k {
		case "AT_JAM_RUNTIME_ADDR":
			return srv.URL
		case "AT_JAM_IDENTITY_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMCPEndForwardsReason(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.end(context.Background(), "merged"); err != nil {
		t.Fatal(err)
	}
	if m != "POST" || p != "/end" || !strings.Contains(b, `"reason":"merged"`) {
		t.Fatalf("%s %s %s", m, p, b)
	}
}

func TestMCPIdleTimeoutForwards(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.idleTimeout(context.Background(), "45m", "next"); err != nil {
		t.Fatal(err)
	}
	if m != "PUT" || p != "/idle" || !strings.Contains(b, `"duration":"45m"`) || !strings.Contains(b, `"scope":"next"`) {
		t.Fatalf("%s %s %s", m, p, b)
	}
}
```

Also extend the test that lists the server's tools (search `mcp_test.go` for the tool-name list assertion, e.g. `"escalate"`) to expect `end` and `idle_timeout`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./cmd/cove-master/ -run 'End|IdleTimeout|Tools'`
Expected: compile errors (`c.end`, `c.idleTimeout` undefined).

- [ ] **Step 3: Implement**

```go
// endIn is the "end" tool's typed input.
type endIn struct {
	Reason string `json:"reason" jsonschema:"why the session is ending (shown to its owner); the session is torn down after this turn ends and is never woken again"`
}

// idleTimeoutIn is the "idle_timeout" tool's typed input.
type idleTimeoutIn struct {
	Duration string `json:"duration" jsonschema:"how long after this turn ends to apply the role's on-idle action if nothing else wakes you, e.g. 45m or 2h; or off"`
	Scope    string `json:"scope" jsonschema:"next (only the next turn end) or always (until the session ends)"`
}
```

Client methods mirror `escalate` (`c.do(ctx, http.MethodPost, "/end", payload)`; `c.do(ctx, http.MethodPut, "/idle", payload)`). Register both tools after `escalate`:

- `end` — Description: `"End this session for good once the current turn finishes: do any wrap-up first, then call this as your last action. Irrevocable; the session is never woken again."`
- `idle_timeout` — Description: `"Change how long after this turn ends Jam waits before applying the role's on-idle action (wake you, or end the session) if nothing else wakes you. scope next = only the next turn end; always = until the session ends. duration off disables it."`

Check `c.do` accepts `http.MethodPut`; if it hard-codes POST semantics anywhere (e.g. a status check expecting 200 vs 204), make it accept 204 as `escalate` does.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./cmd/cove-master/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/cove-master
git commit -m "feat(cove-master): end and idle_timeout intercom tools"
```

---

### Task 5: Wake-on enforces `end` and the idle deadline

**Files:**
- Modify: `internal/wakeon/wakeon.go` (interfaces, `Engine` fields, `tick` ~147-200, `wakeRunning` ~205-225), `cmd/at-jam/nag.go` (`NotifyEnded`), `cmd/at-jam/main.go` (~1830, wire `SetTurnEnd`)
- Test: `internal/wakeon/wakeon_test.go`, `cmd/at-jam/nag_test.go` (if present; else a small test beside the nagger's existing tests)

**Interfaces:**
- Consumes: `Instance.EndRequested`, `IdleDeadline` (Task 2), `Role.TurnEnd.Action()` (Task 1), `jam.WakeIdle` (slice 1).
- Produces:
  ```go
  // wakeon
  type TurnEndState interface{ ClearIdleDeadline(actorID string) error } // *jam.Supervisor
  type Ender interface {
      NotifyEnded(ctx context.Context, inst jam.Instance, reason string) error
  }
  func (e *Engine) SetTurnEnd(roles RoleLookup, state TurnEndState, ender Ender) // call before Run; ender may be nil
  // cmd/at-jam
  func (n intercomNagger) NotifyEnded(ctx context.Context, inst jam.Instance, reason string) error // owner squawk; no owner → nil
  ```

Behavior (in `tick`, per instance):
1. `EndRequested != nil`: if `Activity == Waiting` → `Teardown`; on success, `NotifyEnded` (best-effort, logged). Then `continue` — whatever the activity, it is **never woken** (no squawk wake, no idle wake) and never paused (a teardown is imminent).
2. `Running`/`Holding`: `woke := e.wakeRunning(inst)` (now returns bool); if `woke` → `ClearIdleDeadline`. Then, **Holding only**, if the idle deadline is due and the action is `wake` → `Wake(WakeIdle)` + `ClearIdleDeadline` (a `teardown` action waits for `waiting`). `continue`.
3. `Waiting`:
   - `wait-max` teardown only when `!IsResident && IdleDeadline.IsZero() && waited > MaxWait`.
   - Squawk replies: as today; when it wakes (Waiting, not Idled) also `ClearIdleDeadline`.
   - Idle deadline due (`!IdleDeadline.IsZero() && now >= IdleDeadline`):
     - `wake`: if `Phase == Idled` → `Resume` and `continue` (woken on a later tick); else `Wake(WakeIdle)` + `ClearIdleDeadline`; `continue`.
     - `teardown`: `Teardown` (log `wakeon: idle timeout, tearing down`); `continue`.
   - Then the personal idle ladder and warm-timeout pause as today.

`SetTurnEnd` not called (tests, or a Jam without it) → no idle deadlines fire and end is not enforced; `wait-max` keeps today's behavior (deadline is always zero then — fine).

- [ ] **Step 1: Write the failing tests** (`internal/wakeon/wakeon_test.go`)

Add fakes:

```go
type fakeTurnEnd struct{ cleared []string }

func (f *fakeTurnEnd) ClearIdleDeadline(a string) error { f.cleared = append(f.cleared, a); return nil }

type fakeEnder struct{ ended []string }

func (f *fakeEnder) NotifyEnded(_ context.Context, inst jam.Instance, reason string) error {
	f.ended = append(f.ended, inst.ActorID+":"+reason)
	return nil
}

// turnEndEngine builds an engine with the turn-end hooks on, at now=10000s.
func turnEndEngine(insts []jam.Instance, inbox Inbox, roles fakeRoles) (*Engine, *fakeWaker, *fakeReaper, *fakeIdler, *fakeTurnEnd, *fakeEnder) {
	reg := &fakeReg{insts: insts}
	wake, reap, idler, te, ender := &fakeWaker{}, &fakeReaper{}, &fakeIdler{}, &fakeTurnEnd{}, &fakeEnder{}
	if inbox == nil {
		inbox = &fakeInbox{}
	}
	e := New(reg, wake, reap, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: 24 * time.Hour}, nil)
	e.SetRunningWake(&fakeCursor{reg: reg})
	e.SetTurnEnd(roles, te, ender)
	e.now = func() time.Time { return time.Unix(10000, 0) }
	return e, wake, reap, idler, te, ender
}
```

Tests (each one behavior):

```go
func TestTick_EndRequestedWaitingTornDownAndNotified(t *testing.T) {
	e, wake, reap, _, _, ender := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindPersonal, Owner: "alice", WaitingSince: time.Unix(9990, 0),
		EndRequested: &jam.EndRequest{Reason: "wrapped up"}}}, nil, nil)
	e.tick(context.Background())
	if len(reap.down) != 1 || len(ender.ended) != 1 || ender.ended[0] != "a1:wrapped up" || len(wake.woke) != 0 {
		t.Fatalf("teardown=%v ended=%v woke=%v", reap.down, ender.ended, wake.woke)
	}
}

func TestTick_EndRequestedNeverWoken(t *testing.T) {
	for _, act := range []jam.Activity{jam.ActivityRunning, jam.ActivityHolding} {
		inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{"a1": {extInbound("a1", 6, "id-6")}}}
		e, wake, reap, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: act, WaitSeq: 5,
			EndRequested: &jam.EndRequest{Reason: "x"}}}, inbox, nil)
		e.tick(context.Background())
		if len(wake.woke) != 0 || len(reap.down) != 0 {
			t.Fatalf("%s: woke=%v teardown=%v; want neither until it waits", act, wake.woke, reap.down)
		}
	}
}

func TestTick_IdleDeadlineWakes(t *testing.T) {
	e, wake, _, _, te, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Project: "p", Role: "r", Phase: jam.PhaseLive,
		Activity: jam.ActivityWaiting, SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0),
		IdleDeadline: time.Unix(9999, 0)}}, nil, fakeRoles{"p/r": {TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Minute}}})
	e.tick(context.Background())
	if len(wake.woke) != 1 || wake.reasons[0][0].Kind != jam.WakeIdle || len(te.cleared) != 1 {
		t.Fatalf("woke=%v reasons=%v cleared=%v", wake.woke, wake.reasons, te.cleared)
	}
}

func TestTick_IdleDeadlineNotYetDue(t *testing.T) {
	e, wake, _, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0), IdleDeadline: time.Unix(10001, 0)}}, nil, nil)
	e.tick(context.Background())
	if len(wake.woke) != 0 {
		t.Fatalf("woke before the deadline: %v", wake.woke)
	}
}

func TestTick_IdleDeadlineTeardown(t *testing.T) {
	e, wake, reap, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Project: "p", Role: "r", Phase: jam.PhaseLive,
		Activity: jam.ActivityWaiting, WaitingSince: time.Unix(9000, 0), IdleDeadline: time.Unix(9999, 0)}},
		nil, fakeRoles{"p/r": {TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Minute, OnIdle: jam.OnIdleTeardown}}})
	e.tick(context.Background())
	if len(reap.down) != 1 || len(wake.woke) != 0 {
		t.Fatalf("teardown=%v woke=%v", reap.down, wake.woke)
	}
}

func TestTick_IdleWakeResumesPausedSession(t *testing.T) {
	e, wake, _, idler, te, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0), IdleDeadline: time.Unix(9999, 0)}}, nil, nil)
	e.tick(context.Background())
	if len(idler.resumed) != 1 || len(wake.woke) != 0 || len(te.cleared) != 0 {
		t.Fatalf("resumed=%v woke=%v cleared=%v; want resume now, wake later", idler.resumed, wake.woke, te.cleared)
	}
}

func TestTick_HoldingIdleWakeButNotTeardown(t *testing.T) {
	roles := fakeRoles{"p/w": {TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Minute}}, "p/t": {TurnEnd: jam.TurnEndPolicy{IdleTimeout: time.Minute, OnIdle: jam.OnIdleTeardown}}}
	e, wake, reap, _, _, _ := turnEndEngine([]jam.Instance{
		{ActorID: "w", Project: "p", Role: "w", Phase: jam.PhaseLive, Activity: jam.ActivityHolding, IdleDeadline: time.Unix(9999, 0)},
		{ActorID: "t", Project: "p", Role: "t", Phase: jam.PhaseLive, Activity: jam.ActivityHolding, IdleDeadline: time.Unix(9999, 0)},
	}, nil, roles)
	e.tick(context.Background())
	if len(wake.woke) != 1 || wake.woke[0] != "w" || len(reap.down) != 0 {
		t.Fatalf("woke=%v teardown=%v", wake.woke, reap.down)
	}
}

func TestTick_SquawkWakeClearsIdleDeadline(t *testing.T) {
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{"a1": {extInbound("a1", 6, "id-6")}}}
	e, wake, _, _, te, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitSeq: 5, WaitingSince: time.Unix(9000, 0), IdleDeadline: time.Unix(9999, 0)}}, inbox, nil)
	e.tick(context.Background())
	if len(wake.woke) != 1 || wake.reasons[0][0].Kind != jam.WakeSquawk || len(te.cleared) != 1 {
		t.Fatalf("woke=%v reasons=%v cleared=%v; want one squawk wake and the deadline cleared", wake.woke, wake.reasons, te.cleared)
	}
}

func TestTick_WaitMaxOnlyWithoutIdleDeadline(t *testing.T) {
	// non-resident, waited 2h > MaxWait 1h
	base := jam.Instance{Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, WaitingSince: time.Unix(10000-7200, 0)}
	armed, unarmed := base, base
	armed.ActorID, armed.IdleDeadline = "armed", time.Unix(20000, 0)
	unarmed.ActorID = "unarmed"
	e, _, reap, _, _, _ := turnEndEngine([]jam.Instance{armed, unarmed}, nil, nil)
	e.tick(context.Background())
	if len(reap.down) != 1 || reap.down[0] != "unarmed" {
		t.Fatalf("teardown=%v, want only the session with no idle deadline", reap.down)
	}
}
```

(Check `fakeReaper.down`, `fakeIdler.resumed` field names against the file; the `wakeon` test file's existing fakes are authoritative.)

`cmd/at-jam` — in the nagger's test file, add:

```go
func TestNagger_NotifyEnded(t *testing.T) {
	lg := openTestLog(t)
	n := intercomNagger{log: lg, roster: &fakeStore{}}
	if err := n.NotifyEnded(context.Background(), nagInst, "wrapped up"); err != nil {
		t.Fatal(err)
	}
	if err := n.NotifyEnded(context.Background(), jam.Instance{ActorID: "s1", Project: "acme"}, "x"); err != nil {
		t.Fatal(err)
	}
	got := lg.List(intercom.Filter{})
	if len(got) != 1 || got[0].To[0] != (intercom.Target{Kind: "human", Ref: "alice"}) || !strings.Contains(got[0].Body, "wrapped up") {
		t.Fatalf("sent %+v; want one notice to alice, none for the ownerless session", got)
	}
}
```

(`openTestLog`, `fakeStore` and `nagInst` (owner alice, project acme) are the existing fixtures in `cmd/at-jam/nag_test.go`.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/wakeon/ ./cmd/at-jam/ -run 'EndRequested|Idle|WaitMax|NotifyEnded'`
Expected: compile errors (`SetTurnEnd`, `NotifyEnded` undefined).

- [ ] **Step 3: Implement**

`wakeon.go`: add the two interfaces, fields `turnEnd TurnEndState; ender Ender` (and reuse `e.roles` — `SetTurnEnd` sets it if non-nil, without disturbing `SetIdleLadder`), `SetTurnEnd`, and helpers:

```go
// idleAction is the role's on-idle action for inst (wake when unknown).
func (e *Engine) idleAction(inst jam.Instance) string {
	if e.roles != nil {
		if r, ok := e.roles.GetRole(inst.Project, inst.Role); ok {
			return r.TurnEnd.Action()
		}
	}
	return jam.OnIdleWake
}

func (e *Engine) idleDue(inst jam.Instance) bool {
	return e.turnEnd != nil && !inst.IdleDeadline.IsZero() && !e.now().Before(inst.IdleDeadline)
}

func (e *Engine) clearIdle(inst jam.Instance) {
	if e.turnEnd == nil || inst.IdleDeadline.IsZero() {
		return
	}
	if err := e.turnEnd.ClearIdleDeadline(inst.ActorID); err != nil {
		e.log.Warn("wakeon: clear idle deadline failed", "actor", inst.ActorID, "error", err.Error())
	}
}

// endSession tears down a Waiting session that asked to end, then tells its
// owner (best-effort). A failed teardown is retried next tick.
func (e *Engine) endSession(ctx context.Context, inst jam.Instance) {
	e.log.Info("wakeon: session ended itself", "actor", inst.ActorID, "reason", inst.EndRequested.Reason)
	if err := e.reap.Teardown(ctx, inst.ActorID); err != nil {
		e.log.Warn("wakeon: teardown (end) failed; retrying next tick", "actor", inst.ActorID, "error", err.Error())
		return
	}
	if e.ender != nil {
		if err := e.ender.NotifyEnded(ctx, inst, inst.EndRequested.Reason); err != nil {
			e.log.Warn("wakeon: end notice failed", "actor", inst.ActorID, "error", err.Error())
		}
	}
}
```

Restructure `tick` per the Behavior list above; `wakeRunning` returns `bool` (true when it sent a Wake). Enforcing `end` requires `e.turnEnd != nil` (i.e. `SetTurnEnd` called): guard step 1 with it so engines without the hook behave exactly as before. Update the package doc comment to mention the idle deadline and `end`.

`cmd/at-jam/nag.go`:

```go
// NotifyEnded tells a personal session's owner that it ended itself. A
// session with no owner (standing, ticket) gets no notice: wake-on logs it.
func (n intercomNagger) NotifyEnded(_ context.Context, inst jam.Instance, reason string) error {
	if inst.Owner == "" {
		return nil
	}
	return n.send(inst, "", fmt.Sprintf("Your personal session %s (%s) ended itself: %s", inst.ActorID, inst.Role, reason))
}
```

`cmd/at-jam/main.go`, after `eng.SetRunningWake(sup)`:

```go
	// Turn-end: enforce end(reason) and the role's idle timeout.
	eng.SetTurnEnd(st /*RoleLookup*/, sup /*TurnEndState*/, nagger /*Ender*/)
```

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/wakeon/ ./internal/jam/... ./cmd/at-jam/`
Expected: PASS, including every pre-existing wake-on test (engines built without `SetTurnEnd` are unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/wakeon cmd/at-jam
git commit -m "feat(wakeon): enforce end and the idle deadline; wait-max as a backstop"
```

---

### Task 6: Slice-1 deferred minors + docs

**Files:**
- Modify: `internal/agentrun/workload.go` (`episode`'s `holdC` branch), `internal/agentrun/episode_test.go`
- Docs: `docs/usage/jam/turn-end.md`, `docs/usage/jam/roster.md`, `docs/usage/jam/intercom.md`, `docs/usage/jam/coves.md`, `docs/usage/jam/INDEX.md` (row text if `turn-end.md`'s `read_when` changes)

- [ ] **Step 1: Write the failing tests** (`internal/agentrun/episode_test.go`)

```go
// A background task completing starts a self-turn: Holding → Running.
func TestEpisodeBackgroundSelfTurnReportsRunning(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnStarted, lnResult)
	if !eventually(func() bool { return h.count(covemaster.Holding) == 1 }) {
		t.Fatal("no Holding")
	}
	before := h.count(covemaster.Running)
	p.emit(lnTasks0, lnUpdated)
	p.emit(lnNotify) // the completion starts a self-turn
	if !eventually(func() bool { return h.count(covemaster.Running) == before+1 }) {
		t.Fatalf("self-turn did not report Running (running=%d, before=%d)", h.count(covemaster.Running), before)
	}
	cancel()
	<-done
}

// The BackgroundWait cap ends the hold: the agent is told to stop its tasks,
// so the session is busy (Running) until the episode exits, not Holding.
func TestEpisodeBackgroundWaitCapLeavesHolding(t *testing.T) {
	dir := t.TempDir()
	s := newStreamSpawner()
	w := streamWL(t, dir, s, func(c *Config) { c.Resident = true; c.BackgroundWait = 30 * time.Millisecond })
	h := &recordHandle{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runAsync(ctx, w, h)
	p := s.next(t)
	p.in.next(t)
	p.emit(lnInit, lnTasks1, lnResult)
	p.in.waitClosed(t) // the cap closed stdin
	if !eventually(func() bool { return h.count(covemaster.Running) >= 2 }) {
		t.Fatalf("cap did not report Running (running=%d)", h.count(covemaster.Running))
	}
	cancel()
	<-done
}
```

(If `lnTasks0`, `lnUpdated`, `lnNotify`, `lnStarted` differ in name, use the constants `TestEpisodeHoldsStdinUntilBackgroundTasksDone` uses. If `TestEpisodeBackgroundSelfTurnReportsRunning` passes already — `actWait` already calls `unhold()` — keep it as a regression guard and ledger that it did not go RED.)

- [ ] **Step 2: Run to verify**

Run: `go test ./internal/agentrun/ -run 'SelfTurn|CapLeavesHolding'`
Expected: `TestEpisodeBackgroundWaitCapLeavesHolding` FAILS (no Running after the cap).

- [ ] **Step 3: Implement** — in `episode`'s `case <-holdC:` branch, after `closeInput("background-wait elapsed")`, call `unhold()`.

- [ ] **Step 4: Run** `go test ./internal/agentrun/` — PASS.

- [ ] **Step 5: Docs** (docs-author skill)

- `turn-end.md`: update the opening paragraph (end and the idle timeout are now live; alarms/gates/report still later). Add **## Ending a session** (`end(reason)`: takes effect at turn end — a holding session first finishes its background tasks; never woken again once requested; the owner of a personal session gets a notice; standing/ticket sessions: logged; ticket-state transition arrives with `report`) and **## Idle timeout** (role `--idle-timeout`/`--on-idle`, default `wake`; armed when a turn ends, from the override or the role; `idle_timeout(duration|off, next|always)`; `wake` wakes with the idle reason (resuming a paused session first); `teardown` ends a waiting session, never a holding one; a squawk wake disarms it; `wait-max` remains only as a backstop for non-resident sessions with no deadline armed). Bump `updated`; widen `owns`/`read_when`/`summary` to cover `end` and the idle timeout and mirror `read_when` into `docs/usage/jam/INDEX.md`.
- `roster.md`: document `role add --idle-timeout --on-idle` beside `--idle-after`, linking `turn-end.md#idle-timeout` for semantics (no restating).
- `intercom.md`: in "What the tools do", add one bullet linking `end` and `idle_timeout` to `turn-end.md`; in the wake-on section, where `wait-max` teardown is described, add "unless the role's idle timeout is armed — see turn-end.md#idle-timeout".
- `coves.md`: the line saying non-personal studios are torn down at `wait-max` gets the same qualifier by link.
- Run `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep -v superpowers/` — no new errors in touched docs.

- [ ] **Step 6: Full verification**

Run: `go build ./... && go test -count=1 ./... && just lint`
Expected: all PASS.

- [ ] **Step 7: Commit and open the PR**

```bash
git add internal/agentrun docs
git commit -m "fix(agentrun): leave holding when the background-wait cap fires; docs for end and idle timeout"
```

PR title: `feat(jam): turn-end slice 2 — end + idle timeout`. Body: what changed, the five design rulings above, the Review Focus items with their tests.
