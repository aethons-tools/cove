# harbor cove inbox consumer — durable commit cursor + seekable reads (Phase 3b) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the cove's `/messages` read into a durable, acked, seekable queue consumer: a per-cove commit cursor (initialized at raise), seekable reads that never move it, and an explicit commit op that advances it.

**Architecture:** `msglog` gains a backward read (`ReadInboxBefore`) to pair with `ReadInboxSince`. `harbor.Store`/`Instance` gain a durable `CommitCursor` (set at raise to the log tail, advanced atomically by `AdvanceCommitCursor`). `GET /messages` becomes cursor-relative + seekable; `POST /messages/commit` advances the cursor. cove-master's `read` gains seek params and a new `commit` tool.

**Tech Stack:** Go, pgx/v5 (only in `msglogpg`/`pgstore`), the conformance suites + `store-integration` CI, the modelcontextprotocol/go-sdk MCP server.

**Spec:** `docs/superpowers/specs/2026-09-15-harbor-cove-inbox-cursor.md`

## Global Constraints

- **Additive/behavioral scope only where the spec says.** `msglog` core stays stdlib-only (pgx only in `msglogpg`); `Message`/`Target`/`Filter` unchanged; no ctx.
- **Cursor semantics:** ids are lexically ordered == append order. `ReadInboxSince(afterID,…)` = ids > afterID (afterID `""` = from start). `ReadInboxBefore(beforeID,…)` = ids < beforeID, the nearest-below, returned **ascending** (beforeID `""` = from the end). `limit<=0` = unbounded.
- **`CommitCursor` is monotonic-forward**; commit is idempotent; reads never advance it. `AdvanceCommitCursor` is atomic in each store backend.
- **`CommitCursor` is separate from `WaitCursor`** — do not touch wake-on.
- Both store backends behavior-identical (conformance is the oracle: file/memState hermetic; `msglogpg`/`pgstore` behind `integration` → CI Postgres).
- Before every commit: `go build ./... && go build -tags integration ./...`, `gofmt -l` (empty over changed files), `go vet` (both tags where relevant).
- **Commit trailer — verbatim** (do NOT substitute your own model name):

  ```
  Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv
  ```

- Branch is `feat/harbor-cove-inbox-cursor` (the spec is committed there).

## File Structure

- `internal/msglog/store.go`, `read.go`, `msglogpg/msglogpg.go`, `msglogtest/conformance.go` — `ReadInboxBefore` (Task 1).
- `internal/harbor/instance.go` — `CommitCursor` field (Task 2).
- `internal/harbor/filestore.go` (interface), `memstate.go`, `pgstore.go`, `storetest/conformance.go` — `AdvanceCommitCursor` (Task 2).
- `internal/harbor/supervisor.go` (+`_test.go`) — raise-time init (Task 2).
- `internal/harbor/messages.go` — seekable `handleGet` + `handleCommit` + interface changes (Task 3).
- `cmd/at-harbor/mux.go` — route `/messages/commit` (Task 3).
- `cmd/cove-master/mcp.go` (+`_test.go`) — `read` params + `commit` tool (Task 4).
- `docs/usage/harbor/messaging.md` (Task 5).

---

### Task 1: `msglog.ReadInboxBefore` (interface + both backends + conformance)

**Files:** `internal/msglog/store.go`, `read.go`, `msglogpg/msglogpg.go`, `msglogtest/conformance.go`

**Interfaces:** Produces `Store.ReadInboxBefore(t Target, beforeID string, limit int) []Message`.

- [ ] **Step 1: Conformance cases (RED — undefined)**

Append to `internal/msglog/msglogtest/conformance.go` in `RunConformance`:

```go
	t.Run("read_inbox_before", func(t *testing.T) {
		s := newStore(t)
		m1, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "b1"})
		_, _ = s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "b2"})
		m3, _ := s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x")}, Body: "b3"})
		// from the end: last 2, ascending.
		end := s.ReadInboxBefore(actor("x"), "", 2)
		if len(end) != 2 || end[0].Body != "b2" || end[1].Body != "b3" {
			t.Fatalf("ReadInboxBefore(x,\"\",2) = %+v, want [b2,b3]", end)
		}
		// before m3 (exclusive): the nearest-below, ascending.
		before := s.ReadInboxBefore(actor("x"), m3.ID, 10)
		if len(before) != 2 || before[0].Body != "b1" || before[1].Body != "b2" {
			t.Fatalf("ReadInboxBefore(x,m3,10) = %+v, want [b1,b2]", before)
		}
		// before m1: nothing (exclusive anchor).
		if none := s.ReadInboxBefore(actor("x"), m1.ID, 10); len(none) != 0 {
			t.Fatalf("ReadInboxBefore(x,m1) = %+v, want empty", none)
		}
	})

	t.Run("read_inbox_before_multi_recipient", func(t *testing.T) {
		s := newStore(t)
		_, _ = s.Append(msglog.Message{From: actor("c1"), To: []msglog.Target{actor("x"), actor("y")}, Body: "shared"})
		if got := s.ReadInboxBefore(actor("x"), "", 10); len(got) != 1 || len(got[0].To) != 2 {
			t.Fatalf("multi-recipient before(x) = %+v", got)
		}
		if got := s.ReadInboxBefore(actor("y"), "", 10); len(got) != 1 {
			t.Fatalf("multi-recipient before(y) = %+v", got)
		}
	})
```

- [ ] **Step 2: Run — RED** (`go test ./internal/msglog/ -run TestFileLogConformance`; undefined method).

- [ ] **Step 3: Interface** — add to `Store` in `store.go`:

```go
	// ReadInboxBefore returns messages addressed to t with id < beforeID, the
	// `limit` nearest below beforeID, in append (ascending) order (limit <= 0 =
	// unbounded). beforeID == "" means "from the end" (the last `limit`). Pairs
	// with ReadInboxSince for backward paging: next-backward from a page is
	// ReadInboxBefore(t, page.first, limit).
	ReadInboxBefore(t Target, beforeID string, limit int) []Message
```

- [ ] **Step 4: File backend** — in `read.go` (scan the append-ordered mirror, collect matches with id < beforeID, keep the last `limit`, return ascending):

```go
func (l *Log) ReadInboxBefore(t Target, beforeID string, limit int) []Message {
	var out []Message
	for _, m := range l.snapshot() {
		if beforeID != "" && m.ID >= beforeID {
			continue
		}
		for _, r := range m.To {
			if r == t {
				out = append(out, m)
				break
			}
		}
	}
	// keep the last `limit` (nearest below beforeID), preserving ascending order.
	if limit > 0 && len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
```

- [ ] **Step 5: Postgres backend** — in `msglogpg/msglogpg.go` (select nearest-below via DESC+LIMIT, then reverse to ascending; reuse `scanMessages`):

```go
func (s *Store) ReadInboxBefore(t msglog.Target, beforeID string, limit int) []msglog.Message {
	// nearest-below beforeID: order DESC + LIMIT, then reverse to ascending.
	sql := `SELECT m.id, m.from_kind, m.from_ref, m.body, m.at, m.project, m.reply_to, m."to"
	        FROM messages m JOIN message_recipients r ON r.message_id = m.id
	        WHERE r.kind = $1 AND r.ref = $2`
	args := []any{t.Kind, t.Ref}
	if beforeID != "" {
		sql += ` AND m.id < $3`
		args = append(args, beforeID)
	}
	sql += ` ORDER BY m.id DESC`
	if limit > 0 {
		sql += fmt.Sprintf(" LIMIT %d", limit)
	}
	out := s.query(sql, args...)
	// reverse to ascending.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
```

- [ ] **Step 6: Run GREEN + build both tags + gofmt/vet**

`go test ./internal/msglog/...`; `go build ./... && go build -tags integration ./...`; `gofmt -l internal/msglog`; `go vet ./internal/msglog/... && go vet -tags integration ./internal/msglog/...`.

- [ ] **Step 7: Commit**

