package jam

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestAdmin(t *testing.T) (http.Handler, Store) {
	t.Helper()
	store := NewMemStore()
	credExists := func(n string) bool { return n == "git-pat" || n == "anthropic-key" }
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return h, store
}

// loopback requests carry a loopback RemoteAddr; httptest.NewRequest defaults to
// 192.0.2.1, so set it explicitly.
func adminReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5000"
	return r
}

// doReq issues a loopback request against h and returns the recorded response.
func doReq(t *testing.T, h http.Handler, method, path string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, body)
	r.RemoteAddr = "127.0.0.1:5000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// doJSON marshals v as the request body and issues the request.
func doJSON(t *testing.T, h http.Handler, method, path string, v any) *httptest.ResponseRecorder {
	t.Helper()
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	return doReq(t, h, method, path, bytes.NewReader(buf))
}

// getJSON issues a GET and decodes the JSON response body into out, failing the
// test on a non-200 status or decode error.
func getJSON(t *testing.T, h http.Handler, path string, out any) {
	t.Helper()
	rec := doReq(t, h, "GET", path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, body=%s", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s JSON: %v", path, err)
	}
}

// decodeJSON decodes a recorder's body into out, failing the test on error.
func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode JSON: %v (body=%s)", err, rec.Body.String())
	}
}

func TestAdminListDestination(t *testing.T) {
	h, store := newTestAdmin(t)
	if err := store.AddDestination(Destination{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: "basic-password", CredName: "git-pat", Apply: "basic-password"}); err != nil {
		t.Fatal(err)
	}
	var got []Destination
	getJSON(t, h, "/admin/destinations", &got)
	if len(got) != 1 || got[0].Name != "git" {
		t.Fatalf("GET destinations = %+v", got)
	}
}

// Destinations change only in git: the write routes are gone, the read stays.
func TestRoleConfigRoutesGone(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "dev", Scope: Scope{Destinations: []string{"git"}},
		Allocation: RoleAllocation{Standing: []StandingSession{{Name: "bot", Prompt: "hi"}}}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/admin/roles"},
		{"DELETE", "/admin/roles/acme/dev"},
		{"POST", "/admin/destinations"},
		{"DELETE", "/admin/destinations/git"},
		{"POST", "/admin/roles/acme/dev/standing"},
		{"DELETE", "/admin/roles/acme/dev/standing/bot"},
		{"PUT", "/admin/roles/acme/dev/egress"},
		{"DELETE", "/admin/roles/acme/dev/egress"},
		{"POST", "/admin/model-specs"},
		{"PUT", "/admin/model-specs/default"},
		{"DELETE", "/admin/model-specs/default"},
	} {
		if rec := doReq(t, h, c.method, c.path, strings.NewReader("{}")); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", c.method, c.path, rec.Code)
		}
	}
	// The kept neighbours still answer: the reads, and standing reset.
	for _, p := range []string{"/admin/roles?project=acme", "/admin/destinations", "/admin/roles/acme/dev/standing", "/admin/roles/acme/dev/egress"} {
		if rec := doReq(t, h, "GET", p, nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, rec.Code)
		}
	}
	if rec := doReq(t, h, "POST", "/admin/roles/acme/dev/standing/bot/reset", nil); rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
		t.Errorf("standing reset = %d, want it mounted", rec.Code)
	}
	if _, ok := store.GetRole("acme", "dev"); !ok || len(store.ListDestinations()) != 0 {
		t.Error("a removed route changed the store")
	}
}

func TestAdminRejectsUnresolvableCredName(t *testing.T) {
	err := ValidateDestination(Destination{Name: "bad", Route: "/bad/", Upstream: "https://x", IdentityIn: "bearer", CredName: "nope", Apply: "bearer"}, func(n string) bool { return n == "git-pat" })
	if err == nil {
		t.Fatal("want an error for an unresolvable cred_name")
	}
}

