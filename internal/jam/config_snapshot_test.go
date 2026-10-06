package jam

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// populated returns a MemStore with one entry in every config aggregate AND in
// the excluded state (an instance + an unread cursor), so a test can assert the
// export includes config and excludes state.
func populated(t *testing.T) *MemStore {
	t.Helper()
	s := NewMemStore()
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
	if err := s.CommitChannelRead("usr_01j9q3aaaaaaaaaaaaaaaaaaaa", "chn_01j9q3aaaaaaaaaaaaaaaaaaaa", 7); err != nil {
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

	dst := NewMemStore()
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

	cases := map[string]func(*MemStore){
		"actors":       func(s *MemStore) { _ = s.AddActor(Actor{ID: "x", TokenHash: "hx"}) },
		"roles":        func(s *MemStore) { _ = s.PutRole(DefaultProject, Role{Name: "r"}) },
		"kits":         func(s *MemStore) { _, _ = s.PushKit("k", "cfg") },
		"destinations": func(s *MemStore) { _ = s.AddDestination(Destination{Name: "d"}) },
		"projects":     func(s *MemStore) { _ = s.AddHuman(DefaultProject, Human{Name: "h"}) },
		"jam_context":  func(s *MemStore) { _ = s.SetJamContext(sessionctx.Layer{Core: "J"}) },
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			dst := NewMemStore()
			seed(dst)
			err := dst.ImportConfig(snap)
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
	dst := NewMemStore()
	if err := dst.ImportConfig(ConfigSnapshot{Version: 999}); !errors.Is(err, ErrUnsupportedConfigVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedConfigVersion", err)
	}
}

func TestConfigSnapshotCarriesContext(t *testing.T) {
	src := NewMemStore()
	if err := src.PutRole("default", Role{Name: "dev", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if err := SetRoleContext(src, "default", "dev", sessionctx.Layer{Core: "ROLE"}); err != nil {
		t.Fatal(err)
	}
	if err := src.SetProjectContext("default", sessionctx.Layer{Core: "PROJECT"}, []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "https://x"}}); err != nil {
		t.Fatal(err)
	}
	if err := src.SetJamContext(sessionctx.Layer{Core: "JAM"}); err != nil {
		t.Fatal(err)
	}
	snap := src.ExportConfig()
	dst := NewMemStore()
	if err := dst.ImportConfig(snap); err != nil {
		t.Fatal(err)
	}
	r, _ := dst.GetRole("default", "dev")
	p, _ := dst.GetProject("default")
	if r.Context.Core != "ROLE" || p.Context.Core != "PROJECT" || len(p.Resources) != 1 || dst.GetJamContext().Core != "JAM" {
		t.Fatalf("round trip lost context: role=%q project=%q res=%d jam=%q", r.Context.Core, p.Context.Core, len(p.Resources), dst.GetJamContext().Core)
	}
	if err := NewMemStore().ImportConfig(ConfigSnapshot{Version: ConfigSnapshotVersion, JamContext: &sessionctx.Layer{Core: "J"}}); err != nil {
		t.Fatalf("a jam-context-only snapshot imports: %v", err)
	}
}

// Import applies the same authoring rules as the admin API, so a hand-edited
// backup can't store context sessions would receive truncated or broken.
func TestImportConfigValidatesAuthoredContext(t *testing.T) {
	good := func() ConfigSnapshot {
		return ConfigSnapshot{Version: ConfigSnapshotVersion, Projects: []Project{{Name: DefaultProject}}}
	}
	for name, mut := range map[string]func(*ConfigSnapshot){
		"jam over budget": func(s *ConfigSnapshot) {
			s.JamContext = &sessionctx.Layer{Core: strings.Repeat("x", sessionctx.BudgetJam+1)}
		},
		"role bad leaf": func(s *ConfigSnapshot) {
			s.Roles = map[string]map[string]Role{DefaultProject: {"r": {Name: "r", Context: sessionctx.Layer{Leaves: []sessionctx.Leaf{{Name: "../x.md", ReadWhen: "w"}}}}}}
		},
		"project bad resource": func(s *ConfigSnapshot) {
			s.Projects[0].Resources = []sessionctx.Resource{{Name: "r", Kind: "wiki", Ref: "x"}}
		},
		"project reserved leaf": func(s *ConfigSnapshot) {
			s.Projects[0].Context = sessionctx.Layer{Core: "C", Leaves: []sessionctx.Leaf{{Name: sessionctx.ResourcesLeaf, ReadWhen: "w"}}}
			s.Projects[0].Resources = []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "x"}}
		},
		"project core plus pointer over budget": func(s *ConfigSnapshot) {
			s.Projects[0].Context = sessionctx.Layer{Core: strings.Repeat("x", sessionctx.BudgetProject-5)}
			s.Projects[0].Resources = []sessionctx.Resource{{Name: "r", Kind: "url", Ref: "x"}}
		},
		"long destination note": func(s *ConfigSnapshot) {
			s.Destinations = []Destination{{Name: "d", Route: "/d/", Upstream: "https://d", Note: strings.Repeat("n", MaxDestinationNote+1)}}
		},
		"destination custom without spec": func(s *ConfigSnapshot) {
			s.Destinations = []Destination{{Name: "d", Route: "/d/", Upstream: "https://d", Apply: ApplyCustom}}
		},
	} {
		s := good()
		mut(&s)
		if err := NewMemStore().ImportConfig(s); err == nil || !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: err = %v, want ErrInvalidConfig", name, err)
		}
	}
	if err := NewMemStore().ImportConfig(good()); err != nil {
		t.Fatalf("a valid snapshot must import: %v", err)
	}
}

// Backups carry destinations in both shapes: preset strings (pre-spec) and
// custom header specs; both survive export → JSON → import.
func TestConfigSnapshotDestinationHeaderSpecsRoundTrip(t *testing.T) {
	src := NewMemStore()
	for _, d := range []Destination{
		{Name: "git", Route: "/git/", Upstream: "https://github.com", IdentityIn: ApplyBasicPassword, Apply: ApplyBasicPassword},
		{Name: "gl", Route: "/gl/", Upstream: "https://gl", IdentityIn: ApplyCustom, IdentityInSpec: &InboundSpec{Header: "Private-Token"},
			Apply: ApplyCustom, ApplySpec: &OutboundSpec{Header: "Private-Token", Template: "{cred}"}},
	} {
		if err := src.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	b, err := json.Marshal(src.ExportConfig())
	if err != nil {
		t.Fatal(err)
	}
	var snap ConfigSnapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	dst := NewMemStore()
	if err := dst.ImportConfig(snap); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dst.ListDestinations(), src.ListDestinations()) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", dst.ListDestinations(), src.ListDestinations())
	}
}
