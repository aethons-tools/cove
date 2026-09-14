# harbor msgport Slice 1a — outbound dual-write shadow — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `/messages` sends shadow-write the logical, raw message into the durable Log, with zero live-behavior change.

**Architecture:** `MessagesHandler` gains a nil-safe `appender` dep; `handlePost` best-effort `Append`s the logical message (`From: actor`, `To: channel:<unit>` / `human:<name>` / `channel:<name>`, raw body) after the unchanged live `PostComment`. cmd hoists the already-existing Log open and shares one `*msglog.Log` between the messages handler (writer) and the adminui reader.

**Tech Stack:** Go; `internal/msglog` (already a harbor dependency in adminui).

## Global Constraints

- **Zero live-behavior change:** the dual-write is **best-effort** — an append error is `Warn`-logged and swallowed; the send outcome (`PostComment`, the `204`/error codes) is never altered. A nil `appender` (message-log unconfigured) disables the dual-write entirely.
- **The Log stores the logical, RAW message** — `Body = req.Body` (NOT the `@handle`-prefixed body used for the live `PostComment`); `To` is the logical target (`channel:<inst.Unit>` for own-ticket, else `SendTarget.Kind:SendTarget.Name`). Rendering is the adapter's job at egress (Slice 3).
- **Only `handlePost` (send) dual-writes** — `read`/GET, `/messages/targets`, `/escalate`, and the escalation engine are untouched (escalation → the Log is Slice 4).
- **No secret/body in logs:** the append-error `Warn` carries the actor id + error only, never the body.
- **One Log, shared:** the writer (messages handler) and the reader (adminui `MessageReader`) use the SAME `*msglog.Log` opened once in `cmdServe`.
- **TDD, DRY, YAGNI, frequent commits.** Every task ends green (`GOPROXY=off go build ./... && GOPROXY=off go test ./...`), gofmt-clean, `.at-cove/` untouched. Prefix go commands with `GOPROXY=off`.

---

### Task 1: The dual-write in the messages handler

**Files:**
- Modify: `internal/harbor/messages.go` (appender dep + handlePost append)
- Modify: `cmd/at-harbor/main.go` (pass `nil` at the existing call site — keeps the build green; Task 2 activates it)
- Test: `internal/harbor/messages_test.go`

**Interfaces:**
- Produces: `appender` interface; `NewMessagesHandler(store messagesStore, cmt Commenter, lg appender, log *slog.Logger)` (adds the `lg` param); the shadow append in `handlePost`.

- [ ] **Step 1: Write failing tests**

Read `internal/harbor/messages_test.go` first for the existing fakes + the `postMessage`-style helper + how `NewMessagesHandler` is currently constructed in tests. Add a `fakeAppender` and dual-write tests:

```go
type fakeAppender struct {
	got []msglog.Message
	err error
}
func (f *fakeAppender) Append(m msglog.Message) (msglog.Message, error) {
	f.got = append(f.got, m)
	return m, f.err
}

func TestSendShadowWritesOwnTicket(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}} // adapt to the file's real fake names
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"}
	ap := &fakeAppender{}
	h := NewMessagesHandler(st, fc, ap, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"status"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	if len(ap.got) != 1 {
		t.Fatalf("expected 1 shadow append, got %d", len(ap.got))
	}
	m := ap.got[0]
	if m.From.Kind != "actor" || m.From.Ref != "cove-1" ||
		len(m.To) != 1 || m.To[0].Kind != "channel" || m.To[0].Ref != "ACME-42" ||
		m.Body != "status" || m.Project != "acme" {
		t.Fatalf("wrong shadow message: %+v", m)
	}
	// live path intact: the comment was still posted
	if fc.lastIssue != "iss-42" || fc.lastBody != "status" {
		t.Fatalf("live PostComment regressed: %q %q", fc.lastIssue, fc.lastBody)
	}
}

func TestSendShadowWritesHumanRawBody(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"}
	st.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"human:*"}}}}
	st.rosters["acme"] = Roster{Humans: []Human{{Name: "alice", Handle: "alice.h"}}}
	ap := &fakeAppender{}
	h := NewMessagesHandler(st, fc, ap, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"ping","to":"human:alice"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
	m := ap.got[0]
	if m.To[0].Kind != "human" || m.To[0].Ref != "alice" || m.Body != "ping" { // RAW body, not "@alice.h ping"
		t.Fatalf("human shadow wrong: %+v", m)
	}
	// live path still posts the @-mention on the own ticket
	if fc.lastIssue != "iss-42" || !strings.HasPrefix(fc.lastBody, "@alice.h ") {
		t.Fatalf("live @mention regressed: %q", fc.lastBody)
	}
}

func TestSendShadowWritesChannel(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42", "ACME-1": "iss-1"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h", Grants: []Grant{{Project: "acme", Role: "impl"}}}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"}
	st.roles["acme"] = map[string]Role{"impl": {Name: "impl", Scope: Scope{Addressing: []string{"channel:*"}}}}
	st.rosters["acme"] = Roster{Channels: []Channel{{Name: "eng", Service: "linear", Ref: "ACME-1"}}}
	ap := &fakeAppender{}
	h := NewMessagesHandler(st, fc, ap, testLogger())

	_ = postMessage(t, h, "tok-for-h", `{"body":"heads up","to":"channel:eng"}`)
	m := ap.got[0]
	if m.To[0].Kind != "channel" || m.To[0].Ref != "eng" || m.Body != "heads up" {
		t.Fatalf("channel shadow wrong: %+v", m)
	}
}

func TestSendShadowAppendErrorDoesNotFailSend(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"}
	ap := &fakeAppender{err: errors.New("disk full")}
	h := NewMessagesHandler(st, fc, ap, testLogger())

	rec := postMessage(t, h, "tok-for-h", `{"body":"status"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("append error must not fail the send; status=%d", rec.Code)
	}
	if fc.lastIssue != "iss-42" {
		t.Fatal("live PostComment must still have happened")
	}
}

