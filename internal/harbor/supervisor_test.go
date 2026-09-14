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
	probed      []string
	paused      []string
	resumed     []string
	gotSpec     RaiseSpec
	gotCreds    LaunchCreds
}

func (f *fakeLauncher) Raise(_ context.Context, spec RaiseSpec, creds LaunchCreds) (string, error) {
	f.gotSpec, f.gotCreds = spec, creds
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
func (f *fakeLauncher) Probe(_ context.Context, inst Instance) (Liveness, error) {
	f.probed = append(f.probed, inst.ActorID)
	return f.liveness, f.probeErr
}
func (f *fakeLauncher) Pause(_ context.Context, inst Instance) error {
	f.paused = append(f.paused, inst.ActorID)
	return nil
}
func (f *fakeLauncher) Unpause(_ context.Context, inst Instance) error {
	f.resumed = append(f.resumed, inst.ActorID)
	return nil
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
	inst, tok, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Unit: "AET-1"})
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
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err == nil {
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
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "ghost"}); err == nil {
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

func TestReportWaitingStampsAndClearsCursor(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.SetWaitCursor("w1", "3"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitingSince.IsZero() {
		t.Fatal("WaitingSince not set on transition into Waiting")
	}
	if inst.WaitCursor != "" {
		t.Fatalf("WaitCursor should clear on transition into Waiting, got %q", inst.WaitCursor)
	}

	// A second Report(Waiting) while already Waiting must NOT reset
	// WaitingSince, and must NOT clear a cursor set in between.
	first := inst.WaitingSince
	if err := sup.SetWaitCursor("w1", "4"); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ = store.GetInstance("w1")
	if !inst.WaitingSince.Equal(first) {
		t.Fatal("WaitingSince reset while already Waiting")
	}
	if inst.WaitCursor != "4" {
		t.Fatalf("WaitCursor cleared while already Waiting, got %q", inst.WaitCursor)
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

	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
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

func TestReconcileReapsExpiredDead(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	*clk = clk.Add(2 * time.Minute) // lease (60s) now expired
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("expired+dead instance must be reaped")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("reaped instance must be revoked")
	}
	if len(f.tornDown) != 1 {
		t.Fatalf("launcher teardown expected once, got %d", len(f.tornDown))
	}
}

func TestReconcileAdoptsExpiredAlive(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	// Simulate another holder owning it, lease expired.
	inst, _ := store.GetInstance("w1")
	inst.Lease = Lease{Holder: "holder-B", Expiry: time.Unix(900, 0).UTC()}
	store.PutInstance(inst)
	*clk = clk.Add(1 * time.Minute) // now 1060 > 900
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := store.GetInstance("w1")
	if !ok {
		t.Fatal("alive instance must be adopted, not reaped")
	}
	if got.Lease.Holder != "holder-A" || !got.Lease.Expiry.Equal(time.Unix(1120, 0).UTC()) {
		t.Fatalf("lease not stolen+renewed: %+v", got.Lease)
	}
	if len(f.tornDown) != 0 {
		t.Fatal("alive instance must not be torn down")
	}
}

func TestReconcileLeavesHealthyInstance(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	*clk = clk.Add(10 * time.Second) // well within the 60s lease
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	// Ours + unexpired ⇒ renewed to now+ttl; never probed/torn down.
	if !got.Lease.Expiry.Equal(time.Unix(1070, 0).UTC()) {
		t.Fatalf("own lease should be renewed: %+v", got.Lease)
	}
	if len(f.tornDown) != 0 {
		t.Fatal("healthy instance must not be torn down")
	}
}

func TestRestartReadoptsLiveInstances(t *testing.T) {
	// Seed a store with a Live instance as if a prior process had raised it, then
	// build a FRESH supervisor over the same store (a restart) and reconcile.
	store, err := NewFileStore(filepath.Join(t.TempDir(), "store.json"))
	if err != nil {
		t.Fatal(err)
	}
	store.PutInstance(Instance{ActorID: "survivor", Project: "default", Role: "guest",
		Phase: PhaseLive, Lease: Lease{Holder: "old-holder", Expiry: time.Unix(100, 0).UTC()}})
	f := &fakeLauncher{liveness: LivenessAlive}
	clk := time.Unix(1000, 0).UTC()
	sup := NewSupervisor(store, f, "holder-NEW", 60*time.Second, 30*time.Second,
		func() time.Time { return clk }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := store.GetInstance("survivor")
	if !ok {
		t.Fatal("a live instance must survive a restart (re-adopted), not be dropped")
	}
	if got.Lease.Holder != "holder-NEW" {
		t.Fatalf("re-adopt should steal the lease, got holder %q", got.Lease.Holder)
	}
}

// TestReconcileResumesTerminating proves that an instance persisted in
// PhaseTerminating or PhaseLost (e.g. Report(Done) or a prior Teardown that
// marked Terminating then crashed before RemoveInstance) is always finished by
// Reconcile — never renewed or adopted — regardless of lease state. This
// covers the worst case (lease still unexpired, held by self) as well as the
// expired-lease case.
func TestReconcileResumesTerminating(t *testing.T) {
	t.Run("unexpired lease", func(t *testing.T) {
		f := &fakeLauncher{liveness: LivenessAlive}
		sup, store, _ := supTestKit(t, f)
		sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})

		// Simulate a persisted-Terminating crash state: Done was reported (or a
		// Teardown got partway through) but RemoveInstance never ran. The lease
		// is left exactly as Raise set it — unexpired, held by us.
		inst, ok := store.GetInstance("w1")
		if !ok {
			t.Fatal("expected instance after raise")
		}
		inst.Phase = PhaseTerminating
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}

		if err := sup.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}

		if _, ok := store.GetInstance("w1"); ok {
			t.Fatal("Terminating instance must be torn down by Reconcile, not renewed/adopted")
		}
		if len(store.ListActors()) != 0 {
			t.Fatal("resumed teardown must revoke the identity")
		}
		if len(f.tornDown) != 1 || f.tornDown[0] != "w1" {
			t.Fatalf("launcher teardown not called: %+v", f.tornDown)
		}
	})

	t.Run("expired lease", func(t *testing.T) {
		f := &fakeLauncher{liveness: LivenessAlive}
		sup, store, clk := supTestKit(t, f)
		sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})

		inst, ok := store.GetInstance("w1")
		if !ok {
			t.Fatal("expected instance after raise")
		}
		inst.Phase = PhaseLost
		if err := store.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
		*clk = clk.Add(2 * time.Minute) // lease (60s) now expired

		if err := sup.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}

		if _, ok := store.GetInstance("w1"); ok {
			t.Fatal("Lost instance must be torn down by Reconcile even with an expired lease")
		}
		if len(store.ListActors()) != 0 {
			t.Fatal("resumed teardown must revoke the identity")
		}
		if len(f.tornDown) != 1 || f.tornDown[0] != "w1" {
			t.Fatalf("launcher teardown not called: %+v", f.tornDown)
		}
	})
}

