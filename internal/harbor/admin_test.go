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
)

func newTestAdmin(t *testing.T) (http.Handler, Store) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	credExists := func(n string) bool { return n == "git-pat" || n == "anthropic-key" }
	h := NewAdminHandler(store, LoopbackAuthenticator{}, credExists, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	h := NewAdminHandler(store, fixedOperator{id: "auth0|alice"}, credExists, nil, log)
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
	h := NewAdminHandler(store, denyAll{}, func(string) bool { return true }, lc, slog.New(slog.NewTextHandler(io.Discard, nil)))

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
	h := NewAdminHandler(store, LoopbackAuthenticator{}, func(string) bool { return true }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
