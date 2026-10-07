package adminui_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
)

// grantingAlloc is a jam.SessionAllocator that always grants and records grants.
type grantingAlloc struct{ grants []string }

func (a *grantingAlloc) GrantPersonal(_ context.Context, project, role, id, owner string) (bool, error) {
	a.grants = append(a.grants, project+"/"+role+"/"+owner)
	return true, nil
}
func (a *grantingAlloc) RecordRelease(context.Context, string, string, string) error { return nil }

// promptLauncher records the prompt of each raise.
type promptLauncher struct {
	fakeLauncher
	prompts []string
}

func (l *promptLauncher) Raise(ctx context.Context, spec jam.RaiseSpec, c jam.LaunchCreds) (string, error) {
	l.prompts = append(l.prompts, spec.Prompt)
	return l.fakeLauncher.Raise(ctx, spec, c)
}

// requestKit is an acme/pair role, a discord chat service, and roster human
// alice linked to login auth0|alice with a discord delivery profile.
func requestKit(t *testing.T) (jam.Store, *promptLauncher, *grantingAlloc, http.Handler) {
	t.Helper()
	store := newStore(t)
	mustCreateProject(t, store, "acme")
	if err := store.PutRole("acme", jam.Role{Name: "pair"}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(store, "acme", jam.Human{Name: "alice", Handle: "@alice", Login: "auth0|alice",
		Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "111"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetChatService("acme", "discord"); err != nil {
		t.Fatal(err)
	}
	l := &promptLauncher{}
	sup := jam.NewSupervisor(store, l, "test-holder", 60e9, 30e9, nil, testLogger())
	a := &grantingAlloc{}
	return store, l, a, adminui.Handler(store, testLogger(), sup, a, anyCred, nil)
}

// requestAs POSTs the role Request action as operator op (CSRF-valid).
func requestAs(t *testing.T, h http.Handler, op, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://"+req.Host)
	req = jam.WithOperator(req, jam.Operator{ID: op})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRolesPageOffersRequest(t *testing.T) {
	_, _, _, h := requestKit(t)
	body := get(t, h, "/ui/projects/acme/roles").Body.String()
	if !strings.Contains(body, `hx-post="/ui/roles/acme/pair/request"`) {
		t.Errorf("a project's roles should offer a Request action per role; got:\n%s", body)
	}
}

func TestRoleRequestRaisesPersonalSessionForSignedInOperator(t *testing.T) {
	store, l, a, h := requestKit(t)
	rec := requestAs(t, h, "auth0|alice", "/ui/roles/acme/pair/request")
	if rec.Code != http.StatusOK {
		t.Fatalf("request = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	alice, _ := store.LookupName(ident.User, "alice")
	if len(a.grants) != 1 || a.grants[0] != "acme/pair/"+string(alice) {
		t.Errorf("grants = %v, want one acme/pair/<alice's id>", a.grants)
	}
	var inst jam.Instance
	for _, i := range store.ListInstances() {
		inst = i
	}
	if inst.Owner != "alice" || inst.SessionKind != jam.SessionKindPersonal {
		t.Fatalf("instance = %+v, want a personal session owned by alice", inst)
	}
	if len(l.prompts) != 1 || !strings.HasSuffix(l.prompts[0], "Squawk me (user:alice) and we will get to work.") {
		t.Errorf("prompt = %q, want it to end with the squawk-me request", l.prompts)
	}
	if !strings.Contains(rec.Body.String(), inst.ActorID) {
		t.Errorf("response should name the new session %s; got: %s", inst.ActorID, rec.Body.String())
	}
}

func TestRoleRequestAsLoopbackOperatorAsksToSignIn(t *testing.T) {
	store, _, a, h := requestKit(t)
	rec := requestAs(t, h, "local", "/ui/roles/acme/pair/request")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("request = %d, want 403", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/ui/auth/login") {
		t.Errorf("an anonymous loopback operator should be pointed at sign-in; got: %s", rec.Body.String())
	}
	if len(a.grants) != 0 || len(store.ListInstances()) != 0 {
		t.Error("no grant or raise should happen for an unidentified operator")
	}
}

func TestRoleRequestUnlinkedLoginRefused(t *testing.T) {
	_, _, a, h := requestKit(t)
	rec := requestAs(t, h, "auth0|stranger", "/ui/roles/acme/pair/request")
	if rec.Code != http.StatusForbidden || len(a.grants) != 0 {
		t.Fatalf("request = %d grants=%v, want 403 and no grant", rec.Code, a.grants)
	}
}

func TestRoleRequestRejectsCrossOrigin(t *testing.T) {
	_, _, a, h := requestKit(t)
	req := httptest.NewRequest(http.MethodPost, "/ui/roles/acme/pair/request", nil)
	req.Header.Set("Origin", "http://evil.example")
	req = jam.WithOperator(req, jam.Operator{ID: "auth0|alice"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(a.grants) != 0 {
		t.Fatalf("cross-origin request = %d grants=%v, want 403 and no grant", rec.Code, a.grants)
	}
}
