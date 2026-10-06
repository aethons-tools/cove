package jam

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
)

// StandingActorID is the actor (and reservation) id Jam raises a role's
// standing session under: "standing-<project>-<role>-<name>", each part with
// characters outside [A-Za-z0-9._-] mapped to '-' (as for personal session ids).
// One id per name is what keeps the standing reconciler idempotent.
func StandingActorID(project, role, name string) string {
	return "standing-" + safeIDPart(project) + "-" + safeIDPart(role) + "-" + safeIDPart(name)
}

// StandingResetResult is the reset route's body: Pending when the reset is
// still in progress (its state is in use or its teardown failed) and the
// standing reconciler finishes it on a later pass; the name is not raised
// until then.
type StandingResetResult struct {
	Pending bool   `json:"pending"`
	Reason  string `json:"reason,omitempty"`
}

// ResetStanding resets the declared standing session name of project's role
// through the standing reconciler (sup's StandingResetter): its cove is torn
// down and its state deleted, the declaration kept, so the reconciler raises it
// fresh. A name that is down has its state deleted all the same. Errors are
// *WriteError: 404 for an unknown role or name, 503 with no supervisor or
// reconciler, 409 for an actor id held by a cove that is not this session, and
// 202 when the reset is pending (the reconciler retries it every pass).
func ResetStanding(ctx context.Context, store Store, sup *Supervisor, project, roleName, name string) error {
	role, ok := store.GetRole(project, roleName)
	if !ok {
		return writeErr(http.StatusNotFound, "role %s/%s does not exist", project, roleName)
	}
	if !slices.ContainsFunc(role.Allocation.Standing, func(s StandingSession) bool { return s.Name == name }) {
		return writeErr(http.StatusNotFound, "no standing session %q on role %s/%s", name, project, roleName)
	}
	if sup == nil || sup.resetter == nil {
		return writeErr(http.StatusServiceUnavailable, "standing reconciler not running")
	}
	id := StandingActorID(project, roleName, name)
	if inst, ok := store.GetInstance(id); ok && (inst.SessionKind != SessionKindStanding || inst.Project != project || inst.Role != roleName || inst.Name != name) {
		return writeErr(http.StatusConflict, "actor id %s is held by another cove; not resetting it", id)
	}
	if err := sup.resetter.ResetStanding(ctx, project, roleName, name); err != nil {
		return writeErr(http.StatusAccepted, "reset of %s pending (Jam retries it every standing pass): %s", id, err.Error())
	}
	return nil
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
// AddStanding/RemoveStanding, which hold the shared role lock. Reset needs the
// supervisor (nil → 503).
func registerStanding(mux *http.ServeMux, store Store, sup *Supervisor, log *slog.Logger) {

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

	// Reset: see ResetStanding. 200 {pending:false} when done, 202
	// {pending:true, reason} when the reconciler is still finishing it.
	mux.HandleFunc("POST /admin/roles/{project}/{role}/standing/{name}/reset", func(w http.ResponseWriter, r *http.Request) {
		project, roleName, name := r.PathValue("project"), r.PathValue("role"), r.PathValue("name")
		err := ResetStanding(r.Context(), store, sup, project, roleName, name)
		if status := WriteStatus(err, http.StatusInternalServerError); err != nil && status != http.StatusAccepted {
			http.Error(w, err.Error(), status)
			return
		}
		res, code := StandingResetResult{}, http.StatusOK
		if err != nil {
			res, code = StandingResetResult{Pending: true, Reason: err.Error()}, http.StatusAccepted
		}
		log.Info("admin standing session reset", "operator", OperatorID(r), "project", project, "role", roleName, "name", name, "pending", res.Pending)
		writeJSON(w, code, res)
	})
}
