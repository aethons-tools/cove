# Turn-end lifecycle — Slice 5: `report` and the Requisitioner switch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A ticket studio reports its ticket's state with `report(state, summary, pr?)` (Jam moves the Linear ticket and comments), owns its branch/PR through merge, and ends with `end`; a ticket session torn down without a terminal report is marked `blocked`. On the Jam path `worker-result.json` is retired: every session's turn ends the same way.

**Architecture:** A new cove endpoint `POST /report` (self-scoped like `/end`) validates the report and calls a `jam.TicketReporter` — implemented in `cmd/at-jam` over the Requisitioner's Linear client (`IssueByIdentifier` → `Transition` + `PostComment`) — then stamps `Instance.Report`. The wake-on engine calls the same adapter's `BlockUnfinished` before any teardown it performs on a ticket session (`end`, idle teardown, `wait-max`) whose last report is not terminal. The Requisitioner's prompt replaces the `worker-result` protocol with the report/end protocol. `agentrun` stops reading `worker-result`: every episode ends in `waiting` (a crashed turn is logged loudly, as resident sessions already do).

**Tech Stack:** Go; the existing `internal/dispatch/linear` client and `internal/dispatch/scheduler` types are **used, never edited**.

**Spec:** `docs/superpowers/specs/2026-10-05-turn-end-lifecycle-design.md` — §8 slice 5; §1 `report` row; §2 `Report`; §4 entirely; §5 report errors.

## Global Constraints

- **No edits** under `internal/dispatch/**`, `internal/dispatchrun`, `cmd/at-task`, `cmd/at-cove`. Importing `internal/dispatch/scheduler` and `internal/dispatch/linear` from `cmd/at-jam` (as today) is fine; `internal/jam` core must not import them.
- `report` states (exact): `in-progress`, `in-review`, `needs-input`, `blocked`, `done` → `scheduler.RoleInProgress`, `RoleInReview`, `RoleNeedsInput`, `RoleBlocked`, `RoleDone`. Terminal: `done`, `blocked`.
- `POST /report` body `{"state","summary","pr"}`: `summary` required, ≤ 4000 bytes, trimmed; `pr` optional, ≤ 500 bytes, must be an `http(s)://` URL; `in-review` requires `pr`. Errors: no ticket on the session → 400; no tracker configured → 503; tracker failure → 502 (nothing stamped); success → 204.
- Comment body on the ticket (exact shape): `**<state>** — <summary>` plus, when `pr` is set, `\n\nPR: <pr>`.
- Blocked-on-teardown comment: `**blocked** — session ended without a final report: <reason>` where reason is the `end` reason, `idle timeout`, or `wait-max`.
- Teardown always proceeds even if the tracker call fails (logged).
- TDD; hermetic; `export GOPROXY=https://proxy.golang.org GOSUMDB=off GOTOOLCHAIN=local`.

## Design rulings

1. **Crashed agent turns on ticket studios wait, not `blocked`/`done`.** With no result file there is no ticket-specific "error" outcome; a crashed turn is logged loudly (as resident turns are) and the session waits. Jam's idle teardown / `wait-max` then ends it and marks the ticket `blocked`. One code path for every kind (spec §4 said "still reports `blocked`").
2. **`wait-max` teardown of a ticket session also marks it `blocked`** (spec named only `end` and idle teardown) — it is the same "ended without a final report" case.
3. **`report` uses the Requisitioner's tracker directly**, synchronously, not the squawk relay — so the agent learns of a failure (502) and can retry.
4. **The resume prompt for ticket studios** no longer mentions `worker-result`; it says to read new input, and to use `report`/`end`.

## Review Focus

