# Jam app (slice 4): installable PWA — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `/app` browser app installable (home screen / dock, own window) with a manifest, a minimal service worker, and icons generated from the J mark.

**Architecture:** A pure-Go generator (`internal/jam/uiassets/icongen`, driven by `cmd/gen-icons` and `just icons`) renders `brand/j-logo-plain.svg` into committed PNGs under `uiassets/static/icons/`; a test regenerates and compares with a drift-fraction tolerance. `appui` serves `/app/manifest.webmanifest` (rendered per Jam) and `/app/sw.js` **outside** the gate, and owns a shared `app-head` template (manifest link, theme colours, icons, iOS meta, service-worker registration) that both `adminui` and `userui` include in `<head>`.

**Tech Stack:** Go 1.27, `github.com/srwiley/oksvg` + `github.com/srwiley/rasterx` (new deps, generator only), `html/template`, `encoding/json`.

**Spec:** `docs/superpowers/specs/2026-10-22-jam-app-pwa-design.md` §6.

## Global Constraints

- Manifest at `/app/manifest.webmanifest`, content type `application/manifest+json`: `name` "{display name} Jam" (the shell brand; "Jam" when unset), `short_name` "Jam", `scope` `/app/`, `start_url` `/app/`, `display` `standalone`, `theme_color` `#2F67B2`, `background_color` `#F5F6F8`, `icons` per the table below.
- Dark theme colour via `<meta name="theme-color" content="#0F1116" media="(prefers-color-scheme: dark)">` (light: `#2F67B2` with `media="(prefers-color-scheme: light)"`).
- Service worker at `/app/sw.js` (`text/javascript`, `Cache-Control: no-cache`): `install` → `skipWaiting()`, `activate` → `clients.claim()`, install/activate only, no `fetch` handler (so every request goes to the network), caches nothing. Registered with scope `/app/`.
- iOS: `<link rel="apple-touch-icon" href="/app/static/icons/apple-touch-icon.png?v=…">`, `<meta name="apple-mobile-web-app-capable" content="yes">`, `<meta name="apple-mobile-web-app-title" content="Jam">`.
- Manifest, `sw.js` and `/app/static/*` (icons included) are served **without** the gate; every other `/app/` path stays gated.
- Icons, rendered from `j-logo-plain.svg`, committed under `internal/jam/uiassets/static/icons/`:

  | File | Size | Background |
  |---|---|---|
  | `favicon-32.png` | 32 | transparent |
  | `icon-192.png`, `icon-512.png` | 192, 512 | transparent |
  | `icon-maskable-512.png` | 512 | white `#FFFFFF`, mark scaled into the centred 80% safe zone |
  | `apple-touch-icon.png` | 180 | white `#FFFFFF` |

  The transparent icons are rendered with the mark centred, scaled to fit with 4% padding on each side.
- Manifest icon entries: `icon-192.png` (`sizes` "192x192", `purpose` "any"), `icon-512.png` ("512x512", "any"), `icon-maskable-512.png` ("512x512", "maskable"); all `type` "image/png"; URLs carry `?v=<content hash>`.
- Generated PNGs are committed; a test regenerates and fails on drift: a pixel counts as differing when any 8-bit channel is off by more than 2, and the test passes only if at most 0.5% of the icon's pixels differ and no channel differs by more than 48. Antialiasing coverage differs slightly across CPU architectures (Go fuses multiply-add on arm64 but not amd64), so scattered edge-pixel differences must be tolerated while real drift (SVG edit, scale, background) still fails.
- No custom install prompt. Docs updated in the same PR. Hermetic TDD tests; `just test`, `just lint`.
- Commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Before go/just: `export GOPROXY=https://proxy.golang.org,direct`.

## Review Focus

