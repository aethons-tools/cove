# VS Code slice 4 (the other panes) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Four more Jam views beside Conversation, plus whiteboards as editor tabs:
- **Thinking**: the session's live event feed, `web/app`'s `ThinkingView`;
- **Previews**: the session's exposed previews, opened through Remote-SSH port forwarding;
- **Status**: the session header's facts (state, mission, cost, context use) and actions, then the Status rows;
- **Artifacts**: whiteboards, files, asks and links shared in the session's home channel;
- **Boards**: one editor tab per whiteboard (several may be open, side by side), with `retainContextWhenHidden`.

Each view is an independent webview that follows the window's focused session with its own pin, exactly as Conversation does.

**Architecture:**
- **Same seams, two more.** `web/app` gains a *feed opener* seam in `useThinkingFeed` (`setFeedOpener`, the `EventSource` it opens today) and moves `BoardEditor`'s two raw `fetch`es onto `apiAt`, so the existing `setTransport` covers them. `Artifacts` images go through `useBlobUrl`. `SessionHeader` can render without its pane tabs. `HostContext` gains `openPreview`. With no setter called, the web app is unchanged.
- **One pane bundle, many panes.** The slice-3 bundle (`vite.vscode.config.ts` → `pane.js`) renders the pane named in `#root`'s `data-pane`. `Pane.tsx` becomes the shell every pane shares: the sign-in, no-session, gone and error states, the theme, a `QueryClient` per sign-in and target, `HostContext`, `BootContext` and the memory router with `NavBridge`. `panes.tsx` holds the five session bodies, and `BoardTab.tsx` the board editor.
- **Thinking streams are the host's.** The router keeps refusing `…/thinking/events`. A pane asks the host for a feed with `thinkingOpen`. The host's `ThinkingStreams` holds **one SSE connection per (session, stream)** that some Thinking pane in the window shows, replays what that connection has seen to a pane that joins late, resumes with `Last-Event-ID` after a network drop, and fans events out by subscription id. The pane side is an `EventSource`-shaped object over the bridge, so `useThinkingFeed`'s retry and 404 logic run unchanged.
- **Previews forward through VS Code's tunnel service.** A UI extension's `vscode.env.asExternalUri(http://localhost:<port>/)` in a remote window opens a Remote-SSH forward: the local extension host is started with the window's `remote.authority` (`localProcessExtensionHost.ts`), and `env.asExternalUri` passes `allowTunneling: !!initData.remote.authority` (`extHost.api.impl.ts`). This contradicts the API docs' "no-op on the client", so it is the checklist's first *Verify first* item. The forward happens only for the window's own studio; any other session's preview opens its preview URL in the browser (spec "Previews").
- **The host side is slice 3's, generalized.** The shared webview wiring in `ConversationViewProvider` (request-id mapping, router, hub, intents, sign-in, state refresh) moves to `panes/port.ts`. `panes/session.ts`'s `SessionPaneProvider` serves all five views, and `panes/board.ts`'s `BoardPanels` serves the editor tabs. Intents gain `preview`, `reveal` and `session`. `board` now opens the editor tab instead of the browser. `panes/actions.ts` performs them.

**Tech Stack:** as slice 3. Also `vscode.window.createWebviewPanel` and `registerWebviewPanelSerializer`, `vscode.env.asExternalUri`, and the built-in Simple Browser (`simpleBrowser.show`).

**Spec:** `docs/superpowers/specs/2026-10-28-vscode-code-window-design.md` ("In the window", "Focus and pin", "Anti-goals" → *Not N live connections*, "Data flow", "Previews", "Left to the builder"). This is slice 4 of 5. It consumes slice 3 (`docs/superpowers/plans/2026-10-29-vscode-3-conversation.md`): `protocol.ts`, `Bridge`/`install`, `linkAction`, `NavBridge`, `Pane`, `Router`/`checkPath`/`ID_RE`, `intentAction`, `LiveStream`/`SseParser`, `PaneHub`, `FocusModel`, `FocusReporter`, `webviewHtml`/`newNonce`, `ConversationViewProvider`.

**Out of scope, slice 5 (notifications):** the server's `event: notify`, VS Code and OS notifications, the activity-bar badge and the notify lock file. `PaneHub` still drops `notify`.

## Global Constraints

- **The web app is unchanged.** With no setter called and no `HostContext.openPreview`, every request, `EventSource`, image and link is the same as before. Every existing `web/app` test passes. The only edits to existing tests are the ones Tasks 1 and 3 name. `npm run size` reports the same two areas.
- **The token never enters a webview.** Slice 3's rules hold for every view and every board tab: no message carries a token, `jam.url` or a cookie, and the CSP is exactly
  `default-src 'none'; script-src 'nonce-<n>' <cspSource>; style-src <cspSource> 'unsafe-inline'; font-src <cspSource>; img-src <cspSource> blob: data:; connect-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'`.
  - Options are `enableScripts: true`, `enableCommandUris: false`, `localResourceRoots: [<ext>/dist/webview]`.
  - `retainContextWhenHidden: true` is set on board panels only. Views never set it (spec: "only … the board editor").
- **The proxy is unchanged.** It still refuses `/app/user/events` and `…/thinking/events`. Only `ThinkingStreams` opens a Thinking stream, with `checkPath(path, jamUrl, { streams: true })`.
- **Thinking streams:**
  - one connection per key `(session, stream)`, where `stream` is `""` (follow the newest) or a pinned stream id. It opens with the key's first subscriber and aborts with its last;
  - the path is `/app/user/session/<ses>/thinking/events`, plus `?stream=<id>` when `stream` is set. The headers are `Authorization: Bearer`, `Accept: text/event-stream` and `Last-Event-ID` when resuming;
  - forwarded events are only `open` (synthetic), `stream`, `totals`, `ev`, `more`, `skipped` and `newstream`;
  - the replay buffer holds `THINKING_REPLAY_MAX = 2000` `ev` events per key;
  - backoff and stall are `LIVE_BACKOFF_MIN_MS`, `LIVE_BACKOFF_MAX_MS` and `LIVE_STALL_MS` from `live.ts`;
  - `THINKING_SUBS_PER_PANE = 2`.
