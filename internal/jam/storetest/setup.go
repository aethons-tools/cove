package storetest

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// runSetupConformance exercises the SetupSnapshotStore contract.
func runSetupConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	at := time.Unix(1_000_000, 0).UTC()
	withInstance := func(t *testing.T) jam.Store {
		t.Helper()
		s := newStore(t)
		if err := s.PutInstance(jam.Instance{ActorID: "cove-1", Phase: jam.PhaseLive}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	snap := func(part, fp string) jam.SetupSnapshot {
		return jam.SetupSnapshot{ActorID: "cove-1", Part: part, At: at, Fingerprint: fp, Body: part + " body " + fp}
	}

	t.Run("setup_snapshot_put_list_replace", func(t *testing.T) {
		s := withInstance(t)
		for _, p := range []jam.SetupSnapshot{snap(jam.SetupPartKit, "k1"), snap(jam.SetupPartContext, "c1"), snap(jam.SetupPartContext, "c2")} {
			if err := s.PutSetupSnapshot(p); err != nil {
				t.Fatalf("Put %s: %v", p.Part, err)
			}
		}
		got := s.SetupSnapshots("cove-1")
		if len(got) != 2 || got[0] != snap(jam.SetupPartContext, "c2") || got[1] != snap(jam.SetupPartKit, "k1") {
			t.Fatalf("snapshots = %+v (want context c2, kit k1, sorted by part)", got)
		}
		if len(s.SetupSnapshots("nobody")) != 0 {
			t.Fatal("an unknown instance must list nothing")
		}
	})

	t.Run("setup_snapshot_refusals", func(t *testing.T) {
		s := withInstance(t)
		if err := s.PutSetupSnapshot(jam.SetupSnapshot{ActorID: "ghost", Part: jam.SetupPartEgress, At: at}); !errors.Is(err, jam.ErrNotFound) {
			t.Fatalf("no instance: err = %v, want ErrNotFound", err)
		}
		if err := s.PutSetupSnapshot(snap("bogus", "x")); err == nil {
			t.Fatal("an unknown part must be refused")
		}
		big := snap(jam.SetupPartContext, "c")
		big.Body = strings.Repeat("x", jam.MaxSetupBody+1)
		if err := s.PutSetupSnapshot(big); err == nil {
			t.Fatal("a body over MaxSetupBody must be refused (the caller caps it)")
		}
		if len(s.SetupSnapshots("cove-1")) != 0 {
			t.Fatal("a refused write must store nothing")
		}
	})

	t.Run("setup_snapshot_dropped_with_instance", func(t *testing.T) {
		s := withInstance(t)
		if err := s.PutSetupSnapshot(snap(jam.SetupPartEgress, "d:a.com")); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveInstance("cove-1"); err != nil {
			t.Fatal(err)
		}
		if len(s.SetupSnapshots("cove-1")) != 0 {
			t.Fatal("snapshots must go with their instance")
		}
		if err := s.PutInstance(jam.Instance{ActorID: "cove-1", Phase: jam.PhaseLive}); err != nil {
			t.Fatal(err)
		}
		if len(s.SetupSnapshots("cove-1")) != 0 {
			t.Fatal("a re-raised id must start without the old snapshots")
		}
	})

	// Regression guard: snapshots are runtime state and must never leak into the
	// config export (nothing exports them today).
	t.Run("setup_snapshot_not_exported", func(t *testing.T) {
		s := withInstance(t)
		if err := s.PutSetupSnapshot(snap(jam.SetupPartConnector, "UNIQUE-FP")); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(s.ExportConfig())
		if strings.Contains(string(b), "UNIQUE-FP") {
			t.Fatal("snapshots are runtime state: never in the config export")
		}
	})
}
