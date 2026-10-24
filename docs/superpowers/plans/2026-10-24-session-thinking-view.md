# Session thinking view Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** add a live **Thinking** tab to the `/app/user` session pane: the session's agent events (thinking, text, tool calls and results), filterable and formattable in the browser, visible to anyone who can see the session's home channel.

**Architecture:**
- The admin timeline's event model (`viewOf`), its SSE framing (`sseWrite`) and its SSE handler (`serveSessionEvents`) move into a new template-free package, `internal/jam/sessionview`. Both UIs call it, and each passes its own fragment renderers.
- The store gains newest-first paging so the member view can tail a stream.
- userui adds three routes behind the existing `sessionFor` check, a fourth pane tab, and client-side filter, format and pairing JS over server-rendered fragments.

**Tech Stack:** Go 1.24+ (net/http, html/template), htmx (already loaded), vanilla JS, headless Chrome via chromedp for `-tags browser` tests, pgx for the Postgres store.

**Spec:** `docs/superpowers/specs/2026-10-24-session-thinking-view-design.md`. **Amendment made while planning:** the fragment *templates* stay with their UIs (adminui keeps `session-event`/`session-totals`; userui adds `thinking-event`). `sessionview.Serve` takes render callbacks, so the shared package has no templates. Task 7 records this in the spec.

## Global Constraints

- Repo rules (AGENTS.md):
  - TDD, failing test first.
  - Hermetic tests: no network, no Docker.
  - Docs are updated in the same PR.
  - Secrets never reach disk, argv or logs. Never log event content.
- Run `gofmt` before committing. `bash scripts/lint.sh` and `go test ./...` must pass.
- `GOPROXY` and the toolchain are set up per `docs/DEVELOPMENT.md`. There is no cgo, so no `-race`.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Limits from the spec:
  - tail **300** events; page **300**;
  - detail clip **64 KB** (`65536` bytes);
  - raw cap **1 MB** (`1 << 20`);
  - **4** concurrent thinking streams per viewer;
  - browser keeps at most **2000** lines;
  - output clipped to **40** lines in the narrative expansion.
- Access: every thinking route calls `h.sessionFor(p, id)` and answers 404 when it fails, on every request, including SSE reconnects.
- Event text reaches the DOM only through html/template output or `textContent`, never `innerHTML` of event text.
- Delivery:
  - **PR 1** = Tasks 1–3, on branch `feat/sessionview`, with no behaviour change.
  - **PR 2** = Tasks 4–7, on branch `feat/thinking-view`, cut from main after PR 1 merges.

## Review Focus

1. **A tool result arriving before its call is on screen** (the call is older than the tail window, or was dropped). The result must still show as its own line rather than vanish. Task 6 pins it: an orphan result renders standalone.
2. **The session restarts mid-view** (a new stream appears). "Current" must move to the new stream with a divider, not keep showing a dead stream. Task 3 tests `newstream`; Task 6 reconnects.
3. **Very large tool output** (a 5 MB file read). The fragment must stay ≤ 64 KB of detail, and raw ≤ 1 MB with a note. Task 2 pins the clip; Task 4 pins the raw cap.
4. **A viewer removed from the channel while watching.** The next reconnect must 404, and the tab must say so. Task 4 tests it.
5. **Agent text containing HTML or `</script>`, or CR characters.** It must render inert and keep SSE framing intact. The existing admin tests cover this; Task 4 adds the same assertion for the member feed.

---

## PR 1 — `feat/sessionview`

### Task 1: Store paging (`BeforeSeq`, `Newest`)

**Files:**
- Modify: `internal/jam/sessionevents/event.go` (Filter)
- Modify: `internal/jam/sessionevents/memstore.go` (List)
- Modify: `internal/jam/sessionevents/sessionpg/sessionpg.go:153` (List)
- Test: `internal/jam/sessionevents/sessioneventstest/conformance.go`. This suite runs for MemStore (`memstore_test.go`) and Postgres (`sessionpg_integration_test.go`, store-integration CI).

**Interfaces:**
- Produces: `sessionevents.Filter{ActorID, StreamID string; AfterSeq, BeforeSeq uint64; Limit int; Newest bool}`.
  - `BeforeSeq` 0 means no upper bound; otherwise it selects `seq < BeforeSeq`.
  - `Newest` with `Limit > 0` returns the **last** `Limit` matching events, still in ascending seq order. `Newest` without a `Limit` is the same as not setting it.

- [ ] **Step 1: Write the failing conformance subtest.** In `RunConformance`, after "append list order and filters":

```go
t.Run("newest and before paging", func(t *testing.T) {
	s := newStore(t)
	t0 := time.Unix(1000, 0)
	for i := uint64(1); i <= 10; i++ {
		must(t, s.Append(ev("a", streamA, i, t0.Add(time.Duration(i)*time.Second), `{"type":"x"}`)))
	}
	seqs := func(f sessionevents.Filter) []uint64 {
		t.Helper()
		evs, err := s.List(f)
		must(t, err)
		var out []uint64
		for _, e := range evs {
			out = append(out, e.Seq)
		}
		return out
	}
	if got := seqs(sessionevents.Filter{ActorID: "a", StreamID: streamA, Newest: true, Limit: 3}); !slices.Equal(got, []uint64{8, 9, 10}) {
		t.Fatalf("newest 3 = %v", got)
	}
	if got := seqs(sessionevents.Filter{ActorID: "a", StreamID: streamA, Newest: true, Limit: 3, BeforeSeq: 8}); !slices.Equal(got, []uint64{5, 6, 7}) {
		t.Fatalf("newest 3 before 8 = %v", got)
	}
	if got := seqs(sessionevents.Filter{ActorID: "a", StreamID: streamA, BeforeSeq: 3}); !slices.Equal(got, []uint64{1, 2}) {
		t.Fatalf("before 3 = %v", got)
	}
	if got := seqs(sessionevents.Filter{ActorID: "a", StreamID: streamA, AfterSeq: 2, BeforeSeq: 5}); !slices.Equal(got, []uint64{3, 4}) {
		t.Fatalf("between = %v", got)
	}
})
```

