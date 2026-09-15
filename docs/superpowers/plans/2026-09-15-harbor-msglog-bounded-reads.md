# harbor bounded incremental message-log reads (Phase 3a) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add bounded read methods (`ListSince`, `ReadInboxSince`, `TailID`) to `msglog.Store` and switch the three whole-scan pollers (egress, `logTailID`, wake-on) to them; wake-on's reply-detection moves from the `WaitingSince` timestamp to the `WaitCursor` append-position.

**Architecture:** Additive interface methods implemented by both backends (file: bounded mirror scan; `msglogpg`: `WHERE id > $1 … LIMIT`). Egress reads only `ListSince(LastMsg, batch)`; `logTailID` uses `TailID()`; the supervisor baselines `WaitCursor` to the log tail on entering Waiting, and wake-on checks `ReadInboxSince(actor, WaitCursor)` for any external message. Existing read methods unchanged; no ctx.

**Tech Stack:** Go, pgx/v5 (only in `msglogpg`), the Phase-2 conformance suite + `store-integration` CI.

**Spec:** `docs/superpowers/specs/2026-09-15-harbor-msglog-bounded-reads.md`

## Global Constraints

- **Additive only:** `List`/`ReadInbox`/`ReadThread`/`SeenIDs` unchanged; add the three new methods. No ctx (consistent with the interface).
- **`internal/msglog` core stays stdlib-only;** pgx only in `internal/msglog/msglogpg`.
- **Both backends behavior-identical** — the conformance suite is the oracle (file hermetic; `msglogpg` behind the `integration` tag).
- **`WaitingSince` is retained** for wake-on's MaxWait teardown + WarmTimeout idle; only `replied()` migrates to `WaitCursor`.
- **Cursor semantics:** `afterID` selects `id > afterID` (exclusive); `afterID == ""` = from the beginning; `limit <= 0` = unbounded.
- Single-writer preserved. Before every commit: `go build ./... && go build -tags integration ./...`, `gofmt -l` (empty over changed files), `go vet` (both tags where relevant).
- **Commit trailer — verbatim** (do NOT substitute your own model name):

  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

- Branch is `feat/harbor-msglog-bounded-reads` (the spec is committed there).

## File Structure

- `internal/msglog/store.go` — add the 3 methods to the `Store` interface (Task 1).
- `internal/msglog/read.go` — file `Log` impl of the 3 methods (Task 1).
- `internal/msglog/msglogtest/conformance.go` — conformance cases for the 3 methods (Task 1).
- `internal/msglog/msglogpg/msglogpg.go` — `msglogpg` impl of the 3 methods (Task 2).
- `internal/msgport/egress.go` — `egressTick` → `ListSince`; `internal/msgport/egress_test.go` (Task 3).
- `cmd/at-harbor/main.go` — `logTailID` → `TailID`; wire the supervisor tail reader (Tasks 3, 4).
- `internal/wakeon/wakeon.go` + `_test.go` — `Inbox` → `ReadInboxSince`; `replied` uses `WaitCursor` (Task 4).
- `internal/harbor/supervisor.go` + `_test.go` — baseline `WaitCursor` from a tail reader on entering Waiting (Task 4).
- `docs/usage/harbor/*` (Task 5).

---

### Task 1: New `Store` methods + file backend + conformance

**Files:**
- Modify: `internal/msglog/store.go`, `internal/msglog/read.go`, `internal/msglog/msglogtest/conformance.go`

**Interfaces:**
- Produces: `Store.ListSince(afterID string, limit int) []Message`, `Store.ReadInboxSince(t Target, afterID string, limit int) []Message`, `Store.TailID() (string, bool)` — consumed by Tasks 2–4.

- [ ] **Step 1: Add conformance cases (fail to compile — methods undefined)**

Append to `internal/msglog/msglogtest/conformance.go` inside `RunConformance` (reuse the `actor`/`human` helpers already defined there):

