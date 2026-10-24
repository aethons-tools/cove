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

// /app/user creates a board, merges and reads elements, and the live stream says
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
	rec := do("POST", "/app/user/boards", `{"channel":"`+engID+`","title":"Arch"}`)
	var c struct {
		BoardID string `json:"board_id"`
	}
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
	if rec := do("POST", "/app/user/boards/"+c.BoardID+"/elements", `{"elements":[{"id":"r1","type":"rectangle","version":1,"versionNonce":1}]}`); rec.Code != http.StatusOK {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	readUntil(t, sc, "event: board")
	readUntil(t, sc, "data: "+c.BoardID)
	if rec := do("GET", "/app/user/boards/"+c.BoardID+"/elements?since=0", ""); !strings.Contains(rec.Body.String(), `"r1"`) {
		t.Fatalf("read = %s", rec.Body)
	}
	if rec := do("GET", "/app/user/boards/"+c.BoardID+"/thumb.png", ""); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("thumb = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// A board's card shows its title, thumbnail and Open; opening it shows the
// editor wired to the bundle; the header offers New whiteboard — all only when
// this binary carries the editor.
func TestBoardCardAndEditor(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	b, err := e.Deps.Boards.Create(jam.Poster{ID: e.alice.ID}, engID, "Architecture", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	withEditor := func(on bool) func() {
		old := whiteboardBuilt
		whiteboardBuilt = func() bool { return on }
		return func() { whiteboardBuilt = old }
	}
	defer withEditor(true)()
	body := e.page(t, p, "/app/user/?c="+engID)
	for _, want := range []string{`class="board-card"`, "Architecture", `src="/app/user/boards/` + string(b.ID) + `/thumb.png`, `href="/app/user/?c=` + engID + `&board=` + string(b.ID) + `"`, "New whiteboard", "addEventListener('board'"} {
		if !strings.Contains(body, want) {
			t.Errorf("conversation missing %q", want)
		}
	}
	ed := e.page(t, p, "/app/user/?c="+engID+"&board="+string(b.ID))
	for _, want := range []string{`id="board-editor"`, `data-board="` + string(b.ID) + `"`, `type="module"`, "whiteboard/whiteboard.js", "EXCALIDRAW_ASSET_PATH", "Back to chat"} {
		if !strings.Contains(ed, want) {
			t.Errorf("editor missing %q", want)
		}
	}
	if strings.Contains(ed, `id="stream"`) {
		t.Error("the editor replaces the stream")
	}
	// Without the bundle: no New whiteboard, and the card says so.
	defer withEditor(false)()
	off := e.page(t, p, "/app/user/?c="+engID)
	if strings.Contains(off, "New whiteboard") || !strings.Contains(off, "whiteboard not built") {
		t.Error("without the bundle: no New whiteboard; cards say it isn't built")
	}
}

// A board in a channel the viewer can't see doesn't open.
func TestBoardEditorRefusesUnseen(t *testing.T) {
	e, p := fixture()
	e.Deps.Boards = jam.NewBoards(e.lg, e.Intercom)
	body := e.page(t, p, "/app/user/?c="+engID+"&board=brd_01j9q3zzzzzzzzzzzzzzzzzz")
	if strings.Contains(body, `id="board-editor"`) {
		t.Error("an unknown board opened the editor")
	}
}
