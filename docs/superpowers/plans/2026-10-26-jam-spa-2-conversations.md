# Jam SPA, slice 2: conversations — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** opening a conversation in the Jam app at `/app/next/c/:channel` shows it live:
- the header, with Join, Leave and Call in;
- the message list: server-rendered markdown, Text/Markdown copy, code-block copy buttons, attachments, system lines, "re:" links and day dividers;
- "Load earlier" paging and mark-read;
- the session presence strip;
- live refresh from the existing signal-only event stream, with a 30-second poll as fallback.

**Architecture:**
- `userui` gains JSON siblings of the conversation routes under `/app/user/api/conversations/{c}`. They serialize the existing `Conversation`/`MessageRow`/`SessionRow` view models, which move into `apitypes.go` with JSON tags, behind the same checks.
- The app reads them with TanStack Query: `useInfiniteQuery` for the message pages, and one `EventSource` that invalidates query keys.
- Message HTML still comes only from `squawkrender`. It is inserted at a single, lint-exempted site.

**Tech Stack:**
- The slice-1 stack (React 19.3, TS 6.0, Mantine 9.7, React Router 8, TanStack Query 5, Vitest 5, MSW 3, chromedp v0.21), plus `@mantine/notifications` 9.7.4.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md): §2 (API, live updates, errors), §3 slice 2, §4, §5. Slice 1 plan: [`2026-10-26-jam-spa-1-foundation.md`](2026-10-26-jam-spa-1-foundation.md). Its helpers (`writeJSON`, `writeAPIError`, `apiGet`, `ApiError`, `shouldRetry`, `Problem`, `renderApp`, `pageProblems`, `step`/`into`/`call`) are on main.

## Global Constraints

