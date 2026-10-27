package userui

// The API's share of the authorization and behavior tests the old Inbox's
// HTML routes carried (spec §5): written against the API before the cutover
// deleted those routes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

func railJSON(t *testing.T, h http.Handler, p jam.Participant) RailResponse {
	t.Helper()
	rec := apiGet(t, h, &p, "/app/user/api/rail")
	var r RailResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &r) != nil {
		t.Fatalf("rail = %d %s", rec.Code, rec.Body)
	}
	return r
}

// rowLabeled is the rail row labelled label, and its group's bucket.
func rowLabeled(r RailResponse, label string) (ChannelRow, jam.AttentionBucket, bool) {
	for _, g := range r.Groups {
		for _, row := range g.Rows {
			if row.Label == label {
				return row, g.Bucket, true
			}
		}
	}
	return ChannelRow{}, "", false
}

// The rail counts each conversation's unread and lists the viewer's History.
func TestAPIRailUnreadAndHistory(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	pl, err := e.Intercom.PlanPersonChat(bob, p.UserID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	legacy := intercom.NewLegacyMemLog()
	if _, err := legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "old-cove"}, To: []intercom.Target{{Kind: "human", Ref: "alice"}},
		Body: "from the old log", Project: "proj"}); err != nil {
		t.Fatal(err)
	}
	e.Legacy = legacy
	h := Handler(e.Deps, nil)

	r := railJSON(t, h, p)
	if row, _, ok := rowLabeled(r, "bob"); !ok || row.Unread != 1 {
		t.Errorf("bob's chat = %+v %v, want 1 unread", row, ok)
	}
	var history *ChannelRow
	for _, g := range r.Groups {
		if g.Title == "History (before the upgrade)" && len(g.Rows) > 0 {
			history = &g.Rows[0]
		}
	}
	if history == nil || !strings.HasPrefix(history.ID, "legacy:") {
		t.Fatalf("no History group in %+v", r.Groups)
	}
	if err := e.st.CommitChannelRead(p.UserID, m.Channel, m.Seq); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := rowLabeled(railJSON(t, h, p), "bob"); row.Unread != 0 {
		t.Errorf("read chat still unread: %+v", row)
	}
	// A History conversation reads but takes no reply.
	_, c, body := getConv(t, e, p, history.ID, "")
	if c.SendTo != "" || !strings.Contains(body, "from the old log") {
		t.Errorf("history conversation = %s", body)
	}
	// Leaving the project takes its channels off the rail at once.
	if err := e.st.RemoveMember(e.room.ProjectID, p.UserID); err != nil {
		t.Fatal(err)
	}
	after := railJSON(t, h, p)
	if _, _, ok := rowLabeled(after, "eng"); ok {
		t.Errorf("eng on the rail after leaving the project: %+v", after)
	}
	if _, _, ok := rowLabeled(after, "bob"); ok {
		t.Errorf("bob on the rail after leaving the project: %+v", after)
	}
}

// A viewer renamed in a name clash doesn't see the old name's History.
func TestAPIHistoryIsTheViewersNameOnly(t *testing.T) {
	e, p := fixture()
	legacy := intercom.NewLegacyMemLog()
	if _, err := legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "old-cove"}, To: []intercom.Target{{Kind: "human", Ref: "alice"}},
		Body: "for the other alice", Project: "elsewhere"}); err != nil {
		t.Fatal(err)
	}
	e.Legacy = legacy
	p.Name = "alice-proj" // as the clash renamed them
	for _, g := range railJSON(t, Handler(e.Deps, nil), p).Groups {
		if g.Title == "History (before the upgrade)" {
			t.Errorf("a renamed viewer sees human:alice's history: %+v", g)
		}
	}
}

// The rail carries the unread total (the app badge).
func TestAPIRailUnreadTotal(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bobID, _ := e.st.LookupName(ident.User, "bob")
	e.post(bobID, "one", "")
	e.post(bobID, "two", "")
	want := jam.UnreadTotal(e.st, e.Intercom, e.lg, p.UserID)
	if want < 2 {
		t.Fatalf("fixture unread = %d, want ≥ 2", want)
	}
	if got := railJSON(t, Handler(e.Deps, nil), p).UnreadTotal; got != want {
		t.Errorf("unreadTotal = %d, want %d", got, want)
	}
}

