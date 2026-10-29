package adminui_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/adminui"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

func appTokenFixture(t *testing.T) (*bytes.Buffer, *jam.MemStore, jam.User, jam.User, []jam.AppToken) {
	t.Helper()
	store := jam.NewMemStore()
	alice, err := store.CreateUser(jam.User{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := store.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var toks []jam.AppToken
	for i, op := range []string{"", "op-1"} {
		tok, _, err := jam.NewAppToken(alice.ID, "VS Code", op, t0.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutAppToken(tok); err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tok)
	}
	var logs bytes.Buffer
	return &logs, store, alice, bob, toks
}

func TestAPIUserDetailAppTokens(t *testing.T) {
	logs, store, alice, bob, toks := appTokenFixture(t)
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(logs, nil)), nil, nil, anyCred, nil)
	var d adminui.UserDetail
	if rec := apiDo(t, h, "GET", "/app/admin/api/users/"+string(alice.ID), nil, &d); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(d.AppTokens) != 2 || d.AppTokens[0].ID != string(toks[1].ID) || !d.AppTokens[0].Operator || d.AppTokens[1].Operator ||
		d.AppTokens[0].Label != "VS Code" || !d.AppTokens[0].Expires.Equal(toks[1].ExpiresAt) {
		t.Fatalf("app tokens %+v", d.AppTokens)
	}
	if rec := apiDo(t, h, "GET", "/app/admin/api/users/"+string(bob.ID), nil, &d); rec.Code != 200 || d.AppTokens == nil || len(d.AppTokens) != 0 {
		t.Fatalf("bob: %d %+v", rec.Code, d.AppTokens)
	}
}

func TestAPIUserAppTokenRevoke(t *testing.T) {
	logs, store, alice, _, toks := appTokenFixture(t)
	var revoked []ident.ID
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(logs, nil)), nil, nil, anyCred, nil,
		adminui.WithAppTokenRevoke(func(id ident.ID) { revoked = append(revoked, id) }))
	rec := apiDoAs(t, h, appui.Principal{Admin: true, Operator: jam.Operator{ID: "op-9"}}, "DELETE",
		"/app/admin/api/users/"+string(alice.ID)+"/app-tokens/"+string(toks[0].ID), nil, nil)
	if rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if _, ok := store.AppTokenByID(toks[0].ID); ok {
		t.Fatal("token kept")
	}
	if len(revoked) != 1 || revoked[0] != toks[0].ID {
		t.Fatalf("revoked %v", revoked)
	}
	l := logs.String()
	if !strings.Contains(l, "ui app token revoked") || !strings.Contains(l, "op-9") || !strings.Contains(l, string(toks[0].ID)) ||
		!strings.Contains(l, string(alice.ID)) || strings.Contains(l, toks[0].Hash) {
		t.Fatalf("log:\n%s", l)
	}
}

func TestAPIUserAppTokenRevokeWrongUser404(t *testing.T) {
	logs, store, _, bob, toks := appTokenFixture(t)
	revokes := 0
	h := adminui.Handler(store, slog.New(slog.NewTextHandler(logs, nil)), nil, nil, anyCred, nil,
		adminui.WithAppTokenRevoke(func(ident.ID) { revokes++ }))
	for _, path := range []string{
		"/app/admin/api/users/" + string(bob.ID) + "/app-tokens/" + string(toks[0].ID),
		"/app/admin/api/users/" + string(bob.ID) + "/app-tokens/nope",
		"/app/admin/api/users/usr_missing/app-tokens/" + string(toks[0].ID),
	} {
		rec := apiDo(t, h, "DELETE", path, nil, nil)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), "no such app token on this user") {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if _, ok := store.AppTokenByID(toks[0].ID); !ok || revokes != 0 {
		t.Fatalf("kept=%v revokes=%d", ok, revokes)
	}
}
