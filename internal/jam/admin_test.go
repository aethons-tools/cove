package jam

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func newTestAdmin(t *testing.T) (http.Handler, Store) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	credExists := func(n string) bool { return n == "git-pat" || n == "anthropic-key" }
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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

func TestAdminAddAndListDestination(t *testing.T) {
	h, store := newTestAdmin(t)
	body := `{"name":"git","route":"/git/","upstream":"https://github.com","identity_in":"basic-password","cred_name":"git-pat","apply":"basic-password","repo_scoped":true}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/destinations", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if len(store.ListDestinations()) != 1 {
		t.Fatalf("store has %d destinations", len(store.ListDestinations()))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/destinations", ""))
	var got []Destination
	json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Name != "git" {
		t.Fatalf("GET destinations = %+v", got)
	}
}

func TestAdminRejectsUnresolvableCredName(t *testing.T) {
	h, _ := newTestAdmin(t)
	body := `{"name":"bad","route":"/bad/","upstream":"https://x","identity_in":"bearer","cred_name":"nope","apply":"bearer"}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/destinations", body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unresolvable cred_name", rec.Code)
	}
}

func TestAdminEnrollThenRevoke(t *testing.T) {
	h, store := newTestAdmin(t)
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
	h.ServeHTTP(rec, adminReq("GET", "/admin/roster", ""))
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

func TestAdminHandlerMountsUI(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("UI:" + r.URL.Path))
	})
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), ui, nil)

	// Root redirects to /ui/.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq(http.MethodGet, "/", ""))
	if rec.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Errorf("redirect Location = %q, want /ui/", loc)
	}

	// /ui/* reaches the mounted handler WITHOUT the API auth gate applying — the
	// ui handler owns its own auth now.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.9:1000" // off-loopback: the API gate would have 403'd before
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "UI:/ui/coves" {
		t.Fatalf("GET /ui/coves = %d %q, want 200 UI:/ui/coves (ui owns its own auth)", rec.Code, rec.Body.String())
	}

	// /admin/* is still guarded by the API auth.
	rec = httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodGet, "/admin/roster", nil)
	badReq.RemoteAddr = "203.0.113.9:1000"
	h.ServeHTTP(rec, badReq)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-loopback GET /admin/roster = %d, want 403", rec.Code)
	}
}

// fixedOperator authenticates every request as a known operator — stands in for
// the OIDC authenticator so the test can assert the sub is attributed in logs.
type fixedOperator struct{ id string }

func (f fixedOperator) Authenticate(*http.Request) (Operator, error) { return Operator{ID: f.id}, nil }

func TestAdminLogsOperatorOnMutations(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, nil))
	credExists := func(n string) bool { return n == "git-pat" }
	h := NewAdminHandler(store, nil, nil, fixedOperator{id: "auth0|alice"}, credExists, nil, log, nil, nil)
	if err := store.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{"git"}}}); err != nil {
		t.Fatal(err)
	}

	// add destination + enroll + revoke — each is a mutation and must be attributed.
	h.ServeHTTP(httptest.NewRecorder(), adminReq("POST", "/admin/destinations",
		`{"name":"git","route":"/git/","upstream":"https://github.com","identity_in":"basic-password","cred_name":"git-pat","apply":"basic-password"}`))
	h.ServeHTTP(httptest.NewRecorder(), adminReq("POST", "/admin/enrollments",
		`{"id":"spider-18","project":"ACME","role":"guest"}`))
	h.ServeHTTP(httptest.NewRecorder(), adminReq("DELETE", "/admin/destinations/git", ""))
	h.ServeHTTP(httptest.NewRecorder(), adminReq("DELETE", "/admin/enrollments/spider-18", ""))

	logs := logbuf.String()
	for _, want := range []string{
		`admin destination added`,
		`admin enrolled`,
		`admin destination removed`,
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
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	lc := &OperatorLoginConfig{Issuer: "https://acme.auth0.com/", Audience: "https://jam.acme/api", ClientID: "cid", Scope: "openid"}
	h := NewAdminHandler(store, nil, nil, denyAll{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

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
	store, _ := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	h := NewAdminHandler(store, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/login-config", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login-config status = %d, want 404 when not OIDC-gated", rec.Code)
	}
}

func TestAdminRolesCRUD(t *testing.T) {
	h, _ := newTestAdmin(t) // existing helper: returns handler + store
	// create
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "guest", Destinations: []string{"anthropic"}, TTLSeconds: 3600})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /admin/roles = %d", rec.Code)
	}
	// list
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Name != "guest" || roles[0].TTLSeconds != 3600 {
		t.Fatalf("roles = %+v", roles)
	}
	// projects
	var projs []string
	getJSON(t, h, "/admin/projects", &projs)
	if len(projs) == 0 {
		t.Fatal("expected acme in projects")
	}
	// delete
	rec = doReq(t, h, "DELETE", "/admin/roles/acme/guest", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE role = %d", rec.Code)
	}
}

