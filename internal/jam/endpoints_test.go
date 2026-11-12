package jam

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// ep is an endpoint with a route and an upstream.
func ep(name, route string) Endpoint {
	return Endpoint{Name: name, Route: route, Upstream: "https://" + name + ".example"}
}

func TestValidateDestinationEndpoints(t *testing.T) {
	ok := Destination{Name: "gh", Endpoints: []Endpoint{ep("rest", "/api/v3/"), ep("graphql", "/api/"), ep("git", "/git/")}}
	if err := ValidateDestination(ok, credIs()); err != nil {
		t.Fatalf("valid destination refused: %v", err)
	}
	many := Destination{Name: "many"}
	for i := range MaxEndpoints + 1 {
		many.Endpoints = append(many.Endpoints, ep(string(rune('a'+i)), "/"+string(rune('a'+i))+"/"))
	}
	if err := ValidateDestination(Destination{Name: "ten", Endpoints: many.Endpoints[:MaxEndpoints]}, credIs()); err != nil {
		t.Fatalf("%d endpoints refused: %v", MaxEndpoints, err)
	}
	for name, tc := range map[string]struct {
		eps  []Endpoint
		want string
	}{
		"no endpoints":            {nil, "required"},
		"unnamed of several":      {[]Endpoint{ep("a", "/a/"), ep("", "/b/")}, "needs a name"},
		"name twice":              {[]Endpoint{ep("a", "/a/"), ep("a", "/b/")}, `"a" is used twice`},
		"neither subdomain/route": {[]Endpoint{ep("a", "/a/"), ep("b", "")}, `endpoint "b": a subdomain or a route`},
		"no upstream":             {[]Endpoint{ep("a", "/a/"), {Name: "b", Route: "/b/"}}, `endpoint "b": upstream`},
		"subdomain":               {[]Endpoint{{Name: "a", Subdomain: "gh", Upstream: "https://x"}}, "no destinations domain"},
		"too many":                {many.Endpoints, "at most 10"},
		"bad spec":                {[]Endpoint{ep("a", "/a/"), {Name: "b", Route: "/b/", Upstream: "https://x", Apply: ApplyCustom}}, `endpoint "b": apply custom requires`},
		"bad allow_paths":         {[]Endpoint{{Name: "a", Route: "/a/", Upstream: "https://x", AllowPaths: []string{"x"}}}, "allow_paths"},
		"bad logging":             {[]Endpoint{{Name: "a", Route: "/a/", Upstream: "https://x", LogHeadersIn: "raw"}}, "log_headers_in"},
	} {
		err := ValidateDestination(Destination{Name: "d", Endpoints: tc.eps}, credIs())
		if WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 400 mentioning %q", name, err, tc.want)
		}
	}
}

// No route may claim a path Jam serves on the cove listener: the paths
// themselves, a route above them, or one under their first segment.
func TestValidateDestinationReservedRoutes(t *testing.T) {
	for _, route := range []string{"/", "/connector", "/context", "/squawks/", "/squawks/x/", "/sq", "/alarms/", "/exposures/a/", "/end", "/metrics/", "/report"} {
		err := ValidateDestination(Destination{Name: "d", Endpoints: []Endpoint{ep("d", route)}}, credIs())
		if err == nil || !strings.Contains(err.Error(), "which Jam serves") {
			t.Errorf("route %s: err = %v", route, err)
		}
	}
	for _, route := range []string{"/anthropic/", "/git/", "/endpoints/", "/squawksmith/", "/api/v3/"} {
		if err := ValidateDestination(Destination{Name: "d", Endpoints: []Endpoint{ep("d", route)}}, credIs()); err != nil {
			t.Errorf("route %s refused: %v", route, err)
		}
	}
}

// Every path the cove mux serves itself is one CoveHandlerFor names.
func TestCoveHandlerFor(t *testing.T) {
	for path, want := range map[string]CoveHandler{
		"/connector": CoveConnector, "/squawks/ask/amend": CoveSquawks, "/squawks/boards/b1/ops": CoveSquawks,
		"/alarms/x": CoveAlarms, "/kept": CoveTurnEnd, "/metrics": CoveMetrics,
		"/squawks/other": "", "/anthropic/v1": "", "/connector/x": "",
	} {
		if got := CoveHandlerFor(path); got != want {
			t.Errorf("CoveHandlerFor(%s) = %q, want %q", path, got, want)
		}
	}
}

