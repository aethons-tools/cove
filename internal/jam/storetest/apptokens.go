package storetest

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runAppTokenConformance exercises the AppTokenStore contract.
func runAppTokenConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
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
	n := 0
	tok := func(u ident.ID, age int) jam.AppToken {
		n++
		at := base.Add(time.Duration(age) * time.Minute)
		return jam.AppToken{ID: ident.New(ident.AppToken), UserID: u, Hash: fmt.Sprintf("hash-%d", n), Label: "VS Code",
			CreatedAt: at, LastUsedAt: at, ExpiresAt: at.Add(30 * 24 * time.Hour)}
	}
	put := func(t *testing.T, s jam.Store, a jam.AppToken) {
		t.Helper()
		if _, err := s.PutAppToken(a); err != nil {
			t.Fatalf("PutAppToken: %v", err)
		}
	}
	ids := func(ts []jam.AppToken) []ident.ID {
		var out []ident.ID
		for _, x := range ts {
			out = append(out, x.ID)
		}
		return out
	}

	t.Run("app_token_put_lookup_by_hash", func(t *testing.T) {
		s, a, _ := setup(t)
		in := tok(a.ID, 1)
		in.OperatorID = "op-1"
		in.Label = "VS Code on mac"
		in.LastUsedAt = base.Add(90 * time.Minute).Add(1234 * time.Microsecond)
		evicted, err := s.PutAppToken(in)
		if err != nil || len(evicted) != 0 {
			t.Fatalf("Put: evicted=%v err=%v", evicted, err)
		}
		got, ok := s.AppTokenByHash(in.Hash)
		if !ok || got != in {
			t.Fatalf("by hash = %+v ok=%v, want %+v", got, ok, in)
		}
		got, ok = s.AppTokenByID(in.ID)
		if !ok || got != in {
			t.Fatalf("by id = %+v ok=%v", got, ok)
		}
		if _, ok := s.AppTokenByHash("nope"); ok {
			t.Fatal("unknown hash found")
		}
		if _, ok := s.AppTokenByID("apt_missing"); ok {
			t.Fatal("unknown id found")
		}
	})

	t.Run("app_token_unknown_or_removed_user_refused", func(t *testing.T) {
		s, a, _ := setup(t)
		if _, err := s.PutAppToken(tok("usr_missing", 1)); err == nil {
			t.Fatal("unknown user must be refused")
		}
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutAppToken(tok(a.ID, 1)); err == nil {
			t.Fatal("removed user must be refused")
		}
		if _, err := s.PutAppToken(jam.AppToken{UserID: a.ID, Hash: "h"}); err == nil {
			t.Fatal("missing id must be refused")
		}
	})

	t.Run("app_token_duplicate_hash_refused", func(t *testing.T) {
		s, a, b := setup(t)
		first := tok(a.ID, 1)
		put(t, s, first)
		dup := tok(b.ID, 2)
		dup.Hash = first.Hash
		if _, err := s.PutAppToken(dup); err == nil {
			t.Fatal("duplicate hash must be refused")
		}
		again := tok(a.ID, 3)
		again.ID = first.ID
		if _, err := s.PutAppToken(again); err == nil {
			t.Fatal("duplicate id must be refused")
		}
		if got, _ := s.AppTokenByHash(first.Hash); got != first {
			t.Fatalf("original changed: %+v", got)
		}
	})

	t.Run("app_token_list_oldest_first_owner_only", func(t *testing.T) {
		s, a, b := setup(t)
		t2, t1, tb := tok(a.ID, 2), tok(a.ID, 1), tok(b.ID, 0)
		put(t, s, t2)
		put(t, s, t1)
		put(t, s, tb)
		got := ids(s.AppTokens(a.ID))
		if len(got) != 2 || got[0] != t1.ID || got[1] != t2.ID {
			t.Fatalf("list = %v", got)
		}
		if len(s.AppTokens("usr_none")) != 0 {
			t.Fatal("unknown user must list nothing")
		}
	})

	t.Run("app_token_cap_evicts_least_recently_used", func(t *testing.T) {
		s, a, b := setup(t)
		put(t, s, tok(b.ID, -100))
		var lru ident.ID
		for i := range jam.MaxAppTokensPerUser {
			x := tok(a.ID, i)
			x.LastUsedAt = base.Add(time.Duration(100-i) * time.Minute)
			if i == 7 {
				x.LastUsedAt = base // the least recently used, though not the oldest
				lru = x.ID
			}
			put(t, s, x)
		}
		evicted, err := s.PutAppToken(tok(a.ID, 50))
		if err != nil {
			t.Fatal(err)
		}
		if len(evicted) != 1 || evicted[0] != lru {
			t.Fatalf("evicted = %v, want [%s]", evicted, lru)
		}
		if n := len(s.AppTokens(a.ID)); n != jam.MaxAppTokensPerUser {
			t.Fatalf("after cap: %d tokens", n)
		}
		if _, ok := s.AppTokenByID(lru); ok {
			t.Fatal("evicted token still present")
		}
		if len(s.AppTokens(b.ID)) != 1 {
			t.Fatal("eviction touched another user")
		}
	})

	t.Run("app_token_delete_owner_scoped", func(t *testing.T) {
		s, a, b := setup(t)
		x := tok(a.ID, 1)
		put(t, s, x)
		if ok, err := s.DeleteUserAppToken(b.ID, x.ID); err != nil || ok {
			t.Fatalf("other user's delete: ok=%v err=%v", ok, err)
		}
		if _, ok := s.AppTokenByID(x.ID); !ok {
			t.Fatal("token must stay")
		}
		if ok, err := s.DeleteUserAppToken(a.ID, x.ID); err != nil || !ok {
			t.Fatalf("own delete: ok=%v err=%v", ok, err)
		}
		if _, ok := s.AppTokenByHash(x.Hash); ok {
			t.Fatal("token still present by hash")
		}
		if ok, err := s.DeleteUserAppToken(a.ID, x.ID); err != nil || ok {
			t.Fatalf("absent delete: ok=%v err=%v", ok, err)
		}
	})

	t.Run("app_token_touch_updates_last_used_and_expiry", func(t *testing.T) {
		s, a, _ := setup(t)
		x := tok(a.ID, 1)
		put(t, s, x)
		used, exp := base.Add(time.Hour), base.Add(40*24*time.Hour)
		if err := s.TouchAppToken(x.ID, used, exp); err != nil {
			t.Fatal(err)
		}
		got, _ := s.AppTokenByHash(x.Hash)
		if !got.LastUsedAt.Equal(used) || !got.ExpiresAt.Equal(exp) || !got.CreatedAt.Equal(x.CreatedAt) {
			t.Fatalf("touch: %+v", got)
		}
		if err := s.TouchAppToken("apt_absent", used, exp); err != nil {
			t.Fatalf("absent touch: %v", err)
		}
	})

	t.Run("app_token_delete_expired_returns_ids", func(t *testing.T) {
		s, a, b := setup(t)
		old, live, oldB := tok(a.ID, 1), tok(a.ID, 2), tok(b.ID, 3)
		now := base.Add(time.Hour)
		old.ExpiresAt, live.ExpiresAt, oldB.ExpiresAt = now.Add(-time.Second), now.Add(time.Second), now
		put(t, s, old)
		put(t, s, live)
		put(t, s, oldB)
		gone, err := s.DeleteExpiredAppTokens(now)
		if err != nil {
			t.Fatal(err)
		}
		if len(gone) != 2 || !slices.Contains(gone, old.ID) || !slices.Contains(gone, oldB.ID) {
			t.Fatalf("deleted = %v", gone)
		}
		if got := ids(s.AppTokens(a.ID)); len(got) != 1 || got[0] != live.ID {
			t.Fatalf("left = %v", got)
		}
		if gone, err := s.DeleteExpiredAppTokens(now); err != nil || len(gone) != 0 {
			t.Fatalf("second sweep: %v %v", gone, err)
		}
	})

	t.Run("app_token_dropped_on_remove_user", func(t *testing.T) {
		s, a, b := setup(t)
		x, y := tok(a.ID, 1), tok(b.ID, 2)
		put(t, s, x)
		put(t, s, y)
		if err := s.RemoveUser(a.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.AppTokenByHash(x.Hash); ok || len(s.AppTokens(a.ID)) != 0 {
			t.Fatal("removed user's token remains")
		}
		if len(s.AppTokens(b.ID)) != 1 {
			t.Fatal("other user's token dropped")
		}
	})

	t.Run("app_token_dropped_on_config_user_removed", func(t *testing.T) {
		s, a, b := setup(t)
		x, y := tok(a.ID, 1), tok(b.ID, 2)
		put(t, s, x)
		put(t, s, y)
		if err := s.ApplyConfig(jam.ConfigWrite{Users: []jam.User{{ID: a.ID, Name: a.Name, Status: jam.StatusRemoved}}}); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.AppTokenByID(x.ID); ok || len(s.AppTokens(a.ID)) != 0 {
			t.Fatal("removed user's token remains")
		}
		if len(s.AppTokens(b.ID)) != 1 {
			t.Fatal("other user's token dropped")
		}
	})
}
