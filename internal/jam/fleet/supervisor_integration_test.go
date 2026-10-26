package fleet

import (
	"context"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/studio"
)

// routedSupervisor is a jam.Supervisor over a MemStore with a guest role whose
// launcher is a real Router holding only Local(loc).
func routedSupervisor(t *testing.T, loc *fakeManager) (*jam.Supervisor, jam.Store) {
	t.Helper()
	store := jam.NewMemStore()
	if err := store.PutRole("default", jam.Role{Name: "guest", Scope: jam.Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	r := newRouter()
	mustAdd(t, r, Local(loc))
	clk := time.Unix(1000, 0).UTC()
	sup := jam.NewSupervisor(store, r, "holder-A", time.Minute, 30*time.Second,
		func() time.Time { return clk }, slog.New(slog.DiscardHandler))
	return sup, store
}

func TestSupervisorOverRouterRecordsLocalManager(t *testing.T) {
	loc := newFake("x")
	sup, store := routedSupervisor(t, loc)
	inst, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetInstance("w1"); inst.Manager != LocalName || got.Manager != LocalName {
		t.Fatalf("Manager = %q / stored %q, want %q", inst.Manager, got.Manager, LocalName)
	}
	if inst.Location != "x-loc" || !slices.Equal(loc.get(&loc.raised), []string{"guest"}) {
		t.Fatalf("location %q raised %v", inst.Location, loc.get(&loc.raised))
	}
}

func TestSupervisorOverRouterPreparesKitOnLocalThenRaises(t *testing.T) {
	loc := newFake("x")
	loc.notReadyOnce.Store(true)
	loc.prepareState = jam.KitReady
	sup, store := routedSupervisor(t, loc)
	ref, err := jam.EnsureStudioKit(store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetDefaultStudioKit(ref)
	inst, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.get(&loc.prepared); len(got) != 1 {
		t.Fatalf("PrepareKit calls = %v, want one", got)
	}
	if inst.Manager != LocalName || len(loc.get(&loc.raised)) != 1 {
		t.Fatalf("manager %q raised %v, want the retry on local", inst.Manager, loc.get(&loc.raised))
	}
}

func TestSupervisorOverRouterPinsStandingSessionToLocal(t *testing.T) {
	loc := newFake("x")
	sup, store := routedSupervisor(t, loc)
	pid, ok := jam.ProjectIDOf(store, "default")
	if !ok {
		t.Fatal("no default project")
	}
	sid := string(ident.New(ident.Session))
	if err := store.PutStandingSession(pid, "guest", "spider", sid); err != nil {
		t.Fatal(err)
	}
	inst, _, _, err := sup.Raise(context.Background(), jam.RaiseSpec{
		ActorID: sid, Role: "guest", Name: "spider", SessionKind: jam.SessionKindStanding,
	})
	if err != nil {
		t.Fatal(err)
	}
	if inst.Manager != LocalName {
		t.Fatalf("manager = %q, want %q", inst.Manager, LocalName)
	}
	if got := store.StandingSessionManager(pid, "guest", "spider"); got != LocalName {
		t.Fatalf("pin = %q, want %q", got, LocalName)
	}
}