func TestAdminEnrollRequiresExistingRole(t *testing.T) {
	h, _ := newTestAdmin(t)
	// no role yet → 400 (fail closed)
	rec := doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "x", Role: "guest"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("enroll with missing role = %d, want 400", rec.Code)
	}
	// create the role, then enroll succeeds
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Name: "guest", Destinations: []string{"anthropic"}})
	rec = doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "x", Role: "guest"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("enroll = %d, want 201", rec.Code)
	}
}

func TestAdminGrantAddRemove(t *testing.T) {
	h, _ := newTestAdmin(t)
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "guest", Destinations: []string{"anthropic"}})
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "beta", Name: "review", Destinations: []string{"git"}})
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "guest"})
	rec := doJSON(t, h, "POST", "/admin/actors/m/grants", GrantBody{Project: "beta", Role: "review"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add grant = %d", rec.Code)
	}
	var roster []ActorSummary
	getJSON(t, h, "/admin/roster", &roster)
	if len(roster) != 1 || len(roster[0].Grants) != 2 {
		t.Fatalf("roster = %+v", roster)
	}
	rec = doReq(t, h, "DELETE", "/admin/actors/m/grants/beta/review", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove grant = %d", rec.Code)
	}
}

func TestAdminRosterRoutes(t *testing.T) {
	h, _ := newTestAdmin(t)
	rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", Human{Name: "alice", Handle: "alice.h"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST human = %d", rec.Code)
	}
	rec = doJSON(t, h, "POST", "/admin/projects/acme/channels", Channel{Name: "eng-help", Service: "linear", Ref: "ACME-1"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST channel = %d", rec.Code)
	}
	var rr Roster
	getJSON(t, h, "/admin/projects/acme/roster", &rr)
	if len(rr.Humans) != 1 || rr.Humans[0].Name != "alice" || rr.Humans[0].Handle != "alice.h" {
		t.Fatalf("roster humans = %+v", rr.Humans)
	}
	if len(rr.Channels) != 1 || rr.Channels[0].Name != "eng-help" || rr.Channels[0].Ref != "ACME-1" {
		t.Fatalf("roster channels = %+v", rr.Channels)
	}
	rec = doReq(t, h, "DELETE", "/admin/projects/acme/humans/alice", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE human = %d", rec.Code)
	}
	rec = doReq(t, h, "DELETE", "/admin/projects/acme/channels/eng-help", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE channel = %d", rec.Code)
	}
	var after Roster
	getJSON(t, h, "/admin/projects/acme/roster", &after)
	if len(after.Humans) != 0 || len(after.Channels) != 0 {
		t.Fatalf("roster after removal = %+v", after)
	}
}

