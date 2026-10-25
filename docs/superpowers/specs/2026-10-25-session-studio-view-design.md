# Session view, part 3: the studio view — design

**Status:** built (2026-10-25): #481 code-server in the image and cove-master, #482 `ide` routing, #483 the frame boundary, and #485 the UI.
**Amendments:** the IDE frame is kept across pane tab switches (only a conversation switch reloads it, as designed); the first open widens the pane to 60% unless a size was saved; `POST /app/user/session/{id}/frame` is limited to 30 mints per user per minute; an `ide` exposure is omitted from Previews.
**Context:** sub-project 3 of the session view ([part 1](2026-10-24-session-view-shell-design.md), [part 2](2026-10-24-session-thinking-view-design.md); user doc [session-view.md](../../usage/jam/session-view.md)). The pane gains a **Studio** tab, a web IDE (code-server) on the session's workspace, and the **Previews** tab shows previews inline. Both are framed inside `/app/user`.

## Decisions (2026-10-24)

- **The IDE.** Every studio gets one built in. A kit can override it by exposing its own preview named `ide`.
- **Access.** The Studio tab is gated on the **SSH** right. There is no browse-only mode.
- **Delivery.** code-server is **baked into `cove-base-image`** and started lazily by cove-master.
- **Inline display (approach A).** Jam frames the real preview host, so studio code never runs on Jam's origin. A frame-only login handles sign-in: a one-time ticket, then a partitioned cookie. Proxying previews under Jam's origin was rejected as an XSS hole.
- **Switching conversations** is a full page load, as today, so the IDE frame reloads (1–3 s). code-server and VS Code state survive. Keeping frames alive across switches is deferred.

## 1. The built-in IDE

**Image.** `images/cove-base-image` installs a pinned code-server release, verified against its published SHA-256 and pinned by a Renovate-managed version variable. The build runs `code-server --version` as a smoke test.

**The reserved port is `13337`.**
- `preview.IDEPort = 13337`.
- `preview.ValidPort` refuses it, as it already refuses 2222 and 3128, so an agent can't expose it.
- `ide` stays a valid preview name; that is how a kit overrides the built-in IDE.

**cove-master runs the IDE.**
- Its tunnel allow-list adds 13337 to sshd, the configured ports, and Jam's exposed ports.
- A tunnel to 13337 runs through an `ide` supervisor:
  1. If code-server isn't running, start it as the agent user (`code-server --bind-addr 127.0.0.1:13337 --auth none --disable-telemetry --disable-update-check /home/agent/workspace`).
  2. Wait up to **15 s** for the port to accept.
  3. Then dial as usual.
- If the binary is missing (an older image), the tunnel is refused and the log says `ide unavailable: code-server not installed`.
- The supervisor counts open tunnels to 13337. After **30 min** with none, it stops code-server: SIGTERM, then SIGKILL after 10 s. It logs `ide started` and `ide stopped`.
- `--auth none` is acceptable. Only Jam's tunnel and in-studio processes (the agent, which owns the workspace) reach loopback.

**Jam routing.** A preview request for the name `ide` resolves this way:
1. If the session has an exposure named `ide`, use its port. This is the kit override.
2. Otherwise use `preview.IDEPort`, unless the Jam has no `previews:` block. The `ide` host only exists where previews do.

Either way, `ide` requests are authorised for **`collab.SSH`**, not `collab.Preview`. Collab's live re-check ends open connections within a minute of losing SSH. An older studio's refused tunnel surfaces as Jam's existing 502/504, and the Studio tab explains it (§3).

## 2. Framing and the frame login

**Frame ticket.**
- The UI calls `POST /app/user/session/{id}/frame` with `name=<preview name>` (`ide` for the Studio tab).
- It needs the viewer's `sessionFor` access and the right for that name (`ssh` for `ide`, otherwise `preview`), else 404 or 403. The name must be exposed, or be `ide` (§1). Otherwise 404.
- The ticket is 32 random bytes, stored as a SHA-256 hash, single-use and valid for **60 s**. It is bound to (user id, preview host, right).
- Tickets live in an in-memory store shared by userui and the preview auth in the same serve process.
- The route returns JSON `{"url": "https://<name>--<label>.<domain>[:port]/__jam/frame?t=<ticket>"}`.

**Exchange.** `GET /__jam/frame?t=` on a preview host.
- It is only valid when `Sec-Fetch-Dest: iframe`; otherwise 400.
- *Amended 2026-10-25:* it requires an iframe navigation with `Sec-Fetch-Site` `cross-site` or `same-site` (never `same-origin`, `none` or absent), so a UI sharing the previews' registrable domain can frame them; the frame cookie itself is honoured only same-origin or on a same-host WebSocket.
- The ticket must match the host and be unused and unexpired; otherwise 400 "frame link expired — reopen the tab". It is consumed on first use.
- On success it creates a **frame session** (8 h, server-side, bound to user, host and right) and sets `__Host-jam_frame`, with the attributes `Secure; HttpOnly; Path=/; SameSite=None; Partitioned`.
- It then responds 303 to `/`.