1. **`end` after `report(done)`** → no second transition, no blocked comment. (Task 3, `TestTick_EndAfterTerminalReportNoBlock`.)
2. **`end` without a terminal report** (or after `report(in-review)`) → ticket blocked with the reason, then teardown — even if the tracker call fails. (Task 3, `TestTick_EndUnfinishedBlocksThenTearsDown`.)
3. **Tracker down during `report`** → 502, `Instance.Report` unchanged, agent can retry. (Task 1, `TestReportTrackerFailureIs502`.)
4. **A ticket studio whose agent process crashes** → it waits (logged), not torn down instantly; the old `worker-result` file is never read. (Task 4, `TestRunCrashedTurnWaits`.)
5. **A personal/standing session calls `report`** → 400 "no ticket", nothing transitioned. (Task 1, `TestReportWithoutTicketIs400`.)

---

## File Structure

| File | Change |
|------|--------|
| `internal/jam/report.go` (new) | `TicketReport`, states, `ValidateReport`, `TicketReporter` interface, `ReportHandler`. |
| `internal/jam/instance.go` | `Report *TicketReport`. |
| `internal/jam/supervisor.go` | `SetReport`. |
| `cmd/at-jam/ticket.go` (new) | `linearTicketer`: `Report`, `BlockUnfinished` over `*linear.Client`. |
| `cmd/at-jam/mux.go`, `main.go` | mount `/report`; pass the ticketer; wire wake-on. |
| `internal/wakeon/wakeon.go` | `SetTickets(TicketCloser)`; block before end/idle/wait-max teardown. |
| `internal/dispatcher/dispatcher.go` | new prompt protocol. |
| `internal/agentrun/workload.go` (+ tests) | drop `worker-result`; every episode → waiting. |
| `cmd/cove-master/mcp.go` | `report` tool. |
| Docs | `turn-end.md` §Reporting a ticket; `requisitioner.md`; `coves.md` (outcomes); `intercom.md` (tools). |

---

### Task 1: `TicketReport`, `POST /report`, `SetReport`

**Files:** create `internal/jam/report.go`, `internal/jam/report_test.go`; modify `internal/jam/instance.go`, `internal/jam/supervisor.go`.

**Interfaces — Produces:**
```go
const (ReportInProgress = "in-progress"; ReportInReview = "in-review"; ReportNeedsInput = "needs-input"; ReportBlocked = "blocked"; ReportDone = "done")
type TicketReport struct {
    State   string    `json:"state"`
    Summary string    `json:"summary"`
    PR      string    `json:"pr,omitempty"`
    At      time.Time `json:"at"`
}
func (r TicketReport) Terminal() bool                  // done | blocked
func ValidateReport(r *TicketReport) error             // trims Summary/PR; rules from Global Constraints
type TicketReporter interface {
    Report(ctx context.Context, inst Instance, r TicketReport) error // transition + comment
}
func NewReportHandler(store escalateStore, reporter TicketReporter, setter reportSetter, now func() time.Time, log *slog.Logger) *ReportHandler
type reportSetter interface{ SetReport(actorID string, r TicketReport) error }
func (s *Supervisor) SetReport(actorID string, r TicketReport) error   // under instMu
// Instance gains Report *TicketReport `json:"report,omitempty"`
```

- [ ] **Step 1: Failing tests** (`report_test.go`, reusing `newFakeEscStore`, `HashToken`, `testLogger`, `doTurnEnd`):

```go
type fakeReporter struct {
	got []TicketReport
	err error
}

func (f *fakeReporter) Report(_ context.Context, _ Instance, r TicketReport) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, r)
	return nil
}

type fakeReportSetter struct{ set []TicketReport }

func (f *fakeReportSetter) SetReport(_ string, r TicketReport) error { f.set = append(f.set, r); return nil }

func reportFixture(unit string) (*ReportHandler, *fakeReporter, *fakeReportSetter) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "cove-AET-1"}
	st.instances["cove-AET-1"] = Instance{ActorID: "cove-AET-1", Unit: unit}
	rep, set := &fakeReporter{}, &fakeReportSetter{}
	return NewReportHandler(st, rep, set, func() time.Time { return time.Unix(5000, 0) }, testLogger()), rep, set
}

func TestReportTransitionsAndStamps(t *testing.T) {
	h, rep, set := reportFixture("AET-1")
	rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"in-review","summary":" PR is up ","pr":"https://github.com/o/r/pull/7"}`)
	if rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	want := TicketReport{State: "in-review", Summary: "PR is up", PR: "https://github.com/o/r/pull/7", At: time.Unix(5000, 0)}
	if len(rep.got) != 1 || rep.got[0] != want || len(set.set) != 1 || set.set[0] != want {
		t.Fatalf("reported %+v stamped %+v", rep.got, set.set)
	}
}