- **Previews:**
  - a port is forwarded only when the intent's session equals the *window's studio*, which is `studioFromAuthority(windowAuthority(workspace))`, never the focused session;
  - otherwise the preview's own `url` opens in the browser, `http:`/`https:` only;
  - `port` is an integer in 1–65535.
- **Ids** use `ID_RE` (slice 3). Stream ids are `^[0-9a-f]{32}$` (`sessionevents.ValidStreamID`). A reveal anchor is `^ask-[A-Za-z0-9_-]{1,80}$`.
- **Copy:**
  - Signed out, per pane:
    - conversation keeps "Sign in to Jam to see this conversation.";
    - the other session panes say "Sign in to Jam to see this session's <noun>.";
    - a board says "Sign in to Jam to see this whiteboard.";
    - all three offer [Sign in].
  - No session: "This window isn't on a Jam studio. Focus a session to see its <noun>." [Focus a session…].
  - Gone: "That session has ended, or you can't see it." [Focus a session…].
  - The nouns are `conversation`, `thinking`, `previews`, `status` and `artifacts`.
  - View names: "Conversation", "Thinking", "Previews", "Status", "Artifacts".
  - Preview buttons:
    - "Open", titled "Open in VS Code's Simple Browser";
    - "Browser", titled "Open in your browser".
  - Board:
    - a board you can't read: "This whiteboard isn't available to you.";
    - the bar's link: "Open in the web app";
    - the tab title is the board's title (at most 60 chars), else "Whiteboard".
  - A failed forward: "Couldn't forward port <port>: <message>".

## Review Focus

1. **Several Thinking panes in one window.** Two panes on the same session, one pinned to another session, then a third joining late. There is one server stream per (session, stream). The late pane shows the same tail as the others, not an empty list. One window never uses more than one of the user's four feeds (`maxThinkingStreams = 4`) per session it shows. Task 4 tests sharing and replay.
2. **A preview for a session that isn't this window's studio.** Focus or pin a pane on another session, then press Open. Its preview URL opens in the browser. A `localhost` port is never forwarded, because that would land on *this* studio's port. Task 5 tests a table.
3. **A compromised pane** posts:
   - `thinkingOpen` with `../x`, a `chn_` id, a non-hex stream, or 50 subscriptions;
   - a `preview` intent with port `0`, `70000`, `"22"` or a `javascript:` URL;
   - `reveal` with anchor `"x\" onmouseover="`;
   - `session` with a bad id.

   Each is refused, and no fetch, forward or command follows. Tasks 5 and 6 test it.
4. **The session ends while panes show it** (Release from Status). Every session pane switches to the gone copy, from its own `sessions/<id>` 404, with no error text or retry loop. Thinking shows "This session is no longer available to you". Task 3 tests it.
5. **Board tabs:**
   - the same board opened twice, from a card and from Artifacts, reveals one tab;
   - a window reload restores the open boards;
   - signing out shows Sign in in the tab, and signing in again resumes it;
   - an id that fails `ID_RE.brd` opens nothing.

   Task 7 tests it.

---

### Task 1: web/app seams for the other panes

**Files:**
- Create: `web/app/src/boards/api.ts`, `web/app/src/BlobImage.tsx`, `web/app/src/session/PaneTabs.test.tsx`.
- Modify:
  - `src/session/thinking/useThinkingFeed.ts`;
  - `src/boards/BoardEditor.tsx`, so both `fetch`es go through `boards/api.ts`;
  - `src/session/PaneTabs.tsx`, so Artifacts images use `BlobImage` and Previews use `HostContext.openPreview`;
  - `src/session/SessionHeader.tsx`;
  - `src/host.ts`.
- Test: `useThinkingFeed.test.tsx`, `boards/boards.test.tsx`, `PaneTabs.test.tsx`, `SessionView.test.tsx` (unchanged, must pass).

**Interfaces:**
- Produces:
  ```ts
  // session/thinking/useThinkingFeed.ts
  export type FeedSource = { readonly readyState: number;                 // 0 connecting, 1 open, 2 closed (EventSource's)
                             addEventListener(type: string, fn: (e: { data: string }) => void): void; close(): void }
  export type FeedOpener = (url: string) => FeedSource
  export const eventSourceFeed: FeedOpener                                 // (u) => new EventSource(u)
  export function setFeedOpener(o?: FeedOpener): void                      // undefined restores eventSourceFeed
  export const FEED_CLOSED = 2                                             // replaces EventSource.CLOSED in the error handler
  // boards/api.ts (apiAt(`/app/user/boards/${enc(id)}/`))
  export function loadElements(id: string, since: number): Promise<{ seq: number; elements: unknown[] }>   // ApiError → Error(`load failed (${status})`)
  export function saveElements(id: string, batch: Snap[]): Promise<{ lost: unknown[] }>                   // body {elements: batch.map(b => JSON.parse(b.json))}; ApiError → Error(`save failed (${status})`)
  // BlobImage.tsx
  export function BlobImage(p: { path: string } & Omit<ImageProps, "src"> & { alt: string; loading?: "lazy" }): JSX.Element // src = useBlobUrl(path)
  // host.ts
  export type Host = { signIn?: () => void; openPreview?: (session: string, v: PreviewLink, inBrowser: boolean) => void }
  // SessionHeader.tsx
  export function SessionHeader(p: { head: SessionHead; tab?: PaneTab | null; onTab?: (t: PaneTab) => void }): JSX.Element // no onTab → no tablist
  ```
  - `useThinkingFeed`'s `Conn.es` becomes `FeedSource | null`, and `open` calls the current opener. Nothing else in the hook changes.
  - With `openPreview` set, each Previews row is the name with "port N", plus the Open and Browser buttons (copy), which call `openPreview(p.id, v, false|true)`. It has no `href` and no View here. Without it, the rows are today's.