// A subdomain and route belong to one endpoint in the whole Jam: on create,
// on update, and within one destination.
func TestEndpointAddressesUniqueAcrossDestinations(t *testing.T) {
	st := NewMemStore()
	must(t, CreateDestination(st, Destination{Name: "gh", Endpoints: []Endpoint{ep("rest", "/api/v3/"), ep("graphql", "/api/")}}, credIs()))
	err := CreateDestination(st, Destination{Name: "other", Endpoints: []Endpoint{ep("o", "/api/v3/")}}, credIs())
	if WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), `destination "gh"'s endpoint "rest"`) {
		t.Fatalf("clashing create = %v", err)
	}
	must(t, CreateDestination(st, Destination{Name: "other", Endpoints: []Endpoint{ep("o", "/o/")}}, credIs()))
	if err := UpdateDestination(st, Destination{Name: "other", Endpoints: []Endpoint{ep("o", "/api/")}}, credIs()); err == nil {
		t.Fatal("clashing update accepted")
	}
	if err := UpdateDestination(st, Destination{Name: "gh", Endpoints: []Endpoint{ep("rest", "/api/v3/"), ep("graphql", "/graphql/")}}, credIs()); err != nil {
		t.Fatalf("a destination's own addresses clash with themselves: %v", err)
	}
	if err := CreateDestination(st, Destination{Name: "twice", Endpoints: []Endpoint{ep("a", "/t/"), ep("b", "/t/")}}, credIs()); err == nil {
		t.Fatal("two endpoints of one destination on one address accepted")
	}
}

// The config plan refuses a destination file whose endpoint takes another's
// address, and accepts the list form.
func TestPlanDestinationEndpoints(t *testing.T) {
	st := NewMemStore()
	tr := treeOf(t, map[string]string{"destinations/github.yml": listFile})
	applyPlan(t, st, planOn(t, st, tr))
	if got := st.ListDestinations(); len(got) != 1 || !reflect.DeepEqual(got[0], listDest()) {
		t.Fatalf("applied %+v", got)
	}
	if o := opOf(planOn(t, st, tr), ConfigKindDestination, "github"); o.Action != ConfigUnchanged {
		t.Fatalf("re-plan %+v", o)
	}
	clash := treeOf(t, map[string]string{"destinations/github.yml": listFile,
		"destinations/git.yml": "kind: destination\nversion: 1\nroute: /git/\nupstream: https://github.com\n"})
	if p := planOn(t, st, clash); len(p.Refusals) == 0 || !strings.Contains(strings.Join(p.Refusals, "\n"), "already") {
		t.Fatalf("clash not refused: %v", p.Refusals)
	}
	must(t, st.AddDestination(Destination{Name: "unmanaged", Endpoints: []Endpoint{ep("unmanaged", "/legacy/")}}))
	onUnmanaged := treeOf(t, map[string]string{"destinations/github.yml": listFile,
		"destinations/new.yml": "kind: destination\nversion: 1\nroute: /legacy/\nupstream: https://x\n"})
	if p := planOn(t, st, onUnmanaged); len(p.Refusals) == 0 {
		t.Fatal("clash with an unmanaged destination not refused")
	}
	reserved := treeOf(t, map[string]string{"destinations/x.yml": "kind: destination\nversion: 1\nroute: /squawks/\nupstream: https://x\n"})
	if p := planOn(t, st, reserved); len(p.Refusals) == 0 {
		t.Fatal("reserved route not refused")
	}
}

// The admin form edits only the first endpoint: the others, and the first's
// name, are kept.
func TestUpdateDestinationKeepSpecsKeepsOtherEndpoints(t *testing.T) {
	st := NewMemStore()
	must(t, CreateDestination(st, listDest(), credIs("gh")))
	form := Destination{Name: "github", CredName: "gh", Endpoints: []Endpoint{{Name: "github", Route: "/api/v4/", Upstream: "https://api.github.com"}}}
	must(t, UpdateDestinationKeepSpecs(st, form, credIs("gh")))
	got, _ := findDestination(st, "github")
	if len(got.Endpoints) != 2 || got.Endpoints[0].Name != "rest" || got.Endpoints[0].Route != "/api/v4/" || !reflect.DeepEqual(got.Endpoints[1], listDest().Endpoints[1]) {
		t.Fatalf("stored %+v", got.Endpoints)
	}
}

