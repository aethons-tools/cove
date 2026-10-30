# VS Code slice 3 (webview bridge, Conversation pane) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A **Conversation** view in a Jam activity-bar container. It is a webview rendering `web/app`'s `ConversationView` (composer and asks inline) for the window's *focused session*:
- by default, the session whose studio the window is attached to;
- or the one picked with *Jam: Focus Session…*;
- or the session the pane is pinned to.

Every byte the pane reads or writes goes through the extension host. The host adds the bearer, holds one `/app/user/events` stream for the window and fans it out, and reports push focus.

**Architecture:**
- **Seams in `web/app`, defaults unchanged.** Five module-level setters swap a direct network use for a bridge, and with none set the web app behaves exactly as today:
  - `setTransport` (`api/client.ts`), with `ApiError` semantics in one shared `interpret`;
  - `setUploader` (`composer/upload.ts`);
  - `setBlobResolver` (`blobs.ts`, read by `useBlobUrl` in `Message` and `BoardCard`);
  - `setLiveSource` (`live/useLiveEvents.ts`; the invalidation logic stays where it is);
  - `setFocusSink` (`push/useFocusReport.ts`).
- **Router: a memory router plus a link interceptor, not a navigation context.** The pane mounts `createMemoryRouter([{ path: "*", element }])` at `/app/user/c/<channel>`, so `Link`, `useNavigate`, `useLocation` and `useSearchParams` in shared components work unchanged. `NavBridge` watches the location. Any navigation away from the pane's own path becomes an *open intent* posted to the extension, then a `replace` back; the `*` route keeps `ConversationView` mounted throughout. A capture-phase click listener catches every `<a>` click, router `Link`s and the server-rendered message HTML alike, and maps it to an intent or an in-pane scroll. The webview itself never navigates.
  - *Why not a navigation context:* it would change four shared components (`ConversationView`, `Message`, `BoardCard`, `NewBoard`) and still not cover links inside the message HTML, which need the interceptor anyway. The spec's "an adapter, not a fork" favours leaving them alone.
- **One bundle, its own Vite config.** `web/app/vite.vscode.config.ts` builds `src/vscode/main.tsx` into `web/vscode/dist/webview/`, with fixed entry names `pane.js` and `pane.css`, `base: "./"` and hashed lazy chunks. `vite.config.ts`, `index.html` and the size budget don't change. The extension writes the HTML itself, with a nonce CSP and `asWebviewUri` paths.
- **The extension is pure modules plus a thin VS Code layer, as in slice 2.** The modules: `bridge/router.ts` (request proxy, path guard, intents), `live.ts` (SSE client and fan-out), `focus.ts` (focused-session and pin model), `focusreport.ts` (push focus) and `panes/html.ts`. `panes/conversation.ts` is the `WebviewViewProvider` and imports `vscode`.
- **Shared protocol types:** `web/app/src/bridge/protocol.ts`, type-imported by `web/vscode` exactly as slice 2 does `src/api/types.ts`.

**Tech Stack:** as slice 2. Also: Vite 8 (second config), React Router memory router, `ReadableStream` bodies with `duplex: "half"` (Node 22 fetch) for upload progress.

**Spec:** `docs/superpowers/specs/2026-10-28-vscode-code-window-design.md` ("In the window", "Focus and pin", "Data flow", "Which session a window belongs to", "Notifications" → *Focus*, "Left to the builder"). This is slice 3 of 5. It consumes slice 2 (`docs/superpowers/plans/2026-10-29-vscode-2-extension.md`, Tasks 2–4 and 7: `SessionHead.channel`, the skeleton, `Auth`, `JamApi`, `SignedOutError`, `pickSession`, `studioFromAuthority`).

**Correction to slice 2:** `vscode.env.remoteAuthority` is *proposed* API (the `resolvers` proposal), so a published extension can't read it. Task 6 replaces slice 2's `Ui.remoteAuthority()` with `windowAuthority()`, which reads `workspace.workspaceFolders[0].uri` (or `workspace.workspaceFile`) when its scheme is `vscode-remote`. `jam.forgetHostKey` switches to it too. Remote-SSH may also hex-encode the host as JSON (`ssh-remote+7b22686f73744e616d65223a…7d` = `{"hostName":"jam-ses_…"}`), and `studioFromAuthority` learns to decode that form.

## Global Constraints

- **The web app is unchanged.** With no setter called, every request, upload, image, `EventSource` and focus post is the same as before. Every existing `web/app` test passes without edits; Task 2 edits only the `ConversationView` test, adding a case. `npm run size` reports the same two areas.
- **The token never enters a webview.** No extension→pane message carries a token, `jam.url` or a cookie. The webview CSP is exactly:
  `default-src 'none'; script-src 'nonce-<n>' <cspSource>; style-src <cspSource> 'unsafe-inline'; font-src <cspSource>; img-src <cspSource> blob: data:; connect-src 'none'; frame-src 'none'; base-uri 'none'; form-action 'none'`
  - `<n>` is 32 random bytes in base64, new for each HTML render;
  - webview options are `enableScripts: true`, `enableCommandUris: false`, `localResourceRoots: [<ext>/dist/webview]`;
  - `retainContextWhenHidden` stays unset (spec: only the board editor gets it).
- **The request proxy (the webview is less trusted):**
  - methods `GET`, `POST`, `DELETE` only;
  - the path must start with `/app/user/` and be at most 2048 chars;
  - it is refused when it contains `\`, `//`, a `.` or `..` segment, `#`, a control character, or (case-insensitive) `%2f`, `%2e` or `%5c`;
  - it is refused when `new URL(path, jamUrl)` changes the origin or the path part;
  - it is refused when the path is `/app/user/events` or ends `/thinking/events`, since streams come only from the extension;
  - forwarded headers are only `content-type` and `x-filename` from the pane, plus the extension's own `Authorization: Bearer`, `Accept` and `redirect: "manual"`, nothing else;
  - so `/me/studios/*` (cert issuance) and `/app/admin/*` are never reachable from a pane.
