package harbor

import (
	"bytes"
	"encoding/json"
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
	h := NewAdminHandler(store, LoopbackAuthenticator{}, credExists, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return h, store
}

// loopback requests carry a loopback RemoteAddr; httptest.NewRequest defaults to
// 192.0.2.1, so set it explicitly.
func adminReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:5000"
	return r
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
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("POST", "/admin/enrollments", `{"id":"spider-18","project":"ACME","role":"guest","destinations":["git"],"repos":["acme/*"]}`))
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
	// GET must not leak tokens or hashes.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, adminReq("GET", "/admin/enrollments", ""))
	if bytes.Contains(rec.Body.Bytes(), []byte(res.Token)) || bytes.Contains(rec.Body.Bytes(), []byte(HashToken(res.Token))) {
		t.Fatal("enrollment list leaked token or hash")
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
	h := NewAdminHandler(store, fixedOperator{id: "auth0|alice"}, credExists, log)

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
