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

	t.Run("project_has_id_and_resolves", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		p, _ := s.GetProject("acme")
		if p.ID.Kind() != ident.Project {
			t.Fatalf("project id = %q", p.ID)
		}
		if _, err := ident.Parse(string(p.ID)); err != nil {
			t.Fatalf("project id does not parse: %v", err)
		}
		if id, ok := s.LookupName(ident.Project, "acme"); !ok || id != p.ID {
			t.Fatalf("LookupName = %q, %v", id, ok)
		}
		if e, ok := s.Resolve(p.ID); !ok || e.Kind != ident.Project || e.Label() != "acme" {
			t.Fatalf("Resolve = %+v, %v", e, ok)
		}
		if err := s.CreateProject("beta"); err != nil {
			t.Fatal(err)
		}
		if b, _ := s.GetProject("beta"); b.ID == p.ID {
			t.Fatal("two projects share an id")
		}
	})

	t.Run("default_project_materialized_with_id", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRole("", jam.Role{Name: "r"}); err != nil {
			t.Fatal(err)
		}
		if p, ok := s.GetProject(jam.DefaultProject); !ok || p.ID.Kind() != ident.Project {
			t.Fatalf("default project = %+v, %v", p, ok)
		}
	})

	t.Run("project_id_survives_edits", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		before, _ := s.GetProject("acme")
		if err := s.SetChatService("acme", "discord"); err != nil {
			t.Fatal(err)
		}
		if after, _ := s.GetProject("acme"); after.ID != before.ID {
			t.Fatalf("id changed on edit: %q → %q", before.ID, after.ID)
		}
	})

	t.Run("project_recreated_gets_new_id", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		old, _ := s.GetProject("acme")
		if err := s.RemoveProject("acme"); err != nil {
			t.Fatal(err)
		}
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		if p, _ := s.GetProject("acme"); p.ID == old.ID {
			t.Fatal("a re-created project must get a new id")
		}
	})

	t.Run("export_import_keeps_project_ids", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		want, _ := s.GetProject("acme")
		s2 := newStore(t)
		if err := s2.ImportConfig(s.ExportConfig()); err != nil {
			t.Fatalf("ImportConfig: %v", err)
		}
		if got, _ := s2.GetProject("acme"); got.ID != want.ID {
			t.Fatalf("imported id = %q, want %q", got.ID, want.ID)
		}
	})

	t.Run("import_mints_missing_project_ids", func(t *testing.T) {
		s := newStore(t)
		snap := jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion,
			Projects: []jam.Project{{Name: "acme"}},
			Roles:    map[string]map[string]jam.Role{"beta": {"r": {Name: "r"}}}}
		if err := s.ImportConfig(snap); err != nil {
			t.Fatalf("ImportConfig: %v", err)
		}
		for _, name := range []string{"acme", "beta"} {
			p, ok := s.GetProject(name)
			if !ok || p.ID.Kind() != ident.Project {
				t.Fatalf("%s = %+v, %v", name, p, ok)
			}
			if e, ok := s.Resolve(p.ID); !ok || e.Name != name {
				t.Fatalf("Resolve(%s) = %+v, %v", name, e, ok)
			}
		}
	})

	t.Run("import_rejects_bad_project_ids", func(t *testing.T) {
		dup := ident.New(ident.Project)
		for name, ps := range map[string][]jam.Project{
			"wrong kind": {{ID: ident.New(ident.User), Name: "acme"}},
			"malformed":  {{ID: "prj_nope", Name: "acme"}},
			"duplicate":  {{ID: dup, Name: "acme"}, {ID: dup, Name: "beta"}},
		} {
			s := newStore(t)
			if err := s.ImportConfig(jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Projects: ps}); !errors.Is(err, jam.ErrInvalidConfig) {
				t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
			}
		}
	})

	mustProject := func(t *testing.T, s jam.Store, name string) ident.ID {
		t.Helper()
		if err := s.CreateProject(name); err != nil {
			t.Fatal(err)
		}
		p, _ := s.GetProject(name)
		return p.ID
	}

	t.Run("membership_lifecycle", func(t *testing.T) {
		s := newStore(t)
		acme, beta := mustProject(t, s, "acme"), mustProject(t, s, "beta")
		a, b := mustUser(t, s, "alice"), mustUser(t, s, "bob")
		for _, m := range [][2]ident.ID{{acme, a.ID}, {acme, b.ID}, {beta, a.ID}, {acme, a.ID}} {
			if err := s.AddMember(m[0], m[1]); err != nil {
				t.Fatalf("AddMember %v: %v", m, err)
			}
		}
		if !s.IsMember(acme, a.ID) || s.IsMember(beta, b.ID) {
			t.Fatal("IsMember wrong")
		}
		if got := s.ListMembers(acme); len(got) != 2 {
			t.Fatalf("ListMembers = %v, want 2 (re-adding is a no-op)", got)
		}
		if got := s.ListMemberships(a.ID); len(got) != 2 {
			t.Fatalf("ListMemberships = %v", got)
		}
		if err := s.RemoveMember(acme, b.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveMember(acme, b.ID); !errors.Is(err, jam.ErrMembershipNotFound) {
			t.Fatalf("second RemoveMember: %v, want ErrMembershipNotFound", err)
		}
		if err := s.AddMember(ident.New(ident.Project), a.ID); !errors.Is(err, jam.ErrProjectNotFound) {
			t.Fatalf("unknown project: %v, want ErrProjectNotFound", err)
		}
		if err := s.AddMember(acme, ident.New(ident.User)); !errors.Is(err, jam.ErrUserNotFound) {
			t.Fatalf("unknown user: %v, want ErrUserNotFound", err)
		}
	})

	t.Run("membership_dropped_on_user_removal", func(t *testing.T) {
		s := newStore(t)
		acme := mustProject(t, s, "acme")
		a := mustUser(t, s, "alice")
		if err := s.AddMember(acme, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatal(err)
		}
		if s.IsMember(acme, a.ID) || len(s.ListMembers(acme)) != 0 {
			t.Fatal("a removed user must not stay a member")
		}
		if err := s.AddMember(acme, a.ID); !errors.Is(err, jam.ErrRemoved) {
			t.Fatalf("add removed user: %v, want ErrRemoved", err)
		}
	})

	t.Run("project_with_members_not_removable", func(t *testing.T) {
		s := newStore(t)
		acme := mustProject(t, s, "acme")
		a := mustUser(t, s, "alice")
		if err := s.AddMember(acme, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveProject("acme"); !errors.Is(err, jam.ErrProjectInUse) {
			t.Fatalf("RemoveProject with members: %v, want ErrProjectInUse", err)
		}
		if err := s.RemoveMember(acme, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveProject("acme"); err != nil {
			t.Fatalf("RemoveProject after the last member left: %v", err)
		}
	})

	t.Run("membership_delivery", func(t *testing.T) {
		s := newStore(t)
		acme := mustProject(t, s, "acme")
		a := mustUser(t, s, "alice")
		d := []jam.DeliveryProfile{{Service: "discord", Address: "chan-1"}}
		if err := s.PutMembership(jam.Membership{ProjectID: acme, UserID: a.ID, Delivery: d}); err != nil {
			t.Fatalf("PutMembership: %v", err)
		}
		if err := s.AddMember(acme, a.ID); err != nil { // re-adding keeps the delivery
			t.Fatal(err)
		}
		m, ok := s.GetMembership(acme, a.ID)
		if !ok || len(m.Delivery) != 1 || m.Delivery[0].Address != "chan-1" {
			t.Fatalf("GetMembership = %+v, %v", m, ok)
		}
		m.Delivery[0].Address = "mutated"
		if again, _ := s.GetMembership(acme, a.ID); again.Delivery[0].Address != "chan-1" {
			t.Fatal("store mutated through a returned membership")
		}
		if err := s.PutMembership(jam.Membership{ProjectID: acme, UserID: a.ID}); err != nil {
			t.Fatal(err)
		}
		if m, _ := s.GetMembership(acme, a.ID); len(m.Delivery) != 0 {
			t.Fatalf("PutMembership must replace the delivery: %+v", m)
		}
		if _, ok := s.GetMembership(acme, ident.New(ident.User)); ok {
			t.Fatal("GetMembership of a non-member must be false")
		}
		if err := s.PutMembership(jam.Membership{ProjectID: ident.New(ident.Project), UserID: a.ID}); !errors.Is(err, jam.ErrProjectNotFound) {
			t.Fatalf("unknown project: %v, want ErrProjectNotFound", err)
		}
	})

	t.Run("roster_humans_are_jam_wide_users", func(t *testing.T) {
		s := newStore(t)
		acme, beta := mustProject(t, s, "acme"), mustProject(t, s, "beta")
		if err := s.AddHuman("acme", jam.Human{Name: "alice", Handle: "@alice", Login: "auth0|a"}); err != nil {
			t.Fatal(err)
		}
		if err := s.AddHuman("beta", jam.Human{Name: "alice", Handle: "@alice", Login: "auth0|a",
			Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-b"}}}); err != nil {
			t.Fatal(err)
		}
		id, ok := s.LookupName(ident.User, "alice")
		if !ok || !s.IsMember(acme, id) || !s.IsMember(beta, id) || len(s.ListUsers()) != 1 {
			t.Fatalf("alice = %q, %v; users %+v", id, ok, s.ListUsers())
		}
		if ms, _ := s.GetMembership(beta, id); len(ms.Delivery) != 1 || ms.Delivery[0].Address != "inbox-b" {
			t.Fatalf("beta membership = %+v", ms)
		}
		if p, _ := s.GetProject("beta"); len(p.Roster.Humans) != 1 || p.Roster.Humans[0].Handle != "@alice" {
			t.Fatalf("beta roster view = %+v", p.Roster.Humans)
		}
		if err := s.RemoveHuman("acme", "alice"); err != nil {
			t.Fatal(err)
		}
		if s.IsMember(acme, id) || !s.IsMember(beta, id) {
			t.Fatal("RemoveHuman ends only that project's membership")
		}
		if _, ok := s.GetUser(id); !ok {
			t.Fatal("the user outlives a membership")
		}
	})

	t.Run("import_migrates_roster_humans", func(t *testing.T) {
		s := newStore(t)
		snap := jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Projects: []jam.Project{
			{Name: "acme", Roster: jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "@a", Login: "auth0|a",
				Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-a", UserID: "111"}}}}}},
			{Name: "beta", Roster: jam.Roster{Humans: []jam.Human{{Name: "alice"}}}},
		}}
		if err := s.ImportConfig(snap); err != nil {
			t.Fatalf("ImportConfig: %v", err)
		}
		id, ok := s.LookupName(ident.User, "alice")
		if !ok {
			t.Fatalf("users = %+v", s.ListUsers())
		}
		for _, project := range []string{"acme", "beta"} {
			if got, ok := s.LegacyHumanAlias(project, "alice"); !ok || got != id {
				t.Fatalf("alias %s/alice = %q, %v", project, got, ok)
			}
		}
		r, _ := s.GetRoster("acme")
		if len(r.Humans) != 1 || r.Humans[0].Login != "auth0|a" || r.Humans[0].Handle != "@a" {
			t.Fatalf("acme roster = %+v", r.Humans)
		}
		if d, ok := r.Humans[0].DeliveryFor("discord"); !ok || d.Address != "inbox-a" || d.UserID != "111" {
			t.Fatalf("acme delivery = %+v, %v", d, ok)
		}
		// A (v2) export carries the people in the registry, not as humans.
		if snap := s.ExportConfig(); len(snap.Users) != 1 || len(snap.Memberships) != 2 {
			t.Fatalf("exported users %d, memberships %d; want 1 and 2", len(snap.Users), len(snap.Memberships))
		}
	})

	t.Run("add_human_never_strips_identity_elsewhere", func(t *testing.T) {
		s := newStore(t)
		mustProject(t, s, "acme")
		mustProject(t, s, "beta")
		full := jam.Human{Name: "alice", Handle: "@alice", Login: "auth0|a",
			Identity: []jam.OIDCIdentity{{Issuer: "i", Subject: "s"}},
			Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-a", UserID: "111"}}}
		if err := s.AddHuman("acme", full); err != nil {
			t.Fatal(err)
		}
		// alice joins beta with nothing but her name: acme's view must not change.
		if err := s.AddHuman("beta", jam.Human{Name: "alice"}); err != nil {
			t.Fatal(err)
		}
		r, _ := s.GetRoster("acme")
		h := r.Humans[0]
		if h.Login != "auth0|a" || h.Handle != "@alice" || len(h.Identity) != 1 {
			t.Fatalf("acme alice after a bare add in beta = %+v", h)
		}
		if d, _ := h.DeliveryFor("discord"); d.UserID != "111" || d.Address != "inbox-a" {
			t.Fatalf("acme delivery = %+v", d)
		}
		// A new handle given anywhere is the person's handle everywhere.
		if err := s.AddHuman("beta", jam.Human{Name: "alice", Handle: "@alice2"}); err != nil {
			t.Fatal(err)
		}
		if r, _ := s.GetRoster("acme"); r.Humans[0].Handle != "@alice2" {
			t.Fatalf("acme handle = %q, want @alice2", r.Humans[0].Handle)
		}
	})

	t.Run("connection_cred_and_chat_service_refs", func(t *testing.T) {
		s := newStore(t)
		c := mustConn(t, s, "discord", "discord-main")
		if err := s.SetConnectionCred(c.ID, "bot-tok"); err != nil {
			t.Fatalf("SetConnectionCred: %v", err)
		}
		if got, _ := s.GetConnection(c.ID); got.CredName != "bot-tok" {
			t.Fatalf("cred = %q", got.CredName)
		}
		if err := s.SetConnectionCred(ident.New(ident.Connection), "x"); !errors.Is(err, jam.ErrConnectionNotFound) {
			t.Fatalf("unknown connection: %v, want ErrConnectionNotFound", err)
		}
		mustProject(t, s, "acme")
		if err := s.SetChatService("acme", "discord-main"); err != nil {
			t.Fatalf("SetChatService by name: %v", err)
		}
		if p, _ := s.GetProject("acme"); p.ChatService != string(c.ID) {
			t.Fatalf("chat service = %q, want the connection id %q", p.ChatService, c.ID)
		}
		if err := s.RemoveConnection(c.ID); !errors.Is(err, jam.ErrConnectionInUse) {
			t.Fatalf("remove a project's chat service: %v, want ErrConnectionInUse", err)
		}
		lin := mustConn(t, s, "linear", "linear-main")
		if err := s.SetChatService("acme", string(lin.ID)); err == nil {
			t.Fatal("a linear connection is not a chat service")
		}
		if err := s.SetChatService("acme", "nope"); !errors.Is(err, jam.ErrConnectionNotFound) {
			t.Fatalf("unknown chat service: %v, want ErrConnectionNotFound", err)
		}
		if err := s.SetChatService("acme", ""); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveConnection(c.ID); err != nil {
			t.Fatalf("remove once unreferenced: %v", err)
		}
	})

	t.Run("roster_view_follows_connection_of_kind", func(t *testing.T) {
		s := newStore(t)
		mustProject(t, s, "acme")
		if err := s.AddHuman("acme", jam.Human{Name: "alice", Handle: "@alice"}); err != nil {
			t.Fatal(err)
		}
		lin, ok := s.LookupName(ident.Connection, "linear")
		if !ok {
			t.Fatal("AddHuman with a handle must create the implicit linear connection")
		}
		if err := s.RenameConnection(lin, "linear-acme"); err != nil {
			t.Fatal(err)
		}
		if r, _ := s.GetRoster("acme"); len(r.Humans) != 1 || r.Humans[0].Handle != "@alice" {
			t.Fatalf("after renaming the linear connection, roster = %+v", r.Humans)
		}
		// A new handle binds on the same (renamed) connection, not a new implicit one.
		if err := s.AddHuman("acme", jam.Human{Name: "bob", Handle: "@bob"}); err != nil {
			t.Fatal(err)
		}
		if n := len(s.ListConnections()); n != 1 {
			t.Fatalf("connections = %+v, want only linear-acme", s.ListConnections())
		}
	})

	t.Run("chat_service_survives_export_import", func(t *testing.T) {
		s := newStore(t)
		mustProject(t, s, "acme")
		if err := s.SetChatService("acme", "discord"); err != nil {
			t.Fatal(err)
		}
		s2 := newStore(t)
		if err := s2.ImportConfig(s.ExportConfig()); err != nil {
			t.Fatalf("ImportConfig: %v", err)
		}
		if p, _ := s2.GetProject("acme"); jam.ChatKind(s2, p) != "discord" {
			t.Fatalf("restored chat service = %q, want a discord connection", p.ChatService)
		}
		// A snapshot naming a connection id this Jam doesn't have is cleared,
		// never left dangling.
		s3 := newStore(t)
		snap := jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Projects: []jam.Project{{Name: "beta", ChatService: string(ident.New(ident.Connection))}}}
		if err := s3.ImportConfig(snap); err != nil {
			t.Fatal(err)
		}
		if p, _ := s3.GetProject("beta"); p.ChatService != "" {
			t.Fatalf("dangling chat service kept: %q", p.ChatService)
		}
	})

	t.Run("export_import_v2_round_trips_registry", func(t *testing.T) {
		s := newStore(t)
		// A v1 restore builds the registry (and legacy aliases) from humans.
		v1 := jam.ConfigSnapshot{Version: 1, Projects: []jam.Project{{Name: "acme", Roster: jam.Roster{Humans: []jam.Human{
			{Name: "alice", Handle: "@alice", Login: "auth0|a", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox-a", UserID: "111"}}},
		}}}}}
		if err := s.ImportConfig(v1); err != nil {
			t.Fatalf("v1 import: %v", err)
		}
		gone, _ := s.CreateUser(jam.User{Name: "gone"})
		if err := s.RemoveUser(gone.ID); err != nil {
			t.Fatal(err)
		}
		if err := s.SetChatService("acme", "discord"); err != nil {
			t.Fatal(err)
		}
		snap := s.ExportConfig()
		if snap.Version != jam.ConfigSnapshotVersion || len(snap.Users) != 2 || len(snap.Connections) != 2 || len(snap.Accounts) != 2 || len(snap.Memberships) != 1 || len(snap.LegacyAliases) != 1 {
			t.Fatalf("v2 export = version %d, %d users, %d connections, %d accounts, %d memberships, %d aliases",
				snap.Version, len(snap.Users), len(snap.Connections), len(snap.Accounts), len(snap.Memberships), len(snap.LegacyAliases))
		}
		for _, p := range snap.Projects {
			if len(p.Roster.Humans) != 0 {
				t.Fatalf("a v2 export carries people in the registry, not roster humans: %+v", p.Roster.Humans)
			}
		}

		s2 := newStore(t)
		if err := s2.ImportConfig(snap); err != nil {
			t.Fatalf("v2 import: %v", err)
		}
		alice, _ := s.LookupName(ident.User, "alice")
		if got, ok := s2.LookupName(ident.User, "alice"); !ok || got != alice {
			t.Fatalf("alice = %q, want her id %q preserved", got, alice)
		}
		if e, ok := s2.Resolve(gone.ID); !ok || e.Label() != "gone (removed)" {
			t.Fatalf("removed user = %+v, %v; tombstones back history and must survive", e, ok)
		}
		if a, ok := s2.LegacyHumanAlias("acme", "alice"); !ok || a != alice {
			t.Fatalf("legacy alias = %q, %v", a, ok)
		}
		r, _ := s2.GetRoster("acme")
		if len(r.Humans) != 1 || r.Humans[0].Handle != "@alice" || r.Humans[0].Login != "auth0|a" {
			t.Fatalf("restored roster = %+v", r.Humans)
		}
		if d, ok := r.Humans[0].DeliveryFor("discord"); !ok || d.Address != "inbox-a" || d.UserID != "111" {
			t.Fatalf("restored delivery = %+v, %v", d, ok)
		}
		p1, _ := s.GetProject("acme")
		if p2, _ := s2.GetProject("acme"); p2.ChatService != p1.ChatService || jam.ChatKind(s2, p2) != "discord" {
			t.Fatalf("restored chat service = %q, want %q", p2.ChatService, p1.ChatService)
		}
		// A target holding users is not empty.
		if err := s2.ImportConfig(snap); !errors.Is(err, jam.ErrConfigNotEmpty) {
			t.Fatalf("import into a populated registry: %v, want ErrConfigNotEmpty", err)
		}
	})

	t.Run("import_v2_rejects_inconsistent_registry", func(t *testing.T) {
		u := jam.User{ID: ident.New(ident.User), Name: "alice", Status: jam.StatusLive}
		c := jam.Connection{ID: ident.New(ident.Connection), Kind: "discord", Name: "discord", Status: jam.StatusLive}
		v2 := func(mut func(*jam.ConfigSnapshot)) jam.ConfigSnapshot {
			s := jam.ConfigSnapshot{Version: jam.ConfigSnapshotVersion, Projects: []jam.Project{{ID: ident.New(ident.Project), Name: "acme"}},
				Users: []jam.User{u}, Connections: []jam.Connection{c}}
			mut(&s)
			return s
		}
		for name, snap := range map[string]jam.ConfigSnapshot{
			"duplicate user name": v2(func(s *jam.ConfigSnapshot) {
				s.Users = append(s.Users, jam.User{ID: ident.New(ident.User), Name: "alice", Status: jam.StatusLive})
			}),
			"account on an unknown connection": v2(func(s *jam.ConfigSnapshot) {
				s.Accounts = []jam.Account{{ID: ident.New(ident.Account), ConnectionID: ident.New(ident.Connection), ServiceUID: "1", Status: jam.StatusLive}}
			}),
			"account linked to an unknown user": v2(func(s *jam.ConfigSnapshot) {
				s.Accounts = []jam.Account{{ID: ident.New(ident.Account), ConnectionID: c.ID, ServiceUID: "1", UserID: ident.New(ident.User), Status: jam.StatusLive}}
			}),
			"membership of an unknown project": v2(func(s *jam.ConfigSnapshot) {
				s.Memberships = []jam.Membership{{ProjectID: ident.New(ident.Project), UserID: u.ID}}
			}),
			"alias to an unknown user": v2(func(s *jam.ConfigSnapshot) {
				s.LegacyAliases = []jam.LegacyAlias{{Project: "acme", Name: "x", UserID: ident.New(ident.User)}}
			}),
			"user id of the wrong kind": v2(func(s *jam.ConfigSnapshot) {
				s.Users[0].ID = ident.New(ident.Account)
			}),
		} {
			if err := newStore(t).ImportConfig(snap); !errors.Is(err, jam.ErrInvalidConfig) {
				t.Errorf("%s: %v, want ErrInvalidConfig", name, err)
			}
		}
	})

	t.Run("import_v2_over_startup_connections", func(t *testing.T) {
		src := newStore(t)
		mustProject(t, src, "acme")
		if err := src.SetChatService("acme", "discord"); err != nil {
			t.Fatal(err)
		}
		c := mustConn(t, src, "linear", "linear")
		// A label-only account (a handle displaced by the uid-wins rule).
		if _, err := src.UpsertAccount(jam.Account{ConnectionID: c.ID, Handle: "bob"}); err != nil {
			t.Fatal(err)
		}
		if _, err := src.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u1"}); err != nil {
			t.Fatal(err)
		}
		if _, err := src.UpsertAccount(jam.Account{ConnectionID: c.ID, ServiceUID: "u1", Handle: "bob"}); err != nil {
			t.Fatal(err)
		}
		snap := src.ExportConfig()

		// The target already has the connection a starting serve creates.
		dst := newStore(t)
		boot := mustConn(t, dst, "discord", "discord")
		if err := dst.ImportConfig(snap); err != nil {
			t.Fatalf("v2 import over a startup connection: %v", err)
		}
		id, ok := dst.LookupName(ident.Connection, "discord")
		srcID, _ := src.LookupName(ident.Connection, "discord")
		if !ok || id != srcID || id == boot.ID {
			t.Fatalf("live discord connection = %q, want the snapshot's %q", id, srcID)
		}
		if p, _ := dst.GetProject("acme"); jam.ChatKind(dst, p) != "discord" {
			t.Fatalf("chat service = %q", p.ChatService)
		}
		if n := len(dst.ListAccounts(c.ID)); n != 2 {
			t.Fatalf("accounts = %d, want both (one label-only)", n)
		}

		// A v1 snapshot reuses the startup connection instead of a second one.
		dst2 := newStore(t)
		boot2 := mustConn(t, dst2, "discord", "discord")
		v1 := jam.ConfigSnapshot{Version: 1, Projects: []jam.Project{{Name: "acme", ChatService: "discord", Roster: jam.Roster{Humans: []jam.Human{
			{Name: "alice", Delivery: []jam.DeliveryProfile{{Service: "discord", Address: "inbox", UserID: "111"}}}}}}}}
		if err := dst2.ImportConfig(v1); err != nil {
			t.Fatalf("v1 import over a startup connection: %v", err)
		}
		if conns := dst2.ListConnections(); len(conns) != 1 || conns[0].ID != boot2.ID {
			t.Fatalf("connections = %+v, want only the startup one", conns)
		}
	})
}
