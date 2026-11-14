package adminui

import (
	"cmp"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/aethons-tools/cove/internal/jam"
)

// The destinations and model-specs JSON API: the JSON twins of
// dest_detail.go and model_specs.go, through the same form readers and jam
// calls.

var specFields = map[string]string{"type": "type", "version": "version", "version-constraint": "versionConstraint", "principal": "principal",
	"model-id": "modelId", "effort": "effort", "mode": "mode", "provider": "provider", "headers": "headers", "allow": "allow", "deny": "deny",
	"provider-env": "providerEnv", "plugins": "plugins", "settings": "settings", "note": "note", "name": "name"}

func methodNames() []string {
	out := []string{}
	for _, m := range methods {
		out = append(out, string(m))
	}
	return out
}

func destDetailOf(store jam.Store, name string) (DestDetail, bool) {
	d, ok := buildDestDetail(store, name)
	if !ok {
		return DestDetail{}, false
	}
	out := DestDetail{Name: d.Dest.Name, Fields: destFieldsOf(d.Dest), Methods: methodNames(), Env: []EnvRow{}, EnvImplied: d.EnvImplied,
		GitRouted: d.GitRouted, GitImplied: d.GitImplied, Uses: []DestUse{}, Conflicts: []DestConflict{}, ConflictRoles: []string{},
		Notes: operatorNotesOf(d.Dest.OperatorNotes)}
	for _, e := range d.Env {
		out.Env = append(out.Env, EnvRow{Key: e.Key, Template: e.Template})
	}
	for _, u := range d.Uses {
		out.Uses = append(out.Uses, DestUse{Project: u.Project, Role: u.Role, Cred: u.Cred, CredIsRole: u.CredIsRole})
	}
	for _, c := range d.Conflicts {
		out.Conflicts = append(out.Conflicts, DestConflict{Project: c.Project, Role: c.Role, Other: c.Other, What: c.What})
	}
	out.ConflictRoles = append(out.ConflictRoles, d.ConflictRoles()...)
	return out, true
}

// destListRowOf is d's list row: its endpoints' addresses and distinct upstreams.
func destListRowOf(d destListRow, attn *AttnFlag) DestListRow {
	out := DestListRow{Name: d.Name, Endpoints: len(d.Endpoints), Addresses: []string{}, Upstreams: []string{}, CredName: d.CredName,
		EnvKeys: append([]string{}, d.EnvKeys...), GitRouted: d.GitRouted, UsedBy: d.UsedBy, Attn: attn}
	for _, e := range d.Endpoints {
		out.Addresses = append(out.Addresses, strings.TrimSpace(e.Subdomain+" "+e.Route))
		if !slices.Contains(out.Upstreams, e.Upstream) {
			out.Upstreams = append(out.Upstreams, e.Upstream)
		}
	}
	return out
}

// destLog is the audit log's summary of d's endpoints: each one's address and upstream.
func destLog(d jam.Destination) []string {
	out := make([]string, 0, len(d.Endpoints))
	for _, e := range d.Endpoints {
		out = append(out, strings.TrimSpace(e.Subdomain+" "+e.Route)+" → "+e.Upstream)
	}
	return out
}

func specFieldsOf(d specDetail) SpecFields {
	m := d.Spec
	return SpecFields{Type: string(m.Type), Version: m.Version, VersionConstraint: m.VersionConstraint, Principal: m.Principal.Credential,
		ModelID: m.Model.ID, Effort: m.Model.Effort, Mode: m.Policy.Mode, Provider: d.Provider, Headers: d.Form.Headers, Allow: d.Form.Allow,
		Deny: d.Form.Deny, ProviderEnv: d.Form.ProviderEnv, Plugins: d.Form.Plugins, Settings: d.Form.Settings, Note: m.Note}
}

func specChoicesOf(c specChoices) SpecChoices {
	out := SpecChoices{Types: []string{}, Principals: append([]string{}, c.Principals...), Modes: append([]string{}, c.Modes...), Providers: append([]string{}, c.Providers...)}
	for _, t := range c.Types {
		out.Types = append(out.Types, string(t))
	}
	return out
}