// TestAdminEscalationRoutes PUTs an escalation policy then GETs it back,
// asserting a round-trip through a real FileStore + admin handler.
func TestAdminEscalationRoutes(t *testing.T) {
	h, _ := newTestAdmin(t)
	rec := doJSON(t, h, "PUT", "/admin/projects/acme/escalation", EscalationBody{
		Tiers: []EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT escalation = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got EscalationView
	getJSON(t, h, "/admin/projects/acme/escalation", &got)
	if len(got.Default) != 1 || got.Default[0].Targets[0] != "human:alice" || got.Default[0].Timeout != 15*time.Minute {
		t.Fatalf("escalation policy = %+v", got.Default)
	}
}

// TestAdminEscalationCategoryRoutes PUTs a category-scoped chain alongside the
// default chain, asserting GET returns both in EscalationView.
func TestAdminEscalationCategoryRoutes(t *testing.T) {
	h, _ := newTestAdmin(t)
	rec := doJSON(t, h, "PUT", "/admin/projects/acme/escalation", EscalationBody{
		Tiers: []EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT default escalation = %d, body=%s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, h, "PUT", "/admin/projects/acme/escalation", EscalationBody{
		Category: "infra",
		Tiers:    []EscalationTier{{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT infra escalation = %d, body=%s", rec.Code, rec.Body.String())
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

// TestChatServiceRoute PUTs a project's chat service then GETs it back,
// asserting a round-trip through a real FileStore + admin handler, and that
// operator auth (non-loopback) is enforced on the route like every other
// /admin/* route.
func TestChatServiceRoute(t *testing.T) {
	h, store := newTestAdmin(t)

	rec := doJSON(t, h, "PUT", "/admin/projects/acme/chat-service", ChatServiceBody{Service: "discord"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT chat-service = %d, body=%s", rec.Code, rec.Body.String())
	}
	var got ChatServiceView
	getJSON(t, h, "/admin/projects/acme/chat-service", &got)
	if got.Service != "discord" {
		t.Fatalf("chat-service view = %+v, want discord", got)
	}
	p, ok := store.GetProject("acme")
	if !ok || p.ChatService != "discord" {
		t.Fatalf("store project chat-service = %q (ok=%v), want discord", p.ChatService, ok)
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
	h, _ := newTestAdmin(t)
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "impl", Destinations: []string{"anthropic"}, Addressing: []string{"human:*", "channel:eng-help"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST role = %d", rec.Code)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || len(roles[0].Addressing) != 2 || roles[0].Addressing[0] != "human:*" || roles[0].Addressing[1] != "channel:eng-help" {
		t.Fatalf("roles = %+v", roles)
	}
}

// A role's max-ephemeral allocation policy round-trips through POST/GET
// /admin/roles; a negative cap is rejected.
func TestAdminRoleMaxEphemeralRoundTrips(t *testing.T) {
	h, store := newTestAdmin(t)
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "worker", Destinations: []string{"anthropic"}, MaxEphemeral: 4})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST role = %d", rec.Code)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].MaxEphemeral != 4 {
		t.Fatalf("roles = %+v, want max_ephemeral 4", roles)
	}
	if r, ok := store.GetRole("acme", "worker"); !ok || r.Allocation.MaxEphemeral != 4 {
		t.Fatalf("stored role = %+v, %v", r, ok)
	}
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "bad", MaxEphemeral: -1}); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative max_ephemeral = %d, want 400", rec.Code)
	}
}

// A role's personal caps round-trip through POST/GET /admin/roles; a negative
// cap is rejected.
func TestAdminRoleMaxPersonalRoundTrips(t *testing.T) {
	h, store := newTestAdmin(t)
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "worker", MaxPersonal: 3, MaxPersonalPerOwner: 1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST role = %d", rec.Code)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].MaxPersonal != 3 || roles[0].MaxPersonalPerOwner != 1 {
		t.Fatalf("roles = %+v, want max_personal 3 / per-owner 1", roles)
	}
	if r, ok := store.GetRole("acme", "worker"); !ok || r.Allocation.MaxPersonal != 3 || r.Allocation.MaxPersonalPerOwner != 1 {
		t.Fatalf("stored role = %+v, %v", r, ok)
	}
	for _, b := range []RoleBody{{Project: "acme", Name: "bad", MaxPersonal: -1}, {Project: "acme", Name: "bad", MaxPersonalPerOwner: -1}} {
		if rec := doJSON(t, h, "POST", "/admin/roles", b); rec.Code != http.StatusBadRequest {
			t.Fatalf("negative personal cap %+v = %d, want 400", b, rec.Code)
		}
	}
}

// A role's personal idle settings round-trip through POST/GET /admin/roles as
// seconds; a negative setting is rejected.
func TestAdminRoleIdleSettingsRoundTrip(t *testing.T) {
	h, store := newTestAdmin(t)
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "pair", IdleAfterSeconds: 3600, NagEverySeconds: 7200, ReclaimAfterSeconds: 259200})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST role = %d", rec.Code)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].IdleAfterSeconds != 3600 || roles[0].NagEverySeconds != 7200 || roles[0].ReclaimAfterSeconds != 259200 {
		t.Fatalf("roles = %+v, want idle 3600 / nag 7200 / reclaim 259200", roles)
	}
	want := RoleAllocation{IdleAfter: time.Hour, NagEvery: 2 * time.Hour, ReclaimAfter: 72 * time.Hour}
	if r, ok := store.GetRole("acme", "pair"); !ok || !reflect.DeepEqual(r.Allocation, want) {
		t.Fatalf("stored role = %+v, %v; want allocation %+v", r, ok, want)
	}
	for _, b := range []RoleBody{
		{Project: "acme", Name: "bad", IdleAfterSeconds: -1},
		{Project: "acme", Name: "bad", NagEverySeconds: -1},
		{Project: "acme", Name: "bad", ReclaimAfterSeconds: -1},
	} {
		if rec := doJSON(t, h, "POST", "/admin/roles", b); rec.Code != http.StatusBadRequest {
			t.Fatalf("negative idle setting %+v = %d, want 400", b, rec.Code)
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

func TestAdminKitsCRUD(t *testing.T) {
	h, _ := newTestAdmin(t)
	// push v1, v2 — two valid, distinct configs (config RoE: parsed as YAML,
	// stored as JSON). v2 adds an egress domain so the versions differ.
	var r1 KitResult
	decodeJSON(t, doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "kind: studio\nname: web\n"}), &r1)
	if r1.Version != 1 {
		t.Fatalf("push v1 = %+v", r1)
	}
	var r2 KitResult
	decodeJSON(t, doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "kind: studio\negress:\n  - example.com\n"}), &r2)
	if r2.Version != 2 {
		t.Fatalf("push v2 = %+v", r2)
	}
	// re-pushing the current definition makes no new version
	var r3 KitResult
	decodeJSON(t, doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "kind: studio\negress: [example.com]\n"}), &r3)
	if r3.Version != 2 || !r3.Unchanged {
		t.Fatalf("unchanged push = %+v, want v2 unchanged", r3)
	}
	// a legacy in-file name must match the name it's pushed under
	if code := doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "kind: studio\nname: api\n"}).Code; code != http.StatusBadRequest {
		t.Fatalf("mismatched legacy name push = %d, want 400", code)
	}
	// an invalid config (unknown field) is rejected at ingestion, not stored
	if code := doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "name: web\nnope: 1\n"}).Code; code != http.StatusBadRequest {
		t.Fatalf("invalid config push = %d, want 400", code)
	}
	// list (still 2 versions — the rejected push stored nothing)
	var kits []KitSummary
	getJSON(t, h, "/admin/kits", &kits)
	if len(kits) != 1 || kits[0].Current != 2 || kits[0].Versions != 2 {
		t.Fatalf("list = %+v", kits)
	}
	// show current + specific version — stored form is canonical JSON
	var cur KitConfigResult
	getJSON(t, h, "/admin/kits/web", &cur)
	if cur.Version != 2 || !json.Valid([]byte(cur.Config)) ||
		strings.Contains(cur.Config, `"name"`) || !strings.Contains(cur.Config, "example.com") {
		t.Fatalf("show current = %+v (want canonical JSON with example.com)", cur)
	}
	var old KitConfigResult
	getJSON(t, h, "/admin/kits/web?version=1", &old)
	if old.Version != 1 || !json.Valid([]byte(old.Config)) ||
		strings.Contains(old.Config, `"name"`) || strings.Contains(old.Config, "example.com") {
		t.Fatalf("show v1 = %+v (want canonical JSON without example.com)", old)
	}
	// versions
	var vers []int
	getJSON(t, h, "/admin/kits/web/versions", &vers)
	if len(vers) != 2 || vers[0] != 1 || vers[1] != 2 {
		t.Fatalf("versions = %+v", vers)
	}
	// pin back to 1
	if rec := doJSON(t, h, "POST", "/admin/kits/web/pin", PinBody{Version: 1}); rec.Code != http.StatusNoContent {
		t.Fatalf("pin = %d", rec.Code)
	}
	getJSON(t, h, "/admin/kits/web", &cur)
	if cur.Version != 1 {
		t.Fatalf("after pin, current = %d", cur.Version)
	}
	// pin to absent → 404
	if rec := doJSON(t, h, "POST", "/admin/kits/web/pin", PinBody{Version: 9}); rec.Code != http.StatusNotFound {
		t.Fatalf("pin absent = %d", rec.Code)
	}
	// rm
	if rec := doReq(t, h, "DELETE", "/admin/kits/web", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("rm = %d", rec.Code)
	}
}

