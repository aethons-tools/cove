package meui

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
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

// presenceFixture adds sessions to #eng, one per interesting state.
func presenceFixture() (*fakeStore, fakeLog, jam.Participant, *fakePresence) {
	store, log, p := fixture()
	eng := log.sq[0].To
	for i, a := range []string{"busy", "idle", "waiting", "paused", "gone", "unknown", "fresh", "background"} {
		log.sq = append(log.sq, intercom.Squawk{Seq: int64(i + 2), From: intercom.Target{Kind: "actor", Ref: a}, To: eng, Body: "hi", Project: "proj"})
	}
	store.insts = []jam.Instance{
		{ActorID: "busy", Name: "builder", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "idle", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "waiting", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityWaiting},
		{ActorID: "paused", Project: "proj", Phase: jam.PhaseIdled},
		{ActorID: "gone", Project: "proj", Phase: jam.PhaseGone},
		{ActorID: "fresh", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityRunning},
		{ActorID: "background", Project: "proj", Phase: jam.PhaseLive, Activity: jam.ActivityHolding},
		// "unknown" has no Instance at all (deregistered): not shown.
	}
	pr := &fakePresence{ch: make(chan struct{}, 1), st: map[string]sessionevents.Status{
		"busy":       {State: sessionevents.StatusRunning, Tool: "Bash"},
		"idle":       {State: sessionevents.StatusIdle},
		"waiting":    {State: sessionevents.StatusRunning, Tool: "Read"}, // Activity wins
		"background": {State: sessionevents.StatusIdle},                  // turn over; Activity wins
	}}
	return store, log, p, pr
}

func TestPresenceRows(t *testing.T) {
	store, log, p, pr := presenceFixture()
	h := Handler(store, log, nil, WithPresence(pr))
	req := jam.WithParticipant(httptest.NewRequest("GET", "/me/presence?c="+url.QueryEscape("named:eng"), nil), p)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, want := range []string{
		`<div class="sess busy"><span class="sname">builder</span> is running <b>Bash</b><span class="dots" aria-hidden="true">`,
		`<div class="sess dim"><span class="sname">idle</span> is idle</div>`,
		`<div class="sess wait"><span class="sname">waiting</span> is waiting on you</div>`,
		`<div class="sess busy"><span class="sname">background</span> is working in the background`,
		`<div class="sess dim"><span class="sname">paused</span> is paused</div>`,
		// Live, but no event seen yet (e.g. just after a Jam restart).
		`<div class="sess busy"><span class="sname">fresh</span> is working<span class="dots" aria-hidden="true">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("presence missing %q in:\n%s", want, body)
		}
	}
	for _, gone := range []string{">gone<", ">unknown<", "Read"} {
		if strings.Contains(body, gone) {
			t.Errorf("presence should not contain %q:\n%s", gone, body)
		}
	}
	// The full page carries the strip too, and the hook to refresh it.
	full := httptest.NewRecorder()
	h.ServeHTTP(full, jam.WithParticipant(httptest.NewRequest("GET", "/me/?c="+url.QueryEscape("named:eng"), nil), p))
	for _, want := range []string{`id="presence"`, `hx-get="/me/presence?c=named:eng"`, `mePresence from:body`, "es.addEventListener('presence'", "builder</span> is running"} {
		if !strings.Contains(full.Body.String(), want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestPresenceWithoutTrackerFallsBackToPhase(t *testing.T) {
	store, log, p, _ := presenceFixture()
	h := Handler(store, log, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest("GET", "/me/presence?c="+url.QueryEscape("named:eng"), nil), p))
	if body := rec.Body.String(); !strings.Contains(body, "builder</span> is working") || !strings.Contains(body, "waiting</span> is waiting on you") {
		t.Errorf("without a tracker, live sessions read as working:\n%s", body)
	}
}

func TestEventsStreamsPresenceSignal(t *testing.T) {
	store, log, p, pr := presenceFixture()
	srv := eventsServer(t, Handler(store, log, nil, WithPresence(pr)), p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/me/events", nil)
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