```bash
git add internal/msglog/
git commit -m "msglog: add backward ReadInboxBefore (file + postgres + conformance)

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 2: `CommitCursor` — Instance field, `AdvanceCommitCursor` (both store backends), raise-time init

**Files:** `internal/harbor/instance.go`, `filestore.go`, `memstate.go`, `pgstore.go`, `storetest/conformance.go`, `supervisor.go`, `supervisor_test.go`

**Interfaces:** Produces `harbor.Store.AdvanceCommitCursor(actorID, upTo string) (Instance, error)` and `Instance.CommitCursor`.

- [ ] **Step 1: Store conformance case (RED)**

Append to `internal/harbor/storetest/conformance.go` in `RunConformance`:

```go
	t.Run("advance_commit_cursor", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutInstance(harbor.Instance{ActorID: "cove-1", Phase: harbor.PhaseLive}); err != nil {
			t.Fatal(err)
		}
		inst, err := s.AdvanceCommitCursor("cove-1", "id-5")
		if err != nil || inst.CommitCursor != "id-5" {
			t.Fatalf("advance to id-5 = %+v, %v", inst.CommitCursor, err)
		}
		// monotonic: a backward/equal up_to is a no-op success.
		inst, err = s.AdvanceCommitCursor("cove-1", "id-3")
		if err != nil || inst.CommitCursor != "id-5" {
			t.Fatalf("backward advance must no-op: %+v, %v", inst.CommitCursor, err)
		}
		inst, err = s.AdvanceCommitCursor("cove-1", "id-9")
		if err != nil || inst.CommitCursor != "id-9" {
			t.Fatalf("forward advance = %+v, %v", inst.CommitCursor, err)
		}
		if _, err := s.AdvanceCommitCursor("absent", "id-1"); err == nil {
			t.Fatal("advance on an absent actor must error")
		}
	})
```

- [ ] **Step 2: Run — RED** (`go test ./internal/harbor/ -run TestFileStoreConformance`).

- [ ] **Step 3: `Instance` field** — in `instance.go`, add after `WaitCursor`:

```go
	CommitCursor string `json:"commit_cursor,omitempty"` // durable inbox consume offset: last message id the cove has committed as processed; set at raise to the log tail, advanced by /messages/commit
```

- [ ] **Step 4: Interface + both backends**

In `filestore.go`, add to the `Store` interface (near the instance methods):

```go
	AdvanceCommitCursor(actorID, upTo string) (Instance, error) // monotonic forward; no-op if upTo <= current; error if actor absent
```

Add the shared mutation helper on `memState` (lock-free; returns the updated instance + found), in `memstate.go`:

```go
// applyAdvanceCommitCursor moves the instance's CommitCursor to upTo iff it is
// forward of the current value; returns the (possibly unchanged) instance and
// whether the actor exists. Caller holds the write lock.
func (m *memState) applyAdvanceCommitCursor(actorID, upTo string) (Instance, bool) {
	i, ok := m.instances[actorID]
	if !ok {
		return Instance{}, false
	}
	if upTo > i.CommitCursor {
		i.CommitCursor = upTo
		m.instances[actorID] = i
	}
	return i, true
}
```

`FileStore.AdvanceCommitCursor` (mutate under `mu`, persist only when it actually changed — but always safe to save):

```go
func (fs *FileStore) AdvanceCommitCursor(actorID, upTo string) (Instance, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	i, ok := fs.applyAdvanceCommitCursor(actorID, upTo)
	if !ok {
		return Instance{}, fmt.Errorf("instance %q not found", actorID)
	}
	return i, fs.save()
}
```

`PostgresStore.AdvanceCommitCursor` (atomic guarded UPDATE, then cache-update via the same helper; the WHERE clause enforces monotonicity in the DB too):

```go
func (s *PostgresStore) AdvanceCommitCursor(actorID, upTo string) (Instance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.instances[actorID]; !ok {
		return Instance{}, fmt.Errorf("instance %q not found", actorID)
	}
	// Advance only when forward; jsonb_set the single field. No-op UPDATE (0 rows)
	// when upTo <= current is fine — the cache helper below is the source of truth
	// for the returned value and also no-ops on a non-forward upTo.
	if err := s.exec("AdvanceCommitCursor",
		`UPDATE instances SET doc = jsonb_set(doc, '{commit_cursor}', to_jsonb($2::text)), version = version + 1, updated_at = now()
		 WHERE actor_id = $1 AND coalesce(doc->>'commit_cursor','') < $2`,
		actorID, upTo); err != nil {
		return Instance{}, err
	}
	i, _ := s.applyAdvanceCommitCursor(actorID, upTo)
	return i, nil
}
```

(`applyAdvanceCommitCursor` lives on `memState`, shared by both stores — add it once in `memstate.go` as above.)

- [ ] **Step 5: Raise-time init** — in `supervisor.go` `Raise`, before the instance's first `PutInstance` (~line 147), set the baseline:

```go
	inst.CommitCursor = s.tailID()
