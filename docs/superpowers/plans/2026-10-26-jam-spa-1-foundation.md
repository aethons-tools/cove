# Jam SPA, slice 1: foundation — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a React + Mantine single-page app served at `/app/next/` that a member can open today. It shows the app shell (title bar, theme toggle, user menu) and their **read-only rail** from a new JSON API. It also sets up everything later slices build on: the build, embedding, CSP, types, tests and CI.

**Architecture:**
- `web/app/` (Vite) builds into `internal/jam/uiassets/app/`, which is embedded and git-ignored.
- A new `internal/jam/webapp` package serves that bundle. It injects a per-request bootstrap into `index.html` and sets a strict CSP.
- `userui` gains `/app/user/api/{bootstrap,rail}`, which serialize the existing rail view model, and a `Next` constructor that hands its bootstrap to `webapp`.
- `appui` mounts `/app/next/` behind the same gate as `/app/user/`.
- tygo generates `web/app/src/api/types.ts` from `userui/apitypes.go`.

**Tech Stack:**
- Go 1.26: `net/http`, `embed`, tygo v0.2.21 as a `go tool`.
- React 19.3, TypeScript 6.0, Vite 8, `@vitejs/plugin-react`, Mantine 9.7 (`@mantine/core`, `@mantine/hooks`), React Router 8 (`react-router`), TanStack Query 5, `@fontsource/ibm-plex-sans` and `@fontsource/ibm-plex-mono`.
- Testing: Vitest 5, jsdom, Testing Library, MSW 2, ESLint 10 with typescript-eslint and react-hooks.

**Spec:** [`docs/superpowers/specs/2026-10-26-jam-spa-design.md`](../specs/2026-10-26-jam-spa-design.md) (§1, §2 "Types"/"Errors", §3 slice 1, §4, §5).

## Global Constraints

- Mantine is **9.x**. The spec said v8, but v9 has been current since 2026-03; Task 7 corrects the spec. React must be ≥ 19.2.
- TypeScript is **6.0.x** (`~6.0.3`); typescript-eslint supports `<6.1`.
- Pin npm deps exactly (`npm install --save-exact`), commit `package-lock.json`, and install in scripts and CI with `npm ci`.
- Renovate needs **no** change: `renovate.json` stays untouched.
- Node 22 (CI's setup-node `node-version: 22`).
- The app is served at `/app/next/`; the JSON API lives at `/app/user/api/`; Vite `base` is `/app/next/`.
- No Node at runtime and no CDN: fonts and assets are bundled.
- A Go build without the bundle still compiles and runs, and `/app/next/` then answers with the "not built" page.
- The CSP on `index.html` is exactly:
  `default-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' blob: data:; font-src 'self'; style-src 'self' 'unsafe-inline'; frame-src <origins or 'none'>; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`
- API errors are `{"error": "...", "code": "..."}` with the real status code.
- JSON responses carry `Content-Type: application/json`, `X-Content-Type-Options: nosniff` and `Cache-Control: no-store`.
- The API encodes empty lists as `[]`, never `null`, because `types.ts` declares arrays as non-null.
- The shell route's initial JS must stay ≤ 350 KB gzipped.
- `dangerouslySetInnerHTML` is banned by ESLint. Slice 2 adds its one allowed site.
- Doc rules (AGENTS.md): docs ship in the same change; new docs carry frontmatter and are linked from an INDEX.

## Review Focus

Five cases the spec implies but its tests wouldn't naturally pin. Each line names the task whose test pins it.

1. **The sign-in expires while the app is open.** The gate then answers API calls with an HTML 403 or a redirect, not JSON. The app must show "Your sign-in has expired" with a sign-in link, not crash on a JSON parse error. (Task 5 `api.test.ts`.)
2. **A member with no channels yet.** The rail API must return `"groups": []`, not `null`, and the app must show an empty state. (Task 2 Go test, Task 6 component test.)
3. **Reloading a deep link** such as `/app/next/c/chn_x` must serve `index.html` with the bootstrap, not a 404. (Task 1.)
4. **A stale or missing asset** (`/app/next/assets/old-1234.js` after a deploy) must be a 404, not `index.html`, which would produce a confusing MIME-type error. `index.html` itself must be `no-store`, so a deploy is picked up immediately. (Task 1.)
5. **A brand or user name containing `</script>` or `<!--`** must not break out of the bootstrap block. (Task 1.)

---

## File structure

| File | Responsibility |
|---|---|
| `internal/jam/uiassets/app/.keep` (new) | Placeholder, so `go:embed all:app` compiles without a bundle. |
| `internal/jam/uiassets/uiassets.go` (modify) | Embed `all:app`; export `AppFS()` and `AppBuilt()`. |
| `internal/jam/webapp/webapp.go` (new) | Serve an SPA bundle: assets, index with bootstrap, CSP, "not built" page. Generic, so admin can reuse it later. |
| `internal/jam/webapp/webapp_test.go` (new) | Tests for the above. |
| `internal/jam/userui/apitypes.go` (new) | Every JSON-facing type; the only file tygo reads. `RailGroup`/`ChannelRow` move here from `userui.go`. |
| `internal/jam/userui/api.go` (new) | `writeJSON`, `writeAPIError`, the `api/bootstrap` and `api/rail` routes, `Next`. |
| `internal/jam/userui/api_test.go` (new) | API shape, headers and authorization tests. |
| `internal/jam/userui/handler.go` (modify) | Factor `newHandler`; register the API routes. |
| `internal/jam/appui/mux.go` (modify) | `Deps.Next`, mounted at `/app/next/` behind the user gate. |
| `cmd/at-jam/main.go` (modify) | Wire `userui.Next`. |
| `tygo.yaml`, `go.mod`/`go.sum` (new/modify) | The tygo tool and its config. |
| `web/app/**` (new) | The SPA. |
| `scripts/app.sh` (new) | Build the bundle (`npm ci`, `vite build`, size budget, atomic swap). |
| `justfile`, `scripts/build.sh`, `.gitignore`, `.goreleaser.yaml`, `.github/workflows/{gate,release}.yml` (modify) | Build and CI wiring. |
| `internal/jam/userui/next_browser_test.go` (new) | chromedp end-to-end test of `/app/next/`. |
| `docs/web-app.md` (new), `docs/INDEX.md`, `docs/usage/jam/intercom-ui.md`, the spec (modify) | Docs. |

---

### Task 1: Embed the app bundle and serve it (`uiassets` + `webapp`)

**Files:**
- Create: `internal/jam/uiassets/app/.keep`, `internal/jam/webapp/webapp.go`, `internal/jam/webapp/webapp_test.go`
- Modify: `internal/jam/uiassets/uiassets.go`, `.gitignore`

**Interfaces:**
- Produces:
  - `uiassets.AppFS() fs.FS`: the bundle root, holding `index.html` and `assets/…`.
  - `uiassets.AppBuilt() bool`: true when `index.html` is present.
  - `webapp.Config{Prefix string; FS fs.FS; Bootstrap func(*http.Request) (any, error); FrameSrc []string; Name string}`.
  - `webapp.Handler(c Config) http.Handler`.

- [ ] **Step 1: Add the placeholder and ignore rules.**

`internal/jam/uiassets/app/.keep`:
```
Placeholder: the Jam app bundle (web/app) is built here by `just app` (git-ignored).
```
Append to `.gitignore`, next to the whiteboard lines:
```
/internal/jam/uiassets/app/*
/internal/jam/uiassets/app.tmp/
/internal/jam/uiassets/app.old/
!/internal/jam/uiassets/app/.keep
```

- [ ] **Step 2: Write the failing webapp tests.**

`internal/jam/webapp/webapp_test.go`:
```go
package webapp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = `<!doctype html><html><head><!--jam:bootstrap--><script type="module" src="/app/next/assets/index-abc.js"></script></head><body><div id="root"></div></body></html>`

func bundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":         {Data: []byte(indexHTML)},
		"assets/index-abc.js": {Data: []byte("console.log(1)")},
		"assets/font.woff2":  {Data: []byte("woff")},
		".keep":              {Data: []byte("x")},
	}
}

func serve(t *testing.T, c Config, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func cfg() Config {
	return Config{Prefix: "/app/next/", FS: bundle(), Name: "the Jam app",
		Bootstrap: func(*http.Request) (any, error) { return map[string]string{"brand": "Acme Jam"}, nil }}
}

func TestIndexCarriesBootstrapAndCSP(t *testing.T) {
	for _, path := range []string{"/app/next/", "/app/next/c/chn_x", "/app/next/session/ses_1/thinking"} {
		rec := serve(t, cfg(), "GET", path)
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `<script id="jam-bootstrap" type="application/json">{"brand":"Acme Jam"}</script>`) {
			t.Errorf("GET %s: no bootstrap block:\n%s", path, body)
		}
		if strings.Contains(body, "<!--jam:bootstrap-->") {
			t.Errorf("GET %s: marker left in page", path)
		}
		h := rec.Header()
		if got, want := h.Get("Content-Security-Policy"), "default-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' blob: data:; font-src 'self'; style-src 'self' 'unsafe-inline'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"; got != want {
			t.Errorf("CSP = %q\nwant %q", got, want)
		}
		if got := h.Get("Cache-Control"); got != "no-store" {
			t.Errorf("index Cache-Control = %q, want no-store", got)
		}
		if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("index Content-Type = %q", got)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("missing nosniff/referrer headers: %v", h)
		}
	}
}

func TestFrameSrcListsOrigins(t *testing.T) {
	c := cfg()
	c.FrameSrc = []string{"https://a.preview.example", "https://*.preview.example:8081"}
	csp := serve(t, c, "GET", "/app/next/").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src https://a.preview.example https://*.preview.example:8081;") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestBootstrapCannotBreakOutOfScript(t *testing.T) {
	c := cfg()
	c.Bootstrap = func(*http.Request) (any, error) {
		return map[string]string{"brand": `</script><script>alert(1)</script><!--`}, nil
	}
	body := serve(t, c, "GET", "/app/next/").Body.String()
	if strings.Count(body, "</script>") != 2 { // the bootstrap's own and the module script's
		t.Errorf("bootstrap broke out of its script:\n%s", body)
	}
	if strings.Contains(body, "<!--<") || strings.Contains(body, "alert(1)</script>") {
		t.Errorf("unescaped markup in bootstrap:\n%s", body)
	}
}

func TestBootstrapErrorIs500(t *testing.T) {
	c := cfg()
	c.Bootstrap = func(*http.Request) (any, error) { return nil, errors.New("store down") }
	if rec := serve(t, c, "GET", "/app/next/"); rec.Code != 500 {
		t.Errorf("code = %d, want 500", rec.Code)
	}
}

func TestAssetsImmutableAndMissingAssets404(t *testing.T) {
	rec := serve(t, cfg(), "GET", "/app/next/assets/index-abc.js")
	if rec.Code != 200 || rec.Body.String() != "console.log(1)" {
		t.Fatalf("asset = %d %q", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q", got)
	}
	for _, path := range []string{"/app/next/assets/old-999.js", "/app/next/assets/", "/app/next/assets/.keep", "/app/next/.keep", "/app/next/index.html"} {
		if rec := serve(t, cfg(), "GET", path); path == "/app/next/index.html" {
			if rec.Code != 301 && rec.Code != 308 && rec.Code != 200 {
				t.Errorf("GET %s = %d", path, rec.Code)
			}
		} else if rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestOnlyGetAndHead(t *testing.T) {
	if rec := serve(t, cfg(), "POST", "/app/next/"); rec.Code != 405 {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
	if rec := serve(t, cfg(), "HEAD", "/app/next/"); rec.Code != 200 {
		t.Errorf("HEAD = %d, want 200", rec.Code)
	}
}

func TestNotBuiltPage(t *testing.T) {
	c := cfg()
	c.FS = fstest.MapFS{".keep": {Data: []byte("x")}}
	for _, path := range []string{"/app/next/", "/app/next/c/x"} {
		rec := serve(t, c, "GET", path)
		if rec.Code != 503 {
			t.Errorf("GET %s = %d, want 503", path, rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, "the Jam app") || !strings.Contains(body, "just app") {
			t.Errorf("not-built page = %q", body)
		}
	}
	if rec := serve(t, c, "GET", "/app/next/assets/x.js"); rec.Code != 404 {
		t.Errorf("asset without bundle = %d, want 404", rec.Code)
	}
}
```

Note: `/app/next/index.html` is allowed to answer 200 (index) or a redirect; the only requirement is that it isn't a raw file without the bootstrap. The implementation below serves it like any other non-asset path, which returns 200 with the bootstrap.

- [ ] **Step 3: Run the tests to verify they fail.**

Run: `go test ./internal/jam/webapp/`
Expected: FAIL to compile (`undefined: Handler`, `undefined: Config`).

- [ ] **Step 4: Implement `webapp`.**

`internal/jam/webapp/webapp.go`:
```go
// Package webapp serves a single-page app bundle built by Vite (web/app,
// embedded via uiassets): hashed files under assets/ as immutable, and every
// other path as index.html with a per-request bootstrap and a strict CSP, so
// the app's client-side routes deep-link. It knows nothing about who may see
// what: mount it behind the area's gate. See docs/web-app.md.
package webapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Config is one mounted app.
type Config struct {
	Prefix string // the mount path, with trailing slash, e.g. "/app/next/"
	FS     fs.FS  // the bundle root: index.html and assets/
	// Bootstrap is the request's JSON bootstrap, injected into index.html
	// as <script id="jam-bootstrap" type="application/json">.
	Bootstrap func(*http.Request) (any, error)
	// FrameSrc are the origins the app may frame (CSP frame-src); none = 'none'.
	FrameSrc []string
	Name     string // for the not-built page, e.g. "the Jam app"
}

// marker is where index.html takes the bootstrap (web/app/index.html).
const marker = "<!--jam:bootstrap-->"

// Handler serves c. Only GET and HEAD.
func Handler(c Config) http.Handler {
	index, _ := fs.ReadFile(c.FS, "index.html")
	csp := policy(c.FrameSrc)
	assets := http.FileServer(http.FS(c.FS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, c.Prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(rest, "assets/") || (rest != "" && strings.HasPrefix(path.Base(rest), ".")) {
			// Files only: no listings, no dotfiles, and never index.html for a
			// missing asset (a stale chunk must fail as a 404, not as HTML).
			if index == nil || strings.HasSuffix(rest, "/") || strings.HasPrefix(path.Base(rest), ".") {
				http.NotFound(w, r)
				return
			}
			if _, err := fs.Stat(c.FS, rest); err != nil {
				http.NotFound(w, r)
				return
			}
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/" + rest
			assets.ServeHTTP(w, r2)
			return
		}
		if index == nil {
			notBuilt(w, c.Name)
			return
		}
		boot, err := c.Bootstrap(r)
		if err != nil {
			http.Error(w, "could not load the app", http.StatusInternalServerError)
			return
		}
		js, err := json.Marshal(boot) // escapes <, >, & as <… — no breakout
		if err != nil {
			http.Error(w, "could not load the app", http.StatusInternalServerError)
			return
		}
		page := bytes.Replace(index, []byte(marker),
			[]byte(`<script id="jam-bootstrap" type="application/json">`+string(js)+`</script>`), 1)
		h.Set("Content-Security-Policy", csp)
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
}

func policy(frameSrc []string) string {
	frames := "'none'"
	if len(frameSrc) > 0 {
		frames = strings.Join(frameSrc, " ")
	}
	return "default-src 'self'; script-src 'self'; connect-src 'self'; img-src 'self' blob: data:; font-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; frame-src " + frames + "; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
}

func notBuilt(w http.ResponseWriter, name string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>Not built</title>`+
		`<p>This Jam binary was built without %s's bundle. Build it with <code>just app</code> (needs Node), then rebuild Jam.</p>`, html.EscapeString(name))
}
```

- [ ] **Step 5: Embed the bundle in `uiassets`.**

In `internal/jam/uiassets/uiassets.go`, after the `whiteboardFiles` embed, add:
```go
// appFiles is the Jam app bundle (web/app), built into app/ by `just app`
// (scripts/app.sh); only a placeholder is committed, so a plain `go build`
// has none and webapp serves its not-built page.
//
//go:embed all:app
var appFiles embed.FS

