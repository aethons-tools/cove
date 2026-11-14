package jam

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// meSend posts body as participant u to /me/send.
func meSend(t *testing.T, h http.Handler, u User, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/me/send", strings.NewReader(body))
	r = WithParticipant(r, Participant{UserID: u.ID, Name: u.Name, Projects: []string{"acme"}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// A reply into a ticket's conversation reaches the session working it (so it
// wakes it), as the person — who joins the conversation.
func TestParticipantSendIntoAChannel(t *testing.T) {
	f := newICFixture(t)
	if err := f.ic.SetUp(f.ticket); err != nil {
		t.Fatal(err)
	}
	ticket, _ := f.ic.HomeChannel(f.ticket)
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	if w := meSend(t, h, f.bob, `{"to":"`+string(ticket.ID)+`","body":"go ahead"}`); w.Code != http.StatusNoContent {
		t.Fatalf("send = %d %s", w.Code, w.Body)
	}
	got := f.log.InboxSince(ident.ID(f.ticket.ActorID), 0, 0)
	if len(got) != 1 || got[0].From != f.bob.ID || got[0].Body != "go ahead" || got[0].Channel != ticket.ID {
		t.Fatalf("session inbox = %+v", got)
	}
	if !isMember(f.store, ticket.ID, f.bob.ID) {
		t.Fatal("posting joins bob to the ticket's conversation")
	}
}

// A new message to a person starts (or reuses) a dm with them; to several
// people, a group; to a session, a dm with it.
func TestParticipantSendNewConversations(t *testing.T) {
	f := newICFixture(t)
	dave, err := f.store.CreateUser(User{Name: "dave"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.AddMember(f.project.ID, dave.ID); err != nil {
		t.Fatal(err)
	}
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	p := Participant{UserID: f.bob.ID, Name: f.bob.Name, Projects: []string{"acme"}}
	for _, c := range []struct {
		to      string
		kind    SourceKind
		members []ident.ID
	}{
		{"user:" + string(f.alice.ID), SourceDM, ids(f.alice, f.bob)},
		{"user:alice", SourceDM, ids(f.alice, f.bob)},
		{"chat:user:alice,user:" + string(dave.ID), SourceGroup, ids(f.alice, f.bob, dave)},
		{"chat:user:alice", SourceDM, ids(f.alice, f.bob)},
		{"session:" + f.standing.ActorID, SourceDM, ids(f.bob, f.standing)},
	} {
		m, err := h.Send(p, SendRequest{To: c.to, Body: "hello"})
		if err != nil {
			t.Fatalf("to %s: %v", c.to, err)
		}
		ch, _ := f.store.GetChannel(m.Channel)
		var got []ident.ID
		for _, mb := range f.store.ChannelMembers(ch.ID) {
			got = append(got, mb.ParticipantID)
		}
		if ch.Kind != c.kind || !slices.Equal(got, c.members) {
			t.Fatalf("to %s: a %s of %v, want a %s of %v", c.to, ch.Kind, got, c.kind, c.members)
		}
		for _, who := range c.members {
			if who == f.bob.ID {
				continue
			}
			if in := f.log.InboxSince(who, 0, 0); len(in) == 0 || in[len(in)-1].ID != m.ID {
				t.Fatalf("to %s: %s did not get it", c.to, who)
			}
		}
	}
	if chats := f.store.ListChannels(f.project.ID, SourceDM); len(chats) != 2 {
		t.Fatalf("dms = %+v, want bob+alice (reused by id, name, and chat:) and bob+the session", chats)
	}
}

// session:<id|label> is the dm between the person and the session
// (conversations.adoc#targets). Until the turn-end rule lands, the session's
// turn-end text still goes to its home channel, so the person also joins
// that, as before, to get the reply. Someone else's personal session isn't
// theirs to reach.
func TestParticipantSendToASession(t *testing.T) {
	f := newICFixture(t)
	f.standing.Name = "spider"
	if err := f.store.PutInstance(f.standing); err != nil {
		t.Fatal(err)
	}
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	p := Participant{UserID: f.bob.ID, Name: f.bob.Name, Projects: []string{"acme"}}
	var chans []ident.ID
	for _, to := range []string{"session:" + f.standing.ActorID, "session:spider"} {
		m, err := h.Send(p, SendRequest{To: to, Body: "hello"})
		if err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
		chans = append(chans, m.Channel)
	}
	dm, ok := f.store.GetChannel(chans[0])
	if !ok || dm.Kind != SourceDM || chans[1] != chans[0] {
		t.Fatalf("sent into %v (%+v), want one dm", chans, dm)
	}
	if !isMember(f.store, dm.ID, f.bob.ID) || !isMember(f.store, dm.ID, ident.ID(f.standing.ActorID)) {
		t.Fatalf("dm members = %+v", f.store.ChannelMembers(dm.ID))
	}
	if got := f.log.InboxSince(ident.ID(f.standing.ActorID), 0, 0); len(got) != 2 || got[0].Channel != dm.ID {
		t.Fatalf("session inbox = %+v", got)
	}
	home, _ := f.ic.HomeChannelIfExists(f.standing)
	if !isMember(f.store, home.ID, f.bob.ID) {
		t.Fatal("bob joins the session's home channel too, where its turn-end reply goes")
	}
	if _, err := h.Send(p, SendRequest{To: "session:" + f.personal.ActorID, Body: "hi"}); err == nil {
		t.Fatal("bob reached alice's personal session")
	}
	alice := Participant{UserID: f.alice.ID, Name: f.alice.Name, Projects: []string{"acme"}}
	if m, err := h.Send(alice, SendRequest{To: "session:" + f.personal.ActorID, Body: "hi"}); err != nil {
		t.Fatalf("alice to her personal session: %v", err)
	} else if ch, _ := f.store.GetChannel(m.Channel); ch.Kind != SourceDM {
		t.Fatalf("alice's message went to %+v", ch)
	}
}

func TestParticipantSendErrors(t *testing.T) {
	f := newICFixture(t)
	ownerChat, _ := f.ic.HomeChannel(f.personal)
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	for body, want := range map[string]int{
		`{"to":"user:alice","body":""}`: http.StatusBadRequest,
		`{"to":"","body":"x"}`:          http.StatusBadRequest,
		`not json`:                      http.StatusBadRequest,
		`{"to":"user:alice","body":"x","content_type":"text/html"}`: http.StatusBadRequest,
		`{"to":"user:carol","body":"x"}`:                            http.StatusNotFound, // not a member
		`{"to":"user:nobody","body":"x"}`:                           http.StatusNotFound,
		`{"to":"session:nope","body":"x"}`:                          http.StatusForbidden, // never tells whether it exists
		`{"to":"session:` + f.personal.ActorID + `","body":"x"}`:    http.StatusForbidden, // alice's personal session
		`{"to":"chat:user:alice,user:carol","body":"x"}`:            http.StatusNotFound,  // carol is not a member
		`{"to":"chat:session:spider","body":"x"}`:                   http.StatusNotFound,  // people only
		`{"to":"chat:","body":"x"}`:                                 http.StatusNotFound,
		`{"to":"chn_01j9q3zzzzzzzzzzzzzzzzzzzz","body":"x"}`:        http.StatusForbidden, // never tells whether it exists
		`{"to":"` + string(ownerChat.ID) + `","body":"x"}`:          http.StatusForbidden, // alice's chat, not bob's
		`{"to":"pigeon:alice","body":"x"}`:                          http.StatusNotFound,
	} {
		if w := meSend(t, h, f.bob, body); w.Code != want {
			t.Errorf("send %.50q = %d, want %d", body, w.Code, want)
		}
	}
	r := httptest.NewRequest("POST", "/me/send", strings.NewReader(`{"to":"user:alice","body":"x"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no participant = %d", w.Code)
	}
	if w := meSend(t, NewParticipantSendHandler(f.store, nil, nil), f.bob, `{"to":"user:alice","body":"x"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d", w.Code)
	}
}

func TestParticipantSendContentType(t *testing.T) {
	f := newICFixture(t)
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	if w := meSend(t, h, f.bob, `{"to":"user:alice","body":"a_b","content_type":"text/plain"}`); w.Code != http.StatusNoContent {
		t.Fatalf("send = %d", w.Code)
	}
	if got := f.log.InboxSince(f.alice.ID, 0, 0); len(got) != 1 || got[0].ContentType != intercom.ContentPlain {
		t.Fatalf("inbox = %+v", got)
	}
}

// A /me reply may be linked to an ask in the same channel (Discuss), never
// to one elsewhere.
func TestParticipantSendReplyToAsk(t *testing.T) {
	f := newICFixture(t)
	home := f.mustPlan(f.standing, "").Channel
	if err := f.ic.JoinChannel(f.bob.ID, home.ID); err != nil {
		t.Fatal(err)
	}
	a, _, err := f.ic.Ask(f.poster(f.standing), "", AskInput{Question: "Merge?", Options: askYesNo}, f.now)
	if err != nil {
		t.Fatal(err)
	}
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	if w := meSend(t, h, f.bob, `{"to":"`+string(home.ID)+`","body":"which PR?","reply_to":"`+a.ID+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("send = %d %s", w.Code, w.Body)
	}
	got := f.log.InboxSince(ident.ID(f.standing.ActorID), 0, 0)
	if last := got[len(got)-1]; last.ReplyTo != a.ID {
		t.Fatalf("reply_to = %q", last.ReplyTo)
	}
	if w := meSend(t, h, f.bob, `{"to":"`+string(f.room.ID)+`","body":"x","reply_to":"`+a.ID+`"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("reply_to an ask in another channel = %d", w.Code)
	}
}

// Send is the handler's logic without HTTP: the posted squawk (its channel
// and seq) or a *SendError carrying the old route's status and text.
func TestParticipantSendDirect(t *testing.T) {
	f := newICFixture(t)
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	p := Participant{UserID: f.bob.ID, Name: f.bob.Name, Projects: []string{"acme"}}
	m, err := h.Send(p, SendRequest{To: "user:" + string(f.alice.ID), Body: "hello"})
	if err != nil || m.Channel == "" || m.Seq == 0 || m.From != f.bob.ID || m.Body != "hello" {
		t.Fatalf("Send = %+v, %v", m, err)
	}
	for _, tc := range []struct {
		req    SendRequest
		status int
		code   string
	}{
		{SendRequest{To: "user:alice"}, 400, "bad_request"},
		{SendRequest{Body: "x"}, 400, "bad_request"},
		{SendRequest{To: "user:alice", Body: "x", ContentType: "text/html"}, 400, "bad_request"},
		{SendRequest{To: "user:nobody", Body: "x"}, 404, "not_found"},
		{SendRequest{To: "user:alice", Body: "x", Attachments: []string{"blb_x"}}, 400, "bad_request"}, // attachments off
	} {
		_, err := h.Send(p, tc.req)
		var se *SendError
		if !errors.As(err, &se) || se.Status != tc.status || se.Code != tc.code || se.Msg == "" {
			t.Errorf("Send(%+v) = %v, want %d %s", tc.req, err, tc.status, tc.code)
		}
	}
	if _, err := NewParticipantSendHandler(f.store, nil, nil).Send(p, SendRequest{To: "user:alice", Body: "x"}); !isSendStatus(err, 503) {
		t.Errorf("no intercom: %v, want 503", err)
	}
}

func isSendStatus(err error, status int) bool {
	var se *SendError
	return errors.As(err, &se) && se.Status == status
}