```go
	t.Run("list_since_and_tail", func(t *testing.T) {
		s := newStore(t)
		var ids []string
		for _, b := range []string{"m1", "m2", "m3"} {
			got, err := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{human("h")}, Body: b})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, got.ID)
		}
		if tail, ok := s.TailID(); !ok || tail != ids[2] {
			t.Fatalf("TailID = %q,%v, want %q,true", tail, ok, ids[2])
		}
		after := s.ListSince(ids[0], 0) // everything after m1
		if len(after) != 2 || after[0].Body != "m2" || after[1].Body != "m3" {
			t.Fatalf("ListSince(m1,0) = %+v, want [m2,m3]", after)
		}
		if lim := s.ListSince("", 2); len(lim) != 2 || lim[0].Body != "m1" {
			t.Fatalf("ListSince(\"\",2) = %+v, want [m1,m2]", lim)
		}
		if none := s.ListSince(ids[2], 0); len(none) != 0 {
			t.Fatalf("ListSince(tail,0) = %+v, want empty", none)
		}
	})

	t.Run("tail_empty_log", func(t *testing.T) {
		if id, ok := newStore(t).TailID(); ok || id != "" {
			t.Fatalf("TailID(empty) = %q,%v, want \"\",false", id, ok)
		}
	})

	t.Run("read_inbox_since", func(t *testing.T) {
		s := newStore(t)
		m1, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "before"})
		m2, _ := s.Append(msglog.Message{From: human("a"), To: []msglog.Target{actor("x")}, Body: "after1"})
		_, _ = s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("y")}, Body: "other"})
		got := s.ReadInboxSince(actor("x"), m1.ID, 0)
		if len(got) != 1 || got[0].Body != "after1" || got[0].ID != m2.ID {
			t.Fatalf("ReadInboxSince(x, m1) = %+v, want [after1]", got)
		}
		if all := s.ReadInboxSince(actor("x"), "", 0); len(all) != 2 {
			t.Fatalf("ReadInboxSince(x, \"\") = %d, want 2", len(all))
		}
		if none := s.ReadInboxSince(actor("z"), "", 0); len(none) != 0 {
			t.Fatalf("ReadInboxSince(z) = %+v, want empty", none)
		}
	})
```

- [ ] **Step 2: Run — RED**

Run: `go test ./internal/msglog/ -run TestFileLogConformance 2>&1 | tail -15`
Expected: compile failure — `ListSince`/`ReadInboxSince`/`TailID` undefined on `msglog.Store`.

- [ ] **Step 3: Add to the interface**

In `internal/msglog/store.go`, add to the `Store` interface (after `List`):

```go
	// ListSince returns messages with id > afterID, in append order, capped at
	// limit (limit <= 0 = unbounded). afterID == "" starts from the beginning.
	ListSince(afterID string, limit int) []Message
	// ReadInboxSince returns messages addressed to t with id > afterID, in append
	// order, capped at limit (<= 0 = unbounded). afterID == "" = the whole inbox.
	ReadInboxSince(t Target, afterID string, limit int) []Message
	// TailID returns the highest-id (last-appended) message's id, or ("", false)
	// when the log is empty.
	TailID() (string, bool)
```

- [ ] **Step 4: Implement on the file `Log`**

In `internal/msglog/read.go` (the mirror is append-ordered; ids sort with append order, so `m.ID > afterID` selects the tail):

```go
// ListSince returns messages with id > afterID, in append order, capped at limit.
func (l *Log) ListSince(afterID string, limit int) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if m.ID <= afterID {
			continue
		}
		out = append(out, m)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// ReadInboxSince returns messages addressed to t with id > afterID, capped at limit.
func (l *Log) ReadInboxSince(t Target, afterID string, limit int) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if m.ID <= afterID {
			continue
		}
		for _, r := range m.To {
			if r == t {
				out = append(out, m)
				break
			}
		}
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out
}

// TailID returns the last message's id, or ("", false) if the log is empty.
func (l *Log) TailID() (string, bool) {
	ms := l.snapshot()
	if len(ms) == 0 {
		return "", false
	}
	return ms[len(ms)-1].ID, true
}
```

Note the `limit` cap for `ReadInboxSince` is applied only after a match is appended (so `limit` bounds returned matches, not messages scanned) — matching the intent (a bounded page of the inbox).

- [ ] **Step 5: Run — GREEN (file backend)**