- **Message HTML:** it comes only from `squawkrender` (spec §4). Exactly one component, `MessageBody`, uses `dangerouslySetInnerHTML`, behind an `eslint-disable-next-line no-restricted-syntax` comment that says why.
- **Live stream:** `GET /app/user/events` stays signal-only (`changed`, `presence`, `board`). Every refetch goes through the normal API checks.
- **Errors and headers:** API errors are `{"error","code"}` with the real status. JSON responses use `writeJSON`, so they get `nosniff` and `no-store`. Lists are `[]`, never `null`; optional row fields use `omitempty`, so they're optional in TS.
- **Writes:** API writes are `POST` with a JSON body. `decodeJSON` refuses any other `Content-Type` with 415 (defense in depth on top of the gate's `RequireSameOrigin`).
- **No existence leak:** a conversation the viewer may not see answers exactly like one that doesn't exist: `404 {"code":"not_found"}`.
- **Parity rule:** everything the old conversation pane shows for slice 2's scope works. Ask cards and board cards (slices 4 and 7) and the composer (slice 3) are out of scope. Ask and board rows show a placeholder linking to `/app/user/?c=<id>`.
- **Untouched:** the old HTML routes and templates.
- **Types:** `types.ts` is regenerated (`go generate ./internal/jam/userui/`), and `scripts/app-types-check.sh` passes.
- **Size budget:** the initial JS stays ≤ 350 KB gzipped (it's about 168 KB now).
- **Docs:** they ship in the same change (AGENTS.md).

## Review Focus

1. **A conversation the viewer can't see, a missing one and a removed one** must get byte-identical `404 not_found` bodies, with no label. (Task 2 Go test.)
2. **A body containing raw HTML** (`<script>`, `<img onerror>`, a `javascript:` link) must render inert. The API's `body` never contains them, and in the browser nothing runs and no CSP violation fires. (Task 2 Go test; Task 6 browser test.)
3. **A History (`legacy:…`) conversation.** The id contains `:`, and the API must route it. The conversation is read-only: no Join/Leave/Call in, and no mark-read POST. (Task 2 Go test; Task 5 component test.)
4. **A conversation with more than 200 messages** opens on the newest page. "Load earlier" adds older ones without duplicates, and a live refresh keeps the loaded older pages. (Task 2 Go test; Task 5 component test.)
5. **Live push unavailable** (events answers 204, or the browser has no `EventSource`): the app doesn't crash, and the queries still poll every 30 s. (Task 4 test.)

---

## File structure

| File | Responsibility |
|---|---|
| `internal/jam/userui/apitypes.go` (modify) | Gains `MessageRow`, `FileChip`, `AskRef`, `Conversation`, `SessionRow`, `NewMessageOption` (moved from `conversation.go`/`presence.go`) with JSON tags, plus `PresenceResponse` and `CallInRequest`/`ReadRequest`. |
| `internal/jam/userui/conversation.go` (modify) | `messageRow` sets `Time`; `conversation()` sets `Card`. Types move out. |
| `internal/jam/userui/presence.go` (modify) | `SessionRow` moves out. |
| `internal/jam/userui/api_conversation.go` (new) | `GET api/conversations/{c}`, `…/presence`, `POST …/read|join|leave|call-in`, plus `decodeJSON` and `pageOf`. |
| `internal/jam/userui/api_conversation_test.go` (new) | Shape, paging, authorization and write tests. |
| `internal/jam/userui/api.go` (modify) | Registers the routes; `apiRail` honours `?c=`. |
| `tygo.yaml` (modify) | Mappings for `template.HTML` and `time.Time`. |
| `web/app/src/api/client.ts` (modify) | `apiPost`, built on a shared `request`. |
| `web/app/src/live/useLiveEvents.ts` (new) | The one `EventSource`, which invalidates query keys. |
| `web/app/src/conversation/*` (new) | `useConversation`, `ConversationView`, `ConversationHeader`, `MessageList`, `Message`, `MessageBody`, `PresenceStrip`, `copy.ts`, `days.ts`, `body.css`. |
| `web/app/src/routes/Conversation.tsx` (modify) | Renders `ConversationView`. |
| `web/app/src/rail/useRail.ts`, `Rail.tsx` (modify) | Pass the selected channel (`rail?c=`). |
| `web/app/src/shell/Shell.tsx`, `start.tsx` (modify) | `useLiveEvents`, `Notifications`. |
| `internal/jam/userui/next_browser_test.go` (modify) | End-to-end: markdown, inert HTML, code copy, live update, scroll position. |
| `docs/web-app.md`, `docs/usage/jam/intercom-ui.md` (modify) | Docs. |

---

### Task 1: The conversation view models as API types

**Files:**
- Modify: `internal/jam/userui/apitypes.go`, `internal/jam/userui/conversation.go`, `internal/jam/userui/presence.go`, `tygo.yaml`, `web/app/src/api/types.ts` (generated)
- Test: `internal/jam/userui/api_conversation_test.go` (new; this task adds the first test)

**Interfaces:**
- Produces (JSON names are the TS contract):
  - `MessageRow`:
    - `from`, `body` (HTML string), `raw`, `plain`, `at` (server "15:04", kept for the templates), `time` (RFC 3339), `mine`, `seq`;
    - `system`, `sysText`;
    - `reAsk?` (`AskRef{id, question}`) and `files?` (`FileChip[]`);
    - `card` (`"" | "ask" | "board"`).
    - `Ask`, `Board` and `Channel` are `json:"-"` until slices 4 and 7.
  - `FileChip{id, name, size, image}`.
  - `Conversation`:
    - `channelId`, `label`, `kind`, `project`, `phase`, `needsYou`, `sendTo`, `messages`, `lastSeq`, `earlier`, `sessionIds`, `sessions`;
    - `canJoin`, `canLeave`, `callIn` (`NewMessageOption[]`).
    - `HasMessages`, `OpenAsks`, `NewBoard`, `Board` and `Head` are `json:"-"`.
  - `SessionRow{label, text, tool, class}`.
  - `NewMessageOption{to, label, kind, project}`.
  - `PresenceResponse{sessions: SessionRow[]}`, `ReadRequest{seq}`, `CallInRequest{who}`.
  - New Go fields: `MessageRow.Time time.Time`, `MessageRow.Card string`, `Conversation.Earlier bool`.

- [ ] **Step 1: Write the failing test.**

`internal/jam/userui/api_conversation_test.go`:
```go
package userui

import (
	"encoding/json"
	"testing"
	"time"
)

// The view models' JSON names are the app's contract (types.ts): pin them,
// and that template-only fields stay out.
func TestConversationJSONShape(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 5, 0, 0, time.UTC)
	c := Conversation{ChannelID: "chn_x", Label: "eng", SendTo: "chn_x", LastSeq: 3, Earlier: true,
		Messages:   []MessageRow{{From: "bob", Body: "<p>hi</p>", Raw: "hi", At: "09:05", Time: at, Seq: 3, Card: "ask", Ask: &AskCard{ID: "a1"}}},
		Sessions:   []SessionRow{{Label: "spider", Text: "is running", Tool: "Bash", Class: "busy"}},
		CallIn:     []NewMessageOption{{To: "user:usr_1", Label: "bob", Kind: "person"}},
		SessionIDs: []string{"ses_1"}, HasMessages: true,
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"channelId", "label", "kind", "project", "phase", "needsYou", "sendTo", "messages", "lastSeq", "earlier", "sessionIds", "sessions", "canJoin", "canLeave", "callIn"} {
		if _, ok := got[k]; !ok {
			t.Errorf("conversation JSON lacks %q: %s", k, b)
		}
	}
	for _, k := range []string{"HasMessages", "hasMessages", "OpenAsks", "openAsks", "Board", "board", "Head", "head", "NewBoard", "newBoard"} {
		if _, ok := got[k]; ok {
			t.Errorf("conversation JSON carries template-only %q", k)
		}
	}
	m := got["messages"].([]any)[0].(map[string]any)
	for k, want := range map[string]any{"from": "bob", "body": "<p>hi</p>", "raw": "hi", "at": "09:05", "time": "2026-10-26T09:05:00Z", "seq": float64(3), "card": "ask", "mine": false, "plain": false, "system": false} {
		if m[k] != want {
			t.Errorf("message[%q] = %v, want %v", k, m[k], want)
		}
	}
	for _, k := range []string{"Ask", "ask", "files", "reAsk", "Board", "board", "Channel", "channel"} {
		if _, ok := m[k]; ok {
			t.Errorf("message JSON carries %q (template-only, or omitempty when empty): %s", k, b)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails.**

Run: `go test ./internal/jam/userui/ -run TestConversationJSONShape`
Expected: FAIL to compile (`unknown field Earlier`, `Time`, `Card`).

- [ ] **Step 3: Move the types and tag them.**

Cut `MessageRow`, `FileChip`, `Conversation` and `NewMessageOption` (with their doc comments) from `conversation.go`, `AskRef` from `asks.go`, and `SessionRow` from `presence.go`. Append them to `apitypes.go`, keeping each original doc comment above its type:
```go
// MessageRow is one squawk in the conversation pane, from the viewer's side.
type MessageRow struct {
	From string        `json:"from"`
	Body template.HTML `json:"body"` // rendered per the squawk's content type (squawkrender): the app inserts it as HTML
	Raw  string        `json:"raw"`  // the body as sent, for the Markdown copy (escaped by the template)
	// Plain marks a text/plain squawk, shown literally with its whitespace.
	Plain bool      `json:"plain"`
	At    string    `json:"at"`   // server-local "15:04" (the templates)
	Time  time.Time `json:"time"` // when it was sent (the app's day dividers and local times)
	Mine  bool      `json:"mine"`
	Seq   int64     `json:"seq"`
	// Ask is the card of an ask's own squawk; System marks an ask's later
	// event (a one-line note, not a bubble); ReAsk a reply about an ask.
	Ask     *AskCard `json:"-"` // the app shows asks from slice 4 (Card says which rows)
	System  bool     `json:"system"`
	SysText string   `json:"sysText"` // a System row's line
	ReAsk   *AskRef  `json:"reAsk,omitempty"`
	// Files are the message's attachments.
	Files []FileChip `json:"files,omitempty"`
	// Board is a board's card (the squawk that started it).
	Board *BoardCard `json:"-"` // the app shows boards from slice 7 (Card says which rows)
	// Channel is the conversation's id (a board card links into it).
	Channel string `json:"-"`
	// Card names the card this row is ("ask" or "board"; "" = none), so the
	// app can place what it doesn't render yet.
	Card string `json:"card"`
}

// FileChip is one attachment of a message: an inline image or a download.
type FileChip struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Size  string `json:"size"`
	Image bool   `json:"image"`
}

// AskRef names the ask a reply is about.
type AskRef struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

// Conversation is the right-pane view of one open channel.
type Conversation struct {
	ChannelID   string          `json:"channelId"`
	Label       string          `json:"label"`
	Kind        jam.ChannelKind `json:"kind"`
	Project     string          `json:"project"`
	Phase       string          `json:"phase"`
	NeedsYou    bool            `json:"needsYou"`
	SendTo      string          `json:"sendTo"` // what the composer POSTs to /app/user/send ("" = read-only History)
	Messages    []MessageRow    `json:"messages"`
	HasMessages bool            `json:"-"`
	LastSeq     int64           `json:"lastSeq"`
	// Earlier: the API page has older messages before its first (GET
	// api/conversations/{c}?before=<first seq>).
	Earlier bool `json:"earlier"`
	// SessionIDs are the sessions taking part; Sessions is their presence
	// strip (filled by the handler, which holds the Presence source).
	SessionIDs []string     `json:"sessionIds"`
	Sessions   []SessionRow `json:"sessions"`
	OpenAsks   []AskCard    `json:"-"` // the channel's open asks (the panel above the stream)
	// Membership controls: Join (a channel they can see but aren't in),
	// Leave (one they're in; never a chat), and who they may call in.
	CanJoin  bool               `json:"canJoin"`
	CanLeave bool               `json:"canLeave"`
	CallIn   []NewMessageOption `json:"callIn"`
	// Whiteboards: NewBoard offers New whiteboard (the viewer may post here
	// and the editor is built); Board is the board ?board= opened.
	NewBoard bool         `json:"-"`
	Board    *BoardEditor `json:"-"`
	// Head is the session header when this is a session's home channel.
	Head *SessionHead `json:"-"`
}

// NewMessageOption is one recipient offered in the New Message picker.
type NewMessageOption struct {
	To      string `json:"to"` // what to POST to /app/user/send: user:<id>, session:<id>, or a channel id
	Label   string `json:"label"`
	Kind    string `json:"kind"`
	Project string `json:"project"`
}

// SessionRow is one session's line in the presence strip: "<Label> is <Text>
// <Tool>". Class is busy (animated dots), wait (needs a human), or dim.
type SessionRow struct {
	Label string `json:"label"`
	Text  string `json:"text"`
	Tool  string `json:"tool"`
	Class string `json:"class"`
}

// PresenceResponse is GET api/conversations/{c}/presence.
type PresenceResponse struct {
	Sessions []SessionRow `json:"sessions"`
}

// ReadRequest is POST api/conversations/{c}/read: the newest seq seen.
type ReadRequest struct {
	Seq int64 `json:"seq"`
}

// CallInRequest is POST api/conversations/{c}/call-in: whom (user:<id> or
// session:<actor>, one of Conversation.callIn's "to").
type CallInRequest struct {
	Who string `json:"who"`
}
```
Add `"html/template"` and `"time"` to `apitypes.go`'s imports.
- `AskRef` was `type AskRef struct{ ID, Question string }`. Its fields keep the same names, so every caller still compiles.
- `SessionRow`'s and `Conversation`'s fields keep their names, so the templates still work.
- `CanJoin, CanLeave bool` now has one field per line.

In `conversation.go`, `messageRow` sets the timestamp:
```go
		At:    m.At.Format("15:04"),
		Time:  m.At,
```
In `conversation()`'s message loop, set `Card` after the board check and before appending:
```go
		switch {
		case row.Board != nil:
			row.Card = "board"
		case row.Ask != nil:
			row.Card = "ask"
		}
```
Remove imports that `conversation.go`, `presence.go` and `asks.go` no longer use; the compiler will name them.

`tygo.yaml`: add these to `type_mappings`:
```yaml
      template.HTML: string
      time.Time: string
```

- [ ] **Step 4: Run the test and the package, then regenerate the types.**

Run: `go test ./internal/jam/userui/ && go generate ./internal/jam/userui/ && grep -n 'export interface \(MessageRow\|Conversation\|SessionRow\)' -A3 web/app/src/api/types.ts && cd web/app && npx tsc --noEmit`
Expected:
- PASS, including every existing template test.
- `types.ts` gains the interfaces, with `body: string`, `time: string`, `files?: FileChip[]` and `reAsk?: AskRef`, and no `ask`/`board`/`head` fields.
- tsc is clean.
- If tygo emits a type name it can't resolve (an import line or an unknown identifier), add that type to `type_mappings` and regenerate.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/userui tygo.yaml web/app/src/api/types.ts
git commit -m "userui: the conversation view models as API types (JSON tags, time, card)"
```

---

### Task 2: `GET` the conversation and its presence

**Files:**
- Create: `internal/jam/userui/api_conversation.go`
- Modify: `internal/jam/userui/api.go`, `internal/jam/userui/api_conversation_test.go`

**Interfaces:**
- Consumes: `conversation(p, d, id) (Conversation, bool)`, `sessionRows(ids, instances, presence) []SessionRow`, `writeJSON`, `writeAPIError`.
- Produces:
  - `GET /app/user/api/conversations/{c}?before=<seq>&limit=<n>` returns a `Conversation` whose `messages` are the newest `limit` (default 200, max 500) with `seq < before` (`before` = 0 means the newest), ascending, with `earlier` set.
  - `GET /app/user/api/conversations/{c}/presence` returns a `PresenceResponse`.
  - `GET /app/user/api/rail?c=<id>` counts `c` as read, as the old rail does.
  - `pageOf(ms []MessageRow, before int64, limit int) ([]MessageRow, bool)`.

- [ ] **Step 1: Write the failing tests.**

Append to `internal/jam/userui/api_conversation_test.go` (add imports `net/url`, `strings`, `fmt`, `ident`, `intercom`, `jam` as needed):
```go
func getConv(t *testing.T, e *env, p jam.Participant, id, query string) (int, Conversation, string) {
	t.Helper()
	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/conversations/"+url.PathEscape(id)+query)
	var c Conversation
	if rec.Code == 200 {
		checkJSONHeaders(t, rec)
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, c, rec.Body.String()
}

func TestAPIConversation(t *testing.T) {
	e, p := fixture()
	code, c, body := getConv(t, e, p, engID, "")
	if code != 200 {
		t.Fatalf("code = %d: %s", code, body)
	}
	if c.Label != "eng" || c.SendTo != engID || len(c.Messages) != 1 || c.Messages[0].Raw != "hi from alice" || !c.Messages[0].Mine || c.Earlier {
		t.Errorf("conversation = %+v", c)
	}
	if !strings.Contains(string(c.Messages[0].Body), "<p>hi from alice</p>") || c.Messages[0].Time.IsZero() {
		t.Errorf("message = %+v", c.Messages[0])
	}
	if !c.CanLeave || c.CanJoin {
		t.Errorf("member: canLeave=%v canJoin=%v", c.CanLeave, c.CanJoin)
	}
	for _, k := range []string{`"sessions":[]`, `"sessionIds":[]`} {
		if !strings.Contains(body, k) {
			t.Errorf("empty list not [] (%s): %s", k, body)
		}
	}
}

// Raw HTML in a body never reaches the app: squawkrender omits it.
func TestAPIConversationBodyIsSanitized(t *testing.T) {
	e, p := fixture()
	e.post(p.UserID, "<script>alert(1)</script> <img src=x onerror=alert(2)> [x](javascript:alert(3))", "")
	_, c, _ := getConv(t, e, p, engID, "")
	b := string(c.Messages[len(c.Messages)-1].Body)
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(b, bad) {
			t.Errorf("body carries %q: %s", bad, b)
		}
	}
}

// Not visible, missing and removed read the same: no label, no existence.
func TestAPIConversationNotFoundIsUniform(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	carol, _ := e.st.LookupName(ident.User, "carol")
	pl, err := e.Intercom.PlanPersonChat(bob, carol)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, id := range []string{string(m.Channel), "chn_01j9q3zzzzzzzzzzzzzzzzzz", "legacy:nope"} {
		code, _, body := getConv(t, e, p, id, "")
		if code != 404 {
			t.Errorf("%s: code = %d, want 404", id, code)
		}
		bodies = append(bodies, body)
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] || strings.Contains(bodies[0], "secret") || !strings.Contains(bodies[0], `"code":"not_found"`) {
		t.Errorf("404 bodies differ or leak: %q", bodies)
	}
}

func TestAPIConversationPages(t *testing.T) {
	e, p := fixture()
	for i := 1; i <= 249; i++ { // + the fixture's message = 250
		e.post(p.UserID, fmt.Sprintf("m%d", i), "")
	}
	_, first, _ := getConv(t, e, p, engID, "")
	if len(first.Messages) != 200 || !first.Earlier || first.Messages[199].Raw != "m249" {
		t.Fatalf("first page: %d messages, earlier=%v, last=%q", len(first.Messages), first.Earlier, first.Messages[len(first.Messages)-1].Raw)
	}
	before := first.Messages[0].Seq
	_, older, _ := getConv(t, e, p, engID, fmt.Sprintf("?before=%d", before))
	if len(older.Messages) != 50 || older.Earlier || older.Messages[49].Seq >= before || older.Messages[0].Raw != "hi from alice" {
		t.Errorf("older page: %d messages, earlier=%v", len(older.Messages), older.Earlier)
	}
	_, small, _ := getConv(t, e, p, engID, "?limit=10")
	if len(small.Messages) != 10 || !small.Earlier {
		t.Errorf("limit=10: %d, earlier=%v", len(small.Messages), small.Earlier)
	}
	_, capped, _ := getConv(t, e, p, engID, "?limit=100000")
	if len(capped.Messages) != 250 { // under the 500 cap: all of them
		t.Errorf("limit cap: %d", len(capped.Messages))
	}
	if code, _, _ := getConv(t, e, p, engID, "?before=abc"); code != 400 {
		t.Errorf("before=abc = %d, want 400", code)
	}
}

func TestPageOf(t *testing.T) {
	ms := []MessageRow{{Seq: 2}, {Seq: 5}, {Seq: 9}}
	for _, tc := range []struct {
		before    int64
		limit     int
		want      []int64
		wantEarly bool
	}{
		{0, 10, []int64{2, 5, 9}, false},
		{0, 2, []int64{5, 9}, true},
		{9, 10, []int64{2, 5}, false},
		{6, 1, []int64{5}, true},
		{2, 10, nil, false},
	} {
		got, early := pageOf(ms, tc.before, tc.limit)
		var seqs []int64
		for _, m := range got {
			seqs = append(seqs, m.Seq)
		}
		if fmt.Sprint(seqs) != fmt.Sprint(tc.want) || early != tc.wantEarly {
			t.Errorf("pageOf(before=%d, limit=%d) = %v %v, want %v %v", tc.before, tc.limit, seqs, early, tc.want, tc.wantEarly)
		}
	}
}

// History: routable by its legacy: id, read-only.
func TestAPIConversationHistory(t *testing.T) {
	e, p := fixture()
	legacy := intercom.NewLegacyMemLog()
	if _, err := legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "old-cove"}, To: []intercom.Target{{Kind: "human", Ref: "alice"}}, Body: "from the old log", Project: "proj"}); err != nil {
		t.Fatal(err)
	}
	e.Legacy = legacy
	var id string
	for _, g := range Rail(p, e.Deps, "") {
		for _, r := range g.Rows {
			if strings.HasPrefix(r.ID, legacyPrefix) {
				id = r.ID
			}
		}
	}
	if id == "" {
		t.Fatal("no History row")
	}
	code, c, body := getConv(t, e, p, id, "")
	if code != 200 || len(c.Messages) != 1 || c.Messages[0].Raw != "from the old log" {
		t.Fatalf("History = %d %s", code, body)
	}
	if c.SendTo != "" || c.CanJoin || c.CanLeave || len(c.CallIn) != 0 {
		t.Errorf("History is not read-only: %+v", c)
	}
}

func TestAPIPresenceAndRailSelection(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil)
	rec := apiGet(t, h, &p, "/app/user/api/conversations/"+engID+"/presence")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sessions":[]`) {
		t.Errorf("presence = %d %s", rec.Code, rec.Body)
	}
	if rec := apiGet(t, h, &p, "/app/user/api/conversations/chn_01j9q3zzzzzzzzzzzzzzzzzz/presence"); rec.Code != 404 {
		t.Errorf("presence of a missing channel = %d", rec.Code)
	}
	// bob's unread chat reads as read while it is the open conversation.
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	pl, _ := e.Intercom.PlanPersonChat(bob, p.UserID)
	m, _ := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "ping"})
	for q, want := range map[string]int{"": 1, "?c=" + string(m.Channel): 0} {
		var rr RailResponse
		_ = json.Unmarshal(apiGet(t, h, &p, "/app/user/api/rail"+q).Body.Bytes(), &rr)
		if rr.UnreadTotal != want {
			t.Errorf("rail%s unreadTotal = %d, want %d", q, rr.UnreadTotal, want)
		}
	}
}
```
These tests use the slice-1 helpers `apiGet` and `checkJSONHeaders`, which already set the participant and the principal.

- [ ] **Step 2: Run them to verify they fail.**

Run: `go test ./internal/jam/userui/ -run 'TestAPIConversation|TestPageOf|TestAPIPresence'`
Expected: FAIL (`undefined: pageOf`, then 404s from the catch-all once it compiles).

- [ ] **Step 3: Implement.**

`internal/jam/userui/api_conversation.go`:
```go
package userui

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// Page sizes for GET api/conversations/{c}.
const (
	pageDefault = 200
	pageMax     = 500
)

