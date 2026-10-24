package jam

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
)

// pushTree pushes path → content files (a "*" suffix makes 0755) as kit name's next version.
func pushTree(t *testing.T, st Store, name string, kv ...string) kittree.Tree {
	t.Helper()
	tr, blobs := kittreetest.Tree(t, kv...)
	if _, err := st.PushKitVersion(name, tr.Hash(), KitObjects{Trees: []kittree.Tree{tr}, Blobs: blobs}, KitVersionMeta{PushedAt: time.Unix(1, 0).UTC()}, nil); err != nil {
		t.Fatal(err)
	}
	return tr
}

const dockerfile = "ARG COVE_BASE_IMAGE\nFROM ${COVE_BASE_IMAGE}\n"

func TestLoadKitVersionTree(t *testing.T) {
	st := NewMemStore()
	tr := pushTree(t, st, "app",
		"kit.yml", "kind: studio\nnotes:\n  - {name: r.md, read-when: releasing, file: docs/r.md}\n",
		"docs/r.md", "# R\n", "Dockerfile", dockerfile, "run.sh*", "echo\n")
	v, ok := LoadKitVersion(st, "app", 0)
	if !ok || v.Err != nil {
		t.Fatalf("load: %v %v", ok, v.Err)
	}
	if v.Version != 1 || v.TreeHash != tr.Hash() || v.Kit.Notes[0].Body != "# R\n" || v.Kit.BuildContext != tr.ContextHash() {
		t.Fatalf("version = %+v", v)
	}
	if !strings.HasPrefix(string(v.KitYML), "kind: studio\n") || v.Key() != "tree:"+tr.Hash() {
		t.Fatalf("kit.yml %q key %q", v.KitYML, v.Key())
	}
	f, err := v.Files.Open("run.sh")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	if string(b) != "echo\n" {
		t.Fatalf("run.sh = %q", b)
	}
	if fsys, ok := KitFS(st, tr.Hash()); !ok || fsys == nil {
		t.Fatal("KitFS of a stored tree")
	}
	if _, ok := KitFS(st, strings.Repeat("0", 64)); ok {
		t.Fatal("KitFS of an unknown tree")
	}
}

func TestLoadKitVersionTextAndBroken(t *testing.T) {
	st := NewMemStore()
	doc := Kit{Name: "old", Current: 1, Versions: map[int]string{1: `{"kind":"studio","prompt":"p"}`}, Broken: map[int]string{2: "its base.context is not a readable tar.gz"}}
	if err := st.PutKitDocs([]Kit{doc}, KitObjects{}); err != nil {
		t.Fatal(err)
	}
	v, ok := LoadKitVersion(st, "old", 1)
	if !ok || v.Err != nil || v.Kit.Prompt != "p" || v.Files != nil || !strings.HasPrefix(v.Key(), "text:") {
		t.Fatalf("text version = %+v", v)
	}
	b, ok := LoadKitVersion(st, "old", 2)
	if !ok || b.Err == nil || b.Err.Error() != "kit old@v2 could not be migrated to a tree: its base.context is not a readable tar.gz" {
		t.Fatalf("broken version = %+v", b)
	}
	if _, ok := LoadKitVersion(st, "old", 3); ok {
		t.Fatal("a missing version must be ok=false")
	}
}

func TestLoadKitVersionReturnsCopies(t *testing.T) {
	st := NewMemStore()
	pushTree(t, st, "app", "kit.yml", "kind: studio\negress: [a.com]\n")
	v, _ := LoadKitVersion(st, "app", 0)
	v.Kit.Egress[0] = "mutated"
	if again, _ := LoadKitVersion(st, "app", 0); again.Kit.Egress[0] != "a.com" {
		t.Fatal("the parse cache handed out a shared slice")
	}
}

func TestResolvedChainCarriesFiles(t *testing.T) {
	st := NewMemStore()
	parent := pushTree(t, st, "web", "kit.yml", "kind: studio\n", "Dockerfile", dockerfile)
	pushTree(t, st, "app", "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", dockerfile+"RUN true\n")
	def, err := StudioKitDefinition(st, "app", harnessinstall.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Chain) != 2 || def.Chain[0].Files == nil || def.Chain[1].Files == nil {
		t.Fatalf("chain = %+v", def.Chain)
	}
	if def.Chain[0].Kit.BuildContext != parent.ContextHash() || def.Chain[1].Kit.BuildContext == "" {
		t.Fatalf("build contexts %q %q", def.Chain[0].Kit.BuildContext, def.Chain[1].Kit.BuildContext)
	}
	if _, err := def.Chain[1].Files.Stat("Dockerfile"); err != nil {
		t.Fatal(err)
	}
}
