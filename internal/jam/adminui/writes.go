package adminui

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/browserauth"
)

// originGuard returns the CSRF Origin check for state-changing requests: it
// writes a 403 and returns false when the request must be refused.
func originGuard(trustedOrigins []string) func(http.ResponseWriter, *http.Request) bool {
	trusted := map[string]bool{}
	for _, o := range trustedOrigins {
		trusted[o] = true
	}
	return func(w http.ResponseWriter, r *http.Request) bool {
		if !browserauth.SameOrigin(r, trusted) {
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

// createRole creates a role in project from the New role form's fields (get
// reads one by its form name: name, destinations, ttl-seconds, kit,
// model-spec). Create only: an existing role is a 409 that says to edit it on
// its page.
func createRole(store jam.Store, credExists func(string) bool, project string, get func(string) string) error {
	name := strings.TrimSpace(get("name"))
	if name == "" {
		return badRequest("name is required")
	}
	kit := strings.TrimSpace(get("kit"))
	if kit != "" {
		if _, ok := store.GetKit(kit); !ok {
			return badRequest("kit does not exist")
		}
	}
	ttl := 0
	if v := strings.TrimSpace(get("ttl-seconds")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return badRequest("ttl-seconds must be an integer")
		}
		ttl = n
	}
	dests, creds, err := jam.ParseDestinations(get("destinations"))
	if err != nil {
		return badRequest(err.Error())
	}
	role := jam.Role{
		Name: name,
		Scope: jam.Scope{
			Destinations: dests,
			Credentials:  creds,
			TTL:          time.Duration(ttl) * time.Second,
		},
		Kit:       kit,
		ModelSpec: strings.TrimSpace(get("model-spec")), // CreateRole checks it exists
	}
	if err := jam.ValidateCredentials(role.Scope, credExists); err != nil {
		return badRequest(err.Error())
	}
	if err := jam.CreateRole(store, project, role); err != nil {
		status := jam.WriteStatus(err, http.StatusBadRequest)
		msg := err.Error()
		if status == http.StatusConflict {
			msg += "; edit it on its page"
		}
		return &jam.WriteError{Status: status, Msg: msg}
	}
	return nil
}

// enrollActor enrolls id with a grant of project/role (dests: destination
// overrides, name=cred,…) and returns its identity token — shown once, never
// logged.
func enrollActor(store jam.Store, credExists func(string) bool, id, project, role, dests string) (string, error) {
	if id == "" {
		return "", badRequest("id is required")
	}
	if role == "" {
		return "", badRequest("role is required")
	}
	overrides, err := overridesFrom(store, project, role, dests, credExists)
	if err != nil {
		return "", badRequest(err.Error())
	}
	token, err := jam.Enroll(store, id, project, role, overrides, time.Now())
	if err != nil {
		return "", badRequest(err.Error())
	}
	return token, nil
}

// addActorGrant grants actor id project/role, with dests as overrides.
func addActorGrant(store jam.Store, credExists func(string) bool, id, project, role, dests string) error {
	if role == "" {
		return badRequest("role is required")
	}
	overrides, err := overridesFrom(store, project, role, dests, credExists)
	if err != nil {
		return badRequest(err.Error())
	}
	if err := store.AddGrant(id, jam.Grant{Project: project, Role: role, Overrides: overrides}); err != nil {
		return &jam.WriteError{Status: http.StatusNotFound, Msg: err.Error()}
	}
	return nil
}

// raiseManual raises a manual studio labelled label and returns its session
// id. The identity token and launch secret RaiseManual returns are dropped:
// with a real launcher Jam consumes them; they never reach a browser or log.
func raiseManual(ctx context.Context, store jam.Store, sup *jam.Supervisor, label, project, role, unit, prompt string) (string, error) {
	if label == "" {
		return "", badRequest("id is required")
	}
	if role == "" {
		return "", badRequest("role is required")
	}
	_, sid, _, _, err := jam.RaiseManual(ctx, store, sup, label, jam.RaiseSpec{Project: project, Role: role, Unit: unit, Prompt: prompt})
	return sid, err
}

func registerWrites(mux *http.ServeMux, store jam.Store, log *slog.Logger, sup *jam.Supervisor, credExists func(string) bool, guardWrite func(http.ResponseWriter, *http.Request) bool) {
	mux.HandleFunc("POST /app/admin/enrollments", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		id, project, role := strings.TrimSpace(r.FormValue("id")), strings.TrimSpace(r.FormValue("project")), strings.TrimSpace(r.FormValue("role"))
		token, err := enrollActor(store, credExists, id, project, role, r.FormValue("destinations"))
		if err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return
		}
		log.Info("ui enrolled", "operator", jam.OperatorID(r), "id", id, "project", project, "role", role)
		renderFragment(w, r, "agents", "enroll-result", map[string]any{"ID": id, "Token": token})
	})

	mux.HandleFunc("DELETE /app/admin/enrollments/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		id := r.PathValue("id")
		if err := store.RemoveActor(id); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui revoked", "operator", jam.OperatorID(r), "id", id)
		// The agent page navigates to the agents list on success.
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /app/admin/roles", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		project, name := strings.TrimSpace(r.FormValue("project")), strings.TrimSpace(r.FormValue("name"))
		if err := createRole(store, credExists, project, r.FormValue); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return
		}
		// The new role's page is where it is edited; htmx follows the redirect.
		w.Header().Set("HX-Redirect", roleURL(project, name))
		log.Info("ui role created", "operator", jam.OperatorID(r), "project", orDefaultProject(project), "role", name)
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /app/admin/actors/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "invalid form")
			return
		}
		project, role := strings.TrimSpace(r.FormValue("project")), strings.TrimSpace(r.FormValue("role"))
		if err := addActorGrant(store, credExists, r.PathValue("id"), project, role, r.FormValue("destinations")); err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return
		}
		log.Info("ui grant added", "operator", jam.OperatorID(r), "id", r.PathValue("id"), "project", orDefaultProject(project), "role", role)
		// The agent page reloads on success.
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("DELETE /app/admin/actors/{id}/grants/{project}/{role}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if err := store.RemoveGrant(r.PathValue("id"), r.PathValue("project"), r.PathValue("role")); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui grant removed", "operator", jam.OperatorID(r), "id", r.PathValue("id"), "project", r.PathValue("project"), "role", r.PathValue("role"))
		// The agent page reloads on success.
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("DELETE /app/admin/roles/{project}/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		project, name := r.PathValue("project"), r.PathValue("name")
		if err := store.RemoveRole(project, name); err != nil {
			renderError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Info("ui role removed", "operator", jam.OperatorID(r), "project", project, "role", name)
		// The role page navigates to its project's roles on success.
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /app/admin/coves", func(w http.ResponseWriter, r *http.Request) {
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
		id, project, role := strings.TrimSpace(r.FormValue("id")), strings.TrimSpace(r.FormValue("project")), strings.TrimSpace(r.FormValue("role"))
		sid, err := raiseManual(r.Context(), store, sup, id, project, role, strings.TrimSpace(r.FormValue("unit")), r.FormValue("prompt"))
		if err != nil {
			renderError(w, jam.WriteStatus(err, http.StatusBadRequest), err.Error())
			return
		}
		log.Info("ui cove raised", "operator", jam.OperatorID(r), "id", sid, "label", id, "project", orDefaultProject(project), "role", role)
		renderFragment(w, r, "agents", "agents-table", newAgentsData(store, sup, currentStatus(r), true))
	})

	mux.HandleFunc("DELETE /app/admin/coves/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !guardWrite(w, r) {
			return
		}
		if sup == nil {
			http.Error(w, "runtime supervisor not configured", http.StatusServiceUnavailable)
			return
		}
		id := jam.ResolveSession(store, r.PathValue("id"))
		if err := sup.Teardown(r.Context(), id); err != nil {
			renderError(w, http.StatusInternalServerError, err.Error())
			return
		}
		log.Info("ui cove torn down", "operator", jam.OperatorID(r), "id", id)
		// Pages that offer Teardown reload on success.
		w.WriteHeader(http.StatusOK)
	})

}

// currentStatus is the status filter of the Agents page the request came from
// (htmx sends the page's URL as HX-Current-URL), so a refreshed table keeps
// its filter; "" when the header is missing or unparseable.
func currentStatus(r *http.Request) string {
	u, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		return ""
	}
	return statusFilter(&http.Request{URL: u})
}
