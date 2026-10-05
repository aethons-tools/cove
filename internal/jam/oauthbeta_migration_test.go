package jam

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// legacyDest is a destination as a pre-COV-241 Jam stored it: the subscription
// pool's anthropic destination with the removed oauth_beta flag, decoded from
// its stored JSON (pg rows and backups carry the flag as "oauth_beta").
func legacyDest(t *testing.T, upstream string) Destination {
	t.Helper()
	doc := `{"name":"anthropic","route":"/anthropic/","upstream":"` + upstream + `","identity_in":"bearer","cred_name":"anthropic-sub","apply":"bearer","oauth_beta":true}`
	var d Destination
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatalf("a stored destination with oauth_beta must still load: %v", err)
	}
	if !d.LegacyOAuthBeta {
		t.Fatal("the legacy oauth_beta flag must be read (tolerate + migrate)")
	}
	return d
}

func countRule(rules []modelspec.HeaderRule) int {
	n := 0
	for _, r := range rules {
		if r == PoolOAuthBetaRule() {
			n++
		}
	}
	return n
}

// Schema step 3 (COV-241): an oauth_beta destination becomes a principal
// header rule on every pool-principal spec (and on specs naming the
// destination's own credential), the flag is cleared, and the step never runs
// twice; other specs are untouched and an existing rule is not duplicated.
func TestMigrateModelSpecsOAuthBeta(t *testing.T) {
	st := NewMemStore()
	if err := st.AddDestination(legacyDest(t, "https://api.anthropic.com")); err != nil {
		t.Fatal(err)
	}
	if err := st.AddDestination(Destination{Name: "github", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBearer, CredName: "gh", Apply: ApplyBearer}); err != nil {
		t.Fatal(err)
	}
	def := modelspec.Default(PoolPrincipal)
	pooled := modelspec.Default(PoolPrincipal)
	pooled.Name = "pooled"
	pooled.Principal.Headers = []modelspec.HeaderRule{{Name: "X-Team", Set: "a"}}
	already := modelspec.Default(PoolPrincipal)
	already.Name = "already"
	already.Principal.Headers = []modelspec.HeaderRule{PoolOAuthBetaRule()}
	sub := modelspec.Default("anthropic-sub")
	sub.Name = "sub"
	keyed := modelspec.Default("api-key")
	keyed.Name = "keyed"
	for _, m := range []ModelSpec{def, pooled, already, sub, keyed} {
		if err := st.PutModelSpec(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetModelSpecSchema(2); err != nil {
		t.Fatal(err)
	}
	rep, err := MigrateModelSpecs(st)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Migrated, []string{"claude-default", "pooled", "sub"}) || !slices.Equal(rep.Destinations, []string{"anthropic"}) {
		t.Fatalf("report = %+v", rep)
	}
	for name, want := range map[string]int{"claude-default": 1, "pooled": 1, "already": 1, "sub": 1, "keyed": 0} {
		m, _ := st.GetModelSpec(name)
		if got := countRule(m.Principal.Headers); got != want {
			t.Errorf("%s: oauth beta rules = %d, want %d (%+v)", name, got, want, m.Principal.Headers)
		}
		if err := ValidateModelSpec(m, credIs("anthropic-sub", "api-key"), true); err != nil {
			t.Errorf("migrated %s is invalid: %v", name, err)
		}
	}
	if m, _ := st.GetModelSpec("pooled"); m.Principal.Headers[0].Name != "X-Team" || m.Principal.Headers[1] != PoolOAuthBetaRule() {
		t.Fatalf("the rule must be appended after the existing ones (it used to apply last): %+v", m.Principal.Headers)
	}
	for _, d := range st.ListDestinations() {
		if d.LegacyOAuthBeta {
			t.Fatalf("destination %q kept the removed flag", d.Name)
		}
	}
	if st.ModelSpecSchema() != ModelSpecSchemaVersion || ModelSpecSchemaVersion != 3 {
		t.Fatalf("marker = %d", st.ModelSpecSchema())
	}
	// Recorded: an operator removing the rule later is not undone.
	m, _ := st.GetModelSpec("pooled")
	m.Principal.Headers = nil
	_ = st.PutModelSpec(m)
	if again, err := MigrateModelSpecs(st); err != nil || len(again.Migrated) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
}

// Without an oauth_beta destination step 3 changes nothing.
func TestMigrateModelSpecsOAuthBetaNoFlag(t *testing.T) {
	st := NewMemStore()
	_ = st.AddDestination(anthropicDest())
	_ = st.PutModelSpec(modelspec.Default(PoolPrincipal))
	_ = st.SetModelSpecSchema(2)
	if rep, err := MigrateModelSpecs(st); err != nil || len(rep.Migrated) != 0 || len(rep.Destinations) != 0 {
		t.Fatalf("report = %+v, %v", rep, err)
	}
}

// End to end at the broker: a pool cove behind a pre-COV-241 oauth_beta
// destination still sends anthropic-beta: …oauth-2025-04-20 — exactly once —
// after the migration moved the beta onto its model-spec.
func TestBrokerOAuthBetaAfterMigration(t *testing.T) {
	var got []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Values("anthropic-beta")
		io.WriteString(w, "ok")
	}))
	defer up.Close()

	st := NewMemStore()
	tok, _ := MintToken()
	mustCreateProject(t, st, "ACME")
	if err := st.AddDestination(legacyDest(t, up.URL)); err != nil {
		t.Fatal(err)
	}
	// A pool claude-default seeded before COV-241 (no header rules); the role
	// is unbound, so it resolves to claude-default.
	if err := st.PutModelSpec(modelspec.Default(PoolPrincipal)); err != nil {
		t.Fatal(err)
	}
	_ = st.SetModelSpecSchema(2)
	if err := st.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddActor(Actor{ID: "spider", TokenHash: HashToken(tok), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateModelSpecs(st); err != nil {
		t.Fatal(err)
	}
	b := NewBroker(st, fakeCreds{"anthropic-sub": "REAL"}, testLogger())
	for _, tc := range []struct{ in, want string }{
		{"", "oauth-2025-04-20"},
		{"claude-code-20250219,context-1m-2025-08-07", "claude-code-20250219,context-1m-2025-08-07,oauth-2025-04-20"},
		{"claude-code-20250219,oauth-2025-04-20", "claude-code-20250219,oauth-2025-04-20"},
	} {
		got = nil
		req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+tok)
		if tc.in != "" {
			req.Header.Set("anthropic-beta", tc.in)
		}
		rec := httptest.NewRecorder()
		b.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("code = %d", rec.Code)
		}
		if len(got) != 1 || got[0] != tc.want || strings.Count(got[0], "oauth-2025-04-20") != 1 {
			t.Errorf("in %q: anthropic-beta = %q, want %q (the beta exactly once)", tc.in, got, tc.want)
		}
	}
}

