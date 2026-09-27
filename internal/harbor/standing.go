package harbor

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync"
)

// StandingActorID is the actor (and reservation) id harbor raises a role's
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

// registerStanding mounts the standing-declaration routes. Each write is a
// read-modify-write of the Role that keeps every other field; mu serializes
// them so two concurrent declarations can't drop one another.
func registerStanding(mux *http.ServeMux, store Store, log *slog.Logger) {
	var mu sync.Mutex

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
		if b.Name == "" || b.Prompt == "" {
			http.Error(w, "name and prompt are required", http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		role, ok := store.GetRole(project, roleName)
		if !ok {
			http.Error(w, fmt.Sprintf("role %s/%s does not exist", project, roleName), http.StatusNotFound)
			return
		}
		id := StandingActorID(project, roleName, b.Name)
		for _, s := range role.Allocation.Standing {
			if s.Name == b.Name {
				http.Error(w, fmt.Sprintf("standing session %q is already declared on %s/%s", b.Name, project, roleName), http.StatusBadRequest)
				return
			}
		}
		// The reconciler keys each cove on its actor id, so it must be unique across
		// every declaration, in any role or project.
		if owner, ok := standingIDHolder(store, id); ok {
			http.Error(w, fmt.Sprintf("standing session %q would share actor id %s with declared %s; pick a name that differs in [A-Za-z0-9._-]", b.Name, id, owner), http.StatusBadRequest)
			return
		}
		// A fresh slice, so the stored role never aliases the one we read.
		role.Allocation.Standing = append(slices.Clone(role.Allocation.Standing), b)
		if err := store.PutRole(project, role); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin standing session declared", "operator", OperatorID(r), "project", project, "role", roleName, "name", b.Name, "id", id)
		w.WriteHeader(http.StatusCreated)
	})

	mux.HandleFunc("DELETE /admin/roles/{project}/{role}/standing/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, roleName, name := r.PathValue("project"), r.PathValue("role"), r.PathValue("name")
		mu.Lock()
		defer mu.Unlock()
		role, ok := store.GetRole(project, roleName)
		if !ok {
			http.Error(w, fmt.Sprintf("role %s/%s does not exist", project, roleName), http.StatusNotFound)
			return
		}
		i := slices.IndexFunc(role.Allocation.Standing, func(s StandingSession) bool { return s.Name == name })
		if i < 0 {
			http.Error(w, fmt.Sprintf("no standing session %q on %s/%s", name, project, roleName), http.StatusNotFound)
			return
		}
		role.Allocation.Standing = slices.Delete(slices.Clone(role.Allocation.Standing), i, i+1)
		if len(role.Allocation.Standing) == 0 {
			role.Allocation.Standing = nil
		}
		if err := store.PutRole(project, role); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Info("admin standing session dismissed", "operator", OperatorID(r), "project", project, "role", roleName, "name", name)
		w.WriteHeader(http.StatusNoContent)
	})
}
