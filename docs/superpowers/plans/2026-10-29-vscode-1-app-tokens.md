# VS Code slice 1 (app tokens) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Jam mints **app tokens**: opaque, hashed, 30-day bearer credentials that a signed-in user grants to a client (VS Code) through a PKCE consent page. With one, the client:
- acts as that user's browser session on `/app/user/*` (except the Web Push subscription routes) and on `/me/studios/*`;
- can never use `/app/admin/*`.

The user and operators can list and revoke them. Revoking one ends its open event streams.

**Architecture:**
- **Store.** `jam.AppToken` rows (`app_tokens` table, MemStore and Postgres) are looked up by `HashToken(raw)`. The rules live in `internal/jam/app_tokens.go`: expiry, the throttled last-used touch, `ResolveAppToken`.
- **New package `internal/jam/apptoken`.** It holds three things:
  - the consent page and code exchange, mounted at `/app/auth/app-token` beside the ungated login and resolving the session itself with `appui.Sessions.Resolve`;
  - single-use in-memory PKCE codes;
  - `Live`, a registry of the contexts of in-flight bearer requests, keyed by token id. Revoking cancels them, which ends `/app/user/events` and the Thinking stream (both already return on `r.Context().Done()`), so `events.go` doesn't change.
- **appui.** `appui.Handler` gets a bearer branch for `/app/user/` that skips `RequireSameOrigin`. It refuses a request that carries both a bearer and a cookie, and `/app/admin/` refuses any bearer.
- **collab.** `collabhttp.AppTokenBearer` sits beside `ParticipantBearer`, and cmd chains the two by token shape.

**Tech Stack:** Go (net/http, pgx), html/template for the consent page, React 19 + Mantine 9 + TanStack Query, Vitest + MSW, tygo.

**Spec:** `docs/superpowers/specs/2026-10-28-vscode-code-window-design.md` ("Sign-in: a Jam-minted app token"; "Left to the builder": the slicing). This is slice 1 of 5: server and web-app work only, no VS Code code.

## Global Constraints

- **Secrets:** a raw app token, PKCE code or verifier is never logged, never put in argv or a URL Jam builds (except the one `code` in the redirect), never stored. Only `HashToken(raw)` is stored. Logs name a token by its `apt_…` id.
- **Responses:** the exchange response and the consent page send `Cache-Control: no-store`. The consent page also sends `X-Frame-Options: DENY` and `Content-Security-Policy: default-src 'none'; style-src 'self'; form-action 'self' vscode: vscode-insiders:; frame-ancestors 'none'`.
- **Redirects:** the redirect must start with `vscode://aethons.jam/` or `vscode-insiders://aethons.jam/`, compared as an exact string prefix. Anything else is `400`, and no redirect is ever made to it.
- **Lifetimes:**
  - tokens last 30 days (`AppTokenTTL`). A member token slides: on a touch, `expires = now + 30d`. A token with operator standing has a fixed expiry.
  - last-used is written at most once per `AppTokenTouchEvery = 5 * time.Minute` per token, so a busy client writes Postgres at most every 5 minutes.
  - codes live 60 s (`CodeTTL`), are single-use (a failed redeem also burns the code), and are bound to challenge, redirect, user, operator id and label.
- **Operator standing** is `Principal.Operator.ID` from a *verified access-token cookie* (`Sessions.Resolve`) at consent, never loopback trust or dev-identity. Tokens are minted only through `Sessions.Resolve` (never `NewGate`), and the routes aren't mounted without browser login.
- **Bearer scope:**
  - accepted on `/app/user/*` and `/me/studios/*`;
  - refused (`403`) on `POST`/`DELETE /app/user/push/subscriptions` and `GET /app/user/push/key`;
  - refused (`401`) everywhere under `/app/admin/`, loopback included;
  - a bearer together with a `jam_app_id`/`jam_app_at` cookie is `400`;
  - a bearer request never gains loopback admin.
