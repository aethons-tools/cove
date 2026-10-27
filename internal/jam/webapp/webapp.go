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
	"html/template"
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
	// Head is trusted head markup (the app's manifest link, icons) put at
	// <!--jam:head-->; nil = none.
	Head func(*http.Request) template.HTML
	Name string // for the not-built page, e.g. "the Jam app"
}

// marker is where index.html takes the bootstrap (web/app/index.html).
const marker = "<!--jam:bootstrap-->"

// headMarker is where index.html takes Config.Head.
const headMarker = "<!--jam:head-->"

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
		var head []byte
		if c.Head != nil {
			head = []byte(c.Head(r))
		}
		page = bytes.Replace(page, []byte(headMarker), head, 1)
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