- **Unauthenticated install fetch** — a browser fetching `/app/manifest.webmanifest`, `/app/sw.js` or an icon with no cookies off-loopback gets 200, not a login redirect. (Task 2 test.)
- **Display name with quotes/HTML/unicode** (e.g. `Acme "R&D" <Ops> Jäm`) → the manifest is valid JSON with the exact string; the head template escapes it. (Task 2 test.)
- **Cross-architecture regeneration** — CI (amd64) regenerating icons committed from arm64 must not fail on sub-LSB differences. (Task 1 tolerance test.)
- **Gate unaffected** — adding ungated `/app/manifest.webmanifest` and `/app/sw.js` patterns must not ungate anything else under `/app/` (e.g. `/app/sw.js.map`, `/app/manifest.webmanifest/x`). (Task 2 test.)
- **SW registration where unsupported/insecure** — on plain-HTTP non-localhost (no `navigator.serviceWorker`), the registration snippet is a no-op with no console error. (Task 2: guarded snippet, asserted in the template test.)

---

Branch: `jam-app-4-installable` from `main`.

### Task 1: icon generator and committed icons

**Files:**
- Create: `internal/jam/uiassets/icongen/icongen.go`, `internal/jam/uiassets/icongen/icongen_test.go`, `cmd/gen-icons/main.go`, `internal/jam/uiassets/static/icons/*.png` (generated)
- Modify: `internal/jam/uiassets/uiassets.go` (embed `static/icons/*.png`; add `IconHref`), `internal/jam/uiassets/uiassets_test.go`, `justfile` (`icons` recipe), `go.mod`/`go.sum`

**Interfaces:**
- Produces:

```go
package icongen
type Spec struct {
	Name       string      // file name, e.g. "icon-192.png"
	Size       int         // square edge in px
	Background color.Color // nil = transparent
	Scale      float64     // fraction of the edge the mark's box occupies (0.92 or 0.8)
}
var Specs = []Spec{ /* the five icons */ }
func Render(svg []byte, s Spec) (*image.RGBA, error)
func Encode(img image.Image) ([]byte, error) // png, BestCompression
```

```go
package uiassets
// IconHref is an icon's URL under prefix, versioned by its content hash.
func IconHref(prefix, name string) string // e.g. "/app/static/icons/icon-192.png?v=ab12cd34ef56"
```

- [ ] **Step 1: Add deps.** `go get github.com/srwiley/oksvg@latest github.com/srwiley/rasterx@latest && go mod tidy`. (A spike confirmed `oksvg.ReadIcon` renders `j-logo-plain.svg` — including its `<style>` class fills — correctly.)

- [ ] **Step 2: Write failing tests** `icongen_test.go`:

```go
func TestSpecs(t *testing.T) {
	want := map[string]int{"favicon-32.png": 32, "icon-192.png": 192, "icon-512.png": 512, "icon-maskable-512.png": 512, "apple-touch-icon.png": 180}
	// each Specs entry: Name in want with that Size; maskable and apple-touch have a white Background; others nil.
}

func TestRenderTransparentHasMarkColours(t *testing.T) {
	// Render(plainSVG, icon-512 spec): corner (0,0) alpha == 0; somewhere an opaque
	// pixel near orange #F7941D and one near blue #2F67B2 (within 8 per channel).
}

func TestRenderMaskableSafeZone(t *testing.T) {
	// Render maskable-512: corners are opaque white; every pixel outside the
	// centred 80% circle (radius 0.4*512 from centre) is white — the mark stays in the safe zone.
}

func TestCommittedIconsMatchGenerator(t *testing.T) {
	// For each Spec: Render, decode the committed ../static/icons/<Name>, same bounds,
	// and apply the drift rule (differing pixels > 0.5% of total, or maxDiff > 48, fails). On failure:
	// t.Fatalf("%s drifted: n of total (pct), maxDiff; run `just icons`", ...).
}
```

Load the SVG from `../static/brand/j-logo-plain.svg` via `os.ReadFile`.

- [ ] **Step 3: Run** `go test ./internal/jam/uiassets/icongen/` — FAIL.

- [ ] **Step 4: Implement** `icongen.go`:

```go
// Package icongen renders the Jam app's PNG icons from the J mark SVG — pure
// Go, so `just icons` needs no system tools. The output is committed under
// uiassets/static/icons; a test regenerates and checks for drift.
package icongen

var white = color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}

var Specs = []Spec{
	{Name: "favicon-32.png", Size: 32, Scale: 0.92},
	{Name: "icon-192.png", Size: 192, Scale: 0.92},
	{Name: "icon-512.png", Size: 512, Scale: 0.92},
	{Name: "icon-maskable-512.png", Size: 512, Background: white, Scale: 0.8 * 0.92},
	{Name: "apple-touch-icon.png", Size: 180, Background: white, Scale: 0.92},
}

func Render(svg []byte, s Spec) (*image.RGBA, error) {
	icon, err := oksvg.ReadIconStream(bytes.NewReader(svg), oksvg.StrictErrorMode)
	if err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, s.Size, s.Size))
	if s.Background != nil {
		draw.Draw(img, img.Bounds(), image.NewUniform(s.Background), image.Point{}, draw.Src)
	}
	box := float64(s.Size) * s.Scale
	off := (float64(s.Size) - box) / 2
	icon.SetTarget(off, off, box, box)
	icon.Draw(rasterx.NewDasher(s.Size, s.Size, rasterx.NewScannerGV(s.Size, s.Size, img, img.Bounds())), 1)
	return img, nil
}

func Encode(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

(If the maskable safe-zone test fails because the mark's viewBox box is wider than its circle, reduce the maskable `Scale` until it passes and record the value in the report.)

`cmd/gen-icons/main.go`: read `internal/jam/uiassets/static/brand/j-logo-plain.svg`, `Render`+`Encode` each `Spec`, write to `internal/jam/uiassets/static/icons/<Name>` (mkdir -p), print each path. Run from repo root. `justfile`:

```
# regenerate the Jam app's PNG icons from the J mark (internal/jam/uiassets/static/icons).
# Generated files ARE committed; a test fails when they drift from the SVG.
icons:
    go run ./cmd/gen-icons
