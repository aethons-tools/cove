package userui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/sessionevents"
)

// Looking up finished conversations and ended sessions
// (docs/modules/ROOT/pages/usage/jam/rail-api.adoc#lookup): only what the
// viewer may read, by the Inbox's access rules.

// lookupEnv is newConvEnv once the work is over: spider (standing) and
// alice's personal session ended, COV-1 is done, a room alice was in was
// archived; and what alice may not read: an archived room and a done work
// log in a project she isn't in, bob's dm with spider, and bob's own
// personal session. Every one of them says "login".
type lookupEnv struct {
	*convEnv
	release, otherRoom, otherWork, bobSpider ident.ID
	bobPersonal                              jam.Instance
	personalDM                               string
}

var lookupT0 = time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC)

func newLookupEnv(t *testing.T) *lookupEnv {
	t.Helper()
	c := newConvEnv(t)
	l := &lookupEnv{convEnv: c}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	say := func(ch jam.Channel, from ident.ID, body string) {
		t.Helper()
		_, err := c.Intercom.PostTrusted(ch, intercom.Squawk{From: from, Body: body})
		must(err)
	}
	get := func(id ident.ID) jam.Channel {
		ch, _ := c.st.GetChannel(id)
		return ch
	}
	proj, _ := c.st.GetProject("proj")
	spider := ident.ID(c.spider.ActorID)

	// alice's: the dm with spider, COV-1's log, her personal session's dm,
	// and the release room.
	say(get(c.agentDM), c.p.UserID, "Is the login fix ready for review?")
	must(c.st.StartUnitWorker(c.work.ID, jam.UnitWorker{Session: spider, Label: "spider"}, jam.UnitIssue{Title: "Fix the flaky login test"}))
	say(c.work, spider, "Replacing the sleep with a poll.")
	must(c.st.ReportUnit(c.work.ID, jam.ReportDone, "", lookupT0.Add(2*time.Minute)))
	l.personalDM = c.homeOf(t, c.personal)
	say(get(ident.ID(l.personalDM)), ident.ID(c.personal.ActorID), "Ran the checkout test 200 times.")
	release, err := c.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceRoom, Key: "release", Label: "release"})
	must(err)
	must(c.st.JoinChannel(release.ID, c.p.UserID, 0))
	say(release, c.bob, "Release checklist is done; the login page is tagged.")
	must(c.st.ArchiveChannel(release.ID))
	l.release = release.ID

	// Not alice's: bob's dm with spider, bob's personal session, and the
	// other project's room and work log.
	pl, err := c.Intercom.PlanSessionDM(c.bob, c.spider.ActorID)
	must(err)
	say(pl.Channel, c.bob, "login is broken for me")
	l.bobSpider = pl.Channel.ID
	l.bobPersonal = jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "pair", SessionKind: jam.SessionKindPersonal,
		OwnerID: c.bob, Mission: "Try the login flow", Phase: jam.PhaseGone, RaisedAt: lookupT0, LastSeen: lookupT0.Add(time.Minute)}
	must(c.st.PutInstance(l.bobPersonal))
	must(c.st.CreateProject("other"))
	must(jam.AddPerson(c.st, "other", jam.Human{Name: "carol"}))
	other, _ := c.st.GetProject("other")
	carol, _ := c.st.LookupName(ident.User, "carol")
	room, err := c.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: other.ID, Kind: jam.SourceRoom, Key: "secret", Label: "secret"})
	must(err)
	say(room, carol, "the login secret")
	must(c.st.ArchiveChannel(room.ID))
	l.otherRoom = room.ID
	work, err := c.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: other.ID, Kind: jam.SourceWork, Key: "linear/OTH-1", Label: "OTH-1"})
	must(err)
	say(work, carol, "login, elsewhere")
	must(c.st.ReportUnit(work.ID, jam.ReportDone, "", lookupT0))
	l.otherWork = work.ID

	// Spider and alice's personal session end.
	c.spider.Phase, c.spider.LastSeen, c.spider.Mission = jam.PhaseGone, lookupT0.Add(3*time.Minute), "Fix the flaky login test"
	must(c.st.PutInstance(c.spider))
	c.personal.Phase, c.personal.LastSeen, c.personal.Mission = jam.PhaseGone, lookupT0.Add(4*time.Minute), "Check out the cart"
	must(c.st.PutInstance(c.personal))
	return l
}

// lookupAs is GET api/lookup?q= as the env's viewer, an operator when admin.
func lookupAs(t *testing.T, l *lookupEnv, admin bool, q string) LookupResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(l.Deps, nil).ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/api/lookup?q="+url.QueryEscape(q), nil), l.p, admin))
	return decodeAs[LookupResponse](t, rec)
}

func convIDs(r LookupResponse) []string {
	var ids []string
	for _, c := range r.Conversations {
		ids = append(ids, c.Row.ID)
	}
	return ids
}

