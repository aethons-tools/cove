# harbor: message-log append-`Seq` ordering (COV-184)

**Status:** design approved (root-fix scope B; message-id wire + internal int64 Seq + `SeqOf` resolution), pre-plan
**Issue:** COV-184 (the wake-on-reply bug; root cause is systemic). **Touches:** `internal/msglog` (core + file + pg + conformance), `internal/msgport` (egress), `internal/harbor` (instance/supervisor/memstate/messages/storetest), `internal/wakeon`, `cmd/at-harbor`.

## Problem

`Message.ID` is deterministic-for-dedup (ingress ids are `in:<svc>:<foreignID>`), **not** monotonic-for-order. But the whole cursor/paging layer added by #198 — `ReadInboxSince`/`ReadInboxBefore`/`ListSince`/`TailID` + `CommitCursor` + `WaitCursor` + `EgressMark.LastMsg`, and the pg backend's `ORDER BY m.id` / `WHERE m.id > $` — assumes **lexical id order == append order**. That premise is false for ingress ids (`in:discord:… < in:linear:…`; `in:*` vs digit-prefixed `%020d` internal ids; random uuids). Consequences:
- **wakeon** skips a reply (cove not woken) when the Log tail at wait-entry is an ingress message (COV-184).
- **`/messages` read paging** can omit newer replies past a cursor.
- **pg backend** returns inboxes in lexical-id (random, for uuids) order — a cove reads replies scrambled.

## Fix

Add a monotonic **append-`Seq`** and make it the single ordering/cursor key internally; keep the cove-facing wire in **message ids** (unchanged contract), resolving id↔Seq at the harbor boundary.

### 1. `Message.Seq` (`internal/msglog`)

```go
type Message struct {
	Seq int64 `json:"seq"` // monotonic append order; assigned at Append, 0 before
	ID  string `json:"id"`
	// … unchanged …
}
```
- **Assigned at `Append`** (after `Prepare` assigns id/At), NOT in `Prepare` (Seq is append order, not a pre-append property).
- **File backend:** an `int64` counter on `*Log`, resumed on `Open` to `max(seen Seq) + 1` (scan the loaded messages; 0 if empty), then `m.Seq = l.nextSeq; l.nextSeq++` under the append mutex. Persisted in the JSONL line (so cursors survive restart and resume is exact).
- **Pg backend:** a `seq BIGSERIAL` column (new migration `0002_seq.sql`: `ALTER TABLE messages ADD COLUMN seq BIGSERIAL`; pre-existing rows get arbitrary seq order — acceptable, migration-in-progress, minimal real data; add `CREATE INDEX idx_messages_seq ON messages (seq)` and a per-recipient index for inbox-by-seq). `Append`'s INSERT adds `RETURNING seq` → set `m.Seq`; `scanMessages` scans `seq`; every SELECT includes `seq`.

### 2. `msglog.Store` API: cursors → `int64` Seq + a resolver

