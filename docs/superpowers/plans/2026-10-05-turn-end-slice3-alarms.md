# Turn-end lifecycle — Slice 3: alarms (no gates) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An agent can set any number of named alarms — one-shot (`at` an RFC 3339 time) or recurring (5-field cron in the role's time zone) — that wake it with the alarm's note once its turn has ended.

**Architecture:** Alarms live on the `Instance` (`[]Alarm`), managed through three cove endpoints (`PUT`/`DELETE /alarms/{name}`, `GET /alarms`) and three MCP tools. The supervisor validates and schedules (`NextAt`, via `robfig/cron/v3` in the role's `TurnEnd.TimeZone`). The wake-on engine fires due alarms only for `holding`/`waiting` sessions (held while `running`): `Supervisor.FireAlarms` stamps `FiredAt` and advances cron `NextAt`; the engine sends one Wake carrying every fired alarm's reason (plus `squawk` if a reply is pending) and **re-sends it each tick until the session runs** — `Supervisor.Report` on turn start then deletes fired one-shots and clears `FiredAt` (the same delivery guarantee slice 2 gave the idle wake).

**Tech Stack:** Go; `github.com/robfig/cron/v3` (new dependency, v3.0.1, available via proxy.golang.org); `time/tzdata` embedded in `at-jam`.

**Spec:** `docs/superpowers/specs/2026-10-05-turn-end-lifecycle-design.md` — §8 slice 3 ("alarms without gates"): §1 `alarm_*` rows and limits, §2 `Alarms`, §3 "Due alarms" minus gates, "While running", Wake reasons.

## Global Constraints

- **No changes** under `internal/dispatch/**`, `internal/dispatchrun`, `cmd/at-task`. **No gates** (slice 4): no `Gate` field, no `RunGate`.
- Tools (exact): `alarm_set(name, schedule, note?)`, `alarm_clear(name)`, `alarm_list()`. Endpoints: `PUT /alarms/{name}` body `{"schedule","note"}`; `DELETE /alarms/{name}`; `GET /alarms` → `{"alarms":[{"name","schedule","note","next_at","fired"}]}`.
- Limits (spec §1): ≤ 20 alarms per session; recurring no more often than once a minute; note ≤ 1000 bytes. Name: `^[a-z0-9][a-z0-9_-]{0,63}$`. A one-shot time must be in the future and ≤ 366 days out.
- Cron: `cron.ParseStandard` (5 fields, or descriptors like `@daily`); `@every D` allowed only if `D ≥ 1m`. Evaluated in the role's `TurnEnd.TimeZone` (IANA name; `""` = UTC).
- No catch-up: a cron alarm whose matches were missed fires once.
- Wake reason: `jam.WakeAlarm` with `Alarm` = name, `Note` = note (renders `Alarm "<name>" fired: <note>` — slice 1).
- Tests hermetic; TDD; `export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local`.
- Docs updated in the same change.

## Design rulings (deviations or refinements of the spec)

1. **`FiredAt` until the session runs**, not "delete on fire": a Wake to a cove whose stream hasn't reconnected is dropped silently, so a fired alarm stays pending (and its Wake is re-sent each tick) until `Report` sees the turn start — then one-shots are deleted and cron alarms un-fired. Mirrors slice 2's idle deadline.
2. **One Wake per tick carries all reasons** — a pending reply (`squawk`) and every fired alarm together — instead of the squawk branch waking alone; the idle wake still fires only when nothing else does.
3. **A paused (`idled`) session with a due alarm is fired and resumed in the same tick**, woken on a later tick (the existing resume-then-wake pattern).
4. **Time zone lives on `Role.TurnEnd.TimeZone`** with the slice-2 turn-end policy (flag `--time-zone`, API `time_zone`).

## Review Focus

1. **The Wake for a fired alarm is dropped (no stream)** — it must be re-sent next tick, not lost. (Task 4, `TestTick_FiredAlarmResentUntilRunning`.)
2. **A one-shot alarm, then the session runs** — the alarm is gone afterwards and never fires again; a cron alarm stays with its next `NextAt`. (Task 2, `TestReportTurnStartRetiresFiredAlarms`.)
3. **An alarm due while the session is `running`** — not fired until it holds/waits. (Task 4, `TestTick_AlarmHeldWhileRunning`.)
4. **A cron alarm whose matches were missed for hours** (Jam down) — fires once, `NextAt` lands after now. (Task 2, `TestFireAlarmsNoCatchUp`.)
5. **`@every 10s` / `* * * * * *` (6-field) / a past `at` / a 21st alarm** — all rejected 400 with nothing stored. (Task 3, `TestAlarmsRejects`.)

---

## File Structure

| File | Change |
|------|--------|
| `go.mod`, `go.sum` | `github.com/robfig/cron/v3 v3.0.1`. |
| `internal/jam/alarm.go` (new) | `Alarm`, `ParseSchedule`, `nextFire`, `ValidateAlarmName`, limits. |
| `internal/jam/turnend.go` | `TurnEndPolicy.TimeZone`, `Location()`, validation. |
| `internal/jam/instance.go` | `Alarms []Alarm`. |
| `internal/jam/supervisor.go` | `SetAlarm`, `ClearAlarm`, `FireAlarms`; `Report` turn start retires fired alarms. |
| `internal/jam/alarm_handler.go` (new) | `/alarms` endpoints. |
| `internal/jam/admin.go`, `adminclient`, `adminui/role_detail.go`, `cmd/at-jam/main.go` | `time_zone` / `--time-zone`. |
| `cmd/at-jam/mux.go` | route `/alarms`, `/alarms/{name}`. |
| `cmd/at-jam/main.go` | `import _ "time/tzdata"`; `SetTurnEnd(st, nagger, sup)`. |
| `cmd/cove-master/mcp.go` | `alarm_set`, `alarm_clear`, `alarm_list`. |
| `internal/wakeon/wakeon.go` | fire alarms for holding/waiting; combined reasons; re-send. |
| `internal/jam/turnend_handler.go` | slice-2 minor: trim `/end` reason. |
| Docs | `turn-end.md` §Alarms; `roster.md` (`--time-zone`); `intercom.md` tool bullet. |

---

### Task 1: Schedules, the role time zone, and the `Alarm` type

**Files:**
- Create: `internal/jam/alarm.go`, `internal/jam/alarm_test.go`
- Modify: `go.mod`/`go.sum`, `internal/jam/turnend.go`, `internal/jam/turnend_test.go`, `internal/jam/instance.go`, `internal/jam/admin.go`, `internal/jam/adminclient/adminclient.go`, `internal/jam/adminui/role_detail.go`, `cmd/at-jam/main.go`
- Test: `internal/jam/alarm_test.go`, `internal/jam/turnend_test.go`, `internal/jam/admin_test.go`

**Interfaces:**
- Produces:
  ```go
  const (MaxAlarms = 20; maxAlarmNote = 1000; maxAlarmAhead = 366 * 24 * time.Hour)
  type Alarm struct {
      Name     string    `json:"name"`
      Schedule string    `json:"schedule"`            // RFC 3339 (one-shot) or cron
      Note     string    `json:"note,omitempty"`
      NextAt   time.Time `json:"next_at,omitzero"`    // zero once a one-shot has fired
      FiredAt  time.Time `json:"fired_at,omitzero"`   // fired, awaiting the session's next run
  }
  func (a Alarm) OneShot() bool                                    // Schedule parses as RFC 3339
  func ValidateAlarmName(name string) error
  func ParseSchedule(s string, loc *time.Location, now time.Time) (next time.Time, err error)
      // one-shot: the time itself (must be after now, within maxAlarmAhead);
      // cron: first match after now; rejects >1/minute and 6-field specs
  func NextAfter(a Alarm, loc *time.Location, now time.Time) time.Time // cron: next match after now; one-shot: zero
  // TurnEndPolicy gains TimeZone string `json:"time_zone,omitempty"`
  func (p TurnEndPolicy) Location() *time.Location                  // UTC when "" or unloadable
  // ValidateTurnEnd also rejects an unloadable TimeZone
  // Instance gains Alarms []Alarm `json:"alarms,omitempty"`
  // RoleBody/RoleSummary gain TimeZone string `json:"time_zone,omitempty"`
  ```

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/robfig/cron/v3@v3.0.1 && go mod tidy`
Expected: `go.mod` lists it; `go build ./...` passes.

- [ ] **Step 2: Write the failing tests** (`internal/jam/alarm_test.go`)

```go
package jam

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC) // a Monday

