package adminui

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// The roles JSON API: a project's Agents view and a role's page with its
// writes and its Setup — the same calls as role_detail.go, role_edit.go,
// role_request.go, writes.go's role create/delete and setup_page.go.

// ttlText is a TTL as the pages show it ("—" = none).
func ttlText(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	return fmtDur(d)
}

// attnFlag is the attention item on tab about subject, if any.
func attnFlag(items []attnItem, tab, subject string) *AttnFlag {
	for _, it := range items {
		if it.Tab == tab && it.Subject == subject {
			return &AttnFlag{Kind: string(it.Kind), Why: it.Why}
		}
	}
	return nil
}

func statusText(phase, activity string, hasStudio bool) string {
	return string(jam.StatusOf(jam.Phase(phase), jam.Activity(activity), hasStudio))
}

func coveRows(cs []jam.CoveSummary, items []attnItem) []CoveRow {
	out := []CoveRow{}
	for _, c := range cs {
		row := CoveRow{ID: c.ID, Name: c.Name, Project: c.Project, Role: c.Role, Unit: c.Unit, Status: statusText(c.Phase, c.Activity, true),
			Connector: c.Connector, Image: c.Image, Manager: c.Manager, LeaseHolder: c.LeaseHolder, RaisedAt: c.RaisedAt.Format("Jan 2 15:04"),
			Attn: attnFlag(items, "agents", c.ID)}
		if !c.LastSeen.IsZero() {
			row.LastSeen = c.LastSeen.Format("15:04:05")
		}
		out = append(out, row)
	}
	return out
}

func projectAgentsOf(store jam.Store, img jam.ImageResolver, items []attnItem, project string, canRequest bool) ProjectAgents {
	out := ProjectAgents{Project: project, Roles: []RoleRow{}, Agents: []AgentRow{}, CanRequest: canRequest}
	for _, r := range roleRows(store) {
		if r.Project != project {
			continue
		}
		row := RoleRow{Name: r.Name, Destinations: []DestCred{}, TTL: ttlText(r.TTL), Kit: r.Kit, Attn: attnFlag(items, "agents", project+"/"+r.Name)}
		for _, d := range r.Destinations {
			row.Destinations = append(row.Destinations, DestCred{Name: d, Cred: r.Credentials[d]})
		}
		out.Roles = append(out.Roles, row)
	}
	for _, a := range projectAgents(store, img, project) {
		row := AgentRow{ID: a.ID, Name: a.Name, Kind: a.Kind, Unit: a.Unit, Status: string(a.Status), Image: a.Image,
			Roles: append([]string{}, a.Roles...), Attn: attnFlag(items, "agents", a.ID)}
		if !a.LastSeen.IsZero() {
			row.LastSeen = a.LastSeen.Format("Jan 2 15:04:05")
		}
		out.Agents = append(out.Agents, row)
	}
	return out
}

