# Jam SPA, slice 6a: the Thinking tab — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the session pane's **Thinking** tab works in the Jam app, matching the old Inbox (`layout.html`'s `meTh*` code):
- the session's newest stream arrives live: a backfill of the newest 300 events, then live ones, followed at the bottom, with "↓ N new" when scrolled up;
- **Load earlier** pages older events in;
- a reconnect's hole (`skipped`) shows a marker that fills the hole on demand;
- a restart (`newstream`) adds "— session restarted —" and follows the new stream;
- a tool result folds into its call's line, with ✓/✗, the time taken, and the output clipped to 40 lines with "show all N lines";
- result lines become episode cost dividers;
- runs of empty thinking collapse into "thinking ×N";
- the filters work: the chips (Thinking, Text, Tools, Errors only, Progress), the tool selector, search, the format selector (Narrative / Detailed / Raw, where Raw fetches each line's JSON when it comes into view) and the stream selector;
- the list is capped at 2000 rows:
  - live events past the cap drop the oldest rows and offer Load earlier;
  - a loaded page past the cap drops the newest rows and pauses following, and the pill reloads the tail;
- "working…" shows while the session is busy;
- a refused feed explains itself: "no longer available" on 404, otherwise backoff and then "Too many Thinking views open";
- the preferences live in `localStorage["jam-thinking"]`, shared with the old Inbox.

**Architecture:**
- **Go.** The live feed stays the same SSE route (`/app/user/session/{id}/thinking/events`). With `?format=json`, each `ev` carries a `ThinkingEvent` JSON object instead of an HTML fragment. Everything else is unchanged: the cursor, `more`, `skipped`, `newstream`, the per-viewer cap and the access re-check (spec §2).
- **New API routes:**
  - `GET api/sessions/{id}/pane/thinking`: the stream list, or `eventsOff`;
  - `GET api/sessions/{id}/thinking?stream=&before=`: a page;
  - `GET api/sessions/{id}/thinking/raw?stream=&seq=`: one event's JSON.
  - Each shares its lookup with the old HTML route.
- **App.** A lazily loaded `src/session/thinking/` chunk, made of:
  - pure modules: `prefs`, `format`, `feed` (a reducer over the list's items, including the cap) and `rows` (the derived display rows: pairing, costs, runs, filters);
  - a `useThinkingFeed` hook (the EventSource and its refusal handling);
  - the `ThinkingTab` component, which renders the rows with `@tanstack/react-virtual`. That replaces the old IntersectionObserver bookkeeping (the spec's "a virtualized list replaces the parked minors").

**Tech Stack:** the slice 1–5 stack plus **`@tanstack/react-virtual` 3.14.23**, the one new dependency. It's from the TanStack family the app already uses, and Renovate groups it.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md):
- §3 slice 6, the Thinking half: "the Thinking tab (live tail, paging, raw, the 2000-row cap; a virtualized list replaces the parked `isConnected`/unobserve minors)";
- §2: "The Thinking tail stream gains a JSON-rows form (`…/thinking/events?format=json`) that keeps its cursor, `newstream` and the per-viewer stream cap";
- §1 and §5: "the whiteboard, Thinking and Studio are lazy chunks".

This plan is **slice 6a**. Studio, Previews "View here" and the `frame-src` CSP are **6b**, with their own plan.

Helpers already on main:
- Go: `thinkEnv`, `textEvent`, `appendText`, `thinkStream`, `sseLines`, `sseRead`, `sessAPI`, `decodeAs`, `checkJSONHeaders`;
- browser tests: `assistantBlock`, `toolResultBlock`, `thinkRaw`, `pageProblems`, `browserCtx`, `into`, `call`;
- TS: `apiGet`, `ApiError`, `usePaneTab`, `renderApp(b, path, client)`, `boot`, `server`, and `FakeEventSource` (pattern in `useLiveEvents.test.tsx`).

## Global Constraints

- **Parity:** what the old Thinking tab does works the same. The old routes keep their exact output, and `thinking_test.go` and `thinking_browser_test.go` pass unchanged:
  - `…/thinking/events` without `format`;
  - `…/thinking/page`;
  - `…/thinking/raw`.
- **Agent text is data:** every string in a `ThinkingEvent` is rendered as text (React text nodes). It is never HTML, never `dangerouslySetInnerHTML`, never a URL.
- **Authorization:**
  - every route re-checks `sessionFor` (the home-channel rule);
  - an unseen session answers exactly like a missing one (`not_found`, "no such session");
  - the per-viewer cap of 4 open feeds holds for JSON feeds too, since it's the same counter.
- **JSON:** `{"error","code"}` errors; `[]` lists; types in `apitypes.go`, regenerated with `go generate ./internal/jam/userui/`.
- **Shared storage:** `localStorage["jam-thinking"]` holds `{"f":{"thinking","text","tools","errors","progress"},"tool","format"}`, exactly as `meThSave` writes it. The search is never saved.
- **Numbers:**
  - the cap is 2000 rows;
  - output is clipped at 40 lines;
  - the retry backoff is 2 s, 5 s, 15 s;
  - "at the bottom" means within 16 px;
  - the server's backfill is 300 events (`thinkingTail`) and a page is 300 (`thinkingPageSize`).
- **Lazy chunk:** the Thinking code isn't in the initial bundle. Initial JS must stay ≤ 350 KB gzipped (it's 207 KB now).
- **CSP:** no inline script. Styles come from a bundled `.css` file.
- **Docs:** they ship in the same change.
- **gofmt:** use positional struct literals in table tests (CI runs Go 1.26).

## Review Focus

1. **Agent text with HTML or script** (`<img onerror>`, `</script>`, a CR or LF inside a line) shows literally and runs nothing, from both the live feed and a page. (Task 1 Go test; Task 6 test.)
2. **A reconnect hole:**
   - the `skipped` marker appears where the hole is;
   - filling it with a page that doesn't reach its start shrinks it ("events 5-12 skipped" becomes "5-9");
   - filling it entirely removes it.
   (Task 3 test.)
3. **The cap from both ends:**
   - live events past 2000 drop the oldest rows and offer Load earlier;
   - Load earlier past 2000 drops the newest and pauses;
   - while paused, live events only count toward the pill, and the pages already loaded stay;
   - the pill reloads the live tail.
   (Task 3 test; Task 6 test.)
4. **A restart mid-view:**
   - `newstream` adds the divider;
   - the feed reconnects to the new stream without clearing;
   - the old lines stay above.
   (Task 5 test.)
5. **A refused feed:**
   - a 404 from the pane check says "This session is no longer available to you" and stops;
   - anything else retries after 2 s, 5 s and 15 s, then says "Too many Thinking views open — close one, or reload" and stops;
   - it never loops.
   (Task 5 test.)

---

## File structure

| File | Responsibility |
|---|---|
| `internal/jam/userui/apitypes.go` (modify) | `ThinkingEvent`, `ThinkingPage`, `ThinkingRaw`, `ThinkingStream`; `SessionPane.ThinkingStreams` (`streams`) and `EventsOff` (`eventsOff`) join the JSON. |
| `internal/jam/userui/thinking.go` (modify) | `thinkingEventOf`, `?format=json` on the feed, and the shared `thinkingPageOf` and `thinkingRawOf`. |
| `internal/jam/userui/sessionpane.go` (modify) | `buildPane` fills `ThinkingStreams`; `paneLists` normalizes it. |
| `internal/jam/userui/api_sessions.go`, `api_sessions_test.go` (modify) | `pane/thinking`, `thinking`, `thinking/raw`. |
| `internal/jam/userui/thinking_test.go` (modify) | The JSON feed's tests. |
| `web/app/package.json`, `package-lock.json` (modify) | `@tanstack/react-virtual`. |
| `web/app/src/session/thinking/prefs.ts`, `format.ts`, `feed.ts`, `rows.ts` + tests (new) | Pure. |
| `web/app/src/session/thinking/useThinkingFeed.ts` + test (new) | The feed connection. |
| `web/app/src/session/thinking/ThinkingTab.tsx`, `thinking.css`, `ThinkingTab.test.tsx` (new) | The tab (default export, lazily loaded). |
| `web/app/src/session/PaneTabs.tsx`, `SessionView.tsx`, `SessionView.test.tsx` (modify) | Thinking replaces its placeholder; `busy` is passed down. |
| `internal/jam/userui/next_browser_test.go` (modify) | End-to-end. |
| `docs/web-app.md`, `docs/usage/jam/intercom-ui.md`, the spec's status line (modify) | Docs. |

---

### Task 1: `ThinkingEvent` and the JSON feed

**Files:**
- Modify: `internal/jam/userui/apitypes.go`, `internal/jam/userui/thinking.go`, `internal/jam/userui/thinking_test.go`
- Modify: `web/app/src/api/types.ts` (generated)

**Interfaces:**
- Produces (Go):
  - `type ThinkingEvent struct { Stream string "stream"; Seq uint64 "seq"; Turn uint32 "turn"; At int64 "at" (unix ms); Kind string "kind"; Tool string "tool"; ToolUseID string "toolUseId"; Text string "text"; Detail string "detail"; DetailTruncated int "detailTruncated"; IsError bool "isError"; TruncatedBytes uint64 "truncatedBytes"; CostUSD float64 "costUsd"; DurationMS int64 "durationMs"; ClaudeSession string "claudeSession" }`;
  - `func thinkingEventOf(v sessionview.EventView) ThinkingEvent`.
  - `GET /app/user/session/{id}/thinking/events?format=json`: each `ev`'s data is one line of `ThinkingEvent` JSON.
- Produces (TS, generated): `ThinkingEvent` with those camelCase fields; the numbers are `number`.

- [ ] **Step 1: Write the failing test** (append to `thinking_test.go`):

```go
// ?format=json: the same feed, each ev a ThinkingEvent (agent text as JSON
// data, never markup); without format the HTML fragments are unchanged.
func TestThinkingEventsJSON(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	actor := personal.ActorID
	if err := store.Append(textEvent(actor, 1, "<img src=x onerror=alert(1)>\r\nsecond line")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asWho(Handler(e.Deps, nil), p))
	defer func() { srv.CloseClientConnections(); srv.Close() }()
	res, err := http.Get(srv.URL + "/app/user/session/" + actor + "/thinking/events?format=json")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf strings.Builder
	sseRead(t, sseLines(res.Body), &buf, "event: ev")
	var data string
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.HasPrefix(l, "data: {") {
			data = strings.TrimPrefix(l, "data: ")
		}
	}
	var ev ThinkingEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		t.Fatalf("ev data %q: %v\n%s", data, err, buf.String())
	}
	if ev.Stream != thinkStream || ev.Seq != 1 || ev.Kind != "text" || ev.At != time.Unix(1001, 0).UnixMilli() ||
		!strings.Contains(ev.Text+ev.Detail, "<img src=x onerror=alert(1)>") {
		t.Errorf("ev = %+v", ev)
	}
	if strings.Contains(buf.String(), `class="te`) {
		t.Errorf("JSON feed carries HTML fragments:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "id: "+thinkStream+":1") {
		t.Errorf("JSON feed lost its cursor:\n%s", buf.String())
	}
}

func TestThinkingEventOf(t *testing.T) {
	at := time.UnixMilli(1_700_000_000_123)
	for _, tc := range []struct {
		in   sessionview.EventView
		want ThinkingEvent
	}{
		{sessionview.EventView{StreamID: "s", Seq: 2, Turn: 1, At: at, Kind: sessionview.KindToolCall, Tool: "Bash", ToolUseID: "tu1", Narrative: "go test", Detail: "go test ./...", Raw: "{}"},
			ThinkingEvent{Stream: "s", Seq: 2, Turn: 1, At: at.UnixMilli(), Kind: "tool_call", Tool: "Bash", ToolUseID: "tu1", Text: "go test", Detail: "go test ./..."}},
		{sessionview.EventView{Kind: sessionview.KindGap, Gap: true, Summary: "3 events lost", Narrative: "ignored"},
			ThinkingEvent{Kind: "gap", Text: "3 events lost"}},
		{sessionview.EventView{Kind: sessionview.KindOther, Narrative: "system/init", Detail: "hidden"},
			ThinkingEvent{Kind: "other", Text: "system/init"}},
		{sessionview.EventView{Kind: sessionview.KindResult, CostUSD: 0.12, DurationMS: 38000, ClaudeSession: "c1", IsError: true, TruncatedBytes: 9, DetailTruncated: 4, Detail: "d"},
			ThinkingEvent{Kind: "result", CostUSD: 0.12, DurationMS: 38000, ClaudeSession: "c1", IsError: true, TruncatedBytes: 9, DetailTruncated: 4, Detail: "d"}},
	} {
		if got := thinkingEventOf(tc.in); got != tc.want {
			t.Errorf("thinkingEventOf(%+v)\n = %+v\nwant %+v", tc.in, got, tc.want)
		}
	}
}
```

`ThinkingEvent` must stay comparable (only scalar fields), so `got != tc.want` compiles. The `At` of a zero `time.Time` gives `UnixMilli()` of year 1. To keep the table literal simple, map a zero `At` to `0`.

- [ ] **Step 2: Run them to see them fail.**

Run: `cd internal/jam/userui && GOPROXY=https://proxy.golang.org go test -run 'TestThinkingEventsJSON|TestThinkingEventOf' .`

Expected: a compile error, `undefined: ThinkingEvent`.

- [ ] **Step 3: Implement.**

In `apitypes.go`:

```go
// ThinkingEvent is one session event as the Thinking tab shows it: an `ev`
// of the feed with ?format=json, and a row of a page. Every string is agent
// text, shown as text, never as markup.
type ThinkingEvent struct {
	Stream    string `json:"stream"`
	Seq       uint64 `json:"seq"`
	Turn      uint32 `json:"turn"`
	At        int64  `json:"at"`   // unix ms; 0 = unknown
	Kind      string `json:"kind"` // thinking | text | prompt | tool_call | tool_result | result | progress | gap | other
	Tool      string `json:"tool"`
	ToolUseID string `json:"toolUseId"`
	// Text is the one line (a gap's summary); Detail the expanded body ("" for
	// kind other), DetailTruncated the bytes cut from it.
	Text            string  `json:"text"`
	Detail          string  `json:"detail"`
	DetailTruncated int     `json:"detailTruncated"`
	IsError         bool    `json:"isError"`
	TruncatedBytes  uint64  `json:"truncatedBytes"` // dropped by the recorder
	CostUSD         float64 `json:"costUsd"`        // result: the claude session's cumulative cost
	DurationMS      int64   `json:"durationMs"`     // result
	ClaudeSession   string  `json:"claudeSession"`  // result: the claude session CostUSD accumulates over
}
```

In `thinking.go`:

```go
// thinkingEventOf is v as the app's Thinking tab reads it: the
// thinking-event template's fields (a gap shows its summary; kind other
// keeps no detail).
func thinkingEventOf(v sessionview.EventView) ThinkingEvent {
	ev := ThinkingEvent{Stream: v.StreamID, Seq: v.Seq, Turn: v.Turn, Kind: string(v.Kind), Tool: v.Tool, ToolUseID: v.ToolUseID,
		Text: v.Narrative, Detail: v.Detail, DetailTruncated: v.DetailTruncated, IsError: v.IsError, TruncatedBytes: v.TruncatedBytes,
		CostUSD: v.CostUSD, DurationMS: v.DurationMS, ClaudeSession: v.ClaudeSession}
	if !v.At.IsZero() {
		ev.At = v.At.UnixMilli()
	}
	if v.Gap {
		ev.Text = v.Summary
	}
	if v.Kind == sessionview.KindOther {
		ev.Detail, ev.DetailTruncated = "", 0
	}
	return ev
}

// thinkingJSON is one event as the JSON feed's ev data (one line).
func (h *handler) thinkingJSON(v sessionview.EventView) string {
	b, err := json.Marshal(thinkingEventOf(v))
	if err != nil {
		h.lg.Error("userui thinking json failed", "error", err.Error())
		return "{}"
	}
	return string(b)
}
```

In `thinkingEvents`, choose the renderer:

```go
	event := h.thinkingFragment
	if r.URL.Query().Get("format") == "json" {
		event = h.thinkingJSON // the Jam app (spec §2)
	}
	sessionview.Serve(w, r, sessionview.Source{Store: h.d.Events, Hub: h.d.Hub, ActorID: inst.ActorID,
		Tail: thinkingTail, FollowNewest: true, Event: event, Allowed: allowed})
```

Add `"encoding/json"` to the imports. `json.Marshal` escapes `<`, `>` and `&` as `<` and so on, and never emits a raw newline, so `WriteSSE` keeps the data on one line.

The test's `strings.Contains(ev.Text+ev.Detail, "<img…>")` works on the decoded string. Check which of Narrative and Detail carries the text for a text block (`sessionview.View`); the assertion accepts either.

Regenerate the types: `GOPROXY=https://proxy.golang.org go generate ./internal/jam/userui/`.

- [ ] **Step 4: Run them to see them pass.**

Run:
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/userui/`
- `./scripts/app-types-check.sh` (after `git add`)

Expected: PASS, with every old `TestThinking*` test unchanged.

- [ ] **Step 5: Commit.** Message: `jam app: the Thinking feed's JSON form`.

---

### Task 2: `pane/thinking`, `thinking` (pages) and `thinking/raw` in the API

**Files:**
- Modify: `internal/jam/userui/apitypes.go`, `thinking.go`, `sessionpane.go`, `api_sessions.go`, `api_sessions_test.go`
- Modify: `web/app/src/api/types.ts` (generated)

**Interfaces:**
- Produces (Go types):
  - `ThinkingStream { ID string "id"; FirstAt time.Time "firstAt"; Events int "events" }`;
  - `ThinkingPage { Events []ThinkingEvent "events"; Before uint64 "before" }` (`before` > 0: older events exist below it);
  - `ThinkingRaw { Raw string "raw"; Truncated int "truncated" }`;
  - `SessionPane.ThinkingStreams []ThinkingStream "streams"`;
  - `SessionPane.EventsOff` becomes `"eventsOff"`.
- Produces (routes):
  - `GET api/sessions/{id}/pane/thinking` answers `SessionPane` with `streams` (newest first) and `eventsOff`.
  - `GET api/sessions/{id}/thinking?stream=&before=` answers `ThinkingPage`. Errors:
    - 400 `bad_request` for a bad stream or before;
    - 404 `not_found` for an unseen session, or when events are off (`"session events aren't recorded on this jam"`).
  - `GET api/sessions/{id}/thinking/raw?stream=&seq=` answers `ThinkingRaw`, or 404 `not_found` "no such event".
- Produces (Go helpers):
  - `func (h *handler) thinkingPageOf(actor, stream string, before uint64) ([]sessionview.EventView, uint64, error)`;
  - `func (h *handler) thinkingRawOf(actor, stream string, seq uint64) (raw string, truncated int, found bool, err error)`.
  - The HTML `thinkingPage` and `thinkingRaw` use them.

- [ ] **Step 1: Write the failing tests** (append to `api_sessions_test.go`; add `"fmt"`, `"time"` and `"github.com/aethons-tools/cove/internal/jam/sessionevents"` to the imports as needed):

```go
func TestAPIThinking(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	actor := personal.ActorID
	old := thinkingPageSize
	thinkingPageSize = 3
	t.Cleanup(func() { thinkingPageSize = old })
	appendText(t, store, actor, 1, 5)

	pane := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", actor+"/pane/thinking", "", false))
	if pane.EventsOff || len(pane.ThinkingStreams) != 1 || pane.ThinkingStreams[0].ID != thinkStream || pane.ThinkingStreams[0].Events != 5 {
		t.Errorf("pane/thinking = %+v", pane)
	}
	pg := decodeAs[ThinkingPage](t, sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=6", "", false))
	if len(pg.Events) != 3 || pg.Events[0].Seq != 3 || pg.Events[2].Seq != 5 || pg.Before != 3 || pg.Events[0].Kind != "text" {
		t.Errorf("page below 6 = %+v", pg)
	}
	last := decodeAs[ThinkingPage](t, sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=3", "", false))
	if len(last.Events) != 2 || last.Before != 0 {
		t.Errorf("first page = %+v", last)
	}
	if rec := sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=1", "", false); !strings.Contains(rec.Body.String(), `"events":[]`) {
		t.Errorf("empty page = %s", rec.Body)
	}
	raw := decodeAs[ThinkingRaw](t, sessAPI(t, e, p, "GET", actor+"/thinking/raw?stream="+thinkStream+"&seq=2", "", false))
	if !strings.Contains(raw.Raw, "said 2") || raw.Truncated != 0 {
		t.Errorf("raw = %+v", raw)
	}
	for _, tc := range []struct {
		path string
		code int
		want string
	}{
		{"/thinking?stream=bogus&before=3", 400, `"code":"bad_request"`},
		{"/thinking?stream=" + thinkStream + "&before=0", 400, `"code":"bad_request"`},
		{"/thinking/raw?stream=" + thinkStream + "&seq=99", 404, "no such event"},
		{"/thinking/raw?stream=" + thinkStream, 400, `"code":"bad_request"`},
	} {
		if rec := sessAPI(t, e, p, "GET", actor+tc.path, "", false); rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s = %d %s, want %d %s", tc.path, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
	// No events store: the pane says so; the page and raw are 404.
	e.Deps.Events, e.Deps.Hub = nil, nil
	if off := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", actor+"/pane/thinking", "", false)); !off.EventsOff || off.ThinkingStreams == nil {
		t.Errorf("events off: pane = %+v", off)
	}
	if rec := sessAPI(t, e, p, "GET", actor+"/thinking?stream="+thinkStream+"&before=3", "", false); rec.Code != 404 || !strings.Contains(rec.Body.String(), "aren't recorded") {
		t.Errorf("events off: page = %d %s", rec.Code, rec.Body)
	}
}

func TestAPIThinkingRawCapped(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	big := strings.Repeat("x", rawMax+10)
	if err := store.Append(textEvent(personal.ActorID, 1, big)); err != nil {
		t.Fatal(err)
	}
	raw := decodeAs[ThinkingRaw](t, sessAPI(t, e, p, "GET", personal.ActorID+"/thinking/raw?stream="+thinkStream+"&seq=1", "", false))
	if len(raw.Raw) != rawMax || raw.Truncated <= 0 {
		t.Errorf("raw len %d, truncated %d", len(raw.Raw), raw.Truncated)
	}
}
```

Extend `TestAPISessionHiddenIsUniform`'s table with:

```go
		{"GET", "/pane/thinking", ""},
		{"GET", "/thinking?stream=" + strings.Repeat("a", 32) + "&before=3", ""},
		{"GET", "/thinking/raw?stream=" + strings.Repeat("a", 32) + "&seq=1", ""},
```

In `TestAPISessionPane`, the "slice-6 field" check becomes the Studio fields only: `[]string{"Streams", "studio", "ideOpen", "IDEOpen"}`, because `streams` and `eventsOff` are now part of the JSON. Also add `"streams":[]` to the "every list is []" keys.

- [ ] **Step 2: Run them to see them fail.**

Run: `GOPROXY=https://proxy.golang.org go test -run 'TestAPIThinking|TestAPISession' ./internal/jam/userui/`

Expected: a compile error, `undefined: ThinkingPage`.

- [ ] **Step 3: Implement.**

`apitypes.go`:

```go
// ThinkingStream is one recorded event stream of a session (the Thinking
// tab's stream selector), newest first.
type ThinkingStream struct {
	ID      string    `json:"id"`
	FirstAt time.Time `json:"firstAt"`
	Events  int       `json:"events"`
}

// ThinkingPage is GET api/sessions/{id}/thinking: a stream's events below
// ?before=, in seq order; Before > 0: older ones exist below it.
type ThinkingPage struct {
	Events []ThinkingEvent `json:"events"`
	Before uint64          `json:"before"`
}

// ThinkingRaw is GET api/sessions/{id}/thinking/raw: one event's pretty JSON,
// capped; Truncated is the bytes cut.
type ThinkingRaw struct {
	Raw       string `json:"raw"`
	Truncated int    `json:"truncated"`
}
```

In `SessionPane`:
- replace the `Streams … json:"-"` / `EventsOff … json:"-"` comment block with `Streams` kept as `json:"-"` (the template's);
- add `ThinkingStreams []ThinkingStream `json:"streams"`` (the app's);
- change `EventsOff bool `json:"eventsOff"``.

The comment reads: "Streams are the session's recorded event streams (the template's); ThinkingStreams the same for the app; EventsOff: this jam records no session events."

`sessionpane.go`:
- In `buildPane`'s `"thinking"` case, after `pane.Streams, _ = …`, add:

```go
			for _, s := range pane.Streams {
				pane.ThinkingStreams = append(pane.ThinkingStreams, ThinkingStream{ID: s.StreamID, FirstAt: s.FirstAt, Events: s.Events})
			}
```

- In `paneLists`, add `if p.ThinkingStreams == nil { p.ThinkingStreams = []ThinkingStream{} }`.
- Add `"thinking"` to `apiPaneTabs` in `api_sessions.go`.

`thinking.go`: factor out the lookups.

```go
// thinkingPageOf is the thinkingPageSize events of actor's stream below
// before, in seq order, and the seq below which older ones exist (0 = none).
func (h *handler) thinkingPageOf(actor, stream string, before uint64) ([]sessionview.EventView, uint64, error) {
	evs, err := h.d.Events.List(sessionevents.Filter{ActorID: actor, StreamID: stream, BeforeSeq: before, Newest: true, Limit: thinkingPageSize})
	if err != nil {
		return nil, 0, err
	}
	out := make([]sessionview.EventView, 0, len(evs))
	for _, ev := range evs {
		out = append(out, sessionview.View(ev))
	}
	var below uint64
	if len(evs) > 0 && evs[0].Seq > 1 {
		below = evs[0].Seq
	}
	return out, below, nil
}

// thinkingRawOf is one event's pretty JSON, capped at rawMax (truncated:
// the bytes cut); found false when the stream has no such seq.
func (h *handler) thinkingRawOf(actor, stream string, seq uint64) (string, int, bool, error) {
	evs, err := h.d.Events.List(sessionevents.Filter{ActorID: actor, StreamID: stream, AfterSeq: seq - 1, Limit: 1})
	if err != nil || len(evs) == 0 || evs[0].Seq != seq {
		return "", 0, false, err
	}
	raw := sessionview.View(evs[0]).Raw
	if len(raw) > rawMax {
		return raw[:rawMax], len(raw) - rawMax, true, nil
	}
	return raw, 0, true, nil
}
```

`thinkingPage` (HTML) becomes:

```go
	evs, below, err := h.thinkingPageOf(inst.ActorID, stream, before)
	if err != nil {
		h.lg.Error("thinking page: listing events failed", "session", inst.ActorID, "error", err.Error())
		http.Error(w, "listing events failed; see the jam log", http.StatusInternalServerError)
		return
	}
	page := inboxPage{PaneEvents: evs, PaneBefore: below}
	// … render as before
```

`thinkingRaw` (HTML) becomes:

```go
	raw, cut, found, err := h.thinkingRawOf(inst.ActorID, stream, seq)
	if err != nil {
		h.lg.Error("thinking raw: listing events failed", "session", inst.ActorID, "error", err.Error())
		http.Error(w, "listing events failed; see the jam log", http.StatusInternalServerError)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, raw)
	if cut > 0 {
		_, _ = fmt.Fprintf(w, "\n… truncated %d bytes", cut)
	}
```

`api_sessions.go`:

```go
	mux.HandleFunc("GET /app/user/api/sessions/{id}/thinking", h.apiThinkingPage)
	mux.HandleFunc("GET /app/user/api/sessions/{id}/thinking/raw", h.apiThinkingRaw)
```

```go
// openThinking is openSession for the Thinking routes: 404 when this jam
// records no session events, else the session and the request's ?stream=.
func (h *handler) openThinking(w http.ResponseWriter, r *http.Request) (jam.Instance, string, bool) {
	_, inst, _, ok := h.openSession(w, r)
	if !ok {
		return inst, "", false
	}
	if h.d.Events == nil {
		writeAPIError(w, http.StatusNotFound, "not_found", "session events aren't recorded on this jam")
		return inst, "", false
	}
	s := r.URL.Query().Get("stream")
	if !sessionevents.ValidStreamID(s) {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "stream: not a stream id")
		return inst, "", false
	}
	return inst, s, true
}

// positive is the request's query parameter name as a positive integer.
func positive(w http.ResponseWriter, r *http.Request, name string) (uint64, bool) {
	n, err := strconv.ParseUint(r.URL.Query().Get(name), 10, 64)
	if err != nil || n == 0 {
		writeAPIError(w, http.StatusBadRequest, "bad_request", name+": not a positive number")
		return 0, false
	}
	return n, true
}

func (h *handler) apiThinkingPage(w http.ResponseWriter, r *http.Request) {
	inst, stream, ok := h.openThinking(w, r)
	if !ok {
		return
	}
	before, ok := positive(w, r, "before")
	if !ok {
		return
	}
	evs, below, err := h.thinkingPageOf(inst.ActorID, stream, before)
	if err != nil {
		h.lg.Error("api thinking page: listing events failed", "session", inst.ActorID, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "listing events failed")
		return
	}
	page := ThinkingPage{Events: make([]ThinkingEvent, 0, len(evs)), Before: below}
	for _, v := range evs {
		page.Events = append(page.Events, thinkingEventOf(v))
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *handler) apiThinkingRaw(w http.ResponseWriter, r *http.Request) {
	inst, stream, ok := h.openThinking(w, r)
	if !ok {
		return
	}
	seq, ok := positive(w, r, "seq")
	if !ok {
		return
	}
	raw, cut, found, err := h.thinkingRawOf(inst.ActorID, stream, seq)
	switch {
	case err != nil:
		h.lg.Error("api thinking raw: listing events failed", "session", inst.ActorID, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "listing events failed")
	case !found:
		writeAPIError(w, http.StatusNotFound, "not_found", "no such event")
	default:
		writeJSON(w, http.StatusOK, ThinkingRaw{Raw: raw, Truncated: cut})
	}
}
```

Notes:
- The uniform-hidden rows send a valid stream, so the visibility check (in `openSession`, which runs first) answers before the stream check. The hidden and missing bodies are then both "no such session".
- `apiThinkingPage` runs `openThinking` before `positive`, for the same reason.
- `openThinking` runs `openSession` first, so an unseen session never learns whether events are on.
- Imports: `strconv` and `sessionevents`.

Regenerate the types.

- [ ] **Step 4: Run them to see them pass.**

Run:
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/userui/`
- `cd web/app && npm run typecheck`

Expected: PASS. The old `TestThinkingPage` and `TestThinkingRawCapped` pass unchanged.

- [ ] **Step 5: Commit.** Message: `jam app API: Thinking pages, raw, and the stream list`.

---

### Task 3: The pure modules (prefs, format, the feed reducer)

**Files:**
- Create: `web/app/src/session/thinking/prefs.ts`, `format.ts`, `feed.ts`, `prefs.test.ts`, `feed.test.ts`

**Interfaces:**
- Produces (`prefs.ts`):
  - `type Format = "narrative" | "detailed" | "raw"`;
  - `interface ThinkingPrefs { f: { thinking: boolean; text: boolean; tools: boolean; errors: boolean; progress: boolean }; tool: string; format: Format }`;
  - `defaultPrefs(): ThinkingPrefs`, `loadPrefs(): ThinkingPrefs`, `savePrefs(p: ThinkingPrefs): void`.
- Produces (`format.ts`):
  - `fmtDuration(ms: number): string`, giving `850ms`, `2.1s`, `1m 4s`;
  - `usd(v: number): string`, giving `$1,234.50`;
  - `clock(ms: number): string`, local `HH:MM:SS`, `""` for 0;
  - `num(n: number): string`, giving `12,345`.
- Produces (`feed.ts`):
  - `type Item = { t: "ev"; ev: ThinkingEvent } | { t: "restart"; key: string } | { t: "skip"; key: string; stream: string; from: number; to: number }`;
  - `interface Feed { items: Item[]; more: number; stream: string; paused: boolean; unseen: number; note: string }`;
  - `emptyFeed: Feed`;
  - `type FeedAction`:
    - `{ type: "reset" }`, `{ type: "stream"; stream: string }`;
    - `{ type: "live"; ev: ThinkingEvent; following: boolean }`;
    - `{ type: "more"; before: number }`, `{ type: "skipped"; from: number; to: number }`;
    - `{ type: "restart" }`;
    - `{ type: "page"; events: ThinkingEvent[]; at: string | null; from: number; before: number }`;
    - `{ type: "seen" }`, `{ type: "note"; text: string }`;
  - `feedReducer(max: number): (s: Feed, a: FeedAction) => Feed`;
  - `itemKey(i: Item): string` (`"<stream>:<seq>"` for an event).

- [ ] **Step 1: Write the failing tests.**

`prefs.test.ts`:

```ts
import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultPrefs, loadPrefs, savePrefs } from "./prefs";

afterEach(() => { localStorage.clear(); vi.restoreAllMocks(); });

describe("thinking prefs", () => {
  it("defaults, and reads what the old Inbox wrote, keeping only valid parts", () => {
    expect(loadPrefs()).toEqual(defaultPrefs());
    localStorage.setItem("jam-thinking", JSON.stringify({ f: { thinking: false, errors: true, text: "yes" }, tool: "Bash", format: "raw" }));
    expect(loadPrefs()).toEqual({ f: { thinking: false, text: true, tools: true, errors: true, progress: false }, tool: "Bash", format: "raw" });
    localStorage.setItem("jam-thinking", JSON.stringify({ format: "fancy", tool: 3 }));
    expect(loadPrefs()).toEqual(defaultPrefs());
    localStorage.setItem("jam-thinking", "{nope");
    expect(loadPrefs()).toEqual(defaultPrefs());
  });

  it("saves the old Inbox's shape and survives a storage that throws", () => {
    const p = { ...defaultPrefs(), tool: "Edit" };
    savePrefs(p);
    expect(JSON.parse(localStorage.getItem("jam-thinking")!)).toEqual({ f: p.f, tool: "Edit", format: "narrative" });
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("full"); });
    expect(() => savePrefs(p)).not.toThrow();
  });
});
```

`feed.test.ts`:

```ts
import { describe, expect, it } from "vitest";
import type { ThinkingEvent } from "../../api/types";
import { emptyFeed, feedReducer, type Feed, type FeedAction } from "./feed";

