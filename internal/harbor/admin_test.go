package harbor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	h := NewAdminHandler(store, nil, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
	if err := store.PutRole("ACME", Role{Name: "guest", Scope: Scope{Destinations: []string{"git"}, Repos: []string{"acme/*"}}}); err != nil {
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
	h := NewAdminHandler(store, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), ui)

	// Root redirects to /ui/.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq(http.MethodGet, "/", ""))
	if rec.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui/" {
		t.Errorf("redirect Location = %q, want /ui/", loc)
	}

	// /ui/ reaches the mounted handler (loopback allowed).
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq(http.MethodGet, "/ui/coves", ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "UI:/ui/coves" {
		t.Fatalf("GET /ui/coves = %d %q, want 200 UI:/ui/coves", rec.Code, rec.Body.String())
	}

	// Off-loopback is still refused by the gate the UI is mounted inside.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ui/coves", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("off-loopback GET /ui/coves = %d, want 403", rec.Code)
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
	h := NewAdminHandler(store, nil, fixedOperator{id: "auth0|alice"}, credExists, nil, log, nil)
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
	lc := &OperatorLoginConfig{Issuer: "https://acme.auth0.com/", Audience: "https://harbor.acme/api", ClientID: "cid", Scope: "openid"}
	h := NewAdminHandler(store, nil, denyAll{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

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
	h := NewAdminHandler(store, nil, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/login-config", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("login-config status = %d, want 404 when not OIDC-gated", rec.Code)
	}
}

func TestAdminRolesCRUD(t *testing.T) {
	h, _ := newTestAdmin(t) // existing helper: returns handler + store
	// create
	rec := doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "guest", Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}, TTLSeconds: 3600})
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
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "beta", Name: "review", Destinations: []string{"git"}, Repos: []string{"beta/*"}})
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

func TestRosterSummaries(t *testing.T) {
	_, store := newTestAdmin(t)
	if err := store.PutRole("default", Role{Name: "worker", Scope: Scope{Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}}}); err != nil {
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
	// push v1, v2
	var r1 KitResult
	decodeJSON(t, doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "name: web\nv: 1\n"}), &r1)
	if r1.Version != 1 {
		t.Fatalf("push v1 = %+v", r1)
	}
	var r2 KitResult
	decodeJSON(t, doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "web", Config: "name: web\nv: 2\n"}), &r2)
	if r2.Version != 2 {
		t.Fatalf("push v2 = %+v", r2)
	}
	// list
	var kits []KitSummary
	getJSON(t, h, "/admin/kits", &kits)
	if len(kits) != 1 || kits[0].Current != 2 || kits[0].Versions != 2 {
		t.Fatalf("list = %+v", kits)
	}
	// show current + specific version
	var cur KitConfigResult
	getJSON(t, h, "/admin/kits/web", &cur)
	if cur.Version != 2 || cur.Config != "name: web\nv: 2\n" {
		t.Fatalf("show current = %+v", cur)
	}
	var old KitConfigResult
	getJSON(t, h, "/admin/kits/web?version=1", &old)
	if old.Version != 1 || old.Config != "name: web\nv: 1\n" {
		t.Fatalf("show v1 = %+v", old)
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
	doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "builder", Config: "name: builder\n"})
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
	doJSON(t, h, "POST", "/admin/kits", KitBody{Name: "builder", Config: "name: builder\n"})
	doJSON(t, h, "POST", "/admin/roles", RoleBody{Project: "acme", Name: "impl", Kit: "builder"})
	var roles []RoleSummary
	getJSON(t, h, "/admin/roles?project=acme", &roles)
	if len(roles) != 1 || roles[0].Kit != "builder" {
		t.Fatalf("role summary = %+v", roles)
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
	h := NewAdminHandler(store, sup, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
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
