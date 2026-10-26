# Jam SPA, slice 3: composer — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the Jam app can send messages, with feature parity with the old composer:
- a reply box in every writable conversation, and a **New message** picker;
- Cmd/Ctrl+Enter to send, Shift+Cmd/Ctrl+V to paste as code, and the Cmd/Ctrl+↓/↑ draft stack;
- the saved reply per recipient and the plain-text toggle;
- attachments: 📎, paste, drag-and-drop, chips with progress, and thumbnails;
- notification chimes, with their per-device Sounds settings.

**Architecture:**
- `ParticipantSendHandler`'s logic moves into a `Send` method; the old `/app/user/send` keeps its exact responses.
- `userui` adds `POST /app/user/api/messages`, which answers `{channel, seq}`, and `GET /app/user/api/recipients`.
- The app gets:
  - a `Composer` component built on small pure modules (fence, drafts, keys, upload);
  - a `/app/next/new` route;
  - a TypeScript port of `sounds.js` that keeps its storage contract with the service worker.

**Tech Stack:** the slice 1/2 stack. There are no new npm dependencies.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md): §2, §3 slice 3, §4, §5. Earlier plans: [slice 1](2026-10-26-jam-spa-1-foundation.md), [slice 2](2026-10-26-jam-spa-2-conversations.md). Their helpers are on main: `writeJSON`, `writeAPIError`, `decodeJSON`, `apiGet`/`apiPost`, `ApiError`, `POLL_MS`, `renderApp`, `boot`, `pageProblems`, `step`/`into`/`call`, `testChanges`.

## Global Constraints

- **Parity:** everything the old composer does works. The old HTML route `POST /app/user/send` keeps its exact status codes and text bodies, and its tests must pass unchanged.
- **Storage keys (compatible with the old Inbox):**
  - the reply is `sessionStorage["me-reply:" + to]`;
  - the draft stack is `sessionStorage["me-drafts:" + to]` as `[{text, sel}]`;
  - sound settings are `localStorage["jam-sounds"]` `{messages, needs, volume}` and `localStorage["jam-sounds-last"]` `{at, kind}`;
  - the service worker's copies are IndexedDB `jam` → store `prefs`, keys `sounds` and `chimer`.
- **Uploads:** they stay on the existing `POST /app/user/uploads` (raw body, `X-Filename` header, 201 `{"ref"}`).
- **API errors:** `{"error","code"}`; writes are JSON (`decodeJSON`).
- **Message HTML:** it still comes only from `squawkrender`, and the composer never renders it.
- **Size budget:** initial JS ≤ 350 KB gzipped (188 KB now).
- **Docs:** they ship in the same change.

## Review Focus

1. **A failed send** (403, 404, network) keeps the typed text and the attachment chips, and says why in a notification. (Task 3 test.)
2. **Send keys:**
   - Cmd+Enter on a Mac or Ctrl+Enter elsewhere sends.
   - Shift+Enter and plain Enter insert a newline.
   - Enter during IME composition (`isComposing`) never sends.
   - On a Mac, Ctrl+Enter doesn't send.
   (Task 3 test.)
3. **Uploads in flight:** Send is refused while a chip is still uploading, a failed chip is never sent, and removing a chip aborts its upload. (Task 4 test.)
4. **Pasting:**
   - Text that contains its own ``` fence gets a longer fence when pasted as code.
   - A paste carrying both text and an image (an Office copy) pastes the text, not a file.
   (Tasks 3 and 4 tests.)
5. **A reply stays with its conversation:** a reply typed in one conversation never shows in another. It survives switching away and back, and popping a stacked draft never overwrites text in the box. (Task 3 test.)

---

## File structure

| File | Responsibility |
|---|---|
| `internal/jam/me_send.go` (modify) | Gains `SendRequest`, `SendError` and `(*ParticipantSendHandler).Send`; `ServeHTTP` decodes, calls `Send` and maps errors exactly as before. |
| `internal/jam/me_send_test.go` (modify) | Adds a `Send` unit test. |
| `internal/jam/userui/apitypes.go` (modify) | `SendMessageRequest`, `MessageSent`, `RecipientsResponse`. |
| `internal/jam/userui/api_compose.go` (new), `api_compose_test.go` (new) | `POST api/messages`, `GET api/recipients`. |
| `internal/jam/userui/userui.go` (modify) | `Deps.Send`. |
| `cmd/at-jam/main.go` (modify) | Passes `sendH` as `userDeps.Send`. |
| `web/app/src/composer/fence.ts`, `drafts.ts`, `keys.ts`, `upload.ts` (new) | Pure helpers (each with tests). |
| `web/app/src/composer/Composer.tsx`, `AttachmentChips.tsx`, `Composer.test.tsx` (new) | The composer. |
| `web/app/src/compose/NewMessage.tsx`, `RecipientPicker.tsx`, `NewMessage.test.tsx` (new) | New message. |
| `web/app/src/sounds/sounds.ts`, `chimes.ts`, `SoundsSettings.tsx` + tests (new) | Chimes and settings. |
| `web/app/src/conversation/ConversationView.tsx`, `rail/Rail.tsx`, `shell/TitleBar.tsx`, `App.tsx` (modify) | Wiring. |
| `internal/jam/userui/next_browser_test.go` (modify) | End-to-end send. |
| `docs/web-app.md`, `docs/usage/jam/intercom-ui.md` (modify) | Docs. |

---

### Task 1: `ParticipantSendHandler.Send`

**Files:**
- Modify: `internal/jam/me_send.go`, `internal/jam/me_send_test.go`

**Interfaces:**
- Produces:
  - `type SendRequest struct{ To, Body, ContentType, ReplyTo string; Attachments []string }`, with JSON tags `to`, `body`, `content_type,omitempty`, `reply_to,omitempty` and `attachments,omitempty`, matching the old wire format.
  - `type SendError struct{ Status int; Code, Msg string }`, which implements `error` (`Error()` returns `Msg`).
  - `func (h *ParticipantSendHandler) Send(p Participant, req SendRequest) (intercom.Squawk, error)`. The returned squawk carries `Channel` and `Seq`. Every refusal is a `*SendError`; any other error is unexpected (500).

- [ ] **Step 1: Write the failing test.**

Append to `internal/jam/me_send_test.go` (add `"errors"` to its imports):
```go
// Send is the handler's logic without HTTP: the posted squawk (its channel
// and seq) or a *SendError carrying the old route's status and text.
func TestParticipantSendDirect(t *testing.T) {
	f := newICFixture(t)
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	p := Participant{UserID: f.bob.ID, Name: f.bob.Name, Projects: []string{"acme"}}
	m, err := h.Send(p, SendRequest{To: "user:" + string(f.alice.ID), Body: "hello"})
	if err != nil || m.Channel == "" || m.Seq == 0 || m.From != f.bob.ID || m.Body != "hello" {
		t.Fatalf("Send = %+v, %v", m, err)
	}
	for _, tc := range []struct {
		req    SendRequest
		status int
		code   string
	}{
		{SendRequest{To: "user:alice"}, 400, "bad_request"},
		{SendRequest{Body: "x"}, 400, "bad_request"},
		{SendRequest{To: "user:alice", Body: "x", ContentType: "text/html"}, 400, "bad_request"},
		{SendRequest{To: "user:nobody", Body: "x"}, 404, "not_found"},
		{SendRequest{To: "user:alice", Body: "x", Attachments: []string{"blb_x"}}, 400, "bad_request"}, // attachments off
	} {
		_, err := h.Send(p, tc.req)
		var se *SendError
		if !errors.As(err, &se) || se.Status != tc.status || se.Code != tc.code || se.Msg == "" {
			t.Errorf("Send(%+v) = %v, want %d %s", tc.req, err, tc.status, tc.code)
		}
	}
	if _, err := NewParticipantSendHandler(f.store, nil, nil).Send(p, SendRequest{To: "user:alice", Body: "x"}); !isSendStatus(err, 503) {
		t.Errorf("no intercom: %v, want 503", err)
	}
}

func isSendStatus(err error, status int) bool {
	var se *SendError
	return errors.As(err, &se) && se.Status == status
}
```

- [ ] **Step 2: Run it to verify it fails.**

Run: `go test ./internal/jam/ -run TestParticipantSend`
Expected: FAIL to compile (`undefined: SendRequest`).

- [ ] **Step 3: Implement.**

In `internal/jam/me_send.go`, replace `ServeHTTP`'s body from `r.Body = http.MaxBytesReader(...)` to the end with a decode step, a call to `Send`, and the old mapping. Put the logic in `Send`. Keep every message string exactly as it is today:
```go
// SendRequest is a /app/user send (POST /app/user/send's JSON).
type SendRequest struct {
	To          string   `json:"to"`
	Body        string   `json:"body"`
	ContentType string   `json:"content_type,omitempty"` // "" = text/markdown
	ReplyTo     string   `json:"reply_to,omitempty"`     // an ask in the same channel (Discuss)
	Attachments []string `json:"attachments,omitempty"`  // uploaded blob refs
}

// SendError is a refused send: the HTTP status and text the /app/user/send
// route answers, and a code for the JSON API.
type SendError struct {
	Status    int
	Code, Msg string
}

func (e *SendError) Error() string { return e.Msg }

