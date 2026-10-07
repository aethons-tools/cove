package escalate

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

type fakeReg struct{ insts []jam.Instance }

func (f *fakeReg) ListInstances() []jam.Instance { return f.insts }

type fakeProjects struct{ projects map[string]jam.Project }

func (f *fakeProjects) GetProject(name string) (jam.Project, bool) {
	p, ok := f.projects[name]
	return p, ok
}

// Members reads the members off the fake project's (legacy-shaped) roster.
func (f *fakeProjects) Members(name string) []jam.Member {
	var out []jam.Member
	for _, h := range f.projects[name].Roster.Humans {
		out = append(out, jam.Member{User: jam.User{ID: h.UserID, Name: h.Name}, Handle: h.Handle})
	}
	return out
}

type fakeState struct {
	called   bool
	lastTier int
	lastAt   time.Time
}

func (f *fakeState) SetEscalation(actorID string, tier int, at time.Time) error {
	f.called = true
	f.lastTier = tier
	f.lastAt = at
	return nil
}

// fakeCaller records the call-ins: lastIssue is the session's ticket (or
// id), lastBody the called-in members as "@handle" (else "@name").
type fakeCaller struct {
	postErr   error
	called    bool
	lastIssue string
	lastBody  string
	lastCat   string
	lastIDs   []ident.ID
}

func (f *fakeCaller) Escalate(_ context.Context, inst jam.Instance, tier int, category string, members []jam.Member) error {
	f.called = true
	f.lastIssue = inst.Unit
	if f.lastIssue == "" {
		f.lastIssue = inst.ActorID
	}
	var names []string
	f.lastIDs = nil
	for _, m := range members {
		n := m.Handle
		if n == "" {
			n = m.User.Name
		}
		names = append(names, "@"+n)
		f.lastIDs = append(f.lastIDs, m.User.ID)
	}
	f.lastBody = strings.Join(names, " ") + " tier " + strconv.Itoa(tier) + " on " + inst.Unit
	f.lastCat = category
	return f.postErr
}

// needsInput is the report that asks for a person (and so opens escalation).
var needsInput = &jam.TicketReport{State: jam.ReportNeedsInput}

func TestOpensTierZeroImmediately(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.lastTier != 0 || !st.lastAt.Equal(clock) {
		t.Fatalf("expected SetEscalation(0, now); got tier=%d at=%v", st.lastTier, st.lastAt)
	}
	if pg.lastIssue != "ACME-42" || !strings.Contains(pg.lastBody, "@alice.h") || !strings.Contains(pg.lastBody, "ACME-42") {
		t.Fatalf("expected tier-0 ping on own ticket with @alice.h; issue=%q body=%q", pg.lastIssue, pg.lastBody)
	}
}

func TestAdvancesTierOnTimeout(t *testing.T) {
	clock := time.Unix(2000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: jam.ActivityWaiting, Report: needsInput, EscalationTier: 0, TierPingedAt: clock.Add(-16 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{
			{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute},
			{Targets: []string{"human:bob"}, Timeout: time.Hour}},
		Roster: jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "a"}, {Name: "bob", Handle: "b"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.lastTier != 1 {
		t.Fatalf("expected advance to tier 1, got %d", st.lastTier)
	}
	if !strings.Contains(pg.lastBody, "@b") {
		t.Fatalf("expected tier-1 ping to @b, got %q", pg.lastBody)
	}
}

func TestNoAdvanceBeforeTimeout(t *testing.T) {
	clock := time.Unix(2000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: jam.ActivityWaiting, Report: needsInput, EscalationTier: 0, TierPingedAt: clock.Add(-1 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}, {Targets: []string{"human:bob"}, Timeout: time.Hour}},
		Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "a"}, {Name: "bob", Handle: "b"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("must not ping/advance before timeout")
	}
}

func TestLastTierNoFurtherAdvanceNoTeardown(t *testing.T) {
	clock := time.Unix(3000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: jam.ActivityWaiting, Report: needsInput, EscalationTier: 0, TierPingedAt: clock.Add(-time.Hour)}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "a"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("last tier already pinged: no further ping/advance (teardown is wake-on's job)")
	}
}

