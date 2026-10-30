package userui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

func putAppToken(t *testing.T, st jam.Store, user ident.ID, label, op string, at time.Time) jam.AppToken {
	t.Helper()
	tok, _, err := jam.NewAppToken(user, label, op, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutAppToken(tok); err != nil {
		t.Fatal(err)
	}
	return tok
}

func apiDelete(t *testing.T, h http.Handler, p jam.Participant, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := appui.WithPrincipal(jam.WithParticipant(httptest.NewRequest("DELETE", path, nil), p), appui.Principal{User: &p})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIAppTokensListsOwnNewestFirst(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	old := putAppToken(t, e.st, p.UserID, "old laptop", "", t0)
	newer := putAppToken(t, e.st, p.UserID, "VS Code on mac", "op-1", t0.Add(time.Hour))
	putAppToken(t, e.st, bob, "bob's", "", t0.Add(2*time.Hour))
	h := Handler(e.Deps, nil)
	rec := apiGet(t, h, &p, "/app/user/api/app-tokens")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var got AppTokensResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tokens) != 2 || got.Tokens[0].ID != string(newer.ID) || got.Tokens[1].ID != string(old.ID) {
		t.Fatalf("tokens %+v", got.Tokens)
	}
	r := got.Tokens[0]
	if r.Label != "VS Code on mac" || !r.Operator || !r.Created.Equal(newer.CreatedAt) || !r.LastUsed.Equal(newer.LastUsedAt) || !r.Expires.Equal(newer.ExpiresAt) {
		t.Fatalf("row %+v", r)
	}
	if got.Tokens[1].Operator {
		t.Fatal("member token marked operator")
	}
	// None: an empty list, not null.
	bp := jam.Participant{UserID: bob, Name: "bob", Projects: []string{"proj"}}
	for _, tk := range e.st.AppTokens(bob) {
		_, _ = e.st.DeleteUserAppToken(bob, tk.ID)
	}
	if rec := apiGet(t, h, &bp, "/app/user/api/app-tokens"); rec.Body.String() != "{\"tokens\":[]}\n" {
		t.Fatalf("empty: %s", rec.Body)
	}
}

func TestAPIAppTokensDeleteOwn(t *testing.T) {
	e, p := fixture()
	tok := putAppToken(t, e.st, p.UserID, "VS Code", "", time.Now())
	var revoked []ident.ID
	h := Handler(e.Deps, nil, WithAppTokenRevoke(func(id ident.ID) { revoked = append(revoked, id) }))
	if rec := apiDelete(t, h, p, "/app/user/api/app-tokens/"+string(tok.ID)); rec.Code != http.StatusNoContent {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, ok := e.st.AppTokenByID(tok.ID); ok {
		t.Fatal("token kept")
	}
	if len(revoked) != 1 || revoked[0] != tok.ID {
		t.Fatalf("revoked %v", revoked)
	}
}

func TestAPIAppTokensDeleteOthers204Kept(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	tok := putAppToken(t, e.st, bob, "VS Code", "", time.Now())
	revokes := 0
	h := Handler(e.Deps, nil, WithAppTokenRevoke(func(ident.ID) { revokes++ }))
	if rec := apiDelete(t, h, p, "/app/user/api/app-tokens/"+string(tok.ID)); rec.Code != http.StatusNoContent {
		t.Fatalf("%d", rec.Code)
	}
	if _, ok := e.st.AppTokenByID(tok.ID); !ok || revokes != 0 {
		t.Fatalf("kept=%v revokes=%d", ok, revokes)
	}
}

func TestAPIAppTokensDeleteMalformedID204(t *testing.T) {
	e, p := fixture()
	revokes := 0
	h := Handler(e.Deps, nil, WithAppTokenRevoke(func(ident.ID) { revokes++ }))
	for _, id := range []string{"nope", "usr_01j9q3ffffffffffffffffffff"} {
		if rec := apiDelete(t, h, p, "/app/user/api/app-tokens/"+id); rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", id, rec.Code)
		}
	}
	if revokes != 0 {
		t.Fatal("revoked")
	}
}
