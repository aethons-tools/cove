package jam

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

const vertexModelPath = "/v1/projects/p1/locations/us-east5/publishers/anthropic/models/claude-opus-5-5:streamRawPredict"

func vertexDest() Destination {
	return Destination{
		Name: "vertex-us-east5", Route: "/vertex/us-east5/", IdentityIn: ApplyBearer, CredName: "vertex-gcp", Apply: ApplyBearer,
		Env: map[string]string{
			"ANTHROPIC_VERTEX_BASE_URL":    "{url}/v1",
			"CLAUDE_CODE_SKIP_VERTEX_AUTH": "1",
			"ANTHROPIC_CUSTOM_HEADERS":     "Authorization: Bearer {token}",
		},
		AllowPaths: []string{
			"/v1/projects/p1/locations/us-east5/publishers/anthropic/models/*:rawPredict",
			"/v1/projects/p1/locations/us-east5/publishers/anthropic/models/*:streamRawPredict",
		},
	}
}

// The broker forwards only allow-listed paths, refusing anything else before
// resolving the credential; an unknown identity is still a 401 first.
func TestBrokerAllowPaths(t *testing.T) {
	var hits atomic.Int32
	var gotAuth, gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	d := vertexDest()
	d.Upstream = up.URL
	store := NewMemStore()
	tok, _ := MintToken()
	mustCreateProject(t, store, "ACME")
	if err := store.AddDestination(d); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{d.Name}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(Actor{ID: "spider", TokenHash: HashToken(tok), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	resolved := 0
	creds := credFunc(func(name string) (string, error) { resolved++; return "GCP-ACCESS", nil })
	var logs strings.Builder
	b := NewBroker(store, creds, testLoggerTo(&logs))
	send := func(path, bearer string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		b.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send("/vertex/us-east5"+vertexModelPath, tok); code != 200 {
		t.Fatalf("allowed path: status %d", code)
	}
	if gotAuth != "Bearer GCP-ACCESS" || gotPath != vertexModelPath {
		t.Fatalf("upstream got Authorization %q path %q", gotAuth, gotPath)
	}
	hits.Store(0)
	resolved = 0
	for _, p := range []string{
		"/v1/projects/p2/locations/us-east5/publishers/anthropic/models/claude-opus-5-5:streamRawPredict", // other project
		"/v1/projects/p1/locations/us-east5/endpoints/e1:predict",                                         // not an anthropic model
		"/v1/projects/p1/locations/us-east5/publishers/anthropic/models/m/x:rawPredict",                   // * never crosses /
		"/v1/projects/p1/locations/us-east5/publishers/anthropic/models/../../../../../p2/datasets:rawPredict",
		"/v1/projects/p1/locations/us-east5/publishers/anthropic/models/%2e%2e/x:rawPredict",
		"/v1/projects/p1/locations/us-east5/publishers/anthropic//models/m:rawPredict",
	} {
		if code := send("/vertex/us-east5"+p, tok); code != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403", p, code)
		}
	}
	if code := send("/vertex/us-east5/v1/projects/p2/x", "not-a-token"); code != http.StatusUnauthorized {
		t.Errorf("unknown identity on a refused path: status %d, want 401", code)
	}
	if hits.Load() != 0 || resolved != 0 {
		t.Fatalf("refused requests reached upstream (%d) or resolved the credential (%d)", hits.Load(), resolved)
	}
	if strings.Contains(logs.String(), tok) || strings.Contains(logs.String(), "GCP-ACCESS") {
		t.Fatal("secret material leaked into logs")
	}
}

type credFunc func(string) (string, error)

func (f credFunc) Resolve(name string) (string, error) { return f(name) }

func TestDestinationAllowPaths(t *testing.T) {
	ok := vertexDest()
	if err := ok.ValidateAllowPaths(); err != nil {
		t.Fatalf("valid allow_paths refused: %v", err)
	}
	for name, paths := range map[string][]string{
		"relative":  {"v1/x"},
		"unclean":   {"/v1/../x"},
		"trailing":  {"/v1/"},
		"bad glob":  {"/v1/["},
		"too many":  make([]string, MaxAllowPaths+1),
		"empty one": {""},
	} {
		d := Destination{AllowPaths: paths}
		if err := d.ValidateAllowPaths(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if !(Destination{}).PathAllowed("/anything/../at/all") {
		t.Error("no allow_paths must allow any path")
	}
	err := ValidateDestination(Destination{Name: "v", Route: "/v/", Upstream: "https://x", AllowPaths: []string{"nope"}}, func(string) bool { return true })
	if WriteStatus(err, 0) != http.StatusBadRequest {
		t.Errorf("ValidateDestination: err = %v, want 400", err)
	}
}

// Principal header rules apply on the destination serving the spec's
// provider: a vertex spec on a vertex destination (ANTHROPIC_VERTEX_BASE_URL),
// never on the anthropic one, and an anthropic spec never on vertex.
func TestBrokerPrincipalRulesFollowProvider(t *testing.T) {
	v := vertexDest()
	v.CredName = "anthropic-sub"
	rule := modelspec.HeaderRule{Name: "X-Foo", Set: "bar"}
	pc := newPrincipalCase(t, []modelspec.HeaderRule{rule}, anthropicDest(), v)
	if got := pc.send("/vertex/us-east5"+vertexModelPath, "/vertex/us-east5/").Get("X-Foo"); got != "" {
		t.Errorf("anthropic spec applied a rule on the vertex destination: %q", got)
	}
	spec, _ := pc.broker.store.GetModelSpec("pooled")
	spec.Claude.Provider = "vertex"
	if err := pc.broker.store.PutModelSpec(spec); err != nil {
		t.Fatal(err)
	}
	if got := pc.send("/vertex/us-east5"+vertexModelPath, "/vertex/us-east5/").Get("X-Foo"); got != "bar" {
		t.Errorf("vertex spec on the vertex destination: X-Foo = %q, want bar", got)
	}
	if got := pc.send("/anthropic/v1/messages", "/anthropic/").Get("X-Foo"); got != "" {
		t.Errorf("vertex spec applied a rule on the anthropic destination: %q", got)
	}
}

// fakeGoogleTokenServer serves the OAuth token endpoint a service-account
// credential's token_uri points at, counting exchanges.
func fakeGoogleTokenServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.Form.Get("assertion") == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"ya29.fake","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func serviceAccountJSON(t *testing.T, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	b, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "p1", "private_key_id": "k1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "jam@p1.iam.gserviceaccount.com", "token_uri": tokenURI,
	})
	return string(b)
}

func TestGCPTokenResolver(t *testing.T) {
	srv, exchanges := fakeGoogleTokenServer(t)
	baseCalls := 0
	base := credFunc(func(name string) (string, error) {
		baseCalls++
		switch name {
		case "vertex-gcp":
			return serviceAccountJSON(t, srv.URL), nil
		case "git-pat":
			return "PAT", nil
		}
		return "", errors.New("no such credential")
	})
	g := NewGCPTokenResolver(base, []string{"vertex-gcp"})
	if err := g.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if exchanges.Load() != 0 {
		t.Fatal("Load minted a token; it must only parse")
	}
	for range 3 {
		tok, err := g.Resolve("vertex-gcp")
		if err != nil || tok != "ya29.fake" {
			t.Fatalf("Resolve = %q, %v", tok, err)
		}
	}
	if exchanges.Load() != 1 {
		t.Errorf("token exchanged %d times, want 1 (cached)", exchanges.Load())
	}
	if v, err := g.Resolve("git-pat"); err != nil || v != "PAT" {
		t.Errorf("non-gcp name: %q, %v", v, err)
	}
	if baseCalls != 2 {
		t.Errorf("base resolved %d times, want 2 (the JSON once, git-pat once)", baseCalls)
	}
}

func TestGCPTokenResolverRefusals(t *testing.T) {
	for name, supply := range map[string]string{
		"not json":         "sekrit-not-json",
		"unsupported type": `{"type":"gdch_service_account","x":"sekrit"}`,
	} {
		g := NewGCPTokenResolver(credFunc(func(string) (string, error) { return supply, nil }), []string{"c"})
		err := g.Load()
		if err == nil {
			t.Errorf("%s: Load accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "sekrit") || !strings.Contains(err.Error(), `"c"`) {
			t.Errorf("%s: err = %v; want the credential named and no secret echoed", name, err)
		}
	}
	// A malformed key only fails at the first exchange, still without echoing it.
	g := NewGCPTokenResolver(credFunc(func(string) (string, error) {
		return `{"type":"service_account","private_key":"sekrit","client_email":"a@b","token_uri":"http://127.0.0.1:1"}`, nil
	}), []string{"c"})
	if _, err := g.Resolve("c"); err == nil || strings.Contains(err.Error(), "sekrit") {
		t.Errorf("bad key: err = %v; want a refusal without the key", err)
	}
	// A failed build is not cached: a fixed supply is picked up on retry.
	srv, _ := fakeGoogleTokenServer(t)
	supply := "broken"
	g = NewGCPTokenResolver(credFunc(func(string) (string, error) { return supply, nil }), []string{"c"})
	if _, err := g.Resolve("c"); err == nil {
		t.Fatal("broken supply resolved")
	}
	supply = serviceAccountJSON(t, srv.URL)
	if tok, err := g.Resolve("c"); err != nil || tok != "ya29.fake" {
		t.Fatalf("after fix: %q, %v", tok, err)
	}
}