func roleDetailOf(store jam.Store, img jam.ImageResolver, items []attnItem, marks map[jam.ConfigKey]jam.ConfigMark, project, name string, canRequest, setupOn bool) (RoleDetail, bool) {
	d, ok := buildRoleDetail(store, img, project, name)
	if !ok {
		return RoleDetail{}, false
	}
	r := d.Role
	out := RoleDetail{Project: d.Project, Name: name, Managed: managedJSON(marks, jam.ConfigKindRole, d.ConfigName), Kit: r.Kit,
		ModelSpec: r.ModelSpecName(), ModelSpecDefault: r.ModelSpec == "", TTL: ttlText(r.Scope.TTL), Dests: []DestRow{},
		Addressing: append([]string{}, r.Scope.Addressing...), EgressManaged: d.EgressManaged, EgressDomains: []string{},
		Allocation: []Setting{}, Standing: []StandingRow{}, Holders: []HolderRow{}, Running: coveRows(d.Coves, items),
		Form: RoleForm{Destinations: d.Form.Destinations, Addressing: d.Form.Addressing, TTL: d.Form.TTL, Kit: r.Kit, ModelSpec: r.ModelSpec,
			MaxEphemeral: d.Form.MaxEphemeral, MaxPersonal: d.Form.MaxPersonal, MaxPersonalPerOwner: d.Form.MaxPersonalPerOwner,
			IdleAfter: d.Form.IdleAfter, NagEvery: d.Form.NagEvery, ReclaimAfter: d.Form.ReclaimAfter, Egress: d.Form.Egress},
		Context: contextPanelOf(d.Context, marks), CanRequest: canRequest, SetupEnabled: setupOn,
		Attn: attnFlag(items, "agents", d.Project+"/"+name)}
	if r.Scope.Egress != nil {
		out.EgressDomains = append(out.EgressDomains, r.Scope.Egress.Domains...)
	}
	for _, x := range d.Dests {
		out.Dests = append(out.Dests, DestRow{Name: x.Name, Cred: x.Cred, CredIsRole: x.CredIsRole, Route: x.Route, Missing: x.Missing})
	}
	for _, s := range d.Allocation {
		out.Allocation = append(out.Allocation, Setting{Label: s.Label, Value: s.Value, Unset: s.Unset})
	}
	for _, s := range d.Standing {
		row := StandingRow{Name: s.Name, Prompt: s.Prompt, ActorID: s.ActorID, Image: s.Image, Upgrade: s.Upgrade}
		if s.Phase != "" {
			row.Status = statusText(s.Phase, s.Activity, true)
		}
		out.Standing = append(out.Standing, row)
	}
	for _, h := range d.Holders {
		out.Holders = append(out.Holders, HolderRow{ID: h.ID, Override: h.Override})
	}
	return out, true
}

// formOf maps the JSON fields of a role write onto the HTML forms' field
// names, so both run the same apply functions.
func formOf(m map[string]string, names map[string]string) func(string) string {
	return func(field string) string { return m[names[field]] }
}

var (
	scopeFields = map[string]string{"destinations": "destinations", "addressing": "addressing", "ttl": "ttl", "kit": "kit", "model-spec": "modelSpec"}
	allocFields = map[string]string{"max-ephemeral": "maxEphemeral", "max-personal": "maxPersonal", "max-personal-per-owner": "maxPersonalPerOwner",
		"idle-after": "idleAfter", "nag-every": "nagEvery", "reclaim-after": "reclaimAfter"}
	createFields = map[string]string{"name": "name", "destinations": "destinations", "ttl-seconds": "ttlSeconds", "kit": "kit", "model-spec": "modelSpec"}
)

// roleAPI is what the roles API needs beyond the frame source.
type roleAPI struct {
	sup        *jam.Supervisor
	alloc      jam.SessionAllocator
	credExists func(string) bool
	setup      *jam.SetupDeps
	managers   func() []string
}

