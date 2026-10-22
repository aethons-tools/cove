# Jam app (slices 1–3): one login, one `/app`, one title bar — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge the participant intercom (`/me`) and the operator console (`/ui`) into one browser app under `/app` with a single OIDC login, a principal-aware gate, and one shared title bar with blended tabs.

**Architecture:** A new package `internal/jam/appui` owns the browser shell: the `Principal` (member and/or admin), the gate that resolves it from two session cookies (ID token + access token) or loopback trust, the area guards, the `/app` mux (auth, static, user, admin, landing, legacy redirects), and the title bar. `meui` is renamed `userui` and rebased to `/app/user`; `adminui` is rebased to `/app/admin`. Both render the `appui` title bar.

**Tech Stack:** Go 1.27 stdlib `net/http` (1.22+ patterns), `html/template`, htmx; `github.com/coreos/go-oidc/v3` (already a dependency). Tests are `httptest`, hermetic.

**Spec:** `docs/superpowers/specs/2026-10-22-jam-app-pwa-design.md`

## Global Constraints

- Browser routes live under `/app`: `/app/user/*` (was `/me/*` UI), `/app/admin/*` (was `/ui/*`), `/app/auth/{login,callback,logout}`, `/app/static/*`.
- `/me/studios/*` (bearer collaboration API) and `/admin/*` (admin API) do **not** move. `/me/send` **does** move, to `/app/user/send` (it is cookie-gated UI, used by the composer).
- Session cookies: `jam_app_id` (ID token) and `jam_app_at` (access token), `Path=/app`, HttpOnly, Secure under TLS, SameSite=Lax.
- Admin = access token verifies with the configured operator scope (`jam.OIDCAuthenticator.VerifyToken`), or loopback trust. Jam config never grants admin.
- Sign-in admitted if user, admin, or both; neither → 403, never a login redirect (it would loop).
- Old `GET`/`HEAD` on `/ui/*` and `/me/*` UI paths → `308` to the `/app` equivalent, query preserved. Other methods on old paths → 404.
- Package `internal/jam/meui` is renamed `internal/jam/userui`.
- Title bar: logo · "{display name} Jam" · tabs (Inbox for users; Dashboard, Projects, Agents, Users, Specs, Config, Health for admins) · principal menu at far right. Tabs a principal can't use are not rendered.
- Docs are updated in the same PR as the behaviour (AGENTS.md rule). Design history under `docs/superpowers/` is not rewritten.
- Hermetic tests only; TDD (failing test first). Build/test via `just test`; if `go` can't fetch, read `docs/DEVELOPMENT.md`.
- Commit identity is already configured in the repo; end every commit message with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

- **Admin's access token expired, ID token still valid** → the person keeps the Inbox as a user-only principal; no redirect loop, no 500. (Test in Task 2.)
- **Valid login that is neither roster member nor admin** → 403 on every `/app` page including `/app/` landing; never a redirect to login. (Tests in Tasks 2 and 5.)
- **Old bookmark with a query** (`/me/?c=chn_x`, `/ui/agents?project=p`) → 308 to `/app/user/?c=chn_x` / `/app/admin/agents?project=p`; a `POST` to an old path is **not** redirected (no replayed write). (Test in Task 5.)
- **Loopback request carrying a member-only session** → principal is both user and admin (loopback trust adds admin). (Test in Task 2.)
- **DNS-rebinding Host on a loopback request** → still 403 under the new gate. (Test in Task 2.)

---

## Slice 1 — one login (PR A, not mounted)

Branch: `jam-app-1-login` from `jam-app-pwa` (which carries the spec commit).

### Task 1: browserauth — the `/app` mount with both tokens

**Files:**
- Modify: `internal/jam/browserauth/mount.go`, `internal/jam/browserauth/session.go`, `internal/jam/browserauth/service.go`, `internal/jam/browserauth/devidentity.go`
- Test: `internal/jam/browserauth/service_test.go`, `internal/jam/browserauth/devidentity_test.go`

**Interfaces:**
- Produces:
  - `const BothTokensSession SessionToken` — a mount storing both tokens.
  - `func AppMount() Mount` → `Mount{Prefix: "/app", SessionCookie: "jam_app", Session: BothTokensSession}`.
  - `func (m Mount) IDCookie() string` → `"jam_app_id"`; `func (m Mount) AccessCookie() string` → `"jam_app_at"` (i.e. `SessionCookie+"_id"` / `+"_at"`).
  - `func (s *Service) VerifySubject(ctx context.Context, raw string) (string, error)` — exported wrapper of `s.verifySubject`.
  - `func (s *Service) Issuer() string`.
  - `func (d DevIdentity) Participant() (jam.Participant, bool)` — the dev user's participant via their first OIDC identity.
  - `func (d DevIdentity) OperatorID() string` — first login, else `"local"`.

- [ ] **Step 1: Write failing tests** in `service_test.go` (follow the existing fake-IdP helpers in that file for building a `Service` and driving `callback`):

```go
func TestAppMountCookies(t *testing.T) {
	m := AppMount()
	if m.Prefix != "/app" || m.IDCookie() != "jam_app_id" || m.AccessCookie() != "jam_app_at" {
		t.Fatalf("AppMount = %+v id=%q at=%q", m, m.IDCookie(), m.AccessCookie())
	}
}

func TestCallbackBothTokensSetsTwoCookies(t *testing.T) {
	// Drive a successful callback on a Service built with AppMount() (reuse the
	// existing successful-callback test's fake IdP / exchange setup).
	// Expect: Set-Cookie jam_app_id=<id token> and jam_app_at=<access token>,
	// both Path=/app, HttpOnly, SameSite=Lax; redirect to "/app/" by default.
}

func TestLogoutBothTokensClearsBoth(t *testing.T) {
	// GET /app/auth/logout → both jam_app_id and jam_app_at set with MaxAge<0,
	// redirect to /app/auth/login.
}
```

Write the two callback/logout bodies concretely by copying the nearest existing test (`grep -n 'func Test.*Callback' service_test.go`) and switching the mount.