export const tev = (seq: number, over: Partial<ThinkingEvent> = {}): ThinkingEvent => ({
  stream: "s1", seq, turn: 1, at: 1000 * seq, kind: "text", tool: "", toolUseId: "", text: `said ${seq}`, detail: "",
  detailTruncated: 0, isError: false, truncatedBytes: 0, costUsd: 0, durationMs: 0, claudeSession: "", ...over,
});
const run = (max: number, ...as: FeedAction[]): Feed => as.reduce(feedReducer(max), emptyFeed);
const seqs = (f: Feed) => f.items.map((i) => (i.t === "ev" ? i.ev.seq : i.t));
const live = (seq: number, following = true): FeedAction => ({ type: "live", ev: tev(seq), following });

describe("the Thinking feed", () => {
  it("appends live events once each, counting the unseen when not following", () => {
    const f = run(10, { type: "stream", stream: "s1" }, live(1), live(2), live(2), live(3, false));
    expect(seqs(f)).toEqual([1, 2, 3]);
    expect(f.stream).toBe("s1");
    expect(f.unseen).toBe(1);
    expect(run(10, live(1, false), { type: "seen" }).unseen).toBe(0);
  });

  it("caps live events at max, dropping the oldest and offering Load earlier", () => {
    const f = run(3, live(1), live(2), live(3), live(4), live(5));
    expect(seqs(f)).toEqual([3, 4, 5]);
    expect(f.more).toBe(3);
  });

  it("inserts a Load earlier page above, then trims the newest past max and pauses", () => {
    let f = run(4, live(5), live(6), { type: "more", before: 5 });
    f = feedReducer(4)(f, { type: "page", events: [tev(3), tev(4)], at: null, from: 0, before: 3 });
    expect(seqs(f)).toEqual([3, 4, 5, 6]);
    expect(f.more).toBe(3);
    expect(f.paused).toBe(false);
    f = feedReducer(4)(f, { type: "page", events: [tev(1), tev(2)], at: null, from: 0, before: 0 });
    expect(seqs(f)).toEqual([1, 2, 3, 4]); // 5 and 6 trimmed
    expect(f.more).toBe(0);
    expect(f.paused).toBe(true);
    expect(f.unseen).toBe(2);
    // Paused: live events only count; the loaded pages stay.
    f = feedReducer(4)(f, live(7));
    expect(seqs(f)).toEqual([1, 2, 3, 4]);
    expect(f.unseen).toBe(3);
    expect(feedReducer(4)(f, { type: "seen" }).unseen).toBe(3); // seen doesn't clear a pause's count
  });

  it("marks a reconnect's hole, and fills it from below: shrinking, then gone", () => {
    let f = run(100, live(1), live(2), { type: "skipped", from: 3, to: 9 }, live(10));
    expect(seqs(f)).toEqual([1, 2, "skip", 10]);
    const key = f.items[2]!.t === "skip" ? f.items[2]!.key : "";
    // A page below 10 reaching down to 7 (older remain below 7).
    f = feedReducer(100)(f, { type: "page", events: [tev(6), tev(7), tev(8), tev(9)], at: key, from: 3, before: 6 });
    expect(seqs(f)).toEqual([1, 2, "skip", 6, 7, 8, 9, 10]);
    expect(f.items[2]).toMatchObject({ t: "skip", from: 3, to: 5 });
    f = feedReducer(100)(f, { type: "page", events: [tev(2), tev(3), tev(4), tev(5)], at: key, from: 3, before: 2 });
    expect(seqs(f)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10]); // 2 deduped; the marker gone
  });

  it("adds a restart divider in place, and keeps the lines above", () => {
    const f = run(100, live(1), { type: "restart" }, { type: "stream", stream: "s2" }, { type: "live", ev: tev(1, { stream: "s2" }), following: true });
    expect(seqs(f)).toEqual([1, "restart", 1]);
    expect(f.stream).toBe("s2");
  });

  it("resets, and carries a note", () => {
    expect(run(10, live(1), { type: "reset" })).toEqual(emptyFeed);
    expect(run(10, { type: "note", text: "gone" }).note).toBe("gone");
  });
});
```

- [ ] **Step 2: Run them to see them fail.**

Run: `cd web/app && npx vitest run src/session/thinking`

Expected: FAIL, "Failed to resolve import".

- [ ] **Step 3: Implement.**

`prefs.ts`:

```ts
// The Thinking tab's preferences, stored as the old Inbox stores them
// (layout.html's meThPrefs/meThSave): localStorage "jam-thinking" holds
// {f, tool, format}; the search is never saved.
export type Format = "narrative" | "detailed" | "raw";
export const FORMATS: Format[] = ["narrative", "detailed", "raw"];
export type Chip = "thinking" | "text" | "tools" | "errors" | "progress";
export interface ThinkingPrefs { f: Record<Chip, boolean>; tool: string; format: Format }