func TestIgnoresNonWaitingAndNoPolicy(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{
		{ActorID: "running", Project: "acme", Unit: "ACME-1", Activity: jam.ActivityRunning},
		{ActorID: "nopolicy", Project: "beta", Unit: "BETA-1", Activity: jam.ActivityWaiting, Report: needsInput},
	}}
	proj := &fakeProjects{projects: map[string]jam.Project{
		"acme": {Name: "acme", Escalation: []jam.EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}}},
		"beta": {Name: "beta"}, // no policy
	}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("non-Waiting and no-policy instances must be ignored")
	}
}

func TestEmptyTierAdvancesWithoutPosting(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"channel:x", "human:ghost"}, Timeout: time.Minute}},
		Roster:     jam.Roster{}}}} // no matching humans
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.lastTier != 0 || !st.lastAt.Equal(clock) {
		t.Fatalf("empty tier must still advance the timer; tier=%d", st.lastTier)
	}
	if pg.called {
		t.Fatal("empty tier must post nothing")
	}
}

func TestUnknownRosterNameWarns(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:ghost"}, Timeout: time.Minute}},
		Roster:     jam.Roster{}}}} // no matching humans
	st := &fakeState{}
	pg := &fakeCaller{}
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	e := New(reg, proj, st, pg, Config{}, log)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if !strings.Contains(logBuf.String(), "user target not a project member") || !strings.Contains(logBuf.String(), "human:ghost") {
		t.Fatalf("expected warn for unknown roster target; log=%q", logBuf.String())
	}
}

func TestPingFailureDoesNotAdvance(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{postErr: errIssueLookup}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.called {
		t.Fatal("a transient ticket/post error must not advance the tier (must retry next tick)")
	}
}

func TestPostCommentFailureDoesNotAdvance(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{postErr: &testError{"post comment failed"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.called {
		t.Fatal("a transient PostComment error must not advance the tier (must retry next tick)")
	}
}

func TestRoutesByCategory(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput, EscalationCategory: "infra"}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation:           []jam.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]jam.EscalationTier{"infra": {{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}},
		Roster:               jam.Roster{Humans: []jam.Human{{Name: "oncall", Handle: "oncall.h"}, {Name: "sre", Handle: "sre.h"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if !strings.Contains(pg.lastBody, "@sre.h") || strings.Contains(pg.lastBody, "@oncall.h") {
		t.Fatalf("infra category must ping @sre.h (not the default @oncall.h); body=%q", pg.lastBody)
	}
}

func TestUnknownCategoryFallsBackToDefault(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput, EscalationCategory: "nonexistent"}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation:           []jam.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]jam.EscalationTier{"infra": {{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}},
		Roster:               jam.Roster{Humans: []jam.Human{{Name: "oncall", Handle: "oncall.h"}, {Name: "sre", Handle: "sre.h"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if !strings.Contains(pg.lastBody, "@oncall.h") {
		t.Fatalf("unknown category must fall back to default chain (@oncall.h); body=%q", pg.lastBody)
	}
}

func TestEmptyCategoryUsesDefault(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput}}} // no category
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation:           []jam.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]jam.EscalationTier{"infra": {{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute}}},
		Roster:               jam.Roster{Humans: []jam.Human{{Name: "oncall", Handle: "oncall.h"}, {Name: "sre", Handle: "sre.h"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if !strings.Contains(pg.lastBody, "@oncall.h") {
		t.Fatalf("empty category must use default chain; body=%q", pg.lastBody)
	}
}

func TestCategoryAdvanceUsesCategoryChainTimeout(t *testing.T) {
	clock := time.Unix(2000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput,
		EscalationCategory: "infra", EscalationTier: 0, TierPingedAt: clock.Add(-11 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:oncall"}, Timeout: 30 * time.Minute}},
		EscalationByCategory: map[string][]jam.EscalationTier{"infra": {
			{Targets: []string{"human:sre"}, Timeout: 10 * time.Minute},
			{Targets: []string{"human:lead"}, Timeout: time.Hour}}},
		Roster: jam.Roster{Humans: []jam.Human{{Name: "sre", Handle: "s"}, {Name: "lead", Handle: "l"}}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.lastTier != 1 || !strings.Contains(pg.lastBody, "@l") {
		t.Fatalf("infra tier-0 (10m) elapsed → advance to infra tier-1 (@l); tier=%d body=%q", st.lastTier, pg.lastBody)
	}
}

var errIssueLookup = &testError{"issue lookup failed"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// A session with no ticket escalates too (slice 4), when it asked for a
// person with escalate; without asking, it isn't escalated.
func TestTicketlessSessionEscalatesWhenItAsks(t *testing.T) {
	for _, asked := range []bool{false, true} {
		reg := &fakeReg{insts: []jam.Instance{{ActorID: "p1", Project: "acme", Owner: "alice", SessionKind: jam.SessionKindPersonal, Activity: jam.ActivityWaiting, EscalationAsked: asked}}}
		proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
			Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
			Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}}}}}
		st := &fakeState{}
		pg := &fakeCaller{}
		e := New(reg, proj, st, pg, Config{}, nil)
		e.tick(context.Background())
		if pg.called != asked || st.called != asked {
			t.Errorf("asked=%v: called=%v state=%v", asked, pg.called, st.called)
		}
	}
}

// A session that asked to end is not soliciting anyone: never escalate it.
func TestSkipsEndRequested(t *testing.T) {
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput,
		EndRequested: &jam.EndRequest{Reason: "merged"}}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}}}}}
	pg := &fakeCaller{}
	e := New(reg, proj, &fakeState{}, pg, Config{}, nil)
	e.tick(context.Background())
	if pg.lastIssue != "" {
		t.Fatalf("pinged %q for a session that asked to end", pg.lastIssue)
	}
}

