# harbor msgport Slice 2 — wake-on reads the Log — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** wake-on wakes a Waiting cove when an external-origin inbound message addressed to it, with `At` after `WaitingSince`, is in the Log — replacing the Linear ticket-comment poll. Clean switch.

**Architecture:** `wakeon.Engine` drops its `Cursors`/`Commenter` deps for one nil-safe `Inbox{ReadInbox}` over `*msglog.Log`; `tick`'s reply-detection becomes an `At`-after-`WaitingSince` scan; everything else (max-wait teardown, warm-timeout Idle, B2 Resume) is untouched. The engine + its cmd wiring change together (the `New` signature changes).

**Tech Stack:** Go; `internal/wakeon`, `internal/msglog`, `internal/harbor`.

## Global Constraints

- **Detection = `Classify(m.From)==External && m.At.After(inst.WaitingSince)`** over `ReadInbox(actor:<coveID>)` — no cursor, no comment-count, no id sort. `WaitingSince` (stamped by `Report`) is the baseline.
- **Migration-safe + over-report-safe + no self-wake** (a cove's outbound `To` is never itself).
- **Untouched:** max-wait `Teardown`, warm-timeout `Idle`, `PhaseIdled → Resume`. Only the reply-detection source changes.
- **Nil-safe `Inbox`:** a nil inbox (message-log unconfigured) → reply-waking off, but teardown + pause still run. cmd warns when a tracker is set without a message-log.
- **Clean switch:** delete the ticket-poll path; no flag.
- **Boundary:** `internal/wakeon` may import `internal/msglog` (stdlib-only; already imports `internal/harbor`). No cycle.
- **Deferred (NOT this slice):** removing the now-dead `Instance.WaitCursor` / `Supervisor.SetWaitCursor` / `Report`'s WaitCursor-clear — a later cleanup. Wake-on's own `Cursors` param IS removed here.
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Prefix go commands with `GOPROXY=off`.

---

### Task 1: Wake-on reads the Log (engine + wiring + docs)

**Files:**
- Modify: `internal/wakeon/wakeon.go`
- Modify: `cmd/at-harbor/main.go` (the one `wakeon.New` call site + a warn)
- Modify: `docs/usage/harbor/messaging.md`
- Test: `internal/wakeon/wakeon_test.go`

**Interfaces:**
- Produces: `wakeon.Inbox` interface; `New(reg Registry, wake Waker, reap Reaper, idler Idler, inbox Inbox, cfg Config, log)` (drops `cur Cursors`, `cmt Commenter`; adds `inbox`).
- Removes (from wakeon): the `Cursors` and `Commenter` interfaces + `cur`/`cmt` fields + all `IssueByIdentifier`/`Comments`/`SetWaitCursor` usage.

- [ ] **Step 1: Rewrite the failing tests**

Read `internal/wakeon/wakeon_test.go` fully first (its fakes: a fake registry, the old fake `Commenter`/`Cursors`, fake `Waker`/`Reaper`/`Idler`, the injected clock, and the existing max-wait/Idle/Resume tests). Replace the `Commenter`/`Cursors` fakes with a `fakeInbox`, and rewrite the reply-detection tests:

```go
type fakeInbox struct {
	byActor map[string][]msglog.Message // actor ref → its inbox
}
func (f *fakeInbox) ReadInbox(t msglog.Target) []msglog.Message { return f.byActor[t.Ref] }

func extInbound(coveID string, at time.Time) msglog.Message {
	return msglog.Message{From: msglog.Target{Kind: "human", Ref: "alice"}, To: []msglog.Target{{Kind: "actor", Ref: coveID}}, Body: "reply", At: at}
}

func TestWakesOnExternalReplyAfterWaitingSince(t *testing.T) {
	clock := time.Unix(2000, 0)
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, WaitingSince: waitStart}}}
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{"cove-1": {extInbound("cove-1", waitStart.Add(time.Minute))}}}
	wake := &fakeWaker{}
	e := New(reg, wake, &fakeReaper{}, &fakeIdler{}, inbox, Config{MaxWait: time.Hour, WarmTimeout: 10 * time.Minute}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if !wake.woke["cove-1"] {
		t.Fatal("an external reply after WaitingSince must Wake the cove")
	}
}

func TestNoWakeOnOldInboundThenIdle(t *testing.T) {
	clock := time.Unix(2000, 0)
	waitStart := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, WaitingSince: waitStart}}}
	// inbound BEFORE WaitingSince → not a reply
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{"cove-1": {extInbound("cove-1", waitStart.Add(-time.Minute))}}}
	wake, idler := &fakeWaker{}, &fakeIdler{}
	e := New(reg, wake, &fakeReaper{}, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: 30 * time.Second}, nil)
	e.now = func() time.Time { return clock } // > WaitingSince + WarmTimeout
	e.tick(context.Background())
	if wake.woke["cove-1"] {
		t.Fatal("old inbound (before WaitingSince) must not wake")
	}
	if !idler.idled["cove-1"] {
		t.Fatal("no reply past warm-timeout → Idle")
	}
}

func TestNoWakeOnInternalOrigin(t *testing.T) {
	clock, waitStart := time.Unix(2000, 0), time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, WaitingSince: waitStart}}}
	internal := msglog.Message{From: msglog.Target{Kind: "actor", Ref: "cove-2"}, To: []msglog.Target{{Kind: "actor", Ref: "cove-1"}}, At: waitStart.Add(time.Minute)}
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{"cove-1": {internal}}}
	wake := &fakeWaker{}
	e := New(reg, wake, &fakeReaper{}, &fakeIdler{}, inbox, Config{MaxWait: time.Hour, WarmTimeout: time.Hour}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if wake.woke["cove-1"] {
		t.Fatal("internal-origin inbound must not wake")
	}
}

func TestIdledReplyResumes(t *testing.T) {
	clock, waitStart := time.Unix(2000, 0), time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Phase: harbor.PhaseIdled, Activity: harbor.ActivityWaiting, WaitingSince: waitStart}}}
	inbox := &fakeInbox{byActor: map[string][]msglog.Message{"cove-1": {extInbound("cove-1", waitStart.Add(time.Minute))}}}
	wake, idler := &fakeWaker{}, &fakeIdler{}
	e := New(reg, wake, &fakeReaper{}, idler, inbox, Config{MaxWait: time.Hour, WarmTimeout: time.Hour}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if wake.woke["cove-1"] || !idler.resumed["cove-1"] {
		t.Fatal("Idled + reply → Resume (not Wake)")
	}
}

func TestNilInboxNoWakeStillTeardownAtMaxWait(t *testing.T) {
	clock, waitStart := time.Unix(1_000_000, 0), time.Unix(0, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Phase: harbor.PhaseLive, Activity: harbor.ActivityWaiting, WaitingSince: waitStart}}}
	reap := &fakeReaper{}
	e := New(reg, &fakeWaker{}, reap, &fakeIdler{}, nil /*nil inbox*/, Config{MaxWait: time.Minute}, nil)
	e.now = func() time.Time { return clock } // way past max-wait
	e.tick(context.Background())
	if !reap.tornDown["cove-1"] {
		t.Fatal("nil inbox must still teardown at max-wait")
	}
}
```
> Adapt `fakeReg`/`fakeWaker`/`fakeReaper`/`fakeIdler` and their recorded-fields to the file's ACTUAL fake names/shapes (from the COV-160/162 tests). DELETE the old fake `Commenter`/`Cursors` and any comment-count baseline tests (they no longer apply). Keep any max-wait/Idle/Resume tests, adapting them to the new `New` signature + `fakeInbox`.

- [ ] **Step 2: Run tests, verify they fail**

Run: `GOPROXY=off go test ./internal/wakeon/ -v` → FAIL (signature/undefined).

- [ ] **Step 3: Rewrite `wakeon.go`**

- Remove the `Cursors` + `Commenter` interfaces. Add:
```go
// Inbox is the read side of the message Log the engine uses to detect replies.
// Satisfied by *msglog.Log; may be nil (message-log unconfigured → no reply-waking).
type Inbox interface {
	ReadInbox(t msglog.Target) []msglog.Message
}
```
- `Engine`: drop `cur`/`cmt`; add `inbox Inbox`. `New(reg Registry, wake Waker, reap Reaper, idler Idler, inbox Inbox, cfg Config, log *slog.Logger) *Engine` (keep the default-config + nil-log handling; set `inbox`).
- Rewrite `tick`'s body to the reply-detection above (keep the max-wait teardown, the `replied` gate for Wake/Resume, and the warm-timeout Idle). Add the `replied` method:
```go
func (e *Engine) replied(inst harbor.Instance) bool {
	if e.inbox == nil {
		return false
	}
	for _, m := range e.inbox.ReadInbox(msglog.Target{Kind: "actor", Ref: inst.ActorID}) {
		if msglog.Classify(m.From) == msglog.External && m.At.After(inst.WaitingSince) {
			return true
		}
	}
	return false
}
```
- Remove the now-unused imports (`strconv` for atoi, etc.); add `internal/msglog`.

- [ ] **Step 4: Update the cmd call site (main.go)**

At the `wakeon.New(...)` call (~line 1095), switch to the new signature with `messageLog` as the `Inbox` (drop `sup`-as-Cursors + `linearCommenter{tracker}`):
```go
		eng := wakeon.New(st, rsrv /*Waker*/, sup /*Reaper*/, sup /*Idler*/, messageLog /*Inbox, may be nil*/, wakeon.Config{PollInterval: wpoll, MaxWait: wmax, WarmTimeout: warm}, log)
```
Add the degradation warning (once, near the wake-on log line), when a tracker is configured but no Log:
```go
		if messageLog == nil {
			log.Warn("harbor wake-on: message-log not configured — coves will not wake on replies (teardown/pause only)")
		}
```
(`linearCommenter{tracker}` is still used by `/messages` and escalation — leave those.)

- [ ] **Step 5: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/wakeon/ -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → all green. Confirm `go list -deps ./internal/wakeon | grep -iE 'dispatch|linear'` no longer pulls the tracker path via wakeon (wakeon should now depend on msglog + harbor only for its own needs; the Commenter is gone).

- [ ] **Step 6: Docs**

In `docs/usage/harbor/messaging.md`, in the wake-on section: wake-on now detects replies via the **durable Log** (fed by the msgport ingress engine), not by polling ticket comments — so **`message-log` is required for wake-on to wake coves on replies**; without it, a Waiting cove is bounded only by `wait-max` teardown. Bump `updated`.

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/wakeon/wakeon.go internal/wakeon/wakeon_test.go cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git add internal/wakeon/ cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git commit -m "harbor: wake-on detects replies from the Log, not ticket polling (COV-175)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 engine → Steps 1–3; §2 wiring → Step 4; §3 docs → Step 6; tests → Step 1.
- **Detection correctness** pinned: wake on external+after-WaitingSince (`TestWakesOnExternalReplyAfterWaitingSince`); no wake on old inbound (`TestNoWakeOnOldInboundThenIdle`); no wake on internal-origin (`TestNoWakeOnInternalOrigin`); Idled→Resume (`TestIdledReplyResumes`); nil-inbox degrades but still tears down (`TestNilInboxNoWakeStillTeardownAtMaxWait`).
- **Untouched paths** (max-wait/Idle/Resume) preserved — the diff only changes the reply-detection source + the deps.
- **Signature coupling** handled in one task (engine + tests + the single cmd call site) so the build stays green.
- **Dead code (WaitCursor/SetWaitCursor) deliberately deferred** — flagged for a cleanup slice, not removed here.
- **Placeholder scan:** only "adapt to the file's real fake names" (COV-160/162 test fakes) — real, discoverable.
