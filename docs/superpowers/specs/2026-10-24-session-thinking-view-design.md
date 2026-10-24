# Session view, part 2: the thinking view — design

**Status:** built — PR 1 #475 (sessionview, tail mode), PR 2 (the Thinking tab).
**Context:** sub-project 2 of the session view ([part 1, the shell](2026-10-24-session-view-shell-design.md); user doc [session-view.md](../../usage/jam/session-view.md)). A **Thinking** tab in the session pane shows what the session's agent is thinking and doing, live, beside the chat.

## Decisions (2026-10-24)

- **Who sees it:** a session's event content (thinking, text, tool inputs and outputs) is visible to **anyone who can see the session's home channel**, "for now". This reverses session-events.md's operator-only stance for content. cove-master's existing redaction stays. The admin timeline and the NDJSON export stay operator-only.
- **Purpose:** both following along and inspecting. A compact narrative is the default, and options choose **what** is shown (filters) and **how** (format).
- **Approach:** server-rendered event fragments (html/template, as in adminui today), filtered and formatted in the browser with CSS and a little JS. No client-side rendering of event content.

## Planning amendment

Fragment templates stay with their UIs (adminui, userui); `sessionview.Serve` takes render callbacks (`Source.Event` and `Source.Totals`) instead of owning templates.

## 1. Architecture

### `internal/jam/sessionview` (new; extracted from adminui)

- **`View(ev Event) EventView`.** The admin timeline's `viewOf` moves here and gains:
  - **`Kind`:** one of `thinking | text | tool_call | tool_result | result | progress | gap | other`.
  - **`Tool`:** the tool name.
  - **`ToolUseID`:** on a `tool_use` block, its `id`; on a `tool_result`, its `tool_use_id`. Used to pair a call with its result.
  - **`Narrative`:** a one-line summary. For a tool call it shows the tool's key input:
    - `Bash` → `command`;
    - `Read`/`Write`/`Edit`/`MultiEdit`/`NotebookEdit` → `file_path` (or `notebook_path`);
    - `Grep`/`Glob` → `pattern`;
    - `WebFetch` → `url`; `WebSearch` → `query`; `Task`/`Agent` → `description`;
    - anything else → its input JSON, clipped to 120 characters.

    For thinking or text it shows the first line, clipped to 160 characters. For a result it shows "turn N · $cost · duration".
  - **`At`:** the event's ObservedAt.
  - **Detail clip:** the detail is cut to **64 KB** with a `DetailTruncated` byte count. `View` still fills `Raw`: the admin fragment renders it, as today; the member fragment never does (members fetch raw on demand).
- **Fragment templates.** The `session-event` and `session-totals` templates move into the package and render through its `Fragment(name, data)`. The adminui session page keeps its own page template and includes the package's fragments. The member fragment (`thinking-event`) lives beside it in the package.
- **`Serve(w, r, Source)`.** The SSE handler moves here:
  - subscribe first, backfill from the store, then go live;
  - `Last-Event-ID: <stream>:<seq>` resume;
  - `stream`, `ev`, `totals` events and a 25s keep-alive, as today.

  `Source` carries the store, the hub, the actor id, a `Backfill` mode and the fragment to render. The **admin** mode replays the whole stream, as today. The **tail** mode sends the newest 300 events, then a `more` event carrying the lowest seq sent.
- **Store paging.** `sessionevents.Filter` gains `BeforeSeq uint64` and `Newest bool`. `Newest` with `Limit` returns the last `Limit` events matching the filter, still in ascending seq order. Implemented in MemStore and in the Postgres store (an `ORDER BY seq DESC LIMIT n` subquery, re-sorted).

adminui calls `sessionview` for its timeline. Its behaviour is unchanged, and golden tests pin its fragments.

### Member routes (internal/jam/userui)

Access is the pane's rule, `sessionFor(p, id)`: the session's home channel must be visible to the viewer, else 404. The check runs on every request, including every SSE reconnect.

| Route | Does |
|-------|------|
| `GET /app/user/session/{id}/thinking/events?stream=` | The SSE feed in tail mode. With no `stream`, it follows the newest stream. When a newer stream starts, it sends a `newstream` event, and the client reconnects to it with a "session restarted" divider. |
| `GET /app/user/session/{id}/thinking/page?stream=&before=` | An HTML fragment of the 300 events before `before` ("Load earlier"), plus the next `before` (none when exhausted). |
| `GET /app/user/session/{id}/thinking/raw?stream=&seq=` | One event's raw JSON (`text/plain`), capped at 1 MB with a truncation note. |
| `GET /app/user/session/{id}/pane?tab=thinking` | The tab's shell: toolbar, stream picker (`Streams`), the event list container. |