func (h *handler) conversationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/conversations/{c}", h.apiConversation)
	mux.HandleFunc("GET /app/user/api/conversations/{c}/presence", h.apiPresence)
	mux.HandleFunc("POST /app/user/api/conversations/{c}/read", h.apiRead)
	mux.HandleFunc("POST /app/user/api/conversations/{c}/join", h.apiJoin)
	mux.HandleFunc("POST /app/user/api/conversations/{c}/leave", h.apiLeave)
	mux.HandleFunc("POST /app/user/api/conversations/{c}/call-in", h.apiCallIn)
}

// notFound is every "no such conversation": the same for one the viewer may
// not see, so a refusal never says whether the channel exists.
func notFound(w http.ResponseWriter) {
	writeAPIError(w, http.StatusNotFound, "not_found", "no such conversation")
}

// openConversation is the request's participant and {c}'s conversation, or
// it has answered (403 / 404).
func (h *handler) openConversation(w http.ResponseWriter, r *http.Request) (jam.Participant, Conversation, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return p, Conversation{}, false
	}
	conv, ok := conversation(p, h.d, r.PathValue("c"))
	if !ok {
		notFound(w)
		return p, Conversation{}, false
	}
	return p, conv, true
}

func (h *handler) apiConversation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, err := intParam(q.Get("before"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "before: not a number")
		return
	}
	limit, err := intParam(q.Get("limit"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "limit: not a number")
		return
	}
	_, conv, ok := h.openConversation(w, r)
	if !ok {
		return
	}
	switch {
	case limit <= 0:
		limit = pageDefault
	case limit > pageMax:
		limit = pageMax
	}
	conv.Messages, conv.Earlier = pageOf(conv.Messages, before, int(limit))
	conv.Sessions = sessionRows(conv.SessionIDs, h.d.Store.ListInstances(), h.presence)
	writeJSON(w, http.StatusOK, lists(conv))
}

func (h *handler) apiPresence(w http.ResponseWriter, r *http.Request) {
	_, conv, ok := h.openConversation(w, r)
	if !ok {
		return
	}
	rows := sessionRows(conv.SessionIDs, h.d.Store.ListInstances(), h.presence)
	if rows == nil {
		rows = []SessionRow{}
	}
	writeJSON(w, http.StatusOK, PresenceResponse{Sessions: rows})
}

// pageOf is the newest limit messages with seq < before (0 = no bound), in
// order, and whether older ones exist. ms is in seq order.
func pageOf(ms []MessageRow, before int64, limit int) ([]MessageRow, bool) {
	end := len(ms)
	if before > 0 {
		end = sort.Search(len(ms), func(i int) bool { return ms[i].Seq >= before })
	}
	start := max(0, end-limit)
	if start == end {
		return nil, false
	}
	return ms[start:end], start > 0
}