func apiRoles(api *http.ServeMux, src frameSource, ra roleAPI, log *slog.Logger) {
	store := src.store
	canRequest := ra.sup != nil
	api.HandleFunc("GET "+apiPrefix+"projects/{project}/agents", func(w http.ResponseWriter, r *http.Request) {
		p := r.PathValue("project")
		if pr, ok := store.GetProject(p); !ok || pr.Status == jam.StatusRemoved {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such project")
			return
		}
		writeJSON(w, http.StatusOK, projectAgentsOf(store, src.img, src.items(), p, canRequest))
	})
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/roles", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if !decodeJSON(w, r, &body) {
			return
		}
		project, name := r.PathValue("project"), strings.TrimSpace(body["name"])
		if err := createRole(store, ra.credExists, project, formOf(body, createFields)); err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui role created", "operator", jam.OperatorID(r), "project", orDefaultProject(project), "role", name)
		writeJSON(w, http.StatusCreated, Created{ID: name, Path: "config/projects/" + url.PathEscape(orDefaultProject(project)) + "/agents/" + url.PathEscape(name)})
	})
	api.HandleFunc("GET "+apiPrefix+"projects/{project}/roles/{role}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := roleDetailOf(store, src.img, src.items(), src.marks(), r.PathValue("project"), r.PathValue("role"), canRequest, ra.setup != nil)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such role")
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	api.HandleFunc("DELETE "+apiPrefix+"projects/{project}/roles/{role}", func(w http.ResponseWriter, r *http.Request) {
		project, name := r.PathValue("project"), r.PathValue("role")
		if err := store.RemoveRole(project, name); err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		log.Info("ui role removed", "operator", jam.OperatorID(r), "project", project, "role", name)
		w.WriteHeader(http.StatusNoContent)
	})
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/roles/{role}/request", func(w http.ResponseWriter, r *http.Request) {
		res, err := requestRole(r.Context(), store, ra.sup, ra.alloc, log, jam.OperatorID(r), r.PathValue("project"), r.PathValue("role"))
		if err != nil {
			apiFail(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, Requested{ID: res.ID, Name: res.Name, Message: "Requested agent " + res.Name + " (" + res.ID + "); it will squawk you on the intercom."})
	})

	// edit runs one section write on {project}/{role} (a POST's JSON body as
	// a string map) and answers with the role and the write's message, as
	// registerRoleEdits does.
	edit := func(what string, apply func(r *http.Request, project, name string, body map[string]string) (string, error)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			body := map[string]string{}
			if r.Method == http.MethodPost && r.ContentLength != 0 && !decodeJSON(w, r, &body) {
				return
			}
			project, name := r.PathValue("project"), r.PathValue("role")
			msg, err := apply(r, project, name, body)
			if err != nil {
				apiFail(w, err, http.StatusInternalServerError)
				return
			}
			log.Info("ui role "+what, "operator", jam.OperatorID(r), "project", orDefaultProject(project), "role", name)
			d, ok := roleDetailOf(store, src.img, src.items(), src.marks(), project, name, canRequest, ra.setup != nil)
			if !ok {
				writeAPIError(w, http.StatusNotFound, "not_found", "role no longer exists")
				return
			}
			writeJSON(w, http.StatusOK, RoleResult{Role: d, Message: msg})
		}
	}
	quiet := func(apply func(r *http.Request, project, name string, body map[string]string) error) func(*http.Request, string, string, map[string]string) (string, error) {
		return func(r *http.Request, project, name string, body map[string]string) (string, error) {
			return "", apply(r, project, name, body)
		}
	}
	base := apiPrefix + "projects/{project}/roles/{role}/"
	api.HandleFunc("POST "+base+"scope", edit("scope set", quiet(func(_ *http.Request, project, name string, b map[string]string) error {
		return setRoleScope(store, ra.credExists, project, name, formOf(b, scopeFields))
	})))
	api.HandleFunc("POST "+base+"allocation", edit("allocation set", quiet(func(_ *http.Request, project, name string, b map[string]string) error {
		return setRoleAllocation(store, project, name, formOf(b, allocFields))
	})))
	api.HandleFunc("POST "+base+"context", edit("context set", quiet(func(_ *http.Request, project, name string, b map[string]string) error {
		cb, err := jam.ParseContextYAML([]byte(b["yaml"]), nil)
		if err != nil {
			return badRequest(err.Error())
		}
		return jam.SetRoleContextChecked(store, project, name, cb)
	})))
	api.HandleFunc("DELETE "+base+"context", edit("context cleared", quiet(func(_ *http.Request, project, name string, _ map[string]string) error {
		return jam.ClearRoleContext(store, project, name)
	})))
	api.HandleFunc("POST "+base+"egress", edit("egress set", quiet(func(_ *http.Request, project, name string, b map[string]string) error {
		_, err := jam.SetRoleEgress(store, project, name, splitList(b["domains"]))
		return err
	})))
	api.HandleFunc("DELETE "+base+"egress", edit("egress cleared", quiet(func(_ *http.Request, project, name string, _ map[string]string) error {
		return jam.ClearRoleEgress(store, project, name)
	})))
	api.HandleFunc("POST "+base+"standing", edit("standing declared", quiet(func(_ *http.Request, project, name string, b map[string]string) error {
		return jam.AddStanding(store, project, name, jam.StandingSession{Name: strings.TrimSpace(b["name"]), Prompt: strings.TrimSpace(b["prompt"])})
	})))
	api.HandleFunc("DELETE "+base+"standing/{session}", edit("standing dismissed", quiet(func(r *http.Request, project, name string, _ map[string]string) error {
		return jam.RemoveStanding(store, project, name, r.PathValue("session"))
	})))
	api.HandleFunc("POST "+base+"standing/{session}/reset", edit("standing reset", func(r *http.Request, project, name string, _ map[string]string) (string, error) {
		return resetStandingMsg(r.Context(), store, ra.sup, project, name, r.PathValue("session"))
	}))
	api.HandleFunc("POST "+base+"standing/{session}/upgrade", edit("standing upgrade", func(r *http.Request, project, name string, _ map[string]string) (string, error) {
		return upgradeStandingMsg(store, ra.sup, project, name, r.PathValue("session"))
	}))
	api.HandleFunc("GET "+base+"setup", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		project, name := r.PathValue("project"), r.PathValue("role")
		if _, ok := store.GetRole(project, name); !ok || ra.setup == nil {
			writeAPIError(w, http.StatusNotFound, "not_found", "no setup for this role")
			return
		}
		sections, _, err := setupQuery(r.URL.Query(), false)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "bad_request", jam.SetupDisplayText(err.Error()))
			return
		}
		var managers []string
		if ra.managers != nil {
			managers = ra.managers()
		}
		manager := r.URL.Query().Get("manager")
		s, err := jam.RoleSetup(r.Context(), *ra.setup, project, name, sections, manager)
		if err != nil {
			setupFailJSON(w, log, err, "role "+project+"/"+name, "role "+project+"/"+name+" does not exist")
			return
		}
		auditSetup(log, r, s.Subject, sections, false)
		out := setupResponseOf(newSetupView(s, ""), "")
		if len(managers) > 1 {
			out.Managers = managers
			out.Manager = manager
			if out.Manager == "" {
				out.Manager = managers[0] // "" reads the first available manager
			}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// setupResponseOf converts a setup view; diffBase ("" = none) is where a
// stale section's diff loads from under the API.
func setupResponseOf(v setupView, diffBase string) SetupResponse {
	out := SetupResponse{Note: v.Note, Stale: v.Stale, Sections: []SetupSection{}, SectionNames: jam.SetupSectionNames(), Managers: []string{}}
	for _, sv := range v.Sections {
		out.Sections = append(out.Sections, setupSectionOf(sv, diffBase))
	}
	return out
}

func setupSectionOf(sv setupSectionView, diffBase string) SetupSection {
	out := SetupSection{Name: sv.Name, Source: sv.Source, Reason: sv.Reason, At: sv.At, Stale: sv.Stale, Open: sv.Open, Body: sv.Body, Head: sv.Head,
		Files: []SetupFile{}, Diffed: sv.Diffed, DiffTooLarge: sv.DiffTooLarge, DiffSame: sv.DiffSame, Diff: diffLines(sv.Diff), DiffNote: sv.DiffNote}
	for _, f := range sv.Files {
		out.Files = append(out.Files, SetupFile{Path: f.Path, Body: f.Body, Bytes: f.Bytes})
	}
	if sv.DiffURL != "" && diffBase != "" {
		out.DiffPath = diffBase + "/" + url.PathEscape(sv.Name)
	}
	return out
}
