package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

func destNamed(t *testing.T, store jam.Store, name string) jam.Destination {
	t.Helper()
	for _, d := range store.ListDestinations() {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no destination %s", name)
	return jam.Destination{}
}

func destBody(over map[string]string) map[string]string {
	b := map[string]string{"name": "gh", "route": "/gh/", "upstream": "https://api.github.com", "identityIn": "bearer", "apply": "bearer",
		"credName": "", "env": "GH_HOST={host}", "allowPaths": "", "note": "", "logHeadersIn": "none", "logHeadersOut": "none", "git": ""}
	for k, v := range over {
		b[k] = v
	}
	return b
}

func TestDestinationsAPI(t *testing.T) {
	h, store := adminHandler(t)
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations", destBody(nil), &c); rec.Code != 201 || c.Path != "config/destinations/gh" {
		t.Fatalf("create = %d %+v %s", rec.Code, c, rec.Body)
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/destinations", destBody(nil), nil)
	wantErr(t, rec, 409, "conflict")
	if !strings.Contains(rec.Body.String(), "; edit it on its page") {
		t.Errorf("409 = %s", rec.Body)
	}
	var list adminui.DestinationsResponse
	apiDo(t, h, "GET", "/app/admin/api/destinations", nil, &list)
	if len(list.Destinations) != 1 || strings.Join(list.Destinations[0].EnvKeys, ",") != "GH_HOST" || len(list.Methods) == 0 {
		t.Fatalf("list = %+v", list)
	}
	var d adminui.DestDetail
	apiDo(t, h, "POST", "/app/admin/api/destinations/gh", destBody(map[string]string{"note": "use gh", "git": "on"}), &d)
	if d.Fields.Note != "use gh" || !d.Fields.Git || !d.GitRouted || d.Fields.Env != "GH_HOST={host}" || d.Fields.LogHeadersIn != "none" {
		t.Errorf("edited = %+v", d)
	}
	// A custom identity-in spec survives a form save.
	dest := destNamed(t, store, "gh")
	dest.IdentityIn, dest.IdentityInSpec = jam.ApplyCustom, &jam.InboundSpec{Header: "X-Agent-Token"}
	if err := jam.UpdateDestination(store, dest, anyCred); err != nil {
		t.Fatal(err)
	}
	apiDo(t, h, "POST", "/app/admin/api/destinations/gh", destBody(map[string]string{"identityIn": "custom"}), &d)
	if after := destNamed(t, store, "gh"); after.IdentityInSpec == nil || d.IdentityInSpec == "" {
		t.Errorf("custom spec lost: %+v / %q", after.IdentityInSpec, d.IdentityInSpec)
	}
	// Two destinations in one role setting GH_HOST differently conflict.
	apiDo(t, h, "POST", "/app/admin/api/destinations", destBody(map[string]string{"name": "ghe", "route": "/ghe/", "upstream": "https://ghe.example.com", "env": "GH_HOST=ghe.example.com"}), nil)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "coder", Scope: jam.Scope{Destinations: []string{"gh", "ghe"}}}); err != nil {
		t.Fatal(err)
	}
	apiDo(t, h, "GET", "/app/admin/api/destinations/gh", nil, &d)
	if len(d.Conflicts) == 0 || strings.Join(d.ConflictRoles, ",") != "acme/coder" || len(d.Uses) != 1 {
		t.Errorf("conflicts = %+v roles %v uses %v", d.Conflicts, d.ConflictRoles, d.Uses)
	}
	if err := store.RemoveRole("acme", "coder"); err != nil {
		t.Fatal(err)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/destinations/ghe", nil, nil); rec.Code != 204 {
		t.Errorf("delete = %d %s", rec.Code, rec.Body)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/destinations/ghe", nil, nil), 404, "not_found")
}

func specBody(over map[string]string) map[string]string {
	b := map[string]string{"name": "fast", "type": "claude", "version": "2.1.3", "versionConstraint": "", "principal": "anthropic", "modelId": "", "effort": "",
		"mode": "", "provider": "anthropic", "headers": "", "allow": "", "deny": "", "providerEnv": "", "plugins": "", "settings": "", "note": ""}
	for k, v := range over {
		b[k] = v
	}
	return b
}