func sendErr(status int, code, msg string) error { return &SendError{Status: status, Code: code, Msg: msg} }

func (h *ParticipantSendHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Identity comes solely from the gate-injected participant, never the body.
	// Fail closed: no participant (a gate misconfiguration) denies.
	p, ok := ParticipantFrom(r)
	if !ok {
		http.Error(w, "no participant", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSquawkBodyBytes)
	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if _, err := h.Send(p, req); err != nil {
		var se *SendError
		if errors.As(err, &se) {
			http.Error(w, se.Msg, se.Status)
			return
		}
		http.Error(w, "send failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Send posts req as p, or refuses with a *SendError (see ServeHTTP).
func (h *ParticipantSendHandler) Send(p Participant, req SendRequest) (intercom.Squawk, error) {
	parts, err := h.att.RefParts(req.Attachments)
	if err != nil {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "attachments are off")
	}
	if req.Body == "" && len(parts) > 0 {
		req.Body = h.att.Names(req.Attachments)
	}
	if req.Body == "" {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "empty body")
	}
	if req.To == "" {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "empty to")
	}
	if !intercom.ValidContentType(req.ContentType) {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "unsupported content_type (want text/markdown or text/plain)")
	}
	if h.ic == nil {
		return intercom.Squawk{}, sendErr(http.StatusServiceUnavailable, "unavailable", "messaging not configured")
	}
	pl, err := h.plan(p, req.To)
	switch {
	case errors.Is(err, ErrSendDenied):
		return intercom.Squawk{}, sendErr(http.StatusForbidden, "forbidden", "not allowed")
	case errors.Is(err, ErrSendUnresolved), errors.Is(err, ErrRemoved):
		return intercom.Squawk{}, sendErr(http.StatusNotFound, "not_found", "recipient not found")
	case err != nil:
		h.log.Error("participant send: plan failed", "user", string(p.UserID), "error", err.Error())
		return intercom.Squawk{}, sendErr(http.StatusInternalServerError, "internal", "send failed")
	}
	if req.ReplyTo != "" {
		if a, ok := h.ic.GetAsk(req.ReplyTo); !ok || a.Channel != pl.Channel.ID {
			return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "reply_to must be an ask in this conversation")
		}
	}
	m, err := h.ic.Post(pl, intercom.Squawk{From: p.UserID, Body: req.Body, ContentType: req.ContentType, ReplyTo: req.ReplyTo, Parts: parts})
	if errors.Is(err, intercom.ErrInvalid) {
		return intercom.Squawk{}, sendErr(http.StatusBadRequest, "bad_request", "invalid attachments")
	}
	if err != nil {
		h.log.Error("participant send: append failed", "user", string(p.UserID), "channel", string(pl.Channel.ID), "error", err.Error())
		return intercom.Squawk{}, sendErr(http.StatusBadGateway, "unavailable", "send failed")
	}
	h.log.Info("participant send", "user", string(p.UserID), "channel", string(pl.Channel.ID), "audience", len(pl.Audience), "bytes", len(req.Body))
	return m, nil
}
```
Before replacing anything, read the current `ServeHTTP` from line 110 to the end. If it does anything between the plan and the post that this block lacks, carry it into `Send` verbatim, and ledger it.

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/jam/ -run 'TestParticipantSend' && go test ./internal/jam/...`
Expected: PASS. The old tests pass unchanged, so the HTML route answers exactly as before.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/me_send.go internal/jam/me_send_test.go
git commit -m "jam: ParticipantSendHandler.Send — the send logic without HTTP"
```

---

### Task 2: `POST api/messages` and `GET api/recipients`

**Files:**
- Create: `internal/jam/userui/api_compose.go`, `internal/jam/userui/api_compose_test.go`
- Modify: `internal/jam/userui/apitypes.go`, `internal/jam/userui/userui.go`, `internal/jam/userui/api.go`, `cmd/at-jam/main.go`, `web/app/src/api/types.ts` (generated)

**Interfaces:**
- Consumes: `jam.SendRequest`, `jam.SendError`, `(*jam.ParticipantSendHandler).Send` (Task 1); `newMessageOptions(p, d) []NewMessageOption`; `decodeJSON`, `writeJSON`, `writeAPIError`.
- Produces:
  - `Deps.Send Sender` (nil means messaging is off: 503), where `type Sender interface{ Send(jam.Participant, jam.SendRequest) (intercom.Squawk, error) }`.
  - `SendMessageRequest{to, body, contentType?, replyTo?, attachments?}`.
  - `MessageSent{channel, seq}`.
  - `RecipientsResponse{options: NewMessageOption[]}`.
  - `decodeJSONMax(w, r, v, max int64) bool` (an over-`max` body gets 413 `too_large`), and `jam.MaxSquawkBodyBytes` (exported).
  - Routes:
    - `POST /app/user/api/messages` answers 201 with `MessageSent`, or the `SendError`'s status and `{error: Msg, code: Code}`.
    - `GET /app/user/api/recipients`.

- [ ] **Step 1: Write the failing tests.**

`internal/jam/userui/api_compose_test.go`:
```go
package userui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

func TestAPISendMessage(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	e.Send = jam.NewParticipantSendHandler(e.st, e.Intercom, nil)
	h := Handler(e.Deps, nil)

	rec := apiPost(t, h, p, "/app/user/api/messages", `{"to":"user:`+string(bob)+`","body":"hi bob"}`)
	if rec.Code != 201 {
		t.Fatalf("send = %d %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var sent MessageSent
	if err := json.Unmarshal(rec.Body.Bytes(), &sent); err != nil || sent.Channel == "" || sent.Seq == 0 {
		t.Fatalf("sent = %+v %v", sent, err)
	}
	// The new conversation is the viewer's and holds the message.
	if _, c, _ := getConv(t, e, p, sent.Channel, ""); len(c.Messages) != 1 || c.Messages[0].Raw != "hi bob" || !c.Messages[0].Mine {
		t.Errorf("new conversation = %+v", c)
	}
	// Into an existing one, as plain text.
	rec = apiPost(t, h, p, "/app/user/api/messages", `{"to":"`+engID+`","body":"*lit*","contentType":"text/plain"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"channel":"`+engID+`"`) {
		t.Fatalf("send to eng = %d %s", rec.Code, rec.Body)
	}
	if _, c, _ := getConv(t, e, p, engID, ""); !c.Messages[len(c.Messages)-1].Plain {
		t.Error("contentType text/plain wasn't kept")
	}
	for body, want := range map[string]string{
		`{"to":"user:nobody","body":"x"}`: `"code":"not_found"`,
		`{"to":"user:alice"}`:             `"code":"bad_request"`,
		`{"to":"user:alice","body":"` + strings.Repeat("x", 17<<10) + `"}`: `"code":"too_large"`,
	} {
		if rec := apiPost(t, h, p, "/app/user/api/messages", body); rec.Code < 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("send %s = %d %s, want %s", body, rec.Code, rec.Body, want)
		}
	}
}

func TestAPISendMessageOff(t *testing.T) {
	e, p := fixture() // Deps.Send unset
	if rec := apiPost(t, Handler(e.Deps, nil), p, "/app/user/api/messages", `{"to":"`+engID+`","body":"x"}`); rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Errorf("send without a sender = %d %s", rec.Code, rec.Body)
	}
}

func TestAPIRecipients(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/recipients")
	if rec.Code != 200 {
		t.Fatalf("recipients = %d %s", rec.Code, rec.Body)
	}
	var got RecipientsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	var labels []string
	for _, o := range got.Options {
		labels = append(labels, o.Label+"/"+o.Kind)
	}
	if s := strings.Join(labels, ","); !strings.Contains(s, "bob/person") || !strings.Contains(s, "eng/room") || strings.Contains(s, "alice/") {
		t.Errorf("recipients = %s (want bob and eng, never yourself)", s)
	}
}
```
`apiPost`, `apiGet`, `getConv` and `checkJSONHeaders` are the slice 2 test helpers.

- [ ] **Step 2: Run them to verify they fail.**

Run: `go test ./internal/jam/userui/ -run 'TestAPISend|TestAPIRecipients'`
Expected: FAIL to compile (`e.Send undefined`, `MessageSent undefined`).

- [ ] **Step 3: Implement.**

Add to `Deps` in `userui.go`:
```go
	// Send posts the viewer's messages (POST api/messages): the same
	// *jam.ParticipantSendHandler as /app/user/send (nil: 503).
	Send Sender
```
and in the same file:
```go
// Sender posts a participant's message: *jam.ParticipantSendHandler.
type Sender interface {
	Send(p jam.Participant, req jam.SendRequest) (intercom.Squawk, error)
}
```
Append to `apitypes.go`:
```go
// SendMessageRequest is POST api/messages: to a channel id, user:<id>, or
// session:<actor> (a NewMessageOption's "to").
type SendMessageRequest struct {
	To          string   `json:"to"`
	Body        string   `json:"body"`
	ContentType string   `json:"contentType,omitempty"` // "" = markdown; "text/plain" shown literally
	ReplyTo     string   `json:"replyTo,omitempty"`     // an ask in the same channel (Discuss)
	Attachments []string `json:"attachments,omitempty"` // refs from POST /app/user/uploads
}

