package jam

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// raiseWorker raises a ticket session the way the Requisitioner does.
func raiseWorker(t *testing.T, sup *Supervisor) Instance {
	t.Helper()
	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest", Unit: "AET-1", Prompt: "do AET-1", Mission: "AET-1"})
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

// A studio found dead (lease expired, probe dead) is not ended: the session
// goes recovering, its studio torn down but its instance, identity slot,
// channels and ticket untouched.
func TestReconcileLostSessionRecovers(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead}
	sup, store, clk := supTestKit(t, f)
	tb, fr := &fakeTicketBlocker{}, &fakeReleaser{}
	sup.SetTicketBlocker(tb)
	sup.SetReleaser(fr)
	raiseWorker(t, sup)
	*clk = clk.Add(2 * time.Minute)
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok := store.GetInstance("w1")
	if !ok || got.Phase != PhaseRecovering || got.Recovery == nil || got.Recovery.Reason != "lost" || !got.Recovery.Next.Equal(clk.Add(RecoveryBackoffInitial)) {
		t.Fatalf("instance = %+v, %v; want recovering", got, ok)
	}
	if len(f.tornDown) != 1 || len(f.purged) != 0 || len(tb.calls) != 0 || len(fr.calls) != 0 {
		t.Fatalf("tornDown=%v purged=%v blocked=%v released=%v", f.tornDown, f.purged, tb.calls, fr.calls)
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("the dead studio's identity must be revoked")
	}
}

// An agent that exits without end (Done) recovers the same way; one that
// asked to end is ended.
func TestReportDoneWithoutEndRecovers(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	raiseWorker(t, sup)
	if err := sup.Report(context.Background(), "w1", ActivityDone); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.GetInstance("w1"); !ok || got.Phase != PhaseRecovering || got.Recovery.Reason != "agent exited" {
		t.Fatalf("instance = %+v, %v; want recovering", got, ok)
	}

	g := &fakeLauncher{liveness: LivenessAlive}
	sup2, store2, _ := supTestKit(t, g)
	raiseWorker(t, sup2)
	if err := sup2.SetEndRequested("w1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := sup2.Report(context.Background(), "w1", ActivityDone); err != nil {
		t.Fatal(err)
	}
	if _, ok := store2.GetInstance("w1"); ok {
		t.Fatal("a session that asked to end is ended, not recovered")
	}
}

// Once due, a recovering session is re-raised under the same id, onto its
// prompt and kind, keeping its inbox cursor, focus and report.
func TestRecoveringSessionReraised(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead}
	sup, store, clk := supTestKit(t, f)
	raiseWorker(t, sup)
	sup.patchInstance("w1", func(i *Instance) {
		i.CommitSeq, i.CommitCursor, i.Focus = 42, "m42", "fixing tests"
		i.Report = &TicketReport{State: ReportInReview, PR: "https://x/pr/1"}
	})
	*clk = clk.Add(2 * time.Minute)
	if err := sup.Reconcile(context.Background()); err != nil { // → recovering
		t.Fatal(err)
	}
	f.liveness = LivenessAlive
	*clk = clk.Add(RecoveryBackoffInitial)
	if err := sup.Reconcile(context.Background()); err != nil { // → re-raised
		t.Fatal(err)
	}
	got, ok := store.GetInstance("w1")
	if !ok || got.Phase != PhaseLive || got.Recovery != nil || !got.Recoverable {
		t.Fatalf("instance = %+v, %v; want live again", got, ok)
	}
	if f.gotSpec.ActorID != "w1" || f.gotSpec.Prompt != "do AET-1" || f.gotSpec.Unit != "AET-1" || f.gotSpec.Mission != "AET-1" {
		t.Fatalf("re-raise spec = %+v", f.gotSpec)
	}
	if got.CommitSeq != 42 || got.CommitCursor != "m42" || got.Focus != "fixing tests" || got.Report == nil || got.Report.State != ReportInReview {
		t.Fatalf("kept state = %+v", got)
	}
	if len(f.raised) != 2 {
		t.Fatalf("raises = %v", f.raised)
	}
}

