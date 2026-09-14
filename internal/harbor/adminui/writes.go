package adminui

import (
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

// sameOrigin reports whether a state-changing request's Origin (or, absent that,
// Referer) host matches the request Host. Fail-closed: neither header → false.
func sameOrigin(r *http.Request) bool {
	check := func(v string) (bool, bool) {
		if v == "" {
			return false, false
		}
		u, err := url.Parse(v)
		return err == nil && u.Host == r.Host, true
	}
	if ok, present := check(r.Header.Get("Origin")); present {
		return ok
	}
	if ok, present := check(r.Header.Get("Referer")); present {
		return ok
	}
	return false
}

// guardWrite enforces the CSRF Origin check; it writes a 403 and returns false
// when the request must be refused.
func guardWrite(w http.ResponseWriter, r *http.Request) bool {
	if !sameOrigin(r) {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return false
	}
	return true
}

// splitCSV parses a comma-separated form field into a trimmed, non-empty slice.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// overridesFrom builds a *harbor.Override from optional comma-separated fields,
// or nil when both are empty (inherit the role's scope).
func overridesFrom(dests, repos string) *harbor.Override {
	d, rp := splitCSV(dests), splitCSV(repos)
	if len(d) == 0 && len(rp) == 0 {
		return nil
	}
	return &harbor.Override{Destinations: d, Repos: rp}
}

// orDefaultProject normalizes an empty project to harbor.DefaultProject for
// audit logging, mirroring the JSON admin API's orDefaultProject.
func orDefaultProject(p string) string {
	if p == "" {
		return harbor.DefaultProject
	}
	return p
}

// renderError renders an inline error fragment with the given status.
func renderError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// Escaped by html/template's HTMLEscapeString via a tiny inline render.
	_, _ = w.Write([]byte(`<p class="error">` + template.HTMLEscapeString(msg) + `</p>`))
}

