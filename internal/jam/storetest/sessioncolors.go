package storetest

import (
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

// runSessionColorConformance exercises the SessionColorStore contract.
func runSessionColorConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	t.Run("session_color_absent", func(t *testing.T) {
		s := newStore(t)
		if c, ok := s.SessionColor("ses_x"); ok || c != "" {
			t.Fatalf("SessionColor = %q, %v; want none", c, ok)
		}
	})
	t.Run("session_color_put_get", func(t *testing.T) {
		s := newStore(t)
		got, err := s.PutSessionColor("ses_x", "#aa3f4e")
		if err != nil || got != "#aa3f4e" {
			t.Fatalf("PutSessionColor = %q, %v", got, err)
		}
		if c, ok := s.SessionColor("ses_x"); !ok || c != "#aa3f4e" {
			t.Fatalf("SessionColor = %q, %v", c, ok)
		}
	})
	t.Run("session_color_first_wins", func(t *testing.T) {
		s := newStore(t)
		if _, err := s.PutSessionColor("ses_x", "#aa3f4e"); err != nil {
			t.Fatal(err)
		}
		got, err := s.PutSessionColor("ses_x", "#ab4235")
		if err != nil || got != "#aa3f4e" {
			t.Fatalf("second PutSessionColor = %q, %v; want the first", got, err)
		}
		if c, _ := s.SessionColor("ses_x"); c != "#aa3f4e" {
			t.Fatalf("SessionColor = %q", c)
		}
	})
	t.Run("session_color_survives_instance_removal", func(t *testing.T) {
		s := newStore(t)
		if err := s.PutInstance(jam.Instance{ActorID: "ses_x"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutSessionColor("ses_x", "#aa3f4e"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveInstance("ses_x"); err != nil {
			t.Fatal(err)
		}
		if c, ok := s.SessionColor("ses_x"); !ok || c != "#aa3f4e" {
			t.Fatalf("after RemoveInstance: %q, %v", c, ok)
		}
	})
}
