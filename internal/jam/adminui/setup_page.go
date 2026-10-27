package adminui

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// WithSetupDeps enables the Setup tab of a running studio's agent page and of
// a role page: it renders jam.InstanceSetup / jam.RoleSetup in-process with
// d — the deps serve gives the admin API's jam.WithSetupInspect. Without it
// the tab is hidden and its routes are not mounted.
func WithSetupDeps(d jam.SetupDeps) Option {
	return func(o *options) { o.setup = &d }
}

// WithSetupManagers gives a role's Setup tab the managers to pick the image
// facts' manager from (the router's AvailableNames). With fewer than two the
// tab shows no selector.
func WithSetupManagers(names func() []string) Option {
	return func(o *options) { o.setupManagers = names }
}

// managerChoice is one entry of a role Setup tab's manager selector.
type managerChoice struct {
	Name    string
	Href    string
	Current bool
}

// setupPage is the Setup tab's shell: the subject's head, the Overview ·
// Setup strip, the section filter, the diff toggle (a studio's) and the lazy
// loader. It inspects nothing: SectionsURL is fetched once it has painted.
type setupPage struct {
	Title       string
	Studio      bool    // a studio's tab; else a role's
	ID          string  // studio: its session id (the agent id)
	Project     string  // role: the project, as in its URL
	Name        string  // role: the role's name
	ConfigName  string  // role: "<project>/<role>" (the managed badge)
	Crumbs      []crumb // role: Agents / <role> / Setup
	Filter      []subTab
	DiffHref    string          // studio: this page with ?diff=role toggled
	Diffing     bool            // studio: ?diff=role is on
	SectionsURL string          // the fragment the loader fetches, with this page's query
	Managers    []managerChoice // role: the manager selector (two or more managers)
	Error       string          // a bad ?sections= or ?diff=, shown instead of the loader
	NotFound    bool
}

// setupView is the sections fragment.
type setupView struct {
	Note     string // the render's note (a studio whose role is gone: no diff)
	Stale    int    // sections stale vs role
	Diffable bool   // some section has a DiffURL (its stale badge loads a diff)
	Sections []setupSectionView
}

// setupSectionView is one collapsible section. Every string in it has been
// through jam.SetupDisplayText; html/template escapes the rest.
type setupSectionView struct {
	Name, Source, Reason string
	Stale                bool
	At, AtISO            string // the snapshot time ("" without one)
	Body                 string
	Head                 string          // context: the file table, budgets and warnings
	Files                []setupFileView // context: each file, raw (none: Body is shown whole)
	Open                 bool
	DiffURL              string // a stale, undiffed studio section: its diff fragment
	Diffed, DiffTooLarge bool
	DiffSame             bool // diffed, and identical to a raise now
	Diff                 []diffLine
	DiffNote             string // why there is no diff (the render's note)
}

// setupFileView is one context file at its real path; Bytes is its shown size.
type setupFileView struct {
	Path, Body string
	Bytes      int
}

var errSetupDiff = errors.New(`diff: want "role"`)

// setupQuery reads ?sections= (empty = all) and, on a studio, ?diff=role.
func setupQuery(q url.Values, studio bool) (sections []string, diff bool, err error) {
	sections, err = jam.ParseSetupSections(q.Get("sections"))
	if err != nil {
		return nil, false, err
	}
	switch d := q.Get("diff"); {
	case d == "":
		return sections, false, nil
	case d == "role" && studio:
		return sections, true, nil
	default:
		return nil, false, errSetupDiff
	}
}

func withQuery(path string, q url.Values) string {
	if len(q) == 0 {
		return path
	}
	return path + "?" + q.Encode()
}

func cloneQuery(q url.Values) url.Values {
	out := url.Values{}
	for k, v := range q {
		out[k] = v
	}
	return out
}

// setupFilter is the section chips: All, then one per section, each keeping
// the rest of the query (?project=, ?diff=).
func setupFilter(base string, q url.Values) []subTab {
	cur := q.Get("sections")
	all := cloneQuery(q)
	all.Del("sections")
	out := []subTab{{Label: "All", Href: withQuery(base, all), Current: cur == ""}}
	for _, n := range jam.SetupSectionNames() {
		one := cloneQuery(q)
		one.Set("sections", n)
		out = append(out, subTab{Label: n, Href: withQuery(base, one), Current: cur == n})
	}
	return out
}