// A re-raise that keeps failing backs off, then gives up: the session ends
// for good — its ticket blocked and its slot released.
func TestRecoveryGivesUp(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead}
	sup, store, clk := supTestKit(t, f)
	tb, fr := &fakeTicketBlocker{}, &fakeReleaser{}
	sup.SetTicketBlocker(tb)
	sup.SetReleaser(fr)
	raiseWorker(t, sup)
	*clk = clk.Add(2 * time.Minute)
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.raiseErr = errors.New("no manager")
	for i := 0; i < RecoveryMaxAttempts; i++ {
		got, ok := store.GetInstance("w1")
		if !ok {
			t.Fatalf("gave up after %d attempts, want %d", i, RecoveryMaxAttempts)
		}
		*clk = got.Recovery.Next
		if err := sup.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("still recovering after the last attempt")
	}
	if len(tb.calls) != 1 || tb.calls[0] != "AET-1:re-raise gave up" || len(fr.calls) != 1 {
		t.Fatalf("blocked=%v released=%v", tb.calls, fr.calls)
	}
}

// A session raised before recovery existed is ended as before.
func TestLegacySessionNotRecovered(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead}
	sup, store, clk := supTestKit(t, f)
	if err := store.PutInstance(Instance{ActorID: "old", Project: "default", Role: "guest", Phase: PhaseLive, Lease: Lease{Holder: "holder-A", Expiry: clk.Add(time.Second)}}); err != nil {
		t.Fatal(err)
	}
	*clk = clk.Add(2 * time.Minute)
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("old"); ok {
		t.Fatal("a legacy session must be torn down, not recovered")
	}
}

// A studio lost with its manager (removed, or force-revoked) is ended, not
// recovered: its state went with the manager.
func TestLostWithItsManagerNotRecovered(t *testing.T) {
	s := &recoverySpy{}
	sup, store, _ := supTestKit(t, s)
	inst := raiseWorker(t, sup)
	sup.patchInstance(inst.ActorID, func(i *Instance) { i.Phase, i.Manager = PhaseLost, "m-gone" })
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("a session lost with its manager must be ended")
	}
}

// recoverySpy is a placing fakeLauncher that knows no managers.
type recoverySpy struct{ fakeLauncher }