func registerWrites(mux *http.ServeMux, store harbor.Store, log *slog.Logger, sup *harbor.Supervisor, credExists func(string) bool) {
	mux.HandleFunc("POST /ui/enrollments", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		id := strings.TrimSpace(r.FormValue("id"))
		if id == "" {
			renderError(w, http.StatusBadRequest, "id is required")
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			renderError(w, http.StatusBadRequest, "role is required")
			return
		}
		project := strings.TrimSpace(r.FormValue("project"))
		overrides := overridesFrom(r.FormValue("destinations"), r.FormValue("repos"))
		token, err := harbor.Enroll(store, id, project, role, overrides, time.Now())
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui enrolled", "operator", harbor.OperatorID(r), "id", id, "project", project, "role", role)
		renderFragment(w, "roster", "enroll-result", map[string]any{"ID": id, "Token": token})
	})

	mux.HandleFunc("DELETE /ui/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		id := r.PathValue("id")
		if err := store.RemoveActor(id); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui revoked", "operator", harbor.OperatorID(r), "id", id)
		renderFragment(w, "roster", "roster-table", map[string]any{"Actors": harbor.RosterSummaries(store)})
	})

	mux.HandleFunc("POST /ui/roles", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			renderError(w, http.StatusBadRequest, "name is required")
			return
		}
		kit := strings.TrimSpace(r.FormValue("kit"))
		if kit != "" {
			if _, ok := store.GetKit(kit); !ok {
				renderError(w, http.StatusBadRequest, "kit does not exist")
				return
			}
		}
		ttl := 0
		if v := strings.TrimSpace(r.FormValue("ttl-seconds")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				renderError(w, http.StatusBadRequest, "ttl-seconds must be an integer")
				return
			}
			ttl = n
		}
		project := strings.TrimSpace(r.FormValue("project"))
		role := harbor.Role{
			Name: name,
			Scope: harbor.Scope{
				Destinations: splitCSV(r.FormValue("destinations")),
				Repos:        splitCSV(r.FormValue("repos")),
				TTL:          time.Duration(ttl) * time.Second,
			},
			Kit: kit,
		}
		if err := store.PutRole(project, role); err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui role put", "operator", harbor.OperatorID(r), "project", orDefaultProject(project), "role", name)
		renderFragment(w, "roles", "roles-table", map[string]any{"Roles": roleRows(store)})
	})

	mux.HandleFunc("POST /ui/actors/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			renderError(w, http.StatusBadRequest, "role is required")
			return
		}
		project := strings.TrimSpace(r.FormValue("project"))
		g := harbor.Grant{Project: project, Role: role, Overrides: overridesFrom(r.FormValue("destinations"), r.FormValue("repos"))}
		if err := store.AddGrant(r.PathValue("id"), g); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui grant added", "operator", harbor.OperatorID(r), "id", r.PathValue("id"), "project", orDefaultProject(project), "role", role)
		renderFragment(w, "roster", "roster-table", map[string]any{"Actors": harbor.RosterSummaries(store)})
	})

	mux.HandleFunc("DELETE /ui/actors/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := store.RemoveGrant(r.PathValue("id"), r.PathValue("project"), r.PathValue("role")); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui grant removed", "operator", harbor.OperatorID(r), "id", r.PathValue("id"), "project", r.PathValue("project"), "role", r.PathValue("role"))
		renderFragment(w, "roster", "roster-table", map[string]any{"Actors": harbor.RosterSummaries(store)})
	})

	mux.HandleFunc("DELETE /ui/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		project, name := r.PathValue("project"), r.PathValue("name")
		if err := store.RemoveRole(project, name); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui role removed", "operator", harbor.OperatorID(r), "project", project, "role", name)
		renderFragment(w, "roles", "roles-table", map[string]any{"Roles": roleRows(store)})
	})

	mux.HandleFunc("POST /ui/coves", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		id := strings.TrimSpace(r.FormValue("id"))
		if id == "" {
			renderError(w, http.StatusBadRequest, "id is required")
			return
		}
		role := strings.TrimSpace(r.FormValue("role"))
		if role == "" {
			renderError(w, http.StatusBadRequest, "role is required")
			return
		}
		project := strings.TrimSpace(r.FormValue("project"))
		// Discard the returned identity token + launch secret: with a real launcher
		// harbor consumes them internally; they must never reach the browser or a log.
		_, _, _, err := sup.Raise(r.Context(), harbor.RaiseSpec{
			ActorID: id, Project: project, Role: role,
			Unit: strings.TrimSpace(r.FormValue("unit")), Prompt: r.FormValue("prompt"),
		})
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui cove raised", "operator", harbor.OperatorID(r), "id", id, "project", orDefaultProject(project), "role", role)
		renderFragment(w, "coves", "coves-table", map[string]any{"Coves": harbor.CoveSummaries(store), "CanEdit": true})
	})

	mux.HandleFunc("DELETE /ui/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		id := r.PathValue("id")
		if err := sup.Teardown(r.Context(), id); err != nil {
			renderError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Info("ui cove torn down", "operator", harbor.OperatorID(r), "id", id)
		renderFragment(w, "coves", "coves-table", map[string]any{"Coves": harbor.CoveSummaries(store), "CanEdit": true})
	})

	mux.HandleFunc("POST /ui/kits", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		config := r.FormValue("config")
		if name == "" || config == "" {
			renderError(w, http.StatusBadRequest, "name and config are required")
			return
		}
		v, err := store.PushKit(name, config)
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui kit pushed", "operator", harbor.OperatorID(r), "kit", name, "version", v)
		renderFragment(w, "kits", "kits-table", map[string]any{"Kits": store.ListKits()})
	})

	mux.HandleFunc("POST /ui/kits/{name}/pin", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("version")))
		if err != nil {
			renderError(w, http.StatusBadRequest, "version must be an integer")
			return
		}
		if err := store.PinKit(r.PathValue("name"), v); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui kit pinned", "operator", harbor.OperatorID(r), "kit", r.PathValue("name"), "version", v)
		renderFragment(w, "kits", "kits-table", map[string]any{"Kits": store.ListKits()})
	})

	mux.HandleFunc("DELETE /ui/kits/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		name := r.PathValue("name")
		if project, role, ok := store.RoleReferencingKit(name); ok {
			renderError(w, http.StatusConflict, fmt.Sprintf("kit %q is referenced by role %s/%s", name, project, role))
			return
		}
		if err := store.RemoveKit(name); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui kit removed", "operator", harbor.OperatorID(r), "kit", name)
		renderFragment(w, "kits", "kits-table", map[string]any{"Kits": store.ListKits()})
	})

	mux.HandleFunc("POST /ui/destinations", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		d := harbor.Destination{
			Name:       strings.TrimSpace(r.FormValue("name")),
			Route:      strings.TrimSpace(r.FormValue("route")),
			Upstream:   strings.TrimSpace(r.FormValue("upstream")),
			IdentityIn: harbor.ApplyMethod(strings.TrimSpace(r.FormValue("identity-in"))),
			CredName:   strings.TrimSpace(r.FormValue("cred-name")),
			Apply:      harbor.ApplyMethod(strings.TrimSpace(r.FormValue("apply"))),
			RepoScoped: r.FormValue("repo-scoped") != "",
		}
		if d.Name == "" || d.Route == "" || d.Upstream == "" {
			renderError(w, http.StatusBadRequest, "name, route and upstream are required")
			return
		}
		if d.CredName != "" && !credExists(d.CredName) {
			renderError(w, http.StatusBadRequest, "cred-name does not resolve to a configured credential")
			return
		}
		if err := store.AddDestination(d); err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui destination added", "operator", harbor.OperatorID(r), "name", d.Name, "route", d.Route, "upstream", d.Upstream)
		renderFragment(w, "destinations", "destinations-table", map[string]any{"Destinations": store.ListDestinations()})
	})

	mux.HandleFunc("DELETE /ui/destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := store.RemoveDestination(r.PathValue("name")); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui destination removed", "operator", harbor.OperatorID(r), "name", r.PathValue("name"))
		renderFragment(w, "destinations", "destinations-table", map[string]any{"Destinations": store.ListDestinations()})
	})
}
