package adminui_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

// apiDo sends method path with body as JSON (nil: none) to h as a Local
// admin, same-origin, and decodes a JSON reply into out (nil: skip).
func apiDo(t *testing.T, h http.Handler, method, path string, body, out any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Origin", "http://example.com") // httptest's Host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	if out != nil && rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, rec.Body)
		}
	}
	return rec
}

// wantErr asserts rec is a JSON API error with status and code.
func wantErr(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var e adminui.APIError
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &e) != nil || e.Code != code {
		t.Errorf("got %d %s, want %d %s", rec.Code, rec.Body, status, code)
	}
}

func TestUsersAPI(t *testing.T) {
	h, store := adminHandler(t)
	if _, err := store.CreateConnection(jam.Connection{Kind: "discord", Name: "disc", CredName: "tok"}); err != nil {
		t.Fatal(err)
	}
	var created adminui.Created
	if rec := apiDo(t, h, "POST", "/app/admin/api/users", map[string]string{"name": "alice", "logins": "a@x\n\nlocal", "oidc": ""}, &created); rec.Code != 201 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if !strings.HasPrefix(created.ID, "usr_") || created.Path != "config/users/"+created.ID {
		t.Errorf("created = %+v", created)
	}
	var list adminui.UsersResponse
	apiDo(t, h, "GET", "/app/admin/api/users", nil, &list)
	if len(list.Users) != 1 || list.Users[0].Name != "alice" || strings.Join(list.Users[0].Logins, ",") != "a@x,local" {
		t.Errorf("list = %+v", list)
	}
	var d adminui.UserDetail
	for _, ref := range []string{"alice", created.ID} {
		if rec := apiDo(t, h, "GET", "/app/admin/api/users/"+ref, nil, &d); rec.Code != 200 || d.User.ID != created.ID {
			t.Errorf("get %s = %d %+v", ref, rec.Code, d)
		}
	}
	if len(d.Connections) != 1 || d.Connections[0].Name != "disc" || d.LoginSpec != "a@x\nlocal" {
		t.Errorf("detail = %+v", d)
	}
	apiDo(t, h, "POST", "/app/admin/api/users/alice/name", map[string]string{"name": "alicia"}, &d)
	if d.User.Name != "alicia" {
		t.Errorf("renamed = %+v", d.User)
	}
	apiDo(t, h, "POST", "/app/admin/api/users/alicia/logins", map[string]string{"logins": "z@x"}, &d)
	apiDo(t, h, "POST", "/app/admin/api/users/alicia/oidc", map[string]string{"oidc": "https://idp.example:sub-1"}, &d)
	if d.LoginSpec != "z@x" || d.OIDCSpec != "https://idp.example:sub-1" {
		t.Errorf("logins/oidc = %q %q", d.LoginSpec, d.OIDCSpec)
	}
	apiDo(t, h, "POST", "/app/admin/api/users/alicia/accounts", map[string]string{"connection": "disc", "uid": "42", "handle": "ali"}, &d)
	if len(d.User.Accounts) != 1 || d.User.Accounts[0].Connection != "disc" || d.User.Accounts[0].ServiceUID != "42" || d.User.Accounts[0].Handle != "ali" {
		t.Fatalf("accounts = %+v", d.User.Accounts)
	}
	apiDo(t, h, "DELETE", "/app/admin/api/users/alicia/accounts/"+d.User.Accounts[0].ID, nil, &d)
	if len(d.User.Accounts) != 0 {
		t.Errorf("after unlink = %+v", d.User.Accounts)
	}
	if rec := apiDo(t, h, "DELETE", "/app/admin/api/users/alicia", nil, nil); rec.Code != 204 {
		t.Errorf("remove = %d %s", rec.Code, rec.Body)
	}
	apiDo(t, h, "GET", "/app/admin/api/users/"+created.ID, nil, &d)
	if !d.Removed {
		t.Errorf("after remove = %+v", d)
	}
}

func TestUsersAPIRefusals(t *testing.T) {
	h, store := adminHandler(t)
	if _, err := store.CreateUser(jam.User{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	carol, _ := store.CreateUser(jam.User{Name: "carol"})
	con, _ := store.CreateConnection(jam.Connection{Kind: "discord", Name: "disc", CredName: "tok"})
	acc, err := jam.UpsertAccountChecked(store, jam.AccountBody{Connection: con.Name, ServiceUID: "9", User: string(carol.ID)})
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/users", map[string]string{"name": "bob"}, nil), 409, "conflict")
	wantErr(t, apiDo(t, h, "GET", "/app/admin/api/users/nobody", nil, nil), 404, "not_found")
	wantErr(t, apiDo(t, h, "POST", "/app/admin/api/users/nobody/name", map[string]string{"name": "x"}, nil), 404, "not_found")
	wantErr(t, apiDo(t, h, "DELETE", "/app/admin/api/users/bob/accounts/"+string(acc.ID), nil, nil), 404, "not_found")
	r := httptest.NewRequest("POST", "/app/admin/api/users", strings.NewReader("name=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, appui.WithPrincipal(r, appui.Principal{Admin: true, Local: true}))
	wantErr(t, rec, 415, "bad_request")
}

func TestUsersAPIManaged(t *testing.T) {
	h, _ := managedUI(t)
	var d adminui.UserDetail
	apiDo(t, h, "GET", "/app/admin/api/users/bob", nil, &d)
	if d.User.Managed == nil || d.User.Managed.Commit != "abc1234" {
		t.Errorf("managed = %+v", d.User.Managed)
	}
	var list adminui.UsersResponse
	apiDo(t, h, "GET", "/app/admin/api/users", nil, &list)
	if len(list.Users) != 1 || list.Users[0].Managed == nil || len(list.Users[0].Projects) != 1 {
		t.Errorf("list = %+v", list.Users)
	}
}
