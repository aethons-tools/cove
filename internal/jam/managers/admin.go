package managers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/aethons-tools/cove/internal/jam"
)

// AdminRoutes are the /admin/managers routes over h; mount them with
// jam.NewAdminHandler so they sit behind the operator authenticator.
func AdminRoutes(h *Hub) []jam.AdminOption {
	return []jam.AdminOption{
		jam.WithAdminRoute("GET /admin/managers", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, h.Statuses())
		})),
		jam.WithAdminRoute("POST /admin/managers", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			if err := jam.ValidManagerName(body.Name); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			tok, err := h.Add(body.Name)
			if err != nil {
				http.Error(w, err.Error(), errStatus(err))
				return
			}
			writeJSON(w, http.StatusCreated, tok)
		})),
		jam.WithAdminRoute("POST /admin/managers/{name}/rotate", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok, err := h.Rotate(r.PathValue("name"))
			if err != nil {
				http.Error(w, err.Error(), errStatus(err))
				return
			}
			writeJSON(w, http.StatusOK, tok)
		})),
		jam.WithAdminRoute("DELETE /admin/managers/{name}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			force, _ := strconv.ParseBool(r.URL.Query().Get("force"))
			if err := h.Revoke(r.PathValue("name"), force); err != nil {
				http.Error(w, err.Error(), errStatus(err))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})),
	}
}

// errStatus maps a Hub registry error to its HTTP status.
func errStatus(err error) int {
	switch {
	case errors.Is(err, jam.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, jam.ErrManagerExists), errors.Is(err, ErrManagerHasStudios):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
