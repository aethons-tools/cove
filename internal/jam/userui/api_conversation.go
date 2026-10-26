package userui

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
)

// Page sizes for GET api/conversations/{c}.
const (
	pageDefault = 200
	pageMax     = 500
)

func (h *handler) conversationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/conversations/{c}", h.apiConversation)
	mux.HandleFunc("GET /app/user/api/conversations/{c}/presence", h.apiPresence)
}

// notFound is every "no such conversation": the same for one the viewer may
// not see, so a refusal never says whether the channel exists.
func notFound(w http.ResponseWriter) {
	writeAPIError(w, http.StatusNotFound, "not_found", "no such conversation")
}

// openConversation is the request's participant and {c}'s conversation, or
// it has answered (403 / 404).
func (h *handler) openConversation(w http.ResponseWriter, r *http.Request) (jam.Participant, Conversation, bool) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return p, Conversation{}, false
	}
	conv, ok := conversation(p, h.d, r.PathValue("c"))
	if !ok {
		notFound(w)
		return p, Conversation{}, false
	}
	return p, conv, true
}

func (h *handler) apiConversation(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, err := intParam(q.Get("before"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "before: not a number")
		return
	}
	limit, err := intParam(q.Get("limit"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "bad_request", "limit: not a number")
		return
	}
	_, conv, ok := h.openConversation(w, r)
	if !ok {
		return
	}
	switch {
	case limit <= 0:
		limit = pageDefault
	case limit > pageMax:
		limit = pageMax
	}
	conv.Messages, conv.Earlier = pageOf(conv.Messages, before, int(limit))
	conv.Sessions = sessionRows(conv.SessionIDs, h.d.Store.ListInstances(), h.presence)
	writeJSON(w, http.StatusOK, lists(conv))
}

func (h *handler) apiPresence(w http.ResponseWriter, r *http.Request) {
	_, conv, ok := h.openConversation(w, r)
	if !ok {
		return
	}
	rows := sessionRows(conv.SessionIDs, h.d.Store.ListInstances(), h.presence)
	if rows == nil {
		rows = []SessionRow{}
	}
	writeJSON(w, http.StatusOK, PresenceResponse{Sessions: rows})
}

// pageOf is the newest limit messages with seq < before (0 = no bound), in
// order, and whether older ones exist. ms is in seq order.
func pageOf(ms []MessageRow, before int64, limit int) ([]MessageRow, bool) {
	end := len(ms)
	if before > 0 {
		end = sort.Search(len(ms), func(i int) bool { return ms[i].Seq >= before })
	}
	start := max(0, end-limit)
	if start == end {
		return nil, false
	}
	return ms[start:end], start > 0
}

func intParam(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseInt(s, 10, 64)
}

// lists makes conv's lists [] where empty (types.ts says T[], never null).
func lists(conv Conversation) Conversation {
	if conv.Messages == nil {
		conv.Messages = []MessageRow{}
	}
	if conv.Sessions == nil {
		conv.Sessions = []SessionRow{}
	}
	if conv.SessionIDs == nil {
		conv.SessionIDs = []string{}
	}
	if conv.CallIn == nil {
		conv.CallIn = []NewMessageOption{}
	}
	return conv
}
