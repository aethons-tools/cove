package userui

import (
	"encoding/json"
	"testing"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

func TestCardStateMapping(t *testing.T) {
	live := func(a jam.Activity) jam.Instance {
		return jam.Instance{ActorID: "a", Phase: jam.PhaseLive, Activity: a}
	}
	asking := live(jam.ActivityWaiting)
	asking.EscalationAsked = true
	pausedAsking := asking
	pausedAsking.Phase = jam.PhaseIdled
	kept := live(jam.ActivityWaiting)
	kept.KeptProcesses = 1
	alarmed := live(jam.ActivityWaiting)
	alarmed.Alarms = []jam.Alarm{{Name: "x"}}
	running := func(st string, tool string) sessionevents.Status { return sessionevents.Status{State: st, Tool: tool} }
	for _, c := range []struct {
		name  string
		inst  jam.Instance
		st    sessionevents.Status
		known bool
		state CardState
		pill  string
		tool  string
	}{
		{"paused", jam.Instance{Phase: jam.PhaseIdled}, sessionevents.Status{}, false, CardSleeping, "paused", ""},
		{"raising", jam.Instance{Phase: jam.PhaseRaising}, sessionevents.Status{}, false, CardThinking, "starting", ""},
		{"recovering", jam.Instance{Phase: jam.PhaseRecovering}, sessionevents.Status{}, false, CardThinking, "recovering", ""},
		{"asking", asking, sessionevents.Status{}, false, CardIdle, "needs you", ""},
		{"paused and asking", pausedAsking, sessionevents.Status{}, false, CardSleeping, "needs you", ""},
		{"blocked", live(jam.ActivityBlocked), sessionevents.Status{}, false, CardIdle, "blocked", ""},
		{"holding", live(jam.ActivityHolding), sessionevents.Status{}, false, CardWaiting, "waiting", ""},
		{"waiting on kept processes", kept, sessionevents.Status{}, false, CardWaiting, "waiting", ""},
		{"waiting on an alarm", alarmed, sessionevents.Status{}, false, CardWaiting, "waiting", ""},
		{"idle", live(jam.ActivityWaiting), running(sessionevents.StatusRunning, "Read"), true, CardIdle, "idle", ""},
		{"done", live(jam.ActivityDone), sessionevents.Status{}, false, CardIdle, "done", ""},
		{"in a tool", live(jam.ActivityRunning), running(sessionevents.StatusRunning, "Bash"), true, CardWorking, "working", "Bash"},
		{"thinking", live(jam.ActivityRunning), running(sessionevents.StatusThinking, ""), true, CardThinking, "thinking", ""},
		{"writing", live(jam.ActivityRunning), running(sessionevents.StatusWriting, ""), true, CardThinking, "thinking", ""},
		{"running, status unknown", live(jam.ActivityRunning), sessionevents.Status{}, false, CardThinking, "thinking", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			card, ok := cardOf(c.inst, c.st, c.known, 0, sessionevents.Totals{})
			if !ok || card.State != c.state || card.Pill != c.pill || card.Tool != c.tool {
				t.Fatalf("card = %+v ok=%v, want %s/%q/%q", card, ok, c.state, c.pill, c.tool)
			}
		})
	}
	for _, ph := range []jam.Phase{jam.PhaseTerminating, jam.PhaseLost, jam.PhaseGone} {
		if _, ok := cardOf(jam.Instance{Phase: ph}, sessionevents.Status{}, false, 0, sessionevents.Totals{}); ok {
			t.Errorf("%s session has a card", ph)
		}
	}
}

func TestCardFields(t *testing.T) {
	inst := jam.Instance{ActorID: "ses_1", Name: "builder", Phase: jam.PhaseLive, Activity: jam.ActivityRunning, Mission: "ship it", Focus: "reading auth",
		OpenAsks: 2, Alarms: []jam.Alarm{{Name: "a"}, {Name: "b"}, {Name: "c"}}, Exposures: []jam.Exposure{{Name: "p"}}, KeptProcesses: 4}
	card, ok := cardOf(inst, sessionevents.Status{}, false, 3, sessionevents.Totals{Context: 84_000, ContextWindow: 200_000})
	want := SessionCard{Session: "ses_1", Name: "builder", State: CardThinking, Pill: "thinking", Mission: "ship it", Focus: "reading auth",
		Counts: CardCounts{Asks: 2, Alarms: 3, Previews: 1, Processes: 4}, Context: CardContext{Used: 84_000, Window: 200_000}, Inits: 3}
	if !ok || card != want {
		t.Fatalf("card = %+v\nwant   %+v", card, want)
	}
	inst.Name = ""
	if card, _ := cardOf(inst, sessionevents.Status{}, false, 0, sessionevents.Totals{}); card.Name != "ses_1" {
		t.Fatalf("unnamed card Name = %q, want the actor id", card.Name)
	}
}

func TestAPIRailCards(t *testing.T) {
	e, p, personal, standing, _ := sessionEnv(t)
	pr := &fakePresence{ch: make(chan struct{}, 1), inits: map[string]int{standing.ActorID: 2}, subs: map[string]int{standing.ActorID: 3},
		st: map[string]sessionevents.Status{standing.ActorID: {State: sessionevents.StatusRunning, Tool: "Bash"}}}
	var r RailResponse
	if rec := apiGet(t, Handler(e.Deps, nil, WithPresence(pr)), &p, "/app/user/api/rail"); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &r) != nil {
		t.Fatalf("rail = %d %s", rec.Code, rec.Body)
	}
	cards := map[string]*SessionCard{}
	for _, g := range r.Groups {
		for _, row := range g.Rows {
			cards[row.ID] = row.Card
		}
	}
	if c := cards[e.homeOf(t, standing)]; c == nil || c.State != CardWorking || c.Tool != "Bash" || c.Inits != 2 || c.Counts.Subagents != 3 {
		t.Errorf("standing card = %+v", c)
	}
	if c := cards[e.homeOf(t, personal)]; c == nil || c.State != CardIdle || c.Context.Window != 200_000 || c.Counts.Subagents != 0 {
		t.Errorf("personal card = %+v", c)
	}
	if c, ok := cards[engID]; !ok || c != nil {
		t.Errorf("room row card = %+v (present %v), want a row with no card", c, ok)
	}
}

func TestAPISessionHeadCard(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	hd := decodeAs[SessionHead](t, sessAPI(t, e, p, "GET", personal.ActorID, "", false))
	if hd.Card.Session != personal.ActorID || hd.Card.State != CardIdle || hd.Card.Context.Used != 84_300 {
		t.Errorf("head card = %+v", hd.Card)
	}
}