- **Live:**
  - one `/app/user/events` stream per window, opened while signed in and at least one pane is attached, closed when the last pane goes;
  - forwarded to panes only: `changed`, `presence`, `board` (with its data) and the synthetic `open`; any other event, `notify` included, is not forwarded;
  - backoff `LIVE_BACKOFF_MIN_MS = 1000`, doubling to `LIVE_BACKOFF_MAX_MS = 30_000`, with ±20 % jitter, reset by a successful open;
  - `LIVE_STALL_MS = 60_000` without a byte → abort and reconnect (the server's keep-alive is 25 s);
  - `204` → off, no retry (panes poll every `POLL_MS`);
  - `401` → `signedOut()` once, no retry until signed in again.
- **Focus report:** the extension posts `POST /app/user/push/focus {channel}` itself, mirroring `useFocusReport`:
  - the reported channel is that of the first attached pane that is visible, in a focused window, and showing a channel;
  - a post happens on each change, and every `FOCUS_REFRESH_MS = 30_000` while non-null;
  - `null` is posted when that stops and on deactivate;
  - a pane sends `viewing` only when its bootstrap has `pushFocus`.
- **Ids** use the `internal/ident` alphabet: `^(ses|chn|brd|blb)_[0-7][0-9a-hjkmnp-tv-z]{25}$` with the stated prefix.
- **Copy:**
  - no session: "This window isn't on a Jam studio. Focus a session to see its conversation." [Focus a session…];
  - a gone session: "That session has ended, or you can't see it." [Focus a session…];
  - signed out: "Sign in to Jam to see this conversation." [Sign in];
  - a refused proxy request is `ApiError(400, "bad_request", "That isn't a Jam user-area request.")`;
  - a `postForm` through the bridge is `ApiError(0, "unavailable", "This isn't available in VS Code yet.")`.

## Review Focus

1. **A compromised pane** (script in message HTML that slipped past the sanitizer) posts `request`s for `/me/studios/ses_…/ssh/cert`, `/app/admin/api/users`, `/app/user/../../me/studios`, `/app/user/%2e%2e/admin`, `//evil.example/x`, `https://evil.example/`, or sets an `Authorization` or `Cookie` header. Each is refused or stripped, and no fetch carries the token anywhere but `/app/user/`. Task 4 tests a table.
2. **A token revoked while panes are open:** the stream's 401 calls `signedOut()` once, not once per pane or per retry. Every pane switches to "Sign in to Jam…". After Sign In the stream restarts and the panes reload their bootstrap and conversation. Tasks 5 and 7 test it.
3. **A stream that dies silently** (laptop sleep, proxy idle cut) is noticed by the stall timer. On reconnect the panes get `open` and refetch what they missed. Task 5 tests the timer, and Task 2 that a second `open` invalidates.
4. **Clicks in message HTML:** a `javascript:`, `command:`, `vscode:` or `file:` link does nothing; an `https:` link opens in the browser; `/app/user/c/<other>` opens the web app; `#ask-…` and a link to the pane's own conversation scroll in place. The webview never navigates. Tasks 3 and 4 test it.
5. **A window that isn't a plain `jam-` remote:**
   - a hex-JSON authority resolves to the session;
   - `ssh-remote+other`, a `wsl+…` or dev-container remote, a local folder, or an empty window has no default session and shows the "Focus a session" state;
   - a stored focus for a session that is now gone shows the ended state, not an error loop.

   Task 6 tests it.

---

### Task 1: The request transport seam and the bridge protocol

**Files:**
- Create: `web/app/src/bridge/protocol.ts`.
- Modify: `web/app/src/api/client.ts`.
- Test: `web/app/src/api/client.test.ts`.

**Interfaces:**
- Produces, in `protocol.ts` (types and string constants only, no DOM or React imports; `web/vscode/tsconfig.json` adds it to `include`):
  ```ts
  export type PaneName = "conversation"
  export type Method = "GET" | "POST" | "DELETE"
  export type BridgeRequest = { method: Method; path: string; json?: unknown; bytes?: ArrayBuffer;
                                headers?: Record<string, string>; accept?: "json" | "bytes"; progress?: boolean }
  export type BridgeResponse =
    | { kind: "http"; status: number; contentType: string; json?: unknown; text?: string; bytes?: ArrayBuffer }
    | { kind: "unreachable" } | { kind: "signedOut" } | { kind: "refused"; reason: string }
  export type SessionTarget = { session: string; label: string; channel: string } | { session: string; gone: true }
  export type PaneState = { signedIn: boolean; target: SessionTarget | null; pinned: boolean; elsewhere: boolean }
  export type OpenIntent = { kind: "board"; id: string } | { kind: "conversation"; channel: string }
    | { kind: "web"; path: string } | { kind: "external"; url: string } | { kind: "focusSession" }
  export type PaneToExt =
    | { type: "ready"; pane: PaneName }
    | { type: "request"; id: number; req: BridgeRequest } | { type: "abort"; id: number }
    | { type: "viewing"; channel: string | null } | { type: "open"; intent: OpenIntent } | { type: "signIn" }
  export type ExtToPane =
    | { type: "state"; state: PaneState }                      // on ready and on every change
    | { type: "response"; id: number; res: BridgeResponse }
    | { type: "progress"; id: number; loaded: number; total: number }
    | { type: "live"; event: "changed" | "presence" | "board" | "open"; data: string }
  ```
- Produces, in `client.ts`:
  ```ts
  export type Raw = { status: number; contentType: string; redirected: boolean; json?: unknown; text?: string }
  export type TransportRequest = { method: Method; url: string; json?: unknown; form?: FormData }
  export type Transport = (r: TransportRequest) => Promise<Raw>   // throws ApiError for transport-level failures
  export function interpret<T>(raw: Raw): T                        // today's status/content-type rules, moved verbatim
  export const fetchTransport: Transport                           // today's fetch: credentials same-origin, redirect manual; a throw → ApiError(0,"unavailable",…)
  export function setTransport(t?: Transport): void                // undefined restores fetchTransport
  ```
  - `request()` builds `{method, url: base + path, json | form}` and calls the current transport, then `interpret`.
  - `redirected` is `res.type === "opaqueredirect"`.

- [ ] **Step 1: Write the failing tests** in `client.test.ts`:
  - `interpret`, a table:
    - `{204}` → `undefined`;
    - `{200, application/json, json:{a:1}}` → `{a:1}`;
    - `{404, json:{error:"no such",code:"not_found"}}` → `ApiError(404,"not_found","no such")`;
    - `{401, text/html}`, `{403, text/html}`, `{302}` and `{0, redirected:true}` → `code "signed_out"`, message "Your sign-in has expired.";
    - `{500, text/plain}` → `"unavailable"`, "Jam answered 500. Try again shortly.".
  - `setTransport routes apiAt calls and restores fetch`: a fake transport records `{method:"POST", url:"/app/user/api/messages", json:{x:1}}`. `setTransport()` then sends the next call to MSW again.
  - `postForm passes FormData as form` (to the fake).
  - The existing MSW tests are unchanged and pass.
- [ ] **Step 2:** `cd web/app && npx vitest run src/api` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/app && npm run check` → PASS.
- [ ] **Step 5: Commit** `refactor(app): pluggable API transport; bridge protocol types`.

### Task 2: The other seams, and ConversationView without the session pane

**Files:**
- Create: `web/app/src/blobs.ts`, `web/app/src/host.ts`, `web/app/src/blobs.test.tsx`.
- Modify:
  - `src/composer/upload.ts`;
  - `src/live/useLiveEvents.ts`;
  - `src/push/useFocusReport.ts`;
  - `src/conversation/Message.tsx` (image `src` from `useBlobUrl`);
  - `src/boards/BoardCard.tsx` (thumb `src` from `useBlobUrl`);
  - `src/Problem.tsx`;
  - `src/conversation/ConversationView.tsx`.
- Test: `upload.test.ts`, `useLiveEvents.test.tsx`, `push/focus.test.tsx`, `ConversationView.test.tsx`.

**Interfaces:**
- Produces:
  ```ts
  // composer/upload.ts
  export type Uploader = (file: File, onProgress: (pct: number) => void) => { done: Promise<string>; abort: () => void }
  export const xhrUpload: Uploader                     // today's body
  export function setUploader(u?: Uploader): void
  export function upload(...args: Parameters<Uploader>): ReturnType<Uploader>  // delegates
  export function uploadOutcome(status: number, body: string): string | Error // 201 + {ref} → ref; else Error(body.trim() || `failed ${status}`)
  // blobs.ts
  export type BlobResolver = (path: string) => Promise<string>   // → blob: URL
  export function setBlobResolver(r?: BlobResolver): void
  export function useBlobUrl(path: string): string | undefined   // no resolver: path, synchronously; else undefined until resolved
  // live/useLiveEvents.ts
  export type LiveSource = (on: (event: string, data: string) => void) => () => void // emits "open" on each (re)connect
  export const eventSourceLive: LiveSource              // EventSource("/app/user/events"): open, changed, presence, board
  export function setLiveSource(s?: LiveSource): void
  // push/useFocusReport.ts
  export function setFocusSink(s?: (channel: string | null) => void): void
  // host.ts
  export type Host = { signIn?: () => void }
  export const HostContext: React.Context<Host>         // default {}
  ```
  - `useLiveEvents` keeps its handlers. It subscribes through the current source; the first `open` is free and each later one calls `changed()`, as today.
  - With a sink, `useFocusReport(ch, on)` calls `sink(ch)` when `on` and `ch` change, and `sink(null)` on cleanup when it last sent non-null. It has no focus, visibility or timer logic, since the extension owns those.
  - `Problem`: when `useContext(HostContext).signIn` is set, the signed-out link is `<Anchor component="button" onClick={signIn}>Sign in again</Anchor>`.
  - `ConversationView({ id, withSession = true })`:
    - with `withSession={false}`, it renders `header` + main in the `Stack`, with no `SessionView` (and so no session header or pane, which are slice 4's Status pane);
    - the `Stack`'s height becomes `calc(100dvh - var(--app-shell-header-height, 0px) - 2 * var(--app-shell-padding, 0px))`, so outside an `AppShell` it fills the viewport.
  - Anchor `href`s stay the plain paths, since the pane's link interceptor handles their clicks.

- [ ] **Step 1: Write the failing tests:**
  - upload: `uploadOutcome` gives `(201,'{"ref":"r1"}')` → `"r1"`; `(413,"too large\n")` → Error "too large"; `(201,"")` → Error "failed 201". `setUploader(fake)` makes `upload` call the fake.
  - live: `a custom source drives the same invalidations`: `changed` → `[rail, conversation, presence, session]`; `presence` → `[presence, session]`; a second `open` → the `changed` set; `board` re-dispatches `jam-board` with its data; unmount calls the unsubscribe. The `EventSource` tests are unchanged.
  - focus: `a sink gets the channel, not fetch`: `setFocusSink(spy)`, `Probe ch="chn_a" on` → `spy("chn_a")`, with no `fetch` call and no 30 s repeat. A rerender with `chn_b` → `null`, `"chn_b"`; unmount → `null`; `on=false` → nothing.
  - blobs: `useBlobUrl returns the path without a resolver`; `with a resolver it resolves to the blob URL`.
  - ConversationView: `withSession false renders no session header` (the head mock returns a session; `queryByText("Copy SSH")` is null and the composer is present). The existing tests pass unchanged.
- [ ] **Step 2:** `cd web/app && npx vitest run src/composer src/live src/push src/blobs.test.tsx src/conversation` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/app && npm run check && npm run build && npm run size` → PASS, and `size` prints the same two areas.
- [ ] **Step 5: Commit** `refactor(app): seams for uploads, blobs, live events and focus; ConversationView without the session pane`.

### Task 3: The webview pane app (web/app/src/vscode)

**Files:**
- Create in `web/app/`:
  - `vite.vscode.config.ts`;
  - `src/vscode/main.tsx`, `bridge.ts`, `links.ts`, `NavBridge.tsx`, `scheme.ts`, `Pane.tsx`, `pane.css`;
  - tests `src/vscode/bridge.test.ts`, `links.test.ts`, `scheme.test.ts`, `Pane.test.tsx`, and `src/vscode/fakeHost.ts` (the fake `acquireVsCodeApi`).
- Modify: `tsconfig.json` (`include` adds `vite.vscode.config.ts`). The output lands under `web/vscode/dist/`, which slice 2 already git-ignores.

**Interfaces:**
- Consumes: Tasks 1–2's setters and `protocol.ts`.
- Produces:
  ```ts
  // bridge.ts
  export interface VsApi { postMessage(m: PaneToExt): void }
  export class Bridge {
    constructor(api: VsApi, win: Window)                     // listens for "message"
    request(req: BridgeRequest, onProgress?: (loaded: number, total: number) => void): { res: Promise<BridgeResponse>; abort: () => void }
    post(m: PaneToExt): void
    onState(cb: (s: PaneState) => void): () => void
    onLive(cb: (event: string, data: string) => void): () => void
  }
  export function install(b: Bridge, host: { signIn: () => void }): void
  ```
  `install` wires the seams:
  - **transport:** `form` → the copy error, `refused` → the copy error, `unreachable` → `ApiError(0,"unavailable","Jam can't be reached. Check your connection.")`, `signedOut` → `ApiError(401,"signed_out","Your sign-in has expired.")`, and `http` → `Raw`, then `interpret`;
  - **uploader:** `file.arrayBuffer()` → `request({method:"POST", path:"/app/user/uploads", bytes, headers:{"Content-Type": type||"application/octet-stream", "X-Filename": encodeURIComponent(name)}, progress:true})` → `uploadOutcome`; `abort` posts `abort` and rejects `AbortError`;
  - **blob resolver:** `request({method:"GET", path, accept:"bytes"})` → `URL.createObjectURL`, cached per path, with `BLOB_CACHE_MAX = 200` and the oldest revoked. A `board` live event drops the cached `/app/user/boards/<id>/thumb.png`;
  - **live source:** emits a synthetic `open` at once on subscribe, then the forwarded events;
  - **focus sink:** posts `{type:"viewing", channel}`.

  ```ts
  // links.ts
  export type LinkAction = { intent: OpenIntent } | { scrollTo: string } | { allow: true } | { drop: true }
  export function linkAction(href: string, paneChannel: string): LinkAction
  export function interceptLinks(doc: Document, channel: () => string, post: (i: OpenIntent) => void): () => void // capture-phase click
  // NavBridge.tsx
  export function NavBridge({ home, onLeave }: { home: string; onLeave: (path: string) => void }): null
  // scheme.ts
  export function schemeOf(body: DOMTokenList): "light" | "dark" // vscode-dark | vscode-high-contrast → dark; else light
  export function useVscodeScheme(): "light" | "dark"           // MutationObserver on body's class
  // Pane.tsx
  export function Pane({ bridge }: { bridge: Bridge }): JSX.Element
  ```
  - **`linkAction`** on the resolved `href`:
    - `#…` → allow;
    - `/app/user/c/<paneChannel>#ask-<id>` or `/app/user/c/<paneChannel>` → `scrollTo` `ask-<id>` (or nothing);
    - `/app/user/c/<chn>` → `{kind:"conversation"}`;
    - `/app/user/boards/<brd>` → `{kind:"board"}`;
    - another `/app/…` path → `{kind:"web", path}` (blobs, downloads);
    - `http:`, `https:` or `mailto:` → `{kind:"external", url}`;
    - anything else (`javascript:`, `command:`, `vscode:`, `file:`, `data:`) → drop.
    - Every action except allow calls `preventDefault` and `stopPropagation`.
  - **`NavBridge`**: on a location that isn't `home`, `onLeave(pathname + search + hash)` → `linkAction` → `post`, then `navigate(home, { replace: true })`.
  - **`Pane`** waits for the first `state`. Then:
    - signed out → the signed-out copy with [Sign in] (`signIn`);
    - target `null` → the no-session copy with [Focus a session…] (`open` `focusSession`);
    - `gone` → the gone copy with [Focus a session…];
    - otherwise it renders `MantineProvider forceColorScheme={useVscodeScheme()} theme={theme}` → `Notifications` → `QueryClientProvider` (a new `QueryClient` per sign-in generation and target channel, with `staleTime` and `retry` as `start.tsx`) → `HostContext{signIn}` → `BootContext` (from `apiGet<Bootstrap>("bootstrap")`, shown as `Problem` on failure) → the memory router (`*` → `<><NavBridge/><ConversationView key={channel} id={channel} withSession={false} /></>`, initial `/app/user/c/<channel>`).
    - `useLiveEvents()` runs inside the client.
  - **`main.tsx`** imports the same styles as `src/main.tsx` plus `pane.css`, then `const b = new Bridge(acquireVsCodeApi(), window); install(b, …); interceptLinks(…)`, renders `Pane` into `#root` and posts `{type:"ready", pane: root.dataset.pane}`. No service worker and no sounds.
  - **`pane.css`**: `:root { --mantine-color-body: transparent; }`, `html, body, #root { height: 100%; margin: 0 }`.
  - **`vite.vscode.config.ts`**: `base: "./"`, `plugins: [react(), excalidrawFonts()]`, `build.outDir: process.env.VSCODE_WEBVIEW_OUT ?? "../vscode/dist/webview"`, `emptyOutDir: true`, `cssCodeSplit: false`, `rollupOptions.input: "src/vscode/main.tsx"`, and `output: { entryFileNames: "pane.js", chunkFileNames: "assets/[name]-[hash].js", assetFileNames: (a) => a.names?.[0]?.endsWith(".css") ? "pane.css" : "assets/[name]-[hash][extname]" }`.

- [ ] **Step 1: Write the failing tests:**
  - bridge: `request correlates responses by id` (two in flight, answered out of order); `progress reaches its request's callback only`; `abort posts {type:"abort", id}`; `transport maps unreachable, signedOut, refused and FormData to the ApiErrors in the copy`.
  - links, a table (Review Focus 4), channel `chn_a`:
    - `javascript:alert(1)`, `command:workbench.action.reload`, `vscode://x` and `file:///etc/passwd` → drop;
    - `https://example.com/x` → external;
    - `/app/user/c/chn_b` → conversation;
    - `/app/user/c/chn_a#ask-ask_1` → `scrollTo "ask-ask_1"`;
    - `/app/user/boards/brd_…` → board;
    - `/app/user/blobs/blb_…` → web;
    - `#x` → allow.
    - Also `interceptLinks prevents the click's default` on an `<a>` inside `.jam-body`.
  - scheme: `vscode-dark` → dark, `vscode-high-contrast` → dark, `vscode-high-contrast-light` → light, `vscode-light` → light.
  - Pane, with `fakeHost` answering `request`s from a route table:
    - `renders the focused session's conversation through the bridge`: the first two requests are `GET /app/user/api/bootstrap`, then `GET /app/user/api/conversations/chn_a`. Every request path starts `/app/user/api/`, and none is `sessions/…`. The message text shows, the composer shows, and no "Copy SSH".
    - `no target shows Focus a session, which posts the intent`.
    - `signed out shows Sign in, which posts signIn`.
    - `gone shows the ended copy`.
    - `a new target switches the conversation` (a request for `chn_b`, and `chn_a`'s text gone).
    - `Open on a board card posts a board intent and the conversation stays mounted` (the composer textarea keeps its typed text).
    - `New whiteboard navigation posts a board intent` (`boards: true`, `newBoard: true`, `POST /app/user/api/boards` → `{board:"brd_…"}`).
    - `a live changed event refetches the conversation`.
    - `an image attachment loads as a blob URL` (`URL.createObjectURL` stubbed → `blob:1`; the `img` `src` is `blob:1`).
    - `a 401 shows Sign in again, which posts signIn`.
    - `viewing is posted only when pushFocus is on`.
- [ ] **Step 2:** `cd web/app && npx vitest run src/vscode` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/app && npm run check && npx vite build -c vite.vscode.config.ts` → PASS. `../vscode/dist/webview/` then holds `pane.js`, `pane.css`, `assets/`, and IBM Plex `woff2` files, with no `index.html`. `grep -c esm.sh ../vscode/dist/webview -r` → 0.
- [ ] **Step 5: Commit** `feat(app): the VS Code webview pane entry (bridge, links, theme, Conversation)`.

### Task 4: Extension bridge router (proxy, path guard, intents)

**Files:**
- Create: `web/vscode/src/bridge/router.ts`, `web/vscode/src/bridge/intents.ts`, `web/vscode/test/router.test.ts`, `web/vscode/test/intents.test.ts`.
- Modify: `web/vscode/tsconfig.json` (`include` adds `../app/src/bridge/protocol.ts`).

**Interfaces:**
- Consumes: `protocol.ts`; slice 2's `Session` and `normalizeJamUrl`.
- Produces:
  ```ts
  export const ID_RE: Record<"ses" | "chn" | "brd" | "blb", RegExp>
  export function checkPath(path: string, jamUrl: string, opts?: { streams?: boolean }): string | Error // → URL string; Constraints' rules
  export type RouterDeps = { session: () => Promise<{ url: string; token: string } | undefined>; fetch: typeof fetch; onSignedOut: () => void }
  export class Router {
    constructor(d: RouterDeps)
    handle(m: PaneToExt, reply: (m: ExtToPane) => void): void        // "request" and "abort"; others are the provider's
  }
  export const UPLOAD_CHUNK = 64 * 1024
  export type IntentAction = { openExternal: string } | { command: "jam.focusSession" }
  export function intentAction(i: OpenIntent, jamUrl: string): IntentAction | Error
  ```
  - `handle` replies with exactly one `response` per request. An aborted request gets no reply.
  - No session → `signedOut`, without a fetch. A fetch throw → `unreachable`.
  - `401` → `signedOut`, plus `onSignedOut()`. Concurrent 401s call it once per session token.
  - Any other status → `http`: `json` when the content-type is `application/json`, `bytes` when `accept:"bytes"`, else `text` capped at 64 KiB.
  - With `bytes`, the body is a `ReadableStream` over `UPLOAD_CHUNK` slices (`duplex:"half"`, `Content-Length` set), and with `progress` each pulled slice posts `progress`.
  - `intentAction`:
    - board → `<jam>/app/user/boards/<id>`, and slice 4 replaces this with the board editor tab;
    - conversation → `<jam>/app/user/c/<id>`;
    - web → `checkPath`, then `<jam><path>`;
    - external → only `http:`, `https:` or `mailto:`, re-serialized by `new URL`;
    - focusSession → the command;
    - ids are checked against `ID_RE`, and anything else is an Error.

- [ ] **Step 1: Write the failing tests:**
  - `checkPath`, a table (Review Focus 1). Each of these is an Error:
    - `/me/studios/ses_…/ssh/cert`, `/app/admin/api/users`;
    - `/app/user/../../me/studios`, `/app/user/./api`;
    - `/app/user/%2e%2e/admin`, `/app/user/%2F`, `/app/user/a\\b`;
    - `//evil.example/x`, `https://evil.example/app/user/x`;
    - `/app/user/api/x#y`, `/app/user/api/\u0000`;
    - `/app/user/events`, `/app/user/session/ses_…/thinking/events`;
    - a 2049-char path.

    And `/app/user/api/conversations/chn_a?before=5` → `https://jam.example.com/app/user/api/conversations/chn_a?before=5`.
  - Router, with a fake fetch:
    - `forwards bearer, Accept and only allowed headers`: the pane sends `Authorization: x`, `Cookie: y`, `X-Filename: a`, and the fetch sees `Authorization: Bearer <token>`, `X-Filename: a`, and no `Cookie`;
    - `method PUT is refused`;
    - `401 replies signedOut and calls onSignedOut once for two concurrent requests` (Review Focus 2);
    - `fetch throw replies unreachable`;
    - `json, text and bytes bodies`;
    - `upload posts progress per 64 KiB chunk and a final 201` (a 200 KiB buffer → loaded 65536, 131072, 196608, 204800);
    - `abort aborts the fetch signal and sends no response`;
    - `refused path never calls fetch`.
  - intents, a table:
    - `{kind:"external", url:"javascript:alert(1)"}`, `"command:x"` and `"file:///x"` → Error;
    - `https://a.example/b` → itself;
    - board `brd_…` → `https://jam.example.com/app/user/boards/brd_…`;
    - board `../x` → Error;
    - web `/me/studios/x` → Error;
    - focusSession → the command.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/router.test.ts test/intents.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): webview request bridge restricted to /app/user/, and open intents`.

### Task 5: Live stream, pane hub and focus reporter

**Files:**
- Create: `web/vscode/src/live.ts`, `web/vscode/src/panes/hub.ts`, `web/vscode/src/focusreport.ts`, and tests `live.test.ts`, `hub.test.ts`, `focusreport.test.ts`.

**Interfaces:**
- Consumes: `RouterDeps`-style `session()` and `onSignedOut`.
- Produces:
  ```ts
  // live.ts
  export const LIVE_BACKOFF_MIN_MS = 1000, LIVE_BACKOFF_MAX_MS = 30_000, LIVE_STALL_MS = 60_000
  export type LiveEvent = { event: string; data: string }
  export class SseParser { push(chunk: string): LiveEvent[] }   // \n, \r\n, \r; ":" comments; multi-line data joined by \n; "retry:" ignored; default event "message"
  export type LiveDeps = { session: RouterDeps["session"]; fetch: typeof fetch; onSignedOut: () => void;
                           setTimeout: typeof setTimeout; clearTimeout: typeof clearTimeout; random: () => number }
  export class LiveStream {
    constructor(d: LiveDeps)
    subscribe(cb: (e: LiveEvent) => void): () => void  // first subscriber connects; last unsubscribe aborts
    restart(): void                                    // after sign-in: reconnect now if subscribed
    readonly state: "idle" | "connecting" | "open" | "off" | "signedOut"
  }
  // panes/hub.ts
  export interface PanePort { readonly id: string; post(m: ExtToPane): void }
  export class PaneHub {
    constructor(live: LiveStream, onEvent?: (e: LiveEvent) => void) // onEvent sees every event while subscribed (FocusModel.invalidate)
    attach(p: PanePort): () => void                   // the first attach subscribes; the last detach unsubscribes
    broadcast(m: ExtToPane): void
  }
  // focusreport.ts
  export const FOCUS_REFRESH_MS = 30_000
  export class FocusReporter {
    constructor(d: { post: (channel: string | null) => Promise<void>; setInterval: typeof setInterval; clearInterval: typeof clearInterval })
    pane(id: string, s: { visible: boolean; channel: string | null }): void
    drop(id: string): void
    windowFocused(f: boolean): void
    dispose(): void                                   // posts null if it last posted non-null
  }
  ```
  - `LiveStream` emits `{event:"open", data:""}` on every successful connect.
  - The hub forwards only the allowed events, as `{type:"live", event, data}`, to every attached pane.
  - Reconnect delay: `min(MAX, MIN * 2^n) * (0.8 + 0.4 * random())`.
  - `FocusReporter.post` errors are swallowed.

- [ ] **Step 1: Write the failing tests** (fake timers, a fake fetch whose body is a controllable `ReadableStream`):
  - parser: `event: changed\ndata: 1\n\n` → `[{changed,"1"}]`; the same split across 3 chunks; CRLF; a `: keep-alive` comment yields nothing; two `data:` lines join with `\n`.
  - live:
    - `one fetch for three subscribers, with bearer and Accept text/event-stream`;
    - `last unsubscribe aborts`;
    - `5xx retries at 1 s, 2 s, 4 s … capped at 30 s` (with `random = () => 0.5`);
    - `a successful open resets the backoff`;
    - `60 s of silence aborts and reconnects; a keep-alive comment resets the timer` (Review Focus 3);
    - `204 goes off and never retries`;
    - `401 calls onSignedOut once and stops; restart reconnects` (Review Focus 2);
    - `not signed in stays signedOut without a fetch`.
  - hub: `open, changed, presence and board reach every pane; notify and message don't`; `detach of the last pane unsubscribes`.
  - focusreport:
    - `reports the visible pane's channel only while the window is focused`: posts `chn_a`, then after a blur `null`, then after a focus `chn_a`;
    - `refreshes every 30 s while non-null, not while null`;
    - `hidden pane → null`;
    - `a switch from chn_a to chn_b posts chn_b`;
    - `dispose posts null`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/live.test.ts test/hub.test.ts test/focusreport.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): one live event stream per window, fanned out to panes; push focus`.

### Task 6: Focused session, pins and Jam: Focus Session…

**Files:**
- Create: `web/vscode/src/focus.ts`, `web/vscode/test/focus.test.ts`.
- Modify:
  - `web/vscode/src/open.ts`: `studioFromAuthority` decodes hex-JSON; `windowAuthority`; `sessionPicks` and `sessionOfPick` factored out of `pickSession`, which now uses them;
  - `Ui`: `remoteAuthority()` → `windowAuthority()`;
  - `test/open.test.ts`.

**Interfaces:**
- Consumes: slice 2's `JamApi.sessionHead`/`rail`/`conversation`, `SESSION_ID_RE` and `Ui`.
- Produces:
  ```ts
  // open.ts
  export function studioFromAuthority(a: string | undefined): string | undefined
  //   "ssh-remote+jam-<id>" or "ssh-remote+<hex of JSON {hostName:"jam-<id>", …}>" → id, when SESSION_ID_RE matches; else undefined
  export function windowAuthority(w: { workspaceFolders?: readonly { uri: { scheme: string; authority: string } }[];
                                       workspaceFile?: { scheme: string; authority: string } }): string | undefined // first vscode-remote URI's authority
  export type SessionPick = { label: string; description?: string; detail?: string; row: RailRow }
  export async function sessionPicks(api: JamApi): Promise<SessionPick[]>      // slice 2's filter and labels
  export async function sessionOfPick(api: JamApi, p: SessionPick): Promise<string | undefined> // conversation(row.id).session || undefined
  // focus.ts
  export interface StateLike { get<T>(k: string): T | undefined; update(k: string, v: unknown): Thenable<void> }
  export class FocusModel {
    constructor(d: { windowDefault: string | undefined; state: StateLike; resolve: (session: string) => Promise<SessionTarget> })
    windowSession(): string | undefined              // workspaceState "jam.focus" ?? windowDefault
    focus(session: string | undefined): Promise<void> // undefined or the default → clears the override
    isPinned(view: string): boolean
    togglePin(view: string): Promise<void>           // pins to the session the view shows now; again → follow the window
    stateOf(view: string): Promise<{ target: SessionTarget | null; pinned: boolean; elsewhere: boolean }>
    invalidate(): void                               // drops cached targets; re-resolves and fires onChange
    onChange(cb: () => void): () => void
  }
  export async function focusSessionCommand(d: { api: JamApi; ui: Ui; model: FocusModel; hasDefault: boolean }): Promise<void>
  ```
  - `resolve` is `sessionHead(id)` → `{session, label, channel}`. A `404` gives `{session, gone:true}`, and other errors propagate. Results are cached per session and cleared by `invalidate()`, which the hub's `onEvent` calls on `presence` and `open`.
  - Pins are kept in workspaceState `"jam.pins"`, as `{[view]: session}`.
  - `elsewhere` means the view's session ≠ `windowSession()`.
  - `focusSessionCommand`'s quick pick has the placeholder "Focus a session (unpinned Jam panes follow it)". When `hasDefault`, the first item is `$(home) This window's studio`, which calls `focus(undefined)`. A pick runs `sessionOfPick`; when that is empty it shows "That conversation has no session to focus".