var app = func() fs.FS {
	sub, err := fs.Sub(appFiles, "app")
	if err != nil {
		panic(err)
	}
	return sub
}()

// AppFS is the Jam app bundle's root (index.html, assets/).
func AppFS() fs.FS { return app }

// AppBuilt reports whether this binary carries the Jam app bundle.
func AppBuilt() bool {
	_, err := fs.Stat(app, "index.html")
	return err == nil
}
```
Also add `AppBuilt` to the package comment's list of assets ("…and the Jam app bundle (web/app)").

- [ ] **Step 6: Run the tests to verify they pass.**

Run: `go test ./internal/jam/webapp/ ./internal/jam/uiassets/`
Expected: PASS. If the `index.html` case in `TestAssetsImmutableAndMissingAssets404` fails, check it reaches the index branch: `path.Base("index.html")` doesn't start with "." and isn't under `assets/`, so it should.

- [ ] **Step 7: Commit.**
```bash
git add .gitignore internal/jam/uiassets internal/jam/webapp
git commit -m "webapp: serve an embedded SPA bundle with bootstrap and CSP"
```

---

### Task 2: The JSON API foundation in `userui` (`api/bootstrap`, `api/rail`, `Next`)

**Files:**
- Create: `internal/jam/userui/apitypes.go`, `internal/jam/userui/api.go`, `internal/jam/userui/api_test.go`
- Modify: `internal/jam/userui/userui.go` (move `ChannelRow`/`RailGroup` out), `internal/jam/userui/handler.go`

**Interfaces:**
- Consumes: `webapp.Config`, `webapp.Handler`, `uiassets.AppFS`, `uiassets.WhiteboardBuilt` (Task 1); `appui.TitlebarFor(r, "inbox")`; `Rail(p, d, "")`; `unreadTotal(groups)`.
- Produces (JSON field names are the contract `types.ts` mirrors):
  - `type Bootstrap struct{ Brand string "brand"; User BootUser "user"; Roles string "roles"; Admin bool "admin"; SignOut bool "signOut"; Tabs []NavTab "tabs"; AttachMax int64 "attachMax"; PushKey string "pushKey"; Boards bool "boards" }`
  - `type BootUser struct{ ID, Name, Initial string }` → `"id"`, `"name"`, `"initial"`
  - `type NavTab struct{ Key, Label, Href string; Badge int; BadgeRed bool; BadgeTitle string }` → `"key"`, `"label"`, `"href"`, `"badge"`, `"badgeRed"`, `"badgeTitle"`
  - `type RailResponse struct{ Groups []RailGroup "groups"; UnreadTotal int "unreadTotal" }`
  - `type APIError struct{ Error, Code string }` → `"error"`, `"code"`
  - `RailGroup` → `"bucket"`, `"title"`, `"rows"`. `ChannelRow` → `"id"`, `"label"`, `"project"`, `"kind"`, `"needsYou"`, `"phase"`, `"unread"`, `"openAsks"`, `"lastSeq"`, `"selected"`, `"mission"`.
  - `func Next(d Deps, lg *slog.Logger, frameSrc []string, opts ...Option) http.Handler`, which serves `/app/next/`.
  - Helpers for later slices: `writeJSON(w, status int, v any)` and `writeAPIError(w, status int, code, msg string)`.

- [ ] **Step 1: Move the rail types and add the API types.**

Cut the `ChannelRow` and `RailGroup` declarations (with their doc comments) out of `internal/jam/userui/userui.go` (currently lines 51–72) and put them, with JSON tags, in the new `internal/jam/userui/apitypes.go`:
```go
package userui

import "github.com/aethons-tools/cove/internal/jam"

