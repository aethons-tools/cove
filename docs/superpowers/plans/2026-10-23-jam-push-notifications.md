# Jam push notifications — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** People opt in per device and get Web Push notifications for new messages in their Jam conversations (Slack-style), delivered by a third relay on the existing relay engine.

**Architecture:** PR 1 adds the `runtime.push` config (VAPID key via the credential resolver), `at-jam push keygen`, a `push_subscriptions` store (Postgres + MemStore), the subscribe API under `/app/user/push/*` and a menu toggle — nothing is sent. PR 2 adds a pure recipient/payload package (`internal/jam/push`), a focus registry, a `relay.Surface` for service `"push"` that encrypts and sends with `webpush-go`, the directory branch and engine wiring in `cmd/at-jam`, and the service worker's push/click/badge handling.

**Tech Stack:** Go 1.26+, `github.com/SherClockHolmes/webpush-go` (new, MIT), Postgres migrations, `html/template`, service worker JS.

**Spec:** `docs/superpowers/specs/2026-10-23-jam-push-notifications-design.md`

## Global Constraints

- Config block `runtime.push` with `vapid-private-key-cred` (required, must name a `credentials:` entry), `subject` (required, `mailto:` or `https:`), `preview` (bool, default true). Omitted ⇒ push off: no menu item, no relay, `/app/user/push/*` → 404.
- The VAPID private key never reaches disk, argv or logs; resolve it with the credential resolver (as `runtime.discord`'s token and the SSH CA's `CACred` are).
- Table `push_subscriptions(endpoint text primary key, user_id text references users(id), p256dh text, auth text, user_agent text, created_at timestamptz, last_ok_at timestamptz)`, migration `internal/jam/migrations/0017_push_subscriptions.sql`. Upsert by endpoint. Max 20 per user (oldest evicted). Deleted on unsubscribe, 404/410, and `RemoveUser`.
- API (all under `/app/user/`, gated member-only, same-origin on writes): `GET /app/user/push/key` → `{"key":"<base64url public key>"}`; `POST /app/user/push/subscriptions` (body = `PushSubscription.toJSON()`, endpoint `https:` ≤ 2048 bytes, `keys.p256dh` decodes to 65 bytes, `keys.auth` to 16) → 204, else 400; `DELETE /app/user/push/subscriptions` `{"endpoint"}` → deletes only the caller's row → 204; `POST /app/user/push/focus` `{"channel": id|null}` → 204 (PR 2).
- Notify rule: user recipients only (never sessions/accounts), in the squawk's channel membership and able to see it, not the author, not focused on that channel; channel kind `chat` or `session`, OR an ask (`intercom.AskOf`, event `ask`) whose `Of` includes them, OR a call-in notice inviting them (`jam.IsCallInNotice`), OR an escalation notice targeting them (`jam.EscalationNoticeTargets`), in any channel kind. Ask amend/withdraw/answer never notify.
- Focus: in-memory user → (channel, expires now+60 s); page re-posts every 30 s while focused; `null` clears.
- Delivery: `Address = "push:" + hex(sha256(endpoint))[:16]`; TTL 86400; `Topic` = hex(sha256(channel id))[:32]; 2xx → success + `last_ok_at`; 404/410 → delete + success; other 4xx → log + success; 429/5xx/network → error (retried); squawk older than 10 min at delivery → success without sending; condition `push:relay` raised after 5 consecutive transient failures, cleared on success.
- Payload JSON ≤ 3 KB: `{"title","body","channel","id","unread"}`; title `"<sender label> · <conversation label>"`; body = markdown-stripped first 140 runes; ask → `"❓ "+question`; call-in → `"📣 "+body`; escalation → `"⚠️ "+body`; `preview:false` ⇒ body `"New message"`.
- Hermetic TDD; `just test`, `just lint`; docs updated in the same PR. Before go/just: `export GOPROXY=https://proxy.golang.org,direct`. Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never stage `.superpowers/`.

## Review Focus

- **Same device re-subscribes after switching users** → the endpoint moves to the new user; the old user stops getting pushes on it. (Task 2 conformance test.)
- **One dead endpoint among a user's devices** → 404 prunes it, the user's other devices still get pushes, and the relay's low-water mark keeps advancing. (Task 5 test.)
- **Relay down for an hour, then back** → no burst of stale buzzes; only squawks < 10 min old are pushed. (Task 5 test.)
- **A member removed from the project or a left chat member** → no further pushes to them even if their subscription still exists. (Task 4 test.)
- **A crashed tab that last reported focus** → pushes resume within 60 s. (Task 4 test with an injected clock.)

