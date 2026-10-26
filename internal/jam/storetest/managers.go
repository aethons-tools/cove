package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// runManagerConformance exercises the ManagerStore contract.
func runManagerConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	base := time.Unix(1_000_000, 0).UTC()
	mgr := func(name, hash string, age int) jam.ManagerRecord {
		return jam.ManagerRecord{Name: name, TokenHash: hash, CreatedAt: base.Add(time.Duration(age) * time.Minute)}
	}

	t.Run("manager_add_list_order", func(t *testing.T) {
		s := newStore(t)
		for _, m := range []jam.ManagerRecord{mgr("b", "hb", 2), mgr("a", "ha", 1)} {
			if err := s.AddManager(m); err != nil {
				t.Fatalf("AddManager: %v", err)
			}
		}
		got := s.ListManagers()
		if len(got) != 2 || got[0] != mgr("a", "ha", 1) || got[1] != mgr("b", "hb", 2) {
			t.Fatalf("list = %+v", got)
		}
	})

	t.Run("manager_add_duplicate", func(t *testing.T) {
		s := newStore(t)
		if err := s.AddManager(mgr("a", "ha", 1)); err != nil {
			t.Fatal(err)
		}
		if err := s.AddManager(mgr("a", "other", 2)); !errors.Is(err, jam.ErrManagerExists) {
			t.Fatalf("duplicate = %v, want ErrManagerExists", err)
		}
		if got := s.ListManagers(); len(got) != 1 || got[0].TokenHash != "ha" {
			t.Fatalf("list = %+v", got)
		}
	})

	t.Run("manager_add_invalid_name", func(t *testing.T) {
		s := newStore(t)
		for _, name := range []string{"local", "", "Bad", "-x"} {
			if err := s.AddManager(mgr(name, "h"+name, 1)); err == nil {
				t.Errorf("AddManager(%q) must error", name)
			}
		}
		if got := s.ListManagers(); len(got) != 0 {
			t.Fatalf("stored %+v", got)
		}
	})

	t.Run("manager_lookup_by_hash", func(t *testing.T) {
		s := newStore(t)
		_ = s.AddManager(mgr("a", "ha", 1))
		if got, ok := s.ManagerByTokenHash("ha"); !ok || got != mgr("a", "ha", 1) {
			t.Fatalf("hit = %+v, %v", got, ok)
		}
		if _, ok := s.ManagerByTokenHash("nope"); ok {
			t.Fatal("unknown hash must miss")
		}
	})

	t.Run("manager_set_token", func(t *testing.T) {
		s := newStore(t)
		_ = s.AddManager(mgr("a", "ha", 1))
		if err := s.SetManagerToken("a", "ha2"); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.ManagerByTokenHash("ha"); ok {
			t.Fatal("old hash must miss")
		}
		if got, ok := s.ManagerByTokenHash("ha2"); !ok || got.Name != "a" {
			t.Fatalf("new hash = %+v, %v", got, ok)
		}
		if err := s.SetManagerToken("ghost", "x"); !errors.Is(err, jam.ErrNotFound) {
			t.Fatalf("unknown = %v, want ErrNotFound", err)
		}
	})

	t.Run("manager_remove", func(t *testing.T) {
		s := newStore(t)
		_ = s.AddManager(mgr("a", "ha", 1))
		if err := s.RemoveManager("a"); err != nil {
			t.Fatal(err)
		}
		if len(s.ListManagers()) != 0 {
			t.Fatal("still listed")
		}
		if _, ok := s.ManagerByTokenHash("ha"); ok {
			t.Fatal("hash still resolves")
		}
		if err := s.RemoveManager("a"); !errors.Is(err, jam.ErrNotFound) {
			t.Fatalf("second remove = %v, want ErrNotFound", err)
		}
	})
}
