package jam

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
	"github.com/aethons-tools/cove/internal/studio"
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

	// onProbe / onEgress run inside Probe / ApplyEgress, simulating a concurrent
	// write (e.g. a cove's connector report) landing mid-Reconcile.
	onProbe  func(Instance)
	onEgress func(Instance)

	// Kit-prepare scripting: notReadyOnce makes the first Raise return
	// ErrKitNotReady until a PrepareKit lands; prepareCalls counts PrepareKit and
	// preparedDef records the last definition it received; prepareState is the
	// status PrepareKit reports (zero value KitPreparing → set KitReady to succeed).
	notReadyOnce bool
	prepareCalls int
	preparedDef  KitDefinition
	prepareState KitState
	prepareErr   error
}

func (f *fakeLauncher) Raise(_ context.Context, spec RaiseSpec, creds LaunchCreds) (string, error) {
	f.gotSpec, f.gotCreds = spec, creds
	if f.raiseErr != nil {
		return "", f.raiseErr
	}
	if f.notReadyOnce && f.prepareCalls == 0 {
		return "", fmt.Errorf("raise %s: %w", spec.Kit, ErrKitNotReady)
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
	if f.onProbe != nil {
		f.onProbe(inst)
	}
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
func (f *fakeLauncher) ApplyEgress(_ context.Context, inst Instance, p *EgressPolicy) error {
	f.egressed = append(f.egressed, p)
	if f.onEgress != nil {
		f.onEgress(inst)
	}
	return f.egressErr
}
func (f *fakeLauncher) PrepareKit(_ context.Context, def KitDefinition) (KitStatus, error) {
	f.prepareCalls++
	f.preparedDef = def
	if f.prepareErr != nil {
		return KitStatus{State: KitPreparing, Err: f.prepareErr.Error()}, f.prepareErr
	}
	return KitStatus{State: f.prepareState}, nil
}

// supTestKit builds a supervisor over a temp store with a guest role, a fixed
// clock, and the given launcher. Returns the supervisor, store, and a pointer to
// the mutable clock.
func supTestKit(t *testing.T, l Launcher) (*Supervisor, Store, *time.Time) {
	t.Helper()
	store := NewMemStore()
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

// A studio supervisor stamps the default KitRef on the raise; when the launcher
// reports ErrKitNotReady it resolves the definition from the registry, calls
// PrepareKit once, and retries the raise, which then succeeds.
func TestSupervisorPreparesKitOnNotReadyThenRaises(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, notReadyOnce: true, prepareState: KitReady}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}}
	ref, err := EnsureStudioKit(store, "base", sk)
	if err != nil {
		t.Fatal(err)
	}
	sup.SetDefaultStudioKit(ref)

	inst, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "spider", Project: "default", Role: "guest"})
	if err != nil {
		t.Fatalf("Raise after prepare: %v", err)
	}
	if fl.prepareCalls != 1 {
		t.Fatalf("PrepareKit called %d times, want 1", fl.prepareCalls)
	}
	if len(fl.raised) != 1 || inst.Location == "" {
		t.Fatalf("cove not raised after prepare+retry: raised=%v inst=%+v", fl.raised, inst)
	}
	if fl.gotSpec.Kit != ref {
		t.Fatalf("raise spec.Kit = %v, want the default studio ref %v", fl.gotSpec.Kit, ref)
	}
	if fl.preparedDef.Ref != ref || !slices.Contains(fl.preparedDef.Kit.Egress, "github.com") {
		t.Fatalf("PrepareKit got the wrong definition: %+v", fl.preparedDef)
	}
	if _, ok := store.GetInstance("spider"); !ok {
		t.Fatal("prepared+raised cove must be recorded")
	}
}

// A PrepareKit that is still preparing (async remote build) must NOT block the
// raise: the raise fails cleanly (retried on the next reconcile) and leaves no
// dangling identity or instance.
func TestSupervisorDefersWhenKitPreparing(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive, notReadyOnce: true, prepareState: KitPreparing}
	sup, store, _ := supTestKit(t, fl)
	ref, err := EnsureStudioKit(store, "base", studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}})
	if err != nil {
		t.Fatal(err)
	}
	sup.SetDefaultStudioKit(ref)

	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "spider", Project: "default", Role: "guest"}); err == nil {
		t.Fatal("expected the raise to defer (error) while the kit is preparing")
	}
	if len(fl.raised) != 0 {
		t.Fatal("no cove may be raised while the kit is still preparing")
	}
	if len(store.ListActors()) != 0 {
		t.Fatal("a deferred raise must roll back the identity")
	}
	if _, ok := store.GetInstance("spider"); ok {
		t.Fatal("a deferred raise must record no instance")
	}
}

