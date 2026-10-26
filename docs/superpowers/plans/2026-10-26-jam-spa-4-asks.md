# Jam SPA, slice 4: asks — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** structured asks work in the Jam app, at parity with the old Inbox:
- an ask renders as a card where it was asked, showing its current state (open with options, answered "✓ X chose Y — comment", or withdrawn with its reason), "edited" and "for …";
- the open-asks panel sits above the stream;
- the viewer answers with an option, or with Other… plus a comment;
- Discuss starts a reply about the ask (the composer carries `replyTo` and a "re:" line).

**Architecture:**
- `AskCard` becomes an API type, and `MessageRow.ask` and `Conversation.openAsks` join the JSON.
- `POST /app/user/api/asks/{id}/answer` shares one error mapping with the old HTML route, so both say the same thing.
- The app adds an `AskCard` component, an `OpenAsks` panel, and a `discuss` handle on the composer (React's `useImperativeHandle`).

**Tech Stack:** the slice 1–3 stack. No new dependencies.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md) §3 slice 4 ("ask cards inline in the conversation, the open-asks ('Needs you') panel, answer with comment, and Discuss replies"), plus §2 and §4. The helpers from the earlier slices are on main: `writeJSON`, `writeAPIError`, `decodeJSON`, `notFound`, `getConv`, `apiPost` (Go test), `apiGet`/`apiPost` (TS), `renderApp`, `boot`, `pageProblems`, `testChanges`, and the `env.ask`/`yesNoOpts` test helpers in `asks_test.go`.

## Global Constraints

- **Parity:** everything the old ask card and panel do works. The old `POST /app/user/asks/{id}/answer` keeps its exact status codes and texts, and its tests (`asks_test.go`) pass unchanged.
- **JSON:**
  - API errors use `{"error","code"}`.
  - Writes are JSON (`decodeJSON`).
  - Lists are `[]`, never `null`.
  - Optional row fields use `omitempty`.
