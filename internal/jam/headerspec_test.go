package jam

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPresetExpansion(t *testing.T) {
	cases := []struct {
		preset ApplyMethod
		in     InboundSpec
		out    OutboundSpec
	}{
		{ApplyBearer,
			InboundSpec{Header: "Authorization", Prefixes: []string{"Bearer ", "token "}},
			OutboundSpec{Header: "Authorization", Template: "Bearer {cred}"}},
		{ApplyBasicPassword,
			InboundSpec{Header: "Authorization", Encoding: EncodingBasic},
			OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "x-access-token"}},
		{ApplyXAPIKey,
			InboundSpec{Header: "X-Api-Key"},
			OutboundSpec{Header: "X-Api-Key", Template: "{cred}"}},
		{ApplyRaw,
			InboundSpec{Header: "Authorization"},
			OutboundSpec{Header: "Authorization", Template: "{cred}"}},
	}
	for _, c := range cases {
		d := Destination{IdentityIn: c.preset, Apply: c.preset}
		in, ok := d.InboundSpec()
		if !ok || !reflect.DeepEqual(in, c.in) {
			t.Errorf("%s inbound = %+v, %v; want %+v", c.preset, in, ok, c.in)
		}
		out, ok := d.OutboundSpec()
		if !ok || !reflect.DeepEqual(out, c.out) {
			t.Errorf("%s outbound = %+v, %v; want %+v", c.preset, out, ok, c.out)
		}
	}
	if _, ok := (Destination{IdentityIn: "nope"}).InboundSpec(); ok {
		t.Error("unknown preset expanded")
	}
	if _, ok := (Destination{}).OutboundSpec(); ok {
		t.Error("empty preset expanded")
	}
}

func TestCustomSpecUsedOnlyWhenCustom(t *testing.T) {
	in := &InboundSpec{Header: "X-Jam-Token"}
	out := &OutboundSpec{Header: "Private-Token", Template: "{cred}"}
	d := Destination{IdentityIn: ApplyCustom, IdentityInSpec: in, Apply: ApplyCustom, ApplySpec: out}
	if got, ok := d.InboundSpec(); !ok || got.Header != "X-Jam-Token" {
		t.Errorf("custom inbound = %+v, %v", got, ok)
	}
	if got, ok := d.OutboundSpec(); !ok || got.Header != "Private-Token" {
		t.Errorf("custom outbound = %+v, %v", got, ok)
	}
	if _, ok := (Destination{IdentityIn: ApplyCustom}).InboundSpec(); ok {
		t.Error("custom without a spec expanded")
	}
}

func TestBasicEncodeDecodeRoundTrip(t *testing.T) {
	out := OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "x-access-token"}
	in := InboundSpec{Header: "Authorization", Encoding: EncodingBasic}
	for _, cred := range []string{"ghp_abc123", "with:colon", "ünïcode"} {
		r := httptest.NewRequest("GET", "/", nil)
		out.apply(r.Header, cred)
		u, p, ok := r.BasicAuth()
		if !ok || u != "x-access-token" || p != cred {
			t.Errorf("std BasicAuth = %q %q %v; want %q", u, p, ok, cred)
		}
		got, ok := in.extract(r.Header)
		if !ok || got != cred {
			t.Errorf("round trip = %q %v; want %q", got, ok, cred)
		}
	}
}