- [ ] **Step 1: Write the failing tests:**
  - feed: `a custom opener receives the events URL and drives the feed`:
    - `setFeedOpener(fake)`, render `ThinkingView` with `userThinkingSource("ses_…")`;
    - the fake got `/app/user/session/ses_…/thinking/events`;
    - emitting `stream`, then `ev` for `tev(1)` shows "said 1";
    - `readyState = 2` plus `error` calls `check()`;
    - `setFeedOpener()` restores `EventSource`.
  - boards: `the editor loads and saves through the API transport`:
    - `setTransport(fake)`, mount `BoardEditor`;
    - the fake sees `GET /app/user/boards/brd_…/elements?since=0`;
    - after a change, `POST …/elements` with `json.elements` the changed elements;
    - a `409` gives the text "save failed (409)".
  - PaneTabs:
    - `artifacts images resolve through the blob resolver`: `setBlobResolver` → `blob:1`, and the thumb and image `src` are `blob:1`;
    - `previews call openPreview when the host offers it`: the Open click → `(id, v, false)`, the Browser click → `(id, v, true)`, with no `a[href]` and no View here;
    - `without a host, previews are today's links`.
  - header: `without onTab the header has no pane tabs` (`queryByRole("tablist")` is null, and "Interrupt" shows).
- [ ] **Step 2:** `cd web/app && npx vitest run src/session src/boards` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/app && npm run check && npm run build && npm run size` → PASS, and `size` prints the same two areas.
- [ ] **Step 5: Commit** `refactor(app): seams for Thinking feeds, board sync, artifact images and previews`.

### Task 2: Bridge protocol and the pane side of feeds, reveal and session links

**Files:**
- Modify: `web/app/src/bridge/protocol.ts`, `src/vscode/bridge.ts`, `src/vscode/links.ts`, `src/vscode/fakeHost.ts`.
- Test: `src/vscode/bridge.test.ts`, `src/vscode/links.test.ts`.

**Interfaces:**
- Consumes: Task 1's `FeedSource`, `FeedOpener`, `setFeedOpener`.
- Produces, in `protocol.ts`:
  ```ts
  export type SessionPaneName = "conversation" | "thinking" | "previews" | "status" | "artifacts"
  export type PaneName = SessionPaneName | "board"
  export type ThinkingEventName = "open" | "stream" | "totals" | "ev" | "more" | "skipped" | "newstream"
  // OpenIntent adds:
    | { kind: "preview"; session: string; port: number; url: string; inBrowser: boolean }
    | { kind: "reveal"; anchor?: string }                     // show the Conversation view, then scroll to anchor
    | { kind: "session"; id: string }                         // open that session's code window (the extension link)
  // PaneToExt adds:
    | { type: "thinkingOpen"; sub: number; session: string; stream: string }
    | { type: "thinkingClose"; sub: number }
    | { type: "title"; title: string }                        // board tabs only
  // ExtToPane adds:
    | { type: "thinking"; sub: number; event: ThinkingEventName; data: string }
    | { type: "thinkingError"; sub: number; closed: boolean }
    | { type: "scrollTo"; anchor: string }
  ```
- Produces, in `bridge.ts`:
  ```ts
  export interface VsApi { postMessage(m: PaneToExt): void; setState(s: unknown): void }
  Bridge.onThinking(sub: number, cb: (m: { event: ThinkingEventName; data: string } | { error: true; closed: boolean }) => void): () => void
  Bridge.onScrollTo(cb: (anchor: string) => void): () => void
  export function parseThinkingUrl(url: string): { session: string; stream: string } | undefined
  export function bridgeFeedOpener(b: Bridge): FeedOpener
  // install(b, host) also calls setFeedOpener(bridgeFeedOpener(b))
  ```
  - `parseThinkingUrl` accepts only `/app/user/session/<ses>/thinking/events`, with an optional `?stream=<32 hex>`.
  - **`bridgeFeedOpener`'s source:**
    - it posts `thinkingOpen` with the next sub id and starts at `readyState` 0;
    - `open` sets 1;
    - each event dispatches `{data}` to that name's listeners;
    - `thinkingError` sets 2 when `closed` (else 0) and dispatches `error`;
    - `close()` posts `thinkingClose` once and sets 2, and later messages for that sub are ignored;
    - a URL that doesn't parse gives a source already at 2, which dispatches `error` on the next microtask and posts nothing.
  - **`linkAction`:** `vscode://aethons.jam/session/<ses>` and `vscode-insiders://aethons.jam/session/<ses>` → `{ intent: { kind: "session", id } }`. Every other `vscode:` link is still dropped.

- [ ] **Step 1: Write the failing tests:**
  - bridge:
    - `parseThinkingUrl`, a table: the plain URL → `{session, stream:""}`; with `?stream=<32 hex>` → that stream; `/app/user/session/x/thinking/events`, `?stream=../x`, `/app/user/events` → undefined.
    - `the feed opener posts thinkingOpen and dispatches by sub`: two sources, events for sub 1 reach only the first.
    - `a closed error sets readyState 2 and fires error; a transient one sets 0`.
    - `close posts thinkingClose once and ignores later events`.
    - `scrollTo reaches onScrollTo`.
  - links:
    - `vscode://aethons.jam/session/ses_…` → session;
    - `vscode://aethons.jam/session/../x` → drop;
    - `vscode://other/x` → drop;
    - the slice-3 table is unchanged.