func TestAdminKitRemoveBlockedByRole(t *testing.T) {
	h, _ := newTestAdmin(t)
	doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "builder", Config: "kind: studio\nname: builder\n"})
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "impl", Kit: "builder"}); rec.Code != http.StatusCreated {
		t.Fatalf("role add = %d", rec.Code)
	}
	// rm while referenced → 409
	if rec := doReq(t, h, "DELETE", "/admin/kits/builder", nil); rec.Code != http.StatusConflict {
		t.Fatalf("rm referenced kit = %d, want 409", rec.Code)
	}
}

func TestAdminRoleRejectsMissingKit(t *testing.T) {
	h, _ := newTestAdmin(t)
	if rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Name: "impl", Kit: "ghost"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("role with missing kit = %d, want 400", rec.Code)
	}
	// roster/role summary reflects a valid kit
	doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "builder", Config: "kind: studio\nname: builder\n"})
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "impl", Kit: "builder"})
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Kit != "builder" {
		t.Fatalf("role summary = %+v", roles)
	}
}

func TestAdminKitsPushRejectsNonStudioConfig(t *testing.T) {
	h, _ := newTestAdmin(t)
	// A config without kind: studio is rejected (fail-closed on the authoritative
	// server path), even if otherwise valid-looking.
	rec := doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "name: web\nsecrets: {}\n"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("push non-studio config = %d, want 400", rec.Code)
	}
}

