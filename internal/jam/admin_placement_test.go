package jam

import (
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func TestParsePlacement(t *testing.T) {
	got, err := ParsePlacement([]string{"a", "b"}, "2m")
	if err != nil || !reflect.DeepEqual(got, Placement{Managers: []string{"a", "b"}, Timeout: 2 * time.Minute}) {
		t.Fatalf("ParsePlacement = %+v, %v", got, err)
	}
	got, err = ParsePlacement(nil, "")
	if err != nil || !reflect.DeepEqual(got, Placement{}) {
		t.Fatalf("zero ParsePlacement = %+v, %v", got, err)
	}
	for _, bad := range []string{"-1s", "soon"} {
		if _, err := ParsePlacement(nil, bad); err == nil {
			t.Errorf("timeout %q: want an error", bad)
		}
	}
}

func placingAdmin(t *testing.T, store Store, sup *Supervisor, alloc SessionAllocator) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	op := "auth0|alice"
	return NewAdminHandler(store, sup, alloc, switchOperator{id: &op}, func(string) bool { return true }, nil, log, nil)
}

func TestPostCovesPassesPlacement(t *testing.T) {
	p := newPlacingFake("a", "b")
	sup, store, _ := supTestKit(t, p)
	h := placingAdmin(t, store, sup, nil)
	rec := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "w1", Role: "guest", Managers: []string{"b"}, PlacementTimeout: "0s"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("raise = %d %s", rec.Code, rec.Body.String())
	}
	if got := p.by["b"].gotSpec.Placement.Managers; !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("placement managers = %v, want [b]", got)
	}
	if inst, _ := store.GetInstance(ResolveSession(store, "w1")); inst.Manager != "b" {
		t.Fatalf("instance manager = %q, want b", inst.Manager)
	}
}

func TestPostCovesBadPlacementTimeout400(t *testing.T) {
	p := newPlacingFake("a")
	sup, store, _ := supTestKit(t, p)
	h := placingAdmin(t, store, sup, nil)
	rec := doJSON(t, h, "POST", "/admin/coves", CoveRaiseBody{ID: "w1", Role: "guest", PlacementTimeout: "-5s"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("raise = %d %s, want 400", rec.Code, rec.Body.String())
	}
	if len(p.by["a"].raised) != 0 {
		t.Fatal("a raise happened")
	}
}

func TestPersonalSessionPassesPlacement(t *testing.T) {
	k := newSessionKit(t)
	p := newPlacingFake("a", "b")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sup := NewSupervisor(k.store, p, "holder-placing", time.Minute, 30*time.Second, nil, log)
	h := placingAdmin(t, k.store, sup, k.alloc)
	rec := doJSON(t, h, "POST", "/admin/sessions/personal", PersonalSessionBody{Project: "acme", Role: "pair", Prompt: "hi", Managers: []string{"b"}, PlacementTimeout: "10s"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("request = %d %s", rec.Code, rec.Body.String())
	}
	spec := p.by["b"].gotSpec
	if !reflect.DeepEqual(spec.Placement, Placement{Managers: []string{"b"}, Timeout: 10 * time.Second}) {
		t.Fatalf("placement = %+v", spec.Placement)
	}
	// A bad timeout is refused before any slot is granted.
	n := len(k.alloc.grants)
	rec = doJSON(t, h, "POST", "/admin/sessions/personal", PersonalSessionBody{Project: "acme", Role: "pair", PlacementTimeout: "soon"})
	if rec.Code != http.StatusBadRequest || len(k.alloc.grants) != n {
		t.Fatalf("bad timeout = %d, grants %d -> %d", rec.Code, n, len(k.alloc.grants))
	}
}

func TestCoveSummaryCarriesManager(t *testing.T) {
	store := NewMemStore()
	if err := store.PutInstance(Instance{ActorID: "w1", Role: "guest", Manager: "b", Phase: PhaseLive}); err != nil {
		t.Fatal(err)
	}
	got := CoveSummaries(store, nil)
	if len(got) != 1 || got[0].Manager != "b" {
		t.Fatalf("summaries = %+v", got)
	}
}
