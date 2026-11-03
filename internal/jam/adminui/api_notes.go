package adminui

import (
	"log/slog"
	"net/http"

	"github.com/aethons-tools/cove/internal/jam"
	"github.com/aethons-tools/cove/internal/jam/squawkrender"
)

// operatorNotesOf is notes as a page shows them: the markdown and its
// rendering (squawkrender: no raw HTML, no dangerous links).
func operatorNotesOf(notes string) OperatorNotes {
	out := OperatorNotes{Markdown: notes}
	if notes != "" {
		out.HTML = string(squawkrender.Body("", notes))
	}
	return out
}

// maxNotesBody bounds a notes write's body. Notes have no size limit of their
// own; this only keeps one request from being unbounded.
const maxNotesBody = 64 << 20

// apiOperatorNotes mounts each config object's operator-notes write (a
// branch write, like the rest of its config). Kits and the Jam keep theirs
// in kit.yml and jam.yml.
func apiOperatorNotes(api *http.ServeMux, store jam.Store, log *slog.Logger) {
	route := func(pattern, kind string, name func(r *http.Request) string) {
		api.HandleFunc("POST "+apiPrefix+pattern, func(w http.ResponseWriter, r *http.Request) {
			var f struct {
				Notes string `json:"notes"`
			}
			if !decodeJSONMax(w, r, &f, maxNotesBody) {
				return
			}
			key := jam.ConfigKey{Kind: kind, Name: name(r)}
			if err := jam.SetOperatorNotes(store, key, f.Notes); err != nil {
				apiFail(w, err, http.StatusBadRequest)
				return
			}
			log.Info("ui operator notes set", "operator", jam.OperatorID(r), "kind", key.Kind, "name", key.Name)
			writeJSON(w, http.StatusOK, operatorNotesOf(f.Notes))
		})
	}
	path := func(k string) func(*http.Request) string {
		return func(r *http.Request) string { return r.PathValue(k) }
	}
	route("users/{user}/operator-notes", jam.ConfigKindUser, path("user"))
	route("destinations/{name}/operator-notes", jam.ConfigKindDestination, path("name"))
	route("model-specs/{name}/operator-notes", jam.ConfigKindModelSpec, path("name"))
	route("projects/{project}/operator-notes", jam.ConfigKindProject, path("project"))
	route("projects/{project}/roles/{role}/operator-notes", jam.ConfigKindRole, func(r *http.Request) string {
		return r.PathValue("project") + "/" + r.PathValue("role")
	})
}
