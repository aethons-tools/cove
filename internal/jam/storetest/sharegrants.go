package storetest

import (
	"reflect"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runShareGrantConformance exercises the ShareGrantStore contract.
func runShareGrantConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	setup := func(t *testing.T) (jam.Store, jam.User, jam.User) {
		t.Helper()
		s := newStore(t)
		a, err := s.CreateUser(jam.User{Name: "alice"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateUser(jam.User{Name: "bob"})
		if err != nil {
			t.Fatal(err)
		}
		return s, a, b
	}
	exp := time.Unix(2_000_000, 0).UTC()
	grant := func(session string, u jam.User, rights ...string) jam.ShareGrant {
		return jam.ShareGrant{SessionID: session, UserID: u.ID, Rights: rights, ExpiresAt: exp, CreatedAt: exp.Add(-time.Hour), GrantedBy: "op:local"}
	}

	t.Run("share_grant_put_list_replace", func(t *testing.T) {
		s, a, b := setup(t)
		for _, g := range []jam.ShareGrant{grant("ses_1", b, "preview"), grant("ses_1", a, "ssh"), grant("ses_2", a, "ssh")} {
			if err := s.PutShareGrant(g); err != nil {
				t.Fatalf("PutShareGrant: %v", err)
			}
		}
		byUser := func(gs []jam.ShareGrant) map[ident.ID]jam.ShareGrant {
			m := map[ident.ID]jam.ShareGrant{}
			for _, g := range gs {
				m[g.UserID] = g
			}
			return m
		}
		got := s.ShareGrants("ses_1")
		if m := byUser(got); len(got) != 2 || !reflect.DeepEqual(m[a.ID], grant("ses_1", a, "ssh")) || !reflect.DeepEqual(m[b.ID], grant("ses_1", b, "preview")) || got[0].UserID > got[1].UserID {
			t.Fatalf("ShareGrants(ses_1) = %+v", got)
		}
		// Replace by (session, user).
		if err := s.PutShareGrant(grant("ses_1", a, "preview", "ssh")); err != nil {
			t.Fatal(err)
		}
		got = s.ShareGrants("ses_1")
		if m := byUser(got); len(got) != 2 || !reflect.DeepEqual(m[a.ID].Rights, []string{"preview", "ssh"}) {
			t.Fatalf("after replace = %+v", got)
		}
		// Returned values are copies.
		got[0].Rights[0] = "mutated"
		if r := s.ShareGrants("ses_1")[0].Rights[0]; r == "mutated" {
			t.Fatal("ShareGrants must return copies")
		}
		if len(s.ShareGrants("ses_none")) != 0 {
			t.Fatal("unknown session has grants")
		}
	})

	t.Run("share_grant_remove", func(t *testing.T) {
		s, a, b := setup(t)
		_ = s.PutShareGrant(grant("ses_1", a, "ssh"))
		_ = s.PutShareGrant(grant("ses_1", b, "ssh"))
		if err := s.RemoveShareGrant("ses_1", a.ID); err != nil {
			t.Fatal(err)
		}
		if got := s.ShareGrants("ses_1"); len(got) != 1 || got[0].UserID != b.ID {
			t.Fatalf("after remove = %+v", got)
		}
		if err := s.RemoveShareGrant("ses_1", a.ID); err != nil {
			t.Fatalf("removing an absent grant: %v", err)
		}
	})

	t.Run("share_grant_remove_of_session", func(t *testing.T) {
		s, a, b := setup(t)
		_ = s.PutShareGrant(grant("ses_1", a, "ssh"))
		_ = s.PutShareGrant(grant("ses_1", b, "ssh"))
		_ = s.PutShareGrant(grant("ses_2", a, "ssh"))
		if err := s.RemoveShareGrantsOf("ses_1"); err != nil {
			t.Fatal(err)
		}
		if len(s.ShareGrants("ses_1")) != 0 || len(s.ShareGrants("ses_2")) != 1 {
			t.Fatalf("ses_1=%+v ses_2=%+v", s.ShareGrants("ses_1"), s.ShareGrants("ses_2"))
		}
		if err := s.RemoveShareGrantsOf("ses_1"); err != nil {
			t.Fatalf("removing an absent session's grants: %v", err)
		}
	})

	t.Run("share_grant_requires_session_and_user", func(t *testing.T) {
		s, a, _ := setup(t)
		if err := s.PutShareGrant(jam.ShareGrant{UserID: a.ID}); err == nil {
			t.Fatal("grant without a session accepted")
		}
		if err := s.PutShareGrant(jam.ShareGrant{SessionID: "ses_1"}); err == nil {
			t.Fatal("grant without a user accepted")
		}
	})
}