func TestInboundExtractionPerPreset(t *testing.T) {
	cases := []struct {
		preset        ApplyMethod
		header, value string
		want          string
		ok            bool
	}{
		{ApplyBearer, "Authorization", "Bearer TOK", "TOK", true},
		{ApplyBearer, "Authorization", "token TOK", "TOK", true}, // gh to a GHE host
		{ApplyBearer, "Authorization", "Basic TOK", "", false},
		{ApplyBearer, "Authorization", "Bearer ", "", false},
		{ApplyBasicPassword, "Authorization", "Basic eC1hY2Nlc3MtdG9rZW46VE9L", "TOK", true}, // x-access-token:TOK
		{ApplyBasicPassword, "Authorization", "Bearer TOK", "", false},
		{ApplyXAPIKey, "X-Api-Key", "TOK", "TOK", true},
		{ApplyXAPIKey, "Authorization", "Bearer TOK", "", false},
		{ApplyRaw, "Authorization", "TOK", "TOK", true},
		{ApplyRaw, "X-Api-Key", "TOK", "", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set(c.header, c.value)
		in, _ := Destination{IdentityIn: c.preset}.InboundSpec()
		got, ok := presentedToken(r, in)
		if got != c.want || ok != c.ok {
			t.Errorf("%s %s=%q: got %q %v, want %q %v", c.preset, c.header, c.value, got, ok, c.want, c.ok)
		}
	}
}

func TestOutboundApplyPerPreset(t *testing.T) {
	cases := map[ApplyMethod][2]string{
		ApplyBearer:  {"Authorization", "Bearer CRED"},
		ApplyXAPIKey: {"X-Api-Key", "CRED"},
		ApplyRaw:     {"Authorization", "CRED"},
	}
	for p, want := range cases {
		h := http.Header{}
		out, _ := Destination{Apply: p}.OutboundSpec()
		out.apply(h, "CRED")
		if got := h.Get(want[0]); got != want[1] {
			t.Errorf("%s: %s = %q, want %q", p, want[0], got, want[1])
		}
	}
}

func TestValidateHeaderSpecs(t *testing.T) {
	ok := func(d Destination) Destination {
		d.Name, d.Route, d.Upstream = "x", "/x/", "https://x"
		return d
	}
	good := []Destination{
		ok(Destination{}), // unset: legacy, allowed
		ok(Destination{IdentityIn: ApplyRaw, Apply: ApplyRaw}),
		ok(Destination{IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "X-Tok", Prefixes: []string{"T "}},
			Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "u"}}),
	}
	for i, d := range good {
		if err := ValidateDestination(d, func(string) bool { return true }); err != nil {
			t.Errorf("good[%d]: %v", i, err)
		}
	}
	bad := map[string]Destination{
		"unknown preset":          ok(Destination{Apply: "bogus"}),
		"custom without spec":     ok(Destination{Apply: ApplyCustom}),
		"spec without custom":     ok(Destination{Apply: ApplyBearer, ApplySpec: &OutboundSpec{Header: "A", Template: "{cred}"}}),
		"bad header name":         ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Bad Header", Template: "{cred}"}}),
		"no cred placeholder":     ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "A", Template: "Bearer"}}),
		"cred twice":              ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "A", Template: "{cred}{cred}"}}),
		"newline in template":     ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "A", Template: "x\r\n{cred}"}}),
		"basic not Authorization": ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "X-A", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "u"}}),
		"basic without user":      ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic}}),
		"user without basic":      ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "A", Template: "{cred}", BasicUser: "u"}}),
		"unknown encoding":        ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "A", Template: "{cred}", Encoding: "hex"}}),
		"inbound basic not Auth":  ok(Destination{IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "X-A", Encoding: EncodingBasic}}),
		"inbound basic prefixes":  ok(Destination{IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "Authorization", Encoding: EncodingBasic, Prefixes: []string{"x"}}}),
		"inbound bad header":      ok(Destination{IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: ""}}),
		"inbound newline prefix":  ok(Destination{IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "A", Prefixes: []string{"a\n"}}}),
	}
	for name, d := range bad {
		if err := ValidateDestination(d, func(string) bool { return true }); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// The error never echoes the template (it may sit next to a credential shape).
	err := ValidateDestination(ok(Destination{Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "A", Template: "SECRETISH"}}), func(string) bool { return true })
	if err == nil || strings.Contains(err.Error(), "SECRETISH") {
		t.Errorf("template error = %v", err)
	}
}

func TestDestinationPersistenceOldAndNewShapes(t *testing.T) {
	// The pre-spec shape: preset strings only. Loads unchanged; re-marshals without spec fields.
	old := `{"name":"git","route":"/git/","upstream":"https://github.com","identity_in":"basic-password","cred_name":"pat","apply":"basic-password"}`
	var d Destination
	if err := json.Unmarshal([]byte(old), &d); err != nil {
		t.Fatal(err)
	}
	if d.IdentityIn != ApplyBasicPassword || d.Apply != ApplyBasicPassword || d.IdentityInSpec != nil || d.ApplySpec != nil {
		t.Fatalf("old shape = %+v", d)
	}
	b, _ := json.Marshal(d)
	if string(b) != old {
		t.Fatalf("old shape re-marshal = %s", b)
	}

	// The new shape: custom + specs, through JSON (stores, backups) and YAML (import).
	nd := Destination{Name: "linear", Route: "/linear/", Upstream: "https://api.linear.app",
		IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "X-Jam", Prefixes: []string{"Jam "}},
		Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Authorization", Template: "{cred}", Encoding: EncodingBasic, BasicUser: "u"}}
	b, _ = json.Marshal(nd)
	var back Destination
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, nd) {
		t.Fatalf("json round trip = %+v, %v (from %s)", back, err, b)
	}
	y := `
destinations:
  - name: linear
    route: /linear/
    upstream: https://api.linear.app
    identity_in: custom
    identity_in_spec: {header: X-Jam, prefixes: ["Jam "]}
    apply: custom
    apply_spec: {header: Authorization, template: "{cred}", encoding: basic, basic_user: u}
`
	var conf Config
	if err := yaml.Unmarshal([]byte(y), &conf); err != nil || len(conf.Destinations) != 1 || !reflect.DeepEqual(conf.Destinations[0], nd) {
		t.Fatalf("yaml = %+v, %v", conf.Destinations, err)
	}
}

func TestClientEnvCustomHasNoDefault(t *testing.T) {
	d := Destination{Route: "/anthropic/", IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "X-Api-Key"}}
	env := d.ClientEnv()
	if _, set := env["ANTHROPIC_API_KEY"]; set {
		t.Errorf("custom identity got a default token env: %v", env)
	}
	if _, set := env["ANTHROPIC_AUTH_TOKEN"]; set {
		t.Errorf("custom identity got a default token env: %v", env)
	}
}

