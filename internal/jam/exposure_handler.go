package jam

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// ExposureSetter is the narrow slice of *Supervisor the /exposures handler needs.
type ExposureSetter interface {
	Expose(actorID, name string, port int) (Exposure, error)
	Unexpose(actorID, name string) error
	Exposures(actorID string) []Exposure
}

// ExposureHandler is Jam's brokered preview endpoint, self-scoped to the
// caller's OWN instance like /alarms: GET /exposures lists its previews, PUT
// /exposures/{name} {"port":N} exposes (or re-points) one, DELETE
// /exposures/{name} removes one. Implements http.Handler.
type ExposureHandler struct {
	store  escalateStore
	setter ExposureSetter
	urlFor func(actorID, name string) (string, error) // nil: previews are off
	log    *slog.Logger
}

// NewExposureHandler constructs an ExposureHandler. urlFor maps a preview to
// the URL collaborators open; nil means previews are not enabled on this Jam.
func NewExposureHandler(store escalateStore, setter ExposureSetter, urlFor func(actorID, name string) (string, error), log *slog.Logger) *ExposureHandler {
	return &ExposureHandler{store: store, setter: setter, urlFor: urlFor, log: log}
}

// exposureView is one preview on the wire.
type exposureView struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	URL  string `json:"url"`
}

func (h *ExposureHandler) view(actorID string, e Exposure) (exposureView, error) {
	u, err := h.urlFor(actorID, e.Name)
	if err != nil {
		return exposureView{}, err
	}
	return exposureView{Name: e.Name, Port: e.Port, URL: u}, nil
}

func (h *ExposureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, named := strings.CutPrefix(r.URL.Path, "/exposures/")
	switch {
	case r.URL.Path == "/exposures":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	case named && name != "" && !strings.Contains(name, "/"):
		if r.Method != http.MethodPut && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "PUT, DELETE")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	actor, ok := authenticateCove(w, r, h.store)
	if !ok {
		return
	}
	if h.urlFor == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "previews are not enabled on this Jam"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		out := struct {
			Exposures []exposureView `json:"exposures"`
		}{Exposures: []exposureView{}}
		for _, e := range h.setter.Exposures(actor.ID) {
			v, err := h.view(actor.ID, e)
			if err != nil {
				h.log.Error("exposures: url failed", "actor", actor.ID, "error", err.Error())
				http.Error(w, "url failed", http.StatusBadGateway)
				return
			}
			out.Exposures = append(out.Exposures, v)
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodDelete:
		if err := h.setter.Unexpose(actor.ID, name); err != nil {
			if errors.Is(err, ErrNoExposure) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			h.log.Error("exposures: unexpose failed", "actor", actor.ID, "error", err.Error())
			http.Error(w, "unexpose failed", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default: // PUT
		r.Body = http.MaxBytesReader(w, r.Body, maxTurnEndBodyBytes)
		var req struct {
			Port int `json:"port"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		// The URL first, so a name it refuses never leaves an exposure behind.
		u, err := h.urlFor(actor.ID, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest) // the name: the agent reads the reason
			return
		}
		e, err := h.setter.Expose(actor.ID, name, req.Port)
		if err != nil {
			status := http.StatusBadRequest // validation: the agent reads the reason
			if errors.Is(err, ErrExposureLimit) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, http.StatusOK, exposureView{Name: e.Name, Port: e.Port, URL: u})
	}
}