func TestParseScheduleOneShot(t *testing.T) {
	next, err := ParseSchedule("2026-10-05T14:30:00Z", time.UTC, t0)
	if err != nil || !next.Equal(time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC)) {
		t.Fatalf("next=%v err=%v", next, err)
	}
	for _, bad := range []string{"2026-10-05T11:00:00Z", "2028-01-01T00:00:00Z"} { // past; > 366d
		if _, err := ParseSchedule(bad, time.UTC, t0); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseScheduleCronInZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	next, err := ParseSchedule("0 9 * * *", ny, t0) // 12:00Z = 08:00 EDT
	if err != nil || !next.Equal(time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("next=%v err=%v; want 09:00 New York = 13:00Z", next, err)
	}
	if _, err := ParseSchedule("@daily", time.UTC, t0); err != nil {
		t.Fatalf("@daily: %v", err)
	}
}

func TestParseScheduleRejects(t *testing.T) {
	for _, bad := range []string{"", "soon", "@every 10s", "* * * * * *", "61 * * * *"} {
		if _, err := ParseSchedule(bad, time.UTC, t0); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ParseSchedule("@every 5m", time.UTC, t0); err != nil {
		t.Errorf("@every 5m: %v", err)
	}
}

func TestNextAfterNoCatchUp(t *testing.T) {
	a := Alarm{Name: "h", Schedule: "0 * * * *"}
	if got := NextAfter(a, time.UTC, t0.Add(5*time.Hour+time.Minute)); !got.Equal(t0.Add(6 * time.Hour)) {
		t.Fatalf("next = %v, want the first match after now", got)
	}
	if got := NextAfter(Alarm{Schedule: "2026-10-05T14:30:00Z"}, time.UTC, t0); !got.IsZero() {
		t.Fatalf("one-shot next = %v, want zero", got)
	}
}

func TestValidateAlarmName(t *testing.T) {
	for _, ok := range []string{"pr-watch", "nightly_1", "a"} {
		if err := ValidateAlarmName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "PR", "-x", "a b", "a/b", string(make([]byte, 65))} {
		if err := ValidateAlarmName(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
```

Append to `turnend_test.go`:

```go
func TestTurnEndTimeZone(t *testing.T) {
	if (TurnEndPolicy{}).Location() != time.UTC {
		t.Fatal("default location is not UTC")
	}
	if loc := (TurnEndPolicy{TimeZone: "Europe/Berlin"}).Location(); loc.String() != "Europe/Berlin" {
		t.Fatalf("location = %v", loc)
	}
	if err := ValidateTurnEnd(TurnEndPolicy{TimeZone: "Mars/Olympus"}); err == nil {
		t.Fatal("accepted an unknown time zone")
	}
}
```

In `admin_test.go`, extend `TestAdminRoleTurnEndRoundTrip`: POST with `TimeZone: "Europe/Berlin"`, assert `roles[0].TimeZone == "Europe/Berlin"` and the stored `TurnEnd.TimeZone`; add `{Project: "acme", Name: "bad", TimeZone: "Mars/Olympus"}` to the 400 cases.

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./internal/jam/ -run 'Schedule|NextAfter|AlarmName|TimeZone|TurnEndRoundTrip'`
Expected: compile errors (`ParseSchedule`, `TimeZone` undefined).

- [ ] **Step 4: Implement**

`internal/jam/alarm.go`:

```go
package jam

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// Alarm limits (turn-end spec §1).
const (
	MaxAlarms     = 20
	maxAlarmNote  = 1000
	maxAlarmAhead = 366 * 24 * time.Hour
)

var alarmName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Alarm is one of a session's named wake conditions (the alarm_* tools): a
// one-shot RFC 3339 time or a cron schedule in the role's time zone. See
// docs/usage/jam/turn-end.md#alarms.
type Alarm struct {
	Name     string    `json:"name"`
	Schedule string    `json:"schedule"`
	Note     string    `json:"note,omitempty"`
	NextAt   time.Time `json:"next_at,omitzero"`  // zero once a one-shot has fired
	FiredAt  time.Time `json:"fired_at,omitzero"` // fired, awaiting the session's next run
}

// OneShot reports whether the schedule is a single RFC 3339 time.
func (a Alarm) OneShot() bool {
	_, err := time.Parse(time.RFC3339, a.Schedule)
	return err == nil
}

// ValidateAlarmName accepts lowercase letters, digits, '-' and '_' (≤ 64).
func ValidateAlarmName(name string) error {
	if !alarmName.MatchString(name) {
		return fmt.Errorf("alarm name must match %s", alarmName)
	}
	return nil
}

// ParseSchedule validates s and returns its first fire time after now.
func ParseSchedule(s string, loc *time.Location, now time.Time) (time.Time, error) {
	if at, err := time.Parse(time.RFC3339, s); err == nil {
		if !at.After(now) || at.Sub(now) > maxAlarmAhead {
			return time.Time{}, fmt.Errorf("an at time must be in the future and within 366 days")
		}
		return at, nil
	}
	sched, err := parseCron(s)
	if err != nil {
		return time.Time{}, err
	}
	first := sched.Next(now.In(loc))
	if sched.Next(first).Sub(first) < time.Minute {
		return time.Time{}, fmt.Errorf("a recurring alarm may fire at most once a minute")
	}
	return first, nil
}

// NextAfter is a cron alarm's next fire time after now (no catch-up); zero for
// a one-shot.
func NextAfter(a Alarm, loc *time.Location, now time.Time) time.Time {
	if a.OneShot() {
		return time.Time{}
	}
	sched, err := parseCron(a.Schedule)
	if err != nil {
		return time.Time{}
	}
	return sched.Next(now.In(loc))
}

func parseCron(s string) (cron.Schedule, error) {
	if len(strings.Fields(s)) == 6 {
		return nil, fmt.Errorf("schedule must be an RFC 3339 time or a 5-field cron expression")
	}
	sched, err := cron.ParseStandard(s)
	if err != nil {
		return nil, fmt.Errorf("schedule must be an RFC 3339 time or a 5-field cron expression: %v", err)
	}
	return sched, nil
}
```

(Returned times are compared with `.Equal`; `sched.Next` returns in `loc` — fine.)

`turnend.go`: add `TimeZone string \`json:"time_zone,omitempty"\`` to `TurnEndPolicy`, plus:

```go
// Location is the zone cron alarms are evaluated in: TimeZone, or UTC.
func (p TurnEndPolicy) Location() *time.Location {
	if p.TimeZone == "" {
		return time.UTC
	}
	if loc, err := time.LoadLocation(p.TimeZone); err == nil {
		return loc
	}
	return time.UTC
}
```

and in `ValidateTurnEnd`, before the `OnIdle` switch: `if p.TimeZone != "" { if _, err := time.LoadLocation(p.TimeZone); err != nil { return fmt.Errorf("unknown time zone %q", p.TimeZone) } }`.

`instance.go`: `Alarms []Alarm \`json:"alarms,omitempty"\` // the cove's alarms (alarm_* tools)`.

Plumbing (same places as slice 2's `IdleTimeoutSeconds`/`OnIdle`): `RoleBody`/`RoleSummary` `TimeZone string \`json:"time_zone,omitempty"\``; admin GET/POST mapping (`te.TimeZone = b.TimeZone` before `ValidateTurnEnd`); adminclient both directions; `at-jam role add --time-zone` (`"IANA zone cron alarms are evaluated in (default UTC)"`) and `role list` `\ttime-zone=%s` (print `UTC` when empty); `role_detail.go` row `{Label: "Alarm time zone", Value: …}` (`"UTC"` when empty). `cmd/at-jam/main.go`: `import _ "time/tzdata" // IANA zones for role time zones, whatever the image ships`.

- [ ] **Step 5: Run to verify they pass**

Run: `go build ./... && go test ./internal/jam/... ./cmd/at-jam/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/jam cmd/at-jam
git commit -m "feat(jam): alarm schedules (one-shot and cron) and the role time zone"
```

---

### Task 2: Supervisor — set, clear, fire, retire

**Files:**
- Modify: `internal/jam/supervisor.go` (`Report` turn start; new methods after `SetIdleOverride`)
- Test: `internal/jam/supervisor_test.go`

**Interfaces:**
- Consumes: Task 1.
- Produces:
  ```go
  var ErrAlarmLimit = errors.New("alarm limit reached")      // in alarm.go
  var ErrNoSuchAlarm = errors.New("no such alarm")           // in alarm.go
  func (s *Supervisor) SetAlarm(actorID, name, schedule, note string) (Alarm, error)
      // validates name/note/schedule (role zone), replaces by name or appends (≤ MaxAlarms)
  func (s *Supervisor) ClearAlarm(actorID, name string) error   // ErrNoSuchAlarm if absent
  func (s *Supervisor) FireAlarms(actorID string, now time.Time) ([]Alarm, error)
      // for each alarm with !NextAt.IsZero() && !NextAt.After(now): FiredAt = now (if not
      // already fired) and NextAt = NextAfter(...) (zero for a one-shot); returns the
      // instance's alarms after the update
  // Report: on turn start, delete fired one-shots and clear FiredAt on the rest
  ```
  Validation errors from `SetAlarm` are plain `error`s (the handler maps them to 400); `ErrAlarmLimit` → 400 too.

- [ ] **Step 1: Write the failing tests**

```go
func TestSetAlarmSchedulesInRoleZone(t *testing.T) {
	sup, store, now := raiseWithTurnEnd(t, TurnEndPolicy{TimeZone: "America/New_York"})
	*now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	a, err := sup.SetAlarm("w1", "standup", "0 9 * * *", "post the standup")
	if err != nil {
		t.Fatal(err)
	}
	if !a.NextAt.Equal(time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("NextAt = %v", a.NextAt)
	}
	if inst, _ := store.GetInstance("w1"); len(inst.Alarms) != 1 || inst.Alarms[0].Note != "post the standup" {
		t.Fatalf("alarms = %+v", inst.Alarms)
	}
	// same name replaces
	if _, err := sup.SetAlarm("w1", "standup", "0 10 * * *", ""); err != nil {
		t.Fatal(err)
	}
	if inst, _ := store.GetInstance("w1"); len(inst.Alarms) != 1 || inst.Alarms[0].Schedule != "0 10 * * *" {
		t.Fatalf("replace: alarms = %+v", inst.Alarms)
	}
}

func TestSetAlarmLimits(t *testing.T) {
	sup, store, _ := raiseWithTurnEnd(t, TurnEndPolicy{})
	for i := 0; i < MaxAlarms; i++ {
		if _, err := sup.SetAlarm("w1", fmt.Sprintf("a%d", i), "@hourly", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sup.SetAlarm("w1", "one-more", "@hourly", ""); !errors.Is(err, ErrAlarmLimit) {
		t.Fatalf("21st alarm: err = %v, want ErrAlarmLimit", err)
	}
	for _, c := range [][3]string{{"BAD", "@hourly", ""}, {"ok", "@every 1s", ""}, {"ok", "@hourly", strings.Repeat("x", 1001)}} {
		if _, err := sup.SetAlarm("w1", c[0], c[1], c[2]); err == nil {
			t.Errorf("accepted %q", c)
		}
	}
	if inst, _ := store.GetInstance("w1"); len(inst.Alarms) != MaxAlarms {
		t.Fatalf("rejected sets changed the alarms: %d", len(inst.Alarms))
	}
}

func TestClearAlarm(t *testing.T) {
	sup, store, _ := raiseWithTurnEnd(t, TurnEndPolicy{})
	_, _ = sup.SetAlarm("w1", "x", "@hourly", "")
	if err := sup.ClearAlarm("w1", "x"); err != nil {
		t.Fatal(err)
	}
	if err := sup.ClearAlarm("w1", "x"); !errors.Is(err, ErrNoSuchAlarm) {
		t.Fatalf("second clear: %v", err)
	}
	if inst, _ := store.GetInstance("w1"); len(inst.Alarms) != 0 {
		t.Fatalf("alarms = %+v", inst.Alarms)
	}
}

func TestFireAlarmsNoCatchUp(t *testing.T) {
	sup, _, now := raiseWithTurnEnd(t, TurnEndPolicy{})
	*now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	_, _ = sup.SetAlarm("w1", "hourly", "0 * * * *", "")
	_, _ = sup.SetAlarm("w1", "once", "2026-10-05T12:30:00Z", "")
	_, _ = sup.SetAlarm("w1", "later", "2026-10-06T00:00:00Z", "")
	at := time.Date(2026, 10, 5, 17, 5, 0, 0, time.UTC) // 5 hourly matches missed
	got, err := sup.FireAlarms("w1", at)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Alarm{}
	for _, a := range got {
		byName[a.Name] = a
	}
	if h := byName["hourly"]; !h.FiredAt.Equal(at) || !h.NextAt.Equal(time.Date(2026, 10, 5, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("hourly = %+v", h)
	}
	if o := byName["once"]; !o.FiredAt.Equal(at) || !o.NextAt.IsZero() {
		t.Fatalf("once = %+v", o)
	}
	if l := byName["later"]; !l.FiredAt.IsZero() {
		t.Fatalf("later fired early: %+v", l)
	}
	// firing again while still pending keeps the first FiredAt
	got, _ = sup.FireAlarms("w1", at.Add(time.Hour))
	for _, a := range got {
		if a.Name == "hourly" && !a.FiredAt.Equal(at) {
			t.Fatalf("FiredAt moved to %v", a.FiredAt)
		}
	}
}

func TestReportTurnStartRetiresFiredAlarms(t *testing.T) {
	sup, store, now := raiseWithTurnEnd(t, TurnEndPolicy{})
	*now = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	_, _ = sup.SetAlarm("w1", "hourly", "0 * * * *", "")
	_, _ = sup.SetAlarm("w1", "once", "2026-10-05T12:30:00Z", "")
	_ = sup.Report(context.Background(), "w1", ActivityWaiting)
	_, _ = sup.FireAlarms("w1", time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC))
	_ = sup.Report(context.Background(), "w1", ActivityRunning)
	inst, _ := store.GetInstance("w1")
	if len(inst.Alarms) != 1 || inst.Alarms[0].Name != "hourly" || !inst.Alarms[0].FiredAt.IsZero() {
		t.Fatalf("alarms after the turn started = %+v; want hourly only, un-fired", inst.Alarms)
	}
}
```

(Add `errors`, `fmt`, `strings` imports to the test file if missing.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/jam/ -run 'Alarm'`
Expected: compile errors (`SetAlarm` undefined).

- [ ] **Step 3: Implement** — in `alarm.go` add the two sentinel errors. In `supervisor.go`:

```go
// SetAlarm validates and schedules the named alarm on the cove (replacing one
// of the same name), in its role's time zone.
func (s *Supervisor) SetAlarm(actorID, name, schedule, note string) (Alarm, error) {
	if err := ValidateAlarmName(name); err != nil {
		return Alarm{}, err
	}
	if len(note) > maxAlarmNote {
		return Alarm{}, fmt.Errorf("note must be at most %d bytes", maxAlarmNote)
	}
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return Alarm{}, fmt.Errorf("no instance for actor %q", actorID)
	}
	next, err := ParseSchedule(schedule, s.alarmZone(inst), s.now())
	if err != nil {
		return Alarm{}, err
	}
	a := Alarm{Name: name, Schedule: schedule, Note: note, NextAt: next}
	alarms := slices.Clone(inst.Alarms)
	if i := slices.IndexFunc(alarms, func(x Alarm) bool { return x.Name == name }); i >= 0 {
		alarms[i] = a
	} else if len(alarms) >= MaxAlarms {
		return Alarm{}, ErrAlarmLimit
	} else {
		alarms = append(alarms, a)
	}
	inst.Alarms = alarms
	return a, s.store.PutInstance(inst)
}

// ClearAlarm removes the named alarm (ErrNoSuchAlarm if absent).
func (s *Supervisor) ClearAlarm(actorID, name string) error {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return fmt.Errorf("no instance for actor %q", actorID)
	}
	i := slices.IndexFunc(inst.Alarms, func(x Alarm) bool { return x.Name == name })
	if i < 0 {
		return ErrNoSuchAlarm
	}
	inst.Alarms = slices.Delete(slices.Clone(inst.Alarms), i, i+1)
	return s.store.PutInstance(inst)
}

// FireAlarms marks the cove's due alarms fired (keeping an earlier FiredAt)
// and moves each cron alarm to its next match after now (no catch-up); a
// one-shot's NextAt goes zero. It returns the alarms after the update.
func (s *Supervisor) FireAlarms(actorID string, now time.Time) ([]Alarm, error) {
	inst, ok := s.store.GetInstance(actorID)
	if !ok {
		return nil, fmt.Errorf("no instance for actor %q", actorID)
	}
	alarms := slices.Clone(inst.Alarms)
	changed := false
	for i, a := range alarms {
		if a.NextAt.IsZero() || a.NextAt.After(now) {
			continue
		}
		if a.FiredAt.IsZero() {
			a.FiredAt = now
		}
		a.NextAt = NextAfter(a, s.alarmZone(inst), now)
		alarms[i], changed = a, true
	}
	if !changed {
		return alarms, nil
	}
	inst.Alarms = alarms
	return alarms, s.store.PutInstance(inst)
}

// alarmZone is the cove's role time zone (UTC when the role is gone).
func (s *Supervisor) alarmZone(inst Instance) *time.Location {
	if role, ok := s.store.GetRole(inst.Project, inst.Role); ok {
		return role.TurnEnd.Location()
	}
	return time.UTC
}
```

In `Report`'s `if turnStarted {` block add:

```go
		// Fired alarms were answered by this turn: retire one-shots, re-arm the rest.
		var kept []Alarm
		for _, al := range inst.Alarms {
			if al.FiredAt.IsZero() {
				kept = append(kept, al)
			} else if !al.OneShot() {
				al.FiredAt = time.Time{}
				kept = append(kept, al)
			}
		}
		inst.Alarms = kept
```

(Imports: `slices`.)

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/jam/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam
git commit -m "feat(jam): set, clear and fire session alarms; retire fired ones when the session runs"
```

---

### Task 3: `/alarms` endpoints (+ trim the `/end` reason)

**Files:**
- Create: `internal/jam/alarm_handler.go`, `internal/jam/alarm_handler_test.go`
- Modify: `cmd/at-jam/mux.go`, `cmd/at-jam/mux_test.go`, `internal/jam/turnend_handler.go`, `internal/jam/turnend_handler_test.go`

**Interfaces:**
- Consumes: `SetAlarm`, `ClearAlarm`, `ErrAlarmLimit`, `ErrNoSuchAlarm` (Task 2); `escalateStore`, `TurnEndHandler.authenticate` pattern.
- Produces:
  ```go
  type alarmSetter interface {
      SetAlarm(actorID, name, schedule, note string) (Alarm, error)
      ClearAlarm(actorID, name string) error
  }
  func NewAlarmHandler(store escalateStore, setter alarmSetter, now func() time.Time, log *slog.Logger) *AlarmHandler
  // GET /alarms → 200 {"alarms":[{"name","schedule","note","next_at","fired"}]} (next_at RFC 3339 or omitted)
  // PUT /alarms/{name} {"schedule","note"} → 200 with the alarm; validation/limit → 400
  // DELETE /alarms/{name} → 204; absent → 404
  ```
  GET reads the caller's instance from `store.GetInstance` (self-scoped). 401/403/405/413 exactly as `/end`.

- [ ] **Step 1: Write the failing tests** — `alarm_handler_test.go`, following `turnend_handler_test.go` (reuse `newFakeEscStore`, `HashToken`, `testLogger`, `doTurnEnd`):

```go
type fakeAlarmSetter struct {
	set      []string // "name|schedule|note"
	cleared  []string
	setErr   error
	clearErr error
}

func (f *fakeAlarmSetter) SetAlarm(_, n, s, note string) (Alarm, error) {
	if f.setErr != nil {
		return Alarm{}, f.setErr
	}
	f.set = append(f.set, n+"|"+s+"|"+note)
	return Alarm{Name: n, Schedule: s, Note: note, NextAt: time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeAlarmSetter) ClearAlarm(_, n string) error { f.cleared = append(f.cleared, n); return f.clearErr }

func alarmFixture() (*AlarmHandler, *fakeAlarmSetter, *fakeEscStore) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-1"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Alarms: []Alarm{{Name: "nightly", Schedule: "0 2 * * *", Note: "backup", NextAt: time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)}}}
	set := &fakeAlarmSetter{}
	return NewAlarmHandler(st, set, time.Now, testLogger()), set, st
}

func TestAlarmsList(t *testing.T) {
	h, _, _ := alarmFixture()
	rec := doTurnEnd(h, "GET", "/alarms", "tok", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"nightly"`) || !strings.Contains(rec.Body.String(), `"next_at":"2026-10-06T02:00:00Z"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestAlarmsSetAndClear(t *testing.T) {
	h, set, _ := alarmFixture()
	if rec := doTurnEnd(h, "PUT", "/alarms/pr-watch", "tok", `{"schedule":"*/5 * * * *","note":"check the PR"}`); rec.Code != 200 {
		t.Fatalf("PUT %d %s", rec.Code, rec.Body)
	}
	if len(set.set) != 1 || set.set[0] != "pr-watch|*/5 * * * *|check the PR" {
		t.Fatalf("set = %v", set.set)
	}
	if rec := doTurnEnd(h, "DELETE", "/alarms/pr-watch", "tok", ""); rec.Code != 204 {
		t.Fatalf("DELETE %d", rec.Code)
	}
	set.clearErr = ErrNoSuchAlarm
	if rec := doTurnEnd(h, "DELETE", "/alarms/gone", "tok", ""); rec.Code != 404 {
		t.Fatalf("DELETE absent %d, want 404", rec.Code)
	}
}

func TestAlarmsRejects(t *testing.T) {
	h, set, _ := alarmFixture()
	set.setErr = ErrAlarmLimit
	if rec := doTurnEnd(h, "PUT", "/alarms/x", "tok", `{"schedule":"@hourly"}`); rec.Code != 400 {
		t.Fatalf("limit → %d, want 400", rec.Code)
	}
	set.setErr = errors.New("a recurring alarm may fire at most once a minute")
	if rec := doTurnEnd(h, "PUT", "/alarms/x", "tok", `{"schedule":"@every 10s"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "once a minute") {
		t.Fatalf("invalid → %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ m, p, tok string; want int }{
		{"GET", "/alarms", "", 401}, {"POST", "/alarms", "tok", 405}, {"PATCH", "/alarms/x", "tok", 405}, {"PUT", "/alarms/", "tok", 404},
	} {
		if rec := doTurnEnd(h, c.m, c.p, c.tok, `{}`); rec.Code != c.want {
			t.Errorf("%s %s → %d, want %d", c.m, c.p, rec.Code, c.want)
		}
	}
}
```

Also an end-to-end validation test through the real supervisor (`TestAlarmsRejectsThroughSupervisor`): build `NewAlarmHandler(store, sup, …)` over `raiseWithTurnEnd`'s store/supervisor with an actor whose token maps to `w1` (mirror how `supTestKit`'s raise returns the token — `sup.Raise` returns the token as its second result), then PUT `@every 10s`, `* * * * * *`, a past `at`, and a 21st alarm, asserting 400 each and `len(inst.Alarms)` unchanged.

`turnend_handler_test.go`: add `{"POST", "tok", `{"reason":"   "}`, 400}` to `TestEndRejects`.

`mux_test.go` `TestSquawksMuxRouting`: add an `alarmH` writing `"alarms"`, pass it, and cases `{"/alarms", "alarms"}`, `{"/alarms/pr-watch", "alarms"}`, `{"/alarmsx", "broker"}`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/jam/ ./cmd/at-jam/ -run 'Alarms|EndRejects|MuxRouting'`
Expected: compile errors / the whitespace-reason case → 204.

- [ ] **Step 3: Implement**

`alarm_handler.go`: same auth helper shape as `TurnEndHandler.authenticate` (copy it as a package-level `authenticateCove(w, r, store) (Actor, bool)` and make `TurnEndHandler.authenticate` call it — a two-line refactor inside this package). Route: `r.URL.Path == "/alarms"` → GET only (`Allow: GET`); `strings.CutPrefix(r.URL.Path, "/alarms/")` with a non-empty name containing no `/` → PUT or DELETE (`Allow: PUT, DELETE`); anything else → 404. PUT body capped at 2048 bytes; `ErrAlarmLimit` and any other `SetAlarm` error → 400 with `err.Error()`, except a "no instance" error → 403 is unnecessary (authenticate already checked). Response JSON:

```go
type alarmView struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Note     string `json:"note,omitempty"`
	NextAt   string `json:"next_at,omitempty"` // RFC 3339; empty once a one-shot fired
	Fired    bool   `json:"fired,omitempty"`   // fired, awaiting the session's next run
}
```

`turnend_handler.go`: `req.Reason = strings.TrimSpace(req.Reason)` before the empty check.

`cmd/at-jam/mux.go`: `squawksMux(squawksH, escH, turnEndH, alarmH, broker)`; route `case r.URL.Path == "/alarms" || strings.HasPrefix(r.URL.Path, "/alarms/"): alarmH.ServeHTTP` (convert the `switch r.URL.Path` to a tagless `switch` or add the prefix check before it); build `jam.NewAlarmHandler(st, sup, time.Now, log)` in `coveHTTPHandler`; log `Jam alarms: mounted`.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/jam/ ./cmd/at-jam/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/jam cmd/at-jam
git commit -m "feat(jam): /alarms cove endpoints; trim the end reason"
```

---

### Task 4: Wake-on fires alarms

**Files:**
- Modify: `internal/wakeon/wakeon.go` (interface `AlarmFirer`; `SetTurnEnd(roles, ender, alarms)`; tick), `cmd/at-jam/main.go` (wiring)
- Test: `internal/wakeon/wakeon_test.go`

**Interfaces:**
- Consumes: `Supervisor.FireAlarms`, `jam.Alarm`, `jam.WakeAlarm`.
- Produces:
  ```go
  type AlarmFirer interface {
      FireAlarms(actorID string, now time.Time) ([]jam.Alarm, error)
  }
  func (e *Engine) SetTurnEnd(roles RoleLookup, ender Ender, alarms AlarmFirer) // alarms may be nil
  ```

Behavior:
- `alarmReasons(inst) []jam.WakeReason`: if `e.alarms == nil` → nil. If any alarm in `inst.Alarms` is due (`!NextAt.IsZero() && !NextAt.After(now)`), call `FireAlarms(inst.ActorID, now)` and use its result (on error: log and use `inst.Alarms`); then one `WakeReason{Kind: jam.WakeAlarm, Alarm: a.Name, Note: a.Note}` per alarm with `!FiredAt.IsZero()`.
- **Running**: unchanged (squawk wake only; alarms held — `alarmReasons` is not called).
- **Holding**: `extra := e.alarmReasons(inst)`; `woke := e.wakeRunning(inst, extra)` (now: on replies → `Wake(squawk, extra...)`; else if `len(extra) > 0` → `Wake(extra...)`; returns whether it sent); idle as before only when `!woke`.
- **Waiting** (after the wait-max check): `alarms := e.alarmReasons(inst)` (fires even when idled). Replies branch: the personal command check as before; `reasons := append([]WakeReason{{Kind: WakeSquawk}}, alarms...)`. Else if `len(alarms) > 0`: `reasons = alarms`. If `reasons != nil`: `Idled` → `Resume`, `continue`; else `Wake(reasons...)`, `continue`. Then the idle deadline, ladder, warm-timeout pause as before.
- Wakes are re-sent each tick while alarms stay fired (until `Report` retires them on turn start).

- [ ] **Step 1: Write the failing tests** (extend `turnEndEngine` with a `fakeAlarms` that applies the same rule against the registry so ticks see the update):

```go
// fakeAlarms mirrors Supervisor.FireAlarms on the registry's instances.
type fakeAlarms struct{ reg *fakeReg }

func (f *fakeAlarms) FireAlarms(id string, now time.Time) ([]jam.Alarm, error) {
	for i := range f.reg.insts {
		if f.reg.insts[i].ActorID != id {
			continue
		}
		for j, a := range f.reg.insts[i].Alarms {
			if !a.NextAt.IsZero() && !a.NextAt.After(now) {
				if a.FiredAt.IsZero() {
					a.FiredAt = now
				}
				a.NextAt = jam.NextAfter(a, time.UTC, now)
				f.reg.insts[i].Alarms[j] = a
			}
		}
		return f.reg.insts[i].Alarms, nil
	}
	return nil, nil
}
```

Change `turnEndEngine` to `e.SetTurnEnd(roles, ender, &fakeAlarms{reg: reg})`. Tests:

```go
var due = time.Unix(9999, 0) // before turnEndEngine's now (10000)

func TestTick_AlarmWakesWaiting(t *testing.T) {
	e, wake, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0),
		Alarms: []jam.Alarm{{Name: "nightly", Schedule: "1970-01-01T02:46:39Z", Note: "backup", NextAt: due}}}}, nil, nil)
	e.tick(context.Background())
	if len(wake.woke) != 1 || len(wake.reasons[0]) != 1 || wake.reasons[0][0] != (jam.WakeReason{Kind: jam.WakeAlarm, Alarm: "nightly", Note: "backup"}) {
		t.Fatalf("woke=%v reasons=%v", wake.woke, wake.reasons)
	}
}

func TestTick_FiredAlarmResentUntilRunning(t *testing.T) {
	e, wake, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0),
		Alarms: []jam.Alarm{{Name: "x", Schedule: "1970-01-01T02:46:39Z", NextAt: due}}}}, nil, nil)
	e.tick(context.Background())
	e.tick(context.Background())
	if len(wake.woke) != 2 {
		t.Fatalf("woke=%v; want the alarm wake re-sent while still waiting", wake.woke)
	}
}