Run: `go test ./internal/msglog/ 2>&1 | tail -6`
Expected: PASS (new conformance cases + existing). Then `go build ./... && gofmt -l internal/msglog && go vet ./internal/msglog/...`.

- [ ] **Step 6: Commit**

```bash
git add internal/msglog/store.go internal/msglog/read.go internal/msglog/msglogtest/conformance.go
git commit -m "msglog: add bounded ListSince/ReadInboxSince/TailID (file backend + conformance)

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 2: `msglogpg` implementation of the three methods

**Files:**
- Modify: `internal/msglog/msglogpg/msglogpg.go`

**Interfaces:**
- Consumes: the `Store` methods (Task 1); the existing `query`/`scanMessages` helpers, `likePrefix`, the column projection.
- Produces: `msglogpg.Store` satisfies the extended interface (the `var _ msglog.Store = (*Store)(nil)` assertion now also covers the 3 methods).

- [ ] **Step 1: Implement the methods**

Add to `internal/msglog/msglogpg/msglogpg.go` (reuse the `query` helper + the fixed column projection used by `ReadInbox`/`List`; `LIMIT` is added only when `limit > 0`):

```go
func (s *Store) ListSince(afterID string, limit int) []msglog.Message {
	sql := `SELECT id, from_kind, from_ref, body, at, project, reply_to, "to"
	        FROM messages WHERE id > $1 ORDER BY id`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	return s.query(sql, afterID)
}

func (s *Store) ReadInboxSince(t msglog.Target, afterID string, limit int) []msglog.Message {
	sql := `SELECT m.id, m.from_kind, m.from_ref, m.body, m.at, m.project, m.reply_to, m."to"
	        FROM messages m JOIN message_recipients r ON r.message_id = m.id
	        WHERE r.kind = $1 AND r.ref = $2 AND m.id > $3 ORDER BY m.id`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	return s.query(sql, t.Kind, t.Ref, afterID)
}

