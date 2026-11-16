package jam

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

const (
	obsStreamOld = "11111111111111111111111111111111"
	obsStreamNew = "22222222222222222222222222222222"
)

// obsFixture is the /squawks fixture with the standing session granted
// scope.observe (role optimizer) and the ticket session's events recorded:
// an old stream (three events, one a result) and a newer one (two).
type obsFixture struct {
	*sqFixture
	events *sessionevents.MemStore
	t0     time.Time
}

func newObsFixture(t *testing.T) *obsFixture {
	t.Helper()
	f := &obsFixture{sqFixture: newSqFixture(t, "user:*"), events: sessionevents.NewMemStore(), t0: time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)}
	if err := f.store.PutRole("acme", Role{Name: "optimizer", Scope: Scope{Observe: ObserveAll}}); err != nil {
		t.Fatal(err)
	}
	f.standing.Role = "optimizer"
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	f.h.SetObserve(f.events)
	stamp := sessionevents.Stamp{Project: "acme", Role: "impl", Unit: "ACME-7", SessionKind: "ephemeral", RaisedAt: f.t0}
	add := func(stream string, seq uint64, at time.Duration, raw string) {
		t.Helper()
		ev := sessionevents.Event{ActorID: f.ticket.ActorID, StreamID: stream, Seq: seq, Kind: sessionevents.KindEvent, Turn: 1,
			ObservedAt: f.t0.Add(at), ReceivedAt: f.t0.Add(at), Raw: []byte(raw), Stamp: stamp, Index: sessionevents.DeriveIndex([]byte(raw))}
		if err := f.events.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	add(obsStreamOld, 1, 0, `{"type":"system"}`)
	add(obsStreamOld, 2, time.Minute, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`)
	add(obsStreamOld, 3, 2*time.Minute, `{"type":"result","total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":20}}`)
	add(obsStreamNew, 1, time.Hour, `{"type":"system"}`)
	add(obsStreamNew, 2, time.Hour+time.Minute, `{"type":"result","total_cost_usd":0.25,"is_error":true}`)
	return f
}

func (f *obsFixture) get(inst Instance, path string, out any) int {
	f.t.Helper()
	rec := f.do(http.MethodGet, path, inst, "")
	if rec.Code == http.StatusOK && out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			f.t.Fatalf("decode %s: %v (%s)", path, err, rec.Body)
		}
	}
	return rec.Code
}

// A role without scope.observe gets 403 from every endpoint, whether or not
// the session exists; only GET is served.
func TestObserveNeedsTheGrant(t *testing.T) {
	f := newObsFixture(t)
	id := f.ticket.ActorID
	paths := []string{
		"/squawks/observe/sessions",
		"/squawks/observe/sessions/" + id + "/activity",
		"/squawks/observe/sessions/" + id + "/streams",
		"/squawks/observe/sessions/" + id + "/events",
		"/squawks/observe/sessions/nobody/events",
		"/squawks/observe/sessions?since=junk",
	}
	for _, p := range paths {
		if code := f.get(f.ticket, p, nil); code != http.StatusForbidden {
			t.Errorf("GET %s without the grant = %d, want 403", p, code)
		}
		if code := f.get(f.standing, p, nil); code == http.StatusForbidden {
			t.Errorf("GET %s with the grant = 403", p)
		}
	}
	if rec := f.do(http.MethodPost, "/squawks/observe/sessions", f.standing, "{}"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
	if code := f.get(Instance{}, "/squawks/observe/sessions", nil); code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", code)
	}
	// Taking the grant away takes effect at once.
	if err := f.store.PutRole("acme", Role{Name: "optimizer"}); err != nil {
		t.Fatal(err)
	}
	if code := f.get(f.standing, "/squawks/observe/sessions", nil); code != http.StatusForbidden {
		t.Errorf("after revoking = %d, want 403", code)
	}
}

func TestObserveSessions(t *testing.T) {
	f := newObsFixture(t)
	var out observedSessions
	if code := f.get(f.standing, "/squawks/observe/sessions", &out); code != http.StatusOK {
		t.Fatalf("sessions = %d", code)
	}
	if len(out.Sessions) != 1 {
		t.Fatalf("sessions = %+v", out.Sessions)
	}
	s := out.Sessions[0]
	if s.ID != f.ticket.ActorID || s.Label != f.ic.PartyOf(ident.ID(f.ticket.ActorID)).Label || s.Label == "" {
		t.Errorf("id/label = %q %q", s.ID, s.Label)
	}
	if s.Kind != "ephemeral" || s.Project != "acme" || s.Role != "impl" || s.Unit != "ACME-7" || !s.RaisedAt.Equal(f.t0) {
		t.Errorf("stamp = %+v", s)
	}
	if !s.FirstAt.Equal(f.t0) || !s.LastAt.Equal(f.t0.Add(time.Hour+time.Minute)) || !s.Live {
		t.Errorf("times/live = %+v", s)
	}
	tot := s.Totals
	if tot.Turns != 2 || tot.Episodes != 2 || tot.ToolCalls != 1 || tot.Errors != 1 || tot.InputTokens != 10 || tot.OutputTokens != 20 || tot.CostUSD != 0.75 {
		t.Errorf("totals = %+v", tot)
	}

	// An ended session is still listed, not live.
	gone := f.ticket
	gone.Phase = PhaseGone
	if err := f.store.PutInstance(gone); err != nil {
		t.Fatal(err)
	}
	f.get(f.standing, "/squawks/observe/sessions", &out)
	if len(out.Sessions) != 1 || out.Sessions[0].Live {
		t.Errorf("ended = %+v", out.Sessions)
	}

	// The window: a session whose streams lie outside it isn't listed.
	q := "?since=" + f.t0.Add(-48*time.Hour).Format(time.RFC3339) + "&until=" + f.t0.Add(-47*time.Hour).Format(time.RFC3339)
	f.get(f.standing, "/squawks/observe/sessions"+q, &out)
	if len(out.Sessions) != 0 {
		t.Errorf("outside the window = %+v", out.Sessions)
	}
	// The default window is the last 24 hours.
	if err := f.events.Append(sessionevents.Event{ActorID: "old-session", StreamID: obsStreamOld, Seq: 1, Kind: sessionevents.KindEvent,
		ObservedAt: f.t0.Add(-30 * time.Hour), ReceivedAt: f.t0.Add(-30 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	f.get(f.standing, "/squawks/observe/sessions", &out)
	if len(out.Sessions) != 1 {
		t.Errorf("default window = %+v", out.Sessions)
	}
	for _, bad := range []string{"?since=yesterday", "?until=1", "?since=" + f.t0.Format(time.RFC3339) + "&until=" + f.t0.Add(-time.Hour).Format(time.RFC3339)} {
		if code := f.get(f.standing, "/squawks/observe/sessions"+bad, nil); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
}

func TestObserveStreams(t *testing.T) {
	f := newObsFixture(t)
	var out observedStreams
	if code := f.get(f.standing, "/squawks/observe/sessions/"+f.ticket.ActorID+"/streams", &out); code != http.StatusOK {
		t.Fatalf("streams = %d", code)
	}
	if len(out.Streams) != 2 || out.Streams[0].StreamID != obsStreamOld || out.Streams[1].StreamID != obsStreamNew {
		t.Fatalf("streams (oldest first) = %+v", out.Streams)
	}
	if s := out.Streams[0]; s.Events != 3 || s.LastSeq != 3 || !s.FirstAt.Equal(f.t0) || !s.LastAt.Equal(f.t0.Add(2*time.Minute)) {
		t.Errorf("old stream = %+v", s)
	}
	if code := f.get(f.standing, "/squawks/observe/sessions/nobody/streams", nil); code != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", code)
	}
}

func TestObserveEvents(t *testing.T) {
	f := newObsFixture(t)
	base := "/squawks/observe/sessions/" + f.ticket.ActorID + "/events"
	seqs := func(q string) observedEvents {
		t.Helper()
		var out observedEvents
		if code := f.get(f.standing, base+q, &out); code != http.StatusOK {
			t.Fatalf("events%s = %d", q, code)
		}
		return out
	}
	// The newest stream by default.
	if out := seqs(""); out.StreamID != obsStreamNew || len(out.Events) != 2 || out.More {
		t.Fatalf("default = %+v", out)
	}
	out := seqs("?stream=" + obsStreamOld + "&after_seq=1&limit=1")
	if out.StreamID != obsStreamOld || len(out.Events) != 1 || !out.More || out.NextAfterSeq != 2 {
		t.Fatalf("paged = %+v", out)
	}
	var ev struct {
		StreamID string `json:"stream_id"`
		Seq      uint64 `json:"seq"`
		Type     string `json:"type"`
		ToolName string `json:"tool_name"`
		Raw      json.RawMessage
	}
	if err := json.Unmarshal(out.Events[0], &ev); err != nil || ev.StreamID != obsStreamOld || ev.Seq != 2 || ev.ToolName != "Bash" || len(ev.Raw) == 0 {
		t.Fatalf("event = %+v %v", ev, err)
	}
	// types filters by the event's type.
	out = seqs("?stream=" + obsStreamOld + "&types=result,assistant")
	var types []string
	for _, raw := range out.Events {
		var e struct{ Type string }
		_ = json.Unmarshal(raw, &e)
		types = append(types, e.Type)
	}
	if !slices.Equal(types, []string{"assistant", "result"}) || out.More || out.NextAfterSeq != 3 {
		t.Fatalf("types = %v %+v", types, out)
	}
	if out = seqs("?stream=" + obsStreamOld + "&types=result&limit=1"); len(out.Events) != 1 || out.NextAfterSeq != 3 {
		t.Fatalf("one result = %+v", out)
	}
	for q, want := range map[string]int{
		"?stream=nope":                       http.StatusBadRequest,
		"?after_seq=-1":                      http.StatusBadRequest,
		"?limit=0":                           http.StatusBadRequest,
		"?limit=x":                           http.StatusBadRequest,
		"?stream=" + strings.Repeat("3", 32): http.StatusNotFound,
	} {
		if code := f.get(f.standing, base+q, nil); code != want {
			t.Errorf("events%s = %d, want %d", q, code, want)
		}
	}
	if code := f.get(f.standing, "/squawks/observe/sessions/nobody/events", nil); code != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", code)
	}
}

func TestObserveEventsLimit(t *testing.T) {
	f := newObsFixture(t)
	for i := uint64(3); i <= 2100; i++ {
		if err := f.events.Append(sessionevents.Event{ActorID: f.ticket.ActorID, StreamID: obsStreamNew, Seq: i, Kind: sessionevents.KindEvent,
			ObservedAt: f.t0.Add(time.Hour + 2*time.Minute), ReceivedAt: f.t0.Add(time.Hour + 2*time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	base := "/squawks/observe/sessions/" + f.ticket.ActorID + "/events"
	var out observedEvents
	if f.get(f.standing, base, &out); len(out.Events) != 500 || !out.More {
		t.Errorf("default limit = %d", len(out.Events))
	}
	if f.get(f.standing, base+"?limit=5000", &out); len(out.Events) != 2000 || !out.More || out.NextAfterSeq != 2000 {
		t.Errorf("max limit = %d next %d", len(out.Events), out.NextAfterSeq)
	}
}

// activity pages the observed session's messages as the session view does.
func TestObserveActivity(t *testing.T) {
	f := newObsFixture(t)
	var sent []intercom.Squawk
	for _, body := range []string{"one", "two", "three"} {
		sent = append(sent, f.reply(f.ticket, f.alice.ID, body))
	}
	base := "/squawks/observe/sessions/" + f.ticket.ActorID + "/activity"
	page := func(q string) observedActivity {
		t.Helper()
		var out observedActivity
		if code := f.get(f.standing, base+q, &out); code != http.StatusOK {
			t.Fatalf("activity%s = %d", q, code)
		}
		return out
	}
	bodies := func(a observedActivity) []string {
		var out []string
		for _, m := range a.Messages {
			out = append(out, m.Body)
		}
		return out
	}
	if a := page("?limit=2"); !slices.Equal(bodies(a), []string{"two", "three"}) || !a.Earlier || a.Later || a.PageFirst != sent[1].ID {
		t.Fatalf("end = %+v", a)
	}
	a := page("?anchor=start&limit=1")
	if !slices.Equal(bodies(a), []string{"one"}) || a.Earlier || !a.Later {
		t.Fatalf("start = %+v", a)
	}
	m := a.Messages[0]
	if m.Out || m.From == nil || m.From.Label != "alice" || m.Channel == nil || m.Channel.Kind != "dm" {
		t.Errorf("message = %+v", m)
	}
	if a := page("?anchor=id&id=" + sent[1].ID + "&dir=backward"); !slices.Equal(bodies(a), []string{"one"}) {
		t.Errorf("backward = %v", bodies(a))
	}
	if a := page("?anchor=id&id=" + sent[0].ID); !slices.Equal(bodies(a), []string{"two", "three"}) {
		t.Errorf("forward = %v", bodies(a))
	}
	for _, q := range []string{"?anchor=cursor", "?anchor=id", "?anchor=id&id=nope", "?dir=sideways", "?limit=0"} {
		if code := f.get(f.standing, base+q, nil); code != http.StatusBadRequest {
			t.Errorf("activity%s = %d, want 400", q, code)
		}
	}
	if code := f.get(f.standing, "/squawks/observe/sessions/nobody/activity", nil); code != http.StatusNotFound {
		t.Errorf("unknown session = %d, want 404", code)
	}
	if code := f.get(f.standing, "/squawks/observe/sessions/"+f.ticket.ActorID+"/nothing", nil); code != http.StatusNotFound {
		t.Errorf("unknown view = %d, want 404", code)
	}
}

// Observing reads nothing into the observed session: its inbox cursor and
// messages are unchanged.
func TestObserveIsReadOnly(t *testing.T) {
	f := newObsFixture(t)
	f.reply(f.ticket, f.alice.ID, "hello")
	before, _ := f.store.GetInstance(f.ticket.ActorID)
	for _, p := range []string{"sessions", "sessions/" + f.ticket.ActorID + "/activity", "sessions/" + f.ticket.ActorID + "/streams", "sessions/" + f.ticket.ActorID + "/events"} {
		if code := f.get(f.standing, "/squawks/observe/"+p, nil); code != http.StatusOK {
			t.Fatalf("%s = %d", p, code)
		}
	}
	after, _ := f.store.GetInstance(f.ticket.ActorID)
	if after.CommitSeq != before.CommitSeq || after.CommitCursor != before.CommitCursor {
		t.Errorf("cursor moved: %+v → %+v", before, after)
	}
	if r := f.read(f.ticket, "anchor=start"); len(r.Squawks) != 1 {
		t.Errorf("observed inbox = %+v", r.Squawks)
	}
}