- **Stream cap:** at most **4** concurrent thinking streams per viewer (user id), counted in memory. A fifth gets 429 and the tab says "Too many open thinking views".
- **No event store** (session events aren't configured): the tab says "Session events aren't recorded on this jam". The events route answers 204, which stops EventSource.

## 2. The Thinking tab

- **Placement:** a fourth header tab, **Thinking**, in the existing pane. It gets the pane's behaviour: right or below, resize, per-session layout memory, `&tab=thinking`.
- **Feed lifecycle:** the tab opens an `EventSource` only while it is the open tab, and closes it on tab switch, pane close or page unload. The pane's 30s and push refresh do not reload this tab; it is live.
- **Toolbar:**
  - **Filter chips** (combinable): Thinking · Text · Tools · Errors only · Progress (off by default).
  - **Tool dropdown:** "All tools" plus the tool names seen so far.
  - **Search box:** filters to lines whose text contains the query, case-insensitive, live.
  - **Format switch:** Narrative · Detailed · Raw.
  - **Stream picker:** "Current" (the default; follows the newest stream) plus past streams by start time.

  The filters, tool choice and format are saved per browser in `localStorage["jam-thinking"]`, not per session. Search is not saved.
- **How filtering works:** each event fragment is an element carrying `data-kind`, `data-tool`, `data-error` and `data-tid` (the tool use id). The chips and the dropdown set classes on the list container, and CSS hides non-matching lines. Search toggles a `hidden` class per line. Gap and truncation markers are never hidden.
- **Narrative format** (the default):
  - One line per step: `💭` thinking, `💬` text, `▶ <Tool> <key input>` for a tool call.
  - **Tool results get no line of their own.** When a result arrives, JS finds the call with the same `data-tid` and moves the result's detail into it. The call line gains `✓` or `✗` and a duration (result `At` minus call `At`). A call still awaiting its result shows `…`.
  - Clicking a line expands it: the full thinking or text; the tool input, pretty-printed; and the output, clipped to 40 lines with "show all".
  - Each `result` renders as a thin divider: "turn N · $0.12 · 38s" (the turn's cost delta, as in `Totals`).
- **Detailed format:** the same lines, all expanded.
- **Raw format:** each line shows its event's raw JSON. It is fetched from `/thinking/raw` on first display and cached in memory.
- **Following:** while the list is scrolled to the bottom, new lines keep it there. Scrolling up pauses following and shows a "↓ N new" pill, which jumps back to the bottom. While the session is busy (the presence status is not idle), a "working…" line with the presence dots follows the last line.
- **Load earlier:** a button at the top fetches `/thinking/page?before=<lowest seq>` and prepends the result. Scroll position is kept. The button disappears when the stream is exhausted.

## 3. Errors, limits, testing, delivery

- **Losing access:** a reconnect after losing access gets 404. EventSource then errors, and the tab shows "This session is no longer available to you".
- **Session ends:** the hub stops sending. The tab keeps what it has, and the header's state shows the session ended.
- **Slow viewer:** the hub drops a subscriber whose 256-event buffer overflows, by closing its channel. The handler then ends the response. EventSource reconnects with `Last-Event-ID` and the backfill resumes from that seq. If more than 300 events were missed, it sends the newest 300 and a `skipped` marker carrying "Load earlier" for the hole.
- **Sizes:** detail is clipped to 64 KB per event on the server. Raw is capped at 1 MB. The browser keeps at most 2,000 lines; beyond that the oldest are dropped and "Load earlier" comes back.
- **Escaping:** all event content reaches the page through html/template or `textContent`, never `innerHTML` of event text. SSE framing uses the existing CR-safe `sseWrite`.
- **Tests** (TDD, hermetic):
  - `sessionview`: a table test of `View` over stream-json samples (thinking, text, each key-input tool, an unknown tool, tool_result with error, result, system/thinking_tokens, gap, truncation, unparseable); golden tests of the admin fragments before and after the move; and `Serve` tests for tail mode, resume, a dropped subscriber and `newstream`.
  - `sessionevents`: `BeforeSeq` and `Newest` tests on MemStore. The Postgres store runs the same cases in the store-integration suite.
  - `userui`:
    - SSE backfill then live through a Hub;
    - outsiders get 404 on every thinking route, and so does a reconnect after leaving the channel;
    - the raw route's cap;
    - the per-viewer stream cap (429);
    - the page route's paging.
  - Browser (`-tags browser`):
    - filters and the tool dropdown;
    - search;
    - format switch;
    - a call line pairing with its result (✓ and duration);
    - following, and the "↓ new" pill;
    - load earlier;
    - options remembered across a reload.
- **Docs:**
  - session-events.md §Sensitivity and §UI: content is now visible to home-channel members, "for now".
  - session-view.md: the Thinking tab.
  - ui-console.md: the admin timeline is built on the shared package. Its behaviour is unchanged.
- **Delivery:**
  - **PR 1:** extract `sessionview` and add the store paging, with no behaviour change.
  - **PR 2:** the Thinking tab, its routes and the policy and doc changes.