Use the stream-id constant the file already defines; if it has none, add `const streamA = "0123456789abcdef0123456789abcdef"`. Import `slices`.

- [ ] **Step 2: Run it and watch it fail.** `go test ./internal/jam/sessionevents/ -run Conformance` fails because the fields `BeforeSeq` and `Newest` are undefined.
- [ ] **Step 3: Add the fields and implement MemStore.**

```go
// Filter selects events from a Store.
type Filter struct {
	ActorID, StreamID string // both required
	AfterSeq          uint64
	BeforeSeq         uint64 // 0 = no upper bound; else seq < BeforeSeq
	Limit             int    // <= 0 → unbounded
	Newest            bool   // with Limit: the last Limit matches (still ascending)
}
```

MemStore.List: collect every `e` with `e.Seq > f.AfterSeq && (f.BeforeSeq == 0 || e.Seq < f.BeforeSeq)`. Then, if `f.Limit > 0`, keep `out[len(out)-f.Limit:]` when `f.Newest`, else `out[:f.Limit]`, guarding against `len(out) < f.Limit`. Clone each event, as today.

- [ ] **Step 4: Implement Postgres.** Build the WHERE clause with `seq > $3`, plus `AND seq < $N` when `BeforeSeq > 0`. When `Newest && Limit > 0`, wrap the query:

```go
q := `SELECT * FROM (SELECT ` + selectCols + ` FROM session_events WHERE ` + where + ` ORDER BY seq DESC LIMIT $N) t ORDER BY seq`
```

Otherwise keep `ORDER BY seq` and the optional `LIMIT`. `selectCols` names columns unqualified, so `SELECT *` from the subquery keeps the scan order. Verify that by reading the `selectCols` constant: if it uses expressions such as `COALESCE(...)`, give them aliases in the inner query and select the aliases in the same order outside.

- [ ] **Step 5: Run the tests.** `go test ./internal/jam/sessionevents/...` should PASS. If a local Postgres is available (see the `local-postgres-recipe` memory or `docs/DEVELOPMENT.md`), also run `go test -tags integration ./internal/jam/sessionevents/sessionpg/`. Otherwise CI's store-integration job covers it.
- [ ] **Step 6: Commit.** Message: `feat(sessionevents): newest-first and before-seq paging`.

### Task 2: `sessionview.View` — the shared event model

**Files:**
- Create: `internal/jam/sessionview/view.go` and `internal/jam/sessionview/view_test.go`.
- Modify: `internal/jam/adminui/session.go`. Delete `eventView`, `viewOf`, `clip`, `pretty`, `viewBlock` and `blockText`, and call `sessionview.View`. Template field names stay valid: `Label`, `Summary`, `Detail`, `Raw`, `IsError`, `Progress`, `Gap`, `TruncatedBytes` and `Turn` are all kept.
- Test: `internal/jam/adminui/session_test.go`. The existing tests must still pass. Add one golden fragment test, written **before** the move.

**Interfaces:**
- Produces:

```go
package sessionview

type Kind string

const (
	KindThinking   Kind = "thinking"
	KindText       Kind = "text"
	KindToolCall   Kind = "tool_call"
	KindToolResult Kind = "tool_result"
	KindResult     Kind = "result"
	KindProgress   Kind = "progress"
	KindGap        Kind = "gap"
	KindOther      Kind = "other"
)

// DetailMax caps EventView.Detail (bytes); DetailTruncated counts the rest.
const DetailMax = 64 << 10

type EventView struct {
	StreamID        string
	Seq             uint64
	Turn            uint32
	At              time.Time
	Kind            Kind
	Label           string // admin label: "thinking", "tool_use Bash", "system/init", …
	Tool            string // tool name (tool_call; tool_result when known from Index.ToolName)
	ToolUseID       string // tool_use id / tool_result tool_use_id
	Narrative       string // one line for the member view
	Summary         string // admin summary (unchanged semantics)
	Detail          string // ≤ DetailMax bytes
	DetailTruncated int    // bytes cut from Detail
	Raw             string // pretty JSON (admin renders it; members fetch raw separately)
	IsError         bool
	Progress        bool
	Gap             bool
	TruncatedBytes  uint64
	CostUSD         float64 // result: the episode's cumulative cost (Index.CostUSD)
	DurationMS      int64   // result
}

func View(ev sessionevents.Event) EventView
func Clip(s string, n int) string // whitespace-collapsed, rune-safe, "…"
```

- [ ] **Step 1: Write the admin golden test first, on the current code.** In `adminui/session_test.go`, render the `session-event` fragment for a thinking event, a tool_use, a tool_result error and a result. Use the existing `fragment("session-event", viewOf(ev))` (internal test file, `session_internal_test.go`). Assert the exact HTML strings. Run it: it PASSES on today's code and pins the output.
- [ ] **Step 2: Write `view_test.go`**, a table test over raw stream-json lines:

```go
func TestView(t *testing.T) {
	at := time.Unix(1000, 0)
	mk := func(raw string) sessionevents.Event {
		return sessionevents.Event{StreamID: "s", Seq: 7, Turn: 2, Kind: sessionevents.KindEvent, ObservedAt: at, Raw: []byte(raw)}
	}
	cases := []struct {
		name, raw                         string
		kind                              Kind
		tool, tid, narrative, labelPrefix string
		isErr                             bool
	}{
		{"thinking", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"Plan:\nfirst read the file"}]}}`, KindThinking, "", "", "Plan: first read the file", "thinking", false},
		{"text", `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}`, KindText, "", "", "Done.", "assistant", false},
		{"bash", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./...","description":"run"}}]}}`, KindToolCall, "Bash", "tu1", "go test ./...", "tool_use Bash", false},
		{"edit", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu2","name":"Edit","input":{"file_path":"/w/a.go","old_string":"x","new_string":"y"}}]}}`, KindToolCall, "Edit", "tu2", "/w/a.go", "tool_use Edit", false},
		{"grep", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu3","name":"Grep","input":{"pattern":"func View"}}]}}`, KindToolCall, "Grep", "tu3", "func View", "tool_use Grep", false},
		{"unknown tool", `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu4","name":"mcp__x__y","input":{"a":1}}]}}`, KindToolCall, "mcp__x__y", "tu4", `{"a":1}`, "tool_use mcp__x__y", false},
		{"result err", `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"boom"}]}}`, KindToolResult, "", "tu1", "boom", "tool_result", true},
		{"result", `{"type":"result","subtype":"success","result":"ok"}`, KindResult, "", "", "", "result", false},
		{"progress", `{"type":"system","subtype":"thinking_tokens"}`, KindProgress, "", "", "", "system/thinking_tokens", false},
		{"system", `{"type":"system","subtype":"init"}`, KindOther, "", "", "", "system/init", false},
		{"unparsed", `not json`, KindOther, "", "", "", "unparsed", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := View(mk(c.raw))
			if v.Kind != c.kind || v.Tool != c.tool || v.ToolUseID != c.tid || v.IsError != c.isErr || !strings.HasPrefix(v.Label, c.labelPrefix) {
				t.Fatalf("View = %+v", v)
			}
			if c.narrative != "" && v.Narrative != c.narrative {
				t.Fatalf("narrative = %q, want %q", v.Narrative, c.narrative)
			}
			if v.Seq != 7 || v.Turn != 2 || v.StreamID != "s" || !v.At.Equal(at) {
				t.Fatalf("identity = %+v", v)
			}
		})
	}
}

func TestViewGapAndClip(t *testing.T) {
	g := View(sessionevents.Event{Kind: sessionevents.KindGap, GapFrom: 3, GapTo: 5})
	if g.Kind != KindGap || !g.Gap || !strings.Contains(g.Summary, "3 events lost") {
		t.Fatalf("gap = %+v", g)
	}
	big := strings.Repeat("x", DetailMax+100)
	v := View(sessionevents.Event{Kind: sessionevents.KindEvent, Raw: []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"` + big + `"}]}}`)})
	if len(v.Detail) != DetailMax || v.DetailTruncated != 100 {
		t.Fatalf("clip: len %d, truncated %d", len(v.Detail), v.DetailTruncated)
	}
}
```

Notes for the result case: `Narrative` is built from Index fields (`CostUSD`, `DurationMS`), which this raw-only event lacks, so it isn't asserted here.

The tool-result narrative is the first line of the output, clipped to 160 characters.

- [ ] **Step 3: Run it and watch it fail.** `go test ./internal/jam/sessionview/` fails because the package does not exist.
- [ ] **Step 4: Implement `view.go`.** Move `viewOf` and its helpers from `adminui/session.go` verbatim, then extend them:
  - Set `StreamID`, `At` (= `ev.ObservedAt`), `CostUSD` and `DurationMS` from `ev.Index`.
  - Set Kind per branch:
    - gap → `KindGap`;
    - system/thinking_tokens → `KindProgress`;
    - other system → `KindOther`;
    - assistant thinking → `KindThinking`, Narrative `Clip(firstLine, 160)`;
    - assistant text → `KindText`, Narrative `Clip(text, 160)`;
    - tool_use → `KindToolCall`, `Tool = b.Name`, `ToolUseID = b.ID` (add `ID string \`json:"id"\`` and `ToolUseID string \`json:"tool_use_id"\`` to `viewBlock`), Narrative `keyInput(b.Name, b.Input)`;
    - tool_result → `KindToolResult`, `ToolUseID = b.ToolUseID`, `Tool = ev.Index.ToolName` when non-empty, Narrative `Clip(firstLine(text), 160)`;
    - result → `KindResult`; unparsed or unknown → `KindOther`.

    For the thinking narrative, collapse whitespace across the whole block rather than taking only the first line. That is what `Clip` does, so the thinking case in the test expects "Plan: first read the file".
  - `keyInput`:

```go
var keyFields = map[string][]string{
	"Bash": {"command"}, "Read": {"file_path"}, "Write": {"file_path"}, "Edit": {"file_path"}, "MultiEdit": {"file_path"},
	"NotebookEdit": {"notebook_path"}, "Grep": {"pattern"}, "Glob": {"pattern"}, "WebFetch": {"url"}, "WebSearch": {"query"},
	"Task": {"description"}, "Agent": {"description"},
}

func keyInput(tool string, input json.RawMessage) string {
	var m map[string]any
	if json.Unmarshal(input, &m) == nil {
		for _, f := range keyFields[tool] {
			if s, ok := m[f].(string); ok && s != "" {
				return Clip(s, 160)
			}
		}
	}
	return Clip(string(input), 120)
}
```

  - Detail clip: after setting `Detail`, if `len(Detail) > DetailMax`, cut to `DetailMax` bytes at a rune boundary (step back while `!utf8.RuneStart`) and set `DetailTruncated = original - len(Detail)`. The test uses ASCII, so the result is exactly `DetailMax`.

- [ ] **Step 5: Switch adminui to the shared model.** Replace `viewOf(ev)` with `sessionview.View(ev)`, and delete the moved helpers. `fragment("session-event", …)` takes `sessionview.EventView`. Run `go test ./internal/jam/adminui/ ./internal/jam/sessionview/`: both PASS, including the golden test from Step 1. If the golden test differs only because Detail is clipped, fix the clip rather than changing the golden.
- [ ] **Step 6: Commit.** Message: `refactor: move the session event view model to sessionview`.

### Task 3: `sessionview.Serve` — the shared SSE handler, plus tail mode

**Files:**
- Create: `internal/jam/sessionview/serve.go` and `internal/jam/sessionview/serve_test.go`.
- Modify: `internal/jam/adminui/session.go`. Delete `sseWrite`, `serveSessionEvents` and the backfill constants, and call `sessionview.Serve`. Move `TestSSEWriteFraming` from `adminui/session_internal_test.go` to `sessionview` as a test of `WriteSSE`.

**Interfaces:**
- Consumes: `View`, from Task 2.
- Produces:

```go
// WriteSSE emits one SSE message (CR-safe; id/event one line).
func WriteSSE(w io.Writer, event, id, data string)

type Source struct {
	Store   sessionevents.Store
	Hub     *sessionevents.Hub
	ActorID string
	// Tail > 0: backfill only the newest Tail events (then `more` with the
	// lowest seq sent, when older exist); 0 = the whole stream (admin).
	Tail int
	// FollowNewest: with no stream chosen, follow the newest; when a newer
	// stream's event arrives, send `newstream` (data = its id) and return.
	FollowNewest bool
	Event  func(EventView) string               // the `ev` fragment
	Totals func(sessionevents.Totals) string    // the `totals` fragment; nil = none sent
}

// Serve streams one actor's events over SSE (see the package doc for the
// event names): stream, ev (id "<stream>:<seq>"), totals, more, skipped,
// newstream; keep-alive every 25s.
func Serve(w http.ResponseWriter, r *http.Request, src Source)
```

  - **Stream choice:** `?stream=` if valid. Otherwise, when `FollowNewest`, the newest stream from `Store.Streams`. Otherwise none, until the first live event arrives, as today.
  - **Resume:** a `Last-Event-ID` of `<stream>:<seq>` resumes after `seq`. In tail mode, if more than `Tail` events lie after `seq`, send only the newest `Tail`, preceded by a `skipped` event whose data is `"<from>-<to>"`, the missed seq range.
  - **Totals:** still computed over the whole stream, as the admin code does today. Tail mode skips the totals entirely when `Totals` is nil, so there is no whole-stream read.
  - **More:** in tail mode, after the backfill send `more` with the lowest seq sent, but only if `> 1`.

- [ ] **Step 1: Write the failing tests** in `serve_test.go`, driving `httptest.NewRecorder` through a cancellable context, plus a goroutine.
  - **Simpler alternative:** `httptest.NewServer` and read the body with a timeout. Copy the pattern from `adminui/session_test.go` (`TestSessionSSEBackfillThenLive`), which already does this.
  - **Tail:** a stream with 10 events and `Tail: 3` → the body has `id: s:8`, `id: s:9`, `id: s:10`, no `id: s:7`, and `event: more` with `data: 8`.
  - **Resume skipped:** `Last-Event-ID: s:2`, 10 events, `Tail: 3` → `event: skipped`, `data: 3-7`, then ids 8–10.
  - **Newstream:** `FollowNewest`, stream A backfilled. Publish an event on stream B through the Hub → the body contains `event: newstream` and `data: <B>`, and the handler returns.
  - **Admin mode unchanged:** the existing adminui SSE tests are the regression suite. They must pass unmodified.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement `serve.go`.** Port `serveSessionEvents` (`adminui/session.go:206` onward) and add the Tail, FollowNewest, more, skipped and newstream behaviour:
  - **Tail backfill:** `List(Filter{…, AfterSeq: resumeAfter, Newest: true, Limit: Tail})`.
    - **Skipped:** if `resumeAfter > 0` and the first returned seq is `> resumeAfter+1`, emit `skipped` for `resumeAfter+1` to `first-1`.
    - **Totals:** fold the backfilled events into totals only when `Totals != nil`. In that case also fold the earlier events with a separate `List(Filter{…, BeforeSeq: first})` loop.
  - **Newstream:** in the live loop, when `FollowNewest && stream != "" && ev.StreamID != stream`, emit `newstream` and return.
- [ ] **Step 4: Switch adminui to `Serve`.** Its events handler becomes:

```go
sessionview.Serve(w, r, sessionview.Source{Store: store, Hub: hub, ActorID: r.PathValue("id"),
	Event:  func(v sessionview.EventView) string { return fragment("session-event", v) },
	Totals: func(t sessionevents.Totals) string { return fragment("session-totals", t) }})
```

