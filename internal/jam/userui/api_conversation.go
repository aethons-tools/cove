package userui

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
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
	mux.HandleFunc("POST /app/user/api/conversations/{c}/read", h.apiRead)
	mux.HandleFunc("POST /app/user/api/conversations/{c}/join", h.apiJoin)
	mux.HandleFunc("POST /app/user/api/conversations/{c}/leave", h.apiLeave)
	mux.HandleFunc("POST /app/user/api/conversations/{c}/call-in", h.apiCallIn)
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
	p, conv, ok := h.openConversation(w, r)
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
	if inst, ok := h.headSession(p, conv.ChannelID); ok {
		conv.Session = inst.ActorID
	}
	conv.NewBoard = h.d.Boards != nil && conv.SendTo != "" && h.d.Boards.MayCreateIn(p.UserID, ident.ID(conv.ChannelID))
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
	if conv.OpenAsks == nil {
		conv.OpenAsks = []AskCard{}
	}
	return conv
}

// decodeJSON reads r's JSON body (up to 64 KiB) into v; see decodeJSONMax.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeJSONMax(w, r, v, 64<<10)
}

// decodeJSONMax reads r's JSON body into v, or answers 415 (not JSON: a
// cross-site form can't send application/json without a preflight), 413
// (over max) or 400.
func decodeJSONMax(w http.ResponseWriter, r *http.Request, v any, max int64) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "send application/json")
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "too_large", "body too large")
			return false
		}
		writeAPIError(w, http.StatusBadRequest, "bad_request", "bad JSON body")
		return false
	}
	return true
}

// apiRead advances the viewer's read cursor (History has none: a no-op).
func (h *handler) apiRead(w http.ResponseWriter, r *http.Request) {
	var req ReadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	id := r.PathValue("c")
	if strings.HasPrefix(id, legacyPrefix) || h.d.Log == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, ok := jam.UserChannel(h.d.Store, h.d.Intercom, h.d.Log, p.UserID, ident.ID(id)); !ok {
		notFound(w)
		return
	}
	if err := h.d.Store.CommitChannelRead(p.UserID, ident.ID(id), req.Seq); err != nil {
		h.lg.Warn("userui api mark-read failed", "channel", id, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "could not mark read")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) apiJoin(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	h.apiMembership(w, r, func(p jam.Participant, ch ident.ID) error { return h.d.Intercom.JoinChannel(p.UserID, ch) })
}

func (h *handler) apiLeave(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if !decodeJSON(w, r, &req) {
		return
	}
	h.apiMembership(w, r, func(p jam.Participant, ch ident.ID) error { return h.d.Intercom.LeaveChannel(p.UserID, ch) })
}

func (h *handler) apiCallIn(w http.ResponseWriter, r *http.Request) {
	var req CallInRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	h.apiMembership(w, r, func(p jam.Participant, ch ident.ID) error {
		_, _, err := h.d.Intercom.CallIn(jam.Poster{ID: p.UserID}, ch, req.Who)
		return err
	})
}

// apiMembership runs a membership change and answers as the HTML route does,
// in the API's envelope: never saying whether a refused channel exists.
func (h *handler) apiMembership(w http.ResponseWriter, r *http.Request, act func(jam.Participant, ident.ID) error) {
	p, ok := jam.ParticipantFrom(r)
	if !ok || h.d.Intercom == nil {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	err := act(p, ident.ID(r.PathValue("c")))
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, jam.ErrSendDenied):
		writeAPIError(w, http.StatusForbidden, "forbidden", "not allowed")
	case errors.Is(err, jam.ErrSendUnresolved), errors.Is(err, jam.ErrRemoved):
		notFound(w)
	case errors.Is(err, jam.ErrFixedMembers):
		writeAPIError(w, http.StatusConflict, "conflict", "this channel's members are fixed")
	default:
		h.lg.Error("userui api membership change failed", "channel", r.PathValue("c"), "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "the change failed")
	}
}
