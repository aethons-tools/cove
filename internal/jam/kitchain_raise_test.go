package jam

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harnessinstall"
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
// when that moves the digest, the fresh definition is prepared and the retried
// raise (and the recorded kit) carries its ref.
func TestPrepareKitAndRetryAdoptsFreshChainDigest(t *testing.T) {
	sup, store, fl := chainRaiseKit(t)
	k, _ := store.GetKit("my-app")
	stale, ok, err := ResolveKitDefinition(store, KitRef{ID: "my-app", Version: k.Current}, harnessinstall.Default())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	web := studio.StudioKit{Kind: studio.Kind, Egress: []string{"proxy.golang.org"}}
	web.Base.Kit = "default"
	mustEnsure(t, store, "web-dev", web) // a following parent moves
	spec := RaiseSpec{ActorID: "w1", Kit: stale.Ref}
	if _, err := sup.prepareKitAndRetry(context.Background(), &spec, LaunchCreds{}, nil); err != nil {
		t.Fatal(err)
	}
	fresh := fl.preparedDef.Ref
	if fresh.Digest == stale.Ref.Digest || fresh.ID != "my-app" || fresh.Version != stale.Ref.Version {
		t.Fatalf("prepared %+v, stale %+v: want the re-resolved digest", fresh, stale.Ref)
	}
	if spec.Kit != fresh || fl.gotSpec.Kit != fresh {
		t.Errorf("spec.Kit %+v / retried %+v, want %+v", spec.Kit, fl.gotSpec.Kit, fresh)
	}
}