```go
ReadInbox(t Target) []Message                                   // ordered by Seq
ReadInboxSince(t Target, afterSeq int64, limit int) []Message   // Seq > afterSeq, ordered by Seq (afterSeq <= 0 = from start)
ReadInboxBefore(t Target, beforeSeq int64, limit int) []Message // Seq < beforeSeq, nearest below (beforeSeq <= 0 = from end)
ListSince(afterSeq int64, limit int) []Message                  // Seq > afterSeq
ReadThread(rootID string) []Message                             // ordered by Seq
List(f Filter) []Message                                        // ordered by Seq
TailSeq() (int64, bool)                                         // the max-Seq message's Seq (replaces TailID)
SeqOf(id string) (int64, bool)                                  // NEW: resolve a message id → its Seq (boundary use)
SeenIDs(prefix string) []string                                 // UNCHANGED (dedup membership, id-based)
Append(m Message) (Message, error)
Close() error
```
- **`TailID` is removed**, replaced by `TailSeq`. `SeqOf` is new.
- **File backend:** ordering is append-order (the snapshot already is); the `m.ID <= afterID` filter becomes `m.Seq <= afterSeq`; `ReadInboxBefore` bounds by `m.Seq < beforeSeq`. `SeqOf` scans for the id. `TailSeq` = last message's Seq.
- **Pg backend:** every `ORDER BY m.id`/`m.id DESC` → `ORDER BY m.seq`/`m.seq DESC`; every `WHERE m.id > $`/`< $` → `m.seq > $`/`< $`; `TailSeq` = `SELECT seq … ORDER BY seq DESC LIMIT 1`; `SeqOf` = `SELECT seq FROM messages WHERE id = $1`.
- **Conformance** (`msglogtest`): rewrite cursor assertions to seqs; add a **mixed-id-namespace ordering test** (append internal-id + `in:linear:<uuid>` + `in:discord:<snow>` messages in a deliberate order; assert `ReadInbox`/`ReadInboxSince`/`ListSince` return them in **append order** regardless of lexical id — the regression guard for this whole bug), and a `SeqOf` test. Both backends.

### 3. `internal/msgport` egress → `LastSeq`

- `EgressMark.LastMsg string` → `LastSeq int64` (the low-water: every Seq ≤ this is fully delivered). The `Pending` dedup map stays keyed by `m.ID` (a dedup key, not an order key).
- `egress.go`: `e.lg.ListSince(mark.LastSeq, egressBatch)`; the contiguous-done-prefix advance uses `m.Seq` (set `mark.LastSeq = m.Seq`); the "have I reached the low-water" scan compares `m.Seq` / uses the ListSince bound. (No lexical id compare remains.)
- `fileMarkers` persists `LastSeq` (int64 JSON). `logTailID`→`logTailSeq` in cmd; the linear + discord egress **seeds** set `EgressMark{LastSeq: logTailSeq(messageLog)}`.

### 4. `internal/harbor` — Instance cursors → Seq

- `Instance.WaitCursor string` → **`WaitSeq int64`** (wakeon-only; no id needed; old persisted `wait_cursor` ignored → defaults 0 = wake-on-anything-after-0, safe on ephemeral coves).
- `Instance` **adds `CommitSeq int64`** (the ordering key) and **keeps `CommitCursor string`** (the committed message **id**, for the response echo/display) — a deliberate denormalization so the commit/read responses keep returning a message id with **no seq→id reverse lookup**. Old persisted `commit_cursor` id loads fine; `CommitSeq` defaults 0 (read-from-start) — harmless on ephemeral coves.
- `memstate.applyAdvanceCommitCursor(actorID, upToID string, upToSeq int64)`: forward iff `upToSeq > i.CommitSeq` → set `i.CommitSeq = upToSeq; i.CommitCursor = upToID`; else no-op (both unchanged). `Store.AdvanceCommitCursor(actorID, upToID string, upToSeq int64)`.
- `supervisor`: `tailReader.TailID()`→`TailSeq() (int64,bool)`; `tailID()`→`tailSeq()`; `Report` baselines `inst.WaitSeq = s.tailSeq()` and `inst.CommitSeq = s.tailSeq()` (and leaves `CommitCursor` "" at raise — the cove has read nothing yet); `SetWaitCursor(actorID, cursor string)` → `SetWaitSeq(actorID string, seq int64)`.
- `storetest` conformance: `AdvanceCommitCursor(actor, id, seq)` — forward advance sets both id+seq; a lower-seq call is a no-op (both unchanged).

### 5. `internal/wakeon`

```go
type Inbox interface {
	ReadInboxSince(t msglog.Target, afterSeq int64, limit int) []msglog.Message
}
func (e *Engine) replied(inst harbor.Instance) bool {
	if e.inbox == nil { return false }
	for _, m := range e.inbox.ReadInboxSince(msglog.Target{Kind: "actor", Ref: inst.ActorID}, inst.WaitSeq, 0) {
		if msglog.Classify(m.From) == msglog.External { return true }
	}
	return false
}
```
Correct now: Seq > WaitSeq is append-order, so any externally-authored inbound appended after wait-entry fires — regardless of id namespace.

