package jam

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

// legacyDestJSON is a destination as a pre-COV-241 Jam stored it (pg rows and
// backups carry the removed flag as "oauth_beta").
func legacyDestJSON(name, upstream, cred string) string {
	return `{"name":"` + name + `","route":"/` + name + `/","upstream":"` + upstream + `","identity_in":"bearer","cred_name":"` + cred + `","apply":"bearer","oauth_beta":true}`
}

func legacyDest(t *testing.T, name, upstream, cred string) Destination {
	t.Helper()
	var d Destination
	if err := json.Unmarshal([]byte(legacyDestJSON(name, upstream, cred)), &d); err != nil {
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
		if r == OAuthBetaRule() {
			n++
		}
	}
	return n
}

// poolBroker is a broker whose resolver is the subscription pool (cred name
// "anthropic-sub", one account) chained over a base holding "anthropic-key";
// its upstream records every forwarded anthropic-beta.
type poolBroker struct {
	t     *testing.T
	store *MemStore
	tok   string
	got   []string
	b     *Broker
}

func newPoolBroker(t *testing.T, spec *ModelSpec, roleSpec string, dests ...Destination) *poolBroker {
	t.Helper()
	pb := &poolBroker{t: t, store: NewMemStore()}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pb.got = r.Header.Values("anthropic-beta")
		io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	ps, err := NewFilePoolStore(filepath.Join(t.TempDir(), "pool.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ps.SetAccount(PoolAccount{Name: "a", AccessToken: "SUB", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	pb.tok, _ = MintToken()
	mustCreateProject(t, pb.store, "ACME")
	if spec != nil {
		if err := pb.store.PutModelSpec(*spec); err != nil {
			t.Fatal(err)
		}
	}
	var names []string
	for _, d := range dests {
		d.Upstream = up.URL
		if err := pb.store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
		names = append(names, d.Name)
	}
	if err := pb.store.PutRole("ACME", Role{Name: "guest", ModelSpec: roleSpec, Scope: Scope{Destinations: names}}); err != nil {
		t.Fatal(err)
	}
	if err := pb.store.AddActor(Actor{ID: "spider", TokenHash: HashToken(pb.tok), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	pb.b = NewBroker(pb.store, NewChainResolver(fakeCreds{"anthropic-key": "KEY"}, NewPool(ps), "anthropic-sub"), testLogger())
	return pb
}

func (pb *poolBroker) send(path, betaIn string) []string {
	pb.t.Helper()
	pb.got = nil
	req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+pb.tok)
	if betaIn != "" {
		req.Header.Set("anthropic-beta", betaIn)
	}
	rec := httptest.NewRecorder()
	pb.b.ServeHTTP(rec, req)
	if rec.Code != 200 {
		pb.t.Fatalf("%s: code = %d", path, rec.Code)
	}
	return pb.got
}

func poolDest(name, route string) Destination {
	return Destination{Name: name, Route: route, IdentityIn: ApplyBearer, CredName: "anthropic-sub", Apply: ApplyBearer}
}

// The broker ensures the subscription-OAuth beta whenever the credential comes
// from the pool — exactly once, preserving the cove's betas — with no spec rule,
// no destination flag, and whatever the spec resolution does.
func TestBrokerPoolCredentialGetsOAuthBeta(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "oauth-2025-04-20"},
		{"claude-code-20250219,context-1m-2025-08-07", "claude-code-20250219,context-1m-2025-08-07,oauth-2025-04-20"},
		{"claude-code-20250219,oauth-2025-04-20", "claude-code-20250219,oauth-2025-04-20"},
	}
	def := modelspec.Default(PoolPrincipal)
	missing := "ghost" // the role is bound to a spec that does not exist: resolution fails
	for label, pb := range map[string]*poolBroker{
		"no spec rules":   newPoolBroker(t, &def, "", poolDest("anthropic", "/anthropic/")),
		"spec unresolved": newPoolBroker(t, nil, missing, poolDest("anthropic", "/anthropic/")),
		"other route":     newPoolBroker(t, &def, "", poolDest("claude", "/claude/")),
	} {
		path := "/anthropic/v1/messages"
		if label == "other route" {
			path = "/claude/v1/messages"
		}
		for _, tc := range cases {
			if got := pb.send(path, tc.in); len(got) != 1 || got[0] != tc.want {
				t.Errorf("%s, in %q: anthropic-beta = %q, want %q", label, tc.in, got, tc.want)
			}
		}
	}
}

// The beta is ensured after the principal rules, so a set rule cannot drop it.
func TestBrokerPoolOAuthBetaSurvivesSetRule(t *testing.T) {
	spec := modelspec.Default(PoolPrincipal)
	spec.Principal.Headers = []modelspec.HeaderRule{{Name: "anthropic-beta", Set: "context-1m"}}
	pb := newPoolBroker(t, &spec, "", poolDest("anthropic", "/anthropic/"))
	if got := pb.send("/anthropic/v1/messages", "a"); len(got) != 1 || got[0] != "context-1m,oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q", got)
	}
}

