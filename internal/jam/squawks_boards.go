package jam

import (
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
		id := strings.TrimSuffix(strings.TrimPrefix(rest, "/"), "/elements")
		if strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		h.brd.HandleElements(w, r, p.ID, ident.ID(id))
	case rest == "":
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	default:
		http.NotFound(w, r)
	}
}
