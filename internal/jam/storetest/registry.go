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

	mustConn := func(t *testing.T, s jam.Store, kind, name string) jam.Connection {
		t.Helper()
		c, err := s.CreateConnection(jam.Connection{Kind: kind, Name: name, CredName: name + "-cred"})
		if err != nil {
			t.Fatalf("CreateConnection %s: %v", name, err)
		}
		return c
	}

	t.Run("connection_lifecycle", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "linear", "linear-acme")
		if c.ID.Kind() != ident.Connection || c.Status != jam.StatusLive {
			t.Fatalf("created = %+v", c)
		}
		if _, err := s.CreateConnection(jam.Connection{Kind: "linear", Name: "linear-acme"}); !errors.Is(err, jam.ErrNameTaken) {
			t.Fatalf("duplicate: %v, want ErrNameTaken", err)
		}
		if _, err := s.CreateConnection(jam.Connection{Kind: "slack", Name: "s"}); err == nil {
			t.Fatal("an unknown kind must be rejected")
		}
		if err := s.RenameConnection(c.ID, "linear-main"); err != nil {
			t.Fatalf("RenameConnection: %v", err)
		}
		if id, _ := s.LookupName(ident.Connection, "linear-main"); id != c.ID {
			t.Fatalf("renamed lookup = %q", id)
		}
		if err := s.RemoveConnection(c.ID); err != nil {
			t.Fatalf("RemoveConnection: %v", err)
		}
		if e, _ := s.Resolve(c.ID); e.Label() != "linear-main (removed)" {
			t.Fatalf("Resolve removed = %+v", e)
		}
		if len(s.ListConnections()) != 0 {
			t.Fatalf("ListConnections = %+v", s.ListConnections())
		}
	})

	t.Run("connection_remove_refused_while_accounts", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "discord", "discord-main")
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "123"}); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveConnection(c.ID); !errors.Is(err, jam.ErrConnectionInUse) {
			t.Fatalf("remove with accounts: %v, want ErrConnectionInUse", err)
		}
	})

	t.Run("account_upsert_finds_and_learns", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "linear", "linear-acme")
		a, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, Handle: "alice.h"})
		if err != nil || a.ID.Kind() != ident.Account {
			t.Fatalf("create by handle = %+v, %v", a, err)
		}
		// Ingress later learns the uid for the same handle: same account.
		b, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-1", Handle: "alice.h", Label: "Alice H"})
		if err != nil || b.ID != a.ID || b.ServiceUID != "u-1" || b.Label != "Alice H" {
			t.Fatalf("learn uid = %+v, %v", b, err)
		}
		if got, ok := s.AccountByUID(c.ID, "u-1"); !ok || got.ID != a.ID {
			t.Fatalf("AccountByUID = %+v, %v", got, ok)
		}
		// The handle changed on the service: found by uid, handle updated.
		d, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-1", Handle: "alice.new"})
		if err != nil || d.ID != a.ID || d.Handle != "alice.new" {
			t.Fatalf("handle change = %+v, %v", d, err)
		}
		if _, ok := s.AccountByHandle(c.ID, "alice.h"); ok {
			t.Fatal("the old handle must no longer resolve")
		}
		if e, _ := s.Resolve(a.ID); e.Label() != "Alice H" {
			t.Fatalf("account label = %q", e.Label())
		}
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID}); err == nil {
			t.Fatal("an account needs a uid or a handle")
		}
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: ident.New(ident.Connection), Handle: "x"}); !errors.Is(err, jam.ErrConnectionNotFound) {
			t.Fatalf("unknown connection: %v, want ErrConnectionNotFound", err)
		}
	})

	t.Run("account_uid_wins_over_stale_handle", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "linear", "linear-acme")
		stale, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, Handle: "alice.h"})
		if err != nil {
			t.Fatal(err)
		}
		bob, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-2", Handle: "bob.h"})
		if err != nil {
			t.Fatal(err)
		}
		// uid u-2 now carries handle alice.h: the uid's account takes the handle
		// and the stale holder gives it up. The two are never merged.
		got, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-2", Handle: "alice.h"})
		if err != nil || got.ID != bob.ID || got.Handle != "alice.h" {
			t.Fatalf("uid claims handle = %+v, %v", got, err)
		}
		if a, _ := s.AccountByHandle(c.ID, "alice.h"); a.ID != bob.ID {
			t.Fatalf("handle resolves to %q, want %q", a.ID, bob.ID)
		}
		if a, _ := s.GetAccount(stale.ID); a.Handle != "" || a.Status != jam.StatusLive {
			t.Fatalf("stale holder = %+v, want live with no handle", a)
		}
		if e, _ := s.Resolve(stale.ID); e.Label() != "alice.h" {
			t.Fatalf("stale holder must stay renderable, label = %q", e.Label())
		}

		// A new uid takes a handle still held by an account with another uid
		// (someone renamed away unseen, then a new person took the handle).
		carol, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-1", Handle: "carol.h"})
		if err != nil {
			t.Fatal(err)
		}
		dave, err := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u-3", Handle: "carol.h"})
		if err != nil || dave.ID == carol.ID || dave.ServiceUID != "u-3" || dave.Handle != "carol.h" {
			t.Fatalf("new uid on a held handle = %+v, %v", dave, err)
		}
		if a, _ := s.AccountByUID(c.ID, "u-1"); a.ID != carol.ID || a.Handle != "" {
			t.Fatalf("displaced account = %+v, want carol with no handle", a)
		}
		if n := len(s.ListAccounts(c.ID)); n != 4 {
			t.Fatalf("ListAccounts = %d, want 4 (nothing merged)", n)
		}
	})

	t.Run("account_link_and_unlink_on_user_removal", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "discord", "discord-main")
		u := mustUser(t, s, "alice")
		a, _ := s.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "123"})
		if err := s.LinkAccount(a.ID, u.ID); err != nil {
			t.Fatalf("LinkAccount: %v", err)
		}
		if got, _ := s.GetAccount(a.ID); got.UserID != u.ID {
			t.Fatalf("linked = %+v", got)
		}
		if err := s.LinkAccount(a.ID, ident.New(ident.User)); !errors.Is(err, jam.ErrUserNotFound) {
			t.Fatalf("link to unknown user: %v, want ErrUserNotFound", err)
		}
		if err := s.RemoveUser(u.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetAccount(a.ID); got.UserID != "" {
			t.Fatalf("removing the user must unlink: %+v", got)
		}
		if err := s.LinkAccount(a.ID, u.ID); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("link to removed user: %v, want ErrRemoved", err)
		}
		if err := s.LinkAccount(ident.New(ident.Account), ""); !errors.Is(err, jam.ErrAccountNotFound) {
			t.Fatalf("unknown account: %v, want ErrAccountNotFound", err)
		}
	})

	t.Run("accounts_listed_per_connection", func(t *testing.T) {
		s := newStore(t)
		c1 := mustConn(t, s, "discord", "d1")
		c2 := mustConn(t, s, "discord", "d2")
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c1.ID, ServiceUID: "1"}); err != nil {
			t.Fatal(err)
		}
		// The same service uid on another connection is a different account.
		if _, err := s.UpsertAccount(jam.Account{ConnectionID: c2.ID, ServiceUID: "1"}); err != nil {
			t.Fatal(err)
		}
		if n1, n2 := len(s.ListAccounts(c1.ID)), len(s.ListAccounts(c2.ID)); n1 != 1 || n2 != 1 {
			t.Fatalf("ListAccounts = %d, %d; want 1, 1", n1, n2)
		}
	})
}