- [ ] **Step 1: Write the failing tests:**
  - `studioFromAuthority`, a table (Review Focus 5):
    - `ssh-remote+jam-<id>` → id;
    - hex of `{"hostName":"jam-<id>"}` → id;
    - hex of `{"hostName":"jam-<id>","user":"agent"}` → id;
    - `ssh-remote+other`, `wsl+Ubuntu`, `dev-container+abc`, hex of bad JSON, `jam-ses_x`, `undefined` → undefined.
  - `windowAuthority`: a remote folder → its authority; a `file` folder → undefined; no folders but a remote `workspaceFile` → its authority; empty → undefined.
  - FocusModel, with a fake state and resolve:
    - `defaults to the window's studio`;
    - `focus overrides and persists; focus(undefined) clears`;
    - `a pinned view keeps its session when the window's focus changes, and reports elsewhere`;
    - `unpin follows the window again`;
    - `no default and no override → target null`;
    - `a 404 session is gone, not an error`;
    - `onChange fires on focus and pin`.
  - `focusSessionCommand`: `This window's studio` resets; a pick of a row with a session focuses it; a row without one shows the message.
  - The existing `pickSession` tests still pass.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/focus.test.ts test/open.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): the window's focused session, per-pane pins, Jam: Focus Session…`.

### Task 7: The Conversation view (provider, HTML, manifest, packaging)