// Interrupt, compact and clear run only while the session's activity allows.
func TestAPISessionCommandsMatrix(t *testing.T) {
	e, p, personal, _, ops := sessionEnv(t)
	for _, tt := range []struct {
		name     string
		phase    jam.Phase
		activity jam.Activity
		cmd      jam.SessionCommand
		want     int
	}{
		{"interrupt running", jam.PhaseLive, jam.ActivityRunning, jam.CmdInterrupt, http.StatusNoContent},
		{"interrupt holding", jam.PhaseLive, jam.ActivityHolding, jam.CmdInterrupt, http.StatusConflict},
		{"interrupt waiting", jam.PhaseLive, jam.ActivityWaiting, jam.CmdInterrupt, http.StatusConflict},
		{"compact running", jam.PhaseLive, jam.ActivityRunning, jam.CmdCompact, http.StatusNoContent},
		{"compact holding", jam.PhaseLive, jam.ActivityHolding, jam.CmdCompact, http.StatusNoContent},
		{"compact waiting", jam.PhaseLive, jam.ActivityWaiting, jam.CmdCompact, http.StatusNoContent},
		{"clear running", jam.PhaseLive, jam.ActivityRunning, jam.CmdClear, http.StatusConflict},
		{"clear holding", jam.PhaseLive, jam.ActivityHolding, jam.CmdClear, http.StatusConflict},
		{"clear waiting", jam.PhaseLive, jam.ActivityWaiting, jam.CmdClear, http.StatusNoContent},
		{"clear blocked", jam.PhaseLive, jam.ActivityBlocked, jam.CmdClear, http.StatusConflict},
		{"clear paused", jam.PhaseIdled, jam.ActivityRunning, jam.CmdClear, http.StatusNoContent},
		{"interrupt paused", jam.PhaseIdled, jam.ActivityRunning, jam.CmdInterrupt, http.StatusConflict},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inst := personal
			inst.Phase, inst.Activity = tt.phase, tt.activity
			if err := e.st.PutInstance(inst); err != nil {
				t.Fatal(err)
			}
			ops.cmds = nil
			rec := sessAPI(t, e, p, "POST", inst.ActorID+"/"+string(tt.cmd), "{}", false)
			if rec.Code != tt.want {
				t.Fatalf("POST %s = %d %s, want %d", tt.cmd, rec.Code, rec.Body, tt.want)
			}
			if tt.want == http.StatusConflict {
				if len(ops.cmds) != 0 {
					t.Errorf("a refused %s was sent: %v", tt.cmd, ops.cmds)
				}
				if want := string(tt.cmd) + " is not available while the session is"; !strings.Contains(rec.Body.String(), want) || !strings.Contains(rec.Body.String(), `"code":"conflict"`) {
					t.Errorf("409 body = %s, want %q", rec.Body, want)
				}
				return
			}
			if want := (cmdCall{inst.ActorID, tt.cmd, "alice"}); len(ops.cmds) != 1 || ops.cmds[0] != want {
				t.Errorf("sent %v, want [%v]", ops.cmds, want)
			}
		})
	}
}

