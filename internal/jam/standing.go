package jam

import (
	"fmt"
	"log/slog"
	"net/http"
)

// StandingActorID is the actor (and reservation) id Jam raises a role's
// standing session under: "standing-<project>-<role>-<name>", each part with
// characters outside [A-Za-z0-9._-] mapped to '-' (as for personal session ids).
// One id per name is what keeps the standing reconciler idempotent.
func StandingActorID(project, role, name string) string {
	return "standing-" + safeIDPart(project) + "-" + safeIDPart(role) + "-" + safeIDPart(name)
}

// standingIDHolder returns "project/role/name" of the declared standing session
// whose actor id is id, if any.
func standingIDHolder(store Store, id string) (string, bool) {
	for _, p := range store.ListProjects() {
		for _, ro := range store.ListRoles(p) {
			for _, s := range ro.Allocation.Standing {
				if StandingActorID(p, ro.Name, s.Name) == id {
					return p + "/" + ro.Name + "/" + s.Name, true
				}
			}
		}
	}
	return "", false
}

// registerStanding mounts the standing-declaration routes. Writes go through
// AddStanding/RemoveStanding, which hold the shared role lock.
func registerStanding(mux *http.ServeMux, store Store, log *slog.Logger) {

	mux.HandleFunc("GET /admin/roles/{project}/{role}/standing", func(w http.ResponseWriter, r *http.Request) {
		role, ok := store.GetRole(r.PathValue("project"), r.PathValue("role"))
		if !ok {
			http.Error(w, fmt.Sprintf("role %s/%s does not exist", r.PathValue("project"), r.PathValue("role")), http.StatusNotFound)
			return
		}
		out := role.Allocation.Standing
		if out == nil {
			out = []StandingSession{}
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("POST /admin/roles/{project}/{role}/standing", func(w http.ResponseWriter, r *http.Request) {
		project, roleName := r.PathValue("project"), r.PathValue("role")
		var b StandingSession
		if !decode(w, r, &b) {
			return
		}
		if err := AddStanding(store, project, roleName, b); err != nil {
			http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
			return
		}
		log.Info("admin standing session declared", "operator", OperatorID(r), "project", project, "role", roleName, "name", b.Name, "id", StandingActorID(project, roleName, b.Name))
		w.WriteHeader(http.StatusCreated)
	})

	mux.HandleFunc("DELETE /admin/roles/{project}/{role}/standing/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, roleName, name := r.PathValue("project"), r.PathValue("role"), r.PathValue("name")
		if err := RemoveStanding(store, project, roleName, name); err != nil {
			http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
			return
		}
		log.Info("admin standing session dismissed", "operator", OperatorID(r), "project", project, "role", roleName, "name", name)
		w.WriteHeader(http.StatusNoContent)
	})
}