// A non-pool credential gets no beta from the broker — not even behind a
// destination still carrying the legacy flag (the broker never reads it).
func TestBrokerNonPoolCredentialNoOAuthBeta(t *testing.T) {
	keyed := modelspec.Default("anthropic-key")
	d := legacyDest(t, "anthropic", "", "anthropic-key")
	pb := newPoolBroker(t, &keyed, "", d)
	if got := pb.send("/anthropic/v1/messages", "a"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("anthropic-beta = %q, want the cove's betas untouched", got)
	}
}

// The legacy-flag scan: a flagged NON-pool destination's beta moves onto the
// specs whose principal is its credential (appended, never duplicated); a
// flagged pool destination needs no rule (the broker guarantees its beta);
// every flag is cleared. It runs on every startup, whatever the marker, so a
// flag written later (an old binary in a rolling deploy) is handled too.
func TestMigrateLegacyOAuthBetaFlags(t *testing.T) {
	st := NewMemStore()
	_ = st.AddDestination(legacyDest(t, "anthropic", "https://api.anthropic.com", "anthropic-sub")) // pool-backed
	_ = st.AddDestination(legacyDest(t, "solo", "https://api.anthropic.com", "solo-sub"))           // a non-pool subscription
	pooled := modelspec.Default(PoolPrincipal)
	solo := modelspec.Default("solo-sub")
	solo.Name = "solo"
	solo.Principal.Headers = []modelspec.HeaderRule{{Name: "X-Team", Set: "a"}}
	already := modelspec.Default("solo-sub")
	already.Name = "already"
	already.Principal.Headers = []modelspec.HeaderRule{OAuthBetaRule()}
	keyed := modelspec.Default("api-key")
	keyed.Name = "keyed"
	for _, m := range []ModelSpec{pooled, solo, already, keyed} {
		_ = st.PutModelSpec(m)
	}
	_ = st.SetModelSpecSchema(ModelSpecSchemaVersion) // a current store: the scan is not marker-gated
	rep, err := MigrateModelSpecs(st)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rep.Migrated, []string{"solo"}) || !slices.Equal(rep.Destinations, []string{"anthropic", "solo"}) {
		t.Fatalf("report = %+v", rep)
	}
	for name, want := range map[string]int{"claude-default": 0, "solo": 1, "already": 1, "keyed": 0} {
		m, _ := st.GetModelSpec(name)
		if got := countRule(m.Principal.Headers); got != want {
			t.Errorf("%s: oauth beta rules = %d, want %d (%+v)", name, got, want, m.Principal.Headers)
		}
		if err := ValidateModelSpec(m, credIs("solo-sub", "api-key"), true); err != nil {
			t.Errorf("migrated %s is invalid: %v", name, err)
		}
	}
	if m, _ := st.GetModelSpec("solo"); m.Principal.Headers[1] != OAuthBetaRule() {
		t.Fatalf("the rule must be appended after the existing ones: %+v", m.Principal.Headers)
	}
	for _, d := range st.ListDestinations() {
		if d.LegacyOAuthBeta {
			t.Fatalf("destination %q kept the removed flag", d.Name)
		}
	}
	// Idempotent: nothing left to do.
	if again, err := MigrateModelSpecs(st); err != nil || len(again.Migrated) != 0 || len(again.Destinations) != 0 {
		t.Fatalf("second run = %+v, %v", again, err)
	}
	// A flag written later by an old binary is cleared at the next startup.
	_ = st.AddDestination(legacyDest(t, "anthropic", "https://api.anthropic.com", "anthropic-sub"))
	if again, err := MigrateModelSpecs(st); err != nil || !slices.Equal(again.Destinations, []string{"anthropic"}) {
		t.Fatalf("late flag = %+v, %v", again, err)
	}
}

