package jam

import (
	"context"
	"errors"
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

// StandingStatus is one declared standing session as the list route reports
// it: the declaration plus its pending upgrade state ("" none; see
// StandingUpgrader.UpgradeState).
type StandingStatus struct {
	StandingSession
	Upgrade string `json:"upgrade,omitempty"`
}

// standingTarget is the shared prologue of the per-name standing operations:
// 404 for an unknown role or name, 503 when the operation's reconciler hook
// isn't wired, and 409 when the name's actor id is held by a live cove that is
// not this session. It returns the actor id.
func standingTarget(store Store, project, roleName, name string, wired bool) (string, error) {
	role, ok := store.GetRole(project, roleName)
	if !ok {
		return "", writeErr(http.StatusNotFound, "role %s/%s does not exist", project, roleName)
	}
	if !slices.ContainsFunc(role.Allocation.Standing, func(s StandingSession) bool { return s.Name == name }) {
		return "", writeErr(http.StatusNotFound, "no standing session %q on role %s/%s", name, project, roleName)
	}
	if !wired {
		return "", writeErr(http.StatusServiceUnavailable, "standing reconciler not running")
	}
	id := StandingActorID(project, roleName, name)
	if inst, ok := store.GetInstance(id); ok && inst.Phase != PhaseGone &&
		(inst.SessionKind != SessionKindStanding || inst.Project != project || inst.Role != roleName || inst.Name != name) {
		return "", writeErr(http.StatusConflict, "actor id %s is held by another cove; not touching it", id)
	}
	return id, nil
}

// ResetStanding resets the declared standing session name of project's role
// through the standing reconciler (sup's StandingResetter): its cove is torn
// down and its state deleted, the declaration kept, so the reconciler raises it
// fresh. A name that is down has its state deleted all the same. Pending in
// the result: the teardown or purge failed and the reconciler retries it every
// pass (the reset is accepted and will happen). Errors are *WriteError (see
// standingTarget).
func ResetStanding(ctx context.Context, store Store, sup *Supervisor, project, roleName, name string) (StandingResetResult, error) {
	id, err := standingTarget(store, project, roleName, name, sup != nil && sup.resetter != nil)
	if err != nil {
		return StandingResetResult{}, err
	}
	if err := sup.resetter.ResetStanding(ctx, project, roleName, name); err != nil {
		return StandingResetResult{Pending: true, Reason: fmt.Sprintf("reset of %s pending (Jam retries it every standing pass): %s", id, err)}, nil
	}
	return StandingResetResult{}, nil
}

// StandingUpgradeResult is the upgrade route's body. Pending: the upgrade is
// queued with the standing reconciler, State its current pending state (watch
// it in the standing list). Not pending: the session already runs the current
// image (Image) and was left alone (Reason "already current").
type StandingUpgradeResult struct {
	Pending bool   `json:"pending"`
	State   string `json:"state,omitempty"`
	Image   string `json:"image,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// UpgradeBusy names the state that puts inst mid-episode, or "" when it may be
// restarted: idled (paused), or live and waiting, blocked or done; a
// terminating or lost cove is going away anyway. holding is busy — its turn
// ended but background tasks still run, which a teardown would kill — as are
// running, raising and a live cove that hasn't reported an activity yet.
func UpgradeBusy(inst Instance) string {
	switch inst.Phase {
	case PhaseIdled, PhaseTerminating, PhaseLost, PhaseGone:
		return ""
	case PhaseLive:
		switch inst.Activity {
		case ActivityWaiting, ActivityBlocked, ActivityDone:
			return ""
		case "":
			return "live, activity unreported"
		}
		return "live, activity " + string(inst.Activity)
	}
	return string(inst.Phase)
}

// UpgradeStanding queues an upgrade of the declared standing session name of
// project's role with the standing reconciler (sup's StandingUpgrader,
// COV-251): on its next passes the reconciler prepares the image a raise would
// run now, waits until the session is idle (UpgradeBusy; force skips the
// wait), tears its cove down keeping its state, and raises it again on that
// image, resuming its conversation and workspace. A session already on the
// current image (status ok) is left alone unless force. Nothing slow happens
// here. Errors are *WriteError: standingTarget's 404/503/409, 409 while a
// reset of the name is pending, 404 if the name was dismissed meanwhile.
func UpgradeStanding(store Store, sup *Supervisor, project, roleName, name string, force bool) (StandingUpgradeResult, error) {
	id, err := standingTarget(store, project, roleName, name, sup != nil && sup.upgrader != nil)
	if err != nil {
		return StandingUpgradeResult{}, err
	}
	if inst, ok := store.GetInstance(id); ok && !force &&
		imageStatus(sup, map[[2]string]currentImage{}, inst) == "ok" {
		return StandingUpgradeResult{Image: inst.ImageTag, Reason: "already current"}, nil
	}
	if err := sup.upgrader.QueueUpgrade(project, roleName, name, force); err != nil {
		switch {
		case errors.Is(err, ErrStandingNotDeclared):
			return StandingUpgradeResult{}, writeErr(http.StatusNotFound, "%s", err.Error())
		case errors.Is(err, ErrStandingResetPending):
			return StandingUpgradeResult{}, writeErr(http.StatusConflict, "%s; it raises the session fresh on the current image once done", err.Error())
		}
		return StandingUpgradeResult{}, err
	}
	st := sup.upgrader.UpgradeState(project, roleName, name)
	if st == "" {
		st = UpgradeQueued
	}
	return StandingUpgradeResult{Pending: true, State: st}, nil
}

// writeStandingResult answers a per-name standing operation: its error with
// its status (a store failure 500), else body — 202 when pending (accepted,
// the reconciler finishes it), 200 when done.
func writeStandingResult(w http.ResponseWriter, body any, pending bool, err error) {
	if err != nil {
		http.Error(w, err.Error(), WriteStatus(err, http.StatusInternalServerError))
		return
	}
	code := http.StatusOK
	if pending {
		code = http.StatusAccepted
	}
	writeJSON(w, code, body)
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
// AddStanding/RemoveStanding, which hold the shared role lock. Reset and
// upgrade need the supervisor (nil → 503).
func registerStanding(mux *http.ServeMux, store Store, sup *Supervisor, log *slog.Logger) {

	mux.HandleFunc("GET /admin/roles/{project}/{role}/standing", func(w http.ResponseWriter, r *http.Request) {
		role, ok := store.GetRole(r.PathValue("project"), r.PathValue("role"))
		if !ok {
			http.Error(w, fmt.Sprintf("role %s/%s does not exist", r.PathValue("project"), r.PathValue("role")), http.StatusNotFound)
			return
		}
		out := []StandingStatus{}
		for _, s := range role.Allocation.Standing {
			out = append(out, StandingStatus{StandingSession: s, Upgrade: sup.StandingUpgradeState(r.PathValue("project"), role.Name, s.Name)})
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
		res, err := ResetStanding(r.Context(), store, sup, project, roleName, name)
		if err == nil {
			log.Info("admin standing session reset", "operator", OperatorID(r), "project", project, "role", roleName, "name", name, "pending", res.Pending)
		}
		writeStandingResult(w, res, res.Pending, err)
	})

	// Upgrade: see UpgradeStanding. ?force=true skips the already-current
	// check and the idle wait. 202 {pending:true, state} when queued, 200
	// {pending:false, reason:"already current"} when there's nothing to do.
	mux.HandleFunc("POST /admin/roles/{project}/{role}/standing/{name}/upgrade", func(w http.ResponseWriter, r *http.Request) {
		project, roleName, name := r.PathValue("project"), r.PathValue("role"), r.PathValue("name")
		force := r.URL.Query().Get("force") == "true"
		res, err := UpgradeStanding(store, sup, project, roleName, name, force)
		if err == nil {
			log.Info("admin standing session upgrade", "operator", OperatorID(r), "project", project, "role", roleName, "name", name,
				"force", force, "pending", res.Pending, "state", res.State, "reason", res.Reason)
		}
		writeStandingResult(w, res, res.Pending, err)
	})
}
