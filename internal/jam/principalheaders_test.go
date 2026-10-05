package jam

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam/modelspec"
)

func testLoggerTo(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

func withHeaders(rules ...modelspec.HeaderRule) ModelSpec {
	m := validSpec()
	m.Principal.Headers = rules
	return m
}

func TestValidatePrincipalHeadersAccepts(t *testing.T) {
	m := withHeaders(
		modelspec.HeaderRule{Name: "anthropic-beta", EnsureListItem: "oauth-2025-04-20"},
		modelspec.HeaderRule{Name: "X-Foo", Set: "bar baz=1"},
	)
	if err := ValidateModelSpec(m, credIs("anthropic"), false); err != nil {
		t.Fatalf("valid header rules refused: %v", err)
	}
	full := make([]modelspec.HeaderRule, MaxPrincipalHeaderRules)
	for i := range full {
		full[i] = modelspec.HeaderRule{Name: fmt.Sprintf("X-H%d", i), Set: "v"}
	}
	if err := ValidateModelSpec(withHeaders(full...), credIs("anthropic"), false); err != nil {
		t.Fatalf("%d rules refused: %v", MaxPrincipalHeaderRules, err)
	}
}

func TestValidatePrincipalHeadersRefusals(t *testing.T) {
	tooMany := make([]modelspec.HeaderRule, MaxPrincipalHeaderRules+1)
	for i := range tooMany {
		tooMany[i] = modelspec.HeaderRule{Name: fmt.Sprintf("X-H%d", i), Set: "v"}
	}
	for name, tc := range map[string]struct {
		rules []modelspec.HeaderRule
		want  string
	}{
		"no name":         {[]modelspec.HeaderRule{{Set: "v"}}, "principal.headers[0]"},
		"bad name":        {[]modelspec.HeaderRule{{Name: "X Foo", Set: "v"}}, "not a valid HTTP header name"},
		"neither":         {[]modelspec.HeaderRule{{Name: "X-Foo"}}, "exactly one of set or ensure-list-item"},
		"both":            {[]modelspec.HeaderRule{{Name: "X-Foo", Set: "a", EnsureListItem: "b"}}, "exactly one of set or ensure-list-item"},
		"authorization":   {[]modelspec.HeaderRule{{Name: "authorization", Set: "v"}}, "Authorization"},
		"x-api-key":       {[]modelspec.HeaderRule{{Name: "x-api-key", Set: "v"}}, "X-Api-Key"},
		"cookie":          {[]modelspec.HeaderRule{{Name: "Cookie", Set: "v"}}, "Cookie"},
		"proxy-anything":  {[]modelspec.HeaderRule{{Name: "Proxy-Foo", Set: "v"}}, "Proxy-Foo"},
		"hop-by-hop":      {[]modelspec.HeaderRule{{Name: "Connection", Set: "v"}}, "hop-by-hop"},
		"host":            {[]modelspec.HeaderRule{{Name: "Host", Set: "v"}}, "hop-by-hop"},
		"crlf set":        {[]modelspec.HeaderRule{{Name: "X-Foo", Set: "a\r\nX-Evil: 1"}}, "single-line"},
		"lf item":         {[]modelspec.HeaderRule{{Name: "X-Foo", EnsureListItem: "a\nb"}}, "single-line"},
		"comma item":      {[]modelspec.HeaderRule{{Name: "X-Foo", EnsureListItem: "a,b"}}, "comma"},
		"padded item":     {[]modelspec.HeaderRule{{Name: "X-Foo", EnsureListItem: " a"}}, "whitespace"},
		"too many":        {tooMany, "at most"},
		"second rule bad": {[]modelspec.HeaderRule{{Name: "X-Ok", Set: "v"}, {Name: "Cookie", Set: "v"}}, "principal.headers[1]"},
	} {
		err := ValidateModelSpec(withHeaders(tc.rules...), credIs("anthropic"), false)
		if err == nil {
			t.Errorf("%s: accepted, want a 400 mentioning %q", name, tc.want)
			continue
		}
		if WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want 400 mentioning %q", name, err, tc.want)
		}
	}
}

func TestValidatePrincipalHeadersNeverEchoesValues(t *testing.T) {
	err := ValidateModelSpec(withHeaders(modelspec.HeaderRule{Name: "Cookie", Set: "session=sekrit"}), credIs("anthropic"), false)
	if err == nil || strings.Contains(err.Error(), "sekrit") {
		t.Fatalf("err = %v; want a refusal that does not echo the value", err)
	}
	err = ValidateModelSpec(withHeaders(modelspec.HeaderRule{Name: "X-Foo", Set: "sekrit\n"}), credIs("anthropic"), false)
	if err == nil || strings.Contains(err.Error(), "sekrit") {
		t.Fatalf("err = %v; want a refusal that does not echo the value", err)
	}
}

