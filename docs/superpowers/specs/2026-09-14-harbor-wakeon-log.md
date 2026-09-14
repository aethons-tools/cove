# harbor: msgport Slice 2 — wake-on reads the Log (COV-175)

**Status:** design approved (brainstorm + clean-switch fork), pre-plan
**Issue:** COV-175. **Foundation:** COV-174 (1b ingress — feeds inbound to the Log), COV-172 (spine), COV-171 (msglog), COV-160/162 (the wake-on this migrates).

## Summary

The first slice that **reads** the Log. Wake-on stops polling Linear ticket comments and instead wakes a Waiting cove when an **external-origin inbound message addressed to it** landed in the Log **after it started waiting**. Fed by the 1b ingress engine. **Clean switch** — no flag; revert by redeploying the prior binary.

## 1. Wake-on engine (`internal/wakeon`)

Replace the reply-detection source:

- **Drop** the `Cursors` (`SetWaitCursor`) and `Commenter` (`IssueByIdentifier`/`Comments`) deps + the `cur`/`cmt` `Engine` fields + the `New` params.
- **Add** a nil-safe `Inbox`:
```go
type Inbox interface {
	ReadInbox(t msglog.Target) []msglog.Message
}
// Engine gains: inbox Inbox  (may be nil → reply-waking disabled, teardown/pause still run)
```
- **New signature:** `New(reg Registry, wake Waker, reap Reaper, idler Idler, inbox Inbox, cfg Config, log *slog.Logger) *Engine` (imports `internal/msglog`).

**`tick` — the reply-detection change** (everything else in the loop is unchanged):
```
for each inst in reg.ListInstances():
    if inst.Activity != harbor.ActivityWaiting: continue
    if !inst.WaitingSince.IsZero() && now.Sub(inst.WaitingSince) > cfg.MaxWait:
        Teardown(inst.ActorID); continue                 // UNCHANGED
    if e.replied(inst):                                  // NEW: Log-based detection
        if inst.Phase == harbor.PhaseIdled: Resume(inst.ActorID); continue   // UNCHANGED branch
        Wake(inst.ActorID); continue                                          // UNCHANGED branch
    if inst.Phase != harbor.PhaseIdled && now.Sub(inst.WaitingSince) > cfg.WarmTimeout:
        Idle(inst.ActorID)                               // UNCHANGED (B2)

// replied reports whether an external-origin inbound message addressed to the
// cove arrived after it started waiting.
func (e *Engine) replied(inst harbor.Instance) bool {
	if e.inbox == nil { return false }                   // no Log → no reply-waking
	for _, m := range e.inbox.ReadInbox(msglog.Target{Kind: "actor", Ref: inst.ActorID}) {
		if msglog.Classify(m.From) == msglog.External && m.At.After(inst.WaitingSince) {
			return true
		}
	}
	return false
}
```

- **Gone:** `IssueByIdentifier`, `Comments`, the `WaitCursor == ""` baseline, `atoi(WaitCursor)`, `SetWaitCursor`. No cursor at all in the Log path — `WaitingSince` (stamped by `Report` on entering Waiting) IS the baseline.
- **Migration-safe:** every Waiting instance already has a correct `WaitingSince`; no cursor semantics to port. **Over-report-safe:** re-reading the same inbound never double-fires (idempotent — a reply is a boolean condition, not a count).
- **No self-wake:** a cove's own outbound has `To = [human/channel]`, not itself, so it's never in its own `ReadInbox`.
- **`Classify(m.From) == External`** restricts waking to human/channel-origin replies (parity with today; a future internal actor→cove message is a separate concern).
- Max-wait teardown, warm-timeout `Idle`, and `PhaseIdled → Resume` are byte-for-byte unchanged — they never depended on the comment source.

## 2. cmd wiring (`cmd/at-harbor/main.go`)

- Change the wake-on construction to the new signature, passing the **same `messageLog`** (the 1a/1b Log) as the `Inbox`, and dropping the `linearCommenter{tracker}` + the `sup`-as-`Cursors` args:
```go
	eng := wakeon.New(st, rsrv /*Waker*/, sup /*Reaper*/, sup /*Idler*/, messageLog /*Inbox, may be nil*/, wakeon.Config{PollInterval: wpoll, MaxWait: wmax, WarmTimeout: warm}, log)
```
- **Graceful degradation + warning:** `messageLog` may be nil (message-log unconfigured). Wake-on still runs (teardown + pause), but reply-waking is off. Emit a clear one-time warning when a tracker is configured but `messageLog == nil`:
```go
	if messageLog == nil {
		log.Warn("harbor wake-on: message-log not configured — coves will not wake on replies (teardown/pause only)")
	}
```
- `linearCommenter` is still used elsewhere (`/messages`, escalation) — leave it. Only the wake-on call site changes.

## 3. Docs

`docs/usage/harbor/messaging.md` (or coves.md / the wake-on section): note that wake-on now detects replies via the **durable Log** (fed by the msgport ingress engine), not by polling ticket comments — so **`message-log` is required for wake-on to wake coves on replies** (without it, a Waiting cove is only bounded by `wait-max` teardown). Bump `updated`.

## Tests (hermetic)

Rewrite `internal/wakeon/wakeon_test.go`'s reply-detection tests to a `fakeInbox` (returns scripted `[]msglog.Message` for a target). Keep/adapt the max-wait / Idle / Resume tests (they set up Waiting instances + assert Teardown/Idle/Resume — now with `fakeInbox` supplying reply presence/absence):
- **wake on external reply after WaitingSince:** an inbound `From: human:*`, `To:[actor:cove-1]`, `At` > `WaitingSince` → `Wake`.
- **no wake on old inbound:** an inbound with `At` <= `WaitingSince` → no wake (then warm-timeout → `Idle`).
- **no wake on internal-origin:** an inbound `From: actor:*` → not a reply (Classify Internal).
- **Idled + reply → Resume** (not Wake).
- **max-wait → Teardown** regardless of inbox.
- **nil inbox → no wake** (but teardown/pause still fire).
- **no self-wake:** a cove's own outbound (`To:[human]`, not the cove) isn't in its inbox → no wake.

## Deferred / boundaries

- **Dead code (deferred to a cleanup slice):** `Instance.WaitCursor`, `Supervisor.SetWaitCursor`, and `Report`'s WaitCursor-clear are unused after this (wake-on was their only consumer). Leave them; remove in a dedicated cleanup (touches supervisor/instance/tests — out of scope here). Wake-on's own `Cursors` param IS removed (clean).
- `internal/wakeon` now imports `internal/msglog` (stdlib-only; no cycle; it already imports `internal/harbor`).
- No change to `/messages`, escalation, the dispatcher, or the ingress engine. Slice 3 (egress cutover) is next.
- Secrets/bodies never logged (the reply-scan reads message metadata; the wake log lines carry only actor ids).