export const defaultPrefs = (): ThinkingPrefs => ({
  f: { thinking: true, text: true, tools: true, errors: false, progress: false }, tool: "", format: "narrative",
});

export function loadPrefs(): ThinkingPrefs {
  const d = defaultPrefs();
  try {
    const v = JSON.parse(localStorage.getItem("jam-thinking") ?? "null") as unknown;
    if (!v || typeof v !== "object") return d;
    const o = v as { f?: Record<string, unknown>; tool?: unknown; format?: unknown };
    for (const k of Object.keys(d.f) as Chip[]) {
      if (o.f && typeof o.f[k] === "boolean") d.f[k] = o.f[k];
    }
    if (typeof o.tool === "string") d.tool = o.tool;
    if (FORMATS.includes(o.format as Format)) d.format = o.format as Format;
  } catch { /* unreadable: the defaults */ }
  return d;
}

export function savePrefs(p: ThinkingPrefs): void {
  try {
    localStorage.setItem("jam-thinking", JSON.stringify({ f: p.f, tool: p.tool, format: p.format }));
  } catch { /* storage full or denied */ }
}
```

`format.ts`:

```ts
// fmtDuration renders a duration: 850ms, 2.1s, 1m 4s (the old meThFmt).
export function fmtDuration(ms: number): string {
  const v = Math.max(0, Math.round(ms) || 0);
  if (v < 1000) return `${v}ms`;
  if (v < 60000) return `${(v / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  return `${Math.floor(v / 60000)}m ${Math.floor((v % 60000) / 1000)}s`;
}
export const usd = (v: number) => "$" + v.toLocaleString("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
export const num = (n: number) => n.toLocaleString("en-US");
// clock is an event's local time of day (HH:MM:SS); "" when unknown.
export const clock = (ms: number) =>
  ms ? new Date(ms).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }) : "";
```

`feed.ts`:

```ts
import type { ThinkingEvent } from "../../api/types";

// The Thinking list's items, in display order: events, the dividers a
// restart leaves, and the markers a reconnect's hole leaves. The reducer
// owns the 2000-row cap (max): live events past it drop the oldest (Load
// earlier offers them again); a loaded page past it drops the newest and
// pauses following, so the pages asked for stay (the pill reloads the tail).
export type Item =
  | { t: "ev"; ev: ThinkingEvent }
  | { t: "restart"; key: string }
  | { t: "skip"; key: string; stream: string; from: number; to: number };

export interface Feed {
  items: Item[];
  more: number; // > 0: Load earlier fetches below this seq
  stream: string; // the stream being followed
  paused: boolean; // the newest end was trimmed: live events only count
  unseen: number; // the "↓ N new" pill
  note: string; // why the feed stopped ("" = it hasn't)
}

export type FeedAction =
  | { type: "reset" }
  | { type: "stream"; stream: string }
  | { type: "live"; ev: ThinkingEvent; following: boolean }
  | { type: "more"; before: number }
  | { type: "skipped"; from: number; to: number }
  | { type: "restart" }
  // A page of older events: inserted above the first event (at null: Load
  // earlier) or above the skip marker keyed at (filling its hole), keeping
  // seq >= from; before is the page's own boundary (0 = nothing older).
  | { type: "page"; events: ThinkingEvent[]; at: string | null; from: number; before: number }
  | { type: "seen" }
  | { type: "note"; text: string };

export const emptyFeed: Feed = { items: [], more: 0, stream: "", paused: false, unseen: 0, note: "" };

export const evKey = (ev: ThinkingEvent) => `${ev.stream}:${ev.seq}`;
export const itemKey = (i: Item) => (i.t === "ev" ? evKey(i.ev) : i.key);

let markers = 0;
const marker = (kind: string) => `${kind}:${++markers}`;

const evCount = (items: Item[]) => items.reduce((n, i) => n + (i.t === "ev" ? 1 : 0), 0);

export function feedReducer(max: number) {
  return (s: Feed, a: FeedAction): Feed => {
    switch (a.type) {
      case "reset":
        return emptyFeed;
      case "stream":
        return { ...s, stream: a.stream };
      case "note":
        return { ...s, note: a.text };
      case "seen":
        return s.paused ? s : { ...s, unseen: 0 };
      case "more":
        return { ...s, more: a.before };
      case "restart":
        return { ...s, items: [...s.items, { t: "restart", key: marker("restart") }] };
      case "skipped":
        return { ...s, items: [...s.items, { t: "skip", key: marker("skip"), stream: s.stream, from: a.from, to: a.to }] };
      case "live": {
        if (s.paused) return { ...s, unseen: s.unseen + 1 };
        const k = evKey(a.ev);
        if (s.items.some((i) => itemKey(i) === k)) return s;
        let items: Item[] = [...s.items, { t: "ev", ev: a.ev }];
        let more = s.more;
        let over = evCount(items) - max;
        if (over > 0) {
          items = items.filter((i) => i.t !== "ev" || over-- <= 0);
          const first = items.find((i) => i.t === "ev");
          if (first?.t === "ev") more = first.ev.seq;
        }
        return { ...s, items, more, unseen: a.following ? s.unseen : s.unseen + 1 };
      }
      case "page": {
        const have = new Set(s.items.map(itemKey));
        const add: Item[] = a.events.filter((e) => e.seq >= a.from && !have.has(evKey(e))).map((ev) => ({ t: "ev", ev }));
        let items = [...s.items];
        let more = s.more;
        if (a.at === null) {
          const i = items.findIndex((x) => x.t === "ev");
          items.splice(i < 0 ? items.length : i, 0, ...add);
          more = a.before;
        } else {
          const i = items.findIndex((x) => x.t === "skip" && x.key === a.at);
          if (i < 0) return s; // the marker went (a reset): drop the page
          const m = items[i] as Extract<Item, { t: "skip" }>;
          items.splice(i + 1, 0, ...add);
          if (a.before > m.from) items[i] = { ...m, to: a.before - 1 };
          else items.splice(i, 1);
        }
        let paused = s.paused, unseen = s.unseen;
        const over = evCount(items) - max;
        if (over > 0) {
          let n = over;
          for (let j = items.length - 1; j >= 0 && n > 0; j--) {
            if (items[j]!.t === "ev") { items.splice(j, 1); n--; }
          }
          paused = true;
          unseen += over;
        }
        return { ...s, items, more, paused, unseen };
      }
    }
  };
}
```

Notes:
- **The hole-filling page:** a page fetched for a marker comes back with events below `to + 1`, and they're inserted **after** the marker. The marker always sits above the events it is missing; the old `meThSkipped` inserted before the marker element, but there the marker was where the hole was, and the page's lines went before it, above the newer lines. The test fixes the order: `[1, 2, "skip", 6, 7, 8, 9, 10]` while the hole is 3–5. In other words, what's still missing is shown above what was filled. When the page reaches `from`, the marker is removed.
- **The duplicate in the second hole page:** event 2 was already loaded, so `have` drops it.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run src/session/thinking && npm run typecheck && npm run lint`

Expected: PASS.

- [ ] **Step 5: Commit.** Message: `jam app: Thinking prefs, formats and the feed reducer`.

---

### Task 4: The display rows (pairing, costs, runs, filters)

**Files:**
- Create: `web/app/src/session/thinking/rows.ts`, `web/app/src/session/thinking/rows.test.ts`

**Interfaces:**
- Consumes: `Item`, `itemKey` (feed.ts); `ThinkingPrefs` (prefs.ts); `fmtDuration`, `usd` (format.ts).
- Produces:
  - `interface Row { key: string; kind: string; ev?: ThinkingEvent; icon: string; text: string; status: string; err: boolean; output: string; outputTruncated: number; empty: boolean; marker?: "restart" | "skip"; skip?: { from: number; to: number } }`;
  - `deriveRows(items: Item[], prefs: ThinkingPrefs, q: string): Row[]`, the visible rows in order;
  - `toolsIn(items: Item[]): string[]`, the tool names seen, in first-seen order;
  - `ICONS: Record<string, string>`.

- [ ] **Step 1: Write the failing tests** (`rows.test.ts`):

```ts
import { describe, expect, it } from "vitest";
import type { Item } from "./feed";
import { defaultPrefs, type ThinkingPrefs } from "./prefs";
import { deriveRows, toolsIn } from "./rows";
import { tev } from "./feed.test";

const evs = (...e: ReturnType<typeof tev>[]): Item[] => e.map((ev) => ({ t: "ev", ev }));
const prefs = (over: Partial<ThinkingPrefs> & { f?: Partial<ThinkingPrefs["f"]> } = {}): ThinkingPrefs => {
  const d = defaultPrefs();
  return { ...d, ...over, f: { ...d.f, ...over.f } };
};

describe("the Thinking rows", () => {
  const call = tev(2, { kind: "tool_call", tool: "Bash", toolUseId: "tu1", text: "go test", at: 1000 });
  const res = tev(3, { kind: "tool_result", tool: "Bash", toolUseId: "tu1", text: "ok", detail: Array.from({ length: 45 }, (_, i) => `l${i}`).join("\n"), at: 4100 });

  it("folds a tool result into its call: status, time, output", () => {
    const rows = deriveRows(evs(tev(1), call, res), prefs(), "");
    expect(rows.map((r) => r.kind)).toEqual(["text", "tool_call"]);
    expect(rows[1]).toMatchObject({ icon: "▶", status: "✓ 3.1s", err: false, text: "go test" });
    expect(rows[1]!.output.split("\n")).toHaveLength(45);
    const fail = deriveRows(evs(call, tev(3, { kind: "tool_result", toolUseId: "tu1", isError: true, at: 1500 })), prefs(), "");
    expect(fail[0]).toMatchObject({ status: "✗ 500ms", err: true });
    expect(deriveRows(evs(call), prefs(), "")[0]!.status).toBe("…");
  });

  it("pairs a result that arrived before its call (an older page)", () => {
    const rows = deriveRows(evs(res, call), prefs(), "");
    expect(rows.map((r) => r.kind)).toEqual(["tool_call"]);
    expect(rows[0]!.status).toMatch(/^✓/);
  });

  it("labels results as episode dividers: the cost since the session's previous result, else its total", () => {
    const r1 = tev(4, { kind: "result", turn: 1, costUsd: 0.12, durationMs: 38000, claudeSession: "c1" });
    const r2 = tev(9, { kind: "result", turn: 2, costUsd: 0.5, durationMs: 1200, claudeSession: "c1" });
    const r3 = tev(12, { kind: "result", turn: 3, costUsd: 0.05, durationMs: 900, claudeSession: "c1" }); // a cleared session: lower
    const rows = deriveRows(evs(r1, r2, r3), prefs(), "");
    expect(rows.map((r) => r.text)).toEqual([
      "episode 1 · $0.12 session total · 38s", "episode 2 · $0.38 · 1.2s", "episode 3 · $0.05 session total · 900ms"]);
  });

  it("collapses a run of empty thinking into its first; hidden progress doesn't break it", () => {
    const think = (seq: number) => tev(seq, { kind: "thinking", text: " " });
    const items = evs(think(1), tev(2, { kind: "progress", text: "…" }), think(3), think(4), tev(5), think(6));
    const rows = deriveRows(items, prefs(), "");
    expect(rows.map((r) => [r.kind, r.text])).toEqual([["thinking", "thinking ×3"], ["text", "said 5"], ["thinking", "thinking"]]);
    expect(rows[0]!.empty).toBe(true);
    // With Progress shown, the progress line splits the run.
    expect(deriveRows(items, prefs({ f: { progress: true } }), "").map((r) => r.text)).toEqual(["thinking", "…", "thinking ×2", "said 5", "thinking"]);
  });

  it("filters by chip, errors, tool and search; gaps and markers always show", () => {
    const items: Item[] = [...evs(tev(1, { kind: "thinking", text: "hmm" }), tev(2, { kind: "prompt", text: "do it" }), call, res,
      tev(5, { kind: "tool_call", tool: "Edit", toolUseId: "tu2", text: "edit x" }), tev(6, { kind: "tool_result", toolUseId: "tu2", isError: true }),
      tev(7, { kind: "gap", text: "3 events lost" })), { t: "skip", key: "skip:1", stream: "s1", from: 8, to: 9 }];
    const kinds = (p: ThinkingPrefs, q = "") => deriveRows(items, p, q).map((r) => r.marker ?? r.text);
    expect(kinds(prefs({ f: { thinking: false } }))).toEqual(["do it", "go test", "edit x", "3 events lost", "skip"]);
    expect(kinds(prefs({ f: { text: false } }))).toEqual(["hmm", "go test", "edit x", "3 events lost", "skip"]);
    expect(kinds(prefs({ f: { tools: false } }))).toEqual(["hmm", "do it", "3 events lost", "skip"]);
    expect(kinds(prefs({ f: { errors: true } }))).toEqual(["hmm", "do it", "edit x", "3 events lost", "skip"]);
    expect(kinds(prefs({ tool: "Edit" }))).toEqual(["edit x", "3 events lost", "skip"]);
    expect(kinds(prefs(), "l44")).toEqual(["go test", "3 events lost", "skip"]); // the folded output is searched
    expect(toolsIn(items)).toEqual(["Bash", "Edit"]);
  });
});
```

`feed.test.ts` exports `tev` for this file. If ESLint or Vitest objects to importing from a test file, move `tev` into `src/session/thinking/testing.ts` and import it in both.

- [ ] **Step 2: Run them to see them fail.**

Run: `npx vitest run src/session/thinking/rows.test.ts`

Expected: FAIL, "Failed to resolve import ./rows".

- [ ] **Step 3: Implement** `rows.ts`:

```ts
import type { ThinkingEvent } from "../../api/types";
import { itemKey, type Item } from "./feed";
import { fmtDuration, usd } from "./format";
import type { ThinkingPrefs } from "./prefs";

export const ICONS: Record<string, string> = {
  thinking: "💭", text: "💬", prompt: "→", tool_call: "▶", tool_result: "◀", result: "─", gap: "⚠", progress: "·", other: "·",
};

// Row is one line of the list as shown.
export interface Row {
  key: string;
  kind: string;
  ev?: ThinkingEvent;
  icon: string;
  text: string;
  status: string; // a tool call's "…" / "✓ 3.1s" / "✗ 500ms"
  err: boolean;
  output: string; // a tool call's folded result
  outputTruncated: number;
  empty: boolean; // a thinking block with only its signature
  marker?: "restart" | "skip";
  skip?: { from: number; to: number };
}

const TOOLS = new Set(["tool_call", "tool_result"]);

// deriveRows is items as shown (layout.html's meThPair, meThCosts, meThRuns
// and the filter classes, recomputed from scratch): a tool result folds into
// its call (wherever either sits); results become episode dividers; a run of
// empty thinking collapses into its first; then the chips, Errors only, the
// tool and the search hide rows (gaps and markers always show).
export function deriveRows(items: Item[], p: ThinkingPrefs, q: string): Row[] {
  const rows: Row[] = items.map((i): Row => {
    if (i.t !== "ev") {
      return { key: i.key, kind: i.t, icon: "", text: "", status: "", err: false, output: "", outputTruncated: 0, empty: false,
        marker: i.t, skip: i.t === "skip" ? { from: i.from, to: i.to } : undefined };
    }
    const e = i.ev;
    const empty = e.kind === "thinking" && !e.text.trim();
    return { key: itemKey(i), kind: e.kind, ev: e, icon: ICONS[e.kind] ?? "·", text: empty ? "thinking" : e.text,
      status: e.kind === "tool_call" ? "…" : "", err: e.isError, output: "", outputTruncated: 0, empty };
  });
  // Pair: each call takes the first result with its tool-use id.
  const calls = new Map<string, Row>();
  for (const r of rows) if (r.kind === "tool_call" && r.ev?.toolUseId && !calls.has(r.ev.toolUseId)) calls.set(r.ev.toolUseId, r);
  const folded = new Set<string>();
  const paired = new Set<Row>();
  for (const r of rows) {
    if (r.kind !== "tool_result" || !r.ev?.toolUseId) continue;
    const c = calls.get(r.ev.toolUseId);
    if (!c || paired.has(c)) continue;
    paired.add(c);
    folded.add(r.key);
    c.status = `${r.ev.isError ? "✗" : "✓"} ${fmtDuration(r.ev.at - c.ev!.at)}`;
    c.err = c.err || r.ev.isError;
    c.output = r.ev.detail;
    c.outputTruncated = r.ev.detailTruncated;
  }
  const shown = rows.filter((r) => !folded.has(r.key));
  // Costs: each result against the previous result of its claude session.
  const prev = new Map<string, number>();
  for (const r of shown) {
    if (r.kind !== "result" || !r.ev) continue;
    const e = r.ev, k = e.claudeSession || `${e.stream}:${e.turn}`, before = prev.get(k);
    prev.set(k, e.costUsd);
    const cost = before === undefined || e.costUsd < before ? `${usd(e.costUsd)} session total` : usd(e.costUsd - before);
    r.text = `episode ${e.turn} · ${cost} · ${fmtDuration(e.durationMs)}`;
  }
  // Runs of empty thinking (same stream); hidden progress doesn't break one.
  const absorbed = new Set<Row>();
  let head: Row | null = null, n = 0;
  for (const r of shown) {
    if (!p.f.progress && r.kind === "progress") continue;
    if (!r.empty) { head = null; continue; }
    if (head && head.ev!.stream === r.ev!.stream) {
      n++;
      absorbed.add(r);
      head.text = `thinking ×${n}`;
      continue;
    }
    head = r;
    n = 1;
  }
  const needle = q.trim().toLowerCase();
  return shown.filter((r) => {
    if (r.marker) return true;
    if (absorbed.has(r)) return false;
    const k = r.kind;
    if (k === "thinking" && !p.f.thinking) return false;
    if ((k === "text" || k === "prompt") && !p.f.text) return false;
    if (TOOLS.has(k) && !p.f.tools) return false;
    if (k === "progress" && !p.f.progress) return false;
    if (p.f.errors && TOOLS.has(k) && !r.err) return false;
    if (k === "gap") return true;
    if (p.tool && r.ev!.tool !== p.tool) return false;
    if (needle && !`${r.ev!.tool} ${r.text} ${r.ev!.detail} ${r.output}`.toLowerCase().includes(needle)) return false;
    return true;
  });
}

export function toolsIn(items: Item[]): string[] {
  const out: string[] = [];
  for (const i of items) if (i.t === "ev" && i.ev.tool && !out.includes(i.ev.tool)) out.push(i.ev.tool);
  return out;
}
```

One check on the "search" case: `l44` appears only in Bash's folded output, so the search keeps "go test" and drops the rest, except the gap and the marker. Edit's "edit x" doesn't contain "l44", so it's dropped.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run src/session/thinking && npm run typecheck && npm run lint`

Expected: PASS.

- [ ] **Step 5: Commit.** Message: `jam app: the Thinking rows (pairing, costs, runs, filters)`.

---

### Task 5: `useThinkingFeed` — the connection

**Files:**
- Create: `web/app/src/session/thinking/useThinkingFeed.ts`, `web/app/src/session/thinking/useThinkingFeed.test.tsx`

**Interfaces:**
- Consumes: `feedReducer`, `emptyFeed`, `Feed`, `FeedAction` (feed.ts); `apiGet`, `ApiError`; `ThinkingPage` (types).
- Produces:
  - `useThinkingFeed(id: string, opts: { max?: number; backoff?: number[]; following: () => boolean }): ThinkingFeed`;
  - `interface ThinkingFeed { feed: Feed; pinned: string; connect(stream: string): void; earlier(): Promise<void>; fill(key: string): Promise<void>; seen(): void }`.
  - `connect(stream)` clears the list, then follows `stream` (`""` = the newest).
  - `earlier()` loads the page below `feed.more`.
  - `fill(key)` loads the page that fills that skip marker.
- The EventSource URL is `/app/user/session/${enc(id)}/thinking/events?format=json` plus `&stream=${enc(stream)}`.

- [ ] **Step 1: Write the failing tests** (`useThinkingFeed.test.tsx`):

```tsx
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, waitFor } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { tev } from "./feed.test";
import { useThinkingFeed, type ThinkingFeed } from "./useThinkingFeed";

class FakeES {
  static all: FakeES[] = [];
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  readyState = 0;
  closed = false;
  constructor(public url: string) { FakeES.all.push(this); }
  addEventListener(ev: string, fn: (e: MessageEvent) => void) { (this.listeners[ev] ??= []).push(fn); }
  close() { this.closed = true; this.readyState = 2; }
  emit(ev: string, data = "") { this.listeners[ev]?.forEach((fn) => fn(new MessageEvent(ev, { data }))); }
  fail() { this.readyState = 2; this.emit("error"); }
  static get last() { return FakeES.all[FakeES.all.length - 1]!; }
}

let feed: ThinkingFeed;
function Probe({ backoff }: { backoff?: number[] }) {
  feed = useThinkingFeed("ses_1", { max: 100, backoff, following: () => true });
  return null;
}
const seqs = () => feed.feed.items.map((i) => (i.t === "ev" ? `${i.ev.stream}:${i.ev.seq}` : i.t));

afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); FakeES.all = []; });

describe("useThinkingFeed", () => {
  it("follows the JSON feed: stream, events, more, skipped", () => {
    vi.stubGlobal("EventSource", FakeES);
    render(<Probe />);
    const es = FakeES.last;
    expect(es.url).toBe("/app/user/session/ses_1/thinking/events?format=json");
    act(() => {
      es.emit("stream", "s1");
      es.emit("ev", JSON.stringify(tev(4)));
      es.emit("more", "4");
      es.emit("skipped", "5-7");
      es.emit("ev", JSON.stringify(tev(8)));
    });
    expect(seqs()).toEqual(["s1:4", "skip", "s1:8"]);
    expect(feed.feed.more).toBe(4);
    expect(feed.feed.stream).toBe("s1");
  });

  it("on newstream: a divider, then the new stream without clearing", () => {
    vi.stubGlobal("EventSource", FakeES);
    render(<Probe />);
    act(() => { FakeES.last.emit("stream", "s1"); FakeES.last.emit("ev", JSON.stringify(tev(1))); });
    const first = FakeES.last;
    act(() => first.emit("newstream", "s2"));
    expect(first.closed).toBe(true);
    expect(FakeES.all).toHaveLength(2);
    expect(FakeES.last.url).toBe("/app/user/session/ses_1/thinking/events?format=json");
    act(() => { FakeES.last.emit("stream", "s2"); FakeES.last.emit("ev", JSON.stringify(tev(1, { stream: "s2" }))); });
    expect(seqs()).toEqual(["s1:1", "restart", "s2:1"]);
  });

  it("loads earlier pages and fills holes from the API", async () => {
    vi.stubGlobal("EventSource", FakeES);
    const asked: string[] = [];
    server.use(http.get("/app/user/api/sessions/:id/thinking", ({ request }) => {
      const u = new URL(request.url);
      asked.push(`${u.searchParams.get("stream")}<${u.searchParams.get("before")}`);
      const before = Number(u.searchParams.get("before"));
      return HttpResponse.json({ events: [tev(before - 2), tev(before - 1)], before: before - 2 > 1 ? before - 2 : 0 });
    }));
    render(<Probe />);
    act(() => { FakeES.last.emit("stream", "s1"); FakeES.last.emit("ev", JSON.stringify(tev(10))); FakeES.last.emit("more", "10"); });
    await act(() => feed.earlier());
    expect(seqs()).toEqual(["s1:8", "s1:9", "s1:10"]);
    expect(feed.feed.more).toBe(8);
    act(() => { FakeES.last.emit("skipped", "11-14"); FakeES.last.emit("ev", JSON.stringify(tev(15))); });
    const key = feed.feed.items.find((i) => i.t === "skip")!;
    await act(() => feed.fill(key.t === "skip" ? key.key : ""));
    expect(asked).toEqual(["s1<10", "s1<15"]);
    expect(seqs()).toEqual(["s1:8", "s1:9", "s1:10", "skip", "s1:13", "s1:14", "s1:15"]);
  });

  it("a refused feed: 404 says the session is gone, and stops", async () => {
    vi.stubGlobal("EventSource", FakeES);
    server.use(http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json({ error: "no such session", code: "not_found" }, { status: 404 })));
    render(<Probe backoff={[1]} />);
    act(() => FakeES.last.fail());
    await waitFor(() => expect(feed.feed.note).toBe("This session is no longer available to you"));
    expect(FakeES.all).toHaveLength(1);
  });

  it("a refused feed otherwise retries on the backoff, then says so, and stops", async () => {
    vi.stubGlobal("EventSource", FakeES);
    server.use(http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json({ id: "ses_1", tab: "thinking" })));
    render(<Probe backoff={[1, 1]} />);
    act(() => FakeES.last.fail());
    await waitFor(() => expect(FakeES.all).toHaveLength(2));
    act(() => FakeES.last.fail());
    await waitFor(() => expect(FakeES.all).toHaveLength(3));
    act(() => FakeES.last.fail());
    await waitFor(() => expect(feed.feed.note).toBe("Too many Thinking views open — close one, or reload"));
    await new Promise((r) => setTimeout(r, 20));
    expect(FakeES.all).toHaveLength(3);
  });

  it("an open resets the backoff; a transient error (still connecting) is EventSource's own retry", async () => {
    vi.stubGlobal("EventSource", FakeES);
    render(<Probe backoff={[1]} />);
    act(() => { FakeES.last.readyState = 0; FakeES.last.emit("error"); });
    await new Promise((r) => setTimeout(r, 10));
    expect(FakeES.all).toHaveLength(1);
  });

  it("closes the feed on unmount", () => {
    vi.stubGlobal("EventSource", FakeES);
    const { unmount } = render(<Probe />);
    unmount();
    expect(FakeES.last.closed).toBe(true);
  });
});
```

- [ ] **Step 2: Run them to see them fail.**

Run: `npx vitest run src/session/thinking/useThinkingFeed.test.tsx`

Expected: FAIL, "Failed to resolve import".

- [ ] **Step 3: Implement** `useThinkingFeed.ts`:

```ts
import { useCallback, useEffect, useReducer, useRef } from "react";
import { apiGet, ApiError } from "../../api/client";
import type { ThinkingEvent, ThinkingPage } from "../../api/types";
import { emptyFeed, feedReducer, type Feed } from "./feed";

export interface ThinkingFeed {
  feed: Feed;
  pinned: string;
  connect(stream: string): void;
  earlier(): Promise<void>;
  fill(key: string): Promise<void>;
  seen(): void;
}

const enc = encodeURIComponent;
export const BACKOFF = [2000, 5000, 15000];
export const MAX_ROWS = 2000;

// useThinkingFeed is a session's Thinking feed: the JSON form of
// …/thinking/events (signals and rows; each row is data, rendered as text),
// older pages from api/sessions/{id}/thinking. A feed the server refuses
// (EventSource can't say why) is checked against the pane: 404 means the
// viewer lost the session; anything else (429 too many feeds, a 502)
// retries after each backoff delay, then says so — never looping.
export function useThinkingFeed(id: string, opts: { max?: number; backoff?: number[]; following: () => boolean }): ThinkingFeed {
  const max = opts.max ?? MAX_ROWS;
  const backoff = opts.backoff ?? BACKOFF;
  const [feed, dispatch] = useReducer(feedReducer(max), emptyFeed);
  const live = useRef<{ es: EventSource | null; pinned: string; tries: number; retry: ReturnType<typeof setTimeout> | undefined; gen: number }>(
    { es: null, pinned: "", tries: 0, retry: undefined, gen: 0 });
  const following = useRef(opts.following);
  following.current = opts.following;
  const feedRef = useRef(feed);
  feedRef.current = feed;

  const open = useCallback((stream: string, clear: boolean) => {
    const L = live.current;
    L.es?.close();
    clearTimeout(L.retry);
    L.pinned = stream;
    const gen = ++L.gen;
    if (clear) dispatch({ type: "reset" });
    const es = new EventSource(`/app/user/session/${enc(id)}/thinking/events?format=json${stream ? `&stream=${enc(stream)}` : ""}`);
    L.es = es;
    const on = (name: string, fn: (e: MessageEvent<string>) => void) =>
      es.addEventListener(name, (e) => { if (L.gen === gen) fn(e as MessageEvent<string>); });
    on("stream", (e) => dispatch({ type: "stream", stream: e.data }));
    on("ev", (e) => {
      let ev: ThinkingEvent;
      try { ev = JSON.parse(e.data) as ThinkingEvent; } catch { return; }
      dispatch({ type: "live", ev, following: following.current() });
    });
    on("more", (e) => dispatch({ type: "more", before: Number(e.data) }));
    on("skipped", (e) => {
      const [from, to] = e.data.split("-").map(Number);
      if (from && to) dispatch({ type: "skipped", from, to });
    });
    on("newstream", () => {
      es.close();
      dispatch({ type: "restart" });
      open("", false);
    });
    on("open", () => { L.tries = 0; });
    on("error", () => {
      if (es.readyState !== EventSource.CLOSED) return; // EventSource retries by itself
      es.close();
      L.es = null;
      void apiGet(`sessions/${enc(id)}/pane/thinking`).then(() => 0, (err: unknown) => (err instanceof ApiError ? err.status : 0)).then((st) => {
        if (L.gen !== gen) return;
        if (st === 404) { dispatch({ type: "note", text: "This session is no longer available to you" }); return; }
        if (L.tries >= backoff.length) { dispatch({ type: "note", text: "Too many Thinking views open — close one, or reload" }); return; }
        const wait = backoff[L.tries++]!;
        L.retry = setTimeout(() => { if (L.gen === gen) open(stream, true); }, wait);
      });
    });
  }, [id, backoff]);

  useEffect(() => {
    const L = live.current;
    open("", false);
    return () => { L.gen++; L.es?.close(); clearTimeout(L.retry); };
    // backoff is a fresh array per render in tests: key on id only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id]);

  const page = async (before: number, at: string | null, from: number) => {
    const f = feedRef.current;
    const first = f.items.find((i) => i.t === "ev");
    const stream = at === null ? (first?.t === "ev" ? first.ev.stream : f.stream) : (f.items.find((i) => i.t === "skip" && i.key === at) as { stream: string } | undefined)?.stream ?? f.stream;
    if (!stream || !before) return;
    const p = await apiGet<ThinkingPage>(`sessions/${enc(id)}/thinking?stream=${enc(stream)}&before=${before}`);
    dispatch({ type: "page", events: p.events, at, from, before: p.before });
  };

  return {
    feed,
    pinned: live.current.pinned,
    connect: (stream) => { live.current.tries = 0; open(stream, true); },
    earlier: () => page(feedRef.current.more, null, 0),
    fill: (key) => {
      const m = feedRef.current.items.find((i) => i.t === "skip" && i.key === key);
      return m?.t === "skip" ? page(m.to + 1, key, m.from) : Promise.resolve();
    },
    seen: () => dispatch({ type: "seen" }),
  };
}
```

Notes:
- **Generations.** `gen` is how a closed feed's late events and a stale retry are ignored. This mirrors the old `meTh===t && t.es===es` guard.
- **Refs written during render.** If the react-hooks lint rule flags `feedRef.current = feed` or `following.current = …` at render time, move them into `useEffect(() => { … })` without deps (or a `useLayoutEffect`), and ledger it.
- **The backoff test.** The first failure is retry 1 and the second is retry 2. With `backoff = [1, 1]`, the third failure finds `tries === 2 === backoff.length`, so it says the message.
- **`fill` after the hole marker shrinks.** `fill` reads the marker's current `to`, so a second call fetches the next page below the remaining hole.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run src/session/thinking && npm run typecheck && npm run lint`

Expected: PASS.

- [ ] **Step 5: Commit.** Message: `jam app: the Thinking feed connection`.

---

### Task 6: `ThinkingTab` — the UI, lazily loaded

**Files:**
- Create: `web/app/src/session/thinking/ThinkingTab.tsx`, `web/app/src/session/thinking/thinking.css`, `web/app/src/session/thinking/ThinkingTab.test.tsx`
- Modify: `web/app/package.json` and `package-lock.json` (`npm install @tanstack/react-virtual@3.14.23 --save-exact`), `web/app/src/session/PaneTabs.tsx`, `web/app/src/session/SessionView.tsx`, `web/app/src/session/SessionView.test.tsx`

**Interfaces:**
- Consumes: everything from Tasks 3–5; `usePaneTab(id, "thinking")`.
- Produces:
  - `export default function ThinkingTab({ id, busy, max?, backoff? }: { id: string; busy: boolean; max?: number; backoff?: number[] })`;
  - `PaneBody` gets a `busy: boolean` prop. Its Thinking branch is `<Suspense fallback={<Loader size="sm" />}><ThinkingTab id={id} busy={busy} /></Suspense>`, with `const ThinkingTab = lazy(() => import("./thinking/ThinkingTab"))`;
  - `SessionView` passes `busy={h.state.class === "busy"}`.
  - The pane body drops its padding and scroll for the Thinking tab (the list scrolls itself).
- DOM contract (for the tests and the browser test):
  - `[data-thinking]` is the tab root;
  - the list is `role="log"` with `aria-label="Session events"`;
  - each line is `[data-kind][data-seq]` with `.te-line` (click toggles details), `.te-status` and `.te-time`;
  - the chips are `button[data-f][aria-pressed]`;
  - the selects carry `aria-label` "Tool", "Format" and "Stream"; the search has `aria-label` "Search events";
  - the buttons are "Load earlier" and "↓ N new" (`aria-label="Show new events"`).

- [ ] **Step 1: Write the failing tests** (`ThinkingTab.test.tsx`):

```tsx
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "../../test/server";
import { theme } from "../../theme";
import { tev } from "./feed.test";
import ThinkingTab from "./ThinkingTab";

class FakeES {
  static last: FakeES;
  listeners: Record<string, ((e: MessageEvent) => void)[]> = {};
  readyState = 1;
  constructor(public url: string) { FakeES.last = this; }
  addEventListener(ev: string, fn: (e: MessageEvent) => void) { (this.listeners[ev] ??= []).push(fn); }
  close() { this.readyState = 2; }
  emit(ev: string, data = "") { act(() => this.listeners[ev]?.forEach((fn) => fn(new MessageEvent(ev, { data })))); }
  send(...evs: ReturnType<typeof tev>[]) { for (const e of evs) this.emit("ev", JSON.stringify(e)); }
}

function mount(props: Partial<Parameters<typeof ThinkingTab>[0]> = {}) {
  vi.stubGlobal("EventSource", FakeES);
  server.use(
    http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json({ id: "ses_1", tab: "thinking", previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [],
      streams: [{ id: "s1", firstAt: "2026-10-26T09:00:00Z", events: 12 }, { id: "s0", firstAt: "2026-10-25T09:00:00Z", events: 300 }], eventsOff: false })),
    http.get("/app/user/api/sessions/:id/thinking/raw", ({ request }) => HttpResponse.json({ raw: `{"seq":${new URL(request.url).searchParams.get("seq")}}`, truncated: 0 })),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<MantineProvider theme={theme} env="test"><QueryClientProvider client={client}><ThinkingTab id="ses_1" busy={false} {...props} /></QueryClientProvider></MantineProvider>);
  FakeES.last.emit("stream", "s1");
  return FakeES.last;
}
const lines = () => screen.queryAllByTestId("te").map((n) => n.querySelector(".te-text")!.textContent);

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear(); });

describe("the Thinking tab", () => {
  it("shows events as text (never markup), pairs tool results, and labels costs", async () => {
    const es = mount();
    es.send(tev(1, { text: "<img src=x onerror=alert(1)>" }),
      tev(2, { kind: "tool_call", tool: "Bash", toolUseId: "tu1", text: "go test", at: 1000 }),
      tev(3, { kind: "tool_result", tool: "Bash", toolUseId: "tu1", detail: "ok pkg", at: 4100 }),
      tev(4, { kind: "result", costUsd: 0.12, durationMs: 38000, claudeSession: "c1" }));
    expect(await screen.findByText("<img src=x onerror=alert(1)>")).toBeInTheDocument();
    expect(document.querySelector("img")).toBeNull();
    expect(lines()).toEqual(["<img src=x onerror=alert(1)>", "go test", "episode 1 · $0.12 session total · 38s"]);
    const call = screen.getAllByTestId("te")[1]!;
    expect(within(call).getByText("✓ 3.1s")).toBeInTheDocument();
    await userEvent.click(call.querySelector(".te-line")!);
    expect(within(call).getByText("ok pkg")).toBeVisible();
  });

  it("clips long output to 40 lines with show all", async () => {
    const es = mount();
    es.send(tev(1, { kind: "tool_call", toolUseId: "t", text: "cat" }),
      tev(2, { kind: "tool_result", toolUseId: "t", detail: Array.from({ length: 45 }, (_, i) => `row${i}`).join("\n") }));
    const call = (await screen.findAllByTestId("te"))[0]!;
    await userEvent.click(call.querySelector(".te-line")!);
    expect(within(call).queryByText(/row44/)).toBeNull();
    await userEvent.click(within(call).getByRole("button", { name: "show all 45 lines" }));
    expect(within(call).getByText(/row44/)).toBeInTheDocument();
  });

  it("filters with the chips, tool and search, and remembers chips, tool and format (not the search)", async () => {
    const es = mount();
    es.send(tev(1, { kind: "thinking", text: "pondering" }), tev(2, { kind: "tool_call", tool: "Edit", toolUseId: "e", text: "edit x" }), tev(3, { text: "done" }));
    await screen.findByText("pondering");
    await userEvent.click(screen.getByRole("button", { name: "Thinking" }));
    expect(lines()).toEqual(["edit x", "done"]);
    expect(screen.getByRole("button", { name: "Thinking" })).toHaveAttribute("aria-pressed", "false");
    await userEvent.selectOptions(screen.getByLabelText("Tool"), "Edit");
    expect(lines()).toEqual(["edit x"]);
    await userEvent.selectOptions(screen.getByLabelText("Tool"), "");
    await userEvent.type(screen.getByLabelText("Search events"), "don");
    expect(lines()).toEqual(["done"]);
    await userEvent.selectOptions(screen.getByLabelText("Format"), "detailed");
    expect(JSON.parse(localStorage.getItem("jam-thinking")!)).toEqual({
      f: { thinking: false, text: true, tools: true, errors: false, progress: false }, tool: "", format: "detailed" });
  });

  it("raw fetches each shown line's JSON", async () => {
    localStorage.setItem("jam-thinking", JSON.stringify({ format: "raw" }));
    const es = mount();
    es.send(tev(7));
    expect(await screen.findByText('{"seq":7}')).toBeInTheDocument();
  });

  it("loads earlier, and past the cap pauses; the pill reloads the live tail", async () => {
    server.use(http.get("/app/user/api/sessions/:id/thinking", ({ request }) => {
      const before = Number(new URL(request.url).searchParams.get("before"));
      return HttpResponse.json({ events: [tev(before - 2), tev(before - 1)], before: before - 2 });
    }));
    const es = mount({ max: 4 });
    es.send(tev(10), tev(11), tev(12));
    es.emit("more", "10");
    await userEvent.click(await screen.findByRole("button", { name: "Load earlier" }));
    await waitFor(() => expect(lines()).toEqual(["said 8", "said 9", "said 10", "said 11"])); // 12 trimmed: paused
    expect(screen.getByRole("button", { name: "Show new events" })).toHaveTextContent("1");
    es.send(tev(13));
    expect(lines()).toEqual(["said 8", "said 9", "said 10", "said 11"]);
    expect(screen.getByRole("button", { name: "Show new events" })).toHaveTextContent("2");
    const before = FakeES.last;
    await userEvent.click(screen.getByRole("button", { name: "Show new events" }));
    expect(FakeES.last).not.toBe(before); // reconnected to the live tail
    expect(lines()).toEqual([]);
  });

  it("marks a hole and a restart; picks an older stream; says working while busy", async () => {
    const es = mount({ busy: true });
    es.send(tev(1));
    es.emit("skipped", "2-5");
    expect(await screen.findByText(/events 2-5 skipped/)).toBeInTheDocument();
    expect(screen.getByText("working…")).toBeInTheDocument();
    es.emit("newstream", "s2");
    expect(screen.getByText("— session restarted —")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("option", { name: /300 events/ })).toBeInTheDocument());
    await userEvent.selectOptions(screen.getByLabelText("Stream"), "s0");
    expect(FakeES.last.url).toBe("/app/user/session/ses_1/thinking/events?format=json&stream=s0");
  });

  it("says when events aren't recorded", async () => {
    vi.stubGlobal("EventSource", FakeES);
    server.use(http.get("/app/user/api/sessions/:id/pane/thinking", () => HttpResponse.json({ id: "ses_1", tab: "thinking", previews: [], previewsOff: false, boards: [], files: [], asks: [], links: [], status: [], streams: [], eventsOff: true })));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<MantineProvider theme={theme} env="test"><QueryClientProvider client={client}><ThinkingTab id="ses_1" busy={false} /></QueryClientProvider></MantineProvider>);
    expect(await screen.findByText("Session events aren't recorded on this jam.")).toBeInTheDocument();
  });
});
```

Each line carries `data-testid="te"`, which is test-only and harmless. The list renders through the virtualizer with `initialRect: { width: 600, height: 800 }`, so in jsdom (no layout) every row in these small fixtures renders.

In `SessionView.test.tsx`, the test "lists previews, artifacts and status, and links Thinking to the current Inbox" no longer finds the Thinking link. Change its last two lines to check Studio instead, which 6b ports:

```tsx
    // (Studio links to the current Inbox until 6b; Thinking is in the app.)
```

With `head({ studio: true })` in that test's `serve`, click the "Studio" tab and expect the link `/app/user/?c=chn_s&tab=studio`.

- [ ] **Step 2: Run them to see them fail.**

Run: `npx vitest run src/session`

Expected: FAIL, "Failed to resolve import ./ThinkingTab".

- [ ] **Step 3: Implement.**

Install: `cd web/app && npm install --save-exact @tanstack/react-virtual@3.14.23`.

`thinking.css` ports layout.html's `.th*`/`.te*` rules, using Mantine variables in place of the old ones:
- `--muted` becomes `var(--mantine-color-dimmed)`;
- `--border` becomes `var(--mantine-color-default-border)`;
- `--surface-2` becomes `var(--mantine-color-default-hover)`;
- `--accent` becomes `var(--mantine-primary-color-filled)`;
- `--wait` becomes `var(--mantine-color-yellow-8)`.

```css
.th { display: flex; flex-direction: column; height: 100%; min-height: 0; position: relative; }
.th-bar { display: flex; flex-wrap: wrap; gap: 6px; padding: 6px 10px; border-bottom: 1px solid var(--mantine-color-default-border); }
.th-bar button[data-f] { font: inherit; font-size: 12px; padding: 2px 7px; border: 1px solid var(--mantine-color-default-border); border-radius: 6px; background: none; color: var(--mantine-color-dimmed); cursor: pointer; }
.th-bar button[data-f][aria-pressed="true"] { background: var(--mantine-primary-color-light); color: var(--mantine-primary-color-filled); }
.th-list { flex: 1; min-height: 0; overflow: auto; font: 12.5px/1.45 var(--mantine-font-family-monospace); padding: 0 10px; }
.te-line { cursor: pointer; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; padding: 2px 0; }
.te-icon { display: inline-block; width: 1.4em; color: var(--mantine-color-dimmed); }
.te-tool { margin-right: 4px; }
.te-meta { font-size: 11px; line-height: 1.3; color: var(--mantine-color-dimmed); padding-left: 1.4em; white-space: nowrap; opacity: .8; }
.te-status { margin-left: 8px; }
.te.err .te-status { color: var(--mantine-color-yellow-8); }
.te[data-kind="other"] .te-line, .te.empty .te-text { color: var(--mantine-color-dimmed); cursor: default; }
.te[data-kind="prompt"] .te-text { font-weight: 600; }
.te[data-kind="thinking"] .te-text { color: var(--mantine-color-dimmed); font-style: italic; }
.te[data-kind="result"] { border-top: 1px solid var(--mantine-color-default-border); color: var(--mantine-color-dimmed); font-size: 11.5px; margin: 4px 0; }
.te[data-kind="gap"], .te-skip { color: var(--mantine-color-yellow-8); }
.te-detail, .te-out, .te-raw { white-space: pre-wrap; word-break: break-word; margin: 2px 0 6px 18px; font: inherit; padding: 4px 6px; background: var(--mantine-color-default-hover); border-radius: 4px; }
.te-trunc, .te-note { margin-left: 8px; font-size: 11px; color: var(--mantine-color-dimmed); }
.te-div, .te-gone, .te-working { color: var(--mantine-color-dimmed); text-align: center; padding: 4px 0; font-size: 11.5px; }
.te-working { text-align: left; }
.th button.te-small { font: inherit; font-size: 11.5px; cursor: pointer; background: none; border: 1px solid var(--mantine-color-default-border); border-radius: 6px; padding: 1px 7px; color: var(--mantine-primary-color-filled); }
.te-more { display: block; margin: 4px auto; }
.th-new { position: absolute; bottom: 10px; left: 50%; transform: translateX(-50%); font: inherit; font-size: 12px; cursor: pointer; background: var(--mantine-primary-color-filled); color: #fff; border: 0; border-radius: 999px; padding: 3px 12px; box-shadow: 0 4px 12px rgba(0,0,0,.18); }
```

`ThinkingTab.tsx`:

```tsx
import { Loader, NativeSelect, Text, TextInput } from "@mantine/core";
import { useVirtualizer } from "@tanstack/react-virtual";
import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { apiGet } from "../../api/client";
import type { ThinkingRaw } from "../../api/types";
import { usePaneTab } from "../useSession";
import { clock, num } from "./format";
import { FORMATS, loadPrefs, savePrefs, type Chip, type ThinkingPrefs } from "./prefs";
import { deriveRows, toolsIn, type Row } from "./rows";
import { useThinkingFeed } from "./useThinkingFeed";
import "./thinking.css";

const CHIPS: [Chip, string][] = [["thinking", "Thinking"], ["text", "Text"], ["tools", "Tools"], ["errors", "Errors only"], ["progress", "Progress"]];
const CLIP = 40;
const enc = encodeURIComponent;

// Output is a folded tool result, clipped to CLIP lines until asked.
function Output({ text }: { text: string }) {
  const [all, setAll] = useState(false);
  const ls = text.split("\n");
  if (all || ls.length <= CLIP) return <pre className="te-out">{text}</pre>;
  return <pre className="te-out">{ls.slice(0, CLIP).join("\n") + "\n"}<button type="button" className="te-small" onClick={() => setAll(true)}>show all {ls.length} lines</button></pre>;
}

// Raw fetches one event's JSON when its line is first shown (cached).
function Raw({ id, row, cache }: { id: string; row: Row; cache: Map<string, string> }) {
  const [text, setText] = useState(() => cache.get(row.key) ?? "…");
  useLayoutEffect(() => {
    if (cache.has(row.key) || !row.ev) return;
    let live = true;
    apiGet<ThinkingRaw>(`sessions/${enc(id)}/thinking/raw?stream=${enc(row.ev.stream)}&seq=${row.ev.seq}`).then(
      (r) => { const s = r.truncated ? `${r.raw}\n… truncated ${num(r.truncated)} bytes` : r.raw; cache.set(row.key, s); if (live) setText(s); },
      (e: { status?: number }) => { if (live) setText(`(raw JSON unavailable: ${e.status ?? "error"})`); });
    return () => { live = false; };
  }, [id, row, cache]);
  return <pre className="te-raw">{text}</pre>;
}

function Line({ id, row, prefs, open, onToggle, cache }: { id: string; row: Row; prefs: ThinkingPrefs; open: boolean; onToggle: () => void; cache: Map<string, string> }) {
  const e = row.ev!;
  const details = prefs.format === "detailed" || (prefs.format === "narrative" && open);
  return (
    <div className={`te${row.err ? " err" : ""}${row.empty ? " empty" : ""}`} data-testid="te" data-kind={row.kind} data-seq={e.seq}>
      <div className="te-line" onClick={onToggle}><span className="te-icon">{row.icon}</span>{e.tool && <b className="te-tool">{e.tool}</b>}<span className="te-text">{row.text}</span></div>
      <div className="te-meta"><span className="te-time">{clock(e.at)}</span>{row.status && <span className="te-status">{row.status}</span>}
        {e.truncatedBytes > 0 && <span className="te-trunc">✂ {num(e.truncatedBytes)} bytes dropped</span>}</div>
      {details && e.detail && <pre className="te-detail">{e.detail}</pre>}
      {details && e.detailTruncated > 0 && <div className="te-note">… {num(e.detailTruncated)} more bytes</div>}
      {details && row.output && <Output text={row.output} />}
      {details && row.outputTruncated > 0 && <div className="te-note">… {num(row.outputTruncated)} more bytes</div>}
      {prefs.format === "raw" && <Raw id={id} row={row} cache={cache} />}
    </div>
  );
}

// ThinkingTab is the session pane's Thinking tab (lazily loaded): the
// session's events, live, filtered and formatted as the viewer chose.
export default function ThinkingTab({ id, busy, max, backoff }: { id: string; busy: boolean; max?: number; backoff?: number[] }) {
  const pane = usePaneTab(id, "thinking");
  const [prefs, setPrefs] = useState(loadPrefs);
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<Set<string>>(() => new Set());
  const list = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const anchor = useRef<{ top: number; total: number } | null>(null);
  const cache = useRef(new Map<string, string>()).current;
  const t = useThinkingFeed(id, { max, backoff, following: () => atBottom.current });
  const rows = useMemo(() => deriveRows(t.feed.items, prefs, q), [t.feed.items, prefs, q]);
  const tools = useMemo(() => { const x = toolsIn(t.feed.items); if (prefs.tool && !x.includes(prefs.tool)) x.push(prefs.tool); return x; }, [t.feed.items, prefs.tool]);
  // The list: Load earlier, the rows, then the working line and a note.
  const extra = (t.feed.more > 0 ? 1 : 0);
  const count = extra + rows.length + (busy ? 1 : 0) + (t.feed.note ? 1 : 0);
  const v = useVirtualizer({ count, getScrollElement: () => list.current, estimateSize: () => 22, overscan: 20,
    initialRect: { width: 600, height: 800 }, getItemKey: (i) => (i < extra ? "more" : i - extra < rows.length ? rows[i - extra]!.key : `tail:${i - extra - rows.length}`) });
  const update = (p: ThinkingPrefs) => { setPrefs(p); savePrefs(p); };

  // Follow the end while at the bottom; keep the reader's place when a page
  // is inserted above.
  useLayoutEffect(() => {
    const el = list.current;
    if (!el) return;
    if (anchor.current) {
      el.scrollTop = anchor.current.top + (v.getTotalSize() - anchor.current.total);
      anchor.current = null;
    } else if (atBottom.current && count > 0) {
      v.scrollToIndex(count - 1, { align: "end" });
    }
  }, [count, rows, v]);

  if (pane.data?.eventsOff) return <Text size="sm" c="dimmed" p="sm">Session events aren't recorded on this jam.</Text>;
  const before = (fn: () => Promise<void>) => () => {
    const el = list.current;
    if (el) anchor.current = { top: el.scrollTop, total: v.getTotalSize() };
    void fn().catch(() => { anchor.current = null; });
  };
  const item = (i: number) => {
    if (i < extra) return <button type="button" className="te-small te-more" onClick={before(t.earlier)}>Load earlier</button>;
    const r = rows[i - extra];
    if (!r) {
      if (busy && i - extra === rows.length) return <div className="te-working">working… <Loader type="dots" size="xs" aria-hidden /></div>;
      return <div className="te-gone">{t.feed.note}</div>;
    }
    if (r.marker === "restart") return <div className="te-div">— session restarted —</div>;
    if (r.marker === "skip") return <div className="te-skip">⚠ events {r.skip!.from}-{r.skip!.to} skipped — <button type="button" className="te-small" onClick={() => void t.fill(r.key)}>Load earlier</button></div>;
    return <Line id={id} row={r} prefs={prefs} cache={cache} open={open.has(r.key)}
      onToggle={() => setOpen((s) => { const n = new Set(s); if (n.has(r.key)) n.delete(r.key); else n.add(r.key); return n; })} />;
  };
  return (
    <div className="th" data-thinking>
      <div className="th-bar">
        <span role="group" aria-label="Show" style={{ display: "inline-flex", gap: 4, flexWrap: "wrap" }}>
          {CHIPS.map(([k, label]) => (
            <button key={k} type="button" data-f={k} aria-pressed={prefs.f[k]} onClick={() => update({ ...prefs, f: { ...prefs.f, [k]: !prefs.f[k] } })}>{label}</button>
          ))}
        </span>
        <NativeSelect size="xs" aria-label="Tool" value={prefs.tool} onChange={(e) => update({ ...prefs, tool: e.currentTarget.value })}
          data={[{ value: "", label: "All tools" }, ...tools.map((x) => ({ value: x, label: x }))]} />
        <TextInput size="xs" type="search" placeholder="Search" aria-label="Search events" value={q} onChange={(e) => setQ(e.currentTarget.value)} style={{ flex: 1, minWidth: 90 }} />
        <NativeSelect size="xs" aria-label="Format" value={prefs.format} onChange={(e) => update({ ...prefs, format: e.currentTarget.value as ThinkingPrefs["format"] })}
          data={FORMATS.map((f) => ({ value: f, label: f[0]!.toUpperCase() + f.slice(1) }))} />
        <NativeSelect size="xs" aria-label="Stream" value={t.pinned} onChange={(e) => t.connect(e.currentTarget.value)}
          data={[{ value: "", label: "Current" }, ...(pane.data?.streams ?? []).map((s) => ({ value: s.id, label: `${new Date(s.firstAt).toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false })} · ${s.events} events` }))]} />
      </div>
      <div ref={list} className="th-list" role="log" aria-live="polite" aria-label="Session events"
        onScroll={(e) => { const el = e.currentTarget; atBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 16; if (atBottom.current) t.seen(); }}>
        <div style={{ height: v.getTotalSize(), position: "relative" }}>
          {v.getVirtualItems().map((vi) => (
            <div key={vi.key} data-index={vi.index} ref={v.measureElement} style={{ position: "absolute", top: 0, left: 0, width: "100%", transform: `translateY(${vi.start}px)` }}>
              {item(vi.index)}
            </div>
          ))}
        </div>
      </div>
      {t.feed.unseen > 0 && (
        <button type="button" className="th-new" aria-label="Show new events"
          onClick={() => { if (t.feed.paused) t.connect(t.pinned); else { atBottom.current = true; v.scrollToIndex(count - 1, { align: "end" }); t.seen(); } }}>
          ↓ <span>{t.feed.unseen}</span> new
        </button>
      )}
    </div>
  );
}
```

