package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// ctxBase is an own-context base (inline tree) whose Dockerfile builds FROM the
// injected parent; marker keeps sibling contexts distinct.
func ctxBase(parent, marker string) studio.Base {
	df := "FROM ${COVE_BASE_IMAGE}\nRUN echo " + marker + "\n"
	return studio.Base{Kit: parent, ContextFiles: studio.ContextTree{"dockerfile": {File: &df}}}
}

// chainLink is one resolved chain link for kit name@vN with the given own base.
func chainLink(name string, version int, base studio.Base) jam.KitLink {
	sk := studio.StudioKit{Kind: studio.Kind, Base: base}
	return jam.KitLink{Ref: KitRef{ID: name, Version: version, Digest: studio.BuildDigest(sk, harnessinstall.Default())}, Kit: sk}
}

// chainDef is a definition whose Chain is links (root first, self last) and
// whose merged Kit carries buildArgs. Its Ref digest is derived from every
// link's digest so distinct chains get distinct image tags.
func chainDef(buildArgs map[string]string, links ...jam.KitLink) KitDefinition {
	self := links[len(links)-1]
	var ds []string
	for _, l := range links {
		ds = append(ds, l.Ref.Digest)
	}
	sum := sha256.Sum256([]byte(strings.Join(ds, "/")))
	return KitDefinition{
		Ref:     KitRef{ID: self.Ref.ID, Version: self.Ref.Version, Digest: hex.EncodeToString(sum[:])},
		Kit:     studio.StudioKit{Kind: studio.Kind, Base: self.Kit.Base, BuildArgs: buildArgs},
		Chain:   links,
		Harness: harnessinstall.Default(),
	}
}

func mustStage(t *testing.T, parentRef string, b studio.Base) string {
	t.Helper()
	tag, err := kitStageTag(newPrepareLauncher(&fakeOps{}, &fakeInv{}, noAsm).asm, parentRef, studio.StudioKit{Base: b})
	if err != nil {
		t.Fatal(err)
	}
	return tag
}

func noAsm(KitDefinition, string) error { return nil }

// The stage tag is cove-kit-base:<first 32 hex of sha256(asm, NUL, parentRef, NUL,
// canonical JSON of the link's own Base)>.
func TestKitStageTagFormula(t *testing.T) {
	b := ctxBase("web-dev", "x")
	js, _ := json.Marshal(b)
	sum := sha256.Sum256(append(append(append(append([]byte("asm"), 0), []byte("cove-kit-base:p")...), 0), js...))
	want := "cove-kit-base:" + hex.EncodeToString(sum[:])[:32]
	if got, _ := kitStageTag("asm", "cove-kit-base:p", studio.StudioKit{Base: b}); got != want {
		t.Fatalf("stage tag = %q, want %q", got, want)
	}
	if q, _ := kitStageTag("asm", "cove-kit-base:q", studio.StudioKit{Base: b}); q == want {
		t.Fatal("a different parent stage must give a different stage tag")
	}
	if y, _ := kitStageTag("asm", "cove-kit-base:p", studio.StudioKit{Base: ctxBase("web-dev", "y")}); y == want {
		t.Fatal("a different own base must give a different stage tag")
	}
}

// root (blessed default) → mid (own context) → leaf (own context): each stage is
// built on the previous ref, in order, and the one BuildKitImage runs FROM the
// leaf stage with the merged build args.
func TestChainBuildsStagesRootToLeaf(t *testing.T) {
	ops := &fakeOps{}
	root := chainLink("base-dev", 1, studio.Base{})
	mid := chainLink("web-dev", 2, ctxBase("base-dev", "mid"))
	leaf := chainLink("my-app", 0, ctxBase("web-dev", "leaf"))
	def := chainDef(map[string]string{"A": "1"}, root, mid, leaf)
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)

	if st, err := l.PrepareKit(context.Background(), def); err != nil || st.State != KitReady {
		t.Fatalf("PrepareKit = %+v, %v", st, err)
	}
	midStage := mustStage(t, "blessed-default", mid.Kit.Base)
	leafStage := mustStage(t, midStage, leaf.Kit.Base)
	if len(ops.stages) != 2 ||
		ops.stages[0].parent != "blessed-default" || ops.stages[0].stage != midStage ||
		ops.stages[1].parent != midStage || ops.stages[1].stage != leafStage {
		t.Fatalf("stages = %+v; want mid on blessed-default, leaf on mid", ops.stages)
	}
	if !strings.Contains(untarNames(t, ops.stages[0].tar)["Dockerfile"], "echo mid") ||
		!strings.Contains(untarNames(t, ops.stages[1].tar)["Dockerfile"], "echo leaf") {
		t.Fatal("each stage must stream its own link's context")
	}
	if ops.builds != 1 || ops.builtBase != leafStage || ops.builtArgs["A"] != "1" {
		t.Fatalf("BuildKitImage: builds=%d base=%q args=%v; want once FROM %s with merged args", ops.builds, ops.builtBase, ops.builtArgs, leafStage)
	}
}