- **Message HTML:** it still comes only from `squawkrender`. Ask questions, options, comments and reasons are plain text, rendered as text nodes, never as HTML.
- **Live refreshes:** a refresh must never wipe a half-typed answer comment.
- **Size budget:** initial JS ≤ 350 KB gzipped (it's 202 KB now).
- **Docs:** they ship in the same change.

## Review Focus

1. **A stale or already-answered ask:**
   - An answer to an ask amended since it rendered gets 409 "this ask changed — review the new version". The card shows the new version and nothing is recorded.
   - An ask someone else already answered says "already answered by <who>: <choice>".
   (Task 2 Go test; Task 3 component test.)
2. **Other… without a comment** can't be sent: the client refuses it, and the server's 400 is shown if it's reached. (Task 3 test.)
3. **A half-typed comment survives** a live refresh of the conversation. (Task 3 test.)
4. **An ask for someone else** shows its options read-only, with "for <names>", and no answer form. (Task 1 Go test; Task 3 test.)
5. **Discuss when the reply box already has text** sets `replyTo` without overwriting the text. With an empty box it pre-fills "re: <question>". Sending carries `replyTo` once and then clears it. (Task 4 test.)

---

## File structure

| File | Responsibility |
|---|---|
| `internal/jam/userui/apitypes.go` (modify) | `AskCard` (moved from `asks.go`) with JSON tags; `MessageRow.Ask` → `ask,omitempty`; `Conversation.OpenAsks` → `openAsks`; `AnswerRequest`. |
| `internal/jam/userui/asks.go` (modify) | `answerOutcome(err, id)`, shared by the HTML and API routes; `answerAsk` uses it. |
| `internal/jam/userui/api_asks.go`, `api_asks_test.go` (new) | `POST api/asks/{id}/answer`. |
| `internal/jam/userui/api_conversation.go`, `api_conversation_test.go` (modify) | `lists()` normalizes `OpenAsks`; the shape test now expects `ask` and `openAsks`. |
| `tygo.yaml` (modify) | Maps `intercom.AskOption`. |
| `web/app/src/asks/AskCard.tsx`, `OpenAsks.tsx`, `AskCard.test.tsx` (new) | The card and the panel. |
| `web/app/src/conversation/Message.tsx`, `MessageList.tsx`, `ConversationView.tsx` (modify) | Render cards; the panel; wire Discuss. |
| `web/app/src/composer/Composer.tsx`, `Composer.test.tsx` (modify) | A `discuss` handle; `replyTo`; the "re:" chip. |
| `internal/jam/userui/next_browser_test.go` (modify) | End-to-end answer. |
| `docs/web-app.md`, `docs/usage/jam/intercom-ui.md` (modify) | Docs. |

---

### Task 1: Asks in the API's view models

**Files:**
- Modify: `internal/jam/userui/apitypes.go`, `internal/jam/userui/asks.go`, `internal/jam/userui/api_conversation.go`, `internal/jam/userui/api_conversation_test.go`, `tygo.yaml`, `web/app/src/api/types.ts` (generated)

**Interfaces:**
- Produces:
  - `AskCard{id, question, options: {id, label, detail?}[], version, state ("open" | "answered" | "withdrawn"), answeredBy, choice, comment, reason, edited, canAnswer, for}`.
  - `MessageRow.ask?: AskCard`.
  - `Conversation.openAsks: AskCard[]`.
  - `AnswerRequest{version, choice, comment?}`.

- [ ] **Step 1: Write the failing tests.**

In `TestConversationJSONShape` (`api_conversation_test.go`):
- add `"openAsks"` to the list of keys that must be present;
- remove `"OpenAsks"` and `"openAsks"` from the list of template-only keys;
- remove `"ask"` and `"Ask"` from the message's forbidden keys, and assert instead that `m["ask"]` is a map with `"id": "a1"`.

The test's `Ask: &AskCard{ID: "a1"}` stays as it is.

Append to `api_conversation_test.go`:
```go
// The conversation carries its asks as cards: open ones in openAsks and on
// their own rows; one for someone else is read-only and says for whom.
func TestAPIConversationAsks(t *testing.T) {
	e, p := fixture()
	carol, err := e.st.CreateUser(jam.User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	open := e.ask(intercom.AskEvent{Question: "Merge PR #12?", Options: yesNoOpts})
	forCarol := e.ask(intercom.AskEvent{Question: "Carol only?", Options: yesNoOpts, Of: []ident.ID{carol.ID}})
	_, c, body := getConv(t, e, p, engID, "")
	if len(c.OpenAsks) != 2 {
		t.Fatalf("openAsks = %d: %s", len(c.OpenAsks), body)
	}
	cards := map[string]*AskCard{}
	for i := range c.Messages {
		if a := c.Messages[i].Ask; a != nil {
			cards[a.ID] = a
			if c.Messages[i].Card != "ask" {
				t.Errorf("ask row card = %q", c.Messages[i].Card)
			}
		}
	}
	if a := cards[open.ID]; a == nil || !a.CanAnswer || a.State != "open" || a.Version != 1 || len(a.Options) != 2 || a.Options[1].Detail != "wait for CI" {
		t.Errorf("open card = %+v", a)
	}
	if a := cards[forCarol.ID]; a == nil || a.CanAnswer || a.For != "carol" {
		t.Errorf("card for carol = %+v", a)
	}
	if !strings.Contains(body, `"options":[{"id":"yes","label":"Ship it"},{"id":"no","label":"Hold","detail":"wait for CI"}]`) {
		t.Errorf("options JSON: %s", body)
	}
}
```
This uses `env.ask` and `yesNoOpts` from `asks_test.go`, which is in the same package.

- [ ] **Step 2: Run them to verify they fail.**

Run: `go test ./internal/jam/userui/ -run 'TestConversationJSONShape|TestAPIConversationAsks'`
Expected: FAIL. The shape test finds no `openAsks`, and `c.Messages[i].Ask` stays nil after decoding because the field is `json:"-"`.

- [ ] **Step 3: Implement.**

Move `AskCard` (with its doc comment) from `asks.go` to `apitypes.go`:
```go
// AskCard is one ask as the viewer sees it.
type AskCard struct {
	ID       string               `json:"id"`
	Question string               `json:"question"`
	Options  []intercom.AskOption `json:"options"`
	Version  int                  `json:"version"`
	State    string               `json:"state"` // open | answered | withdrawn
	AnsweredBy string             `json:"answeredBy"`
	Choice   string               `json:"choice"` // the chosen option's label ("other" for Other)
	Comment  string               `json:"comment"`
	Reason   string               `json:"reason"`
	Edited   bool                 `json:"edited"`
	CanAnswer bool                `json:"canAnswer"`
	For      string               `json:"for"` // who it is for, when Of names others than the viewer
}

// AnswerRequest is POST api/asks/{id}/answer: the version answered (a
// stale one is refused), an option id or "other", and a comment (needed
// for "other").
type AnswerRequest struct {
	Version int    `json:"version"`
	Choice  string `json:"choice"`
	Comment string `json:"comment,omitempty"`
}
```
Then run gofmt, which aligns the fields. Add `"github.com/aethons-tools/cove/internal/intercom"` to `apitypes.go`'s imports.
- In `MessageRow`, change `Ask *AskCard \`json:"-"\`` to `Ask *AskCard \`json:"ask,omitempty"\`` and update its comment to "// the ask's card (Card == "ask")".
- In `Conversation`, change `OpenAsks []AskCard \`json:"-"\`` to `\`json:"openAsks"\``.
- In `lists()`, add the same nil → `[]AskCard{}` normalization for `OpenAsks`.
- In `askCard`, an ask without options leaves `Options` nil. Normalize it to `[]intercom.AskOption{}` at the end of `askCard` (`if c.Options == nil { c.Options = []intercom.AskOption{} }`).

In `tygo.yaml`, under `type_mappings`:
```yaml
      intercom.AskOption: "{ id: string; label: string; detail?: string }"
```
Then run `go generate ./internal/jam/userui/`.

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/jam/userui/ && ./scripts/app-types-check.sh || git diff --stat web/app/src/api/types.ts; (cd web/app && npx tsc --noEmit)`
Expected: PASS, with every old template and ask test unchanged. `types.ts` gains `AskCard` and `AnswerRequest`, `ask?: AskCard` on `MessageRow`, and `openAsks: AskCard[]` on `Conversation`. tsc is clean. If tsc fails in a test fixture that builds a `Conversation` literal (slice 2/3 tests), add `openAsks: []` to that fixture.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/userui tygo.yaml web/app
git commit -m "userui: asks in the API's view models (AskCard, ask, openAsks)"
```

---

### Task 2: `POST api/asks/{id}/answer`

**Files:**
- Create: `internal/jam/userui/api_asks.go`, `internal/jam/userui/api_asks_test.go`
- Modify: `internal/jam/userui/asks.go`, `internal/jam/userui/api.go`

**Interfaces:**
- Consumes: `AnswerRequest` (Task 1); `Intercom.AnswerAsk(user, id, version, choice, comment)`.
- Produces:
  - `answerOutcome(d Deps, id string, err error) (status int, code, msg string)`. `err == nil` gives `(204, "", "")`, and the texts are the old route's exactly. The codes are:
    - `ask_changed` (409)
    - `ask_closed` (409; the message names who answered, if anyone did)
    - `bad_request` (400)
    - `not_found` (404)
    - `forbidden` (403)
    - `internal` (500)
  - `POST /app/user/api/asks/{id}/answer` answers 204 or the envelope.

- [ ] **Step 1: Write the failing tests.**

`internal/jam/userui/api_asks_test.go`:
```go
package userui

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

func answerJSON(t *testing.T, e *env, p jam.Participant, id, body string) (int, string) {
	t.Helper()
	rec := apiPost(t, Handler(e.Deps, nil), p, "/app/user/api/asks/"+id+"/answer", body)
	return rec.Code, rec.Body.String()
}

func TestAPIAnswerAsk(t *testing.T) {
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge?", Options: yesNoOpts})
	if code, body := answerJSON(t, e, p, m.ID, `{"version":1,"choice":"no","comment":"after lunch"}`); code != http.StatusNoContent {
		t.Fatalf("answer = %d %s", code, body)
	}
	_, c, _ := getConv(t, e, p, engID, "")
	var card *AskCard
	for i := range c.Messages {
		if c.Messages[i].Ask != nil {
			card = c.Messages[i].Ask
		}
	}
	if card == nil || card.State != "answered" || card.AnsweredBy != "alice" || card.Choice != "Hold" || card.Comment != "after lunch" || len(c.OpenAsks) != 0 {
		t.Fatalf("after answering: card %+v, openAsks %d", card, len(c.OpenAsks))
	}
	if code, body := answerJSON(t, e, p, m.ID, `{"version":1,"choice":"yes"}`); code != 409 || !strings.Contains(body, `"code":"ask_closed"`) || !strings.Contains(body, "already answered by alice: Hold") {
		t.Errorf("second answer = %d %s", code, body)
	}
}

func TestAPIAnswerAskRefusals(t *testing.T) {
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge?", Options: yesNoOpts})
	if _, err := e.lg.Append(intercom.Squawk{Channel: e.room.ID, From: askerID, Body: "edited", Parts: []intercom.Part{intercom.AskPart(intercom.AskEvent{Event: intercom.AskEventAmend, AskID: m.ID, Question: "Merge now?"})}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, body string
		code     int
		want     string
	}{
		{m.ID, `{"version":1,"choice":"yes"}`, 409, `"code":"ask_changed"`},
		{m.ID, `{"version":2,"choice":"other"}`, 400, `"code":"bad_request"`},
		{"nope", `{"version":1,"choice":"yes"}`, 404, `"code":"not_found"`},
		{m.ID, `{"version":`, 400, `"code":"bad_request"`},
	} {
		if code, body := answerJSON(t, e, p, tc.id, tc.body); code != tc.code || !strings.Contains(body, tc.want) {
			t.Errorf("answer %s %s = %d %s, want %d %s", tc.id, tc.body, code, body, tc.code, tc.want)
		}
	}
	bob, err := e.st.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	pb := jam.Participant{UserID: bob.ID, Name: "bob", Projects: []string{"proj"}}
	if code, body := answerJSON(t, e, pb, m.ID, `{"version":2,"choice":"yes"}`); code != 403 || !strings.Contains(body, `"code":"forbidden"`) {
		t.Errorf("non-member = %d %s", code, body)
	}
}

// The HTML route and the API say the same thing for every outcome.
func TestAnswerOutcomeMatchesTheOldRoute(t *testing.T) {
	e, _ := fixture()
	for _, err := range []error{intercom.ErrAskChanged, intercom.ErrAskNotFound, intercom.ErrAskForbidden, jam.ErrSendDenied, fmt.Errorf("%w: other needs a comment", intercom.ErrInvalid)} {
		status, code, msg := answerOutcome(e.Deps, "x", err)
		if status < 400 || code == "" || msg == "" {
			t.Errorf("answerOutcome(%v) = %d %q %q", err, status, code, msg)
		}
	}
}
```
The `other`-without-comment case expects 400. If `AnswerAsk` accepts an empty comment for `other` (check `intercom.AskEvent` validation for answers), change that row to `{"version":2,"choice":"maybe"}`, an unknown option, which must be `ErrInvalid` → 400, and ledger the change.

- [ ] **Step 2: Run them to verify they fail.**

Run: `go test ./internal/jam/userui/ -run 'TestAPIAnswer|TestAnswerOutcome'`
Expected: FAIL to compile (`undefined: answerOutcome`).

- [ ] **Step 3: Implement.**

In `asks.go`, replace `answerAsk`'s error switch with one that uses a shared mapping:
```go
// answerOutcome maps an AnswerAsk result to the HTTP status, API code and
// message both answer routes give (the HTML route writes msg as text).
func answerOutcome(d Deps, id string, err error) (int, string, string) {
	switch {
	case err == nil:
		return http.StatusNoContent, "", ""
	case errors.Is(err, intercom.ErrAskChanged):
		return http.StatusConflict, "ask_changed", "this ask changed — review the new version"
	case errors.Is(err, intercom.ErrAskClosed):
		msg := "this ask is closed"
		if a, ok := d.Intercom.GetAsk(id); ok && a.Answer != nil {
			msg = "already answered by " + d.Intercom.PartyOf(a.Answer.By).Label + ": " + a.ChoiceLabel(a.Answer.Choice)
		}
		return http.StatusConflict, "ask_closed", msg
	case errors.Is(err, intercom.ErrInvalid):
		return http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), "intercom: invalid squawk: ")
	case errors.Is(err, intercom.ErrAskNotFound):
		return http.StatusNotFound, "not_found", "not found"
	case errors.Is(err, jam.ErrSendDenied), errors.Is(err, intercom.ErrAskForbidden):
		return http.StatusForbidden, "forbidden", "not allowed"
	default:
		return http.StatusInternalServerError, "internal", "failed"
	}
}
```
In `answerAsk`, after the `AnswerAsk` call:
```go
	status, _, msg := answerOutcome(h.d, id, err)
	switch {
	case status == http.StatusNoContent:
		w.Header().Set("HX-Trigger", "meChanged")
		w.WriteHeader(http.StatusNoContent)
	case status == http.StatusInternalServerError:
		h.lg.Error("userui answer failed", "ask", id, "error", err.Error())
		http.Error(w, msg, status)
	default:
		http.Error(w, msg, status)
	}
```
`internal/jam/userui/api_asks.go`:
```go
package userui

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

func (h *handler) askRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /app/user/api/asks/{id}/answer", h.apiAnswer)
}

// apiAnswer records the viewer's answer to an ask (see answerOutcome).
func (h *handler) apiAnswer(w http.ResponseWriter, r *http.Request) {
	var req AnswerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	if h.d.Intercom == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "messaging not configured")
		return
	}
	id := r.PathValue("id")
	_, err := h.d.Intercom.AnswerAsk(p.UserID, id, req.Version, req.Choice, req.Comment)
	status, code, msg := answerOutcome(h.d, id, err)
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	if status == http.StatusInternalServerError {
		h.lg.Error("userui api answer failed", "ask", id, "error", err.Error())
	}
	writeAPIError(w, status, code, msg)
}
```
Register `h.askRoutes(mux)` in `apiRoutes`, before the catch-all.

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/jam/userui/`
Expected: PASS, with the old `asks_test.go` unchanged.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/userui
git commit -m "userui: POST api/asks/{id}/answer (one error mapping with the HTML route)"
```

---

### Task 3: The ask card and the open-asks panel

**Files:**
- Create: `web/app/src/asks/AskCard.tsx`, `web/app/src/asks/OpenAsks.tsx`, `web/app/src/asks/AskCard.test.tsx`
- Modify: `web/app/src/conversation/Message.tsx`, `web/app/src/conversation/ConversationView.tsx`

**Interfaces:**
- Consumes: `AskCard`, `AnswerRequest` (types); `apiPost`, `ApiError`; query key `["conversation", channel]`.
- Produces:
  - `<AskCardView card channel onDiscuss? />`, rendered with `id="ask-<id>"`. Its `onDiscuss(card)` is called by Discuss; the Discuss button shows only when the card is open and `onDiscuss` is given.
  - `<OpenAsks asks onJump />`: a collapsible "Open asks N" panel. Each entry is a link that scrolls to its card. Nothing renders when the list is empty.
  - `Message` gains an `onDiscuss` prop. When a row's `card === "ask"` and `ask` is set, it renders `AskCardView`; a board row keeps slice 2's placeholder.

- [ ] **Step 1: Write the failing tests.**

`web/app/src/asks/AskCard.test.tsx`:
```tsx
import { describe, expect, it, vi } from "vitest";
import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { focusManager } from "@tanstack/react-query";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { AskCard, Conversation, MessageRow } from "../api/types";

const card = (over: Partial<AskCard> = {}): AskCard => ({
  id: "ask_1", question: "Merge PR #12?", options: [{ id: "yes", label: "Ship it" }, { id: "no", label: "Hold", detail: "wait for CI" }],
  version: 1, state: "open", answeredBy: "", choice: "", comment: "", reason: "", edited: false, canAnswer: true, for: "", ...over,
});
const row = (c: AskCard, seq = 1): MessageRow => ({
  from: "spider", body: "<p>asked</p>", raw: "asked", plain: false, at: "", time: new Date().toISOString(), mine: false, seq,
  system: false, sysText: "", card: "ask", ask: c,
});
const conv = (cards: AskCard[]): Conversation => ({
  channelId: "chn_a", label: "eng", kind: "room", project: "test", phase: "", needsYou: true, sendTo: "chn_a",
  messages: cards.map((c, i) => row(c, i + 1)), lastSeq: cards.length, earlier: false, sessionIds: [], sessions: [],
  canJoin: false, canLeave: true, callIn: [], openAsks: cards.filter((c) => c.state === "open"),
});

function serve(get: () => Conversation, answer?: (body: unknown) => Response) {
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })),
    http.get("/app/user/api/conversations/:c", () => HttpResponse.json(get())),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: [] })),
    http.post("/app/user/api/conversations/:c/read", () => new HttpResponse(null, { status: 204 })),
    http.post("/app/user/api/asks/:id/answer", async ({ request }) => (answer ? answer(await request.json()) : new HttpResponse(null, { status: 204 }))),
  );
}

describe("ask cards", () => {
  it("answers with an option, then shows the answered state", async () => {
    let c = card();
    let got: unknown;
    serve(() => conv([c]), (body) => {
      got = body;
      c = card({ state: "answered", answeredBy: "alice", choice: "Hold", comment: "after lunch", canAnswer: false });
      return new HttpResponse(null, { status: 204 });
    });
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("wait for CI")).toBeInTheDocument();
    await userEvent.type(screen.getByRole("textbox", { name: "Comment on Merge PR #12?" }), "after lunch");
    await userEvent.click(screen.getByRole("button", { name: /Hold/ }));
    expect(got).toEqual({ version: 1, choice: "no", comment: "after lunch" });
    expect(await screen.findByText(/alice chose/)).toBeInTheDocument();
    expect(screen.getByText("Hold")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Ship it/ })).toBeNull();
  });

  it("refuses Other… without a comment", async () => {
    let posts = 0;
    serve(() => conv([card()]), () => { posts++; return new HttpResponse(null, { status: 204 }); });
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Other…" }));
    expect(await screen.findByText("Write a comment for Other…")).toBeInTheDocument();
    expect(posts).toBe(0);
  });

  it("says why when the ask changed, and shows the new version", async () => {
    let c = card();
    serve(() => conv([c]), () => {
      c = card({ version: 2, question: "Merge PR #12 now?", edited: true });
      return HttpResponse.json({ error: "this ask changed — review the new version", code: "ask_changed" }, { status: 409 });
    });
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: /Ship it/ }));
    expect(await screen.findByText("this ask changed — review the new version")).toBeInTheDocument();
    expect(await screen.findByText("Merge PR #12 now?")).toBeInTheDocument();
    expect(screen.getByText("edited")).toBeInTheDocument();
  });

  it("is read-only, and says for whom, when the viewer may not answer", async () => {
    serve(() => conv([card({ canAnswer: false, for: "carol" })]));
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("for carol")).toBeInTheDocument();
    expect(screen.getAllByText("Ship it").every((el) => el.closest("button") === null)).toBe(true);
    expect(screen.queryByRole("textbox", { name: /Comment on/ })).toBeNull();
  });

  it("shows a withdrawn ask with its reason", async () => {
    serve(() => conv([card({ state: "withdrawn", reason: "superseded", canAnswer: false })]));
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("withdrawn: superseded")).toBeInTheDocument();
  });

  it("keeps a half-typed comment across a live refresh", async () => {
    serve(() => conv([card()]));
    renderApp(boot, "/app/next/c/chn_a");
    const box = await screen.findByRole("textbox", { name: "Comment on Merge PR #12?" });
    await userEvent.type(box, "half");
    act(() => { focusManager.setFocused(false); focusManager.setFocused(true); });
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.getByRole("textbox", { name: "Comment on Merge PR #12?" })).toHaveValue("half");
  });

  it("lists open asks in a panel that jumps to the card", async () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    serve(() => conv([card(), card({ id: "ask_2", question: "Deploy?", for: "carol", canAnswer: false })]));
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("Open asks")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Deploy?" }));
    await waitFor(() => expect(scroll).toHaveBeenCalled());
  });
});
```
Every slice 2/3 test fixture that builds a `Conversation` gets `openAsks: []`. tsc reports each one, so add it wherever it's missing.

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/asks`
Expected: FAIL. The ask rows still render slice 2's placeholder, so "wait for CI" is never found.

- [ ] **Step 3: Implement.**

`web/app/src/asks/AskCard.tsx`:
```tsx
import { Badge, Button, Group, Paper, Stack, Text, Textarea } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiPost, ApiError } from "../api/client";
import type { AnswerRequest, AskCard } from "../api/types";

// AskCardView is an ask where it was asked, in its current state: open
// (options to answer, or read-only "for …"), answered, or withdrawn. Its
// comment box is local state, so a live refresh never wipes it.
export function AskCardView({ card, channel, onDiscuss }: { card: AskCard; channel: string; onDiscuss?: (c: AskCard) => void }) {
  const client = useQueryClient();
  const [comment, setComment] = useState("");
  const answer = useMutation<void, ApiError, AnswerRequest>({
    mutationFn: (req) => apiPost(`asks/${encodeURIComponent(card.id)}/answer`, req),
    onSuccess: () => setComment(""),
    onError: (e) => notifications.show({ color: e.code === "ask_changed" || e.code === "ask_closed" ? "yellow" : "red", message: e.message }),
    // Either way the card's state moved on (answered, changed, closed): refetch.
    onSettled: () => void client.invalidateQueries({ queryKey: ["conversation", channel] }),
  });
  const choose = (choice: string) => {
    if (choice === "other" && !comment.trim()) {
      notifications.show({ color: "yellow", message: "Write a comment for Other…" });
      return;
    }
    answer.mutate({ version: card.version, choice, ...(comment.trim() ? { comment: comment.trim() } : {}) });
  };
  return (
    <Paper id={`ask-${card.id}`} withBorder p="sm" my={6} maw="85%" style={{ borderLeft: `3px solid var(--mantine-color-${card.state === "open" ? "yellow" : "gray"}-6)` }}>
      <Group gap={6} mb={6}>
        <Text fw={600}>{card.question}</Text>
        {card.edited && <Badge size="xs" variant="light" color="gray">edited</Badge>}
        {card.for && <Text size="xs" c="dimmed">for {card.for}</Text>}
      </Group>
      {card.state === "answered" && (
        <Text size="sm">✓ {card.answeredBy} chose <b>{card.choice}</b>{card.comment && <> — {card.comment}</>}</Text>
      )}
      {card.state === "withdrawn" && <Text size="sm" c="dimmed">withdrawn{card.reason && `: ${card.reason}`}</Text>}
      {card.state === "open" && card.canAnswer && (
        <Stack gap={6}>
          <Group gap={6}>
            {card.options.map((o) => (
              <Button key={o.id} variant="default" size="xs" loading={answer.isPending && answer.variables?.choice === o.id} onClick={() => choose(o.id)}
                styles={{ label: { flexDirection: "column", alignItems: "flex-start" } }} h="auto" py={4}>
                <span>{o.label}</span>
                {o.detail && <Text span size="xs" c="dimmed">{o.detail}</Text>}
              </Button>
            ))}
            <Button variant="subtle" size="xs" onClick={() => choose("other")}>Other…</Button>
          </Group>
          <Textarea size="xs" autosize minRows={1} maxRows={4} aria-label={`Comment on ${card.question}`}
            placeholder="Comment (needed for Other…)" value={comment} onChange={(e) => setComment(e.currentTarget.value)} />
        </Stack>
      )}
      {card.state === "open" && !card.canAnswer && (
        <Group gap={6}>{card.options.map((o) => <Badge key={o.id} variant="outline" color="gray">{o.label}</Badge>)}</Group>
      )}
      {card.state === "open" && onDiscuss && (
        <Button variant="subtle" size="compact-xs" mt={6} onClick={() => onDiscuss(card)}>Discuss</Button>
      )}
    </Paper>
  );
}
```
The answered state renders `<b>{card.choice}</b>`, so "Hold" is its own text node, which is what the first test asserts.

`web/app/src/asks/OpenAsks.tsx`:
```tsx
import { Badge, Group, Paper, Stack, Text, UnstyledButton } from "@mantine/core";
import type { AskCard } from "../api/types";

// OpenAsks lists the conversation's open asks above the stream, so piled-up
// asks can be answered in any order; each jumps to its card.
export function OpenAsks({ asks, onJump }: { asks: AskCard[]; onJump: (id: string) => void }) {
  if (asks.length === 0) return null;
  return (
    <Paper withBorder radius={0} px="sm" py={6} style={{ borderLeft: 0, borderRight: 0, borderTop: 0 }}>
      <Group gap={6} mb={4}><Text size="xs" fw={700}>Open asks</Text><Badge size="xs" circle>{asks.length}</Badge></Group>
      <Stack gap={2}>
        {asks.map((a) => (
          <Group key={a.id} gap={6} wrap="nowrap">
            <UnstyledButton fz="sm" c="var(--mantine-color-anchor)" onClick={() => onJump(a.id)}>{a.question}</UnstyledButton>
            {a.for && <Text size="xs" c="dimmed">for {a.for}</Text>}
          </Group>
        ))}
      </Stack>
    </Paper>
  );
}
```
In `Message.tsx`:
- add the prop `onDiscuss?: (c: AskCard) => void`;
- before the `m.card` placeholder, add `if (m.card === "ask" && m.ask) return <div data-seq={m.seq}><AskCardView card={m.ask} channel={channel} onDiscuss={onDiscuss} /></div>;`;
- keep the placeholder for `board`.

In `MessageList.tsx`, thread `onDiscuss` through to `Message`.

In `ConversationView.tsx`, render `<OpenAsks asks={head.openAsks} onJump={(id) => document.getElementById(`ask-${id}`)?.scrollIntoView({ behavior: "smooth", block: "center" })} />` between the header and the list. Discuss wiring comes in Task 4; pass `onDiscuss={undefined}` for now.

The read-only test asserts every "Ship it" text is outside a button, so read-only options must render as `Badge`s, not buttons.

- [ ] **Step 4: Run the checks.**

Run: `cd web/app && npm run check`
Expected: all pass, including the slice 2 card-placeholder test. That test's fixture row has `card: "ask"` but no `ask`, so it still shows the placeholder. If you'd rather it test the real card, update it and ledger the change.

- [ ] **Step 5: Commit.**
```bash
git add web/app
git commit -m "web/app: ask cards (answer, Other…, states) and the open-asks panel"
```

---

### Task 4: Discuss

**Files:**
- Modify: `web/app/src/composer/Composer.tsx`, `web/app/src/composer/Composer.test.tsx`, `web/app/src/conversation/ConversationView.tsx`

**Interfaces:**
- Consumes: `AskCardView`'s `onDiscuss` (Task 3); `SendMessageRequest.replyTo`.
- Produces:
  - `type ComposerHandle = { discuss(id: string, question: string): void }`.
  - `Composer` accepts `ref?: React.Ref<ComposerHandle>` (React 19 passes refs as props).
  - `discuss` sets `replyTo`. If the box is empty, it fills it with `re: <question>\n` (as in the old `meDiscuss`). It then focuses the box and shows a chip "Replying about: <question> ✕"; ✕ clears `replyTo`.
  - The next successful send carries `replyTo` and then clears it. A failed send keeps it.

- [ ] **Step 1: Write the failing tests.**

Append to `Composer.test.tsx`:
```tsx
describe("Composer discuss", () => {
  function mountWithRef() {
    const client = new QueryClient();
    const ref: { current: import("./Composer").ComposerHandle | null } = { current: null };
    render(
      <MantineProvider theme={theme}><Notifications />
        <QueryClientProvider client={client}><Composer ref={ref} to="chn_a" label="eng" attachMax={0} /></QueryClientProvider>
      </MantineProvider>,
    );
    return { ref, box: screen.getByRole("textbox", { name: "Message eng" }) as HTMLTextAreaElement };
  }

  it("prefills an empty box, carries replyTo once, then clears it", async () => {
    const bodies: unknown[] = [];
    server.use(http.post("/app/user/api/messages", async ({ request }) => { bodies.push(await request.json()); return HttpResponse.json({ channel: "chn_a", seq: 1 }, { status: 201 }); }));
    const { ref, box } = mountWithRef();
    act(() => ref.current!.discuss("ask_1", "Merge PR #12?"));
    expect(box.value).toBe("re: Merge PR #12?\n");
    expect(screen.getByText(/Replying about: Merge PR #12\?/)).toBeInTheDocument();
    await userEvent.type(box, "not yet");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toEqual({ to: "chn_a", body: "re: Merge PR #12?\nnot yet", replyTo: "ask_1" });
    await waitFor(() => expect(screen.queryByText(/Replying about/)).toBeNull());
    await userEvent.type(box, "next");
    fireEvent.keyDown(box, { key: "Enter", ctrlKey: true });
    await waitFor(() => expect(bodies).toHaveLength(2));
    expect(bodies[1]).toEqual({ to: "chn_a", body: "next" });
  });

  it("never overwrites text already in the box, and ✕ drops the reply", async () => {
    const { ref, box } = mountWithRef();
    await userEvent.type(box, "my own words");
    act(() => ref.current!.discuss("ask_1", "Merge?"));
    expect(box.value).toBe("my own words");
    await userEvent.click(screen.getByRole("button", { name: "Stop replying about Merge?" }));
    expect(screen.queryByText(/Replying about/)).toBeNull();
  });
});
```
Add `act` to the file's `@testing-library/react` import.

Append to `web/app/src/asks/AskCard.test.tsx`:
```tsx
  it("Discuss starts a reply about the ask in the composer", async () => {
    serve(() => conv([card()]));
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Discuss" }));
    expect(screen.getByRole("textbox", { name: "Message eng" })).toHaveValue("re: Merge PR #12?\n");
  });
```
Put it inside the `describe("ask cards")` block.

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/composer src/asks`
Expected: FAIL (`ref.current` is null, since `Composer` exposes no handle, and there's no Discuss button).

- [ ] **Step 3: Implement.**

In `Composer.tsx`:
```tsx
export type ComposerHandle = { discuss(id: string, question: string): void };
```
- Add `ref?: React.Ref<ComposerHandle>` to the props.
- Add `const [replyTo, setReplyTo] = useState<{ id: string; question: string } | null>(null);`.
- Add `useImperativeHandle`:
```tsx
  useImperativeHandle(ref, () => ({
    discuss(id, question) {
      setReplyTo({ id, question });
      if (!box.current?.value.trim()) setText(`re: ${question}\n`);
      requestAnimationFrame(() => { const b = box.current; if (b) { b.focus(); b.setSelectionRange(b.value.length, b.value.length); } });
    },
  }));
```
- In `send()`, add `...(replyTo ? { replyTo: replyTo.id } : {})` to the request, and call `setReplyTo(null)` on success only.
- Above the textarea, render:
```tsx
      {replyTo && (
        <Group gap={4} mb={4}>
          <Text size="xs" c="dimmed">Replying about: {replyTo.question}</Text>
          <CloseButton size="xs" aria-label={`Stop replying about ${replyTo.question}`} onClick={() => setReplyTo(null)} />
        </Group>
      )}
```
In `ConversationView.tsx`:
- `const composer = useRef<ComposerHandle>(null);`
- pass `ref={composer}` to `Composer`;
- pass `onDiscuss={(c) => composer.current?.discuss(c.id, c.question)}` down through `MessageList` (replacing `undefined`).

History conversations have no composer, so Discuss doesn't show there: pass `onDiscuss` only when `head.sendTo` is set.

- [ ] **Step 4: Run the checks.**

Run: `cd web/app && npm run check`
Expected: all pass.

- [ ] **Step 5: Commit.**
```bash
git add web/app
git commit -m "web/app: Discuss (reply about an ask: replyTo, re: line, chip)"
```

---

### Task 5: End-to-end, docs

**Files:**
- Modify: `internal/jam/userui/next_browser_test.go`, `docs/web-app.md`, `docs/usage/jam/intercom-ui.md`

- [ ] **Step 1: Write the browser test.**

Append:
```go
// An ask renders as a card; choosing an option records the answer (the
// log's ask state) and the card shows who chose what.
func TestBrowserNextAnswersAnAsk(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge PR #12?", Options: yesNoOpts})
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

	var problems pageProblems
	var text string
	hold := chromedp.CSS(`#ask-` + m.ID + ` button:nth-of-type(2)`) // Ship it, Hold, Other…
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(hold),
		chromedp.Click(hold),
		chromedp.WaitVisible(chromedp.CSS(`#ask-`+m.ID+` b`)),
		into(&text, chromedp.Text(chromedp.CSS(`#ask-`+m.ID))),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "alice chose") || !strings.Contains(text, "Hold") {
		t.Errorf("card after answering = %q", text)
	}
	if a, ok := e.Intercom.GetAsk(m.ID); !ok || a.Answer == nil || a.Answer.Choice != "no" {
		t.Errorf("ask state = %+v", a)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}
```
Run: `./scripts/app.sh && go test -tags browser ./internal/jam/userui/ -run TestBrowserNext -v`
Expected: PASS for all four. If `chromedp.Text(chromedp.CSS(...))` reports the selector's text differently (for example, because of whitespace), compare with `strings.Fields` joined by spaces.

- [ ] **Step 2: Docs.**

`docs/web-app.md`, under Layout: add `src/asks/`: "ask cards (`AskCardView`: answer with an option or Other… + comment; Discuss) and the open-asks panel. `POST /app/user/api/asks/{id}/answer` shares `answerOutcome` with the HTML route."

`docs/usage/jam/intercom-ui.md`, in the preview section: replace "asks and whiteboards still happen here." with "and answer asks there; whiteboards still happen here."

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: no new errors beyond this plan file's own frontmatter and orphan lines.

- [ ] **Step 3: The whole suite, then commit.**

Run: `go test ./... && just app-check && ./scripts/app.sh && just test-browser && just lint`
Expected: all green. Go table tests must use positional struct cases, not map literals whose keys are expressions; CI's gofmt (Go 1.26) aligns those differently.
```bash
git add internal/jam/userui/next_browser_test.go docs
git commit -m "Jam app: ask end-to-end test and docs"
```
Open one PR, "Jam app slice 4: asks".

---

## Self-review notes

- **Spec coverage (§3 slice 4):**
  - ask cards inline: Tasks 1 and 3;
  - the open-asks panel: Task 3. The "Needs you" rail bucket itself already ships with the rail (slice 1).
  - answer with comment: Tasks 2 and 3;
  - Discuss replies: Task 4.
- **Parity:** the old route's texts come from the shared `answerOutcome`, and the old tests are unchanged.
- **Deferred:** none in scope. Asking and amending stay agent-side (MCP); people only answer and discuss, as in the old Inbox.
