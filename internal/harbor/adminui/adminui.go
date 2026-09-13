// Package adminui is harbor's read-only, server-rendered observability UI. It
// reads state directly from a harbor.Store and renders embedded html/template
// pages, with htmx polling for the live cove view. It exposes an unauthenticated
// http.Handler; the operator-auth gate is applied by harbor.NewAdminHandler,
// which mounts this handler inside its existing middleware.
package adminui

import (
	"embed"
	"html/template"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

//go:embed templates/*.html htmx.min.js
var files embed.FS

// page holds one parsed template set (layout + that page's content). Each set's
// full page is rendered via ExecuteTemplate(w, "layout", data).
var pages = map[string]*template.Template{
	"index":        mustParse("index.html"),
	"coves":        mustParse("coves.html"),
	"roster":       mustParse("roster.html"),
	"roles":        mustParse("roles.html"),
	"kits":         mustParse("kits.html"),
	"destinations": mustParse("destinations.html"),
}

// roleRow is one project/role pair flattened for the roles table.
type roleRow struct {
	Project      string
	Name         string
	Destinations []string
	Repos        []string
	TTL          time.Duration
	Kit          string
}

func roleRows(store harbor.Store) []roleRow {
	var out []roleRow
	for _, p := range store.ListProjects() {
		for _, r := range store.ListRoles(p) {
			out = append(out, roleRow{
				Project: p, Name: r.Name,
				Destinations: r.Scope.Destinations, Repos: r.Scope.Repos,
				TTL: r.Scope.TTL, Kit: r.Kit,
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
	return template.Must(template.ParseFS(files, paths...))
}

// Handler returns the UI mux (no auth wrap). store is the only dependency:
// every view is an in-process read.
func Handler(store harbor.Store) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /ui/static/", http.StripPrefix("/ui/static/", http.FileServer(http.FS(files))))

	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
		render(w, "index", map[string]any{"Title": "Dashboard"})
	})

	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{"Title": "Coves", "Coves": store.ListInstances()}
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "coves", "coves-table", data)
			return
		}
		render(w, "coves", data)
	})

	mux.HandleFunc("GET /ui/roster", func(w http.ResponseWriter, r *http.Request) {
		render(w, "roster", map[string]any{"Title": "Roster", "Actors": harbor.RosterSummaries(store)})
	})
	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
		render(w, "roles", map[string]any{"Title": "Roles", "Roles": roleRows(store)})
	})
	mux.HandleFunc("GET /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		render(w, "kits", map[string]any{"Title": "Kits", "Kits": store.ListKits()})
	})
	mux.HandleFunc("GET /ui/destinations", func(w http.ResponseWriter, r *http.Request) {
		render(w, "destinations", map[string]any{"Title": "Destinations", "Destinations": store.ListDestinations()})
	})

	return mux
}

// render executes the named page's "layout" template.
func render(w http.ResponseWriter, page string, data any) {
	t, ok := pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