func TestReportRejects(t *testing.T) {
	h, rep, _ := reportFixture("AET-1")
	for _, body := range []string{
		`{"state":"merged","summary":"x"}`,
		`{"state":"done","summary":"  "}`,
		`{"state":"in-review","summary":"x"}`, // needs a PR
		`{"state":"done","summary":"x","pr":"ftp://x"}`,
		`{"state":"done","summary":"` + strings.Repeat("x", 4001) + `"}`,
	} {
		if rec := doTurnEnd(h, "POST", "/report", "tok", body); rec.Code != 400 {
			t.Errorf("%.60s → %d, want 400", body, rec.Code)
		}
	}
	if len(rep.got) != 0 {
		t.Fatalf("reported %+v", rep.got)
	}
}

func TestReportWithoutTicketIs400(t *testing.T) {
	h, rep, _ := reportFixture("")
	if rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"done","summary":"x"}`); rec.Code != 400 || !strings.Contains(rec.Body.String(), "no ticket") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(rep.got) != 0 {
		t.Fatal("reported without a ticket")
	}
}

func TestReportTrackerFailureIs502(t *testing.T) {
	h, rep, set := reportFixture("AET-1")
	rep.err = errors.New("linear down")
	if rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"done","summary":"x"}`); rec.Code != 502 {
		t.Fatalf("%d", rec.Code)
	}
	if len(set.set) != 0 {
		t.Fatal("stamped a report the tracker never took")
	}
}

func TestReportNoTrackerIs503(t *testing.T) {
	st := newFakeEscStore()
	st.actors[HashToken("tok")] = Actor{ID: "c"}
	st.instances["c"] = Instance{ActorID: "c", Unit: "AET-1"}
	h := NewReportHandler(st, nil, &fakeReportSetter{}, time.Now, testLogger())
	if rec := doTurnEnd(h, "POST", "/report", "tok", `{"state":"done","summary":"x"}`); rec.Code != 503 {
		t.Fatalf("%d", rec.Code)
	}
}
```

And in `supervisor_test.go`: `TestSetReport` — `raiseWithTurnEnd`, `SetReport("w1", TicketReport{State: "done", Summary: "x"})`, the store's `Report` matches; `Terminal()` true for done/blocked only (table).

- [ ] **Step 2:** `go test ./internal/jam/ -run Report` — compile errors.
- [ ] **Step 3: Implement** `report.go`: constants; `ValidateReport` (trim; state in set; summary non-empty ≤ 4000; pr ≤ 500 and `strings.HasPrefix` `http://`/`https://` when set; `in-review` requires pr); `ReportHandler.ServeHTTP` — POST only, `authenticateCove`, 4 KiB+1 KiB body cap (`maxReportBodyBytes = 8192`), decode, `ValidateReport` → 400 with the message, `inst.Unit == ""` → 400 `"no ticket: report is for ticket sessions"`, `reporter == nil` → 503 `"no tracker configured"`, `r.At = now()`, `reporter.Report` error → 502 (log the error, not the summary), then `setter.SetReport` (error → 502), 204; log `report` with actor, state. `SetReport` in `supervisor.go` under `instMu`. `Instance.Report` field.
- [ ] **Step 4:** `go test ./internal/jam/...` — PASS.
- [ ] **Step 5:** Commit `feat(jam): POST /report — ticket state from the session`.

---

### Task 2: The Linear ticketer and mounting `/report`

**Files:** create `cmd/at-jam/ticket.go`, `cmd/at-jam/ticket_test.go`; modify `cmd/at-jam/mux.go` (+ test), `cmd/at-jam/main.go`.

