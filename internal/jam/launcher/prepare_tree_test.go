package launcher

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/kittree"
	"github.com/aethons-tools/cove/internal/kittree/kittreetest"
	"github.com/aethons-tools/cove/internal/studio"
)

// treeLink is chain link name@vN read from a tree of path → content files.
func treeLink(t *testing.T, name string, version int, kv ...string) jam.KitLink {
	t.Helper()
	tr, blobs := kittreetest.Tree(t, kv...)
	sk, err := studio.ParseTree(tr, kittree.MapBlobs(blobs))
	if err != nil {
		t.Fatal(err)
	}
	return jam.KitLink{Ref: KitRef{ID: name, Version: version, Digest: studio.BuildDigest(sk, harnessinstall.Default())}, Kit: sk, Files: kittree.NewFS(tr, kittree.MapBlobs(blobs))}
}

func tarNames(t *testing.T, b []byte) string {
	t.Helper()
	var names []string
	r := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return strings.Join(names, ",")
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

const df = "ARG JAM_STUDIO_BASE_IMAGE\nFROM ${JAM_STUDIO_BASE_IMAGE}\n"

func TestTreeKitStreamsItsDirectoryMinusKitYML(t *testing.T) {
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	link := treeLink(t, "app", 1, "kit.yml", "kind: studio\n", "Dockerfile", df, "bin/run.sh", "echo")
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	if _, err := l.PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if got := tarNames(t, ops.resolvedTar); got != "Dockerfile,bin/run.sh" {
		t.Fatalf("context tar = %s", got)
	}
}

func TestTreeKitWithoutFilesFailsClosed(t *testing.T) {
	l := newPrepareLauncher(&fakeOps{}, &fakeInv{}, noAsm)
	link := treeLink(t, "app", 1, "kit.yml", "kind: studio\n", "Dockerfile", df)
	link.Files = nil
	def := KitDefinition{Ref: link.Ref, Kit: link.Kit, Chain: []jam.KitLink{link}, Harness: harnessinstall.Default()}
	if _, err := l.PrepareKit(context.Background(), def); err == nil || !strings.Contains(err.Error(), "files are not loaded") {
		t.Fatalf("err = %v", err)
	}
}

func TestChainTreeStageBuildsOnItsParent(t *testing.T) {
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	root := treeLink(t, "web", 1, "kit.yml", "kind: studio\n")
	child := treeLink(t, "app", 2, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df+"RUN true\n")
	if _, err := l.PrepareKit(context.Background(), chainDef(nil, root, child)); err != nil {
		t.Fatal(err)
	}
	if len(ops.stages) != 1 || ops.stages[0].parent != "blessed-default" || tarNames(t, ops.stages[0].tar) != "Dockerfile" {
		t.Fatalf("stages = %+v", ops.stages)
	}
}

func TestStageTagKeyedOnBuildContext(t *testing.T) {
	a := treeLink(t, "app", 1, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df)
	b := treeLink(t, "app", 2, "kit.yml", "kind: studio\nbase: {kit: web}\nprompt: edited\n", "Dockerfile", df)
	c := treeLink(t, "app", 3, "kit.yml", "kind: studio\nbase: {kit: web}\n", "Dockerfile", df+"RUN x\n")
	ta, _ := kitStageTag("asm", "p", a.Kit)
	tb, _ := kitStageTag("asm", "p", b.Kit)
	tc, _ := kitStageTag("asm", "p", c.Kit)
	if ta != tb {
		t.Fatal("a kit.yml-only edit must reuse the stage")
	}
	if ta == tc {
		t.Fatal("a Dockerfile edit must make a new stage")
	}
}
