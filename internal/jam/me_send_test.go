package jam

import (
	"net/http"
	"net/http/httptest"
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
	ticket, _ := f.ic.DefaultChannel(f.ticket)
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

// A new message to a person or a session starts (or reuses) a chat with them.
func TestParticipantSendNewConversations(t *testing.T) {
	f := newICFixture(t)
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	for to, who := range map[string]ident.ID{
		"user:" + string(f.alice.ID):    f.alice.ID,
		"user:alice":                    f.alice.ID,
		"session:" + f.personal.ActorID: ident.ID(f.personal.ActorID),
	} {
		before := len(f.log.InboxSince(who, 0, 0))
		if w := meSend(t, h, f.bob, `{"to":"`+to+`","body":"hello"}`); w.Code != http.StatusNoContent {
			t.Fatalf("to %s = %d %s", to, w.Code, w.Body)
		}
		if got := f.log.InboxSince(who, 0, 0); len(got) != before+1 || got[len(got)-1].From != f.bob.ID {
			t.Fatalf("to %s: %s's inbox = %+v", to, who, got)
		}
	}
	if chats := f.store.ListChannels(f.project.ID, SourceChat); len(chats) != 2 {
		t.Fatalf("chats = %+v, want bob+alice (reused by id and name) and bob+session", chats)
	}
}

func TestParticipantSendErrors(t *testing.T) {
	f := newICFixture(t)
	ownerChat, _ := f.ic.DefaultChannel(f.personal)
	h := NewParticipantSendHandler(f.store, f.ic, nil)
	for body, want := range map[string]int{
		`{"to":"user:alice","body":""}`: http.StatusBadRequest,
		`{"to":"","body":"x"}`:          http.StatusBadRequest,
		`not json`:                      http.StatusBadRequest,
		`{"to":"user:alice","body":"x","content_type":"text/html"}`: http.StatusBadRequest,
		`{"to":"user:carol","body":"x"}`:                            http.StatusNotFound, // not a member
		`{"to":"user:nobody","body":"x"}`:                           http.StatusNotFound,
		`{"to":"session:nope","body":"x"}`:                          http.StatusNotFound,
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