// End to end: a pool cove behind a pre-COV-241 oauth_beta destination still
// sends the beta exactly once after the startup migration.
func TestBrokerOAuthBetaAfterMigration(t *testing.T) {
	def := modelspec.Default(PoolPrincipal)
	d := legacyDest(t, "anthropic", "", "anthropic-sub")
	pb := newPoolBroker(t, &def, "", d)
	if _, err := MigrateModelSpecs(pb.store); err != nil {
		t.Fatal(err)
	}
	if got := pb.send("/anthropic/v1/messages", "claude-code-20250219"); len(got) != 1 || got[0] != "claude-code-20250219,oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q", got)
	}
}

// A backup taken before COV-241 still imports: it validates, the import clears
// the flags and moves a non-pool destination's beta onto its credential's spec.
func TestMigrateSnapshotOAuthBeta(t *testing.T) {
	solo := modelspec.Default("solo-sub")
	solo.Name = "solo"
	doc := `{"version":1,"exported_at":"2026-10-01T00:00:00Z","model_spec_schema":2,
	  "destinations":[` + legacyDestJSON("anthropic", "https://api.anthropic.com", "anthropic-sub") + `,` + legacyDestJSON("solo", "https://api.anthropic.com", "solo-sub") + `],
	  "model_specs":[` + string(mustJSON(t, modelspec.Default(PoolPrincipal))) + `,` + string(mustJSON(t, solo)) + `]}`
	var snap ConfigSnapshot
	if err := json.Unmarshal([]byte(doc), &snap); err != nil {
		t.Fatal(err)
	}
	if err := validateSnapshotContext(snap); err != nil {
		t.Fatalf("an old backup must validate: %v", err)
	}
	MigrateSnapshotModelSpecs(&snap)
	if snap.ModelSpecSchema != ModelSpecSchemaVersion || snap.Destinations[0].LegacyOAuthBeta || snap.Destinations[1].LegacyOAuthBeta ||
		countRule(snap.ModelSpecs[0].Principal.Headers) != 0 || countRule(snap.ModelSpecs[1].Principal.Headers) != 1 {
		t.Fatalf("migrated snapshot = %+v", snap)
	}
	st := NewMemStore()
	if err := st.ImportConfig(snap); err != nil {
		t.Fatalf("import: %v", err)
	}
	if b, _ := json.Marshal(st.ListDestinations()); strings.Contains(string(b), "oauth_beta") {
		t.Fatalf("the removed flag was re-exported: %s", b)
	}
}

// A seeded claude-default carries no header rules — the broker owns the pool beta.
func TestDefaultModelSpecForHasNoHeaderRules(t *testing.T) {
	if m, err := DefaultModelSpecFor(NewMemStore(), true); err != nil || len(m.Principal.Headers) != 0 {
		t.Fatalf("pool seed = %+v, %v", m.Principal, err)
	}
}

// The flag is gone from the write path: a destination write carrying it is
// refused with a hint naming its replacement.
func TestValidateDestinationRefusesOAuthBeta(t *testing.T) {
	d := anthropicDest()
	d.Upstream = "https://api.anthropic.com"
	d.LegacyOAuthBeta = true
	err := ValidateDestination(d, func(string) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "oauth_beta") || !strings.Contains(err.Error(), "pool.md") {
		t.Fatalf("err = %v", err)
	}
}

// Through the admin API: POST /admin/config of a pre-COV-241 backup succeeds
// and stores the migrated shape.
func TestAdminConfigImportMigratesOAuthBeta(t *testing.T) {
	dst := NewMemStore()
	ts := httptest.NewServer(NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, testLogger(), nil, nil))
	defer ts.Close()
	body := `{"version":1,"model_spec_schema":2,
	  "destinations":[` + legacyDestJSON("anthropic", "https://api.anthropic.com", "anthropic-sub") + `],
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
	if ds := dst.ListDestinations(); len(ds) != 1 || ds[0].LegacyOAuthBeta {
		t.Fatalf("imported destinations = %+v", ds)
	}
	if dst.ModelSpecSchema() != ModelSpecSchemaVersion {
		t.Fatalf("marker = %d", dst.ModelSpecSchema())
	}
}
