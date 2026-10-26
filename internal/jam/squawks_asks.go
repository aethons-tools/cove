package jam

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aethons-tools/cove/internal/ident"
	"github.com/aethons-tools/cove/internal/intercom"
)

// The /squawks ask routes (intercom structured asks): a session asks,
// amends or withdraws its asks, and lists them. Answers reach it through
// read, as squawks carrying their ask part.

// askRoute serves an ask route if path is one, reporting whether it did.
func (h *SquawksHandler) askRoute(w http.ResponseWriter, r *http.Request, actor Actor, inst Instance) bool {
	var handle func(http.ResponseWriter, *http.Request, Poster)
	method := http.MethodPost
	switch {
	case strings.HasSuffix(r.URL.Path, "/ask/amend"):
		handle = h.handleAmend
	case strings.HasSuffix(r.URL.Path, "/ask/withdraw"):
		handle = h.handleWithdraw
	case strings.HasSuffix(r.URL.Path, "/ask"):
		handle = h.handleAsk
	case strings.HasSuffix(r.URL.Path, "/asks"):
		handle, method = h.handleAsks, http.MethodGet
	default:
		return false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return true
	}
	if h.configured(w) {
		handle(w, r, Poster{ID: ident.ID(actor.ID), Session: &inst, Actor: &actor})
	}
	return true
}

// askError writes err's status, reporting whether there was none.
func (h *SquawksHandler) askError(w http.ResponseWriter, p Poster, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, intercom.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, ErrSendDenied), errors.Is(err, intercom.ErrAskForbidden):
		http.Error(w, "not allowed", http.StatusForbidden)
	case errors.Is(err, ErrSendUnresolved), errors.Is(err, ErrRemoved), errors.Is(err, intercom.ErrAskNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, intercom.ErrAskClosed), errors.Is(err, intercom.ErrAskChanged):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		h.log.Error("intercom: ask failed", "actor", string(p.ID), "error", err.Error())
		http.Error(w, "ask failed", http.StatusInternalServerError)
	}
	return false
}

func decodeAsk(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, MaxSquawkBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	return true
}

func (h *SquawksHandler) handleAsk(w http.ResponseWriter, r *http.Request, p Poster) {
	var req struct {
		Question string               `json:"question"`
		Options  []intercom.AskOption `json:"options"`
		To       string               `json:"to"`
		Of       []string             `json:"of"`
	}
	if !decodeAsk(w, r, &req) {
		return
	}
	a, ch, err := h.ic.Ask(p, req.To, AskInput{Question: req.Question, Options: req.Options, Of: req.Of}, h.now())
	if !h.askError(w, p, err) {
		return
	}
	h.log.Info("intercom", "actor", string(p.ID), "op", "ask", "ask", a.ID, "channel", string(ch.ID))
	writeJSON(w, http.StatusOK, struct {
		AskID   string `json:"ask_id"`
		Version int    `json:"version"`
		Channel Party  `json:"channel"`
	}{a.ID, a.Version, h.ic.ChannelParty(ch.ID)})
}

func (h *SquawksHandler) handleAmend(w http.ResponseWriter, r *http.Request, p Poster) {
	var req struct {
		AskID    string               `json:"ask_id"`
		Question string               `json:"question"`
		Options  []intercom.AskOption `json:"options"`
	}
	if !decodeAsk(w, r, &req) {
		return
	}
	a, err := h.ic.AmendAsk(p, req.AskID, req.Question, req.Options)
	if !h.askError(w, p, err) {
		return
	}
	h.log.Info("intercom", "actor", string(p.ID), "op", "amend", "ask", a.ID, "version", a.Version)
	writeJSON(w, http.StatusOK, struct {
		Version int `json:"version"`
	}{a.Version})
}

func (h *SquawksHandler) handleWithdraw(w http.ResponseWriter, r *http.Request, p Poster) {
	var req struct {
		AskID  string `json:"ask_id"`
		Reason string `json:"reason"`
	}
	if !decodeAsk(w, r, &req) {
		return
	}
	if _, err := h.ic.WithdrawAsk(p, req.AskID, req.Reason); !h.askError(w, p, err) {
		return
	}
	h.log.Info("intercom", "actor", string(p.ID), "op", "withdraw", "ask", req.AskID)
	w.WriteHeader(http.StatusNoContent)
}

// askOut is one ask in GET /squawks/asks.
type askOut struct {
	AskID    string               `json:"ask_id"`
	Channel  Party                `json:"channel"`
	Question string               `json:"question"`
	Options  []intercom.AskOption `json:"options"`
	Version  int                  `json:"version"`
	State    intercom.AskState    `json:"state"`
	Answer   *intercom.AskAnswer  `json:"answer,omitempty"`
	Reason   string               `json:"reason,omitempty"`
}

func (h *SquawksHandler) handleAsks(w http.ResponseWriter, r *http.Request, p Poster) {
	out := []askOut{}
	for _, a := range h.ic.AsksOf(p.ID, intercom.AskState(r.URL.Query().Get("state"))) {
		out = append(out, askOut{AskID: a.ID, Channel: h.ic.ChannelParty(a.Channel), Question: a.Question, Options: a.Options,
			Version: a.Version, State: a.State, Answer: a.Answer, Reason: a.Reason})
	}
	writeJSON(w, http.StatusOK, struct {
		Asks []askOut `json:"asks"`
	}{out})
}