// MessageSent is POST api/messages's answer: where the message went (a new
// message's new conversation) and its seq.
type MessageSent struct {
	Channel string `json:"channel"`
	Seq     int64  `json:"seq"`
}

// RecipientsResponse is GET api/recipients: whom the viewer may start a
// conversation with (the New message picker).
type RecipientsResponse struct {
	Options []NewMessageOption `json:"options"`
}
```
`internal/jam/userui/api_compose.go`:
```go
package userui

import (
	"errors"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

func (h *handler) composeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /app/user/api/messages", h.apiSend)
	mux.HandleFunc("GET /app/user/api/recipients", h.apiRecipients)
}

// apiSend posts the viewer's message, as /app/user/send does, and says where
// it went.
func (h *handler) apiSend(w http.ResponseWriter, r *http.Request) {
	var req SendMessageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	if h.d.Send == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "messaging not configured")
		return
	}
	m, err := h.d.Send.Send(p, jam.SendRequest{To: req.To, Body: req.Body, ContentType: req.ContentType, ReplyTo: req.ReplyTo, Attachments: req.Attachments})
	var se *jam.SendError
	switch {
	case errors.As(err, &se):
		writeAPIError(w, se.Status, se.Code, se.Msg)
		return
	case err != nil:
		h.lg.Error("userui api send failed", "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "send failed")
		return
	}
	writeJSON(w, http.StatusCreated, MessageSent{Channel: string(m.Channel), Seq: m.Seq})
}

func (h *handler) apiRecipients(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	opts := newMessageOptions(p, h.d)
	if opts == nil {
		opts = []NewMessageOption{}
	}
	writeJSON(w, http.StatusOK, RecipientsResponse{Options: opts})
}
```
The old route caps a send at 16 KiB (`maxSquawkBodyBytes` in `internal/jam/squawks.go`), so the API must too. Export it as `jam.MaxSquawkBodyBytes` and update its users. In `api_conversation.go`, add `decodeJSONMax(w, r, v, max int64) bool`: when the body is over `max` (`*http.MaxBytesError`), it answers 413 `too_large` "body too large"; otherwise it behaves exactly like `decodeJSON`. `decodeJSON` then becomes `decodeJSONMax(w, r, v, 64<<10)`. `apiSend` uses `decodeJSONMax(w, r, &req, jam.MaxSquawkBodyBytes)`.

Register `h.composeRoutes(mux)` in `apiRoutes`, before the catch-all.

In `cmd/at-jam/main.go`, set `Send: sendH,` in the `userDeps` literal; `sendH` is already built just above it.

Run `go generate ./internal/jam/userui/`.

- [ ] **Step 4: Run the tests and build.**

Run: `go test ./internal/jam/... && go build ./cmd/at-jam/ && ./scripts/app-types-check.sh || git diff --stat web/app/src/api/types.ts`
Expected: PASS and the build succeeds. `types.ts` gains `SendMessageRequest`, `MessageSent` and `RecipientsResponse`; commit it.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/userui cmd/at-jam/main.go web/app/src/api/types.ts
git commit -m "userui: POST api/messages (where it went) and GET api/recipients"
```

---

### Task 3: The composer: text, keys, paste-as-code, drafts, the saved reply, sending

**Files:**
- Create:
  - `web/app/src/composer/fence.ts`, `fence.test.ts`
  - `web/app/src/composer/drafts.ts`, `drafts.test.ts`
  - `web/app/src/composer/keys.ts`, `keys.test.ts`
  - `web/app/src/composer/Composer.tsx`, `Composer.test.tsx`
- Modify: `web/app/src/conversation/ConversationView.tsx`, `web/app/src/conversation/MessageList.tsx`

**Interfaces:**
- Consumes: `apiPost`, `ApiError`; `SendMessageRequest`, `MessageSent`; query keys `["conversation", id]` and `["rail"]`.
- Produces:
  - `fenceFor(s: string): string` and `asCodeBlock(text: string, before: string): string`.
  - `loadReply(to)`, `saveReply(to, text)`, `loadDrafts(to): Draft[]`, `saveDrafts(to, d)`, where `type Draft = { text: string; sel: number }`. They use sessionStorage, falling back to memory.
  - `isMac(): boolean`; `isSendKey(e: KeyboardEvent-like, mac: boolean): boolean`; `isPasteAsCodeKey(e, mac): boolean`; `isDraftKey(e, mac): "push" | "pop" | null`; `hint(mac): string` (the placeholder suffix).
  - `<Composer to label onSent? attachMax />`, where `onSent(sent: MessageSent)` runs after a 201. Attachments arrive in Task 4; until then `attachMax` is accepted and ignored.
  - `MessageList` gains a `stickToEnd` counter prop: when it changes, the list scrolls to the end.

- [ ] **Step 1: Write the failing tests.**

`web/app/src/composer/fence.test.ts`:
```ts
import { describe, expect, it } from "vitest";
import { asCodeBlock, fenceFor } from "./fence";

describe("fence", () => {
  it("is longer than any backtick run, at least three", () => {
    expect(fenceFor("plain")).toBe("```");
    expect(fenceFor("a ```b``` c")).toBe("````");
    expect(fenceFor("x `````")).toBe("``````");
  });
  it("wraps pasted text on its own lines", () => {
    expect(asCodeBlock("a ```b``` c", "see:")).toBe("\n````\na ```b``` c\n````\n");
    expect(asCodeBlock("x\n", "")).toBe("```\nx\n```\n");
    expect(asCodeBlock("x", "line\n")).toBe("```\nx\n```\n");
  });
});
```
`web/app/src/composer/drafts.test.ts`:
```ts
import { beforeEach, describe, expect, it } from "vitest";
import { loadDrafts, loadReply, saveDrafts, saveReply } from "./drafts";

beforeEach(() => sessionStorage.clear());

describe("drafts", () => {
  it("keeps a reply per recipient, under the old Inbox's key", () => {
    saveReply("chn_a", "half typed");
    expect(loadReply("chn_a")).toBe("half typed");
    expect(loadReply("chn_b")).toBe("");
    expect(sessionStorage.getItem("me-reply:chn_a")).toBe("half typed");
    saveReply("chn_a", "");
    expect(sessionStorage.getItem("me-reply:chn_a")).toBeNull();
  });
  it("stacks drafts per recipient, under the old Inbox's key", () => {
    saveDrafts("chn_a", [{ text: "one", sel: 2 }]);
    expect(loadDrafts("chn_a")).toEqual([{ text: "one", sel: 2 }]);
    expect(JSON.parse(sessionStorage.getItem("me-drafts:chn_a")!)).toEqual([{ text: "one", sel: 2 }]);
    expect(loadDrafts("chn_b")).toEqual([]);
  });
  it("tolerates junk in storage", () => {
    sessionStorage.setItem("me-drafts:chn_a", "{not json");
    expect(loadDrafts("chn_a")).toEqual([]);
  });
});
```
`web/app/src/composer/keys.test.ts`:
```ts
import { describe, expect, it } from "vitest";
import { hint, isDraftKey, isPasteAsCodeKey, isSendKey } from "./keys";

const k = (o: Partial<{ key: string; code: string; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; isComposing: boolean }>) =>
  ({ key: "", code: "", metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, isComposing: false, ...o });

describe("keys", () => {
  it("sends on Cmd+Enter (Mac) / Ctrl+Enter (elsewhere) only", () => {
    expect(isSendKey(k({ key: "Enter", metaKey: true }), true)).toBe(true);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true }), true)).toBe(false);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true }), false)).toBe(true);
    expect(isSendKey(k({ key: "Enter", metaKey: true }), false)).toBe(false);
    expect(isSendKey(k({ key: "Enter" }), false)).toBe(false);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true, shiftKey: true }), false)).toBe(false);
    expect(isSendKey(k({ key: "Enter", ctrlKey: true, isComposing: true }), false)).toBe(false);
  });
  it("pastes as code on Shift+Cmd+V / Shift+Ctrl+V", () => {
    expect(isPasteAsCodeKey(k({ code: "KeyV", shiftKey: true, metaKey: true }), true)).toBe(true);
    expect(isPasteAsCodeKey(k({ code: "KeyV", shiftKey: true, ctrlKey: true }), false)).toBe(true);
    expect(isPasteAsCodeKey(k({ code: "KeyV", ctrlKey: true }), false)).toBe(false);
    expect(isPasteAsCodeKey(k({ code: "KeyV", shiftKey: true, ctrlKey: true, altKey: true }), false)).toBe(false);
  });
  it("pushes/pops drafts on Cmd/Ctrl+↓/↑", () => {
    expect(isDraftKey(k({ key: "ArrowDown", metaKey: true }), true)).toBe("push");
    expect(isDraftKey(k({ key: "ArrowUp", ctrlKey: true }), false)).toBe("pop");
    expect(isDraftKey(k({ key: "ArrowUp", ctrlKey: true, shiftKey: true }), false)).toBeNull();
  });
  it("labels the keys for the platform", () => {
    expect(hint(true)).toBe("⌘↵ to send · ⇧⌘V pastes as code · ⌘↓ stacks a draft");
    expect(hint(false)).toBe("Ctrl+↵ to send · ⇧Ctrl+V pastes as code · Ctrl+↓ stacks a draft");
  });
});
```
`web/app/src/composer/Composer.test.tsx`:
```tsx
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { Notifications } from "@mantine/notifications";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { theme } from "../theme";
import { Composer } from "./Composer";
import * as keys from "./keys";