Notes on `ThinkingTab`:
- **`pinned`.** `useThinkingFeed`'s `pinned` is read from a ref, which isn't state. If the stream select doesn't follow it, keep `pinned` in `useState` inside the hook and set it in `connect`.
- **Ref writes during render.** If ESLint's react-hooks refs rule flags `cache` read from `useRef(...).current` during render, use `useState(() => new Map())[0]`.

`PaneTabs.tsx`:
- add `import { lazy, Suspense } from "react";` and `const ThinkingTab = lazy(() => import("./thinking/ThinkingTab"));`;
- `PaneBody` takes `busy`;
- the branch becomes:

```tsx
  if (tab === "thinking") return <Suspense fallback={<Loader size="sm" />}><ThinkingTab id={id} busy={busy} /></Suspense>;
  if (tab === "studio") { /* the placeholder, unchanged (6b) */ }
```

`SessionView.tsx`:
- pass `busy={h!.state.class === "busy"}` to `PaneBody`;
- give the body `style={tab === "thinking" ? { flex: 1, minHeight: 0, overflow: "hidden" } : { …as today }}`.

- [ ] **Step 4: Run them to see them pass.**

Run: `npx vitest run && npm run typecheck && npm run lint`, then `cd ../.. && just app`.

Expected:
- PASS;
- the size budget still passes;
- `just app` prints a separate `ThinkingTab-*.js` chunk, so the initial JS doesn't grow by the virtualizer.

