# harbor: msgport Slice 4 — read/GET onto the Log (COV-180)

**Status:** design approved (brainstorm + read-only scope fork), pre-plan
**Issue:** COV-180. **Foundation:** COV-176 (egress cutover — POST already append-only + tracker-independent), COV-174 (1b ingress — fills the Log inbox), COV-175 (Slice 2 — wake-on reads the Log; establishes the `ReadInbox` primitive), COV-171 (msglog).

## Summary

Move the `/messages` **GET/read** path off direct Linear reads and onto the Log. A cove's `read` returns its **inbox** — `ReadInbox(actor:<coveID>)`, the inbound human replies 1b ingress appends — instead of `Commenter.Comments(issueID)`. This makes read **tracker-independent** (like POST after Slice 3) and completes the story: after this slice the `/messages` handler has **zero** direct tracker dependency. **Escalation stays direct** (its own future slice — see the scope decision). Only the read path changes.

## Scope decision (from brainstorm)

Slice 4 = **read/GET only**. Escalation-onto-Log is deferred to its own slice: it carries a real authorship/rendering question (harbor-authored ping → who is `From`; egress delivers one comment per target, so a multi-human tier becomes N comments) that pairs naturally with Discord's fan-out rendering in Slice 5. The read path has none of that.

## 1. `MessagesHandler`: Log reader replaces the tracker (`internal/harbor/messages.go`)

Replace the `cmt Commenter` field/param with a nil-safe reader — the **same primitive wake-on uses** (`ReadInbox`):

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

- **Remove** the `Commenter` interface (confirmed used only here in `internal/harbor`). **Keep** the `Comment` type — it stays the read response shape. Update `Comment`'s doc comment (no longer "as returned by a Commenter").

## 2. `handleGet` reads the inbox (`internal/harbor/messages.go`)

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

- `Author = m.From.Ref` — for an inbound reply, 1b Route stamped `From = human:<displayName>`, so `From.Ref` is the human's name (parity with the old `Comment.Author`).
- `at := m.At` inside the loop (fresh copy per iteration) — never take `&m.At` of a range variable.
- Self-scoped by construction: the inbox target is built from the authenticated `actor.ID`, never a client field. A cove cannot read another cove's inbox.
- Signature drops `issueID` (no longer used).

## 3. `ServeHTTP` stops resolving the ticket (`internal/harbor/messages.go`)

Remove the `issueID, err := h.cmt.IssueByIdentifier(...)` block entirely (POST already ignored it; GET now reads the Log). Dispatch becomes:

```go
	switch r.Method {
	case http.MethodPost:
		h.handlePost(w, r, actor, inst)
	case http.MethodGet:
		h.handleGet(w, r, actor, inst)
	}
```

`inst` is still passed (used for log-line `ticket` context + the 403-no-instance gate above is unchanged). The `/messages/targets` GET branch above stays as-is.

## 4. cmd wiring (`cmd/at-harbor/main.go`)

The two `NewMessagesHandler` call sites (~1105-1112) pass `messageLog` as **both** reader and appender, keeping the typed-nil guard (pass untyped `nil` when unconfigured — a typed-nil `*msglog.Log` in an interface param reads as non-nil and would 500/panic):

```go
		if messageLog != nil {
			msgH = harbor.NewMessagesHandler(st, messageLog, messageLog, log)
		} else {
			msgH = harbor.NewMessagesHandler(st, nil, nil, log)
		}
```

- Drop `linearCommenter{tracker}` from these calls. `linearCommenter` stays defined (still the `escalate.Pinger`); confirm it's still referenced by the escalation wiring and not left dead.

## 5. Tests (hermetic) (`internal/harbor/messages_test.go`)

Rewrite the read tests to a Log-backed reader. Add a `fakeReader` (or use a real `*msglog.Log` in a temp dir — simpler and exercises the real primitive):

- **read returns the inbox:** seed the Log with two inbound messages `From: human:Alice`, `To: [actor:<coveID>]` (+ Body/At); GET → 200, `{"messages":[…]}` with `author=="Alice"`, bodies + ids + `at` present, in Log order.
- **read excludes the cove's own outbound:** also append a message `From: actor:<coveID>, To:[channel:<Unit>]` (the cove's own send); assert it is **not** in the read result (it's not in the cove's inbox).
- **self-scope:** a second cove's inbox message is not returned to the first cove (the target is derived from the authenticated actor).
- **nil reader → 503.**
- **empty inbox → 200 with `{"messages":[]}`** (not null).
- Keep unchanged: 401/403(no instance)/405/never-logs-token, and the `/messages/targets` test. The POST tests are unaffected (handlePost signature already dropped issueID in Slice 3).
- Update the handler-construction test helper to the new `NewMessagesHandler` signature (reader, appender) — most POST tests can pass a real Log or a nil reader (they don't read).

## 6. Docs (`docs/usage/harbor/messaging.md`)

- **Read is now Log-backed and tracker-independent:** `read` returns the cove's **inbox** — the inbound messages addressed to it (human replies), from the durable message-log — not a live Linear query.
- Name the semantics: read returns messages sent **to** the cove, **not** its own sent messages, and reflects the Log from when ingestion began (pre-Log ticket history isn't included). Since wake-on only wakes a cove once a reply is in the Log, the reply is always present by read time.
- Bump `updated`.

## Deferred / boundaries

- **Deferred:** escalation-onto-Log (its own slice, with the multi-recipient/authorship design; pairs with Slice 5 Discord fan-out). The dead `Instance.WaitCursor`/`SetWaitCursor` cleanup (from Slice 2) remains separate. COV-177/178/179 (Slice 3 follow-ups) untouched.
- **Boundaries:** `internal/harbor` core stays msglog + stdlib (the reader is a local interface; no new import — `Commenter` removal *reduces* surface). The Linear read capability (`Commenter.Comments`) is no longer referenced by the handler; `linearCommenter` remains in cmd only for `escalate`. at-cove / connect / oidc untouched. No secret/token/body in any log line (read logs only `count`).