- [ ] **Step 2: Run** `go test ./internal/jam/browserauth/ -run 'AppMount|BothTokens' -v` — expect FAIL (undefined `AppMount`).

- [ ] **Step 3: Implement.** In `mount.go`:

```go
const (
	AccessTokenSession SessionToken = iota
	IDTokenSession
	// BothTokensSession stores both tokens, each in its own cookie (two JWTs can
	// exceed a single cookie's 4 KB): the ID token names the roster user, the
	// access token carries the operator scope. The /app plane.
	BothTokensSession
)

func (m Mount) IDCookie() string     { return m.SessionCookie + "_id" }
func (m Mount) AccessCookie() string { return m.SessionCookie + "_at" }

// AppMount is the unified browser app's "/app" mount.
func AppMount() Mount {
	return Mount{Prefix: "/app", SessionCookie: "jam_app", Session: BothTokensSession}
}
```

In `session.go`, generalize `setSession`/`clearSession` to a cookie name, and add both-token variants:

```go
func (m Mount) setCookie(w http.ResponseWriter, name, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: token, Path: m.Prefix,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

func (m Mount) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: m.Prefix,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

func (m Mount) setSession(w http.ResponseWriter, token string, secure bool) { m.setCookie(w, m.SessionCookie, token, secure) }

func (m Mount) clearSession(w http.ResponseWriter) {
	if m.Session == BothTokensSession {
		m.clearCookie(w, m.IDCookie())
		m.clearCookie(w, m.AccessCookie())
		return
	}
	m.clearCookie(w, m.SessionCookie)
}
```

In `service.go` `callback`, replace the token selection:

```go
	switch s.mount.Session {
	case BothTokensSession:
		s.mount.setCookie(w, s.mount.IDCookie(), idTok, isSecure(r))
		s.mount.setCookie(w, s.mount.AccessCookie(), accessTok, isSecure(r))
	case IDTokenSession:
		s.mount.setSession(w, idTok, isSecure(r))
	default:
		s.mount.setSession(w, accessTok, isSecure(r))
	}
```

and add:

```go
// VerifySubject verifies a browser ID token (aud = ClientID) and returns its subject.
func (s *Service) VerifySubject(ctx context.Context, raw string) (string, error) { return s.verifySubject(ctx, raw) }

// Issuer is the OIDC issuer this service logs in against.
func (s *Service) Issuer() string { return s.cfg.Issuer }
```

In `devidentity.go`, add (reusing the unexported `user()`):

```go
// Participant is the dev user's participant, resolved through their first
// OIDC identity as a real login would be.
func (d DevIdentity) Participant() (jam.Participant, bool) {
	u, ok := d.user()
	if !ok || len(u.OIDC) == 0 {
		return jam.Participant{}, false
	}
	return jam.ParticipantByIdentity(d.Store, u.OIDC[0].Issuer, u.OIDC[0].Subject)
}

// OperatorID is the dev user's first login, else "local".
func (d DevIdentity) OperatorID() string {
	if u, ok := d.user(); ok && len(u.Logins) > 0 {
		return u.Logins[0]
	}
	return "local"
}
```

Refactor `OperatorLoopbackTrust` and `ParticipantSession` in that file to call these (behaviour unchanged). Add a `devidentity_test.go` case for `Participant()`/`OperatorID()` using that file's existing fake store.

- [ ] **Step 4: Run** `go test ./internal/jam/browserauth/ -v` — expect PASS (all, including existing).
- [ ] **Step 5: Commit** `feat(jam): browserauth /app mount storing ID and access tokens`.

### Task 2: appui — Principal, gate and area guards

**Files:**
- Create: `internal/jam/appui/appui.go` (package doc), `internal/jam/appui/principal.go`, `internal/jam/appui/gate.go`
- Test: `internal/jam/appui/gate_test.go`

**Interfaces:**
- Consumes: Task 1's `Mount.IDCookie/AccessCookie`, `Service.VerifySubject/Issuer`, `DevIdentity.Participant/OperatorID`; existing `browserauth.Gate`, `browserauth.SessionOutcome`, `jam.ParticipantByIdentity`, `jam.WithParticipant`, `jam.WithOperator`, `jam.IsLoopbackRequest`.
- Produces:

```go
type Principal struct {
	User     *jam.Participant // roster member of ≥1 project, else nil
	Admin    bool
	Local    bool         // admin by loopback trust with no login
	Operator jam.Operator // set when Admin
}
func (p Principal) DisplayName() string
func WithPrincipal(r *http.Request, p Principal) *http.Request // also injects jam.WithParticipant / jam.WithOperator
func PrincipalFrom(r *http.Request) (Principal, bool)

type Sessions struct {
	Issuer       string
	IDCookie     string
	AccessCookie string
	VerifyID     func(ctx context.Context, raw string) (subject string, err error)
	VerifyAccess func(ctx context.Context, raw string) (jam.Operator, error) // nil = no operator OIDC
	Store        jam.ParticipantStore
}
func (s Sessions) Resolve(r *http.Request) (*http.Request, browserauth.SessionOutcome)

type GateConfig struct {
	Sessions      *Sessions                 // nil = no browser login (loopback-only)
	Dev           *browserauth.DevIdentity  // nil = none
	LoginPath     string                    // "/app/auth/login" when Sessions != nil
	ExpectedHosts []string
	Log           *slog.Logger
}
func NewGate(c GateConfig) browserauth.Gate
func RequireUser(next http.Handler) http.Handler
func RequireAdmin(next http.Handler) http.Handler
```

- [ ] **Step 1: Write the failing gate matrix** in `gate_test.go`. Use fakes: `VerifyID` returns subject = raw value unless raw == "bad"; `VerifyAccess` returns `jam.Operator{ID: "op-"+raw}` unless raw == "bad" or "expired" (error). The fake store maps subject `"alice"` to a user who is a project member, `"stranger"` to nobody. Build requests with `httptest.NewRequest`; set `r.RemoteAddr = "203.0.113.5:1234"` for off-loopback, `"127.0.0.1:1234"` with `r.Host = "127.0.0.1:8443"` for loopback. Probe handler writes the principal:

