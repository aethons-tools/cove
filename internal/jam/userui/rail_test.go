package userui

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// The rail's sections, Me, Work, Agents, and History, and the saved order
// (docs/modules/ROOT/pages/usage/jam/inbox-rail.adoc).

// railAs is GET api/rail (with query q) as p, an operator when admin.
func railAs(t *testing.T, c *convEnv, admin bool, q string) RailResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(c.Deps, nil).ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/api/rail"+q, nil), c.p, admin))
	return decodeAs[RailResponse](t, rec)
}

func rowIDs(rows []ChannelRow) []string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

func agentIDs(items []AgentItem) []string {
	var ids []string
	for _, a := range items {
		ids = append(ids, a.Session)
	}
	return ids
}

func meRow(r RailResponse, id string) (ChannelRow, bool) {
	i := slices.IndexFunc(r.Me, func(row ChannelRow) bool { return row.ID == id })
	if i < 0 {
		return ChannelRow{}, false
	}
	return r.Me[i], true
}

// Me lists the live conversations alice is a member of; Work every unit with
// a current worker; Agents the active sessions whose view she may open.
func TestRailSections(t *testing.T) {
	c := newConvEnv(t)
	c.spider.UnitTitle, c.spider.Unit = "Fix the flaky login test", "COV-1"
	if err := c.st.PutInstance(c.spider); err != nil {
		t.Fatal(err)
	}
	personalDM := c.homeOf(t, c.personal)
	r := railAs(t, c, false, "")

	got := rowIDs(r.Me)
	slices.Sort(got)
	want := []string{string(c.agentDM), string(c.bobDM), engID, personalDM}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("me = %q, want %q (not the work log alice doesn't follow)", got, want)
	}
	if row, _ := meRow(r, string(c.agentDM)); row.Card == nil || row.Card.Session != c.spider.ActorID || row.Kind != "dm" {
		t.Errorf("agent dm row = %+v, want spider's card", row)
	}
	if row, _ := meRow(r, personalDM); row.Session != c.personal.ActorID || !row.SSH {
		t.Errorf("personal dm row = %+v, want its session and ssh", row)
	}
	if len(r.Work) != 1 {
		t.Fatalf("work = %+v, want COV-1", r.Work)
	}
	w := r.Work[0]
	if w.Key != "COV-1" || w.Title != "Fix the flaky login test" || w.Log != string(c.work.ID) ||
		w.Worker.ID != c.spider.ActorID || w.Worker.Label != "spider" || w.Worker.State != CardThinking || w.Unread != nil {
		t.Errorf("work row = %+v (worker %+v), want COV-1 by spider, no unread", w, w.Worker)
	}
	if got := agentIDs(r.Agents); !slices.Equal(got, []string{c.personal.ActorID}) {
		t.Errorf("agents = %q, want only alice's own session (spider is standing: operators only)", got)
	}
	if a := r.Agents[0]; !a.SSH || a.Card.Session != c.personal.ActorID {
		t.Errorf("agent item = %+v", a)
	}
	if r.History == nil || len(r.History) != 0 {
		t.Errorf("history = %#v, want an empty list", r.History)
	}
	if len(r.Groups) == 0 {
		t.Error("groups went before the clients moved to the sections")
	}

	// Following the log puts it in Me and gives its work row a count.
	if err := c.st.JoinChannel(c.work.ID, c.p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Intercom.PostTrusted(c.work, intercom.Squawk{From: ident.ID(c.spider.ActorID), Body: "progress"}); err != nil {
		t.Fatal(err)
	}
	r = railAs(t, c, false, "")
	if _, ok := meRow(r, string(c.work.ID)); !ok {
		t.Errorf("me lacks the followed work log: %q", rowIDs(r.Me))
	}
	if len(r.Work) != 1 || r.Work[0].Unread == nil || *r.Work[0].Unread != 1 {
		t.Errorf("followed work row = %+v, want 1 unread", r.Work)
	}

	// Once spider ends, its dm leaves Me and its unit has no current worker.
	c.spider.Phase = jam.PhaseGone
	if err := c.st.PutInstance(c.spider); err != nil {
		t.Fatal(err)
	}
	r = railAs(t, c, false, "")
	if _, ok := meRow(r, string(c.agentDM)); ok {
		t.Error("the dm with an ended session is still in Me")
	}
	if len(r.Work) != 0 {
		t.Errorf("work = %+v, want none: COV-1 has no live worker", r.Work)
	}
}