func (r *recoverySpy) RaiseOn(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (Placed, error) {
	loc, err := r.Raise(ctx, spec, creds)
	return Placed{Location: loc}, err
}
func (r *recoverySpy) PrepareKitOn(ctx context.Context, _ string, def KitDefinition) (KitStatus, error) {
	return r.PrepareKit(ctx, def)
}
func (r *recoverySpy) ImageTagOn(_ string, ref KitRef) string { return r.ImageTag(ref) }
func (r *recoverySpy) HasManager(string) bool                 { return false }

// A dying stream's late report doesn't touch a recovering session.
func TestRecoveringIgnoresStaleReports(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	raiseWorker(t, sup)
	if err := sup.Report(context.Background(), "w1", ActivityDone); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err == nil {
		t.Fatal("a report for a recovering session must be refused")
	}
	if err := sup.Heartbeat("w1"); err == nil {
		t.Fatal("a heartbeat for a recovering session must be refused")
	}
	if got, _ := store.GetInstance("w1"); got.Phase != PhaseRecovering || got.Activity != "" || !got.Lease.Expiry.IsZero() {
		t.Fatalf("instance = %+v", got)
	}
}

// Restart tears a session's studio down and recovers it at once, keeping the
// session; a standing one is torn down for its reconciler to re-raise; a
// legacy one can't be restarted.
func TestRestartRecoversAtOnce(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	raiseWorker(t, sup)
	if err := sup.Restart(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	if got.Phase != PhaseRecovering || got.Recovery.Reason != "restarted" || !got.Recovery.Next.Equal(*clk) {
		t.Fatalf("after restart = %+v", got)
	}
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetInstance("w1"); got.Phase != PhaseLive {
		t.Fatalf("not re-raised: %+v", got)
	}

	if err := store.PutInstance(Instance{ActorID: "old", Project: "default", Role: "guest", Phase: PhaseLive}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Restart(context.Background(), "old"); !errors.Is(err, ErrNotRestartable) {
		t.Fatalf("legacy restart = %v", err)
	}
}

// raceLauncher runs during a Raise whatever onRaise does (say, an operator's
// End landing while a re-raise is in flight).
type raceLauncher struct {
	fakeLauncher
	onRaise func()
}

func (r *raceLauncher) Raise(ctx context.Context, spec RaiseSpec, creds LaunchCreds) (string, error) {
	if r.onRaise != nil {
		f := r.onRaise
		r.onRaise = nil
		f()
	}
	return r.fakeLauncher.Raise(ctx, spec, creds)
}

// A session ended while its re-raise is in flight stays ended: the re-raise
// rolls its new studio and identity back instead of reviving it.
func TestEndDuringReraiseStaysEnded(t *testing.T) {
	l := &raceLauncher{fakeLauncher: fakeLauncher{liveness: LivenessDead}}
	sup, store, clk := supTestKit(t, l)
	raiseWorker(t, sup)
	*clk = clk.Add(2 * time.Minute)
	if err := sup.Reconcile(context.Background()); err != nil { // → recovering
		t.Fatal(err)
	}
	l.onRaise = func() {
		if err := sup.Teardown(context.Background(), "w1"); err != nil {
			t.Error(err)
		}
	}
	*clk = clk.Add(RecoveryBackoffInitial)
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.GetInstance("w1"); ok {
		t.Fatalf("ended session revived: %+v", got)
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("the rolled-back re-raise left an identity behind")
	}
}

// failTeardownLauncher fails its next teardowns.
type failTeardownLauncher struct {
	fakeLauncher
	fails int
}

func (f *failTeardownLauncher) Teardown(ctx context.Context, inst Instance) error {
	if f.fails > 0 {
		f.fails--
		return errors.New("docker hiccup")
	}
	return f.fakeLauncher.Teardown(ctx, inst)
}

// An agent that exits without end, whose studio then fails to tear down, is
// retried by the next reconcile pass rather than stranded.
func TestDoneRecoveryRetriedAfterTeardownFailure(t *testing.T) {
	l := &failTeardownLauncher{fakeLauncher: fakeLauncher{liveness: LivenessAlive}}
	sup, store, _ := supTestKit(t, l)
	raiseWorker(t, sup)
	l.fails = 1
	_ = sup.Report(context.Background(), "w1", ActivityDone) // the attach server ignores this error
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetInstance("w1"); got.Phase != PhaseRecovering {
		t.Fatalf("after a failed teardown and a reconcile pass = %+v; want recovering", got)
	}
}

// Waiting on infrastructure (a kit still building, no manager available)
// doesn't use up re-raise attempts: only the age limit ends such a recovery.
func TestRecoveryTransientFailuresDontCount(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessDead}
	sup, store, clk := supTestKit(t, f)
	raiseWorker(t, sup)
	*clk = clk.Add(2 * time.Minute)
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.raiseErr = fmt.Errorf("%w: m1", ErrManagerUnavailable)
	for i := 0; i < RecoveryMaxAttempts+3; i++ {
		got, ok := store.GetInstance("w1")
		if !ok {
			t.Fatalf("gave up after %d transient failures", i)
		}
		*clk = got.Recovery.Next
		if err := sup.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := store.GetInstance("w1")
	if got.Recovery == nil || got.Recovery.Attempts != 0 {
		t.Fatalf("transient failures counted: %+v", got.Recovery)
	}
	*clk = got.Recovery.Since.Add(RecoveryMaxAge)
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("still recovering past the age limit")
	}
}

// Restart only restarts a running (or paused, or recovering) session: one
// being ended, or that asked to end, is refused.
func TestRestartRefusesEndingSessions(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	raiseWorker(t, sup)
	sup.patchInstance("w1", func(i *Instance) { i.Phase = PhaseTerminating })
	if err := sup.Restart(context.Background(), "w1"); err == nil {
		t.Fatal("restart of a terminating session must be refused")
	}
	sup.patchInstance("w1", func(i *Instance) { i.Phase, i.EndRequested = PhaseLive, &EndRequest{Reason: "done"} })
	if err := sup.Restart(context.Background(), "w1"); !errors.Is(err, ErrEnding) {
		t.Fatalf("restart of a session that asked to end = %v", err)
	}
	if got, _ := store.GetInstance("w1"); got.Phase == PhaseRecovering {
		t.Fatal("refused restart still recovered the session")
	}
}

// A recovering session whose manager goes away is ended, not re-raised
// elsewhere with empty state.
func TestRecoveringOnARemovedManagerGivesUp(t *testing.T) {
	s := &recoverySpy{}
	sup, store, clk := supTestKit(t, s)
	raiseWorker(t, sup)
	sup.patchInstance("w1", func(i *Instance) {
		i.Phase, i.Manager = PhaseRecovering, "m-gone"
		i.Recovery = &Recovery{Since: *clk, Next: *clk}
	})
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.GetInstance("w1"); ok {
		t.Fatal("a recovering session on a removed manager must be ended")
	}
	if len(s.raised) != 1 {
		t.Fatalf("re-raised elsewhere: %v", s.raised)
	}
}
