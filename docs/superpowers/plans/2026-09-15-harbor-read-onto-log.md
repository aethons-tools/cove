# msgport Slice 4 — read/GET onto the Log Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the `/messages` GET/read path off direct Linear reads onto the Log (the cove reads its inbox), making the whole `/messages` handler tracker-independent.

**Architecture:** `MessagesHandler` swaps its `Commenter` (Linear) dependency for a nil-safe `inboxReader` over `*msglog.Log`; `handleGet` returns `ReadInbox(actor:<coveID>)` mapped to the existing `Comment` shape; `ServeHTTP` stops resolving the ticket. Escalation is untouched (stays direct — its own future slice).

**Tech Stack:** Go; `internal/harbor` (core, msglog+stdlib only), `cmd/at-harbor` (wiring), hermetic tests via a real temp-dir `*msglog.Log`.

## Global Constraints

- `internal/harbor` core must gain NO new kit/grpc/dispatch/backend/connect import. The reader is a local interface satisfied by `*msglog.Log` (already an allowed import via 1a).
- Self-scope: the read target derives ONLY from the authenticated `actor.ID`, never a client field.
- Secrets/tokens/message bodies NEVER in a log line (read logs only `count`).
- Read response JSON shape is unchanged: `{"messages":[{"id","author","body","at"}]}` (the MCP `read` tool parses it). Empty → `[]`, never `null`.
- `Author` for an inbound message = `m.From.Ref` (1b Route stamps `From = human:<displayName>`).
- Tests hermetic (no network, no live Linear). `-race` not runnable in-sandbox (no cgo) — reason manually. Build offline: `GOPROXY=off`.
- The pre-existing uncommitted `.at-cove/config.yml` (and untracked `.at-cove/example-kitconfig.yml`, `.claude/`) stay OUT of every commit — stage files explicitly by path.

---

## File Structure

- `internal/harbor/messages.go` — swap `Commenter`→`inboxReader`; rewrite `handleGet`; simplify `ServeHTTP`; remove `Commenter` interface; keep `Comment`.
- `internal/harbor/messages_test.go` — rewrite read tests to a real `*msglog.Log`; update the handler-construction helper to the new signature.
- `cmd/at-harbor/main.go` — the two `NewMessagesHandler` call sites (pass `messageLog` as reader+appender; drop `linearCommenter{tracker}`).
- `docs/usage/harbor/messaging.md` — read is Log-backed / tracker-independent / inbox semantics.

---

## Task 1: `MessagesHandler` reads the Log inbox

**Files:**
- Modify: `internal/harbor/messages.go` (field/param swap, `handleGet`, `ServeHTTP`, remove `Commenter`)
- Test: `internal/harbor/messages_test.go` (read tests + construction helper)

**Interfaces:**
- Consumes: `msglog.Message{ID,From,To,Body,At}`, `msglog.Target{Kind,Ref}`, `(*msglog.Log).ReadInbox(msglog.Target) []msglog.Message`, `(*msglog.Log).Append`.
- Produces: `NewMessagesHandler(store messagesStore, reader inboxReader, lg appender, log *slog.Logger) *MessagesHandler`; `inboxReader` interface. Task 2 supplies the concrete reader (`*msglog.Log`) at the call site.

- [ ] **Step 1: Rewrite the read tests (failing)**

In `internal/harbor/messages_test.go`. First check the existing construction helper (search `NewMessagesHandler(` in the test file, likely a helper like `newTestMessagesHandler(...)`). Update it (and any inline constructions) to the new signature `NewMessagesHandler(store, reader, appender, log)`.

Add a helper to build a real Log + handler and enroll one actor+instance. Reuse the existing store/token/actor setup that the POST tests use (e.g. whatever `TestMessagesPostIsSelfScoped` builds — an actor with a bearer token and a `GetInstance` returning `Unit`). Then:

```go
func TestReadReturnsInbox(t *testing.T) {
	lg, err := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// two inbound replies to the cove + one of the cove's OWN outbound (must be excluded)
	coveActor := msglog.Target{Kind: "actor", Ref: "cove-1"}
	mustAppend(t, lg, msglog.Message{From: msglog.Target{Kind: "human", Ref: "Alice"}, To: []msglog.Target{coveActor}, Body: "first", Project: "acme"})
	mustAppend(t, lg, msglog.Message{From: coveActor, To: []msglog.Target{{Kind: "channel", Ref: "ACME-7"}}, Body: "my own send", Project: "acme"})
	mustAppend(t, lg, msglog.Message{From: msglog.Target{Kind: "human", Ref: "Alice"}, To: []msglog.Target{coveActor}, Body: "second", Project: "acme"})

	// store: actor "cove-1" with a token, instance Unit "ACME-7"
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger(nil))

	rec := doGet(t, h, tokenFor("cove-1")) // GET /messages with the actor's bearer
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Messages []Comment `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (own send excluded); got %+v", len(resp.Messages), resp.Messages)
	}
	if resp.Messages[0].Author != "Alice" || resp.Messages[0].Body != "first" {
		t.Fatalf("msg0 = %+v, want Alice/first", resp.Messages[0])
	}
	if resp.Messages[1].Body != "second" {
		t.Fatalf("msg1 = %+v, want second", resp.Messages[1])
	}
	for _, m := range resp.Messages {
		if m.ID == "" || m.At == nil {
			t.Fatalf("id/at missing: %+v", m)
		}
	}
}

