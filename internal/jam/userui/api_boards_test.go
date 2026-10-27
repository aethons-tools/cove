package userui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/jam"
)

func TestAPIBoard(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	b, err := e.Deps.Boards.Create(jam.Poster{ID: e.alice.ID}, engID, "Arch", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(e.Deps, nil)
	rec := apiGet(t, h, &p, "/app/user/api/boards/"+string(b.ID))
	if rec.Code != 200 {
		t.Fatalf("board = %d %s", rec.Code, rec.Body)
	}
	checkJSONHeaders(t, rec)
	var info BoardInfo
	_ = json.Unmarshal(rec.Body.Bytes(), &info)
	if info != (BoardInfo{ID: string(b.ID), Title: "Arch", Channel: engID, ReadOnly: false}) {
		t.Errorf("board = %+v", info)
	}
	// Unseen and missing answer alike; so does whiteboards off.
	bob, err := e.st.CreateUser(jam.User{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	outsider := jam.Participant{UserID: bob.ID, Name: "bob", Projects: []string{"proj"}}
	hidden := apiGet(t, h, &outsider, "/app/user/api/boards/"+string(b.ID))
	gone := apiGet(t, h, &p, "/app/user/api/boards/brd_01j9q3zzzzzzzzzzzzzzzzzz")
	if hidden.Code != 404 || hidden.Body.String() != gone.Body.String() || !strings.Contains(gone.Body.String(), "no such board") {
		t.Errorf("hidden %d %s, missing %d %s", hidden.Code, hidden.Body, gone.Code, gone.Body)
	}
	e.Deps.Boards = nil
	if rec := apiGet(t, Handler(e.Deps, nil), &p, "/app/user/api/boards/"+string(b.ID)); rec.Code != 404 {
		t.Errorf("boards off = %d", rec.Code)
	}
}

func TestAPINewBoard(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	h := Handler(e.Deps, nil)
	rec := apiPost(t, h, p, "/app/user/api/boards", `{"channel":"`+engID+`","title":"Plan"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var got NewBoardResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !strings.HasPrefix(got.Board, "brd_") || got.Channel != engID {
		t.Errorf("created %+v", got)
	}
	if b, ok := e.lg.GetBoard(ident.ID(got.Board)); !ok || b.Title != "Plan" {
		t.Errorf("stored %+v %v", b, ok)
	}
	for _, tc := range []struct {
		body string
		code int
		want string
	}{
		{`{"channel":"` + engID + `","title":""}`, 400, "a title is 1–200 bytes"},
		{`{"channel":"chn_01j9q3zzzzzzzzzzzzzzzzzz","title":"x"}`, 404, "no such conversation"},
		{`{"channel":`, 400, `"code":"bad_request"`},
	} {
		if rec := apiPost(t, h, p, "/app/user/api/boards", tc.body); rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s = %d %s, want %d %s", tc.body, rec.Code, rec.Body, tc.code, tc.want)
		}
	}
}

// The API conversation offers New whiteboard where the viewer may start one.
func TestAPIConversationNewBoard(t *testing.T) {
	e, p := fixture()
	if _, c, _ := getConv(t, e, p, engID, ""); c.NewBoard {
		t.Error("New whiteboard with whiteboards off")
	}
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	if _, c, _ := getConv(t, e, p, engID, ""); !c.NewBoard {
		t.Error("no New whiteboard in a room alice may post to")
	}
}