func intParam(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// lists makes conv's lists [] where empty (types.ts says T[], never null).
func lists(conv Conversation) Conversation {
	if conv.Messages == nil {
		conv.Messages = []MessageRow{}
	}
	if conv.Sessions == nil {
		conv.Sessions = []SessionRow{}
	}
	if conv.SessionIDs == nil {
		conv.SessionIDs = []string{}
	}
	if conv.CallIn == nil {
		conv.CallIn = []NewMessageOption{}
	}
	return conv
}
```
In `api.go`'s `apiRoutes`, call `h.conversationRoutes(mux)` before the `/app/user/api/` catch-all. Also change `apiRail`'s `Rail(p, h.d, "")` to `Rail(p, h.d, r.URL.Query().Get("c"))`, and update its comment: "?c= is the open conversation, counted as read (it is being read)".

The POST handlers `apiRead`, `apiJoin`, `apiLeave` and `apiCallIn` belong to Task 3. To keep this task compiling, register only the two GET routes now and add the four POSTs in Task 3. Remove unused imports (`errors`, `mime`, `json`, `ident`, `strings`) until Task 3 needs them.

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/jam/userui/`
Expected: PASS. If `TestAPIConversationHistory` 404s, check that `conversation()`'s `legacy:` branch receives the id. `r.PathValue("c")` is already unescaped, so `legacy%3Ax` arrives as `legacy:x`.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/userui
git commit -m "userui: GET api/conversations/{c} (paged) and its presence; rail ?c= reads as read"
```

---

### Task 3: The conversation writes: read, join, leave, call in

**Files:**
- Modify: `internal/jam/userui/api_conversation.go`, `internal/jam/userui/api_conversation_test.go`

**Interfaces:**
- Consumes: `ReadRequest`, `CallInRequest` (Task 1); `notFound` and `openConversation` (Task 2); `isIn(st, ch, uid)` (existing test helper).
- Produces:
  - `POST /app/user/api/conversations/{c}/read {seq}` returns 204. History and a log-less Jam are a no-op 204; a conversation the viewer can't see is a 404.
  - `POST …/join`, `…/leave` and `…/call-in {who}` return 204 on success. Errors map as: `ErrSendDenied` → 403 `forbidden`; `ErrSendUnresolved`/`ErrRemoved` → 404 `not_found`; `ErrFixedMembers` → 409 `conflict`; anything else → 500 `internal`.
  - `decodeJSON(w, r, v any) bool`: 415 `unsupported_media_type` unless `Content-Type` is `application/json`; 400 `bad_request` on bad JSON; bodies are capped at 64 KiB.

- [ ] **Step 1: Write the failing tests.**

Append:
```go
func apiPost(t *testing.T, h http.Handler, p jam.Participant, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = appui.WithPrincipal(jam.WithParticipant(req, p), appui.Principal{User: &p})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIRead(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	pl, _ := e.Intercom.PlanPersonChat(bob, p.UserID)
	m, _ := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "ping"})
	h := Handler(e.Deps, nil)
	path := "/app/user/api/conversations/" + string(m.Channel) + "/read"
	if rec := apiPost(t, h, p, path, fmt.Sprintf(`{"seq":%d}`, m.Seq)); rec.Code != 204 {
		t.Fatalf("read = %d %s", rec.Code, rec.Body)
	}
	var rr RailResponse
	_ = json.Unmarshal(apiGet(t, h, &p, "/app/user/api/rail").Body.Bytes(), &rr)
	if rr.UnreadTotal != 0 {
		t.Errorf("after read, unreadTotal = %d", rr.UnreadTotal)
	}
	if rec := apiPost(t, h, p, "/app/user/api/conversations/chn_01j9q3zzzzzzzzzzzzzzzzzz/read", `{"seq":1}`); rec.Code != 404 {
		t.Errorf("read of a missing channel = %d", rec.Code)
	}
	if rec := apiPost(t, h, p, "/app/user/api/conversations/legacy:x/read", `{"seq":1}`); rec.Code != 204 {
		t.Errorf("read of History = %d, want 204 (no-op)", rec.Code)
	}
	if rec := apiPost(t, h, p, path, `{"seq":`); rec.Code != 400 {
		t.Errorf("bad JSON = %d", rec.Code)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader("seq=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(req, p))
	if rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Body.String(), `"code":"unsupported_media_type"`) {
		t.Errorf("form body = %d %s, want 415", rec.Code, rec.Body)
	}
}

func TestAPIMembership(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	h := Handler(e.Deps, nil)
	base := "/app/user/api/conversations/" + engID
	if rec := apiPost(t, h, p, base+"/call-in", `{"who":"user:`+string(bob)+`"}`); rec.Code != 204 || !isIn(e.st, e.room.ID, bob) {
		t.Fatalf("call-in = %d %s", rec.Code, rec.Body)
	}
	if rec := apiPost(t, h, p, base+"/leave", `{}`); rec.Code != 204 || isIn(e.st, e.room.ID, p.UserID) {
		t.Fatalf("leave = %d %s", rec.Code, rec.Body)
	}
	if _, c, _ := getConv(t, e, p, engID, ""); !c.CanJoin || c.CanLeave {
		t.Errorf("after leave: canJoin=%v canLeave=%v", c.CanJoin, c.CanLeave)
	}
	if rec := apiPost(t, h, p, base+"/join", `{}`); rec.Code != 204 || !isIn(e.st, e.room.ID, p.UserID) {
		t.Fatalf("join = %d %s", rec.Code, rec.Body)
	}
	if rec := apiPost(t, h, p, "/app/user/api/conversations/chn_01j9q3zzzzzzzzzzzzzzzzzz/join", `{}`); rec.Code != 404 {
		t.Errorf("join missing = %d %s", rec.Code, rec.Body)
	}
	if rec := apiPost(t, h, p, base+"/call-in", `{"who":"user:nobody"}`); rec.Code == 204 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("call-in nobody = %d %s", rec.Code, rec.Body)
	}
	// A chat's members are fixed.
	pl, _ := e.Intercom.PlanPersonChat(bob, p.UserID)
	m, _ := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "hi"})
	if rec := apiPost(t, h, p, "/app/user/api/conversations/"+string(m.Channel)+"/leave", `{}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"conflict"`) {
		t.Errorf("leave a chat = %d %s, want 409 conflict", rec.Code, rec.Body)
	}
}
```
Add the imports `net/http`, `net/http/httptest` and `appui`. If `LeaveChannel` on a chat returns a different error than `ErrFixedMembers`, read `intercom_membership.go` and assert on what it actually returns, keeping the 409 mapping for `ErrFixedMembers`.

- [ ] **Step 2: Run them to verify they fail.**

Run: `go test ./internal/jam/userui/ -run 'TestAPIRead|TestAPIMembership'`
Expected: FAIL. Without the POST routes the catch-all answers 404, so `read = 404`.

- [ ] **Step 3: Implement.**

Register the four POST routes in `conversationRoutes`, then add to `api_conversation.go`:
```go
// decodeJSON reads r's JSON body into v, or answers 415 (not JSON: a
// cross-site form can't send application/json without a preflight) or 400.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(v); err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad JSON body")
		return false
	}
	return true
}

