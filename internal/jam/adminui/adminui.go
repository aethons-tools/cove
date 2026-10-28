// Package adminui is Jam's read-only, server-rendered observability UI. It
// reads state directly from a jam.Store and renders embedded html/template
// pages, with htmx polling for the live cove view. It exposes an unauthenticated
// http.Handler; the operator-auth gate is applied by jam.NewAdminHandler,
// which mounts this handler inside its existing middleware.
package adminui

import (
	"bytes"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
	"github.com/aethons-tools/cove/internal/jam/collabhttp"
	"github.com/aethons-tools/cove/internal/jam/condition"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
	"github.com/aethons-tools/cove/internal/jam/uiassets"
	"github.com/aethons-tools/cove/internal/numfmt"
)

//go:embed templates/*.html
var files embed.FS

// page is one parsed template set (layout + that page's content) and where the
// page sits in the frame. The set is a master: each render executes a clone
// with the request's frame bound (renderStatus), so the master never executes.
type page struct {
	t    *template.Template
	meta pageMeta
}

var (
	jamPage   = func(tab string) pageMeta { return pageMeta{Kind: scopeJam, Tab: tab} }
	specsPage = func(sub string) pageMeta { return pageMeta{Kind: scopeJam, Tab: "specs", SubTab: sub} }
	projPage  = pageMeta{Kind: scopeProject}
	// listPage is the projects list: under Projects, with the rail.
	listPage = pageMeta{Kind: scopeProjects}
)

