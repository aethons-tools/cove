package userui

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// Who may open a session view (conversations-sessions.adoc#session-view):
// the operators, and for a personal session its starter. Being in the
// session's home channel grants nothing, and leaving it takes nothing away.
func TestSessionViewAccess(t *testing.T) {
	e, p, personal, standing, _ := sessionEnv(t)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	if err := e.st.JoinChannel(ident.ID(e.homeOf(t, personal)), bob.UserID, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.LeaveChannel(ident.ID(e.homeOf(t, personal)), p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		who   jam.Participant
		inst  jam.Instance
		admin bool
		code  int
	}{
		{"the starter, out of its channel", p, personal, false, 200},
		{"a member of its channel", bob, personal, false, 404},
		{"a member of a standing session's channel", p, standing, false, 404},
		{"an operator", bob, standing, true, 200},
		{"an operator, someone's personal session", bob, personal, true, 200},
	} {
		for _, path := range []string{"", "/activity", "/pane/status"} {
			if rec := sessAPI(t, e, tc.who, "GET", tc.inst.ActorID+path, "", tc.admin); rec.Code != tc.code {
				t.Errorf("%s: GET %s = %d %s, want %d", tc.name, path, rec.Code, rec.Body, tc.code)
			}
		}
	}
}

// Once a session ends, its view keeps the activity, without the header or
// the pane, and its composer goes.
func TestSessionViewEnded(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	home, _ := e.Intercom.HomeChannel(personal)
	if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: ident.ID(personal.ActorID), Body: "done"}); err != nil {
		t.Fatal(err)
	}
	personal.Phase = jam.PhaseGone
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "/pane/status"} {
		if rec := sessAPI(t, e, p, "GET", personal.ActorID+path, "", false); rec.Code != http.StatusNotFound {
			t.Errorf("ended: GET %q = %d", path, rec.Code)
		}
	}
	a := decodeAs[SessionActivity](t, sessAPI(t, e, p, "GET", personal.ActorID+"/activity", "", false))
	if len(a.Messages) != 1 || a.Messages[0].Raw != "done" || a.SendTo != "" || !a.Ended {
		t.Errorf("ended activity = %+v", a)
	}
	// Its turns' thinking still opens; the live feed is gone with the pane.
	events := sessionevents.NewMemStore()
	e.Deps.Events, e.Deps.Hub = events, sessionevents.NewHub()
	appendText(t, events, personal.ActorID, 1, 2)
	if pg := decodeAs[ThinkingPage](t, sessAPI(t, e, p, "GET", personal.ActorID+"/thinking?stream="+thinkStream+"&after=0&before=3", "", false)); len(pg.Events) != 2 {
		t.Errorf("ended thinking page = %+v", pg)
	}
	if rec := thinkGet(Handler(e.Deps, nil), p, "/app/user/session/"+personal.ActorID+"/thinking/events"); rec.Code != http.StatusNotFound {
		t.Errorf("ended feed = %d", rec.Code)
	}
}

