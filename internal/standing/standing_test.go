package standing

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/allocator"
	"github.com/aethons-tools/cove/internal/harbor"
)

// fakeWorld is the roster, the registry and the supervisor: Raise adds a Live
// instance (unless the actor id is set to fail), Teardown removes it.
type fakeWorld struct {
	roles     map[string][]harbor.Role // project → roles
	insts     map[string]harbor.Instance
	raised    []harbor.RaiseSpec
	torn      []string
	failRaise map[string]bool
}

func newWorld() *fakeWorld {
	return &fakeWorld{roles: map[string][]harbor.Role{}, insts: map[string]harbor.Instance{}, failRaise: map[string]bool{}}
}

func (w *fakeWorld) ListProjects() []string {
	var out []string
	for p := range w.roles {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func (w *fakeWorld) ListRoles(project string) []harbor.Role { return w.roles[project] }

func (w *fakeWorld) ListInstances() []harbor.Instance {
	var out []harbor.Instance
	for _, i := range w.insts {
		out = append(out, i)
	}
	return out
}

func (w *fakeWorld) Raise(_ context.Context, spec harbor.RaiseSpec) (harbor.Instance, string, string, error) {
	w.raised = append(w.raised, spec)
	if w.failRaise[spec.ActorID] {
		return harbor.Instance{}, "", "", errors.New("launch failed")
	}
	inst := harbor.Instance{ActorID: spec.ActorID, Project: spec.Project, Role: spec.Role, Name: spec.Name, Owner: spec.Owner, SessionKind: spec.SessionKind, Phase: harbor.PhaseLive}
	w.insts[spec.ActorID] = inst
	return inst, "tok", "secret", nil
}

func (w *fakeWorld) Teardown(_ context.Context, id string) error {
	w.torn = append(w.torn, id)
	delete(w.insts, id)
	return nil
}

// declare sets project/role's standing declarations.
func (w *fakeWorld) declare(project, role string, ss ...harbor.StandingSession) {
	rs := w.roles[project]
	for i := range rs {
		if rs[i].Name == role {
			rs[i].Allocation.Standing = ss
			return
		}
	}
	w.roles[project] = append(rs, harbor.Role{Name: role, Allocation: harbor.RoleAllocation{Standing: ss}})
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

var bot = harbor.StandingSession{Name: "alice-bot", Prompt: "review every PR"}

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
	if spec.ActorID != botID || spec.Project != "acme" || spec.Role != "reviewer" || spec.Name != "alice-bot" || spec.SessionKind != harbor.SessionKindStanding || spec.Owner != "" {
		t.Fatalf("spec = %+v", spec)
	}
	wantPrompt := "You are the standing session \"alice-bot\" for role reviewer in project acme. You run until an operator\n" +
		"removes you. When you have results or need input, message people with the intercom `send` tool — you\n" +
		"must name the recipient (`to`); replies wake you and are available via `read`.\n" +
		"---\nreview every PR"
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
	other := harbor.StandingSession{Name: "bob-bot", Prompt: "triage"}
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
	other := harbor.StandingSession{Name: "bob-bot", Prompt: "triage"}
	w.declare("acme", "reviewer", bot, other)
	w.declare("acme", "triager", harbor.StandingSession{Name: "t", Prompt: "p"})
	r.Tick(context.Background())
	if len(w.insts) != 3 {
		t.Fatalf("want 3 coves, got %d", len(w.insts))
	}

	w.declare("acme", "reviewer", other) // alice-bot removed
	w.roles["acme"] = slices.DeleteFunc(w.roles["acme"], func(ro harbor.Role) bool { return ro.Name == "triager" })
	r.Tick(context.Background())
	slices.Sort(w.torn)
	if want := []string{botID, "standing-acme-triager-t"}; !slices.Equal(w.torn, want) {
		t.Fatalf("torn = %v, want %v", w.torn, want)
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
	w.insts["cove-1"] = harbor.Instance{ActorID: "cove-1", Project: "acme", Role: "reviewer", Unit: "AET-1", Phase: harbor.PhaseLive}
	w.insts["personal-alice-1"] = harbor.Instance{ActorID: "personal-alice-1", Project: "acme", Role: "reviewer", Owner: "alice", SessionKind: harbor.SessionKindPersonal, Phase: harbor.PhaseLive}
	w.insts[botID] = harbor.Instance{ActorID: botID, Project: "acme", Role: "reviewer", SessionKind: harbor.SessionKindPersonal, Phase: harbor.PhaseLive}
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

func (s signalWorld) Raise(ctx context.Context, spec harbor.RaiseSpec) (harbor.Instance, string, string, error) {
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
	if len(w.raised) != 1 || !strings.HasPrefix(w.raised[0].Prompt, "You are the standing session") {
		t.Fatalf("raised = %+v", w.raised)
	}
}