- **Per-user cap:** `MaxAppTokensPerUser = 20`. Adding a 21st evicts the user's least recently used token (and `Live.Revoke`s it).
- **Tests are hermetic.** The Postgres conformance runs only under `integration`, as today.

## Review Focus

1. **A signed-in loopback viewer** (or `dev-identity`) approving consent gets a token with **no** operator standing unless their access-token cookie verifies. A loopback request with no session is sent to login, not trusted. Task 3 tests it.
2. **A bad link** (redirect `vscode://aethons.jam.evil/`, `vscode://aethons.jam` with no slash, `https://…`, missing or short `code_challenge`, method `plain`, missing `state`) is `400` before any login redirect, so nobody signs in for a link that can't work. Task 3 tests a table.
3. **A code used twice**, or with the wrong verifier, a different redirect or after 60 s, is `400 invalid_grant`, and a failed attempt burns the code. Task 3 tests it.
4. **Revoking while VS Code holds `/app/user/events` open** ends that stream at once. Removing the user ends it within the 5 s sweep, and the token stops working on its next request. Tasks 4 and 6 test it.
5. **A bearer on loopback** must not become the local admin. `/app/admin/` with a valid app token is `401` on loopback too. Task 4 tests it.

---

### Task 1: AppToken store

**Files:**
- Modify:
  - `internal/ident/ident.go`: `AppToken Kind = "apt"`, added to `kinds`.
  - `internal/jam/store.go`: embed `AppTokenStore`.
  - `internal/jam/memstate.go`: `appTokens map[ident.ID]AppToken`, `appTokenByHash map[string]ident.ID`.
  - User removal, in all three places push subscriptions are dropped:
    - `memstore_registry.go:30-43` and `pgstore_registry.go:43-67` (`RemoveUser`);
    - `config_marks.go:276-281` (`applyConfigWrite`) and `pgstore_config.go:46-50` (`writeConfigTx`). Each also drops the user's app tokens.
  - `pgstore_registry.go:476`: call `loadAppTokens`.
  - `pgstore_testhelpers.go:18`: add `app_tokens` to the TRUNCATE before `users`.
  - `storetest/conformance.go:25`: call `runAppTokenConformance`.
- Create:
  - `internal/jam/app_tokens.go` (type, interface, memState prepare/apply, MemStore methods);
  - `internal/jam/pgstore_app_tokens.go`;
  - `internal/jam/migrations/0023_app_tokens.sql`: `app_tokens(id text PK, user_id text NOT NULL REFERENCES users(id), token_hash text NOT NULL UNIQUE, label text NOT NULL, operator_id text NOT NULL DEFAULT '', created_at, last_used_at, expires_at timestamptz NOT NULL)`, plus an index on `user_id` and a header comment like 0017's;
  - `internal/jam/storetest/apptokens.go`.

**Interfaces:**
- Produces:
  ```go
  type AppToken struct {
      ID, UserID ident.ID
      Hash, Label, OperatorID string // OperatorID "" = member
      CreatedAt, LastUsedAt, ExpiresAt time.Time
  }
  type AppTokenStore interface {
      PutAppToken(t AppToken) (evicted []ident.ID, err error) // insert; live user required; duplicate id/hash refused; evicts least-recently-used beyond MaxAppTokensPerUser
      AppTokenByHash(hash string) (AppToken, bool)
      AppTokenByID(id ident.ID) (AppToken, bool)
      AppTokens(user ident.ID) []AppToken            // oldest CreatedAt first
      DeleteUserAppToken(user, id ident.ID) (bool, error) // only if user owns it
      TouchAppToken(id ident.ID, lastUsed, expires time.Time) error // no error when absent
      DeleteExpiredAppTokens(now time.Time) ([]ident.ID, error)
  }
  const MaxAppTokensPerUser = 20
  ```
  The memState split follows push: `prepareAppToken(t AppToken) ([]ident.ID, error)` validates the row and picks the evictions, then `applyPutAppToken(t, evicted)` writes them. `applyDropAppTokens(user)` serves the removal paths.

