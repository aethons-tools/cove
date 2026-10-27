package userui

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

func answerJSON(t *testing.T, e *env, p jam.Participant, id, body string) (int, string) {
	t.Helper()
	rec := apiPost(t, Handler(e.Deps, nil), p, "/app/user/api/asks/"+id+"/answer", body)
	return rec.Code, rec.Body.String()
}

func TestAPIAnswerAsk(t *testing.T) {
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge?", Options: yesNoOpts})
	if code, body := answerJSON(t, e, p, m.ID, `{"version":1,"choice":"no","comment":"after lunch"}`); code != http.StatusNoContent {
		t.Fatalf("answer = %d %s", code, body)
	}
	_, c, _ := getConv(t, e, p, engID, "")
	var card *AskCard
	for i := range c.Messages {
		if c.Messages[i].Ask != nil {
			card = c.Messages[i].Ask
		}
	}
	if card == nil || card.State != "answered" || card.AnsweredBy != "alice" || card.Choice != "Hold" || card.Comment != "after lunch" || len(c.OpenAsks) != 0 {
		t.Fatalf("after answering: card %+v, openAsks %d", card, len(c.OpenAsks))
	}
	if code, body := answerJSON(t, e, p, m.ID, `{"version":1,"choice":"yes"}`); code != 409 || !strings.Contains(body, `"code":"ask_closed"`) || !strings.Contains(body, "already answered by alice: Hold") {
		t.Errorf("second answer = %d %s", code, body)
	}
}

func TestAPIAnswerAskRefusals(t *testing.T) {
	e, p := fixture()
	m := e.ask(intercom.AskEvent{Question: "Merge?", Options: yesNoOpts})
	if _, err := e.lg.Append(intercom.Squawk{Channel: e.room.ID, From: askerID, Body: "edited", Parts: []intercom.Part{intercom.AskPart(intercom.AskEvent{Event: intercom.AskEventAmend, AskID: m.ID, Question: "Merge now?"})}}, nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, body string
		code     int
		want     string
	}{
		{m.ID, `{"version":1,"choice":"yes"}`, 409, `"code":"ask_changed"`},
		{m.ID, `{"version":2,"choice":"other"}`, 400, `"code":"bad_request"`},
		{"nope", `{"version":1,"choice":"yes"}`, 404, `"code":"not_found"`},
		{m.ID, `{"version":`, 400, `"code":"bad_request"`},
	} {
		if code, body := answerJSON(t, e, p, tc.id, tc.body); code != tc.code || !strings.Contains(body, tc.want) {
			t.Errorf("answer %s %s = %d %s, want %d %s", tc.id, tc.body, code, body, tc.code, tc.want)
		}
	}
	bob, err := e.st.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	pb := jam.Participant{UserID: bob.ID, Name: "bob", Projects: []string{"proj"}}
	if code, body := answerJSON(t, e, pb, m.ID, `{"version":2,"choice":"yes"}`); code != 403 || !strings.Contains(body, `"code":"forbidden"`) {
		t.Errorf("non-member = %d %s", code, body)
	}
}

// Every refusal has a status, an API code and a message.
func TestAnswerOutcomeRefusals(t *testing.T) {
	e, _ := fixture()
	for _, err := range []error{intercom.ErrAskChanged, intercom.ErrAskNotFound, intercom.ErrAskForbidden, jam.ErrSendDenied, fmt.Errorf("%w: other needs a comment", intercom.ErrInvalid)} {
		status, code, msg := answerOutcome(e.Deps, "x", err)
		if status < 400 || code == "" || msg == "" {
			t.Errorf("answerOutcome(%v) = %d %q %q", err, status, code, msg)
		}
	}
}
