package standing

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/jam"
)

// fakeWorld is the roster, the registry and the supervisor: Raise adds a Live
// instance (unless the actor id is set to fail), Teardown removes it.
type fakeWorld struct {
	roles     map[string][]jam.Role // project → roles
	insts     map[string]jam.Instance
	raised    []jam.RaiseSpec
	torn      []string
	purged    []string        // successful PurgeState ids, in order
	state     map[string]bool // actor ids with persisted state (a standing raise creates it)
	failPurge map[string]bool // PurgeState of these ids fails
	failTear  map[string]bool // Teardown of these ids fails
	failRaise map[string]bool
	tag       string // the image tag Raise records (the "current" image)
}

func newWorld() *fakeWorld {
	return &fakeWorld{roles: map[string][]jam.Role{}, insts: map[string]jam.Instance{}, failRaise: map[string]bool{},
		state: map[string]bool{}, failPurge: map[string]bool{}, failTear: map[string]bool{}}
}

func (w *fakeWorld) ListProjects() []string {
	var out []string
	for p := range w.roles {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func (w *fakeWorld) ListRoles(project string) []jam.Role { return w.roles[project] }

func (w *fakeWorld) ListInstances() []jam.Instance {
	var out []jam.Instance
	for _, i := range w.insts {
		out = append(out, i)
	}
	return out
}

func (w *fakeWorld) Raise(_ context.Context, spec jam.RaiseSpec) (jam.Instance, string, string, error) {
	w.raised = append(w.raised, spec)
	if w.failRaise[spec.ActorID] {
		return jam.Instance{}, "", "", errors.New("launch failed")
	}
	inst := jam.Instance{ActorID: spec.ActorID, Project: spec.Project, Role: spec.Role, Name: spec.Name, Owner: spec.Owner, SessionKind: spec.SessionKind, Phase: jam.PhaseLive, ImageTag: w.tag}
	w.insts[spec.ActorID] = inst
	if spec.SessionKind == jam.SessionKindStanding {
		w.state[spec.ActorID] = true
	}
	return inst, "tok", "secret", nil
}

func (w *fakeWorld) Teardown(_ context.Context, id string) error {
	if w.failTear[id] {
		return errors.New("teardown failed")
	}
	w.torn = append(w.torn, id)
	delete(w.insts, id)
	return nil
}

// PurgeState refuses state still in use by a live cove, like docker.
func (w *fakeWorld) PurgeState(_ context.Context, id string) error {
	if _, live := w.insts[id]; live || w.failPurge[id] {
		return errors.New("volume in use")
	}
	delete(w.state, id)
	w.purged = append(w.purged, id)
	return nil
}

func (w *fakeWorld) StateOwners(context.Context) ([]string, error) {
	var out []string
	for id := range w.state {
		out = append(out, id)
	}
	slices.Sort(out)
	return out, nil
}

// declare sets project/role's standing declarations.
func (w *fakeWorld) declare(project, role string, ss ...jam.StandingSession) {
	rs := w.roles[project]
	for i := range rs {
		if rs[i].Name == role {
			rs[i].Allocation.Standing = ss
			return
		}
	}
	w.roles[project] = append(rs, jam.Role{Name: role, Allocation: jam.RoleAllocation{Standing: ss}})
}

type fakeGranter struct {
	deny     bool
	err      error
	grants   []allocator.Request
	releases []string
}

func (g *fakeGranter) Grant(_ context.Context, req allocator.Request) (bool, error) {
	g.grants = append(g.grants, req)
	return !g.deny && g.err == nil, g.err
}

func (g *fakeGranter) RecordRelease(_ context.Context, project, role, id string) error {
	g.releases = append(g.releases, id)
	return nil
}

func kit() (*Reconciler, *fakeWorld, *fakeGranter, func(time.Duration)) {
	w, g := newWorld(), &fakeGranter{}
	r := New(w, w, g, w, 0, nil)
	now := time.Unix(1_000_000, 0)
	r.now = func() time.Time { return now }
	return r, w, g, func(d time.Duration) { now = now.Add(d) }
}

var bot = jam.StandingSession{Name: "alice-bot", Prompt: "review every PR"}

const botID = "standing-acme-reviewer-alice-bot"

// A declared name with no cove is granted (standing, by name) and raised with
// the preamble, under its per-name actor id.
func TestTick_RaisesDeclaredName(t *testing.T) {
	r, w, g, _ := kit()
	w.declare("acme", "reviewer", bot)
	r.Tick(context.Background())

	want := allocator.Request{Project: "acme", Role: "reviewer", ReservationID: botID, Kind: allocator.SessionStanding, Name: "alice-bot"}
	if len(g.grants) != 1 || g.grants[0] != want {
		t.Fatalf("grants = %+v, want [%+v]", g.grants, want)
	}
	if len(w.raised) != 1 {
		t.Fatalf("raised = %+v, want one", w.raised)
	}
	spec := w.raised[0]
	if spec.ActorID != botID || spec.Project != "acme" || spec.Role != "reviewer" || spec.Name != "alice-bot" || spec.SessionKind != jam.SessionKindStanding || spec.Owner != "" {
		t.Fatalf("spec = %+v", spec)
	}
	// The declared prompt is delivered as-is; the standing preamble is now the
	// Boilerplate layer of the session context (sessionctx).
	wantPrompt := "review every PR"
	if spec.Prompt != wantPrompt {
		t.Fatalf("prompt =\n%s\nwant\n%s", spec.Prompt, wantPrompt)
	}
}

// A live cove is left alone: no grant, no raise.
func TestTick_LiveLeftAlone(t *testing.T) {
	r, w, g, _ := kit()
	w.declare("acme", "reviewer", bot)
	r.Tick(context.Background())
	r.Tick(context.Background())
	if len(g.grants) != 1 || len(w.raised) != 1 || len(w.torn) != 0 {
		t.Fatalf("second tick acted on a live cove: grants=%d raised=%d torn=%v", len(g.grants), len(w.raised), w.torn)
	}
}

// A cove that died (its instance is gone) is raised again under the same id.
func TestTick_DeadRaisedAgainSameID(t *testing.T) {
	r, w, g, _ := kit()
	w.declare("acme", "reviewer", bot)
	r.Tick(context.Background())
	delete(w.insts, botID) // the Supervisor reaped it (teardown released its reservation)
	r.Tick(context.Background())
	if len(w.raised) != 2 || w.raised[1].ActorID != botID || len(g.grants) != 2 {
		t.Fatalf("want a second raise under %s; raised=%+v grants=%d", botID, w.raised, len(g.grants))
	}
	if len(w.purged) != 0 || !w.state[botID] {
		t.Fatalf("a restart must keep the session's state; purged %v", w.purged)
	}
}

// Reset tears the cove down and purges its state, keeping the declaration;
// the next tick raises it fresh. It clears the name's backoff.
func TestReset_PurgesAndReRaises(t *testing.T) {
	r, w, _, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	r.Tick(ctx)
	if err := r.ResetStanding(ctx, "acme", "reviewer", "alice-bot"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.torn, []string{botID}) || !slices.Equal(w.purged, []string{botID}) {
		t.Fatalf("torn=%v purged=%v; want both [%s]", w.torn, w.purged, botID)
	}
	r.Tick(ctx)
	if len(w.raised) != 2 || w.raised[1].ActorID != botID {
		t.Fatalf("want a fresh raise after reset; raised=%+v", w.raised)
	}
}

// A reset clears the name's raise backoff: a reset name is raised on the next
// tick even mid-backoff.
func TestReset_ClearsBackoff(t *testing.T) {
	r, w, _, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	w.failRaise[botID] = true
	r.Tick(ctx) // fails → backing off
	w.failRaise[botID] = false
	if err := r.ResetStanding(ctx, "acme", "reviewer", "alice-bot"); err != nil {
		t.Fatal(err)
	}
	r.Tick(ctx)
	if _, ok := w.insts[botID]; !ok {
		t.Fatal("reset must clear the backoff; not raised")
	}
}

// A reset whose purge fails stays pending: the name is not raised (it would
// re-attach the old state), and each tick retries until the purge succeeds;
// then the name is raised in that same tick.
func TestReset_PendingRetriedNotRaised(t *testing.T) {
	r, w, _, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	r.Tick(ctx)
	w.failPurge[botID] = true
	if err := r.ResetStanding(ctx, "acme", "reviewer", "alice-bot"); err == nil {
		t.Fatal("want a pending error")
	}
	r.Tick(ctx)
	r.Tick(ctx)
	if len(w.raised) != 1 {
		t.Fatalf("a pending reset must not be raised; raised=%+v", w.raised)
	}
	w.failPurge[botID] = false
	r.Tick(ctx)
	if !slices.Equal(w.purged, []string{botID}) || len(w.raised) != 2 {
		t.Fatalf("purged=%v raised=%d; want the retry to purge then raise", w.purged, len(w.raised))
	}
}

// The sweep purges the state of a name no longer declared even when it has no
// cove (dismissed while down), and keeps retrying a purge that failed.
func TestSweep_PurgesUndeclaredState(t *testing.T) {
	r, w, _, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	w.state["standing-old-gone-x"] = true // dismissed while down
	w.state[botID] = true
	w.failPurge["standing-old-gone-x"] = true
	r.Tick(ctx)
	if len(w.purged) != 0 {
		t.Fatalf("purged %v; a failed purge and a declared name's state must stay", w.purged)
	}
	w.failPurge["standing-old-gone-x"] = false
	r.Tick(ctx)
	if !slices.Equal(w.purged, []string{"standing-old-gone-x"}) || !w.state[botID] {
		t.Fatalf("purged=%v state=%v", w.purged, w.state)
	}
}

// State in use by a cove that failed to tear down is skipped, then purged
// once the teardown succeeds.
func TestSweep_SkipsInUseState(t *testing.T) {
	r, w, _, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	r.Tick(ctx)
	w.declare("acme", "reviewer")
	w.failTear[botID] = true
	r.Tick(ctx)
	if len(w.purged) != 0 {
		t.Fatalf("in-use state purged: %v", w.purged)
	}
	w.failTear[botID] = false
	r.Tick(ctx)
	if !slices.Equal(w.purged, []string{botID}) {
		t.Fatalf("purged = %v", w.purged)
	}
}

// A raise failure releases the grant and backs off: no retry before the
// backoff, a retry after it, doubling each failure, capped at 30m, and reset
// once the name has a live cove.
func TestTick_RaiseFailureReleasesAndBacksOff(t *testing.T) {
	r, w, g, advance := kit()
	w.declare("acme", "reviewer", bot)
	w.failRaise[botID] = true
	ctx := context.Background()

	r.Tick(ctx)
	if len(w.raised) != 1 || !slices.Equal(g.releases, []string{botID}) {
		t.Fatalf("first failure: raised=%d releases=%v; want 1 raise and its release", len(w.raised), g.releases)
	}

	// Expected delay after the k-th failure: 30s, 1m, 2m, …, capped at 30m.
	delays := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for k, d := range delays {
		raises := len(w.raised)
		advance(d - time.Second)
		r.Tick(ctx)
		if len(w.raised) != raises {
			t.Fatalf("failure %d: retried %v in, before the %v backoff", k+1, d-time.Second, d)
		}
		advance(time.Second)
		r.Tick(ctx)
		if len(w.raised) != raises+1 {
			t.Fatalf("failure %d: not retried after the %v backoff", k+1, d)
		}
	}
	if len(g.releases) != len(w.raised) {
		t.Fatalf("every failed raise must release its grant: raises=%d releases=%d", len(w.raised), len(g.releases))
	}

	// Recovery resets the backoff: once live, a later death is retried at once.
	w.failRaise[botID] = false
	advance(30 * time.Minute)
	r.Tick(ctx)
	if _, ok := w.insts[botID]; !ok {
		t.Fatal("not raised after the capped backoff")
	}
	r.Tick(ctx) // sees it live
	delete(w.insts, botID)
	raises := len(w.raised)
	r.Tick(ctx)
	if len(w.raised) != raises+1 {
		t.Fatal("backoff not reset after the name had a live cove")
	}
}

// Backoff is per name: one failing name doesn't delay another.
func TestTick_BackoffPerName(t *testing.T) {
	r, w, _, _ := kit()
	other := jam.StandingSession{Name: "bob-bot", Prompt: "triage"}
	w.declare("acme", "reviewer", bot)
	w.failRaise[botID] = true
	r.Tick(context.Background())
	w.declare("acme", "reviewer", bot, other)
	r.Tick(context.Background())
	if _, ok := w.insts["standing-acme-reviewer-bob-bot"]; !ok {
		t.Fatalf("bob-bot not raised while alice-bot backs off; raised=%+v", w.raised)
	}
}

// A removed name's cove is torn down, as is one whose role is gone.
func TestTick_DismissedTornDown(t *testing.T) {
	r, w, _, _ := kit()
	other := jam.StandingSession{Name: "bob-bot", Prompt: "triage"}
	w.declare("acme", "reviewer", bot, other)
	w.declare("acme", "triager", jam.StandingSession{Name: "t", Prompt: "p"})
	r.Tick(context.Background())
	if len(w.insts) != 3 {
		t.Fatalf("want 3 coves, got %d", len(w.insts))
	}

	w.declare("acme", "reviewer", other) // alice-bot removed
	w.roles["acme"] = slices.DeleteFunc(w.roles["acme"], func(ro jam.Role) bool { return ro.Name == "triager" })
	r.Tick(context.Background())
	slices.Sort(w.torn)
	if want := []string{botID, "standing-acme-triager-t"}; !slices.Equal(w.torn, want) {
		t.Fatalf("torn = %v, want %v", w.torn, want)
	}
	// Dismissal (a name removed, a role gone) deletes the session's state in
	// the same pass, once its cove is gone.
	slices.Sort(w.purged)
	if !slices.Equal(w.purged, w.torn) {
		t.Fatalf("purged = %v, want every dismissed session %v", w.purged, w.torn)
	}
	if _, ok := w.insts["standing-acme-reviewer-bob-bot"]; !ok {
		t.Fatal("a still-declared name was torn down")
	}
}

// A denied (or failed) grant raises nothing and doesn't back off.
func TestTick_DeniedGrantDoesNotRaise(t *testing.T) {
	for name, g := range map[string]*fakeGranter{"denied": {deny: true}, "error": {err: errors.New("db down")}} {
		w := newWorld()
		r := New(w, w, g, w, 0, nil)
		w.declare("acme", "reviewer", bot)
		r.Tick(context.Background())
		r.Tick(context.Background())
		if len(w.raised) != 0 || len(g.releases) != 0 {
			t.Fatalf("%s: raised=%+v releases=%v; want nothing", name, w.raised, g.releases)
		}
		if len(g.grants) != 2 {
			t.Fatalf("%s: grants=%d; want a retry every tick (no backoff)", name, len(g.grants))
		}
	}
}

// Ephemeral and personal coves are never touched, and a standing id held by
// something else is not raised over.
func TestTick_IgnoresOtherKinds(t *testing.T) {
	r, w, g, _ := kit()
	w.insts["cove-1"] = jam.Instance{ActorID: "cove-1", Project: "acme", Role: "reviewer", Unit: "AET-1", Phase: jam.PhaseLive}
	w.insts["personal-alice-1"] = jam.Instance{ActorID: "personal-alice-1", Project: "acme", Role: "reviewer", Owner: "alice", SessionKind: jam.SessionKindPersonal, Phase: jam.PhaseLive}
	w.insts[botID] = jam.Instance{ActorID: botID, Project: "acme", Role: "reviewer", SessionKind: jam.SessionKindPersonal, Phase: jam.PhaseLive}
	w.declare("acme", "reviewer", bot)
	r.Tick(context.Background())
	if len(w.torn) != 0 || len(w.raised) != 0 || len(g.grants) != 0 {
		t.Fatalf("touched other kinds: torn=%v raised=%+v grants=%+v", w.torn, w.raised, g.grants)
	}
}

// signalWorld signals each raise, so a test can wait on Run's goroutine.
type signalWorld struct {
	*fakeWorld
	raisedCh chan struct{}
}

func (s signalWorld) Raise(ctx context.Context, spec jam.RaiseSpec) (jam.Instance, string, string, error) {
	inst, tok, sec, err := s.fakeWorld.Raise(ctx, spec)
	s.raisedCh <- struct{}{}
	return inst, tok, sec, err
}

// Run ticks at once and stops when its context is cancelled.
func TestRun_TicksAndStops(t *testing.T) {
	w, g := newWorld(), &fakeGranter{}
	w.declare("acme", "reviewer", bot)
	sw := signalWorld{fakeWorld: w, raisedCh: make(chan struct{}, 1)}
	r := New(w, w, g, sw, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	select {
	case <-sw.raisedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not tick")
	}
	cancel()
	<-done
	if len(w.raised) != 1 || w.raised[0].SessionKind != jam.SessionKindStanding {
		t.Fatalf("raised = %+v", w.raised)
	}
}

// fakeActors is the actor store: a leftover actor makes the real Raise fail
// with "already exists", so the fake world refuses to raise over one.
type fakeActors struct {
	ids     map[string]bool
	removed []string
}

func (a *fakeActors) ListActors() []jam.Actor {
	var out []jam.Actor
	for id := range a.ids {
		out = append(out, jam.Actor{ID: id})
	}
	return out
}

func (a *fakeActors) RemoveActor(id string) error {
	a.removed = append(a.removed, id)
	delete(a.ids, id)
	return nil
}

// A crash between enrolling a standing cove's identity and recording its
// instance leaves an actor with the standing id and no cove. Standing ids are
// Jam-owned and the name has no live cove, so the reconciler removes the
// leftover actor and raises — instead of failing "already exists" and backing
// off forever.
func TestTick_RemovesLeftoverActorBeforeRaising(t *testing.T) {
	r, w, g, _ := kit()
	actors := &fakeActors{ids: map[string]bool{botID: true, "someone-else": true}}
	r.SetActors(actors)
	w.declare("acme", "reviewer", bot)
	r.Tick(context.Background())

	if !slices.Equal(actors.removed, []string{botID}) {
		t.Fatalf("removed = %v, want only the leftover %s", actors.removed, botID)
	}
	if len(w.raised) != 1 || w.raised[0].ActorID != botID || len(g.releases) != 0 {
		t.Fatalf("expected one successful raise after cleanup: raised=%+v releases=%v", w.raised, g.releases)
	}
}

// A live cove's actor is never removed.
func TestTick_LiveCoveActorKept(t *testing.T) {
	r, w, _, _ := kit()
	actors := &fakeActors{ids: map[string]bool{}}
	r.SetActors(actors)
	w.declare("acme", "reviewer", bot)
	r.Tick(context.Background())
	actors.ids[botID] = true // the raise enrolled it
	r.Tick(context.Background())
	if len(actors.removed) != 0 {
		t.Fatalf("removed a live cove's actor: %v", actors.removed)
	}
}

// Upgrade (COV-251) tears the live cove down — keeping its state, no purge —
// and raises it again in the same call, on whatever image a raise picks now.
func TestUpgrade_TeardownThenRaiseKeepsState(t *testing.T) {
	r, w, g, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	w.tag = "img:old"
	r.Tick(ctx)
	w.tag = "img:new"
	if err := r.UpgradeStanding(ctx, "acme", "reviewer", "alice-bot"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(w.torn, []string{botID}) {
		t.Fatalf("torn = %v, want [%s]", w.torn, botID)
	}
	if len(w.purged) != 0 || !w.state[botID] {
		t.Fatalf("upgrade must keep the state; purged %v", w.purged)
	}
	if len(w.raised) != 2 || w.raised[1].ActorID != botID || w.raised[1].Prompt != bot.Prompt || len(g.grants) != 2 {
		t.Fatalf("want a second raise under %s within the call; raised=%+v grants=%d", botID, w.raised, len(g.grants))
	}
	if got := w.insts[botID].ImageTag; got != "img:new" {
		t.Fatalf("re-raised on %q, want img:new", got)
	}
}

// Upgrading a name with no cove raises it now, clearing its backoff.
func TestUpgrade_NoInstanceRaisesNowClearingBackoff(t *testing.T) {
	r, w, _, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	w.failRaise[botID] = true
	r.Tick(ctx) // fails → backing off
	w.failRaise[botID] = false
	if err := r.UpgradeStanding(ctx, "acme", "reviewer", "alice-bot"); err != nil {
		t.Fatal(err)
	}
	if _, ok := w.insts[botID]; !ok {
		t.Fatal("upgrade of a down name must raise it now, despite its backoff")
	}
}

// A re-raise that fails is reported (pending); the name backs off and a later
// pass raises it as usual. A failed teardown leaves the cove alone.
func TestUpgrade_FailuresArePending(t *testing.T) {
	r, w, g, advance := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	r.Tick(ctx)

	w.failTear[botID] = true
	if err := r.UpgradeStanding(ctx, "acme", "reviewer", "alice-bot"); err == nil {
		t.Fatal("a failed teardown must be reported")
	}
	if len(w.raised) != 1 {
		t.Fatalf("a failed teardown must not re-raise; raised=%d", len(w.raised))
	}
	w.failTear[botID] = false

	w.failRaise[botID] = true
	if err := r.UpgradeStanding(ctx, "acme", "reviewer", "alice-bot"); err == nil {
		t.Fatal("a failed re-raise must be reported")
	}
	if len(g.releases) == 0 || len(w.purged) != 0 {
		t.Fatalf("a failed re-raise releases its grant and keeps state; releases=%v purged=%v", g.releases, w.purged)
	}
	w.failRaise[botID] = false
	advance(BackoffInitial)
	r.Tick(ctx)
	if _, ok := w.insts[botID]; !ok {
		t.Fatal("a later pass must raise the name")
	}
}

// An undeclared name is refused, and a name whose reset is pending is not
// raised on its old state.
func TestUpgrade_RefusesUndeclaredAndPendingReset(t *testing.T) {
	r, w, _, _ := kit()
	ctx := context.Background()
	w.declare("acme", "reviewer", bot)
	if err := r.UpgradeStanding(ctx, "acme", "reviewer", "nobody"); err == nil {
		t.Fatal("upgrade of an undeclared name must fail")
	}
	r.Tick(ctx)
	w.failPurge[botID] = true
	_ = r.ResetStanding(ctx, "acme", "reviewer", "alice-bot") // pending
	if err := r.UpgradeStanding(ctx, "acme", "reviewer", "alice-bot"); err == nil {
		t.Fatal("upgrade during a pending reset must fail")
	}
	if _, ok := w.insts[botID]; ok || len(w.raised) != 1 {
		t.Fatalf("a pending reset's name must not be raised; raised=%d", len(w.raised))
	}
}
