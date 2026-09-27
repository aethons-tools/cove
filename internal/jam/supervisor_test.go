package jam

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
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
	egressed    []*EgressPolicy // policies passed to ApplyEgress, in order
	egressErr   error
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
func (f *fakeLauncher) ApplyEgress(_ context.Context, _ Instance, p *EgressPolicy) error {
	f.egressed = append(f.egressed, p)
	return f.egressErr
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

// A personal raise records its owner and session kind on the Instance (and the
// launcher sees them on the spec); an ordinary raise leaves both empty.
func TestRaiseRecordsOwnerAndSessionKind(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "p1", Role: "guest", Owner: "alice", SessionKind: "personal"}); err != nil {
		t.Fatal(err)
	}
	got, ok := store.GetInstance("p1")
	if !ok || got.Owner != "alice" || got.SessionKind != "personal" {
		t.Fatalf("instance = %+v,%v; want owner alice, kind personal", got, ok)
	}
	if f.gotSpec.Owner != "alice" || f.gotSpec.SessionKind != "personal" {
		t.Fatalf("launcher spec = %+v", f.gotSpec)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetInstance("w1"); got.Owner != "" || got.SessionKind != "" {
		t.Fatalf("plain raise instance = %+v; want no owner/kind", got)
	}
}

// A standing raise records its name and kind on the Instance; the name is how
// the standing reconciler matches a cove to its declaration.
func TestRaiseRecordsStandingName(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	id := StandingActorID("default", "guest", "alice-bot")
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Role: "guest", Name: "alice-bot", SessionKind: SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	got, ok := store.GetInstance(id)
	if !ok || got.Name != "alice-bot" || got.SessionKind != SessionKindStanding || got.Owner != "" {
		t.Fatalf("instance = %+v,%v; want name alice-bot, kind standing, no owner", got, ok)
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

func TestRaiseBaselinesCommitSeqFromTailReader(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sup.SetTailReader(&fakeTailReader{seq: 7, ok: true})
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.CommitSeq != 7 {
		t.Fatalf("CommitSeq = %d, want 7 (baselined from tail reader)", inst.CommitSeq)
	}
	if inst.CommitCursor != "" {
		t.Fatalf("CommitCursor = %q, want \"\" (an ordering baseline, not an echoable id — the cove has read nothing yet)", inst.CommitCursor)
	}
	got, _ := store.GetInstance("w1")
	if got.CommitSeq != 7 || got.CommitCursor != "" {
		t.Fatalf("persisted commit cursor = %+v, want CommitSeq=7 CommitCursor=\"\"", got)
	}
}

func TestRaiseCommitSeqZeroWithNoTailReader(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive}) // no SetTailReader call
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	if inst.CommitSeq != 0 || inst.CommitCursor != "" {
		t.Fatalf("commit cursor = %+v, want zero (no tail reader wired)", inst)
	}
	got, _ := store.GetInstance("w1")
	if got.CommitSeq != 0 || got.CommitCursor != "" {
		t.Fatalf("persisted commit cursor = %+v, want zero", got)
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

func TestReportWaitingBaselinesWaitSeq(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.SetWaitSeq("w1", 3); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitingSince.IsZero() {
		t.Fatal("WaitingSince not set on transition into Waiting")
	}
	if inst.WaitSeq != 0 {
		t.Fatalf("WaitSeq should baseline to the log tail (0 here: no tail reader wired) on transition into Waiting, got %d", inst.WaitSeq)
	}

	// A second Report(Waiting) while already Waiting must NOT reset
	// WaitingSince, and must NOT re-baseline (or otherwise clear) a cursor set
	// in between.
	first := inst.WaitingSince
	if err := sup.SetWaitSeq("w1", 4); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ = store.GetInstance("w1")
	if !inst.WaitingSince.Equal(first) {
		t.Fatal("WaitingSince reset while already Waiting")
	}
	if inst.WaitSeq != 4 {
		t.Fatalf("WaitSeq cleared while already Waiting, got %d", inst.WaitSeq)
	}
}

// fakeTailReader is a scripted tailReader standing in for a message log's TailSeq.
type fakeTailReader struct {
	seq int64
	ok  bool
}

func (f *fakeTailReader) TailSeq() (int64, bool) { return f.seq, f.ok }

func TestReportWaitingBaselinesWaitSeqFromTailReader(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sup.SetTailReader(&fakeTailReader{seq: 9, ok: true})
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitSeq != 9 {
		t.Fatalf("WaitSeq = %d, want 9 (baselined from tail reader)", inst.WaitSeq)
	}
}

func TestReportWaitingWaitSeqZeroWithNoTailReader(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive}) // no SetTailReader call
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitSeq != 0 {
		t.Fatalf("WaitSeq = %d, want 0 (no tail reader wired)", inst.WaitSeq)
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

// fakeReleaser records RecordRelease calls for assertions (the actual-state-out
// half of the Supervisor↔Allocator seam).
type fakeReleaser struct{ calls []string }

func (f *fakeReleaser) RecordRelease(_ context.Context, project, role, id string) error {
	f.calls = append(f.calls, project+"/"+role+"/"+id)
	return nil
}

// TestTeardownRecordsRelease proves that tearing a live instance down records a
// ReservationReleased for its (project, role, actorID) via the releaser — the
// shadow-ledger release wiring (slice 3).
func TestTeardownRecordsRelease(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutInstance(Instance{ActorID: "cove-AET-1", Project: "acme", Role: "worker", Phase: PhaseLive, Activity: ActivityRunning}); err != nil {
		t.Fatal(err)
	}
	fr := &fakeReleaser{}
	sup.SetReleaser(fr)
	if err := sup.Teardown(context.Background(), "cove-AET-1"); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 || fr.calls[0] != "acme/worker/cove-AET-1" {
		t.Fatalf("release calls = %v", fr.calls)
	}
}

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

func TestSetEscalationCategoryPersistsAcrossWaitingEntry(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityRunning}); err != nil {
		t.Fatal(err)
	}
	if err := sup.SetEscalationCategory("cove-1", "infra"); err != nil {
		t.Fatal(err)
	}
	// entering Waiting resets tier state but must NOT clear the category
	if err := sup.Report(context.Background(), "cove-1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("cove-1")
	if got.EscalationCategory != "infra" {
		t.Fatalf("category must persist across Waiting-entry, got %q", got.EscalationCategory)
	}
	if !got.TierPingedAt.IsZero() {
		t.Fatal("tier state should still reset on entering Waiting")
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

// A personal raise enrolls the cove with an owner-only addressing override
// (least privilege: it may message its owner and nobody else); a raise with no
// owner keeps the role's addressing (no override).
func TestRaisePersonalGetsOwnerOnlyAddressing(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "p1", Role: "guest", Owner: "alice", SessionKind: SessionKindPersonal}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Unit: "AET-1"}); err != nil {
		t.Fatal(err)
	}
	grants := map[string][]Grant{}
	for _, a := range store.ListActors() {
		grants[a.ID] = a.Grants
	}
	p := grants["p1"]
	if len(p) != 1 || p[0].Overrides == nil || len(p[0].Overrides.Addressing) != 1 || p[0].Overrides.Addressing[0] != "human:alice" {
		t.Fatalf("personal cove grant = %+v; want an Addressing override of exactly [human:alice]", p)
	}
	if w := grants["w1"]; len(w) != 1 || w[0].Overrides != nil {
		t.Fatalf("Requisitioner cove grant = %+v; want no override", w)
	}
}