- [x] **Step 1: Write the failing conformance tests** in `storetest/apptokens.go`, each a `t.Run`:
  - `app_token_put_lookup_by_hash`: by hash and by id, with fields round-tripped (times `.UTC()`, truncated to µs for Postgres).
  - `app_token_unknown_or_removed_user_refused`.
  - `app_token_duplicate_hash_refused`.
  - `app_token_list_oldest_first_owner_only`.
  - `app_token_cap_evicts_least_recently_used`: 21 puts leave 20, and the returned `evicted` is exactly the id with the oldest `LastUsedAt`.
  - `app_token_delete_owner_scoped`: bob deleting alice's id is `false`, and the token stays.
  - `app_token_touch_updates_last_used_and_expiry`.
  - `app_token_delete_expired_returns_ids`.
  - `app_token_dropped_on_remove_user`.
  - `app_token_dropped_on_config_user_removed` (via `ApplyConfig` with `Status: StatusRemoved`).
- [x] **Step 2:** `go test ./internal/jam/ -run TestMemStoreConformance` → FAIL (undefined).
- [x] **Step 3:** Implement MemStore, then Postgres: write-through, then apply to the cache, like `pgstore_registry.go:660-737`.
- [x] **Step 4:** `go test ./internal/jam/... && go test -tags integration ./internal/jam/ -run TestPostgresStoreConformance` (skips without `JAM_TEST_POSTGRES_DSN`) → PASS.
- [x] **Step 5: Commit** `feat(jam): app token store`.

### Task 2: Token rules and resolution

**Files:**
- Modify: `internal/jam/app_tokens.go`, `internal/jam/participant.go`.
- Test: `internal/jam/app_tokens_test.go`, `internal/jam/participant_test.go`.

**Interfaces:**
- Consumes: Task 1, plus `MintToken`/`HashToken` (`identity.go:388,397`).
- Produces:
  ```go
  const AppTokenTTL = 30 * 24 * time.Hour
  const AppTokenTouchEvery = 5 * time.Minute
  const AppTokenLabelMax = 64
  var ErrAppTokenInvalid = errors.New("invalid or expired app token")
  func NewAppToken(user ident.ID, label, operatorID string, now time.Time) (t AppToken, raw string, err error)
  func (t AppToken) Expired(now time.Time) bool          // !now.Before(ExpiresAt)
  func (t AppToken) Operator() bool
  func CleanAppTokenLabel(s string) string              // trim, drop control runes, cap AppTokenLabelMax runes, "" → "VS Code"
  func ResolveAppToken(store Store, raw string, now time.Time, log *slog.Logger) (AppToken, User, error)
  func ParticipantByUser(store interface{ ParticipantStore; GetUser(ident.ID) (User, bool) }, id ident.ID) (Participant, bool)
  ```
  - `ResolveAppToken` fails with `ErrAppTokenInvalid` for an unknown hash, an expired token or a user who isn't live.
  - When `now - LastUsedAt ≥ AppTokenTouchEvery` it calls `TouchAppToken`, sliding member tokens only. A touch error is logged by token id and doesn't fail the request.
  - `ParticipantByUser` matches `ParticipantByIdentity`: a live user who is a member of at least one project. Issuer and Subject come from the user's first OIDC identity ("" when there is none; no handler reads them).

