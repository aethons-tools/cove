# VS Code slice 5 (notifications) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jam tells the user, in VS Code, about what their phone buzzes for:
- **the server** sends a per-user `event: notify` on `/app/user/events` for each squawk that the Web Push rule would push to them (any chat or session message, an ask they may answer, a call-in, an escalation; never their own; never for the conversation they're reading);
- **the extension** turns each into a VS Code notification and, while VS Code isn't focused, a **real OS notification**. Only one window notifies per event. Clicking opens that session's code window (or the web app for a chat);
- **a badge** on the Jam activity-bar icon counts what "Needs you" across all sessions, as the web app's rail does.

The web app's Web Push keeps working unchanged and independently.

**Architecture:**
- **The rule is the push relay's, not a copy.** A second relay service, `notify`, runs beside `push` over the same channel log, markers and `directory`. Its `Surfaces` lists one delivery per `push.Recipients` user (the live audience, minus the author, minus the people `push.Focus` says are reading that channel, minus anyone who already read it). Its `Deliver` re-checks the recipient at send time, builds the payload with `push.Build` (through cmd's existing `pushPayload`), adds the session id, and publishes it to an in-process per-user `notify.Hub`. The engine gives it the relay's exactly-once delivery, and the surface gives it the 10-minute drop (`push.MaxAge`).
- **New package `internal/jam/notify`.** It holds `Event` (push's `Payload` plus `session`), `Hub` (per-user fan-out, never blocking a publisher), and `Surface` (`relay.Surface` for service `"notify"`: egress only, no ingress).
- **userui.** `WithNotify(hub)` makes `/app/user/events` subscribe to the viewer's own events and write each as `event: notify` with one JSON line. Only this event carries content (the spec's one exception). `POST /app/user/push/focus` is mounted with `WithPushFocus` plus either `WithPush` or `WithNotify`, so focus suppression works on a Jam without VAPID keys.
- **`jam.HomeSession`.** `userui.homeSession` (a channel's live session: a session channel's own, or the session working a ticket channel) moves to `internal/jam`, so cmd can name the session in a notification.
- **The extension.** Pure modules under `web/vscode/src/notify/`:
  - `event.ts` parses the event, keeps bursts down with `ChannelGate`, and builds the click target;
  - `lock.ts` lets only one window claim each event, through a lock file in global storage;
  - `os.ts` sends OS notifications through argv-only child processes: macOS uses the ad-hoc signed (not notarized) `terminal-notifier.app` vendored from `node-notifier@12.0.0` (falling back to `osascript`), and Linux uses `notify-send`;
  - `notifier.ts` ties them together.

  `needsyou.ts` is a small **Needs You** tree view in the Jam container. Its `TreeView.badge` is the activity-bar count. The window's one `LiveStream` gains a permanent subscriber, so notifications and the badge work with every pane closed. `PaneHub` still drops `notify`.

**Tech Stack:** Go (net/http SSE, the `internal/relay` engine), TypeScript, the VS Code API (`window.createTreeView` with `TreeView.badge`, `show*Message`, `ExtensionContext.globalStorageUri`), Node `child_process.execFile` and `fs/promises`, Vitest. On macOS, `terminal-notifier` 3.0.0 (as vendored by `node-notifier@12.0.0` from registry.npmjs.org) or `osascript`; on Linux, libnotify's `notify-send`.

**Spec:** `docs/superpowers/specs/2026-10-28-vscode-code-window-design.md` ("Goals" → *Attention*, "Notifications", "Anti-goals" → *Not N live connections*, "Left to the builder"). This is slice 5 of 5. It consumes:
- slice 3 (`docs/superpowers/plans/2026-10-29-vscode-3-conversation.md`): `LiveStream`, `LiveEvent`, `PaneHub`, `FocusReporter` and `JamApi.pushFocus`;
- slice 2: `openSession`, `webUrl`, `sessionOfPick`, `JamApi.rail`/`conversation`, `SESSION_ID_RE` and `Auth`;
- slice 4: `ID_RE`, `perform`/`ActionDeps`, and `signOutOnce`/`panesSignedOut` in `extension.ts`;
- the push design (`docs/superpowers/specs/2026-10-23-jam-push-notifications-design.md`): `push.Recipients`, `push.Build`, `push.Focus`, `push.MaxAge`, `pushReader` and `pushPayload`.

## Global Constraints

- **Web Push is unchanged.** `push.Surface`, `pushSurfaces`, subscriptions, payloads and the `push` relay marker behave exactly as before. `TestSurfacesPush`, `TestSurfacesPushAsk`, `TestPushEngineDeadEndpoint`, `TestPushPayload*` and every `internal/jam/push` test pass unedited. No file in `web/app/src` changes.
- **One rule.** Recipients come only from `push.Recipients`, and the wording only from `push.Build` (via `pushPayload`). Suppression comes from the one `*push.Focus` that `/app/user/push/focus` writes. No second "who is notified" predicate exists anywhere, in Go or TypeScript.
- **The `notify` event:**
  - it goes only to its recipient's streams (`ParticipantFrom(r).UserID`), never to another user's;
  - the SSE frame is exactly `event: notify\ndata: <json>\n\n`, where `<json>` is one line: `{"title","body","channel","id","unread","kind","session"}`. `id` is always the squawk id, even when `Build` cleared it for size. `session` is `""` when the channel has no live session. `kind` is `"needs"` or `"messages"`;
  - `body` honours `runtime.push.preview`: `false` gives "New message". Without `runtime.push` it is `true`, push's own default.
- **Delivery:**
  - relay service `"notify"` (`notify.Service`), egress only: `Projects` is nil and `Route` is false;
  - its marker is kept **in memory** (not `relay-markers.json`) and seeded at the log tail on every start. Its ingress cursors are a no-op. It needs no state dir, and the relay state-dir condition is unchanged;
  - a squawk older than `push.MaxAge` (10 min) is dropped;
  - a recipient with no open stream gets nothing, and nothing is queued for later. `Deliver` returns before building the event (no `pushPayload`, no `UnreadTotal`) when `Hub.Subscribers(user) == 0`;
  - it runs whenever the user app is served (`cfg.AdminListen != ""`), independent of `runtime.push`.
- **Hub:**
  - `notify.SubBuffer = 16` events per subscriber;
  - `Publish` never blocks: a full subscriber drops the event and the drop is logged at Debug with the user id, never the body;
  - unsubscribe is idempotent.
- **Focus route:**
  - `POST /app/user/push/focus` is mounted iff `WithPushFocus(f)` and (`WithPush(key≠"")` or `WithNotify(hub≠nil)`). Otherwise it is `404`;
  - the bootstrap's `pushFocus` is true under the same condition;
  - app-token scope is unchanged, so focus stays allowed and the subscription routes stay refused.
- **The extension:**
  - no token, `jam.url` or notification content enters a webview. `PaneHub.FORWARD` is unchanged;
  - no new host is contacted, and every Jam call goes through `JamApi` or `LiveStream`;
  - child processes are started with `execFile` (no shell), with every value a separate argv entry.
