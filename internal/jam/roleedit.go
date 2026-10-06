package jam

import (
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/aethons-tools/cove/internal/jam/sessionctx"
)

// roleMu serializes every read-modify-write of a Role — role put, egress,
// standing — from the JSON admin API and the UI alike, so no writer can drop
// another's change. Every Role write outside a raw store.PutRole goes through
// the functions in this file.
var roleMu sync.Mutex

// UpdateRole applies fn to the stored role and stores the result, under the
// role lock. A missing role is a 404 WriteError; an error from fn aborts the
// write and is returned as is. fn edits a copy, but its slices and maps may
// alias the stored role's, so it must replace them, never mutate in place.
func UpdateRole(store Store, project, name string, fn func(*Role) error) error {
	roleMu.Lock()
	defer roleMu.Unlock()
	role, ok := store.GetRole(project, name)
	if !ok {
		return writeErr(http.StatusNotFound, "role %s/%s does not exist", project, name)
	}
	before := role.ModelSpec
	if err := fn(&role); err != nil {
		return err
	}
	if role.ModelSpec != before {
		if err := checkRoleModelSpec(store, role.ModelSpec); err != nil {
			return err
		}
	}
	return store.PutRole(project, role)
}

// checkRoleModelSpec refuses a role binding to a model-spec that does not
// exist (400). "" (unbound → DefaultModelSpec) is always accepted.
func checkRoleModelSpec(store Store, name string) error {
	if name == "" {
		return nil
	}
	if _, ok := store.GetModelSpec(name); !ok {
		return writeErr(http.StatusBadRequest, "model-spec %q does not exist", name)
	}
	return nil
}

// CreateRole stores a new role; an existing one is a 409 WriteError.
func CreateRole(store Store, project string, role Role) error {
	roleMu.Lock()
	defer roleMu.Unlock()
	if _, ok := store.GetRole(project, role.Name); ok {
		return writeErr(http.StatusConflict, "role %s/%s already exists", orDefaultProject(project), role.Name)
	}
	if err := checkRoleModelSpec(store, role.ModelSpec); err != nil {
		return err
	}
	return store.PutRole(project, role)
}

// PutRoleKeeping creates or replaces a role, keeping an existing role's
// standing declarations, egress policy and context: those have their own
// writers (AddStanding/RemoveStanding, SetRoleEgress/ClearRoleEgress,
// SetRoleContext/ClearRoleContext).
func PutRoleKeeping(store Store, project string, role Role) error {
	roleMu.Lock()
	defer roleMu.Unlock()
	if err := checkRoleModelSpec(store, role.ModelSpec); err != nil {
		return err
	}
	if existing, ok := store.GetRole(project, role.Name); ok {
		role.Allocation.Standing = existing.Allocation.Standing
		role.Scope.Egress = existing.Scope.Egress
		role.Context = existing.Context
	}
	return store.PutRole(project, role)
}

// SetRoleEgress replaces the role's egress policy with the normalized domains
// and returns how many it kept. A bad domain is a 400 WriteError.
func SetRoleEgress(store Store, project, name string, domains []string) (int, error) {
	n := 0
	err := UpdateRole(store, project, name, func(r *Role) error {
		norm, err := NormalizeEgress(domains)
		if err != nil {
			return &WriteError{Status: http.StatusBadRequest, Msg: err.Error()}
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
// prompt, a name with "/" (it names a route segment) or a duplicate name is a
// 400 WriteError. Its first raise starts a new session (standing-session map).
func AddStanding(store Store, project, name string, s StandingSession) error {
	if s.Name == "" || s.Prompt == "" {
		return writeErr(http.StatusBadRequest, "name and prompt are required")
	}
	if strings.Contains(s.Name, "/") {
		return writeErr(http.StatusBadRequest, "standing session name %q must not contain /", s.Name)
	}
	return UpdateRole(store, project, name, func(r *Role) error {
		if slices.ContainsFunc(r.Allocation.Standing, func(x StandingSession) bool { return x.Name == s.Name }) {
			return writeErr(http.StatusBadRequest, "standing session %q is already declared on %s/%s", s.Name, project, name)
		}
		r.Allocation.Standing = append(slices.Clone(r.Allocation.Standing), s)
		return nil
	})
}

// RemoveStanding dismisses a declared standing session; an unknown one is a
// 404 WriteError.
func RemoveStanding(store Store, project, name, session string) error {
	return UpdateRole(store, project, name, func(r *Role) error {
		i := slices.IndexFunc(r.Allocation.Standing, func(s StandingSession) bool { return s.Name == session })
		if i < 0 {
			return writeErr(http.StatusNotFound, "no standing session %q on %s/%s", session, project, name)
		}
		r.Allocation.Standing = slices.Delete(slices.Clone(r.Allocation.Standing), i, i+1)
		if len(r.Allocation.Standing) == 0 {
			r.Allocation.Standing = nil
		}
		return nil
	})
}

// SetRoleContext replaces the role's authored context layer. An invalid layer
// is a 400 WriteError; a missing role 404.
func SetRoleContext(store Store, project, name string, l sessionctx.Layer) error {
	if err := sessionctx.ValidateLayer(l, sessionctx.BudgetRole); err != nil {
		return writeErr(http.StatusBadRequest, "role context: %s", err.Error())
	}
	return UpdateRole(store, project, name, func(r *Role) error {
		r.Context = sessionctx.Layer{Core: l.Core, Leaves: slices.Clone(l.Leaves)}
		return nil
	})
}

// ClearRoleContext removes the role's authored context layer.
func ClearRoleContext(store Store, project, name string) error {
	return UpdateRole(store, project, name, func(r *Role) error {
		r.Context = sessionctx.Layer{}
		return nil
	})
}