// apiRead advances the viewer's read cursor (History has none: a no-op).
func (h *handler) apiRead(w http.ResponseWriter, r *http.Request) {
	var req ReadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	id := r.PathValue("c")
	if strings.HasPrefix(id, legacyPrefix) || h.d.Log == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, ok := jam.UserChannel(h.d.Store, h.d.Intercom, h.d.Log, p.UserID, ident.ID(id)); !ok {
		notFound(w)
		return
	}
	if err := h.d.Store.CommitChannelRead(p.UserID, ident.ID(id), req.Seq); err != nil {
		h.lg.Warn("userui api mark-read failed", "channel", id, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "could not mark read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) apiJoin(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	h.apiMembership(w, r, func(p jam.Participant, ch ident.ID) error { return h.d.Intercom.JoinChannel(p.UserID, ch) })
}

func (h *handler) apiLeave(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	h.apiMembership(w, r, func(p jam.Participant, ch ident.ID) error { return h.d.Intercom.LeaveChannel(p.UserID, ch) })
}

func (h *handler) apiCallIn(w http.ResponseWriter, r *http.Request) {
	var req CallInRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	h.apiMembership(w, r, func(p jam.Participant, ch ident.ID) error {
		_, _, err := h.d.Intercom.CallIn(jam.Poster{ID: p.UserID}, ch, req.Who)
		return err
	})
}

// apiMembership runs a membership change and answers as the HTML route does,
// in the API's envelope: never saying whether a refused channel exists.
func (h *handler) apiMembership(w http.ResponseWriter, r *http.Request, act func(jam.Participant, ident.ID) error) {
	p, ok := jam.ParticipantFrom(r)
	if !ok || h.d.Intercom == nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	err := act(p, ident.ID(r.PathValue("c")))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, jam.ErrSendDenied):
		writeAPIError(w, http.StatusForbidden, "forbidden", "not allowed")
	case errors.Is(err, jam.ErrSendUnresolved), errors.Is(err, jam.ErrRemoved):
		notFound(w)
	case errors.Is(err, jam.ErrFixedMembers):
		writeAPIError(w, http.StatusConflict, "conflict", "this channel's members are fixed")
	default:
		h.lg.Error("userui api membership change failed", "channel", r.PathValue("c"), "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "the change failed")
	}
}
```
The join of a missing channel must answer 404. If `JoinChannel` returns an error the switch doesn't map (so the test sees 500), check which error it returns for an unknown channel. If it's a plain "not found" sentinel, map it to `notFound` too, and ledger the ruling.

- [ ] **Step 4: Run the tests.**

Run: `go test ./internal/jam/userui/`
Expected: PASS.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/userui
git commit -m "userui: POST api/conversations/{c}/read|join|leave|call-in (JSON)"
```

---

### Task 4: Client writes, live events and the rail's selection

**Files:**
- Modify: `web/app/src/api/client.ts`, `web/app/src/api/client.test.ts`, `web/app/src/rail/useRail.ts`, `web/app/src/rail/Rail.tsx`, `web/app/src/shell/Shell.tsx`, `web/app/src/start.tsx`, `web/app/src/test/render.tsx`, `web/app/package.json`
- Create: `web/app/src/live/useLiveEvents.ts`, `web/app/src/live/useLiveEvents.test.tsx`

**Interfaces:**
- Consumes: `apiGet`, `ApiError`, `shouldRetry`.
- Produces:
  - `apiPost<T = void>(path: string, body: unknown): Promise<T>`: sends JSON and resolves `undefined` on 204.
  - `useLiveEvents(): void`: mounted once in `Shell`. `changed` invalidates `["rail"]`, `["conversation"]` and `["presence"]`; `presence` invalidates `["presence"]`; a missing `EventSource` is a no-op.
  - `POLL_MS = 30_000`: every live query's `refetchInterval`.
  - `useRail(selected?: string)`, with key `["rail", selected ?? ""]`, which fetches `rail?c=`.

- [ ] **Step 1: Write the failing tests.**

Append to `web/app/src/api/client.test.ts`:
```ts
describe("apiPost", () => {
  it("sends JSON and resolves undefined on 204", async () => {
    let got: { ct: string | null; body: unknown } | undefined;
    server.use(http.post("/app/user/api/conversations/chn_a/read", async ({ request }) => {
      got = { ct: request.headers.get("Content-Type"), body: await request.json() };
      return new HttpResponse(null, { status: 204 });
    }));
    await expect(apiPost("conversations/chn_a/read", { seq: 7 })).resolves.toBeUndefined();
    expect(got).toEqual({ ct: "application/json", body: { seq: 7 } });
  });

  it("throws the envelope on refusal", async () => {
    server.use(http.post("/app/user/api/conversations/chn_a/leave", () => HttpResponse.json({ error: "this channel's members are fixed", code: "conflict" }, { status: 409 })));
    await expect(apiPost("conversations/chn_a/leave", {})).rejects.toMatchObject({ status: 409, code: "conflict" });
  });
});
```
Update its import to `import { ApiError, apiGet, apiPost, shouldRetry } from "./client";`.

`web/app/src/live/useLiveEvents.test.tsx`:
```tsx
import { afterEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useLiveEvents } from "./useLiveEvents";

class FakeEventSource {
  static last: FakeEventSource | undefined;
  listeners: Record<string, (() => void)[]> = {};
  closed = false;
  constructor(public url: string) { FakeEventSource.last = this; }
  addEventListener(ev: string, fn: () => void) { (this.listeners[ev] ??= []).push(fn); }
  close() { this.closed = true; }
  emit(ev: string) { this.listeners[ev]?.forEach((fn) => fn()); }
}

function Probe() { useLiveEvents(); return null; }

function mount() {
  const client = new QueryClient();
  const spy = vi.spyOn(client, "invalidateQueries");
  const r = render(<QueryClientProvider client={client}><Probe /></QueryClientProvider>);
  return { spy, ...r };
}

afterEach(() => { vi.unstubAllGlobals(); FakeEventSource.last = undefined; });

describe("useLiveEvents", () => {
  it("maps changed and presence to invalidations, and closes on unmount", () => {
    vi.stubGlobal("EventSource", FakeEventSource);
    const { spy, unmount } = mount();
    const es = FakeEventSource.last!;
    expect(es.url).toBe("/app/user/events");
    es.emit("changed");
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["rail"], ["conversation"], ["presence"]]);
    spy.mockClear();
    es.emit("presence");
    expect(spy.mock.calls.map((c) => c[0]?.queryKey)).toEqual([["presence"]]);
    unmount();
    expect(es.closed).toBe(true);
  });

  it("does nothing without EventSource (the 30 s poll covers it)", () => {
    vi.stubGlobal("EventSource", undefined);
    expect(() => mount()).not.toThrow();
  });
});
```
In `web/app/src/App.test.tsx`, the rail tests now fetch `rail?c=`. MSW matches on path, so the handlers still match.

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/api src/live`
Expected: FAIL (`apiPost` is not exported; `./useLiveEvents` doesn't exist).

- [ ] **Step 3: Implement.**

`web/app/src/api/client.ts`: replace `apiGet`'s body with a shared `request`, keeping its error mapping:
```ts
async function request<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
  let res: Response;
  try {
    res = await fetch(API + path, {
      method,
      headers: body === undefined ? { Accept: "application/json" } : { Accept: "application/json", "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
      redirect: "manual",
    });
  } catch {
    throw new ApiError(0, "unavailable", "Jam can't be reached. Check your connection.");
  }
  if (res.status === 204) return undefined as T;
  const json = (res.headers.get("Content-Type") ?? "").startsWith("application/json");
  if (!json) {
    if (res.type === "opaqueredirect" || res.status === 401 || res.status === 403 || (res.status >= 300 && res.status < 400)) {
      throw new ApiError(res.status, "signed_out", "Your sign-in has expired.");
    }
    throw new ApiError(res.status, "unavailable", `Jam answered ${res.status}. Try again shortly.`);
  }
  const data: unknown = await res.json();
  if (!res.ok) {
    const e = data as { error?: string; code?: string };
    throw new ApiError(res.status, e.code ?? "error", e.error ?? res.statusText);
  }
  return data as T;
}

export const apiGet = <T>(path: string) => request<T>("GET", path);
export const apiPost = <T = void>(path: string, body: unknown) => request<T>("POST", path, body);

// POLL_MS is the live queries' fallback refresh (the old Inbox's 30 s poll),
// for when the event stream is down or absent.
export const POLL_MS = 30_000;
```

`web/app/src/live/useLiveEvents.ts`:
```ts
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";

// useLiveEvents listens to /app/user/events, which carries only signals,
// never content (spec §2), and refetches what each one names; each refetch
// goes through the API's own checks. Without EventSource, or when the stream
// answers 204 (no live push), the queries' 30 s poll keeps things fresh.
export function useLiveEvents(): void {
  const client = useQueryClient();
  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    const es = new EventSource("/app/user/events");
    es.addEventListener("changed", () => {
      void client.invalidateQueries({ queryKey: ["rail"] });
      void client.invalidateQueries({ queryKey: ["conversation"] });
      void client.invalidateQueries({ queryKey: ["presence"] });
    });
    es.addEventListener("presence", () => void client.invalidateQueries({ queryKey: ["presence"] }));
    return () => es.close();
  }, [client]);
}
```

`web/app/src/rail/useRail.ts`:
```ts
import { useQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { RailResponse } from "../api/types";

// useRail is the rail, counting the open conversation (selected) as read.
// Keys start with "rail", so a `changed` event refetches every one.
export const useRail = (selected?: string) => useQuery<RailResponse, ApiError>({
  queryKey: ["rail", selected ?? ""],
  queryFn: () => apiGet<RailResponse>(selected ? `rail?c=${encodeURIComponent(selected)}` : "rail"),
  refetchInterval: POLL_MS,
});
```
In `Rail.tsx`, call `useRail(channel)` (with `channel` from `useParams`, which is already read).

`Shell.tsx`: call `useLiveEvents()` at the top of `Shell`.

Notifications: run `cd web/app && npm install --save-exact --no-audit --no-fund @mantine/notifications@9.7.4`.
- In `start.tsx`, import `"@mantine/notifications/styles.css"` and `{ Notifications } from "@mantine/notifications"`, and render `<Notifications />` inside `MantineProvider`, before `QueryClientProvider`.
- In `test/render.tsx`, render `<Notifications />` inside `MantineProvider` too, so components can call `notifications.show`.
- Put the CSS import in `main.tsx`, next to the core styles.

- [ ] **Step 4: Run the checks.**

Run: `cd web/app && npm run check`
Expected: PASS (all earlier tests plus 4 new). Typecheck and lint are clean.

- [ ] **Step 5: Commit.**
```bash
git add web/app
git commit -m "web/app: apiPost, live events (signal → invalidate), rail selection, notifications"
```

---

### Task 5: The conversation view

**Files:**
- Create:
  - `web/app/src/conversation/useConversation.ts`
  - `web/app/src/conversation/ConversationView.tsx`
  - `web/app/src/conversation/ConversationHeader.tsx`
  - `web/app/src/conversation/MessageList.tsx`
  - `web/app/src/conversation/Message.tsx`
  - `web/app/src/conversation/MessageBody.tsx`
  - `web/app/src/conversation/PresenceStrip.tsx`
  - `web/app/src/conversation/copy.ts`
  - `web/app/src/conversation/days.ts`, `web/app/src/conversation/days.test.ts`
  - `web/app/src/conversation/body.css`
  - `web/app/src/conversation/ConversationView.test.tsx`
- Modify: `web/app/src/routes/Conversation.tsx`, `web/app/src/App.test.tsx` (its old "deep-links to the current Inbox" test is replaced)

**Interfaces:**
- Consumes: `apiGet`, `apiPost`, `POLL_MS`, `ApiError`, `Problem`; the types `Conversation`, `MessageRow`, `SessionRow`, `PresenceResponse`; query keys `["conversation", id]`, `["presence", id]` and `["rail"]`.
- Produces:
  - `useConversation(id)`: a `useInfiniteQuery<Conversation>` with `initialPageParam: 0`. `getNextPageParam` returns the `before` value: the first message's `seq` when `earlier`.
  - `messagesOf(pages: Conversation[]): MessageRow[]`: older pages first, de-duplicated by seq.
  - `dayLabel(iso, now)`: "Today", "Yesterday", or the date via `toLocaleDateString`.
  - `copyText(text): Promise<boolean>`.
  - `<MessageBody html plain />`: the one inner-HTML site; adds code-copy buttons.

- [ ] **Step 1: Write the failing tests.**

`web/app/src/conversation/days.test.ts`:
```ts
import { describe, expect, it } from "vitest";
import { dayLabel, sameDay } from "./days";

describe("days", () => {
  const now = new Date(2026, 9, 26, 15, 0); // local time
  it("labels today, yesterday and older dates", () => {
    expect(dayLabel(new Date(2026, 9, 26, 0, 5).toISOString(), now)).toBe("Today");
    expect(dayLabel(new Date(2026, 9, 25, 23, 59).toISOString(), now)).toBe("Yesterday");
    expect(dayLabel(new Date(2026, 9, 20, 12).toISOString(), now)).toBe(new Date(2026, 9, 20, 12).toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" }));
  });
  it("compares local days", () => {
    expect(sameDay(new Date(2026, 9, 26, 0, 1).toISOString(), new Date(2026, 9, 26, 23, 59).toISOString())).toBe(true);
    expect(sameDay(new Date(2026, 9, 26, 0, 1).toISOString(), new Date(2026, 9, 25, 23, 59).toISOString())).toBe(false);
  });
});
```

`web/app/src/conversation/ConversationView.test.tsx`:
```tsx
import { describe, expect, it } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { boot, renderApp } from "../test/render";
import type { Conversation, MessageRow, RailResponse } from "../api/types";

const rail: RailResponse = { unreadTotal: 0, groups: [] };
const msg = (seq: number, over: Partial<MessageRow> = {}): MessageRow => ({
  from: "spider", body: `<p>m${seq}</p>`, raw: `m${seq}`, plain: false, at: "09:00", time: new Date(2026, 9, 26, 9, seq % 60).toISOString(),
  mine: false, seq, system: false, sysText: "", card: "", ...over,
});
const conv = (over: Partial<Conversation> = {}): Conversation => ({
  channelId: "chn_a", label: "intercom-spider", kind: "session", project: "test", phase: "live", needsYou: false, sendTo: "chn_a",
  messages: [msg(1), msg(2, { mine: true, raw: "**hi**", body: "<p><strong>hi</strong></p>" })], lastSeq: 2, earlier: false,
  sessionIds: ["ses_1"], sessions: [{ label: "intercom-spider", text: "is running", tool: "Bash", class: "busy" }],
  canJoin: false, canLeave: true, callIn: [{ to: "user:usr_2", label: "bob", kind: "person", project: "test" }], ...over,
});

function serve(c: Conversation | ((before: number) => Conversation), reads: number[] = []) {
  server.use(
    http.get("/app/user/api/rail", () => HttpResponse.json(rail)),
    http.get("/app/user/api/conversations/:c", ({ request }) => {
      const before = Number(new URL(request.url).searchParams.get("before") ?? 0);
      return HttpResponse.json(typeof c === "function" ? c(before) : c);
    }),
    http.get("/app/user/api/conversations/:c/presence", () => HttpResponse.json({ sessions: (typeof c === "function" ? c(0) : c).sessions })),
    http.post("/app/user/api/conversations/:c/read", async ({ request }) => {
      reads.push(((await request.json()) as { seq: number }).seq);
      return new HttpResponse(null, { status: 204 });
    }),
  );
  return reads;
}

describe("the conversation view", () => {
  it("shows the header, messages (server HTML), presence, and marks read", async () => {
    const reads = serve(conv());
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByRole("heading", { name: "intercom-spider" })).toBeInTheDocument();
    expect(screen.getByText("test · agent")).toBeInTheDocument();
    expect(screen.getByText("m1")).toBeInTheDocument();
    expect(screen.getByText("hi").tagName).toBe("STRONG"); // rendered by squawkrender, inserted as HTML
    expect(screen.getByText("you")).toBeInTheDocument();
    expect(screen.getByText("Today")).toBeInTheDocument();
    expect(await screen.findByText(/is running/)).toBeInTheDocument();
    expect(screen.getByText("Bash")).toBeInTheDocument();
    await waitFor(() => expect(reads).toEqual([2]));
  });

  it("offers Leave and Call in to a member, Join to a non-member", async () => {
    serve(conv());
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByRole("button", { name: "Leave" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Call in" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Join" })).toBeNull();
  });

  it("joins, then refetches", async () => {
    let joined = false;
    serve(conv({ canJoin: true, canLeave: false, callIn: [] }));
    server.use(
      http.post("/app/user/api/conversations/chn_a/join", () => { joined = true; return new HttpResponse(null, { status: 204 }); }),
      http.get("/app/user/api/conversations/:c", () => HttpResponse.json(conv({ canJoin: !joined, canLeave: joined }))),
    );
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Join" }));
    expect(await screen.findByRole("button", { name: "Leave" })).toBeInTheDocument();
  });

  it("shows a refused membership change as a notification", async () => {
    serve(conv());
    server.use(http.post("/app/user/api/conversations/chn_a/leave", () => HttpResponse.json({ error: "this channel's members are fixed", code: "conflict" }, { status: 409 })));
    renderApp(boot, "/app/next/c/chn_a");
    await userEvent.click(await screen.findByRole("button", { name: "Leave" }));
    expect(await screen.findByText("this channel's members are fixed")).toBeInTheDocument();
  });

  it("History is read-only: no controls, no mark-read", async () => {
    const reads = serve(conv({ channelId: "legacy:x", sendTo: "", canLeave: false, callIn: [], sessions: [] }));
    renderApp(boot, "/app/next/c/legacy%3Ax");
    expect(await screen.findByText("m1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Leave|Join|Call in/ })).toBeNull();
    await new Promise((r) => setTimeout(r, 50));
    expect(reads).toEqual([]);
  });

  it("loads earlier pages without duplicates, and keeps them across a refresh", async () => {
    const all = Array.from({ length: 250 }, (_, i) => msg(i + 1));
    const page = (before: number) => {
      const upto = before > 0 ? all.filter((m) => m.seq < before) : all;
      const slice = upto.slice(-200);
      return conv({ messages: slice, earlier: upto.length > 200, lastSeq: 250 });
    };
    serve(page);
    const { container } = renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByText("m250")).toBeInTheDocument();
    expect(screen.queryByText("m50")).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Load earlier" }));
    expect(await screen.findByText("m50")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load earlier" })).toBeNull();
    expect(container.querySelectorAll("[data-seq]").length).toBe(250);
  });

  it("shows attachments, system lines, re: links and a placeholder for cards", async () => {
    serve(conv({ messages: [
      msg(1, { files: [{ id: "blb_1", name: "shot.png", size: "12 KB", image: true }, { id: "blb_2", name: "log.txt", size: "1 KB", image: false }] }),
      msg(2, { system: true, sysText: "bob answered: Yes" }),
      msg(3, { reAsk: { id: "ask_1", question: "Ship it?" } }),
      msg(4, { card: "ask", body: "<p>Ship it?</p>" }),
    ] }));
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByRole("img", { name: "shot.png" })).toHaveAttribute("src", "/app/user/blobs/blb_1");
    expect(screen.getByRole("link", { name: /log\.txt/ })).toHaveAttribute("href", "/app/user/blobs/blb_2");
    expect(screen.getByText("bob answered: Yes")).toBeInTheDocument();
    expect(screen.getByText("re: Ship it?")).toBeInTheDocument();
    const card = screen.getByText(/This ask shows in the current Inbox/);
    expect(within(card.closest("[data-seq]") as HTMLElement).getByRole("link", { name: /current Inbox/ })).toHaveAttribute("href", "/app/user/?c=chn_a");
  });

  it("gives code blocks a copy button", async () => {
    serve(conv({ messages: [msg(1, { body: "<pre><code>go test ./...\n</code></pre>" })] }));
    renderApp(boot, "/app/next/c/chn_a");
    expect(await screen.findByRole("button", { name: "Copy code" })).toBeInTheDocument();
  });

  it("says when the conversation can't be opened", async () => {
    server.use(
      http.get("/app/user/api/rail", () => HttpResponse.json(rail)),
      http.get("/app/user/api/conversations/:c", () => HttpResponse.json({ error: "no such conversation", code: "not_found" }, { status: 404 })),
    );
    renderApp(boot, "/app/next/c/chn_gone");
    expect(await screen.findByText("no such conversation")).toBeInTheDocument();
  });
});
```
In `App.test.tsx`, delete the test "deep-links a conversation to the current Inbox until slice 2". Its route now renders the real view, which these tests cover.

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/conversation`
Expected: FAIL (the modules don't exist).

- [ ] **Step 3: Implement the helpers.**

`web/app/src/conversation/days.ts`:
```ts
const key = (d: Date) => `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;

// sameDay: a and b (RFC 3339) fall on the same local day.
export const sameDay = (a: string, b: string) => key(new Date(a)) === key(new Date(b));

// dayLabel is a day divider's text: Today, Yesterday, or the full local date.
export function dayLabel(iso: string, now = new Date()): string {
  const d = new Date(iso);
  if (key(d) === key(now)) return "Today";
  const y = new Date(now);
  y.setDate(now.getDate() - 1);
  if (key(d) === key(y)) return "Yesterday";
  return d.toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric", year: "numeric" });
}

// timeOf is a message's local time of day.
export const timeOf = (iso: string) => new Date(iso).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
```

`web/app/src/conversation/copy.ts`:
```ts
// copyText puts text on the clipboard: the async API in a secure context,
// else a hidden textarea and execCommand (the old Inbox's meCopy).
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(text); return true; } catch { return false; }
  }
  const ta = document.createElement("textarea");
  ta.value = text;
  ta.style.position = "fixed";
  ta.style.opacity = "0";
  document.body.appendChild(ta);
  ta.select();
  try { return document.execCommand("copy"); } finally { ta.remove(); }
}
```

`web/app/src/conversation/useConversation.ts`:
```ts
import { useInfiniteQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { Conversation, MessageRow } from "../api/types";

// useConversation pages a conversation newest-first: page 0 is the newest
// (before=0), and each next page is older. A refetch (a `changed` event or
// the poll) refetches every loaded page in order, so loaded history stays.
export const useConversation = (id: string) => useInfiniteQuery<Conversation, ApiError>({
  queryKey: ["conversation", id],
  initialPageParam: 0,
  queryFn: ({ pageParam }) => apiGet<Conversation>(`conversations/${encodeURIComponent(id)}${pageParam ? `?before=${pageParam as number}` : ""}`),
  getNextPageParam: (last) => (last.earlier && last.messages.length > 0 ? last.messages[0]!.seq : undefined),
  refetchInterval: POLL_MS,
});

// messagesOf is every loaded message, oldest first, each seq once.
export function messagesOf(pages: Conversation[]): MessageRow[] {
  const seen = new Set<number>();
  const out: MessageRow[] = [];
  for (const p of [...pages].reverse()) {
    for (const m of p.messages) {
      if (!seen.has(m.seq)) { seen.add(m.seq); out.push(m); }
    }
  }
  return out;
}
```

- [ ] **Step 4: Implement the components.**

`web/app/src/conversation/MessageBody.tsx`:
```tsx
import { useEffect, useRef } from "react";
import { copyText } from "./copy";
import "./body.css";

// addCodeButtons gives each code block a copy button, in a wrapper so the
// button stays put while the block scrolls sideways (the old meCodeButtons).
function addCodeButtons(root: HTMLElement) {
  root.querySelectorAll("pre").forEach((pre) => {
    if (pre.parentElement?.classList.contains("codebox")) return;
    const box = document.createElement("div");
    box.className = "codebox";
    const b = document.createElement("button");
    b.type = "button";
    b.className = "copy-code";
    b.title = "Copy code";
    b.setAttribute("aria-label", "Copy code");
    b.textContent = "⧉";
    b.onclick = () => void copyText((pre.textContent ?? "").replace(/\n$/, "")).then((ok) => {
      if (ok) { b.textContent = "✓"; setTimeout(() => { b.textContent = "⧉"; }, 1200); }
    });
    pre.parentNode?.insertBefore(box, pre);
    box.append(pre, b);
  });
}

// MessageBody shows a squawk body as squawkrender rendered it on the server.
export function MessageBody({ html, plain }: { html: string; plain: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => { if (ref.current && !plain) addCodeButtons(ref.current); }, [html, plain]);
  return (
    // eslint-disable-next-line no-restricted-syntax -- the one allowed site: squawkrender's sanitized HTML (spec §4, docs/web-app.md)
    <div ref={ref} className={plain ? "jam-body plain" : "jam-body md"} dangerouslySetInnerHTML={{ __html: html }} />
  );
}
```

`web/app/src/conversation/body.css`:
```css
/* Message bodies: squawkrender's HTML, styled with Mantine's variables. */
.jam-body { overflow-wrap: anywhere; line-height: 1.5; }
.jam-body.plain { white-space: pre-wrap; font-family: var(--mantine-font-family-monospace); font-size: var(--mantine-font-size-sm); }
.jam-body > :first-child { margin-top: 0; }
.jam-body > :last-child { margin-bottom: 0; }
.jam-body p { margin: 0 0 0.5em; }
.jam-body a { color: var(--mantine-color-anchor); }
.jam-body code { font-family: var(--mantine-font-family-monospace); font-size: 0.9em; background: var(--mantine-color-default-hover); padding: 0 0.25em; border-radius: var(--mantine-radius-xs); }
.jam-body pre { margin: 0; padding: 0.6em 0.8em; overflow-x: auto; background: var(--mantine-color-default-hover); border-radius: var(--mantine-radius-sm); }
.jam-body pre code { background: none; padding: 0; }
.jam-body .codebox { position: relative; margin: 0.5em 0; }
.jam-body .copy-code { position: absolute; top: 4px; right: 4px; border: 1px solid var(--mantine-color-default-border); background: var(--mantine-color-body); border-radius: var(--mantine-radius-xs); cursor: pointer; font-size: 12px; line-height: 1; padding: 3px 5px; opacity: 0.7; }
.jam-body .copy-code:hover { opacity: 1; }
.jam-body blockquote { margin: 0.5em 0; padding-left: 0.8em; border-left: 3px solid var(--mantine-color-default-border); color: var(--mantine-color-dimmed); }
.jam-body table { border-collapse: collapse; margin: 0.5em 0; }
.jam-body th, .jam-body td { border: 1px solid var(--mantine-color-default-border); padding: 0.2em 0.5em; }
.jam-body img { max-width: 100%; }
```

`web/app/src/conversation/Message.tsx`:
```tsx
import { Anchor, Group, Image, Paper, Text, UnstyledButton } from "@mantine/core";
import { useRef } from "react";
import type { MessageRow } from "../api/types";
import { MessageBody } from "./MessageBody";
import { copyText } from "./copy";
import { timeOf } from "./days";

const CARD_NAME: Record<string, string> = { ask: "ask", board: "whiteboard" };

// Message is one row: a system line, a card placeholder, or a bubble.
export function Message({ m, channel }: { m: MessageRow; channel: string }) {
  const bodyRef = useRef<HTMLDivElement>(null);
  if (m.system) {
    return <Text data-seq={m.seq} size="xs" c="dimmed" ta="center" my={4}>{m.sysText} · {timeOf(m.time)}</Text>;
  }
  if (m.card) {
    return (
      <Paper data-seq={m.seq} withBorder p="xs" my={4} maw="80%">
        <Text size="sm">This {CARD_NAME[m.card] ?? m.card} shows in the current Inbox for now.{" "}
          <Anchor href={`/app/user/?c=${encodeURIComponent(channel)}`}>Open it in the current Inbox</Anchor>
        </Text>
      </Paper>
    );
  }
  const textOf = () => (bodyRef.current?.querySelector(".jam-body") as HTMLElement | null)?.innerText.replace(/\n+$/, "") ?? "";
  return (
    <Paper data-seq={m.seq} p="xs" my={4} maw="80%" ml={m.mine ? "auto" : 0} bg={m.mine ? "var(--mantine-primary-color-light)" : "var(--mantine-color-default-hover)"}>
      <Group justify="space-between" gap="xs" wrap="nowrap">
        <Text size="xs" fw={600}>{m.mine ? "you" : m.from}</Text>
        <Group gap={6} wrap="nowrap">
          <UnstyledButton fz="xs" c="dimmed" title="Copy the message as text" onClick={() => void copyText(textOf())}>Text</UnstyledButton>
          <UnstyledButton fz="xs" c="dimmed" title="Copy the message as markdown, exactly as sent" onClick={() => void copyText(m.raw)}>Markdown</UnstyledButton>
          <Text size="xs" c="dimmed">{timeOf(m.time)}</Text>
        </Group>
      </Group>
      {m.reAsk && <Anchor size="xs" href={`/app/user/?c=${encodeURIComponent(channel)}#ask-${m.reAsk.id}`}>re: {m.reAsk.question}</Anchor>}
      <div ref={bodyRef}>{m.body && <MessageBody html={m.body} plain={m.plain} />}</div>
      {m.files && (
        <Group gap="xs" mt={4}>
          {m.files.map((f) => f.image
            ? <Anchor key={f.id} href={`/app/user/blobs/${f.id}`} target="_blank" rel="noopener"><Image src={`/app/user/blobs/${f.id}`} alt={f.name} h={120} w="auto" radius="sm" loading="lazy" /></Anchor>
            : <Anchor key={f.id} href={`/app/user/blobs/${f.id}`} download size="sm">📄 {f.name} <Text span size="xs" c="dimmed">{f.size}</Text></Anchor>)}
        </Group>
      )}
    </Paper>
  );
}
```

`web/app/src/conversation/MessageList.tsx`:
```tsx
import { Button, Center, Divider, ScrollArea, Text } from "@mantine/core";
import { Fragment, useEffect, useLayoutEffect, useRef } from "react";
import type { MessageRow } from "../api/types";
import { Message } from "./Message";
import { dayLabel, sameDay } from "./days";

// MessageList shows the messages oldest-first with day dividers. It opens
// at the newest, stays pinned there while new ones arrive (unless the reader
// scrolled up), and keeps position when older ones load above.
export function MessageList({ messages, channel, earlier, loadingEarlier, onEarlier }: {
  messages: MessageRow[]; channel: string; earlier: boolean; loadingEarlier: boolean; onEarlier: () => void;
}) {
  const viewport = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);
  const prev = useRef<{ first?: number; height: number }>({ height: 0 });

  useLayoutEffect(() => {
    const el = viewport.current;
    if (!el) return;
    const first = messages[0]?.seq;
    if (prev.current.first !== undefined && first !== undefined && first < prev.current.first) {
      el.scrollTop += el.scrollHeight - prev.current.height; // older loaded above: keep the view
    } else if (pinned.current) {
      el.scrollTop = el.scrollHeight;
    }
    prev.current = { first, height: el.scrollHeight };
  }, [messages]);

  useEffect(() => { pinned.current = true; }, [channel]);

  if (messages.length === 0) return <Center flex={1}><Text c="dimmed">No messages yet.</Text></Center>;
  return (
    <ScrollArea flex={1} viewportRef={viewport} onScrollPositionChange={() => {
      const el = viewport.current;
      if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    }}>
      {earlier && <Center my="xs"><Button variant="subtle" size="xs" loading={loadingEarlier} onClick={onEarlier}>Load earlier</Button></Center>}
      {messages.map((m, i) => (
        <Fragment key={m.seq}>
          {(i === 0 || !sameDay(messages[i - 1]!.time, m.time)) && <Divider my="xs" label={dayLabel(m.time)} labelPosition="center" />}
          <Message m={m} channel={channel} />
        </Fragment>
      ))}
    </ScrollArea>
  );
}
```

`web/app/src/conversation/PresenceStrip.tsx`:
```tsx
import { Group, Loader, Text } from "@mantine/core";
import { useQuery } from "@tanstack/react-query";
import { apiGet, POLL_MS, type ApiError } from "../api/client";
import type { PresenceResponse } from "../api/types";

