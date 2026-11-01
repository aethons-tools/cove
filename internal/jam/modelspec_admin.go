package jam

import (
	"errors"
	"net/http"
)

// WithModelSpecs mounts the model-spec admin API (under the /admin/* operator
// gate, like every AdminOption route):
//
//	GET    /admin/model-specs         list (sorted by name)
//	GET    /admin/model-specs/{name}  show (404 if absent)
//
// Model-specs change only in git.
func WithModelSpecs(store Store) AdminOption {
	return func(mux *adminMux) {
		mux.HandleFunc("GET /admin/model-specs", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, store.ListModelSpecs())
		})
		mux.HandleFunc("GET /admin/model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
			m, ok := store.GetModelSpec(r.PathValue("name"))
			if !ok {
				http.Error(w, "model-spec not found", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, m)
		})
	}
}

// ModelSpecRemoveStatus maps a RemoveModelSpec error to its HTTP status: 409
// while a role resolves to the spec, else 404 (absent).
func ModelSpecRemoveStatus(err error) int {
	if errors.Is(err, ErrModelSpecInUse) {
		return http.StatusConflict
	}
	return http.StatusNotFound
}
