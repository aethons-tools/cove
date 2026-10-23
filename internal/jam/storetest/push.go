package storetest

import (
	"fmt"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runPushConformance exercises the PushSubscriptionStore contract.
func runPushConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	base := time.Unix(1_000_000, 0).UTC()
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
	sub := func(endpoint string, u ident.ID, age int) jam.PushSubscription {
		return jam.PushSubscription{Endpoint: endpoint, UserID: u, P256dh: "p", Auth: "a", UserAgent: "ua",
			CreatedAt: base.Add(time.Duration(age) * time.Minute), LastOKAt: base.Add(time.Duration(age) * time.Minute)}
	}
	endpoints := func(ps []jam.PushSubscription) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.Endpoint)
		}
		return out
	}

	t.Run("push_put_list_oldest_first", func(t *testing.T) {
		s, a, b := setup(t)
		for _, p := range []jam.PushSubscription{sub("https://e/2", a.ID, 2), sub("https://e/1", a.ID, 1), sub("https://e/3", b.ID, 3)} {
			if err := s.PutPushSubscription(p); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
		got := s.PushSubscriptions(a.ID)
		if len(got) != 2 || got[0] != sub("https://e/1", a.ID, 1) || got[1].Endpoint != "https://e/2" {
			t.Fatalf("list = %+v", got)
		}
		if len(s.PushSubscriptions("none")) != 0 {
			t.Fatal("unknown user must list nothing")
		}
		if err := s.PutPushSubscription(sub("https://e/x", "usr_missing", 1)); err == nil {
			t.Fatal("unknown user must be refused")
		}
	})

	t.Run("push_upsert_moves_endpoint_between_users", func(t *testing.T) {
		s, a, b := setup(t)
		_ = s.PutPushSubscription(sub("https://e/1", a.ID, 1))
		moved := sub("https://e/1", b.ID, 5)
		moved.P256dh = "p2"
		if err := s.PutPushSubscription(moved); err != nil {
			t.Fatal(err)
		}
		if got := s.PushSubscriptions(a.ID); len(got) != 0 {
			t.Fatalf("old user still has %+v", got)
		}
		if got := s.PushSubscriptions(b.ID); len(got) != 1 || got[0] != moved {
			t.Fatalf("new user = %+v", got)
		}
	})

	t.Run("push_cap_evicts_oldest", func(t *testing.T) {
		s, a, b := setup(t)
		_ = s.PutPushSubscription(sub("https://other", b.ID, 0))
		for i := 1; i <= jam.MaxPushSubscriptionsPerUser+1; i++ {
			if err := s.PutPushSubscription(sub(fmt.Sprintf("https://e/%02d", i), a.ID, i)); err != nil {
				t.Fatal(err)
			}
		}
		got := endpoints(s.PushSubscriptions(a.ID))
		if len(got) != jam.MaxPushSubscriptionsPerUser || got[0] != "https://e/02" {
			t.Fatalf("after cap: %d subs, first %v", len(got), got[:1])
		}
		if len(s.PushSubscriptions(b.ID)) != 1 {
			t.Fatal("eviction touched another user")
		}
		// Re-putting an existing endpoint at the cap evicts nothing.
		if err := s.PutPushSubscription(sub("https://e/05", a.ID, 50)); err != nil {
			t.Fatal(err)
		}
		if n := len(s.PushSubscriptions(a.ID)); n != jam.MaxPushSubscriptionsPerUser {
			t.Fatalf("upsert at cap: %d subs", n)
		}
	})

	t.Run("push_delete", func(t *testing.T) {
		s, a, b := setup(t)
		_ = s.PutPushSubscription(sub("https://e/1", a.ID, 1))
		_ = s.PutPushSubscription(sub("https://e/2", a.ID, 2))
		if ok, err := s.DeleteUserPushSubscription(b.ID, "https://e/1"); err != nil || ok {
			t.Fatalf("other user's delete: ok=%v err=%v", ok, err)
		}
		if len(s.PushSubscriptions(a.ID)) != 2 {
			t.Fatal("row must be kept")
		}
		if ok, err := s.DeleteUserPushSubscription(a.ID, "https://e/1"); err != nil || !ok {
			t.Fatalf("own delete: ok=%v err=%v", ok, err)
		}
		if err := s.DeletePushSubscription("https://e/2"); err != nil {
			t.Fatal(err)
		}
		if len(s.PushSubscriptions(a.ID)) != 0 {
			t.Fatal("rows remain")
		}
		if err := s.DeletePushSubscription("https://absent"); err != nil {
			t.Fatalf("absent delete: %v", err)
		}
		if ok, err := s.DeleteUserPushSubscription(a.ID, "https://absent"); err != nil || ok {
			t.Fatalf("absent user delete: ok=%v err=%v", ok, err)
		}
	})

	t.Run("push_touch", func(t *testing.T) {
		s, a, _ := setup(t)
		_ = s.PutPushSubscription(sub("https://e/1", a.ID, 1))
		at := base.Add(time.Hour)
		if err := s.TouchPushSubscription("https://e/1", at); err != nil {
			t.Fatal(err)
		}
		if got := s.PushSubscriptions(a.ID); len(got) != 1 || !got[0].LastOKAt.Equal(at) {
			t.Fatalf("touch: %+v", got)
		}
		if err := s.TouchPushSubscription("https://absent", at); err != nil {
			t.Fatalf("absent touch: %v", err)
		}
	})

	t.Run("push_remove_user_deletes_subscriptions", func(t *testing.T) {
		s, a, b := setup(t)
		_ = s.PutPushSubscription(sub("https://e/1", a.ID, 1))
		_ = s.PutPushSubscription(sub("https://e/2", b.ID, 2))
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatal(err)
		}
		if len(s.PushSubscriptions(a.ID)) != 0 || len(s.PushSubscriptions(b.ID)) != 1 {
			t.Fatalf("after RemoveUser: a=%+v b=%+v", s.PushSubscriptions(a.ID), s.PushSubscriptions(b.ID))
		}
	})
}