func TestAdminEnrollThenRevoke(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "ACME")
	if err := store.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{"git"}}}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/enrollments", `{"id":"spider-18","project":"ACME","role":"guest"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var res EnrollResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Token == "" || res.ID != "spider-18" {
		t.Fatalf("enroll result = %+v", res)
	}
	if _, ok := store.Lookup(HashToken(res.Token)); !ok {
		t.Fatal("enrolled identity not in store")
	}
	// GET must not leak tokens or hashes, and must report the effective scope.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/actors", ""))
	if bytes.Contains(rec.Body.Bytes(), []byte(res.Token)) || bytes.Contains(rec.Body.Bytes(), []byte(HashToken(res.Token))) {
		t.Fatal("roster leaked token or hash")
	}
	var roster []ActorSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &roster); err != nil {
		t.Fatalf("roster JSON: %v", err)
	}
	if len(roster) != 1 || len(roster[0].Grants) != 1 || roster[0].Grants[0].Destinations[0] != "git" {
		t.Fatalf("roster = %+v", roster)
	}
	// revoke
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("DELETE", "/admin/enrollments/spider-18", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d", rec.Code)
	}
	if _, ok := store.Lookup(HashToken(res.Token)); ok {
		t.Fatal("identity still present after revoke")
	}
}

func TestAdminHandlerMountsBrowser(t *testing.T) {
	store := NewMemStore()
	browser := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("APP:" + r.URL.Path))
	})
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), browser)

	// Everything outside /admin/ reaches the browser handler WITHOUT the API
	// auth gate applying — the browser app owns its own gates.
	for _, p := range []string{"/", "/app/admin/coves", "/ui/coves", "/me/studios/x"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.RemoteAddr = "203.0.113.9:1000" // off-loopback: the API gate would have 403'd
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "APP:"+p {
			t.Fatalf("GET %s = %d %q, want 200 APP:%s (browser owns its own auth)", p, rec.Code, rec.Body.String(), p)
		}
	}

	// /admin/* is still guarded by the API auth.
	rec := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodGet, "/admin/actors", nil)
	badReq.RemoteAddr = "203.0.113.9:1000"
	h.ServeHTTP(rec, badReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-loopback GET /admin/actors = %d, want 403", rec.Code)
	}
}

// fixedOperator authenticates every request as a known operator — stands in for
// the OIDC authenticator so the test can assert the sub is attributed in logs.
type fixedOperator struct{ id string }

func (f fixedOperator) Authenticate(*http.Request) (Operator, error) { return Operator{ID: f.id}, nil }

func TestAdminLogsOperatorOnMutations(t *testing.T) {
	store := NewMemStore()
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, nil))
	credExists := func(n string) bool { return n == "git-pat" }
	h := NewAdminHandler(store, nil, nil, fixedOperator{id: "auth0|alice"}, credExists, nil, log, nil)
	mustCreateProject(t, store, "ACME")
	if err := store.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{"git"}}}); err != nil {
		t.Fatal(err)
	}

	// enroll + revoke — each is a mutation and must be attributed.
	h.ServeHTTP(httptest.NewRecorder(), adminReq("POST", "/admin/enrollments",
		`{"id":"spider-18","project":"ACME","role":"guest"}`))
	h.ServeHTTP(httptest.NewRecorder(), adminReq("DELETE", "/admin/enrollments/spider-18", ""))

	logs := logbuf.String()
	for _, want := range []string{
		`admin enrolled`,
		`admin revoked`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("missing mutation log %q\n---\n%s", want, logs)
		}
	}
	// every mutation line must carry operator=auth0|alice.
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(line, "msg=\"admin ") && !strings.Contains(line, `operator=auth0|alice`) {
			t.Errorf("mutation log not attributed to operator: %s", line)
		}
	}
	// the raw token must never appear in logs.
	if strings.Contains(logs, "token=") && !strings.Contains(logs, "operator=") {
		t.Error("unexpected token in logs")
	}
}

// denyAll rejects every request — proves /admin/login-config bypasses operator auth.
type denyAll struct{}

func (denyAll) Authenticate(*http.Request) (Operator, error) {
	return Operator{}, errDeny
}

var errDeny = fmt.Errorf("denied")