func newTestAdminWithSupervisor(t *testing.T) (http.Handler, Store, *Supervisor) {
	t.Helper()
	h, store, sup, _ := newTestAdminWithSupervisorAndLauncher(t)
	return h, store, sup
}

func newTestAdminWithSupervisorAndLauncher(t *testing.T) (http.Handler, Store, *Supervisor, *fakeLauncher) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{liveness: LivenessAlive}
	sup := NewSupervisor(store, launcher, "holder-admin",
		time.Minute, 30*time.Second, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	credExists := func(n string) bool { return true }
	h := NewAdminHandler(store, sup, nil, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
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
	if len(coves) != 1 || coves[0].ID != "w1" || coves[0].Role != "guest" || coves[0].Unit != "AET-9" {
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
	got, _ := store.GetInstance("w1")
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
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("instance still present after teardown")
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

// A roster human's login (the admin operator identity) round-trips through the
// roster routes, and a login may link at most one human per project.
func TestAdminRosterHumanLogin(t *testing.T) {
	h, _ := newTestAdmin(t)
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", Human{Name: "alice", Handle: "alice.h", Login: "auth0|abc"}); rec.Code != http.StatusCreated {
		t.Fatalf("POST human = %d %s", rec.Code, rec.Body.String())
	}
	var rr Roster
	getJSON(t, h, "/admin/projects/acme/roster", &rr)
	if len(rr.Humans) != 1 || rr.Humans[0].Login != "auth0|abc" {
		t.Fatalf("roster humans = %+v", rr.Humans)
	}
	// Re-upserting the same human with the same login is fine.
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", Human{Name: "alice", Handle: "alice2", Login: "auth0|abc"}); rec.Code != http.StatusCreated {
		t.Fatalf("re-upsert alice = %d %s", rec.Code, rec.Body.String())
	}
	// A different human claiming the same login in the same project is rejected.
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", Human{Name: "bob", Handle: "bob.h", Login: "auth0|abc"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate login = %d, want 400", rec.Code)
	}
	// ...but the same login may link a human in another project.
	if rec := doJSON(t, h, "POST", "/admin/projects/beta/humans", Human{Name: "bob", Handle: "bob.h", Login: "auth0|abc"}); rec.Code != http.StatusCreated {
		t.Fatalf("same login in another project = %d %s", rec.Code, rec.Body.String())
	}
}

