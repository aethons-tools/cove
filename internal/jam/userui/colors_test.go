package userui

import (
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// A session's color (docs/usage/jam/session-colors.md) reaches its head, its
// rail card, its home conversation and its messages; a person's messages and
// an uncolored session carry "".
func TestAPISessionColors(t *testing.T) {
	e, p, personal, standing, _ := sessionEnv(t)
	if _, err := e.st.PutSessionColor(personal.ActorID, "#aa3f4e"); err != nil {
		t.Fatal(err)
	}
	if hd := decodeAs[SessionHead](t, sessAPI(t, e, p, "GET", personal.ActorID, "", false)); hd.Color != "#aa3f4e" || hd.Card.Color != "#aa3f4e" {
		t.Errorf("head color = %q, card color = %q", hd.Color, hd.Card.Color)
	}
	if hd := decodeAs[SessionHead](t, sessAPI(t, e, p, "GET", standing.ActorID, "", false)); hd.Color != "" {
		t.Errorf("uncolored head color = %q", hd.Color)
	}
	home, err := e.Intercom.HomeChannel(personal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: ident.ID(personal.ActorID), Body: "from the session", At: fixtureAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Intercom.PostTrusted(home, intercom.Squawk{From: p.UserID, Body: "from alice", At: fixtureAt}); err != nil {
		t.Fatal(err)
	}
	_, c, body := getConv(t, e, p, string(home.ID), "")
	if c.Color != "#aa3f4e" {
		t.Errorf("conversation color = %q\n%s", c.Color, body)
	}
	got := map[string]string{}
	for _, m := range c.Messages {
		got[m.Raw] = m.Color
	}
	if got["from the session"] != "#aa3f4e" || got["from alice"] != "" {
		t.Errorf("message colors = %v", got)
	}
	if _, c, _ := getConv(t, e, p, engID, ""); c.Color != "" {
		t.Errorf("a room's color = %q", c.Color)
	}
}
