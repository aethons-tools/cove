package storetest

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/legacynames"
)

type recordRenamer struct {
	retired []string
	also    []string
}

func (r *recordRenamer) RetireLegacyContainers(context.Context, []string) ([]string, error) {
	return nil, nil
}
func (r *recordRenamer) RetireLegacyStudio(_ context.Context, id string) error {
	r.retired = append(r.retired, id)
	return nil
}
func (r *recordRenamer) MigrateStateVolumes(_ context.Context, also []string) ([]string, error) {
	r.also = also
	return nil, nil
}
func (r *recordRenamer) StudioLocation(id string) string { return "jam-studio-" + id }

// runRenameMigrationConformance: the jam-rename migrations leave both stores
// in the same state, and a second run changes nothing.
func runRenameMigrationConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	t.Run("rename_migrations", func(t *testing.T) {
		s := newStore(t)
		if err := s.CreateProject("acme"); err != nil {
			t.Fatal(err)
		}
		pid, ok := jam.ProjectIDOf(s, "acme")
		if !ok {
			t.Fatal("no project acme")
		}
		if err := s.PutStandingSession(pid, "r", "idle", "s2"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutInstance(jam.Instance{ActorID: "s1", SessionKind: jam.SessionKindStanding, Phase: jam.PhaseLive,
			Location: legacynames.Container("s1"), ImageTag: "old", KitDraftImage: "old-draft"}); err != nil {
			t.Fatal(err)
		}
		df := "ARG " + legacynames.KitRenames[2].Old + "\nFROM ${" + legacynames.KitRenames[2].Old + "}\n"
		push := func(name string, files ...kittree.File) {
			tree, blobs, err := kittree.BuildFiles(files)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.PushKitVersion(name, tree.Hash(), jam.KitObjects{Trees: []kittree.Tree{tree}, Blobs: blobs}, jam.KitVersionMeta{PushedAt: time.Now()}, nil); err != nil {
				t.Fatal(err)
			}
		}
		push("base", kittree.File{Path: "kit.yml", Mode: 0o644, Data: []byte("kind: studio\n")},
			kittree.File{Path: "Dockerfile", Mode: 0o644, Data: []byte(df)})
		push("child", kittree.File{Path: "kit.yml", Mode: 0o644, Data: []byte("kind: studio\nbase:\n  kit: base@v1\n")})
		for run := 0; run < 2; run++ {
			r := &recordRenamer{}
			if _, err := jam.MigrateStudioNames(context.Background(), s, r, nil); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(r.also, []string{"s1", "s2"}) {
				t.Fatalf("run %d: standing ids = %v", run, r.also)
			}
			if want := []string{"s1"}; run == 0 && !slices.Equal(r.retired, want) || run == 1 && len(r.retired) != 0 {
				t.Fatalf("run %d: retired = %v", run, r.retired)
			}
			if _, err := jam.MigrateKitRename(s, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		inst, _ := s.GetInstance("s1")
		if inst.Location != "jam-studio-s1" || inst.ImageTag != "" || inst.KitDraftImage != "" || inst.Phase != jam.PhaseLost {
			t.Fatalf("instance = %+v", inst)
		}
		k, _ := s.GetKit("base")
		if k.Current != 2 || k.Meta[2].Origin != jam.KitOriginMigration {
			t.Fatalf("kit current v%d meta %+v (exactly one migration version)", k.Current, k.Meta[k.Current])
		}
		c, _ := s.GetKit("child")
		cur, _ := jam.LoadKitVersion(s, "child", 0)
		if c.Current != 2 || c.Meta[2].Origin != jam.KitOriginMigration || !strings.Contains(string(cur.KitYML), "kit: base@v2") {
			t.Fatalf("child current v%d meta %+v kit.yml %q", c.Current, c.Meta[c.Current], cur.KitYML)
		}
	})
}