// activityEnv is sessionEnv with the personal session in the room: alice
// says hello (delivered to it), it answers, bob talks in a dm it isn't in,
// and alice DMs it. Times are a minute apart from t0.
func activityEnv(t *testing.T) (*env, jam.Participant, jam.Instance, time.Time) {
	t.Helper()
	e, p, personal, _, _ := sessionEnv(t)
	sid := ident.ID(personal.ActorID)
	if err := e.st.JoinChannel(e.room.ID, sid, 0); err != nil {
		t.Fatal(err)
	}
	proj, _ := e.st.GetProject("proj")
	bob := ident.New(ident.User)
	other, err := e.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceDM, Key: "x", Label: "alice, bob"})
	if err != nil {
		t.Fatal(err)
	}
	dm, err := e.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceDM, Key: "y", Label: "alice, pair"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct {
		ch jam.Channel
		id ident.ID
	}{{other, p.UserID}, {other, bob}, {dm, p.UserID}, {dm, sid}} {
		if err := e.st.JoinChannel(m.ch.ID, m.id, 0); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	for i, m := range []struct {
		ch   jam.Channel
		from ident.ID
		body string
	}{
		{e.room, p.UserID, "hello pair"},
		{e.room, sid, "hi alice"},
		{other, bob, "not for pair"},
		{dm, p.UserID, "a word, privately"},
	} {
		if _, err := e.Intercom.PostTrusted(m.ch, intercom.Squawk{From: m.from, Body: m.body, At: t0.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	return e, p, personal, t0
}

func TestSessionActivity(t *testing.T) {
	e, p, personal, _ := activityEnv(t)
	a := decodeAs[SessionActivity](t, sessAPI(t, e, p, "GET", personal.ActorID+"/activity", "", false))
	var got []string
	for _, m := range a.Messages {
		got = append(got, m.Raw)
	}
	if want := []string{"hello pair", "hi alice", "a word, privately"}; !slices.Equal(got, want) {
		t.Fatalf("activity = %q, want %q (oldest first; bob's dm left out)", got, want)
	}
	hello, reply, dm := a.Messages[0], a.Messages[1], a.Messages[2]
	if hello.Conversation.ID != string(e.room.ID) || hello.Conversation.Kind != "room" || hello.Conversation.Label != "eng" || hello.Out || !hello.Mine {
		t.Errorf("hello = %+v", hello)
	}
	if !reply.Out || reply.Mine || reply.From == "" {
		t.Errorf("the session's reply = %+v", reply)
	}
	if dm.Conversation.Kind != "dm" {
		t.Errorf("dm = %+v", dm)
	}
	if a.SendTo != "session:"+personal.ActorID || a.Ended || a.Earlier || a.Later {
		t.Errorf("activity = %+v", a)
	}
	rec := sessAPI(t, e, p, "GET", personal.ActorID+"/activity", "", false)
	for _, k := range []string{`"messages":[`, `"turns":[]`, `"limited":[]`, `"pageFirst":`} {
		if !strings.Contains(rec.Body.String(), k) {
			t.Errorf("activity JSON lacks %s: %s", k, rec.Body)
		}
	}
}

// The activity pages like read: anchor start, end (the default) or id, dir
// forward or backward, and a limit.
func TestSessionActivityPaging(t *testing.T) {
	e, p, personal, _ := activityEnv(t)
	page := func(q string) SessionActivity {
		t.Helper()
		return decodeAs[SessionActivity](t, sessAPI(t, e, p, "GET", personal.ActorID+"/activity?"+q, "", false))
	}
	bodies := func(a SessionActivity) []string {
		var out []string
		for _, m := range a.Messages {
			out = append(out, m.Raw)
		}
		return out
	}
	last := page("limit=1")
	if got := bodies(last); !slices.Equal(got, []string{"a word, privately"}) || !last.Earlier || last.Later {
		t.Fatalf("last page = %q %+v", got, last)
	}
	prev := page("anchor=id&dir=backward&limit=1&id=" + last.PageFirst)
	if got := bodies(prev); !slices.Equal(got, []string{"hi alice"}) || !prev.Earlier || !prev.Later {
		t.Fatalf("page before = %q %+v", got, prev)
	}
	first := page("anchor=start&limit=2")
	if got := bodies(first); !slices.Equal(got, []string{"hello pair", "hi alice"}) || first.Earlier || !first.Later {
		t.Fatalf("first page = %q %+v", got, first)
	}
	next := page("anchor=id&id=" + first.PageLast)
	if got := bodies(next); !slices.Equal(got, []string{"a word, privately"}) || !next.Earlier || next.Later {
		t.Fatalf("page after = %q %+v", got, next)
	}
	for _, q := range []string{"anchor=bogus", "anchor=id", "anchor=id&id=nope", "limit=0", "limit=x", "dir=sideways"} {
		if rec := sessAPI(t, e, p, "GET", personal.ActorID+"/activity?"+q, "", false); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d %s, want 400", q, rec.Code, rec.Body)
		}
	}
}

// Each turn's thinking folds into the page it falls in: from the page's
// first message up to the next page's first (the newest page: on to now;
// the oldest: from the start).
func TestSessionActivityTurns(t *testing.T) {
	e, p, personal, t0 := activityEnv(t)
	events := sessionevents.NewMemStore()
	e.Deps.Events = events
	turnAt := map[uint32]time.Duration{0: -time.Minute, 1: 30 * time.Second, 2: 2*time.Minute + 30*time.Second, 3: 10 * time.Minute}
	seq := uint64(0)
	for _, turn := range []uint32{0, 1, 2, 3} {
		for i := range 2 {
			seq++
			ev := textEvent(personal.ActorID, seq, "thought")
			ev.Turn, ev.ObservedAt = turn, t0.Add(turnAt[turn]+time.Duration(i)*time.Second)
			if err := events.Append(ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	turns := func(q string) []uint32 {
		t.Helper()
		a := decodeAs[SessionActivity](t, sessAPI(t, e, p, "GET", personal.ActorID+"/activity?"+q, "", false))
		var out []uint32
		for _, f := range a.Turns {
			if f.Stream != thinkStream || f.Events != 2 || f.After != f.Before-3 || f.End-f.Start != 1000 {
				t.Errorf("fold = %+v", f)
			}
			out = append(out, f.Turn)
		}
		return out
	}
	if got := turns(""); !slices.Equal(got, []uint32{0, 1, 2, 3}) {
		t.Errorf("one page: turns %v", got)
	}
	if got := turns("limit=1"); !slices.Equal(got, []uint32{3}) {
		t.Errorf("the newest page (the dm at +3m): turns %v", got)
	}
	if got := turns("anchor=start&limit=1"); !slices.Equal(got, []uint32{0, 1}) {
		t.Errorf("the oldest page (hello at +0, up to the reply at +1m): turns %v", got)
	}
	// A fold opens with the Thinking page between its seqs.
	a := decodeAs[SessionActivity](t, sessAPI(t, e, p, "GET", personal.ActorID+"/activity?anchor=start&limit=1", "", false))
	f := a.Turns[1]
	e.Deps.Hub = sessionevents.NewHub()
	pg := decodeAs[ThinkingPage](t, sessAPI(t, e, p, "GET", personal.ActorID+"/thinking?stream="+f.Stream+"&after="+strconv.FormatUint(f.After, 10)+"&before="+strconv.FormatUint(f.Before, 10), "", false))
	if len(pg.Events) != 2 || pg.Events[0].Turn != 1 || pg.Events[1].Turn != 1 || pg.Before != 0 {
		t.Errorf("turn 1's thinking = %+v", pg)
	}
}

// fakeLimit trips the agent-to-agent limit in the conversations it holds.
type fakeLimit map[ident.ID]bool

func (f fakeLimit) Tripped(ch ident.ID) bool { return f[ch] }

// The activity names the page's conversations where the agent-to-agent
// limit is tripped, so the view can offer Resume.
func TestSessionActivityLimited(t *testing.T) {
	e, p, personal, _ := activityEnv(t)
	e.Deps.AgentLimit = fakeLimit{e.room.ID: true, ident.New(ident.Channel): true}
	a := decodeAs[SessionActivity](t, sessAPI(t, e, p, "GET", personal.ActorID+"/activity", "", false))
	if len(a.Limited) != 1 || a.Limited[0].ID != string(e.room.ID) || a.Limited[0].Label != "eng" {
		t.Errorf("limited = %+v, want just the room", a.Limited)
	}
}
