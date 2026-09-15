# harbor: bounded incremental message-log reads (Phase 3a)

**Status:** design approved (approach B2), pre-plan
**Issue:** Phase 3a of the store migration — stop the polling loops from scanning the whole log/inbox. First of the decomposed Phase-3 pieces (the others: retention, the log decouple toggle, and client-facing `/messages` GET pagination — each its own spec).
**Driver:** **C** scale — three read paths currently materialize far more than they need on every poll.
**Foundation:** Phase 2 (`msglog.Store` + the file and `msglogpg` backends, the conformance suite); the `msgport` egress loop; the `wakeon` engine; the `harbor.Supervisor` `WaitingSince`/`WaitCursor` fields.

## Summary

Add three **bounded** read methods to `msglog.Store` and switch the three whole-scan callers to them:
- **`ListSince(afterID, limit)`** — egress stops reading the entire log every tick.
- **`TailID()`** — `logTailID` stops reading the entire log for one id.
- **`ReadInboxSince(t, afterID, limit)`** — wake-on stops reading a cove's entire inbox every poll, and migrates reply-detection from the `WaitingSince` **timestamp** to the **`WaitCursor` append-position** (waking the dormant field; "timeclocks lie" — a captured log position is a more honest "since here" than a wall-clock compare).

The existing `List`/`ReadInbox`/`ReadThread` stay (the admin view + conformance still use them). Both backends implement the new methods; no ctx (consistent with the interface — queries are indexed and limit-bounded). Read signatures of the *existing* methods are unchanged.

## 1. Interface additions (`internal/msglog/store.go`)

```go
type Store interface {
	// ... existing: Append, ReadInbox, ReadThread, List, SeenIDs, Close ...

	// ListSince returns messages with id > afterID, in append order, capped at
	// limit (limit <= 0 = unbounded). afterID == "" starts from the beginning.
	ListSince(afterID string, limit int) []Message
	// ReadInboxSince returns messages addressed to t with id > afterID, in append
	// order, capped at limit (<= 0 = unbounded). afterID == "" = whole inbox.
	ReadInboxSince(t Target, afterID string, limit int) []Message
	// TailID returns the last (highest-id) message's id, or ("", false) if empty.
	TailID() (string, bool)
}
```

- Ids are time-sortable and append order == id order, so `id > afterID` is the forward cursor and `ORDER BY id DESC LIMIT 1` is the tail.
- **File backend:** bounded scans of the in-memory mirror (find the index after `afterID`, take up to `limit`); `TailID` = last mirror element.
- **`msglogpg` backend:** `WHERE id > $1 ORDER BY id LIMIT $2`; inbox variant joins `message_recipients` (`WHERE kind=$1 AND ref=$2 AND m.id > $3 ORDER BY m.id LIMIT $4`); `SELECT id FROM messages ORDER BY id DESC LIMIT 1` for the tail. Reuse the shared `scanMessages` helper (with its `rows.Err()` check). `limit <= 0` omits the `LIMIT` clause.

## 2. egress → `ListSince` (`internal/msgport/egress.go`)

`egressTick` currently does `e.lg.List(msglog.Filter{})` then skips everything up to `mark.LastMsg` in both passes. Its entire working set is "messages with id > `LastMsg`", so replace the full read with:

```go
msgs := e.lg.List... → e.lg.ListSince(mark.LastMsg, egressBatch)
```

and drop the "skip up to LastMsg" (`seenLast`) guards in both passes — the window already starts after the low-water. `egressBatch` is a package constant (e.g. 500); a backlog drains over successive ticks (Pass 2 advances `LastMsg` across the delivered prefix, so the next tick continues). Behavior-preserving: the window *is* what the skip logic carved out. The engine's `lg` is already `msglog.Store` (Phase 2), so the field type is unchanged.

## 3. `logTailID` → `TailID` (`cmd/at-harbor/main.go`)

Replace the whole-log read:

```go
func logTailID(lg msglog.Store) string {
	id, _ := lg.TailID()
	return id
}
```

(Same signature; empty log → "" as before.)

## 4. wake-on → `ReadInboxSince` + the `WaitCursor` position baseline

**Baseline capture (supervisor).** When a cove enters Waiting, the supervisor currently sets `inst.WaitCursor = ""` (supervisor.go, the `enteringWaiting` block). Change it to capture the log tail as the baseline:

```go
if enteringWaiting {
	inst.WaitingSince = now
	inst.WaitCursor = s.tailID()   // "" when no log reader is wired (degenerate)
	inst.EscalationTier = 0
	inst.TierPingedAt = time.Time{}
}
```

The `Supervisor` gains a narrow tail source, `tailReader interface { TailID() (string, bool) }`, wired from the selected message-log backend; `nil` ⇒ `WaitCursor` stays `""`. (Plan decides constructor-param vs. a `SetTailReader` setter + wiring order, since `sup` is currently built before `messageLog` in `main`.)

**Reply detection (wakeon).** `replied` drops the `At.After(WaitingSince)` time filter and reads only the post-baseline inbox:

```go
func (e *Engine) replied(inst harbor.Instance) bool {
	if e.inbox == nil {
		return false
	}
	for _, m := range e.inbox.ReadInboxSince(msglog.Target{Kind: "actor", Ref: inst.ActorID}, inst.WaitCursor, 0) {
		if msglog.Classify(m.From) == msglog.External {
			return true
		}
	}
	return false
}
```

- `wakeon.Inbox` interface changes from `{ ReadInbox }` to `{ ReadInboxSince }`.
- **`limit = 0` (unbounded) here is deliberate:** the *cursor* bounds the read to messages after the wait-baseline (small in practice), and existence-with-a-positive-limit could miss an external reply sitting past the limit behind a run of internal messages (the baseline cursor never advances within a wait, so a truncated window would never be re-examined). The cursor, not a limit, is the bound.
- **`WaitingSince` is retained** for the MaxWait teardown and WarmTimeout idle in `tick` — coarse wall-clock timeouts where clock quality is immaterial. Only reply-detection moves to the position cursor.
- **Semantics:** a reply is "an external-origin message addressed to the cove appended after it entered Waiting." `WaitCursor==""` (no log/degenerate) makes `ReadInboxSince` return the whole inbox; with the log configured, `TailID` yields a real position so prior-wait messages are excluded. The tiny race (a reply landing between the cove finishing its turn and `SetActivity(Waiting)` recording) is identical to the prior `WaitingSince` scheme — no regression.

## 5. Tests

- **Conformance suite** (`internal/msglog/msglogtest`): add cases for `ListSince` (after a cursor, from "", with/without limit, append order), `ReadInboxSince` (recipient match + cursor + multi-recipient), and `TailID` (empty → false; returns the highest id). Runs against **both** backends (file hermetic; `msglogpg` behind the `integration` tag → CI Postgres).
- **egress** (`internal/msgport/egress_test.go`): existing tests should hold (behavior-preserving); add a case that a message ≤ `LastMsg` is never re-delivered and that a backlog beyond `egressBatch` drains across ticks.
- **wakeon** (`internal/wakeon/wakeon_test.go`): reply detected only for an external message appended **after** the baseline cursor; a pre-baseline external message does **not** wake; nil inbox → no wake. Update the `Inbox` fake to `ReadInboxSince`.
- **supervisor** (`internal/harbor/supervisor_test.go`): entering Waiting stamps `WaitCursor` = the current tail id (via a fake `tailReader`); nil reader ⇒ `WaitCursor==""`.
- Hermetic default preserved; `store-integration` CI already runs `./internal/msglog/...`.

## 6. Boundaries & non-goals

- **Scope:** the three internal polling-loop scans only (egress, `logTailID`, wake-on). Additive interface methods; existing methods unchanged; no ctx.
- **Deferred (separate Phase-3 pieces, each its own spec):** client-facing **`/messages` GET pagination** (messages.go's `ReadInbox` on the cove read path — a protocol change needing cursor/limit params, landed by #196), **retention/compaction**, the **log decouple toggle**, and general **ctx-threading**.
- No `Message`/`Target`/`Filter`/`Classify` change. Single-writer preserved. File backend stays the hermetic/dev default.

## 7. Docs (same change)

- `docs/usage/harbor/` — wake-on reply-detection now keys off the `WaitCursor` log position (not `WaitingSince`); note it lives with whichever wake-on/escalation doc owns the engine behavior. Keep it a one-liner; link, don't duplicate.
- Route via docs-author; verify with docs-audit (no net-new findings).