func TestModelSpecsAPI(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithCredentialNames("anthropic", "other"))
	var c adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/model-specs", specBody(nil), &c); rec.Code != 201 || c.Path != "config/model-specs/fast" {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/model-specs", specBody(nil), nil)
	wantErr(t, rec, 409, "conflict")
	var d adminui.SpecDetail
	apiDo(t, h, "GET", "/app/admin/api/model-specs/fast", nil, &d)
	if strings.Join(d.Choices.Principals, ",") != "anthropic,other" || d.Fields.Principal != "anthropic" || !d.HasClaude {
		t.Fatalf("detail = %+v", d)
	}
	apiDo(t, h, "POST", "/app/admin/api/model-specs/fast", specBody(map[string]string{"headers": "anthropic-beta += oauth-1", "allow": "Bash(go test:*)", "providerEnv": "CLOUD_ML_REGION=us-east5"}), &d)
	if len(d.HeaderRules) != 1 || d.HeaderRules[0].Op != "+=" || strings.Join(d.Allow, ",") != "Bash(go test:*)" || strings.Join(d.ProviderEnvKeys, ",") != "CLOUD_ML_REGION" {
		t.Errorf("edited = %+v", d)
	}
	rec = apiDo(t, h, "POST", "/app/admin/api/model-specs/fast", specBody(map[string]string{"providerEnv": "oops-secret"}), nil)
	wantErr(t, rec, 400, "bad_request")
	if b := rec.Body.String(); !strings.Contains(b, "provider-env line 1 is not KEY=VALUE") || strings.Contains(b, "oops-secret") {
		t.Errorf("provider-env refusal = %s", b)
	}
	rec = apiDo(t, h, "POST", "/app/admin/api/model-specs/fast", specBody(map[string]string{"settings": "[]"}), nil)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "claude.settings is not a JSON object") {
		t.Errorf("settings = %d %s", rec.Code, rec.Body)
	}
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "coder", ModelSpec: "fast"}); err != nil {
		t.Fatal(err)
	}
	wantErr(t, apiDo(t, h, "DELETE", "/app/admin/api/model-specs/fast", nil, nil), 409, "conflict")
	// A pasted secret with "=" in it is no KEY=VALUE line; the refusal names the line only.
	rec = apiDo(t, h, "POST", "/app/admin/api/model-specs/fast", specBody(map[string]string{"providerEnv": "sk-ant-api03-SECRETXYZ=="}), nil)
	wantErr(t, rec, 400, "bad_request")
	if strings.Contains(rec.Body.String(), "SECRETXYZ") {
		t.Errorf("the pasted value was echoed: %s", rec.Body)
	}
	apiDo(t, h, "GET", "/app/admin/api/model-specs/fast", nil, &d)
	if len(d.Uses) != 1 || d.Uses[0].Implicit {
		t.Errorf("uses = %+v", d.Uses)
	}
	var list adminui.SpecsResponse
	apiDo(t, h, "GET", "/app/admin/api/model-specs", nil, &list)
	if len(list.Specs) == 0 || list.New.Provider != "anthropic" || list.New.Type != "claude" {
		t.Errorf("list = %+v", list)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/model-specs/nope", nil, nil), 404, "not_found")
	apiDo(t, h, "POST", "/app/admin/api/model-specs", specBody(map[string]string{"name": "spare"}), nil)
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/model-specs/spare", nil, nil); rec.Code != 204 {
		t.Errorf("deleting an unused spec = %d %s", rec.Code, rec.Body)
	}
}

func TestModelSpecStalePrincipal(t *testing.T) {
	store := newStore(t)
	h := adminui.Handler(store, testLogger(), nil, nil, anyCred, nil, adminui.WithCredentialNames("anthropic"))
	apiDo(t, h, "POST", "/app/admin/api/model-specs", specBody(map[string]string{"principal": "retired"}), nil)
	var d adminui.SpecDetail
	apiDo(t, h, "GET", "/app/admin/api/model-specs/fast", nil, &d)
	if strings.Join(d.Choices.Principals, ",") != "anthropic,retired" || d.Fields.Principal != "retired" {
		t.Errorf("choices = %v principal %q", d.Choices.Principals, d.Fields.Principal)
	}
}