func TestLoginConfigServedAndAuthExempt(t *testing.T) {
	store := NewMemStore()
	lc := &OperatorLoginConfig{Issuer: "https://acme.auth0.com/", Audience: "https://jam.acme/api", ClientID: "cid", Scope: "openid"}
	h := NewAdminHandler(store, nil, nil, denyAll{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	// login-config is reachable with NO token even though the authenticator denies all.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/login-config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("login-config status = %d, want 200 (auth-exempt)", rec.Code)
	}
	var got OperatorLoginConfig
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got != *lc {
		t.Fatalf("login-config = %+v, want %+v", got, *lc)
	}
	// a normal route is still gated.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/destinations", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("destinations status = %d, want 403", rec.Code)
	}
}

func TestLoginConfig404WhenNotConfigured(t *testing.T) {
	store := NewMemStore()
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/login-config", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login-config status = %d, want 404 when not OIDC-gated", rec.Code)
	}
}

func TestAdminRolesList(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Name != "guest" || roles[0].TTLSeconds != 3600 {
		t.Fatalf("roles = %+v", roles)
	}
	var projs []string
	getJSON(t, h, "/admin/projects", &projs)
	if len(projs) == 0 {
		t.Fatal("expected acme in projects")
	}
}

func TestAdminEnrollRequiresExistingRole(t *testing.T) {
	h, store := newTestAdmin(t)
	// no role yet → 400 (fail closed)
	rec := doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "x", Role: "guest"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("enroll with missing role = %d, want 400", rec.Code)
	}
	// create the role, then enroll succeeds
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "x", Role: "guest"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d, want 201", rec.Code)
	}
}

func TestAdminGrantAddRemove(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme", "beta")
	for _, r := range []struct{ project, name, dest string }{{"acme", "guest", "anthropic"}, {"beta", "review", "git"}} {
		if err := store.PutRole(r.project, Role{Name: r.name, Scope: Scope{Destinations: []string{r.dest}}}); err != nil {
			t.Fatal(err)
		}
	}
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "guest"})
	rec := doJSON(t, h, "POST", "/admin/actors/m/grants", GrantBody{Project: "beta", Role: "review"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add grant = %d", rec.Code)
	}
	var roster []ActorSummary
	getJSON(t, h, "/admin/actors", &roster)
	if len(roster) != 1 || len(roster[0].Grants) != 2 {
		t.Fatalf("roster = %+v", roster)
	}
	rec = doReq(t, h, "DELETE", "/admin/actors/m/grants/beta/review", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove grant = %d", rec.Code)
	}
}

// TestAdminEscalationRoutes sets an escalation policy in the store then GETs it
// back, asserting the read route reflects a real MemStore.
func TestAdminEscalationRoutes(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.SetEscalationPolicy("acme", "", []EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	var got EscalationView
	getJSON(t, h, "/admin/projects/acme/escalation", &got)
	if len(got.Default) != 1 || got.Default[0].Targets[0] != "human:alice" || got.Default[0].Timeout != 15*time.Minute {
		t.Fatalf("escalation policy = %+v", got.Default)
	}
}

// TestAdminEscalationCategoryRoutes sets a category-scoped chain alongside the
// default chain, asserting GET returns both in EscalationView.
func TestAdminEscalationCategoryRoutes(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.SetEscalationPolicy("acme", "", []EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEscalationPolicy("acme", "infra", []EscalationTier{{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}); err != nil {
		t.Fatal(err)
	}
	var got EscalationView
	getJSON(t, h, "/admin/projects/acme/escalation", &got)
	if len(got.Default) != 1 || got.Default[0].Targets[0] != "human:alice" {
		t.Fatalf("default chain = %+v", got.Default)
	}
	infra, ok := got.ByCategory["infra"]
	if !ok || len(infra) != 1 || infra[0].Targets[0] != "human:sre" || infra[0].Timeout != 10*time.Minute {
		t.Fatalf("infra chain = %+v", got.ByCategory)
	}
}

// TestChatServiceRoute sets a project's chat service in the store then GETs it
// back, asserting the read route reflects a real MemStore, and that
// operator auth (non-loopback) is enforced on the route like every other
// /admin/* route.
func TestChatServiceRoute(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")

	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	var got ChatServiceView
	getJSON(t, h, "/admin/projects/acme/chat-service", &got)
	if got.Service != "discord" {
		t.Fatalf("chat-service view = %+v, want discord", got)
	}
	p, ok := store.GetProject("acme")
	if !ok || ChatKind(store, p) != "discord" {
		t.Fatalf("store project chat-service = %q (ok=%v), want a discord connection", p.ChatService, ok)
	}

	// operator auth is enforced on this route, like every other /admin/* route.
	r := httptest.NewRequest("GET", "/admin/projects/acme/chat-service", nil)
	r.RemoteAddr = "10.0.0.9:1234"
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, r)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("non-loopback GET chat-service = %d, want 403", rec2.Code)
	}
}

func TestAdminRoleAddressingRoundTrips(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "impl", Scope: Scope{Destinations: []string{"anthropic"}, Addressing: []string{"human:*", "channel:eng-help"}}}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || len(roles[0].Addressing) != 2 || roles[0].Addressing[0] != "human:*" || roles[0].Addressing[1] != "channel:eng-help" {
		t.Fatalf("roles = %+v", roles)
	}
}

