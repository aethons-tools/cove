package jam

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/kit"
	"github.com/aethons-tools/cove/internal/studio"
)

// chainRaiseKit sets up my-app → web-dev → default and a role "dev" raising
// my-app. The launcher reports the kit not ready once, so PrepareKit records
// the definition the supervisor resolved.
func chainRaiseKit(t *testing.T) (*Supervisor, Store, *fakeLauncher) {
	t.Helper()
	fl := &fakeLauncher{liveness: LivenessAlive, notReadyOnce: true, prepareState: KitReady}
	sup, store, _ := supTestKit(t, fl)
	root := studio.StudioKit{Kind: studio.Kind, Prompt: "root prompt", Egress: []string{"github.com"},
		Notes: []studio.KitNote{{Name: "a.md", ReadWhen: "root a", Body: "ROOT-A"}, {Name: "b.md", ReadWhen: "root b", Body: "ROOT-B"}}}
	mustEnsure(t, store, "default", root)
	web := studio.StudioKit{Kind: studio.Kind, Egress: []string{"pkg.go.dev"}, BuildArgs: map[string]string{"GO_VERSION": "1.27.1", "NODE_VERSION": "20"},
		MCPServers: map[string]kit.MCPServer{"docs": {Type: "stdio", Command: "docs-mcp"}}}
	web.Base.Kit = "default"
	mustEnsure(t, store, "web-dev", web)
	app := studio.StudioKit{Kind: studio.Kind, Prompt: "leaf prompt", Egress: []string{"npmjs.org"}, BuildArgs: map[string]string{"NODE_VERSION": "22"},
		Notes: []studio.KitNote{{Name: "b.md", ReadWhen: "leaf b", Body: "LEAF-B"}, {Name: "c.md", ReadWhen: "leaf c", Body: "LEAF-C"}}}
	app.Base.Kit = "web-dev"
	mustEnsure(t, store, "my-app", app)
	if err := store.PutRole("default", Role{Name: "dev", Kit: "my-app", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	return sup, store, fl
}

func raiseDev(t *testing.T, sup *Supervisor, id string) (Instance, error) {
	t.Helper()
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Project: "default", Role: "dev", Prompt: "P"})
	return inst, err
}

func TestRaiseFromMergedKitChain(t *testing.T) {
	sup, _, fl := chainRaiseKit(t)
	inst, err := raiseDev(t, sup, "w1")
	if err != nil {
		t.Fatal(err)
	}
	def := fl.preparedDef
	if len(def.Chain) != 3 || def.Chain[0].Ref.ID != "default" || def.Chain[1].Ref.ID != "web-dev" || def.Chain[2].Ref.ID != "my-app" {
		t.Fatalf("chain = %+v", def.Chain)
	}
	if self := def.Chain[2].Ref; self.Version != 1 {
		t.Errorf("the raised kit's own link must carry its stored version, got %+v", self)
	}
	if want := []string{"github.com", "npmjs.org", "pkg.go.dev"}; !slices.Equal(def.Kit.Egress, want) {
		t.Errorf("egress = %v, want %v", def.Kit.Egress, want)
	}
	if _, ok := def.Kit.MCPServers["docs"]; !ok {
		t.Errorf("parent MCP server missing: %v", def.Kit.MCPServers)
	}
	if def.Kit.BuildArgs["GO_VERSION"] != "1.27.1" || def.Kit.BuildArgs["NODE_VERSION"] != "22" {
		t.Errorf("build args = %v", def.Kit.BuildArgs)
	}
	if want := studio.ChainDigest(chainKits(def.Chain), def.Harness); def.Ref.Digest != want || inst.Kit != def.Ref {
		t.Errorf("ref %+v / instance %+v, want chain digest %s", def.Ref, inst.Kit, want)
	}

	c := fl.gotSpec.Context
	if !strings.Contains(c.Core, "root prompt\n\nleaf prompt") {
		t.Errorf("kit layer must carry the merged prompt:\n%s", c.Core)
	}
	if !strings.Contains(c.Files["kit/a.md"], "ROOT-A") || !strings.Contains(c.Files["kit/b.md"], "LEAF-B") || !strings.Contains(c.Files["kit/c.md"], "LEAF-C") {
		t.Errorf("merged notes wrong: %v", c.Files)
	}
	tools := c.Files["kit/tools.md"]
	if !strings.Contains(tools, "| go | 1.27.1 |") || !strings.Contains(tools, "| node | 22 |") {
		t.Errorf("tools.md must list the parent's tool versions (child wins):\n%s", tools)
	}
}