func TestTick_AlarmHeldWhileRunning(t *testing.T) {
	e, wake, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityRunning,
		Alarms: []jam.Alarm{{Name: "x", Schedule: "@hourly", NextAt: due}}}}, nil, nil)
	e.tick(context.Background())
	if len(wake.woke) != 0 || !e.reg.(*fakeReg).insts[0].Alarms[0].FiredAt.IsZero() {
		t.Fatalf("fired while running: woke=%v alarms=%+v", wake.woke, e.reg.(*fakeReg).insts[0].Alarms)
	}
}

func TestTick_AlarmAndSquawkOneWake(t *testing.T) {
	inbox := &fakeInbox{byActor: map[string][]intercom.Squawk{"a1": {extInbound("a1", 6, "id-6")}}}
	e, wake, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitSeq: 5, WaitingSince: time.Unix(9000, 0),
		Alarms: []jam.Alarm{{Name: "x", Note: "n", Schedule: "@hourly", NextAt: due}}}}, inbox, nil)
	e.tick(context.Background())
	if len(wake.woke) != 1 || len(wake.reasons[0]) != 2 || wake.reasons[0][0].Kind != jam.WakeSquawk || wake.reasons[0][1].Alarm != "x" {
		t.Fatalf("woke=%v reasons=%v; want one wake with squawk then the alarm", wake.woke, wake.reasons)
	}
}