func TestEnsureListItem(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "x"},
		{"a", "a,x"},
		{"a,x", "a,x"},
		{"a, x ,b", "a, x ,b"},
		{"x", "x"},
		{"xx,a", "xx,a,x"},
	} {
		h := http.Header{}
		if tc.in != "" {
			h.Set("anthropic-beta", tc.in)
		}
		ensureListItem(h, "anthropic-beta", "x")
		if got := h.Get("anthropic-beta"); got != tc.want {
			t.Errorf("ensureListItem(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// principalCase is a broker over one pool-principal spec (rules as given) bound
// to a role scoped to dests; it records what each upstream received.
type principalCase struct {
	t      *testing.T
	broker *Broker
	tok    string
	got    map[string]http.Header // destination route → headers received
	logs   *strings.Builder
}

func newPrincipalCase(t *testing.T, rules []modelspec.HeaderRule, dests ...Destination) *principalCase {
	t.Helper()
	pc := &principalCase{t: t, got: map[string]http.Header{}}
	store := NewMemStore()
	tok, _ := MintToken()
	pc.tok = tok
	mustCreateProject(t, store, "ACME")
	spec := modelspec.Default(PoolPrincipal)
	spec.Name = "pooled"
	spec.Principal.Headers = rules
	if err := store.PutModelSpec(spec); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range dests {
		route := d.Route
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pc.got[route] = r.Header.Clone()
			io.WriteString(w, "ok")
		}))
		t.Cleanup(up.Close)
		d.Upstream = up.URL
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
		names = append(names, d.Name)
	}
	if err := store.PutRole("ACME", Role{Name: "guest", ModelSpec: "pooled", Scope: Scope{Destinations: names}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(Actor{ID: "spider", TokenHash: HashToken(tok), Grants: []Grant{{Project: "ACME", Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	pc.logs = &strings.Builder{}
	pc.broker = NewBroker(store, fakeCreds{"anthropic-sub": "REAL", "gh": "GHREAL"}, testLoggerTo(pc.logs))
	return pc
}

// send sends one request to path carrying header kv pairs and returns the
// headers the upstream at route received.
func (pc *principalCase) send(path, route string, kv ...string) http.Header {
	pc.t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+pc.tok)
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Set(kv[i], kv[i+1])
	}
	rec := httptest.NewRecorder()
	pc.broker.ServeHTTP(rec, req)
	h, ok := pc.got[route]
	if rec.Code != 200 || !ok {
		pc.t.Fatalf("request to %s did not reach upstream (code=%d)", path, rec.Code)
	}
	delete(pc.got, route)
	return h
}

func anthropicDest() Destination {
	return Destination{Name: "anthropic", Route: "/anthropic/", IdentityIn: ApplyBearer, CredName: "anthropic-sub", Apply: ApplyBearer}
}

func gitDest() Destination {
	return Destination{Name: "github", Route: "/git/", IdentityIn: ApplyBearer, CredName: "gh", Apply: ApplyBearer}
}

var oauthRule = modelspec.HeaderRule{Name: "anthropic-beta", EnsureListItem: "oauth-2025-04-20"}

func TestBrokerPrincipalEnsureListItemAddsBetaOnce(t *testing.T) {
	pc := newPrincipalCase(t, []modelspec.HeaderRule{oauthRule}, anthropicDest())
	for _, tc := range []struct{ in, want string }{
		{"", "oauth-2025-04-20"},
		{"claude-code-20250219", "claude-code-20250219,oauth-2025-04-20"},
		{"claude-code-20250219,oauth-2025-04-20", "claude-code-20250219,oauth-2025-04-20"},
		{"oauth-2025-04-20", "oauth-2025-04-20"},
	} {
		var kv []string
		if tc.in != "" {
			kv = []string{"anthropic-beta", tc.in}
		}
		h := pc.send("/anthropic/v1/messages", "/anthropic/", kv...)
		if got := h.Get("anthropic-beta"); got != tc.want {
			t.Errorf("in %q: anthropic-beta = %q, want %q", tc.in, got, tc.want)
		}
		if got := h.Get("Authorization"); got != "Bearer REAL" {
			t.Errorf("credential = %q, want Bearer REAL", got)
		}
	}
}

func TestBrokerPrincipalRuleAndOAuthBetaFlagCompose(t *testing.T) {
	d := anthropicDest()
	d.OAuthBeta = true
	pc := newPrincipalCase(t, []modelspec.HeaderRule{oauthRule}, d)
	h := pc.send("/anthropic/v1/messages", "/anthropic/", "anthropic-beta", "a")
	if got := h.Get("anthropic-beta"); got != "a,oauth-2025-04-20" {
		t.Fatalf("anthropic-beta = %q, want the beta exactly once", got)
	}
}

func TestBrokerPrincipalSetRule(t *testing.T) {
	pc := newPrincipalCase(t, []modelspec.HeaderRule{{Name: "X-Foo", Set: "bar"}}, anthropicDest())
	h := pc.send("/anthropic/v1/messages", "/anthropic/", "X-Foo", "caller")
	if got := h.Values("X-Foo"); len(got) != 1 || got[0] != "bar" {
		t.Fatalf("X-Foo = %q, want [bar]", got)
	}
}

func TestBrokerPrincipalRulesNotAppliedToOtherDestinations(t *testing.T) {
	pc := newPrincipalCase(t, []modelspec.HeaderRule{oauthRule, {Name: "X-Foo", Set: "bar"}}, anthropicDest(), gitDest())
	h := pc.send("/git/org/repo.git/info/refs", "/git/")
	if h.Get("anthropic-beta") != "" || h.Get("X-Foo") != "" {
		t.Fatalf("rules leaked to the git destination: %v", h)
	}
	if got := h.Get("Authorization"); got != "Bearer GHREAL" {
		t.Fatalf("git credential = %q", got)
	}
}

// Without a destination named "anthropic", the one routed at /anthropic/ serves
// the provider (the same rule DefaultModelSpecFor uses); with one, a different
// destination on that route does not.
func TestBrokerPrincipalProviderDestinationByRoute(t *testing.T) {
	d := anthropicDest()
	d.Name = "claude-sub"
	pc := newPrincipalCase(t, []modelspec.HeaderRule{oauthRule}, d)
	if got := pc.send("/anthropic/v1/messages", "/anthropic/").Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Fatalf("route-matched destination: anthropic-beta = %q", got)
	}

	other := Destination{Name: "other", Route: "/anthropic2/", IdentityIn: ApplyBearer, CredName: "anthropic-sub", Apply: ApplyBearer}
	named := anthropicDest()
	named.Route = "/claude/"
	pc = newPrincipalCase(t, []modelspec.HeaderRule{oauthRule}, named, other)
	if got := pc.send("/anthropic2/v1/messages", "/anthropic2/").Get("anthropic-beta"); got != "" {
		t.Fatalf("non-provider destination got anthropic-beta %q", got)
	}
	if got := pc.send("/claude/v1/messages", "/claude/").Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Fatalf("destination named anthropic: anthropic-beta = %q", got)
	}
}

func TestBrokerPrincipalRuleNotAppliedForOtherProviders(t *testing.T) {
	pc := newPrincipalCase(t, []modelspec.HeaderRule{oauthRule}, anthropicDest())
	spec, _ := pc.broker.store.GetModelSpec("pooled")
	spec.Claude.Provider = "vertex"
	if err := pc.broker.store.PutModelSpec(spec); err != nil {
		t.Fatal(err)
	}
	if got := pc.send("/anthropic/v1/messages", "/anthropic/").Get("anthropic-beta"); got != "" {
		t.Fatalf("vertex spec applied a rule on the anthropic destination: %q", got)
	}
}

// A rule naming a header the destination's identity or apply spec uses is
// skipped at apply time with a warning naming the header only; the credential
// stays Jam's.
func TestBrokerPrincipalRuleConflictingWithDestinationSkipped(t *testing.T) {
	d := anthropicDest()
	d.Apply = ApplyCustom
	d.ApplySpec = &OutboundSpec{Header: "X-Upstream-Key", Template: "{cred}"}
	pc := newPrincipalCase(t, []modelspec.HeaderRule{
		{Name: "x-upstream-key", Set: "rule-value-123"},
		{Name: "X-Ok", Set: "fine"},
	}, d)
	h := pc.send("/anthropic/v1/messages", "/anthropic/")
	if got := h.Get("X-Upstream-Key"); got != "REAL" {
		t.Fatalf("X-Upstream-Key = %q, want the credential", got)
	}
	if got := h.Get("X-Ok"); got != "fine" {
		t.Fatalf("non-conflicting rule not applied: X-Ok = %q", got)
	}
	logs := pc.logs.String()
	if !strings.Contains(logs, "X-Upstream-Key") || strings.Contains(logs, "rule-value-123") || strings.Contains(logs, "fine") {
		t.Fatalf("want a warning naming the header but no value, logs:\n%s", logs)
	}
}

// A rule a write-time check would refuse (it reached the store another way) is
// skipped at apply time too.
func TestBrokerPrincipalStaticDenylistRecheckedAtApply(t *testing.T) {
	pc := newPrincipalCase(t, []modelspec.HeaderRule{{Name: "Cookie", Set: "c=1"}}, anthropicDest())
	if got := pc.send("/anthropic/v1/messages", "/anthropic/").Get("Cookie"); got != "" {
		t.Fatalf("denied rule applied: Cookie = %q", got)
	}
}