// This file holds every type the /app/user/api JSON carries. tygo reads only
// this file (tygo.yaml) to generate web/app/src/api/types.ts: keep JSON types
// here, give every field a json tag, and run `go generate ./internal/jam/userui/`
// after a change. Lists are never nil in a response (types.ts says T[]).

// APIError is every /app/user/api error body.
type APIError struct {
	Error string `json:"error"` // for people
	Code  string `json:"code"`  // for code: forbidden, not_found, bad_request, internal
}

// Bootstrap is what the app needs before its first render: injected into
// index.html by webapp, and served at GET /app/user/api/bootstrap (the dev
// server's index has none).
type Bootstrap struct {
	Brand     string   `json:"brand"`
	User      BootUser `json:"user"`
	Roles     string   `json:"roles"` // "member" or "member · admin"
	Admin     bool     `json:"admin"`
	SignOut   bool     `json:"signOut"` // offer Sign out (/app/auth/logout)
	Tabs      []NavTab `json:"tabs"`    // the title bar's links
	AttachMax int64    `json:"attachMax"` // per-file cap; 0 = attachments off
	PushKey   string   `json:"pushKey"`   // VAPID public key; "" = Web Push off
	Boards    bool     `json:"boards"`    // whiteboards on and the editor built
}

// BootUser is the signed-in member.
type BootUser struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Initial string `json:"initial"`
}

// NavTab is one title-bar link (appui.Tab, flattened).
type NavTab struct {
	Key        string `json:"key"`
	Label      string `json:"label"`
	Href       string `json:"href"`
	Badge      int    `json:"badge"`
	BadgeRed   bool   `json:"badgeRed"`
	BadgeTitle string `json:"badgeTitle"`
}

// RailResponse is GET /app/user/api/rail.
type RailResponse struct {
	Groups      []RailGroup `json:"groups"`
	UnreadTotal int         `json:"unreadTotal"`
}

// ChannelRow is one rail entry.
type ChannelRow struct {
	ID       string          `json:"id"`
	Label    string          `json:"label"`
	Project  string          `json:"project"`
	Kind     jam.ChannelKind `json:"kind"`
	NeedsYou bool            `json:"needsYou"`
	Phase    string          `json:"phase"`
	Unread   int             `json:"unread"`
	OpenAsks int             `json:"openAsks"`
	LastSeq  int64           `json:"lastSeq"`
	Selected bool            `json:"selected"`
	Mission  string          `json:"mission"` // the backing session's one-line focus; "" = none
}

// RailGroup is one attention section of the rail (Needs you / Sessions /
// Channels / History), non-empty.
type RailGroup struct {
	Bucket jam.AttentionBucket `json:"bucket"`
	Title  string              `json:"title"`
	Rows   []ChannelRow        `json:"rows"`
}
```
Keep the existing doc comments on `ChannelRow` and `RailGroup` exactly as they were. The ones above are placeholders for whatever `userui.go` had; copy the originals over them.

- [ ] **Step 2: Write the failing API tests.**

`internal/jam/userui/api_test.go`:
```go
package userui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

func apiGet(t *testing.T, h http.Handler, p *jam.Participant, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if p != nil {
		// As the gate does: the participant for userui, the principal for
		// the title bar (appui.TitlebarFor).
		req = appui.WithPrincipal(jam.WithParticipant(req, *p), appui.Principal{User: p})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func checkJSONHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if h.Get("Content-Type") != "application/json" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("JSON headers = %v", h)
	}
}

func TestAPIRailShowsOnlyTheViewersChannels(t *testing.T) {
	e, p := fixture()
	// bob writes to carol in a chat alice is not in: never in alice's rail.
	for _, n := range []string{"bob", "carol"} {
		if err := jam.AddPerson(e.st, "proj", jam.Human{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	carol, _ := e.st.LookupName(ident.User, "carol")
	pl, err := e.Intercom.PlanPersonChat(bob, carol)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "secret"}); err != nil {
		t.Fatal(err)
	}
	// bob to alice: in her rail, unread.
	pa, err := e.Intercom.PlanPersonChat(bob, p.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.Post(pa, intercom.Squawk{From: bob, Body: "ping"}); err != nil {
		t.Fatal(err)
	}

	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/rail")
	if rec.Code != 200 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var got RailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	labels := map[string]int{}
	for _, g := range got.Groups {
		for _, r := range g.Rows {
			labels[r.Label] = r.Unread
		}
	}
	if _, ok := labels["eng"]; !ok {
		t.Errorf("rail lacks eng: %s", rec.Body)
	}
	if labels["bob"] != 1 {
		t.Errorf("bob chat unread = %d, want 1: %s", labels["bob"], rec.Body)
	}
	if strings.Contains(rec.Body.String(), "carol") {
		t.Errorf("rail shows a chat alice is not in: %s", rec.Body)
	}
	if got.UnreadTotal != 1 {
		t.Errorf("unreadTotal = %d, want 1", got.UnreadTotal)
	}
}

func TestAPIRailEmptyIsAList(t *testing.T) {
	st := jam.NewMemStore()
	if err := st.CreateProject("proj"); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(st, "proj", jam.Human{Name: "dana"}); err != nil {
		t.Fatal(err)
	}
	dana, _ := st.LookupName(ident.User, "dana")
	lg := intercom.NewMemLog(nil)
	ic := jam.NewIntercom(st, func() (ident.ID, bool) { return "", false }, lg, nil, nil)
	p := jam.Participant{UserID: dana, Projects: []string{"proj"}, Name: "dana"}
	rec := apiGet(t, Handler(Deps{Store: st, Intercom: ic, Log: lg}, nil), &p, "/app/user/api/rail")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"groups":[]`) {
		t.Errorf("empty rail = %d %s, want groups:[]", rec.Code, rec.Body)
	}
}

func TestAPIWithoutParticipantIsJSON403(t *testing.T) {
	e, _ := fixture()
	for _, path := range []string{"/app/user/api/rail", "/app/user/api/bootstrap"} {
		rec := apiGet(t, Handler(e.Deps, nil), nil, path)
		if rec.Code != 403 {
			t.Errorf("%s code = %d, want 403", path, rec.Code)
		}
		checkJSONHeaders(t, rec)
		var body APIError
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Code != "forbidden" || body.Error == "" {
			t.Errorf("%s body = %s", path, rec.Body)
		}
	}
}

func TestAPIUnknownRouteIsJSON404(t *testing.T) {
	e, p := fixture()
	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/nope")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Errorf("unknown API route = %d %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
}

func TestAPIBootstrap(t *testing.T) {
	e, p := fixture()
	rec := apiGet(t, Handler(e.Deps, nil, WithPush("vapid-key")), &p, "/app/user/api/bootstrap")
	if rec.Code != 200 {
		t.Fatalf("code = %d: %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var b Bootstrap
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.User.ID != string(p.UserID) || b.User.Name != "alice" || b.User.Initial != "A" {
		t.Errorf("user = %+v", b.User)
	}
	if b.PushKey != "vapid-key" || b.Admin || b.Boards || b.AttachMax != 0 {
		t.Errorf("bootstrap = %+v", b)
	}
	if len(b.Tabs) != 1 || b.Tabs[0].Key != "inbox" || b.Tabs[0].Href != "/app/next/" {
		t.Errorf("tabs = %+v, want the Inbox pointing at /app/next/", b.Tabs)
	}
}

func TestNextServesTheAppWithBootstrap(t *testing.T) {
	e, p := fixture()
	h := Next(e.Deps, nil, nil)
	rec := apiGet(t, h, &p, "/app/next/c/"+engID)
	// Without a built bundle, 503 (webapp's not-built page); with one, the
	// index carrying alice's bootstrap. Either way, never a 404.
	switch rec.Code {
	case 503:
	case 200:
		if !strings.Contains(rec.Body.String(), `"name":"alice"`) {
			t.Errorf("index lacks alice's bootstrap: %s", rec.Body)
		}
	default:
		t.Errorf("GET /app/next/c/… = %d", rec.Code)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail.**

Run: `go test ./internal/jam/userui/ -run 'TestAPI|TestNext'`
Expected: FAIL to compile (`undefined: Next`, `undefined: RailResponse` …, or the moved types if Step 1 isn't done).

- [ ] **Step 4: Factor `newHandler` and register the API.**

In `internal/jam/userui/handler.go`, split the constructor so `Next` can share options:
```go
func newHandler(d Deps, lg *slog.Logger, opts ...Option) *handler {
	if lg == nil {
		lg = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	h := &handler{d: d, lg: lg, streams: map[ident.ID]int{}, mints: newMintLimiter(time.Now)}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func Handler(d Deps, lg *slog.Logger, opts ...Option) http.Handler {
	h := newHandler(d, lg, opts...)
	mux := http.NewServeMux()
	// … existing routes unchanged …
	h.pushRoutes(mux)
	h.apiRoutes(mux)
	return mux
}
```
Keep `Handler`'s existing doc comment.

- [ ] **Step 5: Implement `api.go`.**

`internal/jam/userui/api.go`:
```go
package userui

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
	"github.com/aethons-tools/cove/internal/jam/webapp"
)

// apiRoutes registers the JSON API the Jam app (web/app) reads: the same view
// models the templates render, behind the same participant checks
// (docs/superpowers/specs/2026-10-26-jam-spa-design.md §2).
func (h *handler) apiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/bootstrap", h.apiBootstrap)
	mux.HandleFunc("GET /app/user/api/rail", h.apiRail)
	mux.HandleFunc("/app/user/api/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such API route")
	})
}

// Next serves the Jam app at /app/next/ (mount it behind the user gate):
// webapp with this participant's bootstrap. frameSrc are the origins its
// panes may frame (none yet: the Studio slice passes the preview origins).
func Next(d Deps, lg *slog.Logger, frameSrc []string, opts ...Option) http.Handler {
	h := newHandler(d, lg, opts...)
	return webapp.Handler(webapp.Config{
		Prefix: "/app/next/", FS: uiassets.AppFS(), FrameSrc: frameSrc, Name: "the Jam app",
		Bootstrap: func(r *http.Request) (any, error) {
			b, ok := h.bootstrap(r)
			if !ok {
				return nil, errors.New("userui: no participant on the request (mount Next behind the user gate)")
			}
			return b, nil
		},
	})
}

func (h *handler) bootstrap(r *http.Request) (Bootstrap, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		return Bootstrap{}, false
	}
	tb := appui.TitlebarFor(r, "inbox")
	b := Bootstrap{
		Brand: tb.Brand, Roles: tb.Roles, SignOut: tb.SignOut, Tabs: []NavTab{},
		User:      BootUser{ID: string(p.UserID), Name: p.Name, Initial: initial(p.Name)},
		AttachMax: h.d.Attachments.MaxBytes(),
		PushKey:   h.pushKey,
		Boards:    h.d.Boards != nil && uiassets.WhiteboardBuilt(),
	}
	if pr, ok := appui.PrincipalFrom(r); ok {
		b.Admin = pr.Admin
	}
	for _, t := range tb.Tabs {
		href := t.Href
		if t.Key == "inbox" {
			href = "/app/next/" // the app's own Inbox while it lives here
		}
		b.Tabs = append(b.Tabs, NavTab{Key: t.Key, Label: t.Label, Href: href, Badge: t.Badge.N, BadgeRed: t.Badge.Red, BadgeTitle: t.Badge.Title})
	}
	return b, true
}

