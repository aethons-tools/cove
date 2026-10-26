package userui

import (
	"errors"
	"net/http"
	"time"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
	"github.com/aethons-tools/cove/internal/jam"
)

func (h *handler) boardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /app/user/api/boards/{id}", h.apiBoard)
	mux.HandleFunc("POST /app/user/api/boards", h.apiNewBoard)
}

// apiBoard is a board the viewer may read (the app's board route): 404 alike
// for missing, unreadable and whiteboards off.
func (h *handler) apiBoard(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	var b intercom.Board
	if h.d.Boards != nil {
		b, _, ok = h.d.Boards.CanRead(p.UserID, ident.ID(r.PathValue("id")))
	}
	if h.d.Boards == nil || !ok {
		writeAPIError(w, http.StatusNotFound, "not_found", "no such board")
		return
	}
	_, writable := h.d.Boards.CanWrite(p.UserID, b.ID)
	writeJSON(w, http.StatusOK, BoardInfo{ID: string(b.ID), Title: b.Title, Channel: string(b.Channel), ReadOnly: !writable})
}

// apiNewBoard starts a whiteboard in a conversation the viewer may post to
// (jam.Boards.Create's checks).
func (h *handler) apiNewBoard(w http.ResponseWriter, r *http.Request) {
	var req NewBoardRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	if h.d.Boards == nil {
		notFound(w)
		return
	}
	b, err := h.d.Boards.Create(jam.Poster{ID: p.UserID}, req.Channel, req.Title, time.Now())
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, NewBoardResponse{Board: string(b.ID), Channel: string(b.Channel)})
	case errors.Is(err, intercom.ErrInvalid):
		writeAPIError(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, jam.ErrSendDenied), errors.Is(err, jam.ErrSendUnresolved), errors.Is(err, jam.ErrRemoved), errors.Is(err, intercom.ErrNoBoard):
		notFound(w)
	default:
		h.lg.Error("api new board failed", "channel", req.Channel, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "couldn't start the whiteboard")
	}
}