function mount(to = "chn_a", onSent = vi.fn()) {
  const client = new QueryClient();
  const r = render(
    <MantineProvider theme={theme}><Notifications />
      <QueryClientProvider client={client}><Composer to={to} label="eng" attachMax={0} onSent={onSent} /></QueryClientProvider>
    </MantineProvider>,
  );
  return { ...r, box: screen.getByRole("textbox") as HTMLTextAreaElement, onSent };
}

beforeEach(() => { sessionStorage.clear(); vi.spyOn(keys, "isMac").mockReturnValue(false); });

describe("Composer", () => {
  it("sends on Ctrl+Enter, clears, and reports where it went", async () => {
    let got: unknown;
    server.use(http.post("/app/user/api/messages", async ({ request }) => { got = await request.json(); return HttpResponse.json({ channel: "chn_a", seq: 9 }, { status: 201 }); }));
    const { box, onSent } = mount();
    await userEvent.type(box, "  hello  ");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(onSent).toHaveBeenCalledWith({ channel: "chn_a", seq: 9 }));
    expect(got).toEqual({ to: "chn_a", body: "hello" });
    expect(box.value).toBe("");
    expect(sessionStorage.getItem("me-reply:chn_a")).toBeNull();
  });

  it("sends plain text when asked", async () => {
    let got: { contentType?: string } = {};
    server.use(http.post("/app/user/api/messages", async ({ request }) => { got = (await request.json()) as typeof got; return HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 }); }));
    const { box } = mount();
    await userEvent.type(box, "*lit*");
    await userEvent.click(screen.getByLabelText("Plain text"));
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(got.contentType).toBe("text/plain"));
  });

  it("keeps the text and says why when a send fails", async () => {
    server.use(http.post("/app/user/api/messages", () => HttpResponse.json({ error: "not allowed", code: "forbidden" }, { status: 403 })));
    const { box, onSent } = mount();
    await userEvent.type(box, "keep me");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    expect(await screen.findByText("not allowed")).toBeInTheDocument();
    expect(box.value).toBe("keep me");
    expect(onSent).not.toHaveBeenCalled();
  });

  it("newlines on Enter / Shift+Enter, never sends mid-composition or on an empty box", async () => {
    let posts = 0;
    server.use(http.post("/app/user/api/messages", () => { posts++; return HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 }); }));
    const { box } = mount();
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true }); // empty
    await userEvent.type(box, "a{Enter}b{Shift>}{Enter}{/Shift}c");
    expect(box.value).toBe("a\nb\nc");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true, isComposing: true });
    await new Promise((r) => setTimeout(r, 30));
    expect(posts).toBe(0);
  });

  it("pastes as code after Shift+Ctrl+V, a plain paste stays plain", () => {
    const { box } = mount();
    fireEvent.change(box, { target: { value: "see:" } });
    box.setSelectionRange(4, 4);
    fireEvent.keyDown(box, { code: "KeyV", key: "V", ctrlKey: true, shiftKey: true });
    fireEvent.paste(box, { clipboardData: { getData: () => "a ```b``` c", files: [] } });
    expect(box.value).toBe("see:\n````\na ```b``` c\n````\n");
    fireEvent.keyDown(box, { code: "KeyV", key: "v", ctrlKey: true });
    const plain = fireEvent.paste(box, { clipboardData: { getData: () => "z", files: [] } });
    expect(plain).toBe(true); // not prevented: the browser pastes it as is
  });

  it("keeps the reply per recipient across remounts", async () => {
    const first = mount("chn_a");
    await userEvent.type(first.box, "for a");
    first.unmount();
    const other = mount("chn_b");
    expect(other.box.value).toBe("");
    other.unmount();
    expect(mount("chn_a").box.value).toBe("for a");
  });

  it("stacks a draft on Ctrl+↓ and pops it back after a send, never over text", async () => {
    server.use(http.post("/app/user/api/messages", () => HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 })));
    const { box } = mount();
    await userEvent.type(box, "draft one");
    fireEvent.keyDown(box, { key: "ArrowDown", ctrlKey: true });
    expect(box.value).toBe("");
    expect(screen.getByRole("button", { name: /1 stacked draft/ })).toBeInTheDocument();
    await userEvent.type(box, "quick");
    fireEvent.keyDown(box, { key: "ArrowUp", ctrlKey: true }); // box not empty: no pop
    expect(box.value).toBe("quick");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(box.value).toBe("draft one"));
    expect(screen.queryByRole("button", { name: /stacked draft/ })).toBeNull();
  });
});
```
The `isMac` spy needs `keys.ts` to read `isMac` through its module object. `Composer` imports `* as keys` and calls `keys.isMac()`, so the spy takes effect.

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/composer`
Expected: FAIL (the modules don't exist).

- [ ] **Step 3: Implement the helpers.**

`web/app/src/composer/fence.ts`:
```ts
// fenceFor is a backtick fence longer than any backtick run in s (at least
// three), so pasted text with its own fences stays inside the block.
export function fenceFor(s: string): string {
  let longest = 0, run = 0;
  for (const ch of s) {
    if (ch === "`") { run++; longest = Math.max(longest, run); } else run = 0;
  }
  return "`".repeat(Math.max(3, longest + 1));
}

// asCodeBlock is text as a fenced block on its own lines, given the box's
// text before the cursor.
export function asCodeBlock(text: string, before: string): string {
  const f = fenceFor(text);
  return (before && !before.endsWith("\n") ? "\n" : "") + f + "\n" + text + (text.endsWith("\n") ? "" : "\n") + f + "\n";
}
```
`web/app/src/composer/drafts.ts`:
```ts
// The composer's per-recipient state in this tab's sessionStorage, under the
// old Inbox's keys (a half-typed reply carries across the two UIs), falling
// back to memory where storage is unavailable. Never sent anywhere.
export type Draft = { text: string; sel: number };

const memory = new Map<string, string>();
function get(key: string): string | null {
  try { return sessionStorage.getItem(key); } catch { return memory.get(key) ?? null; }
}
function set(key: string, value: string | null) {
  try {
    if (value === null) sessionStorage.removeItem(key); else sessionStorage.setItem(key, value);
  } catch {
    if (value === null) memory.delete(key); else memory.set(key, value);
  }
}

export const loadReply = (to: string) => get("me-reply:" + to) ?? "";
export const saveReply = (to: string, text: string) => set("me-reply:" + to, text ? text : null);

export function loadDrafts(to: string): Draft[] {
  try {
    const d: unknown = JSON.parse(get("me-drafts:" + to) ?? "[]");
    return Array.isArray(d) ? (d as Draft[]).filter((x) => typeof x?.text === "string") : [];
  } catch { return []; }
}
export const saveDrafts = (to: string, d: Draft[]) => set("me-drafts:" + to, JSON.stringify(d));
```
`web/app/src/composer/keys.ts`:
```ts
type Keyish = { key: string; code: string; metaKey: boolean; ctrlKey: boolean; shiftKey: boolean; altKey: boolean; isComposing?: boolean };

export const isMac = () => /Mac|iPhone|iPad/.test(navigator.platform);

// isSendKey: Cmd+Enter on a Mac, Ctrl+Enter elsewhere; never with Shift or
// Alt, nor mid-composition (an IME's Enter commits the text).
export function isSendKey(e: Keyish, mac: boolean): boolean {
  const mod = mac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
  return e.key === "Enter" && mod && !e.shiftKey && !e.altKey && !e.isComposing;
}

// isPasteAsCodeKey: the browsers' own paste-as-plain-text shortcut, so a real
// paste event (no permission prompt) follows.
export function isPasteAsCodeKey(e: Keyish, mac: boolean): boolean {
  return e.code === "KeyV" && e.shiftKey && (mac ? e.metaKey : e.ctrlKey && !e.altKey);
}

export function isDraftKey(e: Keyish, mac: boolean): "push" | "pop" | null {
  if (!(mac ? e.metaKey : e.ctrlKey) || e.shiftKey || e.altKey) return null;
  return e.key === "ArrowDown" ? "push" : e.key === "ArrowUp" ? "pop" : null;
}

export const hint = (mac: boolean) => mac
  ? "⌘↵ to send · ⇧⌘V pastes as code · ⌘↓ stacks a draft"
  : "Ctrl+↵ to send · ⇧Ctrl+V pastes as code · Ctrl+↓ stacks a draft";