// PresenceStrip shows each session in the conversation and what it's doing
// (status and tool name only, never event content).
export function PresenceStrip({ channel }: { channel: string }) {
  const { data } = useQuery<PresenceResponse, ApiError>({
    queryKey: ["presence", channel],
    queryFn: () => apiGet<PresenceResponse>(`conversations/${encodeURIComponent(channel)}/presence`),
    refetchInterval: POLL_MS,
  });
  if (!data || data.sessions.length === 0) return null;
  return (
    <Group gap="md" px="sm" py={4}>
      {data.sessions.map((s) => (
        <Group key={s.label} gap={4} wrap="nowrap">
          <Text size="xs" fw={600} c={s.class === "wait" ? "yellow.8" : undefined}>{s.label}</Text>
          <Text size="xs" c={s.class === "dim" ? "dimmed" : undefined}>{s.text}{s.tool && <> <b>{s.tool}</b></>}</Text>
          {s.class === "busy" && <Loader type="dots" size="xs" aria-hidden />}
        </Group>
      ))}
    </Group>
  );
}
```

`web/app/src/conversation/ConversationHeader.tsx`:
```tsx
import { Badge, Button, Group, Select, Text, Title } from "@mantine/core";
import { notifications } from "@mantine/notifications";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { apiPost, ApiError } from "../api/client";
import type { Conversation } from "../api/types";