// A role that names a kit raises from that registered studio kit, not the
// wiring-set default studio kit.
func TestRaiseUsesRoleStudioKit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Egress: []string{"github.com"}}
	if _, err := EnsureStudioKit(store, "web", sk); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev"}); err != nil {
		t.Fatal(err)
	}
	if fl.gotSpec.Kit.ID != "web" || fl.gotSpec.Kit.Digest != studio.BuildDigest(sk) {
		t.Fatalf("raise spec.Kit = %+v, want the studio ref for web", fl.gotSpec.Kit)
	}
}

// The session context is compiled at raise; the prompt stays the launch text.
// A kit stored with an over-budget prompt (before the budget existed) still
// raises: the push path rejects new ones, Compile truncates old ones.
func TestRaiseStoredOverBudgetKitTruncates(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Prompt: strings.Repeat("old kit line\n", 100)}
	text, err := sk.ToJSON()
	if err != nil {
		t.Fatal(err)
	}
	v, err := store.PushKit("legacy", string(text))
	if err != nil {
		t.Fatal(err)
	}
	sup.SetDefaultStudioKit(KitRef{ID: "legacy", Version: v, Digest: studio.BuildDigest(sk)})
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"}); err != nil {
		t.Fatalf("a stored over-budget kit must still raise: %v", err)
	}
	if c := fl.gotSpec.Context; c == nil || !strings.Contains(c.Core, "(truncated — see kit/CORE-full.md)") {
		t.Fatalf("want a truncated kit core: %+v", c)
	}
}

func TestPushStudioKitRejectsOverBudgetPrompt(t *testing.T) {
	_, store, _ := supTestKit(t, &fakeLauncher{})
	cfg := "kind: studio\nprompt: " + strings.Repeat("x", 801) + "\n"
	if _, _, err := PushStudioKit(store, "big", cfg); err == nil || !strings.Contains(err.Error(), "801 bytes") {
		t.Fatalf("push must reject an over-budget prompt, got %v", err)
	}
}

// The unit reaches the boilerplate: with one, `send` defaults to the ticket.
func TestRaiseContextCarriesUnit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, _, _ := supTestKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Unit: "AET-9", Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	if c := fl.gotSpec.Context; c == nil || !strings.Contains(c.Core, "posts to your ticket") {
		t.Fatalf("want ticket default with a unit: %+v", c)
	}
}

func TestRaiseCompilesContext(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Prompt: "KITLAYER"}
	ref, _ := EnsureStudioKit(store, "web", sk)
	sup.SetDefaultStudioKit(ref)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "LAUNCHLAYER", Name: "bot", SessionKind: SessionKindStanding}); err != nil {
		t.Fatal(err)
	}
	if fl.gotSpec.Prompt != "LAUNCHLAYER" {
		t.Fatalf("prompt must be the launch text only, got %q", fl.gotSpec.Prompt)
	}
	c := fl.gotSpec.Context
	if c == nil || !strings.Contains(c.Core, "KITLAYER") || !strings.Contains(c.Core, `standing session "bot"`) || !strings.Contains(c.Core, "## Kit — web@v") {
		t.Fatalf("context missing layers: %+v", c)
	}
}

