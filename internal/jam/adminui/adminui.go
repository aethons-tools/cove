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
// full page is rendered via ExecuteTemplate(w, "layout", data). The first
// argument is the page's top-nav section (see nav.go), which the layout
// highlights.
var pages = map[string]*template.Template{
	"dashboard":    mustParse(navDashboard, "coves.html", "context_panel.html", "dashboard.html"),
	"coves":        mustParse(navAgents, "coves.html"),
	"roster":       mustParse(navAgents, "roster.html"),
	"users":        mustParse(navUsers, "users.html"),
	"user":         mustParse(navUsers, "user.html"),
	"kits":         mustParse(navSpecs, "kits.html"),
	"destinations": mustParse(navSpecs, "dest_fields.html", "destinations.html"),
	"intercom":     mustParse(navIntercom, "squawks.html", "intercom.html"),
	"session":      mustParse(navAgents, "session.html"),
	"role":         mustParse(navProjects, "coves.html", "context_panel.html", "projtree.html", "role.html"),
	"destination":  mustParse(navSpecs, "dest_fields.html", "destination.html"),
	"model-specs":  mustParse(navSpecs, "model_spec_fields.html", "model_specs.html"),
	"model-spec":   mustParse(navSpecs, "model_spec_fields.html", "model_spec.html"),
	"kit":          mustParse(navSpecs, "kit.html"),
	"projects":     mustParse(navProjects, "projects.html"),
	"project":      mustParse(navProjects, "coves.html", "context_panel.html", "squawks.html", "projtree.html", "project.html"),
	"studio":       mustParse(navAgents, "studio.html"),
	"search":       mustParse(navNone, "search.html"),
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

func mustParse(section navSection, names ...string) *template.Template {
	paths := make([]string, 0, len(names)+1)
	paths = append(paths, "templates/layout.html")
	for _, n := range names {
		paths = append(paths, "templates/"+n)
	}
	return template.Must(template.New("").Funcs(funcs).Funcs(template.FuncMap{
		"navSection": func() navSection { return section },
	}).ParseFS(files, paths...))
}

// covesData and rosterData are the payloads of those pages and
// their swapped tables.
func covesData(store jam.Store, img jam.ImageResolver, canEdit bool) map[string]any {
	return map[string]any{"Coves": jam.CoveSummaries(store, img), "CanEdit": canEdit}
}

func rosterData(store jam.Store) map[string]any {
	return map[string]any{"Actors": jam.RosterSummaries(store)}
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
	"navItems":   func() []navItem { return navItems },
	"hl":         highlight,
	"stylesheet": func() string { return uiassets.StylesheetHref("/ui/static/") },
	"destURL":    destURL,
	"specURL":    specURL,
	"kitURL":     kitURL,
	"projectURL": projectURL,
	"userURL":    userURL,
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
	credNames      []string
	poolConfigured bool
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
			"Title":      "Dashboard",
			"Coves":      jam.CoveSummaries(store, sup),
			"Stats":      dashboardStats(store),
			"JamContext": jamPanel(store),
		})
	})

	mux.HandleFunc("GET /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		data := covesData(store, sup, canEdit)
		data["Title"] = "Studios"
		if r.Header.Get("HX-Request") == "true" {
			renderFragment(w, "coves", "coves-table", data)
			return
		}
		render(w, "coves", data)
	})

	mux.HandleFunc("GET /ui/actors", func(w http.ResponseWriter, r *http.Request) {
		data := rosterData(store)
		data["Title"] = "Actors"
		render(w, "roster", data)
	})
	// The global roles list and role pages moved into the project tree.
	mux.HandleFunc("GET /ui/roles", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, "/ui/projects")
	})
	mux.HandleFunc("GET /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, roleURL(r.PathValue("project"), r.PathValue("name")))
	})
	mux.HandleFunc("GET /ui/projects/{project}/roles/{name}", func(w http.ResponseWriter, r *http.Request) {
		handleRoleDetail(w, r, store, sup, canEdit)
	})
	mux.HandleFunc("GET /ui/intercom", func(w http.ResponseWriter, r *http.Request) {
		handleIntercom(w, r, msgs)
	})
	registerSession(mux, o.sessStore, o.sessHub)
	registerStudio(mux, store, msgs, o.sessStore, canEdit)
	registerSearch(mux, store, msgs)
	registerSuggest(mux, store, o.credNames)

	guardWrite := originGuard(o.trustedOrigins)
	registerWrites(mux, store, log, sup, credExists, guardWrite)
	registerRoleRequest(mux, store, log, sup, alloc, guardWrite)
	registerProjects(mux, store, sup, msgs, canEdit, log, guardWrite)
	registerProjectEdits(mux, store, sup, msgs, log, guardWrite)
	registerKits(mux, store, log, guardWrite)
	registerDestinations(mux, store, log, credExists, guardWrite)
	registerModelSpecs(mux, specUI{store: store, credExists: credExists, credNames: o.credNames, pool: o.poolConfigured}, log, guardWrite)
	registerRoleEdits(mux, store, sup, log, sup, credExists, canEdit, guardWrite)
	registerJamContext(mux, store, log, guardWrite)
	registerUsers(mux, store, log, guardWrite)

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
