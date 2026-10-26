package userui

import (
	"errors"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

func (h *handler) composeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /app/user/api/messages", h.apiSend)
	mux.HandleFunc("GET /app/user/api/recipients", h.apiRecipients)
}

// apiSend posts the viewer's message, as /app/user/send does, and says where
// it went.
func (h *handler) apiSend(w http.ResponseWriter, r *http.Request) {
	var req SendMessageRequest
	if !decodeJSONMax(w, r, &req, jam.MaxSquawkBodyBytes) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	if h.d.Send == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "messaging not configured")
		return
	}
	m, err := h.d.Send.Send(p, jam.SendRequest{To: req.To, Body: req.Body, ContentType: req.ContentType, ReplyTo: req.ReplyTo, Attachments: req.Attachments})
	var se *jam.SendError
	switch {
	case errors.As(err, &se):
		writeAPIError(w, se.Status, se.Code, se.Msg)
		return
	case err != nil:
		h.lg.Error("userui api send failed", "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, "internal", "send failed")
		return
	}
	writeJSON(w, http.StatusCreated, MessageSent{Channel: string(m.Channel), Seq: m.Seq})
}

func (h *handler) apiRecipients(w http.ResponseWriter, r *http.Request) {
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	opts := newMessageOptions(p, h.d)
	if opts == nil {
		opts = []NewMessageOption{}
	}
	writeJSON(w, http.StatusOK, RecipientsResponse{Options: opts})
}
