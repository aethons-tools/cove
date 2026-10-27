package fleet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
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

func TestRouterNamesRegistrationOrder(t *testing.T) {
	r := newRouter()
	mustAdd(t, r, newFake("x"), newFake("a"), newFake("b"))
	if got := r.Names(); !slices.Equal(got, []string{"x", "a", "b"}) {
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

func TestRouterAddRefusesDuplicate(t *testing.T) {
	r := newRouter()
	mustAdd(t, r, newFake("a"))
	if err := r.Add(newFake("a")); err == nil {
		t.Fatal("duplicate accepted")
	}
}

// The router has no special name: a manager called local is an ordinary member
// (the hub's ValidManagerName is what refuses the name).
func TestRouterHasNoLocalSpecialCase(t *testing.T) {
	r := newRouter()
	mustAdd(t, r, newFake("m1"))
	if err := r.Add(newFake("local")); err != nil {
		t.Fatalf("Add(local) = %v", err)
	}
	if got := r.Names(); !slices.Equal(got, []string{"m1", "local"}) {
		t.Fatalf("Names = %v", got)
	}
	r2 := newRouter()
	mustAdd(t, r2, newFake("local"), newFake("m1"))
	if got := r2.Names(); !slices.Equal(got, []string{"local", "m1"}) {
		t.Fatalf("Names = %v, local must not sort last", got)
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
	x := newFake(LocalName)
	mustAdd(t, r, newFake("a"), x)
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

// An available manager is asked to purge only when it lists the actor.
func TestPurgeStateAsksOnlyOwners(t *testing.T) {
	r := newRouter()
	owner, other := newFake("m0"), newFake("m1")
	owner.owners = []string{"w1"}
	other.owners = []string{"someone-else"}
	mustAdd(t, r, owner, other)
	if err := r.PurgeState(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(owner.get(&owner.purged), []string{"w1"}) || len(other.get(&other.purged)) != 0 {
		t.Fatalf("owner=%v other=%v", owner.get(&owner.purged), other.get(&other.purged))
	}
}

func TestNewNilLoggerDiscards(t *testing.T) {
	r := New(nil)
	if err := r.Teardown(context.Background(), jam.Instance{ActorID: "i1", Manager: "gone"}); err != nil {
		t.Fatal(err)
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

type factsFake struct {
	*fakeManager
}

func (f factsFake) ImageFacts(_ context.Context, image string) (jam.ImageFacts, error) {
	return jam.ImageFacts{}, fmt.Errorf("facts from %s for %s", f.name, image)
}

func TestRouterImageFacts(t *testing.T) {
	plain := newFake("plain")
	down := newFake("down")
	down.avail.Store(false)
	ok := newFake("ok")
	r := New(nil)
	for _, m := range []Manager{plain, factsFake{fakeManager: down}, factsFake{fakeManager: ok}} {
		if err := r.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.ImageFacts(context.Background(), "img")
	if err == nil || err.Error() != "facts from ok for img" {
		t.Fatalf("err %v", err)
	}

	r2 := New(nil)
	if err := r2.Add(plain); err != nil {
		t.Fatal(err)
	}
	if _, err := r2.ImageFacts(context.Background(), "img"); !errors.Is(err, jam.ErrManagerUnavailable) {
		t.Fatalf("err %v", err)
	}
}

// draftFake is a manager that can build kit drafts and read image facts.
type draftFake struct {
	*fakeManager
	built []string
}

func (d *draftFake) BuildKitDraft(_ context.Context, _ jam.KitDefinition, tag string, _ io.Writer) (string, error) {
	d.built = append(d.built, tag)
	return "id-" + d.name, nil
}
func (d *draftFake) HasKitDraftImage(id string) (bool, error) { return id == "id-"+d.name, nil }
func (d *draftFake) ImageFacts(context.Context, string) (jam.ImageFacts, error) {
	return jam.ImageFacts{}, fmt.Errorf("facts from %s", d.name)
}

func TestDraftManagersOrderAndCapability(t *testing.T) {
	r := newRouter()
	m2 := &draftFake{fakeManager: newFake("m2")}
	m2.avail.Store(false)
	mustAdd(t, r, newFake("m1"), m2, &draftFake{fakeManager: newFake("m3")})
	if got := r.DraftManagers(); !slices.Equal(got, []string{"m3"}) {
		t.Fatalf("DraftManagers = %v", got)
	}
	if got := r.AvailableNames(); !slices.Equal(got, []string{"m1", "m3"}) {
		t.Fatalf("AvailableNames = %v", got)
	}
}

func TestBuildKitDraftOnNamed(t *testing.T) {
	r := newRouter()
	m3 := &draftFake{fakeManager: newFake("m3")}
	m2 := &draftFake{fakeManager: newFake("m2")}
	mustAdd(t, r, m2, m3)
	id, err := r.BuildKitDraftOn(context.Background(), "m2", jam.KitDefinition{}, "t", nil)
	if err != nil || id != "id-m2" || len(m2.built) != 1 || len(m3.built) != 0 {
		t.Fatalf("id=%q err=%v m2=%v m3=%v", id, err, m2.built, m3.built)
	}
	if ok, err := r.HasKitDraftImageOn("m2", "id-m2"); err != nil || !ok {
		t.Fatalf("HasKitDraftImageOn = %v, %v", ok, err)
	}
	// "" is the first available draft-capable manager: m2 is registered first.
	if id, _ := r.BuildKitDraftOn(context.Background(), "", jam.KitDefinition{}, "t", nil); id != "id-m2" {
		t.Fatalf("default id = %q", id)
	}
}

func TestOnFormsErrors(t *testing.T) {
	r := newRouter()
	down := &draftFake{fakeManager: newFake("down")}
	down.avail.Store(false)
	mustAdd(t, r, down, newFake("plain"))
	ctx := context.Background()
	calls := map[string]func(name string) error{
		"build": func(n string) error { _, err := r.BuildKitDraftOn(ctx, n, jam.KitDefinition{}, "t", nil); return err },
		"has":   func(n string) error { _, err := r.HasKitDraftImageOn(n, "id"); return err },
		"draft": r.DraftManagerErr,
		"facts": func(n string) error { _, err := r.ImageFactsOn(ctx, n, "img"); return err },
	}
	for what, call := range calls {
		if err := call("nope"); !errors.Is(err, jam.ErrUnknownManager) || !strings.Contains(err.Error(), "nope") {
			t.Errorf("%s unknown: %v", what, err)
		}
		if err := call("down"); !errors.Is(err, jam.ErrManagerUnavailable) {
			t.Errorf("%s unavailable: %v", what, err)
		}
	}
	for what, want := range map[string]string{
		"build": "manager plain cannot build kit drafts",
		"has":   "manager plain cannot build kit drafts",
		"draft": "manager plain cannot build kit drafts",
		"facts": "manager plain cannot read image facts",
	} {
		err := calls[what]("plain")
		if !errors.Is(err, jam.ErrManagerUnavailable) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s incapable: %v", what, err)
		}
	}
}

func TestImageFactsOnDefaultIsFirstAvailable(t *testing.T) {
	r := newRouter()
	down := &draftFake{fakeManager: newFake("down")}
	down.avail.Store(false)
	mustAdd(t, r, newFake("plain"), down, &draftFake{fakeManager: newFake("ok")})
	for _, name := range []string{"", "ok"} {
		_, err := r.ImageFactsOn(context.Background(), name, "img")
		if err == nil || err.Error() != "facts from ok" {
			t.Fatalf("%q: %v", name, err)
		}
	}
	if _, err := r.ImageFacts(context.Background(), "img"); err == nil || err.Error() != "facts from ok" {
		t.Fatalf("ImageFacts: %v", err)
	}
}

func TestRouterManagerErr(t *testing.T) {
	up, down := newFake("up"), newFake("down")
	down.avail.Store(false)
	r := New(nil)
	for _, m := range []Manager{up, down} {
		if err := r.Add(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.ManagerErr("up"); err != nil {
		t.Errorf("up: %v", err)
	}
	if err := r.ManagerErr("nope"); !errors.Is(err, jam.ErrUnknownManager) || !strings.Contains(err.Error(), "nope") {
		t.Errorf("nope: %v", err)
	}
	if err := r.ManagerErr("down"); !errors.Is(err, jam.ErrManagerUnavailable) || !strings.Contains(err.Error(), "down") {
		t.Errorf("down: %v", err)
	}
}
