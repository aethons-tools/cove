package jam

import (
	"errors"
	"log/slog"
	"net/http"
)

// WithModelSpecs mounts the model-spec admin API (under the /admin/* operator
// gate, like every AdminOption route):
//
//	GET    /admin/model-specs         list (sorted by name)
//	POST   /admin/model-specs         create (409 if the name exists)
//	GET    /admin/model-specs/{name}  show (404 if absent)
//	PUT    /admin/model-specs/{name}  replace (404 if absent; body name must match)
//	DELETE /admin/model-specs/{name}  delete (404 if absent; 409 while a role resolves to it)
//
// credExists and poolConfigured feed ValidateModelSpec's principal check. Audit
// logs carry the operator, name and type only — never provider-env or settings.
func WithModelSpecs(store Store, credExists func(string) bool, poolConfigured bool, log *slog.Logger) AdminOption {
	return func(mux *http.ServeMux) {
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
		mux.HandleFunc("POST /admin/model-specs", func(w http.ResponseWriter, r *http.Request) {
			var m ModelSpec
			if !decode(w, r, &m) {
				return
			}
			if err := CreateModelSpec(store, m, credExists, poolConfigured); err != nil {
				http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
				return
			}
			log.Info("admin model-spec created", "operator", OperatorID(r), "name", m.Name, "type", string(m.Type))
			w.WriteHeader(http.StatusCreated)
		})
		mux.HandleFunc("PUT /admin/model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
			var m ModelSpec
			if !decode(w, r, &m) {
				return
			}
			name := r.PathValue("name")
			if m.Name == "" {
				m.Name = name
			}
			if m.Name != name {
				http.Error(w, "body name does not match the path; a model-spec cannot be renamed", http.StatusBadRequest)
				return
			}
			if err := UpdateModelSpec(store, m, credExists, poolConfigured); err != nil {
				http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
				return
			}
			log.Info("admin model-spec updated", "operator", OperatorID(r), "name", m.Name, "type", string(m.Type))
			w.WriteHeader(http.StatusNoContent)
		})
		mux.HandleFunc("DELETE /admin/model-specs/{name}", func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			if err := store.RemoveModelSpec(name); err != nil {
				http.Error(w, err.Error(), ModelSpecRemoveStatus(err))
				return
			}
			log.Info("admin model-spec deleted", "operator", OperatorID(r), "name", name)
			w.WriteHeader(http.StatusNoContent)
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