- [ ] **Step 5: Run the tests.** `go test ./internal/jam/...` should PASS. `bash scripts/lint.sh` should be clean.
- [ ] **Step 6: Commit and open PR 1.** Message: `refactor: shared sessionview SSE handler with tail mode`.
  - Docs: in `docs/usage/jam/ui-console.md` §Session timeline, add that the timeline is served by `internal/jam/sessionview`, shared with the member view, and bump `updated`. In `docs/usage/jam/session-events.md` §Storage, mention `BeforeSeq`/`Newest` paging if the doc lists Filter.
  - Open the PR via REST:

```bash
gh api repos/aethons-tools/cove/pulls -f title=... -f head=feat/sessionview -f base=main -F body=@file
```

---

## PR 2 — `feat/thinking-view` (from main after PR 1)

### Task 4: Member thinking routes

**Files:**
- Create: `internal/jam/userui/thinking.go` and `internal/jam/userui/thinking_test.go`.
- Modify: `internal/jam/userui/userui.go`. Add to Deps:

```go
// Events and Hub serve the Thinking tab (nil Events: not recorded).
Events sessionevents.Store
Hub    *sessionevents.Hub
```

- Modify: `internal/jam/userui/handler.go`. Add the routes:

```go
mux.HandleFunc("GET /app/user/session/{id}/thinking/events", h.thinkingEvents)
mux.HandleFunc("GET /app/user/session/{id}/thinking/page", h.thinkingPage)
mux.HandleFunc("GET /app/user/session/{id}/thinking/raw", h.thinkingRaw)
```

  Register them **before** `POST /app/user/session/{id}/{action}`. They are GETs, so there is no conflict, but keep them grouped.
- Modify: `cmd/at-jam/main.go:2690`. Add `Events: sessStore, Hub: sessHub` to the `userui.Deps{…}` literal. Check that `sessStore` and `sessHub` are in scope there; they are defined around line 2244.
- Modify: `internal/jam/userui/templates/inbox.html`. Add `{{define "thinking-event"}}` (below) and `{{define "thinkingpage"}}`.

**Interfaces:**
- Consumes: `sessionview.Serve`, `Source`, `View`, `EventView`, and `sessionevents.Filter{BeforeSeq, Newest}`.
- Produces:
  - `const thinkingTail = 300`, `thinkingPageSize = 300`, `rawMax = 1 << 20`, `maxThinkingStreams = 4`.
  - `func (h *handler) thinkingFragment(v sessionview.EventView) string`, which renders `thinking-event` to a string. Add a field `PaneEvent *sessionview.EventView` to `inboxPage` and execute `pages["inbox"]` with it into a `bytes.Buffer`.
  - A per-viewer counter on the handler: `streams map[ident.ID]int` guarded by `mu sync.Mutex`.
  - The `thinking-event` element contract that Task 6's JS relies on:

```html
{{define "thinking-event"}}{{with .PaneEvent}}<div class="te k-{{.Kind}}{{if .IsError}} err{{end}}" data-kind="{{.Kind}}" data-tool="{{.Tool}}" data-tid="{{.ToolUseID}}" data-seq="{{.Seq}}" data-stream="{{.StreamID}}" data-at="{{.At.UnixMilli}}" data-error="{{.IsError}}">
  <div class="te-line"><span class="te-icon"></span><b class="te-tool">{{.Tool}}</b> <span class="te-text">{{if .Gap}}{{.Summary}}{{else}}{{.Narrative}}{{end}}</span><span class="te-status"></span>{{if .TruncatedBytes}}<span class="te-trunc">✂ {{.TruncatedBytes}} bytes dropped</span>{{end}}</div>
  {{if .Detail}}<pre class="te-detail" hidden>{{.Detail}}</pre>{{if .DetailTruncated}}<div class="te-note" hidden>… {{.DetailTruncated}} more bytes</div>{{end}}{{end}}
  {{if eq .Kind "result"}}<span class="te-cost" data-cost="{{.CostUSD}}" data-ms="{{.DurationMS}}" hidden></span>{{end}}
</div>{{end}}{{end}}
```

- **Behaviour:**
  - `thinkingEvents`:
    - `p` comes from `jam.ParticipantFrom`; on failure, 401.
    - `inst, _, ok := h.sessionFor(p, id)`; if not ok, 404.
    - If `h.d.Events == nil || h.d.Hub == nil`, write 204.
    - Stream cap: if `h.streams[p.UserID] >= 4`, answer 429. Otherwise increment, and `defer` the decrement.
    - Then call `sessionview.Serve(w, r, Source{Store: h.d.Events, Hub: h.d.Hub, ActorID: inst.ActorID, Tail: thinkingTail, FollowNewest: true, Event: h.thinkingFragment})`. `Totals` is nil.
  - `thinkingPage`: the same checks. It needs `stream` (must satisfy `ValidStreamID`, else 400) and `before` (uint, else 400). It runs `List(Filter{ActorID, StreamID: stream, BeforeSeq: before, Newest: true, Limit: thinkingPageSize})` and renders `thinkingpage`: the events in order, then, if the first seq is `> 1`, `<button class="te-more" data-before="{{firstSeq}}">Load earlier</button>` at the **top**.
  - `thinkingRaw`: the same checks. It needs `stream` and `seq`, and runs `List(Filter{…, AfterSeq: seq-1, Limit: 1})`. If no event has that seq, 404. Otherwise it writes `text/plain; charset=utf-8` with `X-Content-Type-Options: nosniff`, the pretty JSON, cut to `rawMax` with a trailing `\n… truncated N bytes` when longer.

