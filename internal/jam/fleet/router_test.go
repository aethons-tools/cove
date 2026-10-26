package fleet

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
)

func mustAdd(t *testing.T, r *Router, ms ...Manager) {
	t.Helper()
	for _, m := range ms {
		if err := r.Add(m); err != nil {
			t.Fatalf("Add(%s): %v", m.Name(), err)
		}
	}
}

func newRouter() *Router { return New(slog.New(slog.DiscardHandler)) }

func TestRouterNamesRegistrationOrderLocalLast(t *testing.T) {
	r := newRouter()
	mustAdd(t, r, Local(newFake("x")), newFake("a"), newFake("b"))
	if got := r.Names(); !slices.Equal(got, []string{"a", "b", "local"}) {
		t.Fatalf("Names = %v", got)
	}
	if !r.HasManager("a") || r.HasManager("zz") {
		t.Fatal("HasManager wrong")
	}
	r.Remove("a")
	if r.HasManager("a") {
		t.Fatal("Remove did not remove")
	}
}

func TestRouterAddRefusesDuplicateAndReservedLocal(t *testing.T) {
	r := newRouter()
	mustAdd(t, r, newFake("a"))
	if err := r.Add(newFake("a")); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := r.Add(newFake("local")); err == nil {
		t.Fatal("non-Local manager named local accepted")
	}
}

func TestRouterRoutesByInstanceManager(t *testing.T) {
	r := newRouter()
	a, b := newFake("a"), newFake("b")
	mustAdd(t, r, a, b)
	if err := r.Teardown(context.Background(), jam.Instance{ActorID: "i1", Manager: "b"}); err != nil {
		t.Fatal(err)
	}
	if len(a.get(&a.tornDown)) != 0 || !slices.Equal(b.get(&b.tornDown), []string{"i1"}) {
		t.Fatalf("a=%v b=%v", a.get(&a.tornDown), b.get(&b.tornDown))
	}
}

func TestRouterLegacyInstanceRoutesToLocal(t *testing.T) {
	r := newRouter()
	x := newFake("x")
	mustAdd(t, r, newFake("a"), Local(x))
	inst := jam.Instance{ActorID: "old"}
	if err := r.Teardown(context.Background(), inst); err != nil {
		t.Fatal(err)
	}
	if lv, err := r.Probe(context.Background(), inst); err != nil || lv != jam.LivenessAlive {
		t.Fatalf("probe = %v, %v", lv, err)
	}
	if len(x.get(&x.tornDown)) != 1 || len(x.get(&x.probed)) != 1 {
		t.Fatal("legacy instance did not reach local")
	}
}

func TestRouterProbeUnavailableIsUnknownNotError(t *testing.T) {
	r := newRouter()
	b := newFake("b")
	mustAdd(t, r, b)
	b.avail.Store(false)
	lv, err := r.Probe(context.Background(), jam.Instance{Manager: "b"})
	if err != nil || lv != jam.LivenessUnknown {
		t.Fatalf("got %v, %v", lv, err)
	}
	if len(b.get(&b.probed)) != 0 {
		t.Fatal("probe forwarded to unavailable manager")
	}
}

func TestRouterProbeUnregisteredIsDead(t *testing.T) {
	lv, err := newRouter().Probe(context.Background(), jam.Instance{Manager: "gone"})
	if err != nil || lv != jam.LivenessDead {
		t.Fatalf("got %v, %v", lv, err)
	}
}

func TestRouterTeardownUnavailableErrorsUnregisteredNoops(t *testing.T) {
	r := newRouter()
	b := newFake("b")
	mustAdd(t, r, b)
	b.avail.Store(false)
	if err := r.Teardown(context.Background(), jam.Instance{Manager: "b"}); !errors.Is(err, jam.ErrManagerUnavailable) {
		t.Fatalf("unavailable: %v", err)
	}
	if err := r.Teardown(context.Background(), jam.Instance{Manager: "gone"}); err != nil {
		t.Fatalf("unregistered: %v", err)
	}
}