// Any viewer who can see a session may send it a command.
func TestAPISessionCommandsAnyViewer(t *testing.T) {
	e, _, personal, _, ops := sessionEnv(t)
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob", Identity: []jam.OIDCIdentity{{Issuer: "https://idp", Subject: "sub-bob"}}}); err != nil {
		t.Fatal(err)
	}
	bobID, _ := e.st.LookupName(ident.User, "bob")
	bob := jam.Participant{Issuer: "https://idp", Subject: "sub-bob", UserID: bobID, Projects: []string{"proj"}, Name: "bob"}
	carol := jam.Participant{UserID: ident.New(ident.User), Name: "carol", Projects: []string{"proj"}}
	if rec := sessAPI(t, e, carol, "POST", personal.ActorID+"/compact", "{}", false); rec.Code != http.StatusNotFound {
		t.Errorf("outsider compact = %d, want 404", rec.Code)
	}
	if err := e.st.JoinChannel(ident.ID(e.homeOf(t, personal)), bobID, 0); err != nil {
		t.Fatal(err)
	}
	if rec := sessAPI(t, e, bob, "POST", personal.ActorID+"/compact", "{}", false); rec.Code != http.StatusNoContent {
		t.Fatalf("viewer compact = %d %s, want 204", rec.Code, rec.Body)
	}
	if want := (cmdCall{personal.ActorID, jam.CmdCompact, "bob"}); len(ops.cmds) != 1 || ops.cmds[0] != want {
		t.Errorf("sent %v, want [%v]", ops.cmds, want)
	}
}