func TestReadEmptyInboxIsEmptyArray(t *testing.T) {
	lg, _ := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger(nil))
	rec := doGet(t, h, tokenFor("cove-1"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"messages":[]`) {
		t.Fatalf("body = %s, want empty array (not null)", got)
	}
}

func TestReadNilReaderIs503(t *testing.T) {
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, nil, nil, testLogger(nil))
	rec := doGet(t, h, tokenFor("cove-1"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestReadIsSelfScoped(t *testing.T) {
	lg, _ := msglog.Open(filepath.Join(t.TempDir(), "log.jsonl"), nil)
	// an inbound to a DIFFERENT cove
	mustAppend(t, lg, msglog.Message{From: msglog.Target{Kind: "human", Ref: "Bob"}, To: []msglog.Target{{Kind: "actor", Ref: "cove-2"}}, Body: "for cove-2", Project: "acme"})
	store := newReadTestStore(t, "cove-1", "ACME-7", "acme")
	h := NewMessagesHandler(store, lg, lg, testLogger(nil))
	rec := doGet(t, h, tokenFor("cove-1"))
	var resp struct{ Messages []Comment `json:"messages"` }
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Messages) != 0 {
		t.Fatalf("cove-1 must not see cove-2's inbox; got %+v", resp.Messages)
	}
}
```

Adapt helper names (`mustAppend`, `newReadTestStore`, `doGet`, `tokenFor`, `testLogger`) to whatever the existing test file already provides — REUSE existing helpers; only add the small ones that don't exist. In particular find how existing tests issue an authenticated request and how they build the store/token; mirror that exactly. `msglog.Open`'s real signature must be confirmed (Slice 2/3 used it — check whether it's `Open(path)` or `Open(path, opts)`); adjust the calls.

Keep the existing GET `/messages/targets` test and all 401/403/405/never-logs-token tests; only the ticket-comment read assertions change.

- [ ] **Step 2: Run — verify failing**

Run: `GOPROXY=off go test ./internal/harbor/ -run TestRead`
Expected: compile errors (`NewMessagesHandler` arity; `inboxReader` undefined) / failures.

- [ ] **Step 3: Swap the handler dependency**

In `internal/harbor/messages.go`:

- Add the reader interface and update the struct + constructor:

```go
// inboxReader is the narrow read side of the message Log the /messages GET
// path needs. Satisfied by *msglog.Log; nil disables reads (GET → 503).
type inboxReader interface {
	ReadInbox(t msglog.Target) []msglog.Message
}

type MessagesHandler struct {
	store  messagesStore
	reader inboxReader
	lg     appender
	log    *slog.Logger
}

// NewMessagesHandler constructs a MessagesHandler. reader and lg may each be
// nil: a nil lg makes a send fail 503, a nil reader makes a read fail 503.
func NewMessagesHandler(store messagesStore, reader inboxReader, lg appender, log *slog.Logger) *MessagesHandler {
	return &MessagesHandler{store: store, reader: reader, lg: lg, log: log}
}
```

- **Remove** the `Commenter` interface entirely. **Keep** the `Comment` type; update its doc comment to drop "as returned by a Commenter" (e.g. "Comment is one message in a cove's inbox, as returned by GET /messages.").

- [ ] **Step 4: Rewrite `handleGet`**

Replace `handleGet` with the inbox-reading version (drops the `issueID` param):

```go
func (h *MessagesHandler) handleGet(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) {
	if h.reader == nil {
		http.Error(w, "messaging not configured", http.StatusServiceUnavailable)
		return
	}
	msgs := h.reader.ReadInbox(msglog.Target{Kind: "actor", Ref: actor.ID})
	out := make([]Comment, 0, len(msgs))
	for i := range msgs {
		m := msgs[i]
		at := m.At
		out = append(out, Comment{ID: m.ID, Author: m.From.Ref, Body: m.Body, At: &at})
	}
	h.log.Info("messages", "actor", actor.ID, "ticket", inst.Unit, "op", "read", "count", len(out))
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Messages []Comment `json:"messages"`
	}{Messages: out}); err != nil {
		h.log.Error("messages: encode response failed", "actor", actor.ID, "ticket", inst.Unit, "error", err.Error())
	}
}
```

- [ ] **Step 5: Simplify `ServeHTTP`**

Remove the `issueID, err := h.cmt.IssueByIdentifier(...)` block and update dispatch:

```go
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r, actor, inst)
	case http.MethodGet:
		h.handleGet(w, r, actor, inst)
	}
```

Confirm `inst` is still used (log context / the earlier 403-no-instance gate stays). Confirm no now-unused imports remain (e.g. if nothing else uses a symbol the removed block referenced). `msglog` is used by `handleGet`; `json`/`http`/`time` still used.

- [ ] **Step 6: Run — verify pass**

Run: `GOPROXY=off go test ./internal/harbor/`
Expected: PASS. Also `GOPROXY=off go build ./...` — expect the ONLY breakage to be `cmd/at-harbor/main.go` (the two `NewMessagesHandler` calls with the old arity), fixed in Task 2. If `internal/harbor` itself builds+tests green, proceed.

- [ ] **Step 7: Commit**

```bash
git add internal/harbor/messages.go internal/harbor/messages_test.go
git commit -m "harbor: /messages read from the Log inbox — drop the tracker dep (COV-180)"
```

(Note: the module build is briefly red until Task 2 fixes the call sites — that's expected and the two tasks are one logical change. The `internal/harbor` package and its tests are green at this commit.)

---

## Task 2: cmd wiring + docs

**Files:**
- Modify: `cmd/at-harbor/main.go` (two `NewMessagesHandler` call sites)
- Modify: `docs/usage/harbor/messaging.md`

**Interfaces:**
- Consumes: `harbor.NewMessagesHandler(store, reader, appender, log)`; `messageLog *msglog.Log`.

- [ ] **Step 1: Fix the wiring**

In `cmd/at-harbor/main.go`, the two call sites (~1105-1112) currently read:

```go
		if messageLog != nil {
			msgH = harbor.NewMessagesHandler(st, linearCommenter{tracker}, messageLog, log)
		} else {
			msgH = harbor.NewMessagesHandler(st, linearCommenter{tracker}, nil, log)
		}
```

Replace with (pass `messageLog` as BOTH reader and appender; keep the typed-nil guard — pass untyped `nil` in the unconfigured branch):

```go
		if messageLog != nil {
			msgH = harbor.NewMessagesHandler(st, messageLog, messageLog, log)
		} else {
			msgH = harbor.NewMessagesHandler(st, nil, nil, log)
		}
```

- Confirm `linearCommenter` is still referenced elsewhere (the `escalate.New(...)` wiring — grep `linearCommenter`). It must remain defined and used by escalation; only its use in the `NewMessagesHandler` calls goes away. If the compiler flags `linearCommenter` or `tracker` as unused after this, that means escalation isn't using it — STOP and report (do not delete escalation's dependency).

- [ ] **Step 2: Build + full test**

Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./...`
Expected: PASS across the module.

- [ ] **Step 3: Boundary check**

Run: `GOPROXY=off go list -deps ./internal/harbor | grep -iE 'grpc|dispatch|/kit|backend|connect'`
Expected: empty (the `Commenter` removal only reduces surface; the reader is a local interface).

- [ ] **Step 4: Docs**

In `docs/usage/harbor/messaging.md`, update the section that OWNS the read/`GET` description:

> **Read is Log-backed and tracker-independent.** `GET /messages` returns the cove's **inbox** — the inbound messages addressed to it (human replies), from harbor's durable message-log — not a live Linear query. It returns messages sent **to** the cove (not the cove's own sent messages), and reflects the Log from when ingestion began; pre-Log ticket history is not included. Because wake-on only wakes a cove once a reply is in the Log, the reply is always present by the time the cove reads. A read requires a configured message-log (none → `503`).

Match the doc's structure/voice; don't duplicate the send-path description added in Slice 3. Bump the `updated:` frontmatter to 2026-09-15.

- [ ] **Step 5: docs-audit (delta only)**

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs --index OVERVIEW.md`
Expected: no NEW orphans/dangling links vs the pre-existing baseline (only an existing doc edited). Report the result.

- [ ] **Step 6: Commit**

```bash
git add cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git commit -m "harbor: wire /messages read to the Log + docs — read cutover complete (COV-180)"
```

---

## Self-Review

**Spec coverage:** §1 reader swap → Task 1 Step 3. §2 handleGet → Task 1 Step 4. §3 ServeHTTP → Task 1 Step 5. §4 semantics → tests (Task 1) + docs (Task 2). §5 wiring → Task 2 Step 1. §6 docs → Task 2 Step 4. `Commenter` removal → Task 1 Step 3. All covered.

**Placeholder scan:** test helper names are flagged as "reuse existing / adapt" with concrete fallbacks — the implementer must confirm against the real test file (its exact auth-request + store-building helpers). `msglog.Open` signature flagged for confirmation.

**Type consistency:** `inboxReader.ReadInbox(msglog.Target) []msglog.Message` matches `*msglog.Log` (the same primitive wake-on's `Inbox` uses — Slice 2). `NewMessagesHandler(store, reader, appender, log)` arity matches both Task 2 call sites. `Comment{ID,Author,Body,At *time.Time}` unchanged; `At: &at` uses a per-iteration copy. `handleGet(w,r,actor,inst)` (no issueID) matches the `ServeHTTP` dispatch.

**Risks flagged for review:** (1) confirm `Commenter` truly has no other referent in `internal/harbor` before removal (grep done in planning: only messages.go); (2) confirm `linearCommenter`/`tracker` stay used by escalation after dropping them from the handler calls; (3) the read semantics change (inbox, not full thread) is intended per spec §4 — the reviewer should confirm tests lock in "own outbound excluded" and self-scope.