func specDetailOf(u specUI, m jam.ModelSpec) SpecDetail {
	d := u.detail(m)
	out := SpecDetail{Name: m.Name, Fields: specFieldsOf(d), Choices: specChoicesOf(d.Choices),
		HeaderRules: []HeaderRuleRow{}, Allow: append([]string{}, m.Policy.Allow...), Deny: append([]string{}, m.Policy.Deny...), HasClaude: m.Claude != nil,
		ProviderEnvKeys: []string{}, Plugins: []string{}, SettingsKeys: []string{}, Uses: []SpecUse{}, Notes: operatorNotesOf(m.OperatorNotes)}
	for _, r := range m.Principal.Headers {
		if r.EnsureListItem != "" {
			out.HeaderRules = append(out.HeaderRules, HeaderRuleRow{Name: r.Name, Op: "+=", Value: r.EnsureListItem})
		} else {
			out.HeaderRules = append(out.HeaderRules, HeaderRuleRow{Name: r.Name, Op: "=", Value: r.Set})
		}
	}
	if c := m.Claude; c != nil {
		out.ProviderEnvKeys = append(out.ProviderEnvKeys, slices.Sorted(maps.Keys(c.ProviderEnv))...)
		out.Plugins = append(out.Plugins, c.Plugins...)
		out.SettingsKeys = append(out.SettingsKeys, slices.Sorted(maps.Keys(c.Settings))...)
	}
	for _, x := range d.Uses {
		out.Uses = append(out.Uses, SpecUse{Project: x.Project, Role: x.Role, Implicit: x.Implicit})
	}
	return out
}

// createConflict adds the pages' hint to a create's 409.
func createConflict(err error) error {
	if status := jam.WriteStatus(err, http.StatusInternalServerError); status == http.StatusConflict {
		return &jam.WriteError{Status: status, Msg: err.Error() + "; edit it on its page"}
	}
	return err
}