// Each endpoint brings its own identity-in, apply, allow_paths and header
// logging; the credential is the destination's.
func TestBrokerPerEndpointSpecsAndLogging(t *testing.T) {
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("Private-Token"))
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	b, _, tok := newTestBroker(t, "http://unused", "http://unused")
	must(t, b.store.AddDestination(Destination{Name: "git", CredName: "git-pat", Endpoints: []Endpoint{
		{Name: "api", Route: "/git/api/", Upstream: up.URL, IdentityIn: ApplyBearer, Apply: ApplyCustom,
			ApplySpec: &OutboundSpec{Header: "Private-Token", Template: "{cred}"}, AllowPaths: []string{"/v4/*"}, LogHeadersIn: HeaderLogExamined},
		{Name: "git", Route: "/git/", Upstream: up.URL, IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword},
	}}))
	var logs strings.Builder
	b.log = testLoggerTo(&logs)

	api := httptest.NewRequest("GET", "/git/api/v4/projects", nil)
	api.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, api)
	if rec.Code != 200 {
		t.Fatalf("api status %d", rec.Code)
	}
	gitReq := httptest.NewRequest("GET", "/git/acme/x.git/info/refs", nil)
	gitReq.SetBasicAuth("x-access-token", tok)
	rec = httptest.NewRecorder()
	b.ServeHTTP(rec, gitReq)
	if rec.Code != 200 {
		t.Fatalf("git status %d", rec.Code)
	}
	if want := []string{"/v4/projects  REAL-PAT", "/acme/x.git/info/refs Basic eC1hY2Nlc3MtdG9rZW46UkVBTC1QQVQ= "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("upstream saw %q, want %q", got, want)
	}
	if out := logs.String(); strings.Count(out, "broker headers in") != 1 || !strings.Contains(out, "endpoint=api") {
		t.Fatalf("header logging not per endpoint:\n%s", out)
	}
	denied := httptest.NewRequest("GET", "/git/api/v3/x", nil)
	denied.Header.Set("Authorization", "Bearer "+tok)
	rec = httptest.NewRecorder()
	b.ServeHTTP(rec, denied)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("allow_paths of the api endpoint not applied: %d", rec.Code)
	}
}

// Git routing comes from the endpoint that routes git.
func TestConnectorGitRouteFromEndpoint(t *testing.T) {
	d := Destination{Name: "github", Env: map[string]string{"GH_HOST": "{host}"}, Endpoints: []Endpoint{
		ep("rest", "/api/v3/"), {Name: "git", Route: "/gh-git/", Upstream: "https://github.com", Git: true},
	}}
	st := connectorStore(t, []Destination{d}, map[string]Scope{"w": {Destinations: []string{"github"}}})
	c, err := ConnectorFor(st, actorWith("w"))
	if err != nil || c.GitRoute != "/gh-git/" {
		t.Fatalf("connector = %+v, %v", c, err)
	}
	d.Endpoints = append(d.Endpoints, Endpoint{Name: "git2", Route: "/gh-git2/", Upstream: "https://github.com", Git: true})
	st = connectorStore(t, []Destination{d}, map[string]Scope{"w": {Destinations: []string{"github"}}})
	if _, err := ConnectorFor(st, actorWith("w")); err == nil {
		t.Fatal("two git routes in one scope accepted")
	}
}

// A backup carries destinations of several endpoints through export → JSON →
// import, beside flat ones.
func TestConfigSnapshotEndpointsRoundTrip(t *testing.T) {
	src := NewMemStore()
	must(t, src.AddDestination(listDest()))
	must(t, src.AddDestination(oldFlatDest()))
	b, err := json.Marshal(src.ExportConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), oldFlatJSON[0]) {
		t.Fatalf("a flat destination's backup changed:\n%s", b)
	}
	var snap ConfigSnapshot
	must(t, json.Unmarshal(b, &snap))
	dst := NewMemStore()
	must(t, dst.ImportConfig(snap))
	if !reflect.DeepEqual(dst.ListDestinations(), src.ListDestinations()) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", dst.ListDestinations(), src.ListDestinations())
	}
}