// A roster human's Discord user id binds at most one human per project, must be
// a snowflake (all digits), and only a discord profile may carry one.
func TestAdminRosterHumanDiscordUser(t *testing.T) {
	h, _ := newTestAdmin(t)
	bound := func(name, ch, uid string) Human {
		return Human{Name: name, Handle: name + ".h", Delivery: []DeliveryProfile{{Service: "discord", Address: ch, UserID: uid}}}
	}
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", bound("alice", "inbox-a", "111")); rec.Code != http.StatusCreated {
		t.Fatalf("POST alice = %d %s", rec.Code, rec.Body.String())
	}
	var rr Roster
	getJSON(t, h, "/admin/projects/acme/roster", &rr)
	if d, ok := rr.Humans[0].DeliveryFor("discord"); !ok || d.UserID != "111" {
		t.Fatalf("roster humans = %+v", rr.Humans)
	}
	// Re-adding the same human with the same id is fine.
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", bound("alice", "inbox-a2", "111")); rec.Code != http.StatusCreated {
		t.Fatalf("re-upsert alice = %d %s", rec.Code, rec.Body.String())
	}
	// A different human claiming the same id in the same project is rejected.
	rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", bound("bob", "inbox-b", "111"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"alice"`) {
		t.Fatalf("duplicate discord user = %d %s, want 400 naming alice", rec.Code, rec.Body.String())
	}
	// ...but the same id may bind a human in another project.
	if rec := doJSON(t, h, "POST", "/admin/projects/beta/humans", bound("bob", "inbox-b", "111")); rec.Code != http.StatusCreated {
		t.Fatalf("same id in another project = %d %s", rec.Code, rec.Body.String())
	}
	// A non-snowflake id is rejected.
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", bound("carol", "inbox-c", "abc")); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-digit id = %d, want 400", rec.Code)
	}
	// A user id on a non-discord profile is rejected.
	nd := Human{Name: "dan", Handle: "dan.h", Delivery: []DeliveryProfile{{Service: "linear", Address: "x", UserID: "222"}}}
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", nd); rec.Code != http.StatusBadRequest {
		t.Fatalf("user id on linear profile = %d, want 400", rec.Code)
	}
	getJSON(t, h, "/admin/projects/acme/roster", &rr)
	for _, hu := range rr.Humans {
		if hu.Name != "alice" {
			t.Fatalf("a rejected human was added: %+v", hu)
		}
	}
}

