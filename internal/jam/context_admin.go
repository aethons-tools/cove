package jam

import (
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// ContextBody is an authored session-context layer as the admin API reads and
// writes it. Resources apply to projects only.
type ContextBody struct {
	Core      string                `json:"core"`
	Leaves    []sessionctx.Leaf     `json:"leaves,omitempty"`
	Resources []sessionctx.Resource `json:"resources,omitempty"`
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
	fail := func(w http.ResponseWriter, err error, fallback int) {
		http.Error(w, err.Error(), WriteStatus(err, projectErrStatus(err, fallback)))
	}
	// Roles.
	mux.HandleFunc("GET /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		role, ok := store.GetRole(r.PathValue("project"), r.PathValue("role"))
		if !ok {
			http.Error(w, "role does not exist", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, viewOf(role.Context, nil))
	})
	mux.HandleFunc("PUT /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		var b ContextBody
		if !decode(w, r, &b) {
			return
		}
		if len(b.Resources) > 0 {
			http.Error(w, "resources apply to projects only", http.StatusBadRequest)
			return
		}
		if err := SetRoleContext(store, r.PathValue("project"), r.PathValue("role"), b.layer()); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("admin role context set", "operator", OperatorID(r), "project", r.PathValue("project"), "role", r.PathValue("role"), "core_bytes", len(b.Core), "leaves", len(b.Leaves))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		if err := ClearRoleContext(store, r.PathValue("project"), r.PathValue("role")); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("admin role context cleared", "operator", OperatorID(r), "project", r.PathValue("project"), "role", r.PathValue("role"))
		w.WriteHeader(http.StatusNoContent)
	})
	// Projects.
	mux.HandleFunc("GET /admin/projects/{project}/context", func(w http.ResponseWriter, r *http.Request) {
		p, _ := store.GetProject(r.PathValue("project"))
		writeJSON(w, http.StatusOK, viewOf(p.Context, p.Resources))
	})
	setProject := func(w http.ResponseWriter, r *http.Request, b ContextBody) {
		if err := sessionctx.ValidateLayer(b.layer(), sessionctx.BudgetProject); err != nil {
			http.Error(w, "project context: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := sessionctx.ValidateResources(b.Resources); err != nil {
			http.Error(w, "project resources: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SetProjectContext(r.PathValue("project"), b.layer(), b.Resources); err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		log.Info("admin project context set", "operator", OperatorID(r), "project", r.PathValue("project"), "core_bytes", len(b.Core), "leaves", len(b.Leaves), "resources", len(b.Resources))
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("PUT /admin/projects/{project}/context", func(w http.ResponseWriter, r *http.Request) {
		var b ContextBody
		if decode(w, r, &b) {
			setProject(w, r, b)
		}
	})
	mux.HandleFunc("DELETE /admin/projects/{project}/context", func(w http.ResponseWriter, r *http.Request) {
		setProject(w, r, ContextBody{})
	})
	// The Jam.
	mux.HandleFunc("GET /admin/jam/context", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, viewOf(store.GetJamContext(), nil))
	})
	setJam := func(w http.ResponseWriter, r *http.Request, b ContextBody) {
		if len(b.Resources) > 0 {
			http.Error(w, "resources apply to projects only", http.StatusBadRequest)
			return
		}
		if err := sessionctx.ValidateLayer(b.layer(), sessionctx.BudgetJam); err != nil {
			http.Error(w, "jam context: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := store.SetJamContext(b.layer()); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		log.Info("admin jam context set", "operator", OperatorID(r), "core_bytes", len(b.Core), "leaves", len(b.Leaves))
		w.WriteHeader(http.StatusNoContent)
	}
	mux.HandleFunc("PUT /admin/jam/context", func(w http.ResponseWriter, r *http.Request) {
		var b ContextBody
		if decode(w, r, &b) {
			setJam(w, r, b)
		}
	})
	mux.HandleFunc("DELETE /admin/jam/context", func(w http.ResponseWriter, r *http.Request) {
		setJam(w, r, ContextBody{})
	})
}
