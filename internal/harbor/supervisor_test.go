package harbor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

// fakeLauncher is a scripted Launcher for hermetic supervisor tests.
type fakeLauncher struct {
	loc         string
	raiseErr    error
	teardownErr error
	liveness    Liveness
	probeErr    error
	raised      []string
	tornDown    []string
}

func (f *fakeLauncher) Raise(_ context.Context, spec RaiseSpec) (string, error) {
	if f.raiseErr != nil {
		return "", f.raiseErr
	}
	f.raised = append(f.raised, spec.ActorID)
	if f.loc != "" {
		return f.loc, nil
	}
	return "fake:" + spec.ActorID, nil
}
func (f *fakeLauncher) Teardown(_ context.Context, inst Instance) error {
	f.tornDown = append(f.tornDown, inst.ActorID)
	return f.teardownErr
}
func (f *fakeLauncher) Probe(_ context.Context, _ Instance) (Liveness, error) {
	return f.liveness, f.probeErr
}

// supTestKit builds a supervisor over a temp store with a guest role, a fixed
// clock, and the given launcher. Returns the supervisor, store, and a pointer to
// the mutable clock.
func supTestKit(t *testing.T, l Launcher) (*Supervisor, Store, *time.Time) {
	t.Helper()
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	clk := time.Unix(1000, 0).UTC()
	clkp := &clk
	sup := NewSupervisor(store, l, "holder-A", 60*time.Second, 30*time.Second,
		func() time.Time { return *clkp }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sup, store, clkp
}

func TestRaiseEnrollsAndRecordsLiveInstance(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	inst, tok, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Unit: "AET-1"})
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("expected a minted identity token")
	}
	if inst.Phase != PhaseLive || inst.Activity != ActivityRunning {
		t.Fatalf("phase/activity = %s/%s", inst.Phase, inst.Activity)
	}
	if inst.Lease.Holder != "holder-A" || !inst.Lease.Expiry.Equal(time.Unix(1060, 0).UTC()) {
		t.Fatalf("lease = %+v", inst.Lease)
	}
	if got, ok := store.GetInstance("w1"); !ok || got.Location != "fake:w1" {
		t.Fatalf("instance not recorded: %+v ok=%v", got, ok)
	}
	// Identity was enrolled (an actor exists).
	if len(store.ListActors()) != 1 {
		t.Fatalf("expected 1 actor, got %d", len(store.ListActors()))
	}
	if f.raised[0] != "w1" {
		t.Fatalf("launcher not called: %+v", f.raised)
	}
}

func TestRaiseRollsBackIdentityWhenLauncherFails(t *testing.T) {
	f := &fakeLauncher{raiseErr: errors.New("backend down")}
	sup, store, _ := supTestKit(t, f)
	if _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err == nil {
		t.Fatal("expected raise to fail")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("failed raise must leave no dangling identity")
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("failed raise must record no instance")
	}
}

func TestRaiseRequiresExistingRole(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{})
	if _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "ghost"}); err == nil {
		t.Fatal("expected fail-closed on unknown role")
	}
}

func TestReportSetsActivityAndRenewsLease(t *testing.T) {
	sup, store, clk := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	*clk = clk.Add(10 * time.Second) // now 1010
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	if got.Activity != ActivityWaiting {
		t.Fatalf("activity = %s", got.Activity)
	}
	if !got.Lease.Expiry.Equal(time.Unix(1070, 0).UTC()) { // 1010 + 60
		t.Fatalf("lease not renewed: %+v", got.Lease)
	}
	if got.Phase != PhaseLive {
		t.Fatalf("report must not change Phase off Done: %s", got.Phase)
	}
}

func TestReportDoneTearsDown(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Report(context.Background(), "w1", ActivityDone); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("Done should tear the instance down")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("teardown should revoke the identity")
	}
	if len(f.tornDown) != 1 || f.tornDown[0] != "w1" {
		t.Fatalf("launcher teardown not called: %+v", f.tornDown)
	}
}

func TestTeardownIsIdempotent(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{})
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatalf("second teardown must be a no-op, got %v", err)
	}
}

// revokeFailingStore wraps a real Store and forces RemoveActor to fail with a
// real error (as opposed to "not found") while delegating everything else, so
// tests can exercise Teardown's revoke-before-deregister failure path.
type revokeFailingStore struct {
	Store
	removeActorErr error
}

func (s *revokeFailingStore) RemoveActor(id string) error {
	if s.removeActorErr != nil {
		return s.removeActorErr
	}
	return s.Store.RemoveActor(id)
}

// TestTeardownPropagatesRevokeFailure proves that a genuine identity-revoke
// failure makes Teardown return a non-nil error and leaves the Instance in
// place (not the Actor, which the real store still has) so a retry re-drives
// the whole teardown, including the revoke, instead of silently leaving a
// live token behind.
func TestTeardownPropagatesRevokeFailure(t *testing.T) {
	real, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := real.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	store := &revokeFailingStore{Store: real, removeActorErr: errors.New("store write failed")}
	clk := time.Unix(1000, 0).UTC()
	clkp := &clk
	f := &fakeLauncher{}
	sup := NewSupervisor(store, f, "holder-A", 60*time.Second, 30*time.Second,
		func() time.Time { return *clkp }, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if len(store.ListActors()) != 1 {
		t.Fatalf("expected actor to be present before teardown, got %d", len(store.ListActors()))
	}

	if err := sup.Teardown(context.Background(), "w1"); err == nil {
		t.Fatal("expected teardown to fail when identity revocation fails")
	}

	if _, ok := store.GetInstance("w1"); !ok {
		t.Fatal("a failed revoke must leave the Instance in place so a retry re-revokes")
	}
	if len(store.ListActors()) != 1 {
		t.Fatal("actor should still be present after the failed revoke")
	}

	// A retry with the revoke now working should succeed and clean everything up.
	store.removeActorErr = nil
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatalf("retry after revoke recovers should succeed, got %v", err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("instance should be gone after the successful retry")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("actor should be revoked after the successful retry")
	}
}
