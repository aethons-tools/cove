package adminui

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

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