- [x] **Step 1: Write the failing tests:**
  - `TestNewAppTokenHashOnly`: `t.Hash == HashToken(raw)`, `raw` is never in `t`, `ExpiresAt == now+30d`, `ID.Kind() == ident.AppToken`.
  - `TestResolveAppTokenMemberSlides`: a use 6 min later moves `ExpiresAt` to use+30d.
  - `TestResolveAppTokenOperatorFixed`: `ExpiresAt` is unchanged, `LastUsedAt` moves.
  - `TestResolveAppTokenTouchThrottled`: uses at +1 min and +4 min issue no `TouchAppToken`. Use a counting store wrapper.
  - `TestResolveAppTokenExpired`.
  - `TestResolveAppTokenRemovedUser`.
  - `TestCleanAppTokenLabel`, a table that includes `"\x1b[31mevil"` and a label of 100 runes.
  - `TestParticipantByUser`: no projects → false, removed → false, member → `Projects` set.
- [x] **Step 2:** `go test ./internal/jam/ -run 'AppToken|ParticipantByUser'` → FAIL.
- [x] **Step 3:** Implement.
- [x] **Step 4:** Same command → PASS.
- [x] **Step 5: Commit** `feat(jam): app token expiry, touch and resolution`.

### Task 3: Consent page and code exchange

The consent page is **server-rendered HTML**, not an SPA route. It lives on the ungated `/app/auth/` mount. It needs no bundle or bootstrap, and it must work for a viewer the user gate would bounce. A plain `<form method=post>` behind `RequireSameOrigin` is this codebase's CSRF pattern.

**Files:**
- Create:
  - `internal/jam/apptoken/codes.go`, `service.go`, `consent.html` (embedded; uses `/app/static/jam.css`);
  - tests `codes_test.go`, `service_test.go`.
- Modify:
  - `internal/jam/appui/mux.go`: `Deps.AppTokenRoutes http.Handler`, mounted at `/app/auth/app-token` and `/app/auth/app-token/` when non-nil;
  - `cmd/jam/main.go:2834-2853`: when `sessions != nil`, build the service, set `AppTokenRoutes`, and `go svc.Run(ctx)`.

**Interfaces:**
- Consumes:
  - `appui.Sessions.Resolve` (`appui/gate.go:25`) and `browserauth.RequireSameOrigin`;
  - `jam.NewAppToken`, `jam.CleanAppTokenLabel`, `PutAppToken`.
- Produces:
  ```go
  const CodeTTL = 60 * time.Second
  type Config struct {
      Store jam.Store; Sessions *appui.Sessions; Live *Live // Task 4
      TrustedOrigins []string; Brand string; Now func() time.Time; Log *slog.Logger
  }
  func New(c Config) *Service
  func (s *Service) Routes() http.Handler // GET+POST /app/auth/app-token, POST /app/auth/app-token/exchange
  func (s *Service) Run(ctx context.Context) // every 5 s: prune codes, Live.Sweep; hourly: DeleteExpiredAppTokens → Live.Revoke each
  func ValidRedirect(s string) bool
  ```
- Behaviour:
  - **GET** validates the parameters:
    - `redirect` passes `ValidRedirect`;
    - `state` is 1–512 bytes;
    - `code_challenge` is 43 base64url characters;
    - `code_challenge_method` is `S256`;
    - `client` is optional and passes through `CleanAppTokenLabel`.

    Any failure is a `400` text page.
  - It then calls `Resolve`:
    - `SessionNone` → `302 /app/auth/login?return_to=<RequestURI>`;
    - `SessionForbidden`, or no `p.User` → `403` ("App tokens need a Jam user in at least one project");
    - `OK` → the consent page. It names the client and user, adds a line "with your operator rights, until <date>" when `p.Admin`, and has Allow and Cancel buttons. Hidden fields repeat the parameters.
  - **POST** goes through `RequireSameOrigin`, re-validates the parameters and re-resolves the session:
    - Allow → `303 <redirect>?code=…&state=…`, with `OperatorID = p.Operator.ID` if `p.Admin`, else "";
    - Cancel → `303 <redirect>?error=access_denied&state=…`.

    Query parameters are set with `url.Values`, merged into any query the redirect already has.
  - **Exchange** takes a JSON body of at most 4 KiB: `{code, code_verifier, redirect}`. A verifier is 43–128 characters, `[A-Za-z0-9-._~]`, and `base64url(sha256(verifier)) == challenge`. Success is `200 {"token","expires_at","user"}` with `no-store`, and Jam logs `app token issued` with user, id, label and whether it has operator standing, and it `Live.Revoke`s any ids `PutAppToken` evicted. Every failure is `400 {"error":"invalid_grant"}`. The exchange has no same-origin check, because the client has no Origin.
  - Codes are kept by `HashToken(code)`, with at most 1024 outstanding (`503` beyond that).

