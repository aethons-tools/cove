# Jam in VS Code — phase 1, the code window — design

**Status:** draft for the user's review, 2026-10-28; slice 1 (app tokens) and slice 2 (extension, sign-in, SSH, getting in) built; slice 3 (webview bridge, Conversation pane) built; slice 4 (the other panes) built.
**Context:** the Jam app (`web/app`, React 19 + Mantine 9) is the user and admin UI at `/app/user/` and `/app/admin/` ([Jam SPA](2026-10-26-jam-spa-design.md), [admin SPA](2026-10-28-admin-spa-design.md)). Studios already accept VS Code Remote-SSH through Jam (`jam ssh-config`, Jam-issued 8 h certs over a studio-dialed tunnel, port forwarding on — [collaboration](2026-10-20-studio-collaboration-design.md), `docs/usage/jam/collaboration.md`).

## Intent

The user finds VS Code's workbench an excellent host for what Jam is: independent panes the user arranges with VS Code's own layout (sidebars, panel, editor grid), next to the studio's real editor, terminal and files. **The goal is to move to VS Code completely**, keeping the web app working alongside it until then.

The model stays today's: **focus a session and every dependent pane switches to its context.**

### The three phases (the user agreed this order)

1. **The code window (this design).** One VS Code window per session's studio, with all the Jam panes. The web app stays the hub (rail, config, messaging).
2. **The hub.** A VS Code window with no remote: inbox rail, config tree and a messaging view; a button per session opens or focuses its code window; it knows which sessions have a window open. Its own design, later.
3. **Cutover.** VS Code becomes the main UI. The web app shrinks to messaging for the phone, Web Push stays for that. The PWA exists today to separate Jam from browser-tab noise and show notifications on the dock — VS Code takes that role.

The user wanted the code window first. The user is on desktop VS Code; desktop is the only target.

## Goals (phase 1)

- **Getting in:** an *Open in VS Code* button per session in the web app (the session header), and an extension link `vscode://aethons.jam/session/<ses_id>` that works from anywhere (a notification, a ticket comment, a terminal `open`). Opening it: ensures the user is signed in, ensures the SSH entry and cert for the session's studio, then opens `vscode-remote://ssh-remote+jam-<studio>/home/agent/workspace` — VS Code brings an existing window with that folder to the front instead of opening a second.
- **In the window:** Remote-SSH supplies the studio's editor, terminal and files. The Jam panes are **separate VS Code views**, each movable anywhere: Conversation (with composer and asks inline, as in the web app), Thinking, Previews, Status (the session header facts: state, mission, cost, context use, actions), Artifacts. Boards open as **editor tabs** (several may be open, side by side).
- **Focus and pin:** each window has a *focused session* — by default the one whose studio the window is attached to. Every pane follows it. Each pane has a pin toggle that holds it on another session; the pane's title shows which session it shows when that isn't the window's. A command (*Jam: Focus Session…*, a quick pick of the user's sessions) changes the window's focused session.
- **Attention:** a count badge on the Jam activity-bar icon (things needing you across all sessions, as "Needs you" counts in the web app); VS Code notifications for what the phone buzzes for (see *Notifications*); **real OS notifications** sent by the extension itself (VS Code's own notifications aren't visible outside the window). Clicking a notification for session X lands in X's code window (the link above). The user agreed to try this in place of the PWA's dock badge.
- **One sign-in** for the panes and SSH.

## Non-goals (phase 1)

- The hub window (phase 2); the config tree and admin screens in VS Code.
- One window spanning several studios (a Jam file system / own terminals).
- VS Code forks (Cursor, Windsurf): Microsoft's Remote-SSH isn't licensed for them. VS Code in the browser (vscode.dev). Marketplace publishing.
- The Studio tab (framed code-server) inside VS Code: the window itself *is* the studio. It stays in the web app.
- Retiring anything in the web app.

## Anti-goals

- **The studio never sees the user's Jam credentials.** The extension must run on the user's machine (`extensionKind: ["ui"]`), never as a workspace extension in the studio; no token is written into the studio or passed through the SSH session. (This is why "a Jam extension inside code-server" was rejected.)
- **No second UI to maintain.** Panes reuse `web/app` components; the extension adds an adapter, not a fork. A change to a pane in `web/app` shows up in both.
- **Not N live connections.** Live streams live in the extension host and fan out to the panes, never one per webview: one `/app/user/events` stream per window, plus one thinking stream (`/app/user/session/{id}/thinking/events`) per (session, stream) a Thinking pane shows, shared by the window's panes (one stream per session would need the server work deferred below). Merging thinking into the main stream would be a server change; not in phase 1 (my call).

## Constraints and the decided how

### Where the code lives (my call)

