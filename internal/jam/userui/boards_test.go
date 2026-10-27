package userui

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

// The user area creates a board, merges and reads elements, and the live stream says
// `board` with its id when it changes.
func TestMeBoardsRoutesAndPush(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	h := Handler(e.Deps, nil)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, jam.WithParticipant(req, p))
		return rec
	}
	rec := do("POST", "/app/user/api/boards", `{"channel":"`+engID+`","title":"Arch"}`)
	var c NewBoardResponse
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}

	srv := eventsServer(t, h, p)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/app/user/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	time.Sleep(50 * time.Millisecond) // let the stream subscribe
	if rec := do("POST", "/app/user/boards/"+c.Board+"/elements", `{"elements":[{"id":"r1","type":"rectangle","version":1,"versionNonce":1}]}`); rec.Code != http.StatusOK {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	readUntil(t, sc, "event: board")
	readUntil(t, sc, "data: "+c.Board)
	if rec := do("GET", "/app/user/boards/"+c.Board+"/elements?since=0", ""); !strings.Contains(rec.Body.String(), `"r1"`) {
		t.Fatalf("read = %s", rec.Body)
	}
	if rec := do("GET", "/app/user/boards/"+c.Board+"/thumb.png", ""); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("thumb = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}