- [x] **Step 1: Write the failing tests:**
  - `TestValidRedirect`, a table:
    - accepted: `vscode://aethons.jam/auth`, `vscode-insiders://aethons.jam/auth?x=1`;
    - rejected: `vscode://aethons.jam.evil/`, `vscode://aethons.jam`, `vscode://other/`, `https://aethons.jam/`, `""`.
  - `TestConsentBadParams400NoLoginRedirect` (Review Focus 2).
  - `TestConsentNoSession302Login`: `Location` is `/app/auth/login?return_to=%2Fapp%2Fauth%2Fapp-token%3F…`.
  - `TestConsentShowsClientAndUser`: the escaped label, the user's name, and the `no-store`, XFO and CSP headers.
  - `TestConsentAdminOnly403`.
  - `TestConsentPostCrossOrigin403`.
  - `TestConsentAllowRedirectsWithCodeAndState`.
  - `TestConsentCancelAccessDenied`.
  - `TestExchangeIssuesToken`: the store has a row with `Hash == HashToken(token)`, the response is `no-store`, and the captured log output doesn't contain the token, code or verifier.
  - `TestExchangeOperatorStandingFromAccessCookie`.
  - `TestExchangeLoopbackSessionNoOperatorStanding` (Review Focus 1). The request comes from `127.0.0.1` with only an ID cookie, so the token's `OperatorID == ""`.
  - `TestExchangeReplayWrongVerifierWrongRedirectExpired`, a table (Review Focus 3). Each case is `invalid_grant`, and after a wrong verifier the right one fails too.
- [x] **Step 2:** `go test ./internal/jam/apptoken/` → FAIL.
- [x] **Step 3:** Implement and wire.
- [x] **Step 4:** `go test ./internal/jam/apptoken/ ./internal/jam/appui/ ./cmd/jam/` → PASS.
- [x] **Step 5: Commit** `feat(jam): app-token consent page and PKCE exchange`.

### Task 4: Bearer auth on /app/user, refused on /app/admin, live revocation

**Files:**
- Create:
  - `internal/jam/apptoken/live.go`, `live_test.go`;
  - `internal/jam/apptoken/bearer.go`, `bearer_test.go`;
  - `internal/jam/appui/bearer.go`, `bearer_test.go`.
- Modify:
  - `internal/jam/appui/mux.go:97-104`:
    - `/app/user/` becomes `withBearer(d.Bearer, d.SessionCookies, <bearer chain>, <today's chain>)`, where the bearer chain is `refuseOnBearer(bearerRefusedPaths, RequireUser(WithShell(d.User, d.Shell)))`;
    - `/app/admin/` is wrapped in `RefuseBearer`.
  - `cmd/jam/main.go`: one `apptoken.NewLive()` shared with Task 3's service. Set `Deps.Bearer = svc.Bearer()` and `Deps.SessionCookies = {m.IDCookie(), m.AccessCookie()}`.