// Linear GraphQL personal API keys go bare in Authorization: the raw preset.
func TestBrokerRawSendsBareAuthorization(t *testing.T) {
	var gotAuth string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	b, logbuf, tok := newTestBroker(t, "http://unused", "http://unused")
	b.store.(*MemStore).AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: up.URL, IdentityIn: ApplyRaw, CredName: "anthropic-bearer", Apply: ApplyRaw})
	req := httptest.NewRequest("POST", "/anthropic/graphql", strings.NewReader("{}"))
	req.Header.Set("Authorization", tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != 200 || gotAuth != "REAL-ANTHROPIC" {
		t.Fatalf("status %d, upstream Authorization = %q; want bare real cred", rec.Code, gotAuth)
	}
	if strings.Contains(logbuf.String(), tok) || strings.Contains(logbuf.String(), "REAL-ANTHROPIC") {
		t.Fatal("secret material leaked into logs")
	}
}

// A custom spec: identity in a private header (stripped), cred in another.
func TestBrokerCustomSpecs(t *testing.T) {
	var gotIn, gotOut string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotIn, gotOut = r.Header.Get("X-Jam-Token"), r.Header.Get("Private-Token")
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	b, _, tok := newTestBroker(t, "http://unused", "http://unused")
	b.store.(*MemStore).AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", Upstream: up.URL,
		IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "X-Jam-Token", Prefixes: []string{"Jam "}},
		CredName: "anthropic-bearer", Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Private-Token", Template: "pt-{cred}"}})
	req := httptest.NewRequest("GET", "/anthropic/x", nil)
	req.Header.Set("X-Jam-Token", "Jam "+tok)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, req)
	if rec.Code != 200 || gotIn != "" || gotOut != "pt-REAL-ANTHROPIC" {
		t.Fatalf("status %d, upstream X-Jam-Token=%q Private-Token=%q", rec.Code, gotIn, gotOut)
	}
}

// Headers ReverseProxy drops after the Director (hop-by-hop) or that it owns
// (Host) can't carry an identity or a credential.
func TestValidateRejectsUnhonorableHeaders(t *testing.T) {
	for _, h := range []string{"Connection", "proxy-connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "host"} {
		if err := (InboundSpec{Header: h}).Validate(); err == nil {
			t.Errorf("inbound %s accepted", h)
		}
		if err := (OutboundSpec{Header: h, Template: "{cred}"}).Validate(); err == nil {
			t.Errorf("outbound %s accepted", h)
		}
	}
}

// A backup holding a method value this Jam doesn't know refuses the whole
// import, naming the destination and the field.
func TestImportConfigRefusesUnknownMethodNamingIt(t *testing.T) {
	snap := ConfigSnapshot{Version: ConfigSnapshotVersion, Destinations: []Destination{{Name: "legacy", Route: "/l/", Upstream: "https://l", IdentityIn: ApplyBearer, Apply: "bogus"}}}
	err := NewMemStore().ImportConfig(snap)
	if err == nil || !strings.Contains(err.Error(), "legacy") || !strings.Contains(err.Error(), "apply") {
		t.Fatalf("err = %v, want naming destination legacy and field apply", err)
	}
}

// UpdateDestinationKeepSpecs (the UI's edit) keeps stored custom specs when
// the incoming method is custom with no spec, under the write lock.
func TestUpdateDestinationKeepSpecs(t *testing.T) {
	store := NewMemStore()
	spec := &OutboundSpec{Header: "Private-Token", Template: "{cred}"}
	base := Destination{Name: "gl", Route: "/gl/", Upstream: "https://gl", IdentityIn: ApplyRaw, Apply: ApplyCustom, ApplySpec: spec}
	if err := store.AddDestination(base); err != nil {
		t.Fatal(err)
	}
	credOK := func(string) bool { return true }
	edit := base
	edit.ApplySpec, edit.Upstream = nil, "https://gl2"
	if err := UpdateDestinationKeepSpecs(store, edit, credOK); err != nil {
		t.Fatal(err)
	}
	if d := store.ListDestinations()[0]; d.Upstream != "https://gl2" || d.ApplySpec == nil || *d.ApplySpec != *spec {
		t.Fatalf("stored = %+v", d)
	}
	// Switching to a preset drops the spec; plain UpdateDestination never fills one in.
	edit.Apply = ApplyBearer
	if err := UpdateDestinationKeepSpecs(store, edit, credOK); err != nil || store.ListDestinations()[0].ApplySpec != nil {
		t.Fatalf("preset edit: %v %+v", err, store.ListDestinations()[0])
	}
	edit.Apply = ApplyCustom
	if err := UpdateDestination(store, edit, credOK); err == nil {
		t.Fatal("UpdateDestination filled in a spec")
	}
	if err := UpdateDestinationKeepSpecs(store, Destination{Name: "ghost", Route: "/g/", Upstream: "https://g"}, credOK); WriteStatus(err, 0) != http.StatusNotFound {
		t.Fatalf("missing = %v", err)
	}
}