```

- [ ] **Step 4: Implement the composer.**

`web/app/src/composer/Composer.tsx`:
```tsx
import { Button, Checkbox, Group, Paper, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { apiPost, ApiError } from "../api/client";
import type { MessageSent, SendMessageRequest } from "../api/types";
import { asCodeBlock } from "./fence";
import { loadDrafts, loadReply, saveDrafts, saveReply } from "./drafts";
import * as keys from "./keys";

// Composer is the reply box for `to` (a channel id, or user:/session: for a
// new message). Text is kept per recipient (drafts.ts); a failed send keeps
// everything and says why.
export function Composer({ to, label, attachMax, onSent }: { to: string; label: string; attachMax: number; onSent?: (s: MessageSent) => void }) {
  void attachMax; // attachments: Task 4
  const mac = keys.isMac();
  const client = useQueryClient();
  const box = useRef<HTMLTextAreaElement>(null);
  const asCode = useRef(false);
  const [text, setTextState] = useState(() => loadReply(to));
  const [plain, setPlain] = useState(false);
  const [drafts, setDraftsState] = useState(() => loadDrafts(to));
  const [sending, setSending] = useState(false);

  useEffect(() => { setTextState(loadReply(to)); setDraftsState(loadDrafts(to)); }, [to]);
  const setText = (t: string) => { setTextState(t); saveReply(to, t); };
  const setDrafts = (d: typeof drafts) => { setDraftsState(d); saveDrafts(to, d); };

  const push = () => {
    if (!text.trim()) return false;
    setDrafts([...drafts, { text, sel: box.current?.selectionStart ?? text.length }]);
    setText("");
    return true;
  };
  const pop = (current = text) => {
    if (current.trim() || drafts.length === 0) return false;
    const top = drafts[drafts.length - 1]!;
    setDrafts(drafts.slice(0, -1));
    setText(top.text);
    requestAnimationFrame(() => { const s = Math.min(top.sel, top.text.length); box.current?.setSelectionRange(s, s); box.current?.focus(); });
    return true;
  };

  async function send() {
    const body = text.trim();
    if (!body || sending) return;
    setSending(true);
    const req: SendMessageRequest = { to, body, ...(plain ? { contentType: "text/plain" } : {}) };
    try {
      const sent = await apiPost<MessageSent>("messages", req);
      setText("");
      setPlain(false);
      pop("");
      void client.invalidateQueries({ queryKey: ["conversation", sent.channel] });
      void client.invalidateQueries({ queryKey: ["rail"] });
      onSent?.(sent);
    } catch (e) {
      notifications.show({ color: "red", message: e instanceof ApiError ? e.message : String(e) });
    } finally {
      setSending(false);
    }
  }

  return (
    <Paper p="xs" withBorder radius={0} style={{ borderLeft: 0, borderRight: 0, borderBottom: 0 }}>
      {drafts.length > 0 && (
        <Button size="compact-xs" variant="light" mb={4} title={`Restore the top stacked draft (${mac ? "⌘↑" : "Ctrl+↑"}) — the box must be empty`} onClick={() => pop()}>
          ↩ {drafts.length} stacked draft{drafts.length === 1 ? "" : "s"}
        </Button>
      )}
      <Group align="flex-end" gap="xs" wrap="nowrap">
        <Textarea ref={box} flex={1} autosize minRows={2} maxRows={12} aria-label={`Message ${label}`}
          placeholder={`Message ${label}… (${keys.hint(mac)})`} value={text}
          onChange={(e) => setText(e.currentTarget.value)}
          onKeyDown={(e) => {
            asCode.current = keys.isPasteAsCodeKey(e, mac);
            if (keys.isSendKey(e.nativeEvent, mac) || keys.isSendKey(e, mac)) { e.preventDefault(); void send(); return; }
            const d = keys.isDraftKey(e, mac);
            if (d === "push" && push()) e.preventDefault();
            else if (d === "pop" && pop()) e.preventDefault();
          }}
          onPaste={(e) => {
            if (!asCode.current) return;
            asCode.current = false;
            const pasted = e.clipboardData?.getData("text/plain") ?? "";
            if (!pasted) return;
            e.preventDefault();
            const t = e.currentTarget, start = t.selectionStart, end = t.selectionEnd;
            const block = asCodeBlock(pasted, t.value.slice(0, start));
            // execCommand keeps the browser's undo; jsdom lacks it.
            if (!document.execCommand?.("insertText", false, block)) setText(t.value.slice(0, start) + block + t.value.slice(end));
          }} />
        <Group gap={6} wrap="nowrap">
          <Checkbox size="xs" label="Plain text" title="Send as plain text: shown literally, not rendered as markdown" checked={plain} onChange={(e) => setPlain(e.currentTarget.checked)} />
          <Button size="sm" loading={sending} onClick={() => void send()}>Send {mac ? "⌘↵" : "Ctrl+↵"}</Button>
        </Group>
      </Group>
    </Paper>
  );
}
```
The `isComposing` flag lives on the native event, so `isSendKey(e.nativeEvent, …)` sees it. React's synthetic event has no `isComposing`, which is why both checks run. Simplify to `keys.isSendKey({ ...e, isComposing: e.nativeEvent.isComposing }, mac)` if you prefer, and keep the test.

When `execCommand("insertText")` succeeds it fires an input event, which reaches `onChange`, so the state stays in sync.

In `ConversationView.tsx`, render the composer under the presence strip when `head.sendTo` is set; otherwise show the old UI's read-only line:
```tsx
      {q.data.pages[0]!.sendTo
        ? <Composer to={q.data.pages[0]!.sendTo} label={q.data.pages[0]!.label} attachMax={boot.attachMax}
            onSent={() => setStick((n) => n + 1)} />
        : <Text size="sm" c="dimmed" p="xs">From before the upgrade: read-only.</Text>}
```
`ConversationView` receives `boot: Bootstrap` as a prop. In `routes/Conversation.tsx`, read it from a small context: create `web/app/src/BootContext.ts` exporting `BootContext = createContext<Bootstrap | null>(null)` and `useBoot()`. Provide it in `Shell` around `<Outlet/>`, and in `test/render.tsx` around the `RouterProvider`.

Add `const [stick, setStick] = useState(0);` and pass `stickToEnd={stick}` to `MessageList`. In `MessageList`, add `stickToEnd?: number` to the props, and add:
```tsx
  useEffect(() => { pinned.current = true; }, [stickToEnd]); // a send ends at the bottom
```
The next `messages` change (the refetch that carries the sent message) then scrolls to the end.

- [ ] **Step 5: Run the checks.**

Run: `cd web/app && npm run check`
Expected: all pass. If `userEvent.type` with `{Enter}` triggers send handling, it won't: plain Enter has no modifier, and the test asserts the text. If jsdom's `fireEvent.paste` with a plain-object `clipboardData` doesn't reach React's `onPaste` with `getData`, build a real `DataTransfer`-like object: `Object.assign(new Event("paste", { bubbles: true, cancelable: true }), { clipboardData: { getData: () => "…", files: [] } })` with `box.dispatchEvent`. Keep the assertions unchanged, and ledger the change.

- [ ] **Step 6: Commit.**
```bash
git add web/app
git commit -m "web/app: the composer (send keys, paste as code, drafts, saved reply, plain text)"
```

---

### Task 4: Attachments

**Files:**
- Create: `web/app/src/composer/upload.ts`, `upload.test.ts`, `web/app/src/composer/AttachmentChips.tsx`
- Modify: `web/app/src/composer/Composer.tsx`, `web/app/src/composer/Composer.test.tsx`

**Interfaces:**
- Consumes: `POST /app/user/uploads` (raw body; headers `Content-Type` = the file's type, `X-Filename` = `encodeURIComponent(name)`; 201 `{"ref"}`; refusals are text).
- Produces:
  - `upload(file: File, onProgress: (pct: number) => void): { done: Promise<string>; abort: () => void }`. `done` resolves to the ref, or rejects with an `Error` whose message is the server's text or "upload failed (too large, or the connection dropped)"; on abort it rejects with `AbortError`.
  - `type Pending = { id: number; name: string; pct: number; ref?: string; error?: string; abort: () => void }`.
  - `<AttachmentChips items onRemove />`.
  - `Composer` (when `attachMax > 0`) gains 📎, paste and drop handling. It sends `attachments: refs`, an empty body is allowed when files are attached, and it refuses to send while any chip is uploading.

- [ ] **Step 1: Write the failing tests.**

`web/app/src/composer/upload.test.ts`:
```ts
import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { upload } from "./upload";

describe("upload", () => {
  it("posts the raw file with its name and resolves the ref", async () => {
    let seen: { name: string | null; type: string | null; body: string } | undefined;
    server.use(http.post("/app/user/uploads", async ({ request }) => {
      seen = { name: request.headers.get("X-Filename"), type: request.headers.get("Content-Type"), body: await request.text() };
      return HttpResponse.json({ ref: "blb_1" }, { status: 201 });
    }));
    const { done } = upload(new File(["hello"], "a b.txt", { type: "text/plain" }), () => {});
    await expect(done).resolves.toBe("blb_1");
    expect(seen).toEqual({ name: "a%20b.txt", type: "text/plain", body: "hello" });
  });

  it("rejects with the server's reason", async () => {
    server.use(http.post("/app/user/uploads", () => new HttpResponse("too large (max 25 MB)", { status: 413 })));
    await expect(upload(new File(["x"], "big.bin"), () => {}).done).rejects.toThrow("too large (max 25 MB)");
  });
});
```
Append to `Composer.test.tsx`:
```tsx
describe("Composer attachments", () => {
  function mountWithFiles(onSent = vi.fn()) {
    const client = new QueryClient();
    render(
      <MantineProvider theme={theme}><Notifications />
        <QueryClientProvider client={client}><Composer to="chn_a" label="eng" attachMax={25 << 20} onSent={onSent} /></QueryClientProvider>
      </MantineProvider>,
    );
    return { box: screen.getByRole("textbox") as HTMLTextAreaElement, input: screen.getByLabelText(/Attach files/) as HTMLInputElement, onSent };
  }

  it("uploads picked files and sends their refs, with no text needed", async () => {
    let sent: { attachments?: string[]; body?: string } = {};
    server.use(
      http.post("/app/user/uploads", () => HttpResponse.json({ ref: "blb_9" }, { status: 201 })),
      http.post("/app/user/api/messages", async ({ request }) => { sent = (await request.json()) as typeof sent; return HttpResponse.json({ channel: "chn_a", seq: 3 }, { status: 201 }); }),
    );
    const { input, onSent } = mountWithFiles();
    await userEvent.upload(input, new File(["png"], "shot.png", { type: "image/png" }));
    expect(await screen.findByText("shot.png")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: /Send/ })).toBeEnabled());
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(onSent).toHaveBeenCalled());
    expect(sent).toEqual({ to: "chn_a", body: "", attachments: ["blb_9"] });
    expect(screen.queryByText("shot.png")).toBeNull();
  });

  it("refuses to send while an upload is running; a failed chip is never sent", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => { release = r; });
    let posts = 0;
    server.use(
      http.post("/app/user/uploads", async () => { await gate; return new HttpResponse("upload failed", { status: 502 }); }),
      http.post("/app/user/api/messages", () => { posts++; return HttpResponse.json({ channel: "chn_a", seq: 3 }, { status: 201 }); }),
    );
    const { box, input } = mountWithFiles();
    await userEvent.type(box, "with file");
    await userEvent.upload(input, new File(["x"], "slow.txt"));
    expect(screen.getByRole("button", { name: /Send/ })).toBeDisabled();
    release();
    expect(await screen.findByText(/slow\.txt — upload failed/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    await waitFor(() => expect(posts).toBe(1));
  });

  it("attaches a pasted file, but text on the clipboard wins", async () => {
    let uploads = 0;
    server.use(http.post("/app/user/uploads", () => { uploads++; return HttpResponse.json({ ref: "blb_p" }, { status: 201 }); }));
    const { box } = mountWithFiles();
    const file = new File(["png"], "paste.png", { type: "image/png" });
    fireEvent.paste(box, { clipboardData: { getData: () => "words", files: [file] } });
    await new Promise((r) => setTimeout(r, 30));
    expect(uploads).toBe(0);
    fireEvent.paste(box, { clipboardData: { getData: () => "", files: [file] } });
    expect(await screen.findByText("paste.png")).toBeInTheDocument();
  });
});
```
Expect `sent.body` to be `""`: the server fills in the file names (`ParticipantSendHandler.Send`), as for the old composer, which sent an empty body.

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/composer`
Expected: FAIL (`./upload` is missing; there's no "Attach files" input).

- [ ] **Step 3: Implement.**

`web/app/src/composer/upload.ts`:
```ts
// upload sends one file to /app/user/uploads as a pending attachment (raw
// body; XHR for the progress events fetch lacks). done resolves its ref.
export function upload(file: File, onProgress: (pct: number) => void): { done: Promise<string>; abort: () => void } {
  const xhr = new XMLHttpRequest();
  const done = new Promise<string>((resolve, reject) => {
    xhr.open("POST", "/app/user/uploads");
    xhr.setRequestHeader("Content-Type", file.type || "application/octet-stream");
    xhr.setRequestHeader("X-Filename", encodeURIComponent(file.name));
    xhr.upload.onprogress = (ev) => { if (ev.lengthComputable) onProgress((100 * ev.loaded) / ev.total); };
    xhr.onload = () => {
      let ref = "";
      try { ref = (JSON.parse(xhr.responseText) as { ref?: string }).ref ?? ""; } catch { /* a text refusal */ }
      if (xhr.status === 201 && ref) resolve(ref);
      else reject(new Error((xhr.responseText || `failed ${xhr.status}`).trim()));
    };
    // An early 413 often closes the connection mid-send: say so.
    xhr.onerror = xhr.ontimeout = () => reject(new Error("upload failed (too large, or the connection dropped)"));
    xhr.onabort = () => reject(new DOMException("aborted", "AbortError"));
    xhr.send(file);
  });
  return { done, abort: () => xhr.abort() };
}
```
`web/app/src/composer/AttachmentChips.tsx`:
```tsx
import { CloseButton, Group, Paper, Progress, Text } from "@mantine/core";

export type Pending = { id: number; name: string; pct: number; ref?: string; error?: string; abort: () => void };

// AttachmentChips lists the pending attachments: name, progress, ✕ (which
// aborts a running upload); a failed one says why and is never sent.
export function AttachmentChips({ items, onRemove }: { items: Pending[]; onRemove: (id: number) => void }) {
  if (items.length === 0) return null;
  return (
    <Group gap={6} mb={4}>
      {items.map((p) => (
        <Paper key={p.id} withBorder px={6} py={2} radius="sm" c={p.error ? "red" : undefined}>
          <Group gap={4} wrap="nowrap">
            <Text size="xs">{p.error ? `${p.name} — ${p.error}` : p.name}</Text>
            {!p.ref && !p.error && <Progress w={60} size="xs" value={p.pct} aria-label={`Uploading ${p.name}`} />}
            <CloseButton size="xs" aria-label={`Remove ${p.name}`} onClick={() => onRemove(p.id)} />
          </Group>
        </Paper>
      ))}
    </Group>
  );
}
```
In `Composer.tsx`:
- remove `void attachMax;`;
- add the `Pending[]` state and an `attach(files: Iterable<File>)` function that starts `upload` for each file, updating `pct`, `ref` and `error` by id (an `AbortError` removes the chip);
- render `<AttachmentChips>` above the textarea;
- when `attachMax > 0`, add a `FileButton` (Mantine) labelled "Attach files (or paste / drop them; max N MB each)", with `multiple`, and an `<input>` that has an `aria-label`;
- `onPaste`: after the paste-as-code check, if `attachMax > 0`, `clipboardData.files.length` is non-zero and `getData("text/plain")` is empty, call `preventDefault()` and `attach(files)`;
- `onDragOver`/`onDrop` on the `Paper` root: on drop, if files are present, call `preventDefault()` and `attach`;
- `send()`:
  - if any chip is uploading, notify "Still uploading…" and return;
  - `refs` are the chips that have a `ref`;
  - allow an empty body when there are refs;
  - send `attachments: refs` only when non-empty;
  - on success, clear the chips;
- the Send button is `disabled={uploading}`.

Mantine's `FileButton` renders a hidden `<input type="file">` that `getByLabelText` can't reach. Instead, render your own hidden `<input type="file" multiple aria-label="Attach files (…)" hidden onChange={…} />` next to a 📎 `ActionIcon` that calls `inputRef.current.click()`. `userEvent.upload` targets the input.

- [ ] **Step 4: Run the checks.**

Run: `cd web/app && npm run check`
Expected: all pass. MSW intercepts `XMLHttpRequest` in Node through its interceptors. If upload progress events aren't emitted in jsdom, the tests don't depend on them.

- [ ] **Step 5: Commit.**
```bash
git add web/app
git commit -m "web/app: attachments in the composer (📎, paste, drop, chips with progress)"
```

---

### Task 5: New message

**Files:**
- Create: `web/app/src/compose/RecipientPicker.tsx`, `web/app/src/compose/NewMessage.tsx`, `web/app/src/compose/NewMessage.test.tsx`
- Modify: `web/app/src/App.tsx` (route), `web/app/src/rail/Rail.tsx` (the New message button above the groups, also shown in the empty state)

**Interfaces:**
- Consumes: `GET api/recipients` → `RecipientsResponse`; `Composer`; `useBoot()`.
- Produces:
  - the route `/app/next/new?to=<to>&label=<label>`;
  - `<RecipientPicker opened onClose />`: a Mantine `Modal` with a searchable list grouped by project, showing each option's kind; choosing one navigates to `/app/next/new?…`;
  - `NewMessage`: a header ("New message", "to <label>"), an empty state, and a `Composer` for `to`. On send it navigates (replacing history) to `/app/next/c/<sent.channel>`.

- [ ] **Step 1: Write the failing test.**

`web/app/src/compose/NewMessage.test.tsx`:
```tsx
import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, RecipientsResponse } from "../api/types";

const recipients: RecipientsResponse = { options: [
  { to: "user:usr_bob", label: "bob", kind: "person", project: "test" },
  { to: "session:ses_spider", label: "intercom-spider", kind: "agent", project: "test" },
  { to: "chn_eng", label: "eng", kind: "room", project: "other" },
] };

describe("New message", () => {
  it("picks a recipient, sends, and lands in the new conversation", async () => {
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
      http.get("/app/user/api/recipients", () => HttpResponse.json(recipients)),
      http.post("/app/user/api/messages", () => HttpResponse.json({ channel: "chn_new", seq: 1 }, { status: 201 })),
      http.get("/app/user/api/conversations/chn_new", () => HttpResponse.json({
        channelId: "chn_new", label: "bob", kind: "chat", project: "test", phase: "", needsYou: false, sendTo: "chn_new",
        messages: [{ from: "alice", body: "<p>hello bob</p>", raw: "hello bob", plain: false, at: "", time: new Date().toISOString(), mine: true, seq: 1, system: false, sysText: "", card: "" }],
        lastSeq: 1, earlier: false, sessionIds: [], sessions: [], canJoin: false, canLeave: false, callIn: [],
      } satisfies Conversation)),
      http.get("/app/user/api/conversations/chn_new/presence", () => HttpResponse.json({ sessions: [] })),
      http.post("/app/user/api/conversations/chn_new/read", () => new HttpResponse(null, { status: 204 })),
    );
    renderApp(boot);
    await userEvent.click(await screen.findByRole("button", { name: "New message" }));
    await userEvent.type(screen.getByPlaceholderText("Search people, sessions, rooms…"), "bo");
    expect(screen.queryByText("eng")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: /bob/ }));
    expect(await screen.findByText("to bob")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Message bob" }), "hello bob");
    await userEvent.click(screen.getByRole("button", { name: /Send/ }));
    expect(await screen.findByRole("heading", { name: "bob" })).toBeInTheDocument(); // the conversation view
    expect(screen.getByText("hello bob")).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run it to verify it fails.**

Run: `cd web/app && npx vitest run src/compose`
Expected: FAIL (there's no "New message" button).

- [ ] **Step 3: Implement.**

`web/app/src/compose/RecipientPicker.tsx`:
```tsx
import { Badge, Group, Modal, ScrollArea, Stack, Text, TextInput, UnstyledButton } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { apiGet, type ApiError } from "../api/client";
import type { RecipientsResponse } from "../api/types";

// RecipientPicker lists whom the viewer may start a conversation with,
// grouped by project; picking one opens the New message composer.
export function RecipientPicker({ opened, onClose }: { opened: boolean; onClose: () => void }) {
  const [q, setQ] = useState("");
  const nav = useNavigate();
  const { data } = useQuery<RecipientsResponse, ApiError>({ queryKey: ["recipients"], queryFn: () => apiGet("recipients"), enabled: opened });
  const shown = (data?.options ?? []).filter((o) => o.label.toLowerCase().includes(q.trim().toLowerCase()));
  const projects = [...new Set(shown.map((o) => o.project))];
  return (
    <Modal opened={opened} onClose={onClose} title="New message">
      <TextInput data-autofocus placeholder="Search people, sessions, rooms…" value={q} onChange={(e) => setQ(e.currentTarget.value)} mb="sm" />
      <ScrollArea.Autosize mah={360}>
        {projects.map((p) => (
          <Stack key={p} gap={2} mb="xs">
            <Text size="xs" fw={700} c="dimmed" tt="uppercase">{p}</Text>
            {shown.filter((o) => o.project === p).map((o) => (
              <UnstyledButton key={o.to} px="xs" py={4} onClick={() => { onClose(); nav(`/app/next/new?to=${encodeURIComponent(o.to)}&label=${encodeURIComponent(o.label)}`); }}>
                <Group gap="xs"><Text size="sm">{o.label}</Text><Badge size="xs" variant="light">{o.kind}</Badge></Group>
              </UnstyledButton>
            ))}
          </Stack>
        ))}
        {data && shown.length === 0 && <Text size="sm" c="dimmed">No one matches.</Text>}
      </ScrollArea.Autosize>
    </Modal>
  );
}
```
`web/app/src/compose/NewMessage.tsx`:
```tsx
import { Stack, Text, Title } from "@mantine/core";
import { useNavigate, useSearchParams } from "react-router";
import { useBoot } from "../BootContext";
import { Composer } from "../composer/Composer";

// NewMessage starts a conversation with ?to=; the first send lands in it.
export function NewMessage() {
  const [params] = useSearchParams();
  const to = params.get("to") ?? "";
  const label = params.get("label") ?? to;
  const nav = useNavigate();
  const boot = useBoot();
  return (
    <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height) - 2 * var(--app-shell-padding))">
      <div style={{ padding: "var(--mantine-spacing-xs) var(--mantine-spacing-sm)", borderBottom: "1px solid var(--mantine-color-default-border)" }}>
        <Title order={2} size="h4">New message</Title>
        <Text size="xs" c="dimmed">to {label}</Text>
      </div>
      <Text c="dimmed" flex={1} p="md">New conversation with {label}.</Text>
      <Composer key={to} to={to} label={label} attachMax={boot.attachMax}
        onSent={(s) => nav(`/app/next/c/${encodeURIComponent(s.channel)}`, { replace: true })} />
    </Stack>
  );
}
```
The heading is "New message", which doesn't clash with the test's `heading` query for "bob" after navigation.

`App.tsx`: add the route `{ path: "new", element: <NewMessage /> }`.

`Rail.tsx`: add a header row above the groups, `<Button size="xs" variant="light" onClick={open}>New message</Button>`, with `useDisclosure` from `@mantine/hooks` and `<RecipientPicker opened onClose />`. Render it in every branch (loading, empty and normal), so a member with no conversations can start one. Change the empty-state text to "No conversations yet. Start one with New message."

- [ ] **Step 4: Run the checks.**

Run: `cd web/app && npm run check`
Expected: all pass.

- [ ] **Step 5: Commit.**
```bash
git add web/app
git commit -m "web/app: New message (recipient picker, compose, land in the conversation)"
```

---

### Task 6: Sounds

**Files:**
- Create:
  - `web/app/src/sounds/sounds.ts`, `sounds.test.ts`
  - `web/app/src/sounds/chimes.ts`, `chimes.test.ts`
  - `web/app/src/sounds/SoundsSettings.tsx`, `SoundsSettings.test.tsx`
- Modify: `web/app/src/rail/Rail.tsx` (chime on rail changes), `web/app/src/shell/TitleBar.tsx` (the "Sounds…" menu item), `web/app/src/start.tsx` (`initSounds()`)

**Interfaces:**
- Produces:
  - `CHIMES` (`pop`, `chime`, `ping`, `bell`) and `type SoundSettings = { messages: string; needs: string; volume: number }`.
  - `loadSounds(): SoundSettings` and `storeSounds(s)`, which writes localStorage `jam-sounds` and IndexedDB `jam`/`prefs` key `sounds`.
  - `play(kind: "messages" | "needs"): boolean`, at most one chime a second across tabs, via `jam-sounds-last`.
  - `preview(name, volume)`.
  - `initSounds()`: unlocks audio on the first gesture, keeps the `chimer` heartbeat every 20 s while audio runs, and calls `storeSounds(loadSounds())`.
  - `chimeFor(prev: Map<string, {u: number; n: boolean}> | null, rows: ChannelRow[], open: string | undefined, looking: boolean): "needs" | "messages" | ""`.
  - `<SoundsSettings opened onClose />`.

- [ ] **Step 1: Write the failing tests.**

`web/app/src/sounds/chimes.test.ts`:
```ts
import { describe, expect, it } from "vitest";
import type { ChannelRow } from "../api/types";
import { chimeFor, snapshot } from "./chimes";