```

(`s.tailID()` already exists from Phase 3a; `""` when no tail reader.)

- [ ] **Step 6: Tests**

- Run the store conformance (both file via `TestFileStoreConformance`; Postgres via the integration tag in CI). Confirm GREEN on the file backend.
- Add a supervisor test: with a fake tail reader returning `"tail-7"`, `Raise` produces an instance with `CommitCursor == "tail-7"`; with no tail reader, `""`. (Mirror the existing `WaitCursor` raise/waiting tests.)

- [ ] **Step 7: Verify + commit**

`go build ./... && go build -tags integration ./...`; `gofmt -l internal/harbor`; `go vet` both tags; `go test ./internal/harbor/...`.

```bash
git add internal/harbor/
git commit -m "harbor: durable CommitCursor — Instance field, AdvanceCommitCursor (both backends), raise init

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 3: `/messages` seekable GET + `POST /messages/commit`

**Files:** `internal/harbor/messages.go`, `internal/harbor/messages_test.go`, `cmd/at-harbor/mux.go`

**Interfaces:** Consumes `msglog.ReadInboxSince`/`ReadInboxBefore` (Task 1) and `Store.AdvanceCommitCursor` (Task 2). Produces the seekable GET + commit HTTP contract for Task 4.

- [ ] **Step 1: Widen the handler's narrow interfaces**

In `messages.go`, change `inboxReader` and `messagesStore`:

```go
type inboxReader interface {
	ReadInboxSince(t msglog.Target, afterID string, limit int) []msglog.Message
	ReadInboxBefore(t msglog.Target, beforeID string, limit int) []msglog.Message
}
```

Add to `messagesStore`:

```go
	AdvanceCommitCursor(actorID, upTo string) (Instance, error)
```

(`*msglog.Log`, `*msglogpg.Store`, and `harbor.Store` already satisfy these after Tasks 1–2.)

- [ ] **Step 2: Seekable `handleGet`**

Replace `handleGet` body with anchor/dir/limit parsing (default = next N after the cove's `CommitCursor`):

```go
const (
	defaultReadLimit = 50
	maxReadLimit     = 500
)

func (h *MessagesHandler) handleGet(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) {
	if h.reader == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	anchor := q.Get("anchor") // "", cursor, start, end, id
	dir := q.Get("dir")       // "", forward, backward
	limit := defaultReadLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = n
	}
	if limit > maxReadLimit {
		limit = maxReadLimit
	}
	target := msglog.Target{Kind: "actor", Ref: actor.ID}

	var msgs []msglog.Message
	switch anchor {
	case "", "cursor":
		if dir == "backward" {
			msgs = h.reader.ReadInboxBefore(target, inst.CommitCursor, limit)
		} else {
			msgs = h.reader.ReadInboxSince(target, inst.CommitCursor, limit)
		}
	case "start":
		msgs = h.reader.ReadInboxSince(target, "", limit)
	case "end":
		msgs = h.reader.ReadInboxBefore(target, "", limit)
	case "id":
		id := q.Get("id")
		if id == "" {
			http.Error(w, "anchor=id requires id", http.StatusBadRequest)
			return
		}
		if dir == "backward" {
			msgs = h.reader.ReadInboxBefore(target, id, limit)
		} else {
			msgs = h.reader.ReadInboxSince(target, id, limit)
		}
	default:
		http.Error(w, "invalid anchor", http.StatusBadRequest)
		return
	}

	out := make([]Comment, 0, len(msgs))
	for i := range msgs {
		m := msgs[i]
		at := m.At
		out = append(out, Comment{ID: m.ID, Author: m.From.Ref, Body: m.Body, At: &at})
	}
	pageFirst, pageLast := "", ""
	if len(msgs) > 0 {
		pageFirst, pageLast = msgs[0].ID, msgs[len(msgs)-1].ID
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "read", "anchor", anchor, "dir", dir, "count", len(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Messages        []Comment `json:"messages"`
		CommittedCursor string    `json:"committed_cursor"`
		PageFirst       string    `json:"page_first"`
		PageLast        string    `json:"page_last"`
	}{Messages: out, CommittedCursor: inst.CommitCursor, PageFirst: pageFirst, PageLast: pageLast}); err != nil {
		h.log.Error("messages: encode response failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
	}
}
```

Add `"strconv"` to the imports.

- [ ] **Step 3: `handleCommit` + dispatch**

Add a commit handler:

```go
func (h *MessagesHandler) handleCommit(w http.ResponseWriter, r *http.Request, actor Actor) {
	r.Body = http.MaxBytesReader(w, r.Body, maxMessageBodyBytes)
	var req struct {
		UpTo string `json:"up_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UpTo == "" {
		http.Error(w, "up_to required", http.StatusBadRequest)
		return
	}
	inst, err := h.store.AdvanceCommitCursor(actor.ID, req.UpTo)
	if err != nil {
		http.Error(w, "no instance", http.StatusForbidden)
		return
	}
	h.log.Info("messages", "actor", actor.ID, "op", "commit", "up_to", req.UpTo, "committed", inst.CommitCursor)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		CommittedCursor string `json:"committed_cursor"`
	}{CommittedCursor: inst.CommitCursor}); err != nil {
		h.log.Error("messages: encode commit response failed", "actor", actor.ID, "error", err.Error())
	}
}
```

In `ServeHTTP`, dispatch `POST …/commit` before the general POST (mirror the `/targets` suffix check). After the `/targets` block and before ticket resolution is fine — commit needs no ticket:

```go
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/commit") {
		h.handleCommit(w, r, actor)
		return
	}
