package adminclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

func TestClientRoleAndGrantRoundTrips(t *testing.T) {
	var gotPath, gotMethod, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.RequestURI()
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		switch {
		case r.URL.Path == "/admin/roles" && r.Method == "GET":
			_, _ = w.Write([]byte(`[{"project":"acme","name":"guest","destinations":["anthropic"],"repos":["acme/*"],"ttl_seconds":3600}]`))
		case r.URL.Path == "/admin/projects":
			_, _ = w.Write([]byte(`["acme"]`))
		case r.URL.Path == "/admin/roster":
			_, _ = w.Write([]byte(`[{"id":"m","grants":[{"project":"acme","role":"guest","destinations":["anthropic"],"repos":["acme/*"]}]}]`))
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	if err := c.PutRole("acme", harbor.Role{Name: "guest", Scope: harbor.Scope{Destinations: []string{"anthropic"}, Repos: []string{"acme/*"}, TTL: time.Hour}}); err != nil {
		t.Fatalf("PutRole: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/admin/roles" || !strings.Contains(gotBody, `"ttl_seconds":3600`) {
		t.Fatalf("PutRole wire = %s %s %s", gotMethod, gotPath, gotBody)
	}
	roles, err := c.ListRoles("acme")
	if err != nil || len(roles) != 1 || roles[0].Scope.TTL != time.Hour {
		t.Fatalf("ListRoles = %+v, %v", roles, err)
	}
	if gotPath != "/admin/roles?project=acme" {
		t.Fatalf("ListRoles path = %s", gotPath)
	}

	projs, err := c.ListProjects()
	if err != nil || len(projs) != 1 || projs[0] != "acme" {
		t.Fatalf("ListProjects = %+v, %v", projs, err)
	}

	if err := c.RemoveRole("acme", "guest"); err != nil {
		t.Fatalf("RemoveRole: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/admin/roles/acme/guest" {
		t.Fatalf("RemoveRole wire = %s %s", gotMethod, gotPath)
	}

	roster, err := c.Roster()
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(roster) != 1 || roster[0].ID != "m" || len(roster[0].Grants) != 1 {
		t.Fatalf("Roster = %+v", roster)
	}

	if err := c.AddGrant("m", harbor.Grant{Project: "beta", Role: "review"}); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/admin/actors/m/grants" {
		t.Fatalf("AddGrant wire = %s %s", gotMethod, gotPath)
	}

	if err := c.RemoveGrant("m", "beta", "review"); err != nil {
		t.Fatalf("RemoveGrant: %v", err)
	}
	if gotMethod != "DELETE" || gotPath != "/admin/actors/m/grants/beta/review" {
		t.Fatalf("RemoveGrant wire = %s %s", gotMethod, gotPath)
	}
}

// TestClientEnrollBodyIsTrimmed proves Enroll's wire body carries only
// id/project/role/overrides — no inline destinations/repos/ttl_seconds, since
// scope now comes entirely from the role.
func TestClientEnrollBodyIsTrimmed(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","token":"tok"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "")

	if _, err := c.Enroll(EnrollParams{ID: "x", Project: "acme", Role: "guest"}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	for _, field := range []string{"destinations", "repos", "ttl_seconds"} {
		if strings.Contains(gotBody, field) {
			t.Fatalf("Enroll body still contains %q: %s", field, gotBody)
		}
	}
}