- **Constants** (`src/notify/*.ts`, `src/needsyou.ts`):
  - `NOTIFY_CHANNEL_GAP_MS = 5000`;
  - `NOTIFY_UNFOCUSED_DELAY_MS = 250`;
  - `NOTIFY_LOCK_TTL_MS = 15 * 60_000`, swept at most once per `NOTIFY_SWEEP_MS = 60_000`;
  - `NOTIFY_WAIT_MS = 10 * 60_000` (a Linux `--wait` child is killed after this);
  - `NOTIFY_TITLE_MAX = 120`, `NOTIFY_BODY_MAX = 200` (runes, after parsing);
  - `NEEDS_YOU_DEBOUNCE_MS = 3000`.
- **OS notifiers:**
  - **macOS:** `<ext>/dist/vendor/mac.noindex/terminal-notifier.app/Contents/MacOS/terminal-notifier`, copied unmodified from `node-notifier@12.0.0`: `_CodeSignature` intact (ad-hoc signed, not notarized), and `Contents/Resources/Terminal.icns` (Apple's Terminal icon) kept, as node-notifier ships it. Removing it would break the sealed resources, and the .vsix is private. Every title and message gets a leading U+200B. A spawn failure (ENOENT, EACCES, EPERM, or death by a signal that isn't our own timeout kill) switches the window to `osascript` for the session; any other non-zero exit falls back to `osascript` for that one note only. `osascript` gets the values as `argv` items, never interpolated into the script;
  - **Linux:** `notify-send`;
  - **other platforms:** none (the VS Code notification only);
  - `node-notifier` is a `devDependency` pinned to `12.0.0` and is never bundled or `require`d.
- **Copy:**
  - VS Code notification: `"<title>: <body>"`, with one action, `"Open"`. A `needs` event uses `showWarningMessage`, and a `messages` event `showInformationMessage`;
  - the OS notification's title is `<title>` and its message `<body>`. On Linux the app name is "Jam" and the action is labelled "Open";
  - view name: "Needs You". Welcome text, signed out: "Sign in to Jam to see what needs you.\n[Sign In](command:jam.signIn)". Signed in and empty: "Nothing needs you right now.";
  - badge tooltip: "1 conversation needs you" / "<n> conversations need you".
- **Tests are hermetic** (Go: no network, fake clocks where timing matters; extension: fake `execFile`, a temp dir for locks, fake timers).

## Review Focus