// ConversationHeader: title, project · kind, Needs you, and the membership
// controls (Join / Leave / Call in), which refetch the conversation and rail.
export function ConversationHeader({ c }: { c: Conversation }) {
  const client = useQueryClient();
  const [who, setWho] = useState<string | null>(null);
  const act = useMutation<void, ApiError, { path: string; body: unknown }>({
    mutationFn: ({ path, body }) => apiPost(`conversations/${encodeURIComponent(c.channelId)}/${path}`, body),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["conversation", c.channelId] });
      void client.invalidateQueries({ queryKey: ["rail"] });
    },
    onError: (e) => notifications.show({ color: "red", message: e.message }),
  });
  return (
    <Group justify="space-between" px="sm" py="xs" wrap="nowrap" style={{ borderBottom: "1px solid var(--mantine-color-default-border)" }}>
      <div>
        <Title order={2} size="h4">{c.label}</Title>
        <Text size="xs" c="dimmed">{c.project}{c.kind && ` · ${c.kind === "session" ? "agent" : c.kind}`}</Text>
      </div>
      <Group gap="xs" wrap="nowrap">
        {c.needsYou && <Badge color="yellow">Needs you</Badge>}
        {c.callIn.length > 0 && (
          <Group gap={4} wrap="nowrap">
            <Select size="xs" placeholder="Call in…" aria-label="Who to call in" value={who} onChange={setWho}
              data={c.callIn.map((o) => ({ value: o.to, label: `${o.label} (${o.kind})` }))} />
            <Button size="xs" variant="default" disabled={!who} loading={act.isPending}
              onClick={() => who && act.mutate({ path: "call-in", body: { who } }, { onSuccess: () => setWho(null) })}>Call in</Button>
          </Group>
        )}
        {c.canJoin && <Button size="xs" loading={act.isPending} onClick={() => act.mutate({ path: "join", body: {} })}>Join</Button>}
        {c.canLeave && <Button size="xs" variant="default" loading={act.isPending} onClick={() => act.mutate({ path: "leave", body: {} })}>Leave</Button>}
      </Group>
    </Group>
  );
}
```

`web/app/src/conversation/ConversationView.tsx`:
```tsx
import { Loader, Stack } from "@mantine/core";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { apiPost } from "../api/client";
import { Problem } from "../Problem";
import { ConversationHeader } from "./ConversationHeader";
import { MessageList } from "./MessageList";
import { PresenceStrip } from "./PresenceStrip";
import { messagesOf, useConversation } from "./useConversation";
import { useLocation } from "react-router";