// A role's max-ephemeral allocation policy reads back through GET /admin/roles.
func TestAdminRoleMaxEphemeralRoundTrips(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "worker", Scope: Scope{Destinations: []string{"anthropic"}}, Allocation: RoleAllocation{MaxEphemeral: 4}}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].MaxEphemeral != 4 {
		t.Fatalf("roles = %+v, want max_ephemeral 4", roles)
	}
}

// A role's personal caps read back through GET /admin/roles.
func TestAdminRoleMaxPersonalRoundTrips(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "worker", Allocation: RoleAllocation{MaxPersonal: 3, MaxPersonalPerOwner: 1}}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].MaxPersonal != 3 || roles[0].MaxPersonalPerOwner != 1 {
		t.Fatalf("roles = %+v, want max_personal 3 / per-owner 1", roles)
	}
}

// A role's personal idle settings read back through GET /admin/roles as seconds.
func TestAdminRoleIdleSettingsRoundTrip(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "pair", Allocation: RoleAllocation{IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 72 * time.Hour}}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].IdleAfterSeconds != 3600 || roles[0].NagEverySeconds != 7200 || roles[0].ReclaimAfterSeconds != 259200 {
		t.Fatalf("roles = %+v, want idle 3600 / nag 7200 / reclaim 259200", roles)
	}
}

func TestAdminRoleTurnEndRoundTrip(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	te := TurnEndPolicy{IdleTimeout: 30 * time.Minute, OnIdle: OnIdleTeardown, TimeZone: "Europe/Berlin"}
	if err := store.PutRole("acme", Role{Name: "worker", TurnEnd: te}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].IdleTimeoutSeconds != 1800 || roles[0].OnIdle != "teardown" || roles[0].TimeZone != "Europe/Berlin" {
		t.Fatalf("roles = %+v", roles)
	}
}

// The turn-end policy a role is saved with is validated by ValidateTurnEnd.
func TestValidateTurnEndRejectsBad(t *testing.T) {
	for _, te := range []TurnEndPolicy{{IdleTimeout: -time.Second}, {OnIdle: "nap"}, {TimeZone: "Mars/Olympus"}} {
		if err := ValidateTurnEnd(te); err == nil {
			t.Errorf("ValidateTurnEnd(%+v) = nil, want an error", te)
		}
	}
}

