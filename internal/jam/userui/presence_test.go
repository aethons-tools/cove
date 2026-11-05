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
	st    map[string]sessionevents.Status
	inits map[string]int
	subs  map[string]int
	ch    chan struct{}
}

func (f *fakePresence) Status(a string) (sessionevents.Status, bool) { s, ok := f.st[a]; return s, ok }
func (f *fakePresence) Subscribe() (<-chan struct{}, func())         { return f.ch, func() {} }
func (f *fakePresence) Inits(a string) int                           { return f.inits[a] }
func (f *fakePresence) Subagents(a string) int                       { return f.subs[a] }

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

// A raising or recovering session whose kit a manager is building reads
// "is preparing its kit image — <step>"; preferring its own manager.
func TestPreparingKitRows(t *testing.T) {
	web := jam.KitRef{ID: "web", Version: 2}
	var asked []string
	kits := func(kit jam.KitRef, manager string) (jam.ManagerActivity, bool) {
		asked = append(asked, kit.String()+" on "+manager)
		if kit.ID != "web" {
			return jam.ManagerActivity{}, false
		}
		return jam.ManagerActivity{Kind: jam.ActivityPrepareKit, Kit: web, Step: "building", Detail: "step 7/12"}, true
	}
	rec := jam.Instance{ActorID: "rec", Phase: jam.PhaseRecovering, Kit: web, Manager: "m1"}
	raising := jam.Instance{ActorID: "new", Phase: jam.PhaseRaising, Kit: web}
	other := jam.Instance{ActorID: "db", Phase: jam.PhaseRecovering, Kit: jam.KitRef{ID: "db", Version: 1}}
	live := jam.Instance{ActorID: "live", Phase: jam.PhaseLive, Kit: web}
	nokit := jam.Instance{ActorID: "nokit", Phase: jam.PhaseRaising}
	rows := sessionRows([]string{"rec", "new", "db", "live", "nokit"}, []jam.Instance{rec, raising, other, live, nokit}, nil, kits)
	want := []SessionRow{
		{"rec", "is preparing its kit image — building step 7/12", "", "busy"},
		{"new", "is preparing its kit image — building step 7/12", "", "busy"},
		{"db", "is recovering", "", "busy"},
		{"live", "is working", "", "busy"},
		{"nokit", "is starting", "", "busy"},
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("rows = %+v\nwant %+v", rows, want)
	}
	if !slices.Equal(asked, []string{"web@v2 on m1", "web@v2 on ", "db@v1 on "}) {
		t.Fatalf("lookups = %q", asked)
	}
	// No step yet; and no lookup at all.
	bare := func(jam.KitRef, string) (jam.ManagerActivity, bool) {
		return jam.ManagerActivity{Kind: jam.ActivityPrepareKit}, true
	}
	if rows := sessionRows([]string{"rec"}, []jam.Instance{rec}, nil, bare); rows[0].Text != "is preparing its kit image" {
		t.Fatalf("no step: %+v", rows)
	}
	if rows := sessionRows([]string{"rec"}, []jam.Instance{rec}, nil, nil); rows[0].Text != "is recovering" {
		t.Fatalf("nil lookup: %+v", rows)
	}
}
