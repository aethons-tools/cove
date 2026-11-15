package adminui_test

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
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
		{Name: "anthropic", Endpoints: []jam.Endpoint{{Name: "anthropic", Route: "/anthropic/", Upstream: "https://api.anthropic.com", IdentityIn: jam.ApplyBearer, Apply: jam.ApplyBearer}}, CredName: "anth-key"},
		{Name: "git", Endpoints: []jam.Endpoint{{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: jam.ApplyBasicPassword, Apply: jam.ApplyBasicPassword}}, CredName: "git-pat"},
		{Name: "github-api", Endpoints: []jam.Endpoint{{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, Apply: jam.ApplyBearer}}, CredName: "gh-pat",
			Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"}},
		{Name: "gh-alt", Endpoints: []jam.Endpoint{{Name: "gh-alt", Route: "/alt/", Upstream: "https://alt.example"}}, Env: map[string]string{"GH_HOST": "{base}"}},
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
	if r := rows["github-api"]; r.UsedBy != 2 || r.CredName != "gh-pat" || r.Endpoints != 1 || strings.Join(r.Addresses, ",") != "/api/v3/" ||
		strings.Join(r.Upstreams, ",") != "https://api.github.com" || strings.Join(r.EnvKeys, ",") != "GH_ENTERPRISE_TOKEN,GH_HOST" {
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
	want := adminui.DestFields{CredName: "gh-pat", Env: []adminui.EnvRow{{Key: "GH_ENTERPRISE_TOKEN", Template: "{token}"}, {Key: "GH_HOST", Template: "{host}"}},
		Endpoints: []adminui.DestEndpoint{{Name: "github-api", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: "bearer", Apply: "bearer",
			AllowPaths: []string{}, LogHeadersIn: "none", LogHeadersOut: "none"}}}
	if !reflect.DeepEqual(d.Fields, want) || d.EnvImplied {
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
	if !d.EnvImplied || env["ANTHROPIC_BASE_URL"] != "{base}/anthropic" || len(d.Fields.Env) != 0 {
		t.Errorf("anthropic implied env = %+v %v", d, env)
	}
	apiDo(t, h, "GET", "/app/admin/api/destinations/git", nil, &d)
	if !d.GitRouted || !d.GitImplied || d.Fields.Endpoints[0].Git {
		t.Errorf("git implied routing = %+v", d)
	}
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/destinations/nope", nil, nil), 404, "not_found")
}

// A destination with several endpoints lists each address and its distinct
// upstreams, and reads back every endpoint field: a custom spec as its
// expression ("raw" for the default encoding).
func TestDestinationsAPIEndpointsRead(t *testing.T) {
	e := editOn(t, editSetup{cred: credOK})
	e.Edit(func(st jam.Store) {
		if err := st.AddDestination(jam.Destination{Name: "gh", CredName: "known-cred", Endpoints: []jam.Endpoint{
			{Name: "rest", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, Apply: jam.ApplyCustom,
				ApplySpec: &jam.OutboundSpec{Header: "Authorization", Template: "token {cred}"}, AllowPaths: []string{"/repos/*"}, LogHeadersIn: jam.HeaderLogAll},
			{Name: "graphql", Route: "/api/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyCustom,
				IdentityInSpec: &jam.InboundSpec{Header: "X-Agent", Prefixes: []string{"Agent "}}, Apply: jam.ApplyBearer},
			{Name: "git", Route: "/git/", Upstream: "https://github.com", Git: true},
		}}); err != nil {
			t.Fatal(err)
		}
	})
	var list adminui.DestinationsResponse
	apiDo(t, e.H, "GET", "/app/admin/api/destinations", nil, &list)
	if r := list.Destinations[0]; r.Endpoints != 3 || strings.Join(r.Addresses, ",") != "/api/v3/,/api/,/git/" || strings.Join(r.Upstreams, ",") != "https://api.github.com,https://github.com" || !r.GitRouted {
		t.Errorf("row = %+v", r)
	}
	var d adminui.DestDetail
	apiDo(t, e.H, "GET", "/app/admin/api/destinations/gh", nil, &d)
	eps := d.Fields.Endpoints
	if len(eps) != 3 || eps[0].Name != "rest" || eps[0].IdentityInSpec != nil || eps[0].LogHeadersIn != "all" || eps[0].LogHeadersOut != "none" ||
		!reflect.DeepEqual(eps[0].AllowPaths, []string{"/repos/*"}) || *eps[0].ApplySpec != (adminui.DestOutboundSpec{Header: "Authorization", Template: "token {cred}", Encoding: "raw"}) {
		t.Fatalf("rest = %+v", eps[0])
	}
	if s := eps[1].IdentityInSpec; eps[1].IdentityIn != "custom" || s == nil || s.Header != "X-Agent" || s.Encoding != "raw" || !reflect.DeepEqual(s.Prefixes, []string{"Agent "}) || eps[1].ApplySpec != nil {
		t.Fatalf("graphql = %+v", eps[1])
	}
	if !eps[2].Git || eps[2].Subdomain != "" {
		t.Fatalf("git = %+v", eps[2])
	}
}

// Every field is read from the body: endpoints in order with every field
// (text trimmed, blank allow paths dropped, "none" logging and the "raw"
// encoding stored unset), the env by key. An edit replaces every field:
// endpoints added and removed, expressions edited.
func TestDestinationsAPIWrite(t *testing.T) {
	e := editOn(t, editSetup{cred: credOK})
	h := e.H
	body := adminui.DestFields{Name: " gh ", CredName: "known-cred", Note: " use git over https ",
		Env: []adminui.EnvRow{{Key: " GH_HOST ", Template: " {host} "}, {Key: "GH_ENTERPRISE_TOKEN", Template: "{token}"}},
		Endpoints: []adminui.DestEndpoint{
			{Name: "rest", Route: " /api/v3/ ", Upstream: "https://api.github.com", IdentityIn: "bearer", Apply: "bearer",
				AllowPaths: []string{" /repos/* ", ""}, LogHeadersIn: "examined", LogHeadersOut: "none"},
			{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: "basic-password", Apply: "custom", Git: true,
				ApplySpec: &adminui.DestOutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: "basic", BasicUser: "x-access-token"}},
		}}
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations", body, nil); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	d := destNamed(t, e.Store(), "gh")
	want := jam.Destination{Name: "gh", CredName: "known-cred", Note: "use git over https", Env: map[string]string{"GH_HOST": "{host}", "GH_ENTERPRISE_TOKEN": "{token}"},
		Endpoints: []jam.Endpoint{
			{Name: "rest", Route: "/api/v3/", Upstream: "https://api.github.com", IdentityIn: jam.ApplyBearer, Apply: jam.ApplyBearer, AllowPaths: []string{"/repos/*"}, LogHeadersIn: jam.HeaderLogExamined},
			{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: jam.ApplyBasicPassword, Apply: jam.ApplyCustom, Git: true,
				ApplySpec: &jam.OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: jam.EncodingBasic, BasicUser: "x-access-token"}},
		}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("stored\n%+v\nwant\n%+v", d, want)
	}

	// Edit: drop rest, edit git's expression, add graphql with an identity-in expression.
	var out adminui.DestDetail
	edit := adminui.DestFields{Env: []adminui.EnvRow{}, Endpoints: []adminui.DestEndpoint{
		{Name: "git", Route: "/git/", Upstream: "https://github.com", Apply: "custom",
			ApplySpec: &adminui.DestOutboundSpec{Header: "Private-Token", Template: "{cred}", Encoding: "raw"}},
		{Name: "graphql", Route: "/api/", Upstream: "https://api.github.com", IdentityIn: "custom", Apply: "bearer",
			IdentityInSpec: &adminui.DestInboundSpec{Header: "X-Agent", Prefixes: []string{"Agent "}, Encoding: "raw"}},
	}}
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations/gh", edit, &out); rec.Code != 200 {
		t.Fatalf("edit = %d %s", rec.Code, rec.Body)
	}
	d = destNamed(t, e.Store(), "gh")
	if len(d.Endpoints) != 2 || d.Env != nil || d.CredName != "" || d.Note != "" ||
		*d.Endpoints[0].ApplySpec != (jam.OutboundSpec{Header: "Private-Token", Template: "{cred}"}) ||
		!reflect.DeepEqual(*d.Endpoints[1].IdentityInSpec, jam.InboundSpec{Header: "X-Agent", Prefixes: []string{"Agent "}}) {
		t.Fatalf("after edit = %+v", d)
	}
	if len(out.Fields.Endpoints) != 2 || out.Fields.Endpoints[1].IdentityInSpec.Header != "X-Agent" {
		t.Errorf("edit answer = %+v", out.Fields)
	}
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations/ghost", destBody(nil), nil); rec.Code != 404 {
		t.Errorf("edit of a missing destination = %d %s", rec.Code, rec.Body)
	}
}