func TestTick_AlarmWakesHolding(t *testing.T) {
	e, wake, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityHolding,
		Alarms: []jam.Alarm{{Name: "x", Schedule: "@hourly", NextAt: due}}}}, nil, nil)
	e.tick(context.Background())
	if len(wake.woke) != 1 || wake.reasons[0][0].Kind != jam.WakeAlarm {
		t.Fatalf("woke=%v reasons=%v", wake.woke, wake.reasons)
	}
}

func TestTick_AlarmResumesPausedThenWakes(t *testing.T) {
	e, wake, _, idler, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0),
		Alarms: []jam.Alarm{{Name: "x", Schedule: "@hourly", NextAt: due}}}}, nil, nil)
	e.tick(context.Background())
	if len(idler.resumed) != 1 || len(wake.woke) != 0 {
		t.Fatalf("resumed=%v woke=%v", idler.resumed, wake.woke)
	}
	e.reg.(*fakeReg).insts[0].Phase = jam.PhaseLive
	e.tick(context.Background())
	if len(wake.woke) != 1 || wake.reasons[0][0].Alarm != "x" {
		t.Fatalf("after resume: woke=%v reasons=%v", wake.woke, wake.reasons)
	}
}

func TestTick_AlarmBeatsIdle(t *testing.T) {
	e, wake, _, _, _ := turnEndEngine([]jam.Instance{{ActorID: "a1", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting,
		SessionKind: jam.SessionKindStanding, WaitingSince: time.Unix(9000, 0), IdleDeadline: due,
		Alarms: []jam.Alarm{{Name: "x", Schedule: "@hourly", NextAt: due}}}}, nil, nil)
	e.tick(context.Background())
	for _, rs := range wake.reasons {
		for _, r := range rs {
			if r.Kind == jam.WakeIdle {
				t.Fatalf("idle wake alongside a fired alarm: %v", wake.reasons)
			}
		}
	}
}
```

(`turnEndEngine`'s now is `time.Unix(10000, 0)`; the one-shot schedule string `1970-01-01T02:46:39Z` is `time.Unix(9999,0)` so `OneShot()` is true.)

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/wakeon/`
Expected: compile errors (`SetTurnEnd` arity).

