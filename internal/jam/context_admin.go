package jam

import (
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ContextBody is an authored session-context layer as the admin API reads and
// writes it. Resources apply to projects only.
type ContextBody struct {
	Core      string                `json:"core" yaml:"core"`
	Leaves    []sessionctx.Leaf     `json:"leaves,omitempty" yaml:"leaves,omitempty"`
	Resources []sessionctx.Resource `json:"resources,omitempty" yaml:"resources,omitempty"`
}

func (b ContextBody) layer() sessionctx.Layer {
	return sessionctx.Layer{Core: b.Core, Leaves: b.Leaves}
}

func viewOf(l sessionctx.Layer, rs []sessionctx.Resource) ContextBody {
	return ContextBody{Core: l.Core, Leaves: l.Leaves, Resources: rs}
}

// registerContext mounts the authored session-context endpoints for roles,
// projects and the Jam (see docs/usage/jam/session-context.md).
func registerContext(mux *http.ServeMux, store Store, log *slog.Logger) {
	// Roles.
	mux.HandleFunc("GET /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		role, ok := store.GetRole(r.PathValue("project"), r.PathValue("role"))
		if !ok {
			http.Error(w, "role does not exist", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, viewOf(role.Context, nil))
	})
	// Projects.
	mux.HandleFunc("GET /admin/projects/{project}/context", func(w http.ResponseWriter, r *http.Request) {
		p, ok := store.GetProject(r.PathValue("project"))
		if !ok && r.PathValue("project") != DefaultProject {
			http.Error(w, "project does not exist", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, viewOf(p.Context, p.Resources))
	})
	// The Jam.
	mux.HandleFunc("GET /admin/jam/context", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, viewOf(store.GetJamContext(), nil))
	})
}
