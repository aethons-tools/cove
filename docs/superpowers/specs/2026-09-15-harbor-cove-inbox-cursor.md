# harbor: cove inbox consumer — durable commit cursor + seekable reads (Phase 3b)

**Status:** design approved, pre-plan
**Issue:** the last open Phase-3 piece. Reframes the deferred "`/messages` GET pagination" as what it should be: a **durable, explicitly-acked, seekable per-cove queue consumer** over the message Log — not an inbox view. (Retention and the log decouple toggle were both dropped; see [[msglog-no-retention]].)
**Driver:** **C** scale — the cove's `read` currently pulls its *entire* inbox every call. This replaces that with queue-processing semantics: consume forward from a durable position, commit what you've processed.
**Foundation:** Phase 3a (`ReadInboxSince`/`TailID`, the supervisor's tail reader + `WaitCursor` baseline pattern); the `/messages` GET handler (`internal/harbor/messages.go`); the cove-master messaging MCP (`cmd/cove-master/mcp.go`); `harbor.Store` + `Instance`.

## Summary

Give each cove a **durable commit cursor** (its processed/committed position in its inbox), stored on its `Instance` in the harbor store and **initialized at raise to the current log tail** (a raised cove consumes from "now" forward, not all history). The cove reads its inbox **seekably** (next N from the cursor, page forward/backward, anchored at cursor/start/end/id) — reads never move the cursor — and **explicitly commits** "processed up to id X", which advances the cursor (monotonic, forward-only). This is at-least-once queue processing: until the cove commits, an uncommitted message is re-consumable (e.g. across a cove restart).

Not an email inbox (newest-first) — a **conversation queue** (oldest-first, in order).

## 1. The commit cursor (`Instance` + supervisor + store)

- Add `CommitCursor string \`json:"commit_cursor,omitempty"\`` to `harbor.Instance` — the last message id the cove has committed as processed. Distinct from `WaitCursor` (wake-on reply baseline); the two are kept separate (a future slice could let wake-on key off "uncommitted messages after CommitCursor", but not here).
- **Raise-time init:** in `Supervisor.Raise` (supervisor.go:119, before the `PutInstance`), set `inst.CommitCursor = s.tailID()` — reusing the tail reader already wired in Phase 3a (`s.tail`/`s.tailID()`). No tail reader ⇒ `""` (consume from the beginning).
- **Advance (commit):** `harbor.Store` gains `AdvanceCommitCursor(actorID, upTo string) (Instance, error)` — an **atomic, monotonic** read-modify-write: set `CommitCursor = upTo` only if `upTo > current` (lexical id order == append order), else no-op; return the resulting instance. Atomicity matters: the commit path (a cove request) runs concurrently with supervisor/wake-on instance writes, so the cursor advance itself must be one guarded operation (FileStore: under `mu`; PostgresStore: a single guarded `UPDATE … SET doc = jsonb_set(…) WHERE actor_id=$1 AND (doc->>'commit_cursor') < $2` then cache-update) — not a racy GetInstance+PutInstance. Add to the conformance suite (both backends).
  - *Cross-writer note:* this makes the commit-cursor advance itself atomic, but a concurrent whole-instance write elsewhere (`SetActivity`, the supervisor's `SetWaitCursor`) still uses read-modify-write-whole-doc and could in principle clobber a just-advanced cursor — the **same pre-existing race profile** `WaitCursor` already has. No worse than today; a general fix (per-field updates or optimistic concurrency via the pgstore `version` column) is deferred, not introduced here.

## 2. Seekable reads (`internal/msglog`)

Phase 3a gave forward reads. Add the backward primitive; together they cover every non-date anchor:

```go
// ReadInboxBefore returns messages addressed to t with id < beforeID, in append
// (ascending) order, the `limit` nearest below beforeID (limit <= 0 = unbounded).
// beforeID == "" means "from the end" (the last `limit` messages).
func (Store) ReadInboxBefore(t Target, beforeID string, limit int) []Message
```

- Symmetry: `ReadInboxSince(t, "", N)` = first N (from start); `ReadInboxBefore(t, "", N)` = last N (from end). Both return ascending, so paging composes: next-forward from a page = `ReadInboxSince(t, page.last, N)`; next-backward = `ReadInboxBefore(t, page.first, N)`.
- Implement on both backends (file: bounded reverse scan of the mirror, returned ascending; `msglogpg`: `… WHERE m.id < $3 [or no upper bound when ''] ORDER BY m.id DESC LIMIT $4`, then reverse to ascending — reuse `scanMessages`). Add conformance cases (both backends): before-a-cursor, from-end, empty, multi-recipient.
- **Date anchoring is deferred** (needs an `at`-indexed query) — a documented fast-follow.

## 3. `/messages` GET — seekable, cursor-relative (`internal/harbor/messages.go`)

`handleGet` gains query params (all optional):

| param | values | default | meaning |
|-------|--------|---------|---------|
| `anchor` | `cursor` \| `start` \| `end` \| `id` | `cursor` | where to read from |
| `id` | a message id | — | required when `anchor=id` |
| `dir` | `forward` \| `backward` | `forward` | direction of travel from the anchor |
| `limit` | int | 50 | page size (capped, e.g. ≤ 500) |

Mapping (the cove's `CommitCursor` comes from the resolved `inst`):
- `anchor=cursor,dir=forward` (default) → `ReadInboxSince(actor, inst.CommitCursor, limit)` — **next N unprocessed**.
- `anchor=cursor,dir=backward` → `ReadInboxBefore(actor, inst.CommitCursor, limit)` — re-read already-processed history.
- `anchor=start` → `ReadInboxSince(actor, "", limit)`; `anchor=end` → `ReadInboxBefore(actor, "", limit)`.
- `anchor=id,dir=forward` → `ReadInboxSince(actor, id, limit)`; `anchor=id,dir=backward` → `ReadInboxBefore(actor, id, limit)`.

Reads **never** advance `CommitCursor`. The `reader` interface (`inboxReader`) extends to `{ ReadInboxSince; ReadInboxBefore }` (drop the now-unused unbounded `ReadInbox` from this consumer; `msglog.Store` still has it for the admin view).

Response gains the cursors so a client can page and see its committed position:

```json
{ "messages": [ … ],
  "committed_cursor": "<inst.CommitCursor>",
  "page_first": "<first id or ''>",
  "page_last":  "<last id or ''>" }
```

## 4. `/messages` commit — distinct op (`internal/harbor/messages.go` + mux)

A new route `POST /messages/commit` (the mux in `cmd/at-harbor`), body `{ "up_to": "<message id>" }`:
- Resolve the actor (same bearer-token identity as the rest of `/messages`), then `store.AdvanceCommitCursor(actor.ID, upTo)` (monotonic; a backward/equal `up_to` is a no-op success). Return `{ "committed_cursor": "<new value>" }` (204/200).
- It is a **dedicated op**, not overloaded onto the send `POST /messages`. Empty/malformed `up_to` → 400. No auth beyond the caller's own identity (a cove commits only its own cursor — the actor id comes from the token, never the body).
- **`up_to` is not validated for existence or against the log tail** in v1: it is the cove's own cursor, so a bogus/too-large id only causes the cove to skip its own unread messages (self-inflicted). Monotonic-forward + idempotent (re-commit of an already-committed id succeeds as a no-op). Clamping to the log tail is a deferred hardening (it would couple the store to the log's `TailID`).

## 5. cove-master MCP (`cmd/cove-master/mcp.go`)

- **`read`** gains optional inputs `anchor`, `id`, `dir`, `limit` (defaults: `cursor`/forward/50) and forwards them as query params; `readOut` gains `committed_cursor` / `page_first` / `page_last`. Update its description to queue semantics: "Read your inbox as a queue. Default: the next unprocessed messages after your commit cursor (oldest first). Page with anchor/dir; reading does not mark anything processed."
- **New `commit` tool** — input `{ up_to: "<id>" }` → `POST /messages/commit`. Description: "Confirm you've processed your inbox up to this message id; advances your durable read cursor so you won't be handed those messages again. Call it after you've durably handled them."
- The parameterless call still works (defaults) — but now returns "next N from cursor," not the whole inbox.

## 6. Tests

- **msglog conformance** (both backends): `ReadInboxBefore` (before-cursor, from-end via `""`, empty, multi-recipient; composes with `ReadInboxSince` for round-trip paging).
- **store conformance** (both backends): `AdvanceCommitCursor` — advances forward, no-ops on equal/backward `up_to`, errors on unknown actor, atomic (last-writer-monotonic).
- **supervisor**: `Raise` stamps `CommitCursor = tailID()` (fake tail reader); `""` with no reader.
- **messages handler** (hermetic, file log + a fake store): each anchor/dir mapping returns the right window; default = next-N-after-CommitCursor; reads don't advance the cursor; `POST /messages/commit` advances it and is monotonic; `up_to` malformed → 400; identity from token not body.
- **cove-master mcp**: `read` forwards params + decodes the new fields; `commit` posts `up_to`; parameterless `read` still works.
- Postgres paths (`ReadInboxBefore`, `AdvanceCommitCursor`) run behind the `integration` tag in the `store-integration` CI job.

## 7. Boundaries & non-goals

- **Deferred:** **date** anchoring (needs a time-indexed read) — fast-follow. Unifying wake-on with the commit cursor — separate slice; `WaitCursor` is untouched here.
- **No pruning** ([[msglog-no-retention]]): the cursor is a position into an ever-growing log; committing never deletes. Backward/`start` reads always work.
- **At-least-once:** the durable cursor means a cove that dies before committing re-consumes uncommitted messages on restart — intended.
- Existing `msglog` read methods and `Message`/`Target`/`Filter` unchanged; no ctx (consistent). Single-writer preserved.
- Behavior change: parameterless cove `read` now returns "next N from the commit cursor," not the whole inbox. cove-master ships with harbor (version-locked), so this is coordinated.

## 8. Docs (same change)

- `docs/usage/harbor/messaging.md` — the read model (queue consumer: durable commit cursor, seekable reads, explicit commit; raise-time init; separate from `WaitCursor`) and the `POST /messages/commit` op. Owner of the messaging/read mechanics; link, don't duplicate.
- Route via docs-author; verify with docs-audit (no net-new).