```

(Place this right after the existing `GET …/targets` early-return, so neither needs the ticket resolution below.)

- [ ] **Step 4: Route `/messages/commit` in the mux**

In `cmd/at-harbor/mux.go`, add the path to the msgH case:

```go
		case "/messages", "/messages/targets", "/messages/commit":
			msgH.ServeHTTP(w, r)
```

- [ ] **Step 5: Handler tests** (`internal/harbor/messages_test.go`, hermetic — file `*msglog.Log` reader + a fake `messagesStore` implementing `AdvanceCommitCursor`)

Cover: default GET = next-N-after-CommitCursor (seed an instance with a mid-log CommitCursor); `anchor=start`/`end`/`id`+`dir` map to the right windows; `limit` capped + invalid → 400; response carries `committed_cursor`/`page_first`/`page_last`; a GET does **not** change CommitCursor; `POST /messages/commit {up_to}` calls `AdvanceCommitCursor` and returns the new cursor; missing `up_to` → 400; the actor id is taken from the token, not the body. Keep the existing send/targets tests working (the reader interface change may require the test's fake reader to implement the two new methods — update it).

- [ ] **Step 6: Verify + commit**

`go build ./... && go build -tags integration ./...`; `gofmt -l internal/harbor cmd/at-harbor`; `go vet`; `go test ./internal/harbor/... ./cmd/at-harbor/...`.

```bash
git add internal/harbor/messages.go internal/harbor/messages_test.go cmd/at-harbor/mux.go
git commit -m "harbor: /messages seekable GET (cursor-relative) + POST /messages/commit

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 4: cove-master MCP — `read` seek params + `commit` tool

**Files:** `cmd/cove-master/mcp.go`, `cmd/cove-master/mcp_test.go`

**Interfaces:** Consumes the HTTP contract from Task 3.

- [ ] **Step 1: `read` gains seek params + richer output**

Change `readIn`/`readOut` and `read`:

```go
type readIn struct {
	Anchor string `json:"anchor,omitempty" jsonschema:"where to read from: cursor (default; your commit position), start, end, or id"`
	ID     string `json:"id,omitempty" jsonschema:"message id anchor, required when anchor=id"`
	Dir    string `json:"dir,omitempty" jsonschema:"direction from the anchor: forward (default, oldest-first) or backward"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max messages to return (default 50)"`
}

type readOut struct {
	Messages        []messageOut `json:"messages"`
	CommittedCursor string       `json:"committed_cursor,omitempty"`
	PageFirst       string       `json:"page_first,omitempty"`
	PageLast        string       `json:"page_last,omitempty"`
}
```

`read` builds a query string from non-empty params and forwards it:

```go
func (c *messagingClient) read(ctx context.Context, in readIn) (readOut, error) {
	q := url.Values{}
	if in.Anchor != "" {
		q.Set("anchor", in.Anchor)
	}
	if in.ID != "" {
		q.Set("id", in.ID)
	}
	if in.Dir != "" {
		q.Set("dir", in.Dir)
	}
	if in.Limit > 0 {
		q.Set("limit", strconv.Itoa(in.Limit))
	}
	path := "/messages"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return readOut{}, err
	}
	var out readOut
	if err := json.Unmarshal(body, &out); err != nil {
		return readOut{}, fmt.Errorf("decoding harbor messages response")
	}
	return out, nil
}
```

Update the `read` `AddTool` handler to pass its typed `readIn` through, and its description: "Read your inbox as a queue. Default: the next unprocessed messages after your commit cursor (oldest first). Use anchor/dir/limit to seek (start/end/id, forward/backward). Reading does NOT mark anything processed — call `commit` for that." Add `"net/url"`/`"strconv"` imports as needed.

- [ ] **Step 2: New `commit` tool + client**

```go
type commitIn struct {
	UpTo string `json:"up_to" jsonschema:"the message id you have processed up to; advances your durable read cursor so these messages are not handed to you again"`
}
type commitOut struct {
	CommittedCursor string `json:"committed_cursor,omitempty"`
}

