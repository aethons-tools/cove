package meui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

const askerID = ident.ID("ses_01j9q3ffffffffffffffffffff")

// ask posts a session's ask into the room (trusted, as the store folds it).
func (e *env) ask(ev intercom.AskEvent) intercom.Squawk {
	ev.Event = intercom.AskEventAsk
	m, err := e.Intercom.PostTrusted(e.room, intercom.Squawk{From: askerID, Body: "asked", ContentType: intercom.ContentPlain, Parts: []intercom.Part{intercom.AskPart(ev)}})
	if err != nil {
		panic(err)
	}
	return m
}

func (e *env) page(t *testing.T, p jam.Participant, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(e.Deps, nil).ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest("GET", path, nil), p))
	if rec.Code != 200 {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	return rec.Body.String()
}

func (e *env) answer(p jam.Participant, id string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/me/asks/"+id+"/answer", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	Handler(e.Deps, nil).ServeHTTP(rec, jam.WithParticipant(req, p))
	return rec
}

var yesNoOpts = []intercom.AskOption{{ID: "yes", Label: "Ship it"}, {ID: "no", Label: "Hold", Detail: "wait for CI"}}

func TestAskCardOpenAndAnswered(t *testing.T) {
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge PR #12?", Options: yesNoOpts})
	body := e.page(t, p, "/me/?c="+url.QueryEscape(engID))
	for _, want := range []string{`id="ask-` + m.ID + `"`, "Merge PR #12?", `hx-post="/me/asks/` + m.ID + `/answer"`, `name="version" value="1"`,
		`value="yes"`, "Ship it", "wait for CI", `value="other"`, "Discuss", `class="asks-panel"`} {
		if !strings.Contains(body, want) {
			t.Errorf("open card missing %q", want)
		}
	}
	rec := e.answer(p, m.ID, url.Values{"version": {"1"}, "choice": {"no"}, "comment": {"after lunch"}})
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Trigger") != "meChanged" {
		t.Fatalf("answer = %d %q %s", rec.Code, rec.Header().Get("HX-Trigger"), rec.Body)
	}
	body = e.page(t, p, "/me/?c="+url.QueryEscape(engID))
	for _, want := range []string{"alice chose", "Hold", "after lunch"} {
		if !strings.Contains(body, want) {
			t.Errorf("answered card missing %q", want)
		}
	}
	if strings.Contains(body, `/me/asks/`+m.ID+`/answer"`) {
		t.Error("an answered card still offers answering")
	}
	if strings.Contains(body, `class="asks-panel"`) {
		t.Error("no open asks: no panel")
	}
	// The answer event renders as a system line, not a bubble.
	if !strings.Contains(body, `class="sysline"`) {
		t.Error("the answer event should render as a system line")
	}
}

func TestAskAnswerRefusals(t *testing.T) {
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge?", Options: yesNoOpts})
	if _, err := e.lg.Append(intercom.Squawk{Channel: e.room.ID, From: askerID, Body: "edited", Parts: []intercom.Part{intercom.AskPart(intercom.AskEvent{Event: intercom.AskEventAmend, AskID: m.ID, Question: "Merge now?"})}}, nil); err != nil {
		t.Fatal(err)
	}
	if rec := e.answer(p, m.ID, url.Values{"version": {"1"}, "choice": {"yes"}}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "this ask changed") {
		t.Fatalf("stale = %d %s", rec.Code, rec.Body)
	}
	if rec := e.answer(p, m.ID, url.Values{"version": {"2"}, "choice": {"yes"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("answer = %d %s", rec.Code, rec.Body)
	}
	if rec := e.answer(p, m.ID, url.Values{"version": {"2"}, "choice": {"no"}}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "already answered by alice") {
		t.Fatalf("second = %d %s", rec.Code, rec.Body)
	}
	if rec := e.answer(p, "nope", url.Values{"version": {"1"}, "choice": {"yes"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("no such ask = %d", rec.Code)
	}
	// Someone who isn't in the channel can't answer.
	bob, err := e.st.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	m2 := e.ask(intercom.AskEvent{Question: "Again?", Options: yesNoOpts})
	pb := jam.Participant{UserID: bob.ID, Name: "bob", Projects: []string{"proj"}}
	if rec := e.answer(pb, m2.ID, url.Values{"version": {"1"}, "choice": {"yes"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-member = %d %s", rec.Code, rec.Body)
	}
}

// An ask for someone else is read-only to the viewer and doesn't make the
// channel need them; an ask they may answer does.
func TestAskForOthersAndRail(t *testing.T) {
	e, p := fixture()
	carol, err := e.st.CreateUser(jam.User{Name: "carol"})
	if err != nil {
		t.Fatal(err)
	}
	e.ask(intercom.AskEvent{Question: "Carol only?", Options: yesNoOpts, Of: []ident.ID{carol.ID}})
	body := e.page(t, p, "/me/?c="+url.QueryEscape(engID))
	if !strings.Contains(body, "for carol") || strings.Contains(body, `name="choice"`) {
		t.Error("an ask for carol should be read-only, marked for carol")
	}
	if g := Rail(p, e.Deps, ""); g[0].Bucket == jam.BucketNeedsYou {
		t.Fatalf("an ask for carol put the room in Needs you: %+v", g)
	}
	e.ask(intercom.AskEvent{Question: "Anyone?", Options: yesNoOpts})
	g := Rail(p, e.Deps, "")
	if g[0].Bucket != jam.BucketNeedsYou || g[0].Rows[0].OpenAsks != 2 {
		t.Fatalf("rail = %+v, want the room in Needs you with 2 open asks", g)
	}
	if body := e.page(t, p, "/me/rail"); !strings.Contains(body, `class="badge asks"`) {
		t.Error("rail row should show the open-asks count")
	}
}

// A reply linked to an ask shows a chip pointing at it.
func TestAskReplyChip(t *testing.T) {
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge?", Options: yesNoOpts})
	if _, err := e.Intercom.PostTrusted(e.room, intercom.Squawk{From: e.alice.ID, Body: "which PR?", ReplyTo: m.ID}); err != nil {
		t.Fatal(err)
	}
	body := e.page(t, p, "/me/?c="+url.QueryEscape(engID))
	if !strings.Contains(body, `href="#ask-`+m.ID+`"`) || !strings.Contains(body, "re: Merge?") {
		t.Error("a reply to an ask should carry a re: chip linking the card")
	}
}