// Every ticket turn now ends in Waiting: escalation pings only when the
// session reported needs-input (it asked for a person), not while it waits on
// CI or a PR.
func TestEscalatesOnlyOnNeedsInputReport(t *testing.T) {
	for _, c := range []struct {
		name string
		rep  *jam.TicketReport
		want bool
	}{
		{"no report", nil, false},
		{"in-review", &jam.TicketReport{State: jam.ReportInReview}, false},
		{"needs-input", &jam.TicketReport{State: jam.ReportNeedsInput}, true},
	} {
		reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: c.rep}}}
		proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
			Escalation: []jam.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
			Roster:     jam.Roster{Humans: []jam.Human{{Name: "alice", Handle: "alice.h"}}}}}}
		pg := &fakeCaller{}
		e := New(reg, proj, &fakeState{}, pg, Config{}, nil)
		e.tick(context.Background())
		if got := pg.lastIssue != ""; got != c.want {
			t.Errorf("%s: pinged=%v, want %v", c.name, got, c.want)
		}
	}
}

// Tier targets name people as user:<name>, user:<usr_id> or (alias) human:<name>.
func TestUserTargetsResolveByNameOrID(t *testing.T) {
	alice, bob := ident.New(ident.User), ident.New(ident.User)
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []jam.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: jam.ActivityWaiting, Report: needsInput}}}
	proj := &fakeProjects{projects: map[string]jam.Project{"acme": {Name: "acme",
		Escalation: []jam.EscalationTier{{Targets: []string{"user:" + string(alice), "user:bob", "human:carol"}, Timeout: 15 * time.Minute}},
		Roster: jam.Roster{Humans: []jam.Human{
			{UserID: alice, Name: "alice", Handle: "alice.h"},
			{UserID: bob, Name: "bob", Handle: "bob.h"},
			{Name: "carol", Handle: "carol.h"},
		}}}}}
	st := &fakeState{}
	pg := &fakeCaller{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	for _, want := range []string{"@alice.h", "@bob.h", "@carol.h"} {
		if !strings.Contains(pg.lastBody, want) {
			t.Errorf("ping %q missing %s", pg.lastBody, want)
		}
	}
}
