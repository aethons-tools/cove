package adminui

import (
	"cmp"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// The projects JSON API: the project list and a project's pages with their
// writes — the same store and jam calls as projects.go and project_edit.go.

func managedJSON(marks map[jam.ConfigKey]jam.ConfigMark, kind, name string) *Managed {
	if m := managedOf(marks, kind, name); m != nil {
		return &Managed{Commit: m.Commit, Applied: m.Applied}
	}
	return nil
}

func contextPanelOf(p contextPanel, marks map[jam.ConfigKey]jam.ConfigMark) ContextPanel {
	out := ContextPanel{Scope: p.Scope, Core: p.Core, CoreBytes: p.CoreBytes, Budget: p.Budget, Over: p.Over, Empty: p.Empty,
		ShowResources: p.ShowResources, Leaves: []ContextLeaf{}, Resources: []ContextResource{}, YAML: p.YAML, Managed: managedJSON(marks, p.Kind, p.Name)}
	for _, l := range p.Leaves {
		out.Leaves = append(out.Leaves, ContextLeaf{Name: l.Name, ReadWhen: l.ReadWhen})
	}
	for _, r := range p.Resources {
		out.Resources = append(out.Resources, ContextResource{Name: r.Name, Kind: string(r.Kind), Ref: r.Ref, Note: r.Note})
	}
	return out
}

func chainRowOf(c chainView) ChainRow {
	out := ChainRow{Category: c.Category, Tiers: []TierRow{}, Spec: c.Spec}
	for _, t := range c.Tiers {
		tr := TierRow{Targets: []TargetRow{}, Timeout: t.Timeout}
		for _, x := range t.Targets {
			tr.Targets = append(tr.Targets, TargetRow{Text: x.Text, Unknown: x.Unknown})
		}
		out.Tiers = append(out.Tiers, tr)
	}
	return out
}

func squawkRowsOf(t squawkTable) []SquawkRow {
	out := []SquawkRow{}
	for _, r := range t.Rows {
		out = append(out, SquawkRow{At: r.At, From: r.From, FromID: r.FromID, AgentID: r.AgentID, Channel: r.Channel, ChannelID: r.ChannelID,
			Project: r.Project, Body: string(r.Body), Plain: r.Plain})
	}
	return out
}

func projectDetailOf(store jam.Store, img jam.ImageResolver, msgs SquawkReader, marks map[jam.ConfigKey]jam.ConfigMark, name string, withLog bool) (ProjectDetail, bool) {
	section := sectionOverview
	if withLog {
		section = sectionIntercom
	}
	d, ok := buildProjectDetail(store, img, msgs, name, section)
	if !ok {
		return ProjectDetail{}, false
	}
	out := ProjectDetail{Name: name, Managed: managedJSON(marks, jam.ConfigKindProject, name), InUseBy: d.InUseBy, Renamable: name != "default",
		ChatService: d.ChatService, ChatServices: append([]string{}, d.ChatServices...), Members: []MemberRow{}, Rooms: []RoomRow{},
		Roles: len(d.Roles), Agents: d.Agents, LiveAgents: d.LiveCoves, Escalation: []ChainRow{}, DefaultChain: chainRowOf(d.DefaultChain),
		Context: contextPanelOf(d.Context, marks), LogConfigured: d.LogConfigured, Squawks: squawkRowsOf(d.Squawks)}
	for _, m := range d.Members {
		row := MemberRow{UserID: string(m.UserID), Name: m.Name, Handle: m.Handle, Delivery: []DeliveryRow{}, DeliverySpec: m.DeliverySpec}
		for _, p := range m.Delivery {
			row.Delivery = append(row.Delivery, DeliveryRow{Service: p.Service, Address: p.Address})
		}
		out.Members = append(out.Members, row)
	}
	for _, r := range d.Rooms {
		out.Rooms = append(out.Rooms, RoomRow{Name: r.Name, Connection: r.Connection, Kind: r.Kind, Ref: r.Ref, PostOnly: r.PostOnly})
	}
	for _, c := range d.Escalation {
		out.Escalation = append(out.Escalation, chainRowOf(c))
	}
	return out, true
}

func projectCreated(name string) Created {
	return Created{ID: name, Path: "config/projects/" + url.PathEscape(name)}
}

func apiProjects(api *http.ServeMux, src frameSource, msgs SquawkReader, log *slog.Logger) {
	store := src.store
	api.HandleFunc("GET "+apiPrefix+"projects", func(w http.ResponseWriter, r *http.Request) {
		marks := src.marks()
		out := ProjectsResponse{Projects: []ProjectRow{}}
		for _, name := range slices.Sorted(slices.Values(store.ListProjects())) {
			p, ok := store.GetProject(name)
			if !ok {
				continue
			}
			out.Projects = append(out.Projects, ProjectRow{Name: name, Members: len(store.ListMembers(p.ID)), Roles: len(store.ListRoles(name)),
				Rooms: len(jam.ListRooms(store, p)), Managed: managedJSON(marks, jam.ConfigKindProject, name)})
		}
		slices.SortFunc(out.Projects, func(a, b ProjectRow) int { return cmp.Compare(a.Name, b.Name) })
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("POST "+apiPrefix+"projects", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		if !decodeJSON(w, r, &body) {
			return
		}
		name := strings.TrimSpace(body.Name)
		if err := store.CreateProject(name); err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui project created", "operator", jam.OperatorID(r), "project", name)
		writeJSON(w, http.StatusCreated, projectCreated(name))
	})
	api.HandleFunc("GET "+apiPrefix+"projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := projectDetailOf(store, src.img, msgs, src.marks(), r.PathValue("project"), r.URL.Query().Get("section") == string(sectionIntercom))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such project")
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/rename", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Name string }
		if !decodeJSON(w, r, &body) {
			return
		}
		old, name := r.PathValue("project"), strings.TrimSpace(body.Name)
		if err := store.RenameProject(old, name); err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui project renamed", "operator", jam.OperatorID(r), "project", old, "name", name)
		writeJSON(w, http.StatusOK, projectCreated(name))
	})
	api.HandleFunc("DELETE "+apiPrefix+"projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("project")
		if err := store.RemoveProject(name); err != nil {
			apiFail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("ui project removed", "operator", jam.OperatorID(r), "project", name)
		w.WriteHeader(http.StatusNoContent)
	})

	// edit runs one section write on {project} (a POST's JSON body decoded
	// into fields) and answers with the project, as registerProjectEdits does.
	type fields struct {
		YAML, Service, User, Delivery, Name, Connection, Ref, Category, Tiers string
		Add                                                                   bool
	}
	edit := func(what string, apply func(r *http.Request, project string, f fields) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var f fields
			if r.Method == http.MethodPost && !decodeJSON(w, r, &f) {
				return
			}
			project := r.PathValue("project")
			if _, ok := store.GetProject(project); !ok {
				writeAPIError(w, http.StatusNotFound, "not_found", "project "+`"`+project+`"`+" not found")
				return
			}
			if err := apply(r, project, f); err != nil {
				apiFail(w, err, http.StatusBadRequest)
				return
			}
			log.Info("ui project "+what, "operator", jam.OperatorID(r), "project", project)
			d, ok := projectDetailOf(store, src.img, msgs, src.marks(), project, r.URL.Query().Get("section") == string(sectionIntercom))
			if !ok {
				writeAPIError(w, http.StatusNotFound, "not_found", "project no longer exists")
				return
			}
			writeJSON(w, http.StatusOK, d)
		}
	}
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/context", edit("context set", func(_ *http.Request, project string, f fields) error {
		b, err := jam.ParseContextYAML([]byte(f.YAML), nil)
		if err != nil {
			return badRequest(err.Error())
		}
		return jam.SetProjectContextChecked(store, project, b)
	}))
	api.HandleFunc("DELETE "+apiPrefix+"projects/{project}/context", edit("context cleared", func(_ *http.Request, project string, _ fields) error {
		return jam.SetProjectContextChecked(store, project, jam.ContextBody{})
	}))
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/chat-service", edit("chat service set", func(_ *http.Request, project string, f fields) error {
		return store.SetChatService(project, strings.TrimSpace(f.Service))
	}))
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/members", edit("member put", func(_ *http.Request, project string, f fields) error {
		return putMember(store, project, f.User, f.Delivery, f.Add)
	}))
	api.HandleFunc("DELETE "+apiPrefix+"projects/{project}/members/{user}", edit("member removed", func(r *http.Request, project string, _ fields) error {
		return removeMember(store, project, r.PathValue("user"))
	}))
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/rooms", edit("room put", func(_ *http.Request, project string, f fields) error {
		return putRoom(store, project, f.Name, f.Connection, f.Ref)
	}))
	api.HandleFunc("DELETE "+apiPrefix+"projects/{project}/rooms/{name}", edit("room removed", func(r *http.Request, project string, _ fields) error {
		return registryErr(jam.RemoveRoom(store, project, r.PathValue("name")))
	}))
	api.HandleFunc("POST "+apiPrefix+"projects/{project}/escalation", edit("escalation set", func(_ *http.Request, project string, f fields) error {
		return setEscalation(store, project, f.Category, f.Tiers)
	}))
	api.HandleFunc("DELETE "+apiPrefix+"projects/{project}/escalation", edit("escalation cleared", func(r *http.Request, project string, _ fields) error {
		return store.SetEscalationPolicy(project, r.URL.Query().Get("category"), nil)
	}))
}
