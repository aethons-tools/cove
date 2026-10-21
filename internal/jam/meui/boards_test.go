package meui

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aethons-tools/cove/internal/jam"
)

// /me creates a board, merges and reads elements, and the live stream says
// `board` with its id when it changes.
func TestMeBoardsRoutesAndPush(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	h := Handler(e.Deps, nil)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, jam.WithParticipant(httptest.NewRequest(method, path, strings.NewReader(body)), p))
		return rec
	}
	rec := do("POST", "/me/boards", `{"channel":"`+engID+`","title":"Arch"}`)
	var c struct {
		BoardID string `json:"board_id"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}

	srv := eventsServer(t, h, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/me/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	time.Sleep(50 * time.Millisecond) // let the stream subscribe
	if rec := do("POST", "/me/boards/"+c.BoardID+"/elements", `{"elements":[{"id":"r1","type":"rectangle","version":1,"versionNonce":1}]}`); rec.Code != http.StatusOK {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	readUntil(t, sc, "event: board")
	readUntil(t, sc, "data: "+c.BoardID)
	if rec := do("GET", "/me/boards/"+c.BoardID+"/elements?since=0", ""); !strings.Contains(rec.Body.String(), `"r1"`) {
		t.Fatalf("read = %s", rec.Body)
	}
}