func sessionIDs(r LookupResponse) []string {
	var ids []string
	for _, s := range r.Sessions {
		ids = append(ids, s.Session)
	}
	return ids
}

// Only what alice may read shows up: her finished conversations and the
// ended sessions whose view she may open — never another project's, another
// person's dm, or a session view that isn't hers.
func TestLookupAccess(t *testing.T) {
	l := newLookupEnv(t)
	r := lookupAs(t, l, false, "login")
	got := convIDs(r)
	for _, id := range []ident.ID{l.otherRoom, l.otherWork, l.bobSpider} {
		if slices.Contains(got, string(id)) {
			t.Errorf("conversations hold %s, which alice may not read: %q", id, got)
		}
	}
	for _, id := range []ident.ID{l.agentDM, l.work.ID, l.release} {
		if !slices.Contains(got, string(id)) {
			t.Errorf("conversations lack %s: %q", id, got)
		}
	}
	if slices.Contains(got, string(l.bobDM)) || slices.Contains(got, engID) {
		t.Errorf("live conversations show: %q", got)
	}
	if s := sessionIDs(r); len(s) != 0 {
		t.Errorf("sessions = %q, want none: spider is standing (operators only), bob's is bob's", s)
	}

	// Her own ended personal session is hers to look up; bob's isn't.
	r = lookupAs(t, l, false, "cart")
	if s := sessionIDs(r); !slices.Equal(s, []string{l.personal.ActorID}) {
		t.Errorf("sessions for cart = %q, want alice's personal session", s)
	}
	if s := sessionIDs(lookupAs(t, l, false, "login flow")); len(s) != 0 {
		t.Errorf("bob's personal session showed to alice: %q", s)
	}

	// An operator sees every ended session, but only the conversations a
	// person may read.
	r = lookupAs(t, l, true, "login")
	if s := sessionIDs(r); !slices.Contains(s, l.spider.ActorID) || !slices.Contains(s, l.bobPersonal.ActorID) {
		t.Errorf("operator sessions = %q, want spider and bob's", s)
	}
	if got := convIDs(r); slices.Contains(got, string(l.otherRoom)) || slices.Contains(got, string(l.bobSpider)) {
		t.Errorf("operator conversations = %q: the admin console's access leaked in", got)
	}
}

// A person who left a project reads none of its finished conversations.
func TestLookupLeftProject(t *testing.T) {
	l := newLookupEnv(t)
	proj, _ := l.st.GetProject("proj")
	if err := l.st.RemoveMember(proj.ID, l.p.UserID); err != nil {
		t.Fatal(err)
	}
	l.p.Projects = nil
	r := lookupAs(t, l, false, "login")
	if got := convIDs(r); len(got) != 0 {
		t.Errorf("conversations = %q, want none once alice left the project", got)
	}
}

// Each finished conversation says when it finished and why, and what
// matched: the name, or the newest matching message.
func TestLookupConversations(t *testing.T) {
	l := newLookupEnv(t)
	r := lookupAs(t, l, false, "login")
	by := map[string]LookupConversation{}
	for _, c := range r.Conversations {
		by[c.Row.ID] = c
	}
	if c := by[string(l.agentDM)]; c.Why != "ended" || c.Row.Kind != "dm" || c.Match == nil ||
		c.Match.Text != "Is the login fix ready for review?" || c.Match.From != "alice" || !c.Finished.Equal(lookupT0.Add(3*time.Minute)) {
		t.Errorf("spider dm = %+v (match %+v), want ended at spider's end, matching alice's message", c, c.Match)
	}
	if c := by[string(l.work.ID)]; c.Why != "done" || c.Title != "Fix the flaky login test" || c.Match != nil || !c.Finished.Equal(lookupT0.Add(2*time.Minute)) {
		t.Errorf("work log = %+v, want done, its title matching (no message)", c)
	}
	if c := by[string(l.release)]; c.Why != "archived" || c.Row.Kind != "room" || c.Match == nil || c.Match.From != "bob" {
		t.Errorf("release room = %+v (match %+v), want archived, bob's message", c, c.Match)
	}
	for i := 1; i < len(r.Conversations); i++ {
		if r.Conversations[i].Finished.After(r.Conversations[i-1].Finished) {
			t.Errorf("conversations not newest first: %q", convIDs(r))
		}
	}
	// A ticket key and a label match too, case-insensitively.
	if got := convIDs(lookupAs(t, l, false, "cov-1")); !slices.Equal(got, []string{string(l.work.ID)}) {
		t.Errorf("cov-1 = %q, want COV-1's log", got)
	}
	if got := convIDs(lookupAs(t, l, false, "RELEASE")); !slices.Equal(got, []string{string(l.release)}) {
		t.Errorf("RELEASE = %q, want the release room", got)
	}
}