// RecordNag stamps the nag time and counts the nag on the instance.
func TestRecordNagPersists(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityWaiting}); err != nil {
		t.Fatal(err)
	}
	first, second := time.Unix(5000, 0).UTC(), time.Unix(9000, 0).UTC()
	if err := sup.RecordNag("cove-1", first); err != nil {
		t.Fatal(err)
	}
	if err := sup.RecordNag("cove-1", second); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("cove-1")
	if !got.LastNagAt.Equal(second) || got.Nags != 2 {
		t.Fatalf("nag state = last %v / nags %d, want %v / 2", got.LastNagAt, got.Nags, second)
	}
	if err := sup.RecordNag("absent", first); err == nil {
		t.Fatal("RecordNag on an absent instance should error")
	}
}

// KeepWaiting restarts a Waiting session's idle period without waking it: the
// wait baseline moves past the owner's "keep" reply and the ladder resets.
func TestKeepWaitingRestartsIdlePeriod(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutInstance(Instance{ActorID: "cove-1", Phase: PhaseIdled, Activity: ActivityWaiting,
		WaitSeq: 3, WaitingSince: time.Unix(100, 0), LastNagAt: time.Unix(500, 0), Nags: 2}); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(9000, 0).UTC()
	if err := sup.KeepWaiting("cove-1", 7, at); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("cove-1")
	if got.WaitSeq != 7 || !got.WaitingSince.Equal(at) || !got.LastNagAt.IsZero() || got.Nags != 0 {
		t.Fatalf("after keep: seq %d since %v last %v nags %d", got.WaitSeq, got.WaitingSince, got.LastNagAt, got.Nags)
	}
	if got.Phase != PhaseIdled || got.Activity != ActivityWaiting {
		t.Fatalf("keep must not wake or resume: phase %v activity %v", got.Phase, got.Activity)
	}
	if err := sup.KeepWaiting("absent", 7, at); err == nil {
		t.Fatal("KeepWaiting on an absent instance should error")
	}
	if err := store.PutInstance(Instance{ActorID: "running", Phase: PhaseLive, Activity: ActivityRunning}); err != nil {
		t.Fatal(err)
	}
	if err := sup.KeepWaiting("running", 7, at); err == nil {
		t.Fatal("KeepWaiting on a non-Waiting instance should error")
	}
	if err := store.PutInstance(Instance{ActorID: "gone", Phase: PhaseGone, Activity: ActivityWaiting}); err != nil {
		t.Fatal(err)
	}
	if err := sup.KeepWaiting("gone", 7, at); err == nil {
		t.Fatal("KeepWaiting on a gone instance should error")
	}
}