var pages = map[string]page{
	"dashboard":    mustParse(jamPage("dashboard"), "attention.html", "coves.html", "context_panel.html", "dashboard.html"),
	"agents":       mustParse(jamPage("agents"), "agents.html"),
	"users":        mustParse(jamPage("users"), "users.html"),
	"user":         mustParse(jamPage("users"), "user.html"),
	"kits":         mustParse(specsPage("/app/admin/kits"), "kits.html"),
	"destinations": mustParse(specsPage("/app/admin/destinations"), "dest_fields.html", "destinations.html"),
	"health":       mustParse(jamPage("health"), "health.html"),
	"intercom":     mustParse(jamPage("health"), "squawks.html", "intercom.html"), // the Channel log, under Health
	"projects":     mustParse(listPage, "projects.html"),
	"session":      mustParse(jamPage("agents"), "session.html"),
	"role":         mustParse(projPage, "coves.html", "context_panel.html", "role.html"),
	"destination":  mustParse(specsPage("/app/admin/destinations"), "dest_fields.html", "destination.html"),
	"model-specs":  mustParse(specsPage("/app/admin/model-specs"), "model_spec_fields.html", "model_specs.html"),
	"model-spec":   mustParse(specsPage("/app/admin/model-specs"), "model_spec_fields.html", "model_spec.html"),
	"kit":          mustParse(specsPage("/app/admin/kits"), "kit_shared.html", "kit.html"),
	"kit-draft":    mustParse(specsPage("/app/admin/kits"), "kit_shared.html", "kit_draft.html"),
	"project":      mustParse(projPage, "attention.html", "coves.html", "context_panel.html", "squawks.html", "project.html"),
	"agent":        mustParse(jamPage("agents"), "access.html", "agent.html"),
	"search":       mustParse(pageMeta{}, "search.html"),
	"config":       mustParse(jamPage("config"), "config.html"),
	"role-setup":   mustParse(projPage, "setup.html"),
	"agent-setup":  mustParse(jamPage("agents"), "setup.html"),
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

func mustParse(meta pageMeta, names ...string) page {
	paths := make([]string, 0, len(names)+1)
	paths = append(paths, "templates/layout.html")
	for _, n := range names {
		paths = append(paths, "templates/"+n)
	}
	t := template.Must(template.New("").Funcs(funcs).ParseFS(files, paths...))
	template.Must(t.Parse(appui.TitlebarTemplate))
	return page{t: template.Must(t.Parse(appui.HeadTemplate)), meta: meta}
}

// funcs are the template helpers shared by every page.
var funcs = template.FuncMap{
	// num and usd render amounts with thousands separators.
	"num": numfmt.Of,
	"usd": numfmt.USD,
	// ttl renders a role TTL compactly, or "—" when unset.
	"ttl": func(d time.Duration) string {
		if d <= 0 {
			return "—"
		}
		return fmtDur(d)
	},
	"roleURL":  roleURL,
	"agentURL": agentURL,
	// statusOf is an agent's display status from its studio's phase and activity.
	"statusOf": func(phase, activity string, hasStudio bool) jam.AgentStatus {
		return jam.StatusOf(jam.Phase(phase), jam.Activity(activity), hasStudio)
	},
	"statusClass": func(s jam.AgentStatus) string { return strings.ReplaceAll(string(s), " ", "-") },
	// displayText makes stored text's control and format characters visible
	// (jam.SetupDisplayText); HTML escaping stays the template's.
	"displayText": jam.SetupDisplayText,
	// frame, titlebar, apphead, attn, agentHref, managed and driftNote are bound per
	// render (frameFuncs); these are the defaults a fragment renders with.
	"frame":      func() frame { return frame{} },
	"titlebar":   func() appui.Titlebar { return appui.Titlebar{} },
	"apphead":    func() appui.Head { return appui.Head{} },
	"attn":       func(tab, subject string) *attnItem { return nil },
	"agentHref":  func(id, suffix string) string { return agentURL(id) + suffix },
	"managed":    func(kind, name string) *managedView { return nil },
	"driftNote":  func(kind, name string) string { return "" },
	"hl":         highlight,
	"stylesheet": func() string { return uiassets.StylesheetHref("/app/static/") },
	"destURL":    destURL,
	"specURL":    specURL,
	"kitURL":     kitURL,
	"draftURL":   draftURL,
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
	trustedOrigins  []string
	sessStore       sessionevents.Store
	sessHub         *sessionevents.Hub
	credNames       []string
	poolConfigured  bool
	displayName     string
	conds           *condition.Tracker
	alertmanagerURL string
	live            *collabhttp.Live
	exposures       func(actorID string) []jam.Exposure
	previewURL      func(actorID, name string) (string, error)
	kitFetcher      jam.KitFetcher
	configEnv       jam.ConfigEnv
	setupManagers   func() []string            // nil: no manager selector on a role's Setup tab (WithSetupManagers)
	setup           *jam.SetupDeps             // nil: no Setup tabs (WithSetupDeps)
	managers        func() []jam.ManagerStatus // nil: no Managers panel (WithManagers)
}

// WithDisplayName names this Jam in the UI: the browser tab title reads
// "<name> Jam" — and the title bar too when no appui.Shell brands it ("" keeps
// "Jam").
func WithDisplayName(name string) Option {
	return func(o *options) { o.displayName = name }
}

// WithConditions shows operator-attention conditions: open critical/warning
// ones as Jam attention items, and all of them on the Health tab, which links
// alertmanagerURL's silences when set.
func WithConditions(t *condition.Tracker, alertmanagerURL string) Option {
	return func(o *options) { o.conds, o.alertmanagerURL = t, strings.TrimRight(alertmanagerURL, "/") }
}

// WithSessions enables the live session-event timeline (/app/admin/agents/{id}/session).
func WithSessions(store sessionevents.Store, hub *sessionevents.Hub) Option {
	return func(o *options) { o.sessStore, o.sessHub = store, hub }
}

// WithKitFetcher enables the kit pages' git-source controls (import, track,
// sync, untrack) using f, the same fetcher the admin API uses. Without it the
// pages only show provenance.
func WithKitFetcher(f jam.KitFetcher) Option {
	return func(o *options) { o.kitFetcher = f }
}

// WithManagers shows a read-only Managers panel on the dashboard, one row per
// status list returns. Rotating and revoking stay on the CLI and admin API.
func WithManagers(list func() []jam.ManagerStatus) Option {
	return func(o *options) { o.managers = list }
}

// WithConfigEnv gives the Config page the planner's view of the serve — the
// env the admin API's WithConfig gets: its portable serve config, and the
// conditions an apply or a release re-checks config.drift on. Unset fields
// default to the Handler's credExists and WithPoolConfigured.
func WithConfigEnv(env jam.ConfigEnv) Option {
	return func(o *options) { o.configEnv = env }
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
	src := frameSource{store: store, img: sup, name: o.displayName, conds: o.conds}
	mux := http.NewServeMux()
	canEdit := sup != nil

	// The shared assets (jam.css, htmx) — templates are never reachable here.

	mux.HandleFunc("GET /app/admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("HX-Request") == "true" { // the studio table's poll
			renderFragment(w, r, "dashboard", "coves-table", map[string]any{"Coves": jam.CoveSummaries(store, sup)})
			return
		}
		var mgrs []jam.ManagerStatus
		if o.managers != nil {
			mgrs = o.managers()
		}
		render(w, r, "dashboard", map[string]any{
			"Title":       "Dashboard",
			"HasManagers": o.managers != nil,
			"Managers":    mgrs,
			"Coves":       jam.CoveSummaries(store, sup),
			"Stats":       dashboardStats(store),
			"JamContext":  jamPanel(store),
		})
	})

	registerAgents(mux, store, sup, canEdit)
	registerHealth(mux, o.conds, o.alertmanagerURL)
	// The rail's project list, re-fetched by its own poll so badges stay
	// live on the Projects pages.
	mux.HandleFunc("GET /app/admin/rail", func(w http.ResponseWriter, r *http.Request) {
		renderFragment(w, r, "dashboard", "rail-entries", railFor(r, src, src.items()))
	})
	// Specs has no page of its own: it opens on its first sub-tab.
	mux.HandleFunc("GET /app/admin/specs", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/admin/kits", http.StatusFound)
	})
	// The global roles list and role pages moved into the project tree.
	mux.HandleFunc("GET /app/admin/roles", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, "/app/admin/")
	})
	mux.HandleFunc("GET /app/admin/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		redirect(w, r, roleURL(r.PathValue("project"), r.PathValue("name")))
	})
	mux.HandleFunc("GET /app/admin/projects/{project}/roles/{name}", func(w http.ResponseWriter, r *http.Request) {
		handleRoleDetail(w, r, store, sup, canEdit, o.setup != nil)
	})
	mux.HandleFunc("GET /app/admin/intercom", func(w http.ResponseWriter, r *http.Request) {
		handleIntercom(w, r, msgs)
	})
	registerSession(mux, o.sessStore, o.sessHub)
	registerAgent(mux, store, msgs, o.sessStore, accessDeps{o.live, o.exposures, o.previewURL}, canEdit, o.setup != nil)
	registerSearch(mux, store, msgs)
	registerSuggest(mux, store, o.credNames)

	guardWrite := originGuard(o.trustedOrigins)
	registerWrites(mux, store, log, sup, credExists, guardWrite)
	registerRoleRequest(mux, store, log, sup, alloc, guardWrite)
	registerProjects(mux, store, sup, msgs, canEdit, log, guardWrite)
	registerProjectEdits(mux, store, sup, msgs, log, guardWrite)
	registerKits(mux, store, o.kitFetcher, log, guardWrite)
	registerKitDraft(mux, store, log, guardWrite)
	registerDraftEditor(mux, store, log, guardWrite)
	registerDraftUploads(mux, store, log, guardWrite)
	registerDraftPaths(mux, store, log, guardWrite)
	registerDraftPublish(mux, store, log, guardWrite)
	registerDestinations(mux, store, log, credExists, guardWrite)
	registerModelSpecs(mux, specUI{store: store, credExists: credExists, credNames: o.credNames, pool: o.poolConfigured}, log, guardWrite)
	registerRoleEdits(mux, store, sup, log, sup, credExists, canEdit, guardWrite)
	registerJamContext(mux, store, log, guardWrite)
	registerUsers(mux, store, log, guardWrite)
	registerAccess(mux, store, o.live, log, guardWrite)
	registerSetup(mux, store, o.setup, o.setupManagers, log)
	cfgEnv := o.configEnv
	if cfgEnv.CredExists == nil {
		cfgEnv.CredExists = credExists
	}
	cfgEnv.PoolConfigured = cfgEnv.PoolConfigured || o.poolConfigured
	registerConfig(mux, store, o.kitFetcher, cfgEnv, log, guardWrite)
	registerApp(mux, src, msgs, o.trustedOrigins, o.kitFetcher, cfgEnv, log)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, withFrameSource(r, src))
	})
}

