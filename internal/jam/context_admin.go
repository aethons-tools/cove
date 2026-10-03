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
	fail := func(w http.ResponseWriter, err error) {
		http.Error(w, err.Error(), WriteStatus(err, projectErrStatus(err, http.StatusInternalServerError)))
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
		if err := SetRoleContextChecked(store, r.PathValue("project"), r.PathValue("role"), b); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin role context set", "operator", OperatorID(r), "project", r.PathValue("project"), "role", r.PathValue("role"), "core_bytes", len(b.Core), "leaves", len(b.Leaves))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /admin/roles/{project}/{role}/context", func(w http.ResponseWriter, r *http.Request) {
		if err := ClearRoleContext(store, r.PathValue("project"), r.PathValue("role")); err != nil {
			fail(w, err)
			return
		}
		log.Info("admin role context cleared", "operator", OperatorID(r), "project", r.PathValue("project"), "role", r.PathValue("role"))
		w.WriteHeader(http.StatusNoContent)
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
	setProject := func(w http.ResponseWriter, r *http.Request, b ContextBody) {
		if err := SetProjectContextChecked(store, r.PathValue("project"), b); err != nil {
			fail(w, err)
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
		if err := SetJamContextChecked(store, b); err != nil {
			fail(w, err)
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