func apiSpecs(api *http.ServeMux, src frameSource, u specUI, credExists func(string) bool, log *slog.Logger) {
	store := src.store
	api.HandleFunc("GET "+apiPrefix+"destinations", func(w http.ResponseWriter, r *http.Request) {
		items := src.items()
		out := DestinationsResponse{Destinations: []DestListRow{}, Methods: methodNames()}
		for _, d := range destListRows(store) {
			out.Destinations = append(out.Destinations, destListRowOf(d, attnFlag(items, "specs", "dest:"+d.Name)))
		}
		slices.SortFunc(out.Destinations, func(a, b DestListRow) int { return cmp.Compare(a.Name, b.Name) })
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("POST "+apiPrefix+"destinations", func(w http.ResponseWriter, r *http.Request) {
		var body DestFields
		if !decodeJSON(w, r, &body) {
			return
		}
		d, err := destOf(body, strings.TrimSpace(body.Name))
		if err == nil {
			err = jam.CreateDestination(store, d, credExists, u.subdomains)
		}
		if err != nil {
			apiFail(w, createConflict(err), http.StatusInternalServerError)
			return
		}
		log.Info("ui destination added", "operator", jam.OperatorID(r), "name", d.Name, "endpoints", destLog(d))
		writeJSON(w, http.StatusCreated, Created{ID: d.Name, Path: "config/destinations/" + url.PathEscape(d.Name)})
	})
	api.HandleFunc("GET "+apiPrefix+"destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := destDetailOf(store, r.PathValue("name"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such destination")
			return
		}
		writeJSON(w, http.StatusOK, d)
	})
	api.HandleFunc("POST "+apiPrefix+"destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		var body DestFields
		if !decodeJSON(w, r, &body) {
			return
		}
		d, err := destOf(body, r.PathValue("name"))
		if err == nil {
			err = jam.UpdateDestination(store, d, credExists, u.subdomains) // keeps the operator notes (their own write)
		}
		if err != nil {
			apiFail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("ui destination updated", "operator", jam.OperatorID(r), "name", d.Name, "endpoints", destLog(d))
		out, ok := destDetailOf(store, d.Name)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "destination no longer exists")
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("DELETE "+apiPrefix+"destinations/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveDestination(r.PathValue("name")); err != nil {
			writeAPIError(w, http.StatusNotFound, "not_found", err.Error())
			return
		}
		log.Info("ui destination removed", "operator", jam.OperatorID(r), "name", r.PathValue("name"))
		w.WriteHeader(http.StatusNoContent)
	})

	api.HandleFunc("GET "+apiPrefix+"model-specs", func(w http.ResponseWriter, r *http.Request) {
		seed := u.detail(jam.ModelSpec{Type: jam.HarnessClaude, Claude: &jam.ClaudeSpec{Provider: "anthropic"}})
		out := SpecsResponse{Specs: []SpecListRow{}, New: specFieldsOf(seed), Choices: specChoicesOf(seed.Choices)}
		for _, m := range store.ListModelSpecs() {
			row := SpecListRow{Name: m.Name, Type: string(m.Type), Version: m.Version, VersionConstraint: m.VersionConstraint, Principal: m.Principal.Credential,
				ModelID: m.Model.ID, Effort: m.Model.Effort, Mode: m.Policy.Mode, UsedBy: len(specUses(store, m.Name))}
			if m.Claude != nil {
				row.Provider = m.Claude.Provider
			}
			out.Specs = append(out.Specs, row)
		}
		slices.SortFunc(out.Specs, func(a, b SpecListRow) int { return cmp.Compare(a.Name, b.Name) })
		writeJSON(w, http.StatusOK, out)
	})
	api.HandleFunc("POST "+apiPrefix+"model-specs", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if !decodeJSON(w, r, &body) {
			return
		}
		m, err := specFromFields(formOf(body, specFields), strings.TrimSpace(body["name"]))
		if err == nil {
			err = jam.CreateModelSpec(store, m, u.credExists, u.pool)
		}
		if err != nil {
			apiFail(w, createConflict(err), http.StatusInternalServerError)
			return
		}
		log.Info("ui model-spec created", "operator", jam.OperatorID(r), "name", m.Name, "type", string(m.Type))
		writeJSON(w, http.StatusCreated, Created{ID: m.Name, Path: "config/model-specs/" + url.PathEscape(m.Name)})
	})
	api.HandleFunc("GET "+apiPrefix+"model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
		m, ok := store.GetModelSpec(r.PathValue("name"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "no such model-spec")
			return
		}
		writeJSON(w, http.StatusOK, specDetailOf(u, m))
	})
	api.HandleFunc("POST "+apiPrefix+"model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if !decodeJSON(w, r, &body) {
			return
		}
		m, err := specFromFields(formOf(body, specFields), r.PathValue("name"))
		if cur, ok := store.GetModelSpec(m.Name); ok {
			m.OperatorNotes = cur.OperatorNotes // not on the form: kept (the notes have their own write)
		}
		if err == nil {
			err = jam.UpdateModelSpec(store, m, u.credExists, u.pool)
		}
		if err != nil {
			apiFail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("ui model-spec updated", "operator", jam.OperatorID(r), "name", m.Name, "type", string(m.Type))
		stored, ok := store.GetModelSpec(m.Name)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "not_found", "model-spec no longer exists")
			return
		}
		writeJSON(w, http.StatusOK, specDetailOf(u, stored))
	})
	api.HandleFunc("DELETE "+apiPrefix+"model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if err := store.RemoveModelSpec(r.PathValue("name")); err != nil {
			status := jam.ModelSpecRemoveStatus(err)
			writeAPIError(w, status, apiCode(status), err.Error())
			return
		}
		log.Info("ui model-spec deleted", "operator", jam.OperatorID(r), "name", r.PathValue("name"))
		w.WriteHeader(http.StatusNoContent)
	})
}