- [ ] **Step 2:** `cd web/app && npx vitest run src/vscode` → FAIL.
- [ ] **Step 3:** Implement. `fakeHost.ts` gains `setState` and a way to answer `thinkingOpen`.
- [ ] **Step 4:** `cd web/app && npm run check` → PASS. `cd web/vscode && npm run typecheck` → PASS (the protocol is shared).
- [ ] **Step 5: Commit** `feat(app): bridge protocol for Thinking feeds, previews, reveal and session links`.

### Task 3: The pane shell and the Thinking, Previews, Status, Artifacts and board panes

**Files:**
- Create: `web/app/src/vscode/panes.tsx`, `web/app/src/vscode/BoardTab.tsx`, `web/app/src/vscode/panes.test.tsx`, `web/app/src/vscode/BoardTab.test.tsx`.
- Modify: `src/vscode/Pane.tsx`, `src/vscode/main.tsx`, `src/vscode/Pane.test.tsx` (the only edit is passing `name="conversation"`).

**Interfaces:**
- Consumes: Tasks 1–2; `PaneBody`, `useSessionHead`, `SessionHeader`, `ConversationView`, `BoardEditor` (lazy).
- Produces:
  ```ts
  // Pane.tsx
  export const NOUN: Record<SessionPaneName, string>
  export function copyFor(name: PaneName): { signedOut: string; none: string; gone: string }  // the Constraints' copy
  export function Pane(p: { bridge: Bridge; name: PaneName; board?: string }): JSX.Element
  // panes.tsx
  export function SessionBody(p: { bridge: Bridge; name: SessionPaneName; session: string; channel: string }): JSX.Element
  export function SessionGate(p: { session: string; name: SessionPaneName; onFocus: () => void; children: ReactNode }): JSX.Element
  // BoardTab.tsx
  export function BoardTab(p: { bridge: Bridge; id: string }): JSX.Element
  ```
  - **`Pane`** keeps slice 3's states and providers for every name:
    - a session pane renders `SessionBody` inside the memory router at home `/app/user/c/<channel>`;
    - `board` ignores `target` and renders `BoardTab` once signed in;
    - `retry` posts `ready` with its own `name`;
    - `onLeave` for a pane other than conversation turns a `scrollTo` action into `{kind:"reveal", anchor}`.
  - **`SessionGate`** (every session pane but conversation): `useSessionHead(session)` with `not_found` → the gone copy with [Focus a session…]. Otherwise it renders `children`.
  - **`SessionBody`:**
    - conversation is slice 3's `ConversationView withSession={false}`;
    - thinking is `PaneBody tab="thinking" busy={head.state.class === "busy"}`, in a full-height box;
    - previews is `PaneBody tab="previews"` under `HostContext` with `openPreview`, which posts `{kind:"preview", session, port: v.port, url: v.url, inBrowser}`;
    - status is `<SessionHeader head />` (no tabs), then `PaneBody tab="status"`;
    - artifacts is `PaneBody tab="artifacts" onAsk={(id) => post reveal "ask-"+id}`.
  - **`BoardTab`:**
    - it queries `boards/<id>` (`BoardInfo`) and posts `{type:"title", title}` once loaded;
    - a 404 shows the board copy;
    - otherwise a bar shows the title, a "view only" badge when `readOnly`, and "Open in the web app" (posts `{kind:"web", path:"/app/user/boards/<id>"}`), above the lazy `BoardEditor`.
  - **`main.tsx`:**
    - it reads `data-pane`, `data-board` and `data-assets`;
    - it sets `window.EXCALIDRAW_ASSET_PATH = dataset.assets` before rendering, and `vsApi.setState({ board })` for a board;
    - it wires `onScrollTo` to `getElementById(anchor)?.scrollIntoView({behavior:"smooth", block:"center"})`;
    - it posts `ready` with the name;
    - it does not import `boards/assetPath.ts`.

- [ ] **Step 1: Write the failing tests** (`fakeHost` routes):
  - panes:
    - `thinking renders the session's feed over the bridge`: `thinkingOpen {session:"ses_a", stream:""}` is posted, and an `ev` message for `tev(3)` shows "said 3". `GET /app/user/api/sessions/ses_a/pane/thinking` is requested, and no request path contains `/thinking/events`.
    - `previews Open posts a preview intent for the pane's session`, and `Browser` posts the same with `inBrowser: true`.
    - `status shows the header without tabs and the status rows; Interrupt posts sessions/ses_a/interrupt`.
    - `artifacts ask click posts reveal ask-<id>; a board thumb click posts a board intent`.
    - `every session pane shows its own signed-out and no-session copy` (`it.each` over the five names).
    - `a session head 404 shows the gone copy in thinking, previews, status and artifacts` (Review Focus 4).
    - `a new target switches the pane's session` (status: `ses_a` → `ses_b` labels).
  - BoardTab:
    - `loads the board, posts its title and mounts the editor` (Excalidraw mocked as in `boards.test.tsx`);
    - `a 404 says the whiteboard isn't available`;
    - `Open in the web app posts a web intent`;
    - `signed out shows the board sign-in copy`.
  - The existing `Pane.test.tsx` cases pass.