// fillSetupPage completes the shell from its request; a bad query is p.Error
// and 400.
func fillSetupPage(p *setupPage, r *http.Request, managers []string) int {
	base, q := r.URL.EscapedPath(), r.URL.Query()
	_, diff, err := setupQuery(q, p.Studio)
	if err != nil {
		p.Error = jam.SetupDisplayText(err.Error())
		return http.StatusBadRequest
	}
	p.Diffing = diff
	p.Filter = setupFilter(base, q)
	if p.Studio {
		t := cloneQuery(q)
		if diff {
			t.Del("diff")
		} else {
			t.Set("diff", "role")
		}
		p.DiffHref = withQuery(base, t)
	}
	p.SectionsURL = withQuery(base+"/sections", q)
	if !p.Studio && len(managers) > 1 {
		cur := q.Get("manager")
		if cur == "" {
			cur = managers[0] // "" reads the first available manager
		}
		for _, n := range managers {
			t := cloneQuery(q)
			t.Set("manager", n)
			p.Managers = append(p.Managers, managerChoice{Name: n, Href: withQuery(base, t), Current: n == cur})
		}
	}
	return http.StatusOK
}

// newSetupView is the fragment of s; diffBase ("" = none) is where a stale
// section's diff loads from (diffBase + "/" + name).
func newSetupView(s jam.StudioSetup, diffBase string) setupView {
	v := setupView{Note: jam.SetupDisplayText(s.Note)}
	for _, sec := range s.Sections {
		sv := newSectionView(sec, diffBase)
		if sv.Stale {
			v.Stale++
		}
		v.Diffable = v.Diffable || sv.DiffURL != ""
		v.Sections = append(v.Sections, sv)
	}
	if len(v.Sections) == 1 {
		v.Sections[0].Open = true
	}
	return v
}

func newSectionView(sec jam.SetupSection, diffBase string) setupSectionView {
	sv := setupSectionView{
		Name: sec.Name, Source: sec.Source, Reason: jam.SetupDisplayText(sec.Reason), Stale: sec.Stale,
		Body: jam.SetupDisplayText(sec.Body), Diffed: sec.Diffed, DiffTooLarge: sec.DiffTooLarge,
		Open: sec.Name == jam.SectionIdentity,
	}
	if !sec.At.IsZero() {
		sv.At, sv.AtISO = sec.At.UTC().Format("2006-01-02 15:04:05 UTC"), sec.At.UTC().Format(time.RFC3339)
	}
	if sec.Name == jam.SectionContext {
		if head, files, ok := jam.SplitContextBody(sec.Body); ok {
			sv.Head = jam.SetupDisplayText(head)
			for _, f := range files {
				sv.Files = append(sv.Files, setupFileView{Path: jam.SetupDisplayText(f.Path), Body: jam.SetupDisplayText(f.Body), Bytes: len(f.Body)})
			}
		}
	}
	if sec.Diffed {
		changed := false
		for _, l := range sec.Diff {
			sv.Diff = append(sv.Diff, diffLine{Op: l.Op, Text: jam.SetupDisplayText(l.Text)})
			changed = changed || l.Op == "+" || l.Op == "-"
		}
		sv.DiffSame = !sec.DiffTooLarge && !changed
		sv.Open = sv.Open || changed || sec.DiffTooLarge
	}
	if sec.Stale && !sec.Diffed && diffBase != "" {
		sv.DiffURL = diffBase + "/" + url.PathEscape(sec.Name)
	}
	return sv
}

// auditSetup logs an inspection as the admin API does — never a body.
func auditSetup(log *slog.Logger, r *http.Request, subject string, sections []string, diff bool) {
	log.Info("ui setup inspected", "operator", jam.OperatorID(r), "subject", subject, "sections", strings.Join(sections, ","), "diff", diff)
}

// setupFail maps a render error to its fixed-text response.
func setupFail(w http.ResponseWriter, log *slog.Logger, err error, subject, notFound string) {
	if code, ok := jam.ManagerErrStatus(err); ok {
		renderError(w, code, jam.SetupDisplayText(err.Error()))
		return
	}
	switch {
	case errors.Is(err, jam.ErrNotFound):
		renderError(w, http.StatusNotFound, notFound)
	case errors.Is(err, jam.ErrUnknownSection):
		renderError(w, http.StatusBadRequest, jam.SetupDisplayText(err.Error()))
	default:
		log.Warn("ui setup inspect failed", "subject", subject, "err", err.Error())
		renderError(w, http.StatusInternalServerError, "setup unavailable")
	}
}

