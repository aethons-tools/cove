// Package adminui is Jam's read-only, server-rendered observability UI. It
// reads state directly from a jam.Store and renders embedded html/template
// pages, with htmx polling for the live cove view. It exposes an unauthenticated
// http.Handler; the operator-auth gate is applied by jam.NewAdminHandler,
// which mounts this handler inside its existing middleware.
package adminui

import (
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

//go:embed templates/*.html static/htmx.min.js
var files embed.FS

// staticFS scopes the static route to the static/ subtree only — templates
// live under templates/ and must never be reachable via /ui/static/.
var staticFS = func() fs.FS {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	return sub
}()

// page holds one parsed template set (layout + that page's content). Each set's
// full page is rendered via ExecuteTemplate(w, "layout", data).
var pages = map[string]*template.Template{
	"dashboard":    mustParse("coves.html", "dashboard.html"),
	"coves":        mustParse("coves.html"),
	"roster":       mustParse("roster.html"),
	"roles":        mustParse("roles.html"),
	"kits":         mustParse("kits.html"),
	"destinations": mustParse("dest_fields.html", "destinations.html"),
	"intercom":     mustParse("intercom.html"),
	"role":         mustParse("coves.html", "role.html"),
	"destination":  mustParse("dest_fields.html", "destination.html"),
}

// roleRow is one project/role pair flattened for the roles table.
type roleRow struct {
	Project      string
	Name         string
	Destinations []string
	Credentials  map[string]string // destination → credential name (a reference, not a secret)
	TTL          time.Duration
	Kit          string
}

func roleRows(store jam.Store) []roleRow {
	var out []roleRow
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			out = append(out, roleRow{
				Project: p, Name: r.Name,
				Destinations: r.Scope.Destinations,
				Credentials:  r.Scope.Credentials,
				TTL:          r.Scope.TTL, Kit: r.Kit,
			})
		}
	}
	return out
}

func mustParse(names ...string) *template.Template {
	paths := make([]string, 0, len(names)+1)
	paths = append(paths, "templates/layout.html")
	for _, n := range names {
		paths = append(paths, "templates/"+n)
	}
	return template.Must(template.New("").Funcs(funcs).ParseFS(files, paths...))
}

// funcs are the template helpers shared by every page.
var funcs = template.FuncMap{
	// ttl renders a role TTL compactly, or "—" when unset.
	"ttl": func(d time.Duration) string {
		if d <= 0 {
			return "—"
		}
		return fmtDur(d)
	},
	"roleURL": roleURL,
	"destURL": destURL,
}

// fmtDur renders a duration without trailing zero units: "1h", "1h30m", "45s".
func fmtDur(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// Option configures Handler.
type Option func(*options)

type options struct {
	trustedOrigins []string
}

// WithTrustedOrigins adds exact origins (scheme://host[:port]) the CSRF write
// check accepts besides the request's own Host — e.g. a dev live-reload proxy
// that fronts the admin listener on another port (the serve config's
// ui-origins).
func WithTrustedOrigins(origins ...string) Option {
	return func(o *options) { o.trustedOrigins = append(o.trustedOrigins, origins...) }
}

// Handler returns the UI mux (no auth wrap). store is the primary dependency:
// every read view is an in-process read. log records write-action outcomes
// (never secret values — see writes.go).
func Handler(store jam.Store, log *slog.Logger, sup *jam.Supervisor, alloc jam.SessionAllocator, credExists func(string) bool, msgs SquawkReader, opts ...Option) http.Handler {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	mux := http.NewServeMux()
	canEdit := sup != nil

	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(staticFS))))

	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
		render(w, "dashboard", map[string]any{
			"Title": "Dashboard",
			"Coves": jam.CoveSummaries(store),
			"Stats": dashboardStats(store),
		})
	})

	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": "Studios", "Coves": jam.CoveSummaries(store), "CanEdit": canEdit}
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "coves", "coves-table", data)
			return
		}
		render(w, "coves", data)
	})

	mux.HandleFunc("GET /ui/roster", func(w http.ResponseWriter, r *http.Request) {
		render(w, "roster", map[string]any{"Title": "Roster", "Actors": jam.RosterSummaries(store)})
	})
	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
		render(w, "roles", map[string]any{"Title": "Roles", "Roles": roleRows(store), "CanRequest": canEdit})
	})
	mux.HandleFunc("GET /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		handleRoleDetail(w, r, store, canEdit)
	})
	mux.HandleFunc("GET /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		render(w, "kits", map[string]any{"Title": "Kits", "Kits": store.ListKits()})
	})
	mux.HandleFunc("GET /ui/intercom", func(w http.ResponseWriter, r *http.Request) {
		handleIntercom(w, r, msgs)
	})

	guardWrite := originGuard(o.trustedOrigins)
	registerWrites(mux, store, log, sup, credExists, guardWrite)
	registerRoleRequest(mux, store, log, sup, alloc, guardWrite)
	registerDestinations(mux, store, log, credExists, guardWrite)
	registerRoleEdits(mux, store, log, credExists, canEdit, guardWrite)

	return mux
}

// render executes the named page's "layout" template.
func render(w http.ResponseWriter, page string, data any) {
	renderStatus(w, http.StatusOK, page, data)
}

// renderStatus is render with an explicit status code.
func renderStatus(w http.ResponseWriter, status int, page string, data any) {
	t, ok := pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// renderFragment executes a single named template (e.g. an htmx-swapped table)
// without the page chrome.
func renderFragment(w http.ResponseWriter, page, tmpl string, data any) {
	t, ok := pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, tmpl, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