- [ ] **Step 5: Commit.** Message: `jam app: the Thinking tab (lazy, virtualized)`.

---

### Task 7: End-to-end and docs

**Files:**
- Modify: `internal/jam/userui/next_browser_test.go`, `docs/web-app.md`, `docs/usage/jam/intercom-ui.md`, `docs/superpowers/specs/2026-10-26-jam-spa-design.md`

- [ ] **Step 1: Write the browser test** (append to `next_browser_test.go`; `sessionevents` is imported as in `thinking_browser_test.go`):

```go
// The app's Thinking tab: the backfill (agent markup inert), a tool call with
// its result folded in, a live event, and Load earlier — under the CSP.
func TestBrowserNextThinking(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p, personal, store, hub := thinkEnv(t)
	old := thinkingTail
	thinkingTail = 5
	t.Cleanup(func() { thinkingTail = old })
	actor, cur := personal.ActorID, strings.Repeat("c", 32)
	t0 := time.Now().Add(-time.Minute)
	add := func(seq uint64, raw []byte, ix sessionevents.Index, at time.Duration) {
		t.Helper()
		ev := sessionevents.Event{ActorID: actor, StreamID: cur, Seq: seq, Turn: 1, Kind: sessionevents.KindEvent, ObservedAt: t0.Add(at), ReceivedAt: t0.Add(at), Raw: raw, Index: ix}
		if err := store.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	for s := uint64(1); s <= 4; s++ {
		add(s, assistantBlock(map[string]any{"type": "text", "text": fmt.Sprintf("early %d", s)}), sessionevents.Index{Type: "assistant"}, time.Duration(s)*time.Millisecond)
	}
	add(5, assistantBlock(map[string]any{"type": "text", "text": `<img src=x onerror="document.title='pwned'">`}), sessionevents.Index{Type: "assistant"}, time.Second)
	add(6, assistantBlock(map[string]any{"type": "tool_use", "id": "tu1", "name": "Bash", "input": map[string]any{"command": "go test ./..."}}), sessionevents.Index{Type: "assistant", ToolName: "Bash"}, 2*time.Second)
	add(7, toolResultBlock("tu1", "ok  \tpkg\t0.1s", false), sessionevents.Index{Type: "user", ToolName: "Bash"}, 5100*time.Millisecond)

	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = asPrincipal(r, p, false)
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })

	has := func(js string) step { return chromedp.Poll[chromedp.Void](js, chromedp.WithPollingTimeout(5*time.Second)) }
	text := `document.querySelector('[role="log"][aria-label="Session events"]').textContent`
	var problems pageProblems
	var title, status string
	err := chromedp.Do(ctx,
		problems.watch(ctx),
		call(cdplog.Enable, cdp.Empty{}),
		call(runtime.Enable, cdp.Empty{}),
		chromedp.EmulateViewport(1400, 900),
		chromedp.Navigate(srv.URL+"/app/next/c/"+e.homeOf(t, personal)+"?tab=thinking"),
		has(text+`.includes('go test')`),
		into(&status, chromedp.Text(chromedp.CSS(`[data-kind="tool_call"] .te-status`))),
		into(&title, chromedp.Evaluate[string](`document.title`)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(status, "✓ 3.1s") || title == "pwned" {
		t.Fatalf("status %q, title %q", status, title)
	}
	// A live event, then the page below the tail.
	live := sessionevents.Event{ActorID: actor, StreamID: cur, Seq: 8, Turn: 2, Kind: sessionevents.KindEvent, ObservedAt: time.Now(), ReceivedAt: time.Now(),
		Raw: assistantBlock(map[string]any{"type": "text", "text": "live 8"}), Index: sessionevents.Index{Type: "assistant"}}
	if err := store.Append(live); err != nil {
		t.Fatal(err)
	}
	hub.Publish(live)
	if err := chromedp.Do(ctx,
		has(text+`.includes('live 8')`),
		chromedp.Click(chromedp.CSS(`.te-more`)),
		has(text+`.includes('early 2')`),
	); err != nil {
		t.Fatal(err)
	}
	for _, pr := range problems.all() {
		t.Errorf("page problem: %s", pr)
	}
}
```

