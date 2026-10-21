package jam

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aethons-tools/cove/internal/intercom"
)

func TestSquawksAskLifecycle(t *testing.T) {
	f := newSqFixture(t)
	home := f.mustPlan(f.standing, "").Channel
	if err := f.ic.JoinChannel(f.alice.ID, home.ID); err != nil {
		t.Fatal(err)
	}
	rec := f.do(http.MethodPost, "/squawks/ask", f.standing, `{"question":"Merge PR #12?","options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No","detail":"hold"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("ask = %d %s", rec.Code, rec.Body)
	}
	var asked struct {
		AskID   string `json:"ask_id"`
		Version int    `json:"version"`
		Channel Party  `json:"channel"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &asked); err != nil || asked.AskID == "" || asked.Version != 1 || asked.Channel.ID != home.ID {
		t.Fatalf("ask response %s (%v)", rec.Body, err)
	}
	rec = f.do(http.MethodPost, "/squawks/ask/amend", f.standing, `{"ask_id":"`+asked.AskID+`","question":"Merge PR #12 now?"}`)
	if rec.Code != http.StatusOK || rec.Body.String() == "" {
		t.Fatalf("amend = %d %s", rec.Code, rec.Body)
	}
	if _, err := f.ic.AnswerAsk(f.alice.ID, asked.AskID, 2, "yes", "go"); err != nil {
		t.Fatal(err)
	}
	// The answer reaches the session's read with its part.
	r := f.read(f.standing, "")
	last := r.Squawks[len(r.Squawks)-1]
	if len(last.Parts) != 1 || last.Parts[0].Type != intercom.AskPartType {
		t.Fatalf("read entry %+v", last)
	}
	var ev intercom.AskEvent
	if err := json.Unmarshal(last.Parts[0].Data, &ev); err != nil || ev.Event != intercom.AskEventAnswer || ev.Choice != "yes" || ev.AskID != asked.AskID {
		t.Fatalf("answer part %+v (%v)", ev, err)
	}
	rec = f.do(http.MethodGet, "/squawks/asks?state=answered", f.standing, "")
	var list struct {
		Asks []struct {
			AskID  string              `json:"ask_id"`
			State  string              `json:"state"`
			Answer *intercom.AskAnswer `json:"answer"`
		} `json:"asks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Asks) != 1 || list.Asks[0].Answer == nil || list.Asks[0].Answer.Choice != "yes" {
		t.Fatalf("asks = %d %s (%v)", rec.Code, rec.Body, err)
	}
	if rec := f.do(http.MethodPost, "/squawks/ask/withdraw", f.standing, `{"ask_id":"`+asked.AskID+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("withdraw an answered ask = %d %s", rec.Code, rec.Body)
	}
}

func TestSquawksAskRefusals(t *testing.T) {
	f := newSqFixture(t)
	opts := `"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]`
	rec := f.do(http.MethodPost, "/squawks/ask", f.standing, `{"question":"q",`+opts+`}`)
	var asked struct {
		AskID string `json:"ask_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &asked)
	for _, c := range []struct {
		path, body string
		inst       Instance
		want       int
	}{
		{"/squawks/ask", `{"question":"q","options":[{"id":"yes","label":"Yes"}]}`, f.standing, http.StatusBadRequest},
		{"/squawks/ask", `not json`, f.standing, http.StatusBadRequest},
		{"/squawks/ask", `{"question":"q",` + opts + `,"to":"user:bob"}`, f.standing, http.StatusForbidden},
		{"/squawks/ask", `{"question":"q",` + opts + `,"of":["user:nobody"]}`, f.standing, http.StatusNotFound},
		{"/squawks/ask/amend", `{"ask_id":"` + asked.AskID + `","question":"x"}`, f.ticket, http.StatusForbidden},
		{"/squawks/ask/amend", `{"ask_id":"nope","question":"x"}`, f.standing, http.StatusNotFound},
		{"/squawks/ask/withdraw", `{"ask_id":"nope"}`, f.standing, http.StatusNotFound},
	} {
		if rec := f.do(http.MethodPost, c.path, c.inst, c.body); rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d (%s)", c.path, c.body, rec.Code, c.want, rec.Body)
		}
	}
	if rec := f.do(http.MethodGet, "/squawks/ask", f.standing, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /squawks/ask = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/squawks/asks", f.standing, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /squawks/asks = %d", rec.Code)
	}
}
