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

	"github.com/aethons-tools/cove/internal/jam"
)

// sameOrigin reports whether a state-changing request's Origin (or, absent that,
// Referer) is the request's own Host, or exactly one of the trusted extra
// origins (scheme://host[:port], e.g. a dev proxy fronting the admin listener).
// Fail-closed: neither header → false.
func sameOrigin(r *http.Request, trusted map[string]bool) bool {
	check := func(v string) (bool, bool) {
		if v == "" {
			return false, false
		}
		u, err := url.Parse(v)
		if err != nil {
			return false, true
		}
		return u.Host == r.Host || trusted[u.Scheme+"://"+u.Host], true
	}
	if ok, present := check(r.Header.Get("Origin")); present {
		return ok
	}
	if ok, present := check(r.Header.Get("Referer")); present {
		return ok
	}
	return false
}

// originGuard returns the CSRF Origin check for state-changing requests: it
// writes a 403 and returns false when the request must be refused.
func originGuard(trustedOrigins []string) func(http.ResponseWriter, *http.Request) bool {
	trusted := map[string]bool{}
	for _, o := range trustedOrigins {
		trusted[o] = true
	}
	return func(w http.ResponseWriter, r *http.Request) bool {
		if !sameOrigin(r, trusted) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return false
		}
		return true
	}
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

// overridesFrom builds a *jam.Override from an optional destinations field in
// jam.ParseDestinations syntax ("git=cred,anthropic"), or nil when empty
// (inherit the role's scope). Mapped credentials are validated against the
// effective scope over the grant's role, so mapping credentials onto a role
// that doesn't exist is rejected (as the admin API does).
func overridesFrom(store jam.Store, project, role, dests string, credExists func(string) bool) (*jam.Override, error) {
	d, creds, err := jam.ParseDestinations(dests)
	if err != nil {
		return nil, err
	}
	if len(d) == 0 {
		return nil, nil
	}
	o := &jam.Override{Destinations: d, Credentials: creds}
	if creds == nil {
		return o, nil
	}
	r, ok := store.GetRole(orDefaultProject(project), role)
	if !ok {
		return nil, fmt.Errorf("role %q not found in project %q", role, orDefaultProject(project))
	}
	if err := jam.ValidateCredentials(jam.EffectiveScope(jam.Grant{Overrides: o}, r), credExists); err != nil {
		return nil, err
	}
	return o, nil
}

// orDefaultProject normalizes an empty project to jam.DefaultProject for
// audit logging, mirroring the JSON admin API's orDefaultProject.
func orDefaultProject(p string) string {
	if p == "" {
		return jam.DefaultProject
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

func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *jam.Supervisor, credExists func(string) bool, guardWrite func(http.ResponseWriter, *http.Request) bool) {
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
		overrides, err := overridesFrom(store, project, role, r.FormValue("destinations"), credExists)
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		token, err := jam.Enroll(store, id, project, role, overrides, time.Now())
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui enrolled", "operator", jam.OperatorID(r), "id", id, "project", project, "role", role)
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
		log.Info("ui revoked", "operator", jam.OperatorID(r), "id", id)
		renderFragment(w, "roster", "roster-table", rosterData(store))
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
		dests, creds, err := jam.ParseDestinations(r.FormValue("destinations"))
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		role := jam.Role{
			Name: name,
			Scope: jam.Scope{
				Destinations: dests,
				Credentials:  creds,
				TTL:          time.Duration(ttl) * time.Second,
			},
			Kit:       kit,
			ModelSpec: strings.TrimSpace(r.FormValue("model-spec")), // CreateRole checks it exists
		}
		if err := jam.ValidateCredentials(role.Scope, credExists); err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Create only: an existing role is edited section by section on its page.
		if err := jam.CreateRole(store, project, role); err != nil {
			msg := err.Error()
			if jam.WriteStatus(err, 0) == http.StatusConflict {
				msg += "; edit it on its page"
			}
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), msg)
			return
		}
		w.Header().Set("HX-Redirect", roleURL(project, name))
		log.Info("ui role created", "operator", jam.OperatorID(r), "project", orDefaultProject(project), "role", name)
		renderFragment(w, "roles", "roles-table", rolesData(store, sup != nil))
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
		overrides, err := overridesFrom(store, project, role, r.FormValue("destinations"), credExists)
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		g := jam.Grant{Project: project, Role: role, Overrides: overrides}
		if err := store.AddGrant(r.PathValue("id"), g); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui grant added", "operator", jam.OperatorID(r), "id", r.PathValue("id"), "project", orDefaultProject(project), "role", role)
		renderFragment(w, "roster", "roster-table", rosterData(store))
	})

	mux.HandleFunc("DELETE /ui/actors/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := store.RemoveGrant(r.PathValue("id"), r.PathValue("project"), r.PathValue("role")); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui grant removed", "operator", jam.OperatorID(r), "id", r.PathValue("id"), "project", r.PathValue("project"), "role", r.PathValue("role"))
		renderFragment(w, "roster", "roster-table", rosterData(store))
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
		log.Info("ui role removed", "operator", jam.OperatorID(r), "project", project, "role", name)
		renderFragment(w, "roles", "roles-table", rolesData(store, sup != nil))
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
		// Jam consumes them internally; they must never reach the browser or a log.
		_, _, _, err := sup.Raise(r.Context(), jam.RaiseSpec{
			ActorID: id, Project: project, Role: role,
			Unit: strings.TrimSpace(r.FormValue("unit")), Prompt: r.FormValue("prompt"),
		})
		if err != nil {
			renderError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Info("ui cove raised", "operator", jam.OperatorID(r), "id", id, "project", orDefaultProject(project), "role", role)
		renderFragment(w, "coves", "coves-table", covesData(store, true))
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
		log.Info("ui cove torn down", "operator", jam.OperatorID(r), "id", id)
		renderFragment(w, "coves", "coves-table", covesData(store, true))
	})

}
