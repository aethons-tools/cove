package userui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

func TestAPISendMessage(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := e.st.LookupName(ident.User, "bob")
	e.Send = jam.NewParticipantSendHandler(e.st, e.Intercom, nil)
	h := Handler(e.Deps, nil)

	rec := apiPost(t, h, p, "/app/user/api/messages", `{"to":"user:`+string(bob)+`","body":"hi bob"}`)
	if rec.Code != 201 {
		t.Fatalf("send = %d %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var sent MessageSent
	if err := json.Unmarshal(rec.Body.Bytes(), &sent); err != nil || sent.Channel == "" || sent.Seq == 0 {
		t.Fatalf("sent = %+v %v", sent, err)
	}
	// The new conversation is the viewer's and holds the message.
	if _, c, _ := getConv(t, e, p, sent.Channel, ""); len(c.Messages) != 1 || c.Messages[0].Raw != "hi bob" || !c.Messages[0].Mine {
		t.Errorf("new conversation = %+v", c)
	}
	// Into an existing one, as plain text.
	rec = apiPost(t, h, p, "/app/user/api/messages", `{"to":"`+engID+`","body":"*lit*","contentType":"text/plain"}`)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"channel":"`+engID+`"`) {
		t.Fatalf("send to eng = %d %s", rec.Code, rec.Body)
	}
	if _, c, _ := getConv(t, e, p, engID, ""); !c.Messages[len(c.Messages)-1].Plain {
		t.Error("contentType text/plain wasn't kept")
	}
	// Cases as positional pairs: gofmt aligns map keys differently across
	// Go versions (CI's 1.26 vs 1.27), never positional elements.
	for _, tc := range []struct{ body, want string }{
		{`{"to":"user:nobody","body":"x"}`, `"code":"not_found"`},
		{`{"to":"user:alice"}`, `"code":"bad_request"`},
		{`{"to":"user:alice","body":"` + strings.Repeat("x", 17<<10) + `"}`, `"code":"too_large"`},
	} {
		body, want := tc.body, tc.want
		if rec := apiPost(t, h, p, "/app/user/api/messages", body); rec.Code < 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("send %s = %d %s, want %s", body, rec.Code, rec.Body, want)
		}
	}
}

func TestAPISendMessageOff(t *testing.T) {
	e, p := fixture() // Deps.Send unset
	if rec := apiPost(t, Handler(e.Deps, nil), p, "/app/user/api/messages", `{"to":"`+engID+`","body":"x"}`); rec.Code != 503 || !strings.Contains(rec.Body.String(), `"code":"unavailable"`) {
		t.Errorf("send without a sender = %d %s", rec.Code, rec.Body)
	}
}

func TestAPIRecipients(t *testing.T) {
	e, p := fixture()
	if err := jam.AddPerson(e.st, "proj", jam.Human{Name: "bob"}); err != nil {
		t.Fatal(err)
	}
	rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/recipients")
	if rec.Code != 200 {
		t.Fatalf("recipients = %d %s", rec.Code, rec.Body)
	}
	var got RecipientsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	var labels []string
	for _, o := range got.Options {
		labels = append(labels, o.Label+"/"+o.Kind)
	}
	if s := strings.Join(labels, ","); !strings.Contains(s, "bob/person") || !strings.Contains(s, "eng/room") || strings.Contains(s, "alice/") {
		t.Errorf("recipients = %s (want bob and eng, never yourself)", s)
	}
}
