package jam

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
)

// boardRoute serves a session's board routes; rest follows /squawks/boards.
func (h *SquawksHandler) boardRoute(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance, rest string) {
	if h.brd == nil {
		http.Error(w, "whiteboards unavailable", http.StatusNotFound)
		return
	}
	p := Poster{ID: ident.ID(actor.ID), Session: &inst, Actor: &actor}
	switch {
	case rest == "" && r.Method == http.MethodPost:
		h.brd.HandleCreate(w, r, p, h.now())
	case rest == "" && r.Method == http.MethodGet:
		type item struct {
			BoardID ident.ID `json:"board_id"`
			Channel Party    `json:"channel"`
			Title   string   `json:"title"`
			Seq     int64    `json:"seq"`
		}
		out := []item{}
		for _, b := range h.brd.ForChannelsOf(p.ID) {
			out = append(out, item{b.ID, h.ic.ChannelParty(b.Channel), b.Title, b.Seq})
		}
		writeJSON(w, http.StatusOK, struct {
			Boards []item `json:"boards"`
		}{out})
	case strings.HasSuffix(rest, "/elements"):
		id, ok := boardID(rest, "/elements")
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.brd.HandleElements(w, r, p.ID, id)
	case strings.HasSuffix(rest, "/read") && r.Method == http.MethodGet:
		id, ok := boardID(rest, "/read")
		if !ok {
			http.NotFound(w, r)
			return
		}
		els, seq, err := h.brd.Read(p.ID, id)
		if !boardError(w, err) {
			return
		}
		if els == nil {
			els = []CompactElement{}
		}
		writeJSON(w, http.StatusOK, struct {
			Seq      int64            `json:"seq"`
			Elements []CompactElement `json:"elements"`
		}{seq, els})
	case strings.HasSuffix(rest, "/apply") && r.Method == http.MethodPost:
		id, ok := boardID(rest, "/apply")
		if !ok {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBoardRequest)
		var req struct {
			Ops []BoardOp `json:"ops"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		seq, added, err := h.brd.Apply(p.ID, id, req.Ops)
		if !boardError(w, err) {
			return
		}
		if added == nil {
			added = []string{}
		}
		writeJSON(w, http.StatusOK, struct {
			Seq   int64    `json:"seq"`
			Added []string `json:"added"`
		}{seq, added})
	case strings.HasSuffix(rest, "/snapshot.png") && r.Method == http.MethodGet:
		id, ok := boardID(rest, "/snapshot.png")
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.brd.ServeSnapshot(w, r, p.ID, id)
	case rest == "":
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}

// boardID is the id in "/<id><suffix>", if it is a single path segment.
func boardID(rest, suffix string) (ident.ID, bool) {
	id := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), suffix)
	return ident.ID(id), id != "" && !strings.Contains(id, "/")
}