func TestRaisePassesCredsToLauncher(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, _, _ := supTestKit(t, fl)
	_, tok, secret, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Prompt: "do it"})
	if err != nil {
		t.Fatal(err)
	}
	if fl.gotCreds.IdentityToken != tok || fl.gotCreds.LaunchSecret != secret {
		t.Fatalf("launcher creds = %+v, want token=%q secret=%q", fl.gotCreds, tok, secret)
	}
	if fl.gotSpec.Prompt != "do it" {
		t.Fatalf("launcher spec.Prompt = %q, want %q", fl.gotSpec.Prompt, "do it")
	}
}

func TestRunStartsAndStops(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sup.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// fakeSink records ControlSink calls for assertions.
type fakeSink struct {
	teardowns []string
	wakes     []string
}

func (f *fakeSink) RequestTeardown(id string) { f.teardowns = append(f.teardowns, id) }
func (f *fakeSink) Wake(id string)            { f.wakes = append(f.wakes, id) }

func TestRaiseMintsLaunchSecret(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	inst, tok, secret, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || secret == "" {
		t.Fatalf("expected token AND launch secret, got tok=%q secret=%q", tok, secret)
	}
	// The hash is stored, never the plaintext.
	if inst.LaunchSecretHash != HashToken(secret) {
		t.Fatalf("launch secret hash not stored correctly")
	}
	if inst.LaunchSecretHash == secret {
		t.Fatal("stored the plaintext launch secret")
	}
	got, _ := store.GetInstance("w1")
	if got.LaunchSecretHash != HashToken(secret) {
		t.Fatal("persisted instance missing launch secret hash")
	}
}

func TestHeartbeatRenewsWithoutChangingActivity(t *testing.T) {
	sup, store, clk := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	sup.Report(context.Background(), "w1", ActivityWaiting)
	*clk = clk.Add(10 * time.Second) // 1010
	if err := sup.Heartbeat("w1"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	if got.Activity != ActivityWaiting {
		t.Fatalf("heartbeat changed activity: %s", got.Activity)
	}
	if !got.Lease.Expiry.Equal(time.Unix(1070, 0).UTC()) { // 1010 + 60
		t.Fatalf("heartbeat did not renew lease: %+v", got.Lease)
	}
	if !got.LastSeen.Equal(time.Unix(1010, 0).UTC()) {
		t.Fatalf("heartbeat did not bump LastSeen: %v", got.LastSeen)
	}
}

func TestHeartbeatErrorsForAbsentInstance(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{})
	if err := sup.Heartbeat("ghost"); err == nil {
		t.Fatal("expected error for absent instance")
	}
}

func TestTeardownNudgesSink(t *testing.T) {
	sup, _, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sink := &fakeSink{}
	sup.SetControlSink(sink)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Teardown(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if len(sink.teardowns) != 1 || sink.teardowns[0] != "w1" {
		t.Fatalf("teardown did not nudge the sink: %+v", sink.teardowns)
	}
}

func TestIdlePausesAndSetsPhaseIdled(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Idle(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if len(f.paused) != 1 || f.paused[0] != "w1" {
		t.Fatalf("launcher Pause not called: %+v", f.paused)
	}
	got, ok := store.GetInstance("w1")
	if !ok {
		t.Fatal("expected instance to still be recorded after Idle")
	}
	if got.Phase != PhaseIdled {
		t.Fatalf("phase = %s, want %s", got.Phase, PhaseIdled)
	}
}

func TestResumeUnpausesSetsLiveAndWaitingSince(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Idle(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	*clk = clk.Add(5 * time.Minute) // now 1300
	if err := sup.Resume(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	if len(f.resumed) != 1 || f.resumed[0] != "w1" {
		t.Fatalf("launcher Unpause not called: %+v", f.resumed)
	}
	got, ok := store.GetInstance("w1")
	if !ok {
		t.Fatal("expected instance to still be recorded after Resume")
	}
	if got.Phase != PhaseLive {
		t.Fatalf("phase = %s, want %s", got.Phase, PhaseLive)
	}
	if !got.WaitingSince.Equal(time.Unix(1300, 0).UTC()) {
		t.Fatalf("WaitingSince = %v, want %v", got.WaitingSince, time.Unix(1300, 0).UTC())
	}
}

func TestSetEscalationPersists(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityWaiting}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1000, 0)
	if err := sup.SetEscalation("cove-1", 2, at); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("cove-1")
	if got.EscalationTier != 2 || !got.TierPingedAt.Equal(at) {
		t.Fatalf("escalation state not persisted: tier=%d at=%v", got.EscalationTier, got.TierPingedAt)
	}
}

func TestReportResetsEscalationOnEnteringWaiting(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	// a cove that was mid-escalation, currently Running
	if err := store.PutInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityRunning, EscalationTier: 3, TierPingedAt: time.Unix(500, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "cove-1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("cove-1")
	if !got.TierPingedAt.IsZero() || got.EscalationTier != 0 {
		t.Fatalf("entering Waiting must reset escalation: tier=%d at=%v", got.EscalationTier, got.TierPingedAt)
	}
}

// TestReconcileSkipsIdledInstance proves that an Idled instance is never
// probed, reaped, or adopted by Reconcile even with an expired lease — a
// paused cove can't heartbeat, so without the skip the reconciler would
// wrongly treat it as dead. The wake-on engine (not Reconcile) owns its
// lifecycle via Resume/Teardown.
func TestReconcileSkipsIdledInstance(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead} // would be reaped if probed
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err := sup.Idle(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	*clk = clk.Add(2 * time.Minute) // lease (60s) now expired
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.probed) != 0 {
		t.Fatalf("Idled instance must not be probed: %+v", f.probed)
	}
	if len(f.tornDown) != 0 {
		t.Fatalf("Idled instance must not be torn down: %+v", f.tornDown)
	}
	got, ok := store.GetInstance("w1")
	if !ok {
		t.Fatal("Idled instance must not be reaped by Reconcile")
	}
	if got.Phase != PhaseIdled {
		t.Fatalf("phase = %s, want %s (Reconcile must not adopt/change it)", got.Phase, PhaseIdled)
	}
}