Notes:
- `thinkingTail = 5` backfills seqs 3–7 and sends `more 3`, so Load earlier fetches 1–2 and "early 2" appears.
- `thinkEnv` gives `e.Deps.Events` and `e.Deps.Hub`. The JSON feed counts toward the per-viewer cap like the old one.

Run: `just app && GOPROXY=https://proxy.golang.org go test -tags browser -run 'TestBrowserNext|TestBrowserThinking' ./internal/jam/userui/`

Expected: PASS, with no page problems, and the old Thinking browser tests unchanged.

- [ ] **Step 2: Docs.**

In `docs/web-app.md`'s Layout, add after the `src/session/` bullet:

```markdown
  - `src/session/thinking/` is the Thinking tab, a lazily loaded chunk. Its parts:
    - `feed.ts` reduces the live JSON feed (`…/thinking/events?format=json`) and the pages into items, and owns the 2000-row cap;
    - `rows.ts` derives the display rows: pairing, cost dividers, thinking runs, filters;
    - `useThinkingFeed` holds the EventSource and its refusal backoff;
    - `ThinkingTab` renders the rows with `@tanstack/react-virtual`;
    - the preferences keep the old Inbox's `jam-thinking` localStorage.
```

In `docs/usage/jam/intercom-ui.md`'s preview section, change "(Previews, Artifacts, Status; Thinking and Studio link here for now)" to "(Previews, Artifacts, Status and Thinking; Studio links here for now)".

