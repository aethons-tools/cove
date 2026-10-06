package jam

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
)

func TestAdminUsersLifecycle(t *testing.T) {
	h, store := newTestAdmin(t)
	rec := doJSON(t, h, "POST", "/admin/users", UserBody{Name: "alice", Logins: []string{"auth0|a"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST user = %d %s", rec.Code, rec.Body.String())
	}
	var alice UserView
	decodeBody(t, rec, &alice)
	if alice.ID.Kind() != ident.User || alice.Name != "alice" || !slices.Equal(alice.Logins, []string{"auth0|a"}) {
		t.Fatalf("created = %+v", alice)
	}
	if rec := doJSON(t, h, "POST", "/admin/users", UserBody{Name: "alice"}); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate name = %d, want 409", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/admin/users", UserBody{Name: "bob", Logins: []string{"auth0|a"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("taken login = %d, want 400", rec.Code)
	}
	if rec := doJSON(t, h, "POST", "/admin/users", UserBody{Name: "a b"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad name = %d, want 400", rec.Code)
	}

	// name and id both address the user
	var byName, byID UserView
	getJSON(t, h, "/admin/users/alice", &byName)
	getJSON(t, h, "/admin/users/"+string(alice.ID), &byID)
	if byName.ID != alice.ID || byID.Name != "alice" {
		t.Fatalf("by name %+v, by id %+v", byName, byID)
	}
	if rec := doReq(t, h, "GET", "/admin/users/nobody", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d, want 404", rec.Code)
	}

	if rec := doJSON(t, h, "PUT", "/admin/users/alice/name", RenameBody{Name: "alicia"}); rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, "PUT", "/admin/users/alicia/logins", LoginsBody{Logins: []string{"auth0|a", "local"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("logins = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, "PUT", "/admin/users/alicia/oidc", OIDCBody{OIDC: []OIDCIdentity{{Issuer: "i", Subject: "s"}}}); rec.Code != http.StatusNoContent {
		t.Fatalf("oidc = %d %s", rec.Code, rec.Body.String())
	}
	if u, _ := store.GetUser(alice.ID); u.Name != "alicia" || len(u.Logins) != 2 || len(u.OIDC) != 1 {
		t.Fatalf("stored = %+v", u)
	}
	var list []UserView
	getJSON(t, h, "/admin/users", &list)
	if len(list) != 1 || list[0].Name != "alicia" {
		t.Fatalf("list = %+v", list)
	}

	if rec := doReq(t, h, "DELETE", "/admin/users/alicia", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	getJSON(t, h, "/admin/users", &list)
	if len(list) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
	var removed UserView
	getJSON(t, h, "/admin/users/"+string(alice.ID), &removed)
	if removed.Status != StatusRemoved {
		t.Fatalf("removed user by id = %+v", removed)
	}
	if rec := doJSON(t, h, "PUT", "/admin/users/"+string(alice.ID)+"/name", RenameBody{Name: "x"}); rec.Code != http.StatusConflict {
		t.Fatalf("rename removed = %d, want 409", rec.Code)
	}
}

func TestAdminUserRemoveRefusedWhileOwningPersonalSession(t *testing.T) {
	h, store := newTestAdmin(t)
	if rec := doJSON(t, h, "POST", "/admin/users", UserBody{Name: "alice"}); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if err := store.PutInstance(Instance{ActorID: "personal-alice-1", Project: DefaultProject, Owner: "alice"}); err != nil {
		t.Fatal(err)
	}
	if rec := doReq(t, h, "DELETE", "/admin/users/alice", nil); rec.Code != http.StatusConflict {
		t.Fatalf("delete with a live personal session = %d, want 409", rec.Code)
	}
	// A session's owner is still a name (until 1a-3c): renaming would orphan it.
	if rec := doJSON(t, h, "PUT", "/admin/users/alice/name", RenameBody{Name: "alicia"}); rec.Code != http.StatusConflict {
		t.Fatalf("rename with a live personal session = %d, want 409", rec.Code)
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

func TestAdminProjectMembers(t *testing.T) {
	h, store := newTestAdmin(t)
	mustCreateProject(t, store, "acme")
	doJSON(t, h, "POST", "/admin/users", UserBody{Name: "alice"})
	body := MemberBody{Delivery: []DeliveryProfile{{Service: "discord", Address: "inbox-a"}}}
	if rec := doJSON(t, h, "PUT", "/admin/projects/acme/members/alice", body); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT member = %d %s", rec.Code, rec.Body.String())
	}
	var members []MemberView
	getJSON(t, h, "/admin/projects/acme/members", &members)
	if len(members) != 1 || members[0].User != "alice" || len(members[0].Delivery) != 1 {
		t.Fatalf("members = %+v", members)
	}
	// The roster view sees the member.
	if r, _ := store.GetRoster("acme"); len(r.Humans) != 1 || r.Humans[0].Name != "alice" {
		t.Fatalf("roster = %+v", r.Humans)
	}
	if rec := doJSON(t, h, "PUT", "/admin/projects/ghost/members/alice", MemberBody{}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, h, "PUT", "/admin/projects/acme/members/nobody", MemberBody{}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user = %d, want 404", rec.Code)
	}
	if rec := doJSON(t, h, "PUT", "/admin/projects/acme/members/alice", MemberBody{Delivery: []DeliveryProfile{{Service: "discord"}}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("delivery without an address = %d, want 400", rec.Code)
	}
	if rec := doReq(t, h, "DELETE", "/admin/projects/acme/members/alice", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE member = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, h, "DELETE", "/admin/projects/acme/members/alice", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE non-member = %d, want 404", rec.Code)
	}
}

func TestAdminAccounts(t *testing.T) {
	h, store := newTestAdmin(t)
	if _, err := store.CreateConnection(Connection{Kind: "discord", Name: "discord"}); err != nil {
		t.Fatal(err)
	}
	doJSON(t, h, "POST", "/admin/users", UserBody{Name: "alice"})
	doJSON(t, h, "POST", "/admin/users", UserBody{Name: "bob"})

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

func TestAdminConnectionsLifecycle(t *testing.T) {
	h, store := newTestAdmin(t)
	rec := doJSON(t, h, "POST", "/admin/connections", ConnectionBody{Kind: "discord", Name: "discord-main", Cred: "bot-tok"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST connection = %d %s", rec.Code, rec.Body.String())
	}
	var c Connection
	decodeBody(t, rec, &c)
	if c.ID.Kind() != ident.Connection || c.CredName != "bot-tok" {
		t.Fatalf("created = %+v", c)
	}
	for name, b := range map[string]ConnectionBody{
		"duplicate":    {Kind: "discord", Name: "discord-main"},
		"unknown kind": {Kind: "slack", Name: "s"},
		"bad name":     {Kind: "linear", Name: "a b"},
	} {
		if rec := doJSON(t, h, "POST", "/admin/connections", b); rec.Code < 400 {
			t.Errorf("%s = %d, want a refusal", name, rec.Code)
		}
	}
	if rec := doJSON(t, h, "PUT", "/admin/connections/discord-main/name", RenameBody{Name: "discord-acme"}); rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doJSON(t, h, "PUT", "/admin/connections/discord-acme/cred", CredBody{Cred: "bot-tok-2"}); rec.Code != http.StatusNoContent {
		t.Fatalf("set cred = %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetConnection(c.ID); got.Name != "discord-acme" || got.CredName != "bot-tok-2" {
		t.Fatalf("stored = %+v", got)
	}
	mustCreateProject(t, store, "acme")
	if rec := doJSON(t, h, "PUT", "/admin/projects/acme/chat-service", ChatServiceBody{Service: "discord-acme"}); rec.Code != http.StatusNoContent {
		t.Fatalf("chat service by connection name = %d %s", rec.Code, rec.Body.String())
	}
	var cs ChatServiceView
	getJSON(t, h, "/admin/projects/acme/chat-service", &cs)
	if cs.Service != "discord-acme" {
		t.Fatalf("chat service view = %+v", cs)
	}
	if rec := doReq(t, h, "DELETE", "/admin/connections/discord-acme", nil); rec.Code != http.StatusConflict {
		t.Fatalf("remove a chat service's connection = %d, want 409", rec.Code)
	}
	doJSON(t, h, "PUT", "/admin/projects/acme/chat-service", ChatServiceBody{Service: ""})
	if rec := doReq(t, h, "DELETE", "/admin/connections/"+string(c.ID), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	if rec := doReq(t, h, "DELETE", "/admin/connections/discord-acme", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("remove again by name = %d, want 404", rec.Code)
	}
}