// An unchained kit's definition is a chain of one with the unchanged digest.
func TestUnchainedKitDefinition(t *testing.T) {
	st := NewMemStore()
	sk := studio.StudioKit{Kind: studio.Kind, Egress: []string{"a.example"}}
	ref := mustEnsure(t, st, "solo", sk)
	h := harnessinstall.Default()
	def, ok, err := ResolveKitDefinition(st, ref, h)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if len(def.Chain) != 1 || def.Chain[0].Ref.ID != "solo" || def.Chain[0].Ref.Version != ref.Version {
		t.Fatalf("chain = %+v", def.Chain)
	}
	if def.Ref.Digest != studio.BuildDigest(sk, h) {
		t.Error("an unchained kit's digest must not change")
	}
}

// A parent's prompt grown after the child was pushed takes the merged prompt
// past the kit budget: the raise still succeeds and the compiler truncates
// with its warning.
func TestMergedPromptOverBudgetAtRaise(t *testing.T) {
	sup, store, fl := chainRaiseKit(t)
	big := studio.StudioKit{Kind: studio.Kind, Prompt: strings.Repeat("x", 780)}
	big.Base.Kit = "default"
	mustEnsure(t, store, "web-dev", big) // web-dev's own chain is within budget
	if _, err := raiseDev(t, sup, "w1"); err != nil {
		t.Fatalf("raise must succeed with an over-budget merged prompt: %v", err)
	}
	found := false
	for _, w := range fl.gotSpec.Context.Warnings {
		if strings.Contains(w, "budget 800") && strings.Contains(w, "truncated") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a kit truncation warning, got %v", fl.gotSpec.Context.Warnings)
	}
}

// A parent gone from the registry fails the raise closed, before the launcher.
func TestMissingParentFailsClosed(t *testing.T) {
	sup, store, fl := chainRaiseKit(t)
	if err := store.RemoveKit("web-dev"); err != nil {
		t.Fatal(err)
	}
	_, err := raiseDev(t, sup, "w1")
	if err == nil || !strings.Contains(err.Error(), "kit web-dev (parent of my-app) is missing") {
		t.Fatalf("raise = %v, want the missing-parent error", err)
	}
	if fl.gotSpec.ActorID != "" || fl.prepareCalls != 0 {
		t.Error("the launcher must not be called")
	}
}