In the spec's status line, change it to `slices 1–5 merged; slice 6a (Thinking) in progress ([plan](../plans/2026-10-26-jam-spa-6a-thinking.md))`.

- [ ] **Step 3: Full verification.**

Run:
- `just app` (the budget);
- `just app-check`;
- `./scripts/app-types-check.sh`;
- `GOPROXY=https://proxy.golang.org go test ./internal/jam/...`;
- `GOPROXY=https://proxy.golang.org go test -tags browser ./internal/jam/userui/`;
- `STRICT=1 ./scripts/lint.sh`.

Expected: all PASS.

- [ ] **Step 4: Commit.** Message: `jam app: Thinking end-to-end; docs`.

---

## Self-review notes

- **Spec coverage:**
  - **The JSON feed form** (§2): Task 1.
  - **The API table rows:**
    - `api/sessions/{id}/thinking?before=` and `…/thinking/raw`: Task 2;
    - `pane/{tab}` for thinking: Task 2.
  - **Live tail, paging, raw, the 2000-row cap and the virtualized list:** Tasks 3–6.
  - **The lazy chunk:** Task 6.
  - **Parity details:**
    - pairing, costs and runs: Task 4;
    - newstream and refusal: Task 5;
    - filters, prefs, the pill and working: Task 6.
- **Left for 6b:** the Studio tab, Previews "View here", and the `frame-src` CSP (`userui.Next`'s `frameSrc`).
- **Ordering decision:** a filled hole's remaining marker sits above the events that filled it, so what's still missing reads above what was loaded (Task 3 note).
- **Type consistency:**
  - `ThinkingEvent` fields are the same in Go (Task 1), TS (`tev`, Tasks 3–6) and the browser test.
  - `Feed` and `Item` come from `feed.ts`; `Row` from `rows.ts`.
  - `useThinkingFeed`'s `{feed, pinned, connect, earlier, fill, seen}` is used as defined.