**Interfaces — Produces:**
```go
// cmd/at-jam
type ticketTracker interface { // *linear.Client
    IssueByIdentifier(ctx context.Context, identifier string) (string, error)
    Transition(ctx context.Context, issueID string, role scheduler.Role) error
    PostComment(ctx context.Context, issueID, body string) error
}
type linearTicketer struct{ t ticketTracker }
func (l linearTicketer) Report(ctx context.Context, inst jam.Instance, r jam.TicketReport) error
    // IssueByIdentifier(inst.Unit) → Transition(role for r.State) → PostComment(body); first error returned
func (l linearTicketer) BlockUnfinished(ctx context.Context, inst jam.Instance, reason string) error
    // no Unit, or inst.Report terminal → nil (no-op); else Transition(RoleBlocked) + PostComment(blocked body)
func reportRole(state string) (scheduler.Role, bool)
func coveHTTPHandler(broker http.Handler, st jam.Store, sup *jam.Supervisor, lg intercom.Store, requisitioner bool, tickets jam.TicketReporter, log *slog.Logger) http.Handler
```

- [ ] **Step 1: Failing tests** (`ticket_test.go`, fake `ticketTracker` recording calls):
  - `TestTicketerReport` — `in-review` with PR → `IssueByIdentifier("AET-1")`, `Transition(id, RoleInReview)`, `PostComment(id, "**in-review** — PR is up\n\nPR: https://…")`.
  - `TestTicketerReportStates` — each of the five states maps to its `scheduler.Role`; `reportRole("merged")` is false.
  - `TestTicketerBlockUnfinished` — `Unit: "AET-1"`, no report → `Transition(RoleBlocked)` + comment `**blocked** — session ended without a final report: wrapped up`; with `Report{State: "done"}` → no calls; with `Report{State: "in-review"}` → blocked; `Unit: ""` → no calls.
  - `TestTicketerErrorsPropagate` — `Transition` error → returned (no comment posted).
  - `mux_test.go`: `TestSquawksMuxRouting` gains `{"/report", "turn-end"}` (`/report` routes to the turn-end handler group — see Step 3) and `TestCoveHTTPHandlerMountsSquawksWithoutRequisitioner`'s call site passes `nil` tickets.
- [ ] **Step 2:** run — compile errors.
- [ ] **Step 3: Implement.** `ticket.go` as above (body helpers `reportComment(r)` / `blockedComment(reason)`). In `mux.go`: route `"/report"` to a `reportH` (add a parameter to `squawksMux`, as slice 2 did for `turnEndH`); `coveHTTPHandler` gains `tickets jam.TicketReporter` and builds `jam.NewReportHandler(st, tickets, sup, time.Now, log)` — pass a nil interface (not a typed nil) when there is no tracker. In `main.go`: build `var tickets jam.TicketReporter` before `coveHTTPHandler`; when the Requisitioner's `tracker` is created (inside `if dc != nil`), the handler is already built — so create the Linear client earlier or construct the ticketer lazily: **move** the `httpHandler := coveHTTPHandler(...)` call (or a `tickets` holder with a `set` method guarded by a mutex — `ticketHolder{mu; t jam.TicketReporter}` implementing `TicketReporter` and returning `errNoTracker` → mapped to 503 by checking `errors.Is(err, jam.ErrNoTracker)`) — choose the holder: it keeps the start-up order unchanged. Then `jam.ErrNoTracker` replaces the `reporter == nil` check in the handler (Task 1's 503 test keeps passing with a holder whose tracker is unset — adjust that test to use `jam.ErrNoTracker`).
- [ ] **Step 4:** `go test ./cmd/at-jam/ ./internal/jam/` — PASS.
- [ ] **Step 5:** Commit `feat(at-jam): Linear ticketer for /report; mount the endpoint`.

---

### Task 3: Wake-on marks unfinished tickets `blocked` before teardown