// A raise with no kit still gets the boilerplate.
func TestRaiseContextWithoutKit(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, _, _ := supTestKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	if c := fl.gotSpec.Context; c == nil || !strings.Contains(c.Core, "## Boilerplate") || strings.Contains(c.Core, "## Kit") {
		t.Fatalf("want boilerplate only: %+v", c)
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

// Entering Waiting keeps the wake-on baseline: a reply that landed while the
// cove was Running (e.g. during an episode's background hold) and was not yet
// woken for must still wake it once it waits. WaitingSince still restarts.
func TestReportWaitingKeepsWaitSeq(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	tail := &fakeTailReader{seq: 9, ok: true}
	sup.SetTailReader(tail)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.SetWaitSeq("w1", 3); err != nil {
		t.Fatal(err)
	}
	tail.seq = 12 // a reply (Seq 10..12) arrived while Running
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitingSince.IsZero() {
		t.Fatal("WaitingSince not set on transition into Waiting")
	}
	if inst.WaitSeq != 3 {
		t.Fatalf("WaitSeq = %d, want 3 (kept on entering Waiting, not re-baselined past replies)", inst.WaitSeq)
	}

	// A second Report(Waiting) while already Waiting must NOT reset
	// WaitingSince, nor touch a cursor set in between.
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

// Entering Running baselines WaitSeq to the log tail: the run that starts now
// reads its inbox itself, so only later replies need a Wake. A Running report
// while already Running leaves the baseline alone.
func TestReportRunningBaselinesWaitSeq(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	tail := &fakeTailReader{seq: 2, ok: true}
	sup.SetTailReader(tail)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Report(context.Background(), "w1", ActivityWaiting); err != nil {
		t.Fatal(err)
	}
	tail.seq = 7
	if err := sup.Report(context.Background(), "w1", ActivityRunning); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitSeq != 7 {
		t.Fatalf("WaitSeq = %d, want 7 (baselined to the tail on entering Running)", inst.WaitSeq)
	}
	tail.seq = 9
	if err := sup.Report(context.Background(), "w1", ActivityRunning); err != nil {
		t.Fatal(err)
	}
	inst, _ = store.GetInstance("w1")
	if inst.WaitSeq != 7 {
		t.Fatalf("WaitSeq = %d, want 7 (unchanged while already Running)", inst.WaitSeq)
	}
}

// fakeTailReader is a scripted tailReader standing in for a message log's TailSeq.
type fakeTailReader struct {
	seq int64
	ok  bool
}

func (f *fakeTailReader) TailSeq() (int64, bool) { return f.seq, f.ok }

// Raise baselines WaitSeq to the log tail: the cove starts Running and reads
// its inbox itself, so only later replies need a Wake.
func TestRaiseBaselinesWaitSeqFromTailReader(t *testing.T) {
	sup, store, _ := supTestKit(t, &fakeLauncher{liveness: LivenessAlive})
	sup.SetTailReader(&fakeTailReader{seq: 9, ok: true})
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	inst, _ := store.GetInstance("w1")
	if inst.WaitSeq != 9 {
		t.Fatalf("WaitSeq = %d, want 9 (baselined from tail reader at raise)", inst.WaitSeq)
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
	real := NewMemStore()
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

// A connector report landing while Reconcile is probing must survive the
// adopt write: the adopt touches only the lease, on a fresh read.
func TestReconcileAdoptKeepsConcurrentConnectorReport(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	inst, _ := store.GetInstance("w1")
	inst.Lease = Lease{Holder: "holder-B", Expiry: time.Unix(900, 0).UTC()}
	store.PutInstance(inst)
	*clk = clk.Add(1 * time.Minute)
	f.onProbe = func(inst Instance) {
		if err := sup.RecordConnector(inst.ActorID, "fp-new"); err != nil {
			t.Fatal(err)
		}
	}
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := store.GetInstance("w1")
	if got.Connector != "fp-new" {
		t.Fatalf("connector = %q, want the report that landed mid-pass", got.Connector)
	}
	if got.Lease.Holder != "holder-A" {
		t.Fatalf("lease not adopted: %+v", got.Lease)
	}
}

// A connector report landing while Reconcile re-applies another cove's egress
// must survive that cove's own lease renew (written from the pass's snapshot
// before the fix).
func TestReconcileRenewKeepsConcurrentConnectorReport(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, clk := supTestKit(t, f)
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"})
	sup.Raise(context.Background(), RaiseSpec{ActorID: "w2", Role: "guest"})
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic"}, TTL: time.Hour,
		Egress: &EgressPolicy{Domains: []string{"a.com"}}}}); err != nil {
		t.Fatal(err)
	}
	*clk = clk.Add(10 * time.Second) // leases still ours and live: the renew path
	once := false
	f.onEgress = func(Instance) {
		if once { // the first re-apply only, so a later one cannot re-record over a clobber
			return
		}
		once = true
		for _, id := range []string{"w1", "w2"} {
			if err := sup.RecordConnector(id, "fp-"+id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := sup.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"w1", "w2"} {
		got, _ := store.GetInstance(id)
		if got.Connector != "fp-"+id {
			t.Fatalf("%s connector = %q, want the report that landed mid-pass", id, got.Connector)
		}
		if !got.Lease.Expiry.Equal(time.Unix(1070, 0).UTC()) {
			t.Fatalf("%s lease not renewed: %+v", id, got.Lease)
		}
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
	store := NewMemStore()
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

func TestRaiseHandsTheRolesConnectorToTheLauncher(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	if err := store.AddDestination(legacyAnthropic); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err != nil {
		t.Fatal(err)
	}
	if c := f.gotSpec.Connector; c == nil || c.Env["ANTHROPIC_API_KEY"] != "{token}" {
		t.Fatalf("launcher got connector %+v", c)
	}
}

func TestRaiseConnectorConflictRollsBack(t *testing.T) {
	f := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, f)
	a := Destination{Name: "anthropic", Route: "/anthropic/", Upstream: "https://a", Env: map[string]string{"X": "1"}}
	b := Destination{Name: "b", Route: "/b/", Upstream: "https://b", Env: map[string]string{"X": "2"}}
	for _, d := range []Destination{a, b} {
		if err := store.AddDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{Destinations: []string{"anthropic", "b"}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Role: "guest"}); err == nil {
		t.Fatal("conflicting connector must fail the raise")
	}
	if len(f.raised) != 0 || len(store.ListActors()) != 0 {
		t.Fatalf("raise must roll back: raised=%v actors=%v", f.raised, store.ListActors())
	}
}

// A personal session's Studio layer names its owner as its one target.
func TestRaiseContextStudioNamesOwner(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	if err := store.AddHuman("default", Human{Name: "alice", Handle: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "p1", Project: "default", Role: "guest", Owner: "alice", SessionKind: SessionKindPersonal, Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	if c := fl.gotSpec.Context; c == nil || !strings.Contains(c.Core, "`human:alice` — your owner") {
		t.Fatalf("studio layer must name the owner: %+v", c)
	}
}

// Authored layers reach the session context; a cleared one vanishes.
func TestRaiseCompilesAuthoredLayers(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	if err := SetRoleContext(store, "default", "guest", sessionctx.Layer{Core: "ROLE-RULES"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetProjectContext("default", sessionctx.Layer{Core: "PROJECT-GOALS"}, []sessionctx.Resource{{Name: "cove", Kind: "repo", Ref: "aethons-tools/cove"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetJamContext(sessionctx.Layer{Core: "JAM-RULES"}); err != nil {
		t.Fatal(err)
	}
	raise := func(id string) *sessionctx.Bundle {
		if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: id, Project: "default", Role: "guest", Prompt: "P"}); err != nil {
			t.Fatal(err)
		}
		return fl.gotSpec.Context
	}
	c := raise("w1")
	for _, want := range []string{"ROLE-RULES", "PROJECT-GOALS", "JAM-RULES", "project/resources.md"} {
		if !strings.Contains(c.Core, want) {
			t.Errorf("context missing %q", want)
		}
	}
	if !strings.Contains(c.Files["project/resources.md"], "aethons-tools/cove") {
		t.Error("resources leaf missing")
	}
	if err := store.SetJamContext(sessionctx.Layer{}); err != nil {
		t.Fatal(err)
	}
	if c := raise("w2"); strings.Contains(c.Core, "## Jam") {
		t.Error("a cleared layer must vanish")
	}
}

func TestPushStudioKitRejectsReservedNote(t *testing.T) {
	_, store, _ := supTestKit(t, &fakeLauncher{})
	cfg := "kind: studio\nnotes:\n  - name: tools.md\n    read-when: w\n    body: B\n"
	if _, _, err := PushStudioKit(store, "noted", cfg); WriteStatus(err, 0) != http.StatusBadRequest || !strings.Contains(err.Error(), "tools.md") {
		t.Fatalf("push must reject a note named tools.md, got %v", err)
	}
}

// The kit layer carries the kit's notes and a generated tools.md.
func TestRaiseKitNotesAndTools(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	sk := studio.StudioKit{Kind: studio.Kind, Prompt: "K", BuildArgs: map[string]string{"GO_VERSION": "1.27.1"},
		Notes: []studio.KitNote{{Name: "release.md", ReadWhen: "you are releasing", Body: "STEPS"}}}
	ref, err := EnsureStudioKit(store, "web", sk)
	if err != nil {
		t.Fatal(err)
	}
	sup.SetDefaultStudioKit(ref)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	c := fl.gotSpec.Context
	if !strings.Contains(c.Files["kit/tools.md"], "| go | 1.27.1 |") || !strings.Contains(c.Files["kit/release.md"], "STEPS") {
		t.Fatalf("kit leaves missing: %v", c.Files)
	}
}

// ContextFor recompiles a running session's bundle from current config.
func TestContextForTracksEdits(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Name: "bot", SessionKind: SessionKindStanding, Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	raised := *fl.gotSpec.Context
	got, err := sup.ContextFor("w1")
	if err != nil || got.Fingerprint != raised.Fingerprint {
		t.Fatalf("unchanged config must give the raise bundle: %v (%s vs %s)", err, got.Fingerprint, raised.Fingerprint)
	}
	if err := SetRoleContext(store, "default", "guest", sessionctx.Layer{Core: "NEW RULE"}); err != nil {
		t.Fatal(err)
	}
	got, _ = sup.ContextFor("w1")
	if !strings.Contains(got.Core, "NEW RULE") || !strings.Contains(got.Core, `standing session "bot"`) {
		t.Fatalf("edit not reflected, or session facts lost:\n%s", got.Core)
	}
	if _, err := sup.ContextFor("nobody"); !errors.Is(err, ErrNoInstance) {
		t.Fatalf("unknown actor = %v, want ErrNoInstance", err)
	}
}

// A running session keeps the kit it was raised with: a newer kit version's
// build-args and egress ceiling (the image doesn't have them) never reach its
// refreshed context, while that version's prompt edits do.
func TestContextForKeepsRaisedKitImage(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	v1 := studio.StudioKit{Kind: studio.Kind, Prompt: "V1", Egress: []string{"one.example"}, BuildArgs: map[string]string{"GO_VERSION": "1.0"}}
	ref1, _ := EnsureStudioKit(store, "web", v1)
	sup.SetDefaultStudioKit(ref1)
	if err := store.PutRole("default", Role{Name: "dev", Kit: "web", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev", Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	if inst, _ := store.GetInstance("w1"); inst.Kit.ID != "web" || inst.Kit.Version != ref1.Version {
		t.Fatalf("instance must record the raised kit: %+v", inst.Kit)
	}
	v2 := studio.StudioKit{Kind: studio.Kind, Prompt: "V2", Egress: []string{"two.example"}, BuildArgs: map[string]string{"GO_VERSION": "2.0"}}
	if _, err := EnsureStudioKit(store, "web", v2); err != nil {
		t.Fatal(err)
	}
	got, err := sup.ContextFor("w1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Core, "V2") {
		t.Errorf("the kit's prompt edits must reach the session:\n%s", got.Core)
	}
	if !strings.Contains(got.Files["kit/tools.md"], "| go | 1.0 |") || !strings.Contains(got.Core, "one.example") || strings.Contains(got.Core, "two.example") {
		t.Errorf("build-args and egress must stay the raised image's:\n%s\n%s", got.Core, got.Files["kit/tools.md"])
	}
	if !strings.Contains(got.Core, "## Kit — "+ref1.String()) {
		t.Errorf("the kit header names the raised image: %s", got.Core)
	}
}

// Lint warnings are logged at raise, not on every refresh.
func TestContextForDoesNotRelogLint(t *testing.T) {
	var buf strings.Builder
	store := NewMemStore()
	if err := store.PutRole("default", Role{Name: "guest", Scope: Scope{TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	clk := time.Unix(1000, 0).UTC()
	sup := NewSupervisor(store, &fakeLauncher{liveness: LivenessAlive}, "h", time.Minute, time.Minute, func() time.Time { return clk }, slog.New(slog.NewTextHandler(&buf, nil)))
	if err := store.SetJamContext(sessionctx.Layer{Core: strings.Repeat("x", sessionctx.BudgetJam+50)}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "guest", Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	n := strings.Count(buf.String(), "session context")
	if n == 0 {
		t.Fatal("raise must log the truncation warning")
	}
	if _, err := sup.ContextFor("w1"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(buf.String(), "session context") != n {
		t.Fatalf("refresh re-logged the warnings:\n%s", buf.String())
	}
}

// End to end through Raise: a credentialed destination in scope never puts its
// credential name or env templates into the session context.
func TestRaiseContextCarriesNoSecrets(t *testing.T) {
	fl := &fakeLauncher{liveness: LivenessAlive}
	sup, store, _ := supTestKit(t, fl)
	if err := store.AddDestination(Destination{Name: "gh", Route: "/api/v3/", Upstream: "https://api.github.com", CredName: "gh-pat-SECRETNAME",
		Env: map[string]string{"GH_ENTERPRISE_TOKEN": "{token}", "GH_HOST": "{host}"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRole("default", Role{Name: "dev", Scope: Scope{Destinations: []string{"gh"}, Credentials: map[string]string{"gh": "role-SECRETCRED"}, TTL: time.Hour}}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sup.Raise(context.Background(), RaiseSpec{ActorID: "w1", Project: "default", Role: "dev", Prompt: "P"}); err != nil {
		t.Fatal(err)
	}
	c := fl.gotSpec.Context
	all := c.Core
	for _, f := range c.Files {
		all += f
	}
	if !strings.Contains(all, "GH_ENTERPRISE_TOKEN") {
		t.Fatal("env key names belong in the studio layer")
	}
	for _, secret := range []string{"SECRETNAME", "SECRETCRED", "{token}", "{host}"} {
		if strings.Contains(all, secret) {
			t.Errorf("context leaks %q", secret)
		}
	}
}