- [ ] **Step 3: Implement** per the Behavior list. `wakeRunning(inst jam.Instance, extra []jam.WakeReason) bool`. Wire `eng.SetTurnEnd(st, nagger, sup /*AlarmFirer*/)` in `cmd/at-jam/main.go`. Update the package doc comment (alarms).

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./internal/wakeon/ ./cmd/at-jam/`
Expected: PASS (all slice-1/2 tests still green).

- [ ] **Step 5: Commit**

```bash
git add internal/wakeon cmd/at-jam
git commit -m "feat(wakeon): fire session alarms for holding and waiting sessions"
```

---

### Task 5: MCP tools + docs

**Files:**
- Modify: `cmd/cove-master/mcp.go`, `cmd/cove-master/mcp_test.go`
- Docs: `docs/usage/jam/turn-end.md`, `docs/usage/jam/roster.md`, `docs/usage/jam/intercom.md`, `docs/usage/jam/INDEX.md`

**Interfaces:**
- Produces: tools `alarm_set{name, schedule, note?}` (PUT `/alarms/{url.PathEscape(name)}`), `alarm_clear{name}` (DELETE), `alarm_list{}` (GET `/alarms`, typed output `alarmsOut{Alarms []alarmItem}` with `name, schedule, note, next_at, fired`); client methods `setAlarm`, `clearAlarm`, `listAlarms`.

- [ ] **Step 1: Write the failing tests** — following `TestMCPEndForwardsReason` (`turnEndClient` helper; extend it to return a canned body for GET):

```go
func TestMCPAlarmSetForwards(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.setAlarm(context.Background(), "pr-watch", "*/5 * * * *", "check"); err != nil {
		t.Fatal(err)
	}
	if m != "PUT" || p != "/alarms/pr-watch" || !strings.Contains(b, `"schedule":"*/5 * * * *"`) || !strings.Contains(b, `"note":"check"`) {
		t.Fatalf("%s %s %s", m, p, b)
	}
}