**Interfaces:**
- Consumes: `jam.ResolveAppToken`, `jam.ParticipantByUser`, `appui.WithPrincipal`.
- Produces:
  ```go
  // appui
  type BearerFunc func(r *http.Request, raw string) (rr *http.Request, done func(), err error) // err → 401 WWW-Authenticate: Bearer error="invalid_token"
  func RefuseBearer(next http.Handler) http.Handler // any Authorization: Bearer → 401
  var bearerRefusedPaths = []string{"POST /app/user/push/subscriptions", "DELETE /app/user/push/subscriptions", "GET /app/user/push/key"} // → 403
  // apptoken
  func NewLive() *Live
  func (l *Live) Track(ctx context.Context, id ident.ID) (context.Context, func())
  func (l *Live) Revoke(id ident.ID)                 // cancels every tracked ctx of id
  func (l *Live) Sweep(valid func(ident.ID) bool)    // Revoke ids no longer valid (gone, expired, user removed)
  func (s *Service) Bearer() appui.BearerFunc
  ```
- `Bearer` resolves the token and builds `Principal{User: &participant-or-nil, Admin: t.Operator(), Operator: jam.Operator{ID: t.OperatorID}}`, then calls `WithPrincipal` and `Live.Track`. A user in no project gets `User == nil`, so `RequireUser` answers `403`.
- `Run`'s sweep validity check is `AppTokenByID` present, not `Expired`, and `GetUser` live.

- [x] **Step 1: Write the failing tests:**
  - appui, with a fake `BearerFunc`:
    - `TestUserBearerReachesHandlerWithoutOrigin`: a POST with no Origin is 200, and `PrincipalFrom` is set.
    - `TestUserBearerWithCookie400`, for each of the two cookies.
    - `TestUserBearerInvalid401`.
    - `TestUserBearerPushRoutesRefused`: the three are 403, and `POST /app/user/push/focus` is 200.
    - `TestUserBearerOnLoopbackNotLocalAdmin` (Review Focus 5).
    - `TestAdminRefusesBearer`, off-loopback and on loopback.
    - `TestNoBearerUnchanged`: the cookie path still enforces same-origin.
  - apptoken:
    - `TestBearerMemberPrincipal`.
    - `TestBearerOperatorStanding`.
    - `TestBearerUserInNoProject`.
    - `TestLiveRevokeCancelsTracked`.
    - `TestLiveDoneUntracks`.
    - `TestLiveSweepCancelsRemovedUser`.
    - `TestRevokeEndsEventStream`. It mounts the real `userui` `/app/user/events` behind `appui.Handler` with `Bearer`, opens the stream with `httptest.NewServer`, calls `Live.Revoke`, and expects the body to hit EOF within 1 s (Review Focus 4).
- [x] **Step 2:** `go test ./internal/jam/appui/ ./internal/jam/apptoken/` → FAIL.
- [x] **Step 3:** Implement and wire.
- [x] **Step 4:** `go test ./internal/jam/... ./cmd/jam/` → PASS.
- [x] **Step 5: Commit** `feat(jam): app tokens as bearer on /app/user; never /app/admin`.

### Task 5: App tokens on /me/studios

**Files:**
- Modify:
  - `internal/jam/collabhttp/participant.go` and its test;
  - `cmd/jam/main.go:2946-2953, 3292-3309` (`mountParticipantStudios`);
  - `cmd/jam/collab_test.go:80-125`.

**Interfaces:**
- Consumes: `jam.ResolveAppToken`.
- Produces:
  ```go
  func AppTokenBearer(store jam.Store, now func() time.Time, log *slog.Logger) func(*http.Request) (collab.Principal, string, error)
  // → collab.Principal{UserID: u.ID, Operator: t.OperatorID}, u.Name; no header → ErrNoCredentials; bad → ErrInvalidToken
  func mountParticipantStudios(mux *http.ServeMux, lc *jam.OperatorLoginConfig, auth jam.OperatorAuthenticator, ca *sshca.CA, store jam.Store, routes func(func(*http.Request) (collab.Principal, string, error)) http.Handler) bool
  func participantStudiosBearer(appTok, idTok func(*http.Request) (collab.Principal, string, error)) func(*http.Request) (collab.Principal, string, error)
  ```
  - `participantStudiosBearer` sends a JWT-shaped bearer (exactly two `.`) to `idTok`, or answers `ErrInvalidToken` when `idTok` is nil. Anything else goes to `appTok`.
  - `mountParticipantStudios` mounts whenever `ca != nil`. It adds ID tokens only when `lc != nil` and `auth` is OIDC.
  - App-token `/me/studios` requests aren't tracked by `Live`. Open tunnels keep running on revoke (spec), and new requests fail.