// registerSetup mounts the Setup tabs (nothing without deps):
//
//	GET /app/admin/projects/{project}/roles/{name}/setup[/sections]
//	GET /app/admin/agents/{id}/setup[/sections]          — a running studio (?diff=role)
//	GET /app/admin/agents/{id}/setup/sections/{section}  — one section, diffed (the stale badge)
func registerSetup(mux *http.ServeMux, store jam.Store, d *jam.SetupDeps, managerNames func() []string, log *slog.Logger) {
	if d == nil {
		return
	}
	deps := *d
	names := func() []string {
		if managerNames == nil {
			return nil
		}
		return managerNames()
	}
	mux.HandleFunc("GET /app/admin/projects/{project}/roles/{name}/setup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		project, name := r.PathValue("project"), r.PathValue("name")
		p := setupPage{Title: name + " setup", Project: project, Name: name, ConfigName: orDefaultProject(project) + "/" + name,
			Crumbs: append(projectCrumbs(project, sectionAgents, name), crumb{Label: "Setup"})}
		if _, ok := store.GetRole(project, name); !ok {
			p.NotFound = true
			renderStatus(w, r, http.StatusNotFound, "role-setup", p)
			return
		}
		st := fillSetupPage(&p, r, names())
		renderStatus(w, r, st, "role-setup", p)
	})
	mux.HandleFunc("GET /app/admin/projects/{project}/roles/{name}/setup/sections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sections, _, err := setupQuery(r.URL.Query(), false)
		if err != nil {
			renderError(w, http.StatusBadRequest, jam.SetupDisplayText(err.Error()))
			return
		}
		project, name := r.PathValue("project"), r.PathValue("name")
		s, err := jam.RoleSetup(r.Context(), deps, project, name, sections, r.URL.Query().Get("manager"))
		if err != nil {
			setupFail(w, log, err, "role "+project+"/"+name, "role "+project+"/"+name+" does not exist")
			return
		}
		auditSetup(log, r, s.Subject, sections, false)
		renderFragment(w, r, "role-setup", "setup-sections", newSetupView(s, ""))
	})
	mux.HandleFunc("GET /app/admin/agents/{id}/setup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id := r.PathValue("id")
		p := setupPage{Title: id + " setup", Studio: true, ID: id}
		if inst, ok := store.GetInstance(jam.ResolveSession(store, id)); !ok || inst.Phase == jam.PhaseGone {
			p.NotFound = true
			renderStatus(w, r, http.StatusNotFound, "agent-setup", p)
			return
		}
		st := fillSetupPage(&p, r, nil)
		renderStatus(w, r, st, "agent-setup", p)
	})
	mux.HandleFunc("GET /app/admin/agents/{id}/setup/sections", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		sections, diff, err := setupQuery(r.URL.Query(), true)
		if err != nil {
			renderError(w, http.StatusBadRequest, jam.SetupDisplayText(err.Error()))
			return
		}
		id := r.PathValue("id")
		s, err := jam.InstanceSetup(r.Context(), deps, id, sections, diff)
		if err != nil {
			setupFail(w, log, err, "studio "+id, "studio "+id+" not found")
			return
		}
		auditSetup(log, r, s.Subject, sections, diff)
		renderFragment(w, r, "agent-setup", "setup-sections", newSetupView(s, agentURL(id)+"/setup/sections"))
	})
	mux.HandleFunc("GET /app/admin/agents/{id}/setup/sections/{section}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		id, name := r.PathValue("id"), r.PathValue("section")
		s, err := jam.InstanceSetup(r.Context(), deps, id, []string{name}, true)
		if err != nil {
			setupFail(w, log, err, "studio "+id, "studio "+id+" not found")
			return
		}
		auditSetup(log, r, s.Subject, []string{name}, true)
		v := newSetupView(s, "")
		sec := v.Sections[0]  // InstanceSetup validated name: exactly one section
		sec.DiffNote = v.Note // a role gone: why there is no diff
		renderFragment(w, r, "agent-setup", "setup-section", sec)
	})
}
