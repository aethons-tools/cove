package jam

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

func rect(id string, version int64) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"id": id, "type": "rectangle", "version": version, "versionNonce": 7, "x": 0, "y": 0, "width": 10, "height": 10})
	return b
}

func TestBoardsCreateReadWrite(t *testing.T) {
	f := newICFixture(t)
	bs := NewBoards(f.log, f.ic)
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	b, err := bs.Create(Poster{ID: f.alice.ID}, string(f.room.ID), "Architecture", f.now)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID.Kind() != ident.Board || b.Channel != f.room.ID || b.CreatedBy != f.alice.ID {
		t.Fatalf("board = %+v", b)
	}
	// The card is a post in the channel with the board part.
	msgs := f.log.ChannelSince(f.room.ID, 0, 0)
	last := msgs[len(msgs)-1]
	if len(last.Parts) != 1 || last.Parts[0].Type != intercom.BoardPartType || last.Body != "started a whiteboard: Architecture" {
		t.Fatalf("card = %+v", last)
	}
	sub, cancel := bs.Subscribe()
	defer cancel()
	seq, lost, err := bs.Merge(f.alice.ID, b.ID, []json.RawMessage{rect("r1", 1)})
	if err != nil || seq != 1 || len(lost) != 0 {
		t.Fatalf("merge = %d %v %v", seq, lost, err)
	}
	select {
	case id := <-sub:
		if id != b.ID {
			t.Fatalf("hub sent %s", id)
		}
	default:
		t.Fatal("a merge publishes the board")
	}
	// bob is in the project and can see the room: he reads and, posting
	// being open in a room, writes.
	if els, cur, err := bs.Elements(f.bob.ID, b.ID, 0); err != nil || cur != 1 || len(els) != 1 {
		t.Fatalf("bob reads = %d %d %v", len(els), cur, err)
	}
	// carol isn't in the project: 404 alike.
	if _, _, err := bs.Elements(f.carol.ID, b.ID, 0); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("carol reads: %v", err)
	}
	// A session not in the room can't read; once a member it can.
	ses := ident.ID(f.standing.ActorID)
	if _, _, err := bs.Elements(ses, b.ID, 0); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("a non-member session reads: %v", err)
	}
	if err := f.store.JoinChannel(f.room.ID, ses, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bs.Merge(ses, b.ID, []json.RawMessage{rect("r2", 1)}); err != nil {
		t.Fatalf("a member session writes: %v", err)
	}
	if l := bs.ForChannelsOf(ses); len(l) != 1 || l[0].ID != b.ID {
		t.Fatalf("ForChannelsOf = %+v", l)
	}
	// Archived: read-only.
	if err := RemoveRoom(f.store, "acme", "eng"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bs.Merge(f.alice.ID, b.ID, []json.RawMessage{rect("r1", 2)}); !errors.Is(err, ErrSendDenied) {
		t.Fatalf("write to an archived channel's board: %v", err)
	}
	if _, cur, err := bs.Elements(f.alice.ID, b.ID, 0); err != nil || cur != 2 {
		t.Fatalf("archived still reads = %d %v", cur, err)
	}
	if _, _, err := bs.Merge(f.alice.ID, b.ID, []json.RawMessage{json.RawMessage(`nope`)}); !errors.Is(err, ErrSendDenied) && !errors.Is(err, intercom.ErrInvalid) {
		t.Fatalf("bad doc: %v", err)
	}
}

func TestBoardsBadDocRefused(t *testing.T) {
	f := newICFixture(t)
	bs := NewBoards(f.log, f.ic)
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	b, _ := bs.Create(Poster{ID: f.alice.ID}, string(f.room.ID), "x", f.now)
	if _, _, err := bs.Merge(f.alice.ID, b.ID, []json.RawMessage{rect("ok", 1), json.RawMessage(`nope`)}); !errors.Is(err, intercom.ErrInvalid) {
		t.Fatalf("bad doc: %v", err)
	}
	if els, _, _ := bs.Elements(f.alice.ID, b.ID, 0); len(els) != 0 {
		t.Fatal("a batch with a bad doc stored something")
	}
}

// A session that creates a board in a room it posts to (but isn't a member
// of) can still read and edit its board.
func TestBoardsCreatorKeepsAccess(t *testing.T) {
	f := newICFixture(t, "channel:*")
	bs := NewBoards(f.log, f.ic)
	b, err := bs.Create(f.poster(f.standing), "channel:eng", "Plan", f.now)
	if err != nil {
		t.Fatal(err)
	}
	ses := ident.ID(f.standing.ActorID)
	if _, _, err := bs.Merge(ses, b.ID, []json.RawMessage{rect("a", 1)}); err != nil {
		t.Fatalf("creator writes: %v", err)
	}
	if l := bs.ForChannelsOf(ses); len(l) != 1 {
		t.Fatalf("creator lists = %+v", l)
	}
}

// A merge where nothing was accepted publishes nothing.
func TestBoardsNoOpMergeIsQuiet(t *testing.T) {
	f := newICFixture(t)
	bs := NewBoards(f.log, f.ic)
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	b, _ := bs.Create(Poster{ID: f.alice.ID}, string(f.room.ID), "x", f.now)
	if _, _, err := bs.Merge(f.alice.ID, b.ID, []json.RawMessage{rect("a", 2)}); err != nil {
		t.Fatal(err)
	}
	sub, cancel := bs.Subscribe()
	defer cancel()
	if _, _, err := bs.Merge(f.alice.ID, b.ID, []json.RawMessage{rect("a", 1)}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub:
		t.Fatal("a merge that changed nothing published")
	default:
	}
}

func TestBoardsSessionCreatesAtHome(t *testing.T) {
	f := newICFixture(t)
	bs := NewBoards(f.log, f.ic)
	b, err := bs.Create(f.poster(f.standing), "", "Plan", f.now)
	if err != nil {
		t.Fatal(err)
	}
	if home, _ := f.ic.HomeChannel(f.standing); b.Channel != home.ID {
		t.Fatalf("board in %s, want the home channel", b.Channel)
	}
}

func TestBoardsHTTP(t *testing.T) {
	f := newICFixture(t)
	bs := NewBoards(f.log, f.ic)
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body string, viewer Poster, id ident.ID) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		if id == "" {
			bs.HandleCreate(rec, req, viewer, f.now)
		} else {
			bs.HandleElements(rec, req, viewer.ID, id)
		}
		return rec
	}
	rec := do("POST", "/boards", `{"channel":"`+string(f.room.ID)+`","title":"Arch"}`, Poster{ID: f.alice.ID}, "")
	var created struct {
		BoardID ident.ID `json:"board_id"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &created) != nil || created.BoardID.Kind() != ident.Board {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	id := created.BoardID
	rec = do("POST", "/x", `{"elements":[`+string(rect("r1", 2))+`]}`, Poster{ID: f.alice.ID}, id)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"seq":1`) {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	rec = do("POST", "/x", `{"elements":[`+string(rect("r1", 1))+`]}`, Poster{ID: f.alice.ID}, id)
	if !strings.Contains(rec.Body.String(), `"lost":[{`) {
		t.Fatalf("losing merge = %s", rec.Body)
	}
	rec = do("GET", "/x?since=0", "", Poster{ID: f.bob.ID}, id)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"r1"`) {
		t.Fatalf("read = %d %s", rec.Code, rec.Body)
	}
	if rec := do("GET", "/x", "", Poster{ID: f.carol.ID}, id); rec.Code != http.StatusNotFound {
		t.Fatalf("carol = %d", rec.Code)
	}
	if rec := do("GET", "/x", "", Poster{ID: f.alice.ID}, ident.New(ident.Board)); rec.Code != http.StatusNotFound {
		t.Fatalf("missing = %d", rec.Code)
	}
	if rec := do("POST", "/x", `{"elements":["nope"]}`, Poster{ID: f.alice.ID}, id); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad doc = %d", rec.Code)
	}
	if rec := do("POST", "/boards", `{"channel":"`+string(f.room.ID)+`","title":""}`, Poster{ID: f.alice.ID}, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no title = %d", rec.Code)
	}
}

func TestBoardsAgentRoutes(t *testing.T) {
	f := newSqFixture(t)
	f.h.SetBoards(NewBoards(f.log, f.ic))
	rec := f.do(http.MethodPost, "/squawks/boards", f.standing, `{"title":"Arch"}`)
	var c struct {
		BoardID string `json:"board_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	rec = f.do(http.MethodPost, "/squawks/boards/"+c.BoardID+"/apply", f.standing,
		`{"ops":[{"op":"add","type":"rectangle","id":"api","x":0,"y":0,"text":"API"},{"op":"add","type":"rectangle","id":"db","x":300,"y":0,"text":"DB"},{"op":"add","type":"arrow","from":"api","to":"db"}]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"added":["api","db",`) {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodPost, "/squawks/boards/"+c.BoardID+"/apply", f.standing, `{"ops":[{"op":"add","type":"arrow","from":"api","to":"nope"}]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "nope") {
		t.Fatalf("bad op = %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, "/squawks/boards/"+c.BoardID+"/read", f.standing, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"from":"api","to":"db"`) || !strings.Contains(rec.Body.String(), `"text":"API"`) {
		t.Fatalf("read = %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodGet, "/squawks/boards/"+c.BoardID+"/snapshot.png", f.standing, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() < 100 {
		t.Fatalf("snapshot = %d %q %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	if rec := f.do(http.MethodGet, "/squawks/boards/"+c.BoardID+"/snapshot.png", f.ticket, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("outsider snapshot = %d", rec.Code)
	}
}

// Agent applies and human merges on one board serialize: no apply reports a
// half-applied batch.
func TestBoardsApplySerializedWithMerges(t *testing.T) {
	f := newICFixture(t)
	bs := NewBoards(f.log, f.ic)
	if err := f.store.JoinChannel(f.room.ID, f.alice.ID, 0); err != nil {
		t.Fatal(err)
	}
	b, _ := bs.Create(Poster{ID: f.alice.ID}, string(f.room.ID), "x", f.now)
	if _, _, err := bs.Apply(f.alice.ID, b.ID, []BoardOp{{Op: "add", Type: "rectangle", ID: "s", X: fptr(0), Y: fptr(0)}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, err := bs.Apply(f.alice.ID, b.ID, []BoardOp{{Op: "update", ID: "s", X: fptr(float64(i))}})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			els, _, _ := bs.Elements(f.alice.ID, b.ID, 0)
			var m map[string]any
			_ = json.Unmarshal(els[0], &m)
			m["version"] = m["version"].(float64) + 1
			d, _ := json.Marshal(m)
			_, _, err := bs.Merge(f.alice.ID, b.ID, []json.RawMessage{d})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
}
