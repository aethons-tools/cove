package storetest

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/legacynames"
)

// runRenameMigrationConformance: the jam-rename kit migration leaves both
// stores in the same state, and a second run changes nothing.
func runRenameMigrationConformance(t *testing.T, newStore func(t *testing.T) jam.Store) {
	t.Run("rename_migrations", func(t *testing.T) {
		s := newStore(t)
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
			if _, err := jam.MigrateKitRename(s, nil, nil); err != nil {
				t.Fatal(err)
			}
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
