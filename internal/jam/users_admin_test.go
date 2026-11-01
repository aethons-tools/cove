package jam

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestAdminUsersReadAndOIDC(t *testing.T) {
	h, store := newTestAdmin(t)
	alice, err := store.CreateUser(User{Name: "alice", Logins: []string{"auth0|a"}})
	if err != nil {
		t.Fatal(err)
	}
	// name and id both address the user
	var byName, byID UserView
	getJSON(t, h, "/admin/users/alice", &byName)
	getJSON(t, h, "/admin/users/"+string(alice.ID), &byID)
	if byName.ID != alice.ID || byID.Name != "alice" || !slices.Equal(byName.Logins, []string{"auth0|a"}) {
		t.Fatalf("by name %+v, by id %+v", byName, byID)
	}
	if rec := doReq(t, h, "GET", "/admin/users/nobody", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, h, "PUT", "/admin/users/alice/oidc", OIDCBody{OIDC: []OIDCIdentity{{Issuer: "i", Subject: "s"}}}); rec.Code != http.StatusNoContent {
		t.Fatalf("oidc = %d %s", rec.Code, rec.Body.String())
	}
	if u, _ := store.GetUser(alice.ID); len(u.OIDC) != 1 {
		t.Fatalf("stored = %+v", u)
	}
	var list []UserView
	getJSON(t, h, "/admin/users", &list)
	if len(list) != 1 || list[0].Name != "alice" {
		t.Fatalf("list = %+v", list)
	}
}