**Files:**
- Create:
  - `web/vscode/src/panes/html.ts`, `web/vscode/src/panes/conversation.ts`;
  - `web/vscode/media/jam.svg`, a monochrome 24×24 single-path J mark with `fill="currentColor"`;
  - tests `html.test.ts`, `conversation.test.ts`.
- Modify:
  - `web/vscode/package.json`;
  - `.vscodeignore` (ship `media/` too);
  - `src/extension.ts`;
  - `test/vscode.ts` (the mock adds `window.registerWebviewViewProvider`, `window.state`, `window.onDidChangeWindowState`, `commands.executeCommand("setContext", …)`, `Uri.joinPath`, and a fake `WebviewView`);
  - `test/manifest.test.ts`, `test/extension.test.ts`;
  - `scripts/vscode.sh`.

**Interfaces:**
- Consumes: Tasks 3–6; slice 2's `Auth`, `signedOut()` and `JamApi`.
- Produces:
  - `html.ts`: `export function webviewHtml(o: { cspSource: string; nonce: string; script: string; style: string; pane: PaneName }): string`, which gives the Constraints' CSP `<meta>`, `<link rel="stylesheet" href=style>`, `<div id="root" data-pane=pane>` and `<script type="module" nonce=nonce src=script>`. All values are HTML-attribute-escaped. `export function newNonce(): string`.
  - `conversation.ts`: `export class ConversationViewProvider implements vscode.WebviewViewProvider` with `static readonly viewType = "jam.conversation"`.
    - Its constructor takes `{ extensionUri, hub, router, model, reporter, auth, jamUrl: () => string | Error, openExternal, executeCommand }`.
    - `resolveWebviewView` sets the options and HTML and attaches a `PanePort`. Messages: `ready` → `state`; `request`/`abort` → the router; `viewing` → `reporter.pane`; `open` → `intentAction` (`openExternal`, or `executeCommand`); `signIn` → `jam.signIn`.
    - `onDidChangeVisibility` → `reporter.pane`; `onDidDispose` → detach and `reporter.drop`.
    - On `model.onChange` and `auth.onChange` it posts `state` and sets `view.description` (the session's label when `elsewhere`, else `""`) and the context keys `jam.conversation.pinned` and `jam.conversation.hasSession`.
  - `package.json` additions:
    - `viewsContainers.activitybar: [{ id: "jam", title: "Jam", icon: "media/jam.svg" }]`;
    - `views.jam: [{ type: "webview", id: "jam.conversation", name: "Conversation" }]`;
    - commands `jam.focusSession` "Jam: Focus Session…", `jam.conversation.pin` "Pin to This Session" (`$(pin)`), `jam.conversation.unpin` "Follow the Window's Session" (`$(pinned)`);
    - `menus["view/title"]`: pin when `view == jam.conversation && jam.conversation.hasSession && !jam.conversation.pinned`, unpin when `view == jam.conversation && jam.conversation.pinned`, both `group: "navigation"`;
    - `menus.commandPalette` hides both pin commands (`when: "false"`).
  - `extension.ts` builds one `PaneHub`, `LiveStream`, `Router`, `FocusModel` (`windowDefault = studioFromAuthority(windowAuthority(vscode.workspace))`, `state = ctx.workspaceState`) and `FocusReporter` (`post` → `JamApi` `POST /app/user/push/focus`). The hub's `onEvent` is `model.invalidate` for `presence` and `open`. It registers the provider and the three commands. `auth.onChange` (signed in) → `live.restart()`. `windowFocused` follows `vscode.window.state.focused`, and `deactivate` → `reporter.dispose()`.
  - `scripts/vscode.sh`, before `esbuild`: `(cd ../app && npm ci --no-audit --no-fund --loglevel=error && npx vite build -c vite.vscode.config.ts --logLevel warn)`, then it fails unless `dist/webview/pane.js` and `dist/webview/pane.css` exist.

- [ ] **Step 1: Write the failing tests:**
  - html:
    - `the CSP is exactly the Constraints' string` for `cspSource "https://x.vscode-cdn.net"`, nonce `N`;
    - `script carries the nonce and the asWebviewUri path`;
    - `a quote in a path is escaped`;
    - `two nonces differ and are 44 base64 chars`.
  - manifest:
    - the view container, the view and the three commands exist;
    - `media/jam.svg` exists;
    - every contributed command is registered by `activate`;
    - `.vscodeignore` ships `dist/**` and `media/**`.
  - conversation, on the mock:
    - `resolve sets enableScripts, enableCommandUris false, localResourceRoots dist/webview, no retainContextWhenHidden`;
    - `ready gets state with the window's session`;
    - `a request is answered through the router`;
    - `open board calls openExternal with the jam URL`;
    - `open javascript: is dropped`;
    - `pinned and elsewhere → description is the label and context pinned is true`;
    - `signed-out change posts signedIn false to every pane` (Review Focus 2);
    - `dispose detaches from the hub (the stream closes with no panes)`.
  - extension: `activate registers the view provider and seven commands`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `just vscode` → PASS. `unzip -l web/vscode/dist/jam.vsix` lists `extension/dist/webview/pane.js`, `extension/dist/webview/pane.css` and `extension/media/jam.svg`. `just app` still passes its size budget.
- [ ] **Step 5: Commit** `feat(vscode): the Conversation view in a Jam activity-bar container`.

### Task 8: Docs and the manual checklist

**Files:**
- Modify:
  - `docs/usage/jam/vscode.md` (slice 2's leaf; bump `updated`). Add a **Conversation and focus** section, which owns:
    - the Jam activity-bar icon and the Conversation view, which can be dragged anywhere;
    - the focused session: the window's studio by default, *Jam: Focus Session…* and *This window's studio*;
    - pin and unpin from the view's title bar, and the session label shown when the pane's session isn't the window's;
    - the three empty states;
    - links opening in the browser (boards until the board editor lands);
    - push focus suppression while the view is visible in a focused window;
    - polling when live updates are off.
  - `docs/usage/jam/vscode-checklist.md`:
    - build: `vite.vscode.config.ts`, which `scripts/vscode.sh` runs, and its output `web/vscode/dist/webview/`;
    - the new manual steps:
      - the Jam icon shows, and the Conversation renders for the window's session in the light, dark and high-contrast themes;
      - send a message with an attachment and watch progress, then send an image and see the thumbnail;
      - answer an ask inline;
      - a message from the agent appears without a reload (live);
      - Focus Session to another session, and the view follows; pin, refocus, and the pinned view stays with its label shown; unpin;
      - drag the view to the panel and to the secondary sidebar;
      - a window on a local folder shows "Focus a session";
      - open a board card and a message link, and both open in the browser;
      - revoke the token while the view is open, and the view shows Sign in; Sign In again and the view recovers;
      - sleep the laptop for 2 minutes, wake it, and the messages catch up;
      - with Web Push on, the phone doesn't buzz for the visible conversation, and does once VS Code is unfocused;
      - DevTools (*Developer: Open Webview Developer Tools*) shows no CSP violations, and `fetch("https://example.com")` in its console is blocked.
  - `docs/web-app.md`:
    - Layout: a bullet for `src/vscode/` (the webview entry: `Bridge`, `install`, `linkAction`, `NavBridge`, `Pane`) and `src/bridge/protocol.ts`, which is shared with `web/vscode`;
    - the five seams, one line each, under `api/client.ts`, `composer/`, `live/` and `push/`, plus `blobs.ts` and `host.ts`;
    - Build: `vite.vscode.config.ts` is a second build, outside the size budget, run by `scripts/vscode.sh`, never by `scripts/app.sh`;
    - Security: the webview's own CSP lives in `web/vscode/src/panes/html.ts`, and links to `vscode.md`.
  - The spec's status line: "slice 3 (webview bridge, Conversation pane) built".
- [ ] **Step 1:** Write the docs. Each fact goes in the one doc named above; the others link to it.
- [ ] **Step 2:** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` → no new findings beyond the plan file's own.
- [ ] **Step 3:** `just test && just app-check && just app && just vscode && just lint` → PASS.
- [ ] **Step 4: Commit** `docs: the VS Code Conversation view, focus and the webview bridge`.