```

- [ ] **Step 5: Generate** `just icons`; view `icon-512.png` and `icon-maskable-512.png` (Read tool) to eyeball them; run `go test ./internal/jam/uiassets/icongen/` — PASS.

- [ ] **Step 6: Serve and version icons.** Extend the embed to `static/icons/*.png`; add `IconHref` (sha256 of the file, first 6 bytes hex, like `StylesheetHref`). Test in `uiassets_test.go`: `GET <prefix>icons/icon-192.png` → 200 `image/png`; `IconHref("/app/static/", "icon-192.png")` matches `^/app/static/icons/icon-192\.png\?v=[0-9a-f]{12}$`; unknown icon name panics or returns a documented fallback — pick panic (programmer error) and test it.

- [ ] **Step 7:** `just test && just lint` — PASS. **Commit** `feat(jam): generate the Jam app icons from the J mark`.

### Task 2: manifest, service worker, shared head

**Files:**
- Create: `internal/jam/appui/pwa.go`, `internal/jam/appui/head.html`, `internal/jam/appui/sw.js`, `internal/jam/appui/pwa_test.go`
- Modify: `internal/jam/appui/mux.go`, `internal/jam/appui/titlebar.go` (embed alongside), `internal/jam/adminui/adminui.go` + `templates/layout.html`, `internal/jam/userui/handler.go` + `templates/layout.html`, tests in those packages, `docs/usage/jam/ui.md` (or the leaf that owns the app shell — check `docs/usage/jam/INDEX.md`)

**Interfaces:**
- Consumes: `uiassets.IconHref`, `appui.Shell` (`Brand`), `appui.TitlebarTemplate` parsing pattern in adminui/userui.
- Produces:

```go
const HeadTemplate string // {{define "app-head"}} over a Head
type Head struct{ Title, Favicon, AppleTouch string }
func HeadFor(r *http.Request) Head // Title = shell brand ("Jam" when unset); Favicon/AppleTouch = uiassets.IconHref("/app/static/", …)
func manifestHandler(brand string) http.Handler
func serviceWorker() http.Handler
```

- [ ] **Step 1: Write failing tests** (`pwa_test.go`, using `Handler(Deps{...})` with a gate built from fake Sessions as in `mux_test.go`, request off-loopback with **no cookies**):

```go
// GET /app/manifest.webmanifest → 200, Content-Type application/manifest+json;
//   json.Unmarshal ok; name "Acme \"R&D\" <Ops> Jäm Jam" when Shell.Brand is that brand,
//   short_name "Jam", scope "/app/", start_url "/app/", display "standalone",
//   theme_color "#2F67B2", background_color "#F5F6F8"; 3 icons with the sizes/purposes
//   from Global Constraints, each src == uiassets.IconHref("/app/static/", name).
// Shell.Brand "" → name "Jam".
// GET /app/sw.js → 200, Content-Type text/javascript (charset ok), Cache-Control no-cache;
//   body contains "skipWaiting", "clients.claim" and no "caches." and no "respondWith".
// GET /app/static/icons/icon-512.png (no cookies) → 200 image/png.
// GET /app/sw.js.map, /app/manifest.webmanifest/x, /app/anything (no cookies) → NOT 200
//   (302 to login or 404 — never served ungated content).
// POST /app/manifest.webmanifest → 405 or 404.
// Render HeadTemplate with HeadFor(r) for brand `<b>x</b>`: contains
//   `rel="manifest" href="/app/manifest.webmanifest"`, both theme-color metas with their media,
//   apple-touch-icon with ?v=, apple-mobile-web-app-capable, apple-mobile-web-app-title "Jam",
//   favicon-32 link, the registration snippet guarded by `'serviceWorker' in navigator`
//   with `{scope: '/app/'}`, and the brand escaped (no raw `<b>`).
```

Plus in adminui and userui tests: a full page's `<head>` contains `rel="manifest"` and the registration snippet exactly once.

- [ ] **Step 2: Run** — FAIL. **Step 3: Implement.**

`sw.js` (embedded):

```js
// Jam app service worker: present so the app is installable and so push
// (a later slice) has a home. It caches nothing and has no fetch handler,
// so every request goes straight to the network.
self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));
```

`head.html`:

```html
{{define "app-head"}}
<link rel="manifest" href="/app/manifest.webmanifest">
<meta name="theme-color" content="#2F67B2" media="(prefers-color-scheme: light)">
<meta name="theme-color" content="#0F1116" media="(prefers-color-scheme: dark)">
<link rel="icon" type="image/png" sizes="32x32" href="{{.Favicon}}">
<link rel="apple-touch-icon" href="{{.AppleTouch}}">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-title" content="Jam">
<script>if('serviceWorker' in navigator){navigator.serviceWorker.register('/app/sw.js',{scope:'/app/'}).catch(function(){});}</script>
{{end}}
```

(`Head` gains `Favicon`, `AppleTouch` fields filled from `uiassets.IconHref`.)

`pwa.go`: `manifestHandler` marshals a struct with `encoding/json` (never string-built) and sets `Content-Type: application/manifest+json`; `serviceWorker` serves the embedded file with `text/javascript; charset=utf-8` and `Cache-Control: no-cache`. In `mux.go` register, ungated:

```go
	mux.Handle("GET /app/manifest.webmanifest", manifestHandler(d.Shell.Brand))
	mux.Handle("GET /app/sw.js", serviceWorker())
```

(exact-match patterns — no trailing slash — so nothing else is ungated).

In adminui and userui: parse `appui.HeadTemplate` with the title bar template and add `{{template "app-head" apphead}}` (bound per request like `titlebar`, via `appui.HeadFor(r)`) inside `<head>` after the stylesheet link; userui may pass it as a page field like `Bar`. Remove any existing favicon links they had.

- [ ] **Step 4: Run** focused tests, `go test -tags browser ./internal/jam/userui/ -run Browser`, `just test`, `just lint` — PASS.
- [ ] **Step 5: Docs.** In the doc that owns the `/app` shell (`docs/usage/jam/ui.md`), add "Installing the app": works over HTTPS or localhost; use the browser's Install / Add to Home Screen / Add to Dock; what's cached (nothing — online only); the manifest/sw/icons are public; icons are regenerated with `just icons` (point at `docs/DEVELOPMENT.md` only if that's where recipes are documented). Update frontmatter `updated:`; keep each fact in one place; run the docs-audit checker (no new errors, files within budget).
- [ ] **Step 6: Commit** `feat(jam): installable Jam app — manifest, service worker, icons`.

**Wrap-up:** push `jam-app-4-installable`, open the PR; manual check note: Chrome on a loopback dev Jam shows "Install Jam" in the address bar and opens standalone.
