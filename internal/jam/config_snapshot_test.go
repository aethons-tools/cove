package jam

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// populated returns a FileStore with one entry in every config aggregate AND in
// the excluded state (an instance + an unread cursor), so a test can assert the
// export includes config and excludes state.
func populated(t *testing.T) *FileStore {
	t.Helper()
	s, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRole(DefaultProject, Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddActor(Actor{ID: "spider-18", TokenHash: HashToken("tok"), Grants: []Grant{{Project: DefaultProject, Role: "guest"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PushKit("base", "image: x"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDestination(Destination{Name: "anthropic", Route: "/v1", Upstream: "https://api"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddHuman(DefaultProject, Human{Name: "alice", Handle: "@alice"}); err != nil {
		t.Fatal(err)
	}
	// excluded state:
	if err := s.PutInstance(Instance{ActorID: "spider-18"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CommitUnread("alice", "eng", 7); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExportConfigIncludesConfigExcludesState(t *testing.T) {
	s := populated(t)
	snap := s.ExportConfig()

	if snap.Version != ConfigSnapshotVersion {
		t.Fatalf("Version = %d, want %d", snap.Version, ConfigSnapshotVersion)
	}
	if len(snap.Actors) != 1 || snap.Actors[0].ID != "spider-18" {
		t.Fatalf("actors = %+v", snap.Actors)
	}
	if snap.Actors[0].TokenHash != HashToken("tok") {
		t.Fatalf("token hash not exported: %q", snap.Actors[0].TokenHash)
	}
	if _, ok := snap.Roles[DefaultProject]["guest"]; !ok {
		t.Fatalf("roles = %+v", snap.Roles)
	}
	if len(snap.Kits) != 1 || snap.Kits[0].Versions[1] != "image: x" {
		t.Fatalf("kits = %+v", snap.Kits)
	}
	if len(snap.Destinations) != 1 || len(snap.Projects) != 1 {
		t.Fatalf("dests=%+v projects=%+v", snap.Destinations, snap.Projects)
	}
	// ExportConfig has no field for instances or unread cursors — their absence
	// from the type is the exclusion guarantee; this test documents it.
}

func TestExportConfigIsDeepCopied(t *testing.T) {
	s := populated(t)
	snap := s.ExportConfig()
	// Mutating the snapshot must not reach the store.
	snap.Roles[DefaultProject]["guest"] = Role{Name: "hacked"}
	snap.Kits[0].Versions[1] = "tampered"
	if r, _ := s.GetRole(DefaultProject, "guest"); r.Name != "guest" {
		t.Fatalf("store role mutated via snapshot: %+v", r)
	}
	if cfg, _ := s.KitConfig("base", 1); cfg != "image: x" {
		t.Fatalf("store kit mutated via snapshot: %q", cfg)
	}
}

func TestImportConfigRoundTripFidelity(t *testing.T) {
	src := populated(t)
	snap := src.ExportConfig()

	dst, err := NewFileStore(filepath.Join(t.TempDir(), "dst.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.ImportConfig(snap); err != nil {
		t.Fatalf("ImportConfig: %v", err)
	}

	// Re-export and compare: config round-trips exactly.
	got := dst.ExportConfig()
	got.ExportedAt = snap.ExportedAt // timestamps differ; not part of fidelity
	if a, b := mustJSON(t, got), mustJSON(t, snap); !bytes.Equal(a, b) {
		t.Fatalf("round-trip mismatch:\n got=%s\nwant=%s", a, b)
	}

	// The restored token hash still resolves the actor.
	if a, ok := dst.Lookup(HashToken("tok")); !ok || a.ID != "spider-18" {
		t.Fatalf("Lookup after import = %+v, ok=%v", a, ok)
	}
}

func TestImportConfigRefusesWhenNotEmpty(t *testing.T) {
	snap := populated(t).ExportConfig()

	cases := map[string]func(*FileStore){
		"actors":       func(s *FileStore) { _ = s.AddActor(Actor{ID: "x", TokenHash: "hx"}) },
		"roles":        func(s *FileStore) { _ = s.PutRole(DefaultProject, Role{Name: "r"}) },
		"kits":         func(s *FileStore) { _, _ = s.PushKit("k", "cfg") },
		"destinations": func(s *FileStore) { _ = s.AddDestination(Destination{Name: "d"}) },
		"projects":     func(s *FileStore) { _ = s.AddHuman(DefaultProject, Human{Name: "h"}) },
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			dst, err := NewFileStore(filepath.Join(t.TempDir(), "d.json"))
			if err != nil {
				t.Fatal(err)
			}
			seed(dst)
			err = dst.ImportConfig(snap)
			if !errors.Is(err, ErrConfigNotEmpty) {
				t.Fatalf("err = %v, want ErrConfigNotEmpty", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error should name %q: %v", name, err)
			}
		})
	}
}

func TestImportConfigRejectsBadVersion(t *testing.T) {
	dst, err := NewFileStore(filepath.Join(t.TempDir(), "d.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.ImportConfig(ConfigSnapshot{Version: 999}); !errors.Is(err, ErrUnsupportedConfigVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedConfigVersion", err)
	}
}