func TestRosterSummaries(t *testing.T) {
	_, store := newTestAdmin(t)
	if err := store.PutRole("default", Role{Name: "worker", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddActor(Actor{ID: "spider-1", TokenHash: "deadbeef", Grants: []Grant{{Project: "default", Role: "worker"}}}); err != nil {
		t.Fatal(err)
	}
	out := RosterSummaries(store)
	if len(out) != 1 || out[0].ID != "spider-1" {
		t.Fatalf("summaries = %+v, want one actor spider-1", out)
	}
	if len(out[0].Grants) != 1 || out[0].Grants[0].Role != "worker" {
		t.Fatalf("grants = %+v, want worker", out[0].Grants)
	}
	g := out[0].Grants[0]
	if len(g.Destinations) != 1 || g.Destinations[0] != "anthropic" {
		t.Errorf("effective destinations = %v, want [anthropic]", g.Destinations)
	}
}

func TestAdminRejectsNonLoopback(t *testing.T) {
	h, _ := newTestAdmin(t)
	r := httptest.NewRequest("GET", "/admin/destinations", nil)
	r.RemoteAddr = "10.0.0.9:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for non-loopback", rec.Code)
	}
}

// mustPushStudioKit pushes config as kit name's next version, failing the
// test on error, and returns the version.
func mustPushStudioKit(t *testing.T, store Store, name, config string) (int, bool) {
	t.Helper()
	v, unchanged, err := PushStudioKit(store, name, config)
	if err != nil {
		t.Fatalf("push %s: %v", name, err)
	}
	return v, unchanged
}

// pushStatus is the status a refused kit push (PushStudioKit) answered with.
func pushStatus(store Store, name, config string) int {
	_, _, err := PushStudioKit(store, name, config)
	if err == nil {
		return 0
	}
	return WriteStatus(err, http.StatusInternalServerError)
}

func TestAdminKitsCRUD(t *testing.T) {
	h, store := newTestAdmin(t)
	// push v1, v2 — two valid, distinct configs (parsed and validated as a
	// studio kit, stored as kit.yml byte for byte). v2 adds an egress domain
	// so the versions differ.
	if v, _ := mustPushStudioKit(t, store, "web", "kind: studio\nname: web\n"); v != 1 {
		t.Fatalf("push v1 = v%d", v)
	}
	if v, _ := mustPushStudioKit(t, store, "web", "kind: studio\negress:\n  - example.com\n"); v != 2 {
		t.Fatalf("push v2 = v%d", v)
	}
	// re-pushing the current kit.yml makes no new version
	if v, unchanged := mustPushStudioKit(t, store, "web", "kind: studio\negress:\n  - example.com\n"); v != 2 || !unchanged {
		t.Fatalf("unchanged push = v%d unchanged=%v, want v2 unchanged", v, unchanged)
	}
	// a legacy in-file name must match the name it's pushed under
	if code := pushStatus(store, "web", "kind: studio\nname: api\n"); code != http.StatusBadRequest {
		t.Fatalf("mismatched legacy name push = %d, want 400", code)
	}
	// an invalid config (unknown field) is rejected at ingestion, not stored
	if code := pushStatus(store, "web", "name: web\nnope: 1\n"); code != http.StatusBadRequest {
		t.Fatalf("invalid config push = %d, want 400", code)
	}
	// list (still 2 versions — the rejected push stored nothing)
	var kits []KitSummary
	getJSON(t, h, "/admin/kits", &kits)
	if len(kits) != 1 || kits[0].Current != 2 || kits[0].Versions != 2 {
		t.Fatalf("list = %+v", kits)
	}
	// show current + specific version — stored form is kit.yml as pushed
	var cur KitConfigResult
	getJSON(t, h, "/admin/kits/web", &cur)
	if cur.Version != 2 || cur.Config != "kind: studio\negress:\n  - example.com\n" || cur.Tree == "" || len(cur.Files) != 1 {
		t.Fatalf("show current = %+v (want the pushed kit.yml, one file)", cur)
	}
	var old KitConfigResult
	getJSON(t, h, "/admin/kits/web?version=1", &old)
	if old.Version != 1 || old.Config != "kind: studio\nname: web\n" {
		t.Fatalf("show v1 = %+v (want the pushed kit.yml)", old)
	}
	// versions
	var vers []int
	getJSON(t, h, "/admin/kits/web/versions", &vers)
	if len(vers) != 2 || vers[0] != 1 || vers[1] != 2 {
		t.Fatalf("versions = %+v", vers)
	}
	// pin back to 1
	if err := PinKitChecked(store, "web", 1); err != nil {
		t.Fatalf("pin = %v", err)
	}
	getJSON(t, h, "/admin/kits/web", &cur)
	if cur.Version != 1 {
		t.Fatalf("after pin, current = %d", cur.Version)
	}
	// pin to absent → 404
	if err := PinKitChecked(store, "web", 9); err == nil || WriteStatus(err, http.StatusNotFound) != http.StatusNotFound {
		t.Fatalf("pin absent = %v", err)
	}
	// rm
	if err := RemoveKitChecked(store, slog.New(slog.DiscardHandler), "web"); err != nil {
		t.Fatalf("rm = %v", err)
	}
}