// A roster human's OIDC identity binding round-trips, and a malformed one
// (empty issuer or subject) is rejected with 400, leaving the roster unchanged.
func TestAdminRosterHumanOIDCIdentity(t *testing.T) {
	h, _ := newTestAdmin(t)
	good := Human{Name: "alice", Handle: "alice.h", Identity: []OIDCIdentity{{Issuer: "https://accounts.google.com", Subject: "alice-sub"}}}
	if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", good); rec.Code != http.StatusCreated {
		t.Fatalf("POST alice = %d %s", rec.Code, rec.Body.String())
	}
	var rr Roster
	getJSON(t, h, "/admin/projects/acme/roster", &rr)
	if len(rr.Humans) != 1 || len(rr.Humans[0].Identity) != 1 || rr.Humans[0].Identity[0].Subject != "alice-sub" {
		t.Fatalf("roster humans = %+v", rr.Humans)
	}
	for _, bad := range []OIDCIdentity{{Issuer: "", Subject: "x"}, {Issuer: "x", Subject: ""}} {
		nd := Human{Name: "bob", Handle: "bob.h", Identity: []OIDCIdentity{bad}}
		if rec := doJSON(t, h, "POST", "/admin/projects/acme/humans", nd); rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed identity %+v = %d, want 400", bad, rec.Code)
		}
	}
	getJSON(t, h, "/admin/projects/acme/roster", &rr)
	for _, hu := range rr.Humans {
		if hu.Name != "alice" {
			t.Fatalf("a rejected human was added: %+v", hu)
		}
	}
}

func TestAdminConfigExportImport(t *testing.T) {
	src := populated(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srcH := NewAdminHandler(src, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil)
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

	dst, _ := NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	dstH := NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil)
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
	dst, _ := NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(NewAdminHandler(dst, nil, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, log, nil, nil))
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

func TestAdminRoleCredentialsValidatedAndEchoed(t *testing.T) {
	h, _ := newTestAdmin(t) // credExists: git-pat, anthropic-key
	bad := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "w", Destinations: []string{"git"}, Credentials: map[string]string{"git": "nope"}})
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown credential = %d, want 400", bad.Code)
	}
	notAllowed := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "w", Destinations: []string{"anthropic"}, Credentials: map[string]string{"git": "git-pat"}})
	if notAllowed.Code != http.StatusBadRequest {
		t.Fatalf("credential for a destination not allowed = %d, want 400", notAllowed.Code)
	}
	ok := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "w", Destinations: []string{"git"}, Credentials: map[string]string{"git": "git-pat"}})
	if ok.Code != http.StatusCreated {
		t.Fatalf("valid credentials = %d, want 201", ok.Code)
	}
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Credentials["git"] != "git-pat" {
		t.Fatalf("roles = %+v", roles)
	}
	doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "w"})
	var roster []ActorSummary
	getJSON(t, h, "/admin/roster", &roster)
	if len(roster) != 1 || roster[0].Grants[0].Credentials["git"] != "git-pat" {
		t.Fatalf("roster = %+v", roster)
	}
}

func TestAdminGrantAndEnrollOverrideCredentialsValidated(t *testing.T) {
	h, _ := newTestAdmin(t)
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "w", Destinations: []string{"anthropic"}})
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
	h, store := newTestAdmin(t)
	bad := `{"name":"gh","route":"/api/v3/","upstream":"https://api.github.com","env":{"AT_JAM_IDENTITY_TOKEN":"{token}"}}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/destinations", bad))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("reserved env key = %d, want 400", rec.Code)
	}
	good := `{"name":"gh","route":"/api/v3/","upstream":"https://api.github.com","env":{"GH_HOST":"{host}"},"git":true}`
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/destinations", good))
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid env = %d, body=%s", rec.Code, rec.Body.String())
	}
	if d := store.ListDestinations(); len(d) != 1 || d[0].Env["GH_HOST"] != "{host}" || !d[0].Git {
		t.Fatalf("stored = %+v", d)
	}
}

func TestAdminEnrollReturnsConnector(t *testing.T) {
	h, store := newTestAdmin(t)
	if err := store.AddDestination(Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", Env: map[string]string{"GH_HOST": "{host}"}}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "w", Destinations: []string{"gh"}})
	rec := doJSON(t, h, "POST", "/admin/enrollments", EnrollBody{ID: "m", Project: "acme", Role: "w"})
	var res EnrollResult
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.Connector == nil || res.Connector.Env["GH_HOST"] != "{host}" {
		t.Fatalf("enroll = %d %s", rec.Code, rec.Body.String())
	}
}