- [ ] **Step 1: Write the failing tests** in `thinking_test.go`. Reuse `sessionEnv` and `asPrincipal` from `session_test.go`. Set `e.Deps.Events = store`, where the store is the one `sessionEnv` created. Add the events store to `sessionEnv`'s returns, or set a fresh `sessionevents.NewMemStore()` and append there. Set `e.Deps.Hub = sessionevents.NewHub()`.
  - **Backfill and inert content:** append 3 events to stream `strings.Repeat("a",32)` for `personal.ActorID`, one of them a text block containing `<script>x</script>`. Serve `/thinking/events` through `httptest.NewServer` and read until `id: …:3`. The body has `data-kind="text"` and `&lt;script&gt;`, and no `<script>x`.
  - **Live:** after the backfill, `Hub.Publish` seq 4 → `id: <stream>:4` arrives.
  - **Outsiders:** bob (not a member) gets 404 on `/thinking/events`, `/thinking/page?stream=…&before=3` and `/thinking/raw?stream=…&seq=1`.
  - **Removed member:** alice leaves the home channel (`e.st.LeaveChannel(home, p.UserID, 0)`), then reconnects → 404.
  - **Raw cap:** an event whose raw is 2 MB → the body length is ≤ `rawMax + 64` and it ends with `truncated`.
  - **Stream cap:** set `h.streams[p.UserID] = 4` directly (internal test, same package) → 429.
  - **Paging:** with 5 events, `?before=4` → the body has seqs 1–3, and no `te-more` (the first seq is 1). With `thinkingPageSize` temporarily 2 (make it a `var`), `before=5` → seqs 3 and 4, plus `data-before="3"`.
  - **No store:** `Events` nil → 204.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement** `thinking.go`, the Deps fields, the routes, the templates and the main.go wiring.
- [ ] **Step 4: Run the tests.** `go test ./internal/jam/userui/ ./cmd/at-jam/` should PASS. `go build ./...` should succeed.
- [ ] **Step 5: Commit.** Message: `feat(userui): member thinking feed, paging and raw routes`.

### Task 5: The Thinking tab shell

**Files:**
- Modify: `internal/jam/userui/sessionpane.go`.
  - Add `"thinking"` to `paneTabs`.
  - Add `Streams []sessionevents.StreamInfo` and `EventsOff bool` to `SessionPane`.
  - In `sessionPaneRoute`, `case "thinking":`. If `h.d.Events == nil`, set `pane.EventsOff = true`; otherwise `pane.Streams, _ = h.d.Events.Streams(inst.ActorID)`.
- Modify: `internal/jam/userui/templates/inbox.html`.
  - Add a fourth header tab button, `data-tab="thinking"`, labelled "Thinking", placed first in `.sh-tabs`.
  - Add the thinking branch to `sessionpane` (below).
- Modify: `internal/jam/userui/templates/layout.html`. Add `thinking:'Thinking'` to `mePaneTabs`.
- Test: `internal/jam/userui/sessionpane_test.go`.

**Interfaces:**
- Produces: the DOM contract for Task 6:

```html
{{else if eq .Tab "thinking"}}
  {{if .EventsOff}}<p class="empty">Session events aren't recorded on this jam.</p>
  {{else}}
  <div class="th" id="thinking" data-session="{{.ID}}">
    <div class="th-bar">
      <span class="th-chips" role="group" aria-label="Show">
        <button type="button" data-f="thinking" aria-pressed="true">Thinking</button>
        <button type="button" data-f="text" aria-pressed="true">Text</button>
        <button type="button" data-f="tools" aria-pressed="true">Tools</button>
        <button type="button" data-f="errors" aria-pressed="false">Errors only</button>
        <button type="button" data-f="progress" aria-pressed="false">Progress</button>
      </span>
      <select class="th-tool" aria-label="Tool"><option value="">All tools</option></select>
      <input class="th-search" type="search" placeholder="Search" aria-label="Search events">
      <select class="th-format" aria-label="Format"><option value="narrative">Narrative</option><option value="detailed">Detailed</option><option value="raw">Raw</option></select>
      <select class="th-stream" aria-label="Stream"><option value="">Current</option>{{range .Streams}}<option value="{{.StreamID}}">{{.FirstAt.Local.Format "Jan 2 15:04"}} · {{.Events}} events</option>{{end}}</select>
    </div>
    <div class="th-list" role="log" aria-live="polite"></div>
    <button type="button" class="th-new" hidden>↓ <span>0</span> new</button>
  </div>
  {{end}}
```

- [ ] **Step 1: Write the failing tests.** Extend `TestSessionPanePreviewsAndStatus`, or add `TestSessionPaneThinking`:
  - `?tab=thinking` with `Events` set → 200. The body contains `id="thinking"`, `data-f="errors"`, `class="th-stream"` and one `<option value="<stream>">` per stream.
  - With `Events` nil → "aren't recorded".
  - The full page contains `data-tab="thinking"`.
  - `TestSessionPaneHiddenFromOutsiders` iterates over `paneTabs`, so it covers thinking automatically.
- [ ] **Step 2: Run them and watch them fail.**
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run the tests.** `go test ./internal/jam/userui/` should PASS.
- [ ] **Step 5: Commit.** Message: `feat(userui): Thinking tab shell`.