// A root with its own context resolves by today's rule (ResolveKitBaseTar on
// the blessed base), and the child stage builds on what that returned.
func TestChainRootContextThenChildStage(t *testing.T) {
	ops := &fakeOps{resolvedTarBase: "cove-kit-base:rootid"}
	root := chainLink("base-dev", 1, ctxBase("", "root"))
	leaf := chainLink("my-app", 0, ctxBase("base-dev", "leaf"))
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	if _, err := l.PrepareKit(context.Background(), chainDef(nil, root, leaf)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(untarNames(t, ops.resolvedTar)["Dockerfile"], "echo root") {
		t.Fatal("the root's own context must go through ResolveKitBaseTar")
	}
	want := mustStage(t, "cove-kit-base:rootid", leaf.Kit.Base)
	if len(ops.stages) != 1 || ops.stages[0].parent != "cove-kit-base:rootid" || ops.builtBase != want {
		t.Fatalf("stages=%+v built=%q; want the leaf on the root's base", ops.stages, ops.builtBase)
	}
}

// Two children of one parent: the second child's prepare finds the parent's
// stage present (HasKitBase) and does not rebuild it — only its own stage builds.
func TestSiblingsShareParentStage(t *testing.T) {
	ops := &fakeOps{}
	root := chainLink("base-dev", 1, studio.Base{})
	mid := chainLink("web-dev", 1, ctxBase("base-dev", "mid"))
	a := chainLink("app-a", 0, ctxBase("web-dev", "a"))
	b := chainLink("app-b", 0, ctxBase("web-dev", "b"))
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	if _, err := l.PrepareKit(context.Background(), chainDef(nil, root, mid, a)); err != nil {
		t.Fatal(err)
	}
	midStage := mustStage(t, "blessed-default", mid.Kit.Base)
	ops.hasBaseAsks = nil
	if _, err := l.PrepareKit(context.Background(), chainDef(nil, root, mid, b)); err != nil {
		t.Fatal(err)
	}
	if len(ops.hasBaseAsks) == 0 || ops.hasBaseAsks[0] != midStage {
		t.Fatalf("second child must ask HasKitBase(%s) first; asked %v", midStage, ops.hasBaseAsks)
	}
	var built []string
	for _, s := range ops.stages {
		built = append(built, s.stage)
	}
	bStage := mustStage(t, midStage, b.Kit.Base)
	if len(ops.stages) != 3 || ops.stages[2].stage != bStage || ops.stages[2].parent != midStage {
		t.Fatalf("stages built = %v; want mid once, then a, then b on mid", built)
	}
	if ops.builds != 2 {
		t.Fatalf("each child builds its own image once; builds=%d", ops.builds)
	}
}

// A child with no own context is the parent's stage as-is: BuildKitImage runs
// exactly once FROM the parent's stage (harness + hardening applied once) and no
// extra stage is built for the child.
func TestChildWithoutContextReusesParentStage(t *testing.T) {
	ops := &fakeOps{}
	root := chainLink("base-dev", 1, studio.Base{})
	mid := chainLink("web-dev", 1, ctxBase("base-dev", "mid"))
	child := chainLink("my-app", 0, studio.Base{Kit: "web-dev"})
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	if _, err := l.PrepareKit(context.Background(), chainDef(nil, root, mid, child)); err != nil {
		t.Fatal(err)
	}
	midStage := mustStage(t, "blessed-default", mid.Kit.Base)
	if len(ops.stages) != 1 || ops.stages[0].stage != midStage {
		t.Fatalf("stages = %+v; want only the parent's stage", ops.stages)
	}
	if ops.builds != 1 || ops.builtBase != midStage {
		t.Fatalf("BuildKitImage builds=%d base=%q; want once FROM %s", ops.builds, ops.builtBase, midStage)
	}
}

// A child pinned to parent v1 keeps building on the same stages after the
// parent pushes v2 (its chain still carries v1), while a following child's
// stages move with the parent's new context.
func TestPinnedParentStageStableAcrossParentPushes(t *testing.T) {
	root := chainLink("base-dev", 1, studio.Base{})
	v1 := chainLink("web-dev", 1, ctxBase("base-dev", "v1"))
	v2 := chainLink("web-dev", 2, ctxBase("base-dev", "v2"))
	pinned := chainLink("my-app", 0, ctxBase("web-dev@v1", "app"))
	following := chainLink("my-app", 0, ctxBase("web-dev", "app"))

	leafStage := func(links ...jam.KitLink) string {
		t.Helper()
		ops := &fakeOps{}
		if _, err := newPrepareLauncher(ops, &fakeInv{}, noAsm).PrepareKit(context.Background(), chainDef(nil, links...)); err != nil {
			t.Fatal(err)
		}
		return ops.builtBase
	}
	before := leafStage(root, v1, pinned)
	after := leafStage(root, v1, pinned) // after web-dev@v2 is pushed: the pinned chain is unchanged
	if before != after {
		t.Fatalf("pinned child's stage moved across a parent push: %q → %q", before, after)
	}
	if leafStage(root, v1, following) == leafStage(root, v2, following) {
		t.Fatal("a following child's stage must change when the parent's context does")
	}
}

// A gate failure on any stage fails the prepare, names the kit link, and builds
// no image.
func TestChainStageGateFailureNamesLink(t *testing.T) {
	root := chainLink("base-dev", 1, studio.Base{})
	mid := chainLink("web-dev", 3, ctxBase("base-dev", "mid"))
	leaf := chainLink("my-app", 0, ctxBase("web-dev", "leaf"))
	for _, tc := range []struct {
		at   int
		name string
	}{{1, "web-dev@v3"}, {2, "my-app"}} {
		ops := &fakeOps{stageErrAt: tc.at}
		st, err := newPrepareLauncher(ops, &fakeInv{}, noAsm).PrepareKit(context.Background(), chainDef(nil, root, mid, leaf))
		if err == nil || !strings.Contains(err.Error(), tc.name) || !strings.Contains(err.Error(), "blessed") || st.State == KitReady {
			t.Fatalf("stage %d gate failure: %+v, %v; want an error naming %s", tc.at, st, err, tc.name)
		}
		if ops.builds != 0 {
			t.Fatalf("a gate failure must build no image; builds=%d", ops.builds)
		}
	}

	// The root's own gate failure names the root.
	ops := &fakeOps{resolveTarErr: errors.New("base does not descend from any blessed cove-base-image")}
	rootCtx := chainLink("base-dev", 1, ctxBase("", "root"))
	_, err := newPrepareLauncher(ops, &fakeInv{}, noAsm).PrepareKit(context.Background(), chainDef(nil, rootCtx, leaf))
	if err == nil || !strings.Contains(err.Error(), "base-dev@v1") || ops.builds != 0 {
		t.Fatalf("root gate failure: %v builds=%d; want an error naming base-dev@v1", err, ops.builds)
	}
}

// Malformed chains fail closed: a root that names a parent, and a non-root link
// that does not.
func TestMalformedChainRefused(t *testing.T) {
	leaf := chainLink("my-app", 0, ctxBase("web-dev", "leaf"))
	for name, links := range map[string][]jam.KitLink{
		"root names a parent (no context)": {chainLink("web-dev", 1, studio.Base{Kit: "x"}), leaf},
		"root names a parent (context)":    {chainLink("web-dev", 1, ctxBase("x", "r")), leaf},
		"link without a parent":            {chainLink("base-dev", 1, studio.Base{}), chainLink("my-app", 0, studio.Base{Image: "img"})},
		"parented kit with no chain":       {leaf},
	} {
		ops := &fakeOps{}
		if _, err := newPrepareLauncher(ops, &fakeInv{}, noAsm).PrepareKit(context.Background(), chainDef(nil, links...)); err == nil {
			t.Fatalf("%s: want a refusal", name)
		}
		if ops.builds != 0 || len(ops.stages) != 0 {
			t.Fatalf("%s: a malformed chain must build nothing (builds=%d stages=%d)", name, ops.builds, len(ops.stages))
		}
	}
}

// A blessed-set rotation changes the launcher's assembly fingerprint; a stage
// cached under the old one must not be reused un-gated, so the asm is part of
// the stage tag and the second prepare rebuilds (and gates) the stage.
func TestStageTagIncludesAssemblyFingerprint(t *testing.T) {
	b := ctxBase("web-dev", "x")
	a1, _ := kitStageTag("asm1", "p", studio.StudioKit{Base: b})
	a2, _ := kitStageTag("asm2", "p", studio.StudioKit{Base: b})
	if a1 == a2 {
		t.Fatal("different assembly fingerprints must give different stage tags")
	}
	root := chainLink("base-dev", 1, studio.Base{})
	leaf := chainLink("my-app", 0, ctxBase("base-dev", "leaf"))
	def := chainDef(nil, root, leaf)
	ops := &fakeOps{}
	l := newPrepareLauncher(ops, &fakeInv{}, noAsm)
	if _, err := l.PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	first := ops.stages[0].stage
	l.asm = "rotated"
	if _, err := l.PrepareKit(context.Background(), def); err != nil {
		t.Fatal(err)
	}
	if len(ops.stages) != 2 || ops.stages[1].stage == first {
		t.Fatalf("stages = %+v; want the stage rebuilt under a new tag after asm rotation", ops.stages)
	}
}

// The own-base guard reads the chain's last link, not the merged Kit (Merge
// clears Base.Kit): a one-link chain whose own base names a parent is refused.
func TestOneLinkChainNamingParentRefused(t *testing.T) {
	def := chainDef(nil, chainLink("my-app", 0, ctxBase("web-dev", "leaf")))
	def.Kit.Base = studio.Base{} // as Merge leaves it
	ops := &fakeOps{}
	if _, err := newPrepareLauncher(ops, &fakeInv{}, noAsm).PrepareKit(context.Background(), def); err == nil {
		t.Fatal("want a refusal")
	}
	if ops.builds != 0 {
		t.Fatalf("builds = %d", ops.builds)
	}
}
