package storetest

import (
	"errors"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runRegistryConformance exercises the RegistryStore contract.
func runRegistryConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	mustUser := func(t *testing.T, s jam.Store, name string) jam.User {
		t.Helper()
		u, err := s.CreateUser(jam.User{Name: name})
		if err != nil {
			t.Fatalf("CreateUser %s: %v", name, err)
		}
		return u
	}

	t.Run("user_create_mints_id_and_resolves", func(t *testing.T) {
		s := newStore(t)
		u := mustUser(t, s, "alice")
		if u.ID.Kind() != ident.User || u.Status != jam.StatusLive {
			t.Fatalf("created = %+v", u)
		}
		if got, ok := s.GetUser(u.ID); !ok || got.Name != "alice" {
			t.Fatalf("GetUser = %+v, %v", got, ok)
		}
		if id, ok := s.LookupName(ident.User, "alice"); !ok || id != u.ID {
			t.Fatalf("LookupName = %q, %v", id, ok)
		}
		e, ok := s.Resolve(u.ID)
		if !ok || e.Kind != ident.User || e.Label() != "alice" {
			t.Fatalf("Resolve = %+v, %v", e, ok)
		}
		if _, err := s.CreateUser(jam.User{Name: "alice"}); !errors.Is(err, jam.ErrNameTaken) {
			t.Fatalf("duplicate live name: %v, want ErrNameTaken", err)
		}
		if _, err := s.CreateUser(jam.User{Name: "a b"}); !errors.Is(err, jam.ErrInvalidName) {
			t.Fatalf("bad name: %v, want ErrInvalidName", err)
		}
	})

	t.Run("user_create_with_given_id", func(t *testing.T) {
		s := newStore(t)
		id := ident.New(ident.User)
		u, err := s.CreateUser(jam.User{ID: id, Name: "alice"})
		if err != nil || u.ID != id {
			t.Fatalf("CreateUser with id = %+v, %v", u, err)
		}
		if _, err := s.CreateUser(jam.User{ID: id, Name: "bob"}); err == nil {
			t.Fatal("re-using an id must fail")
		}
		if _, err := s.CreateUser(jam.User{ID: ident.New(ident.Project), Name: "carol"}); err == nil {
			t.Fatal("a user id of the wrong kind must fail")
		}
	})

	t.Run("user_rename", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		mustUser(t, s, "bob")
		if err := s.RenameUser(a.ID, "bob"); !errors.Is(err, jam.ErrNameTaken) {
			t.Fatalf("rename onto a live name: %v, want ErrNameTaken", err)
		}
		if err := s.RenameUser(a.ID, "alice"); err != nil {
			t.Fatalf("rename to own name must be a no-op: %v", err)
		}
		if err := s.RenameUser(a.ID, "alicia"); err != nil {
			t.Fatalf("RenameUser: %v", err)
		}
		if _, ok := s.LookupName(ident.User, "alice"); ok {
			t.Fatal("old name still resolves")
		}
		if id, _ := s.LookupName(ident.User, "alicia"); id != a.ID {
			t.Fatalf("new name resolves to %q, want %q", id, a.ID)
		}
		if err := s.RenameUser(ident.New(ident.User), "x"); !errors.Is(err, jam.ErrUserNotFound) {
			t.Fatalf("rename unknown: %v, want ErrUserNotFound", err)
		}
	})

	t.Run("user_logins_and_oidc_unique", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		b := mustUser(t, s, "bob")
		if err := s.SetUserLogins(a.ID, []string{"auth0|a", "local"}); err != nil {
			t.Fatalf("SetUserLogins: %v", err)
		}
		if err := s.SetUserLogins(b.ID, []string{"local"}); !errors.Is(err, jam.ErrLoginTaken) {
			t.Fatalf("shared login: %v, want ErrLoginTaken", err)
		}
		if u, ok := s.UserByLogin("local"); !ok || u.ID != a.ID {
			t.Fatalf("UserByLogin = %+v, %v", u, ok)
		}
		oidc := []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "s1"}}
		if err := s.SetUserOIDC(a.ID, oidc); err != nil {
			t.Fatalf("SetUserOIDC: %v", err)
		}
		if err := s.SetUserOIDC(b.ID, oidc); !errors.Is(err, jam.ErrIdentityTaken) {
			t.Fatalf("shared oidc: %v, want ErrIdentityTaken", err)
		}
		if err := s.SetUserOIDC(b.ID, []jam.OIDCIdentity{{Issuer: "", Subject: "x"}}); err == nil {
			t.Fatal("an empty issuer must be rejected")
		}
		if u, ok := s.UserByOIDC("https://idp", "s1"); !ok || u.ID != a.ID {
			t.Fatalf("UserByOIDC = %+v, %v", u, ok)
		}
		// Replacing the set releases what was dropped.
		if err := s.SetUserLogins(a.ID, []string{"auth0|a"}); err != nil {
			t.Fatalf("SetUserLogins shrink: %v", err)
		}
		if err := s.SetUserLogins(b.ID, []string{"local"}); err != nil {
			t.Fatalf("a released login must be bindable: %v", err)
		}
	})

	t.Run("user_remove_tombstones_and_frees_name", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatalf("RemoveUser: %v", err)
		}
		if err := s.RemoveUser(a.ID); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("second remove: %v, want ErrRemoved", err)
		}
		if err := s.RenameUser(a.ID, "x"); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("rename removed: %v, want ErrRemoved", err)
		}
		e, ok := s.Resolve(a.ID)
		if !ok || e.Status != jam.StatusRemoved || e.Label() != "alice (removed)" {
			t.Fatalf("Resolve removed = %+v, %v", e, ok)
		}
		if _, ok := s.LookupName(ident.User, "alice"); ok {
			t.Fatal("a removed user must not be found by name")
		}
		if len(s.ListUsers()) != 0 {
			t.Fatalf("ListUsers = %+v, want none live", s.ListUsers())
		}
		a2 := mustUser(t, s, "alice")
		if a2.ID == a.ID {
			t.Fatal("re-using a removed name must mint a new id")
		}
	})

	t.Run("user_remove_frees_logins_and_oidc", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		oidc := []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "s1"}}
		if err := s.SetUserLogins(a.ID, []string{"local"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetUserOIDC(a.ID, oidc); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.UserByLogin("local"); ok {
			t.Fatal("a removed user's login must not resolve")
		}
		b := mustUser(t, s, "bob")
		if err := s.SetUserLogins(b.ID, []string{"local"}); err != nil {
			t.Fatalf("login freed by removal: %v", err)
		}
		if err := s.SetUserOIDC(b.ID, oidc); err != nil {
			t.Fatalf("oidc freed by removal: %v", err)
		}
	})

	t.Run("user_returns_copies", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		if err := s.SetUserLogins(a.ID, []string{"local"}); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetUser(a.ID)
		got.Logins[0] = "mutated"
		if again, _ := s.GetUser(a.ID); again.Logins[0] != "local" {
			t.Fatalf("store mutated through a returned value: %+v", again)
		}
		list := s.ListUsers()
		list[0].Logins[0] = "mutated"
		if u, _ := s.UserByLogin("local"); u.ID != a.ID {
			t.Fatal("store mutated through ListUsers")
		}
	})

	t.Run("users_list_sorted_live", func(t *testing.T) {
		s := newStore(t)
		mustUser(t, s, "carol")
		b := mustUser(t, s, "bob")
		mustUser(t, s, "alice")
		if err := s.RemoveUser(b.ID); err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, u := range s.ListUsers() {
			names = append(names, u.Name)
		}
		if len(names) != 2 || names[0] != "alice" || names[1] != "carol" {
			t.Fatalf("ListUsers names = %v", names)
		}
	})
	t.Run("user_duplicates_in_one_call_rejected", func(t *testing.T) {
		s := newStore(t)
		a := mustUser(t, s, "alice")
		if err := s.SetUserLogins(a.ID, []string{"local", "local"}); err == nil {
			t.Fatal("a login listed twice must be rejected")
		}
		oidc := jam.OIDCIdentity{Issuer: "https://idp", Subject: "s1"}
		if err := s.SetUserOIDC(a.ID, []jam.OIDCIdentity{oidc, oidc}); err == nil {
			t.Fatal("an OIDC binding listed twice must be rejected")
		}
		if _, err := s.CreateUser(jam.User{Name: "bob", Logins: []string{"x", "x"}}); err == nil {
			t.Fatal("CreateUser with a login listed twice must be rejected")
		}
		if got, _ := s.GetUser(a.ID); len(got.Logins) != 0 || len(got.OIDC) != 0 {
			t.Fatalf("a rejected call must store nothing: %+v", got)
		}
	})
}