### Task 6: Thinking tab behaviour (JS and CSS) and the browser test

**Files:**
- Modify: `internal/jam/userui/templates/layout.html`, for the CSS and the JS below.
- Create: `internal/jam/userui/thinking_browser_test.go` (`//go:build browser`).

**Interfaces:**
- Consumes: the Task 4 element contract (`.te`, `data-*`, `.te-line`, `.te-detail`, `.te-status`, `.te-cost`, `.te-more`), the Task 5 DOM (`#thinking`, `.th-*`), and the routes.
- Produces: `meThinkingOpen(el)` and `meThinkingClose()`, called by the pane code. The pane's `mePaneFetch` loads the shell via htmx. After the swap (an `htmx:afterSettle` on `#side-pane-body`), if `#thinking` exists, call `meThinkingOpen`. `mePaneApply`, on closing or on a tab other than thinking, and `beforeunload` both call `meThinkingClose`. The pane's 30s/push `refresh()` must **skip** the thinking tab: add `|| s._pane.tab==='thinking'` to its early return.

- **Behaviour (implement in this order, each with a browser assertion):**
  1. **Open:** `new EventSource('/app/user/session/'+id+'/thinking/events'+(stream?'?stream='+stream:''))`. Handle each named event:
     - `ev`: parse the fragment with a `<template>` element (`tpl.innerHTML = e.data`, which is server-escaped html/template output and not raw event text), then append via `meThinkingAdd(node)`.
     - `more`: insert a `.te-more` button with `data-before` at the top.
     - `skipped`: insert a marker line, "⚠ events `<range>` skipped — Load earlier", with a button that fetches `/thinking/page?stream=&before=<to+1>` and inserts the result at the marker.
     - `newstream`: close the source, append a divider "— session restarted —", and reopen with no stream.
     - `error`: if `es.readyState === EventSource.CLOSED`, show "This session is no longer available to you"; else EventSource retries on its own.
  2. **`meThinkingAdd(node)`:**
     - set the icon by kind: thinking 💭, text 💬, tool_call ▶, result ─, gap ⚠, other ·;
     - add the tool to `.th-tool` if it isn't there yet;
     - **pairing:** for a `tool_result` with `data-tid`, find `.te[data-kind="tool_call"][data-tid=…]` in the list. If found, move the result's `.te-detail` into the call as `.te-out`, set the call's `.te-status` to `✓` or `✗` (from `data-error`) plus the duration `fmt(result.at - call.at)` (ms → "850ms", "2.1s", "1m 4s"), add `err` to the call when the result is an error, and do **not** append the result node. If not found, append it as a standalone line (Review Focus 1).
     - A call with no result yet shows `…` in `.te-status`.
     - A `result` node renders as a divider: the text is "turn N · $X · Ys", where X is the cost delta from the previous result of the same stream and turn, tracked in a JS map keyed by stream and turn, as in `Totals`.
     - Enforce the 2000-line cap by removing the oldest `.te` and ensuring a `.te-more` exists at the top (`data-before` = the new oldest seq).
     - Apply the current filters and search to the new node, then follow (item 5).
  3. **Filters** (`localStorage['jam-thinking'] = {f:{thinking,text,tools,errors,progress}, tool, format}`):
     - The chips toggle `aria-pressed` and set classes on `.th-list`: `hide-thinking`, `hide-text`, `hide-tools`, `only-errors`, `show-progress`.
     - CSS: `.th-list.hide-thinking .k-thinking{display:none}`, and the same for text and tools (`.k-tool_call,.k-tool_result`), and `.th-list:not(.show-progress) .k-progress{display:none}`. `.th-list.only-errors .k-tool_call:not(.err), .th-list.only-errors .k-tool_result:not(.err){display:none}`. Gap lines are never hidden.
     - Tool select: put `data-tool-filter` on the list, and hide lines whose `data-tool` doesn't match. Use JS that sets a `tool-miss` class on each line, re-run on change and on add.
     - Search: case-insensitive `textContent.includes(q)` toggles `search-miss`. It is not saved.
  4. **Format** (a class on `.th-list`: `fmt-narrative|fmt-detailed|fmt-raw`):
     - Narrative: clicking `.te-line` toggles `open` on the `.te`, which shows `.te-detail`, `.te-out` and `.te-note`. `.te-out` is clipped to 40 lines with a "show all" button that removes the clip. Clip in JS by splitting the text on `\n` and storing the full text in a closure.
     - Detailed: every `.te` is treated as open.
     - Raw: each visible `.te` without a `.te-raw` fetches `/thinking/raw?stream=&seq=` and puts the response in `<pre class="te-raw">` via `textContent`, cached; `.te-detail` is hidden.
  5. **Following:** before adding, record `atBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 16`. After adding, if `atBottom`, scroll to the end; otherwise increment the `.th-new` count and show it. Clicking it scrolls to the end and hides it. A `scroll` event reaching the bottom also hides it. A `.te-working` line, "working…" with the presence dots, sits last while the session head's `.sh-state` has class `busy`. Re-check on `htmx:afterSettle` of `#session-head`.
  6. **Load earlier:** clicking `.te-more` fetches `/thinking/page?stream=<data-stream of oldest .te>&before=<data-before>`. Parse the response with `<template>`, insert its `.te` nodes before the first `.te` via the same add path (pairing, filters) but without following, replace the button with the page's new `.te-more` if it has one, and keep the scroll position (`list.scrollTop += list.scrollHeight - before`).
  7. **Stream picker:** a change closes the source, clears the list and reopens with `?stream=` (empty = Current).