func TestAdminKitRemoveBlockedByRole(t *testing.T) {
	_, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	mustPushStudioKit(t, store, "builder", "kind: studio\nname: builder\n")
	if err := store.PutRole("acme", Role{Name: "impl", Kit: "builder"}); err != nil {
		t.Fatal(err)
	}
	// rm while referenced → 409
	if err := RemoveKitChecked(store, slog.New(slog.DiscardHandler), "builder"); WriteStatus(err, http.StatusNotFound) != http.StatusConflict {
		t.Fatalf("rm referenced kit = %v, want 409", err)
	}
}

func TestAdminRoleSummaryShowsKit(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	mustPushStudioKit(t, store, "builder", "kind: studio\nname: builder\n")
	if err := store.PutRole("acme", Role{Name: "impl", Kit: "builder"}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Kit != "builder" {
		t.Fatalf("role summary = %+v", roles)
	}
}

func TestAdminKitsPushRejectsNonStudioConfig(t *testing.T) {
	_, store := newTestAdmin(t)
	// A config without kind: studio is rejected (fail-closed on the authoritative
	// server path), even if otherwise valid-looking.
	if code := pushStatus(store, "web", "name: web\nsecrets: {}\n"); code != http.StatusBadRequest {
		t.Fatalf("push non-studio config = %d, want 400", code)
	}
}

func newTestAdminWithSupervisor(t *testing.T) (http.Handler, Store, *Supervisor) {
	t.Helper()
	h, store, sup, _ := newTestAdminWithSupervisorAndLauncher(t)
	return h, store, sup
}

func newTestAdminWithSupervisorAndLauncher(t *testing.T) (http.Handler, Store, *Supervisor, *fakeLauncher) {
	t.Helper()
	store := NewMemStore()
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{liveness: LivenessAlive}
	sup := NewSupervisor(store, launcher, "holder-admin",
		time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	credExists := func(n string) bool { return true }
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return h, store, sup, launcher
}

func TestCoveRaiseListStatusTeardown(t *testing.T) {
	h, store, _ := newTestAdminWithSupervisor(t)

	// Raise.
	rec := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "w1", Role: "guest", Unit: "AET-9"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("raise code = %d body=%s", rec.Code, rec.Body.String())
	}
	var res CoveRaiseResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Token == "" || res.Phase != string(PhaseLive) {
		t.Fatalf("raise result = %+v", res)
	}

	// List — never leaks a token/hash.
	var coves []CoveSummary
	getJSON(t, h, "/admin/coves", &coves)
	if len(coves) != 1 || coves[0].ID != res.ID || coves[0].Name != "w1" || coves[0].Role != "guest" || coves[0].Unit != "AET-9" {
		t.Fatalf("list = %+v", coves)
	}
	// The runtime summary must never carry identity secrets.
	if body := string(mustJSON(t, coves)); strings.Contains(body, "token") || strings.Contains(body, "hash") {
		t.Fatalf("cove summary leaks a secret field: %s", body)
	}

	// Status report.
	if rc := doJSON(t, h, "POST", "/admin/coves/w1/status", CoveStatusBody{Activity: "waiting"}); rc.Code != http.StatusNoContent {
		t.Fatalf("status code = %d body=%s", rc.Code, rc.Body.String())
	}
	got, _ := store.GetInstance(res.ID) // the label addressed it
	if got.Activity != ActivityWaiting {
		t.Fatalf("activity = %s", got.Activity)
	}

	// Bad activity → 400.
	if rc := doJSON(t, h, "POST", "/admin/coves/w1/status", CoveStatusBody{Activity: "bogus"}); rc.Code != http.StatusBadRequest {
		t.Fatalf("bad activity code = %d", rc.Code)
	}

	// Teardown.
	if rc := doReq(t, h, "DELETE", "/admin/coves/w1", nil); rc.Code != http.StatusNoContent {
		t.Fatalf("teardown code = %d", rc.Code)
	}
	if _, ok := store.GetInstance(res.ID); ok {
		t.Fatal("instance still present after teardown")
	}
	// The label is free again: a new raise is a new session.
	rec = doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "w1", Role: "guest"})
	var again CoveRaiseResult
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &again) != nil || again.ID == res.ID {
		t.Fatalf("re-raise under the label = %d %+v; want a new session", rec.Code, again)
	}
	// ...and while it lives, the label is taken.
	if rc := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "w1", Role: "guest"}); rc.Code != http.StatusConflict {
		t.Fatalf("duplicate live label = %d, want 409", rc.Code)
	}
}