1. **Three code windows open, one focused.** A message arrives. Exactly one VS Code notification appears, in the focused window, and no OS notification while it is focused. With no Jam window focused, exactly one window shows both. Task 4 tests the lock; Task 6 tests "focused wins" and "no OS notification while focused".
2. **The user is reading that conversation**, in VS Code's Conversation view or in a focused web-app tab. Nothing notifies, on the phone or in VS Code. Focus that arrives after `Surfaces` listed the user but before delivery still suppresses it, because the recipient is re-checked at delivery. And this holds on a Jam **without** `runtime.push`. Task 2 tests the route without push; Task 3 tests the focused recipient at both `Surfaces` and `Deliver`.
3. **A hostile or odd `notify` payload** (the server is trusted, but a body is someone's message):
   - malformed JSON, or a non-string `title`;
   - a body of `-help`, `[x]`, `<b>&</b>` or `$(rm -rf ~)`, or 5,000 runes;
   - `session: "../x"`, `channel: "javascript:alert(1)"`.

   No shell runs. Option-like values can't become flags. Linux markup is escaped. A link is built only from ids that pass `SESSION_ID_RE`/`ID_RE.chn`: a bad `channel` drops the event, and a `session` failing `SESSION_ID_RE` (a hostile one, or a grandfathered non-`ses_` actor id) becomes `""`, so a click opens the web app. Tasks 4 and 5 test it.
4. **A burst.** An agent posts 20 messages in 10 s to one session channel, and asks one question in the middle. That shows at most two message notifications for that channel (one per 5 s), and the ask always. Every window drops the same ones, because the gate runs on events seen, before the lock. Task 4 tests it.
5. **No OS notifier works:**
   - Linux without `notify-send`, or with a pre-0.7.10 one without `--action`;
   - macOS with the vendored app blocked (quarantine, lost exec bit).

   The VS Code notification still shows. macOS falls back to `osascript`, and an old `notify-send` notifies without a click action. The failure is logged once per window, not per event. Task 5 tests it.

---

### Task 1: `internal/jam/notify` (event, hub, relay surface)

**Files:**
- Create: `internal/jam/notify/notify.go` (package doc, `Event`, `Service`, `SubBuffer`), `internal/jam/notify/hub.go`, `internal/jam/notify/surface.go`.
- Test: `internal/jam/notify/hub_test.go`, `internal/jam/notify/surface_test.go`.

**Interfaces:**
- Consumes: `push.Payload`, `push.MaxAge` (`internal/jam/push`); `relay.Surface`, `relay.Delivery`, `relay.Event` (`internal/relay`); `intercom.Squawk`; `ident.ID`.
- Produces:
  ```go
  package notify
  const Service = "notify"
  const SubBuffer = 16
  // Event is one notification for one user: push's payload plus the session to open.
  type Event struct {
      push.Payload
      Session string `json:"session"` // "" = none (a chat, or no live session)
  }
  type Hub struct{ /* mu; subs map[ident.ID]map[*sub]struct{}; log */ }
  func NewHub(log *slog.Logger) *Hub                            // nil log: discard
  func (h *Hub) Subscribe(user ident.ID) (<-chan Event, func())  // buffered SubBuffer; cancel idempotent, closes nothing the reader selects on after return
  func (h *Hub) Publish(user ident.ID, e Event)                  // to each of user's subscribers; full → drop (Debug log: user, id)
  func (h *Hub) Subscribers(user ident.ID) int                   // open subscriptions of user (tests, debug)
  type Surface struct {
      Hub   *Hub
      Event func(m intercom.Squawk, user ident.ID) (Event, bool) // ok=false: no longer a recipient
      Now   func() time.Time                                       // nil: time.Now
  }
  func (s *Surface) Service() string                               // Service
  func (s *Surface) Deliver(ctx context.Context, d relay.Delivery, m intercom.Squawk) (string, error) // d.Address is the user id
  func (s *Surface) Poll(context.Context, string, string) ([]relay.Event, string, error)           // nil, "", nil
  func (s *Surface) Close() error
  ```
  - `Deliver`:
    - returns `"", nil` in every case, since a notification is best effort and must never stall the log;
    - drops the squawk when `Now()-m.At > push.MaxAge`;
    - then returns when `Hub.Subscribers(user) == 0`, before calling `Event`, so a Jam where nobody uses VS Code pays no `pushPayload` or `UnreadTotal` per message;
    - publishes `Event(m, user)` when it's ok, and drops it otherwise.

- [ ] **Step 1: Write the failing tests:**
  - hub:
    - `TestHubFansOutToEachSubscriberOfTheUser`: two subscriptions for alice both receive one `Publish`;
    - `TestHubOtherUsersGetNothing`: bob's subscription stays empty;
    - `TestHubPublishNeverBlocks`: 17 publishes to an unread subscriber return within 100 ms, and the reader then gets exactly 16;
    - `TestHubCancelStopsDeliveryAndIsIdempotent`;
    - `TestHubConcurrentPublishSubscribe` (run with `-race`).
  - surface:
    - `TestSurfaceDeliverPublishesTheEvent`: `Address: "usr_alice…"`. The event returned by `Event` reaches alice's subscription, and receipt and error are empty;
    - `TestSurfaceDropsSquawksOlderThanMaxAge` (fake `Now`; `m.At` 11 min old → nothing published, `Event` not called);
    - `TestSurfaceSkipsWhenNoLongerARecipient` (`Event` → false);
    - `TestSurfaceSkipsUsersWithNoStream`: alice has no subscription → `Event` is never called;
    - `TestSurfaceHasNoIngress` (`Poll` → nil, `Service()` → `"notify"`);
    - `TestEventJSON`: `json.Marshal(Event{push.Payload{Title:"a · b", Body:"hi", Channel:"chn_x", ID:"sqk_1", Unread:2, Kind:"messages"}, "ses_y"})` equals `{"title":"a · b","body":"hi","channel":"chn_x","id":"sqk_1","unread":2,"kind":"messages","session":"ses_y"}`.
- [ ] **Step 2:** `go test ./internal/jam/notify/` → FAIL (undefined).
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race ./internal/jam/notify/ && go vet ./internal/jam/notify/` → PASS.
- [ ] **Step 5: Commit** `feat(jam): notify package — per-user notification hub and relay surface`.

### Task 2: userui — `event: notify` on the user's stream, focus without push

**Files:**
- Modify:
  - `internal/jam/userui/handler.go`: a `notify *notify.Hub` field;
  - `internal/jam/userui/events.go`: `WithNotify`, the subscription, the frame, and the 204 condition;
  - `internal/jam/userui/push.go`: `pushRoutes` and `focusOn`, and `WithPushFocus`'s doc comment ("It takes effect only with WithPush") becomes "with WithPush or WithNotify";
  - `internal/jam/userui/push_test.go`: `TestPushFocusOffWithoutPush` becomes `TestPushFocusOffWithoutPushOrNotify`, and is the only edited test.
- Test: `internal/jam/userui/events_test.go`, `internal/jam/userui/push_test.go`, `internal/jam/userui/api_test.go`.

**Interfaces:**
- Consumes: Task 1's `Hub.Subscribe`, `Event`.
- Produces:
  ```go
  // WithNotify streams the viewer's own notifications (hub) on GET
  // /app/user/events as `event: notify`, and turns on push focus without Web Push.
  func WithNotify(hub *notify.Hub) Option
  ```
  - **`events`:**
    - it answers 204 only when `changes`, `presence`, `Boards` and `notify` are all nil;
    - it subscribes `h.notify.Subscribe(p.UserID)` before writing the 200 and cancels it on return;
    - each event is `json.Marshal`ed and written as `event: notify\ndata: %s\n\n`, then flushed. A marshal error skips the event;
    - the doc comment's "No message content" sentence names the one exception.
  - **`pushRoutes`:** the subscription and key routes stay gated on `pushKey`. Focus is mounted when `pushFocus != nil && (pushKey != "" || notify != nil)`, else it is `NotFoundHandler`. `/app/user/push/` keeps answering 404 for the rest when push is off.
  - **`focusOn()`:** `h.pushFocus != nil && (h.pushKey != "" || h.notify != nil)`.

- [ ] **Step 1: Write the failing tests** (`eventsServer`, `readUntil` as in `events_test.go`):
  - `TestEventsStreamsNotifyToItsRecipient`:
    - alice's stream, opened with `WithNotify(hub)` only (no Changes);
    - `hub.Publish(alice, ev)` gives the lines `event: notify` and `data: {"title":…,"session":"ses_…"}`, which unmarshal back to `ev`.
  - `TestEventsNotifyNeverReachesAnotherUser`:
    - streams for alice and bob;
    - publish to bob, then a `changed` signal;
    - alice reads `event: changed` with no `event: notify` before it.
  - `TestEventsWithOnlyNotifyIsAStream`: it answers 200 `text/event-stream`, not 204.
  - `TestEventsNotifyUnsubscribesOnDisconnect`: after cancelling the request, `hub.Subscribers(alice)` drops back to 0 (polled up to 2 s).
  - `TestPushFocusOnWithNotifyAlone`: `Handler(e.Deps, nil, WithNotify(hub), WithPushFocus(f))`. POST `{"channel": engID}` → 204, and `f.Focused(alice, engID)`.
  - `TestPushFocusOffWithoutPushOrNotify`: the renamed table, plus `WithNotify(hub)` alone (no focus) → 404.
  - `TestPushSubscriptionRoutesStayOffWithNotifyAlone`: `GET /app/user/push/key` → 404.
  - `api_test.go`, `TestBootstrapPushFocusWithNotify`: `WithNotify` + `WithPushFocus` → `pushFocus: true`.
- [ ] **Step 2:** `go test ./internal/jam/userui/ -run 'Events|PushFocus|Bootstrap|PushSubscription'` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test -race ./internal/jam/userui/ ./internal/jam/appui/` → PASS. The app-token bearer tests in `appui` are untouched and pass.
- [ ] **Step 5: Commit** `feat(userui): notify events on the user's stream; push focus without Web Push`.

### Task 3: Serve wiring — the `notify` relay over the push rule

**Files:**
- Create:
  - `internal/jam/home_session.go`, with `internal/jam/home_session_test.go`;
  - `cmd/jam/notify.go` (`notifyEvent`, `(*directory).notifySurfaces`, `memMarkers`, `noCursors`), with `cmd/jam/notify_test.go`.
- Modify:
  - `internal/jam/userui/session.go`: `homeSession` becomes a call to `jam.HomeSession`, and its body moves;
  - `cmd/jam/relay_dir.go`:
    - `directory` gains `notifyOn bool`;
    - `Projects` returns nil for `notify.Service`;
    - `Surfaces` routes `notify.Service` to `notifySurfaces`;
    - `Route` returns false for it;
  - `cmd/jam/main.go`:
    - `notifyHub := notify.NewHub(log)` next to `pushFocus` (`:2229`);
    - `runNotify := cfg.AdminListen != ""`. The relay state-dir condition (`:2570`) is **unchanged**;
    - `dir.notifyOn = runNotify`;
    - `reader := pushReader{…}` and `unread := …` are hoisted out of `if runPush {}` (`:2773-2774`) to above both engines, and the push block uses the hoisted ones;
    - the notify engine is started after the push block (`:2766`), on `memMarkers` seeded with `{LastSeq: logTailSeq(chlog)}` and on `noCursors`, not on `relayMarkers`/`relayCursors`;
    - `userui.WithNotify(notifyHub)` is passed only when `runNotify` (`:2895`).
- Test: `cmd/jam/notify_test.go`, `internal/jam/home_session_test.go`, `cmd/jam/relay_dir_test.go` (unchanged push cases must pass).

**Interfaces:**
- Consumes: Tasks 1–2; `push.Recipients`, `pushReader`, `pushPayload`, `push.Focus`, `jam.UnreadTotal`, and `newRelayKit` (`relay_dir_test.go`).
- Produces:
  ```go
  // internal/jam
  // HomeSession is channel ch's live session: a session channel's own, or the
  // live session working a ticket channel's unit in its project (ok=false: none).
  func HomeSession(st Store, ch ident.ID) (Instance, bool)
  // cmd/jam
  func notifyEvent(r pushReader, focus *push.Focus, unread func(ident.ID) int, preview bool) func(intercom.Squawk, ident.ID) (notify.Event, bool)
  func (d *directory) notifySurfaces(m intercom.Squawk) []relay.Delivery // one per push.Recipients user: {Service: notify.Service, Address: string(user)}
  type memMarkers struct{ /* mu; m map[string]relay.EgressMark */ } // relay.Markers in memory: nothing is persisted
  func newMemMarkers(service string, seed relay.EgressMark) *memMarkers
  type noCursors struct{}                                              // relay.Cursors: Ingress "" and SetIngress nil (notify has no ingress)
  ```
  - **`notifyEvent`:**
    - it calls `pushPayload(r, focus, unread, preview)(m, user)` (the send-time re-check) and returns false when that does;
    - `Payload.ID` is set to `m.ID`;
    - `Session` is `jam.HomeSession(r.store, m.Channel)`'s `ActorID`, or `""` (a grandfathered non-`ses_` id is passed as is; the extension maps it to `""`).
  - **`notifySurfaces`:**
    - it returns nil unless `d.notifyOn`;
    - otherwise it uses `push.Recipients(pushReader{…}, m, d.pushFocus.Focused)` (nil focus: none suppressed), exactly as `pushSurfaces` does but with no device loop;
    - parts are not paused (asks notify), as with push.
  - **Preview:** in `main.go`, `preview := cfg.Runtime.Push == nil || cfg.Runtime.Push.preview()`, never a call on a nil `*pushConfig`.
  - **The engine:** `relay.New(&notify.Surface{Hub: notifyHub, Event: notifyEvent(reader, pushFocus, unread, preview), Now: time.Now}, chlog, newMemMarkers(notify.Service, relay.EgressMark{LastSeq: logTailSeq(chlog)}), noCursors{}, dir, relay.Config{EgressEnabled: true}, log)`. It logs `Jam relay (notify): resident, egress ON`.

- [ ] **Step 1: Write the failing tests:**
  - `TestHomeSession` (in `internal/jam`), a table:
    - a session channel with a live instance → that instance;
    - a gone instance → false;
    - a ticket channel with a live session on its unit in the same project → it;
    - the same unit in another project → false;
    - a chat → false;
    - an unknown channel → false.
  - `notify_test.go`:
    - `TestSurfacesNotify`:
      - a session message from the personal session to alice → one delivery `{notify, alice}`;
      - alice's own message → none;
      - with `pushFocus.Set(alice, ch)` → none (Review Focus 2);
      - `notifyOn` false → nil;
      - an ask naming bob in a ticket channel → a delivery for bob.
    - `TestNotifyEventCarriesSessionAndID`:
      - a personal-session squawk → `Session` is that session's id, and `ID == m.ID`;
      - a chat squawk → `Session == ""`;
      - `preview=false` → `Body == "New message"`.
    - `TestNotifyEventRechecksAtDelivery`: `notifyEvent` returns false once alice is focused on the channel after `Surfaces` ran (Review Focus 2).
    - `TestNotifyEngineDeliversToTheRecipientsStream`:
      - like `TestPushEngineDeadEndpoint`: a real `relay.New` with a 5 ms egress poll;
      - alice subscribed to the hub receives one `Event` with the squawk's title and channel;
      - bob (also subscribed, not a member) receives nothing;
      - the engine runs on `newMemMarkers` seeded below `m.Seq`, and `markers.Egress("notify").LastSeq` reaches `m.Seq`.
    - `TestNotifyHasNoIngress`: `Projects("notify")` is nil, and `Route("notify", …)` is false.
    - `TestMemMarkers`: `Egress` of the seeded service is the seed, of another the zero mark, and `SetEgress` then `Egress` round-trips; nothing is written to disk (no path exists to write to).
- [ ] **Step 2:** `go test ./internal/jam/ -run TestHomeSession && go test ./cmd/jam/ -run 'Notify'` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `just test && just lint` → PASS (`go test ./internal/jam/userui/` covers the `homeSession` move).
- [ ] **Step 5: Commit** `feat(jam): notify relay — the push rule's notifications on the user's event stream`.

### Task 4: Extension — notify events, the channel gate and the window lock

**Files:**
- Create: `web/vscode/src/notify/event.ts`, `web/vscode/src/notify/lock.ts`, `web/vscode/test/notifyevent.test.ts`, `web/vscode/test/notifylock.test.ts`.

**Interfaces:**
- Consumes: `ID_RE` (`bridge/router.ts`), `SESSION_ID_RE` (`uri.ts`), `webUrl` (`open.ts`).
- Produces:
  ```ts
  // notify/event.ts
  export const NOTIFY_CHANNEL_GAP_MS = 5000;
  export const NOTIFY_TITLE_MAX = 120;
  export const NOTIFY_BODY_MAX = 200;
  export type NotifyKind = "needs" | "messages";
  export type NotifyEvent = { id: string; title: string; body: string; channel: string; session: string; kind: NotifyKind };
  export function parseNotify(data: string): NotifyEvent | undefined
  export class ChannelGate {                       // per window; fed every parsed event, before the lock
    admit(e: NotifyEvent, now: number): boolean   // needs → always true; messages → false within NOTIFY_CHANNEL_GAP_MS of the last admitted messages event of that channel
  }
  export type NotifyTarget = { session: string } | { web: string };
  export function notifyTarget(e: NotifyEvent, jamUrl: string): NotifyTarget      // session ≠ "" → {session}; else {web: webUrl(jamUrl, channel)}
  export function targetLink(t: NotifyTarget, scheme: string): string              // {session} → `${scheme}://aethons.jam/session/${id}`; {web} → the URL
  // notify/lock.ts
  export const NOTIFY_UNFOCUSED_DELAY_MS = 250;
  export const NOTIFY_LOCK_TTL_MS = 15 * 60_000;
  export const NOTIFY_SWEEP_MS = 60_000;
  export type LockDeps = { dir: string; sleep: (ms: number) => Promise<void>; now: () => number;
                           fs: Pick<typeof import("node:fs/promises"), "mkdir" | "open" | "readdir" | "stat" | "unlink"> };
  export class NotifyLock {
    constructor(d: LockDeps)
    claim(id: string, focused: boolean): Promise<boolean>
  }
  ```
  - **`parseNotify`** returns undefined for:
    - non-JSON, or a non-object;
    - `id` not a non-empty string of at most 200 chars;
    - `title` or `body` not strings;
    - `channel` failing `ID_RE.chn`;
    - `session` not a string.

    A `session` string that fails `SESSION_ID_RE` (a hostile value, or a grandfathered non-`ses_` actor id) becomes `""`, so the click opens the web app instead of the event being dropped.

    It trims and cuts `title` and `body` to `NOTIFY_TITLE_MAX` and `NOTIFY_BODY_MAX` runes, the last an ellipsis (as `push.cut`), and replaces control characters but `\n` with a space. A `kind` other than `"needs"` is `"messages"`.
  - **`claim`:**
    - unfocused, it first sleeps `NOTIFY_UNFOCUSED_DELAY_MS`, so a focused window wins;
    - then it runs `mkdir(dir, {recursive: true})` and `open(join(dir, sha256hex(id).slice(0, 32) + ".lock"), "wx")`, closing the handle → true. `EEXIST` → false;
    - any other error → true (rather two notifications than none), logged by the caller;
    - at most once per `NOTIFY_SWEEP_MS` it also unlinks `.lock` files whose `mtimeMs` is older than `NOTIFY_LOCK_TTL_MS`, ignoring errors.

- [ ] **Step 1: Write the failing tests:**
  - `notifyevent.test.ts`:
    - `parses the server's event`: the Task 1 JSON plus a `ses_…` session → the same fields;
    - `refuses bad input` (Review Focus 3), a table:
      - `"{"` and `"[]"`;
      - `title: 5`;
      - `channel: "javascript:alert(1)"`;
      - `id: ""`, and an `id` of 201 chars;
    - `cuts a 5,000-rune body to 200 with an ellipsis and drops control characters`;
    - `unknown kind is messages`;
    - `a session failing SESSION_ID_RE becomes "" and the event is kept`: `"../x"` and a grandfathered `"alice-bot"` both give `session: ""`, so `notifyTarget` is the web app;
    - `the gate admits one messages event per channel per 5 s and every needs event` (Review Focus 4): 20 events at 500 ms steps on one channel, with a `needs` at step 7 → messages admitted at steps 0 and 10, and the needs at step 7; another channel is independent;
    - `notifyTarget and targetLink`: session → `vscode-insiders://aethons.jam/session/ses_…` with scheme `vscode-insiders`; a chat → `https://jam.example/app/user/c/chn_…`.
  - `notifylock.test.ts` (a real temp dir, fake `sleep` and `now`):
    - `the first claim of an id wins and the second loses` (two `NotifyLock`s on one dir: Review Focus 1);
    - `an unfocused claim waits 250 ms first`, so a focused claim made during the wait wins;
    - `a different id is a different lock`;
    - `the filename is a hash, so an id like ../../x stays inside the dir`;
    - `an unexpected fs error claims`;
    - `old locks are swept at most once a minute`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/notifyevent.test.ts test/notifylock.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): notify event parsing, per-channel gate and the one-window lock`.

### Task 5: Extension — OS notifications (terminal-notifier, osascript, notify-send)

**Files:**
- Create: `web/vscode/src/notify/os.ts`, `web/vscode/test/osnotify.test.ts`.
- Modify:
  - `web/vscode/package.json`: `devDependencies` adds `"node-notifier": "12.0.0"` (regenerate `package-lock.json` with `npm install --save-dev --save-exact node-notifier@12.0.0`);
  - `web/vscode/.vscodeignore`: `!dist/vendor/**`;
  - `scripts/vscode.sh`: after the fonts, `cp -R node_modules/node-notifier/vendor/mac.noindex dist/vendor/` and `cp node_modules/node-notifier/vendor/terminal-notifier-LICENSE dist/vendor/`. It fails unless `dist/vendor/mac.noindex/terminal-notifier.app/Contents/MacOS/terminal-notifier` and `…/Contents/_CodeSignature/CodeResources` exist;
  - `web/vscode/test/manifest.test.ts`: the `.vscodeignore` case adds `dist/vendor/**`.

**Interfaces:**
- Produces:
  ```ts
  export const NOTIFY_WAIT_MS = 10 * 60_000;
  export type OsNote = { title: string; body: string; link: string; group: string; onClick: () => void };
  export type Child = { stdout: string };
  export type OsDeps = {
    platform: NodeJS.Platform; extensionPath: string;
    execFile: (file: string, args: string[], o: { timeout: number; signal?: AbortSignal }) => Promise<Child>;  // rejects on non-zero exit/ENOENT
    chmod: (p: string, mode: number) => Promise<void>;
    log: (m: string) => void };                                                       // the Jam output channel
  export class OsNotifier {
    constructor(d: OsDeps)
    show(n: OsNote): Promise<void>    // never throws
    dispose(): void                   // aborts waiting children
  }
  export function terminalNotifierArgs(n: OsNote): string[]
  export function osascriptArgs(n: OsNote): string[]
  export function notifySendArgs(n: OsNote, actions: boolean, icon: string): string[]
  export function escapeMarkup(s: string): string     // & < > → &amp; &lt; &gt;
  ```
  - **`darwin`:**
    - before the first use it runs `chmod(bin, 0o755)`, ignoring errors (vsce may drop the exec bit);
    - `execFile(bin, terminalNotifierArgs(n), {timeout: 10_000})`;
    - the args are `["-title", "\u200b" + t, "-message", "\u200b" + b, "-open", link, "-group", group]`. The U+200B is unconditional: terminal-notifier reads options through NSUserDefaults' argument domain, which parses a value starting with `-`, `[`, `(`, `{`, `<` or `"` as an option or a property list, and a message like "(edited) …" would otherwise go missing;
    - `group` is the channel id, so a newer notification for a conversation replaces the older one, as Web Push's `Topic` does;
    - a **spawn failure** (the error's `code` is `ENOENT`, `EACCES` or `EPERM`, or the child died by a signal that is not our own timeout kill (`killed: true`), e.g. Gatekeeper's SIGKILL) logs once and switches, for the rest of the session, to `osascript`. **Any other non-zero exit** sends that one note through `osascript` and keeps terminal-notifier for the next. `osascript`'s args are `["-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", "--", title, body]`, which give no click target (documented);
    - the click opens `link` itself through LaunchServices, so `onClick` isn't used.
  - **`linux`:**
    - the first `show` probes `execFile("notify-send", ["--help"])`. Not found → logged once, and OS notifications stay off for the session. Help text containing `--action` → actions;
    - with actions: `["--app-name=Jam", "--icon=<ext>/media/jam.svg", "--action=default=Open", "--wait", "--", title, escapeMarkup(body)]`, with timeout `NOTIFY_WAIT_MS` and an `AbortSignal`. Stdout trimmed `=== "default"` calls `onClick()`;
    - **at most one waiting child per `group`** (channel): a new note for a group aborts the previous one's child first. `dispose` aborts them all;
    - a timeout (`NOTIFY_WAIT_MS`) or an abort (a newer note, `dispose`) is a close without a click: no log, no `onClick`;
    - without actions, the same without `--action` and `--wait`, and a 10 s timeout;
    - the title isn't escaped (it is never markup).
  - **Other platforms:** `show` resolves at once.
  - Every failure is logged once per window, keyed by notifier: Review Focus 5.

- [ ] **Step 1: Write the failing tests** (a fake `execFile` recording calls):
  - `terminalNotifierArgs passes values as separate argv, each title and message after a U+200B` (Review Focus 3): bodies `-help`, `[x]`, `(edited) x`, `{a}` and `plain` all become `"\u200b" + body`; `$(rm -rf ~)` is passed as one item after the U+200B.
  - `darwin runs the vendored binary from the extension path, after chmod`.
  - `darwin: a spawn failure (EACCES) switches to osascript for the session, and logs once`: two `show`s give one log line, and the second goes straight to `osascript`, whose title and body come after `--`.
  - `darwin: a non-zero exit falls back for that note only`: exit 1, then the next `show` tries terminal-notifier again.
  - `linux with --action waits and calls onClick on "default"`.
  - `linux closes without a click: no onClick`.
  - `linux timeout or abort is a quiet close`: no log line, no `onClick`.
  - `linux keeps one waiting child per group`: a second note for the same channel aborts the first child; a note for another channel doesn't.
  - `linux escapes markup in the body only`: `<b>&</b>` → `&lt;b&gt;&amp;&lt;/b&gt;`.
  - `linux without --action notifies without waiting`.
  - `linux without notify-send logs once and never retries the probe`.
  - `dispose aborts a waiting notify-send`.
  - `win32 does nothing`.
  - `show never throws`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/osnotify.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:**
  - `just vscode` → PASS;
  - `unzip -l web/vscode/dist/jam.vsix | grep -c 'dist/vendor/mac.noindex/terminal-notifier.app'` → at least 4;
  - `unzip -l … | grep node_modules` → nothing.
- [ ] **Step 5: Commit** `feat(vscode): OS notifications through terminal-notifier, osascript or notify-send`.

  The vendored app ships whole, including `Contents/Resources/Terminal.icns` (Apple's Terminal icon), as node-notifier does: the bundle's sealed resources need it, and the .vsix is private.

### Task 6: Extension — the notifier and its wiring

**Files:**
- Create: `web/vscode/src/notify/notifier.ts`, `web/vscode/test/notifier.test.ts`.
- Modify:
  - `web/vscode/src/extension.ts`;
  - `web/vscode/package.json`: `activationEvents` adds `"onStartupFinished"`;
  - `web/vscode/test/vscode.ts`: `fakeContext()` gains `globalStorageUri` (a temp dir) and `extensionPath`, and `window.createOutputChannel` is mocked;
  - `web/vscode/test/extension.test.ts`, `web/vscode/test/manifest.test.ts`. Existing `extension.test.ts` cases may change **only their fetch expectations** (the always-on `/app/user/events` fetch at activate, and from Task 7 the initial rail fetch), never the behaviour they assert. The "unedited" rule in the Global Constraints covers the Go tests it names.

**Interfaces:**
- Consumes: Tasks 4–5; `LiveEvent`/`LiveStream.subscribe`; `openSession`/`OpenDeps`; `guard`; `vscode.env.uriScheme`.
- Produces:
  ```ts
  export type NotifierDeps = {
    gate: Pick<ChannelGate, "admit">; lock: Pick<NotifyLock, "claim">; os: Pick<OsNotifier, "show">;
    focused: () => boolean; jamUrl: () => string | Error; scheme: string; now: () => number;
    toast: (kind: NotifyKind, message: string, action: "Open") => Thenable<string | undefined>;
    openSession: (id: string) => unknown; openExternal: (url: string) => unknown;
    log: (m: string) => void };
  export class Notifier {
    constructor(d: NotifierDeps)
    handle(e: LiveEvent): Promise<void>   // ignores everything but event "notify"
  }
  ```
  - **`handle`:**
    - it runs `parseNotify`, and a bad event is logged (length only, never the data) and dropped;
    - then `gate.admit`, then `jamUrl()`; an Error drops the event;
    - then `lock.claim(id, focused())`, and a lost claim returns;
    - it then shows `toast` and, when `!focused()` (read after the claim), `os.show({title, body, link: targetLink(t, scheme), group: channel, onClick: () => open(t)})`, without waiting for the toast;
    - a toast answer of `"Open"`, and `onClick`, both call `open(t)`: `{session}` → `openSession(id)`, and `{web}` → `openExternal(url)`.
  - **`extension.ts`:**
    - it builds the `Jam` output channel, `OsNotifier` (`process.platform`, `ctx.extensionPath`, a promisified `execFile` with `{timeout, signal}`, `fs.chmod`), `NotifyLock` (`dir: Uri.joinPath(ctx.globalStorageUri, "notify").fsPath`) and `ChannelGate`;
    - `focused: () => vscode.window.state.focused`;
    - `toast`: `needs` → `showWarningMessage`, else `showInformationMessage`;
    - `openSession: (id) => guard(() => openSession(openDeps, id))()`;
    - **one permanent subscriber:** `const offLive = live.subscribe((e) => { void notifier.handle(e); needsYou?.onLive(e); })`. Task 7 adds `needsYou`; here the second call is absent. `offLive` and `os.dispose()` are pushed to `ctx.subscriptions`, and `deactivate` keeps calling `reporter.dispose()`. With the stream now always held while signed in, sign-in and sign-out still go through `live.restart()` (unchanged);
    - **`jam.url` changes:** `workspace.onDidChangeConfiguration` with `affectsConfiguration("jam.url")` calls `live.restart()` (and, from Task 7, `needsYou.poke()`), so the permanent stream follows the new Jam.

- [ ] **Step 1: Write the failing tests:**
  - notifier (fakes for every dep):
    - `a notify event shows a toast and, unfocused, an OS notification with the session link`: the toast is `"Alice · build-bot: done?"` with `"Open"`; the OS note's link is `vscode://aethons.jam/session/ses_…` and its group is the channel;
    - `focused: a toast and no OS notification` (Review Focus 1);
    - `a lost claim shows nothing`;
    - `needs uses the warning toast`;
    - `Open on a session opens its code window; on a chat opens the web app`;
    - `an OS click (onClick) opens the same target`;
    - `other events and malformed data do nothing, and the data isn't logged`;
    - `the gate runs before the lock`: a gated event never calls `claim`.
  - extension (through `activate`, with the mock fetch serving `/app/user/events`):
    - `activate holds the events stream with no pane open`: one fetch of `/app/user/events` after `activate` with `jam.url` and a stored session, and no view resolved;
    - `a notify event on the stream shows a toast; Open calls openSession`: it ends in `vscode.openFolder` for `jam-ses_…`, through the existing fakes;
    - `two activations on one global storage dir show one toast for one event` (Review Focus 1: two `fakeContext`s sharing `globalStorageUri`, each with its own mock stream);
    - `signed out: no stream, no toast`;
    - `changing jam.url restarts the events stream`: after the configuration event, a new `/app/user/events` fetch goes to the new URL.
  - manifest: `activationEvents include onStartupFinished`.
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/notifier.test.ts test/extension.test.ts test/manifest.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd web/vscode && npm run check` → PASS, and `test/hub.test.ts` passes unchanged (`notify` still isn't forwarded to panes).
- [ ] **Step 5: Commit** `feat(vscode): VS Code and OS notifications for what the phone buzzes for`.

### Task 7: Extension — the Needs You view and the activity-bar badge

**Files:**
- Create: `web/vscode/src/needsyou.ts`, `web/vscode/test/needsyou.test.ts`.
- Modify:
  - `web/vscode/package.json`:
    - `views.jam` appends `{ "id": "jam.needsYou", "name": "Needs You", "visibility": "collapsed" }`, a tree view after Previews;
    - `viewsWelcome` gets two entries for `jam.needsYou`, with the Constraints' copy, `when` `!jam.signedIn` and `jam.signedIn`;
    - the command `jam.needsYou.open` ("Open", hidden from `commandPalette`);
  - `web/vscode/src/extension.ts`;
  - `web/vscode/test/vscode.ts`: `window.createTreeView` returns a fake with a writable `badge`; also `TreeItem`, `ThemeIcon`, `TreeItemCollapsibleState` and `EventEmitter`;
  - `web/vscode/test/extension.test.ts`: the count case becomes "five view providers, one tree view and sixteen commands";
  - `web/vscode/test/manifest.test.ts`.

**Interfaces:**
- Consumes: `JamApi.rail()`/`conversation()`, `RailResponse`, `RailRow` (`open.ts`), `webUrl`, `openSession`, `LiveEvent`, `SignedOutError`, and `panesSignedOut`.
- Produces:
  ```ts
  export const NEEDS_YOU_DEBOUNCE_MS = 3000;
  export function needsYouRows(r: RailResponse): RailRow[]          // the rows of the group with bucket "needs-you" (the web app's Needs you group), first occurrence of each id
  export function badgeFor(n: number): { value: number; tooltip: string } | undefined   // 0 → undefined; the Constraints' tooltip copy
  export type NeedsYouDeps = {
    session: () => Promise<{ url: string; token: string } | undefined>;  // extension.ts's current()
    api: (s: { url: string; token: string }) => Pick<JamApi, "rail">;
    setBadge: (b: { value: number; tooltip: string } | undefined) => void;
    setContext: (key: "jam.signedIn", v: boolean) => void;
    onSignedOut: (token: string) => void;
    setTimeout: typeof setTimeout; clearTimeout: typeof clearTimeout };
  export class NeedsYou implements vscode.TreeDataProvider<RailRow> {
    constructor(d: NeedsYouDeps)
    readonly onDidChangeTreeData: vscode.Event<void>
    onLive(e: LiveEvent): void        // open, changed, presence, notify → poke()
    poke(): void                      // refresh after NEEDS_YOU_DEBOUNCE_MS, coalescing
    refresh(): Promise<void>
    getChildren(): RailRow[]
    getTreeItem(r: RailRow): vscode.TreeItem   // label, description project, tooltip mission, ThemeIcon "bell", command jam.needsYou.open [r]
  }
  ```
  - **`refresh`:**
    - not signed in: `setContext(false)`, no badge, no rows, no fetch;
    - else `rail()` sets the rows and `setBadge(badgeFor(rows.length))`, with `setContext(true)`;
    - a `SignedOutError` calls `onSignedOut(token)`, then behaves as signed out;
    - any other error keeps the last rows and badge;
    - a refresh still running when the next is due runs once more after it, not in parallel.
  - **`extension.ts`:**
    - it calls `vscode.window.createTreeView("jam.needsYou", { treeDataProvider: needsYou })`, and `setBadge` writes `view.badge`;
    - the live subscriber from Task 6 calls `needsYou.onLive(e)`;
    - `auth.onChange` calls `needsYou.poke()`;
    - `jam.needsYou.open(row)`, through `guard`, reuses `sessionOfPick(api, { label: row.label, row })` (`open.ts`). With a session, it calls `openSession(openDeps, session)`, else `openExternal(webUrl(url, row.id))`;
    - the Task 6 `jam.url` change handler also calls `needsYou.poke()`;
    - an initial `poke()` runs at activation.

- [ ] **Step 1: Write the failing tests:**
  - needsyou:
    - `needsYouRows takes the needs-you group's rows once each`: rows in other groups (even with `needsYou: true`) don't count, and a repeated id counts once;
    - `badgeFor`: 0 → undefined, 1 → "1 conversation needs you", 3 → "3 conversations need you";
    - `live events are debounced into one rail fetch` (fake timers: five `changed` within 3 s → one fetch, 3 s after the first);
    - `signed out: no fetch, no badge, signedIn false`;
    - `a 401 signs out with the token and clears the badge`;
    - `a 500 keeps the last count`;
    - `a refresh during a refresh runs once more after it`;
    - `the tree item opens through jam.needsYou.open with the row`.
  - extension:
    - `the badge shows the needs-you count after activation and follows a changed event`: the mock rail first answers 2 rows, then 1;
    - `jam.needsYou.open on a session row opens its code window; on a chat row opens the web app`.
  - manifest:
    - `jam.needsYou is a collapsed tree view in the jam container, with two welcome entries`;
    - every contributed command is registered (the existing case covers `jam.needsYou.open`).
- [ ] **Step 2:** `cd web/vscode && npx vitest run test/needsyou.test.ts test/extension.test.ts test/manifest.test.ts` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `just vscode` → PASS.
- [ ] **Step 5: Commit** `feat(vscode): Needs You view and the activity-bar badge`.

### Task 8: Docs and the manual checklist

**Files:**
- Modify:
  - `docs/usage/jam/vscode.md` (bump `updated`; `summary`, `read_when` and `owns` add notifications and the badge). Replace the "Notifications and the badge are a later slice" bullet with a **Notifications and the badge** section, which owns:
    - what notifies is the phone's rule (link `push.md#what-notifies`), not restated;
    - a VS Code notification in one window, **usually** the focused one: the focused window gets a 250 ms head start, so a lagging stream can let an unfocused window win, and then it also shows an OS banner while VS Code is focused. An OS notification shows only while the winning window isn't focused. Several windows agree through a lock file in the extension's global storage, so a second VS Code profile, and VS Code Insiders, each notify separately;
    - at most one message notification per conversation per 5 s, while asks, call-ins and escalations always notify;
    - **Open** and the OS notification open the session's code window (`vscode://aethons.jam/session/<id>`), or the web app for a chat;
    - the OS notifiers:
      - macOS uses the bundled `terminal-notifier` 3.0.0, ad-hoc signed and not notarized, shipped whole with Apple's `Terminal.icns` as node-notifier does (the .vsix is private). It is listed as *terminal-notifier* in System Settings → Notifications, where it is allowed or muted. If it can't run it falls back to `osascript`, whose banners can't open a session;
      - Linux uses `notify-send`, with a click only when it supports `--action` (libnotify 0.7.10+);
      - other platforms get VS Code notifications only;
    - nothing is queued while VS Code is closed (the phone still gets Web Push), and a user with both gets both;
    - the **Needs You** view (Jam container, collapsed) lists what needs you; its count is the Jam icon's badge;
    - the extension now starts with VS Code (`onStartupFinished`), in every window;
    - edit the existing **Live updates** bullet (it owns the stream fact; add no new bullet): the one stream per window is now held from startup while signed in, with no pane open, and restarts when `jam.url` changes;
    - edit the existing **Push focus** bullet: suppression now applies to VS Code notifications too, and works without Web Push configured;
    - **Troubleshooting** gains: no OS banners on macOS → check System Settings → Notifications → *terminal-notifier*; if the Jam output channel says the notifier was blocked (Gatekeeper on a quarantined copy), run `xattr -dr com.apple.quarantine` on the extension's `dist/vendor` or rely on the `osascript` fallback.
  - `docs/usage/jam/push.md`:
    - *Privacy and previews* gains one sentence: `preview` also governs the VS Code and OS notifications (it can only be set inside `runtime.push`), and whether an OS notification's body shows on the lock screen follows the OS's own settings;
    - *What notifies* gains one sentence: the same set reaches the Jam VS Code extension (link `vscode.md#notifications-and-the-badge`);
    - the focus paragraph says the suppression also holds for VS Code, and that the focus route works without `runtime.push`.
  - `docs/usage/jam/push-delivery.md`, *How Jam sends*: the **notify relay**. It is a second egress-only relay service, `notify`, over the same rule. It publishes to the recipient's open `/app/user/events` streams, drops after 10 minutes, skips users with no open stream, has no retries (nothing is queued) and no `runtime.push` gate, and keeps its marker in memory, seeded at the log tail on every start (no state dir, nothing written). *Routes*: `POST /app/user/push/focus` is mounted with Web Push or the notify stream.
  - `docs/usage/jam/intercom-ui.md`, *Unread and refresh*: the stream also carries `event: notify`, the one event with content: the viewer's own notification, as a push would show it. The web app ignores it.
  - `docs/usage/jam/vscode-checklist.md`:
    - **Build** gains the vendored `terminal-notifier.app` copy;
    - **Verify first** gains:
      - "Installed from the .vsix on macOS, `dist/vendor/mac.noindex/terminal-notifier.app` runs: it keeps its exec bit (or the chmod restores it), Gatekeeper doesn't block it, and the first notification asks for permission. Clicking a notification opens `vscode://aethons.jam/session/<id>`";
      - "A `TreeView.badge` on the collapsed `jam.needsYou` view shows on the Jam activity-bar icon while the Jam container is closed";
      - "`notify-send --action … --wait` on the target Linux desktops (GNOME, KDE) prints `default` on a click";
      - "`osascript -e 'on run argv' … -- <title> <body>` passes `--` through or not: if `item 1 of argv` comes out as `--`, drop the `--` from `osascriptArgs`";
      - "terminal-notifier 3.0 (UNUserNotificationCenter; its first run waits on the permission prompt, and output saying "declined" is a quiet stop, never an osascript fallback) still displays on the current macOS: a silent non-display exits 0, so no fallback triggers";
    - a **Notifications** step list:
      - an agent message in another session gives one toast (in the focused window) and no OS notification, and with VS Code unfocused, one OS notification;
      - with three code windows open, exactly one toast;
      - clicking an OS notification for session X opens or focuses X's window;
      - an ask shows a warning toast and bumps the badge, and answering it lowers the badge;
      - reading the conversation in the Conversation view, or in a focused web-app tab, gives no notification, on a Jam with and without `runtime.push`;
      - a burst of messages gives at most one notification per 5 s;
      - signing out stops notifications and clears the badge, and signing in restores them;
      - the phone's Web Push still arrives;
      - Linux without `notify-send`: toasts only, one log line in the Jam output channel.
  - The spec's status line: "slice 5 (notifications) built". (The *Notifications* paragraph was already amended in this plan's pre-flight commit.)
- [ ] **Step 1:** Write the docs. Each fact goes in the one doc named above; the others link to it.
- [ ] **Step 2:** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` → no new findings beyond the plan file's own.
- [ ] **Step 3:** `just test && just app-check && just app && just vscode && just lint` → PASS.
- [ ] **Step 4: Commit** `docs: VS Code notifications, the Needs You badge and the notify relay`.

---

## Decisions the spec leaves open (my calls)

- **A relay service, not a hook in the push surface.** `notify` is its own `relay.Surface` over the same `directory`, so it has its own marker and runs with or without VAPID keys. Folding it into `push.Surface` would tie VS Code notifications to Web Push being configured.
- **Recipients are listed whether or not they have a stream open; `Deliver` skips the unsubscribed** (pre-flight F1). `Surfaces` must be deterministic for a squawk, and skipping before `pushPayload` keeps a Jam where nobody uses VS Code from re-running the rule and `UnreadTotal` per message. Nothing is queued for a closed VS Code, since the phone covers that.
- **The notify relay's marker lives in memory** (pre-flight F2). Nothing is queued and nothing appended while Jam is down, so persistence buys nothing. A file marker would rewrite `relay-markers.json` every 2 s and make every `admin-listen` Jam need a state dir.
- **The focus route no longer needs Web Push.** Without this, a Jam with no VAPID keys couldn't suppress VS Code notifications for the conversation being read. The bootstrap's `pushFocus` follows, so the web app reports focus there too.
- **`homeSession` moves to `jam.HomeSession`.** The notification needs the session the web app's header would show; moving it avoids a second copy.
- **No `node-notifier` at runtime; only its ad-hoc signed (not notarized) `terminal-notifier.app`.** `node-notifier@12` is ESM and finds its vendor binaries through `import.meta.dirname`, which esbuild's CJS bundle breaks. The vsix ships without `node_modules`. Its Linux path has no click support we need beyond `notify-send` itself. A 100-line argv-only spawner is smaller than shipping it.
- **`osascript` is the macOS fallback**, even though its banners can't open a session: a visible notification beats none, and it is documented. Only a spawn failure switches for the session; any other failure falls back for that note.
- **Every terminal-notifier title and message starts with U+200B** (pre-flight F3): NSUserDefaults would otherwise parse values starting with `-`, `[`, `(`, `{`, `<` or `"`. It is invisible and leaves no escape characters to render.
- **Linux keeps one waiting `notify-send` per conversation**, and a timeout or abort is a quiet close (pre-flight F13, F16).
- **A session id failing `SESSION_ID_RE` becomes `""`**, not a dropped event (pre-flight F6): grandfathered actor ids exist, and the web app is a fine target.
- **The focused window wins the lock** (a 250 ms head start). The notification then shows where the user is looking.
- **OS notifications only while VS Code isn't focused** (accepted as a builder call; the spec's *Notifications* paragraph is amended to match). The spec wants OS notifications because VS Code's own aren't visible outside the window; a focused window already shows the toast, and a second banner would be noise. Because the focused window only *usually* wins the lock (250 ms head start), this is documented as "usually".
- **A per-channel 5 s gate for messages, never for needs.** The phone collapses per conversation through `Topic`. A toast can't be replaced, so the gate is the equivalent. It runs on events seen, so every window drops the same ones.
- **Notifications run in every VS Code window while signed in** (`onStartupFinished`), not only in `jam-` windows. Otherwise closing the last code window would silence Jam. The cost is one events stream per window, plus the Needs You rail refresh.
- **`NEEDS_YOU_DEBOUNCE_MS = 3000`** (raised from 1 s at pre-flight): with every window now holding a stream, steady log activity would otherwise make each window fetch the rail about once a second. 3 s bounds that to one fetch per window per 3 s, and a badge that lags by 3 s is still timely; `notify` events, which matter most, arrive on the same debounce.
- **The badge lives on a small Needs You tree view.** A `WebviewView`'s badge exists only after the view is resolved, so a closed Jam container would show no count. A `TreeView` badge is set from activation and shows on the container icon. The view also lists the conversations and opens them.
- **"Needs you" is the rows of the rail's `needs-you` group** (`GET /app/user/api/rail`, deduped by channel), literally the web app's Needs you group (pre-flight F14). Open reuses `sessionOfPick`. It refreshes, debounced, on `open`, `changed`, `presence` and `notify`.
- **Toast kind:** `needs` → a warning, `messages` → information, mirroring the web app's two sounds.
- **Lock files are named by `sha256(id)`**, so an id can never escape the directory. They are swept after 15 minutes.
- **No settings in this slice.** OS notifications are muted through the OS (*terminal-notifier* on macOS), and VS Code's own Do Not Disturb covers toasts.
- **`jam.url` changes restart the permanent stream** and refresh the badge (pre-flight F12): before this slice, panes re-resolved on their own; now the stream outlives every pane.

## Known limitations (out of scope)

- **Push focus is one channel per user, last write wins** (slice 3, `push.Focus`). Window A's blur `null` can land after window B's focus post and clear it for up to 30 s, so a conversation being read can still notify (now in VS Code as well as on the phone). A later fix should key focus by client.