**When the frame cookie is honoured.** It authenticates a request only when:
- it is an iframe navigation (`Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest: iframe`); or
- `Sec-Fetch-Site: same-origin`; or
- it is a WebSocket upgrade whose `Origin` is the same host.

Any other use, including another studio framed in the same page, gets 403 "cross-site request refused", as today. The right is re-checked on every request through collab, just as for `__Host-jam_preview`. Frame sessions are pruned with the existing preview sessions. When both cookies are present, the top-level `__Host-jam_preview` rules apply first.

**Response headers.** Every preview-host response gets `Content-Security-Policy: frame-ancestors <UI origins>`. The UI origins are the configured trusted UI origins (`ui_origins`). With none configured it falls back to `frame-ancestors 'none'`, so framing stays off. An app's own CSP is kept; browsers enforce both. An app's `X-Frame-Options` is removed only on responses to frame-authenticated requests.

**Browsers.** Chrome, Edge and Firefox work. Safari blocks third-party cookies, so the exchange's cookie isn't stored and the next request lands on the login redirect. The frame's login page detects `Sec-Fetch-Dest: iframe` and renders a small page saying "This browser can't sign in inside the pane — Open in new tab" (`target=_top`) instead of redirecting to the IdP inside a frame.

**Audit.** `preview frame opened` logs user, session, name and right on each ticket exchange.

## 3. UI

**Studio tab.** It appears in the header tabs only for viewers whose `collab.Decide(…, SSH)` allows, the same check as Copy SSH.
- Opening it fetches a ticket and loads the iframe, with "Starting the IDE…" until `load`.
- The toolbar has **Open in new tab** (the plain preview URL, top-level login) and **Reload** (a new ticket).
- The iframe is kept, hidden, when switching pane tabs, and removed when the pane closes or the page unloads.
- The Studio tab's first open widens the pane to 60% of the split, unless a size was saved.

**Previews tab.**
- Each exposure gets **View here** (framed in the pane, with "← All previews") and **Open in new tab**.
- One framed preview at a time.
- An exposure named `ide` is listed under Studio, not here.

**Errors in a frame.** Jam's preview error responses (404 not exposed, 403 refused, 502/504 studio unreachable or IDE not started) gain a small framed variant. When the request was frame-authenticated, the page `postMessage`s `{jam: "frame-error", status}` to the parent; the target origin is the UI origin. The tab then shows:
- 403: "No longer shared with you."
- 404: "Nothing to show: the studio isn't running."
- 502/504: "The IDE didn't start. This studio's image may predate it: upgrade the studio, or expose an IDE as `ide`."
- The Safari page posts `{jam: "frame-error", status: "cookies"}`.

## 4. Testing

- **covemaster:** 13337 allowed; lazy start on first dial, with a fake `code-server` script in a temp `PATH` that listens on the port; a 15 s timeout; idle stop after the (shortened) idle timer; refusal when the binary is absent; tunnel counting.
- **preview:** `ValidPort(13337)` refused; `ide` is a valid name.
- **collabhttp:**
  - `ide` resolves to an exposure if present, else to 13337;
  - `ide` is authorised with SSH: a preview-only grant gets 403, an SSH holder passes;
  - ticket mint, single use, 60 s expiry, and binding (wrong host refused);
  - the exchange requires `Sec-Fetch-Dest: iframe`;
  - the frame cookie is accepted for iframe navigation, same-origin and a same-host WebSocket, and refused for a cross-site fetch and another host's origin;
  - `frame-ancestors` on every response, and `'none'` without UI origins;
  - `X-Frame-Options` stripped only on frame-authenticated responses;
  - the Safari page on a framed login redirect;
  - the framed error variant posts its message.
- **userui:** the frame route answers 404 for outsiders, 403 without the right, and 404 for a name that isn't exposed. The Studio tab renders only with SSH. Previews gets View here.
- **Browser** (`-tags browser`): the pane frames an httptest preview host. The test drives the exchange, checks content renders, the cookie is sent, a WebSocket echo works, and a cross-host fetch is refused. Headless Chrome needs no cross-site cookie flags for the partitioned cookie; if it does, the test sets them.
- **Image:** the base-image build's `code-server --version`.

## 5. Delivery

1. **The IDE in the studio:** base-image code-server, `preview.IDEPort`, and cove-master's allow-list, lazy start and idle stop.
2. **Jam `ide` routing:** resolution, and the SSH right for `ide` requests.
3. **The frame boundary:** tickets, the frame route, the exchange, the frame cookie and session, framing headers, the Safari page and the framed error pages. This PR gets an opus security review before merge.
4. **The UI:** the Studio tab, Previews View here and the error display. Docs: previews.md (framing, the `ide` name and right, the reserved port), session-view.md (Studio and View here), collaboration.md (`ide` uses SSH), and the image doc for code-server.