func (c *messagingClient) commit(ctx context.Context, upTo string) (commitOut, error) {
	payload, err := json.Marshal(struct {
		UpTo string `json:"up_to"`
	}{UpTo: upTo})
	if err != nil {
		return commitOut{}, err
	}
	body, err := c.do(ctx, http.MethodPost, "/messages/commit", payload)
	if err != nil {
		return commitOut{}, err
	}
	var out commitOut
	if err := json.Unmarshal(body, &out); err != nil {
		return commitOut{}, fmt.Errorf("decoding harbor commit response")
	}
	return out, nil
}
```

Register the tool (mirror the existing `mcp.AddTool` calls): name `commit`, description "Confirm you've processed your inbox up to this message id; advances your durable read cursor so you won't be handed those messages again. Call it after you've durably handled them."

- [ ] **Step 3: Tests** (`cmd/cove-master/mcp_test.go`)

With the existing httptest-based harness: `read` with no params still hits `GET /messages` and decodes the new fields; `read` with `anchor=end`/`dir`/`limit` sends the right query string; `commit` POSTs `{up_to}` to `/messages/commit` and decodes `committed_cursor`. Keep existing send/read/targets tests working (the `read` signature gained a `readIn` arg — update call sites).

- [ ] **Step 4: Verify + commit**

`go build ./...`; `gofmt -l cmd/cove-master`; `go vet ./cmd/cove-master/`; `go test ./cmd/cove-master/...`.

```bash
git add cmd/cove-master/
git commit -m "cove-master: read gains seek params; new commit tool advances the durable cursor

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

### Task 5: Docs + final verify

**Files:** `docs/usage/harbor/messaging.md`

- [ ] **Step 1: Document the consumer model** in `messaging.md` (owner of the read/messaging mechanics): the cove inbox is a **durable queue** — a `CommitCursor` per cove (initialized at raise to the log tail, separate from `WaitCursor`), seekable reads (`anchor` cursor/start/end/id × `dir` forward/backward × `limit`; default = next N after the cursor, oldest-first; reads don't commit), and `POST /messages/commit {up_to}` to advance the cursor (monotonic). Note date anchoring is a future addition and that nothing is ever pruned. Link, don't duplicate `serve.md`/`coves.md` facts.

- [ ] **Step 2: docs-audit** — `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`, diffed against a merge-base baseline (worktree method); no net-new beyond the spec/plan frontmatter/orphan lines (the repo-universal pattern for `superpowers/`).

- [ ] **Step 3: Commit**

```bash
git add docs/usage/harbor/messaging.md
git commit -m "docs(harbor): cove inbox is a durable queue — commit cursor, seekable reads, commit op

$(printf 'Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>\nClaude-Session: https://claude.ai/code/session_01BtTj5yZ4qoJzyK8o6625mv')"
```

---

## Final verification

- [ ] `just test` — full hermetic suite green (msglog + store conformance, supervisor, messages handler, cove-master).
- [ ] `go build ./... && go build -tags integration ./...`; `gofmt -l` empty; `go vet ./...` clean.
- [ ] Skim the diff: `CommitCursor` separate from `WaitCursor`; reads never advance it; commit is monotonic + atomic in both backends; default `read` = next-N-after-cursor; `msglog` core stdlib-only; no ctx; no pruning.
- [ ] Open a PR against `main` (branch `feat/harbor-cove-inbox-cursor`); note `store-integration` exercises `ReadInboxBefore` + `AdvanceCommitCursor` on real Postgres. If `main` advanced, merge it in and re-verify (watch for parallel edits to `messages.go`/`mux.go`/the store).
