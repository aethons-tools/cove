package userui

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

// The Inbox over the conversation kinds (the manual's inbox/channels,
// inbox/composer, and inbox/asks; conversations.adoc#kinds).

// convEnv is sessionEnv plus bob, a dm alice–spider and a dm alice–bob, and
// a work log COV-1 that spider works and alice is not in.
type convEnv struct {
	*env
	p                jam.Participant
	personal, spider jam.Instance
	bob              ident.ID
	agentDM, bobDM   ident.ID
	work             jam.Channel
}

func newConvEnv(t *testing.T) *convEnv {
	t.Helper()
	e, p, personal, spider, _ := sessionEnv(t)
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	c := &convEnv{env: e, p: p, personal: personal, spider: spider, bob: bob}
	post := func(pl jam.Planned, err error) ident.ID {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		m, err := e.Intercom.Post(pl, intercom.Squawk{From: p.UserID, Body: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		return m.Channel
	}
	// A dm with an agent (as an agent's send to user:alice opens one).
	proj, _ := e.st.GetProject("proj")
	dm, err := e.homeCh(spider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.PostTrusted(dm, intercom.Squawk{From: ident.ID(spider.ActorID), Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	c.agentDM = dm.ID
	c.bobDM = post(e.Intercom.PlanPersonChat(p.UserID, bob))
	work, err := e.st.CreateChannel(jam.Channel{ID: ident.New(ident.Channel), ProjectID: proj.ID, Kind: jam.SourceWork, Key: "linear/COV-1", Label: "COV-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.JoinChannel(work.ID, ident.ID(spider.ActorID), 0); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetChannelWorker(work.ID, ident.ID(spider.ActorID)); err != nil {
		t.Fatal(err)
	}
	c.work, _ = e.st.GetChannel(work.ID)
	return c
}

// The rail groups Needs you, Agents (dms and groups with a live agent), and
// Conversations (rooms, work logs, dms with people). A dm with an agent is
// its status card, whose name opens the session view for those who may
// open it (spider is a standing session: operators); every row says its
// conversation kind.
func TestRailConversationGroups(t *testing.T) {
	c := newConvEnv(t)
	if _, err := c.Intercom.PostTrusted(c.work, intercom.Squawk{From: ident.ID(c.spider.ActorID), Body: "progress"}); err != nil {
		t.Fatal(err)
	}
	if err := c.st.JoinChannel(c.work.ID, c.p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	r := railJSON(t, Handler(c.Deps, nil), c.p)
	var titles []string
	for _, g := range r.Groups {
		titles = append(titles, g.Title)
	}
	if !slices.Equal(titles, []string{"Agents", "Conversations"}) {
		t.Fatalf("groups = %q", titles)
	}
	spiderHome := string(c.agentDM) // spider's conversation for alice: it has no unit
	for _, tc := range []struct {
		id, title, kind string
		card            bool
		sessionView     string
	}{
		{string(c.agentDM), "Agents", "dm", true, ""},
		{string(c.bobDM), "Conversations", "dm", false, ""},
		{string(c.work.ID), "Conversations", "work", false, ""},
		{engID, "Conversations", "room", false, ""},
	} {
		row, bucket, ok := rowByID(r, tc.id)
		if !ok {
			t.Fatalf("%s (%s) not in the rail", tc.id, tc.kind)
		}
		if bucketTitle[bucket] != tc.title || row.Kind != tc.kind || (row.Card != nil) != tc.card || row.SessionView != tc.sessionView {
			t.Errorf("%s row: in %q, kind %q, card %v, sessionView %q; want %q, %q, %v, %q",
				row.Label, bucketTitle[bucket], row.Kind, row.Card != nil, row.SessionView, tc.title, tc.kind, tc.card, tc.sessionView)
		}
	}
	if row, _, _ := rowByID(r, string(c.agentDM)); row.Card == nil || row.Card.Session != c.spider.ActorID {
		t.Errorf("agent dm card = %+v, want spider's", row.Card)
	}
	rec := httptest.NewRecorder()
	Handler(c.Deps, nil).ServeHTTP(rec, asPrincipal(httptest.NewRequest("GET", "/app/user/api/rail", nil), c.p, true))
	op := decodeAs[RailResponse](t, rec)
	if row, _, _ := rowByID(op, string(c.agentDM)); row.SessionView != spiderHome {
		t.Errorf("operator's %s row: sessionView %q, want %q", row.Label, row.SessionView, spiderHome)
	}

	// Once the agent ends, its dm is an ordinary conversation.
	c.spider.Phase = jam.PhaseGone
	if err := c.st.PutInstance(c.spider); err != nil {
		t.Fatal(err)
	}
	r = railJSON(t, Handler(c.Deps, nil), c.p)
	if row, bucket, ok := rowByID(r, string(c.agentDM)); !ok || bucketTitle[bucket] != "Conversations" || row.Card != nil {
		t.Errorf("dm with an ended agent: in %q, card %v", bucketTitle[bucket], row.Card)
	}
}

func rowByID(r RailResponse, id string) (ChannelRow, jam.AttentionBucket, bool) {
	for _, g := range r.Groups {
		for _, row := range g.Rows {
			if row.ID == id {
				return row, g.Bucket, true
			}
		}
	}
	return ChannelRow{}, "", false
}

// The header's kind and controls: Join and Leave on rooms and work logs;
// Call in lists people and the agents the viewer may reach; a dm and a
// group have none. A room lists its
// members, its sessions marked as agents.
func TestConversationKindsAndControls(t *testing.T) {
	c := newConvEnv(t)
	labels := func(os []NewMessageOption) []string {
		var out []string
		for _, o := range os {
			out = append(out, o.Label+"/"+o.Kind)
		}
		return out
	}
	_, room, _ := getConv(t, c.env, c.p, engID, "")
	if room.Kind != "room" || !room.CanLeave || room.CanJoin {
		t.Errorf("room: kind %q, canLeave %v, canJoin %v", room.Kind, room.CanLeave, room.CanJoin)
	}
	if got := labels(room.CallIn); !slices.Contains(got, "bob/person") || !slices.Contains(got, "spider/agent") {
		t.Errorf("room call in = %q, want bob and spider", got)
	}
	if err := c.st.JoinChannel(ident.ID(engID), ident.ID(c.spider.ActorID), 0); err != nil {
		t.Fatal(err)
	}
	_, room, _ = getConv(t, c.env, c.p, engID, "")
	var members []string
	for _, m := range room.Members {
		members = append(members, m.Label+"/"+m.Kind)
	}
	if got := members; !slices.Contains(got, "spider/agent") || len(got) != 2 {
		t.Errorf("room members = %q, want the viewer and spider (an agent)", got)
	}
	if got := labels(room.CallIn); slices.Contains(got, "spider/agent") {
		t.Errorf("room call in = %q, offers a member", got)
	}
	_, work, _ := getConv(t, c.env, c.p, string(c.work.ID), "")
	if work.Kind != "work" || !work.CanJoin || work.CanLeave {
		t.Errorf("work log: kind %q, canJoin %v, canLeave %v", work.Kind, work.CanJoin, work.CanLeave)
	}
	for _, id := range []ident.ID{c.agentDM, c.bobDM} {
		_, conv, _ := getConv(t, c.env, c.p, string(id), "")
		if conv.Kind != "dm" || conv.CanJoin || conv.CanLeave || len(conv.CallIn) != 0 {
			t.Errorf("%s: kind %q, canJoin %v, canLeave %v, callIn %+v", conv.Label, conv.Kind, conv.CanJoin, conv.CanLeave, conv.CallIn)
		}
	}
	pl, err := c.Intercom.PlanPersonChat(c.p.UserID, c.bob, mustAddPerson(t, c.env, "dave"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.Intercom.Post(pl, intercom.Squawk{From: c.p.UserID, Body: "hi all"})
	if err != nil {
		t.Fatal(err)
	}
	if _, group, _ := getConv(t, c.env, c.p, string(m.Channel), ""); group.Kind != "group" || (group.Label != "bob, dave" && group.Label != "dave, bob") || group.CanLeave {
		t.Errorf("group: kind %q, label %q, canLeave %v", group.Kind, group.Label, group.CanLeave)
	}
}

func mustAddPerson(t *testing.T, e *env, name string) ident.ID {
	t.Helper()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: name}); err != nil {
		t.Fatal(err)
	}
	id, _ := e.st.LookupName(ident.User, name)
	return id
}

// New message offers people (a dm, or several for a group), live agents (a
// dm), units of work (their log, and a dm with the worker while one works
// it), and rooms.
func TestNewMessageOptionsByKind(t *testing.T) {
	c := newConvEnv(t)
	c.Deps.Intercom = jam.NewIntercom(c.st, func() (ident.ID, bool) { return "linear", true }, c.lg, nil, nil) // COV-1 is on the tracker
	ended := jam.Instance{ActorID: string(ident.New(ident.Session)), Project: "proj", Role: "dev", Name: "gone", Phase: jam.PhaseGone}
	if err := c.st.PutInstance(ended); err != nil {
		t.Fatal(err)
	}
	got := map[string]NewMessageOption{}
	for _, o := range newMessageOptions(c.p, c.Deps) {
		got[o.Label+"/"+o.Kind] = o
	}
	want := map[string]NewMessageOption{
		"bob/person":   {To: "user:" + string(c.bob), Label: "bob", Kind: "person", Project: "proj"},
		"spider/agent": {To: "session:" + c.spider.ActorID, Label: "spider", Kind: "agent", Project: "proj"},
		"COV-1/work":   {To: string(c.work.ID), Worker: "worker:COV-1", Label: "COV-1", Kind: "work", Project: "proj"},
		"eng/room":     {To: engID, Label: "eng", Kind: "room", Project: "proj"},
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %+v, want %+v", k, got[k], w)
		}
	}
	for k := range got {
		if k == "alice/person" || k == "gone/agent" {
			t.Errorf("offered %s", k)
		}
	}

	// With no live worker, the log is still offered, its worker isn't.
	c.spider.Phase = jam.PhaseGone
	if err := c.st.PutInstance(c.spider); err != nil {
		t.Fatal(err)
	}
	for _, o := range newMessageOptions(c.p, c.Deps) {
		if o.Kind == "work" && o.Worker != "" {
			t.Errorf("work with an ended worker offers %q", o.Worker)
		}
	}
}

// In a work log, any project member can answer an ask, not only its members.
func TestWorkLogAskAnswerableByProjectMember(t *testing.T) {
	c := newConvEnv(t)
	m, err := c.Intercom.PostTrusted(c.work, intercom.Squawk{From: ident.ID(c.spider.ActorID), Body: "asked", ContentType: intercom.ContentPlain,
		Parts: []intercom.Part{intercom.AskPart(intercom.AskEvent{Event: intercom.AskEventAsk, Question: "Merge?", Options: yesNoOpts})}})
	if err != nil {
		t.Fatal(err)
	}
	_, conv, body := getConv(t, c.env, c.p, string(c.work.ID), "")
	if len(conv.OpenAsks) != 1 || !conv.OpenAsks[0].CanAnswer || !conv.NeedsYou {
		t.Fatalf("alice, not in the log: openAsks %+v, needsYou %v\n%s", conv.OpenAsks, conv.NeedsYou, body)
	}
	if code, body := answerJSON(t, c.env, c.p, m.ID, `{"version":1,"choice":"yes"}`); code != http.StatusNoContent {
		t.Fatalf("answer = %d %s", code, body)
	}
}