func TestSendNilAppenderNoShadow(t *testing.T) {
	fc := &fakeCommenter{issueIDs: map[string]string{"ACME-42": "iss-42"}}
	st := newFakeMsgStore()
	st.actors["h"] = Actor{ID: "cove-1", TokenHash: "h"}
	st.instances["cove-1"] = Instance{ActorID: "cove-1", Unit: "ACME-42", Project: "acme"}
	h := NewMessagesHandler(st, fc, nil, testLogger()) // nil appender

	rec := postMessage(t, h, "tok-for-h", `{"body":"status"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d", rec.Code)
	}
}
```

> Adapt the fake/helper names (`fakeCommenter`/`newFakeMsgStore`/`postMessage`/`testLogger` + the `roles`/`rosters` fields) to whatever `messages_test.go` actually defines from the C1/C2 slices. Update the existing `NewMessagesHandler(...)` calls in the test file to pass an appender (a `&fakeAppender{}` or `nil`).

- [ ] **Step 2: Run tests, verify fail**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'Shadow|NilAppender' -v` → FAIL (arity/undefined).

- [ ] **Step 3: Add the appender dep (messages.go)**

Add the interface + struct field + constructor param, and import `internal/msglog`:

```go
// appender is the narrow write side of the message Log used for the outbound
// dual-write shadow. Satisfied by *msglog.Log; nil disables the shadow.
type appender interface {
	Append(m msglog.Message) (msglog.Message, error)
}
```
- `MessagesHandler` gains `lg appender`.
- `NewMessagesHandler(store messagesStore, cmt Commenter, lg appender, log *slog.Logger) *MessagesHandler` — set `lg: lg`.

- [ ] **Step 4: Append the logical message in `handlePost`**

Capture the logical target where each branch already computes delivery, then append after `PostComment`. Before the `if req.To != ""` block, declare:

```go
	logicalTo := msglog.Target{Kind: "channel", Ref: inst.Unit} // no `to` → own ticket-as-channel
```
Inside the `if req.To != ""` block, right after `DecideSend` succeeds (where `st` is in scope), set:

```go
		logicalTo = msglog.Target{Kind: st.Kind, Ref: st.Name}
```
Then, after the existing `PostComment` success + the `h.log.Info("messages", …)` line and before `w.WriteHeader(http.StatusNoContent)`:

```go
	if h.lg != nil {
		if _, err := h.lg.Append(msglog.Message{
			From:    msglog.Target{Kind: "actor", Ref: actor.ID},
			To:      []msglog.Target{logicalTo},
			Body:    req.Body, // raw — @handle rendering is the adapter's job at egress (Slice 3)
			Project: inst.Project,
		}); err != nil {
			h.log.Warn("messages: shadow append failed", "actor", actor.ID, "error", err.Error())
		}
	}
```

- [ ] **Step 5: Keep the build green (cmd/at-harbor/main.go)**

The `NewMessagesHandler` arity changed. Update the call site (~line 1055) to pass `nil` for now (Task 2 replaces it with the real Log):

```go
		msgH := harbor.NewMessagesHandler(st, linearCommenter{tracker}, nil, log)
```

- [ ] **Step 6: Run tests, verify pass**

Run: `GOPROXY=off go test ./internal/harbor/ -run 'Shadow|NilAppender|Send|Messages' -v` → PASS
Then: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → all pass (fix any other `NewMessagesHandler` call sites the compiler flags).

- [ ] **Step 7: gofmt + commit**

```bash
gofmt -w internal/harbor/messages.go internal/harbor/messages_test.go cmd/at-harbor/main.go
git add internal/harbor/messages.go internal/harbor/messages_test.go cmd/at-harbor/main.go
git commit -m "harbor: /messages send shadow-writes the logical message to the Log (COV-173)" # + trailers
```

---

### Task 2: Wire the shared Log into the writer (+ dedup the open) + docs

**Files:**
- Modify: `cmd/at-harbor/main.go` (hoist the Log open; pass it to `NewMessagesHandler`; dedup the admin-block open)
- Modify: `docs/usage/harbor/messaging.md`
- Test: `cmd/at-harbor/main_test.go` / `mux_test.go` if they build the handler (keep green)

- [ ] **Step 1: Hoist + share the Log**

Read `cmd/at-harbor/main.go` around the dispatcher block (`msgH` ~1055) and the admin block (the `ml := msglog.Open(cfg.MessageLog, log)` ~1123, currently only for `msgReader`).

- Open the Log ONCE, before the dispatcher block (so `msgH` can use it), guarded by `cfg.MessageLog != ""`:
```go
	var messageLog *msglog.Log
	if cfg.MessageLog != "" {
		ml, err := msglog.Open(cfg.MessageLog, log)
		if err != nil {
			fmt.Fprintln(stderr, "at-harbor: message-log:", err)
			return 1
		}
		defer ml.Close()
		messageLog = ml
		log.Info("harbor message log", "path", cfg.MessageLog)
	}
```
- In the dispatcher block, pass it as the appender (a nil `*msglog.Log` typed value is fine — `handlePost`'s `h.lg != nil` guard handles the untyped-nil interface case; to be safe, pass the interface as nil when `messageLog == nil`):
```go
		var ap appender  // OR use msgport-free: pass messageLog directly; see note
		msgH := harbor.NewMessagesHandler(st, linearCommenter{tracker}, messageLog, log)
```
> **Nil-interface caveat:** passing a typed-nil `*msglog.Log` makes `h.lg != nil` TRUE (non-nil interface wrapping a nil pointer), which would then call `Append` on a nil `*msglog.Log` and panic. So when `messageLog == nil`, pass a literal `nil` to `NewMessagesHandler` (or guard: `if messageLog != nil { msgH = New(..., messageLog, ...) } else { msgH = New(..., nil, ...) }`). Simplest: since `*msglog.Log` is the concrete appender, write `NewMessagesHandler(st, linearCommenter{tracker}, appenderOrNil(messageLog), log)` where a tiny helper returns a nil interface for a nil pointer — OR keep it obvious with an if/else. The task MUST ensure a nil message-log yields a nil `appender` interface, not a typed-nil, so `handlePost` correctly skips.
- In the admin block, REMOVE the duplicate `ml := msglog.Open(...)` and instead set `msgReader = messageLog` (the hoisted one) when non-nil. Keep the "not configured" behavior when nil.

- [ ] **Step 2: Build + test**

Run: `GOPROXY=off go build ./... && GOPROXY=off go test ./...` → green. Manually confirm (by reading) that a configured `message-log` now flows the SAME `*msglog.Log` into both `NewMessagesHandler` and `msgReader`, and an unconfigured one yields a nil appender (no dual-write, no panic).

- [ ] **Step 3: Docs**

In `docs/usage/harbor/messaging.md`, add one line (in the section covering `message-log` / the admin message view, or the send tools): when `message-log` is configured, `/messages` **sends are now also shadow-recorded** to the durable Log (surfaced in the read-only admin message view); live delivery is unchanged. Note this is the first writer in the msgport arc. Bump `updated`.

- [ ] **Step 4: Commit**

```bash
gofmt -w cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git add cmd/at-harbor/main.go docs/usage/harbor/messaging.md
git commit -m "harbor: open the message Log once, share it with the /messages writer (COV-173)" # + trailers
```

---

## Self-Review

- **Spec coverage:** §1 dual-write → Task 1; §2 wiring/hoist → Task 2; §3 docs → Task 2; tests → Task 1.
- **The nil-interface trap is called out** (Task 2 Step 1) — a typed-nil `*msglog.Log` would make `h.lg != nil` true and panic; the wiring must pass a genuine nil interface when unconfigured. The `TestSendNilAppenderNoShadow` test (Task 1) guards the handler side.
- **Green between tasks:** Task 1 changes the arity and passes `nil` at the cmd site (build green, dual-write dormant); Task 2 activates it with the hoisted Log. The dual-write behavior is fully tested in Task 1 independent of cmd.
- **Raw vs rendered body** is pinned by `TestSendShadowWritesHumanRawBody` (append stores `ping`, live posts `@alice.h ping`).
- **Best-effort** pinned by `TestSendShadowAppendErrorDoesNotFailSend`.
- **Placeholder scan:** the only open item is adapting to the test file's real fake/helper names (from C1/C2) — real, discoverable, not a TBD.