---

## PR 1 — subscriptions (branch `jam-push-1-subscriptions`, from main; the spec commit is on it)

### Task 1: `runtime.push` config and `at-jam push keygen`

**Files:**
- Modify: `cmd/at-jam/config.go` (runtime struct near `Discord *discordConfig` at ~:146; validation beside `validateDiscord` ~:810 / the SSH `CACred` check ~:346; demanded credentials), `cmd/at-jam/main.go` (subcommand table ~:114; call validation where `validateDiscord` is called ~:1843)
- Create: `cmd/at-jam/push.go`, `cmd/at-jam/push_test.go`
- Modify: `go.mod`/`go.sum` (add `github.com/SherClockHolmes/webpush-go`)
- Docs: `docs/usage/jam/serve.md` (config table row + a short "Push notifications" note linking to the doc that Task 3 writes)

**Interfaces — Produces:**

```go
type pushConfig struct {
	VAPIDPrivateKeyCred string `yaml:"vapid-private-key-cred"`
	Subject             string `yaml:"subject"`
	Preview             *bool  `yaml:"preview"` // nil ⇒ true
}
func (p *pushConfig) preview() bool
func (c *serveConfig) validatePush() error            // nil when c.Runtime.Push == nil
// vapidPublicKey derives the base64url (raw, uncompressed P-256 point, 65 bytes) public key
// from a base64url private key (32-byte scalar) — the webpush-go key format.
func vapidPublicKey(privateB64 string) (string, error)
// cmdPush implements `at-jam push keygen`: prints "private: <b64>\npublic: <b64>\n".
func cmdPush(args []string, stdout, stderr io.Writer) int
```

(Use the actual config type name the file uses for the serve config in place of `serveConfig`.)

- [ ] **Step 1: Failing tests** in `push_test.go`: (a) config with no `push` → `validatePush` nil; (b) missing cred, cred not under `credentials:`, empty subject, subject `ftp://x` → each an error naming the field; valid `mailto:` and `https:` subjects pass; (c) `preview` nil → true, explicit false → false; (d) the push cred appears in the demanded-credentials set the planner resolves (mirror the existing test for the discord/SSH cred); (e) `cmdPush([]string{"keygen"}, …)` exits 0, prints two lines, and `vapidPublicKey(private)` equals the printed public; the public decodes to 65 bytes starting `0x04`; (f) `cmdPush` with no/unknown subcommand → non-zero with usage on stderr.
- [ ] **Step 2: Run** `go test ./cmd/at-jam/ -run Push` — FAIL.
- [ ] **Step 3: Implement.** `go get github.com/SherClockHolmes/webpush-go@latest`. Keygen: `webpush.GenerateVAPIDKeys()` returns (private, public) base64url — print them. `vapidPublicKey`: decode the private scalar, build an `ecdh.P256()` (or `ecdsa`) key, return `base64.RawURLEncoding` of the uncompressed public point. Register `{Name: "push", Brief: "push notification helpers (keygen: print a fresh VAPID key pair)", Run: …}` in the subcommand table, matching the existing entries' `Run` signature.
- [ ] **Step 4: Run** focused tests, then `just test`, `just lint` — PASS.
- [ ] **Step 5: Docs + commit** `feat(at-jam): runtime.push config and push keygen`.

### Task 2: push subscription store

**Files:**
- Create: `internal/jam/migrations/0017_push_subscriptions.sql`, `internal/jam/push_subscriptions.go` (type, interface, MemStore methods), `internal/jam/storetest/push.go` (conformance)
- Modify: `internal/jam/store.go` (embed the interface in `Store`), Postgres side alongside `PutShareGrant` (`pgstore_registry.go`, write-through cache + load path like share grants), `RemoveUser` in both stores (`pgstore_registry.go:~42` registryTx closure; `memstore_registry.go:~30`), `internal/jam/storetest/conformance.go` (register the suite), `internal/jam/pgstore_testhelpers.go` (TRUNCATE list + in-memory reset), `internal/jam/pgstore_integration_test.go` (a reload test like `TestPostgresShareGrantsReload`)

**Interfaces — Produces:**

```go
type PushSubscription struct {
	Endpoint  string
	UserID    ident.ID
	P256dh    string
	Auth      string
	UserAgent string
	CreatedAt time.Time
	LastOKAt  time.Time
}
type PushSubscriptionStore interface {
	PutPushSubscription(s PushSubscription) error             // upsert by Endpoint; evicts the user's oldest beyond 20
	DeletePushSubscription(endpoint string) error              // no error when absent
	DeleteUserPushSubscription(user ident.ID, endpoint string) (bool, error) // only if owned by user
	PushSubscriptions(user ident.ID) []PushSubscription        // oldest first
	TouchPushSubscription(endpoint string, at time.Time) error // sets LastOKAt; no error when absent
}
const MaxPushSubscriptionsPerUser = 20
```