func TestMCPAlarmClearForwards(t *testing.T) {
	var m, p, b string
	c := turnEndClient(t, &m, &p, &b)
	if err := c.clearAlarm(context.Background(), "pr-watch"); err != nil {
		t.Fatal(err)
	}
	if m != "DELETE" || p != "/alarms/pr-watch" {
		t.Fatalf("%s %s", m, p)
	}
}

func TestMCPAlarmListDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"alarms":[{"name":"nightly","schedule":"0 2 * * *","note":"backup","next_at":"2026-10-06T02:00:00Z"}]}`)
	}))
	defer srv.Close()
	c, err := newMessagingClient(func(k string) string {
		return map[string]string{"AT_JAM_RUNTIME_ADDR": srv.URL, "AT_JAM_IDENTITY_TOKEN": "tok"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.listAlarms(context.Background())
	if err != nil || len(out.Alarms) != 1 || out.Alarms[0].Name != "nightly" || out.Alarms[0].NextAt != "2026-10-06T02:00:00Z" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
```

and add `alarm_set`, `alarm_clear`, `alarm_list` to the tool-name assertion.

- [ ] **Step 2: Run** `go test ./cmd/cove-master/` — compile errors.

- [ ] **Step 3: Implement** the client methods (mirroring `end`/`listTargets`) and register the tools after `idle_timeout`:
- `alarm_set` — `"Set (or replace) a named alarm that wakes you after your turn ends: schedule is an RFC 3339 time (once) or a 5-field cron expression in the role's time zone (recurring, at most once a minute); note is what the wake tells you to do. Up to 20 alarms. Alarms due while you are working fire when your turn ends."`
- `alarm_clear` — `"Remove a named alarm."`
- `alarm_list` — `"List your alarms with their next fire time."`