`web/vscode/` in the cove repo: the extension (TypeScript, its own `package.json`), built with the same Node toolchain as `web/app` (CI already runs setup-node). Webview bundles are built from `web/app` sources — a separate Vite entry per pane (or one bundle that renders the pane named in its boot data) — without React Router at the top; panes take their session id from the extension instead of the URL. Shared code may move within `web/app/src` so both entries import it; `web/app` itself keeps working unchanged. The `.vsix` is a CI artifact and a release asset; the user installs it by hand.

### Data flow (my call)

- **The extension host is the only thing that talks to Jam.** Webviews send requests over `postMessage`; the extension adds the bearer and calls `/app/user/api/*`, returning the JSON. This keeps the token out of the webviews and sidesteps CORS, cookies and the same-origin check (a webview's origin is `vscode-webview://…`). The web app's `apiAt` client gets a transport seam so the same query hooks run over either `fetch` or the bridge.
- **Live events:** the extension holds one `/app/user/events` stream per window (bearer, not cookie) and forwards each signal to every pane, where the existing invalidation logic (`useLiveEvents`) runs unchanged on the forwarded events.
- **Uploads and blobs:** attachments upload through the bridge (bytes as `ArrayBuffer`); images and blobs are fetched by the extension and handed to the webview as data/blob URLs (webviews can't send the bearer on `<img src>`).
- Each webview keeps its own TanStack Query cache; re-renders after a move or a hide come from re-fetching through the extension (cheap, local). `retainContextWhenHidden` only where state would be lost otherwise (the board editor).

### Sign-in: a Jam-minted app token (my call — the main server change)

The web app signs in by cookie; `jam login --participant` yields an IdP ID token that lasts about an hour with no refresh. Neither fits an extension. Phase 1 adds **app tokens**:

- *Jam: Sign In* opens the browser at `https://<jam>/app/auth/app-token?redirect=vscode://aethons.jam/auth&state=…&code_challenge=…` (PKCE). The user signs in with the normal browser login (or is already signed in); a consent page names the client ("VS Code on <machine>") and the user; Jam redirects to `vscode://aethons.jam/auth?code=…&state=…`; the extension exchanges the code (with the verifier) at `POST /app/auth/app-token/exchange` for a token.
- The token is opaque, random, stored hashed by Jam, bound to the **user**, with an expiry (30 days, sliding on use) and a label. It is accepted as `Authorization: Bearer` on the routes in *Scope* below. **It acts exactly as the user's browser session does there (the user decided):** a member is a member; a user who is an operator gets their operator standing in the user area too — SSH and previews on any session, Restart / End / Upgrade in the Status pane — `/app/admin/` refuses app tokens outright.
  - *How (my call — found while planning):* a browser session is an operator only because its IdP access-token cookie passes operator verification (`appui.Sessions.Resolve`); a token can't re-run that check per request. So the token records, at consent, the operator standing of the browser session that approved it (its operator id). A token with operator standing has a **fixed** 30-day expiry (no sliding), so it never outlives the IdP grant by more than that; revoking it, or removing the user, ends it at once. A member's token slides as above.
- Users can see and revoke their app tokens (a small list in the web app's account menu; an operator can revoke any). Revocation is immediate for API calls, and also ends the event stream. An SSH connection already open keeps running until it closes (its cert lasts at most 8 h); new connections and cert renewals are refused.
- The redirect target is fixed to the `vscode://aethons.jam/` prefix (and `vscode-insiders://`): no open redirect.
- **Scope (my call):** all of `/app/user/*` — the API, both event streams, uploads, blobs, board elements and thumbnails, and `push/focus` — except the Web Push subscription routes; plus the participant SSH routes. `/me/studios` is mounted today only when OIDC `device-client-id` and `ssh:` are both set (`cmd/jam/main.go`); app tokens loosen that to `ssh:` alone.
- Bearer requests skip the browser same-origin check (no cookie → no CSRF), and a request carrying both a cookie and a bearer is refused.
- The extension stores the token in VS Code `SecretStorage`.
- **One Jam per extension (my call):** a `jam.url` setting names it, one token. The CLI's several-Jam `--app` profiles aren't mirrored in phase 1; the link format leaves room for a `?jam=` parameter later.

### SSH (my call)

The extension does what `jam ssh-config --write <studio>` does, in TypeScript, using the app token: fetch the studio's cert into `~/.config/jam/ssh/`, write the `Host jam-<studio>` block between the same markers in `~/.ssh/config`. The ProxyCommand stays the `jam` binary (`jam ssh-proxy`), so the user needs the `jam` CLI installed; `ssh-proxy` learns to read a token the extension hands it (a token file under `~/.config/jam/` written by the extension, mode 0600, alongside the existing participant token, preferred when present) so one sign-in covers both. The extension sets `remote.SSH.localServerDownload: always` for `jam-*` hosts (or documents it) so VS Code's server is downloaded on the user's machine and copied over, not fetched by the studio (its egress allow-list doesn't have those hosts). The extension writes the block only for a studio that has none, or whose block it wrote itself; a block `jam ssh-config` wrote is left alone (it already works). **Host keys:** studio host keys live in the container's `/etc/ssh` and aren't kept across a new container; they are most likely baked into the image, so a restart keeps them and an upgrade changes them, after which Remote-SSH fails with "host identification changed" (`docs/usage/jam/collaboration.md`). Phase 1 adds a *Jam: Forget Host Key* command (runs `ssh-keygen -R jam-<studio>`, as `--prune` does) and offers it when a connection fails that way (my call); Jam-signed host certificates (`docs/TODO.md`) are the real fix, not in phase 1. **To verify first:** that this setting avoids any studio-side download, and whether VS Code asks for confirmation when a `vscode://vscode-remote/...` link opens a remote.

### Which session a window belongs to

The window's remote authority is `ssh-remote+jam-<studio>`, and the studio id **is** the session id (`/me/studios/{id}` and `/app/user/api/sessions/{id}` look up the same actor; a standing restart keeps the `ses_…` id, a reset makes a new one) — so no lookup is needed. The Conversation pane needs the session's channel, which the session header doesn't carry today: `GET /app/user/api/sessions/{id}` gains a `channel` field (my call).

**A session the user can see but may not SSH into** (the session header's `SSH` field is empty — the default `--collaborate-ssh none` for members): the web app hides *Open in VS Code*, and the link shows a VS Code error naming the reason, with "Open in the web app" (my call). Opening its panes in a window with no remote is the phase 2 hub's job. A window with no `jam-` remote has no default session; its panes show a "Focus a session" prompt (this is the seed of the phase 2 hub). A studio restart keeps the session (sessions outlive studios); a session that ended shows as ended in every pane.

### Previews

The web app frames previews through the preview domain with frame tickets, whose `frame-ancestors` lists `ui-origins` — a webview isn't one. In the code window, previews use Remote-SSH port forwarding: the Previews pane lists the session's exposed previews and opens one as `localhost:<forwarded port>` in VS Code's Simple Browser or the user's browser. A preview for a pinned session that isn't this window's studio opens its normal preview URL in the browser.

### Notifications

**A server-side notification feed (the user decided).** `/app/user/events` only says that something changed, and the rule for who gets buzzed lives in the push relay (`docs/usage/jam/push.md`, *What notifies*). Phase 1 adds a per-user notification event that the relay's same rule produces — any message in a chat or session conversation, an ask the user may answer, a call-in, an escalation; never their own, and with the same focus suppression — carrying a title, a short body, the channel and the session id. It goes out on the user's `/app/user/events` stream as `event: notify`. This relaxes the stream's "signals only, never content" rule ([Jam SPA](2026-10-26-jam-spa-design.md)) for this one event, sent only to its own recipient and carrying only what a push notification already shows. *How (my call):* a second relay surface (`notify`, alongside `push`) yields one delivery per `push.Recipients` user and publishes `push.Build`'s payload to an in-process per-user hub, so it gets the relay's exactly-once delivery and 10-minute drop for free; the events handler subscribes to the hub by user. The extension turns each into a VS Code notification, plus an OS notification while VS Code isn't focused (a focused window already shows the VS Code one; amended at slice 5's pre-flight, a builder call); clicking opens that session's code window (the link above), or the web app for a chat.

**Focus:** while a Conversation pane is visible in a focused VS Code window, the extension reports it to `POST /app/user/push/focus`, as the web app does, so neither the phone nor VS Code buzzes for the conversation being read (my call).

OS notifications via a small dependency (e.g. `node-notifier`, the builder's choice); with several code windows open, only one window notifies for each event (the windows agree through a lock file in the extension's global storage — my call). The web app's Web Push keeps working independently; a user with both gets both (acceptable in phase 1).

## Left to the builder

- Pane-by-pane adapter details, the bundle split, the bridge message shapes.
- Theming: map VS Code's theme variables to Mantine's (light/dark at least).
- The exact app-token tables/migration, consent page look, the token-list UI.
- How the Open in VS Code button detects that the extension is installed (it may simply link; VS Code reports a missing extension itself).
- Tests: unit tests for the bridge and token flow; Go tests for app tokens and bearer auth on the routes above; a manual end-to-end checklist (VS Code can't run in CI here).
- Slicing into PRs (suggested: app tokens; extension skeleton + sign-in + SSH + link; Conversation pane; the other panes; notifications).