// Me leaves out archived conversations, and the rail's unread total counts
// only Me and History.
func TestRailMeSkipsArchived(t *testing.T) {
	e, p := fixture()
	inst, _, own := ticketSession(t, e, p)
	oc, _ := e.st.GetChannel(own)
	if _, err := e.Intercom.PostTrusted(oc, intercom.Squawk{From: ident.ID(inst.ActorID), Body: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.ArchiveChannel(own); err != nil {
		t.Fatal(err)
	}
	r := railJSON(t, Handler(e.Deps, nil), p)
	if _, ok := meRow(r, string(own)); ok {
		t.Errorf("me lists an archived session channel: %q", rowIDs(r.Me))
	}
	sum := 0
	for _, row := range r.Me {
		sum += row.Unread
	}
	if r.UnreadTotal != sum {
		t.Errorf("unreadTotal = %d, want Me's %d", r.UnreadTotal, sum)
	}
	if all := jam.UnreadTotal(e.st, e.Intercom, e.lg, p.UserID); all <= sum {
		t.Errorf("unread across every channel = %d, want more than Me's %d (the archived channel's message)", all, sum)
	}
}

// ?c= marks the open conversation selected and read in Me.
func TestRailMeSelected(t *testing.T) {
	c := newConvEnv(t)
	if _, err := c.Intercom.PostTrusted(mustChannel(t, c, c.bobDM), intercom.Squawk{From: c.bob, Body: "ping"}); err != nil {
		t.Fatal(err)
	}
	if row, _ := meRow(railAs(t, c, false, ""), string(c.bobDM)); row.Unread != 1 || row.Selected {
		t.Fatalf("bob dm = %+v, want 1 unread", row)
	}
	if row, _ := meRow(railAs(t, c, false, "?c="+string(c.bobDM)), string(c.bobDM)); row.Unread != 0 || !row.Selected {
		t.Errorf("open bob dm = %+v, want selected and read", row)
	}
}

func mustChannel(t *testing.T, c *convEnv, id ident.ID) jam.Channel {
	t.Helper()
	ch, ok := c.st.GetChannel(id)
	if !ok {
		t.Fatalf("no channel %s", id)
	}
	return ch
}

// Operators see every active session in Agents, oldest start first; an
// ended one goes.
func TestRailAgentsOperator(t *testing.T) {
	c := newConvEnv(t)
	starting := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "dev", Phase: jam.PhaseRaising, RaisedAt: time.Now()}
	ended := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "dev", Phase: jam.PhaseGone, RaisedAt: time.Now()}
	for _, inst := range []jam.Instance{starting, ended} {
		if err := c.st.PutInstance(inst); err != nil {
			t.Fatal(err)
		}
	}
	r := railAs(t, c, true, "")
	if got, want := agentIDs(r.Agents), []string{c.personal.ActorID, c.spider.ActorID, starting.ActorID}; !slices.Equal(got, want) {
		t.Errorf("operator's agents = %q, want %q (by start time)", got, want)
	}
}

// putOrder is PUT api/rail/order with body, as p (an operator when admin).
func putOrder(c *convEnv, admin bool, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/app/user/api/rail/order", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	Handler(c.Deps, nil).ServeHTTP(rec, asPrincipal(req, c.p, admin))
	return rec
}

// Me and Agents come in the saved order; the rest go last (Me newest
// message first, Agents by start time). Saving drops ids not in the section.
func TestRailSavedOrder(t *testing.T) {
	c := newConvEnv(t)
	personalDM := c.homeOf(t, c.personal)
	if rec := putOrder(c, false, `{"section":"me","ids":["`+engID+`","chn_01j9q3zzzzzzzzzzzzzzzzzzzz","`+string(c.bobDM)+`"]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT me order = %d %s", rec.Code, rec.Body)
	}
	if got := c.st.RailOrder(c.p.UserID, jam.RailMe); !slices.Equal(got, []string{engID, string(c.bobDM)}) {
		t.Errorf("saved me order = %q, want the unknown id dropped", got)
	}
	r := railAs(t, c, false, "")
	// The rest: the agent dm has spider's newest message; the personal dm none.
	if got, want := rowIDs(r.Me), []string{engID, string(c.bobDM), string(c.agentDM), personalDM}; !slices.Equal(got, want) {
		t.Errorf("me = %q, want %q", got, want)
	}

	if rec := putOrder(c, true, `{"section":"agents","ids":["`+c.spider.ActorID+`"]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT agents order = %d %s", rec.Code, rec.Body)
	}
	if got, want := agentIDs(railAs(t, c, true, "").Agents), []string{c.spider.ActorID, c.personal.ActorID}; !slices.Equal(got, want) {
		t.Errorf("agents = %q, want %q", got, want)
	}
	// alice, not an operator, can't open spider's view: saving drops it.
	if rec := putOrder(c, false, `{"section":"agents","ids":["`+c.spider.ActorID+`","`+c.personal.ActorID+`"]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("PUT agents order = %d %s", rec.Code, rec.Body)
	}
	if got := c.st.RailOrder(c.p.UserID, jam.RailAgents); !slices.Equal(got, []string{c.personal.ActorID}) {
		t.Errorf("saved agents order = %q, want only what alice's Agents lists", got)
	}
}

func TestRailOrderRefusals(t *testing.T) {
	c := newConvEnv(t)
	for _, body := range []string{`{"section":"work","ids":[]}`, `{"section":"history","ids":[]}`, `{"ids":[]}`, `not json`} {
		if rec := putOrder(c, false, body); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, rec.Code)
		}
	}
	req := httptest.NewRequest("PUT", "/app/user/api/rail/order", strings.NewReader(`{"section":"me","ids":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	Handler(c.Deps, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("PUT without a participant = %d, want 403", rec.Code)
	}
}