// ConversationView is one open conversation: header, messages, presence. It
// marks the conversation read up to its newest message while the page is
// visible (History has no cursor: never).
export function ConversationView({ id }: { id: string }) {
  const q = useConversation(id);
  const client = useQueryClient();
  const here = useLocation();
  const head = q.data?.pages[0];
  const lastSeq = head?.lastSeq ?? 0;
  const canRead = !!head?.sendTo;

  useEffect(() => {
    if (!canRead || lastSeq <= 0) return;
    const send = () => {
      if (document.visibilityState !== "visible") return;
      void apiPost(`conversations/${encodeURIComponent(id)}/read`, { seq: lastSeq })
        .then(() => client.invalidateQueries({ queryKey: ["rail"] }))
        .catch(() => {}); // a failed mark-read is retried on the next change
    };
    send();
    document.addEventListener("visibilitychange", send);
    return () => document.removeEventListener("visibilitychange", send);
  }, [id, lastSeq, canRead, client]);

  if (q.isPending) return <Loader m="md" size="sm" />;
  if (q.error) return <Problem error={q.error} here={here.pathname + here.search} />;
  return (
    <Stack gap={0} h="calc(100dvh - var(--app-shell-header-height) - 2 * var(--app-shell-padding))">
      <ConversationHeader c={q.data.pages[0]!} />
      <MessageList messages={messagesOf(q.data.pages)} channel={id} earlier={q.hasNextPage}
        loadingEarlier={q.isFetchingNextPage} onEarlier={() => void q.fetchNextPage()} />
      <PresenceStrip channel={id} />
    </Stack>
  );
}
```

`web/app/src/routes/Conversation.tsx`:
```tsx
import { useParams } from "react-router";
import { ConversationView } from "../conversation/ConversationView";

export function Conversation() {
  const { channel = "" } = useParams();
  return <ConversationView key={channel} id={channel} />;
}
```

- [ ] **Step 5: Run the checks.**

Run: `cd web/app && npm run check`
Expected: all tests pass, and lint is clean. Only `MessageBody` carries the `no-restricted-syntax` disable.

If "loads earlier pages" fails because jsdom has no layout (`scrollHeight` is 0), don't change the assertion. The scroll code tolerates zeros; check instead that `getNextPageParam` returns the first message's seq.

If Mantine's `Divider` label renders twice in jsdom (it can), use `getAllByText("Today")[0]` in the first test and ledger the change.

- [ ] **Step 6: Commit.**
```bash
git add web/app
git commit -m "web/app: the conversation view (messages, paging, mark-read, membership, presence)"
```

---

### Task 6: End-to-end in the browser, docs

**Files:**
- Modify: `internal/jam/userui/next_browser_test.go`, `docs/web-app.md`, `docs/usage/jam/intercom-ui.md`

**Interfaces:**
- Consumes: `pageProblems`, `step`, `into`, `call`, `browserCtx`, `fixture`, `env.post` (existing).

- [ ] **Step 1: Write the browser test.**

Append to `internal/jam/userui/next_browser_test.go` (add imports `fmt` and `time`):
```go
// testChanges is a Changes source the test fires by hand (the events stream's
// `changed`).
type testChanges struct {
	mu   sync.Mutex
	subs []chan struct{}
}

func (c *testChanges) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	return ch, func() {}
}

func (c *testChanges) fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// The conversation renders squawkrender's markdown (code blocks get a copy
// button), raw HTML stays inert under the CSP, a long conversation opens at
// its newest message, and a new message arrives live (events → refetch).
func TestBrowserNextConversation(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	for i := 0; i < 60; i++ {
		e.post(p.UserID, fmt.Sprintf("filler %d", i), "")
	}
	e.post(p.UserID, "```\ngo test ./...\n```\n<img src=x onerror=\"window.__pwned=1\"> <script>window.__pwned=2</script>", "")
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
	t.Cleanup(srv.Close)

	var problems pageProblems
	var pwned, atBottom bool
	var codeButtons int
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(chromedp.CSS(`button[aria-label="Copy code"]`)),
		into(&codeButtons, chromedp.Evaluate[int](`document.querySelectorAll('button[aria-label="Copy code"]').length`)),
		into(&pwned, chromedp.Evaluate[bool](`!!window.__pwned || !!document.querySelector('[data-seq] img[onerror], [data-seq] script')`)),
		chromedp.Sleep(300*time.Millisecond), // layout settles, then the scroll pin
		into(&atBottom, chromedp.Evaluate[bool](`(() => { const v = document.querySelector('.mantine-ScrollArea-viewport'); return !!v && v.scrollHeight - v.scrollTop - v.clientHeight < 40; })()`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if codeButtons != 1 {
		t.Errorf("code copy buttons = %d, want 1", codeButtons)
	}
	if pwned {
		t.Error("raw HTML in a body ran or reached the DOM")
	}
	if !atBottom {
		t.Error("a long conversation didn't open at its newest message")
	}

	// A new message shows up without a reload: the stream's `changed`
	// invalidates the conversation (an invalidation refetches even within
	// the queries' staleTime).
	e.post(p.UserID, "arrived live", "")
	changes.fire()
	var last string
	for i := 0; i < 50 && !strings.Contains(last, "arrived live"); i++ {
		changes.fire() // idempotent: covers a fire before the EventSource connected
		if err := chromedp.Do(ctx, into(&last, chromedp.Evaluate[string](`[...document.querySelectorAll('[data-seq]')].pop()?.innerText ?? ''`))); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(last, "arrived live") {
		t.Errorf("new message didn't arrive; last row = %q", last)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}
```
The loop fires on every iteration because a `changed` event only reaches the page once its `EventSource` is connected, and a repeated fire is harmless.

Run: `./scripts/app.sh && go test -tags browser ./internal/jam/userui/ -run TestBrowserNext -v`
Expected: PASS for both browser tests.

- [ ] **Step 2: Update the docs.**

`docs/web-app.md`:
- Under "Layout", add `src/conversation/` ("the conversation pane: `useConversation` pages newest-first with `useInfiniteQuery`; `MessageBody` is the one inner-HTML site") and `src/live/useLiveEvents.ts` ("the one EventSource: signals → query invalidations; 30 s `POLL_MS` fallback").
- Under "Adding an API route", note: "writes are `POST` with JSON (`decodeJSON`: 415 otherwise); a resource the viewer may not see answers exactly like a missing one (404 `not_found`)".

`docs/usage/jam/intercom-ui.md`: in the "Preview: the Jam app at `/app/next/`" section, replace "So far it shows the title bar and the rail; opening a conversation links back here." with:
"So far it shows the title bar, the rail and conversations (messages, Join/Leave/Call in, presence; live). Replying, asks and whiteboards still happen here."
Keep the section to these lines, since the doc is over its size budget.

Run: `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | tail -1`
Expected: the error count is no higher than on main.

- [ ] **Step 3: Run the whole suite and commit.**

Run: `go test ./... && just app-check && ./scripts/app.sh && just test-browser && just lint`
Expected: all green.
```bash
git add internal/jam/userui/next_browser_test.go docs
git commit -m "Jam app: conversation end-to-end test and docs"
```
Open one PR, "Jam app slice 2: conversations".

---

## Self-review notes

- **Spec coverage, §3 slice 2:**
  - message list with paging and day dividers: Tasks 2 and 5;
  - squawkrender `bodyHtml` plus `raw`: Tasks 1, 2 and 5 (the field is named `body`, matching the view model);
  - copy as markdown: Task 5;
  - mark-read: Tasks 3 and 5;
  - live `changed`: Task 4;
  - join/leave/call-in: Tasks 3 and 5;
  - presence strip: Tasks 2 and 5.
- **Spec coverage, §2 and §4:** JSON siblings, error envelope, signal-only stream, the single HTML sink and no existence leak: Tasks 2–5.
- **Deferred on purpose:**
  - ask cards and the open-asks panel (slice 4);
  - board cards (slice 7);
  - the session header (slice 5);
  - the composer and sounds (slice 3).

  Their rows show a placeholder.
- **Naming:** the spec's `bodyHtml` is carried as `body`, because the view model's field is `Body`; renaming it would fork the template's name. The field holds HTML, as its doc comment says.