- **CSS:** compact monospace lines, matching the pane's styles. For example:

```css
.th{display:flex;flex-direction:column;height:100%}
.th-bar{display:flex;flex-wrap:wrap;gap:6px;padding-bottom:6px;border-bottom:1px solid var(--border)}
.th-list{flex:1;overflow:auto;font:12.5px/1.45 ui-monospace,monospace}
.te-line{cursor:pointer;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;padding:2px 0}
.te.err .te-status{color:var(--wait)}
.te-detail,.te-out,.te-raw{white-space:pre-wrap;margin:2px 0 6px 18px;max-height:none}
.k-result{border-top:1px solid var(--border);color:var(--muted);font-size:11.5px}
```

  `.te-detail` is shown when `.te.open` or `.fmt-detailed`, and hidden in `.fmt-raw`.

- [ ] **Step 1: Write the failing browser test**, `TestBrowserThinking`. Model it on `sessionpane_browser_test.go`: `sessionEnv`, `withStatic`, and `asPrincipal` in the server wrapper. Set `e.Deps.Events` and `e.Deps.Hub`. Append to stream S:
  - a thinking block;
  - a text block;
  - a Bash tool_use (`tu1`, `go test`) at t0;
  - its tool_result (ok) at t0+2100ms;
  - an Edit tool_use (`tu2`);
  - its tool_result (error);
  - a result (cost 0.12, 38000 ms).

  Navigate to `?c=<home>&tab=thinking`, then assert:
  - **(a)** the Bash line shows `✓` and `2.1s`. There is no separate tool_result line for tu1 (`.te[data-kind=tool_result]` count is 0). The Edit line has class `err` and `✗`.
  - **(b)** Clicking the Thinking chip hides `.k-thinking` (computed `display:none`). "Errors only" leaves only the Edit tool line visible among the tool lines.
  - **(c)** The tool select gets options Bash and Edit. Choosing Edit hides the Bash line.
  - **(d)** Typing `go test` in search leaves the Bash line visible and hides the text line.
  - **(e)** The Detailed format shows the Bash `.te-out`. The Raw format shows a `.te-raw` containing `"tool_use"`, fetched from the server.
  - **(f)** After a reload, the chip states and the format persist; search does not.
  - **(g)** Following: `Hub.Publish` a new text event while scrolled to the bottom → the list stays at the bottom. Scroll up, publish another → `.th-new` is visible with `1`.
  - **(h)** Load earlier: set the tail to 3 with a test hook, or append 305 events to a second stream and select it. The `.te-more` button prepends older lines.
  - **(i)** An orphan tool_result, whose call isn't loaded, renders as its own line.
- [ ] **Step 2: Run it and watch it fail.** `go test -tags browser ./internal/jam/userui/ -run BrowserThinking -v` fails.
- [ ] **Step 3: Implement** the JS and CSS in `layout.html`, keeping it in one block commented as "The Thinking tab".
- [ ] **Step 4: Run the tests.** The browser test should PASS. Run the full browser suite for regressions (`go test -tags browser ./internal/jam/userui/`), and also `go test ./internal/jam/userui/`.
- [ ] **Step 5: Commit.** Message: `feat(userui): Thinking tab filters, formats, pairing and following`.

### Task 7: Docs and policy, then PR 2

**Files:**
- Modify: `docs/usage/jam/session-events.md`.
  - The frontmatter `summary`/`owns` say "operator-only"; change them to "operator-only storage/export; content visible to a session's home-channel members in the Thinking tab".
  - Rewrite §Sensitivity: events carry tool inputs and outputs; **for now**, anyone who can see the session's home channel sees them in `/app/user`'s Thinking tab; the admin timeline and the export stay operator-only; cove-master's redaction applies; this remains a deliberate exception to observability rule 3. Keep the link.
  - §UI: add the Thinking tab with a link to `session-view.md#thinking`.
  - Bump `updated`.
- Modify: `docs/usage/jam/session-view.md`. Add a `## Thinking` section covering:
  - the tab;
  - the filters, tool, search and formats;
  - pairing, following and "Load earlier";
  - the stream picker and the "session restarted" divider;
  - the limits (300 tail, 64 KB detail, 1 MB raw, 4 streams, 2000 lines);
  - the routes table rows for `thinking/events|page|raw`.

  Update the frontmatter `owns` and `read_when`, the Pane tab list, and the "Cost and counts" sentence: "event content is shown in the Thinking tab". Bump `updated`.
- Modify: `docs/usage/jam/INDEX.md`. Update the session-view row's read-when to mention the Thinking tab.
- Modify: `docs/superpowers/specs/2026-10-24-session-thinking-view-design.md`.
  - Status: built (PRs #…).
  - Add the planning amendment: the templates stay with their UIs, and `Serve` takes render callbacks.

- [ ] **Step 1: Make the edits.**
- [ ] **Step 2: Check the docs.** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs | grep -E "session-view|session-events|ui-console|jam/INDEX"` must print no new errors. The repo has pre-existing spec-frontmatter and orphan errors; ignore those.
- [ ] **Step 3: Verify everything.** `bash scripts/lint.sh && go test ./... && go test -tags browser ./internal/jam/userui/`.
- [ ] **Step 4: Commit and open PR 2.** Message: `docs: the Thinking tab; session event content visible to members`.