// Artifacts lists the home channel's boards, files, asks and links.
func TestAPISessionPaneArtifacts(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	home, err := e.Intercom.HomeChannel(personal)
	if err != nil {
		t.Fatal(err)
	}
	sid := ident.ID(personal.ActorID)
	for _, body := range []string{
		"Opened https://github.com/aethons-tools/cove/pull/472, see (https://example.com/docs).",
		"Again: https://github.com/aethons-tools/cove/pull/472 and https://github.com/aethons-tools/cove/issues/9",
	} {
		if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: sid, Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: sid, Body: "asked", ContentType: intercom.ContentPlain,
		Parts: []intercom.Part{intercom.AskPart(intercom.AskEvent{Event: intercom.AskEventAsk, Question: "Merge it?", Options: yesNoOpts})}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Deps.Boards.Create(jam.Poster{ID: p.UserID}, string(home.ID), "Plan", time.Now()); err != nil {
		t.Fatal(err)
	}
	e.post(p.UserID, "elsewhere https://example.org/room", "")

	art := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", personal.ActorID+"/pane/artifacts", "", false))
	labels := map[string]int{}
	for _, l := range art.Links {
		labels[l.Label]++
		if strings.Contains(l.URL, "example.org") {
			t.Errorf("artifacts list another channel's link: %+v", l)
		}
	}
	if labels["cove PR #472"] != 1 || labels["cove issue #9"] != 1 || labels["https://example.com/docs"] != 1 {
		t.Errorf("links = %+v", art.Links)
	}
	if len(art.Asks) != 1 || art.Asks[0].Question != "Merge it?" || art.Asks[0].State != "open" {
		t.Errorf("asks = %+v", art.Asks)
	}
	if len(art.Boards) != 1 || art.Boards[0].Title != "Plan" || art.Boards[0].Channel != string(home.ID) {
		t.Errorf("boards = %+v", art.Boards)
	}
}

// Status names the session's kit: published, or a draft's tree.
func TestAPISessionPaneStatusKit(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	kit := func() string {
		st := decodeAs[SessionPane](t, sessAPI(t, e, p, "GET", personal.ActorID+"/pane/status", "", false))
		for _, r := range st.Status {
			if r.Name == "Kit" {
				return r.Value
			}
		}
		return ""
	}
	personal.Kit = jam.KitRef{ID: "my-app", Version: 3}
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	if k := kit(); k != "my-app@v3" {
		t.Errorf("published kit = %q", k)
	}
	personal.Kit = jam.KitRef{ID: "my-app"}
	personal.KitDraft = &jam.KitDraftRef{Kit: "my-app", Tree: strings.Repeat("ab", 32)}
	if err := e.st.PutInstance(personal); err != nil {
		t.Fatal(err)
	}
	if k := kit(); k != "my-app (draft @abababa)" {
		t.Errorf("draft studio kit = %q", k)
	}
}

// The feed backfills the tail, then streams live events, agent text inert.
func TestThinkingJSONBackfillAndLive(t *testing.T) {
	e, p, personal, store, hub := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 2)
	if err := store.Append(textEvent(personal.ActorID, 3, "<script>x</script>")); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(asWho(Handler(e.Deps, nil), p))
	defer func() { srv.CloseClientConnections(); srv.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/app/user/session/"+personal.ActorID+"/thinking/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	rd := sseLines(resp.Body)
	var buf strings.Builder
	sseRead(t, rd, &buf, "id: "+thinkStream+":3")
	evs := func(s string) []ThinkingEvent {
		var out []ThinkingEvent
		for _, l := range strings.Split(s, "\n") {
			if d, ok := strings.CutPrefix(l, "data: {"); ok {
				var ev ThinkingEvent
				if err := json.Unmarshal([]byte("{"+d), &ev); err != nil {
					t.Fatalf("data %q: %v", l, err)
				}
				out = append(out, ev)
			}
		}
		return out
	}
	back := evs(buf.String())
	if len(back) != 3 || back[0].Seq != 1 || back[2].Kind != "text" || !strings.Contains(back[2].Text+back[2].Detail, "<script>x</script>") {
		t.Errorf("backfill = %+v", back)
	}
	if strings.Contains(buf.String(), "<script>") {
		t.Error("agent markup is raw in the feed (JSON escapes it)")
	}
	ev := textEvent(personal.ActorID, 4, "live one")
	if err := store.Append(ev); err != nil {
		t.Fatal(err)
	}
	hub.Publish(ev)
	var live strings.Builder
	sseRead(t, rd, &live, "id: "+thinkStream+":4")
	if l := evs(live.String()); len(l) != 1 || l[0].Seq != 4 {
		t.Errorf("live = %+v", l)
	}
}

// Thinking's page, raw and pane routes are hidden from an outsider and from
// a member who left (TestThinkingHiddenFromOutsiders covers the feed).
func TestAPIThinkingHiddenFromOutsiders(t *testing.T) {
	e, p, personal, store, _ := thinkEnv(t)
	appendText(t, store, personal.ActorID, 1, 3)
	bob := jam.Participant{UserID: ident.New(ident.User), Name: "bob", Projects: []string{"proj"}}
	paths := []string{personal.ActorID + "/thinking?stream=" + thinkStream + "&before=3", personal.ActorID + "/thinking/raw?stream=" + thinkStream + "&seq=1", personal.ActorID + "/pane/thinking"}
	for _, path := range paths {
		if rec := sessAPI(t, e, bob, "GET", path, "", false); rec.Code != http.StatusNotFound {
			t.Errorf("outsider %s = %d", path, rec.Code)
		}
	}
	if err := e.st.LeaveChannel(ident.ID(e.homeOf(t, personal)), p.UserID, 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if rec := sessAPI(t, e, p, "GET", path, "", false); rec.Code != http.StatusNotFound {
			t.Errorf("removed member %s = %d", path, rec.Code)
		}
	}
}

// An ask for someone else shows read-only and doesn't put the room in Needs
// you; one for anyone does.
func TestAPIAskForOthers(t *testing.T) {
	e, p := fixture()
	carol, err := e.st.CreateUser(jam.User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	e.ask(intercom.AskEvent{Question: "Carol only?", Options: yesNoOpts, Of: []ident.ID{carol.ID}})
	_, c, _ := getConv(t, e, p, engID, "")
	if len(c.OpenAsks) != 1 || c.OpenAsks[0].CanAnswer || c.OpenAsks[0].For != "carol" {
		t.Errorf("an ask for carol = %+v, want read-only, for carol", c.OpenAsks)
	}
	h := Handler(e.Deps, nil)
	if r := railJSON(t, h, p); len(r.Groups) > 0 && r.Groups[0].Bucket == jam.BucketNeedsYou {
		t.Fatalf("an ask for carol put the room in Needs you: %+v", r.Groups)
	}
	e.ask(intercom.AskEvent{Question: "Anyone?", Options: yesNoOpts})
	r := railJSON(t, h, p)
	if r.Groups[0].Bucket != jam.BucketNeedsYou || r.Groups[0].Rows[0].OpenAsks != 2 {
		t.Fatalf("rail = %+v, want the room in Needs you with 2 open asks", r.Groups)
	}
}