**Files:** `internal/wakeon/wakeon.go`, `internal/wakeon/wakeon_test.go`, `cmd/at-jam/main.go`.

**Interfaces — Produces:**
```go
type TicketCloser interface {
    BlockUnfinished(ctx context.Context, inst jam.Instance, reason string) error
}
func (e *Engine) SetTickets(t TicketCloser) // nil = no ticket updates
```
Behavior: in `endSession` (reason = the end reason), the idle-teardown branch of `fireIdle` (reason `idle timeout`), and the `wait-max` teardown (reason `wait-max`): call `e.blockUnfinished(ctx, inst, reason)` **before** `Teardown`; it logs a failure and never prevents the teardown. (The adapter decides no-op for ticketless or terminal-reported sessions; the engine calls it for every such teardown.)

- [ ] **Step 1: Failing tests** — `fakeTickets` recording `actor:reason`:
  - `TestTick_EndUnfinishedBlocksThenTearsDown` — waiting, `Unit: "AET-1"`, `EndRequested{Reason: "gave up"}`, fakeTickets returns an error → recorded `a1:gave up` and teardown still happened.
  - `TestTick_EndAfterTerminalReportNoBlock` — the engine still calls `BlockUnfinished` (the adapter no-ops); assert the fake was called once and teardown happened (the no-op itself is covered by `TestTicketerBlockUnfinished`). *(Rename to `TestTick_EndCallsTicketCloser` if clearer; keep Review Focus 1's name on the adapter test instead.)*
  - `TestTick_IdleTeardownBlocks` — idle deadline due, role on-idle teardown → `a1:idle timeout`.
  - `TestTick_WaitMaxBlocks` — non-resident, no deadline, no alarms, waited past MaxWait → `a1:wait-max`.
- [ ] **Step 2:** run — compile errors. **Step 3:** implement; wire `eng.SetTickets(tickets)` with the same holder from Task 2 (its `BlockUnfinished` returns nil when no tracker is set). **Step 4:** `go test ./internal/wakeon/ ./cmd/at-jam/` — PASS. **Step 5:** Commit `feat(wakeon): mark an unfinished ticket blocked before tearing its session down`.

---

### Task 4: Retire `worker-result` on the Jam path

**Files:** `internal/dispatcher/dispatcher.go` (+ test), `internal/agentrun/workload.go` and its tests (`workload_test.go`, `episode_test.go`, `resident_failure_test.go`).

- [ ] **Step 1: Failing tests.**
  - `dispatcher_test.go`: the raised prompt contains `report` and `end` and the PR instructions, and does **not** contain `worker-result`.
  - `agentrun`: `TestRunCrashedTurnWaits` — a non-resident (ticket) workload whose process exits non-zero with no result file reports `Waiting` (not `Done`, Run does not return) and a later Wake starts a `--continue` episode; `TestRunNeverReadsWorkerResult` — a `worker-result.json` with `{"status":{"ok":{}}}` present does **not** end the unit (it waits). Rewrite or delete the tests that encode the old outcomes (`TestRunOK`, `TestRunNeedsInput`, `TestRunErrorResult`, `TestRunNoResultFile`, `TestRunUnparseableResult`, `TestRunMaxWaitEndsUnit`, `TestNonResidentOKStillEnds`, `TestResidentWaitsAfterEveryOutcome`, and any episode test that writes a result to end the unit — end those by cancelling ctx instead).
- [ ] **Step 2:** run — the new tests fail.
- [ ] **Step 3: Implement.**
  - `dispatcher.go`: replace `resultProtocol` with:

```go
// turnEndProtocol tells a ticket studio how it finishes (see
// docs/usage/jam/turn-end.md#reporting-a-ticket): it owns its branch and PR
// through merge, reports the ticket's state with `report`, and ends with `end`.
const turnEndProtocol = `---
Your task is described above. Do the work in this repository: make the changes and run the project's tests.
You own this ticket through merge:
- Work on a branch, push it, and open a pull request yourself (gh pr create).
- Use the intercom ` + "`report`" + ` tool to keep the ticket's state current: in-review with the PR link when it is up, needs-input with your question when you are blocked on a person, blocked if you cannot proceed, done once it has merged.
- While the PR is open, set an alarm (` + "`alarm_set`" + `) whose gate checks the PR (new review comments, failing CI, branch behind main, merged) so you are woken only when there is something to do; address review comments and keep the branch mergeable.
- When the ticket is finished (merged → report done), call ` + "`end`" + ` as your last action.
If you need a person, ask with ` + "`send`" + ` and end your turn; their reply wakes you.`
```
  and use it in `buildPrompt`.
  - `workload.go`: delete the `worker.ReadWorkerResult` branch after an episode; every episode end (resident or not) → `w.logTurn(waitErr)` + `h.Report(covemaster.Waiting)` + `awaitWake(ctx, nil)` + continue with `renderWake(w.resumeText(), rs)`. `logTurn` (renamed from `logResidentTurn`) logs a non-zero exit loudly at WARN and a clean exit at INFO; it reads no file. Remove `MaxWait`'s use (keep the `Config` field for compatibility, documented as unused) and the `worker` import if unused. `resumePrompt` (ticket studios) becomes `"New input may have arrived on your ticket — use the intercom ` + "`read`" + ` tool to fetch it, then continue. Use ` + "`report`" + ` to update the ticket and ` + "`end`" + ` when the ticket is finished."`.
- [ ] **Step 4:** `go test ./internal/dispatcher/ ./internal/agentrun/ ./cmd/cove-master/` — PASS.
- [ ] **Step 5:** Commit `feat(jam): retire worker-result on the Jam path; ticket studios report and end`.

---

### Task 5: `report` MCP tool + docs

**Files:** `cmd/cove-master/mcp.go` (+ test); docs.

- [ ] **Step 1: Failing tests** — `TestMCPReportForwards`: `c.report(ctx, "in-review", "PR is up", "https://…")` → `POST /report` with those three fields; tool list includes `report`.
- [ ] **Step 2:** run — fail. **Step 3:** implement (`reportIn{State, Summary, PR}`; description: `"Update your ticket's state (ticket sessions only): in-progress, in-review (with pr), needs-input (put the question in summary), blocked, or done. Jam moves the ticket and comments. Call it as often as the state changes; it does not end the session — end does."`). **Step 4:** PASS.
- [ ] **Step 5: Docs** (docs-author):
  - `turn-end.md`: **## Reporting a ticket** — the tool/endpoint, states and their ticket states, validation and errors (400 no ticket / bad input, 503 no tracker, 502 tracker failure → retry), the comment shape; ending without a terminal report (`end`, idle teardown, `wait-max`) marks the ticket `blocked`; a typical life (report in-review → PR-watch alarm with a gate → woken → … → report done → end). Opening paragraph: everything in the spec is now live. Update `summary`/`read_when`/`owns` and the INDEX row.
  - `requisitioner.md` step 6 (raise): replace the `worker-result` protocol sentence with the report/end protocol (link turn-end.md#reporting-a-ticket); the "Studios are one-shot ephemeral… tears itself down" model paragraph → studios own the ticket through merge and end themselves.
  - `coves.md`: replace the `ok`/`needs-input`/`error` outcome list with: every episode ends in `waiting`; a crashed turn is logged and waits; ending is `end` (link); `worker-result.json` is not read on the Jam path (it still is by `at-cove work`).
  - `intercom.md`: add `report` to the turn-end tools bullet.
  - `docs/TODO.md`: drop the "turn-end lifecycle … not yet built" line (keep the context-lifecycle item).
  - docs-audit: no new errors for touched docs (watch `coves.md`'s size — keep the replacement shorter than what it replaces).
- [ ] **Step 6:** `go build ./... && go test -count=1 ./... && just lint` — PASS.
- [ ] **Step 7:** Commit `feat(cove-master): report tool; docs for reporting and the retired worker-result`. PR: `feat(jam): turn-end slice 5 — report + Requisitioner switch`.