// prepareKitAndRetry re-resolves the chain at the parents' current versions;
// when that moves the digest, the fresh definition is prepared, handed back,
// and the retried raise carries its ref and a context compiled from it.
func TestPrepareKitAndRetryAdoptsFreshChainDigest(t *testing.T) {
	sup, store, fl := chainRaiseKit(t)
	k, _ := store.GetKit("my-app")
	stale, ok, err := ResolveKitDefinition(store, KitRef{ID: "my-app", Version: k.Current}, harnessinstall.Default())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	web := studio.StudioKit{Kind: studio.Kind, Prompt: "FRESH PARENT", Egress: []string{"proxy.golang.org"}}
	web.Base.Kit = "default"
	mustEnsure(t, store, "web-dev", web) // a following parent moves
	staleCtx := &sessionctx.Bundle{Core: "STALE"}
	spec := RaiseSpec{ActorID: "w1", Kit: stale.Ref, Context: staleCtx}
	_, ran, err := sup.prepareKitAndRetry(context.Background(), &spec, Actor{ID: "w1"}, LaunchCreds{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fresh := fl.preparedDef.Ref
	if fresh.Digest == stale.Ref.Digest || fresh.ID != "my-app" || fresh.Version != stale.Ref.Version {
		t.Fatalf("prepared %+v, stale %+v: want the re-resolved digest", fresh, stale.Ref)
	}
	if spec.Kit != fresh || fl.gotSpec.Kit != fresh || ran.Ref != fresh {
		t.Errorf("spec.Kit %+v / retried %+v / returned %+v, want %+v", spec.Kit, fl.gotSpec.Kit, ran.Ref, fresh)
	}
	if got := chainRefs(ran.Chain); len(got) != 3 || got[1] != (KitRef{ID: "web-dev", Version: 2}) {
		t.Errorf("returned chain = %+v, want web-dev@v2 in it", got)
	}
	if fl.gotSpec.Context == staleCtx || !strings.Contains(fl.gotSpec.Context.Core, "FRESH PARENT") {
		t.Errorf("the retried raise must carry a context compiled from the fresh definition")
	}
}

// When the re-resolve lands on the same ref, the spec is left as it was.
func TestPrepareKitAndRetryUnchangedKeepsSpec(t *testing.T) {
	sup, store, fl := chainRaiseKit(t)
	k, _ := store.GetKit("my-app")
	cur, _, err := ResolveKitDefinition(store, KitRef{ID: "my-app", Version: k.Current}, harnessinstall.Default())
	if err != nil {
		t.Fatal(err)
	}
	ctxIn := &sessionctx.Bundle{Core: "AS RAISED"}
	spec := RaiseSpec{ActorID: "w1", Kit: cur.Ref, Context: ctxIn}
	_, ran, err := sup.prepareKitAndRetry(context.Background(), &spec, Actor{ID: "w1"}, LaunchCreds{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Kit != cur.Ref || spec.Context != ctxIn || fl.gotSpec.Context != ctxIn || ran.Ref != cur.Ref {
		t.Errorf("an unchanged re-resolve must not touch the spec: %+v / %p vs %p", spec.Kit, spec.Context, ctxIn)
	}
}

// The chained twin of TestContextForKeepsRaisedKitImage: a following parent's
// new version reaches a running session's prompt, but its build-args and
// egress (the image doesn't have them) do not.
func TestContextForKeepsRaisedChainImage(t *testing.T) {
	sup, store, _ := chainRaiseKit(t)
	inst, err := raiseDev(t, sup, "w1")
	if err != nil {
		t.Fatal(err)
	}
	want := []KitRef{{ID: "default", Version: 1}, {ID: "web-dev", Version: 1}, {ID: "my-app", Version: 1}}
	if stored, _ := store.GetInstance("w1"); !slices.Equal(inst.KitChain, want) || !slices.Equal(stored.KitChain, want) {
		t.Fatalf("instance chain = %+v / stored %+v, want %+v", inst.KitChain, stored.KitChain, want)
	}
	web := studio.StudioKit{Kind: studio.Kind, Prompt: "NEW PARENT PROMPT", Egress: []string{"proxy.golang.org"}, BuildArgs: map[string]string{"GO_VERSION": "2.0"}}
	web.Base.Kit = "default"
	mustEnsure(t, store, "web-dev", web)
	got, err := sup.ContextFor("w1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Core, "NEW PARENT PROMPT") {
		t.Errorf("the parent's prompt edit must reach the session:\n%s", got.Core)
	}
	tools := got.Files["kit/tools.md"]
	if !strings.Contains(tools, "| go | 1.27.1 |") || strings.Contains(tools, "2.0") {
		t.Errorf("tools must stay the raised image's:\n%s", tools)
	}
	if !strings.Contains(got.Core, "pkg.go.dev") || strings.Contains(got.Core, "proxy.golang.org") {
		t.Errorf("egress must stay the raised image's:\n%s", got.Core)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", got.Warnings)
	}
}

// A recorded image-chain link that can't be read drops the build facts (no
// tool list, egress unknown) with a warning; the prompt still compiles.
func TestContextForUnreadableImageChainFailsClosed(t *testing.T) {
	sup, store, _ := chainRaiseKit(t)
	if _, err := raiseDev(t, sup, "w1"); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	inst.KitChain = []KitRef{{ID: "default", Version: 1}, {ID: "web-dev", Version: 9}, {ID: "my-app", Version: 1}}
	if err := store.PutInstance(inst); err != nil {
		t.Fatal(err)
	}
	got, err := sup.ContextFor("w1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Files["kit/tools.md"]; ok || strings.Contains(got.Core, "pkg.go.dev") {
		t.Errorf("build facts must be omitted:\n%s\n%v", got.Core, got.Files["kit/tools.md"])
	}
	if !strings.Contains(got.Core, "leaf prompt") {
		t.Errorf("the prompt must still compile:\n%s", got.Core)
	}
	if !slices.ContainsFunc(got.Warnings, func(w string) bool { return strings.Contains(w, "web-dev@v9") }) {
		t.Errorf("want a warning naming the missing link, got %v", got.Warnings)
	}
}

// A parent removed under a running session: the refresh warns rather than
// silently dropping the kit layer.
func TestContextForMissingParentWarns(t *testing.T) {
	sup, store, _ := chainRaiseKit(t)
	if _, err := raiseDev(t, sup, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveKit("web-dev"); err != nil {
		t.Fatal(err)
	}
	got, err := sup.ContextFor("w1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(got.Warnings, func(w string) bool {
		return strings.Contains(w, "kit web-dev (parent of my-app) is missing")
	}) {
		t.Errorf("want a missing-parent warning, got %v", got.Warnings)
	}
}