// initial is the avatar letter: the name's first rune, upper-cased; "?" when empty.
func initial(name string) string {
	if c, _ := utf8.DecodeRuneInString(name); c != utf8.RuneError {
		return strings.ToUpper(string(c))
	}
	return "?"
}

func (h *handler) apiBootstrap(w http.ResponseWriter, r *http.Request) {
	b, ok := h.bootstrap(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (h *handler) apiRail(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	groups := Rail(p, h.d, "")
	if groups == nil {
		groups = []RailGroup{}
	}
	writeJSON(w, http.StatusOK, RailResponse{Groups: groups, UnreadTotal: unreadTotal(groups)})
}

// writeJSON writes v as an API response (never cached, never sniffed).
func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError writes the API's error envelope.
func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, APIError{Error: msg, Code: code})
}
```
Each group's `Rows` is non-empty by construction (`groupRail` drops empty groups), so it never encodes `null`.

`appui.TitlebarFor` reads the `Shell` from the context, which only exists behind `appui.WithShell`. Without it (as in these unit tests), `Brand` falls back to "Jam" (`brandOr("")`) and `SignOut` is false. That's fine.

`TitlebarFor` reads the gate's `appui.PrincipalFrom(r)`, which is separate from `jam.ParticipantFrom`. That's why `apiGet` sets both, as `handler_test.go`'s title-bar tests do.

- [ ] **Step 6: Run the tests to verify they pass, then run the whole package.**

Run: `go test ./internal/jam/userui/`
Expected: PASS, including every existing test, since the moved types keep their names.

- [ ] **Step 7: Commit.**
```bash
git add internal/jam/userui
git commit -m "userui: JSON API foundation (bootstrap, rail) and Next for /app/next/"
```

---

### Task 3: Mount `/app/next/` behind the user gate

**Files:**
- Modify: `internal/jam/appui/mux.go`, `internal/jam/appui/mux_test.go`, `cmd/at-jam/main.go`

**Interfaces:**
- Consumes: `userui.Next(d, lg, frameSrc, opts...)` (Task 2).
- Produces: `appui.Deps.Next http.Handler`, which serves `/app/next/*` (nil = not mounted).

- [ ] **Step 1: Write the failing gate tests.**

In `internal/jam/appui/mux_test.go`, add `Next: echo("next"),` to `testMux`'s `Deps`, and add these cases to `TestAreasGated`'s table:
```go
		{"next app, member", "GET", "/app/next/c/x", "alice", "", "", 200, "next /app/next/c/x"},
		{"next app, admin only", "GET", "/app/next/", "stranger", "ok", "", 403, ""},
		{"next app, signed out", "GET", "/app/next/", "", "", "", 0, ""},
```
For the signed-out case, use whatever the `/app/user/` signed-out behaviour already is. First add a case `{"user area, signed out", "GET", "/app/user/", "", "", "", ?, ""}`, run it, read the actual code (a login redirect, 302 or 303), then set both rows to that code. The point is that `/app/next/` behaves exactly like `/app/user/`.

- [ ] **Step 2: Run the tests to verify they fail.**

Run: `go test ./internal/jam/appui/ -run TestAreasGated`
Expected: FAIL (`unknown field Next`).

- [ ] **Step 3: Implement.**

In `internal/jam/appui/mux.go`, add to `Deps` after `User`:
```go
	Next           http.Handler // serves /app/next/* (the Jam app, web/app); nil = none
```
and in `Handler`, after the `d.User` block:
```go
	if d.Next != nil {
		// The Jam app shares the user area's gate and its same-origin rule.
		mux.Handle("/app/next/", d.Gate.Wrap(RequireUser(browserauth.RequireSameOrigin(d.TrustedOrigins, WithShell(d.Next, d.Shell)))))
	}
```

In `cmd/at-jam/main.go`, next to the `userMux.Handle("/app/user/", userui.Handler(…))` line, build the app with the same options:
```go
		userOpts := []userui.Option{userui.WithChanges(logChanges), userui.WithPresence(sessPresence), userui.WithPush(pushPublic), userui.WithPushFocus(pushFocus)}
		userMux.Handle("/app/user/", userui.Handler(userDeps, log, userOpts...))
		// The Jam app (web/app) at /app/next/ while it is ported to parity
		// (docs/superpowers/specs/2026-10-26-jam-spa-design.md); no framing yet.
		nextApp := userui.Next(userDeps, log, nil, userOpts...)
```
and pass `Next: nextApp,` in the `appui.Deps{…}` literal.

- [ ] **Step 4: Run the tests and build.**

Run: `go test ./internal/jam/appui/ && go build ./cmd/at-jam/`
Expected: PASS, and the build succeeds.

- [ ] **Step 5: Commit.**
```bash
git add internal/jam/appui cmd/at-jam/main.go
git commit -m "appui: mount the Jam app at /app/next/ behind the user gate"
```

---

### Task 4: Generate TypeScript types with tygo

**Files:**
- Create: `tygo.yaml`, `web/app/src/api/types.ts` (generated)
- Modify: `go.mod`, `go.sum`, `internal/jam/userui/api.go` (the `go:generate` line)

**Interfaces:**
- Consumes: `internal/jam/userui/apitypes.go` (Task 2).
- Produces: `web/app/src/api/types.ts`, exporting `APIError`, `Bootstrap`, `BootUser`, `NavTab`, `RailResponse`, `ChannelRow`, `RailGroup` (field names per Task 2).

- [ ] **Step 1: Add the tool.**

Run (with GOPROXY set as in docs/DEVELOPMENT.md): `go get -tool github.com/gzuidhof/tygo@v0.2.21 && go mod tidy`
Expected: `go.mod` gains `tool github.com/gzuidhof/tygo`.

- [ ] **Step 2: Configure it.**

`tygo.yaml` (repo root):
```yaml
# Generates the Jam app's API types from Go (docs/web-app.md). Only
# apitypes.go is read; jam's string kinds map to string.
packages:
  - path: github.com/aethons-tools/cove/internal/jam/userui
    include_files: [apitypes.go]
    output_path: web/app/src/api/types.ts
    type_mappings:
      jam.ChannelKind: string
      jam.AttentionBucket: string
```
Add the generate directive at the top of `internal/jam/userui/api.go`, below the package clause:
```go
//go:generate sh -c "cd ../../.. && go tool tygo generate"
```

- [ ] **Step 3: Generate and check.**

Run: `go generate ./internal/jam/userui/ && cat web/app/src/api/types.ts`
Expected: a "Code generated by tygo. DO NOT EDIT." header, then `export interface Bootstrap { brand: string; … tabs: NavTab[]; … }`, `export interface RailGroup { bucket: string; title: string; rows: ChannelRow[]; }`, and so on. No `import` lines: if tygo emits `import … from "../jam"`, a `type_mappings` entry is missing, so add it and regenerate.

- [ ] **Step 4: Add the staleness check script.**

`scripts/app-types-check.sh`:
```bash
#!/usr/bin/env bash
# app-types-check.sh — fail when web/app/src/api/types.ts is stale (CI gate).
set -euo pipefail
cd "$(dirname "$0")/.."
go generate ./internal/jam/userui/
if ! git diff --exit-code -- web/app/src/api/types.ts; then
  echo "web/app/src/api/types.ts is stale: run 'go generate ./internal/jam/userui/' and commit it" >&2
  exit 1
fi
```
Run: `chmod +x scripts/app-types-check.sh && ./scripts/app-types-check.sh`
Expected: exit 0 once `types.ts` is committed (commit first, then run).

- [ ] **Step 5: Commit.**
```bash
git add tygo.yaml go.mod go.sum internal/jam/userui/api.go web/app/src/api/types.ts scripts/app-types-check.sh
git commit -m "tygo: generate the Jam app's API types from userui/apitypes.go"
```

---

### Task 5: Scaffold `web/app` (Vite, theme, API client, build script, size budget)

**Files:**
- Create: `web/app/package.json`, `web/app/package-lock.json` (by npm), `web/app/index.html`, `web/app/vite.config.ts`, `web/app/vitest.config.ts`, `web/app/tsconfig.json`, `web/app/eslint.config.js`, `web/app/src/main.tsx`, `web/app/src/theme.ts`, `web/app/src/api/client.ts`, `web/app/src/api/client.test.ts`, `web/app/src/bootstrap.ts`, `web/app/src/bootstrap.test.ts`, `web/app/src/test/setup.ts`, `web/app/src/test/server.ts`, `web/app/postcss.config.cjs`, `web/app/scripts/size-budget.mjs`, `scripts/app.sh`

**Interfaces:**
- Consumes: `types.ts` (Task 4); the `<!--jam:bootstrap-->` marker and `<script id="jam-bootstrap">` contract (Task 1).
- Produces:
  - `class ApiError extends Error { status: number; code: string }`
  - `apiGet<T>(path: string): Promise<T>`, where `path` is relative to `/app/user/api/`, e.g. `"rail"`.
  - `loadBootstrap(): Promise<Bootstrap>`, which reads the injected block and falls back to `apiGet("bootstrap")`.
  - `theme: MantineThemeOverride`.
  - npm scripts `dev`, `build`, `test`, `lint`, `typecheck`, `size`, `check`.

- [ ] **Step 1: Create `package.json` and install.**

`web/app/package.json`:
```json
{
  "name": "jam-app",
  "private": true,
  "description": "The Jam app (React + Mantine), served at /app/next/ and embedded by at-jam. Built by `just app` into internal/jam/uiassets/app/ (git-ignored). See docs/web-app.md.",
  "license": "UNLICENSED",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "test": "vitest run",
    "lint": "eslint .",
    "typecheck": "tsc --noEmit",
    "size": "node scripts/size-budget.mjs",
    "check": "npm run typecheck && npm run lint && npm run test"
  }
}
```
Then, in `web/app/`, run:
```bash
npm install --save-exact react@19.3.1 react-dom@19.3.1 @mantine/core@9.7.4 @mantine/hooks@9.7.4 react-router@8.5.0 @tanstack/react-query@5.105.0 @fontsource/ibm-plex-sans @fontsource/ibm-plex-mono
npm install --save-exact --save-dev typescript@6.0.3 vite@8.3.5 @vitejs/plugin-react vitest@5.0.5 jsdom @testing-library/react @testing-library/dom @testing-library/jest-dom @testing-library/user-event msw eslint typescript-eslint eslint-plugin-react-hooks @types/react @types/react-dom @types/node postcss postcss-preset-mantine postcss-simple-vars
```
Expected: `package-lock.json` is created. If a peer-dependency conflict appears, prefer the newest versions that satisfy every peer (keep Mantine 9 and TypeScript 6.0) and record any deviation in the commit message.

- [ ] **Step 2: Write the configs.**

`web/app/index.html`:
```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Jam</title>
    <!--jam:bootstrap-->
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

`web/app/vite.config.ts`:
```ts
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dev server (`just app-dev`) proxies the API, auth and shared static
// routes to a running `at-jam serve` (JAM_URL; dev/jam.dev.yml's admin-listen),
// rewriting Origin so RequireSameOrigin admits the proxied writes.
const jam = process.env.JAM_URL ?? "http://localhost:8081";
const toJam = {
  target: jam,
  changeOrigin: true,
  configure: (proxy: { on: (ev: string, fn: (req: { setHeader: (k: string, v: string) => void }) => void) => void }) =>
    proxy.on("proxyReq", (req) => req.setHeader("origin", jam)),
};

export default defineConfig({
  base: "/app/next/",
  plugins: [react()],
  build: {
    outDir: process.env.APP_OUT_DIR ?? "dist",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    proxy: { "/app/user": toJam, "/app/auth": toJam, "/app/static": toJam },
  },
});
```

`web/app/vitest.config.ts`:
```ts
import { defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config";

export default mergeConfig(viteConfig, defineConfig({
  test: { environment: "jsdom", setupFiles: ["./src/test/setup.ts"], css: false },
}));
```

`web/app/postcss.config.cjs`:
```js
module.exports = {
  plugins: {
    "postcss-preset-mantine": {},
    "postcss-simple-vars": {
      variables: { "mantine-breakpoint-xs": "36em", "mantine-breakpoint-sm": "48em", "mantine-breakpoint-md": "62em", "mantine-breakpoint-lg": "75em", "mantine-breakpoint-xl": "88em" },
    },
  },
};
```

`web/app/tsconfig.json`:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noEmit": true,
    "isolatedModules": true,
    "skipLibCheck": true,
    "types": ["vite/client", "@testing-library/jest-dom"]
  },
  "include": ["src", "vite.config.ts", "vitest.config.ts"]
}
```

`web/app/eslint.config.js`:
```js
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  { ignores: ["dist/**", "src/api/types.ts"] },
  ...tseslint.configs.recommended,
  {
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // Message HTML comes only from squawkrender (spec §4): the one allowed
      // site carries an eslint-disable comment saying so.
      "no-restricted-syntax": ["error", {
        selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
        message: "Only the squawkrender message body may set inner HTML (docs/web-app.md).",
      }],
    },
  },
);
```

- [ ] **Step 3: Write the failing client and bootstrap tests.**

`web/app/src/test/setup.ts`:
```ts
import "@testing-library/jest-dom/vitest";
import { afterAll, afterEach, beforeAll } from "vitest";
import { cleanup } from "@testing-library/react";
import { server } from "./server";

// Mantine reads these; jsdom has neither.
window.matchMedia ??= ((query: string) => ({
  matches: false, media: query, onchange: null,
  addListener: () => {}, removeListener: () => {},
  addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false,
})) as typeof window.matchMedia;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };

beforeAll(() => server.listen({ onUnhandledRequest: "error" }));
afterEach(() => { server.resetHandlers(); cleanup(); document.getElementById("jam-bootstrap")?.remove(); });
afterAll(() => server.close());
```

`web/app/src/test/server.ts`:
```ts
import { setupServer } from "msw/node";
export const server = setupServer();
```

`web/app/src/api/client.test.ts`:
```ts
import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "../test/server";
import { ApiError, apiGet } from "./client";

describe("apiGet", () => {
  it("returns the JSON body", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })));
    await expect(apiGet("rail")).resolves.toEqual({ groups: [], unreadTotal: 0 });
  });

  it("throws the API's error envelope", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ error: "no", code: "forbidden" }, { status: 403 })));
    await expect(apiGet("rail")).rejects.toMatchObject({ status: 403, code: "forbidden", message: "no" });
  });

  it("treats an HTML 403 from the gate as signed out", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("<html>sign in</html>", { status: 403, headers: { "Content-Type": "text/html" } })));
    await expect(apiGet("rail")).rejects.toMatchObject({ code: "signed_out" });
  });

  it("treats a non-JSON 502 as unavailable, not signed out", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("bad gateway", { status: 502, headers: { "Content-Type": "text/plain" } })));
    const err = await apiGet("rail").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe("unavailable");
  });
});
```

`web/app/src/bootstrap.test.ts`:
```ts
import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { server } from "./test/server";
import { loadBootstrap } from "./bootstrap";

const boot = { brand: "Acme Jam", user: { id: "usr_1", name: "alice", initial: "A" }, roles: "member", admin: false,
  signOut: true, tabs: [], attachMax: 0, pushKey: "", boards: false };

describe("loadBootstrap", () => {
  it("reads the injected block without a request", async () => {
    const el = document.createElement("script");
    el.id = "jam-bootstrap"; el.type = "application/json"; el.textContent = JSON.stringify(boot);
    document.head.append(el);
    await expect(loadBootstrap()).resolves.toEqual(boot); // MSW errors on any unhandled request
  });

  it("falls back to the API (the dev server's index has none)", async () => {
    server.use(http.get("/app/user/api/bootstrap", () => HttpResponse.json(boot)));
    await expect(loadBootstrap()).resolves.toEqual(boot);
  });
});
```

- [ ] **Step 4: Run them to verify they fail.**

Run: `cd web/app && npx vitest run`
Expected: FAIL (`Cannot find module './client'` / `'./bootstrap'`).

- [ ] **Step 5: Implement the client, bootstrap and theme.**

`web/app/src/api/client.ts`:
```ts
// The /app/user/api client. Every error is an ApiError; a non-JSON refusal
// (the gate's HTML 403 or a login redirect) means the sign-in has expired.
export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string) {
    super(message);
    this.name = "ApiError";
  }
}

export const API = "/app/user/api/";

export async function apiGet<T>(path: string): Promise<T> {
  let res: Response;
  try {
    res = await fetch(API + path, { headers: { Accept: "application/json" }, credentials: "same-origin", redirect: "manual" });
  } catch {
    throw new ApiError(0, "unavailable", "Jam can't be reached. Check your connection.");
  }
  const json = (res.headers.get("Content-Type") ?? "").startsWith("application/json");
  if (!json) {
    if (res.type === "opaqueredirect" || res.status === 401 || res.status === 403 || (res.status >= 300 && res.status < 400)) {
      throw new ApiError(res.status, "signed_out", "Your sign-in has expired.");
    }
    throw new ApiError(res.status, "unavailable", `Jam answered ${res.status}. Try again shortly.`);
  }
  const body: unknown = await res.json();
  if (!res.ok) {
    const e = body as { error?: string; code?: string };
    throw new ApiError(res.status, e.code ?? "error", e.error ?? res.statusText);
  }
  return body as T;
}
```

`web/app/src/bootstrap.ts`:
```ts
import { apiGet } from "./api/client";
import type { Bootstrap } from "./api/types";

// loadBootstrap reads the bootstrap webapp injected into index.html, or (the
// Vite dev server's index has none) fetches it.
export async function loadBootstrap(): Promise<Bootstrap> {
  const el = document.getElementById("jam-bootstrap");
  if (el?.textContent) return JSON.parse(el.textContent) as Bootstrap;
  return apiGet<Bootstrap>("bootstrap");
}
```

`web/app/src/theme.ts`:
```ts
import { createTheme, type MantineColorsTuple } from "@mantine/core";

// jam.css's accent (#B5306A) as a Mantine palette, index 6 = the accent.
const jam: MantineColorsTuple = ["#F9EEF3", "#F0D6E1", "#E5B7CB", "#DA98B4", "#CD729A", "#C04F80", "#B5306A", "#9F2A5D", "#8A2451", "#741F44"];

export const theme = createTheme({
  primaryColor: "jam",
  colors: { jam },
  fontFamily: '"IBM Plex Sans", ui-sans-serif, system-ui, sans-serif',
  fontFamilyMonospace: '"IBM Plex Mono", ui-monospace, Menlo, monospace',
  fontSizes: { xs: "12px", sm: "13px", md: "14px", lg: "16px", xl: "18px" },
  defaultRadius: "md",
});
```

`web/app/src/main.tsx` (the shell arrives in Task 6; this renders a placeholder):
```tsx
import "@mantine/core/styles.css";
import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MantineProvider } from "@mantine/core";
import { theme } from "./theme";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <MantineProvider theme={theme} defaultColorScheme="auto">
      <p>Jam</p>
    </MantineProvider>
  </StrictMode>,
);
```

- [ ] **Step 6: Run the tests, typecheck and lint.**

Run: `cd web/app && npm run check`
Expected: PASS (6 tests), with no tsc or ESLint errors.

- [ ] **Step 7: The size budget and build script.**

`web/app/scripts/size-budget.mjs`:
```js
// Fails when the shell route's initial JS (the entry module plus its
// modulepreloads, as index.html loads them) exceeds the budget gzipped
// (docs/superpowers/specs/2026-10-26-jam-spa-design.md §5). Lazy chunks don't count.
import { readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";

const dir = process.argv[2] ?? "dist";
const budget = 350 * 1024;
const html = readFileSync(join(dir, "index.html"), "utf8");
const srcs = [...html.matchAll(/<(?:script[^>]+src|link[^>]+rel="modulepreload"[^>]+href)="\/app\/next\/([^"]+\.js)"/g)].map((m) => m[1]);
if (srcs.length === 0) throw new Error("size-budget: no scripts found in index.html");
let total = 0;
for (const s of new Set(srcs)) total += gzipSync(readFileSync(join(dir, s))).length;
console.log(`size-budget: initial JS ${(total / 1024).toFixed(1)} KB gzipped (budget ${budget / 1024} KB)`);
if (total > budget) {
  console.error("size-budget: over budget — lazy-load the new code (React.lazy) or drop a dependency");
  process.exit(1);
}
```
The regex covers both attribute orders Vite emits (`<script type="module" crossorigin src=…>` and `<link rel="modulepreload" crossorigin href=…>`). Check it against the built `index.html` in Step 8; if the attribute order differs, adjust the regex so it matches.

`scripts/app.sh`:
```bash
#!/usr/bin/env bash
# app.sh — build the Jam app bundle (web/app) that at-jam embeds, into
# internal/jam/uiassets/app/ (git-ignored). Needs Node and npm with access to
# the npm registry; the lockfile pins every package. Without the bundle at-jam
# still builds: /app/next/ answers "not built". See docs/web-app.md.
#
#   scripts/app.sh                  # build (npm ci + vite build + size budget)
#   APP_OPTIONAL=1 scripts/app.sh   # skip, with a warning, when npm is missing
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root/web/app"

if ! command -v npm >/dev/null 2>&1; then
  if [ "${APP_OPTIONAL:-}" = 1 ]; then
    echo "app: npm not found; skipping the Jam app bundle (at-jam builds without it)" >&2
    exit 0
  fi
  echo "app: npm is required (install Node 22), or set APP_OPTIONAL=1" >&2
  exit 1
fi
out="$root/internal/jam/uiassets/app"
# Build into a scratch directory and swap it in only on success, so a failed
# build never leaves the embedded directory empty (go:embed needs .keep).
rm -rf "$out.tmp" "$out.old"
npm ci --no-audit --no-fund --loglevel=error
APP_OUT_DIR="$out.tmp" npx vite build --logLevel warn
node scripts/size-budget.mjs "$out.tmp"
cp "$out/.keep" "$out.tmp/.keep"
mv "$out" "$out.old"
mv "$out.tmp" "$out"
rm -rf "$out.old"
echo "app: bundle built into internal/jam/uiassets/app/"
```

- [ ] **Step 8: Build and check the output.**

Run: `chmod +x scripts/app.sh && ./scripts/app.sh && ls internal/jam/uiassets/app internal/jam/uiassets/app/assets && git status --short internal/jam/uiassets`
Expected: `index.html`, `assets/*.js|css|woff2` and `.keep` exist; size-budget prints a figure under 350 KB; git status shows nothing under `uiassets/app` (ignored). `grep -c 'jam:bootstrap' internal/jam/uiassets/app/index.html` prints `1`, so Vite kept the marker comment. If it doesn't, Vite stripped it: switch the marker to `<meta name="jam-bootstrap">` in both `index.html` and `webapp.marker`, and update Task 1's test fixture to match.

Then run `go test ./internal/jam/userui/ -run TestNext`. It now takes the 200 branch.

- [ ] **Step 9: Commit.**
```bash
git add web/app scripts/app.sh
git commit -m "web/app: Vite + React + Mantine scaffold, API client, build script and size budget"
```

---

### Task 6: The app shell and the read-only rail

**Files:**
- Create: `web/app/src/App.tsx`, `web/app/src/shell/Shell.tsx`, `web/app/src/shell/TitleBar.tsx`, `web/app/src/rail/Rail.tsx`, `web/app/src/rail/useRail.ts`, `web/app/src/routes/Home.tsx`, `web/app/src/routes/Conversation.tsx`, `web/app/src/SignedOut.tsx`, `web/app/src/test/render.tsx`, `web/app/src/App.test.tsx`
- Modify: `web/app/src/main.tsx`

**Interfaces:**
- Consumes: `apiGet`, `ApiError`, `loadBootstrap`, `theme`, `Bootstrap`, `RailResponse` (Tasks 4–5).
- Produces:
  - `<App boot={Bootstrap} />`, with routes `/app/next/` (Home) and `/app/next/c/:channel` (Conversation placeholder).
  - `useRail(): UseQueryResult<RailResponse, ApiError>`, with query key `["rail"]`. Slice 2 invalidates this key on the `changed` event.
  - `renderApp(boot, path)` test helper.

- [ ] **Step 1: Write the failing component tests.**

`web/app/src/test/render.tsx`:
```tsx
import { render } from "@testing-library/react";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryRouter, RouterProvider } from "react-router";
import type { Bootstrap } from "../api/types";
import { theme } from "../theme";
import { routes } from "../App";

export const boot: Bootstrap = {
  brand: "Acme Jam", user: { id: "usr_1", name: "alice", initial: "A" }, roles: "member", admin: false, signOut: true,
  tabs: [{ key: "inbox", label: "Inbox", href: "/app/next/", badge: 0, badgeRed: false, badgeTitle: "" }],
  attachMax: 0, pushKey: "", boards: false,
};

export function renderApp(b: Bootstrap, path = "/app/next/") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const router = createMemoryRouter(routes(b), { initialEntries: [path] });
  return render(
    <MantineProvider theme={theme}>
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </MantineProvider>,
  );
}
```

`web/app/src/App.test.tsx`:
```tsx
import { describe, expect, it } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "./test/server";
import { boot, renderApp } from "./test/render";
import type { RailResponse } from "./api/types";

const rail: RailResponse = {
  unreadTotal: 2,
  groups: [
    { bucket: "needs-you", title: "Needs you", rows: [{ id: "chn_a", label: "intercom-spider", project: "test", kind: "session", needsYou: true, phase: "waiting", unread: 2, openAsks: 1, lastSeq: 9, selected: false, mission: "SPA slice 1" }] },
    { bucket: "channels", title: "Channels", rows: [{ id: "chn_b", label: "eng", project: "test", kind: "room", needsYou: false, phase: "", unread: 0, openAsks: 0, lastSeq: 3, selected: false, mission: "" }] },
  ],
};

describe("the app shell", () => {
  it("shows the brand, the user and the rail's groups and unread", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    renderApp(boot);
    expect(screen.getByText("Acme Jam")).toBeInTheDocument();
    expect(await screen.findByText("intercom-spider")).toBeInTheDocument();
    expect(screen.getByText("Needs you")).toBeInTheDocument();
    expect(screen.getByText("eng")).toBeInTheDocument();
    expect(screen.getByText("SPA slice 1")).toBeInTheDocument();
    expect(screen.getByLabelText("2 unread")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /eng/ })).toHaveAttribute("href", "/app/next/c/chn_b");
  });

  it("shows an empty state for a member with no channels", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json({ groups: [], unreadTotal: 0 })));
    renderApp(boot);
    expect(await screen.findByText(/No conversations yet/)).toBeInTheDocument();
  });

  it("asks to sign in again when the sign-in has expired", async () => {
    server.use(http.get("/app/user/api/rail", () => new HttpResponse("<html/>", { status: 403, headers: { "Content-Type": "text/html" } })));
    renderApp(boot);
    expect(await screen.findByText("Your sign-in has expired.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/app/auth/login");
  });

  it("deep-links a conversation to the current Inbox until slice 2", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    renderApp(boot, "/app/next/c/chn_b");
    expect(await screen.findByRole("link", { name: /Open in the current Inbox/ })).toHaveAttribute("href", "/app/user/?c=chn_b");
  });

  it("offers admin tabs and sign out from the bootstrap", async () => {
    server.use(http.get("/app/user/api/rail", () => HttpResponse.json(rail)));
    renderApp({ ...boot, admin: true, roles: "member · admin", tabs: [...boot.tabs, { key: "projects", label: "Projects", href: "/app/admin/projects", badge: 3, badgeRed: true, badgeTitle: "3 need attention" }] });
    expect(screen.getByRole("link", { name: /Projects/ })).toHaveAttribute("href", "/app/admin/projects");
    expect(screen.getByLabelText("3 need attention")).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run them to verify they fail.**

Run: `cd web/app && npx vitest run src/App.test.tsx`
Expected: FAIL (`Cannot find module './App'`).

- [ ] **Step 3: Implement the shell.**

`web/app/src/App.tsx`:
```tsx
import type { RouteObject } from "react-router";
import type { Bootstrap } from "./api/types";
import { Shell } from "./shell/Shell";
import { Home } from "./routes/Home";
import { Conversation } from "./routes/Conversation";

// routes are the app's client routes under /app/next/ (webapp serves
// index.html for every one of them).
export function routes(boot: Bootstrap): RouteObject[] {
  return [{
    path: "/app/next/",
    element: <Shell boot={boot} />,
    children: [
      { index: true, element: <Home /> },
      { path: "c/:channel", element: <Conversation /> },
    ],
  }];
}
```

`web/app/src/shell/Shell.tsx`:
```tsx
import { AppShell } from "@mantine/core";
import { Outlet } from "react-router";
import type { Bootstrap } from "../api/types";
import { TitleBar } from "./TitleBar";
import { Rail } from "../rail/Rail";

export function Shell({ boot }: { boot: Bootstrap }) {
  return (
    <AppShell header={{ height: 48 }} navbar={{ width: 300, breakpoint: "sm" }} padding="md">
      <AppShell.Header><TitleBar boot={boot} /></AppShell.Header>
      <AppShell.Navbar><Rail /></AppShell.Navbar>
      <AppShell.Main><Outlet /></AppShell.Main>
    </AppShell>
  );
}
```

`web/app/src/shell/TitleBar.tsx`:
```tsx
import { ActionIcon, Anchor, Avatar, Badge, Group, Menu, Text, useMantineColorScheme } from "@mantine/core";
import type { Bootstrap } from "../api/types";

// TitleBar mirrors the admin's app-titlebar: brand, tabs, principal menu.
export function TitleBar({ boot }: { boot: Bootstrap }) {
  const { colorScheme, setColorScheme } = useMantineColorScheme();
  const next = colorScheme === "dark" ? "light" : colorScheme === "light" ? "auto" : "dark";
  return (
    <Group h="100%" px="md" justify="space-between" wrap="nowrap">
      <Group gap="lg" wrap="nowrap">
        <Text fw={600}>{boot.brand}</Text>
        <Group gap="xs" component="nav" wrap="nowrap">
          {boot.tabs.map((t) => (
            <Anchor key={t.key} href={t.href} size="sm" underline="never">
              {t.label}
              {t.badge > 0 && (
                <Badge ml={6} size="sm" color={t.badgeRed ? "red" : "yellow"} aria-label={t.badgeTitle || `${t.badge}`}>{t.badge}</Badge>
              )}
            </Anchor>
          ))}
        </Group>
      </Group>
      <Group gap="sm" wrap="nowrap">
        <ActionIcon variant="subtle" aria-label={`Theme: ${colorScheme} (switch to ${next})`} onClick={() => setColorScheme(next)}>
          {colorScheme === "dark" ? "☾" : colorScheme === "light" ? "☀" : "◐"}
        </ActionIcon>
        <Menu position="bottom-end">
          <Menu.Target>
            <ActionIcon variant="subtle" radius="xl" aria-label="Account"><Avatar size="sm" radius="xl">{boot.user.initial}</Avatar></ActionIcon>
          </Menu.Target>
          <Menu.Dropdown>
            <Menu.Label>{boot.user.name} · {boot.roles}</Menu.Label>
            <Menu.Item component="a" href="/app/user/">Classic Inbox</Menu.Item>
            {boot.signOut && <Menu.Item component="a" href="/app/auth/logout">Sign out</Menu.Item>}
          </Menu.Dropdown>
        </Menu>
      </Group>
    </Group>
  );
}
```

`web/app/src/rail/useRail.ts`:
```ts
import { useQuery } from "@tanstack/react-query";
import { apiGet, type ApiError } from "../api/client";
import type { RailResponse } from "../api/types";

// useRail is the rail (query key ["rail"]: the live stream invalidates it from slice 2).
export const useRail = () => useQuery<RailResponse, ApiError>({ queryKey: ["rail"], queryFn: () => apiGet<RailResponse>("rail") });
```

`web/app/src/SignedOut.tsx`:
```tsx
import { Alert, Anchor } from "@mantine/core";
import type { ApiError } from "./api/client";

// Problem shows an API failure; an expired sign-in gets a sign-in link.
export function Problem({ error }: { error: ApiError }) {
  return (
    <Alert color={error.code === "signed_out" ? "yellow" : "red"} m="sm">
      {error.message}{" "}
      {error.code === "signed_out" && <Anchor href="/app/auth/login">Sign in</Anchor>}
    </Alert>
  );
}
```

`web/app/src/rail/Rail.tsx`:
```tsx
import { Badge, Loader, NavLink, ScrollArea, Stack, Text } from "@mantine/core";
import { NavLink as RouterLink, useParams } from "react-router";
import { useRail } from "./useRail";
import { Problem } from "../SignedOut";

export function Rail() {
  const { data, error, isPending } = useRail();
  const { channel } = useParams();
  if (isPending) return <Loader m="md" size="sm" />;
  if (error) return <Problem error={error} />;
  if (data.groups.length === 0) return <Text c="dimmed" size="sm" m="md">No conversations yet. When someone messages you, it shows here.</Text>;
  return (
    <ScrollArea>
      <Stack gap="xs" p="xs">
        {data.groups.map((g) => (
          <div key={g.bucket}>
            <Text size="xs" fw={700} c="dimmed" tt="uppercase" px="sm" py={4}>{g.title}</Text>
            {g.rows.map((r) => (
              <NavLink
                key={r.id}
                component={RouterLink}
                to={`/app/next/c/${encodeURIComponent(r.id)}`}
                active={r.id === channel}
                label={r.label}
                description={r.mission || undefined}
                rightSection={r.unread > 0 ? <Badge size="sm" circle aria-label={`${r.unread} unread`}>{r.unread}</Badge> : null}
              />
            ))}
          </div>
        ))}
      </Stack>
    </ScrollArea>
  );
}
```

`web/app/src/routes/Home.tsx`:
```tsx
import { Text } from "@mantine/core";
export const Home = () => <Text c="dimmed">Pick a conversation.</Text>;
```

`web/app/src/routes/Conversation.tsx`:
```tsx
import { Anchor, Text } from "@mantine/core";
import { useParams } from "react-router";

// Conversation is a placeholder until slice 2 ports the conversation pane.
export function Conversation() {
  const { channel = "" } = useParams();
  return (
    <Text>
      This preview doesn't show conversations yet.{" "}
      <Anchor href={`/app/user/?c=${encodeURIComponent(channel)}`}>Open in the current Inbox</Anchor>
    </Text>
  );
}
```

`web/app/src/main.tsx`: replace the placeholder render with:
```tsx
import "@mantine/core/styles.css";
import "@fontsource/ibm-plex-sans/400.css";
import "@fontsource/ibm-plex-sans/600.css";
import "@fontsource/ibm-plex-mono/400.css";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { MantineProvider } from "@mantine/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter, RouterProvider } from "react-router";
import { theme } from "./theme";
import { routes } from "./App";
import { loadBootstrap } from "./bootstrap";

const client = new QueryClient({ defaultOptions: { queries: { staleTime: 10_000 } } });
const root = createRoot(document.getElementById("root")!);

loadBootstrap().then(
  (boot) => root.render(
    <StrictMode>
      <MantineProvider theme={theme} defaultColorScheme="auto">
        <QueryClientProvider client={client}>
          <RouterProvider router={createBrowserRouter(routes(boot))} />
        </QueryClientProvider>
      </MantineProvider>
    </StrictMode>,
  ),
  () => { location.href = "/app/auth/login"; },
);
```
The `encodeURIComponent` on channel ids is harmless today (`chn_…` ids are URL-safe) and keeps `legacy:` History ids working.

- [ ] **Step 4: Run the checks.**

Run: `cd web/app && npm run check && cd ../.. && ./scripts/app.sh`
Expected: all tests pass (11), lint and typecheck are clean, and the build stays under budget.

- [ ] **Step 5: Commit.**
```bash
git add web/app
git commit -m "web/app: the app shell (title bar, theme toggle, menu) and the read-only rail"
```

---

### Task 7: Build and CI wiring, the browser test, and docs

**Files:**
- Create: `internal/jam/userui/next_browser_test.go`, `docs/web-app.md`
- Modify: `justfile`, `scripts/build.sh`, `.goreleaser.yaml`, `.github/workflows/gate.yml`, `.github/workflows/release.yml`, `docs/INDEX.md`, `docs/usage/jam/intercom-ui.md`, `docs/superpowers/specs/2026-10-26-jam-spa-design.md`

**Interfaces:**
- Consumes: everything above.
- Produces: `just app`, `just app-dev`, `just app-check`; the CI gate steps.

- [ ] **Step 1: Write the browser test.**

`internal/jam/userui/next_browser_test.go`:
```go
//go:build browser

package userui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

// The Jam app loads under its CSP, shows the member's rail from the API, and
// deep-links on reload (spec slice 1).
func TestBrowserNextAppShowsRail(t *testing.T) {
	if !uiassets.AppBuilt() {
		t.Skip("the Jam app bundle isn't built (just app)")
	}
	ctx := browserCtx(t)
	e, p := fixture()
	api, app := Handler(e.Deps, nil), Next(e.Deps, nil, nil)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = appui.WithPrincipal(jam.WithParticipant(r, p), appui.Principal{User: &p})
		if strings.HasPrefix(r.URL.Path, "/app/next/") {
			app.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	var violations []string
	chromedp.ListenTarget(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventConsoleAPICalled); ok && e.Type == runtime.APITypeError {
			for _, a := range e.Args {
				violations = append(violations, string(a.Value))
			}
		}
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			violations = append(violations, e.ExceptionDetails.Text)
		}
	})
	var label string
	err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL+"/app/next/c/"+engID),
		chromedp.WaitVisible(`a[href="/app/next/c/`+engID+`"]`, chromedp.ByQuery),
		chromedp.Text(`a[href="/app/next/c/`+engID+`"]`, &label, chromedp.ByQuery),
		chromedp.WaitVisible(`a[href="/app/user/?c=`+engID+`"]`, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(label, "eng") {
		t.Errorf("rail link = %q, want eng", label)
	}
	for _, v := range violations {
		if strings.Contains(v, "Content Security Policy") || strings.Contains(v, "Refused") {
			t.Errorf("CSP violation: %s", v)
		}
	}
	if len(violations) > 0 {
		t.Logf("console errors: %v", violations)
	}
}
```
Run: `./scripts/app.sh && go test -tags browser ./internal/jam/userui/ -run TestBrowserNextAppShowsRail -v`
Expected: PASS, or SKIP when no headless browser is installed. Say which in the PR description.

- [ ] **Step 2: Add the `just` recipes and include the app in `just build`.**

In `justfile`, after the `whiteboard` recipe:
```make
# build the Jam app bundle at-jam embeds at /app/next/ (needs Node/npm; see
# scripts/app.sh and docs/web-app.md). `just build` runs it when npm is available.
app:
    ./scripts/app.sh

# the Jam app's dev server (hot reload) proxying the API to a running
# `at-jam serve` (JAM_URL, default http://localhost:8081); open http://localhost:5173/app/next/
app-dev:
    cd web/app && npm ci --no-audit --no-fund --loglevel=error && npx vite

# the Jam app's checks: API types current, typecheck, lint, unit tests
app-check:
    ./scripts/app-types-check.sh
    cd web/app && npm ci --no-audit --no-fund --loglevel=error && npm run check
```
In `scripts/build.sh`, after the `WHITEBOARD_OPTIONAL=1 ./scripts/whiteboard.sh` line, add the same pattern:
```bash
# Build the Jam app bundle at-jam embeds (skipped, with a warning, when npm is
# missing: /app/next/ then answers "not built").
APP_OPTIONAL=1 ./scripts/app.sh
```

- [ ] **Step 3: CI and release.**

`.github/workflows/gate.yml`: change the setup-node `cache-dependency-path` to cover both lockfiles:
```yaml
          cache-dependency-path: |
            web/whiteboard/package-lock.json
            web/app/package-lock.json
```
After the `whiteboard bundle` step, add:
```yaml
      # The Jam app (web/app): API types current, typecheck, lint, unit
      # tests, then the bundle (with its size budget) so the Go tests below
      # serve the real app.
      - name: app checks
        run: ./scripts/app-types-check.sh && (cd web/app && npm ci --no-audit --no-fund && npm run check)
      - name: app bundle
        run: ./scripts/app.sh
```
`.github/workflows/release.yml`:
- Add the same two-lockfile `cache-dependency-path` to the setup-node step at line ~244.
- In the paths filter that lists `'scripts/whiteboard.sh'` and `'web/whiteboard/**'`, add:
```yaml
              - 'scripts/app.sh'
              - 'web/app/**' # the Jam app bundle at-jam embeds
```
`.goreleaser.yaml`: after `- bash scripts/whiteboard.sh`, add:
```yaml
    # Build the Jam app bundle at-jam embeds (git-ignored output). Required
    # here: a release ships the app.
    - bash scripts/app.sh
```

- [ ] **Step 4: Docs.**

Create `docs/web-app.md`:
```markdown
---
summary: How the Jam app (web/app — React + TypeScript + Vite + Mantine) is built, embedded, served at /app/next/, typed from Go, and tested; the dev loop.
read_when: You are changing the Jam app in web/app, adding a /app/user/api route it reads, or building/testing it locally or in CI.
owns: the web/app source layout, `just app` / `app-dev` / `app-check`, scripts/app.sh and the size budget, the webapp serving package (bootstrap, CSP, not-built page), the tygo type generation, and the app's test layers
prereqs: usage/jam/intercom-ui.md for what the user area does; superpowers/specs/2026-10-26-jam-spa-design.md for why
tier: leaf
updated: 2026-10-26
---

# The Jam app (`web/app`)

The Jam app is the React single-page app replacing the server-rendered `/app/user` inbox
([design](superpowers/specs/2026-10-26-jam-spa-design.md)).
Until the cutover it is served at **`/app/next/`** beside the current inbox, behind the same sign-in.

## Layout

- `web/app/`: Vite + React 19 + TypeScript 6 + Mantine 9, React Router, TanStack Query.
  - `src/api/client.ts` is the `/app/user/api/` client. Every failure is an `ApiError`, and a non-JSON refusal means `signed_out`.
  - `src/api/types.ts` is **generated**; don't edit it (see Types).
  - `src/theme.ts` is the Mantine theme (jam.css's accent and fonts). The colour scheme is light, dark or auto.
  - `src/App.tsx` holds the client routes under `/app/next/`.
- `internal/jam/webapp`: serves a built bundle.
  - Files under `assets/` are served immutable, and a missing one is a 404.
  - Every other path gets `index.html`, with the request's bootstrap replacing `<!--jam:bootstrap-->` and a strict CSP.
  - Without a bundle it answers 503 "not built".
- `internal/jam/userui/api.go` holds the JSON routes. `userui.Next` mounts the app with the member's bootstrap.

## Build

- `just app` (`scripts/app.sh`) runs `npm ci`, `vite build` and the size budget, then swaps the result into `internal/jam/uiassets/app/`. That directory is git-ignored and embedded by at-jam. `just build` runs it when npm is present (`APP_OPTIONAL=1`).
- The size budget: the shell route's initial JS (entry plus modulepreloads) must stay ≤ 350 KB gzipped. Lazy-load anything big with `React.lazy`.
- CI (`gate.yml`) runs the types check, `npm run check` and `scripts/app.sh` before `go test`. Releases build the bundle as a goreleaser hook.

## Dev loop

Run `just dev-watch` (or `dev-serve`), then `just app-dev`, and open `http://localhost:5173/app/next/`.
- Vite hot-reloads the app and proxies `/app/user`, `/app/auth` and `/app/static` to `JAM_URL` (default `http://localhost:8081`, `dev/jam.dev.yml`'s admin-listen).
- The proxy rewrites `Origin`, so writes pass the same-origin check.
- With the dev identity on, loopback requests are already signed in.

## Types

Go is the source of truth. JSON types live in `internal/jam/userui/apitypes.go`, the only file tygo reads (`tygo.yaml`).
After changing them, run `go generate ./internal/jam/userui/` and commit `types.ts`. `scripts/app-types-check.sh` (CI) fails when it's stale.
Rules:
- every field has a `json` tag;
- lists are never `nil` in a response;
- a `jam` string kind needs a `type_mappings` entry.

## Adding an API route

1. Add its response type to `apitypes.go`, then regenerate.
2. Register `GET|POST /app/user/api/…` in `apiRoutes`. Resolve the participant with `jam.ParticipantFrom`, reuse the HTML route's checks, and reply with `writeJSON` / `writeAPIError(status, code, msg)`.
3. Test its shape **and who may not see it** in `api_test.go`.
4. Read it in the app with `useQuery` and `apiGet`.

## Tests

- **Go:** `api_test.go` and `webapp_test.go` (plain `go test`).
- **App:** `npm run check` in `web/app` (typecheck, ESLint, Vitest with Testing Library and MSW). Mock the API with MSW handlers typed from `types.ts`.
- **Browser:** `just test-browser` includes `TestBrowserNextAppShowsRail`. It needs the bundle (`just app`) and a headless Chrome, and skips without either.

## Security

- The app page's CSP allows only same-origin scripts. `style-src` permits inline styles for Mantine.
- `dangerouslySetInnerHTML` is banned by ESLint except for the squawkrender message body.
- The bootstrap is a JSON data block (`json.Marshal` escapes `<`), never script.
```
In `docs/INDEX.md`, add a row after `DEVELOPMENT.md`:
```markdown
| [web-app.md](web-app.md) | You are changing the Jam app (`web/app`, served at `/app/next/`), adding an `/app/user/api` route it reads, or building/testing it. |
```
In `docs/usage/jam/intercom-ui.md`, add a short section near the top (after the intro paragraph):
```markdown
## Preview: the Jam app at `/app/next/`

A new single-page version of this inbox is being built at **`/app/next/`**, behind the same sign-in
([design](../../superpowers/specs/2026-10-26-jam-spa-design.md), [dev doc](../../web-app.md)).
So far it shows the title bar and the rail; opening a conversation links back here. Its JSON API lives
under `/app/user/api/`. This page stays the inbox until the cutover.
```
Bump that doc's `updated:` to 2026-10-26 if it isn't already.
In the spec, change "**Mantine v8**" to "**Mantine v9**" in the Decisions "Stack" bullet and in §1's "Source" line, adding the note "(v9 has been current since 2026-03; corrected in the slice 1 plan)". Change "Status: … not built." to "Status: approved (#496); slice 1 in progress."

- [ ] **Step 5: Run the docs audit and the whole suite.**

Run the docs-audit skill's checker on `docs/`, then: `go test ./... && just app-check && ./scripts/app.sh && just lint`
Expected: all green, with no orphan docs or dangling links.

- [ ] **Step 6: Commit and open the PR.**
```bash
git add justfile scripts/build.sh .goreleaser.yaml .github/workflows internal/jam/userui/next_browser_test.go docs
git commit -m "Jam app: build/CI wiring, browser test, and docs (web-app.md)"
```
Open one PR for slice 1 against `main` titled "Jam app slice 1: foundation at /app/next/". Ask the user to try `/app/next/` on their local Jam once it's merged.

---

## Self-review notes

- **Spec coverage (slice 1):**
  - §1 source, build, serving, bootstrap, dev loop and theme: Tasks 1, 5, 6, 7.
  - §2 API, types and errors: Tasks 2, 4, 5. Live updates are slice 2.
  - §4 gate, CSP, JSON headers and the ESLint ban: Tasks 1, 2, 3, 5.
  - §5 Go, component and browser tests, CI gates (types, size, no-bundle) and docs: Tasks 1, 2, 4, 5, 6, 7.
  - Renovate is untouched, per the spec.
- **Deferred to later slices on purpose:**
  - the SSE event hook (slice 2);
  - `frame-src` origins (slice 6, through `Next`'s `frameSrc`);
  - the PWA and service worker (slice 8).