- [ ] **Step 2:** `cd web/app && npx vitest run src/vscode` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/app && npm run check && npx vite build -c vite.vscode.config.ts` → PASS. `grep -c esm.sh ../vscode/dist/webview -r` → 0.
- [ ] **Step 5: Commit** `feat(app): Thinking, Previews, Status, Artifacts and board panes in the VS Code bundle`.

### Task 4: Host Thinking streams (one per session and stream, shared and replayed)

**Files:**
- Create: `web/vscode/src/thinking.ts`, `web/vscode/test/thinking.test.ts`.
- Modify: `web/vscode/src/live.ts` (`SseParser` keeps the last event id), `web/vscode/test/live.test.ts`.

**Interfaces:**
- Consumes: `LiveDeps`, `SseParser`, the backoff constants, `checkPath` and `ID_RE`.
- Produces:
  ```ts
  // live.ts
  export type LiveEvent = { event: string; data: string; id?: string }   // id: the SSE last-event-id when one was set
  // thinking.ts
  export const THINKING_REPLAY_MAX = 2000
  export const STREAM_RE = /^[0-9a-f]{32}$/
  export type ThinkingSignal = { event: ThinkingEventName; data: string } | { error: true; closed: boolean }
  export class ThinkingStreams {
    constructor(d: LiveDeps)
    subscribe(session: string, stream: string, cb: (s: ThinkingSignal) => void): () => void
    restart(): void                     // sign-in/out: every key ends ({error, closed:true}) and is dropped
    get connections(): number           // keys with a live or retrying connection (tests)
  }
  ```
  - **`subscribe`:**
    - it throws on a session failing `ID_RE.ses`, or a stream that is neither `""` nor `STREAM_RE`;
    - the first subscriber of a key connects;
    - a later one gets `open`, then the key's replay, on the next microtask;
    - the last unsubscribe aborts the connection and drops the key.
  - **Replay** keeps, in arrival order:
    - the latest `stream`;
    - the `skipped` and `ev` events, with at most `THINKING_REPLAY_MAX` `ev`. Past that the oldest go, and a synthetic `more` carries the oldest kept `ev`'s `seq` (from its JSON);
    - the server's `more`, unless a synthetic one replaced it;
    - the latest `totals`.
  - **Outcomes:**
    - `200`: emit `open`, then forward the allowed events, remembering the last `id`;
    - stall (`LIVE_STALL_MS`), network error or end of body: `{error, closed:false}`, then reconnect after the backoff with `Last-Event-ID`, keeping the replay;
    - `401`: `onSignedOut()` once per token, then `{error, closed:true}`, and the key is dropped;
    - any other status: `{error, closed:true}`, and the key is dropped with no retry. The pane's own `check()` and backoff decide what comes next, as with `EventSource`;
    - `newstream`: forwarded, then the key ends (no reconnect) and is dropped. A later subscribe connects afresh;
    - not signed in: `{error, closed:true}` without a fetch.

- [ ] **Step 1: Write the failing tests** (fake timers, a fake fetch with a controllable body):
  - parser: `id: s1:5` before `event: ev` gives `id "s1:5"`, and the id persists to the next event.
  - thinking:
    - `two subscribers of one key share one fetch with bearer and Accept` (Review Focus 1);
    - `different streams of one session are different keys`;
    - `a late subscriber gets open, stream, the evs and the latest totals`;
    - `replay past 2000 evs keeps the newest 2000 and a more at the oldest kept seq`;
    - `a stall reconnects with Last-Event-ID and the subscribers see a transient error`;
    - `a 429 is a closed error with no retry`;
    - `401 calls onSignedOut once and closes`;
    - `newstream is forwarded, then the key ends without reconnecting`;
    - `last unsubscribe aborts`;
    - `restart closes every key`;
    - `a bad session or stream throws` (Review Focus 3);
    - `only the allowed events are forwarded` (a `message` and a `notify` aren't).
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/thinking.test.ts test/live.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): one Thinking stream per session and stream, shared by the window's panes`.

### Task 5: Intents and actions (boards, previews, reveal, session)

**Files:**
- Create: `web/vscode/src/panes/actions.ts`, `web/vscode/test/actions.test.ts`.
- Modify: `web/vscode/src/bridge/intents.ts`, `web/vscode/test/intents.test.ts`.

**Interfaces:**
- Consumes: Task 2's intents, `ID_RE` and `checkPath`.
- Produces:
  ```ts
  // intents.ts
  export const ANCHOR_RE = /^ask-[A-Za-z0-9_-]{1,80}$/
  export type IntentAction =
    | { openExternal: string } | { command: "jam.focusSession" }
    | { openBoard: string } | { forwardPort: number; inBrowser: boolean }
    | { revealConversation: string | null } | { openSession: string }
  export function intentAction(i: OpenIntent, jamUrl: string, windowStudio?: string): IntentAction | Error
  // actions.ts
  export type ActionDeps = {
    openExternal: (url: string) => unknown; executeCommand: (c: string, ...a: unknown[]) => Thenable<unknown>;
    asExternalUri: (url: string) => Promise<string>; openBoard: (id: string) => void; openSession: (id: string) => unknown;
    scrollConversation: (anchor: string) => void; error: (message: string) => void }
  export async function perform(a: IntentAction, d: ActionDeps): Promise<void>
  ```
  - **`intentAction`** changes:
    - `board` → `{openBoard}`, replacing slice 3's `openExternal`;
    - `preview`:
      - the session must match `ID_RE.ses`, and the port is a `number`, an integer, 1–65535;
      - when `session === windowStudio`, it gives `{forwardPort, inBrowser}`;
      - otherwise it is the `external` rule on `url` (`http:`/`https:` only, not `mailto:`), giving `{openExternal}`;
    - `reveal` → `{revealConversation: anchor ?? null}`, where the anchor must match `ANCHOR_RE`;
    - `session` → `{openSession}`, where the id must match `ID_RE.ses`;
    - the other kinds are unchanged.
  - **`perform`:**
    - `forwardPort`: `u = await asExternalUri("http://localhost:<port>/")`. With `inBrowser` it calls `openExternal(u)`, else `executeCommand("simpleBrowser.show", u)`. A throw → `error("Couldn't forward port <port>: <message>")`;
    - `revealConversation`: `executeCommand("jam.conversation.focus")`, then `scrollConversation(anchor)` when one is set;
    - `openBoard`, `openSession`, `openExternal` and `command` call their deps.
    - Errors from deps are caught, and the action is shown through `error`.