const row = (id: string, unread: number, needsYou = false) => ({ id, unread, needsYou }) as ChannelRow;

describe("chimeFor", () => {
  it("is silent for the first rail (the baseline)", () => {
    expect(chimeFor(null, [row("a", 3)], undefined, false)).toBe("");
  });
  it("chimes messages for more unread, needs when something starts needing you", () => {
    const prev = snapshot([row("a", 1), row("b", 0)]);
    expect(chimeFor(prev, [row("a", 2), row("b", 0)], undefined, false)).toBe("messages");
    expect(chimeFor(prev, [row("a", 2), row("b", 0, true)], undefined, false)).toBe("needs");
    expect(chimeFor(prev, [row("a", 1), row("b", 0)], undefined, false)).toBe("");
    expect(chimeFor(prev, [row("a", 0)], undefined, false)).toBe(""); // read: no chime
  });
  it("never chimes for the conversation you're looking at", () => {
    const prev = snapshot([row("a", 1)]);
    expect(chimeFor(prev, [row("a", 2)], "a", true)).toBe("");
    expect(chimeFor(prev, [row("a", 2)], "a", false)).toBe("messages"); // open but not looking
    expect(chimeFor(prev, [row("c", 1)], "a", true)).toBe("messages"); // a new conversation
  });
});
```
`web/app/src/sounds/sounds.test.ts`:
```ts
import { beforeEach, describe, expect, it } from "vitest";
import { loadSounds, storeSounds } from "./sounds";