func TestCoveRaisePassesPromptToLauncher(t *testing.T) {
	h, _, _, launcher := newTestAdminWithSupervisorAndLauncher(t)

	rec := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "w1", Role: "guest", Prompt: "do the thing"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("raise code = %d body=%s", rec.Code, rec.Body.String())
	}
	if launcher.gotSpec.Prompt != "do the thing" {
		t.Fatalf("launcher got prompt = %q, want %q", launcher.gotSpec.Prompt, "do the thing")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAdminConfigExportImport(t *testing.T) {
	src := populated(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srcH := NewAdminHandler(src, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil)
	srcTS := httptest.NewServer(srcH)
	defer srcTS.Close()

	resp, err := http.Get(srcTS.URL + "/admin/config")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	dst := NewMemStore()
	dstH := NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil)
	dstTS := httptest.NewServer(dstH)
	defer dstTS.Close()

	post := func(payload []byte) int {
		r, err := http.Post(dstTS.URL+"/admin/config", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if code := post(body); code != http.StatusNoContent {
		t.Fatalf("POST import status = %d, want 204", code)
	}
	if a, ok := dst.Lookup(HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("import did not restore actor: %+v ok=%v", a, ok)
	}
	if code := post(body); code != http.StatusConflict {
		t.Fatalf("second POST status = %d, want 409", code)
	}
}

func TestAdminConfigImportBadVersion(t *testing.T) {
	dst := NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	defer ts.Close()
	r, err := http.Post(ts.URL+"/admin/config", "application/json", strings.NewReader(`{"version":999}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestWithAdminRouteIsGuarded(t *testing.T) {
	store := NewMemStore()
	extra := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("extra")) })
	h := NewAdminHandler(store, nil, nil, denyAll{}, func(string) bool { return true }, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, WithAdminRoute("GET /admin/sessions/{actor_id}/events", extra))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/sessions/w1/events", nil))
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request reached the route: %d", rec.Code)
	}
	h = NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, WithAdminRoute("GET /admin/sessions/{actor_id}/events", extra))
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/sessions/w1/events", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	h.ServeHTTP(rec, req)
	if rec.Body.String() != "extra" {
		t.Fatalf("route not mounted: %d %q", rec.Code, rec.Body.String())
	}
}

func TestAdminRoleCredentialsValidatedAndEchoed(t *testing.T) {
	h, store := newTestAdmin(t) // credExists: git-pat, anthropic-key
	mustCreateProject(t, store, "acme")
	credExists := func(n string) bool { return n == "git-pat" || n == "anthropic-key" }
	if err := ValidateCredentials(Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "nope"}}, credExists); err == nil {
		t.Fatal("unknown credential: want an error")
	}
	if err := ValidateCredentials(Scope{Destinations: []string{"anthropic"}, Credentials: map[string]string{"git": "git-pat"}}, credExists); err == nil {
		t.Fatal("credential for a destination not allowed: want an error")
	}
	scope := Scope{Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat"}}
	if err := ValidateCredentials(scope, credExists); err != nil {
		t.Fatalf("valid credentials: %v", err)
	}
	if err := store.PutRole("acme", Role{Name: "w", Scope: scope}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Credentials["git"] != "git-pat" {
		t.Fatalf("roles = %+v", roles)
	}
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "w"})
	var roster []ActorSummary
	getJSON(t, h, "/admin/actors", &roster)
	if len(roster) != 1 || roster[0].Grants[0].Credentials["git"] != "git-pat" {
		t.Fatalf("roster = %+v", roster)
	}
}

func TestAdminGrantAndEnrollOverrideCredentialsValidated(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", Role{Name: "w", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	ov := &Override{Credentials: map[string]string{"git": "git-pat"}} // git not in the effective scope
	if rec := doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "w", Overrides: ov}); rec.Code != http.StatusBadRequest {
		t.Fatalf("enroll with bad override = %d, want 400", rec.Code)
	}
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "w"})
	if rec := doJSON(t, h, "POST", "/admin/actors/m/grants", GrantBody{Project: "acme", Role: "w", Overrides: ov}); rec.Code != http.StatusBadRequest {
		t.Fatalf("grant with bad override = %d, want 400", rec.Code)
	}
	good := &Override{Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat"}}
	if rec := doJSON(t, h, "POST", "/admin/actors/m/grants", GrantBody{Project: "acme", Role: "w", Overrides: good}); rec.Code != http.StatusCreated {
		t.Fatalf("grant with valid override = %d, want 201", rec.Code)
	}
}

func TestAdminAddDestinationValidatesEnv(t *testing.T) {
	store := NewMemStore()
	credExists := func(string) bool { return true }
	bad := Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", Env: map[string]string{"JAM_IDENTITY_TOKEN": "{token}"}}
	if err := ValidateDestination(bad, credExists); err == nil {
		t.Fatal("reserved env key: want an error")
	}
	good := Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", Env: map[string]string{"GH_HOST": "{host}"}, Git: true}
	if err := ValidateDestination(good, credExists); err != nil {
		t.Fatalf("valid env: %v", err)
	}
	if err := store.AddDestination(good); err != nil {
		t.Fatal(err)
	}
	if d := store.ListDestinations(); len(d) != 1 || d[0].Env["GH_HOST"] != "{host}" || !d[0].Git {
		t.Fatalf("stored = %+v", d)
	}
}

func TestAdminEnrollReturnsConnector(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := store.AddDestination(Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", Env: map[string]string{"GH_HOST": "{host}"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("acme", Role{Name: "w", Scope: Scope{Destinations: []string{"gh"}}}); err != nil {
		t.Fatal(err)
	}
	rec := doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "w"})
	var res EnrollResult
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Connector == nil || res.Connector.Env["GH_HOST"] != "{host}" {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminConfigImportInvalidContext(t *testing.T) {
	dst := NewMemStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil))
	defer ts.Close()
	body := fmt.Sprintf(`{"version":%d,"jam_context":{"core":%q}}`, ConfigSnapshotVersion, strings.Repeat("x", 900))
	r, err := http.Post(ts.URL+"/admin/config", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestAdminRoleCollaborateRoundTripAndValidation(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	if err := PutRoleKeeping(store, "acme", Role{Name: "pair", Collaborate: CollaboratePolicy{SSH: "members", Preview: "none"}}); err != nil {
		t.Fatal(err)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].CollaborateSSH != "members" || roles[0].CollaboratePreview != "none" {
		t.Fatalf("roles = %+v", roles)
	}
	// A re-put that leaves the policy zero keeps it.
	if err := PutRoleKeeping(store, "acme", Role{Name: "pair"}); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	if r, _ := store.GetRole("acme", "pair"); r.Collaborate != (CollaboratePolicy{Preview: "none", SSH: "members"}) {
		t.Fatalf("re-put dropped the policy: %+v", r.Collaborate)
	}
	for _, c := range []CollaboratePolicy{{SSH: "everyone"}, {Preview: "yes"}} {
		if err := c.Validate(); err == nil {
			t.Fatalf("bad policy %+v: want an error", c)
		}
	}
}