```go
func probe() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r)
		fmt.Fprintf(w, "user=%v admin=%v local=%v", p.User != nil, p.Admin, p.Local)
	})
}

func TestGateMatrix(t *testing.T) {
	cases := []struct {
		name           string
		loopback       bool
		idTok, atTok   string // "" = cookie absent
		wrap           func(http.Handler) http.Handler
		wantCode       int
		wantBody       string // substring
		wantLocation   string
	}{
		{"member only", false, "alice", "", nil, 200, "user=true admin=false", ""},
		{"admin only", false, "stranger", "ok", nil, 200, "user=false admin=true", ""},
		{"both", false, "alice", "ok", nil, 200, "user=true admin=true", ""},
		{"neither", false, "stranger", "", nil, 403, "", ""},
		{"no cookies", false, "", "", nil, 302, "", "/app/auth/login"},
		{"bad id token only", false, "bad", "", nil, 302, "", "/app/auth/login"},
		{"expired access, valid member", false, "alice", "expired", nil, 200, "user=true admin=false", ""},
		{"loopback no session", true, "", "", nil, 200, "user=false admin=true local=true", ""},
		{"loopback member session", true, "alice", "", nil, 200, "user=true admin=true local=false", ""},
		{"user area, admin only", false, "stranger", "ok", RequireUser, 403, "", ""},
		{"admin area, member only", false, "alice", "", RequireAdmin, 403, "", ""},
		{"admin area, loopback", true, "", "", RequireAdmin, 200, "admin=true", ""},
		{"user area, loopback no session", true, "", "", RequireUser, 403, "", ""},
	}
	// For each: build NewGate(GateConfig{Sessions: &fakeSessions, LoginPath: "/app/auth/login"}),
	// h := probe(); if tc.wrap != nil { h = tc.wrap(h) }; serve gate.Wrap(h); assert code, body, Location.
}

func TestGateRebindingHostRefused(t *testing.T) {
	// loopback RemoteAddr with Host "evil.example:8443" → 403.
}

func TestGateNoBrowserLogin(t *testing.T) {
	// Sessions nil: loopback → admin+local 200; off-loopback → 403 (no redirect).
}

func TestGateDevIdentity(t *testing.T) {
	// Dev set with a user bound to "alice": loopback no session → user=true admin=true.
	// (Reuse the fake DevIdentityStore pattern from browserauth/devidentity_test.go.)
}

func TestWithPrincipalInjectsLegacyContext(t *testing.T) {
	// WithPrincipal(r, Principal{User: &p, Admin: true, Operator: jam.Operator{ID: "x"}})
	// → jam.ParticipantFrom ok with p; jam.OperatorFrom ok with ID "x".
}
```

Fill each case body concretely; no case may be left as a comment.

- [ ] **Step 2: Run** `go test ./internal/jam/appui/ -v` — expect FAIL (package missing).

- [ ] **Step 3: Implement.** `appui.go`:

```go
// Package appui is the Jam browser app's shell under /app: the Principal a
// request acts as (a roster member, an admin, or both), the one gate that
// resolves it, the area guards, the /app mux and the shared title bar.
// The participant (userui) and operator (adminui) UIs render inside it.
package appui
```

`principal.go`:

```go
type Principal struct {
	User     *jam.Participant
	Admin    bool
	Local    bool
	Operator jam.Operator
}

// DisplayName is what the principal menu shows.
func (p Principal) DisplayName() string {
	switch {
	case p.User != nil:
		return p.User.Name
	case p.Local:
		return "Local admin"
	default:
		return p.Operator.ID
	}
}

type principalKey struct{}

// WithPrincipal carries p on r, and the legacy per-plane identities the UIs'
// handlers read (jam.ParticipantFrom, jam.OperatorFrom).
func WithPrincipal(r *http.Request, p Principal) *http.Request {
	if p.User != nil {
		r = jam.WithParticipant(r, *p.User)
	}
	if p.Admin {
		r = jam.WithOperator(r, p.Operator)
	}
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
}

func PrincipalFrom(r *http.Request) (Principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(Principal)
	return p, ok
}
```

`gate.go`:

```go
// Resolve reads the two session cookies. Either verified token authenticates;
// the ID token may name a roster user, the access token may carry admin. A
// verified identity that is neither is SessionForbidden (re-login would
// resolve the same), no verified token is SessionNone.
func (s Sessions) Resolve(r *http.Request) (*http.Request, browserauth.SessionOutcome) {
	var p Principal
	authed := false
	if c, err := r.Cookie(s.IDCookie); err == nil && c.Value != "" {
		if sub, err := s.VerifyID(r.Context(), c.Value); err == nil {
			authed = true
			if u, ok := jam.ParticipantByIdentity(s.Store, s.Issuer, sub); ok {
				p.User = &u
			}
		}
	}
	if s.VerifyAccess != nil {
		if c, err := r.Cookie(s.AccessCookie); err == nil && c.Value != "" {
			if op, err := s.VerifyAccess(r.Context(), c.Value); err == nil {
				authed, p.Admin, p.Operator = true, true, op
			}
		}
	}
	switch {
	case !authed:
		return r, browserauth.SessionNone
	case p.User == nil && !p.Admin:
		return r, browserauth.SessionForbidden
	}
	return WithPrincipal(r, p), browserauth.SessionOK
}

// NewGate builds the /app gate: loopback is always an admin (the local
// operator, as /ui was), a signed-in loopback viewer keeps their session's
// identity and gains admin, and off-loopback needs a session.
func NewGate(c GateConfig) browserauth.Gate {
	g := browserauth.Gate{LoginPath: c.LoginPath, ExpectedHosts: c.ExpectedHosts, Log: c.Log}
	g.LoopbackTrust = func(r *http.Request) *http.Request {
		p := Principal{Admin: true, Local: true, Operator: jam.Operator{ID: "local"}}
		if c.Dev != nil {
			p.Operator.ID = c.Dev.OperatorID()
			if u, ok := c.Dev.Participant(); ok {
				p.User = &u
			}
		}
		return WithPrincipal(r, p)
	}
	if c.Sessions != nil {
		g.Session = func(r *http.Request) (*http.Request, browserauth.SessionOutcome) {
			rr, out := c.Sessions.Resolve(r)
			if out == browserauth.SessionOK && jam.IsLoopbackRequest(r) {
				p, _ := PrincipalFrom(rr)
				if !p.Admin {
					p.Admin, p.Operator = true, jam.Operator{ID: "local"}
					rr = WithPrincipal(rr, p)
				}
			}
			return rr, out
		}
	}
	return g
}

// RequireUser admits a principal that is a roster member.
func RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r); !ok || p.User == nil {
			http.Error(w, "signed in, but not a member of any project — ask an admin to add you to a roster", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin admits an admin principal.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFrom(r); !ok || !p.Admin {
			http.Error(w, "signed in, but not an admin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

Note the existing `browserauth.Gate.Wrap` behaviour this relies on: a loopback request with a SessionOK uses that session; otherwise LoopbackTrust applies (so a loopback "neither" session falls back to local admin — correct).

- [ ] **Step 4: Run** `go test ./internal/jam/appui/ ./internal/jam/browserauth/ -v` — expect PASS.
- [ ] **Step 5: Run** `just test` and `just lint` — expect PASS.
- [ ] **Step 6: Commit** `feat(jam): appui principal and gate for the /app browser plane`.

**Slice 1 wrap-up:** push `jam-app-1-login`, open PR A ("Jam app slice 1: one login (not yet mounted)"), body linking the spec; note no behaviour change, so no user docs change. Merge when green and reviewed.

---

## Slice 2 — move everything under `/app` (PR B)

Branch: `jam-app-2-move` from `main` after PR A merges.

### Task 3: rename `meui` → `userui` and rebase to `/app/user`

**Files:**
- Move: `internal/jam/meui/` → `internal/jam/userui/` (`git mv`)
- Modify: every file in it; `cmd/at-jam/main.go` (import)
- Test: the package's existing tests, rebased

**Interfaces:**
- Produces: `userui.Handler(d Deps, lg *slog.Logger, opts ...Option) http.Handler` serving `/app/user/*` (same routes as before, new prefix), **without** a static mount. Stylesheet href is `uiassets.StylesheetHref("/app/static/")`. `userui.Deps`, `userui.Option`, `userui.WithChanges`, `userui.WithPresence` unchanged otherwise.

- [ ] **Step 1: Move and rename.**

```bash
git mv internal/jam/meui internal/jam/userui
sed -i 's/^package meui$/package userui/; s/^package meui_test$/package userui_test/' internal/jam/userui/*.go
grep -rl 'internal/jam/meui' --include=*.go . | xargs sed -i 's#internal/jam/meui#internal/jam/userui#; s/\bmeui\./userui./g'
```

- [ ] **Step 2: Rebase paths** in Go, templates and tests:

```bash
cd internal/jam/userui
sed -i 's#"/me/#"/app/user/#g; s#"/me"#"/app/user"#g; s#/me/static/#/app/static/#g' *.go
sed -i 's#/me/#/app/user/#g' templates/*.html
```

Then fix by hand: delete the `mux.Handle("GET /me/static/", …)` line (now `/app/user/static`) from `handler.go` — static is served by appui (Task 5); change the stylesheet func to `uiassets.StylesheetHref("/app/static/")`; grep `grep -rn '/me\b\|/me/' .` and resolve every remaining hit (comments included — say `/app/user`). The composer posts to `/me/send`: it is now `/app/user/send` (the sed covers templates; confirm in `templates/inbox.html` and any JS).

- [ ] **Step 3: Update tests** for the static move: any test fetching `/me/static/...` from `userui.Handler` should instead assert the page links `/app/static/jam.css?v=`. Run `go test ./internal/jam/userui/ -v` — expect PASS.
- [ ] **Step 4: Commit** `refactor(jam): meui becomes userui, served under /app/user`.

### Task 4: rebase `adminui` to `/app/admin`

**Files:**
- Modify: `internal/jam/adminui/*.go`, `internal/jam/adminui/templates/*.html`
- Test: `internal/jam/adminui/*_test.go`

**Interfaces:**
- Produces: `adminui.Handler(...)` (signature unchanged) serving `/app/admin/*`, **without** a static mount; stylesheet `uiassets.StylesheetHref("/app/static/")`.

- [ ] **Step 1: Rebase.**

```bash
cd internal/jam/adminui
sed -i 's#/ui/static/#/app/static/#g' *.go templates/*.html
sed -i 's#"/ui/#"/app/admin/#g; s#"/ui"#"/app/admin"#g; s#`/ui/#`/app/admin/#g' *.go
sed -i 's#/ui/#/app/admin/#g' templates/*.html
```

Then: delete the `mux.Handle("GET /app/static/", uiassets.Handler(...))` line in `adminui.go`; fix `projectFromRoute`'s `strings.CutPrefix(pat, "/ui/projects/{")` (now `/app/admin/projects/{`); `grep -rn '/ui\b\|/ui/' . ` and fix every remaining hit, including string-built URLs (`"/ui" + ...`), comments, and the `originGuard`. Leave `/ui/auth` mentions out of adminui entirely (login is appui's).

- [ ] **Step 2: Run** `go test ./internal/jam/adminui/ -v`. Failures are leftover `/ui` literals in tests — `sed -i 's#/ui/#/app/admin/#g; s#"/ui"#"/app/admin"#g' *_test.go`, then re-run until PASS. Static-asset tests move as in Task 3.
- [ ] **Step 3: Commit** `refactor(jam): adminui served under /app/admin`.

### Task 5: appui mux, legacy redirects, wiring, docs

**Files:**
- Create: `internal/jam/appui/mux.go`, `internal/jam/appui/redirect.go`
- Test: `internal/jam/appui/mux_test.go`
- Modify: `internal/jam/admin.go` (`NewAdminHandler`), its callers (`grep -rn 'NewAdminHandler(' --include=*.go .`, ~71 sites, mostly tests), `cmd/at-jam/main.go` (lines ~2560–2690, the `/ui` and `/me` wiring), `internal/jam/browserauth/mount.go` (delete `OperatorUIMount`, `ParticipantMount` once unused), docs listed in Step 7.

**Interfaces:**
- Consumes: Task 2 `NewGate`, `RequireUser`, `RequireAdmin`, `PrincipalFrom`; Task 3/4 handlers.
- Produces:

```go
type Deps struct {
	Gate           browserauth.Gate
	AuthRoutes     http.Handler // /app/auth/*; nil = no browser login
	User           http.Handler // serves /app/user/*; nil = none
	Admin          http.Handler // serves /app/admin/*
	TrustedOrigins []string
}
// Handler returns the browser mux: callers may add more routes (cmd adds /me/studios/).
func Handler(d Deps) *http.ServeMux
func LegacyRedirect(from, to string) http.Handler
```

`jam.NewAdminHandler(..., ui http.Handler, me http.Handler, opts...)` becomes `jam.NewAdminHandler(..., browser http.Handler, opts...)`, mounting `parent.Handle("/", browser)` when non-nil.

- [ ] **Step 1: Write failing mux tests** (`mux_test.go`), with stub `User`/`Admin` handlers echoing `r.URL.Path` and a gate from `NewGate` using fake `Sessions` (as in Task 2):

```go
func TestLegacyRedirects(t *testing.T) {
	cases := []struct {
		method, path string
		wantCode    int
		wantLoc     string
	}{} // rows as below
	// GET  /me/?c=chn_x            → 308 /app/user/?c=chn_x
	// GET  /me/rail                → 308 /app/user/rail
	// GET  /ui/agents?project=p    → 308 /app/admin/agents?project=p
	// GET  /ui/                    → 308 /app/admin/
	// GET  /ui/auth/login          → 308 /app/auth/login
	// GET  /me/auth/callback?code=x → 308 /app/auth/callback?code=x
	// GET  /ui/static/jam.css      → 308 /app/static/jam.css
	// HEAD /me/                    → 308 /app/user/
	// POST /me/send                → 404
	// POST /ui/agents              → 404
}

func TestLanding(t *testing.T) {
	// GET / → 302 /app/ (ungated)
	// GET /app/ member → 302 /app/user/; admin-only → 302 /app/admin/; both → /app/user/
	// GET /app/ neither → 403; no session off-loopback → 302 /app/auth/login
}

func TestAreasGated(t *testing.T) {
	// /app/user/x member → 200 (stub); admin-only → 403
	// /app/admin/x admin → 200; member-only → 403
	// POST /app/user/send with Origin https://evil.example → 403 (RequireSameOrigin)
}

func TestStaticUngated(t *testing.T) {
	// GET /app/static/jam.css with no session off-loopback → 200, text/css
}

func TestStudiosRouteCanBeAdded(t *testing.T) {
	// m := Handler(d); m.Handle("/me/studios/", stub) → GET /me/studios/x hits stub, not the redirect.
}
```

Write every case concretely (table rows, not comments).

- [ ] **Step 2: Run** `go test ./internal/jam/appui/ -run 'Legacy|Landing|Areas|Static|Studios' -v` — expect FAIL.

- [ ] **Step 3: Implement** `redirect.go`:

```go
// LegacyRedirect permanently redirects GET/HEAD under the old prefix from to
// the same path under to, keeping the query. Any other method is 404: a write
// to an old URL is never replayed elsewhere.
func LegacyRedirect(from, to string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		u := to + strings.TrimPrefix(r.URL.Path, from)
		if r.URL.RawQuery != "" {
			u += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, u, http.StatusPermanentRedirect)
	})
}
```

`mux.go`:

```go
// legacy maps the pre-/app browser prefixes to their new homes; the longest
// prefix wins (ServeMux). Delete one release after slice 2 ships.
var legacy = []struct{ from, to string }{
	{"/ui/auth/", "/app/auth/"}, {"/me/auth/", "/app/auth/"},
	{"/ui/static/", "/app/static/"}, {"/me/static/", "/app/static/"},
	{"/ui/", "/app/admin/"}, {"/me/", "/app/user/"},
}

func Handler(d Deps) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /app/static/", uiassets.Handler("/app/static/"))
	if d.AuthRoutes != nil {
		mux.Handle("/app/auth/", d.AuthRoutes)
	}
	if d.User != nil {
		mux.Handle("/app/user/", d.Gate.Wrap(RequireUser(browserauth.RequireSameOrigin(d.TrustedOrigins, d.User))))
	}
	if d.Admin != nil {
		mux.Handle("/app/admin/", d.Gate.Wrap(RequireAdmin(d.Admin)))
	}
	mux.Handle("GET /app/{$}", d.Gate.Wrap(http.HandlerFunc(landing)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusFound)
	})
	for _, l := range legacy {
		mux.Handle(l.from, LegacyRedirect(l.from, l.to))
	}
	return mux
}

// landing sends a member to the Inbox, an admin who is not one to the console.
func landing(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r)
	switch {
	case p.User != nil:
		http.Redirect(w, r, "/app/user/", http.StatusFound)
	case p.Admin:
		http.Redirect(w, r, "/app/admin/", http.StatusFound)
	default:
		http.Error(w, "signed in, but not authorized here", http.StatusForbidden)
	}
}
```

If registering `"GET /{$}"` alongside `"/me/"` panics as a conflict, register the root as `"/{$}"` with an explicit method check instead — run the tests to find out.

- [ ] **Step 4: Run** the appui tests — expect PASS.

- [ ] **Step 5: Change `jam.NewAdminHandler`** in `internal/jam/admin.go`: replace the `ui, me http.Handler` params with `browser http.Handler`; replace the mounting block with:

```go
	guarded := authMiddleware(auth, log, mux) // guards every /admin/* route
	if browser == nil {
		return guarded
	}
	// The browser app (/app, its legacy redirects, and /me/studios) owns its own
	// gates, so it is mounted OUTSIDE the /admin/* authenticator. Its session
	// cookies are Path=/app and never reach /admin.
	parent := http.NewServeMux()
	parent.Handle("/admin/", guarded)
	parent.Handle("/", browser)
	return parent
```

Update every caller (`, nil, nil,` → `, nil,` in tests; check each hit, some pass real handlers). Run `go test ./internal/jam/ -v` — PASS.

- [ ] **Step 6: Rewire `cmd/at-jam/main.go`.** Replace the `uiMux`/`gate`/`meMux`/`meGate`/`meSurface` block with:

```go
		var sessions *appui.Sessions
		var authRoutes http.Handler
		if bc := cfg.browserAuthConfig(); bc != nil {
			svc, err := browserauth.New(context.Background(), *bc, browserauth.AppMount(), nil, log)
			if err != nil {
				fmt.Fprintln(stderr, "at-jam: browser login:", err)
				return 1
			}
			authRoutes = svc.Routes()
			m := browserauth.AppMount()
			sessions = &appui.Sessions{Issuer: svc.Issuer(), IDCookie: m.IDCookie(), AccessCookie: m.AccessCookie(), VerifyID: svc.VerifySubject, Store: st}
			if oidcAuth, ok := auth.(*jam.OIDCAuthenticator); ok {
				sessions.VerifyAccess = oidcAuth.VerifyToken
			}
			log.Info("Jam app auth: browser OIDC login", "client-id", bc.ClientID)
		} else {
			log.Info("Jam app auth: loopback-only")
		}
		gc := appui.GateConfig{Sessions: sessions, ExpectedHosts: cfg.UIHosts, Log: log}
		if sessions != nil {
			gc.LoginPath = "/app/auth/login"
		}
		if d := cfg.DevIdentity; d != nil {
			gc.Dev = &browserauth.DevIdentity{Store: st, User: d.User, LegacyProject: d.DeprecatedProject, LegacyHuman: d.DeprecatedHuman}
			log.Warn("DEV IDENTITY ACTIVE: ...") // keep the existing message
		}
		userMux := http.NewServeMux()
		sendH := jam.NewParticipantSendHandler(st, ic, log)
		sendH.SetAttachments(att)
		userMux.Handle("/app/user/send", sendH)
		userMux.Handle("/app/user/", userui.Handler(userui.Deps{Store: st, Intercom: ic, Log: chlog, Legacy: ml, Attachments: att, Boards: boards}, log, userui.WithChanges(logChanges), userui.WithPresence(sessPresence)))
		browser := appui.Handler(appui.Deps{
			Gate: appui.NewGate(gc), AuthRoutes: authRoutes, User: userMux,
			Admin:          adminui.Handler(st, log, sup, personalAllocator{alloc}, credExists, squawkReader, uiOpts...),
			TrustedOrigins: cfg.UIOrigins,
		})
```

Keep the existing `uiOpts` construction and the `liveConns.Run` goroutine. Point `mountParticipantStudios` at `browser` (it takes a `*http.ServeMux`; change its parameter name from `meMux`), and pass `browser` to `jam.NewAdminHandler`. Before, `/me` was only mounted when a participant session existed; now `/app/user` is always mounted and RequireUser refuses non-members — that is intended. Check `jam.OIDCAuthenticator.VerifyToken`'s signature matches `func(context.Context, string) (jam.Operator, error)`; adapt with a closure if not. Delete `OperatorUIMount`/`ParticipantMount` and `Service.ParticipantSession`/`OperatorSession` if nothing else uses them (`grep`); keep `PreviewMount`. Grep `cmd/` for remaining `"/ui` and `"/me` literals (e.g. `previewhost.go`, `serve` log lines, `config.go` validation messages) and update. Run `go build ./... && just test` — PASS.

- [ ] **Step 7: Docs.** Update every user-facing doc that names the old paths, routing via the docs-author skill (single source of truth, frontmatter `updated:`):
  - `docs/usage/jam/ui.md` — owns the gate: rewrite the `/ui` + `/me` auth sections as the one `/app` login (two cookies, Principal, admin = scope or loopback, member = roster, neither = 403), the route table, legacy 308s (one release), and the **IdP redirect URI is now `…/app/auth/callback`**.
  - `docs/usage/jam/intercom-ui.md`, `intercom.md`, `attachments.md`, `whiteboards.md`, `collaboration.md` (keep `/me/studios/*`), `session-events.md`, `ui-*.md`, `model-specs.md`, `previews.md`, `serve.md`, `docs/usage/at-cove-secrets.md`, `docs/usage/jam/INDEX.md` rows — replace `/ui/` → `/app/admin/`, `/me/` → `/app/user/`, `/ui/auth/callback` & `/me/auth/callback` → `/app/auth/callback`, except `/me/studios`.
  - Find them: `grep -rln '/ui/\|/me/\|"/ui"\|"/me"' docs/usage docs/OVERVIEW.md`.
  - Run the docs-audit skill's checker; fix findings.
- [ ] **Step 8: Commit** `feat(jam): one Jam app under /app with one login; legacy /ui and /me redirect`.

**Slice 2 wrap-up:** push, open PR B with a deploy note (register `…/app/auth/callback` at the IdP before deploying). Merge when green and reviewed.

---

## Slice 3 — title bar and blended tabs (PR C)

Branch: `jam-app-3-titlebar` from `main` after PR B merges.

### Task 6: brand marks and the appui title bar

**Files:**
- Bring in from `origin/bam/logo` only: `assets/j-logo.ai`, `assets/j-logo.svg`, `assets/j-logo-plain.svg`
- Move: `assets/j-logo.svg`, `assets/j-logo-plain.svg` → `internal/jam/uiassets/static/brand/`
- Delete: `assets/Main.svg`, `assets/Smaller.svg`, `assets/*.png`
- Modify: `internal/jam/uiassets/uiassets.go` (embed `static/brand/*.svg`), `internal/jam/uiassets/static/jam.css` (title bar styles)
- Create: `internal/jam/appui/titlebar.go`, `internal/jam/appui/titlebar.html` (embedded)
- Test: `internal/jam/appui/titlebar_test.go`, `internal/jam/uiassets/uiassets_test.go`

**Interfaces:**
- Produces:

```go
type Badge struct { N int; Red bool; Title string }
type Tab struct { Key, Label, Href string; Current bool; Badge Badge }
type Titlebar struct {
	Brand     string // "{display name} Jam", else "Jam"
	Tabs      []Tab
	Name      string // Principal.DisplayName()
	Roles     string // "member", "admin" or "member · admin"
	SignOut   bool   // false for a Local admin or with no browser login
}
// Shell is what the gate puts on the context for the title bar.
type Shell struct {
	Brand       string
	SignOut     bool                                    // browser login configured
	AdminBadges func(*http.Request) map[string]Badge // nil = none
}
func WithShell(next http.Handler, s Shell) http.Handler
func TitlebarFor(r *http.Request, current string) Titlebar
const TitlebarTemplate string // defines {{define "app-titlebar"}} over a Titlebar
```

Tab keys and hrefs, in order: `inbox` `/app/user/` (users); `dashboard` `/app/admin/`, `projects` `/app/admin/projects`, `agents` `/app/admin/agents`, `users` `/app/admin/users`, `specs` `/app/admin/specs`, `config` `/app/admin/config`, `health` `/app/admin/health` (admins).

- [ ] **Step 1: Bring in the logo files.**

```bash
git fetch origin bam/logo
git checkout origin/bam/logo -- assets/j-logo.ai assets/j-logo.svg assets/j-logo-plain.svg
git rm -q assets/Main.svg assets/Smaller.svg assets/*.png
mkdir -p internal/jam/uiassets/static/brand
git mv assets/j-logo.svg assets/j-logo-plain.svg internal/jam/uiassets/static/brand/
```

Extend the embed: `//go:embed static/jam.css static/htmx.min.js static/brand/*.svg`. Add a uiassets test: `GET <prefix>brand/j-logo-plain.svg` → 200, `image/svg+xml`. Confirm `grep -c '<text' internal/jam/uiassets/static/brand/*.svg` prints 0 for both.

- [ ] **Step 2: Write failing title bar tests** (`titlebar_test.go`): for each principal, run `TitlebarFor` on a request carrying `WithPrincipal` + shell (use `WithShell(probe, Shell{Brand: "Acme Jam", SignOut: true, AdminBadges: …})`) and assert:

```go
// member only  → tab keys [inbox]; Roles "member"; SignOut true
// admin only   → [dashboard projects agents users specs config health]; Roles "admin"
// both         → [inbox dashboard projects agents users specs config health]; Roles "member · admin"
// local admin  → admin tabs; Name "Local admin"; SignOut false
// current "agents" → only the agents tab has Current
// AdminBadges {"health": {N: 2, Red: true}} → health tab carries it
// rendered TitlebarTemplate for "member only" contains `href="/app/user/"` and not `/app/admin/`;
// for local admin contains "Local admin" and not `/app/auth/logout`;
// contains `src="/app/static/brand/j-logo-plain.svg"` and "Acme Jam".
```

- [ ] **Step 3: Run** — FAIL. **Step 4: Implement** `titlebar.go` (tab table, `TitlebarFor` reading `PrincipalFrom` and the shell; `Brand` falls back to `"Jam"`) and `titlebar.html`:

```html
{{define "app-titlebar"}}
<header class="app-titlebar">
  <a class="app-logo" href="/app/"><img src="/app/static/brand/j-logo-plain.svg" alt="" width="26" height="26"><span>{{.Brand}}</span></a>
  <nav class="app-tabs" aria-label="Jam">
    {{- range .Tabs}}
    <a href="{{.Href}}"{{if .Current}} aria-current="page"{{end}}>{{.Label}}{{if .Badge.N}} <span class="badge{{if .Badge.Red}} red{{end}}" title="{{.Badge.Title}}">{{.Badge.N}}</span>{{end}}</a>
    {{- end}}
  </nav>
  <details class="app-principal">
    <summary><span class="av">{{slice .Name 0 1}}</span><span class="nm">{{.Name}}</span></summary>
    <div class="menu">
      <b>{{.Name}}</b><small>{{.Roles}}</small>
      {{- if .SignOut}}<a href="/app/auth/logout">Sign out</a>{{end}}
    </div>
  </details>
</header>
{{end}}
```

(Guard `slice` against an empty name — use a template func `initial` instead if simpler.) Embed it with `//go:embed titlebar.html` into `TitlebarTemplate`. Add `.app-titlebar`, `.app-logo`, `.app-tabs`, `.app-principal` styles to `jam.css`, reusing its tokens (`--surface`, `--border`, `--accent`, `--accent-soft`, `--ink`, `--muted`); tabs scroll horizontally under 720px. Wrap the `/app/user/`, `/app/admin/` and landing handlers in `Handler` with `WithShell` — add `Shell Shell` to `appui.Deps`; cmd sets `Brand: cfg.displayName()`-derived brand, `SignOut: sessions != nil`, `AdminBadges` from Task 7.

- [ ] **Step 5: Run** `go test ./internal/jam/appui/ ./internal/jam/uiassets/ -v` — PASS. **Step 6: Commit** `feat(jam): app title bar with blended tabs and the J mark`.

### Task 7: adminui — render the title bar; rail only on Projects; Channel log under Health

**Files:**
- Modify: `internal/jam/adminui/nav.go`, `adminui.go`, `templates/layout.html`, `templates/health.html`, `projects.go` (projects list meta), `attention.go` (export badges)
- Test: `nav_test.go`, `polish_test.go`, `styles_test.go`, `health_test.go`, `intercom_test.go` and any test asserting the old topbar/rail/tabs

**Interfaces:**
- Consumes: `appui.TitlebarFor`, `appui.TitlebarTemplate`, `appui.Badge`.
- Produces: `func BadgeSource(store jam.Store, img jam.ImageResolver, conds *condition.Tracker) func(*http.Request) map[string]appui.Badge` — key per admin tab (`dashboard` = Jam-scope total, others = `inScope(items, "", key)`, `projects` = all project-scoped items).

- [ ] **Step 1: Write failing tests:**

```go
// GET /app/admin/agents (admin principal) → body contains `class="app-titlebar"`,
//   the Agents title-bar tab aria-current, NO `class="topbar"`, NO `id="rail"`.
// GET /app/admin/projects/p1 → contains `id="rail"` listing projects (no "◉ Jam" entry),
//   the project's own tabs, and the Projects title-bar tab current.
// GET /app/admin/projects → Projects tab current, rail present.
// GET /app/admin/health → page links "Channel log" (/app/admin/intercom).
// GET /app/admin/intercom → Health tab current; page title "Channel log".
// GET /app/admin/specs/... sub-tabs still render under Specs.
// Search box still present (slim bar) and posts to /app/admin/search.
// BadgeSource with one open Jam-scope condition → dashboard badge N=1.
```

- [ ] **Step 2: Run** — FAIL. **Step 3: Implement:**
  - `layout.html`: parse `appui.TitlebarTemplate` into every page set (in `mustParse`, `template.Must(t.Parse(appui.TitlebarTemplate))`); replace `<header class="topbar">…</header>` with `{{template "app-titlebar" titlebar}}` followed by a slim `<div class="subbar">` holding the rail toggle (only when `frame.Rail` is shown) and the search form. Render `{{template "rail" $f.Rail}}` only when the frame has a rail; the `.shell` grid becomes single-column when it doesn't.
  - `frameFuncs`: add `"titlebar": func() appui.Titlebar { return appui.TitlebarFor(r, f.TitleTab) }` — `renderStatus` must pass `r`; add `TitleTab string` to `frame` (the title-bar tab key: `meta.Tab` for Jam pages, `"projects"` for project pages and the projects list, `"health"` for intercom). Give the template-default FuncMap a stub `titlebar` returning `appui.Titlebar{}`.
  - `frameFor`: `scopeJam` pages get no `Tabs` row and no rail (the title bar carries the tabs); `scopeProject` pages keep `Title`, project `Tabs` and a rail of **projects only**; `buildRail` drops the Jam entry; `/app/admin/rail` keeps polling the projects list.
  - Remove `jamTabs`' role as the page tab row; keep its keys/labels only if `BadgeSource` needs them.
  - Intercom: page meta → `jamPage("health")`, title "Channel log"; add a "Channel log" link on `health.html`.
  - Projects list (`GET /app/admin/projects`): meta with the projects rail (`TitleTab "projects"`).
  - `BadgeSource` in `attention.go` converting `attnBadge` → `appui.Badge`.
  - Delete now-unused topbar CSS from `layout.html`.
- [ ] **Step 4: Run** `go test ./internal/jam/adminui/ -v`; update tests that asserted the old topbar, "Admin" brand, Jam rail entry, or in-page Jam tab row to the new structure (don't delete coverage — re-point it). PASS.
- [ ] **Step 5: Wire** `AdminBadges: adminui.BadgeSource(st, sup, conds)` into `appui.Shell` in `cmd/at-jam/main.go`. `go build ./...`.
- [ ] **Step 6: Docs** — `docs/usage/jam/ui.md` (rail/tabs section: title bar, blended tabs, rail only on Projects, Channel log under Health), `ui-pages.md`, `ui-projects.md` as they describe the frame. Commit `feat(jam): console renders the app title bar; rail only on Projects`.

### Task 8: userui — render the title bar; toggles into the rail header

**Files:**
- Modify: `internal/jam/userui/handler.go` (`mustParse`, render), `templates/layout.html`, `templates/inbox.html`
- Test: `internal/jam/userui/handler_test.go`, `styles_test.go`, `browser_test.go`

**Interfaces:**
- Consumes: `appui.TitlebarFor`, `appui.TitlebarTemplate`.

- [ ] **Step 1: Write failing tests:**

```go
// GET /app/user/ (member principal) → contains `class="app-titlebar"`, Inbox tab aria-current,
//   NO `class="topbar"`, the brand text from the shell (not "Jam Intercom").
// The conversation rail header contains the Rendered/Raw toggle and the "New message" control.
// A member+admin principal's page shows the Dashboard tab too.
```

- [ ] **Step 2: Run** — FAIL. **Step 3: Implement:** parse `appui.TitlebarTemplate` in `mustParse`; bind a per-request `titlebar` func (clone-and-Funcs as adminui does, or pass `Titlebar` in `inboxPage` as `.Bar` — prefer the field: `Bar appui.Titlebar` set from `appui.TitlebarFor(r, "inbox")` in `h.full`); in `layout.html` replace the `.topbar` header with `{{template "app-titlebar" .Bar}}`, move the Rendered/Raw toggle and the `newmsg` `<details>` into `.rail-head` in `inbox.html`; set `<title>` to the brand; drop the `.who` avatar (the principal menu replaces it); adjust `.panes` height for the new bar.
- [ ] **Step 4: Run** `go test ./internal/jam/userui/ -v` — PASS, then `just test && just lint` — PASS.
- [ ] **Step 5: Docs** — `docs/usage/jam/intercom-ui.md` layout section (title bar, toggles in the rail header). Run the docs-audit checker. Commit `feat(jam): inbox renders the app title bar`.

**Slice 3 wrap-up:** run the app (`just run` an at-jam with a loopback admin listener and `dev-identity`) and look at `/app/user/` and `/app/admin/` in a browser or with `curl` to sanity-check; push, open PR C. Merge when green and reviewed.