- [ ] **Step 1: Write the failing tests:**
  - intents, a table (Review Focus 2 and 3), with `windowStudio = ses_W`:
    - preview `ses_W`, 5173 → `{forwardPort:5173, inBrowser:false}`;
    - preview `ses_O`, 5173, `https://p.example/x` → `{openExternal:"https://p.example/x"}`;
    - preview `ses_O`, `javascript:alert(1)` → Error;
    - ports `0`, `70000`, `1.5` and `"22"` → Error;
    - preview with no `windowStudio` → `openExternal`;
    - board `brd_…` → `{openBoard}`;
    - reveal `ask-ask_1` → that anchor, reveal with no anchor → null, `x" onmouseover="` → Error;
    - session `ses_…` → `{openSession}`, `../x` → Error;
    - the slice-3 rows are kept, except board.
  - actions:
    - `forward opens the Simple Browser on the external URI`: asExternalUri → `http://localhost:61234/`, so `simpleBrowser.show` gets it;
    - `inBrowser opens it externally`;
    - `a failing forward shows the copy`;
    - `reveal focuses the Conversation view, then scrolls`;
    - `openBoard and openSession reach their deps`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/intents.test.ts test/actions.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): open intents for boards, previews over port forwarding, reveal and sessions`.

### Task 6: Session pane views (shared webview port, five views, manifest)

**Files:**
- Create: `web/vscode/src/panes/port.ts`, `web/vscode/src/panes/session.ts`, `web/vscode/test/port.test.ts`, `web/vscode/test/session.test.ts`.
- Delete: `web/vscode/src/panes/conversation.ts`, and move `web/vscode/test/conversation.test.ts` into `session.test.ts`, keeping every case for `"conversation"`.
- Modify: `web/vscode/package.json`, `src/extension.ts`, `test/vscode.ts` (the mock gains `env.asExternalUri`), `test/manifest.test.ts`, `test/extension.test.ts`.

**Interfaces:**
- Consumes: Tasks 4–5; slice 3's `Router`, `PaneHub`, `FocusModel`, `FocusReporter`, `Auth` and `webviewHtml`.
- Produces:
  ```ts
  // port.ts
  export const THINKING_SUBS_PER_PANE = 2
  export type PortDeps = { hub: Pick<PaneHub, "attach">; router: Pick<Router, "handle">; thinking: Pick<ThinkingStreams, "subscribe">;
                           jamUrl: () => string | Error; windowStudio: string | undefined;
                           perform: (a: IntentAction) => void; executeCommand: (c: string, ...a: unknown[]) => Thenable<unknown> }
  export type PaneHooks = { state: () => Promise<PaneState>; onViewing?: (channel: string | null) => void; onTitle?: (title: string) => void }
  export function wirePane(w: vscode.Webview, id: string, d: PortDeps, h: PaneHooks):
    { post(m: ExtToPane): void; refresh(): Promise<PaneState | undefined>; dispose(): void }
  export function paneHtml(w: vscode.Webview, extensionUri: vscode.Uri, pane: PaneName, extra?: { board?: string }): string // options + webviewHtml
  // session.ts
  export const SESSION_PANES: readonly SessionPaneName[]   // ["conversation", "thinking", "previews", "status", "artifacts"]
  export type SessionPaneDeps = PortDeps & { extensionUri: vscode.Uri; model: Pick<FocusModel, "stateOf" | "isPinned" | "windowSession" | "onChange">;
                                             reporter: Pick<FocusReporter, "pane" | "drop">; auth: Pick<Auth, "session" | "onChange"> }
  export async function sessionPaneState(d: SessionPaneDeps, view: string): Promise<PaneState>  // slice 3's ConversationViewProvider.state, moved
  export class SessionPaneProvider implements vscode.WebviewViewProvider {
    constructor(name: SessionPaneName, d: SessionPaneDeps)
    readonly viewType: string                // `jam.${name}`
    scrollTo(anchor: string): void           // posts {type:"scrollTo"} to this provider's live views
  }
  ```
  - **`wirePane` handles messages:**
    - slice 3's `ready`, `request`, `abort`, `open` and `signIn`;
    - `open` → `intentAction(intent, url, d.windowStudio)` → `d.perform`;
    - `viewing` → `onViewing`; `title` → `onTitle`;
    - `thinkingOpen` → `d.thinking.subscribe`, forwarding signals as `thinking`/`thinkingError` with the pane's sub id. A bad session or stream, a non-integer sub, a reused sub or more than `THINKING_SUBS_PER_PANE` open subs → `{type:"thinkingError", sub, closed:true}`, with no subscribe;
    - `thinkingClose` → unsubscribe;
    - `dispose` aborts the pane's requests, unsubscribes its feeds and detaches from the hub.
  - **`SessionPaneProvider`** is slice 3's provider for any name:
    - `view.description` is the label when `elsewhere`;
    - the context keys are `jam.<name>.pinned` and `jam.<name>.hasSession`;
    - only `"conversation"` reports `viewing` to the reporter, and other names ignore it.
  - **`package.json`:**
    - `views.jam` holds `jam.conversation`, `jam.status`, `jam.artifacts` and `jam.previews`, the last two with `"visibility": "collapsed"`;
    - `viewsContainers.panel: [{ id: "jamPanel", title: "Jam", icon: "media/jam.svg" }]` with `views.jamPanel: [{ type: "webview", id: "jam.thinking", name: "Thinking" }]`;
    - commands `jam.<name>.pin` ("Pin to This Session", `$(pin)`) and `jam.<name>.unpin` ("Follow the Window's Session", `$(pinned)`) for each of the five names;
    - `view/title` menus with slice 3's `when` clauses per name, and all ten hidden from `commandPalette`.
  - **`extension.ts`:**
    - it builds `ThinkingStreams` (the shared `current` and `panesSignedOut`), with `thinking.restart()` on `auth.onChange`;
    - `windowStudio = windowDefault`;
    - `perform` gets `ActionDeps`:
      - `asExternalUri: async (u) => (await vscode.env.asExternalUri(vscode.Uri.parse(u))).toString()`;
      - `openSession: (id) => guard(() => openSession(openDeps, id))()`;
      - `scrollConversation` → the conversation provider's `scrollTo`;
      - `error` → `showErrorMessage`;
      - `openBoard` opens `<jam>/app/user/boards/<id>` in the browser until Task 7.
    - It registers the five providers and ten pin commands.

- [ ] **Step 1: Write the failing tests:**
  - port, on the mock webview:
    - `thinkingOpen subscribes and forwards events under the pane's sub id`;
    - `a third open sub is refused with a closed error` (Review Focus 3);
    - `a bad session, a non-hex stream or a reused sub is refused without subscribing`;
    - `dispose unsubscribes every feed and detaches`;
    - `open preview for the window's studio performs forwardPort`;
    - `open javascript: performs nothing`.
  - session:
    - the slice-3 conversation cases, verbatim, now on `new SessionPaneProvider("conversation", …)`;
    - `each view type is jam.<name> and sets its own context keys` (`it.each`);
    - `a pinned status view shows the label in its description`;
    - `viewing from a thinking view doesn't reach the reporter`;
    - `scrollTo reaches only that provider's views`;
    - `no view sets retainContextWhenHidden`.
  - manifest:
    - the five views, with thinking in the `jamPanel` container;
    - the ten pin commands with their menus;
    - every contributed command is registered by `activate`.
  - extension: `activate registers five view providers and fifteen commands`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `just vscode` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): Thinking, Previews, Status and Artifacts views that follow the focused session`.

### Task 7: Board editor tabs

**Files:**
- Create: `web/vscode/src/panes/board.ts`, `web/vscode/test/board.test.ts`.
- Modify:
  - `src/panes/html.ts`, which adds `data-board` and `data-assets`;
  - `src/panes/port.ts` (`paneHtml` passes `assets`);
  - `src/extension.ts`;
  - `package.json`, whose `activationEvents` add `onWebviewPanel:jam.board`;
  - `test/vscode.ts`, whose mock adds `window.createWebviewPanel`, `registerWebviewPanelSerializer` and `ViewColumn`;
  - `test/html.test.ts`, `test/manifest.test.ts`;
  - `scripts/vscode.sh`.

**Interfaces:**
- Consumes: Task 6's `wirePane` and `paneHtml`; Task 3's `BoardTab` (`data-board`, `title`, `setState`).
- Produces:
  ```ts
  // html.ts
  export function webviewHtml(o: { cspSource: string; nonce: string; script: string; style: string; pane: PaneName; board?: string; assets?: string }): string
  // board.ts
  export const TITLE_MAX = 60
  export type BoardDeps = PortDeps & { extensionUri: vscode.Uri; auth: Pick<Auth, "session" | "onChange">;
                                       createPanel: typeof vscode.window.createWebviewPanel }
  export class BoardPanels {
    static readonly viewType = "jam.board"
    constructor(d: BoardDeps)
    open(id: string): void                                       // reveal the tab for id, else a new one in the active column
    restore(panel: vscode.WebviewPanel, state: unknown): void    // the serializer: {board} must pass ID_RE.brd, else dispose
  }
  export function tabTitle(t: string): string                    // control chars dropped, trimmed, ≤ TITLE_MAX, "" → "Whiteboard"
  ```
  - **Panel options:** the Constraints' options plus `retainContextWhenHidden: true` and `enableFindWidget: false`. `iconPath` is `media/jam.svg`.
  - **HTML:** `paneHtml(…, "board", { board: id })`, with `assets = asWebviewUri(dist/webview/excalidraw/)` and the same CSP.
  - **State:** `{signedIn, target: null, pinned: false, elsewhere: false}`, refreshed on `auth.onChange`.
  - **`open`:**
    - a bad id is ignored;
    - an id already open is `reveal`ed;
    - `onDidDispose` removes it from the map and disposes its port.
  - **`extension.ts`:**
    - `openBoard` → `boards.open`;
    - `registerWebviewPanelSerializer("jam.board", { deserializeWebviewPanel: (p, s) => boards.restore(p, s) })`.
  - **`scripts/vscode.sh`:** after the webview build, it copies `../app/node_modules/@excalidraw/excalidraw/dist/prod/fonts/*`, except `Xiaolai` as `scripts/app.sh` does, into `dist/webview/excalidraw/fonts/`. It fails unless that directory has a `.woff2`.

- [ ] **Step 1: Write the failing tests:**
  - html:
    - `board and assets attributes are escaped and the CSP is unchanged`;
    - `no board attribute for a view`.
  - board (Review Focus 5):
    - `open creates a panel with retainContextWhenHidden and the exact CSP`;
    - `open of an open board reveals it, no second panel`;
    - `a bad id opens nothing`;
    - `title messages set tabTitle(title)`, from a table: `"a\u0007b"` → `"ab"`, 80 chars → 60, `""` → `"Whiteboard"`;
    - `restore rebuilds a panel from {board}`, and `restore with bad state disposes it`;
    - `sign-out posts signedIn false to open boards`;
    - `dispose then open makes a new panel`;
    - `a board intent from a session pane opens the tab` (`perform` with `openBoard`).
  - manifest: `activationEvents` include `onWebviewPanel:jam.board`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/board.test.ts test/html.test.ts test/manifest.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `just vscode` → PASS. `unzip -l web/vscode/dist/jam.vsix` lists `extension/dist/webview/excalidraw/fonts/` with `.woff2` files. `just app` still passes its size budget.
- [ ] **Step 5: Commit** `feat(vscode): whiteboards as editor tabs`.

### Task 8: Docs and the manual checklist

**Files:**
- Modify:
  - `docs/usage/jam/vscode.md` (bump `updated`): rename **Conversation and focus** to **Panes and focus**, which owns:
    - the five views and where they start (Thinking in the panel's Jam container; Previews and Artifacts collapsed), all draggable anywhere;
    - pin and unpin on every view, and the shared focused session;
    - Thinking shares one stream per session and stream across the window's panes, and counts toward the four-feed limit;
    - Previews: Open (Simple Browser) and Browser forward the port through Remote-SSH for the window's own studio. Another session's preview opens its preview URL in the browser;
    - Status: the header's facts and actions, and Open in VS Code for a pinned session;
    - Artifacts: an ask reveals it in the Conversation view;
    - board tabs: several side by side, kept while hidden, restored after a reload, and Open in the web app. A stroke made in the last 300 ms before closing a tab may not be saved;
    - notifications and the badge are a later slice.
  - `docs/usage/jam/vscode-checklist.md`:
    - **Verify first** gains: "`vscode.env.asExternalUri(http://localhost:<port>/)` from the (UI) Jam extension in a `jam-` remote window adds a forward in the Ports view and returns a working local URL";
    - **Build** gains the Excalidraw fonts copy;
    - a **Panes** step list:
      - each view renders for the window's session in the light, dark and high-contrast themes;
      - Thinking follows a busy session live, with Load earlier and the stream select;
      - two Thinking views on one session, then *Developer: Show Running Extensions* plus Jam's server log, show one feed;
      - pin Thinking to another session, and its label shows;
      - Previews Open shows the app in the Simple Browser and a Ports-view entry, Browser opens the system browser, and a pinned other session's preview opens its preview URL;
      - Status Interrupt and Compact work, and Release flips every pane to the ended copy;
      - an Artifacts ask scrolls the Conversation view, a board thumb opens a tab, and a file opens in the browser;
      - open two boards side by side and draw in one: the other client sees it live. Hide and show a tab and the drawing is still there. Reload the window and the boards come back;
      - revoke the token: every view and board shows Sign in, and recovers after Sign In;
      - DevTools on the Thinking view and a board tab shows no CSP violations.
  - `docs/web-app.md`:
    - Layout: the `src/vscode/` bullet adds `panes.tsx` (`SessionBody`, `SessionGate`) and `BoardTab.tsx`;
    - the seams list adds `setFeedOpener` (`session/thinking/useThinkingFeed.ts`), `boards/api.ts` (board sync through the API client) and `HostContext.openPreview`.
  - The spec's status line: "slice 4 (the other panes) built".
- [ ] **Step 1:** Write the docs. Each fact goes in the one doc named above; the others link to it.
- [ ] **Step 2:** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` → no new findings beyond the plan file's own.
- [ ] **Step 3:** `just test && just app-check && just app && just vscode && just lint` → PASS.
- [ ] **Step 4: Commit** `docs: the VS Code Thinking, Previews, Status and Artifacts views and board tabs`.

---

## Decisions the spec leaves open (my calls)

- **Thinking streams are keyed by (session, stream), not by session.** A pane that pins an older stream needs a different server feed. The common case, "Current", is still shared.
- **Late joiners get a host-side replay** of the connection's events, capped at 2000 `ev`s like the pane's own row cap. Without it a second pane would wait for the next event and show an empty list.
- **The host mirrors `EventSource`.** It reconnects itself only for transient drops. An HTTP refusal is a closed error, and the pane's existing `check()` and backoff decide what happens next, so `useThinkingFeed`'s 404 and 429 handling stays unchanged.
- **`THINKING_SUBS_PER_PANE = 2`.** A compromised pane can't use up the user's four server feeds. Two covers a StrictMode remount.
- **Forwarding uses `asExternalUri`, not a spawned `ssh -L`.** VS Code's source shows the UI extension host is given the window's remote authority and asks for tunnelling. It is verified first in the checklist.
- **Forwarding happens only for the window's studio (`windowDefault`), not the focused session.** `localhost:<port>` exists only on the remote this window is attached to.
- **Open uses the Simple Browser, with a separate Browser button.** The spec allows either. The Simple Browser keeps the preview inside the workbench.
- **Default layout.** Thinking goes in a bottom-panel Jam container, because its log wants width. Previews and Artifacts start collapsed. Every view stays movable.
- **Status renders `SessionHeader` without tabs, then the Status rows.** The spec's "session header facts" are the header, and the rows are what the web app's Status tab adds.
- **The Artifacts ask click becomes a `reveal` intent**, which focuses the Conversation view and scrolls it to the ask. Asks live in the Conversation pane, not in Artifacts.
- **Status's "Open in VS Code" becomes a `session` intent.** Without it the `vscode:` link is dropped, and the button would be dead for a pinned session.
- **Board tabs use a panel serializer, so a reload restores them.** It is cheap, and losing open boards on every reload is jarring.
- **Board titles arrive by a `title` message from the pane.** The host then needs no extra board fetch.
- **A save still pending when a board tab closes is not flushed (≤ 300 ms).** A webview has no unload hook that can delay disposal. This is documented.
- **The board editor's CSP is the panes' CSP, unchanged.** Excalidraw's fonts load from `font-src <cspSource>`. Image tools are already off.