// A backup taken before COV-241 (oauth_beta destinations, schema 2) still
// imports: it validates as migrated, and the import migrates it — the flag
// becomes the pool spec's header rule and is cleared.
func TestMigrateSnapshotOAuthBeta(t *testing.T) {
	doc := `{"version":1,"exported_at":"2026-10-01T00:00:00Z","model_spec_schema":2,
	  "destinations":[{"name":"anthropic","route":"/anthropic/","upstream":"https://api.anthropic.com","identity_in":"bearer","cred_name":"anthropic-sub","apply":"bearer","oauth_beta":true}],
	  "model_specs":[` + string(mustJSON(t, modelspec.Default(PoolPrincipal))) + `]}`
	var snap ConfigSnapshot
	if err := json.Unmarshal([]byte(doc), &snap); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotContext(snap); err != nil {
		t.Fatalf("an old backup must validate: %v", err)
	}
	MigrateSnapshotModelSpecs(&snap)
	if snap.ModelSpecSchema != ModelSpecSchemaVersion || snap.Destinations[0].LegacyOAuthBeta ||
		countRule(snap.ModelSpecs[0].Principal.Headers) != 1 {
		t.Fatalf("migrated snapshot = %+v", snap)
	}
	st := NewMemStore()
	if err := st.ImportConfig(snap); err != nil {
		t.Fatalf("import: %v", err)
	}
	if m, _ := st.GetModelSpec(DefaultModelSpec); countRule(m.Principal.Headers) != 1 {
		t.Fatalf("imported claude-default = %+v", m.Principal)
	}
	if b, _ := json.Marshal(st.ListDestinations()); strings.Contains(string(b), "oauth_beta") {
		t.Fatalf("the removed flag was re-exported: %s", b)
	}
}

// A new claude-default seeded for a pool principal carries the oauth beta
// rule; one seeded for a named credential does not.
func TestDefaultModelSpecForPoolCarriesOAuthBeta(t *testing.T) {
	m, err := DefaultModelSpecFor(NewMemStore(), true)
	if err != nil || !slices.Equal(m.Principal.Headers, []modelspec.HeaderRule{PoolOAuthBetaRule()}) {
		t.Fatalf("pool seed = %+v, %v", m.Principal, err)
	}
	st := NewMemStore()
	_ = st.AddDestination(Destination{Name: "anthropic", Route: "/anthropic/", CredName: "anthropic-key"})
	if m, err := DefaultModelSpecFor(st, false); err != nil || len(m.Principal.Headers) != 0 {
		t.Fatalf("keyed seed = %+v, %v", m.Principal, err)
	}
}

// The flag is gone from the write path: a destination write carrying it is
// refused with a hint naming its replacement.
func TestValidateDestinationRefusesOAuthBeta(t *testing.T) {
	d := anthropicDest()
	d.Upstream = "https://api.anthropic.com"
	d.LegacyOAuthBeta = true
	err := ValidateDestination(d, func(string) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "oauth_beta") || !strings.Contains(err.Error(), "principal") || !strings.Contains(err.Error(), "model-spec-headers.md") {
		t.Fatalf("err = %v", err)
	}
}

// Through the admin API: POST /admin/config of a pre-COV-241 backup (an
// oauth_beta destination, schema 2) succeeds and stores the migrated shape.
func TestAdminConfigImportMigratesOAuthBeta(t *testing.T) {
	dst := NewMemStore()
	ts := httptest.NewServer(NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, testLogger(), nil, nil))
	defer ts.Close()
	body := `{"version":1,"model_spec_schema":2,
	  "destinations":[{"name":"anthropic","route":"/anthropic/","upstream":"https://api.anthropic.com","identity_in":"bearer","cred_name":"anthropic-sub","apply":"bearer","oauth_beta":true}],
	  "model_specs":[` + string(mustJSON(t, modelspec.Default(PoolPrincipal))) + `]}`
	r, err := http.Post(ts.URL+"/admin/config", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("status = %d: %s", r.StatusCode, b)
	}
	if m, _ := dst.GetModelSpec(DefaultModelSpec); countRule(m.Principal.Headers) != 1 {
		t.Fatalf("imported claude-default = %+v", m.Principal)
	}
	if ds := dst.ListDestinations(); len(ds) != 1 || ds[0].LegacyOAuthBeta {
		t.Fatalf("imported destinations = %+v", ds)
	}
	if dst.ModelSpecSchema() != ModelSpecSchemaVersion {
		t.Fatalf("marker = %d", dst.ModelSpecSchema())
	}
}