// A reply runs another turn; the cove's next Waiting period starts a fresh idle
// ladder, so entering Waiting clears the nag state.
func TestReportResetsNagsOnEnteringWaiting(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	if err := store.PutInstance(Instance{ActorID: "cove-1", Phase: PhaseLive, Activity: ActivityRunning, LastNagAt: time.Unix(500, 0), Nags: 3}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "cove-1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("cove-1")
	if !got.LastNagAt.IsZero() || got.Nags != 0 {
		t.Fatalf("entering Waiting must reset nags: last %v / nags %d", got.LastNagAt, got.Nags)
	}
	// Staying Waiting (a repeat report) must not clear nags recorded since.
	if err := sup.RecordNag("cove-1", time.Unix(1500, 0)); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "cove-1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetInstance("cove-1"); got.Nags != 1 {
		t.Fatalf("repeat Waiting report cleared nags: %d", got.Nags)
	}
}

// Raise fills the spec's egress policy from the role (after enrolling),
// overriding anything the caller set: a role with a policy yields a spec carrying
// it, a role without one yields nil (the kit default).
func TestRaiseCarriesRoleEgress(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	if err := store.PutRole("default", Role{Name: "fenced", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour, Egress: &EgressPolicy{Domains: []string{".b.org", "a.com"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "e1", Role: "fenced"}); err != nil {
		t.Fatal(err)
	}
	if f.gotSpec.Egress == nil || !reflect.DeepEqual(f.gotSpec.Egress.Domains, []string{".b.org", "a.com"}) {
		t.Fatalf("launcher spec egress = %+v, want the role's policy", f.gotSpec.Egress)
	}
	// A caller-set policy never survives: the role (kit default here) wins.
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "e2", Role: "guest", Egress: &EgressPolicy{Domains: []string{"evil.example"}}}); err != nil {
		t.Fatal(err)
	}
	if f.gotSpec.Egress != nil {
		t.Fatalf("launcher spec egress = %+v, want nil (role has no policy)", f.gotSpec.Egress)
	}
}