beforeEach(() => localStorage.clear());

describe("sound settings", () => {
  it("defaults, and reads the old Inbox's key", () => {
    expect(loadSounds()).toEqual({ messages: "pop", needs: "chime", volume: 0.6 });
    localStorage.setItem("jam-sounds", JSON.stringify({ messages: "off", needs: "bell", volume: 0.2 }));
    expect(loadSounds()).toEqual({ messages: "off", needs: "bell", volume: 0.2 });
  });
  it("rejects junk", () => {
    localStorage.setItem("jam-sounds", JSON.stringify({ messages: "kazoo", needs: 3, volume: 7 }));
    expect(loadSounds()).toEqual({ messages: "pop", needs: "chime", volume: 0.6 });
  });
  it("stores for the page", () => {
    storeSounds({ messages: "ping", needs: "off", volume: 1 });
    expect(JSON.parse(localStorage.getItem("jam-sounds")!)).toEqual({ messages: "ping", needs: "off", volume: 1 });
  });
});
```
`web/app/src/sounds/SoundsSettings.test.tsx`:
```tsx
import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { theme } from "../theme";
import { SoundsSettings } from "./SoundsSettings";

beforeEach(() => localStorage.clear());

describe("SoundsSettings", () => {
  it("saves a changed chime", async () => {
    render(<MantineProvider theme={theme}><SoundsSettings opened onClose={() => {}} /></MantineProvider>);
    await userEvent.click(screen.getByRole("textbox", { name: "Messages" }));
    await userEvent.click(await screen.findByRole("option", { name: "Bell" }));
    expect(JSON.parse(localStorage.getItem("jam-sounds")!).messages).toBe("bell");
    expect(screen.getByText(/With Jam closed, notifications use your system's sound/)).toBeInTheDocument();
  });
});
```
In jsdom, IndexedDB is absent; `storeSounds` must swallow that, as `put` does in `sounds.js`.

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/sounds`
Expected: FAIL (the modules don't exist).

- [ ] **Step 3: Implement.**

`web/app/src/sounds/sounds.ts` ports `internal/jam/uiassets/static/sounds.js` (read it in full first), keeping these exactly the same:
- the keys;
- `CHIMES`' notes;
- `tone`'s envelope (0.35 × volume peak, exponential ramps);
- the one-chime-a-second rule across tabs, with "needs" never held back by "messages";
- the 20 s `chimer` heartbeat only while the `AudioContext` is running;
- unlocking on the first `pointerdown`/`keydown` (capture, once).

Export the functions named in Interfaces. `put(key, value)` opens IndexedDB `jam` v1, creating the `prefs` store on upgrade, and swallows every error. Don't set `window.jamSound`; the app imports `play` directly.

`web/app/src/sounds/chimes.ts`:
```ts
import { useEffect, useRef } from "react";
import type { ChannelRow, RailResponse } from "../api/types";
import { play } from "./sounds";

export type Snap = Map<string, { u: number; n: boolean }>;
export const snapshot = (rows: ChannelRow[]): Snap => new Map(rows.map((r) => [r.id, { u: r.unread, n: r.needsYou }]));

// chimeFor is the chime a rail refresh calls for: "needs" when a conversation
// starts needing you, else "messages" when one has more unread — never for
// the one you're looking at, and nothing for the first rail (the baseline).
export function chimeFor(prev: Snap | null, rows: ChannelRow[], open: string | undefined, looking: boolean): "needs" | "messages" | "" {
  if (!prev) return "";
  let kind: "needs" | "messages" | "" = "";
  for (const r of rows) {
    if (looking && r.id === open) continue;
    const p = prev.get(r.id);
    if (r.needsYou && !p?.n) kind = "needs";
    else if (r.unread > (p?.u ?? 0) && kind !== "needs") kind = "messages";
  }
  return kind;
}

// useChimes plays chimeFor's sound on each new rail.
export function useChimes(rail: RailResponse | undefined, open: string | undefined): void {
  const prev = useRef<Snap | null>(null);
  useEffect(() => {
    if (!rail) return;
    const rows = rail.groups.flatMap((g) => g.rows);
    const looking = document.visibilityState === "visible" && document.hasFocus();
    const kind = chimeFor(prev.current, rows, open, looking);
    prev.current = snapshot(rows);
    if (kind) play(kind);
  }, [rail, open]);
}
```
`web/app/src/sounds/SoundsSettings.tsx`: a `Modal` titled "Sounds". It has `Select`s labelled "Messages" and "Needs you", with options Off/Pop/Chime/Ping/Bell (values `off`, `pop`, `chime`, `ping`, `bell`), each with a ▶ `ActionIcon` (aria-label "Play the Messages sound" / "Play the Needs you sound") that calls `preview`, and a `Slider` labelled "Volume" (0–100). Every change calls `storeSounds`. Add the note "Chimes play while Jam is open. With Jam closed, notifications use your system's sound."

Wiring:
- In `Rail.tsx`, call `useChimes(data, channel)`. Because `useRail` uses `keepPreviousData`, a selection change keeps the same data object, so it doesn't chime twice.
- In `TitleBar.tsx`, add a `Menu.Item` "Sounds…" that opens `SoundsSettings`.
- In `start.tsx`, call `initSounds()` once before rendering.

- [ ] **Step 4: Run the checks.**

Run: `cd web/app && npm run check`
Expected: all pass.

- [ ] **Step 5: Commit.**
```bash
git add web/app
git commit -m "web/app: notification chimes and per-device Sounds settings (sounds.js port)"
```

---

### Task 7: End-to-end, docs

**Files:**
- Modify: `internal/jam/userui/next_browser_test.go`, `docs/web-app.md`, `docs/usage/jam/intercom-ui.md`

- [ ] **Step 1: Write the browser test.**

Append to `internal/jam/userui/next_browser_test.go` (add `"github.com/chromedp/cdproto/input"` if needed):
```go
// Typing a reply and pressing Ctrl+Enter (headless Chrome reports a Linux
// platform) sends it through POST api/messages; it shows in the list, and
// the box is empty again. Shift+Ctrl+V pastes as code.
func TestBrowserNextComposerSends(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	e.Send = jam.NewParticipantSendHandler(e.st, e.Intercom, nil)
	changes := &testChanges{}
	api, app := Handler(e.Deps, nil, WithChanges(changes)), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = appui.WithPrincipal(jam.WithParticipant(r, p), appui.Principal{User: &p})
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	box := chromedp.CSS(`textarea[aria-label="Message eng"]`)
	var problems pageProblems
	var value string
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(box),
		chromedp.SendKeys(box, "sent from the app"),
		chromedp.KeyEvent("\r", chromedp.KeyModifiers(chromedp.ModifierCtrl)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	for i := 0; i < 50 && !strings.Contains(last, "sent from the app"); i++ {
		if err := chromedp.Do(ctx, into(&last, chromedp.Evaluate[string](`[...document.querySelectorAll('[data-seq]')].pop()?.innerText ?? ''`))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(last, "sent from the app") {
		t.Errorf("sent message didn't show; last row = %q", last)
	}
	if err := chromedp.Do(ctx, into(&value, chromedp.Value(box))); err != nil || value != "" {
		t.Errorf("box after send = %q, %v", value, err)
	}
	ms := e.lg.ChannelSince(e.room.ID, 0, 0)
	if got := ms[len(ms)-1]; got.Body != "sent from the app" || got.From != p.UserID {
		t.Errorf("log's last = %+v", got)
	}

	// Paste as code (the composer marks the box on the shortcut's keydown and
	// wraps the real paste that follows).
	err = chromedp.Do(ctx,
		copy(srv.URL, "a ```b``` c"),
		chromedp.SendKeys(box, "see:"),
		chromedp.KeyEvent("V", chromedp.KeyModifiers(chromedp.ModifierCtrl, chromedp.ModifierShift)),
		into(&value, chromedp.Value(box)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := "see:\n````\na ```b``` c\n````\n"; value != want {
		t.Errorf("paste as code: box = %q, want %q", value, want)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}
```
`copy` is the clipboard helper in `browser_test.go`; it's in the same package.

Run: `./scripts/app.sh && go test -tags browser ./internal/jam/userui/ -run TestBrowserNext -v`
Expected: PASS. If `KeyEvent("\r", …)` doesn't produce `key: "Enter"` with Ctrl, use `chromedp.KeyEvent(kb.Enter, …)` from `github.com/chromedp/chromedp/kb`, and ledger the change.

- [ ] **Step 2: Docs.**

`docs/web-app.md`, under Layout:
- `src/composer/`: the composer, with pure `fence`, `drafts` (the old Inbox's sessionStorage keys), `keys` and `upload` (XHR to `/app/user/uploads`) modules;
- `src/compose/`: New message;
- `src/sounds/`: the `sounds.js` port, which keeps its localStorage/IndexedDB contract with the service worker.

Also note that `POST /app/user/api/messages` shares `jam.ParticipantSendHandler.Send` with `/app/user/send`.

`docs/usage/jam/intercom-ui.md`: in the preview section, replace "Replying, asks and whiteboards still happen here." with "You can reply, start new messages and attach files there too; asks and whiteboards still happen here."

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: no new errors except this plan file's frontmatter and orphan lines, which every plan has.

- [ ] **Step 3: The whole suite, then commit.**

Run: `go test ./... && just app-check && ./scripts/app.sh && just test-browser && just lint`
Expected: all green; the size budget holds.
```bash
git add internal/jam/userui/next_browser_test.go docs
git commit -m "Jam app: composer end-to-end test and docs"
```
Open one PR, "Jam app slice 3: composer".

---

## Self-review notes

- **Spec coverage (§3 slice 3):**
  - recipient picker: Task 5;
  - Cmd/Ctrl+Enter and plain-text toggle: Task 3;
  - attachments (📎, paste, drop, chips, thumbnails): Task 4 (thumbnails already render in the message list from slice 2);
  - notification sounds: Task 6;
  - the old composer's saved reply, draft stack and paste-as-code: Task 3.
- **Spec coverage (§2):** the `api/messages` JSON sibling with the error envelope (Tasks 1–2); uploads unchanged (spec: "stay exactly as they are").
- **Deferred on purpose:** Discuss (`replyTo`) is wired in the API and the composer request type, but the Discuss button arrives with ask cards in slice 4; Web Push is slice 8.
