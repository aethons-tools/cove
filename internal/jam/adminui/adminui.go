// Package adminui is Jam's read-only, server-rendered observability UI. It
// reads state directly from a jam.Store and renders embedded html/template
// pages, with htmx polling for the live cove view. It exposes an unauthenticated
// http.Handler; the operator-auth gate is applied by jam.NewAdminHandler,
// which mounts this handler inside its existing middleware.
package adminui

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
)

//go:embed templates/*.html
var files embed.FS

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
	"session":      mustParse("session.html"),
	"role":         mustParse("coves.html", "role.html"),
	"destination":  mustParse("dest_fields.html", "destination.html"),
	"kit":          mustParse("kit.html"),
	"projects":     mustParse("projects.html"),
	"project":      mustParse("coves.html", "project.html"),
	"studio":       mustParse("studio.html"),
	"search":       mustParse("search.html"),
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

// covesData, rosterData and rolesData are the payloads of the pages (and
// their swapped tables) whose forms carry a project picker.
func covesData(store jam.Store, canEdit bool) map[string]any {
	return map[string]any{"Coves": jam.CoveSummaries(store), "CanEdit": canEdit, "Projects": projectChoices(store)}
}

func rosterData(store jam.Store) map[string]any {
	return map[string]any{"Actors": jam.RosterSummaries(store), "Projects": projectChoices(store)}
}

func rolesData(store jam.Store, canRequest bool) map[string]any {
	return map[string]any{"Roles": roleRows(store), "CanRequest": canRequest, "Projects": projectChoices(store)}
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
	"roleURL":    roleURL,
	"hl":         highlight,
	"stylesheet": func() string { return uiassets.StylesheetHref("/ui/static/") },
	"destURL":    destURL,
	"kitURL":     kitURL,
	"projectURL": projectURL,
	"blankHuman": func() humanRow { return humanRow{} },
	"chainForm": func(project string, c chainView) map[string]any {
		return map[string]any{"Project": project, "Chain": c}
	},
}

// fmtDur renders a duration without trailing zero units: "1h", "1h30m", "45s".
func fmtDur(d time.Duration) string { return jam.FormatDuration(d) }

// Option configures Handler.
type Option func(*options)

type options struct {
	trustedOrigins []string
	sessStore      sessionevents.Store
	sessHub        *sessionevents.Hub
}

// WithSessions enables the live session-event timeline (/ui/coves/{id}/session).
func WithSessions(store sessionevents.Store, hub *sessionevents.Hub) Option {
	return func(o *options) { o.sessStore, o.sessHub = store, hub }
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

	// The shared assets (jam.css, htmx) — templates are never reachable here.
	mux.Handle("GET /ui/static/", uiassets.Handler("/ui/static/"))

	mux.HandleFunc("GET /ui/{$}", func(w http.ResponseWriter, r *http.Request) {
		render(w, "dashboard", map[string]any{
			"Title": "Dashboard",
			"Coves": jam.CoveSummaries(store),
			"Stats": dashboardStats(store),
		})
	})

	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		data := covesData(store, canEdit)
		data["Title"] = "Studios"
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "coves", "coves-table", data)
			return
		}
		render(w, "coves", data)
	})

	mux.HandleFunc("GET /ui/roster", func(w http.ResponseWriter, r *http.Request) {
		data := rosterData(store)
		data["Title"] = "Roster"
		render(w, "roster", data)
	})
	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
		data := rolesData(store, canEdit)
		data["Title"] = "Roles"
		render(w, "roles", data)
	})
	mux.HandleFunc("GET /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		handleRoleDetail(w, r, store, canEdit)
	})
	mux.HandleFunc("GET /ui/intercom", func(w http.ResponseWriter, r *http.Request) {
		handleIntercom(w, r, msgs)
	})
	registerSession(mux, o.sessStore, o.sessHub)
	registerStudio(mux, store, msgs, o.sessStore, canEdit)
	registerSearch(mux, store, msgs)

	guardWrite := originGuard(o.trustedOrigins)
	registerWrites(mux, store, log, sup, credExists, guardWrite)
	registerRoleRequest(mux, store, log, sup, alloc, guardWrite)
	registerProjects(mux, store, log, guardWrite)
	registerProjectEdits(mux, store, log, guardWrite)
	registerKits(mux, store, log, guardWrite)
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