- [ ] **Step 4: Run** `go test ./cmd/cove-master/` — PASS.

- [ ] **Step 5: Docs** (docs-author)
- `turn-end.md`: opening paragraph lists alarms as live (gates and `report` still later); new **## Alarms** section: the three tools and endpoints; one-shot vs cron, role time zone (`--time-zone`, default UTC), limits (20, once a minute, 1000-byte note, 366 days, name pattern); held while running, fire on holding/waiting; a fired alarm's wake is re-sent until the session runs, then a one-shot is removed and a cron alarm re-arms; no catch-up; a paused session is resumed then woken; one wake carries a pending reply and all fired alarms; an alarm beats the idle timeout. Update `summary`/`read_when`/`owns` and the INDEX row.
- `roster.md`: fold `--time-zone` into the existing turn-end parenthetical (stay ≤ 200 lines).
- `intercom.md`: extend the `end`/`idle_timeout` tool bullet with `alarm_set`/`alarm_clear`/`alarm_list` (link `turn-end.md#alarms`).
- docs-audit: no new errors for touched docs.

- [ ] **Step 6: Full verification** — `go build ./... && go test -count=1 ./... && just lint` — all PASS.

- [ ] **Step 7: Commit and PR**

```bash
git add cmd/cove-master docs
git commit -m "feat(cove-master): alarm_set, alarm_clear, alarm_list tools; docs for alarms"
```

PR: `feat(jam): turn-end slice 3 — alarms`.
