package jam

import (
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/gitsource"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

func TestKitSetStoresTreesWithTheParentRefRewrittenInPlace(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, root+"/kits/web/kit.yml", "kind: studio\n", 0o644)
	mustWrite(t, root+"/kits/web/Dockerfile", dockerfile, 0o644)
	mustWrite(t, root+"/kits/app/kit.yml", "kind: studio\n# built on web\nbase:\n  kit: ../web   # sibling\nprompt: hi\n", 0o644)
	st := NewMemStore()
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "kits/app", Ref: "main"}
	set, err := loadKitSet(root, src, "")
	if err != nil {
		t.Fatal(err)
	}
	kitMu.Lock()
	entries, err := resolveKitSet(st, set, src.Repo)
	kitMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutKitDocs(buildKitDocs(st, entries, src, "c1", time.Unix(100, 0).UTC(), false), kitSetObjects(entries)); err != nil {
		t.Fatal(err)
	}
	app, _ := LoadKitVersion(st, "app", 0)
	if string(app.KitYML) != "kind: studio\n# built on web\nbase:\n  kit: web@v1   # sibling\nprompt: hi\n" {
		t.Fatalf("app kit.yml = %q", app.KitYML)
	}
	web, _ := LoadKitVersion(st, "web", 0)
	if _, ok := web.Tree.Lookup("Dockerfile"); !ok || web.Kit.BuildContext == "" {
		t.Fatalf("web tree = %+v", web.Tree)
	}
	kitMu.Lock()
	again, err := resolveKitSet(st, set, src.Repo)
	kitMu.Unlock()
	if err != nil || !again[0].Unchanged || !again[1].Unchanged {
		t.Fatalf("re-import: %+v %v", again, err)
	}
	if strings.Contains(string(app.KitYML), "../web") {
		t.Fatal("the stored kit.yml still has the path ref")
	}
}

// TestKitPushThenIdenticalImportIsUnchanged: a kit directory pushed with
// `kit push`, then imported from a checkout holding the same files (a file:
// note included), is unchanged — the tree hashes match — and a change to its
// Dockerfile alone is a new version.
func TestKitPushThenIdenticalImportIsUnchanged(t *testing.T) {
	const yml = "kind: studio\nnotes:\n  - {name: r.md, read-when: x, file: docs/r.md}\n"
	st := NewMemStore()
	if _, _, err := PushKitFiles(st, "app", kittreetest.Files("kit.yml", yml, "Dockerfile", dockerfile, "docs/r.md", "# R\n", "run.sh*", "echo\n")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mustWrite(t, root+"/app/kit.yml", yml, 0o644)
	mustWrite(t, root+"/app/Dockerfile", dockerfile, 0o644)
	mustWrite(t, root+"/app/docs/r.md", "# R\n", 0o644)
	mustWrite(t, root+"/app/run.sh", "echo\n", 0o755)
	src := gitsource.Source{Repo: "https://github.com/acme/kits", Dir: "app", Ref: "main"}
	resolve := func() []kitSetEntry {
		t.Helper()
		set, err := loadKitSet(root, src, "app")
		if err != nil {
			t.Fatal(err)
		}
		kitMu.Lock()
		defer kitMu.Unlock()
		entries, err := resolveKitSet(st, set, src.Repo)
		if err != nil {
			t.Fatal(err)
		}
		return entries
	}
	if e := resolve(); !e[0].Unchanged || e[0].Version != 1 {
		t.Fatalf("identical import: %+v", e[0])
	}
	mustWrite(t, root+"/app/Dockerfile", dockerfile+"RUN true\n", 0o644)
	if e := resolve(); e[0].Unchanged || e[0].Version != 2 {
		t.Fatalf("Dockerfile change: %+v", e[0])
	}
}