func TestRouterPauseUnknownManagerErrors(t *testing.T) {
	r := newRouter()
	ctx := context.Background()
	inst := jam.Instance{Manager: "gone"}
	if err := r.Pause(ctx, inst); !errors.Is(err, jam.ErrUnknownManager) {
		t.Fatalf("Pause: %v", err)
	}
	if err := r.Unpause(ctx, inst); !errors.Is(err, jam.ErrUnknownManager) {
		t.Fatalf("Unpause: %v", err)
	}
	if err := r.ApplyEgress(ctx, inst, nil); !errors.Is(err, jam.ErrUnknownManager) {
		t.Fatalf("ApplyEgress: %v", err)
	}
	b := newFake("b")
	mustAdd(t, r, b)
	b.avail.Store(false)
	if err := r.Pause(ctx, jam.Instance{Manager: "b"}); !errors.Is(err, jam.ErrManagerUnavailable) {
		t.Fatalf("unavailable Pause: %v", err)
	}
}

func TestRouterStateOwnersUnionAndPurgeOnlyOwner(t *testing.T) {
	r := newRouter()
	a, b := newFake("a"), newFake("b")
	a.owners, b.owners = []string{"x"}, []string{"y"}
	mustAdd(t, r, a, b)
	got, err := r.StateOwners(context.Background())
	if err != nil || !slices.Equal(got, []string{"x", "y"}) {
		t.Fatalf("owners = %v, %v", got, err)
	}
	if err := r.PurgeState(context.Background(), "y"); err != nil {
		t.Fatal(err)
	}
	if len(a.get(&a.purged)) != 0 || !slices.Equal(b.get(&b.purged), []string{"y"}) {
		t.Fatalf("a=%v b=%v", a.get(&a.purged), b.get(&b.purged))
	}
	if err := r.PurgeState(context.Background(), "nobody"); err != nil {
		t.Fatal(err)
	}
}

// Local purges by name: a reset reaches it even when its StateOwners (a label
// filter on the current Jam identity) does not list the id.
func TestRouterPurgeStateAlwaysReachesLocal(t *testing.T) {
	r := newRouter()
	loc, a := newFake("x"), newFake("a")
	a.owners = []string{"other"}
	mustAdd(t, r, Local(loc), a)
	if err := r.PurgeState(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loc.get(&loc.purged), []string{"w1"}) || len(a.get(&a.purged)) != 0 {
		t.Fatalf("local=%v a=%v, want local=[w1] a=[]", loc.get(&loc.purged), a.get(&a.purged))
	}
}

func TestNewNilLoggerDiscards(t *testing.T) {
	r := New(nil)
	if err := r.Teardown(context.Background(), jam.Instance{ActorID: "i1", Manager: "gone"}); err != nil {
		t.Fatal(err)
	}
}

// stateOnly is a bare launcher that is a StateKeeper.
type stateOnly struct{ jam.Launcher }

func (stateOnly) PurgeState(context.Context, string) error { return nil }
func (stateOnly) StateOwners(context.Context) ([]string, error) {
	return []string{"loc"}, nil
}

func TestLocalExposesWrappedOptionalInterfaces(t *testing.T) {
	r := newRouter()
	mustAdd(t, r, Local(stateOnly{}))
	got, err := r.StateOwners(context.Background())
	if err != nil || !slices.Equal(got, []string{"loc"}) {
		t.Fatalf("owners = %v, %v", got, err)
	}
}

func TestRouterPrepareKitAndImageTagUseFirstAvailable(t *testing.T) {
	r := newRouter()
	a, b := newFake("a"), newFake("b")
	b.asm = "img-b"
	mustAdd(t, r, a, b)
	a.avail.Store(false)
	if _, err := r.PrepareKit(context.Background(), jam.KitDefinition{}); err != nil {
		t.Fatal(err)
	}
	if len(b.get(&b.prepared)) != 1 || len(a.get(&a.prepared)) != 0 {
		t.Fatal("PrepareKit not on first available")
	}
	if got := r.ImageTag(jam.KitRef{}); got != "img-b" {
		t.Fatalf("ImageTag = %q", got)
	}
	b.avail.Store(false)
	if got := r.ImageTag(jam.KitRef{}); got != "" {
		t.Fatalf("ImageTag with none = %q", got)
	}
}

func TestRouterBuildKitDraftNoBuilder(t *testing.T) {
	r := newRouter()
	mustAdd(t, r, newFake("a"))
	if _, err := r.BuildKitDraft(context.Background(), jam.KitDefinition{}, "t", nil); err == nil {
		t.Fatal("want error")
	}
	if _, err := r.HasKitDraftImage("id"); err == nil {
		t.Fatal("want error")
	}
}