// Ended sessions match on label, mission, and their activity's text, never
// their thinking; an empty query finds nothing.
func TestLookupSessions(t *testing.T) {
	l := newLookupEnv(t)
	events := sessionevents.NewMemStore()
	l.Deps.Events = events
	if err := events.Append(textEvent(l.personal.ActorID, 1, "zebra crossing")); err != nil {
		t.Fatal(err)
	}
	r := lookupAs(t, l, false, "checkout")
	if len(r.Sessions) != 1 {
		t.Fatalf("sessions for checkout = %+v, want alice's personal session by its activity", r.Sessions)
	}
	s := r.Sessions[0]
	if s.Kind != "personal" || s.Mission != "Check out the cart" || !s.Ended.Equal(lookupT0.Add(4*time.Minute)) ||
		s.Match == nil || s.Match.Text != "Ran the checkout test 200 times." {
		t.Errorf("session = %+v (match %+v)", s, s.Match)
	}
	if got := sessionIDs(lookupAs(t, l, false, "zebra")); len(got) != 0 {
		t.Errorf("thinking matched: %q", got)
	}
	r = lookupAs(t, l, true, "spider")
	if len(r.Sessions) != 1 || r.Sessions[0].Kind != "standing" || r.Sessions[0].Label != "spider" || r.Sessions[0].Match != nil {
		t.Errorf("operator spider = %+v, want the standing session by name", r.Sessions)
	}
	empty := lookupAs(t, l, true, "  ")
	if empty.Conversations == nil || empty.Sessions == nil || len(empty.Conversations)+len(empty.Sessions) != 0 {
		t.Errorf("empty q = %#v, want two empty lists", empty)
	}
}

// Each list holds at most 50, the newest.
func TestLookupCap(t *testing.T) {
	l := newLookupEnv(t)
	for i := range lookupMax + 5 {
		inst := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "pair", SessionKind: jam.SessionKindPersonal,
			OwnerID: l.p.UserID, Mission: "many", Phase: jam.PhaseGone, RaisedAt: lookupT0, LastSeen: lookupT0.Add(time.Duration(i) * time.Minute)}
		if err := l.st.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	r := lookupAs(t, l, false, "many")
	if len(r.Sessions) != lookupMax {
		t.Fatalf("sessions = %d, want %d", len(r.Sessions), lookupMax)
	}
	if want := lookupT0.Add(time.Duration(lookupMax+4) * time.Minute); !r.Sessions[0].Ended.Equal(want) {
		t.Errorf("first ended %v, want the newest, %v", r.Sessions[0].Ended, want)
	}
}

// A finished conversation reads as before for anyone who may read it, with
// no composer; posting to it fails with 409 and says it's finished.
func TestFinishedConversationReadOnly(t *testing.T) {
	l := newLookupEnv(t)
	l.Deps.Send = jam.NewParticipantSendHandler(l.st, l.Intercom, nil)
	h := Handler(l.Deps, nil)
	for _, id := range []ident.ID{l.agentDM, l.work.ID, l.release} {
		code, conv, body := getConv(t, l.env, l.p, string(id), "")
		if code != http.StatusOK || conv.SendTo != "" || conv.Finished == "" || len(conv.Messages) == 0 {
			t.Errorf("GET %s = %d, sendTo %q, finished %q, %d messages: %s", id, code, conv.SendTo, conv.Finished, len(conv.Messages), body)
		}
		rec := apiPost(t, h, l.p, "/app/user/api/messages", `{"to":"`+string(id)+`","body":"one more"}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "finished") {
			t.Errorf("POST to %s = %d %s, want 409 saying it's finished", id, rec.Code, rec.Body)
		}
	}
	if code, _, _ := getConv(t, l.env, l.p, string(l.otherRoom), ""); code != http.StatusNotFound {
		t.Errorf("GET the other project's archived room = %d, want 404", code)
	}
	if code, conv, _ := getConv(t, l.env, l.p, string(l.bobDM), ""); code != http.StatusOK || conv.SendTo == "" || conv.Finished != "" {
		t.Errorf("live dm with bob = %d %+v, want its composer", code, conv)
	}
}

// A done unit's log leaves Me: it is finished.
func TestRailDoneWorkLogLeavesMe(t *testing.T) {
	l := newLookupEnv(t)
	if err := l.st.JoinChannel(l.work.ID, l.p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	r := railAs(t, l.convEnv, false, "")
	if _, ok := meRow(r, string(l.work.ID)); ok {
		t.Errorf("the done unit's log is in Me: %q", rowIDs(r.Me))
	}
	if got := convIDs(lookupAs(t, l, false, "COV-1")); !slices.Equal(got, []string{string(l.work.ID)}) {
		t.Errorf("lookup COV-1 = %q, want its followed log", got)
	}
}