// render executes the named page's "layout" template inside its frame.
func render(w http.ResponseWriter, r *http.Request, name string, data any) {
	renderStatus(w, r, http.StatusOK, name, data)
}

// renderStatus is render with an explicit status code. It executes a clone of
// the page's master set with the request's frame bound to the frame, attn and
// agentHref template funcs.
func renderStatus(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	p, ok := pages[name]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	var f frame
	if src, ok := frameSourceOf(r); ok {
		f = frameFor(r, p.meta, src)
	}
	t, err := p.t.Clone()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t.Funcs(frameFuncs(r, f))
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// frameFuncs binds f: frame returns it; titlebar is r's app title bar with
// f's tab current; apphead is r's installable-app head; attn finds the item a
// row is about (tab and subject, over every scope); agentHref links an agent
// page, keeping a project scope.
func frameFuncs(r *http.Request, f frame) template.FuncMap {
	return template.FuncMap{
		"frame": func() frame { return f },
		"titlebar": func() appui.Titlebar {
			tb := appui.TitlebarFor(r, f.TitleTab)
			if tb.Brand == "Jam" && f.Brand != "" { // no shell brand: the display name's
				tb.Brand = f.Brand
			}
			return tb
		},
		"apphead": func() appui.Head {
			h := appui.HeadFor(r)
			if h.Title == "Jam" && f.Brand != "" { // as titlebar: no shell brand
				h.Title = f.Brand
			}
			return h
		},
		"attn": func(tab, subject string) *attnItem {
			for i := range f.All {
				if f.All[i].Tab == tab && f.All[i].Subject == subject {
					return &f.All[i]
				}
			}
			return nil
		},
		"agentHref": func(id, suffix string) string {
			h := agentURL(id) + suffix
			if f.Kind == scopeProject {
				h += "?project=" + url.QueryEscape(f.Project)
			}
			return h
		},
		"managed":   func(kind, name string) *managedView { return managedOf(f.Marks, kind, name) },
		"driftNote": func(kind, name string) string { return driftNoteOf(f.Marks, kind, name) },
	}
}

// renderFragment executes a single named template (e.g. an htmx-swapped table)
// without the page chrome. Its row flags still bind every attention item, and
// a project page's fragment keeps that project's scope in its agent links
// (the project named by the route); any other fragment is in Jam's scope.
func renderFragment(w http.ResponseWriter, r *http.Request, name, tmpl string, data any) {
	p, ok := pages[name]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	var f frame
	if src, ok := frameSourceOf(r); ok {
		f = fragmentFrame(r, p.meta, src)
	}
	t, err := p.t.Clone()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	t.Funcs(frameFuncs(r, f))
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, tmpl, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// fragmentFrame is the part of a frame a fragment reads: the attention items
// and the scope its agent links keep.
func fragmentFrame(r *http.Request, meta pageMeta, src frameSource) frame {
	f := frame{Kind: meta.Kind, All: src.items(), Marks: src.marks()}
	if meta.Kind == scopeProject {
		if f.Project, _ = projectFromRoute(r); f.Project == "" {
			f.Kind = scopeNone
		}
	}
	return f
}