- [ ] **Step 1: Write the failing tests:**
  - `TestAppTokenBearer`: member, operator standing (`Operator` set), expired, missing header.
  - `TestParticipantStudiosBearerDispatch`.
  - `TestMountParticipantStudios`, with updated cases:
    - "no device login" → mounted;
    - "loopback auth" → mounted;
    - "no SSH CA" → 404.
- [ ] **Step 2:** `go test ./internal/jam/collabhttp/ ./cmd/jam/ -run 'AppToken|ParticipantStudios'` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `go test ./internal/jam/collabhttp/ ./cmd/jam/` → PASS.
- [ ] **Step 5: Commit** `feat(jam): /me/studios accepts app tokens; mounts with ssh alone`.

### Task 6: Revocation APIs (user and operator)

**Files:**
- Create: `internal/jam/userui/api_app_tokens.go`, `api_app_tokens_test.go`.
- Modify:
  - `internal/jam/userui/api.go` (register the routes);
  - `internal/jam/userui/apitypes.go`;
  - `internal/jam/adminui/api_users.go`, `apitypes.go`, `api_users_test.go`, `adminui.go` (option);
  - the generated `web/app/src/api/types.ts` and `web/app/src/admin/api/types.ts` (`go generate ./internal/jam/userui/`);
  - `cmd/jam/main.go` (options get `live.Revoke`).

**Interfaces:**
- Produces:
  - userui:
    - `type AppTokenRow struct{ ID, Label string; Created, LastUsed, Expires time.Time; Operator bool }`, with json `id,label,created,lastUsed,expires,operator`;
    - `type AppTokensResponse struct{ Tokens []AppTokenRow }`, json `tokens`, newest `Created` first, never nil;
    - `func WithAppTokenRevoke(revoke func(ident.ID)) Option`.
  - Routes `GET /app/user/api/app-tokens` and `DELETE /app/user/api/app-tokens/{id}`. The delete is owner-scoped and always `204`, like push unsubscribe. It calls `revoke(id)` only when a row was deleted.
  - adminui:
    - `AppTokenRow` (same fields) and `UserDetail.AppTokens []AppTokenRow` (json `appTokens`);
    - `DELETE /app/admin/api/users/{user}/app-tokens/{token}`: `204`, or `404` "no such app token on this user", logging `ui app token revoked` with operator, user and token id;
    - `func WithAppTokenRevoke(revoke func(ident.ID)) Option`.

- [ ] **Step 1: Write the failing tests:**
  - `TestAPIAppTokensListsOwnNewestFirst`.
  - `TestAPIAppTokensDeleteOwn`: gone, `revoke` called once.
  - `TestAPIAppTokensDeleteOthers204Kept`: no `revoke` call.
  - `TestAPIAppTokensDeleteMalformedID204`.
  - `TestAPIUserDetailAppTokens`.
  - `TestAPIUserAppTokenRevoke`: the log line has the operator and token id, and not the hash.
  - `TestAPIUserAppTokenRevokeWrongUser404`.
- [ ] **Step 2:** `go test ./internal/jam/userui/ ./internal/jam/adminui/ -run AppToken` → FAIL.
- [ ] **Step 3:** Implement and regenerate the types.
- [ ] **Step 4:** `go test ./internal/jam/... && ./scripts/app-types-check.sh` → PASS.
- [ ] **Step 5: Commit** `feat(jam): list and revoke app tokens (user and operator APIs)`.

