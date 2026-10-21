package jam

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSquawksBoards(t *testing.T) {
	f := newSqFixture(t)
	f.h.SetBoards(NewBoards(f.log, f.ic))
	rec := f.do(http.MethodPost, "/squawks/boards", f.standing, `{"title":"Plan"}`)
	var c struct {
		BoardID string `json:"board_id"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &c) != nil {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPost, "/squawks/boards/"+c.BoardID+"/elements", f.standing, `{"elements":[`+string(rect("a", 1))+`]}`); rec.Code != http.StatusOK {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, "/squawks/boards", f.standing, ""); !strings.Contains(rec.Body.String(), c.BoardID) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodGet, "/squawks/boards/"+c.BoardID+"/elements", f.ticket, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a session outside reads = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, "/squawks/boards", f.standing, `{"title":"x","to":"user:bob"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("beyond addressing = %d", rec.Code)
	}
}