### 6. `internal/harbor/messages` — wire stays message ids, resolve at the boundary

The cove-facing contract is **unchanged** (cove-master + MCP untouched): `read` returns `Messages` (with `id`) + `page_first`/`page_last` (message ids); `commit {up_to: "<message id>"}`.
- **`handleCommit`**: resolve `seq, ok := h.reader.SeqOf(req.UpTo)` (bad/unknown id → 400); `h.store.AdvanceCommitCursor(actor.ID, req.UpTo, seq)`; the response echoes `inst.CommitCursor` (the committed id — unchanged after a backward no-op).
- **`handleGet`** (`committed_cursor`/`page_first`/`page_last` stay message ids — wire unchanged):
  - `anchor=cursor` → use `inst.CommitSeq` directly with `ReadInboxSince`/`ReadInboxBefore`.
  - `anchor=id&id=<msgid>` → `SeqOf(id)` (unknown → 400) → seq-based page.
  - `anchor=start` → `ReadInboxSince(target, 0, limit)`; `anchor=end` → `ReadInboxBefore(target, 0, limit)` (0 = from end).
  - `page_first`/`page_last` = `msgs[0].ID` / `msgs[last].ID`; `committed_cursor` = `inst.CommitCursor` (the committed message id, from the kept denormalized field — no reverse lookup).

### Tests (hermetic)

- **msglog:** Seq monotonic + assigned at Append + file resume-on-Open; `SeqOf`; `TailSeq`; the **mixed-namespace ordering** regression test (the core guard); conformance updated (both backends; pg behind its existing integration gate/skip).
- **msgport:** egress `LastSeq` round-trip + exactly-once across ticks with mixed ids; `fileMarkers` LastSeq persist.
- **harbor:** `AdvanceCommitCursor` numeric monotonic; supervisor baselines `WaitSeq`/`CommitSeq` from `TailSeq`; messages `commit`/`read` resolve ids↔seq; storetest updated.
- **wakeon:** external reply with Seq > WaitSeq wakes even when a higher-lexical ingress id is the tail (the COV-184 regression test); no-wake before baseline; internal-origin no-wake; nil inbox.
- **cmd:** `logTailSeq`; egress seeds; full build/test green at the end.

## Staging (one atomic PR; module green returns at the final task)

Bottom-up. Because the cursor type changes across packages, `go build ./...` goes red after the msglog API task and returns green at the final wiring task (each converted package compiles+tests as it lands; the **final PR head is fully green** — CI gates there). Tasks: (1) `Message.Seq` + assignment + pg migration + `SeqOf`/`TailSeq` [additive, green]; (2) msglog cursor API → Seq (both backends + conformance + mixed-namespace test); (3) msgport egress → LastSeq (+ fileMarkers + cmd seeds); (4) harbor Instance/supervisor/memstate/storetest → WaitSeq/CommitSeq; (5) wakeon + messages handlers (SeqOf boundary) → Seq; (6) cmd `logTailSeq` + final build/test/docs green.

## Docs

`docs/usage/harbor/messaging.md` (or the message-log/observability doc): note the message-log orders by a monotonic append sequence (ids are identifiers, not ordering keys) — a one-paragraph invariant so a future change doesn't reintroduce id-ordering. Bump `updated`.

## Deferred / boundaries

- **Deferred:** COV-185 (discord `Event.At`) is independent. Receipt pruning (COV-183) independent.
- **Boundaries:** `internal/msglog` stays stdlib (+pgx in the pg subpackage, as today); `internal/msgport` stays msglog+stdlib (`int64` field, no new import); `internal/harbor` core unchanged in its import set. The cove-facing wire (message ids) + `cove-master` + MCP schema are **unchanged**. No secret/body in logs. The pg `seq BIGSERIAL` migration assigns arbitrary order to any pre-existing rows (noted).
