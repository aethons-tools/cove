package jam

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
)

// roleMu serializes every read-modify-write of a Role — role put, egress,
// standing — from the JSON admin API and the UI alike, so no writer can drop
// another's change. Every Role write outside a raw store.PutRole goes through
// the functions in this file.
var roleMu sync.Mutex

// RoleError is a refused role write, carrying the HTTP status it maps to.
type RoleError struct {
	Status int
	Msg    string
}

func (e *RoleError) Error() string { return e.Msg }

func roleErr(status int, format string, a ...any) error {
	return &RoleError{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// RoleStatus is err's HTTP status: a *RoleError's own, else fallback (the
// caller's status for a store failure).
func RoleStatus(err error, fallback int) int {
	if re, ok := errors.AsType[*RoleError](err); ok {
		return re.Status
	}
	return fallback
}

// UpdateRole applies fn to the stored role and stores the result, under the
// role lock. A missing role is a 404 RoleError; an error from fn aborts the
// write and is returned as is. fn edits a copy, but its slices and maps may
// alias the stored role's, so it must replace them, never mutate in place.
func UpdateRole(store Store, project, name string, fn func(*Role) error) error {
	roleMu.Lock()
	defer roleMu.Unlock()
	role, ok := store.GetRole(project, name)
	if !ok {
		return roleErr(http.StatusNotFound, "role %s/%s does not exist", project, name)
	}
	if err := fn(&role); err != nil {
		return err
	}
	return store.PutRole(project, role)
}

// CreateRole stores a new role; an existing one is a 409 RoleError.
func CreateRole(store Store, project string, role Role) error {
	roleMu.Lock()
	defer roleMu.Unlock()
	if _, ok := store.GetRole(project, role.Name); ok {
		return roleErr(http.StatusConflict, "role %s/%s already exists", orDefaultProject(project), role.Name)
	}
	return store.PutRole(project, role)
}

// PutRoleKeeping creates or replaces a role, keeping an existing role's
// standing declarations and egress policy: those have their own writers
// (AddStanding/RemoveStanding, SetRoleEgress/ClearRoleEgress).
func PutRoleKeeping(store Store, project string, role Role) error {
	roleMu.Lock()
	defer roleMu.Unlock()
	if existing, ok := store.GetRole(project, role.Name); ok {
		role.Allocation.Standing = existing.Allocation.Standing
		role.Scope.Egress = existing.Scope.Egress
	}
	return store.PutRole(project, role)
}

// SetRoleEgress replaces the role's egress policy with the normalized domains
// and returns how many it kept. A bad domain is a 400 RoleError.
func SetRoleEgress(store Store, project, name string, domains []string) (int, error) {
	n := 0
	err := UpdateRole(store, project, name, func(r *Role) error {
		norm, err := NormalizeEgress(domains)
		if err != nil {
			return &RoleError{Status: http.StatusBadRequest, Msg: err.Error()}
		}
		// A fresh policy, so the stored role never aliases one a reader holds.
		r.Scope.Egress = &EgressPolicy{Domains: norm}
		n = len(norm)
		return nil
	})
	return n, err
}

// ClearRoleEgress removes the role's egress policy (its coves revert to the
// kit's default list).
func ClearRoleEgress(store Store, project, name string) error {
	return UpdateRole(store, project, name, func(r *Role) error {
		r.Scope.Egress = nil
		return nil
	})
}

// AddStanding declares a standing session on the role. A missing name or
// prompt, a duplicate name, or an actor id that collides with another
// declaration (in any role or project) is a 400 RoleError.
func AddStanding(store Store, project, name string, s StandingSession) error {
	if s.Name == "" || s.Prompt == "" {
		return roleErr(http.StatusBadRequest, "name and prompt are required")
	}
	return UpdateRole(store, project, name, func(r *Role) error {
		if slices.ContainsFunc(r.Allocation.Standing, func(x StandingSession) bool { return x.Name == s.Name }) {
			return roleErr(http.StatusBadRequest, "standing session %q is already declared on %s/%s", s.Name, project, name)
		}
		// The reconciler keys each cove on its actor id, so it must be unique across
		// every declaration, in any role or project.
		id := StandingActorID(project, name, s.Name)
		if owner, ok := standingIDHolder(store, id); ok {
			return roleErr(http.StatusBadRequest, "standing session %q would share actor id %s with declared %s; pick a name that differs in [A-Za-z0-9._-]", s.Name, id, owner)
		}
		r.Allocation.Standing = append(slices.Clone(r.Allocation.Standing), s)
		return nil
	})
}

// RemoveStanding dismisses a declared standing session; an unknown one is a
// 404 RoleError.
func RemoveStanding(store Store, project, name, session string) error {
	return UpdateRole(store, project, name, func(r *Role) error {
		i := slices.IndexFunc(r.Allocation.Standing, func(s StandingSession) bool { return s.Name == session })
		if i < 0 {
			return roleErr(http.StatusNotFound, "no standing session %q on %s/%s", session, project, name)
		}
		r.Allocation.Standing = slices.Delete(slices.Clone(r.Allocation.Standing), i, i+1)
		if len(r.Allocation.Standing) == 0 {
			r.Allocation.Standing = nil
		}
		return nil
	})
}
