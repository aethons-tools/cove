package storetest

import (
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

// runRailOrderConformance exercises the RailOrderStore contract.
func runRailOrderConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	const u, v = ident.ID("usr_01j9q3aaaaaaaaaaaaaaaaaaaa"), ident.ID("usr_01j9q3bbbbbbbbbbbbbbbbbbbb")
	t.Run("rail_order_absent", func(t *testing.T) {
		s := newStore(t)
		if got := s.RailOrder(u, jam.RailMe); got != nil {
			t.Fatalf("RailOrder = %q, want none", got)
		}
	})
	t.Run("rail_order_put_get_replace", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRailOrder(u, jam.RailMe, []string{"chn_b", "chn_a"}); err != nil {
			t.Fatal(err)
		}
		if got := s.RailOrder(u, jam.RailMe); !slices.Equal(got, []string{"chn_b", "chn_a"}) {
			t.Fatalf("RailOrder = %q", got)
		}
		if err := s.PutRailOrder(u, jam.RailMe, []string{"chn_c"}); err != nil {
			t.Fatal(err)
		}
		if got := s.RailOrder(u, jam.RailMe); !slices.Equal(got, []string{"chn_c"}) {
			t.Fatalf("replaced RailOrder = %q", got)
		}
		if err := s.PutRailOrder(u, jam.RailMe, nil); err != nil {
			t.Fatal(err)
		}
		if got := s.RailOrder(u, jam.RailMe); len(got) != 0 {
			t.Fatalf("emptied RailOrder = %q", got)
		}
	})
	t.Run("rail_order_per_user_and_section", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRailOrder(u, jam.RailMe, []string{"chn_a"}); err != nil {
			t.Fatal(err)
		}
		if err := s.PutRailOrder(u, jam.RailAgents, []string{"ses_a"}); err != nil {
			t.Fatal(err)
		}
		if got := s.RailOrder(v, jam.RailMe); got != nil {
			t.Fatalf("another user's RailOrder = %q", got)
		}
		if got := s.RailOrder(u, jam.RailAgents); !slices.Equal(got, []string{"ses_a"}) {
			t.Fatalf("agents RailOrder = %q", got)
		}
		if got := s.RailOrder(u, jam.RailMe); !slices.Equal(got, []string{"chn_a"}) {
			t.Fatalf("me RailOrder = %q", got)
		}
	})
	t.Run("rail_order_unknown_section", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutRailOrder(u, "work", []string{"x"}); err == nil {
			t.Fatal("PutRailOrder(work) succeeded; only me and agents have an order")
		}
		if got := s.RailOrder(u, "work"); got != nil {
			t.Fatalf("RailOrder(work) = %q", got)
		}
	})
	t.Run("rail_order_is_a_copy", func(t *testing.T) {
		s := newStore(t)
		ids := []string{"chn_a", "chn_b"}
		if err := s.PutRailOrder(u, jam.RailMe, ids); err != nil {
			t.Fatal(err)
		}
		ids[0] = "mutated"
		got := s.RailOrder(u, jam.RailMe)
		got[1] = "mutated"
		if again := s.RailOrder(u, jam.RailMe); !slices.Equal(again, []string{"chn_a", "chn_b"}) {
			t.Fatalf("RailOrder shares its slices: %q", again)
		}
	})
}
