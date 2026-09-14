package escalate

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/harbor"
)

type fakeReg struct{ insts []harbor.Instance }

func (f *fakeReg) ListInstances() []harbor.Instance { return f.insts }

type fakeProjects struct{ projects map[string]harbor.Project }

func (f *fakeProjects) GetProject(name string) (harbor.Project, bool) {
	p, ok := f.projects[name]
	return p, ok
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

type fakePinger struct {
	ids       map[string]string
	called    bool
	lastIssue string
	lastBody  string
}

func (f *fakePinger) IssueByIdentifier(_ context.Context, identifier string) (string, error) {
	return f.ids[identifier], nil
}

func (f *fakePinger) PostComment(_ context.Context, issueID, body string) error {
	f.called = true
	f.lastIssue = issueID
	f.lastBody = body
	return nil
}

func TestOpensTierZeroImmediately(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "alice.h"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.lastTier != 0 || !st.lastAt.Equal(clock) {
		t.Fatalf("expected SetEscalation(0, now); got tier=%d at=%v", st.lastTier, st.lastAt)
	}
	if pg.lastIssue != "iss-42" || !strings.Contains(pg.lastBody, "@alice.h") || !strings.Contains(pg.lastBody, "ACME-42") {
		t.Fatalf("expected tier-0 ping on own ticket with @alice.h; issue=%q body=%q", pg.lastIssue, pg.lastBody)
	}
}

func TestAdvancesTierOnTimeout(t *testing.T) {
	clock := time.Unix(2000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: harbor.ActivityWaiting, EscalationTier: 0, TierPingedAt: clock.Add(-16 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{
			{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute},
			{Targets: []string{"human:bob"}, Timeout: time.Hour}},
		Roster: harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "a"}, {Name: "bob", Handle: "b"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
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
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: harbor.ActivityWaiting, EscalationTier: 0, TierPingedAt: clock.Add(-1 * time.Minute)}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}, {Targets: []string{"human:bob"}, Timeout: time.Hour}},
		Roster:     harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "a"}, {Name: "bob", Handle: "b"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("must not ping/advance before timeout")
	}
}

func TestLastTierNoFurtherAdvanceNoTeardown(t *testing.T) {
	clock := time.Unix(3000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42",
		Activity: harbor.ActivityWaiting, EscalationTier: 0, TierPingedAt: clock.Add(-time.Hour)}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "a"}}}}}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("last tier already pinged: no further ping/advance (teardown is wake-on's job)")
	}
}

func TestIgnoresNonWaitingAndNoPolicy(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{
		{ActorID: "running", Project: "acme", Unit: "ACME-1", Activity: harbor.ActivityRunning},
		{ActorID: "nopolicy", Project: "beta", Unit: "BETA-1", Activity: harbor.ActivityWaiting},
	}}
	proj := &fakeProjects{projects: map[string]harbor.Project{
		"acme": {Name: "acme", Escalation: []harbor.EscalationTier{{Targets: []string{"human:a"}, Timeout: time.Minute}}},
		"beta": {Name: "beta"}, // no policy
	}}
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{}}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }
	e.tick(context.Background())
	if st.called || pg.called {
		t.Fatal("non-Waiting and no-policy instances must be ignored")
	}
}

func TestEmptyTierAdvancesWithoutPosting(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"channel:x", "human:ghost"}, Timeout: time.Minute}},
		Roster:     harbor.Roster{}}}} // no matching humans
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
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
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:ghost"}, Timeout: time.Minute}},
		Roster:     harbor.Roster{}}}} // no matching humans
	st := &fakeState{}
	pg := &fakePinger{ids: map[string]string{"ACME-42": "iss-42"}}
	var logBuf strings.Builder
	log := slog.New(slog.NewTextHandler(&logBuf, nil))
	e := New(reg, proj, st, pg, Config{}, log)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if !strings.Contains(logBuf.String(), "human target not in roster") || !strings.Contains(logBuf.String(), "human:ghost") {
		t.Fatalf("expected warn for unknown roster target; log=%q", logBuf.String())
	}
}

func TestPingFailureDoesNotAdvance(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := &fakeReg{insts: []harbor.Instance{{ActorID: "cove-1", Project: "acme", Unit: "ACME-42", Activity: harbor.ActivityWaiting}}}
	proj := &fakeProjects{projects: map[string]harbor.Project{"acme": {Name: "acme",
		Escalation: []harbor.EscalationTier{{Targets: []string{"human:alice"}, Timeout: 15 * time.Minute}},
		Roster:     harbor.Roster{Humans: []harbor.Human{{Name: "alice", Handle: "alice.h"}}}}}}
	st := &fakeState{}
	pg := &erroringPinger{}
	e := New(reg, proj, st, pg, Config{}, nil)
	e.now = func() time.Time { return clock }

	e.tick(context.Background())

	if st.called {
		t.Fatal("a transient ticket/post error must not advance the tier (must retry next tick)")
	}
}

type erroringPinger struct{}

func (erroringPinger) IssueByIdentifier(_ context.Context, _ string) (string, error) {
	return "", errIssueLookup
}

func (erroringPinger) PostComment(_ context.Context, _, _ string) error {
	return nil
}

var errIssueLookup = &testError{"issue lookup failed"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