### Task 7: Web app: Connected apps

**Files:**
- Create: `web/app/src/account/ConnectedApps.tsx`, `ConnectedApps.test.tsx`; `web/app/src/admin/users/AppTokens.tsx`.
- Modify:
  - `web/app/src/shell/TitleBar.tsx` (`UserTitleBar`: add a "Connected apps…" item after Sounds…, opening the modal, following the `soundsDialog` pattern);
  - `web/app/src/admin/users/UserPage.tsx`, `users.test.tsx`.

**Interfaces:**
- Consumes: Task 6's routes and types.
- Produces:
  - `ConnectedApps({ opened, onClose })` is a Mantine `Modal` titled "Connected apps". It reads `useQuery(["app-tokens"], () => apiGet<AppTokensResponse>("app-tokens"))`, enabled only while opened.
  - Each row shows the label, "Last used <relative>", "Expires <date>", and an "operator rights" badge when `operator` is set. A Revoke button deletes (`apiAt(API).del`) and invalidates the query.
  - With no tokens it says "No apps are connected. Sign in from VS Code with Jam: Sign In."
  - The admin `AppTokens({ user, tokens })` is the same list on the user page, with Revoke going to the admin route. It invalidates the user query.

- [ ] **Step 1: Write the failing tests** (MSW):
  - `ConnectedApps lists tokens with operator badge`.
  - `Revoke deletes and refreshes` (asserts `DELETE /app/user/api/app-tokens/apt_1`).
  - `shows the empty state`.
  - TitleBar: `account menu opens Connected apps`.
  - admin: `user page lists and revokes app tokens`.
- [ ] **Step 2:** `cd web/app && npx vitest run src/account src/shell src/admin/users` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `just app-check` → PASS.
- [ ] **Step 5: Commit** `feat(app): Connected apps dialog and admin app-token list`.

### Task 8: Docs

**Files:**
- Create: `docs/usage/jam/app-tokens.md` (leaf, with frontmatter `summary`/`read_when`/`owns`/`prereqs`/`tier: leaf`/`updated`). It **owns**:
  - the consent and PKCE flow and its two routes and parameters;
  - the redirect prefixes and the 60 s single-use codes;
  - lifetimes: 30 days, sliding for members, fixed with operator standing, 5-minute touch granularity;
  - operator standing taken at consent;
  - bearer scope: accepted, refused, and cookie+bearer `400`;
  - the per-user cap;
  - revocation: Connected apps, the admin user page, streams ending, user removal within 5 s, SSH tunnels running on;
  - storage: hash only, not in `jam export`.
- Modify:
  - `docs/usage/jam/INDEX.md`: a row for app-tokens.md.
  - `docs/usage/jam/ui.md`: a route-table row `/app/auth/app-token`, `/app/auth/app-token/exchange` → app-tokens.md. Keep it ≤ 200 lines, tightening a nearby sentence if needed.
  - `docs/usage/jam/collaboration.md:60-62`: `/me/studios` takes an app token or a `login --participant` ID token, and mounts when `ssh:` is set (`device-client-id` only adds ID tokens).
  - `docs/usage/jam/push-delivery.md`: the subscription and key routes refuse app tokens.
  - `docs/usage/jam/ui-pages.md`: the user page's App tokens list.
  - `docs/web-app.md` § Security: bearer requests skip the same-origin check, and the app never sees a token.
  - `docs/usage/jam/backup.md`: app tokens are not exported.
  - The spec's status line: "slice 1 (app tokens) built".
- [ ] **Step 1:** Write the docs. Each fact goes in the one doc named above; the others link to it.
- [ ] **Step 2:** `python3 /agent-data/skills/docs-audit/scripts/docs_audit.py docs` → no new findings beyond the plan file's own.
- [ ] **Step 3: Commit** `docs: app tokens`.
