package userui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/appui"
)

// The view models' JSON names are the app's contract (types.ts): pin them,
// and that Go-only fields stay out.
func TestConversationJSONShape(t *testing.T) {
	at := time.Date(2026, 10, 26, 9, 5, 0, 0, time.UTC)
	c := Conversation{ChannelID: "chn_x", Label: "eng", SendTo: "chn_x", LastSeq: 3, Earlier: true,
		Messages: []MessageRow{{From: "bob", Body: "<p>hi</p>", Raw: "hi", At: "09:05", Time: at, Seq: 3, Card: "ask", Ask: &AskCard{ID: "a1"}},
			{From: "bob", Time: at, Seq: 4, Card: "board", Board: &BoardCard{ID: "brd_1", Title: "Plan"}}},
		Sessions:   []SessionRow{{Label: "spider", Text: "is running", Tool: "Bash", Class: "busy"}},
		CallIn:     []NewMessageOption{{To: "user:usr_1", Label: "bob", Kind: "person"}},
		SessionIDs: []string{"ses_1"},
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"channelId", "label", "kind", "project", "phase", "needsYou", "sendTo", "messages", "lastSeq", "earlier", "sessionIds", "sessions", "canJoin", "canLeave", "callIn", "openAsks", "session", "newBoard"} {
		if _, ok := got[k]; !ok {
			t.Errorf("conversation JSON lacks %q: %s", k, b)
		}
	}
	for _, k := range []string{"HasMessages", "hasMessages", "OpenAsks", "Board", "board", "Head", "head", "NewBoard"} {
		if _, ok := got[k]; ok {
			t.Errorf("conversation JSON carries template-only %q", k)
		}
	}
	m := got["messages"].([]any)[0].(map[string]any)
	for k, want := range map[string]any{"from": "bob", "body": "<p>hi</p>", "raw": "hi", "at": "09:05", "time": "2026-10-26T09:05:00Z", "seq": float64(3), "card": "ask", "mine": false, "plain": false, "system": false} {
		if m[k] != want {
			t.Errorf("message[%q] = %v, want %v", k, m[k], want)
		}
	}
	if a, ok := m["ask"].(map[string]any); !ok || a["id"] != "a1" {
		t.Errorf("message ask = %v, want the card", m["ask"])
	}
	m2 := got["messages"].([]any)[1].(map[string]any)
	if bd, ok := m2["board"].(map[string]any); !ok || bd["id"] != "brd_1" || bd["title"] != "Plan" {
		t.Errorf("message board = %v, want {id, title}", m2["board"])
	}
	for _, k := range []string{"Ask", "files", "reAsk", "Board", "board", "Channel", "channel"} {
		if _, ok := m[k]; ok {
			t.Errorf("message JSON carries %q (template-only, or omitempty when empty): %s", k, b)
		}
	}
}
func getConv(t *testing.T, e *env, p jam.Participant, id, query string) (int, Conversation, string) {
	t.Helper()
	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/conversations/"+url.PathEscape(id)+query)
	var c Conversation
	if rec.Code == 200 {
		checkJSONHeaders(t, rec)
		if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, c, rec.Body.String()
}

func TestAPIConversation(t *testing.T) {
	e, p := fixture()
	code, c, body := getConv(t, e, p, engID, "")
	if code != 200 {
		t.Fatalf("code = %d: %s", code, body)
	}
	if c.Label != "eng" || c.SendTo != engID || len(c.Messages) != 1 || c.Messages[0].Raw != "hi from alice" || !c.Messages[0].Mine || c.Earlier {
		t.Errorf("conversation = %+v", c)
	}
	if !strings.Contains(string(c.Messages[0].Body), "<p>hi from alice</p>") || c.Messages[0].Time.IsZero() {
		t.Errorf("message = %+v", c.Messages[0])
	}
	if !c.CanLeave || c.CanJoin {
		t.Errorf("member: canLeave=%v canJoin=%v", c.CanLeave, c.CanJoin)
	}
	for _, k := range []string{`"sessions":[]`, `"sessionIds":[]`} {
		if !strings.Contains(body, k) {
			t.Errorf("empty list not [] (%s): %s", k, body)
		}
	}
}

// Raw HTML in a body never reaches the app: squawkrender omits it.
func TestAPIConversationBodyIsSanitized(t *testing.T) {
	e, p := fixture()
	e.post(p.UserID, "<script>alert(1)</script> <img src=x onerror=alert(2)> [x](javascript:alert(3))", "")
	_, c, _ := getConv(t, e, p, engID, "")
	b := string(c.Messages[len(c.Messages)-1].Body)
	for _, bad := range []string{"<script", "onerror", "javascript:"} {
		if strings.Contains(b, bad) {
			t.Errorf("body carries %q: %s", bad, b)
		}
	}
}

// Not visible, missing and removed read the same: no label, no existence.
func TestAPIConversationNotFoundIsUniform(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	carol, _ := e.st.LookupName(ident.User, "carol")
	pl, err := e.Intercom.PlanPersonChat(bob, carol)
	if err != nil {
		t.Fatal(err)
	}
	m, err := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, id := range []string{string(m.Channel), "chn_01j9q3zzzzzzzzzzzzzzzzzz", "legacy:nope"} {
		code, _, body := getConv(t, e, p, id, "")
		if code != 404 {
			t.Errorf("%s: code = %d, want 404", id, code)
		}
		bodies = append(bodies, body)
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] || strings.Contains(bodies[0], "secret") || !strings.Contains(bodies[0], `"code":"not_found"`) {
		t.Errorf("404 bodies differ or leak: %q", bodies)
	}
}

func TestAPIConversationPages(t *testing.T) {
	e, p := fixture()
	for i := 1; i <= 249; i++ { // + the fixture's message = 250
		e.post(p.UserID, fmt.Sprintf("m%d", i), "")
	}
	_, first, _ := getConv(t, e, p, engID, "")
	if len(first.Messages) != 200 || !first.Earlier || first.Messages[199].Raw != "m249" {
		t.Fatalf("first page: %d messages, earlier=%v, last=%q", len(first.Messages), first.Earlier, first.Messages[len(first.Messages)-1].Raw)
	}
	before := first.Messages[0].Seq
	_, older, _ := getConv(t, e, p, engID, fmt.Sprintf("?before=%d", before))
	if len(older.Messages) != 50 || older.Earlier || older.Messages[49].Seq >= before || older.Messages[0].Raw != "hi from alice" {
		t.Errorf("older page: %d messages, earlier=%v", len(older.Messages), older.Earlier)
	}
	_, small, _ := getConv(t, e, p, engID, "?limit=10")
	if len(small.Messages) != 10 || !small.Earlier {
		t.Errorf("limit=10: %d, earlier=%v", len(small.Messages), small.Earlier)
	}
	_, capped, _ := getConv(t, e, p, engID, "?limit=100000")
	if len(capped.Messages) != 250 { // under the 500 cap: all of them
		t.Errorf("limit cap: %d", len(capped.Messages))
	}
	if code, _, _ := getConv(t, e, p, engID, "?before=abc"); code != 400 {
		t.Errorf("before=abc = %d, want 400", code)
	}
}

func TestPageOf(t *testing.T) {
	ms := []MessageRow{{Seq: 2}, {Seq: 5}, {Seq: 9}}
	for _, tc := range []struct {
		before    int64
		limit     int
		want      []int64
		wantEarly bool
	}{
		{0, 10, []int64{2, 5, 9}, false},
		{0, 2, []int64{5, 9}, true},
		{9, 10, []int64{2, 5}, false},
		{6, 1, []int64{5}, true},
		{2, 10, nil, false},
	} {
		got, early := pageOf(ms, tc.before, tc.limit)
		var seqs []int64
		for _, m := range got {
			seqs = append(seqs, m.Seq)
		}
		if fmt.Sprint(seqs) != fmt.Sprint(tc.want) || early != tc.wantEarly {
			t.Errorf("pageOf(before=%d, limit=%d) = %v %v, want %v %v", tc.before, tc.limit, seqs, early, tc.want, tc.wantEarly)
		}
	}
}

// History: routable by its legacy: id, read-only.
func TestAPIConversationHistory(t *testing.T) {
	e, p := fixture()
	legacy := intercom.NewLegacyMemLog()
	if _, err := legacy.Append(intercom.LegacySquawk{From: intercom.Target{Kind: "actor", Ref: "old-cove"}, To: []intercom.Target{{Kind: "human", Ref: "alice"}}, Body: "from the old log", Project: "proj"}); err != nil {
		t.Fatal(err)
	}
	e.Legacy = legacy
	var id string
	for _, g := range Rail(p, e.Deps, "") {
		for _, r := range g.Rows {
			if strings.HasPrefix(r.ID, legacyPrefix) {
				id = r.ID
			}
		}
	}
	if id == "" {
		t.Fatal("no History row")
	}
	code, c, body := getConv(t, e, p, id, "")
	if code != 200 || len(c.Messages) != 1 || c.Messages[0].Raw != "from the old log" {
		t.Fatalf("History = %d %s", code, body)
	}
	if c.SendTo != "" || c.CanJoin || c.CanLeave || len(c.CallIn) != 0 {
		t.Errorf("History is not read-only: %+v", c)
	}
}

func TestAPIPresenceAndRailSelection(t *testing.T) {
	e, p := fixture()
	h := Handler(e.Deps, nil)
	rec := apiGet(t, h, &p, "/app/user/api/conversations/"+engID+"/presence")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"sessions":[]`) {
		t.Errorf("presence = %d %s", rec.Code, rec.Body)
	}
	if rec := apiGet(t, h, &p, "/app/user/api/conversations/chn_01j9q3zzzzzzzzzzzzzzzzzz/presence"); rec.Code != 404 {
		t.Errorf("presence of a missing channel = %d", rec.Code)
	}
	// bob's unread chat reads as read while it is the open conversation.
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	pl, _ := e.Intercom.PlanPersonChat(bob, p.UserID)
	m, _ := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "ping"})
	for q, want := range map[string]int{"": 1, "?c=" + string(m.Channel): 0} {
		var rr RailResponse
		_ = json.Unmarshal(apiGet(t, h, &p, "/app/user/api/rail"+q).Body.Bytes(), &rr)
		if rr.UnreadTotal != want {
			t.Errorf("rail%s unreadTotal = %d, want %d", q, rr.UnreadTotal, want)
		}
	}
}
func apiPost(t *testing.T, h http.Handler, p jam.Participant, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = appui.WithPrincipal(jam.WithParticipant(req, p), appui.Principal{User: &p})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPIRead(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	pl, _ := e.Intercom.PlanPersonChat(bob, p.UserID)
	m, _ := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "ping"})
	h := Handler(e.Deps, nil)
	path := "/app/user/api/conversations/" + string(m.Channel) + "/read"
	if rec := apiPost(t, h, p, path, fmt.Sprintf(`{"seq":%d}`, m.Seq)); rec.Code != 204 {
		t.Fatalf("read = %d %s", rec.Code, rec.Body)
	}
	var rr RailResponse
	_ = json.Unmarshal(apiGet(t, h, &p, "/app/user/api/rail").Body.Bytes(), &rr)
	if rr.UnreadTotal != 0 {
		t.Errorf("after read, unreadTotal = %d", rr.UnreadTotal)
	}
	if rec := apiPost(t, h, p, "/app/user/api/conversations/chn_01j9q3zzzzzzzzzzzzzzzzzz/read", `{"seq":1}`); rec.Code != 404 {
		t.Errorf("read of a missing channel = %d", rec.Code)
	}
	if rec := apiPost(t, h, p, "/app/user/api/conversations/legacy:x/read", `{"seq":1}`); rec.Code != 204 {
		t.Errorf("read of History = %d, want 204 (no-op)", rec.Code)
	}
	if rec := apiPost(t, h, p, path, `{"seq":`); rec.Code != 400 {
		t.Errorf("bad JSON = %d", rec.Code)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader("seq=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jam.WithParticipant(req, p))
	if rec.Code != http.StatusUnsupportedMediaType || !strings.Contains(rec.Body.String(), `"code":"unsupported_media_type"`) {
		t.Errorf("form body = %d %s, want 415", rec.Code, rec.Body)
	}
}

func TestAPIMembership(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	h := Handler(e.Deps, nil)
	base := "/app/user/api/conversations/" + engID
	if rec := apiPost(t, h, p, base+"/call-in", `{"who":"user:`+string(bob)+`"}`); rec.Code != 204 || !isIn(e.st, e.room.ID, bob) {
		t.Fatalf("call-in = %d %s", rec.Code, rec.Body)
	}
	if rec := apiPost(t, h, p, base+"/leave", `{}`); rec.Code != 204 || isIn(e.st, e.room.ID, p.UserID) {
		t.Fatalf("leave = %d %s", rec.Code, rec.Body)
	}
	if _, c, _ := getConv(t, e, p, engID, ""); !c.CanJoin || c.CanLeave {
		t.Errorf("after leave: canJoin=%v canLeave=%v", c.CanJoin, c.CanLeave)
	}
	if rec := apiPost(t, h, p, base+"/join", `{}`); rec.Code != 204 || !isIn(e.st, e.room.ID, p.UserID) {
		t.Fatalf("join = %d %s", rec.Code, rec.Body)
	}
	// JoinChannel refuses a missing, an unseen and a chat channel alike
	// (ErrSendDenied): one 403, which says nothing about existence.
	if rec := apiPost(t, h, p, "/app/user/api/conversations/chn_01j9q3zzzzzzzzzzzzzzzzzz/join", `{}`); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
		t.Errorf("join missing = %d %s, want 403 forbidden", rec.Code, rec.Body)
	}
	if rec := apiPost(t, h, p, base+"/call-in", `{"who":"user:nobody"}`); rec.Code == 204 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("call-in nobody = %d %s", rec.Code, rec.Body)
	}
	// A chat's members are fixed.
	pl, _ := e.Intercom.PlanPersonChat(bob, p.UserID)
	m, _ := e.Intercom.Post(pl, intercom.Squawk{From: bob, Body: "hi"})
	if rec := apiPost(t, h, p, "/app/user/api/conversations/"+string(m.Channel)+"/leave", `{}`); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"code":"conflict"`) {
		t.Errorf("leave a chat = %d %s, want 409 conflict", rec.Code, rec.Body)
	}
}

// The conversation carries its asks as cards: open ones in openAsks and on
// their own rows; one for someone else is read-only and says for whom.
func TestAPIConversationAsks(t *testing.T) {
	e, p := fixture()
	carol, err := e.st.CreateUser(jam.User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	open := e.ask(intercom.AskEvent{Question: "Merge PR #12?", Options: yesNoOpts})
	forCarol := e.ask(intercom.AskEvent{Question: "Carol only?", Options: yesNoOpts, Of: []ident.ID{carol.ID}})
	_, c, body := getConv(t, e, p, engID, "")
	if len(c.OpenAsks) != 2 {
		t.Fatalf("openAsks = %d: %s", len(c.OpenAsks), body)
	}
	cards := map[string]*AskCard{}
	for i := range c.Messages {
		if a := c.Messages[i].Ask; a != nil {
			cards[a.ID] = a
			if c.Messages[i].Card != "ask" {
				t.Errorf("ask row card = %q", c.Messages[i].Card)
			}
		}
	}
	if a := cards[open.ID]; a == nil || !a.CanAnswer || a.State != "open" || a.Version != 1 || len(a.Options) != 2 || a.Options[1].Detail != "wait for CI" {
		t.Errorf("open card = %+v", a)
	}
	if a := cards[forCarol.ID]; a == nil || a.CanAnswer || a.For != "carol" {
		t.Errorf("card for carol = %+v", a)
	}
	if !strings.Contains(body, `"options":[{"id":"yes","label":"Ship it"},{"id":"no","label":"Hold","detail":"wait for CI"}]`) {
		t.Errorf("options JSON: %s", body)
	}
}

// A conversation names the session whose home it is (the app's header
// and pane): a ticket session's home is its
// ticket channel, not its own session channel; a room has none.
func TestAPIConversationSession(t *testing.T) {
	e, p, personal, _, _ := sessionEnv(t)
	if _, c, body := getConv(t, e, p, e.homeOf(t, personal), ""); c.Session != personal.ActorID {
		t.Errorf("personal home: session = %q\n%s", c.Session, body)
	}
	if _, c, _ := getConv(t, e, p, engID, ""); c.Session != "" {
		t.Errorf("a room names session %q", c.Session)
	}
	inst, ticket, own := ticketSession(t, e, p)
	if _, c, _ := getConv(t, e, p, string(ticket), ""); c.Session != inst.ActorID {
		t.Errorf("ticket channel: session = %q, want %q", c.Session, inst.ActorID)
	}
	if _, c, _ := getConv(t, e, p, string(own), ""); c.Session != "" {
		t.Errorf("the session's own channel (not its home) names %q", c.Session)
	}
}
