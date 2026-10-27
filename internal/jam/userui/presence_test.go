package userui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// fakePresence is a Presence with fixed statuses and a hand-fired signal.
type fakePresence struct {
	st map[string]sessionevents.Status
	ch chan struct{}
}

func (f *fakePresence) Status(a string) (sessionevents.Status, bool) { s, ok := f.st[a]; return s, ok }
func (f *fakePresence) Subscribe() (<-chan struct{}, func())         { return f.ch, func() {} }

// presenceFixture adds sessions to eng, one per interesting state.
func presenceFixture() (*env, jam.Participant, *fakePresence) {
	e, p := fixture()
	insts := []jam.Instance{
		{ActorID: "busy", Name: "builder", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "idle", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "waiting", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting},
		{ActorID: "asking", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting, EscalationAsked: true},
		{ActorID: "stuck", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityBlocked},
		{ActorID: "paused", Project: "proj", Phase: jam.PhaseIdled},
		{ActorID: "pausedasker", Project: "proj", Phase: jam.PhaseIdled, Activity: jam.ActivityWaiting, EscalationAsked: true},
		{ActorID: "gone", Project: "proj", Phase: jam.PhaseGone},
		{ActorID: "fresh", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "background", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityHolding},
	}
	for _, inst := range insts {
		if err := e.st.PutInstance(inst); err != nil {
			panic(err)
		}
	}
	// "unknown" has no Instance at all (deregistered): not shown.
	for _, a := range []string{"busy", "idle", "waiting", "asking", "stuck", "paused", "pausedasker", "gone", "unknown", "fresh", "background"} {
		if err := e.st.JoinChannel(engID, ident.ID(a), 1); err != nil {
			panic(err)
		}
	}
	pr := &fakePresence{ch: make(chan struct{}, 1), st: map[string]sessionevents.Status{
		"busy":       {State: sessionevents.StatusRunning, Tool: "Bash"},
		"idle":       {State: sessionevents.StatusIdle},
		"waiting":    {State: sessionevents.StatusRunning, Tool: "Read"}, // Activity wins
		"background": {State: sessionevents.StatusIdle},                  // turn over; Activity wins
	}}
	return e, p, pr
}

// presenceJSON is eng's presence strip as the API serves it.
func presenceJSON(t *testing.T, h http.Handler, p jam.Participant) []SessionRow {
	t.Helper()
	var r PresenceResponse
	if rec := apiGet(t, h, &p, "/app/user/api/conversations/"+engID+"/presence"); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &r) != nil {
		t.Fatalf("presence = %d %s", rec.Code, rec.Body)
	}
	return r.Sessions
}

func TestPresenceRows(t *testing.T) {
	e, p, pr := presenceFixture()
	rows := presenceJSON(t, Handler(e.Deps, nil, WithPresence(pr)), p)
	for _, want := range []SessionRow{
		{"builder", "is running", "Bash", "busy"},
		{"idle", "is idle", "", "dim"},
		// A turn that ended is idle; only a session that asked needs you.
		{"waiting", "is idle", "", "dim"},
		{"asking", "needs you", "", "wait"},
		{"stuck", "is blocked", "", "wait"},
		// Paused while it waits, it still needs you (escalation keeps paging it).
		{"pausedasker", "needs you", "", "wait"},
		{"background", "is working in the background", "", "busy"},
		{"paused", "is paused", "", "dim"},
		// Live, but no event seen yet (e.g. just after a Jam restart).
		{"fresh", "is working", "", "busy"},
	} {
		if !slices.Contains(rows, want) {
			t.Errorf("presence lacks %+v in %+v", want, rows)
		}
	}
	for _, row := range rows {
		if row.Label == "gone" || row.Label == "unknown" {
			t.Errorf("presence shows %q", row.Label)
		}
	}
}

func TestPresenceWithoutTrackerFallsBackToPhase(t *testing.T) {
	e, p, _ := presenceFixture()
	rows := presenceJSON(t, Handler(e.Deps, nil), p)
	for _, want := range []SessionRow{{"builder", "is working", "", "busy"}, {"asking", "needs you", "", "wait"}} {
		if !slices.Contains(rows, want) {
			t.Errorf("without a tracker, live sessions read as working: lacks %+v in %+v", want, rows)
		}
	}
}

func TestEventsStreamsPresenceSignal(t *testing.T) {
	e, p, pr := presenceFixture()
	srv := eventsServer(t, Handler(e.Deps, nil, WithPresence(pr)), p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/app/user/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events with only a presence source = %d, want 200", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	pr.ch <- struct{}{}
	readUntil(t, sc, "event: presence")
	pr.ch <- struct{}{}
	readUntil(t, sc, "event: presence") // throttled, not dropped
}