// A refused write names every problem at its field — the destination's,
// an env row's, an endpoint's, an expression's — and stores nothing.
func TestDestinationsAPIFieldErrors(t *testing.T) {
	e := editOn(t, editSetup{cred: credOK})
	h := e.H
	if rec := apiDo(t, h, "POST", "/app/admin/api/destinations", destBody(nil), nil); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	fields := func(rec *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		var body adminui.APIError
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 400 || body.Code != "bad_request" {
			t.Fatalf("= %d %s, want 400", rec.Code, rec.Body)
		}
		out := map[string]string{}
		for _, f := range body.Errors {
			out[f.Field] = f.Message
		}
		return out
	}
	bad := adminui.DestFields{Name: "x", CredName: "ghost", Env: []adminui.EnvRow{{Key: "A", Template: "1"}, {Key: "A", Template: "2"}, {Key: " "}}}
	got := fields(apiDo(t, h, "POST", "/app/admin/api/destinations", bad, nil))
	if len(got) != 2 || got["env[1].key"] != "env key A is set twice" || got["env[2].key"] != "env key is required" {
		t.Errorf("env rows = %v", got)
	}
	bad.Env = []adminui.EnvRow{{Key: "JAM_X", Template: "1"}}
	bad.Endpoints = []adminui.DestEndpoint{
		{Name: "a", Route: "/gh/", Upstream: "https://x", LogHeadersIn: "loud"},
		{Name: "a", Subdomain: "gh", Upstream: "https://x", AllowPaths: []string{"rel"}},
		{Name: "c", Route: "/squawks/", Apply: "custom", ApplySpec: &adminui.DestOutboundSpec{Header: "X-Token", Template: "no cred", Encoding: "raw"},
			IdentityIn: "custom", IdentityInSpec: &adminui.DestInboundSpec{Header: "Bad Header", Encoding: "raw"}},
	}
	got = fields(apiDo(t, h, "POST", "/app/admin/api/destinations", bad, nil))
	for _, f := range []string{"credName", "env", "endpoints[0].route", "endpoints[0].logHeadersIn", "endpoints[1].name", "endpoints[1].subdomain",
		"endpoints[1].allowPaths", "endpoints[2].route", "endpoints[2].upstream", "endpoints[2].identityInSpec.header", "endpoints[2].applySpec.template"} {
		if got[f] == "" {
			t.Errorf("no error at %s: %v", f, got)
		}
	}
	if !strings.Contains(got["endpoints[0].route"], `destination "gh"'s endpoint "gh"`) {
		t.Errorf("clash = %q", got["endpoints[0].route"])
	}
	bad = destBody(func(b *adminui.DestFields) { b.Name, b.Endpoints = "", nil })
	if got := fields(apiDo(t, h, "POST", "/app/admin/api/destinations", bad, nil)); got["name"] == "" || got["endpoints"] == "" {
		t.Errorf("empty = %v", got)
	}
	// On an edit too.
	got = fields(apiDo(t, h, "POST", "/app/admin/api/destinations/gh", destBody(func(b *adminui.DestFields) { b.Endpoints[0].Apply = "custom" }), nil))
	if got["endpoints[0].apply"] != "apply custom requires apply_spec" {
		t.Errorf("edit = %v", got)
	}
	if ds := e.Store().ListDestinations(); len(ds) != 1 || ds[0].Endpoints[0].Apply != jam.ApplyBearer {
		t.Errorf("a refused write stored: %+v", ds)
	}
}