func TestUserCheckedWritesRefusedWhileOwningPersonalSession(t *testing.T) {
	_, store := newTestAdmin(t)
	alice, err := store.CreateUser(User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutInstance(Instance{ActorID: "personal-alice-1", Project: DefaultProject, Owner: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveUserChecked(store, alice.ID); !errors.Is(err, ErrUserBusy) {
		t.Fatalf("remove with a live personal session = %v, want ErrUserBusy", err)
	}
	// A session's owner is still a name (until 1a-3c): renaming would orphan it.
	if err := RenameUserChecked(store, alice.ID, "alicia"); !errors.Is(err, ErrUserBusy) {
		t.Fatalf("rename with a live personal session = %v, want ErrUserBusy", err)
	}
	if RegistryErrStatus(ErrUserBusy) != http.StatusConflict {
		t.Fatal("ErrUserBusy should map to 409")
	}
}

func TestAdminAccountLinkToRemovedUserWritesNothing(t *testing.T) {
	h, store := newTestAdmin(t)
	if _, err := store.CreateConnection(Connection{Kind: "discord", Name: "discord"}); err != nil {
		t.Fatal(err)
	}
	u, _ := store.CreateUser(User{Name: "gone"})
	if err := store.RemoveUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, h, "POST", "/admin/accounts", AccountBody{Connection: "discord", ServiceUID: "9", User: string(u.ID)}); rec.Code != http.StatusConflict {
		t.Fatalf("link to a removed user = %d, want 409", rec.Code)
	}
	var accs []AccountView
	getJSON(t, h, "/admin/accounts", &accs)
	if len(accs) != 0 {
		t.Fatalf("a refused POST left accounts behind: %+v", accs)
	}
}

func TestAdminProjectMembersList(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	alice, _ := store.CreateUser(User{Name: "alice"})
	acme, _ := store.GetProject("acme")
	if err := store.PutMembership(Membership{ProjectID: acme.ID, UserID: alice.ID, Delivery: []DeliveryProfile{{Service: "discord", Address: "inbox-a"}}}); err != nil {
		t.Fatal(err)
	}
	var members []MemberView
	getJSON(t, h, "/admin/projects/acme/members", &members)
	if len(members) != 1 || members[0].User != "alice" || len(members[0].Delivery) != 1 {
		t.Fatalf("members = %+v", members)
	}
	// The surfaces see the member.
	if ms := MembersOf(store, acme.ID); len(ms) != 1 || ms[0].User.Name != "alice" {
		t.Fatalf("members = %+v", ms)
	}
	if rec := doReq(t, h, "GET", "/admin/projects/ghost/members", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404", rec.Code)
	}
	if err := store.PutMembership(Membership{ProjectID: acme.ID, UserID: alice.ID, Delivery: []DeliveryProfile{{Service: "discord"}}}); err == nil {
		t.Fatal("delivery without an address was accepted")
	}
}

func TestAdminAccounts(t *testing.T) {
	h, store := newTestAdmin(t)
	if _, err := store.CreateConnection(Connection{Kind: "discord", Name: "discord"}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alice", "bob"} {
		if _, err := store.CreateUser(User{Name: n}); err != nil {
			t.Fatal(err)
		}
	}

	var conns []Connection
	getJSON(t, h, "/admin/connections", &conns)
	if len(conns) != 1 || conns[0].Name != "discord" {
		t.Fatalf("connections = %+v", conns)
	}

	rec := doJSON(t, h, "POST", "/admin/accounts", AccountBody{Connection: "discord", ServiceUID: "111", User: "alice"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST account = %d %s", rec.Code, rec.Body.String())
	}
	var acc AccountView
	decodeBody(t, rec, &acc)
	if acc.ID.Kind() != ident.Account || acc.User != "alice" || acc.Connection != "discord" {
		t.Fatalf("account = %+v", acc)
	}
	var list []AccountView
	getJSON(t, h, "/admin/accounts?connection=discord", &list)
	if len(list) != 1 || list[0].ServiceUID != "111" {
		t.Fatalf("accounts = %+v", list)
	}
	// The user view lists the account.
	var alice UserView
	getJSON(t, h, "/admin/users/alice", &alice)
	if len(alice.Accounts) != 1 || alice.Accounts[0].ID != acc.ID {
		t.Fatalf("alice accounts = %+v", alice.Accounts)
	}
	if rec := doJSON(t, h, "PUT", "/admin/accounts/"+string(acc.ID)+"/user", LinkBody{User: "bob"}); rec.Code != http.StatusNoContent {
		t.Fatalf("relink = %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := store.GetAccount(acc.ID); a.UserID == "" {
		t.Fatal("relink did not link")
	}
	if rec := doReq(t, h, "DELETE", "/admin/accounts/"+string(acc.ID)+"/user", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("unlink = %d %s", rec.Code, rec.Body.String())
	}
	if a, _ := store.GetAccount(acc.ID); a.UserID != "" {
		t.Fatal("unlink did not unlink")
	}
	if rec := doJSON(t, h, "POST", "/admin/accounts", AccountBody{Connection: "nope", Handle: "x"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown connection = %d, want 404", rec.Code)
	}
	if rec := doReq(t, h, "DELETE", "/admin/accounts/"+string(ident.New(ident.Account))+"/user", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown account = %d, want 404", rec.Code)
	}
}

func TestAdminActorsReplacesRoster(t *testing.T) {
	h, _ := newTestAdmin(t)
	var actors []ActorSummary
	getJSON(t, h, "/admin/actors", &actors)
	for _, gone := range []struct{ method, path string }{
		{"GET", "/admin/roster"},
		{"POST", "/admin/projects/acme/humans"},
		{"DELETE", "/admin/projects/acme/humans/alice"},
	} {
		if rec := doReq(t, h, gone.method, gone.path, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want gone", gone.method, gone.path, rec.Code)
		}
	}
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
}

func TestAdminConnectionsListAndInUse(t *testing.T) {
	h, store := newTestAdmin(t)
	c, err := store.CreateConnection(Connection{Kind: "discord", Name: "discord-main", CredName: "bot-tok"})
	if err != nil {
		t.Fatal(err)
	}
	var conns []Connection
	getJSON(t, h, "/admin/connections", &conns)
	if len(conns) != 1 || conns[0].ID != c.ID || conns[0].CredName != "bot-tok" {
		t.Fatalf("connections = %+v", conns)
	}
	for name, bad := range map[string]Connection{
		"duplicate":    {Kind: "discord", Name: "discord-main"},
		"unknown kind": {Kind: "slack", Name: "s"},
		"bad name":     {Kind: "linear", Name: "a b"},
	} {
		if _, err := store.CreateConnection(bad); err == nil {
			t.Errorf("%s: CreateConnection accepted it", name)
		}
	}
	mustCreateProject(t, store, "acme")
	if err := store.SetChatService("acme", "discord-main"); err != nil {
		t.Fatalf("chat service by connection name = %v", err)
	}
	var cs ChatServiceView
	getJSON(t, h, "/admin/projects/acme/chat-service", &cs)
	if cs.Service != "discord-main" {
		t.Fatalf("chat service view = %+v", cs)
	}
	if err := store.RemoveConnection(c.ID); !errors.Is(err, ErrConnectionInUse) {
		t.Fatalf("remove a chat service's connection = %v, want ErrConnectionInUse", err)
	}
	if err := store.SetChatService("acme", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveConnection(c.ID); err != nil {
		t.Fatalf("remove = %v", err)
	}
}

func TestUserConfigRoutesGone(t *testing.T) {
	h, store := newTestAdmin(t)
	if _, err := store.CreateUser(User{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateConnection(Connection{Kind: "discord", Name: "discord"}); err != nil {
		t.Fatal(err)
	}
	mustCreateProject(t, store, "acme")
	for _, gone := range []struct{ method, path string }{
		{"POST", "/admin/users"},
		{"PUT", "/admin/users/alice/name"},
		{"PUT", "/admin/users/alice/logins"},
		{"DELETE", "/admin/users/alice"},
		{"PUT", "/admin/projects/acme/members/alice"},
		{"DELETE", "/admin/projects/acme/members/alice"},
		{"POST", "/admin/connections"},
		{"PUT", "/admin/connections/discord/name"},
		{"PUT", "/admin/connections/discord/cred"},
		{"DELETE", "/admin/connections/discord"},
	} {
		if rec := doJSON(t, h, gone.method, gone.path, map[string]string{}); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 404 or 405", gone.method, gone.path, rec.Code)
		}
	}
	// The kept neighbours still answer (fresh names: a live route above may have removed the first set).
	if _, err := store.CreateUser(User{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateConnection(Connection{Kind: "discord", Name: "discord-2"}); err != nil {
		t.Fatal(err)
	}
	if rec := doJSON(t, h, "PUT", "/admin/users/bob/oidc", OIDCBody{OIDC: []OIDCIdentity{{Issuer: "i", Subject: "s"}}}); rec.Code != http.StatusNoContent {
		t.Errorf("PUT oidc = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, "POST", "/admin/accounts", AccountBody{Connection: "discord-2", ServiceUID: "1", User: "bob"}); rec.Code != http.StatusCreated {
		t.Errorf("POST account = %d %s, want 201", rec.Code, rec.Body.String())
	}
}