- [ ] **Step 1: Conformance tests** (`storetest/push.go`, run for MemStore in the existing unit test and Postgres in integration): put/list; upsert same endpoint with a different user moves it (old user's list no longer has it); 21st subscription evicts the oldest by `CreatedAt`; `DeleteUserPushSubscription` refuses another user's endpoint (false, row kept); `Touch` updates `LastOKAt`; `RemoveUser` deletes the user's rows; delete of an absent endpoint is nil.
- [ ] **Step 2: Run** `go test ./internal/jam/ -run Push` — FAIL.
- [ ] **Step 3: Implement** the migration (`CREATE TABLE push_subscriptions (...)`, `CREATE INDEX ON push_subscriptions(user_id)`), MemStore and Postgres methods following the share-grants pattern (mutex, tx, apply to cache, load on open).
- [ ] **Step 4: Run** unit tests; if `JAM_TEST_POSTGRES_DSN` is unavailable locally, say so in the report (CI's store-integration job runs it). `just test`, `just lint` — PASS.
- [ ] **Step 5: Commit** `feat(jam): push subscription store`.

### Task 3: subscribe API, menu toggle, wiring, docs

**Files:**
- Create: `internal/jam/userui/push.go`, `internal/jam/userui/push_test.go`, `internal/jam/appui/push.js` (embedded, served as `/app/static/push.js` via uiassets — or appui's own ungated route; pick the uiassets embed for consistency)
- Modify: `internal/jam/userui/handler.go` (routes, `WithPush` option), `internal/jam/appui/titlebar.go` + `titlebar.html` (menu item when `Shell.Push` is set and principal is a user), `internal/jam/appui/mux.go`/`titlebar.go` (`Shell.Push bool`), `cmd/at-jam/main.go` (resolve the key, set `Shell.Push`, pass `userui.WithPush`)
- Docs: create `docs/usage/jam/push.md` (enabling: keygen, credential, config, egress hosts; opting in per device; iOS Home Screen requirement; what notifies; privacy/preview) with frontmatter; add a row to `docs/usage/jam/INDEX.md`; point `ui.md` "Installing the app" at it

**Interfaces — Consumes:** Task 1 `vapidPublicKey`, Task 2 `PushSubscriptionStore`. **Produces:**

```go
// userui
func WithPush(publicKey string) Option // "" = off (routes 404)
// appui
type Shell struct { /* existing */ Push bool }
```

- [ ] **Step 1: Failing tests** (`userui/push_test.go`, httptest with a member principal via `appui.WithPrincipal`/`jam.WithParticipant` as other userui tests do): key route returns the configured key; subscribe stores `{endpoint, user, keys, user_agent=request UA}` → 204; invalid endpoint (`http:`, > 2048 bytes, not a URL), bad key lengths, malformed JSON → 400; delete removes own row → 204 and leaves another user's row; without `WithPush` all three → 404. In appui: title bar for a user principal with `Shell.Push` renders the Notifications item (`id="push-toggle"`), without it or for an admin-only principal it doesn't; the page includes `/app/static/push.js` only when shown.
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement.** Handlers read the user with `jam.ParticipantFrom(r)`; cap body at 8 KB. `push.js`: on load, find `#push-toggle`; if `!('serviceWorker' in navigator && 'PushManager' in window)`: when on iOS and not `navigator.standalone`/`display-mode: standalone` show "Add Jam to your Home Screen to get notifications", else hide the item. Otherwise reflect state from `registration.pushManager.getSubscription()` and `Notification.permission` (`denied` ⇒ "Blocked in browser settings"); click On ⇒ `Notification.requestPermission()` → fetch key → `subscribe({userVisibleOnly:true, applicationServerKey: urlBase64ToUint8Array(key)})` → POST JSON; click Off ⇒ `unsubscribe()` → DELETE. Send `Content-Type: application/json` and rely on same-origin cookies.
- [ ] **Step 4: Browser test** (`userui/browser_test.go`, headless Chrome): with push configured and a stubbed `PushManager.prototype.subscribe` returning a fake subscription object, clicking the toggle POSTs to `/app/user/push/subscriptions` (assert the server received it). Then `just test`, `just lint`, `go test -tags browser ./internal/jam/userui/ -run Browser`.
- [ ] **Step 5: Docs + commit** `feat(jam): opt in to push notifications per device`.

**PR 1 wrap-up:** whole-PR review, push, PR, merge when green (user has authorised merging this series).

---

## PR 2 — sending (branch `jam-push-2-sending`, from main after PR 1)

### Task 4: recipients, payload, focus, unread total (pure)

**Files:**
- Create: `internal/jam/push/push.go` (package doc), `internal/jam/push/recipients.go`, `internal/jam/push/payload.go`, `internal/jam/push/focus.go`, tests for each
- Modify: `internal/jam/channel_views.go` (add `UnreadTotal`), its test

**Interfaces — Produces:**

```go
package push
// Reader is what recipient selection reads (satisfied by adapters over jam.Store + *jam.Intercom).
type Reader interface {
	Channel(id ident.ID) (jam.Channel, bool)
	Members(ch ident.ID) []ident.ID          // current channel members
	CanSee(user ident.ID, m intercom.Squawk) bool // live-audience visibility (project membership, still a member)
	PartyLabel(id ident.ID) string
	ChannelLabel(ch jam.Channel, forUser ident.ID) string // chat ⇒ other members' labels joined ", "
}
type Recipient struct{ User ident.ID; Reason string } // reason: "message"|"ask"|"callin"|"escalation"
func Recipients(r Reader, m intercom.Squawk, focused func(user, channel ident.ID) bool) []Recipient
type Payload struct {
	Title, Body, Channel, ID string
	Unread int
}
func Build(r Reader, m intercom.Squawk, rc Recipient, unread int, preview bool) Payload // JSON-marshals ≤ 3 KB
type Focus struct{ /* mutex, map, now func() time.Time */ }
func NewFocus(now func() time.Time) *Focus
func (f *Focus) Set(user, channel ident.ID)   // channel "" clears; expires now+60s
func (f *Focus) Focused(user, channel ident.ID) bool
// jam
func UnreadTotal(store Store, lg intercom.Store, user ident.ID) int
```

- [ ] **Step 1: Failing table tests** for `Recipients` with a fake Reader: chat message → other user members, not author, not sessions/accounts; session channel → users in it; room and ticket plain message → none; ask with `Of=[u1]` in a room → u1 only ("ask"); ask amend/withdraw/answer → none; call-in notice id for u2 → u2 ("callin") even in a ticket channel; escalation notice → its targets; a member `CanSee` false (left the project) → excluded; focused on that channel → excluded, focused on another channel → included. `Focus`: set → focused; after 61 s (injected clock) → not; set "" clears. `Build`: title/body formats per Global Constraints, markdown stripped (`**x**`, links, code fences), 140-rune cut on a multibyte string, ask/call-in/escalation prefixes, preview false → "New message", marshalled size ≤ 3072 for a 100 KB body. `UnreadTotal` equals the sum of the rail's per-channel unread for the same fixture.
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement** (call-in invitee: parse it from the `callin:<ch>:<invitee>:<nanos>` id — add `jam.CallInInvitee(id) (ident.ID, bool)` beside `IsCallInNotice` with a test). **Step 4: Run** — PASS; `just test`, `just lint`. **Step 5: Commit** `feat(jam): push recipients, payload and focus`.

### Task 5: the push relay surface and wiring

**Files:**
- Create: `internal/jam/push/surface.go`, `internal/jam/push/surface_test.go`
- Modify: `cmd/at-jam/relay_dir.go` (`Projects("push")` → nil; `Surfaces("push", m)` branch before the parts early-return; `Route`/`Post` never reached for push but must not panic), `cmd/at-jam/main.go` (build the surface + engine when `cfg.Runtime.Push != nil`; include push in the relay-state creation condition; seed the egress mark at the log tail on first enable as discord does ~:2533; add `"push"` to the cutover service list if appropriate), `cmd/at-jam/relay_dir_test.go`

**Interfaces — Consumes:** Task 2 store, Task 4. **Produces:**

```go
type Sender interface { // webpush-go behind an interface for tests
	Send(payload []byte, sub PushSubscription, opts SendOptions) (status int, err error)
}
type SendOptions struct{ TTL int; Topic, Subscriber, VAPIDPrivate, VAPIDPublic string }
type Surface struct {
	Store   jam.PushSubscriptionStore
	Sender  Sender
	Payload func(m intercom.Squawk, user ident.ID) (Payload, bool)
	Opts    SendOptions
	Now     func() time.Time
	Conds   *condition.Tracker // may be nil
}
func (s *Surface) Service() string // "push"
func (s *Surface) Deliver(ctx context.Context, d relay.Delivery, m intercom.Squawk) (string, error)
func (s *Surface) Poll(context.Context, string, string) ([]relay.Event, string, error) // nil, "", nil
func (s *Surface) Close() error
func AddressFor(endpoint string) string // "push:" + hex(sha256)[:16]
```

**Recipient carrier.** `Surfaces("push", m)` emits one `relay.Delivery` per (recipient, subscription): `Service "push"`, `Address = AddressFor(endpoint)`, and `BodyPrefix = string(userID)` — for the push service `BodyPrefix` carries the recipient user id (document this on the push branch; push never renders a body prefix). `Deliver` reads the user from `d.BodyPrefix`, finds the subscription with `PushSubscriptions(user)` filtered by `AddressFor`, and builds the payload with `Payload(m, user)`. The directory's push branch uses a `push.Reader` adapter over `jam.Store` + `*jam.Intercom` (add it in `cmd/at-jam`, e.g. `push_reader.go`, with a test).

- [ ] **Step 1: Failing tests.** Surface with a fake Sender: 201 → nil error and `Touch` called; 404 and 410 → subscription deleted, nil error; 400/403 → nil error, logged; 429/500/network error → error returned; squawk `At` 11 min before `Now` → nil error, Sender not called; 5 consecutive transient errors raise `push:relay`, the next success clears it; subscription vanished between Surfaces and Deliver → nil error. A real-crypto test: Sender backed by `webpush-go` against an `httptest` server with a test browser key pair — decrypt the body (use webpush-go's test helpers or implement RFC 8291 decryption in the test) and assert payload JSON; assert `TTL: 86400`, `Topic`, and `Authorization: vapid t=…, k=<public>`. Directory test: `Surfaces("push", m)` for a chat squawk with two recipients × two devices → 4 deliveries with distinct addresses; focused user omitted; `Projects("push")` nil. Engine test (existing relay fakes): one dead endpoint (404) and one healthy for the same squawk → mark advances, healthy delivered.
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement**; cmd wiring resolves the VAPID private key via `secret.Resolve` exactly as the discord token is, derives the public key (Task 1), builds the Focus registry once and shares it with userui (Task 6). **Step 4: Run** focused, `just test`, `just lint`. **Step 5: Commit** `feat(jam): push relay sends Web Push notifications`.

### Task 6: focus endpoint, service worker, badge, docs

**Files:**
- Modify: `internal/jam/userui/push.go` (+`POST /app/user/push/focus`, `WithPushFocus(*push.Focus)`), `internal/jam/userui/templates/inbox.html` + `layout.html` (focus reporting JS, `data-unread-total` on the rail fragment, badge update), `internal/jam/userui/userui.go` rail data (total), `internal/jam/appui/sw.js` (push + notificationclick), tests in userui/appui, `cmd/at-jam/main.go` (pass the shared Focus)
- Docs: `docs/usage/jam/push.md` (what notifies, focus suppression, badge, health condition, troubleshooting: permission blocked, iOS standalone only, egress hosts), `docs/usage/jam/intercom-ui.md` (one line linking push.md)

- [ ] **Step 1: Failing tests:** focus POST sets the registry for the caller (channel id must be a channel the user can see, else 400; `null` clears); without push → 404. Rail fragment carries `data-unread-total="N"`. `sw.js` contains `addEventListener('push'`, `addEventListener('notificationclick'`, `setAppBadge`, `tag:` and `/app/user/?c=`; it still has no `'fetch'` listener (existing test). Browser test: on a page with a conversation open and focused, a focus POST is observed; on `visibilitychange` to hidden (dispatch via CDP emulation or `document.dispatchEvent` with overridden `visibilityState`) a `null` POST follows.
- [ ] **Step 2: Run** — FAIL. **Step 3: Implement** `sw.js` handlers per spec §8 (replace an existing same-tag notification with "N new messages · latest: <body>", `renotify:true`, icon/badge from `/app/static/icons/`, `navigator.setAppBadge` guarded); page JS posts focus on `focus`, `visibilitychange`, conversation change, and every 30 s while `document.visibilityState==='visible' && document.hasFocus()`; after each rail swap read `data-unread-total` and call `setAppBadge`/`clearAppBadge` when supported. **Step 4: Run** focused, browser tests, `just test`, `just lint`. **Step 5: Docs + commit** `feat(jam): push notification display, focus suppression and badge`.

**PR 2 wrap-up:** whole-PR review, push, PR (body lists the operator steps: keygen, credential, `runtime.push`, egress hosts), merge when green. Manual check: desktop Chrome, Android, iOS Home Screen app.