func (s *Store) TailID() (string, bool) {
	var id string
	err := s.pool.QueryRow(context.Background(),
		`SELECT id FROM messages ORDER BY id DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		s.log.Error("msglogpg: TailID", "error", err.Error())
		return "", false
	}
	return id, true
}
```

`limit` is an `int` from trusted internal callers (not user input); `fmt.Sprintf(" LIMIT %d", limit)` is injection-safe (integer only). Add `"errors"` to the imports if not present; `fmt` and `pgx` are already imported.

- [ ] **Step 2: Verify (compile locally; runs against Postgres in CI)**

Run:
```
go build ./... && go build -tags integration ./...
gofmt -l internal/msglog/msglogpg && go vet ./internal/msglog/... && go vet -tags integration ./internal/msglog/...
go test ./internal/msglog/...   # hermetic (file conformance) still green
```
Expected: compiles; hermetic green. The `msglogpg` conformance (incl. the new cases) runs in CI. If `HARBOR_TEST_POSTGRES_DSN` is reachable, run `go test -tags integration ./internal/msglog/msglogpg/` and report.

- [ ] **Step 3: Commit**

```bash
git add internal/msglog/msglogpg/msglogpg.go
git commit -m "msglogpg: implement bounded ListSince/ReadInboxSince/TailID

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 3: egress → `ListSince`; `logTailID` → `TailID`

**Files:**
- Modify: `internal/msgport/egress.go`, `internal/msgport/egress_test.go`, `cmd/at-harbor/main.go`

**Interfaces:**
- Consumes: `ListSince`, `TailID` (Tasks 1–2).

- [ ] **Step 1: egress uses `ListSince(mark.LastMsg, egressBatch)`**

In `internal/msgport/egress.go`: add a package constant `const egressBatch = 500` and replace the full read:

```go
	msgs := e.lg.List(msglog.Filter{})
```

with:

```go
	msgs := e.lg.ListSince(mark.LastMsg, egressBatch)
```

Then remove the "skip up to LastMsg" guards in **both** passes — the window already starts after `mark.LastMsg`. Concretely:
- Pass 1: delete the `seenLast := mark.LastMsg == ""` line and the leading `if !seenLast { if m.ID == mark.LastMsg { seenLast = true }; continue }` block; iterate `for _, m := range msgs` directly.
- Pass 2: same — delete the `seenLast` reset and skip block; iterate `msgs` directly, keeping the `if !e.egressDone(...) { break }` / `mark.LastMsg = m.ID` / `delete(mark.Pending, m.ID)` body.

(The `msglog` import stays — `Classify` is still used. The `msglog.Filter{}` reference is gone; if `msglog` becomes unused, keep it only if still referenced — it is, via `Classify`.)

- [ ] **Step 2: Add an egress backlog test**

In `internal/msgport/egress_test.go`, add a test that appends more than `egressBatch` internal messages with owned targets and asserts (a) a message with id ≤ the starting `LastMsg` is never delivered, and (b) after enough ticks the whole backlog is delivered and `LastMsg` reaches the tail (drains across ticks). Follow the existing egress-test setup (`openLog`, `fakeSurface`, `fakeMarkers`). Keep existing egress tests unchanged — they must still pass.

- [ ] **Step 3: `logTailID` → `TailID`**

In `cmd/at-harbor/main.go`, replace the body of `logTailID`:

```go
func logTailID(lg msglog.Store) string {
	id, _ := lg.TailID()
	return id
}
```

- [ ] **Step 4: Run**

Run: `go test ./internal/msgport/... ./cmd/at-harbor/... 2>&1 | tail -15 && go build ./... && gofmt -l internal/msgport cmd/at-harbor && go vet ./internal/msgport/ ./cmd/at-harbor/`
Expected: PASS (egress incl. the new backlog test; the `msgport_seed_test` for `logTailID` still passes — a `*msglog.Log` satisfies `msglog.Store` and `TailID` returns the last id). Build + gofmt + vet clean.

- [ ] **Step 5: Commit**

```bash
git add internal/msgport/egress.go internal/msgport/egress_test.go cmd/at-harbor/main.go
git commit -m "msgport+harbor: egress reads ListSince(batch); logTailID uses TailID

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 4: wake-on `ReadInboxSince` + supervisor `WaitCursor` baseline

**Files:**
- Modify: `internal/harbor/supervisor.go`, `internal/harbor/supervisor_test.go`, `internal/wakeon/wakeon.go`, `internal/wakeon/wakeon_test.go`, `cmd/at-harbor/main.go`

**Interfaces:**
- Consumes: `ReadInboxSince`, `TailID` (Tasks 1–2); `Instance.WaitCursor`.
- Produces: `wakeon.Inbox` = `{ ReadInboxSince(Target, string, int) []msglog.Message }`; `Supervisor.SetTailReader(tailReader)`.

- [ ] **Step 1: Supervisor baselines `WaitCursor` from a tail reader**

In `internal/harbor/supervisor.go`:
- Add the narrow reader + a set-once field + a nil-safe helper:

```go
// tailReader is the sliver of the message log the supervisor needs to baseline a
// cove's wake-on cursor to the current log position when it enters Waiting.
type tailReader interface {
	TailID() (string, bool)
}

// SetTailReader wires the message-log tail source used to baseline WaitCursor on
// entering Waiting. Called once at wiring time before serving begins; nil (no
// message log) leaves WaitCursor "".
func (s *Supervisor) SetTailReader(r tailReader) { s.tail = r }

func (s *Supervisor) tailID() string {
	if s.tail == nil {
		return ""
	}
	id, _ := s.tail.TailID()
	return id
}
```

Add the `tail tailReader` field to the `Supervisor` struct.
- In the `enteringWaiting` block, replace `inst.WaitCursor = ""` with:

```go
		inst.WaitCursor = s.tailID()
```

- [ ] **Step 2: Supervisor test — baseline capture**

In `internal/harbor/supervisor_test.go`, add a test: with a fake `tailReader` returning `"id-9"`, driving a cove to Waiting (via the existing `SetActivity(..., ActivityWaiting)` path used by other supervisor tests) stamps `inst.WaitCursor == "id-9"`; with no tail reader set, `WaitCursor == ""`. Keep existing supervisor tests green (they construct the supervisor without a tail reader → `tailID()` returns "").

- [ ] **Step 3: wake-on uses the position cursor**

In `internal/wakeon/wakeon.go`:
- Change the `Inbox` interface:

```go
type Inbox interface {
	ReadInboxSince(t msglog.Target, afterID string, limit int) []msglog.Message
}
```

- Change `replied`:

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

Leave `tick`'s `WaitingSince`-based MaxWait teardown and WarmTimeout idle unchanged.

- [ ] **Step 4: Update the wake-on fake + tests**

In `internal/wakeon/wakeon_test.go`, change `fakeInbox` to implement `ReadInboxSince` (return the actor's scripted messages with `id > afterID`; a simple string-compare filter mirrors the real backend). Update the reply-detection tests so fixtures set message `ID`s and the waiting instance's `WaitCursor`, asserting: an external message with `id > WaitCursor` wakes; a pre-baseline external message (`id <= WaitCursor`) does **not**; nil inbox → no wake; the MaxWait/WarmTimeout tests are unaffected.

- [ ] **Step 5: Wire the tail reader in `main`**

In `cmd/at-harbor/main.go`, after `messageLog` is selected (it is built after `sup`), wire it into the supervisor when non-nil:

```go
	if messageLog != nil {
		sup.SetTailReader(messageLog)
	}
```

Place this immediately after the `messageLog` selection block and before the dispatcher/wake-on goroutines start, so the baseline source is set before any cove can enter Waiting.

- [ ] **Step 6: Run**

Run: `go test ./internal/harbor/... ./internal/wakeon/... 2>&1 | tail -20 && go build ./... && go build -tags integration ./... && gofmt -l internal/harbor internal/wakeon cmd/at-harbor && go vet ./internal/harbor/ ./internal/wakeon/ ./cmd/at-harbor/`
Expected: PASS (supervisor baseline test, wake-on cursor tests, existing harbor tests). Build both tags + gofmt + vet clean.

- [ ] **Step 7: Commit**

```bash
git add internal/harbor/supervisor.go internal/harbor/supervisor_test.go internal/wakeon/wakeon.go internal/wakeon/wakeon_test.go cmd/at-harbor/main.go
git commit -m "harbor+wakeon: baseline WaitCursor to the log tail; reply-detect via ReadInboxSince

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 5: Docs

**Files:**
- Modify: the wake-on/escalation usage doc that owns the engine behavior (find it from `docs/usage/harbor/INDEX.md` — likely `escalation.md` or a wake-on section); optionally `messaging.md`.

**Interfaces:** Consumes the behavior shipped in Tasks 1–4.

- [ ] **Step 1: Document the wake-on cursor change**

Add a one-liner where wake-on reply-detection is described: a cove's reply-wake now keys off the **`WaitCursor` log position** captured when it enters Waiting (an external-origin message appended after that position), rather than a wall-clock timestamp; `WaitingSince` still governs the max-wait teardown and warm-timeout idle. Do not duplicate a fact another doc owns — link. No user-facing config changed.

- [ ] **Step 2: Audit**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`, diffing against a clean baseline (stash method) to confirm **no net-new** errors/warnings. Fix any your edit introduces (mind the slugify anchor rule — no em-dashes in linked headings).

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "docs(harbor): wake-on reply-detection keys off the WaitCursor log position

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

## Final verification

- [ ] `just test` — full hermetic suite green (conformance both new + old; egress, wake-on, supervisor).
- [ ] `go build ./... && go build -tags integration ./...` — both compile.
- [ ] `gofmt -l` over changed files empty; `go vet ./...` clean.
- [ ] Skim the diff: existing read methods unchanged; `msglog` core stdlib-only (pgx only in `msglogpg`); egress no longer reads the whole log; `logTailID` no longer reads the whole log; wake-on no longer reads the whole inbox and keys off `WaitCursor`; `WaitingSince` retained for teardown/warm; no ctx added.
- [ ] Open a PR against `main` (branch `feat/harbor-msglog-bounded-reads`) with the attribution block; note the `store-integration` job exercises the new `msglogpg` methods against real Postgres. If `main` advanced (parallel msgport work), merge it in and re-verify before merging.
