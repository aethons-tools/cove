package userui

import (
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
)

func (h *handler) askRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /app/user/api/asks/{id}/answer", h.apiAnswer)
}

// apiAnswer records the viewer's answer to an ask (see answerOutcome).
func (h *handler) apiAnswer(w http.ResponseWriter, r *http.Request) {
	var req AnswerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p, ok := jam.ParticipantFrom(r)
	if !ok {
		writeAPIError(w, http.StatusForbidden, "forbidden", "sign in as a project member")
		return
	}
	if h.d.Intercom == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "unavailable", "messaging not configured")
		return
	}
	id := r.PathValue("id")
	_, err := h.d.Intercom.AnswerAsk(p.UserID, id, req.Version, req.Choice, req.Comment)
	status, code, msg := answerOutcome(h.d, id, err)
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	if status == http.StatusInternalServerError {
		h.lg.Error("userui api answer failed", "ask", id, "error", err.Error())
	}
	writeAPIError(w, status, code, msg)
}
