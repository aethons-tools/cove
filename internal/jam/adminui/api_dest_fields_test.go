package adminui_test

import (
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// seedDestinations stores the legacy anthropic and git destinations, the gh
// pair with env, and gh-alt whose GH_HOST conflicts with github-api. Role dev
// holds github-api and gh-alt (a conflict); ops holds github-api and anthropic.
func seedDestinations(t *testing.T) jam.Store {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	for _, d := range []jam.Destination{
		{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: jam.ApplyBearer, CredName: "anth-key", Apply: jam.ApplyBearer},
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: jam.ApplyBasicPassword, CredName: "git-pat", Apply: jam.ApplyBasicPassword},
		{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, CredName: "gh-pat", Apply: jam.ApplyBearer,
			Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"}},
		{Name: "gh-alt", Route: "/alt/", Upstream: "https://alt.example", Env: map[string]string{"GH_HOST": "{base}"}},
	} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []jam.Role{
		{Name: "dev", Scope: jam.Scope{Destinations: []string{"github-api", "gh-alt"}, Credentials: map[string]string{"github-api": "gh-pat-acme"}}},
		{Name: "ops", Scope: jam.Scope{Destinations: []string{"github-api", "anthropic"}}},
	} {
		if err := store.PutRole("acme", r); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// The list counts each destination's roles and lists its env keys, a legacy
// route's implied ones included; the detail shows env, uses (with a role's
// own credential) and conflicts, and prefills the edit fields.
func TestDestinationsAPIViews(t *testing.T) {
	h := adminui.Handler(seedDestinations(t), testLogger(), nil, nil, anyCred, nil)
	var list adminui.DestinationsResponse
	apiDo(t, h, "GET", "/app/admin/api/destinations", nil, &list)
	rows := map[string]adminui.DestListRow{}
	var names []string
	for _, r := range list.Destinations {
		rows[r.Name] = r
		names = append(names, r.Name)
	}
	if strings.Join(names, ",") != "anthropic,gh-alt,git,github-api" {
		t.Errorf("list order = %v", names)
	}
	if r := rows["github-api"]; r.UsedBy != 2 || r.CredName != "gh-pat" || r.IdentityIn != "bearer" || r.Apply != "bearer" || strings.Join(r.EnvKeys, ",") != "GH_ENTERPRISE_TOKEN,GH_HOST" {
		t.Errorf("github-api row = %+v", r)
	}
	if r := rows["anthropic"]; r.UsedBy != 1 || !strings.Contains(strings.Join(r.EnvKeys, ","), "ANTHROPIC_BASE_URL") {
		t.Errorf("anthropic row = %+v", r)
	}
	if r := rows["git"]; r.UsedBy != 0 || !r.GitRouted {
		t.Errorf("git row = %+v", r)
	}

	var d adminui.DestDetail
	apiDo(t, h, "GET", "/app/admin/api/destinations/github-api", nil, &d)
	if d.Fields.Env != "GH_ENTERPRISE_TOKEN={token}\nGH_HOST={host}" || d.Fields.Upstream != "https://api.github.com" || d.Fields.CredName != "gh-pat" || d.EnvImplied {
		t.Errorf("fields = %+v", d.Fields)
	}
	if len(d.Uses) != 2 || d.Uses[0] != (adminui.DestUse{Project: "acme", Role: "dev", Cred: "gh-pat-acme", CredIsRole: true}) || d.Uses[1] != (adminui.DestUse{Project: "acme", Role: "ops"}) {
		t.Errorf("uses = %+v", d.Uses)
	}
	if strings.Join(d.ConflictRoles, ",") != "acme/dev" || len(d.Conflicts) != 1 || d.Conflicts[0].Other != "gh-alt" || d.Conflicts[0].What != "both set GH_HOST (to different values)" {
		t.Errorf("conflicts = %+v", d.Conflicts)
	}
	apiDo(t, h, "GET", "/app/admin/api/destinations/anthropic", nil, &d)
	env := map[string]string{}
	for _, e := range d.Env {
		env[e.Key] = e.Template
	}
	if !d.EnvImplied || env["ANTHROPIC_BASE_URL"] != "{base}/anthropic" || d.Fields.Env != "" {
		t.Errorf("anthropic implied env = %+v %v", d, env)
	}
	apiDo(t, h, "GET", "/app/admin/api/destinations/git", nil, &d)
	if !d.GitRouted || !d.GitImplied || d.Fields.Git {
		t.Errorf("git implied routing = %+v", d)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/destinations/nope", nil, nil), 404, "not_found")
}

// Every field is read from the body: env one KEY=TEMPLATE per line (blank
// lines skipped), allow-paths one per line, header logging "none" stored as
// unset; a bad env line, a duplicate key, a bad pattern, a bad logging value
// or an unknown credential is refused and nothing is stored.
func TestDestinationsAPIFields(t *testing.T) {
	e := editOn(t, editSetup{cred: credOK})
	h := e.H
	const pats = "/v1/projects/p1/models/*:rawPredict\r\n\r\n  /v1/projects/p1/models/*:streamRawPredict  \n"
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations", map[string]string{
		"name": " gh ", "route": " /api/v3/ ", "upstream": "https://api.github.com", "identityIn": "bearer", "credName": "known-cred", "apply": "bearer",
		"env": "GH_HOST = {host}\r\n\r\nGH_ENTERPRISE_TOKEN={token}\n", "allowPaths": pats, "git": "on", "note": " use git over https ",
		"logHeadersIn": "examined", "logHeadersOut": "none",
	}, nil); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	d := destNamed(t, e.Store(), "gh")
	if d.Route != "/api/v3/" || d.Env["GH_HOST"] != "{host}" || d.Env["GH_ENTERPRISE_TOKEN"] != "{token}" || len(d.Env) != 2 || !d.Git || d.Note != "use git over https" ||
		d.CredName != "known-cred" || d.IdentityIn != jam.ApplyBearer || d.LogHeadersIn != jam.HeaderLogExamined || d.LogHeadersOut != "" {
		t.Fatalf("stored = %+v", d)
	}
	if len(d.AllowPaths) != 2 || d.AllowPaths[0] != "/v1/projects/p1/models/*:rawPredict" || d.AllowPaths[1] != "/v1/projects/p1/models/*:streamRawPredict" {
		t.Fatalf("allow paths = %q", d.AllowPaths)
	}
	var dd adminui.DestDetail
	apiDo(t, h, "GET", "/app/admin/api/destinations/gh", nil, &dd)
	if dd.Fields.AllowPaths != strings.Join(d.AllowPaths, "\n") || dd.Fields.LogHeadersIn != "examined" || dd.Fields.LogHeadersOut != "none" || len(dd.AllowPaths) != 2 {
		t.Errorf("detail fields = %+v", dd.Fields)
	}

	for what, over := range map[string]map[string]string{
		"no equals":       {"env": "GH_HOST"},
		"empty key":       {"env": "=x"},
		"reserved":        {"env": "JAM_X=1"},
		"bad placeholder": {"env": "A={nope}"},
		"duplicate":       {"env": "A=1\nA=2"},
		"relative path":   {"allowPaths": "relative/path"},
		"bad logging":     {"logHeadersOut": "changed-raw"},
		"unknown cred":    {"credName": "ghost"},
	} {
		b := map[string]string{"name": "n", "route": "/n/", "upstream": "https://n"}
		for k, v := range over {
			b[k] = v
		}
		rec := apiDo(t, h, "POST", "/app/admin/api/destinations", b, nil)
		if rec.Code != 400 {
			t.Errorf("%s = %d %s, want 400", what, rec.Code, rec.Body)
		}
	}
	rec := apiDo(t, h, "POST", "/app/admin/api/destinations", map[string]string{"name": "n", "route": "/n/", "upstream": "https://n", "env": "A=1\nA=2"}, nil)
	if !strings.Contains(rec.Body.String(), "env key A is set twice") {
		t.Errorf("duplicate = %s", rec.Body)
	}
	rec = apiDo(t, h, "POST", "/app/admin/api/destinations", map[string]string{"name": "n", "route": "/n/", "upstream": "https://n", "env": "GH_HOST"}, nil)
	if !strings.Contains(rec.Body.String(), `env line \"GH_HOST\" is not KEY=TEMPLATE`) {
		t.Errorf("no equals = %s", rec.Body)
	}
	if len(e.Store().ListDestinations()) != 1 {
		t.Errorf("a refused create stored: %+v", e.Store().ListDestinations())
	}

	// An edit replaces every field; a bad field is refused on edit too.
	var out adminui.DestDetail
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations/gh", map[string]string{
		"route": "/api/v3/", "upstream": "https://ghe.example/api/v3", "identityIn": "bearer", "apply": "bearer", "env": "GH_HOST={host}",
	}, &out); rec.Code != 200 {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	if d := destNamed(t, e.Store(), "gh"); d.Upstream != "https://ghe.example/api/v3" || d.CredName != "" || len(d.Env) != 1 || d.Git || len(d.AllowPaths) != 0 || d.LogHeadersIn != "" || d.Note != "" {
		t.Errorf("after edit = %+v", d)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/destinations/gh", map[string]string{"route": "/api/v3/", "upstream": "https://x", "env": "nope"}, nil), 400, "bad_request")
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations/ghost", map[string]string{"route": "/g/", "upstream": "https://g"}, nil); rec.Code != 404 {
		t.Errorf("edit of a missing destination = %d %s", rec.Code, rec.Body)
	}
}
